package restorepremigrate_test

import (
	"bytes"
	"context"
	"errors"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/command/restorepremigrate"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestRestorePreMigrateRequiresConfirmAndRejectsLiveManager(t *testing.T) {
	dataDir := t.TempDir()
	dbPath := filepath.Join(dataDir, "usage.sqlite")
	dataKeyPath := filepath.Join(dataDir, "data.key")
	snapshotDir := filepath.Join(dataDir, "backups", "pre-migrate-gen1-to-gen2")

	if err := os.WriteFile(dataKeyPath, []byte("original-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO settings (key, value, updated_at_ms) VALUES ('state', 'before', 1)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := sqlite.CreateOnlineSnapshot(context.Background(), db, dbPath, dataKeyPath, snapshotDir); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE settings SET value = 'after', updated_at_ms = 2 WHERE key = 'state'`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataKeyPath, []byte("mutated-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. Missing --confirm must fail closed without altering live files.
	var stdout, stderr bytes.Buffer
	err = restorepremigrate.Run(context.Background(), []string{
		"--snapshot-dir", snapshotDir,
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "--confirm") || !strings.Contains(err.Error(), "discards business writes") {
		t.Fatalf("expected --confirm refusal explaining write loss, got %v", err)
	}
	if got, _ := os.ReadFile(dataKeyPath); string(got) != "mutated-key" {
		t.Fatalf("data.key modified without --confirm: %q", string(got))
	}

	// 2. Active process lock must fail closed immediately with processlock.ErrLocked.
	heldLock, err := processlock.Acquire(dbPath)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	err = restorepremigrate.Run(context.Background(), []string{
		"--snapshot-dir", snapshotDir,
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--confirm",
	}, &stdout, &stderr)
	if !errors.Is(err, processlock.ErrLocked) || !strings.Contains(err.Error(), "stop Manager Server") {
		_ = heldLock.Close()
		t.Fatalf("expected processlock.ErrLocked, got %v", err)
	}
	if err := heldLock.Close(); err != nil {
		t.Fatal(err)
	}

	// 3. Once stopped and confirmed, restoration succeeds atomically.
	stdout.Reset()
	stderr.Reset()
	if err := restorepremigrate.Run(context.Background(), []string{
		"--snapshot-dir", snapshotDir,
		"--db-path", dbPath,
		"--data-key-path", dataKeyPath,
		"--timeout", "30s",
		"--confirm",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("restore failed: %v (stderr=%s)", err, stderr.String())
	}
	if got, _ := os.ReadFile(dataKeyPath); string(got) != "original-key" {
		t.Fatalf("restored data.key = %q, want original-key", string(got))
	}
	reopened, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var stateVal string
	if err := reopened.QueryRow(`SELECT value FROM settings WHERE key = 'state'`).Scan(&stateVal); err != nil {
		t.Fatal(err)
	}
	if stateVal != "before" {
		t.Fatalf("restored state = %q, want before", stateVal)
	}
}

func TestRestorePreMigrateLatestSelectionAndFlagValidation(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("USAGE_DATA_DIR", dataDir)
	dbPath := filepath.Join(dataDir, "usage.sqlite")
	dataKeyPath := filepath.Join(dataDir, "data.key")
	backupsDir := filepath.Join(dataDir, "backups")

	if err := os.WriteFile(dataKeyPath, []byte("older-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO settings (key, value, updated_at_ms) VALUES ('pick', 'older-snap', 1)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	olderDir := filepath.Join(backupsDir, "pre-migrate-gen1-to-gen2")
	if err := sqlite.CreateOnlineSnapshot(context.Background(), db, dbPath, dataKeyPath, olderDir); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	olderTime := time.Unix(1700000100, 0)
	_ = os.Chtimes(filepath.Join(olderDir, "manifest.json"), olderTime, olderTime)

	if err := os.WriteFile(dataKeyPath, []byte("newer-key"), 0o600); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE settings SET value = 'newest-snap', updated_at_ms = 2 WHERE key = 'pick'`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	newerDir := filepath.Join(backupsDir, "pre-slot-v2.0.0-beta.1-to-v2.0.0-beta.2-1700000200")
	if err := sqlite.CreateOnlineSnapshot(context.Background(), db, dbPath, dataKeyPath, newerDir); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	newerTime := time.Unix(1700000200, 0)
	_ = os.Chtimes(filepath.Join(newerDir, "manifest.json"), newerTime, newerTime)

	// Mutate live state after newest snapshot.
	if _, err := db.Exec(`UPDATE settings SET value = 'post-upgrade-live', updated_at_ms = 3 WHERE key = 'pick'`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataKeyPath, []byte("post-upgrade-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if err := restorepremigrate.Run(context.Background(), []string{"--help"}, &stdout, &stderr); !restorepremigrate.IsHelp(err) {
		t.Fatalf("expected IsHelp for --help, got %v", err)
	}
	if err := restorepremigrate.Run(context.Background(), []string{"--confirm"}, &stdout, &stderr); err == nil {
		t.Fatal("expected error when neither --snapshot-dir nor --latest is provided")
	}
	if err := restorepremigrate.Run(context.Background(), []string{"--snapshot-dir", olderDir, "--latest", "--confirm"}, &stdout, &stderr); err == nil {
		t.Fatal("expected error when both --snapshot-dir and --latest are provided")
	}
	if err := restorepremigrate.Run(context.Background(), []string{"--latest", "--timeout", "0s", "--confirm"}, &stdout, &stderr); err == nil {
		t.Fatal("expected error for non-positive --timeout")
	}

	if err := restorepremigrate.Run(context.Background(), []string{"--latest", "--confirm"}, &stdout, &stderr); err != nil {
		t.Fatalf("restore-pre-migrate --latest failed: %v (stderr=%s)", err, stderr.String())
	}
	if got, _ := os.ReadFile(dataKeyPath); string(got) != "newer-key" {
		t.Fatalf("restored data.key = %q, want newer-key", string(got))
	}
	reopened, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var picked string
	if err := reopened.QueryRow(`SELECT value FROM settings WHERE key = 'pick'`).Scan(&picked); err != nil {
		t.Fatal(err)
	}
	if picked != "newest-snap" {
		t.Fatalf("restored value = %q, want newest-snap", picked)
	}
}

func TestRestorePreMigrateCustomConfigJSONPathsAndDataDirContainment(t *testing.T) {
	rootDir := t.TempDir()
	dataDir := filepath.Join(rootDir, "data")
	customDBDir := filepath.Join(dataDir, "nested-db")
	customKeyDir := filepath.Join(dataDir, "nested-secrets")
	if err := os.MkdirAll(customDBDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(customKeyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	customDBPath := filepath.Join(customDBDir, "custom-manager.sqlite")
	customKeyPath := filepath.Join(customKeyDir, "custom-data.key")
	origKey := []byte("custom-config-data-key-material!")
	if err := os.WriteFile(customKeyPath, origKey, 0o600); err != nil {
		t.Fatal(err)
	}

	cfgPath := filepath.Join(rootDir, "config.json")
	cfgRaw, err := json.Marshal(map[string]any{
		"dataDir":     dataDir,
		"dbPath":      customDBPath,
		"dataKeyPath": customKeyPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, cfgRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPA_MANAGER_CONFIG", cfgPath)
	t.Setenv("USAGE_DATA_DIR", "")
	t.Setenv("USAGE_DB_PATH", "")
	t.Setenv("CPA_MANAGER_DATA_KEY_PATH", "")

	db, err := sqlite.Open(customDBPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO settings(key, value, updated_at_ms) VALUES ('custom_cfg', 'before-slot-upgrade', 1)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}

	snapDir := filepath.Join(customDBDir, "backups", "pre-slot-v2.0.0-beta.1-to-v2.0.0-beta.2-100")
	if err := sqlite.CreateOnlineSnapshot(context.Background(), db, customDBPath, customKeyPath, snapDir); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE settings SET value = 'mutated-after-upgrade' WHERE key = 'custom_cfg'`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(customKeyPath, []byte("mutated-key"), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. Restore via --snapshot-dir + --data-dir without passing --db-path or --data-key-path;
	// target_db_path and target_data_key_path from manifest.json must be preferred.
	var stdout, stderr bytes.Buffer
	if err := restorepremigrate.Run(context.Background(), []string{
		"--snapshot-dir", snapDir,
		"--data-dir", dataDir,
		"--confirm",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("restore custom paths via --snapshot-dir failed: %v (stderr=%s)", err, stderr.String())
	}
	gotKey, err := os.ReadFile(customKeyPath)
	if err != nil || !bytes.Equal(gotKey, origKey) {
		t.Fatalf("restored custom key = %q (err=%v), want %q", gotKey, err, origKey)
	}
	checkRestoredDB := func() {
		t.Helper()
		reopened, openErr := sql.Open("sqlite", customDBPath)
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer reopened.Close()
		var val string
		if scanErr := reopened.QueryRow(`SELECT value FROM settings WHERE key = 'custom_cfg'`).Scan(&val); scanErr != nil {
			t.Fatal(scanErr)
		}
		if val != "before-slot-upgrade" {
			t.Fatalf("restored DB value = %q, want before-slot-upgrade", val)
		}
	}
	checkRestoredDB()

	// 2. Mutate again and restore via --latest + --data-dir without explicit DB/key flags.
	if err := os.WriteFile(customKeyPath, []byte("mutated-key-2"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if err := restorepremigrate.Run(context.Background(), []string{
		"--latest",
		"--data-dir", dataDir,
		"--confirm",
	}, &stdout, &stderr); err != nil {
		t.Fatalf("restore custom paths via --latest failed: %v (stderr=%s)", err, stderr.String())
	}
	gotKey, err = os.ReadFile(customKeyPath)
	if err != nil || !bytes.Equal(gotKey, origKey) {
		t.Fatalf("restored custom key (--latest) = %q (err=%v), want %q", gotKey, err, origKey)
	}

	// 3. Reject manifest target path escaping --data-dir.
	outsideSnap := filepath.Join(rootDir, "outside-snap")
	if err := os.MkdirAll(outsideSnap, 0o700); err != nil {
		t.Fatal(err)
	}
	rawManifest, err := os.ReadFile(filepath.Join(snapDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tampered map[string]any
	if err := json.Unmarshal(rawManifest, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered["target_db_path"] = filepath.Join(rootDir, "outside.sqlite")
	tamperedRaw, _ := json.Marshal(tampered)
	if err := os.WriteFile(filepath.Join(outsideSnap, "manifest.json"), tamperedRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	err = restorepremigrate.Run(context.Background(), []string{
		"--snapshot-dir", outsideSnap,
		"--data-dir", dataDir,
		"--confirm",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "escapes --data-dir") {
		t.Fatalf("expected --data-dir escape rejection, got %v", err)
	}

	// 4. Reject symlink escape inside --data-dir.
	outsideDir := filepath.Join(rootDir, "outside-dir")
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkSubdir := filepath.Join(dataDir, "symlink-sub")
	if err := os.Symlink(outsideDir, symlinkSubdir); err != nil {
		t.Fatal(err)
	}
	tampered["target_db_path"] = customDBPath
	tampered["target_data_key_path"] = filepath.Join(symlinkSubdir, "escaped.key")
	tamperedRaw, _ = json.Marshal(tampered)
	if err := os.WriteFile(filepath.Join(outsideSnap, "manifest.json"), tamperedRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	err = restorepremigrate.Run(context.Background(), []string{
		"--snapshot-dir", outsideSnap,
		"--data-dir", dataDir,
		"--confirm",
	}, &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("expected symlink escape rejection inside --data-dir, got %v", err)
	}
}
