package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/restorepremigrate"
)

func TestEnsurePreMigrateFallbackSnapshotGen0AndGen1Restorable(t *testing.T) {
	t.Run("gen0 to gen1 via helper", func(t *testing.T) {
		dataDir := t.TempDir()
		dbPath := filepath.Join(dataDir, "usage.sqlite")
		dataKeyPath := filepath.Join(dataDir, "data.key")
		if err := os.WriteFile(dataKeyPath, []byte("gen0-data-key-material-012345678"), 0o600); err != nil {
			t.Fatal(err)
		}

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if _, err := db.Exec(`CREATE TABLE legacy_notes (id INTEGER PRIMARY KEY, note TEXT NOT NULL)`); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO legacy_notes (id, note) VALUES (1, 'preserved-gen0-row')`); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}

		if err := ensurePreMigrateFallbackSnapshot(context.Background(), db, 0, 1); err != nil {
			_ = db.Close()
			t.Fatalf("ensurePreMigrateFallbackSnapshot(0->1): %v", err)
		}

		snapshotDir := filepath.Join(dataDir, "backups", "pre-migrate-gen0-to-gen1")
		if !isValidSnapshotDir(snapshotDir) {
			_ = db.Close()
			t.Fatalf("expected valid snapshot at %s", snapshotDir)
		}

		// Simulate destructive schema migration and post-upgrade writes.
		if _, err := db.Exec(`UPDATE legacy_notes SET note = 'mutated-in-gen1' WHERE id = 1`); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dataKeyPath, []byte("mutated-gen1-data-key-material!!"), 0o600); err != nil {
			t.Fatal(err)
		}

		var stdout, stderr bytes.Buffer
		if err := restorepremigrate.Run(context.Background(), []string{
			"--snapshot-dir", snapshotDir,
			"--db-path", dbPath,
			"--data-key-path", dataKeyPath,
			"--confirm",
		}, &stdout, &stderr); err != nil {
			t.Fatalf("restore-pre-migrate failed: %v (stderr=%s)", err, stderr.String())
		}

		keyBytes, err := os.ReadFile(dataKeyPath)
		if err != nil || string(keyBytes) != "gen0-data-key-material-012345678" {
			t.Fatalf("restored data.key = %q err=%v", string(keyBytes), err)
		}

		restoredDB, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer restoredDB.Close()

		var userVersion int
		if err := restoredDB.QueryRow(`PRAGMA user_version`).Scan(&userVersion); err != nil {
			t.Fatal(err)
		}
		if userVersion != 0 {
			t.Fatalf("restored user_version = %d, want 0", userVersion)
		}
		var note string
		if err := restoredDB.QueryRow(`SELECT note FROM legacy_notes WHERE id = 1`).Scan(&note); err != nil {
			t.Fatal(err)
		}
		if note != "preserved-gen0-row" {
			t.Fatalf("restored note = %q, want preserved-gen0-row", note)
		}
	})

	t.Run("gen1 to gen2 automatic Migrate integration and restore-pre-migrate --latest", func(t *testing.T) {
		dataDir := t.TempDir()
		dbPath := filepath.Join(dataDir, "usage.sqlite")
		dataKeyPath := filepath.Join(dataDir, "data.key")
		if err := os.WriteFile(dataKeyPath, []byte("gen1-data-key-material-abcdef123"), 0o600); err != nil {
			t.Fatal(err)
		}

		gen1DB, err := Open(dbPath)
		if err != nil {
			t.Fatalf("open gen1 db: %v", err)
		}
		if _, err := gen1DB.Exec(`INSERT INTO settings (key, value, updated_at_ms) VALUES ('gen_state', 'gen1-original', 100)`); err != nil {
			_ = gen1DB.Close()
			t.Fatal(err)
		}
		if err := gen1DB.Close(); err != nil {
			t.Fatal(err)
		}

		// First boot of a brand-new Gen-1 DB (oldGen == 0) must not emit a
		// pre-migrate snapshot.
		if _, err := os.Stat(filepath.Join(dataDir, "backups", "pre-migrate-gen0-to-gen1")); !os.IsNotExist(err) {
			t.Fatalf("unexpected pre-migrate-gen0-to-gen1 on initial db creation: %v", err)
		}

		// Simulate upgrading to CurrentSchemaGeneration = 2 via Migrate.
		prevOverride := testOverrideCurrentSchemaGeneration
		testOverrideCurrentSchemaGeneration = 2
		t.Cleanup(func() {
			testOverrideCurrentSchemaGeneration = prevOverride
		})

		gen2DB, err := Open(dbPath)
		if err != nil {
			t.Fatalf("open gen2 db: %v", err)
		}
		var genAfter int
		if err := gen2DB.QueryRow(`SELECT schema_generation FROM upgrade_schema_meta WHERE id = 1`).Scan(&genAfter); err != nil {
			_ = gen2DB.Close()
			t.Fatal(err)
		}
		if genAfter != 2 {
			_ = gen2DB.Close()
			t.Fatalf("expected migrated schema_generation=2, got %d", genAfter)
		}
		if _, err := gen2DB.Exec(`UPDATE settings SET value = 'gen2-mutated', updated_at_ms = 200 WHERE key = 'gen_state'`); err != nil {
			_ = gen2DB.Close()
			t.Fatal(err)
		}
		if err := gen2DB.Close(); err != nil {
			t.Fatal(err)
		}

		snapshotDir := filepath.Join(dataDir, "backups", "pre-migrate-gen1-to-gen2")
		if !isValidSnapshotDir(snapshotDir) {
			t.Fatalf("expected Migrate to create valid %s before advancing to gen2", snapshotDir)
		}

		// Reset simulated version back to Gen-1 (downgrade binary) and verify
		// Gen-1 refuses to open the Gen-2 DB until restore-pre-migrate runs.
		testOverrideCurrentSchemaGeneration = 0
		if _, err := Open(dbPath); err == nil || !strings.Contains(err.Error(), "database requires a newer CPA Manager Plus") {
			t.Fatalf("expected gen1 binary to refuse gen2 database before restore, got %v", err)
		}

		t.Setenv("USAGE_DATA_DIR", dataDir)
		var stdout, stderr bytes.Buffer
		if err := restorepremigrate.Run(context.Background(), []string{
			"--latest",
			"--confirm",
		}, &stdout, &stderr); err != nil {
			t.Fatalf("restore-pre-migrate --latest failed: %v (stderr=%s)", err, stderr.String())
		}

		downgradedDB, err := Open(dbPath)
		if err != nil {
			t.Fatalf("gen1 Open after restore-pre-migrate failed: %v", err)
		}
		defer downgradedDB.Close()

		var restoredGen int
		if err := downgradedDB.QueryRow(`SELECT schema_generation FROM upgrade_schema_meta WHERE id = 1`).Scan(&restoredGen); err != nil {
			t.Fatal(err)
		}
		if restoredGen != 1 {
			t.Fatalf("restored schema_generation = %d, want 1", restoredGen)
		}
		var stateVal string
		if err := downgradedDB.QueryRow(`SELECT value FROM settings WHERE key = 'gen_state'`).Scan(&stateVal); err != nil {
			t.Fatal(err)
		}
		if stateVal != "gen1-original" {
			t.Fatalf("restored setting value = %q, want gen1-original", stateVal)
		}
	})
}

func TestEnsurePreMigrateFallbackSnapshotSkipsCoveredAndDisabledCases(t *testing.T) {
	t.Run("skips in-memory database", func(t *testing.T) {
		memDB, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer memDB.Close()
		if err := ensurePreMigrateFallbackSnapshot(context.Background(), memDB, 1, 2); err != nil {
			t.Fatalf("in-memory skip failed: %v", err)
		}
	})

	t.Run("skips when CPAMP_SKIP_AUTO_PRE_MIGRATE_SNAPSHOT=1", func(t *testing.T) {
		t.Setenv("CPAMP_SKIP_AUTO_PRE_MIGRATE_SNAPSHOT", "1")
		dataDir := t.TempDir()
		dbPath := filepath.Join(dataDir, "usage.sqlite")
		db, err := Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if err := ensurePreMigrateFallbackSnapshot(context.Background(), db, 1, 2); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dataDir, "backups")); !os.IsNotExist(err) {
			t.Fatalf("expected no backups dir when skip env=1, err=%v", err)
		}
	})

	t.Run("materializes durable pre-migrate-gen1-to-gen2 from verified pre-slot snapshot under trial hold and survives PrunePreSlotSnapshots", func(t *testing.T) {
		dataDir := t.TempDir()
		dbPath := filepath.Join(dataDir, "usage.sqlite")
		dataKeyPath := filepath.Join(dataDir, "data.key")
		origKey := []byte("slot-trial-key-material-01234567")
		if err := os.WriteFile(dataKeyPath, origKey, 0o600); err != nil {
			t.Fatal(err)
		}
		db, err := Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO settings(key, value, updated_at_ms) VALUES ('durable_marker', 'gen1-before-slot-upgrade', 1)`); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}

		backupsDir := filepath.Join(dataDir, "backups")
		preSlotDir := filepath.Join(backupsDir, "pre-slot-v2.0.0-beta.1-to-v2.0.0-beta.2-100")
		if err := CreateOnlineSnapshot(context.Background(), db, dbPath, dataKeyPath, preSlotDir); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		t0 := time.Unix(1700000000, 0)
		_ = os.Chtimes(filepath.Join(preSlotDir, "manifest.json"), t0, t0)

		t.Setenv("CPAMP_TRIAL_HOLD", "1")
		t.Setenv("CPAMP_RUNNING_SLOT", "v2.0.0-beta.2")
		if err := ensurePreMigrateFallbackSnapshot(context.Background(), db, 1, 2); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}

		preMigrateDir := filepath.Join(backupsDir, "pre-migrate-gen1-to-gen2")
		manifest, _, err := readValidatedSnapshotManifest(preMigrateDir)
		if err != nil {
			_ = db.Close()
			t.Fatalf("expected durable pre-migrate-gen1-to-gen2 manifest, got err=%v", err)
		}
		dbStat, dbStatErr := os.Stat(dbPath)
		manifestDBStat, manifestDBErr := os.Stat(manifest.TargetDBPath)
		keyStat, keyStatErr := os.Stat(dataKeyPath)
		manifestKeyStat, manifestKeyErr := os.Stat(manifest.TargetDataKeyPath)
		if dbStatErr != nil || manifestDBErr != nil || !os.SameFile(dbStat, manifestDBStat) ||
			keyStatErr != nil || manifestKeyErr != nil || !os.SameFile(keyStat, manifestKeyStat) {
			_ = db.Close()
			t.Fatalf("manifest targets = (%q, %q), want same files as (%q, %q)", manifest.TargetDBPath, manifest.TargetDataKeyPath, dbPath, dataKeyPath)
		}

		if _, err := os.Stat(filepath.Join(preMigrateDir, "database")); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		// Simulate subsequent slot upgrades pruning old pre-slot-* snapshots (keep=2).
		for i, name := range []string{
			"pre-slot-v2.0.0-beta.2-to-v2.0.0-beta.3-200",
			"pre-slot-v2.0.0-beta.3-to-v2.0.0-beta.4-300",
			"pre-slot-v2.0.0-beta.4-to-v2.0.0-beta.5-400",
		} {
			nextSnap := filepath.Join(backupsDir, name)
			if err := CreateOnlineSnapshot(context.Background(), db, dbPath, dataKeyPath, nextSnap); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			ts := time.Unix(1700000100+int64(i*60), 0)
			_ = os.Chtimes(filepath.Join(nextSnap, "manifest.json"), ts, ts)
		}
		if err := PrunePreSlotSnapshots(backupsDir, 2); err != nil {
			_ = db.Close()
			t.Fatalf("PrunePreSlotSnapshots: %v", err)
		}
		if _, err := os.Stat(preSlotDir); !os.IsNotExist(err) {
			_ = db.Close()
			t.Fatalf("expected old pre-slot snapshot %s to be pruned, err=%v", preSlotDir, err)
		}
		if _, err := os.Stat(preMigrateDir); err != nil {
			_ = db.Close()
			t.Fatalf("expected durable %s to survive PrunePreSlotSnapshots: %v", preMigrateDir, err)
		}

		// Mutate live DB + data.key to Gen-2 state, close, and restore pre-migrate-gen1-to-gen2.
		if _, err := db.Exec(`UPDATE settings SET value = 'gen2-mutated' WHERE key = 'durable_marker'`); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dataKeyPath, []byte("mutated-gen2-key"), 0o600); err != nil {
			t.Fatal(err)
		}
		var stdout, stderr bytes.Buffer
		if err := restorepremigrate.Run(context.Background(), []string{
			"--snapshot-dir", preMigrateDir,
			"--data-dir", dataDir,
			"--confirm",
		}, &stdout, &stderr); err != nil {
			t.Fatalf("restore durable pre-migrate-gen1-to-gen2 failed: %v (stderr=%s)", err, stderr.String())
		}
		restoredKey, err := os.ReadFile(dataKeyPath)
		if err != nil || !bytes.Equal(restoredKey, origKey) {
			t.Fatalf("restored data.key = %q (err=%v), want %q", restoredKey, err, origKey)
		}
		reopened, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		var gotMarker string
		if err := reopened.QueryRow(`SELECT value FROM settings WHERE key = 'durable_marker'`).Scan(&gotMarker); err != nil {
			t.Fatal(err)
		}
		if gotMarker != "gen1-before-slot-upgrade" {
			t.Fatalf("restored marker = %q, want gen1-before-slot-upgrade", gotMarker)
		}
	})

	t.Run("copies live checkpointed database when pre-slot digest mismatches under trial hold", func(t *testing.T) {
		dataDir := t.TempDir()
		dbPath := filepath.Join(dataDir, "usage.sqlite")
		dataKeyPath := filepath.Join(dataDir, "data.key")
		if err := os.WriteFile(dataKeyPath, []byte("slot-trial-key"), 0o600); err != nil {
			t.Fatal(err)
		}
		db, err := Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()

		preSlotDir := filepath.Join(dataDir, "backups", "pre-slot-v2.0.0-beta.1-to-v2.0.0-beta.2-1700000000")
		if err := CreateOnlineSnapshot(context.Background(), db, dbPath, dataKeyPath, preSlotDir); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO settings(key, value, updated_at_ms) VALUES ('post_preslot', 'live-value', 2)`); err != nil {
			t.Fatal(err)
		}

		t.Setenv("CPAMP_TRIAL_HOLD", "1")
		t.Setenv("CPAMP_RUNNING_SLOT", "v2.0.0-beta.2")
		if err := ensurePreMigrateFallbackSnapshot(context.Background(), db, 1, 2); err != nil {
			t.Fatal(err)
		}
		preMigrateDB := filepath.Join(dataDir, "backups", "pre-migrate-gen1-to-gen2", "database")
		preSlotInfo, err := os.Stat(filepath.Join(preSlotDir, "database"))
		if err != nil {
			t.Fatal(err)
		}
		preMigrateInfo, err := os.Stat(preMigrateDB)
		if err != nil {
			t.Fatal(err)
		}
		if os.SameFile(preSlotInfo, preMigrateInfo) {
			t.Fatal("must not hardlink stale pre-slot database when live checkpointed digest differs")
		}
	})

	t.Run("hardlinks verified pre-slot files when database and data-key SHA-256 digests match live checkpointed state", func(t *testing.T) {
		dataDir := t.TempDir()
		dbPath := filepath.Join(dataDir, "usage.sqlite")
		dataKeyPath := filepath.Join(dataDir, "data.key")
		origKey := []byte("hardlink-key-material-0123456789")
		if err := os.WriteFile(dataKeyPath, origKey, 0o600); err != nil {
			t.Fatal(err)
		}
		db, err := Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO settings(key, value, updated_at_ms) VALUES ('hl_marker', 'gen1-exact', 1)`); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		backupsDir := filepath.Join(dataDir, "backups")
		preSlotDir := filepath.Join(backupsDir, "pre-slot-v2.0.0-beta.1-to-v2.0.0-beta.2-100")
		if err := CreateOnlineSnapshot(context.Background(), db, dbPath, dataKeyPath, preSlotDir); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		// Align live DB bytes with the verified VACUUM INTO snapshot (e.g. post-restore or vacuumed DB).
		var stdout, stderr bytes.Buffer
		if err := restorepremigrate.Run(context.Background(), []string{
			"--snapshot-dir", preSlotDir,
			"--data-dir", dataDir,
			"--confirm",
		}, &stdout, &stderr); err != nil {
			t.Fatalf("restore preSlotDir: %v (stderr=%s)", err, stderr.String())
		}

		reopened, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()

		t.Setenv("CPAMP_TRIAL_HOLD", "1")
		t.Setenv("CPAMP_RUNNING_SLOT", "v2.0.0-beta.2")
		if err := ensurePreMigrateFallbackSnapshot(context.Background(), reopened, 1, 2); err != nil {
			t.Fatal(err)
		}
		preMigrateDir := filepath.Join(backupsDir, "pre-migrate-gen1-to-gen2")
		preSlotDBInfo, err := os.Stat(filepath.Join(preSlotDir, "database"))
		if err != nil {
			t.Fatal(err)
		}
		preMigrateDBInfo, err := os.Stat(filepath.Join(preMigrateDir, "database"))
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(preSlotDBInfo, preMigrateDBInfo) {
			t.Fatal("expected pre-migrate database to hardlink verified pre-slot database when SHA-256 matches")
		}
		preSlotKeyInfo, err := os.Stat(filepath.Join(preSlotDir, "data-key"))
		if err != nil {
			t.Fatal(err)
		}
		preMigrateKeyInfo, err := os.Stat(filepath.Join(preMigrateDir, "data-key"))
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(preSlotKeyInfo, preMigrateKeyInfo) {
			t.Fatal("expected pre-migrate data-key to hardlink verified pre-slot data-key when SHA-256 matches")
		}
	})

	t.Run("partial fallback copy and hash helper", func(t *testing.T) {
		dir := t.TempDir()
		srcPath := filepath.Join(dir, "src.bin")
		dstPath := filepath.Join(dir, "dst.bin")
		content := bytes.Repeat([]byte("cpamp-snapshot-block-"), 1024)
		if err := os.WriteFile(srcPath, content, 0o600); err != nil {
			t.Fatal(err)
		}
		src, err := os.Open(srcPath)
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		dst, err := os.OpenFile(dstPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer dst.Close()

		const prefixLen = 512
		if _, err := io.CopyN(dst, src, prefixLen); err != nil {
			t.Fatal(err)
		}
		digest, total, err := finishFallbackAndHash(context.Background(), dst, src, prefixLen)
		if err != nil {
			t.Fatalf("finishFallbackAndHash: %v", err)
		}
		wantDigest, wantSize, err := hashRegularFilePath(context.Background(), srcPath)
		if err != nil {
			t.Fatal(err)
		}
		if total != wantSize || digest != wantDigest {
			t.Fatalf("got (%s, %d), want (%s, %d)", digest, total, wantDigest, wantSize)
		}
	})
}
