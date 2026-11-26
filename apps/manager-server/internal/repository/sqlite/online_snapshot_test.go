package sqlite_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/managerdatasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestCreateOnlineSnapshotLiveWALAndRestore(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "usage.sqlite")
	dataKeyPath := filepath.Join(dataDir, "data.key")
	snapshotDir := filepath.Join(dataDir, "backups", "pre-slot-v2.0.0-beta.1-to-v2.0.0-beta.2-1700000000")

	originalKey := []byte("0123456789abcdef0123456789abcdef")
	if err := os.WriteFile(dataKeyPath, originalKey, 0o600); err != nil {
		t.Fatalf("write data.key: %v", err)
	}

	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	dbClosed := false
	t.Cleanup(func() {
		if !dbClosed {
			_ = db.Close()
		}
	})

	// Disable automatic WAL checkpointing so frames remain in the live -wal file
	// while CreateOnlineSnapshot runs VACUUM INTO.
	if _, err := db.Exec(`PRAGMA wal_autocheckpoint = 0`); err != nil {
		t.Fatalf("disable wal_autocheckpoint: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO settings (key, value, updated_at_ms) VALUES ('snapshot_marker', 'before-slot-upgrade', 1001)`); err != nil {
		t.Fatalf("insert pre-snapshot setting: %v", err)
	}
	walInfo, err := os.Stat(dbPath + "-wal")
	if err != nil || walInfo.Size() == 0 {
		t.Fatalf("expected active non-empty WAL before snapshot, info=%v err=%v", walInfo, err)
	}

	if err := sqlite.CreateOnlineSnapshot(context.Background(), db, dbPath, dataKeyPath, snapshotDir); err != nil {
		t.Fatalf("CreateOnlineSnapshot: %v", err)
	}

	snapDirInfo, err := os.Lstat(snapshotDir)
	if err != nil || !snapDirInfo.IsDir() || (runtime.GOOS != "windows" && snapDirInfo.Mode().Perm() != 0o700) {
		t.Fatalf("snapshot dir info=%v err=%v", snapDirInfo, err)
	}
	backupsInfo, err := os.Lstat(filepath.Dir(snapshotDir))
	if err != nil || !backupsInfo.IsDir() || (runtime.GOOS != "windows" && backupsInfo.Mode().Perm() != 0o700) {
		t.Fatalf("backups parent info=%v err=%v", backupsInfo, err)
	}

	manifestRaw, err := os.ReadFile(filepath.Join(snapshotDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	var parsedManifest struct {
		Version int `json:"version"`
		Files   map[string]struct {
			Existed bool   `json:"existed"`
			Mode    uint32 `json:"mode"`
			Size    int64  `json:"size"`
			SHA256  string `json:"sha256"`
		} `json:"files"`
	}
	if err := json.Unmarshal(manifestRaw, &parsedManifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if parsedManifest.Version != 1 || len(parsedManifest.Files) != 5 {
		t.Fatalf("unexpected manifest: %s", string(manifestRaw))
	}
	if !parsedManifest.Files["database"].Existed || parsedManifest.Files["database"].Mode != 0o600 || parsedManifest.Files["database"].SHA256 == "" {
		t.Fatalf("unexpected database entry: %+v", parsedManifest.Files["database"])
	}
	if !parsedManifest.Files["data-key"].Existed || parsedManifest.Files["data-key"].Size != int64(len(originalKey)) {
		t.Fatalf("unexpected data-key entry: %+v", parsedManifest.Files["data-key"])
	}
	for _, sidecar := range []string{"database-wal", "database-shm", "database-journal"} {
		if parsedManifest.Files[sidecar].Existed {
			t.Fatalf("expected %s existed=false for VACUUM INTO snapshot, got %+v", sidecar, parsedManifest.Files[sidecar])
		}
	}

	snapshotDBBytes, err := os.ReadFile(filepath.Join(snapshotDir, "database"))
	if err != nil || len(snapshotDBBytes) == 0 {
		t.Fatalf("read snapshot database: len=%d err=%v", len(snapshotDBBytes), err)
	}

	// Mutate both SQLite (leaving post-snapshot WAL/SHM sidecars) and data.key.
	if _, err := db.Exec(`UPDATE settings SET value = 'mutated-after-snapshot', updated_at_ms = 2002 WHERE key = 'snapshot_marker'`); err != nil {
		t.Fatalf("mutate setting: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO settings (key, value, updated_at_ms) VALUES ('post_snapshot_only', 'discard-me', 2003)`); err != nil {
		t.Fatalf("insert post-snapshot setting: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	dbClosed = true

	// Simulate leftover sidecars from an interrupted trial run.
	if err := os.WriteFile(dbPath+"-wal", []byte("dirty-post-snapshot-wal"), 0o600); err != nil {
		t.Fatalf("write dirty wal: %v", err)
	}
	if err := os.WriteFile(dbPath+"-shm", []byte("dirty-post-snapshot-shm"), 0o600); err != nil {
		t.Fatalf("write dirty shm: %v", err)
	}
	if err := os.WriteFile(dataKeyPath, []byte("mutated-key-after-snapshot-00000"), 0o600); err != nil {
		t.Fatalf("mutate data.key: %v", err)
	}

	var stdout, stderr bytes.Buffer
	if err := managerdatasnapshot.Run(context.Background(), []string{
		"restore",
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--snapshot-dir", snapshotDir,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("managerdatasnapshot restore failed: %v (stderr=%s)", err, stderr.String())
	}

	restoredDBBytes, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read restored db: %v", err)
	}
	if !bytes.Equal(restoredDBBytes, snapshotDBBytes) {
		t.Fatal("restored database bytes do not match snapshot database image byte-for-byte")
	}
	restoredKeyBytes, err := os.ReadFile(dataKeyPath)
	if err != nil {
		t.Fatalf("read restored data.key: %v", err)
	}
	if !bytes.Equal(restoredKeyBytes, originalKey) {
		t.Fatalf("restored data.key = %q, want %q", string(restoredKeyBytes), string(originalKey))
	}
	for _, sidecar := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(dbPath + sidecar); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be removed on restore, got err=%v", dbPath+sidecar, err)
		}
	}

	reopened, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen restored sqlite: %v", err)
	}
	defer reopened.Close()

	var markerVal string
	if err := reopened.QueryRow(`SELECT value FROM settings WHERE key = 'snapshot_marker'`).Scan(&markerVal); err != nil {
		t.Fatalf("query restored setting: %v", err)
	}
	if markerVal != "before-slot-upgrade" {
		t.Fatalf("restored setting = %q, want before-slot-upgrade", markerVal)
	}
	var postCount int
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = 'post_snapshot_only'`).Scan(&postCount); err != nil {
		t.Fatalf("count post_snapshot_only: %v", err)
	}
	if postCount != 0 {
		t.Fatalf("expected post_snapshot_only to be absent after restore, got count=%d", postCount)
	}
}

func TestCreateOnlineSnapshotMissingDataKeyAndSingleQuotePaths(t *testing.T) {
	baseDir := filepath.Join(t.TempDir(), "dir'with'quotes")
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(baseDir, "usage'db.sqlite")
	dataKeyPath := filepath.Join(baseDir, "absent-data.key")
	snapshotDir := filepath.Join(baseDir, "backups", "pre-slot-quote'test")

	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO settings (key, value, updated_at_ms) VALUES ('k', 'v1', 1)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := sqlite.CreateOnlineSnapshot(context.Background(), db, dbPath, dataKeyPath, snapshotDir); err != nil {
		_ = db.Close()
		t.Fatalf("CreateOnlineSnapshot with single quotes and absent data.key: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// Create a post-snapshot data.key; restore must remove it because existed=false.
	if err := os.WriteFile(dataKeyPath, []byte("created-later"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if err := managerdatasnapshot.Run(context.Background(), []string{
		"restore",
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--snapshot-dir", snapshotDir,
	}, &stdout, &stderr); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if _, err := os.Lstat(dataKeyPath); !os.IsNotExist(err) {
		t.Fatalf("expected absent data.key to be removed after restore, err=%v", err)
	}
}

func TestCreateOnlineSnapshotValidationFailsClosed(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}

	if err := sqlite.CreateOnlineSnapshot(context.Background(), nil, dbPath, "", filepath.Join(dir, "s1")); err == nil {
		t.Fatal("expected nil db error")
	}
	if err := sqlite.CreateOnlineSnapshot(context.Background(), db, "", "", filepath.Join(dir, "s1")); err == nil {
		t.Fatal("expected empty dbPath error")
	}
	if err := sqlite.CreateOnlineSnapshot(context.Background(), db, dbPath, "", ""); err == nil {
		t.Fatal("expected empty snapshotDir error")
	}

	existingDir := filepath.Join(dir, "existing")
	if err := os.MkdirAll(existingDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.CreateOnlineSnapshot(context.Background(), db, dbPath, "", existingDir); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected already exists error, got %v", err)
	}

	targetParent := filepath.Join(dir, "real-backups")
	if err := os.MkdirAll(targetParent, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkParent := filepath.Join(dir, "linked-backups")
	if err := os.Symlink(targetParent, symlinkParent); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.CreateOnlineSnapshot(context.Background(), db, dbPath, "", filepath.Join(symlinkParent, "snap")); err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("expected symlink parent rejection, got %v", err)
	}

	realKey := filepath.Join(dir, "real.key")
	if err := os.WriteFile(realKey, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkKey := filepath.Join(dir, "link.key")
	if err := os.Symlink(realKey, linkKey); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.CreateOnlineSnapshot(context.Background(), db, dbPath, linkKey, filepath.Join(dir, "backups", "snap-link-key")); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected symlink data.key rejection, got %v", err)
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, "backups", ".cpamp-online-snapshot-tmp-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("expected temp snapshot dirs cleaned up, got %v (err=%v)", leftovers, err)
	}
}

func TestPrunePreSlotSnapshots(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	backupsDir := filepath.Join(dir, "backups")
	snap1 := filepath.Join(backupsDir, "pre-slot-v2.0.0-beta.1-to-v2.0.0-beta.2-100")
	snap2 := filepath.Join(backupsDir, "pre-slot-v2.0.0-beta.2-to-v2.0.0-beta.3-200")
	snap3 := filepath.Join(backupsDir, "pre-slot-v2.0.0-beta.3-to-v2.0.0-beta.4-300")
	snap4 := filepath.Join(backupsDir, "pre-slot-v2.0.0-beta.4-to-v2.0.0-beta.5-400")
	preMigrate := filepath.Join(backupsDir, "pre-migrate-gen1-to-gen2")

	for i, target := range []string{snap1, snap2, snap3, snap4, preMigrate} {
		if err := sqlite.CreateOnlineSnapshot(context.Background(), db, dbPath, "", target); err != nil {
			t.Fatalf("create snapshot %s: %v", target, err)
		}
		ts := time.Unix(1700000000+int64(i*60), 0)
		_ = os.Chtimes(filepath.Join(target, "manifest.json"), ts, ts)
	}

	// Keep newest 2 (snap4, snap3) AND protect snap1; only snap2 should be pruned,
	// and pre-migrate-gen1-to-gen2 must never be touched.
	if err := sqlite.PrunePreSlotSnapshots(backupsDir, 2, snap1); err != nil {
		t.Fatalf("PrunePreSlotSnapshots: %v", err)
	}

	if _, err := os.Stat(snap2); !os.IsNotExist(err) {
		t.Fatalf("expected snap2 to be pruned, got err=%v", err)
	}
	for _, preserved := range []string{snap1, snap3, snap4, preMigrate} {
		if _, err := os.Stat(preserved); err != nil {
			t.Fatalf("expected %s to be preserved: %v", preserved, err)
		}
	}
}

func TestCreateOnlineSnapshotPopulatesManifestTargetPaths(t *testing.T) {
	dir := t.TempDir()
	dbDir := filepath.Join(dir, "db")
	keyDir := filepath.Join(dir, "keys")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(keyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	customDB := filepath.Join(dbDir, "custom.sqlite")
	customKey := filepath.Join(keyDir, "custom.key")
	if err := os.WriteFile(customKey, []byte("custom-key-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(customDB)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	snapExplicit := filepath.Join(dir, "backups", "pre-slot-explicit")
	if err := sqlite.CreateOnlineSnapshot(context.Background(), db, customDB, customKey, snapExplicit); err != nil {
		t.Fatalf("CreateOnlineSnapshot explicit key: %v", err)
	}
	var parsedExplicit struct {
		TargetDBPath      string `json:"target_db_path"`
		TargetDataKeyPath string `json:"target_data_key_path"`
	}
	rawExplicit, err := os.ReadFile(filepath.Join(snapExplicit, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawExplicit, &parsedExplicit); err != nil {
		t.Fatal(err)
	}
	if parsedExplicit.TargetDBPath != filepath.Clean(customDB) || parsedExplicit.TargetDataKeyPath != filepath.Clean(customKey) {
		t.Fatalf("explicit manifest targets = (%q, %q), want (%q, %q)", parsedExplicit.TargetDBPath, parsedExplicit.TargetDataKeyPath, customDB, customKey)
	}

	// Empty dataKeyPath resolves to <dbDir>/data.key so both fields are always populated.
	snapDefaultKey := filepath.Join(dir, "backups", "pre-slot-default-key")
	if err := sqlite.CreateOnlineSnapshot(context.Background(), db, customDB, "", snapDefaultKey); err != nil {
		t.Fatalf("CreateOnlineSnapshot empty key: %v", err)
	}
	var parsedDefault struct {
		TargetDBPath      string `json:"target_db_path"`
		TargetDataKeyPath string `json:"target_data_key_path"`
	}
	rawDefault, err := os.ReadFile(filepath.Join(snapDefaultKey, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawDefault, &parsedDefault); err != nil {
		t.Fatal(err)
	}
	wantDefaultKey := filepath.Join(dbDir, "data.key")
	if parsedDefault.TargetDBPath != filepath.Clean(customDB) || parsedDefault.TargetDataKeyPath != filepath.Clean(wantDefaultKey) {
		t.Fatalf("default manifest targets = (%q, %q), want (%q, %q)", parsedDefault.TargetDBPath, parsedDefault.TargetDataKeyPath, customDB, wantDefaultKey)
	}
}
