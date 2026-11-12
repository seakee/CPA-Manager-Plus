package legacypreflight

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
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

func TestAdmitStartupRefusesHardLinkedDatabaseAndSidecarsWithoutWAL(t *testing.T) {
	for _, role := range []string{"database", "wal", "shm", "journal"} {
		t.Run(role, func(t *testing.T) {
			paths := fixturePaths(t)
			db, err := sqlite.Open(paths.Database)
			if err != nil {
				t.Fatalf("create native v2 store: %v", err)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close native v2 store: %v", err)
			}
			// Ensure clean no-WAL / 0-byte sidecar path.
			for _, suffix := range []string{"-wal", "-shm", "-journal"} {
				_ = os.Remove(paths.Database + suffix)
			}
			lock, err := processlock.Acquire(paths.Database)
			if err != nil {
				t.Fatalf("create lock marker: %v", err)
			}
			if err := lock.Close(); err != nil {
				t.Fatalf("close lock marker: %v", err)
			}
			lockPath := paths.Database + ".manager.lock"
			if err := os.Chmod(lockPath, 0o644); err != nil {
				t.Fatalf("chmod lock 0644: %v", err)
			}

			target := paths.Database
			if role != "database" {
				suffix := "-" + role
				target = paths.Database + suffix
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatalf("create empty %s: %v", suffix, err)
				}
			}
			secondLink := filepath.Join(t.TempDir(), "hardlink-"+role)
			if err := os.Link(target, secondLink); err != nil {
				t.Fatalf("link %s: %v", role, err)
			}
			before := sourceState(t, filepath.Dir(paths.Database))

			admission, err := AdmitStartup(context.Background(), paths, Options{
				UpgradeAdmission: DatabaseUpgradeAdmission{DatabasePath: paths.Database},
			})
			if err == nil || admission != nil {
				if admission != nil {
					_ = admission.Close()
				}
				t.Fatalf("AdmitStartup accepted hard-linked %s without WAL", role)
			}
			if runtime.GOOS != "windows" {
				info, statErr := os.Lstat(lockPath)
				if statErr != nil || info.Mode().Perm() != 0o644 {
					t.Fatalf("refused admission altered marker permissions: info=%v err=%v", info, statErr)
				}
			}
			if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
				t.Fatalf("refused admission mutated source directory")
			}
		})
	}
}

func TestPendingFreshAdmissionTightensMarkerOnlyAfterValidation(t *testing.T) {
	paths := fixturePaths(t)
	initial, err := AdmitStartup(context.Background(), paths, Options{FreshAdmission: true})
	if err != nil {
		t.Fatalf("initial FreshAdmission: %v", err)
	}
	if initial.PendingFresh == nil {
		_ = initial.Close()
		t.Fatal("initial FreshAdmission produced no receipt")
	}
	if err := initial.Close(); err != nil {
		t.Fatalf("close initial FreshAdmission: %v", err)
	}

	lockPath := paths.Database + ".manager.lock"
	if err := os.Chmod(lockPath, 0o644); err != nil {
		t.Fatalf("relax marker mode: %v", err)
	}

	// Validation failure must leave the relaxed marker untouched.
	if err := os.Mkdir(paths.Archives, 0o700); err != nil {
		t.Fatalf("create conflicting archives dir: %v", err)
	}
	if refused, err := AdmitStartup(context.Background(), paths, Options{FreshAdmission: true}); err == nil || refused != nil {
		if refused != nil {
			_ = refused.Close()
		}
		t.Fatal("PendingFreshAdmission succeeded with conflicting archives directory")
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Lstat(lockPath); err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("refused PendingFreshAdmission altered marker mode: info=%v err=%v", info, err)
		}
	}
	if err := os.Remove(paths.Archives); err != nil {
		t.Fatalf("remove conflicting archives dir: %v", err)
	}

	resumed, err := AdmitStartup(context.Background(), paths, Options{FreshAdmission: true})
	if err != nil {
		t.Fatalf("resume PendingFreshAdmission: %v", err)
	}
	defer func() { _ = resumed.Close() }()
	if resumed.PendingFresh == nil {
		t.Fatal("resumed admission lost PendingFresh receipt")
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Lstat(lockPath); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("resumed PendingFreshAdmission marker mode = %v (err=%v), want 0600", info.Mode().Perm(), err)
		}
	}
}

