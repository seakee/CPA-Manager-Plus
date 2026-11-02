package upgrade

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

// readySource builds a minimal 1.x data directory that preflight accepts: the
// base schema, a data key, an archive and the v1 ownership lock file.
func readySource(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	logical := []string{
		`pragma journal_mode = WAL`,
		`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`,
		`create table usage_events (id integer primary key autoincrement, event_hash text not null unique, timestamp_ms integer not null)`,
		`create table usage_event_identity_ledger (event_hash text primary key, raw_event_id integer, timestamp_ms integer, bucket_ms integer)`,
		`create table usage_monitoring_event_projection_v1 (event_id integer primary key, timestamp_ms integer not null)`,
		`create table usage_monitoring_rollup_state (rollup_name text primary key, schema_version integer not null, structure_revision text not null default '', status text not null, coverage_event_id integer not null default 0, updated_at_ms integer not null default 0)`,
		`create table usage_archive_event_refs (event_hash text primary key, run_id text not null, segment_sequence integer not null, raw_event_id integer not null, timestamp_ms integer not null, archived_at_ms integer not null, raw_deleted_at_ms integer)`,
		`create table usage_archive_deleted_coverage_daily (utc_day integer primary key, deleted_event_count integer not null, min_timestamp_ms integer not null, max_timestamp_ms integer not null)`,
		`insert into usage_monitoring_rollup_state (rollup_name, schema_version, structure_revision, status, coverage_event_id) values ('projection_v1', 1, '` + usageidentity.MonitoringProjectionStructureRevision() + `', 'ready', 1)`,
	}
	databasePath := filepath.Join(root, "usage.sqlite")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	for _, statement := range logical {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("apply fixture schema: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "usage-archives"), 0o755); err != nil {
		t.Fatalf("create archives: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "usage-archives", "segment-000001.jsonl"), []byte(`{"events":1}`+"\n"), 0o600); err != nil {
		t.Fatalf("write archive: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "data.key"), []byte("fixture-data-key\n"), 0o600); err != nil {
		t.Fatalf("write data key: %v", err)
	}
	if err := os.WriteFile(databasePath+".manager.lock", nil, 0o600); err != nil {
		t.Fatalf("write source lock: %v", err)
	}
	return root
}

func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := Run(context.Background(), args, stdout, stderr)
	return ExitCode(err), stdout.String(), stderr.String()
}

func TestPreflightJSONExitCodeReady(t *testing.T) {
	root := readySource(t)
	code, stdout, stderr := run(t, "preflight", "--legacy", root, "--source-version", bootstrap.PlannedDirectRelease, "--target-dir", t.TempDir(), "--json")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	var report upgradeservice.Report
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode report: %v; output: %s", err, stdout)
	}
	if !report.Ready || len(report.Blocked) != 0 {
		t.Fatalf("report ready = %t blocked = %v", report.Ready, report.Blocked)
	}
	if len(report.Checks) != 6 {
		t.Fatalf("checks = %d, want 6", len(report.Checks))
	}
}

func TestPreflightJSONExitCodeBlocked(t *testing.T) {
	root := t.TempDir()
	code, stdout, _ := run(t, "preflight", "--legacy", root, "--target-dir", t.TempDir(), "--json")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	var report upgradeservice.Report
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode report: %v; output: %s", err, stdout)
	}
	if report.Ready || len(report.Blocked) == 0 {
		t.Fatalf("report ready = %t blocked = %v, want blocked", report.Ready, report.Blocked)
	}
}

func TestUsageErrorsExitWithTwo(t *testing.T) {
	code, _, _ := run(t)
	if code != 2 {
		t.Fatalf("no subcommand exit code = %d, want 2", code)
	}
	code, _, _ = run(t, "preflight", "--json")
	if code != 2 {
		t.Fatalf("missing --legacy exit code = %d, want 2", code)
	}
	code, _, _ = run(t, "preflight", "--legacy", t.TempDir(), "extra")
	if code != 2 {
		t.Fatalf("positional argument exit code = %d, want 2", code)
	}
	code, _, _ = run(t, "status", "--db-path", filepath.Join(t.TempDir(), "missing.sqlite"))
	if code != 2 {
		t.Fatalf("missing manager database exit code = %d, want 2", code)
	}
}

func TestStatusReportsPersistedOperation(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "manager.sqlite")
	db, err := sqliterepo.Open(databasePath)
	if err != nil {
		t.Fatalf("open manager database: %v", err)
	}
	repository := upgradeoperation.New(db)
	if _, err := repository.Advance(context.Background(), model.UpgradeDomainUsageEvents, model.UpgradeStageCopyIn); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close manager database: %v", err)
	}

	code, stdout, stderr := run(t, "status", "--json", "--db-path", databasePath)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	var output statusOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode status: %v; output: %s", err, stdout)
	}
	if !output.Recorded || output.Operation == nil {
		t.Fatalf("status = %+v, want a recorded operation", output)
	}
	if output.Operation.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("stage = %q, want %q", output.Operation.Stage, model.UpgradeStageCopyIn)
	}
	if len(output.Operation.DomainsDone) != 1 || output.Operation.DomainsDone[0] != model.UpgradeDomainUsageEvents {
		t.Fatalf("domains = %v", output.Operation.DomainsDone)
	}

	code, stdout, stderr = run(t, "status", "--db-path", databasePath)
	if code != 0 {
		t.Fatalf("human status exit code = %d, want 0; stderr: %s", code, stderr)
	}
	if stdout == "" {
		t.Fatal("human status printed nothing")
	}
}

func TestStatusWithoutMigrationReportsNoRecord(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "empty.sqlite")
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open empty database: %v", err)
	}
	if _, err := db.Exec(`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`); err != nil {
		t.Fatalf("create placeholder table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close empty database: %v", err)
	}
	code, stdout, stderr := run(t, "status", "--json", "--db-path", databasePath)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	var output statusOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode status: %v; output: %s", err, stdout)
	}
	if output.Recorded {
		t.Fatalf("status = %+v, want no recorded operation", output)
	}
}

