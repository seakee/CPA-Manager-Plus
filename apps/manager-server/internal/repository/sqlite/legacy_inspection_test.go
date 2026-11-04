package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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
			// Windows locks forbid reading the held, empty ownership file.
			// Its zero size determines the content; metadata is still compared.
			var data []byte
			if info.Size() > 0 {
				data, err = os.ReadFile(path)
				if err != nil {
					return err
				}
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

func TestInspectionCopyPreservesCancellationCauseAndSource(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "owned-source")
			if err := os.WriteFile(source, []byte("owned source content"), 0600); err != nil {
				t.Fatal(err)
			}
			before := inspectionSnapshot(t, dir)
			var ctx context.Context
			var cancel context.CancelFunc
			if cause == context.DeadlineExceeded {
				ctx, cancel = context.WithDeadline(context.Background(), time.Time{})
			} else {
				ctx, cancel = context.WithCancel(context.Background())
			}
			cancel()
			err := copyInspectionFile(ctx, source, filepath.Join(t.TempDir(), "owned-copy"))
			if !errors.Is(err, cause) {
				t.Fatalf("cancellation cause lost: %v", err)
			}
			if !reflect.DeepEqual(before, inspectionSnapshot(t, dir)) {
				t.Fatal("cancelled copy changed source")
			}
		})
	}
}

