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
	"strings"
	"sync"
	"testing"
	"time"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
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

func TestExitCodeMapping(t *testing.T) {
	if code := ExitCode(nil); code != 0 {
		t.Fatalf("nil exit code = %d, want 0", code)
	}
	if code := ExitCode(ErrBlocked); code != 1 {
		t.Fatalf("blocked exit code = %d, want 1", code)
	}
	if code := ExitCode(ErrUsage); code != 2 {
		t.Fatalf("usage exit code = %d, want 2", code)
	}
	if !errors.Is(ErrBlocked, ErrBlocked) {
		t.Fatal("blocked error lost its identity")
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

// TestStatusSucceedsAgainstALiveWriterWithinItsRetryBudget is the regression for
// N1: `upgrade status` used to fail every time it raced a Manager that was
// writing, because one snapshot attempt was all it made. A live Manager writes
// in bursts, so a bounded retry finds a quiet window.
func TestStatusSucceedsAgainstALiveWriterWithinItsRetryBudget(t *testing.T) {
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
	attempts, raced := 0, 0
	original := openSnapshot
	openSnapshot = func(ctx context.Context, path string, scratch string) (*upgradeservice.Snapshot, error) {
		attempts++
		snapshot, err := original(ctx, path, scratch)
		if err != nil && errors.Is(err, upgradeservice.ErrSourceChanged) {
			raced++
			// The writer was live through this attempt. Let it go quiet, like a
			// Manager between bursts, so a retry sees a stopped writer.
			stopOnce.Do(func() { close(stop) })
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

	runErr, stdout, stderr := runError(t, "status", "--json", "--db-path", databasePath)
	<-stopped
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	if runErr != nil {
		t.Fatalf("status against a live writer: %v; stderr: %s", runErr, stderr)
	}
	if raced == 0 {
		t.Fatal("no snapshot attempt raced the live writer; the retry was never exercised")
	}
	if attempts > statusSnapshotAttempts {
		t.Fatalf("snapshot attempts = %d, want at most %d", attempts, statusSnapshotAttempts)
	}
	var output statusOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatalf("decode status: %v; output: %s", err, stdout)
	}
	if !output.Recorded || output.Operation == nil || output.Operation.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("status = %+v, want the recorded operation", output)
	}
	t.Logf("snapshot attempts against a live writer: %d (raced %d)", attempts, raced)
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