// TestExitCodePinsEveryRefusal pins the CLI contract one row per refusal: a
// decision the operator has to look at exits 1 no matter which typed code it
// carries, and only a usage or environment failure exits 2. The rows carry a
// message like the service sends, so the mapping is proven for a real typed
// error and not just for the bare sentinel.
func TestExitCodePinsEveryRefusal(t *testing.T) {
	typed := func(code upgradeservice.UpgradeErrorCode) error {
		return &upgradeservice.UpgradeError{Code: code, Message: "pinned by the exit-code table"}
	}
	cases := []struct {
		name string
		err  error
		want int
	}{
		{name: "ready", err: nil, want: 0},
		{name: "blocked preflight (sentinel)", err: ErrBlocked, want: 1},
		{name: "unacknowledged mismatch", err: typed(upgradeservice.UpgradeErrorReconcileMismatch), want: 1},
		{name: "target incomplete", err: typed(upgradeservice.UpgradeErrorReconcileTargetIncomplete), want: 1},
		{name: "nothing compared", err: typed(upgradeservice.UpgradeErrorReconcileNothingCompared), want: 1},
		{name: "nothing compared (sentinel)", err: upgradeservice.ErrReconcileNothingCompared, want: 1},
		{name: "source changed", err: typed(upgradeservice.UpgradeErrorSourceChanged), want: 2},
		{name: "target modified", err: typed(upgradeservice.UpgradeErrorTargetModified), want: 2},
		{name: "usage", err: ErrUsage, want: 2},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if code := ExitCode(testCase.err); code != testCase.want {
				code2, _ := upgradeservice.UpgradeErrorCodeOf(testCase.err)
				t.Fatalf("exit code for %q = %d, want %d", code2, code, testCase.want)
			}
		})
	}
}

func TestPreflightBlocksWithoutADeclaredSourceVersion(t *testing.T) {
	root := readySource(t)
	code, stdout, stderr := run(t, "preflight", "--legacy", root, "--target-dir", t.TempDir(), "--json")
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s", code, stderr)
	}
	var report upgradeservice.Report
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("decode report: %v; output: %s", err, stdout)
	}
	if len(report.Blocked) != 1 || report.Blocked[0] != upgradeservice.CodeSourceVersionUndeclared {
		t.Fatalf("blocked = %v, want [%s]", report.Blocked, upgradeservice.CodeSourceVersionUndeclared)
	}
}

// TestStatusWorksOnAReadOnlyManagerVolume is the snapshot requirement for
// `upgrade status`: reading the row used to need write access to the volume
// because a read-only open of a WAL database creates -shm/-wal beside it.
func TestStatusWorksOnAReadOnlyManagerVolume(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "manager.sqlite")
	db, err := sqliterepo.Open(databasePath)
	if err != nil {
		t.Fatalf("open manager database: %v", err)
	}
	repository := upgradeoperation.New(db)
	if _, err := repository.Advance(context.Background(), model.UpgradeDomainUsageEvents, model.UpgradeStageCopyIn); err != nil {
		t.Fatalf("advance: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close manager database: %v", err)
	}
	before := treeDigests(t, directory)

	if err := os.Chmod(directory, 0o555); err != nil {
		t.Fatalf("make manager volume read-only: %v", err)
	}
	if err := os.Chmod(databasePath, 0o444); err != nil {
		t.Fatalf("make manager database read-only: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(directory, 0o755)
		_ = os.Chmod(databasePath, 0o600)
	})

	code, stdout, stderr := run(t, "status", "--json", "--db-path", databasePath)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	var output statusOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode status: %v; output: %s", err, stdout)
	}
	if !output.Recorded || output.Operation == nil || output.Operation.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("status = %+v, want the recorded operation", output)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatalf("restore manager volume: %v", err)
	}
	if err := os.Chmod(databasePath, 0o600); err != nil {
		t.Fatalf("restore manager database: %v", err)
	}
	after := treeDigests(t, directory)
	if len(before) != len(after) {
		t.Fatalf("manager volume entry count changed: %v -> %v", before, after)
	}
	for path, digest := range before {
		if after[path] != digest {
			t.Fatalf("manager entry %s changed: %q -> %q", path, digest, after[path])
		}
	}
}

func treeDigests(t *testing.T, root string) map[string]string {
	t.Helper()
	digests := map[string]string{}
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		digest := sha256.New()
		if _, err := io.Copy(digest, file); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		digests[relative] = hex.EncodeToString(digest.Sum(nil))
		return nil
	})
	if walkErr != nil {
		t.Fatalf("digest manager volume: %v", walkErr)
	}
	return digests
}

// newManagerDatabase creates a migrated Manager database with one persisted
// operation, the way a running Manager leaves it. It returns the directory and
// the database path.
func newManagerDatabase(t *testing.T) (string, string) {
	t.Helper()
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "manager.sqlite")
	db, err := sqliterepo.Open(databasePath)
	if err != nil {
		t.Fatalf("open manager database: %v", err)
	}
	repository := upgradeoperation.New(db)
	if _, err := repository.Advance(context.Background(), model.UpgradeDomainUsageEvents, model.UpgradeStageCopyIn); err != nil {
		_ = db.Close()
		t.Fatalf("advance: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close manager database: %v", err)
	}
	return directory, databasePath
}

func runError(t *testing.T, args ...string) (error, string, string) {
	t.Helper()
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := Run(context.Background(), args, stdout, stderr)
	return err, stdout.String(), stderr.String()
}