// TestReadOnlySnapshotReadsAPrivateCopyOfTheColdSource is the entry leaf's
// physical contract. The leaf authenticates a cold source through a read-only
// snapshot; a read-only open in place still creates -wal/-shm beside a database
// whose header says WAL, and a controlled writer switches the source to WAL in
// exactly the window between the header check and the first credential SQL. The
// snapshot must be a private copy: the source keeps the entries, bytes, sizes
// and modification times it had when the read started, and no sidecar appears.
func TestReadOnlySnapshotReadsAPrivateCopyOfTheColdSource(t *testing.T) {
	paths := coldInspectionFixture(t)
	var atAuthorize map[string]string
	_, err := InspectLegacySourceForAdmin(context.Background(), paths, InspectionOptions{ReadOnlySnapshot: true}, func(db *sql.DB) error {
		writer, err := sql.Open("sqlite", paths.Database)
		if err != nil {
			return err
		}
		if _, err := writer.Exec(`pragma journal_mode=wal`); err != nil {
			_ = writer.Close()
			return err
		}
		if err := writer.Close(); err != nil {
			return err
		}
		for _, suffix := range []string{"-wal", "-shm"} {
			if _, err := os.Lstat(paths.Database + suffix); !os.IsNotExist(err) {
				t.Fatalf("the controlled writer left %s: %v", suffix, err)
			}
		}
		// Everything below belongs to the reader.
		atAuthorize = inspectionSnapshot(t, filepath.Dir(paths.Database))
		var rows int
		return db.QueryRowContext(context.Background(), `select count(*) from settings`).Scan(&rows)
	})
	t.Logf("inspection error=%v", err)
	for _, suffix := range []string{"-wal", "-shm"} {
		if info, statErr := os.Lstat(paths.Database + suffix); statErr == nil {
			t.Fatalf("the inspection created %s beside the source (bytes=%d)", suffix, info.Size())
		} else if !os.IsNotExist(statErr) {
			t.Fatal(statErr)
		}
	}
	if after := inspectionSnapshot(t, filepath.Dir(paths.Database)); !reflect.DeepEqual(atAuthorize, after) {
		t.Fatalf("the inspection changed the source: before=%v after=%v", atAuthorize, after)
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

func TestLegacyInspectionMetadataDoesNotEstablishWriterQuiescence(t *testing.T) {
	paths := coldInspectionFixture(t)
	if err := os.WriteFile(paths.Config, []byte("owned-before"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := inspectFiles(paths)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(paths.Config)
	if err != nil {
		t.Fatal(err)
	}
	// Overwrite the same inode with the same byte count, restoring mtime.
	if err := os.WriteFile(paths.Config, []byte("owned-after!"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(paths.Config, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	after, err := inspectFiles(paths)
	if err != nil {
		t.Fatal(err)
	}
	if !sameInspectionFiles(before, after) {
		t.Fatal("fixture did not preserve the compared metadata")
	}
	result, err := InspectLegacySource(context.Background(), paths, InspectionOptions{})
	if err != nil || result.SQLite != "not-inspected" || result.Consistency != "unknown" || len(result.Issues) != 1 || result.Issues[0] != "source-quiescence-unproven" {
		t.Fatalf("metadata was accepted as writer quiescence: result=%+v err=%v", result, err)
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

func TestLegacyInspectionCacheAccountingExactRowIsBoundedAndSafe(t *testing.T) {
	for _, test := range []struct{ name, query, state string }{
		{"missing", `delete from usage_data_migrations`, "unknown"},
		{"unrelated-completed", `insert into usage_data_migrations values('unrelated','completed')`, "unknown"},
		{"completed", `insert into usage_data_migrations values('usage_cache_accounting_v2','completed')`, "completed-observed"},
		{"pending", `insert into usage_data_migrations values('usage_cache_accounting_v2','pending')`, "incomplete-observed"},
		{"discovering", `insert into usage_data_migrations values('usage_cache_accounting_v2','discovering')`, "incomplete-observed"},
		{"running", `insert into usage_data_migrations values('usage_cache_accounting_v2','running')`, "incomplete-observed"},
		{"applying", `insert into usage_data_migrations values('usage_cache_accounting_v2','applying')`, "incomplete-observed"},
		{"clearing", `insert into usage_data_migrations values('usage_cache_accounting_v2','clearing')`, "incomplete-observed"},
		{"failed", `insert into usage_data_migrations values('usage_cache_accounting_v2','failed')`, "failed-observed"},
		{"cancelled", `insert into usage_data_migrations values('usage_cache_accounting_v2','cancelled')`, "unknown"},
		{"invalid", `insert into usage_data_migrations values('usage_cache_accounting_v2','OWNED_SECRET_STATUS')`, "unknown"},
		{"blob", `insert into usage_data_migrations values('usage_cache_accounting_v2',x'4f574e45445f534543524554')`, "unknown"},
		{"duplicate", `drop table usage_data_migrations;create table usage_data_migrations(name text,status text);insert into usage_data_migrations values('usage_cache_accounting_v2','completed'),('usage_cache_accounting_v2','completed')`, "unknown"},
		{"nonbinary-name", `drop table usage_data_migrations;create table usage_data_migrations(name text primary key collate nocase,status text);insert into usage_data_migrations values('USAGE_CACHE_ACCOUNTING_V2','completed')`, "unknown"},
		{"missing-column", `drop table usage_data_migrations;create table usage_data_migrations(name text)`, "not-inspected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := coldInspectionFixture(t)
			db, err := sql.Open("sqlite", paths.Database)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(test.query); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before := inspectionSnapshot(t, filepath.Dir(paths.Database))
			result, err := InspectLegacySource(context.Background(), paths, InspectionOptions{SourceStopped: true})
			if err != nil {
				t.Fatal(err)
			}
			if result.CacheAccountingV2 != test.state {
				t.Fatalf("state=%s expected=%s", result.CacheAccountingV2, test.state)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "OWNED_SECRET") {
				t.Fatal("raw migration status leaked")
			}
			if !reflect.DeepEqual(before, inspectionSnapshot(t, filepath.Dir(paths.Database))) {
				t.Fatal("named migration read changed source")
			}
		})
	}
}

func TestLegacyInspectionQuotaSnapshotExactRowIsBoundedAndSafe(t *testing.T) {
	for _, test := range []struct{ name, query, state string }{
		{"missing", `delete from usage_data_migrations`, "unknown"},
		{"unrelated-completed", `insert into usage_data_migrations values('unrelated','completed')`, "unknown"},
		{"completed", `insert into usage_data_migrations values('quota_snapshot_lifecycle_v1','completed')`, "completed-observed"},
		{"pending", `insert into usage_data_migrations values('quota_snapshot_lifecycle_v1','pending')`, "incomplete-observed"},
		{"running", `insert into usage_data_migrations values('quota_snapshot_lifecycle_v1','running')`, "incomplete-observed"},
		{"offline-required", `insert into usage_data_migrations values('quota_snapshot_lifecycle_v1','offline_required')`, "incomplete-observed"},
		{"failed", `insert into usage_data_migrations values('quota_snapshot_lifecycle_v1','failed')`, "failed-observed"},
		{"wrong-state-machine", `insert into usage_data_migrations values('quota_snapshot_lifecycle_v1','applying')`, "unknown"},
		{"invalid", `insert into usage_data_migrations values('quota_snapshot_lifecycle_v1','OWNED_SECRET_STATUS')`, "unknown"},
		{"blob-status", `insert into usage_data_migrations values('quota_snapshot_lifecycle_v1',x'4f574e45445f534543524554')`, "unknown"},
		{"blob-name", `insert into usage_data_migrations values(cast('quota_snapshot_lifecycle_v1' as blob),'completed')`, "unknown"},
		{"duplicate", `drop table usage_data_migrations;create table usage_data_migrations(name text,status text);insert into usage_data_migrations values('quota_snapshot_lifecycle_v1','completed'),('quota_snapshot_lifecycle_v1','pending')`, "unknown"},
		{"nonbinary-name", `drop table usage_data_migrations;create table usage_data_migrations(name text primary key collate nocase,status text);insert into usage_data_migrations values('QUOTA_SNAPSHOT_LIFECYCLE_V1','completed')`, "unknown"},
		{"nonbinary-status", `drop table usage_data_migrations;create table usage_data_migrations(name text primary key,status text collate nocase);insert into usage_data_migrations values('quota_snapshot_lifecycle_v1','COMPLETED')`, "unknown"},
		{"missing-column", `drop table usage_data_migrations;create table usage_data_migrations(name text)`, "not-inspected"},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := coldInspectionFixture(t)
			db, err := sql.Open("sqlite", paths.Database)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(test.query); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			before := inspectionSnapshot(t, filepath.Dir(paths.Database))
			result, err := InspectLegacySource(context.Background(), paths, InspectionOptions{SourceStopped: true})
			if err != nil {
				t.Fatal(err)
			}
			if result.QuotaSnapshotLifecycleV1 != test.state {
				t.Fatalf("state=%s expected=%s", result.QuotaSnapshotLifecycleV1, test.state)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "OWNED_SECRET") {
				t.Fatal("raw migration state leaked")
			}
			if !reflect.DeepEqual(before, inspectionSnapshot(t, filepath.Dir(paths.Database))) {
				t.Fatal("named migration read changed source")
			}
		})
	}
}
func TestLegacyInspectionAggregateBudgetCannotReplaceExactMigration(t *testing.T) {
	paths := coldInspectionFixture(t)
	db, err := sql.Open("sqlite", paths.Database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`delete from usage_data_migrations;
 with recursive owned(n) as (values(1) union all select n+1 from owned where n<1001)
 insert into usage_data_migrations select 'owned_'||n,'completed' from owned;
 insert into usage_data_migrations values('usage_cache_accounting_v2','completed');
 insert into usage_data_migrations values('quota_snapshot_lifecycle_v1','offline_required')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	before := inspectionSnapshot(t, filepath.Dir(paths.Database))
	result, err := InspectLegacySource(context.Background(), paths, InspectionOptions{SourceStopped: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.CacheAccountingV2 != "completed-observed" || result.QuotaSnapshotLifecycleV1 != "incomplete-observed" || result.History["migrations"] != "unknown" || result.HistoryRowsObserved["migrations"] != 1001 {
		t.Fatalf("named/aggregate states mixed: %+v", result)
	}
	if !reflect.DeepEqual(before, inspectionSnapshot(t, filepath.Dir(paths.Database))) {
		t.Fatal("bounded read changed source")
	}
}