func TestDatabaseUpgradeAdmissionFailsClosedOnZeroLengthDatabaseWithOrphanSidecars(t *testing.T) {
	cleanDB := filepath.Join(t.TempDir(), "usage.sqlite")
	if err := os.WriteFile(cleanDB, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if stage, err := (DatabaseUpgradeAdmission{DatabasePath: cleanDB}).Admitted(context.Background()); err != nil || stage != UpgradeStageNone {
		t.Fatalf("clean zero-length database Admitted = (%q, %v), want (%q, nil)", stage, err, UpgradeStageNone)
	}

	for _, tc := range []struct {
		name   string
		suffix string
		data   []byte
	}{
		{name: "orphan-32kib-shm", suffix: "-shm", data: make([]byte, 32*1024)},
		{name: "orphan-empty-shm", suffix: "-shm", data: nil},
		{name: "orphan-empty-wal", suffix: "-wal", data: nil},
		{name: "orphan-empty-journal", suffix: "-journal", data: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
			if err := os.WriteFile(dbPath, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dbPath+tc.suffix, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			stage, err := (DatabaseUpgradeAdmission{DatabasePath: dbPath}).Admitted(context.Background())
			if err == nil {
				t.Fatalf("%s Admitted succeeded with stage=%q, want error", tc.name, stage)
			}
		})
	}
}

func TestWALOnlyRecordedOperationFixtureHelperProcess(t *testing.T) {
	dbPath := os.Getenv("CPAMP_PREFLIGHT_WAL_ONLY_DB")
	if dbPath == "" {
		return
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA wal_autocheckpoint = 0`,
		`CREATE TABLE upgrade_operations (
			id INTEGER PRIMARY KEY,
			stage TEXT NOT NULL,
			source_kind TEXT NOT NULL DEFAULT '',
			source_fingerprint_before TEXT NOT NULL DEFAULT '',
			source_fingerprint_after TEXT NOT NULL DEFAULT '',
			backup_manifest_sha256 TEXT NOT NULL DEFAULT '',
			domains_done TEXT NOT NULL DEFAULT '[]',
			verification TEXT NOT NULL DEFAULT '{}'
		)`,
		`INSERT INTO upgrade_operations (id, stage, source_kind, source_fingerprint_before, source_fingerprint_after, backup_manifest_sha256)
		 VALUES (1, 'reconcile', 'legacy-manager', 'fp-1', 'fp-1', 'manifest-1')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec %q: %v", stmt, err)
		}
	}
	os.Exit(0)
}