// setOSTempDir points the OS temporary directory at dir for the rest of the
// test. Every variable os.TempDir consults is set, so the redirect holds on Unix
// (TMPDIR) as well as on Windows (TMP, then TEMP).
func setOSTempDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("TMPDIR", dir)
	t.Setenv("TMP", dir)
	t.Setenv("TEMP", dir)
}

// liveWriterStopLimit bounds how long the status live-writer test waits for its
// writer goroutine. A writer that never stops must fail that one test with a
// message instead of consuming the whole binary's timeout.
const liveWriterStopLimit = 10 * time.Second

// waitForStop waits for ch to be closed and fails the test after
// liveWriterStopLimit, so a goroutine that never stops is reported here.
func waitForStop(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(liveWriterStopLimit):
		t.Errorf("%s did not stop within %s", what, liveWriterStopLimit)
	}
}

// liveWriterRun is the observable outcome of one `upgrade status` run against a
// live writer. A failing run is returned as data, so the caller decides how to
// fail and the harness itself never blocks on its writer.
type liveWriterRun struct {
	runErr   error
	stdout   string
	stderr   string
	attempts int
	raced    int
}

// runStatusAgainstALiveWriter keeps a Manager database busy from one goroutine
// and runs `upgrade status` against it. A non-nil failWith makes every snapshot
// attempt fail with that error, which drives the exit path that never races the
// writer.
//
// The writer is released and joined on every path, including the one where
// status fails, so a failure is returned as runErr instead of leaving the caller
// blocked on a goroutine that stops only after a race.
func runStatusAgainstALiveWriter(t *testing.T, failWith error) liveWriterRun {
	t.Helper()
	_, databasePath := newManagerDatabase(t)
	writer, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	writer.SetMaxOpenConns(1)
	// A payload large enough that one snapshot copy spans many writer commits:
	// the first attempt cannot quietly finish between two of them.
	if _, err := writer.Exec(`create table status_writer_probe (id integer primary key, payload blob)`); err != nil {
		_ = writer.Close()
		t.Fatalf("create writer table: %v", err)
	}
	if _, err := writer.Exec(`insert into status_writer_probe (id, payload) values (1, zeroblob(?))`, 8<<20); err != nil {
		_ = writer.Close()
		t.Fatalf("seed writer payload: %v", err)
	}

	stop := make(chan struct{})
	stopped := make(chan struct{})
	var stopOnce sync.Once
	// halt releases the writer exactly once. Every exit path calls it, so a run
	// that fails before a snapshot ever races still ends the goroutine.
	halt := func() { stopOnce.Do(func() { close(stop) }) }
	attempts, raced := 0, 0
	original := openSnapshot
	openSnapshot = func(ctx context.Context, path string, scratch string) (*upgradeservice.Snapshot, error) {
		attempts++
		if failWith != nil {
			return nil, failWith
		}
		snapshot, err := original(ctx, path, scratch)
		if err != nil && errors.Is(err, upgradeservice.ErrSourceChanged) {
			raced++
			// The writer was live through this attempt. Let it go quiet, like a
			// Manager between bursts, so a retry sees a stopped writer.
			halt()
		}
		return snapshot, err
	}
	t.Cleanup(func() { openSnapshot = original })
	go func() {
		defer close(stopped)
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = writer.Exec(`insert into status_writer_probe (id, payload)
				values ((select coalesce(max(id), 1) + 1 from status_writer_probe), x'')`)
			time.Sleep(50 * time.Microsecond)
		}
	}()
	// Close the writer connection however this harness ends, including an early
	// t.Fatalf, and keep the join bounded so a stuck writer is reported here
	// instead of consuming the whole binary's timeout.
	t.Cleanup(func() {
		halt()
		waitForStop(t, stopped, "the status live writer")
		if err := writer.Close(); err != nil {
			t.Errorf("close writer: %v", err)
		}
	})

	runErr, stdout, stderr := runError(t, "status", "--json", "--db-path", databasePath)
	// Release the writer on every outcome and capture the result before joining
	// it, so a broken retry path surfaces as runErr rather than as a wait.
	halt()
	result := liveWriterRun{runErr: runErr, stdout: stdout, stderr: stderr, attempts: attempts, raced: raced}
	waitForStop(t, stopped, "the status live writer")
	return result
}

// TestStatusSucceedsAgainstALiveWriterWithinItsRetryBudget is the regression for
// N1: `upgrade status` used to fail every time it raced a Manager that was
// writing, because one snapshot attempt was all it made. A live Manager writes
// in bursts, so a bounded retry finds a quiet window.
func TestStatusSucceedsAgainstALiveWriterWithinItsRetryBudget(t *testing.T) {
	run := runStatusAgainstALiveWriter(t, nil)
	if run.runErr != nil {
		t.Fatalf("status against a live writer: %v; stderr: %s", run.runErr, run.stderr)
	}
	if run.raced == 0 {
		t.Fatal("no snapshot attempt raced the live writer; the retry was never exercised")
	}
	if run.attempts > statusSnapshotAttempts {
		t.Fatalf("snapshot attempts = %d, want at most %d", run.attempts, statusSnapshotAttempts)
	}
	var output statusOutput
	if err := json.Unmarshal([]byte(run.stdout), &output); err != nil {
		t.Fatalf("decode status: %v; output: %s", err, run.stdout)
	}
	if !output.Recorded || output.Operation == nil || output.Operation.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("status = %+v, want the recorded operation", output)
	}
	t.Logf("snapshot attempts against a live writer: %d (raced %d)", run.attempts, run.raced)
}

