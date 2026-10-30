package legacypreflight

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func sourceState(t *testing.T, root string) map[string]string {
	t.Helper()
	state := map[string]string{}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		var digest [32]byte
		// Empty files have no bytes to read; Windows locks the marker's first
		// byte even though the marker is empty. Still compare all metadata.
		if info.Mode().IsRegular() && info.Size() > 0 {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			digest = sha256.Sum256(data)
		}
		state[path] = fmt.Sprintf("%s/%s/%x", info.Mode(), info.ModTime(), digest)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return state
}

func fixturePaths(t *testing.T) sqlite.InspectionPaths {
	dir := t.TempDir()
	paths := DefaultPaths(filepath.Join(dir, "usage.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	return paths
}

func TestFencedInspectionRetainsOwnershipThroughConsumerAndReleasesAfterward(t *testing.T) {
	paths := fixturePaths(t)
	db, err := sql.Open("sqlite", paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table settings(key text primary key,value text);create table usage_events(id integer primary key)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	marker, err := processlock.Acquire(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := marker.Close(); err != nil {
		t.Fatal(err)
	}
	before := sourceState(t, filepath.Dir(paths.Database))
	called := false
	WithFencedInspection(context.Background(), paths, Options{}, func(report Report, err error) {
		called = true
		if err != nil || report.SourceKind != "legacy-manager" || report.Observed.Consistency != "sqlite-read-snapshot" {
			t.Fatalf("fenced inspection: kind=%s consistency=%s err=%v", report.SourceKind, report.Observed.Consistency, err)
		}
		other, err := processlock.AcquireExisting(paths.Database)
		if other != nil {
			_ = other.Close()
		}
		if !errors.Is(err, processlock.ErrLocked) {
			t.Fatalf("consumer lost its fence: %v", err)
		}
	})
	if !called {
		t.Fatal("observation was not consumed")
	}
	other, err := processlock.AcquireExisting(paths.Database)
	if err != nil {
		t.Fatalf("consumer retained the fence after returning: %v", err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
		t.Fatal("request-scoped inspection modified the source")
	}
}

func TestFencedInspectionObservesRollbackLegacyWithoutPersistentMarker(t *testing.T) {
	paths := fixturePaths(t)
	db, err := sql.Open("sqlite", paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`pragma journal_mode = delete; create table settings(key text primary key,value text);create table usage_events(id integer primary key)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := sourceState(t, filepath.Dir(paths.Database))
	WithFencedInspection(context.Background(), paths, Options{}, func(report Report, err error) {
		if err != nil || report.SourceKind != "legacy-manager" || report.Observed.Consistency != "sqlite-read-snapshot" {
			t.Fatalf("rollback legacy discovery: kind=%s consistency=%s err=%v", report.SourceKind, report.Observed.Consistency, err)
		}
	})
	if _, err := os.Lstat(paths.Database + ".manager.lock"); !os.IsNotExist(err) {
		t.Fatalf("discovery created Manager marker: %v", err)
	}
	if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
		t.Fatal("rollback read-only discovery modified source")
	}
}

func TestFencedInspectionRejectsCleanWALWithoutCoordinationSidecars(t *testing.T) {
	paths := fixturePaths(t)
	db, err := sql.Open("sqlite", paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`pragma journal_mode = wal; create table settings(key text primary key,value text);create table usage_events(id integer primary key)`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, sidecar := range []string{paths.Database + "-wal", paths.Database + "-shm"} {
		if err := os.Remove(sidecar); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	before := sourceState(t, filepath.Dir(paths.Database))
	WithFencedInspection(context.Background(), paths, Options{}, func(report Report, err error) {
		if err == nil || report.SourceKind != "unknown" || report.UpgradeAuthorized {
			t.Fatalf("clean WAL without coordination did not fail closed: kind=%s authorized=%v err=%v", report.SourceKind, report.UpgradeAuthorized, err)
		}
	})
	if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
		t.Fatal("failed WAL snapshot attempt modified source")
	}
}

func TestFencedInspectionRejectsActiveWALAsRecoveryOwned(t *testing.T) {
	paths := fixturePaths(t)
	db, err := sql.Open("sqlite", paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`pragma journal_mode = wal; pragma wal_autocheckpoint = 0; create table settings(key text primary key,value text);create table usage_events(id integer primary key)`); err != nil {
		t.Fatal(err)
	}
	walBefore, err := os.ReadFile(paths.Database + "-wal")
	if err != nil || len(walBefore) == 0 {
		t.Fatalf("WAL fixture unavailable: len=%d err=%v", len(walBefore), err)
	}
	dbBefore, err := os.ReadFile(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	WithFencedInspection(context.Background(), paths, Options{}, func(report Report, err error) {
		if err == nil || report.SourceKind != "unknown" || report.UpgradeAuthorized {
			t.Fatalf("active WAL was not kept recovery-owned: kind=%s authorized=%v err=%v", report.SourceKind, report.UpgradeAuthorized, err)
		}
	})
	if _, err := os.Lstat(paths.Database + ".manager.lock"); !os.IsNotExist(err) {
		t.Fatalf("WAL rejection created Manager marker: %v", err)
	}
	dbAfter, err := os.ReadFile(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	walAfter, err := os.ReadFile(paths.Database + "-wal")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dbBefore, dbAfter) || !reflect.DeepEqual(walBefore, walAfter) {
		t.Fatal("WAL rejection modified database or WAL content")
	}
}

func TestFencedInspectionAllowsFreshInventoryAndRejectsBusyOrCanceledFence(t *testing.T) {
	freshPaths := fixturePaths(t)
	WithFencedInspection(context.Background(), freshPaths, Options{}, func(report Report, err error) {
		if err != nil || report.SourceKind != "fresh" {
			t.Fatalf("fresh public inspection: kind=%s err=%v", report.SourceKind, err)
		}
	})
	for _, kind := range []string{"busy", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			paths := fixturePaths(t)
			if kind == "busy" {
				db, err := sql.Open("sqlite", paths.Database)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`create table settings(key text primary key,value text);create table usage_events(id integer primary key)`); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				lock, err := processlock.Acquire(paths.Database)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "canceled" {
				cancel()
			}
			before := sourceState(t, filepath.Dir(paths.Database))
			calls := 0
			WithFencedInspection(ctx, paths, Options{SourceStopped: true}, func(report Report, err error) {
				calls++
				if err == nil || report.SourceKind != "" {
					t.Fatalf("unfenced request received evidence: kind=%s err=%v", report.SourceKind, err)
				}
			})
			if calls != 1 || !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
				t.Fatal("unavailable inspection changed the source or skipped its consumer")
			}
		})
	}
}

func TestFencedInspectionLeavesWALRecoveryToLaterOwner(t *testing.T) {
	paths := fixturePaths(t)
	db, err := sql.Open("sqlite", paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`create table settings(key text primary key,value text);create table usage_events(id integer primary key)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Database+"-wal", []byte("requires-recovery"), 0600); err != nil {
		t.Fatal(err)
	}
	marker, err := processlock.Acquire(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := marker.Close(); err != nil {
		t.Fatal(err)
	}
	before := sourceState(t, filepath.Dir(paths.Database))
	WithFencedInspection(context.Background(), paths, Options{}, func(report Report, err error) {
		if err == nil || report.SourceKind != "unknown" || report.UpgradeAuthorized {
			t.Fatalf("public WAL inspection did not fail closed: kind=%s authorized=%v err=%v", report.SourceKind, report.UpgradeAuthorized, err)
		}
	})
	if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
		t.Fatal("public WAL rejection modified the source")
	}
}

// Cancel only the fenced second inspection, after exclusive marker creation.
type cancelFencedInspection struct {
	context.Context
	checks int
}

func (c *cancelFencedInspection) Err() error {
	c.checks++
	if c.checks >= 2 {
		return context.Canceled
	}
	return nil
}

func TestFreshAdmissionCancellationAllowsCleanRetry(t *testing.T) {
	for _, initialize := range []bool{false, true} {
		t.Run(fmt.Sprintf("initialize=%v", initialize), func(t *testing.T) {
			paths := fixturePaths(t)
			if initialize {
				if err := os.WriteFile(paths.Config, []byte(`{"dataDir":"."}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			options := Options{Initialize: initialize}
			ctx := &cancelFencedInspection{Context: context.Background()}
			admission, err := AdmitStartup(ctx, paths, options)
			if admission != nil || !errors.Is(err, context.Canceled) || ctx.checks < 2 {
				t.Fatalf("fenced cancellation: admission=%v checks=%d err=%v", admission, ctx.checks, err)
			}
			for _, path := range []string{paths.Database, paths.DataKey} {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("failed admission left source asset %s: %v", path, err)
				}
			}
			retry, err := AdmitStartup(context.Background(), paths, options)
			if err != nil {
				t.Fatalf("clean retry blocked after canceled fresh admission: %v", err)
			}
			defer retry.Close()
		})
	}
}

func TestFreshAdmissionClosePreservesPartialSource(t *testing.T) {
	for _, asset := range []string{"database", "data-key", "wal", "archives", "config"} {
		t.Run(asset, func(t *testing.T) {
			paths := fixturePaths(t)
			admission, err := AdmitStartup(context.Background(), paths, Options{})
			if err != nil {
				t.Fatal(err)
			}
			path := map[string]string{"database": paths.Database, "data-key": paths.DataKey,
				"wal": paths.Database + "-wal", "archives": paths.Archives, "config": paths.Config}[asset]
			if asset == "archives" {
				err = os.Mkdir(path, 0700)
			} else {
				err = os.WriteFile(path, []byte("partial startup evidence"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := sourceState(t, filepath.Dir(paths.Database))
			if err := admission.Close(); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
				t.Fatal("failed initialization cleanup changed source evidence")
			}
			for _, initialize := range []bool{false, true} {
				if retry, err := AdmitStartup(context.Background(), paths, Options{Initialize: initialize}); err == nil || retry != nil {
					if retry != nil {
						_ = retry.Close()
					}
					t.Fatal("partial initialization admitted on retry")
				}
			}
		})
	}
}

func TestFreshAdmissionPreservesMarkerAfterInitializedDatabaseLoss(t *testing.T) {
	paths := fixturePaths(t)
	admission, err := AdmitStartup(context.Background(), paths, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Database, []byte("initialized database"), 0600); err != nil {
		t.Fatal(err)
	}
	admission.RetainMarker()
	if err := os.Remove(paths.Database); err != nil {
		t.Fatal(err)
	}
	before := sourceState(t, filepath.Dir(paths.Database))
	if err := admission.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
		t.Fatal("shutdown discarded established ownership after database loss")
	}
	for _, initialize := range []bool{false, true} {
		if retry, err := AdmitStartup(context.Background(), paths, Options{Initialize: initialize}); retry != nil || err == nil {
			if retry != nil {
				_ = retry.Close()
			}
			t.Fatal("lost initialized database readmitted as fresh")
		}
	}
}

func TestFreshAdmissionRejectsInterposedOwnershipMarker(t *testing.T) {
	// The child creates and releases ownership after the parent's first
	// inspection, deterministically opening the reported admission window.
	if path := os.Getenv("CPAMP_TEST_FRESH_ADMISSION_LOCK_CONTENDER"); path != "" {
		lock, err := processlock.Acquire(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	for _, initialize := range []bool{false, true} {
		t.Run(fmt.Sprintf("initialize=%v", initialize), func(t *testing.T) {
			paths := fixturePaths(t)
			if initialize {
				if err := os.WriteFile(paths.Config, []byte(`{"dataDir":"."}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			options := Options{SourceStopped: true, Initialize: initialize}
			before, err := inspect(context.Background(), paths, options, nil, false)
			if err != nil || before.SourceKind != "fresh" || !startupAllowed(before, UpgradeStageNone) {
				t.Fatalf("first inspection was not fresh: report=%+v err=%v", before, err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestFreshAdmissionRejectsInterposedOwnershipMarker$")
			cmd.Env = append(os.Environ(), "CPAMP_TEST_FRESH_ADMISSION_LOCK_CONTENDER="+paths.Database)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("interposed owner: %v\n%s", err, output)
			}
			state := sourceState(t, filepath.Dir(paths.Database))
			lock, err := acquireStartupLock(paths.Database, before.SourceKind == "fresh")
			if lock != nil {
				_ = lock.Close()
				t.Fatal("fresh admission reused a marker created by another process")
			}
			if !errors.Is(err, os.ErrExist) {
				t.Fatalf("interposed marker did not fail exclusive creation: %v", err)
			}
			if !reflect.DeepEqual(state, sourceState(t, filepath.Dir(paths.Database))) {
				t.Fatal("failed exclusive admission changed source")
			}
			admission, err := AdmitStartup(context.Background(), paths, options)
			if err == nil || admission != nil {
				if admission != nil {
					_ = admission.Close()
				}
				t.Fatal("complete reclassification admitted the interposed old marker")
			}
		})
	}
}

func TestStartupRejectsLegacyUnknownAndPartialWithoutSourceWrites(t *testing.T) {
	for _, kind := range []string{"legacy", "single-gateway-table", "unknown", "orphan-wal", "key-without-db", "config-without-db", "process-lock-without-db", "corrupt", "empty"} {
		t.Run(kind, func(t *testing.T) {
			paths := fixturePaths(t)
			switch kind {
			case "legacy", "single-gateway-table", "unknown":
				db, err := sql.Open("sqlite", paths.Database)
				if err != nil {
					t.Fatal(err)
				}
				query := `create table settings(key text primary key,value text); create table usage_events(id integer primary key)`
				if kind == "single-gateway-table" {
					query += `; create table gateway_api_key_identities(id text primary key,revision integer,lifecycle text)`
				}
				if kind == "unknown" {
					query = `create table unrelated(id integer)`
				}
				if _, err := db.Exec(query); err != nil {
					t.Fatal(err)
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			case "orphan-wal":
				if err := os.WriteFile(paths.Database+"-wal", []byte("owned-orphan"), 0600); err != nil {
					t.Fatal(err)
				}
			case "key-without-db":
				if err := os.WriteFile(paths.DataKey, []byte("owned-key"), 0600); err != nil {
					t.Fatal(err)
				}
			case "config-without-db":
				if err := os.WriteFile(paths.Config, []byte(`{"cpaUpstreamUrl":"https://owned.invalid"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "process-lock-without-db":
				lock, err := processlock.Acquire(paths.Database)
				if err != nil {
					t.Fatal(err)
				}
				if err := lock.Close(); err != nil {
					t.Fatal(err)
				}
			case "corrupt":
				if err := os.WriteFile(paths.Database, []byte("owned-corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := os.WriteFile(paths.Database, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "process-lock-without-db" {
				report, err := Inspect(context.Background(), paths, Options{SourceStopped: true})
				if err != nil || report.SourceKind != "partial-source" || startupAllowed(report, UpgradeStageNone) || report.UpgradeAuthorized {
					t.Fatalf("old ownership marker treated as fresh: report=%+v err=%v", report, err)
				}
			}
			for _, initialize := range []bool{false, true} {
				if kind == "config-without-db" && initialize {
					continue
				}
				before := sourceState(t, filepath.Dir(paths.Database))
				lock, err := AdmitStartup(context.Background(), paths, Options{Initialize: initialize})
				if err == nil || lock != nil {
					if lock != nil {
						_ = lock.Close()
					}
					t.Fatalf("unsafe source admitted, initialize=%v", initialize)
				}
				if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
					t.Fatal("blocked startup changed source or created lock/config/key")
				}
			}
		})
	}
}

func TestNativeAdmissionRetainsFenceAndDoesNotMigrateDuringInspection(t *testing.T) {
	paths := fixturePaths(t)
	db, err := sqlite.Open(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	report, err := Inspect(context.Background(), paths, Options{SourceStopped: true})
	if err != nil || report.SourceKind != "native-v2" || report.UpgradeAuthorized {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	if report.CompatibilityProfile == nil || report.CompatibilityProfile.Status != "not-applicable" || report.BridgeReadiness.Status != "unknown" {
		t.Fatal("legacy profile rules changed native admission or claimed bridge readiness")
	}
	lock, err := AdmitStartup(context.Background(), paths, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	contender, err := processlock.Acquire(paths.Database)
	if !errors.Is(err, processlock.ErrLocked) || contender != nil {
		if contender != nil {
			_ = contender.Close()
		}
		t.Fatalf("ownership fence lost: %v", err)
	}
	after, err := os.ReadFile(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(data, after) {
		t.Fatal("admission migrated native database")
	}
}

func TestNativeSchemaWithoutCanonicalPrimaryKeyCannotEnterMigration(t *testing.T) {
	paths := fixturePaths(t)
	db, err := sqlite.Open(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	db, err = sql.Open("sqlite", paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`drop table gateway_api_key_identities; create table gateway_api_key_identities(id text,revision integer,lifecycle text,created_at_ms integer,updated_at_ms integer)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	before := sourceState(t, filepath.Dir(paths.Database))
	lock, err := AdmitStartup(context.Background(), paths, Options{})
	if err == nil || lock != nil {
		t.Fatal("partial native schema admitted")
	}
	if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
		t.Fatal("partial source was mutated")
	}
}

func TestConfigOnlyStartupRequiresExplicitInitialization(t *testing.T) {
	paths := fixturePaths(t)
	if err := os.WriteFile(paths.Config, []byte(`{"dataDir":"./data"}`), 0600); err != nil {
		t.Fatal(err)
	}
	before := sourceState(t, filepath.Dir(paths.Database))
	lock, err := AdmitStartup(context.Background(), paths, Options{})
	if err == nil || lock != nil {
		t.Fatal("unclassified config bypassed fresh gate")
	}
	if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
		t.Fatal("unclassified source changed")
	}
	report, err := Inspect(context.Background(), paths, Options{})
	if err != nil || report.SourceKind != "partial-source" || startupAllowed(report, UpgradeStageNone) {
		t.Fatalf("existing config is not fresh evidence: report=%+v err=%v", report, err)
	}
	lock, err = AdmitStartup(context.Background(), paths, Options{Initialize: true})
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
}

func TestInitializationCannotBypassSourceArtifacts(t *testing.T) {
	for _, role := range []string{"database", "wal", "shm", "journal", "data-key", "archives", "process-lock"} {
		t.Run(role, func(t *testing.T) {
			paths := fixturePaths(t)
			if err := os.WriteFile(paths.Config, []byte(`{"dataDir":"."}`), 0600); err != nil {
				t.Fatal(err)
			}
			path := map[string]string{"database": paths.Database, "wal": paths.Database + "-wal", "shm": paths.Database + "-shm", "journal": paths.Database + "-journal", "data-key": paths.DataKey, "archives": paths.Archives, "process-lock": paths.Database + ".manager.lock"}[role]
			if role == "archives" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(path, []byte("owned-source"), 0600); err != nil {
				t.Fatal(err)
			}
			before := sourceState(t, filepath.Dir(paths.Database))
			lock, err := AdmitStartup(context.Background(), paths, Options{Initialize: true})
			if lock != nil {
				_ = lock.Close()
				t.Fatal("initialization bypassed source artifacts")
			}
			if err == nil {
				t.Fatal("initialization accepted existing source artifacts")
			}
			if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
				t.Fatal("blocked initialization changed source")
			}
		})
	}
}

func TestInspectNeverClaimsExactReleaseOrBackupAndKeepsMissingDistinct(t *testing.T) {
	paths := fixturePaths(t)
	report, err := Inspect(context.Background(), paths, Options{})
	if err != nil || report.SourceKind != "fresh" || report.UpgradeAuthorized {
		t.Fatalf("fresh report=%+v err=%v", report, err)
	}
	paths.Config = ""
	report, err = Inspect(context.Background(), paths, Options{})
	if err != nil || report.SourceKind != "missing-database" {
		t.Fatalf("incomplete inventory=%+v err=%v", report, err)
	}
	if len(report.Unknown) < 5 {
		t.Fatal("unperformed checks disappeared")
	}
}
