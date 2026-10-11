package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
)

func inspectionSnapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	state := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
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
		state[path] = fmt.Sprintf("%s/%d/%s/%x", info.Mode(), info.Size(), info.ModTime(), digest)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func coldInspectionFixture(t *testing.T) InspectionPaths {
	t.Helper()
	dir := t.TempDir()
	paths := InspectionPaths{Database: filepath.Join(dir, "usage.sqlite"), Config: filepath.Join(dir, "config.json"), DataKey: filepath.Join(dir, "data.key"), Archives: filepath.Join(dir, "usage-archives")}
	db, err := sql.Open("sqlite", paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`create table settings(key text primary key,value text); create table usage_events(id integer primary key);
		create table usage_data_migrations(name text primary key,status text);
		insert into settings values('setup','{"managementKey":"OWNED_SECRET"}');
		insert into usage_data_migrations values('owned','pending')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return paths
}

func TestLegacyInspectionColdAndCancellationPreserveEverySourceFile(t *testing.T) {
	paths := coldInspectionFixture(t)
	for _, test := range []struct {
		name               string
		stopped, cancelled bool
	}{{"cold", true, false}, {"unproven", false, false}, {"cancelled", true, true}} {
		t.Run(test.name, func(t *testing.T) {
			before := inspectionSnapshot(t, filepath.Dir(paths.Database))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if test.cancelled {
				cancel()
			}
			result, err := InspectLegacySource(ctx, paths, InspectionOptions{SourceStopped: test.stopped})
			if test.cancelled && err == nil {
				t.Fatal("cancelled inspection succeeded")
			}
			if test.stopped && !test.cancelled {
				if err != nil || result.SQLite != "observed" || result.HistoryRowsObserved["migrations"] != 1 {
					t.Fatalf("cold result=%+v err=%v", result, err)
				}
				encoded, _ := json.Marshal(result)
				if strings.Contains(string(encoded), "OWNED_SECRET") {
					t.Fatal("raw setting escaped")
				}
			} else if !test.stopped && result.SQLite != "not-inspected" {
				t.Fatal("unfenced live source was opened")
			}
			if after := inspectionSnapshot(t, filepath.Dir(paths.Database)); !reflect.DeepEqual(before, after) {
				t.Fatalf("source changed: before=%v after=%v", before, after)
			}
		})
	}
	// The dedicated cold DSN must reject SQL writes as well as preserve files.
	db, err := sql.Open("sqlite", inspectionDataSourceName(paths.Database, true))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`create table forbidden(id integer)`); err == nil {
		t.Fatal("inspection DSN accepted a write")
	}
}

func TestLegacyInspectionWALReadsCommittedRowOnlyUnderOwnershipFence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.sqlite")
	cmd := exec.Command(os.Args[0], "-test.run=^TestLegacyInspectionCrashFixtureProcess$")
	cmd.Env = append(os.Environ(), "CPAMP_INSPECTION_CRASH_PATH="+path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create crash fixture: %v %s", err, output)
	}
	// Missing SHM reproduces the original mode=ro side effect. This is owned data.
	if err := os.Remove(path + "-shm"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	paths := InspectionPaths{Database: path, DataKey: filepath.Join(dir, "data.key"), Archives: filepath.Join(dir, "usage-archives")}
	before := inspectionSnapshot(t, dir)
	blocked, err := InspectLegacySource(context.Background(), paths, InspectionOptions{SourceStopped: true})
	if err != nil || blocked.SQLite != "not-inspected" || len(blocked.Issues) == 0 {
		t.Fatalf("unfenced WAL=%+v err=%v", blocked, err)
	}
	lock, err := processlock.AcquireExisting(path)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	for _, role := range []string{"config", "data-key"} {
		t.Run("separate-"+role+"-temp-directory-rejected", func(t *testing.T) {
			sourceDir := t.TempDir()
			file := filepath.Join(sourceDir, role)
			if err := os.WriteFile(file, []byte("owned source"), 0600); err != nil {
				t.Fatal(err)
			}
			sourceBefore := inspectionSnapshot(t, sourceDir)
			sourcePaths := paths
			if role == "config" {
				sourcePaths.Config = file
			} else {
				sourcePaths.DataKey = file
			}
			for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
				t.Setenv(key, sourceDir)
			}
			result, err := InspectLegacySource(context.Background(), sourcePaths, InspectionOptions{ManagerLock: lock})
			if err == nil || result.SQLite != "not-inspected" {
				t.Errorf("staging was created in separate %s directory: err=%v", role, err)
			}
			if !reflect.DeepEqual(sourceBefore, inspectionSnapshot(t, sourceDir)) {
				t.Errorf("inspection changed separate %s directory", role)
			}
		})
	}
	t.Run("source-temp-directory-rejected", func(t *testing.T) {
		for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
			t.Setenv(key, dir)
		}
		result, err := InspectLegacySource(context.Background(), paths, InspectionOptions{ManagerLock: lock})
		if err == nil || result.SQLite != "not-inspected" {
			t.Fatal("staging was created inside source")
		}
		if !reflect.DeepEqual(before, inspectionSnapshot(t, dir)) {
			t.Fatal("source staging rejection changed source")
		}
	})
	result, err := InspectLegacySource(context.Background(), paths, InspectionOptions{ManagerLock: lock})
	if err != nil || result.Consistency != "manager-fenced-recovery-copy" || result.HistoryRowsObserved["migrations"] != 1 || result.History["migrations"] != "pending-observed" {
		t.Fatalf("committed row lost: result=%+v err=%v", result, err)
	}
	if after := inspectionSnapshot(t, dir); !reflect.DeepEqual(before, after) {
		t.Fatalf("WAL inspection changed source: before=%v after=%v", before, after)
	}
	// Demonstrate why directly reading this source as immutable is insufficient.
	db, err := sql.Open("sqlite", inspectionDataSourceName(path, true))
	if err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.QueryRow(`select count(*) from usage_data_migrations`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if rows != 0 {
		t.Fatalf("fixture did not isolate WAL committed content: %d", rows)
	}
}

func TestLegacyInspectionInvalidWALHeaderDoesNotBecomeSuccessfulColdRead(t *testing.T) {
	paths := coldInspectionFixture(t)
	lock, err := processlock.Acquire(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	_ = lock.Close()
	if err := os.WriteFile(paths.Database+"-wal", make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	before := inspectionSnapshot(t, filepath.Dir(paths.Database))
	lock, err = processlock.AcquireExisting(paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	result, err := InspectLegacySource(context.Background(), paths, InspectionOptions{ManagerLock: lock})
	if err != nil || result.SQLite != "not-inspected" || len(result.Issues) != 1 || result.Issues[0] != "wal-header-invalid" {
		t.Fatalf("invalid WAL=%+v err=%v", result, err)
	}
	if !reflect.DeepEqual(before, inspectionSnapshot(t, filepath.Dir(paths.Database))) {
		t.Fatal("invalid WAL changed source")
	}
}

func TestLegacyInspectionCrashFixtureProcess(t *testing.T) {
	path := os.Getenv("CPAMP_INSPECTION_CRASH_PATH")
	if path == "" {
		t.Skip("helper process")
	}
	lock, err := processlock.Acquire(path)
	if err != nil {
		t.Fatal(err)
	}
	_ = lock
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	for _, query := range []string{`pragma journal_mode=WAL`, `pragma wal_autocheckpoint=0`,
		`create table settings(key text primary key,value text)`, `create table usage_events(id integer primary key)`,
		`create table usage_data_migrations(name text primary key,status text)`, `pragma wal_checkpoint(truncate)`,
		`insert into usage_data_migrations values('committed-only-in-WAL','pending')`, `insert into usage_events values(1)`} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	os.Exit(0) // Deliberate owned crash: no Close/checkpoint, kernel releases lock.
}

type replacingInspectionContext struct {
	context.Context
	replace func()
	once    bool
}

func (c *replacingInspectionContext) Err() error {
	if !c.once {
		c.once = true
		c.replace()
	}
	return c.Context.Err()
}

func TestLegacyInspectionDetectsReplacementWithIdenticalBytesAndTimestamp(t *testing.T) {
	paths := coldInspectionFixture(t)
	ctx := &replacingInspectionContext{Context: context.Background(), replace: func() {
		info, err := os.Stat(paths.Database)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(paths.Database)
		if err != nil {
			t.Fatal(err)
		}
		replacement := paths.Database + ".replacement"
		if err := os.WriteFile(replacement, data, info.Mode().Perm()); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, paths.Database); err != nil {
			t.Fatal(err)
		}
	}}
	result, err := InspectLegacySource(ctx, paths, InspectionOptions{SourceStopped: true})
	if err == nil || result.Consistency != "changed" {
		t.Fatalf("replacement accepted: result=%+v err=%v", result, err)
	}
}

func TestLegacyInspectionDamagedEmptyJournalAndSymlinkDoNotWrite(t *testing.T) {
	for _, kind := range []string{"corrupt", "empty", "journal", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			paths := coldInspectionFixture(t)
			switch kind {
			case "corrupt":
				if err := os.WriteFile(paths.Database, []byte("OWNED_SECRET-not-a-database"), 0600); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := os.Truncate(paths.Database, 0); err != nil {
					t.Fatal(err)
				}
			case "journal":
				if err := os.WriteFile(paths.Database+"-journal", []byte("unproven hot journal"), 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := paths.Database + ".target"
				if err := os.Rename(paths.Database, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, paths.Database); err != nil {
					t.Skip(err)
				}
			}
			before := inspectionSnapshot(t, filepath.Dir(paths.Database))
			result, err := InspectLegacySource(context.Background(), paths, InspectionOptions{SourceStopped: true})
			if kind == "corrupt" && (err == nil || strings.Contains(err.Error(), "OWNED_SECRET")) {
				t.Fatalf("damaged error=%v", err)
			}
			if len(result.Issues) == 0 {
				t.Fatalf("unsafe source reported no issue: %+v", result)
			}
			if !reflect.DeepEqual(before, inspectionSnapshot(t, filepath.Dir(paths.Database))) {
				t.Fatal("source changed")
			}
		})
	}
}