// TestStatusAgainstALiveWriterReportsANonRaceFailure is the regression for the
// 2026-11-02 review finding that the live-writer test released its writer only
// on ErrSourceChanged: any other status failure left the join blocked, so a real
// regression became a global test timeout. The harness must return the status
// error on a path that never races the writer.
func TestStatusAgainstALiveWriterReportsANonRaceFailure(t *testing.T) {
	run := runStatusAgainstALiveWriter(t, fmt.Errorf("%w: scratch directory is not writable", upgradeservice.ErrSnapshotFailed))
	if run.runErr == nil {
		t.Fatalf("status succeeded while every snapshot attempt failed; stdout: %s", run.stdout)
	}
	if !strings.Contains(run.runErr.Error(), upgradeservice.CodeSnapshotFailed) {
		t.Fatalf("error %q does not carry %s", run.runErr, upgradeservice.CodeSnapshotFailed)
	}
	if code := ExitCode(run.runErr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if run.raced != 0 {
		t.Fatalf("raced = %d, want 0: the writer must be released even when nothing raced", run.raced)
	}
	if run.attempts != 1 {
		t.Fatalf("snapshot attempts = %d, want 1: an unusable scratch directory is not retried", run.attempts)
	}
}

// TestStatusReportsARetryableCodeWhenTheWriterNeverStops covers the exhausted
// budget: the failure is the shared upgrade_source_changed code marked
// retryable, not a generic usage error.
func TestStatusReportsARetryableCodeWhenTheWriterNeverStops(t *testing.T) {
	_, databasePath := newManagerDatabase(t)
	attempts := 0
	original := openSnapshot
	openSnapshot = func(context.Context, string, string) (*upgradeservice.Snapshot, error) {
		attempts++
		return nil, fmt.Errorf("%w: writer held the database", upgradeservice.ErrSourceChanged)
	}
	t.Cleanup(func() { openSnapshot = original })

	runErr, _, _ := runError(t, "status", "--json", "--db-path", databasePath)
	if runErr == nil {
		t.Fatal("status succeeded against a writer that never stopped")
	}
	if !errors.Is(runErr, ErrRetryable) {
		t.Fatalf("error = %v, want ErrRetryable", runErr)
	}
	if errors.Is(runErr, ErrUsage) {
		t.Fatalf("error = %v, was reported as a usage error", runErr)
	}
	if !strings.Contains(runErr.Error(), upgradeservice.CodeSourceChanged) {
		t.Fatalf("error %q does not carry %s", runErr, upgradeservice.CodeSourceChanged)
	}
	if attempts != statusSnapshotAttempts {
		t.Fatalf("snapshot attempts = %d, want the bounded %d", attempts, statusSnapshotAttempts)
	}
	if code := ExitCode(runErr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

// TestStatusRefusesScratchInsideTheManagerVolume is the status half of N2: the
// snapshot copy must never land in the volume it is upgrading.
func TestStatusRefusesScratchInsideTheManagerVolume(t *testing.T) {
	directory, databasePath := newManagerDatabase(t)
	before := treeDigests(t, directory)

	runErr, _, _ := runError(t, "status", "--json", "--db-path", databasePath, "--scratch-dir", filepath.Join(directory, "scratch"))
	if runErr == nil {
		t.Fatal("status accepted a scratch directory inside the Manager volume")
	}
	if !strings.Contains(runErr.Error(), upgradeservice.CodeSnapshotFailed) {
		t.Fatalf("error %q does not carry %s", runErr, upgradeservice.CodeSnapshotFailed)
	}
	if code := ExitCode(runErr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if _, err := os.Lstat(filepath.Join(directory, "scratch")); !os.IsNotExist(err) {
		t.Fatalf("scratch directory was created: %v", err)
	}
	after := treeDigests(t, directory)
	if len(before) != len(after) {
		t.Fatalf("Manager volume entry count changed: %v -> %v", before, after)
	}
	for path, digest := range before {
		if after[path] != digest {
			t.Fatalf("Manager entry %s changed: %q -> %q", path, digest, after[path])
		}
	}
}

// TestStatusRefusesADefaultTemporaryDirectoryInsideTheManagerVolume is the
// status half of the default-scratch rule: with no --scratch-dir, `upgrade
// status` used to skip the placement check and write a full copy of the Manager
// database into the volume it is upgrading.
func TestStatusRefusesADefaultTemporaryDirectoryInsideTheManagerVolume(t *testing.T) {
	directory, databasePath := newManagerDatabase(t)
	temp := filepath.Join(directory, "tmp")
	if err := os.Mkdir(temp, 0o755); err != nil {
		t.Fatalf("create default temporary directory: %v", err)
	}
	setOSTempDir(t, temp)
	before := treeDigests(t, directory)

	runErr, _, _ := runError(t, "status", "--json", "--db-path", databasePath)
	if runErr == nil {
		t.Fatal("status accepted a default temporary directory inside the Manager volume")
	}
	if !strings.Contains(runErr.Error(), upgradeservice.CodeSnapshotFailed) {
		t.Fatalf("error %q does not carry %s", runErr, upgradeservice.CodeSnapshotFailed)
	}
	if code := ExitCode(runErr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	entries, err := os.ReadDir(temp)
	if err != nil {
		t.Fatalf("read default temporary directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch was created in the default temporary directory: %v", entries)
	}
	after := treeDigests(t, directory)
	if len(before) != len(after) {
		t.Fatalf("Manager volume entry count changed: %v -> %v", before, after)
	}
	for path, digest := range before {
		if after[path] != digest {
			t.Fatalf("Manager entry %s changed: %q -> %q", path, digest, after[path])
		}
	}
}

// TestPreflightRequiresTargetDirWhenTheManagerPathCannotBeDerived is N3: the
// disk check belongs to the Manager volume, and without that path there is
// nothing to measure — the working directory is not a substitute.
func TestPreflightRequiresTargetDirWhenTheManagerPathCannotBeDerived(t *testing.T) {
	root := readySource(t)
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write broken config: %v", err)
	}
	t.Setenv("CPA_MANAGER_CONFIG", configPath)

	runErr, _, _ := runError(t, "preflight", "--legacy", root, "--source-version", bootstrap.PlannedDirectRelease, "--json")
	if runErr == nil {
		t.Fatal("preflight succeeded without a derivable Manager path or --target-dir")
	}
	if !errors.Is(runErr, ErrUsage) {
		t.Fatalf("error = %v, want ErrUsage", runErr)
	}
	if !strings.Contains(runErr.Error(), "--target-dir") {
		t.Fatalf("error %q does not ask for --target-dir", runErr)
	}
	if errors.Is(runErr, ErrRetryable) {
		t.Fatalf("error = %v, was reported as retryable", runErr)
	}
	if code := ExitCode(runErr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

// copyReadyRunSource builds a stopped 1.x volume the preflight and the backup
// both accept: the carried-forward usage schema, the v1.14 structure the version
// check requires, no v2-only tables, a real data key, archives and the v1
// ownership marker.
func copyReadyRunSource(t *testing.T) string {
	t.Helper()
	root := canonicalFixtureRoot(t)
	databasePath := filepath.Join(root, "usage.sqlite")
	db, err := sqliterepo.Open(databasePath)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	if _, err := db.Exec(`insert into usage_events (event_hash, timestamp_ms, timestamp, model, requested_model, created_at_ms)
		values ('legacy-event-1', 1000, '1000', 'gpt-test', 'gpt-test', 1000)`); err != nil {
		_ = db.Close()
		t.Fatalf("seed fixture event: %v", err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		_ = db.Close()
		t.Fatalf("open fixture connection: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), `pragma foreign_keys = off`); err != nil {
		_ = conn.Close()
		_ = db.Close()
		t.Fatalf("disable fixture foreign keys: %v", err)
	}
	for _, pattern := range []string{`gateway\_%`, `fresh\_%`, `upgrade_operations`} {
		names, err := fixtureTableNames(conn, pattern)
		if err != nil {
			_ = conn.Close()
			_ = db.Close()
			t.Fatalf("list v2-only fixture tables: %v", err)
		}
		for _, name := range names {
			if _, err := conn.ExecContext(context.Background(), `drop table `+name); err != nil {
				_ = conn.Close()
				_ = db.Close()
				t.Fatalf("drop v2-only fixture table %s: %v", name, err)
			}
		}
	}
	// The v1.14.6 and v1.14.7 markers arrived after the pinned release; a source
	// that carries them is a different source than the one this pin accepts.
	for _, statement := range []string{
		`drop index if exists idx_usage_events_ts_latency_ttft_cover`,
		`delete from settings where key = 'usage_cache_accounting_semantics_revision'`,
	} {
		if _, err := conn.ExecContext(context.Background(), statement); err != nil {
			_ = conn.Close()
			_ = db.Close()
			t.Fatalf("strip post-pin marker %q: %v", statement, err)
		}
	}
	if err := conn.Close(); err != nil {
		_ = db.Close()
		t.Fatalf("close fixture connection: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}
	if _, _, err := security.LoadOrCreateDataKey("", filepath.Join(root, "data.key")); err != nil {
		t.Fatalf("create fixture data key: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "usage-archives", "2026-03"), 0o755); err != nil {
		t.Fatalf("create fixture archives: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "usage-archives", "2026-03", "segment-000001.jsonl"),
		[]byte(`{"events":1}`+"\n"), 0o600); err != nil {
		t.Fatalf("write fixture archive: %v", err)
	}
	if err := os.WriteFile(databasePath+".manager.lock", nil, 0o600); err != nil {
		t.Fatalf("write fixture source lock: %v", err)
	}
	return root
}

// canonicalFixtureRoot is a temporary directory with every symlink resolved:
// maintenancebundle refuses any bundle path below a symbolic link.
func canonicalFixtureRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve fixture root: %v", err)
	}
	return root
}

func fixtureTableNames(db *sql.Conn, pattern string) ([]string, error) {
	rows, err := db.QueryContext(context.Background(),
		`select name from sqlite_master where type = 'table' and name like ? escape '\'`, pattern)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

// startLiveWalWriter leaves the 1.x fixture exactly as a killed 1.x process does:
// WAL mode, uncheckpointed frames in usage.sqlite-wal and a WAL index on disk.
// The writer handle stays open so those frames cannot be checkpointed away.
func startLiveWalWriter(t *testing.T, root string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(root, "usage.sqlite"))
	if err != nil {
		t.Fatalf("open the 1.x fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`pragma journal_mode = wal`,
		`pragma wal_autocheckpoint = 0`,
		`insert into settings (key, value, updated_at_ms) values ('wal_frame_probe', '1', 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("prepare a live source WAL: %v", err)
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if info, err := os.Lstat(filepath.Join(root, "usage.sqlite"+suffix)); err != nil || info.Size() == 0 {
			t.Fatalf("the live source holds no %s (err=%v)", suffix, err)
		}
	}
}

func sourceDatabaseDigests(t *testing.T, root string) map[string]string {
	t.Helper()
	digests := map[string]string{}
	for _, name := range []string{"usage.sqlite", "usage.sqlite-wal", "usage.sqlite-shm"} {
		path := filepath.Join(root, name)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		digest := sha256.Sum256(data)
		digests[name] = hex.EncodeToString(digest[:])
	}
	return digests
}

// TestRunPreflightLeavesALiveWALSourceUnchanged is the M3 regression: `upgrade
// run` used to call the preflight first, and the preflight opened the 1.x
// database in place with a plain read-only open. SQLite created or rewrote the
// source's -shm for that read, so a command that promises to write nothing into
// the 1.x volume had already written into it before the copy ran.
func TestRunPreflightLeavesALiveWALSourceUnchanged(t *testing.T) {
	source := copyReadyRunSource(t)
	startLiveWalWriter(t, source)
	before := sourceDatabaseDigests(t, source)
	if before["usage.sqlite-wal"] == "" || before["usage.sqlite-shm"] == "" {
		t.Fatalf("the fixture is not a live WAL source: %v", before)
	}

	target := t.TempDir()
	code, stdout, stderr := run(t, "run", "--json", "--legacy", source,
		"--source-version", bootstrap.PlannedDirectRelease, "--target", target)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stdout: %s stderr: %s", code, stdout, stderr)
	}
	if after := sourceDatabaseDigests(t, source); !reflect.DeepEqual(before, after) {
		t.Fatalf("the run changed the 1.x volume: before %v, after %v", before, after)
	}
}

// TestRunCopiesAStoppedSourceAndRecordsTheStage walks the whole CLI path: the
// preflight runs, the copy-in writes the v2 layout, the stage is recorded and a
// second run reports the record instead of copying again.
func TestRunCopiesAStoppedSourceAndRecordsTheStage(t *testing.T) {
	source := copyReadyRunSource(t)
	target := t.TempDir()
	bundle := filepath.Join(t.TempDir(), "backup")

	code, stdout, stderr := run(t, "run", "--json", "--legacy", source,
		"--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	var output runOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode run output: %v; output: %s", err, stdout)
	}
	if output.Preflight == nil || !output.Preflight.Ready {
		t.Fatalf("preflight = %+v, want a ready report", output.Preflight)
	}
	if output.Result == nil {
		t.Fatalf("run output has no result: %s", stdout)
	}
	if output.Result.Stage != model.UpgradeStageCopyIn || output.Result.SourceKind != upgradeservice.SourceKindUpgradedLegacy {
		t.Fatalf("result stage/kind = %q/%q", output.Result.Stage, output.Result.SourceKind)
	}
	if output.Result.SourceVersion != bootstrap.PlannedDirectRelease {
		t.Fatalf("recorded source version = %q, want the declaration", output.Result.SourceVersion)
	}
	for _, path := range []string{
		filepath.Join(target, "usage.sqlite"),
		filepath.Join(target, "data.key"),
		filepath.Join(target, "usage-archives", "2026-03", "segment-000001.jsonl"),
		bundle,
	} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("copy-in did not produce %s: %v", path, err)
		}
	}

	code, statusJSON, stderr := run(t, "status", "--json", "--db-path", filepath.Join(target, "usage.sqlite"))
	if code != 0 {
		t.Fatalf("status exit code = %d, want 0; stderr: %s", code, stderr)
	}
	var status statusOutput
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		t.Fatalf("decode status: %v; output: %s", err, statusJSON)
	}
	if !status.Recorded || status.Operation == nil || status.Operation.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("status = %+v, want a recorded copy_in", status)
	}
	if status.Operation.BackupManifestSHA256 != output.Result.BackupManifestSHA256 {
		t.Fatalf("recorded manifest = %q, want %q", status.Operation.BackupManifestSHA256, output.Result.BackupManifestSHA256)
	}
	if status.Operation.SourceFingerprintBefore != output.Result.SourceFingerprintBefore {
		t.Fatalf("recorded fingerprint = %q, want %q", status.Operation.SourceFingerprintBefore, output.Result.SourceFingerprintBefore)
	}

	// A second run reports the recorded copy-in instead of copying again.
	code, again, stderr := run(t, "run", "--json", "--legacy", source,
		"--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle)
	if code != 0 {
		t.Fatalf("second run exit code = %d, want 0; stderr: %s", code, stderr)
	}
	var repeated runOutput
	if err := json.Unmarshal([]byte(again), &repeated); err != nil {
		t.Fatalf("decode second run: %v", err)
	}
	if repeated.Result == nil || !repeated.Result.AlreadyComplete || repeated.Result.Restored {
		t.Fatalf("second run = %+v, want an already complete copy-in", repeated.Result)
	}
}

func TestRunRefusesABlockedSourceAndWritesNothing(t *testing.T) {
	source := copyReadyRunSource(t)
	// Removing the ownership marker makes the source unprovably stopped.
	if err := os.Remove(filepath.Join(source, "usage.sqlite.manager.lock")); err != nil {
		t.Fatalf("remove source lock: %v", err)
	}
	target := t.TempDir()
	code, stdout, stderr := run(t, "run", "--json", "--legacy", source,
		"--source-version", bootstrap.PlannedDirectRelease, "--target", target)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stdout: %s stderr: %s", code, stdout, stderr)
	}
	var output runOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode blocked run: %v; output: %s", err, stdout)
	}
	if output.Preflight == nil || output.Preflight.Ready || output.Result != nil {
		t.Fatalf("blocked run = %+v, want a blocked report and no result", output)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("blocked run wrote %v into the v2 data root", entries)
	}
}

// reconcileRoots returns a v2 data root and a backup destination with every
// symlink resolved: the bundle verifier refuses a path below a symbolic link,
// and every macOS temporary directory is one.
func reconcileRoots(t *testing.T) (string, string) {
	t.Helper()
	target, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve the v2 data root: %v", err)
	}
	bundle, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve the backup destination: %v", err)
	}
	return target, filepath.Join(bundle, "backup")
}

func TestReconcileReportsTheCopiedStoreAndRecordsTheStage(t *testing.T) {
	source := copyReadyRunSource(t)
	target, bundle := reconcileRoots(t)
	if code, _, stderr := run(t, "run", "--json", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle); code != 0 {
		t.Fatalf("copy in exit code = %d, want 0; stderr: %s", code, stderr)
	}

	code, stdout, stderr := run(t, "reconcile", "--json", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle)
	if code != 0 {
		t.Fatalf("reconcile exit code = %d, want 0; stderr: %s; stdout: %s", code, stderr, stdout)
	}
	var output reconcileOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode reconcile output: %v; output: %s", err, stdout)
	}
	if output.Preflight == nil || !output.Preflight.Ready {
		t.Fatalf("preflight = %+v, want a ready report", output.Preflight)
	}
	if output.Report == nil || !output.Report.Recorded {
		t.Fatalf("reconcile output = %+v, want a recorded report", output)
	}
	if output.Report.Stage != model.UpgradeStageReconcile {
		t.Fatalf("stage = %q, want %q", output.Report.Stage, model.UpgradeStageReconcile)
	}
	if len(output.Report.IncompleteDays) != 0 {
		t.Fatalf("incomplete days = %+v, want none", output.Report.IncompleteDays)
	}
	// The fixture source holds the retained event and the derived/coverage
	// tables with no rows of their own. A table both sides hold is established
	// even with no source day — there is nothing to lose — while a table the
	// source does not hold is never recorded, so this is the whole set the step
	// can claim it compared.
	want := []string{
		model.UpgradeDomainSourceCopy,
		model.UpgradeDomainRetainedProjection,
		model.UpgradeDomainPricing,
		model.UpgradeDomainCoverage,
	}
	if !reflect.DeepEqual(output.Report.DomainsDone, want) {
		t.Fatalf("domains done = %v, want %v", output.Report.DomainsDone, want)
	}
}

func TestReconcileExitsOneOnAnUnacknowledgedMismatch(t *testing.T) {
	source := copyReadyRunSource(t)
	target, bundle := reconcileRoots(t)
	if code, _, stderr := run(t, "run", "--json", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle); code != 0 {
		t.Fatalf("copy in exit code = %d, want 0; stderr: %s", code, stderr)
	}
	// A token total that no longer matches the source: the day has to be
	// acknowledged before anything may be marked.
	db, err := sql.Open("sqlite", filepath.Join(target, "usage.sqlite"))
	if err != nil {
		t.Fatalf("open the copied store: %v", err)
	}
	if _, err := db.Exec(`update usage_events set total_tokens = total_tokens + 1`); err != nil {
		_ = db.Close()
		t.Fatalf("change one token total: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the copied store: %v", err)
	}

	code, stdout, stderr := run(t, "reconcile", "--json", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1; stderr: %s; stdout: %s", code, stderr, stdout)
	}
	var output reconcileOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode reconcile output: %v; output: %s", err, stdout)
	}
	if output.ErrorCode != string(upgradeservice.UpgradeErrorReconcileMismatch) {
		t.Fatalf("error code = %q, want %q", output.ErrorCode, upgradeservice.UpgradeErrorReconcileMismatch)
	}
	if output.Report == nil || output.Report.Recorded || len(output.Report.UnacknowledgedDays) != 1 {
		t.Fatalf("reconcile output = %+v, want one unacknowledged day and no record", output)
	}

	// Acknowledging the reported day marks it and completes the step.
	day := output.Report.UnacknowledgedDays[0]
	code, stdout, stderr = run(t, "reconcile", "--json", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease,
		"--target", target, "--bundle", bundle, "--acknowledge", day)
	if code != 0 {
		t.Fatalf("acknowledged exit code = %d, want 0; stderr: %s; stdout: %s", code, stderr, stdout)
	}
	var acknowledged reconcileOutput
	if err := json.Unmarshal([]byte(stdout), &acknowledged); err != nil {
		t.Fatalf("decode acknowledged output: %v; output: %s", err, stdout)
	}
	if acknowledged.Report == nil || !acknowledged.Report.Recorded ||
		acknowledged.Report.NewlyMarkedDays != 1 ||
		acknowledged.Report.IncompleteDays[0].Day != day {
		t.Fatalf("acknowledged reconcile = %+v, want the day marked once", acknowledged.Report)
	}
}

// TestReconcileRefusesAStoreAnotherProcessHolds reproduces review M3/P5 at the
// CLI: a v2 data root another owner had locked was written anyway, because the
// command took no lock at all.
func TestReconcileRefusesAStoreAnotherProcessHolds(t *testing.T) {
	source := copyReadyRunSource(t)
	target, bundle := reconcileRoots(t)
	if code, _, stderr := run(t, "run", "--json", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease,
		"--target", target, "--bundle", bundle); code != 0 {
		t.Fatalf("copy in exit code = %d, want 0; stderr: %s", code, stderr)
	}
	holder, err := processlock.Acquire(filepath.Join(target, "usage.sqlite"))
	if err != nil {
		t.Fatalf("take the v2 data-root lock as another owner: %v", err)
	}
	defer func() { _ = holder.Close() }()

	code, stdout, stderr := run(t, "reconcile", "--json", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease,
		"--target", target, "--bundle", bundle)
	if code != 1 {
		t.Fatalf("reconcile exit code = %d, want 1; stderr: %s; stdout: %s", code, stderr, stdout)
	}
	var output reconcileOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode reconcile output: %v; stderr: %s; output: %s", err, stderr, stdout)
	}
	if output.ErrorCode != string(upgradeservice.UpgradeErrorTargetLocked) {
		t.Fatalf("error code = %q, want %q; output: %s", output.ErrorCode, upgradeservice.UpgradeErrorTargetLocked, stdout)
	}
	if output.Report != nil && output.Report.Recorded {
		t.Fatalf("reconcile recorded under another owner's lock: %+v", output.Report)
	}
	if code, _, stdout := run(t, "status", "--json", "--db-path", filepath.Join(target, "usage.sqlite")); code != 0 {
		t.Fatalf("status exit code = %d; stdout: %s", code, stdout)
	}
}

// TestReconcileKeepsAChangedSourceRetryable pins the contract table's split: a
// source that moved is retryable (exit 2, upgrade_source_changed), while a
// refusal about the store or a day is exit 1.
func TestReconcileKeepsAChangedSourceRetryable(t *testing.T) {
	source := copyReadyRunSource(t)
	target, bundle := reconcileRoots(t)
	if code, _, stderr := run(t, "run", "--json", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease,
		"--target", target, "--bundle", bundle); code != 0 {
		t.Fatalf("copy in exit code = %d, want 0; stderr: %s", code, stderr)
	}
	db, err := sql.Open("sqlite", filepath.Join(source, "usage.sqlite"))
	if err != nil {
		t.Fatalf("open the 1.x source: %v", err)
	}
	if _, err := db.Exec(`insert into settings (key, value, updated_at_ms) values ('drifted', '1', 1)`); err != nil {
		_ = db.Close()
		t.Fatalf("drift the 1.x source: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the 1.x source: %v", err)
	}

	code, stdout, stderr := run(t, "reconcile", "--json", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease,
		"--target", target, "--bundle", bundle)
	if code != 2 {
		t.Fatalf("reconcile exit code = %d, want the retryable 2; stderr: %s; stdout: %s", code, stderr, stdout)
	}
	var output reconcileOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode reconcile output: %v; stderr: %s; output: %s", err, stderr, stdout)
	}
	if output.ErrorCode != string(upgradeservice.UpgradeErrorSourceChanged) {
		t.Fatalf("error code = %q, want %q; output: %s", output.ErrorCode, upgradeservice.UpgradeErrorSourceChanged, stdout)
	}
}