func buildColdDBAndPendingUpgradeWAL(t *testing.T) ([]byte, []byte) {
	t.Helper()
	donorDir := t.TempDir()
	donorDB := filepath.Join(donorDir, "usage.sqlite")
	seed, err := sql.Open("sqlite", donorDB)
	if err != nil {
		t.Fatal(err)
	}
	seed.SetMaxOpenConns(1)
	for _, stmt := range []string{
		`PRAGMA journal_mode = WAL`,
		`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
	} {
		if _, err := seed.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	coldBytes, err := os.ReadFile(donorDB)
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestWALOnlyRecordedOperationFixtureHelperProcess$")
	cmd.Env = append(os.Environ(), "CPAMP_PREFLIGHT_WAL_ONLY_DB="+donorDB)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper process: %v %s", err, out)
	}
	walBytes, err := os.ReadFile(donorDB + "-wal")
	if err != nil || len(walBytes) == 0 {
		t.Fatalf("read pending WAL (len=%d, err=%v)", len(walBytes), err)
	}
	return coldBytes, walBytes
}

func TestRecordedOperationEvidenceDetectsConcurrentJournalAndRewrites(t *testing.T) {
	coldBytes, walBytes := buildColdDBAndPendingUpgradeWAL(t)

	t.Run("wal-only-operation-created-mid-probe", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "usage.sqlite")
		if err := os.WriteFile(dbPath, coldBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		origHook := beforeOperationEvidenceProbe
		beforeOperationEvidenceProbe = func(target string) {
			_ = os.WriteFile(target+"-wal", walBytes, 0o600)
		}
		defer func() { beforeOperationEvidenceProbe = origHook }()

		inFile, journalPending, err := RecordedOperationEvidence(context.Background(), dbPath)
		if err != nil || !journalPending || inFile {
			t.Fatalf("RecordedOperationEvidence = (inFile=%v, journalPending=%v, err=%v), want (false, true, nil)", inFile, journalPending, err)
		}

		// Reset to cold DB and verify DatabaseUpgradeAdmission.Admitted replays the newly created WAL.
		if err := os.Remove(dbPath + "-wal"); err != nil {
			t.Fatal(err)
		}
		stage, err := (DatabaseUpgradeAdmission{DatabasePath: dbPath}).Admitted(context.Background())
		if err != nil || stage != UpgradeStageInProgress {
			t.Fatalf("Admitted with mid-probe WAL-only operation = (%q, %v), want (%q, nil)", stage, err, UpgradeStageInProgress)
		}
		_ = model.UpgradeStageReconcile
	})

	t.Run("corrupt-wal-created-mid-probe", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "usage.sqlite")
		if err := os.WriteFile(dbPath, coldBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		corruptWAL := append([]byte(nil), walBytes...)
		corruptWAL[0] ^= 0xff

		origHook := beforeOperationEvidenceProbe
		beforeOperationEvidenceProbe = func(target string) {
			_ = os.WriteFile(target+"-wal", corruptWAL, 0o600)
		}
		defer func() { beforeOperationEvidenceProbe = origHook }()

		if stage, err := (DatabaseUpgradeAdmission{DatabasePath: dbPath}).Admitted(context.Background()); err == nil {
			t.Fatalf("Admitted succeeded on mid-probe corrupt WAL: stage=%q", stage)
		}
	})

	t.Run("database-rewritten-with-restored-mtime-mid-probe", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "usage.sqlite")
		if err := os.WriteFile(dbPath, coldBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		// The detector compares identity, size, mtime and change stamp, so a
		// rewrite that restores its own bytes and mtime is only detectable
		// through the change stamp. Pin that stamp into the past first: on
		// Windows ChangeTime is file metadata, so every later write reports a
		// strictly later one instead of relying on two writes landing in
		// different clock ticks.
		testutil.AgeChangeStamp(t, dbPath, copyinmarker.ChangeStamp)
		origInfo, err := os.Lstat(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		origID, err := copyinmarker.Identity(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		origStamp, err := copyinmarker.ChangeStamp(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		// The rewrite must change bytes, not merely re-write the ones already
		// there. Mutate the header's user-version field, which SQLite tolerates
		// on read, so the probe still answers normally and only the before/after
		// identity comparison can reject the change.
		const userVersionOffset = 60
		rewritten := append([]byte(nil), coldBytes...)
		rewrittenVersion := binary.BigEndian.Uint32(coldBytes[userVersionOffset:]) ^ 0xa5a5a5a5
		if rewrittenVersion == binary.BigEndian.Uint32(coldBytes[userVersionOffset:]) {
			rewrittenVersion++
		}
		binary.BigEndian.PutUint32(rewritten[userVersionOffset:], rewrittenVersion)

		var hookErr error
		origHook := beforeOperationEvidenceProbe
		beforeOperationEvidenceProbe = func(target string) {
			hookErr = func() error {
				f, err := os.OpenFile(target, os.O_WRONLY, 0)
				if err != nil {
					return err
				}
				if _, err := f.WriteAt(rewritten[userVersionOffset:userVersionOffset+4], userVersionOffset); err != nil {
					_ = f.Close()
					return err
				}
				if err := f.Close(); err != nil {
					return err
				}
				return os.Chtimes(target, origInfo.ModTime(), origInfo.ModTime())
			}()
			if hookErr != nil {
				return
			}
			// Name a broken precondition here rather than reporting it as a
			// detector failure below.
			testutil.RequireChangeStampAdvanced(t, target, origStamp, copyinmarker.ChangeStamp)
		}
		defer func() { beforeOperationEvidenceProbe = origHook }()

		_, _, err = RecordedOperationEvidence(context.Background(), dbPath)
		if hookErr != nil {
			t.Fatalf("apply mid-probe rewrite: %v", hookErr)
		}
		if err == nil {
			t.Fatal("RecordedOperationEvidence succeeded when database changed mid-probe")
		}

		// Prove the fixture exercised the invariant instead of assuming it: the
		// file keeps its identity, size and restored mtime, and only its content
		// and kernel change stamp moved.
		afterInfo, err := os.Lstat(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		afterID, err := copyinmarker.Identity(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if afterID != origID || afterInfo.Size() != origInfo.Size() || afterInfo.ModTime().UnixNano() != origInfo.ModTime().UnixNano() {
			t.Fatalf("fixture did not preserve inode/size/mtime: id %+v->%+v size %d->%d mtime %d->%d",
				origID, afterID, origInfo.Size(), afterInfo.Size(), origInfo.ModTime().UnixNano(), afterInfo.ModTime().UnixNano())
		}
		current, err := os.ReadFile(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(current[userVersionOffset:userVersionOffset+4], coldBytes[userVersionOffset:userVersionOffset+4]) {
			t.Fatal("fixture rewrite left the user-version field unchanged; the mutation never reached the file")
		}
	})
}
