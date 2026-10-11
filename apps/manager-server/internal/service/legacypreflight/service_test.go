package legacypreflight

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
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
		if info.Mode().IsRegular() {
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

func TestStartupRejectsLegacyUnknownAndPartialWithoutSourceWrites(t *testing.T) {
	for _, kind := range []string{"legacy", "single-gateway-table", "unknown", "orphan-wal", "key-without-db", "config-without-db", "corrupt", "empty"} {
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
			case "corrupt":
				if err := os.WriteFile(paths.Database, []byte("owned-corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := os.WriteFile(paths.Database, nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := sourceState(t, filepath.Dir(paths.Database))
			lock, err := AdmitStartup(context.Background(), paths, Options{})
			if err == nil || lock != nil {
				if lock != nil {
					_ = lock.Close()
				}
				t.Fatal("unsafe source admitted")
			}
			if !reflect.DeepEqual(before, sourceState(t, filepath.Dir(paths.Database))) {
				t.Fatal("blocked startup changed source or created lock/config/key")
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

func TestFreshProvisioningConfigAndSourceConfigAreDistinct(t *testing.T) {
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
	lock, err = AdmitStartup(context.Background(), paths, Options{ProvisioningConfig: true})
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
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