func TestReconcileUsageErrors(t *testing.T) {
	if code, _, _ := run(t, "reconcile"); code != 2 {
		t.Fatalf("missing --legacy exit code = %d, want 2", code)
	}
	if code, _, _ := run(t, "reconcile", "--legacy", t.TempDir(), "extra"); code != 2 {
		t.Fatalf("positional argument exit code = %d, want 2", code)
	}
	if code, _, _ := run(t, "reconcile", "--legacy", filepath.Join(t.TempDir(), "missing")); code != 2 {
		t.Fatalf("missing source exit code = %d, want 2", code)
	}
}

func TestRunUsageErrors(t *testing.T) {
	if code, _, _ := run(t, "run"); code != 2 {
		t.Fatalf("missing --legacy exit code = %d, want 2", code)
	}
	if code, _, _ := run(t, "run", "--legacy", t.TempDir(), "extra"); code != 2 {
		t.Fatalf("positional argument exit code = %d, want 2", code)
	}
	if code, _, _ := run(t, "run", "--legacy", filepath.Join(t.TempDir(), "missing")); code != 2 {
		t.Fatalf("missing source exit code = %d, want 2", code)
	}
}

// TestTargetDefaultsToTheManagerDataDirectory covers the default target: a
// Manager configuration where the database, the data key and the archive tree
// all resolve to the canonical names inside one directory.
func TestTargetDefaultsToTheManagerDataDirectory(t *testing.T) {
	dataDir := canonicalFixtureRoot(t)
	t.Setenv("USAGE_DATA_DIR", dataDir)
	t.Setenv("USAGE_DB_PATH", "")
	t.Setenv("CPA_MANAGER_DATA_KEY", "")
	t.Setenv("CPA_MANAGER_DATA_KEY_PATH", "")
	t.Setenv("CPA_MANAGER_CONFIG", filepath.Join(t.TempDir(), "absent-config.json"))
	root, err := resolveTargetRoot("")
	if err != nil {
		t.Fatalf("resolve default target: %v", err)
	}
	if root != dataDir {
		t.Fatalf("default target = %q, want the Manager data directory %q", root, dataDir)
	}
}

