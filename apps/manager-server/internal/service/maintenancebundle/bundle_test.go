package maintenancebundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagearchive"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	usageService "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	usageparser "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	_ "modernc.org/sqlite"
)

func fixture(t *testing.T) (Layout, string, *processlock.Lock) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	l := Layout{
		DatabasePath:    filepath.Join(root, "usage.sqlite"),
		DataKeyPath:     filepath.Join(root, "data.key"),
		ConfigPath:      filepath.Join(root, "manager.json"),
		ArchivesPath:    filepath.Join(root, "usage-archives"),
		RequireConfig:   true,
		RequireArchives: true,
	}
	seedPath := filepath.Join(root, "seed.sqlite")
	seed, err := sql.Open("sqlite", seedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	if _, err := seed.Exec("PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE test_table (id INTEGER PRIMARY KEY, note TEXT); INSERT INTO test_table VALUES (1, 'initial');"); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(seedPath + suffix)
		if err == nil {
			put(t, l.DatabasePath+suffix, data)
		}
	}
	_ = seed.Close()
	if _, _, err := security.LoadOrCreateDataKey("", l.DataKeyPath); err != nil {
		t.Fatal(err)
	}
	put(t, l.ConfigPath, []byte(`{"dbPath":"`+filepath.ToSlash(l.DatabasePath)+`"}`))
	if err := os.MkdirAll(filepath.Join(l.ArchivesPath, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(l.ArchivesPath, "2026", "10"), 0o700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(l.ArchivesPath, "2026", "10", "history.jsonl.gz"), []byte("opaque archive bytes\x00\xff"))
	lock, err := processlock.Acquire(l.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	return l, root, lock
}

func put(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func rewriteManifest(t *testing.T, bundle string) {
	t.Helper()
	m, err := Inspect(context.Background(), BundleLayout(bundle))
	if err != nil {
		t.Fatal(err)
	}
	m.Fence = CooperatingFence
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(bundle, "manifest.json"), data)
	if _, err := Verify(context.Background(), bundle); err != nil {
		t.Fatalf("rewritten inventory must pass Verify: %v", err)
	}
}

func saveConnections(t *testing.T, db *sql.DB, protector *security.Protector, which string) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at_ms INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	repo := setting.New(db, protector)
	if which == "setup" || which == "both" {
		if err := repo.SaveSetup(context.Background(), model.Setup{CPAUpstreamURL: "http://localhost:8317", ManagementKey: "fixture-secret"}); err != nil {
			t.Fatal(err)
		}
	}
	if which == "manager_config_v1" || which == "both" {
		if err := repo.SaveManagerConfig(context.Background(), model.ManagerConfig{CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: "http://localhost:8317", ManagementKey: "fixture-secret"}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.SaveBootstrapState(context.Background(), model.BootstrapState{ConnectionStorageMigrationVersion: 2}); err != nil {
		t.Fatal(err)
	}
}

func mustCreate(t *testing.T, l Layout, root string, lock *processlock.Lock) (string, Manifest) {
	t.Helper()
	bundle := filepath.Join(root, "bundle")
	m, err := Create(context.Background(), l, bundle, lock)
	if err != nil {
		t.Fatal(err)
	}
	return bundle, m
}

func noPublished(t *testing.T, root, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("failure published destination: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), "unpublished") {
			t.Fatalf("temporary artifact retained: %s", entry.Name())
		}
	}
}

func TestCompleteBundleRoundTrip(t *testing.T) {
	l, root, lock := fixture(t)
	before, err := Inspect(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	bundle, m := mustCreate(t, l, root, lock)
	if m.Fence != CooperatingFence || !reflect.DeepEqual(before.Entries, m.Entries) {
		t.Fatal("bundle does not match full source inventory")
	}
	if m.SourceRevision == "" || m.SourceRevision != before.SourceRevision {
		t.Fatalf("source revision mismatch: %q vs %q", m.SourceRevision, before.SourceRevision)
	}
	if !m.Entries["usage-archives/empty"].Directory {
		t.Fatal("empty archive directory was lost")
	}
	if len(m.Entries) != 11 {
		t.Fatalf("unexpected inventory: %+v", m.Entries)
	}
	if _, err := Verify(context.Background(), bundle); err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreNew(context.Background(), bundle, filepath.Join(root, "restored"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Revalidate(context.Background(), restored, before); err != nil {
		t.Fatal(err)
	}
	if err := Revalidate(context.Background(), l, before); err != nil {
		t.Fatalf("source changed: %v", err)
	}
	for name, path := range restored.paths() {
		if name == "usage-archives" {
			continue
		}
		a, errA := os.ReadFile(l.paths()[name])
		b, errB := os.ReadFile(path)
		if os.IsNotExist(errA) && os.IsNotExist(errB) {
			continue
		}
		if errA != nil {
			t.Fatal(errA)
		}
		if errB != nil {
			t.Fatal(errB)
		}
		if !reflect.DeepEqual(a, b) {
			t.Fatalf("unmatched restoration: %s", name)
		}
	}
}

func TestRealSQLiteWALAndMatchingEncryptedKeyRestore(t *testing.T) {
	l, root, lock := fixture(t)
	if err := os.Remove(l.DataKeyPath); err != nil {
		t.Fatal(err)
	}
	key, _, err := security.LoadOrCreateDataKey("", l.DataKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(key)
	if err != nil {
		t.Fatal(err)
	}
	seedPath := filepath.Join(root, "seed.sqlite")
	seed, err := managersqlite.Open(seedPath)
	if err != nil {
		t.Fatal(err)
	}
	seed.SetMaxOpenConns(1)
	defer seed.Close()
	for _, statement := range []string{"PRAGMA journal_mode=WAL", "PRAGMA wal_autocheckpoint=0"} {
		if _, err := seed.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	eventHash := sha256.Sum256([]byte("bundle-production-wal-event"))
	result, err := store.New(seed).InsertEvents(context.Background(), []usageparser.Event{{
		EventHash: hex.EncodeToString(eventHash[:]), TimestampMS: 1000,
		Timestamp: time.UnixMilli(1000).UTC().Format(time.RFC3339Nano),
		Model:     "gpt-test", RawJSON: `{"immutable-history":true}`, CreatedAtMS: 1000,
	}})
	if err != nil || result.Inserted != 1 {
		t.Fatalf("insert production WAL event: %#v %v", result, err)
	}
	saveConnections(t, seed, protector, "both")
	// Clone actual committed WAL bytes to an offline input. The seed writer never
	// uses this clone, and closing it afterward cannot checkpoint the clone.
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(seedPath + suffix)
		if err != nil {
			t.Fatal(err)
		}
		if suffix == "-wal" && len(data) <= 32 {
			t.Fatal("fixture has no committed WAL frames")
		}
		put(t, l.DatabasePath+suffix, data)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(l.DatabasePath + "-journal"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	before, err := Inspect(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	bundle, _ := mustCreate(t, l, root, lock)
	restored, err := RestoreNew(context.Background(), bundle, filepath.Join(root, "restored"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Revalidate(context.Background(), restored, before); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(restored.DatabasePath)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw, integrity string
	if err := db.QueryRow("SELECT raw_json FROM usage_events WHERE id=1").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if raw != `{"immutable-history":true}` {
		t.Fatal("immutable usage row changed")
	}
	restoredKey, _, err := security.LoadOrCreateDataKey("", restored.DataKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	restoredProtector, err := security.NewProtector(restoredKey)
	if err != nil {
		t.Fatal(err)
	}
	repo := setting.New(db, restoredProtector)
	setup, found, err := repo.LoadSetup(context.Background())
	if err != nil || !found || setup.ManagementKey != "fixture-secret" {
		t.Fatalf("DB/key mismatch: %v", err)
	}
	cfg, found, err := repo.LoadManagerConfig(context.Background())
	if err != nil || !found || cfg.CPAConnection.ManagementKey != "fixture-secret" {
		t.Fatalf("manager config/key mismatch: %v", err)
	}
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("restored integrity: %q %v", integrity, err)
	}
	if err := Revalidate(context.Background(), l, before); err != nil {
		t.Fatalf("backup modified source: %v", err)
	}
}

func TestMissingEntriesAreExplicitAndRestoreAbsent(t *testing.T) {
	l, root, lock := fixture(t)
	l.RequireConfig, l.RequireArchives = false, false
	for _, path := range []string{l.DatabasePath + "-wal", l.DatabasePath + "-shm", l.DatabasePath + "-journal", l.ConfigPath, l.ArchivesPath} {
		if err := os.RemoveAll(path); err != nil {
			t.Fatal(err)
		}
	}
	bundle, m := mustCreate(t, l, root, lock)
	if len(m.Entries) != 7 {
		t.Fatalf("missing fixed inventory: %+v", m.Entries)
	}
	for _, name := range []string{"database-wal", "database-shm", "database-journal", "config", "usage-archives"} {
		if e, ok := m.Entries[name]; !ok || e.Present {
			t.Fatalf("missing entry not explicit: %s", name)
		}
	}
	restored, err := RestoreNew(context.Background(), bundle, filepath.Join(root, "restored"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Revalidate(context.Background(), restored, m); err != nil {
		t.Fatal(err)
	}
}

func TestRequiredInputsCannotBeOmitted(t *testing.T) {
	for _, name := range []string{"database", "data-key", "config", "usage-archives"} {
		t.Run(name, func(t *testing.T) {
			l, root, lock := fixture(t)
			if err := os.RemoveAll(l.paths()[name]); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(root, "bundle")
			if _, err := Create(context.Background(), l, dest, lock); err == nil {
				t.Fatal("missing required input accepted")
			}
			noPublished(t, root, dest)
		})
	}
}

func TestSourceChangesPreventPublication(t *testing.T) {
	for _, change := range []string{"content-with-same-stat", "sidecar-added", "sidecar-deleted", "archive-added", "archive-deleted", "key-changed", "config-changed", "lock-replaced"} {
		t.Run(change, func(t *testing.T) {
			l, root, lock := fixture(t)
			if change == "sidecar-added" {
				if err := os.Remove(l.DatabasePath + "-wal"); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
			hook := func() {
				switch change {
				case "content-with-same-stat":
					info, err := os.Stat(l.DatabasePath)
					if err != nil {
						t.Fatal(err)
					}
					put(t, l.DatabasePath, []byte(strings.Repeat("X", int(info.Size()))))
					if err := os.Chtimes(l.DatabasePath, info.ModTime(), info.ModTime()); err != nil {
						t.Fatal(err)
					}
				case "sidecar-added":
					put(t, l.DatabasePath+"-wal", []byte("added"))
				case "sidecar-deleted":
					if err := os.Remove(l.DatabasePath + "-shm"); err != nil {
						t.Fatal(err)
					}
				case "archive-added":
					put(t, filepath.Join(l.ArchivesPath, "new.gz"), []byte("new"))
				case "archive-deleted":
					if err := os.RemoveAll(filepath.Join(l.ArchivesPath, "2026")); err != nil {
						t.Fatal(err)
					}
				case "key-changed":
					put(t, l.DataKeyPath, []byte("new key"))
				case "config-changed":
					put(t, l.ConfigPath, []byte("new config"))
				case "lock-replaced":
					if err := os.Rename(lock.Path(), lock.Path()+".old"); err != nil {
						t.Skipf("platform cannot rename held lock: %v", err)
					}
					put(t, lock.Path(), []byte("replacement"))
				}
			}
			dest := filepath.Join(root, "bundle")
			if _, err := create(context.Background(), l, dest, lock, createHooks{beforeRevalidate: hook}); err == nil {
				t.Fatal("source change accepted")
			}
			noPublished(t, root, dest)
		})
	}
}

func TestOwnershipFenceRequiredAndBoundToDatabase(t *testing.T) {
	l, root, lock := fixture(t)
	wrong, err := processlock.Acquire(filepath.Join(root, "other.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer wrong.Close()
	for _, held := range []*processlock.Lock{nil, wrong} {
		if _, err := Create(context.Background(), l, filepath.Join(root, "bundle"), held); err == nil {
			t.Fatal("unbound lock accepted")
		}
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), l, filepath.Join(root, "bundle"), lock); err == nil {
		t.Fatal("closed lock accepted")
	}
}

func TestCancellationAndDiskFailureDoNotPublish(t *testing.T) {
	for _, action := range []string{"create", "restore"} {
		for _, failure := range []string{"canceled", "disk-full"} {
			t.Run(action+"/"+failure, func(t *testing.T) {
				l, root, lock := fixture(t)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				h := createHooks{beforeRevalidate: cancel}
				if failure == "disk-full" {
					h = createHooks{copy: func(context.Context, string, string) error { return syscall.ENOSPC }}
				}
				dest := filepath.Join(root, "result")
				var err error
				if action == "create" {
					_, err = create(ctx, l, dest, lock, h)
				} else {
					bundle, _ := mustCreate(t, l, root, lock)
					_, err = restoreNew(ctx, bundle, dest, h)
				}
				if err == nil {
					t.Fatal("failure accepted")
				}
				if failure == "canceled" && !errors.Is(err, context.Canceled) {
					t.Fatalf("wrong cancellation: %v", err)
				}
				if failure == "disk-full" && !errors.Is(err, syscall.ENOSPC) {
					t.Fatalf("wrong disk failure: %v", err)
				}
				noPublished(t, root, dest)
			})
		}
	}
}

func TestTamperedBundlesCannotBeRestored(t *testing.T) {
	for _, change := range []string{"database", "data-key", "config", "archive", "archive-extra", "root-extra", "missing-sidecar", "missing-entry", "truncated-manifest", "manifest-symlink"} {
		t.Run(change, func(t *testing.T) {
			l, root, lock := fixture(t)
			bundle, m := mustCreate(t, l, root, lock)
			switch change {
			case "database", "data-key", "config":
				put(t, filepath.Join(bundle, change), []byte("tampered"))
			case "archive":
				put(t, filepath.Join(bundle, "usage-archives", "2026", "10", "history.jsonl.gz"), []byte("tampered"))
			case "archive-extra":
				put(t, filepath.Join(bundle, "usage-archives", "extra"), []byte("extra"))
			case "root-extra":
				put(t, filepath.Join(bundle, "extra"), []byte("extra"))
			case "missing-sidecar":
				if err := os.Remove(filepath.Join(bundle, "database-wal")); err != nil {
					t.Fatal(err)
				}
			case "missing-entry":
				delete(m.Entries, "database-shm")
				data, err := json.Marshal(m)
				if err != nil {
					t.Fatal(err)
				}
				put(t, filepath.Join(bundle, "manifest.json"), data)
			case "truncated-manifest":
				put(t, filepath.Join(bundle, "manifest.json"), []byte(`{"version":1`))
			case "manifest-symlink":
				if err := os.Remove(filepath.Join(bundle, "manifest.json")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(l.ConfigPath, filepath.Join(bundle, "manifest.json")); err != nil {
					t.Skip(err)
				}
			}
			dest := filepath.Join(root, "restored")
			if _, err := RestoreNew(context.Background(), bundle, dest); err == nil {
				t.Fatal("tampered bundle accepted")
			}
			noPublished(t, root, dest)
		})
	}
}

func TestRestoreSourceChangeDuringStagingFailsClosed(t *testing.T) {
	l, root, lock := fixture(t)
	bundle, _ := mustCreate(t, l, root, lock)
	dest := filepath.Join(root, "restored")
	_, err := restoreNew(context.Background(), bundle, dest, createHooks{beforeRevalidate: func() { put(t, filepath.Join(bundle, "data-key"), []byte("replaced key")) }})
	if err == nil {
		t.Fatal("concurrent bundle change accepted")
	}
	noPublished(t, root, dest)
}

func TestRestoreCoherentBundleReplacementDuringStagingFailsClosed(t *testing.T) {
	l, root, lock := fixture(t)
	bundle, _ := mustCreate(t, l, root, lock)
	dest := filepath.Join(root, "restored")
	_, err := restoreNew(context.Background(), bundle, dest, createHooks{beforeRevalidate: func() {
		put(t, filepath.Join(bundle, "data-key"), []byte("replacement key"))
		current, err := Inspect(context.Background(), BundleLayout(bundle))
		if err != nil {
			t.Fatal(err)
		}
		current.Fence = CooperatingFence
		data, err := json.Marshal(current)
		if err != nil {
			t.Fatal(err)
		}
		put(t, filepath.Join(bundle, "manifest.json"), data)
		if _, err := Verify(context.Background(), bundle); err != nil {
			t.Fatalf("replacement is not coherent: %v", err)
		}
	}})
	if err == nil {
		t.Fatal("coherent source replacement accepted")
	}
	noPublished(t, root, dest)
}

func TestPathAliasesAndExistingRootsRejected(t *testing.T) {
	for _, kind := range []string{"overlap-key", "overlap-archive", "destination-in-archive", "destination-contains-source", "existing-destination", "source-symlink", "ancestor-symlink", "archive-hardlink", "archive-symlink"} {
		t.Run(kind, func(t *testing.T) {
			l, root, lock := fixture(t)
			dest := filepath.Join(root, "bundle")
			switch kind {
			case "overlap-key":
				l.DataKeyPath = l.DatabasePath
			case "overlap-archive":
				l.ArchivesPath = root
			case "destination-in-archive":
				dest = filepath.Join(l.ArchivesPath, "bundle")
			case "destination-contains-source":
				dest = root
			case "existing-destination":
				if err := os.Mkdir(dest, 0o700); err != nil {
					t.Fatal(err)
				}
			case "source-symlink":
				alias := filepath.Join(root, "key-link")
				if err := os.Symlink(l.DataKeyPath, alias); err != nil {
					t.Skip(err)
				}
				l.DataKeyPath = alias
			case "ancestor-symlink":
				alias := filepath.Join(root, "archive-link")
				if err := os.Symlink(l.ArchivesPath, alias); err != nil {
					t.Skip(err)
				}
				l.ArchivesPath = filepath.Join(alias, "2026")
			case "archive-hardlink":
				if err := os.Link(l.ConfigPath, filepath.Join(l.ArchivesPath, "alias")); err != nil {
					t.Skip(err)
				}
			case "archive-symlink":
				if err := os.Symlink(l.ConfigPath, filepath.Join(l.ArchivesPath, "alias")); err != nil {
					t.Skip(err)
				}
			}
			if _, err := Create(context.Background(), l, dest, lock); err == nil {
				t.Fatal("unsafe path accepted")
			}
		})
	}
	l, root, lock := fixture(t)
	bundle, _ := mustCreate(t, l, root, lock)
	if _, err := RestoreNew(context.Background(), bundle, root); err == nil {
		t.Fatal("existing restore root accepted")
	}
}

func TestUnrepresentableArchiveNameDoesNotPublish(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows filenames do not admit invalid UTF-8 bytes")
	}
	l, root, lock := fixture(t)
	if err := os.WriteFile(filepath.Join(l.ArchivesPath, string([]byte{0xff})), []byte("archive"), 0o600); err != nil {
		if errors.Is(err, syscall.EILSEQ) {
			t.Skip("filesystem rejects invalid UTF-8 filenames")
		}
		t.Fatal(err)
	}
	dest := filepath.Join(root, "bundle")
	if _, err := Create(context.Background(), l, dest, lock); err == nil {
		t.Fatal("unrepresentable archive name accepted")
	}
	noPublished(t, root, dest)
}

func TestFilesystemCaseAliasesCannotOverlapSourcePaths(t *testing.T) {
	l, root, lock := fixture(t)
	alias := filepath.Join(root, strings.ToUpper(filepath.Base(l.DatabasePath)))
	if _, err := os.Stat(alias); os.IsNotExist(err) {
		t.Skip("filesystem is case-sensitive")
	} else if err != nil {
		t.Fatal(err)
	}
	l.DataKeyPath = alias
	dest := filepath.Join(root, "bundle")
	if _, err := Create(context.Background(), l, dest, lock); err == nil {
		t.Fatal("physical case alias of database accepted as key")
	}
	noPublished(t, root, dest)
}

func TestRestoreCorruptSQLite(t *testing.T) {
	l, root, lock := fixture(t)
	bundle, _ := mustCreate(t, l, root, lock)
	// Tamper database in bundle with corrupt bytes that are not SQLite
	put(t, filepath.Join(bundle, "database"), []byte("not-a-sqlite-database-corrupted-bytes-payload-header"))
	// Re-inspect bundle layout to produce coherent manifest matching the tampered file
	// so that Verify's digest check passes, forcing the failure to come from matching evidence!
	tamperedManifest, err := Inspect(context.Background(), BundleLayout(bundle))
	if err != nil {
		t.Fatal(err)
	}
	tamperedManifest.Fence = CooperatingFence
	data, err := json.MarshalIndent(tamperedManifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(bundle, "manifest.json"), append(data, '\n'))
	// Verify should pass inventory integrity because the manifest was recomputed.
	if _, err := Verify(context.Background(), bundle); err != nil {
		t.Fatalf("verify failed on coherent manifest: %v", err)
	}
	dest := filepath.Join(root, "restored")
	if _, err := RestoreNew(context.Background(), bundle, dest); err == nil {
		t.Fatal("corrupt SQLite accepted during restore")
	}
	noPublished(t, root, dest)
}

func TestRestoreWrongKey(t *testing.T) {
	for _, which := range []string{"setup", "manager_config_v1", "both"} {
		t.Run(which, func(t *testing.T) {
			l, root, lock := fixture(t)
			db, err := sql.Open("sqlite", l.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			key, _, err := security.LoadOrCreateDataKey("", l.DataKeyPath)
			if err != nil {
				t.Fatal(err)
			}
			protector, err := security.NewProtector(key)
			if err != nil {
				t.Fatal(err)
			}
			saveConnections(t, db, protector, which)
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			bundle, _ := mustCreate(t, l, root, lock)

			// Replace data-key in bundle with a different valid 32-byte key
			otherKeyPath := filepath.Join(root, "other.key")
			if _, _, err := security.LoadOrCreateDataKey("", otherKeyPath); err != nil {
				t.Fatal(err)
			}
			otherKeyBytes, err := os.ReadFile(otherKeyPath)
			if err != nil {
				t.Fatal(err)
			}
			put(t, filepath.Join(bundle, "data-key"), otherKeyBytes)

			rewriteManifest(t, bundle)
			dest := filepath.Join(root, "restored")
			if _, err := RestoreNew(context.Background(), bundle, dest); err == nil || !strings.Contains(err.Error(), "data key") {
				t.Fatalf("wrong key must fail matching evidence: %v", err)
			}
			noPublished(t, root, dest)
		})
	}
}

func TestRestoreArchiveMatchingEvidence(t *testing.T) {
	for _, status := range []string{"published", "verified"} {
		for _, change := range []string{"none", "missing", "root-missing", "foreign-content", "wrong-size", "traversal", "absolute", "wrong-run", "backslash", "directory"} {
			t.Run(status+"/"+change, func(t *testing.T) {
				stage := "archived"
				if status == "verified" {
					stage = "verified"
				}
				l, root, lock := archiveLifecycleFixture(t, stage, "")
				fileName := "00000000000000000000000000000001/segment-000001-00000000000000000001-00000000000000000001.jsonl.gz"
				bundle, _ := mustCreate(t, l, root, lock)
				bundleSeg := filepath.Join(bundle, "usage-archives", filepath.FromSlash(fileName))
				switch change {
				case "root-missing":
					if err := os.RemoveAll(filepath.Join(bundle, "usage-archives")); err != nil {
						t.Fatal(err)
					}
				case "missing", "directory":
					if err := os.Remove(bundleSeg); err != nil {
						t.Fatal(err)
					}
					if change == "directory" {
						if err := os.Mkdir(bundleSeg, 0o700); err != nil {
							t.Fatal(err)
						}
					}
				case "foreign-content":
					// Same path and byte count; only DB-recorded SHA-256 exposes the mix.
					payload, err := os.ReadFile(bundleSeg)
					if err != nil {
						t.Fatal(err)
					}
					payload[0] ^= 1
					put(t, bundleSeg, payload)
				case "wrong-size", "traversal", "absolute", "wrong-run", "backslash":
					db, err := sql.Open("sqlite", filepath.Join(bundle, "database"))
					if err != nil {
						t.Fatal(err)
					}
					query, value := "UPDATE usage_archive_segments SET file_name = ?", "../database"
					switch change {
					case "wrong-size":
						query, value = "UPDATE usage_archive_segments SET compressed_bytes = ?", "999"
					case "absolute":
						value = filepath.ToSlash(bundleSeg)
					case "wrong-run":
						query, value = "UPDATE usage_archive_segments SET run_id = ?", "other-run"
					case "backslash":
						value = `00000000000000000000000000000001\segment-000001-00000000000000000001-00000000000000000001.jsonl.gz`
					}
					if _, err := db.Exec(query, value); err != nil {
						t.Fatal(err)
					}
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}
				}
				rewriteManifest(t, bundle)
				dest := filepath.Join(root, "restored")
				_, err := RestoreNew(context.Background(), bundle, dest)
				if change == "none" {
					if err != nil {
						t.Fatalf("matching archive must restore: %v", err)
					}
				} else {
					want := "archive"
					if change == "wrong-run" {
						want = "foreign_key_check"
					}
					if err == nil || !strings.Contains(err.Error(), want) {
						t.Fatalf("invalid archive must fail matching evidence: %v", err)
					}
					noPublished(t, root, dest)
				}
			})
		}
	}
}

func archiveRunFixture(t *testing.T) (Layout, string, *processlock.Lock) {
	return archiveLifecycleFixture(t, "completed", "")
}

func archiveLifecycleFixture(t *testing.T, status, resume string, modes ...string) (Layout, string, *processlock.Lock) {
	t.Helper()
	l, root, lock := fixture(t)
	db, err := managersqlite.Open(l.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(db)
	defer st.Close()
	ctx := context.Background()
	eventHash := sha256.Sum256([]byte("bundle-production-archive-event"))
	result, err := st.InsertEvents(ctx, []usageparser.Event{{
		RequestID: "bundle-production-event", EventHash: hex.EncodeToString(eventHash[:]),
		TimestampMS: 1000, Timestamp: time.UnixMilli(1000).UTC().Format(time.RFC3339Nano),
		Provider: "codex", ExecutorType: "CodexExecutor", Model: "gpt-test",
		Endpoint: "POST /v1/responses", Method: "POST", Path: "/v1/responses",
		AuthIndex: "auth-1", Source: "account@example.com", AccountSnapshot: "account@example.com",
		InputTokens: 100, OutputTokens: 20, TotalTokens: 120,
		RawJSON: `{"request":{"model":"gpt-test"}}`, CreatedAtMS: 1000,
	}})
	if err != nil || result.Inserted != 1 {
		t.Fatalf("insert production archive event: %#v %v", result, err)
	}
	const runID = "00000000000000000000000000000001"
	createRun := st.UsageArchives.CreateRun
	if len(modes) != 0 && modes[0] == usagearchive.RunModeRetention {
		createRun = st.UsageArchives.CreateRetentionRun
	}
	if _, err := createRun(ctx, runID, 2000, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	service := usageService.New(st, usageService.WithArchive(usageService.ArchiveConfig{
		Directory: l.ArchivesPath, SegmentEventLimit: 1, DeleteBatchSize: 1, AggregateReadsEnabled: true,
	}))
	stage := status
	if status == "failed" {
		stage = resume
	}
	switch stage {
	case "previewed":
	case "archiving":
		_, err = st.UsageArchives.BeginArchive(ctx, runID, time.Now().UnixMilli())
	case "cancelled":
		_, err = service.CancelArchive(ctx, runID)
	default:
		if _, err := service.ResumeArchive(ctx, runID); err != nil {
			t.Fatalf("publish production archive: %v", err)
		}
		if stage == "verifying" {
			_, err = st.UsageArchives.BeginVerification(ctx, runID, time.Now().UnixMilli())
		} else if stage != "archived" {
			catchUpArchiveFixture(t, st)
			if _, err := service.VerifyArchive(ctx, runID); err != nil {
				t.Fatalf("verify production archive: %v", err)
			}
			if stage == "deleting" {
				_, err = st.UsageArchives.BeginDelete(ctx, runID, time.Now().UnixMilli())
			} else if stage == "completed" {
				_, err = service.DeleteArchive(ctx, runID)
			}
		}
	}
	if err != nil {
		t.Fatalf("enter production archive stage %s: %v", stage, err)
	}
	if status == "failed" {
		if _, err := st.UsageArchives.RecordFailure(ctx, runID, resume, errors.New("fixture stage interruption"), time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
	}
	actual, err := service.ArchiveStatus(ctx, runID)
	if err != nil || actual.Run.Status != status || actual.Run.ResumeStatus != resume {
		t.Fatalf("production snapshot status: %#v %v", actual.Run, err)
	}
	if status == "completed" {
		if actual.Run.ArchiveDigest == "" || actual.Run.ArchivedAtMS <= 0 || actual.Run.VerifiedAtMS <= 0 ||
			actual.Run.DeleteStartedAtMS <= 0 || actual.Run.CompletedAtMS <= 0 || actual.Run.DeletedEventCount != 1 ||
			len(actual.Segments) != 1 || actual.Segments[0].VerifiedAtMS <= 0 {
			t.Fatalf("production completed evidence missing: %#v", actual)
		}
		var rawCount int
		if err := db.QueryRow("SELECT count(*) FROM usage_events").Scan(&rawCount); err != nil || rawCount != 0 {
			t.Fatalf("completed snapshot must have deleted raw usage: %d %v", rawCount, err)
		}
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return l, root, lock
}

func catchUpArchiveFixture(t *testing.T, st *store.Store) {
	t.Helper()
	ctx, nowMS := context.Background(), time.Now().UnixMilli()
	catchUp := func(name string, run func() (bool, error)) {
		t.Helper()
		for i := 0; i < 10; i++ {
			pending, err := run()
			if err != nil {
				t.Fatalf("catch up production fixture %s: %v", name, err)
			}
			if !pending {
				return
			}
		}
		t.Fatalf("production fixture %s did not catch up", name)
	}
	catchUp("identity", func() (bool, error) { r, e := st.CatchUpIdentityProjection(ctx, 16, nowMS); return r.Pending, e })
	catchUp("aggregate", func() (bool, error) { r, e := st.CatchUpUsageHourlyAggregate(ctx, 16, nowMS); return r.Pending, e })
	catchUp("pricing", func() (bool, error) { r, e := st.CatchUpUsagePricing(ctx, 16, nowMS); return r.Pending, e })
	for _, run := range []func(context.Context, int, int64) (store.UsageMonitoringCatchUpResult, error){
		st.CatchUpUsageMonitoringStats, st.CatchUpUsageMonitoringMetadata,
		st.CatchUpUsageMonitoringProjection, st.CatchUpCodexLegacyIdentityEvidence,
	} {
		catchUp("monitoring", func() (bool, error) { r, e := run(ctx, 16, nowMS); return r.Pending, e })
	}
	catchUp("account history", func() (bool, error) { r, e := st.CatchUpAccountHistoryRollups(ctx, 16, nowMS); return r.Pending, e })
}

func TestRestoreProductionArchiveSnapshot(t *testing.T) {
	for _, status := range []string{"verified", "completed"} {
		t.Run(status, func(t *testing.T) {
			l, root, lock := archiveLifecycleFixture(t, status, "")
			bundle, _ := mustCreate(t, l, root, lock)
			dest := filepath.Join(root, "restored")
			if _, err := RestoreNew(context.Background(), bundle, dest); err != nil {
				t.Fatal(err)
			}
			restored := BundleLayout(dest)
			st, err := store.Open(restored.DatabasePath)
			if err != nil {
				t.Fatalf("first production open of restored snapshot: %v", err)
			}
			defer st.Close()
			service := usageService.New(st, usageService.WithArchive(usageService.ArchiveConfig{
				Directory: restored.ArchivesPath, SegmentEventLimit: 1, DeleteBatchSize: 1, AggregateReadsEnabled: true,
			}))
			ctx := context.Background()
			const runID = "00000000000000000000000000000001"
			if status == "verified" {
				// This executes production manifest decoding and archive/delete safety
				// on the restored files, rather than only inspecting bundle metadata.
				if _, err := service.DeleteArchive(ctx, runID); err != nil {
					t.Fatalf("production delete guard rejected restored verified archive: %v", err)
				}
			}
			actual, err := service.ArchiveStatus(ctx, runID)
			if err != nil || actual.Run.Status != "completed" || actual.Run.DeletedEventCount != 1 {
				t.Fatalf("restored production archive did not complete: %#v %v", actual, err)
			}
			rawCount, _, err := st.Counts(ctx)
			if err != nil || rawCount != 0 {
				t.Fatalf("restored archive raw usage count: %d %v", rawCount, err)
			}
		})
	}
}

func TestRestorePersistedArchiveRunContract(t *testing.T) {
	for _, assignment := range []string{
		"id = 'run-1'", "id = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA'", "id = 'gggggggggggggggggggggggggggggggg'",
		"mode = 'garbage'", "mode = ''", "schema_version = 0", "schema_version = 999", "format = 'garbage'",
		"requested_stage = 'garbage'",
	} {
		t.Run(assignment, func(t *testing.T) {
			l, root, lock := archiveLifecycleFixture(t, "previewed", "")
			bundle, _ := mustCreate(t, l, root, lock)
			mutateBundleDB(t, bundle, "UPDATE usage_archive_runs SET "+assignment)
			rewriteManifest(t, bundle)
			dest := filepath.Join(root, "restored")
			if _, err := RestoreNew(context.Background(), bundle, dest); err == nil || !strings.Contains(err.Error(), "archive run") {
				t.Fatalf("persisted archive contract must fail after Verify passes: %v", err)
			}
			noPublished(t, root, dest)
		})
	}
}

func TestRestoreProductionArchiveModesAndLocks(t *testing.T) {
	for _, mode := range []string{usagearchive.RunModeManual, usagearchive.RunModeRetention} {
		for _, stage := range []string{"previewed", "archiving", "verifying", "deleting"} {
			t.Run(mode+"/"+stage, func(t *testing.T) {
				l, root, lock := archiveLifecycleFixture(t, stage, "", mode)
				bundle, _ := mustCreate(t, l, root, lock)
				dest := filepath.Join(root, "restored")
				if _, err := RestoreNew(context.Background(), bundle, dest); err != nil {
					t.Fatalf("production archive mode/stage must restore: %v", err)
				}
				st, err := store.Open(BundleLayout(dest).DatabasePath)
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				service := usageService.New(st, usageService.WithArchive(usageService.ArchiveConfig{
					Directory: BundleLayout(dest).ArchivesPath, SegmentEventLimit: 1, DeleteBatchSize: 1, AggregateReadsEnabled: true,
				}))
				ctx := context.Background()
				const runID = "00000000000000000000000000000001"
				switch stage {
				case "previewed", "archiving":
					_, err = service.ResumeArchive(ctx, runID)
				case "verifying":
					catchUpArchiveFixture(t, st)
					_, err = service.VerifyArchive(ctx, runID)
				case "deleting":
					_, err = service.DeleteArchive(ctx, runID)
				}
				if err != nil {
					t.Fatalf("production archive API could not resume restored snapshot: %v", err)
				}
				held, exists, err := st.UsageArchives.MaintenanceLock(ctx)
				if err != nil || exists {
					t.Fatalf("production transition did not release restored lock: %#v %v", held, err)
				}
			})
		}
	}
}

func TestRestoreProductionOpenPreservesOriginalSnapshot(t *testing.T) {
	l, root, lock := fixture(t)
	bundle, manifest := mustCreate(t, l, root, lock)
	dest := filepath.Join(root, "restored")
	if _, err := RestoreNew(context.Background(), bundle, dest); err != nil {
		t.Fatal(err)
	}
	if err := Revalidate(context.Background(), BundleLayout(bundle), manifest); err != nil {
		t.Fatalf("production migration qualification changed bundle: %v", err)
	}
	if err := Revalidate(context.Background(), BundleLayout(dest), manifest); err != nil {
		t.Fatalf("migration scratch was published instead of original snapshot: %v", err)
	}
	db, err := sql.Open("sqlite", sqliteURI(BundleLayout(dest).DatabasePath))
	if err != nil {
		t.Fatal(err)
	}
	hasSettings, err := tableExists(context.Background(), db, "settings")
	_ = db.Close()
	if err != nil || hasSettings {
		t.Fatalf("unmigrated source must remain unmigrated until production opens it: %t %v", hasSettings, err)
	}
	st, err := store.Open(BundleLayout(dest).DatabasePath)
	if err != nil {
		t.Fatalf("first production open of legacy snapshot: %v", err)
	}
	_ = st.Close()
}

func mutateBundleDB(t *testing.T, bundle, query string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(bundle, "database"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(query); err != nil {
		t.Fatal(err)
	}
	var quickCheck string
	if err := db.QueryRow("PRAGMA quick_check(1)").Scan(&quickCheck); err != nil || quickCheck != "ok" {
		t.Fatalf("counterexample must pass quick_check: %q %v", quickCheck, err)
	}
	if err := validateForeignKeys(context.Background(), db); err != nil {
		t.Fatalf("counterexample must pass foreign_key_check: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreArchiveMaintenanceLocks(t *testing.T) {
	t.Run("multiple-global-owners", func(t *testing.T) {
		l, root, lock := archiveLifecycleFixture(t, "archiving", "")
		bundle, _ := mustCreate(t, l, root, lock)
		mutateBundleDB(t, bundle, `
			INSERT INTO usage_archive_runs (id, mode, schema_version, format, status,
				cutoff_timestamp_ms, target_event_id, event_count, created_at_ms, updated_at_ms)
			SELECT '00000000000000000000000000000002', mode, schema_version, format, status,
				cutoff_timestamp_ms, target_event_id, event_count, created_at_ms, updated_at_ms
			FROM usage_archive_runs;
			DROP TABLE usage_maintenance_locks;
			CREATE TABLE usage_maintenance_locks (name TEXT NOT NULL, run_id TEXT NOT NULL,
				operation TEXT NOT NULL, acquired_at_ms INTEGER NOT NULL, updated_at_ms INTEGER NOT NULL,
				FOREIGN KEY(run_id) REFERENCES usage_archive_runs(id));
			INSERT INTO usage_maintenance_locks VALUES
				('usage_archive', '00000000000000000000000000000001', 'archiving', 1, 1),
				('usage_archive', '00000000000000000000000000000002', 'archiving', 1, 1)`)
		rewriteManifest(t, bundle)
		dest := filepath.Join(root, "restored")
		if _, err := RestoreNew(context.Background(), bundle, dest); err == nil || !strings.Contains(err.Error(), "maintenance lock") {
			t.Fatalf("global archive lock cannot have multiple owners even without the schema PK: %v", err)
		}
		noPublished(t, root, dest)
	})
	for _, state := range []struct{ status, resume string }{
		{"previewed", ""}, {"archived", ""}, {"verified", ""}, {"completed", ""}, {"cancelled", ""},
		{"failed", "archiving"}, {"failed", "verifying"}, {"failed", "deleting"},
	} {
		t.Run("stale/"+state.status+"/"+state.resume, func(t *testing.T) {
			l, root, lock := archiveLifecycleFixture(t, state.status, state.resume)
			bundle, _ := mustCreate(t, l, root, lock)
			mutateBundleDB(t, bundle, `INSERT INTO usage_maintenance_locks VALUES
				('usage_archive', '00000000000000000000000000000001', 'archiving', 1, 1)`)
			rewriteManifest(t, bundle)
			dest := filepath.Join(root, "restored")
			if _, err := RestoreNew(context.Background(), bundle, dest); err == nil || !strings.Contains(err.Error(), "maintenance lock") {
				t.Fatalf("inactive/failed run must not retain a maintenance lock: %v", err)
			}
			noPublished(t, root, dest)
		})
	}
	for _, stage := range []string{"archiving", "verifying", "deleting"} {
		otherStage := "archiving"
		if stage == otherStage {
			otherStage = "verifying"
		}
		for _, query := range []string{
			"DELETE FROM usage_maintenance_locks", "DROP TABLE usage_maintenance_locks",
			"UPDATE usage_maintenance_locks SET operation = 'garbage'",
			"UPDATE usage_maintenance_locks SET operation = 'completed'",
			"UPDATE usage_maintenance_locks SET operation = '" + otherStage + "'",
			"UPDATE usage_maintenance_locks SET name = 'other'",
			"UPDATE usage_maintenance_locks SET acquired_at_ms = 0",
			"UPDATE usage_maintenance_locks SET updated_at_ms = -1",
		} {
			t.Run(stage+"/"+query, func(t *testing.T) {
				l, root, lock := archiveLifecycleFixture(t, stage, "")
				bundle, _ := mustCreate(t, l, root, lock)
				mutateBundleDB(t, bundle, query)
				rewriteManifest(t, bundle)
				dest := filepath.Join(root, "restored")
				if _, err := RestoreNew(context.Background(), bundle, dest); err == nil || !strings.Contains(err.Error(), "maintenance lock") {
					t.Fatalf("active run requires matching maintenance lock: %v", err)
				}
				noPublished(t, root, dest)
			})
		}
	}
}

func TestRestoreRequiresProductionSQLiteOpen(t *testing.T) {
	for _, includeIndexColumn := range []bool{false, true} {
		t.Run(fmt.Sprintf("index-column-%t", includeIndexColumn), func(t *testing.T) {
			l, root, lock := fixture(t)
			bundle, _ := mustCreate(t, l, root, lock)
			extraColumn := ""
			if includeIndexColumn {
				extraColumn = ", last_event_id INTEGER"
			}
			mutateBundleDB(t, bundle, `CREATE TABLE usage_archive_segments (
		run_id TEXT NOT NULL, sequence INTEGER NOT NULL, status TEXT NOT NULL,
		file_name TEXT NOT NULL, content_sha256 TEXT NOT NULL, compressed_bytes INTEGER NOT NULL,
		verified_at_ms INTEGER`+extraColumn+`, PRIMARY KEY (run_id, sequence))`)
			rewriteManifest(t, bundle)
			before, err := Inspect(context.Background(), BundleLayout(bundle))
			if err != nil {
				t.Fatal(err)
			}
			// Reproduce the production first-open failure on a disposable control copy.
			controlDB := filepath.Join(root, "production-open-control.sqlite")
			if err := copyFile(context.Background(), filepath.Join(bundle, "database"), controlDB); err != nil {
				t.Fatal(err)
			}
			control, err := managersqlite.Open(controlDB)
			if includeIndexColumn {
				if err != nil {
					t.Fatalf("control migration should pass when the index column exists: %v", err)
				}
				_, err = usagearchive.New(control).Segments(context.Background(), "")
				_ = control.Close()
				if err == nil || !strings.Contains(err.Error(), "first_event_id") {
					t.Fatalf("production archive reader must reject missing non-index column: %v", err)
				}
			} else {
				if err == nil {
					_ = control.Close()
					t.Fatal("incomplete same-name table must fail production migration")
				}
				if !strings.Contains(err.Error(), "last_event_id") {
					t.Fatalf("unexpected control migration failure: %v", err)
				}
			}
			dest := filepath.Join(root, "restored")
			if _, err := RestoreNew(context.Background(), bundle, dest); err == nil || !strings.Contains(err.Error(), "production SQLite open/migration") {
				t.Fatalf("unopenable schema must fail after Verify passes: %v", err)
			}
			noPublished(t, root, dest)
			if err := Revalidate(context.Background(), BundleLayout(bundle), before); err != nil {
				t.Fatalf("production open qualification changed bundle: %v", err)
			}
		})
	}
}

func TestRestoreRequiresArchiveDataPlaneSchema(t *testing.T) {
	for _, tc := range []struct {
		name, ddl, runtimeError string
	}{
		{"refs-missing-event-hash", `DROP TABLE usage_archive_event_refs;
			CREATE TABLE usage_archive_event_refs (run_id TEXT, raw_deleted_at_ms INTEGER,
				raw_event_id INTEGER, timestamp_ms INTEGER)`, "event_hash"},
		{"refs-missing-segment-sequence", `ALTER TABLE usage_archive_event_refs RENAME TO old_refs;
			CREATE TABLE usage_archive_event_refs (event_hash TEXT PRIMARY KEY, run_id TEXT,
				raw_event_id INTEGER, timestamp_ms INTEGER, archived_at_ms INTEGER, raw_deleted_at_ms INTEGER);
			DROP TABLE old_refs`, "segment_sequence"},
		{"refs-missing-archived-at", `ALTER TABLE usage_archive_event_refs RENAME TO old_refs;
			CREATE TABLE usage_archive_event_refs (event_hash TEXT PRIMARY KEY, run_id TEXT,
				segment_sequence INTEGER, raw_event_id INTEGER, timestamp_ms INTEGER, raw_deleted_at_ms INTEGER);
			DROP TABLE old_refs`, "archived_at_ms"},
		{"identity-ledger", `DROP TABLE usage_event_identity_ledger;
			CREATE TABLE usage_event_identity_ledger (event_hash TEXT PRIMARY KEY)`, "raw_event_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, root, lock := archiveLifecycleFixture(t, "previewed", "")
			bundle, _ := mustCreate(t, l, root, lock)
			mutateBundleDB(t, bundle, tc.ddl)
			rewriteManifest(t, bundle)
			before, err := Inspect(context.Background(), BundleLayout(bundle))
			if err != nil {
				t.Fatal(err)
			}
			controlPath := filepath.Join(root, "production-control.sqlite")
			if err := copyFile(context.Background(), filepath.Join(bundle, "database"), controlPath); err != nil {
				t.Fatal(err)
			}
			control, err := managersqlite.Open(controlPath)
			if err != nil {
				t.Fatalf("counterexample must pass production migration: %v", err)
			}
			st := store.New(control)
			defer st.Close()
			service := usageService.New(st, usageService.WithArchive(usageService.ArchiveConfig{
				Directory: filepath.Join(root, "control-archives"), SegmentEventLimit: 1,
			}))
			if _, err := service.ResumeArchive(context.Background(), "00000000000000000000000000000001"); err == nil || !strings.Contains(err.Error(), tc.runtimeError) {
				t.Fatalf("production archive must fail on missing data-plane column %s: %v", tc.runtimeError, err)
			}
			dest := filepath.Join(root, "restored")
			if _, err := RestoreNew(context.Background(), bundle, dest); err == nil || !strings.Contains(err.Error(), "production SQLite open/migration archive data-plane") {
				t.Fatalf("unexecutable archive data-plane must fail after Verify passes: %v", err)
			}
			noPublished(t, root, dest)
			if err := Revalidate(context.Background(), BundleLayout(bundle), before); err != nil {
				t.Fatalf("data-plane qualification changed original bundle: %v", err)
			}
		})
	}
}

func TestRestoreRequiresProductionPersistedRows(t *testing.T) {
	for _, tc := range []struct{ stage, query, readerError string }{
		{"previewed", "UPDATE usage_archive_runs SET event_count = 'garbage'", "event_count"},
		{"previewed", "UPDATE usage_archive_runs SET progress_current = 'garbage'", "progress_current"},
		{"verified", "UPDATE usage_archive_segments SET first_event_id = 'garbage'", "first_event_id"},
	} {
		t.Run(tc.readerError, func(t *testing.T) {
			l, root, lock := archiveLifecycleFixture(t, tc.stage, "")
			bundle, _ := mustCreate(t, l, root, lock)
			mutateBundleDB(t, bundle, tc.query)
			rewriteManifest(t, bundle)
			before, err := Inspect(context.Background(), BundleLayout(bundle))
			if err != nil {
				t.Fatal(err)
			}
			controlPath := filepath.Join(root, "production-control.sqlite")
			if err := copyFile(context.Background(), filepath.Join(bundle, "database"), controlPath); err != nil {
				t.Fatal(err)
			}
			control, err := managersqlite.Open(controlPath)
			if err != nil {
				t.Fatalf("counterexample must pass production migration: %v", err)
			}
			defer control.Close()
			repo := usagearchive.New(control)
			if _, err := repo.Run(context.Background(), ""); !errors.Is(err, usagearchive.ErrNotFound) {
				t.Fatalf("empty-ID run qualification must pass: %v", err)
			}
			if _, err := repo.Segments(context.Background(), ""); err != nil {
				t.Fatalf("empty-ID segment qualification must pass: %v", err)
			}
			if err := repo.ValidateDataPlaneSchema(context.Background()); err != nil {
				t.Fatalf("schema compilation must pass with malformed persisted values: %v", err)
			}
			service := usageService.New(store.New(control), usageService.WithArchive(usageService.ArchiveConfig{
				Directory: filepath.Join(root, "control-archives"),
			}))
			if _, err := service.ArchiveStatus(context.Background(), "00000000000000000000000000000001"); err == nil || !strings.Contains(err.Error(), tc.readerError) {
				t.Fatalf("real production scanner must fail on %s: %v", tc.readerError, err)
			}
			dest := filepath.Join(root, "restored")
			if _, err := RestoreNew(context.Background(), bundle, dest); err == nil || !strings.Contains(err.Error(), "archive persisted rows") {
				t.Fatalf("unreadable persisted rows must fail after Verify passes: %v", err)
			}
			noPublished(t, root, dest)
			if err := Revalidate(context.Background(), BundleLayout(bundle), before); err != nil {
				t.Fatalf("persisted-row qualification changed original bundle: %v", err)
			}
		})
	}
}

func TestRestoreRequiresProductionDeleteSchema(t *testing.T) {
	l, root, lock := archiveLifecycleFixture(t, "verified", "")
	bundle, _ := mustCreate(t, l, root, lock)
	mutateBundleDB(t, bundle, `DROP TABLE usage_archive_deleted_coverage_daily;
		CREATE TABLE usage_archive_deleted_coverage_daily (utc_day INTEGER PRIMARY KEY)`)
	rewriteManifest(t, bundle)
	before, err := Inspect(context.Background(), BundleLayout(bundle))
	if err != nil {
		t.Fatal(err)
	}
	controlRoot := filepath.Join(root, "production-control")
	if err := os.Mkdir(controlRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := materialize(context.Background(), BundleLayout(bundle), controlRoot, before, copyFile); err != nil {
		t.Fatal(err)
	}
	control, err := store.Open(BundleLayout(controlRoot).DatabasePath)
	if err != nil {
		t.Fatalf("counterexample must pass production open: %v", err)
	}
	defer control.Close()
	service := usageService.New(control, usageService.WithArchive(usageService.ArchiveConfig{
		Directory: BundleLayout(controlRoot).ArchivesPath, DeleteBatchSize: 1, AggregateReadsEnabled: true,
	}))
	if _, err := service.DeleteArchive(context.Background(), "00000000000000000000000000000001"); err == nil || !strings.Contains(err.Error(), "deleted_event_count") {
		t.Fatalf("production delete must fail on incomplete daily persistence table: %v", err)
	}
	dest := filepath.Join(root, "restored")
	if _, err := RestoreNew(context.Background(), bundle, dest); err == nil || !strings.Contains(err.Error(), "archive data-plane") {
		t.Fatalf("unexecutable delete schema must fail after Verify passes: %v", err)
	}
	noPublished(t, root, dest)
	if err := Revalidate(context.Background(), BundleLayout(bundle), before); err != nil {
		t.Fatalf("delete-schema qualification changed original bundle: %v", err)
	}
}

func TestRestoreInvalidArchiveSegmentStatus(t *testing.T) {
	for _, status := range []string{"garbage", "archived", "completed", ""} {
		for _, change := range []string{"missing", "foreign-content"} {
			t.Run(status+"/"+change, func(t *testing.T) {
				l, root, lock := archiveRunFixture(t)
				bundle, _ := mustCreate(t, l, root, lock)
				db, err := sql.Open("sqlite", filepath.Join(bundle, "database"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("UPDATE usage_archive_segments SET status = ?", status); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				segPath := filepath.Join(bundle, "usage-archives", "00000000000000000000000000000001", "segment-000001-00000000000000000001-00000000000000000001.jsonl.gz")
				if change == "missing" {
					if err := os.Remove(segPath); err != nil {
						t.Fatal(err)
					}
				} else {
					put(t, segPath, []byte("instance B archive bytes"))
				}
				rewriteManifest(t, bundle)
				dest := filepath.Join(root, "restored")
				if _, err := RestoreNew(context.Background(), bundle, dest); err == nil || !strings.Contains(err.Error(), "archive segment status") {
					t.Fatalf("invalid status must fail before a record can escape matching: %v", err)
				}
				noPublished(t, root, dest)
			})
		}
	}
}

func TestRestoreArchiveRunManifestMatchingEvidence(t *testing.T) {
	for _, change := range []string{"none", "missing", "foreign-content", "missing-sha", "missing-file", "invalid-sha", "traversal", "absolute", "backslash", "wrong-run", "empty", "oversize", "directory", "segments-table-absent"} {
		t.Run(change, func(t *testing.T) {
			l, root, lock := archiveRunFixture(t)
			bundle, _ := mustCreate(t, l, root, lock)
			manifestPath := filepath.Join(bundle, "usage-archives", "00000000000000000000000000000001", "manifest.json")
			switch change {
			case "missing", "directory", "segments-table-absent":
				if err := os.Remove(manifestPath); err != nil {
					t.Fatal(err)
				}
				if change == "directory" {
					if err := os.Mkdir(manifestPath, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			case "foreign-content":
				payload, err := os.ReadFile(manifestPath)
				if err != nil {
					t.Fatal(err)
				}
				payload[0] ^= 1 // Same filename and size; DB SHA must reject replacement.
				put(t, manifestPath, payload)
			case "empty":
				put(t, manifestPath, nil)
			case "oversize":
				put(t, manifestPath, make([]byte, 4*1024*1024+1))
			}
			queries := map[string]string{
				"missing-sha":           "UPDATE usage_archive_runs SET manifest_sha256 = NULL",
				"missing-file":          "UPDATE usage_archive_runs SET manifest_file = ''",
				"invalid-sha":           "UPDATE usage_archive_runs SET manifest_sha256 = 'invalid'",
				"traversal":             "UPDATE usage_archive_runs SET manifest_file = '../database'",
				"backslash":             `UPDATE usage_archive_runs SET manifest_file = '00000000000000000000000000000001\manifest.json'`,
				"wrong-run":             "UPDATE usage_archive_runs SET manifest_file = 'other-run/manifest.json'",
				"segments-table-absent": "DELETE FROM usage_archive_event_refs; DROP TABLE usage_archive_segments",
			}
			if query, ok := queries[change]; ok || change == "absolute" {
				db, err := sql.Open("sqlite", filepath.Join(bundle, "database"))
				if err != nil {
					t.Fatal(err)
				}
				if change == "absolute" {
					_, err = db.Exec("UPDATE usage_archive_runs SET manifest_file = ?", filepath.ToSlash(manifestPath))
				} else {
					_, err = db.Exec(query)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			rewriteManifest(t, bundle)
			dest := filepath.Join(root, "restored")
			_, err := RestoreNew(context.Background(), bundle, dest)
			if change == "none" {
				if err != nil {
					t.Fatalf("matching run manifest must restore: %v", err)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), "archive") {
					t.Fatalf("invalid run manifest must fail matching evidence: %v", err)
				}
				noPublished(t, root, dest)
			}
		})
	}
}

func TestRestoreArchiveRelationalAndDowngradeEvidence(t *testing.T) {
	for _, change := range []string{"matching-ref", "orphan-segment", "orphan-ref-ledger", "orphan-ref-segment", "completed-to-previewed"} {
		t.Run(change, func(t *testing.T) {
			l, root, lock := archiveRunFixture(t)
			bundle, _ := mustCreate(t, l, root, lock)
			db, err := sql.Open("sqlite", filepath.Join(bundle, "database"))
			if err != nil {
				t.Fatal(err)
			}
			db.SetMaxOpenConns(1)
			if _, err := db.Exec("PRAGMA foreign_keys = OFF"); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "orphan-segment":
				_, err = db.Exec("DELETE FROM usage_archive_runs WHERE id = '00000000000000000000000000000001'")
			case "orphan-ref-ledger":
				_, err = db.Exec("UPDATE usage_archive_event_refs SET event_hash = 'missing-ledger'")
			case "orphan-ref-segment":
				_, err = db.Exec("UPDATE usage_archive_event_refs SET segment_sequence = 99")
			case "completed-to-previewed":
				_, err = db.Exec("UPDATE usage_archive_runs SET status = 'previewed', manifest_file = NULL, manifest_sha256 = NULL")
			}
			if err != nil {
				t.Fatal(err)
			}
			var result string
			if err := db.QueryRow("PRAGMA quick_check(1)").Scan(&result); err != nil || result != "ok" {
				t.Fatalf("counterexample must pass quick_check: %q %v", result, err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if change == "completed-to-previewed" {
				if err := os.Remove(filepath.Join(bundle, "usage-archives", "00000000000000000000000000000001", "manifest.json")); err != nil {
					t.Fatal(err)
				}
			}
			rewriteManifest(t, bundle)
			dest := filepath.Join(root, "restored")
			_, err = RestoreNew(context.Background(), bundle, dest)
			if change == "matching-ref" {
				if err != nil {
					t.Fatalf("valid foreign key relationships must restore: %v", err)
				}
				return
			}
			want := "foreign_key_check"
			if change == "completed-to-previewed" {
				want = "deletion evidence does not match run lifecycle"
			}
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("relational corruption or completed-run downgrade must fail with %q: %v", want, err)
			}
			noPublished(t, root, dest)
		})
	}
}

func TestRestoreArchiveAtomicTransitionEvidence(t *testing.T) {
	for _, state := range []struct{ status, resume string }{
		{"archived", ""}, {"verifying", ""}, {"failed", "verifying"},
		{"verified", ""}, {"deleting", ""}, {"failed", "deleting"}, {"completed", ""},
	} {
		stage := state.status
		if stage == "failed" {
			stage = state.resume
		}
		fields := []string{"archive_digest", "archived_at_ms"}
		if stage == "verified" || stage == "deleting" || stage == "completed" {
			fields = append(fields, "verified_at_ms", "segment_verified_at_ms")
		}
		if stage == "deleting" || stage == "completed" {
			fields = append(fields, "delete_started_at_ms")
		}
		if stage == "completed" {
			fields = append(fields, "completed_at_ms")
		}
		for _, field := range fields {
			values := []string{"NULL", "0", "-1"}
			if field == "archive_digest" {
				values = []string{"NULL", "''", "'   '"}
			}
			for _, value := range values {
				t.Run(state.status+"/"+state.resume+"/"+field+"/"+value, func(t *testing.T) {
					l, root, lock := archiveLifecycleFixture(t, state.status, state.resume)
					bundle, _ := mustCreate(t, l, root, lock)
					db, err := sql.Open("sqlite", filepath.Join(bundle, "database"))
					if err != nil {
						t.Fatal(err)
					}
					query := "UPDATE usage_archive_runs SET " + field + " = " + value
					if field == "segment_verified_at_ms" {
						query = "UPDATE usage_archive_segments SET verified_at_ms = " + value
					}
					if _, err := db.Exec(query); err != nil {
						t.Fatal(err)
					}
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}
					rewriteManifest(t, bundle)
					dest := filepath.Join(root, "restored")
					if _, err := RestoreNew(context.Background(), bundle, dest); err == nil || !strings.Contains(err.Error(), "archive") {
						t.Fatalf("missing atomic transition evidence %s must fail: %v", field, err)
					}
					noPublished(t, root, dest)
				})
			}
		}
	}
}

func TestRollbackJournalMatchingPolicy(t *testing.T) {
	for _, payload := range [][]byte{nil, {}, []byte("unqualified rollback journal")} {
		name := "absent"
		if payload != nil {
			name = "empty"
			if len(payload) > 0 {
				name = "nonempty"
			}
		}
		t.Run(name, func(t *testing.T) {
			l, root, lock := fixture(t)
			if payload != nil {
				put(t, l.DatabasePath+"-journal", payload)
			}
			before, err := Inspect(context.Background(), l)
			if err != nil {
				t.Fatal(err)
			}
			bundle := filepath.Join(root, "bundle")
			_, err = Create(context.Background(), l, bundle, lock)
			if len(payload) > 0 {
				if err == nil || !strings.Contains(err.Error(), "rollback journal recovery is not qualified") {
					t.Fatalf("nonempty journal must fail before publication: %v", err)
				}
				noPublished(t, root, bundle)
			} else if err != nil {
				t.Fatalf("absent/empty journal remains supported: %v", err)
			} else if _, err := RestoreNew(context.Background(), bundle, filepath.Join(root, "restored")); err != nil {
				t.Fatal(err)
			}
			if err := Revalidate(context.Background(), l, before); err != nil {
				t.Fatalf("journal policy inspection changed source: %v", err)
			}
		})
	}
}

func TestRestoreHotRollbackJournal(t *testing.T) {
	if helperDB := os.Getenv("CPAMP_BUNDLE_HOT_JOURNAL_DB"); helperDB != "" {
		db, err := sql.Open("sqlite", helperDB)
		if err != nil {
			t.Fatal(err)
		}
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(`PRAGMA journal_mode=DELETE; PRAGMA synchronous=FULL;
			PRAGMA cache_size=2; PRAGMA cache_spill=ON; BEGIN IMMEDIATE;
			UPDATE journal_test SET note = CAST(hex(zeroblob(2000)) AS BLOB)`); err != nil {
			t.Fatal(err)
		}
		// Exit without Close/Rollback: spilled uncommitted pages and a real hot
		// journal survive exactly as after a writer crash, in task-owned test data.
		os.Exit(0)
	}
	l, root, lock := fixture(t)
	bundle, _ := mustCreate(t, l, root, lock)
	seedPath := filepath.Join(root, "rollback.sqlite")
	seed, err := sql.Open("sqlite", seedPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`PRAGMA journal_mode=DELETE; CREATE TABLE journal_test (id INTEGER PRIMARY KEY, note BLOB);
		WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<64)
		INSERT INTO journal_test SELECT n, zeroblob(4000) FROM seq`); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile(seedPath)
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestRestoreHotRollbackJournal$")
	child.Env = append(os.Environ(), "CPAMP_BUNDLE_HOT_JOURNAL_DB="+seedPath)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("crash fixture writer: %v %s", err, output)
	}
	dirty, err := os.ReadFile(seedPath)
	if err != nil || bytes.Equal(committed, dirty) {
		t.Fatalf("fixture did not spill uncommitted pages: %v", err)
	}
	journal, err := os.ReadFile(seedPath + "-journal")
	if err != nil || len(journal) < 28 || !bytes.Equal(journal[:8], []byte{0xd9, 0xd5, 0x05, 0xf9, 0x20, 0xa1, 0x63, 0xd7}) {
		t.Fatalf("fixture did not leave a hot rollback journal: %v", err)
	}
	controlPath := filepath.Join(root, "recovery-control.sqlite")
	put(t, controlPath, dirty)
	put(t, controlPath+"-journal", journal)
	control, err := sql.Open("sqlite", sqliteURI(controlPath))
	if err != nil {
		t.Fatal(err)
	}
	var value string
	err = control.QueryRow("SELECT hex(substr(note,1,1)) FROM journal_test WHERE id=1").Scan(&value)
	_ = control.Close()
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "readonly") {
		t.Fatalf("read-only open must require journal recovery: %v", err)
	}
	control, err = sql.Open("sqlite", controlPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := control.QueryRow("SELECT hex(substr(note,1,1)) FROM journal_test WHERE id=1").Scan(&value); err != nil || value != "00" {
		t.Fatalf("writable control must roll back to committed bytes: %q %v", value, err)
	}
	if err := control.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(controlPath + "-journal"); !os.IsNotExist(err) {
		t.Fatalf("recovery control did not consume journal: %v", err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if err := os.Remove(filepath.Join(bundle, "database") + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	put(t, filepath.Join(bundle, "database"), dirty)
	put(t, filepath.Join(bundle, "database-journal"), journal)
	rewriteManifest(t, bundle)
	before, err := Inspect(context.Background(), BundleLayout(bundle))
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(root, "restored")
	if _, err := RestoreNew(context.Background(), bundle, dest); err == nil || !strings.Contains(err.Error(), "rollback journal recovery is not qualified") {
		t.Fatalf("hot journal bundle must fail matching after Verify passes: %v", err)
	}
	noPublished(t, root, dest)
	if err := Revalidate(context.Background(), BundleLayout(bundle), before); err != nil {
		t.Fatalf("rejected hot journal bundle was mutated: %v", err)
	}
}

func TestRestoreArchiveLifecycleRequiredEvidence(t *testing.T) {
	states := []struct{ status, resume, segment string }{
		{"archived", "", "published"}, {"verifying", "", "published"},
		{"verified", "", "verified"}, {"deleting", "", "verified"}, {"completed", "", "verified"},
		{"failed", "verifying", "published"}, {"failed", "deleting", "verified"},
	}
	for _, state := range states {
		for _, change := range []string{"none", "manifest-cleared", "manifest-empty", "segment-status-mismatch", "mixed-segment-statuses", "segments-table-absent", "segments-empty"} {
			t.Run(state.status+"/"+state.resume+"/"+change, func(t *testing.T) {
				l, root, lock := archiveLifecycleFixture(t, state.status, state.resume)
				bundle, _ := mustCreate(t, l, root, lock)
				db, err := sql.Open("sqlite", filepath.Join(bundle, "database"))
				if err != nil {
					t.Fatal(err)
				}
				queries := map[string]string{
					"manifest-cleared":      "UPDATE usage_archive_runs SET manifest_file = NULL, manifest_sha256 = NULL",
					"manifest-empty":        "UPDATE usage_archive_runs SET manifest_file = '', manifest_sha256 = ''",
					"segments-table-absent": "DELETE FROM usage_archive_event_refs; DROP TABLE usage_archive_segments",
					"segments-empty":        "DELETE FROM usage_archive_event_refs; DELETE FROM usage_archive_segments",
				}
				if query, ok := queries[change]; ok {
					if _, err := db.Exec(query); err != nil {
						t.Fatal(err)
					}
				}
				if change == "segment-status-mismatch" || change == "mixed-segment-statuses" {
					otherStatus := "published"
					if state.segment == "published" {
						otherStatus = "verified"
					}
					if change == "mixed-segment-statuses" {
						payload, err := os.ReadFile(filepath.Join(bundle, "usage-archives", "00000000000000000000000000000001", "segment-000001-00000000000000000001-00000000000000000001.jsonl.gz"))
						if err != nil {
							t.Fatal(err)
						}
						put(t, filepath.Join(bundle, "usage-archives", "00000000000000000000000000000001", "second-segment-000001-00000000000000000001-00000000000000000001.jsonl.gz"), payload)
						if _, err := db.Exec(`INSERT INTO usage_archive_segments (
						run_id, sequence, status, file_name, first_event_id, last_event_id,
						min_timestamp_ms, max_timestamp_ms, event_count, uncompressed_bytes,
						compressed_bytes, content_sha256, event_hash_digest, created_at_ms, verified_at_ms
						) SELECT run_id, 2, ?, '00000000000000000000000000000001/second-segment-000001-00000000000000000001-00000000000000000001.jsonl.gz', first_event_id, last_event_id,
						min_timestamp_ms, max_timestamp_ms, event_count, uncompressed_bytes,
						compressed_bytes, content_sha256, event_hash_digest, created_at_ms,
						CASE WHEN ? = 'verified' THEN 2 ELSE NULL END
					FROM usage_archive_segments WHERE sequence = 1`, otherStatus, otherStatus); err != nil {
							t.Fatal(err)
						}
					} else if _, err := db.Exec("UPDATE usage_archive_segments SET status = ?, verified_at_ms = CASE WHEN ? = 'verified' THEN 2 ELSE NULL END", otherStatus, otherStatus); err != nil {
						t.Fatal(err)
					}
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				if change == "manifest-cleared" || change == "manifest-empty" {
					if err := os.Remove(filepath.Join(bundle, "usage-archives", "00000000000000000000000000000001", "manifest.json")); err != nil {
						t.Fatal(err)
					}
				}
				rewriteManifest(t, bundle)
				dest := filepath.Join(root, "restored")
				_, err = RestoreNew(context.Background(), bundle, dest)
				if change == "none" {
					if err != nil {
						t.Fatalf("matching lifecycle evidence must restore: %v", err)
					}
					return
				}
				want := "requires segment evidence"
				if strings.HasPrefix(change, "manifest-") {
					want = "requires manifest file and digest"
				} else if change == "segment-status-mismatch" || change == "mixed-segment-statuses" {
					want = "segment status does not match run lifecycle"
				}
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Fatalf("run lifecycle must fail closed with %q: %v", want, err)
				}
				noPublished(t, root, dest)
			})
		}
	}
}

func TestRestorePrepublicationArchivePolicy(t *testing.T) {
	for _, state := range []struct{ status, resume string }{
		{"previewed", ""}, {"archiving", ""}, {"failed", "archiving"}, {"cancelled", ""},
	} {
		for _, segment := range []string{"none", "published", "verified"} {
			for _, manifest := range []string{"absent", "present"} {
				t.Run(state.status+"/"+state.resume+"/"+segment+"/"+manifest, func(t *testing.T) {
					l, root, lock := archiveLifecycleFixture(t, "archived", "")
					bundle, _ := mustCreate(t, l, root, lock)
					db, err := sql.Open("sqlite", filepath.Join(bundle, "database"))
					if err != nil {
						t.Fatal(err)
					}
					// Keep raw usage intact while forging prepublication/cancelled metadata.
					if _, err := db.Exec(`UPDATE usage_archive_runs SET status = ?, resume_status = ?,
						verified_at_ms = NULL`, state.status, state.resume); err != nil {
						t.Fatal(err)
					}
					if state.status == "archiving" {
						if _, err := db.Exec(`INSERT INTO usage_maintenance_locks VALUES
							('usage_archive', '00000000000000000000000000000001', 'archiving', 1, 1)`); err != nil {
							t.Fatal(err)
						}
					}
					if state.status == "cancelled" || segment == "none" {
						if _, err := db.Exec("DELETE FROM usage_archive_event_refs"); err != nil {
							t.Fatal(err)
						}
					}
					if segment == "none" {
						_, err = db.Exec("DELETE FROM usage_archive_segments")
					} else {
						_, err = db.Exec("UPDATE usage_archive_segments SET status = ?, verified_at_ms = CASE WHEN ? = 'verified' THEN 2 ELSE NULL END", segment, segment)
					}
					if err != nil {
						t.Fatal(err)
					}
					if manifest == "absent" {
						if _, err := db.Exec("UPDATE usage_archive_runs SET manifest_file = NULL, manifest_sha256 = NULL, archive_digest = NULL, archived_at_ms = NULL"); err != nil {
							t.Fatal(err)
						}
						if err := os.Remove(filepath.Join(bundle, "usage-archives", "00000000000000000000000000000001", "manifest.json")); err != nil {
							t.Fatal(err)
						}
					}
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}
					rewriteManifest(t, bundle)
					allowed := segment != "verified" && (state.status == "cancelled" || manifest == "absent") &&
						(state.status != "previewed" || segment == "none")
					dest := filepath.Join(root, "restored")
					_, err = RestoreNew(context.Background(), bundle, dest)
					if allowed {
						if err != nil {
							t.Fatalf("reachable prepublication/cancelled snapshot must restore: %v", err)
						}
					} else {
						if err == nil || !strings.Contains(err.Error(), "archive") {
							t.Fatalf("unreachable segment/manifest combination must fail: %v", err)
						}
						noPublished(t, root, dest)
					}
				})
			}
		}
	}
}

func TestRestoreArchiveDeletionEvidence(t *testing.T) {
	states := []struct{ status, resume string }{
		{"previewed", ""}, {"archiving", ""}, {"archived", ""}, {"verifying", ""},
		{"verified", ""}, {"cancelled", ""}, {"failed", "archiving"}, {"failed", "verifying"},
		{"deleting", ""}, {"failed", "deleting"}, {"completed", ""},
	}
	markers := []struct{ name, assignment string }{
		{"count", "deleted_event_count = 1"}, {"last-id", "last_deleted_event_id = 1"},
		{"started", "delete_started_at_ms = 2"}, {"completed", "completed_at_ms = 3"},
	}
	for _, state := range states {
		for _, marker := range markers {
			t.Run(state.status+"/"+state.resume+"/"+marker.name, func(t *testing.T) {
				l, root, lock := archiveLifecycleFixture(t, state.status, state.resume)
				bundle, _ := mustCreate(t, l, root, lock)
				db, err := sql.Open("sqlite", filepath.Join(bundle, "database"))
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec("UPDATE usage_archive_runs SET " + marker.assignment); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				rewriteManifest(t, bundle)
				deletionAllowed := state.status == "deleting" || state.status == "completed" || state.resume == "deleting"
				allowed := deletionAllowed && (marker.name != "completed" || state.status == "completed")
				dest := filepath.Join(root, "restored")
				_, err = RestoreNew(context.Background(), bundle, dest)
				if allowed {
					if err != nil {
						t.Fatalf("deletion-stage evidence must remain restorable: %v", err)
					}
				} else {
					want := "deletion evidence does not match run lifecycle"
					if deletionAllowed {
						want = "completion evidence requires completed run"
					}
					if err == nil || !strings.Contains(err.Error(), want) {
						t.Fatalf("irreversible marker must fail with %q: %v", want, err)
					}
					noPublished(t, root, dest)
				}
			})
		}
	}
}

func TestRestoreInvalidArchiveRunLifecycle(t *testing.T) {
	for _, state := range []struct{ status, resume string }{
		{"garbage", ""}, {"", ""}, {"published", ""},
		{"failed", ""}, {"failed", "garbage"}, {"failed", "completed"},
		{"failed", "previewed"}, {"failed", "archived"},
		{"completed", "deleting"}, {"previewed", "garbage"},
	} {
		t.Run(state.status+"/"+state.resume, func(t *testing.T) {
			l, root, lock := archiveRunFixture(t)
			bundle, _ := mustCreate(t, l, root, lock)
			db, err := sql.Open("sqlite", filepath.Join(bundle, "database"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("UPDATE usage_archive_runs SET status = ?, resume_status = ?", state.status, state.resume); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			rewriteManifest(t, bundle)
			dest := filepath.Join(root, "restored")
			if _, err := RestoreNew(context.Background(), bundle, dest); err == nil || !strings.Contains(err.Error(), "archive run") {
				t.Fatalf("unknown or impossible run/resume combination must fail: %v", err)
			}
			noPublished(t, root, dest)
		})
	}
}

func TestUnpublishedArchiveRunManifestFieldsAreOptional(t *testing.T) {
	for _, state := range []struct{ status, resume string }{
		{"previewed", ""}, {"archiving", ""}, {"cancelled", ""}, {"failed", "archiving"},
	} {
		for _, pair := range []string{"null", "empty"} {
			for _, segments := range []string{"present", "absent"} {
				t.Run(state.status+"/"+state.resume+"/"+pair+"/segments-"+segments, func(t *testing.T) {
					l, root, lock := archiveLifecycleFixture(t, state.status, state.resume)
					db, err := sql.Open("sqlite", l.DatabasePath)
					if err != nil {
						t.Fatal(err)
					}
					if segments == "absent" {
						if _, err := db.Exec("DROP TABLE usage_archive_segments"); err != nil {
							t.Fatal(err)
						}
					}
					if pair == "empty" {
						if _, err := db.Exec("UPDATE usage_archive_runs SET manifest_file = '', manifest_sha256 = ''"); err != nil {
							t.Fatal(err)
						}
					}
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}
					bundle, _ := mustCreate(t, l, root, lock)
					if _, err := RestoreNew(context.Background(), bundle, filepath.Join(root, "restored")); err != nil {
						t.Fatalf("unpublished runs need not have manifest evidence: %v", err)
					}
				})
			}
		}
	}
}

func TestRestoreInspectionErrorsFailClosed(t *testing.T) {
	cases := []struct {
		name, sql, want string
	}{
		{"settings-query", `CREATE TABLE settings (value TEXT)`, "inspect persisted CPA connection storage"},
		{"settings-view", `CREATE VIEW settings AS SELECT 'setup' AS key, '{}' AS value`, "not a table"},
		{"settings-scan", `CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT); INSERT INTO settings VALUES ('setup', NULL)`, "scan encrypted CPA connection"},
		{"settings-json", `CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT); INSERT INTO settings VALUES ('setup', '{')`, "invalid setup JSON"},
		{"bootstrap-json", `CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT); INSERT INTO settings VALUES ('bootstrap_state_v1', '{')`, "invalid bootstrap state"},
		{"migrated-plaintext", `CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT);
			INSERT INTO settings VALUES ('bootstrap_state_v1', '{"connectionStorageMigrationVersion":2}');
			INSERT INTO settings VALUES ('manager_config_v1', '{"cpaConnection":{"managementKey":"plaintext"}}')`, "not a valid encrypted envelope"},
		{"migrated-malformed-envelope", `CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT);
			INSERT INTO settings VALUES ('bootstrap_state_v1', '{"connectionStorageMigrationVersion":2}');
			INSERT INTO settings VALUES ('setup', '{"managementKey":"enc:v1:broken"}')`, "not a valid encrypted envelope"},
		{"archive-query", `CREATE TABLE usage_archive_segments (file_name TEXT, status TEXT)`, "query archive matching evidence"},
		{"archive-view", `CREATE VIEW usage_archive_segments AS SELECT 'published' AS status`, "not a table"},
		{"archive-scan", `CREATE TABLE usage_archive_segments (run_id TEXT, file_name TEXT, content_sha256 TEXT, compressed_bytes INTEGER, status TEXT, verified_at_ms INTEGER);
			INSERT INTO usage_archive_segments VALUES ('00000000000000000000000000000001', NULL, '', 1, 'published', NULL)`, "scan archive matching evidence"},
		{"archive-size-scan", `CREATE TABLE usage_archive_segments (run_id TEXT, file_name TEXT, content_sha256 TEXT, compressed_bytes INTEGER, status TEXT, verified_at_ms INTEGER);
			INSERT INTO usage_archive_segments VALUES ('00000000000000000000000000000001', '00000000000000000000000000000001/segment.gz', '', 'invalid', 'verified', 1)`, "scan archive matching evidence"},
		{"archive-digest", `CREATE TABLE usage_archive_segments (run_id TEXT, file_name TEXT, content_sha256 TEXT, compressed_bytes INTEGER, status TEXT, verified_at_ms INTEGER);
			INSERT INTO usage_archive_segments VALUES ('00000000000000000000000000000001', '00000000000000000000000000000001/segment.gz', 'invalid', 1, 'verified', 1)`, "invalid archive segment digest"},
		{"archive-run-query", `CREATE TABLE usage_archive_runs (id TEXT)`, "query archive run manifest evidence"},
		{"maintenance-lock-query", `CREATE TABLE usage_maintenance_locks (name TEXT)`, "query archive maintenance locks"},
		{"maintenance-lock-view", `CREATE VIEW usage_maintenance_locks AS SELECT 'usage_archive' AS name`, "not a table"},
		{"maintenance-lock-scan", `CREATE TABLE usage_maintenance_locks (name TEXT, run_id TEXT, operation TEXT, acquired_at_ms INTEGER, updated_at_ms INTEGER);
			INSERT INTO usage_maintenance_locks VALUES ('usage_archive', NULL, 'archiving', 1, 1)`, "scan archive maintenance locks"},
		{"foreign-key-schema", `CREATE TABLE fk_parent (id TEXT);
			CREATE TABLE fk_child (parent_id TEXT REFERENCES fk_parent(id));
			INSERT INTO fk_child VALUES ('orphan')`, "foreign_key_check"},
		{"archive-run-view", `CREATE VIEW usage_archive_runs AS SELECT '00000000000000000000000000000001' AS id`, "not a table"},
		{"archive-run-scan", `CREATE TABLE usage_archive_runs (id TEXT, status TEXT, resume_status TEXT, manifest_file TEXT, manifest_sha256 TEXT,
			deleted_event_count INTEGER, last_deleted_event_id INTEGER, delete_started_at_ms INTEGER, completed_at_ms INTEGER,
			archive_digest TEXT, archived_at_ms INTEGER, verified_at_ms INTEGER,
			mode TEXT, schema_version INTEGER, format TEXT, requested_stage TEXT);
			INSERT INTO usage_archive_runs VALUES (NULL, 'completed', NULL, '00000000000000000000000000000001/manifest.json', 'invalid', 0, 0, NULL, NULL, NULL, NULL, NULL, 'manual', 1, 'gzip-jsonl-v1', NULL)`, "scan archive run manifest evidence"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, root, lock := fixture(t)
			bundle, _ := mustCreate(t, l, root, lock)
			db, err := sql.Open("sqlite", filepath.Join(bundle, "database"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			rewriteManifest(t, bundle)
			dest := filepath.Join(root, "restored")
			if _, err := RestoreNew(context.Background(), bundle, dest); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("inspection must fail closed with %q: %v", tc.want, err)
			}
			noPublished(t, root, dest)
		})
	}
}

func TestMatchingEvidenceMissingKeyDoesNotCreateReplacement(t *testing.T) {
	l, _, _ := fixture(t)
	if err := os.Remove(l.DataKeyPath); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMatchingEvidence(context.Background(), l); err == nil {
		t.Fatal("missing key must fail matching inspection")
	}
	if _, err := os.Lstat(l.DataKeyPath); !os.IsNotExist(err) {
		t.Fatalf("inspection generated a replacement data key: %v", err)
	}
}

func TestLegacyPlaintextConnectionStorageRemainsReadable(t *testing.T) {
	l, root, lock := fixture(t)
	db, err := managersqlite.Open(l.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO settings (key, value, updated_at_ms)
		VALUES ('setup', '{"managementKey":"legacy-plaintext"}', 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	bundle, _ := mustCreate(t, l, root, lock)
	if _, err := RestoreNew(context.Background(), bundle, filepath.Join(root, "restored")); err != nil {
		t.Fatalf("pre-migration legacy plaintext must retain existing guard semantics: %v", err)
	}
}

func TestRestoreCancellationDuringStaging(t *testing.T) {
	l, root, lock := fixture(t)
	bundle, _ := mustCreate(t, l, root, lock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dest := filepath.Join(root, "restored")
	if _, err := restoreNew(ctx, bundle, dest, createHooks{beforeRevalidate: cancel}); !errors.Is(err, context.Canceled) {
		t.Fatalf("restore must return cancellation: %v", err)
	}
	noPublished(t, root, dest)
}

func TestManifestVersionAndRequiredRevision(t *testing.T) {
	for _, format := range []string{"legacy-v1", "v1-with-revision", "future-version", "v2-without-revision"} {
		t.Run(format, func(t *testing.T) {
			l, root, lock := fixture(t)
			bundle, m := mustCreate(t, l, root, lock)
			if m.Version != 2 || m.SourceRevision == "" {
				t.Fatal("new bundles must use format v2 with a source revision")
			}
			switch format {
			case "legacy-v1":
				m.Version, m.SourceRevision = 1, ""
			case "v1-with-revision":
				m.Version = 1
			case "future-version":
				m.Version = Version + 1
			case "v2-without-revision":
				m.SourceRevision = ""
			}
			if err := RevalidateSource(context.Background(), l, m); err == nil {
				t.Fatal("unsupported source authorization accepted")
			}
			data, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			put(t, filepath.Join(bundle, "manifest.json"), data)
			if _, err := Verify(context.Background(), bundle); err == nil || !strings.Contains(err.Error(), "unsupported") {
				t.Fatalf("unsupported format must be explicitly rejected: %v", err)
			}
			dest := filepath.Join(root, "restored")
			if _, err := RestoreNew(context.Background(), bundle, dest); err == nil {
				t.Fatal("unsupported format restored")
			}
			noPublished(t, root, dest)
		})
	}
}

func TestRestoreWrongConfig(t *testing.T) {
	l, root, lock := fixture(t)
	bundle, _ := mustCreate(t, l, root, lock)
	put(t, filepath.Join(bundle, "config"), []byte("{invalid-json-content"))

	tamperedManifest, err := Inspect(context.Background(), BundleLayout(bundle))
	if err != nil {
		t.Fatal(err)
	}
	tamperedManifest.Fence = CooperatingFence
	data, err := json.MarshalIndent(tamperedManifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(bundle, "manifest.json"), append(data, '\n'))

	dest := filepath.Join(root, "restored")
	if _, err := RestoreNew(context.Background(), bundle, dest); err == nil {
		t.Fatal("wrong config accepted during restore")
	}
	noPublished(t, root, dest)
}

func TestVerifyPartialBundle(t *testing.T) {
	l, root, lock := fixture(t)
	bundle, m := mustCreate(t, l, root, lock)

	// 1. Missing manifest.json
	partialRoot := filepath.Join(root, "partial-no-manifest")
	if err := os.Mkdir(partialRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(partialRoot, "database"), []byte("some-data"))
	if _, err := Verify(context.Background(), partialRoot); err == nil {
		t.Fatal("missing manifest accepted")
	}

	// 2. Extra unexpected file in bundle root
	extraFile := filepath.Join(bundle, "unexpected.txt")
	put(t, extraFile, []byte("extra"))
	if _, err := Verify(context.Background(), bundle); err == nil {
		t.Fatal("extra file in bundle accepted")
	}
	_ = os.Remove(extraFile)

	// 3. Unpublished temporary directory in bundle root
	unpubDir := filepath.Join(bundle, ".manager-bundle-unpublished-12345")
	if err := os.Mkdir(unpubDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), bundle); err == nil {
		t.Fatal("unpublished temporary entry accepted")
	}
	_ = os.RemoveAll(unpubDir)

	// 4. Missing required entry in manifest
	corruptManifest := m
	corruptManifest.SourceRevision = "tampered-revision"
	data, err := json.MarshalIndent(corruptManifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(bundle, "manifest.json"), append(data, '\n'))
	if _, err := Verify(context.Background(), bundle); err == nil {
		t.Fatal("manifest source revision mismatch accepted")
	}
}

func TestPartialOutputDirectoryCannotBeRestoredOrCreated(t *testing.T) {
	l, root, lock := fixture(t)
	bundle, _ := mustCreate(t, l, root, lock)

	existingDest := filepath.Join(root, "existing-dest")
	if err := os.Mkdir(existingDest, 0o700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(existingDest, "leftover.file"), []byte("leftover"))

	// Create into existing destination fails
	if _, err := Create(context.Background(), l, existingDest, lock); err == nil {
		t.Fatal("create into existing destination accepted")
	}

	// RestoreNew into existing destination fails
	if _, err := RestoreNew(context.Background(), bundle, existingDest); err == nil {
		t.Fatal("restore into existing destination accepted")
	}
}

func TestSourceChangedSinceAuthorization(t *testing.T) {
	l, root, lock := fixture(t)
	bundle, m := mustCreate(t, l, root, lock)

	// Before any mutation, RevalidateSource succeeds
	if err := RevalidateSource(context.Background(), l, m); err != nil {
		t.Fatalf("revalidate source before mutation: %v", err)
	}

	// Mutate source database by inserting a row
	db, err := sql.Open("sqlite", l.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO test_table VALUES (2, 'new-row');"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	// RevalidateSource MUST fail now because source changed
	if err := RevalidateSource(context.Background(), l, m); err == nil {
		t.Fatal("revalidate source succeeded after source database was mutated")
	}

	// Restore of the old bundle still works to a clean new root, but source is marked changed
	dest := filepath.Join(root, "restored-safe")
	restored, err := RestoreNew(context.Background(), bundle, dest)
	if err != nil {
		t.Fatalf("restore new failed: %v", err)
	}
	// Restored DB has the initial state, NOT the mutated row
	rdb, err := sql.Open("sqlite", restored.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	var count int
	if err := rdb.QueryRow("SELECT count(*) FROM test_table").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("restored count %d != 1", count)
	}
}

func TestCreateAndRestoreWithScratchPreserveReadOnlySourceWhenTMPDIRPointsToSource(t *testing.T) {
	l, _, _ := fixture(t)
	// fixture(t) already holds a lock via t.Cleanup; reuse a dedicated source root
	// with a read-only lock so chmod 0o555 reflects a read-only legacy mount.
	roRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	roLayout := Layout{
		DatabasePath:    filepath.Join(roRoot, "usage.sqlite"),
		DataKeyPath:     filepath.Join(roRoot, "data.key"),
		ConfigPath:      filepath.Join(roRoot, "manager.json"),
		ArchivesPath:    filepath.Join(roRoot, "usage-archives"),
		RequireConfig:   true,
		RequireArchives: true,
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if data, readErr := os.ReadFile(l.DatabasePath + suffix); readErr == nil {
			put(t, roLayout.DatabasePath+suffix, data)
		}
	}
	keyBytes, err := os.ReadFile(l.DataKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	put(t, roLayout.DataKeyPath, keyBytes)
	put(t, roLayout.ConfigPath, []byte(`{"dbPath":"`+filepath.ToSlash(roLayout.DatabasePath)+`"}`))
	if err := os.MkdirAll(filepath.Join(roLayout.ArchivesPath, "2026", "10"), 0o700); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(roLayout.ArchivesPath, "2026", "10", "history.jsonl.gz"), []byte("opaque archive bytes\x00\xff"))
	seedLock, err := processlock.Acquire(roLayout.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := seedLock.Close(); err != nil {
		t.Fatal(err)
	}
	heldReadOnly, err := processlock.AcquireExistingReadOnly(roLayout.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = heldReadOnly.Close() }()

	beforeEntries, err := os.ReadDir(roRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", roRoot)
	t.Setenv("TMP", roRoot)
	t.Setenv("TEMP", roRoot)
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		if err := os.Chmod(roRoot, 0o555); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(roRoot, 0o700) })
	}

	workBase, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	scratchDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bundleDir := filepath.Join(workBase, "bundle")
	restoredDir := filepath.Join(workBase, "restored")

	if err := ValidateMatchingEvidenceWithScratch(context.Background(), roLayout, scratchDir); err != nil {
		t.Fatalf("ValidateMatchingEvidenceWithScratch failed: %v", err)
	}
	if _, err := CreateWithScratch(context.Background(), roLayout, bundleDir, heldReadOnly, scratchDir); err != nil {
		t.Fatalf("CreateWithScratch failed: %v", err)
	}
	if _, err := RestoreNewWithScratch(context.Background(), bundleDir, restoredDir, scratchDir); err != nil {
		t.Fatalf("RestoreNewWithScratch failed: %v", err)
	}

	afterEntries, err := os.ReadDir(roRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterEntries) != len(beforeEntries) {
		t.Fatalf("source root entry count changed: before=%d after=%d", len(beforeEntries), len(afterEntries))
	}
	for i := range beforeEntries {
		if afterEntries[i].Name() != beforeEntries[i].Name() {
			t.Fatalf("source root entry[%d] = %q, want %q", i, afterEntries[i].Name(), beforeEntries[i].Name())
		}
	}
	scratchLeftovers, err := os.ReadDir(scratchDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(scratchLeftovers) != 0 {
		t.Fatalf("scratchDir retained temporary entries: %v", scratchLeftovers)
	}
}