// TestTargetRefusesAManagerLayoutTheCopyCannotPopulate is the M4 regression. The
// default target used to be filepath.Dir(cfg.DBPath) and nothing else, while the
// service reads the data key from cfg.DataKeyPath, the archive tree from
// cfg.UsageArchiveDir and an inline key from the environment. Copying a data key
// the service never loads means the 1.x encrypted fields cannot be decrypted, so
// the run has to refuse a layout it cannot populate and ask for --target.
func TestTargetRefusesAManagerLayoutTheCopyCannotPopulate(t *testing.T) {
	dataDir := canonicalFixtureRoot(t)
	elsewhere := canonicalFixtureRoot(t)
	t.Setenv("CPA_MANAGER_CONFIG", filepath.Join(t.TempDir(), "absent-config.json"))
	t.Setenv("CPA_MANAGER_DATA_KEY", "")
	t.Setenv("CPA_MANAGER_DATA_KEY_PATH", "")
	t.Setenv("USAGE_DATA_DIR", dataDir)

	for _, test := range []struct {
		name  string
		setup func(t *testing.T)
	}{
		{
			name: "manager database beside another key",
			setup: func(t *testing.T) {
				t.Setenv("USAGE_DB_PATH", filepath.Join(elsewhere, "usage.sqlite"))
			},
		},
		{
			name: "inline environment key overrides the copied key",
			setup: func(t *testing.T) {
				t.Setenv("USAGE_DB_PATH", "")
				t.Setenv("CPA_MANAGER_DATA_KEY", "an-inline-key")
			},
		},
		{
			name: "manager database is not the canonical file name",
			setup: func(t *testing.T) {
				t.Setenv("USAGE_DB_PATH", filepath.Join(dataDir, "manager.sqlite"))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.setup(t)
			root, err := resolveTargetRoot("")
			if err == nil {
				t.Fatalf("resolved target %q, want a refusal", root)
			}
			if !errors.Is(err, ErrUsage) {
				t.Fatalf("error = %v, want ErrUsage", err)
			}
			if !strings.Contains(err.Error(), upgradeservice.CodeTargetLayoutMismatch) {
				t.Fatalf("error %q does not carry %s", err, upgradeservice.CodeTargetLayoutMismatch)
			}
			if !strings.Contains(err.Error(), "--target") {
				t.Fatalf("error %q does not ask for --target", err)
			}

			// An explicit --target states the layout, so it is never second-guessed.
			explicit := canonicalFixtureRoot(t)
			root, err = resolveTargetRoot(explicit)
			if err != nil || root != explicit {
				t.Fatalf("explicit target = %q/%v, want %q", root, err, explicit)
			}
		})
	}
}
