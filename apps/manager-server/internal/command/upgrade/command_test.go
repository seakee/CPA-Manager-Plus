package upgrade

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

// readySource builds a minimal 1.x data directory that preflight accepts: the
// base schema, a data key, an archive and the v1 ownership lock file.
func readySource(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	logical := []string{
		`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`,
		`create table usage_events (id integer primary key autoincrement, event_hash text not null unique, timestamp_ms integer not null)`,
		`create table usage_event_identity_ledger (event_hash text primary key, raw_event_id integer, timestamp_ms integer, bucket_ms integer)`,
		`create table usage_monitoring_event_projection_v1 (event_id integer primary key, timestamp_ms integer not null)`,
		`create table usage_monitoring_rollup_state (rollup_name text primary key, schema_version integer not null, status text not null, coverage_event_id integer not null default 0, updated_at_ms integer not null default 0)`,
		`create table usage_archive_event_refs (event_hash text primary key, run_id text not null, segment_sequence integer not null, raw_event_id integer not null, timestamp_ms integer not null, archived_at_ms integer not null, raw_deleted_at_ms integer)`,
		// Every 1.14.x release creates the deleted-raw summary, v1.14.0 included.
		`create table usage_archive_deleted_coverage_daily (utc_day integer primary key, deleted_event_count integer not null, min_timestamp_ms integer not null, max_timestamp_ms integer not null)`,
		`insert into usage_monitoring_rollup_state (rollup_name, schema_version, status, coverage_event_id) values ('projection_v1', 1, 'ready', 1)`,
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
	code, stdout, stderr := run(t, "preflight", "--legacy", root, "--json")
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
	code, stdout, _ := run(t, "preflight", "--legacy", root, "--json")
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

// copyReadySource builds a stopped 1.x volume that both the preflight and the
// backup accept: the carried-forward usage schema with rows, no v2-only tables,
// a real data key, archives and the v1 ownership marker.
func copyReadySource(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve fixture root: %v", err)
	}
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
		rows, err := conn.QueryContext(context.Background(),
			`select name from sqlite_master where type = 'table' and name like ? escape '\'`, pattern)
		if err != nil {
			_ = conn.Close()
			_ = db.Close()
			t.Fatalf("list v2-only fixture tables: %v", err)
		}
		names := []string{}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				_ = rows.Close()
				_ = conn.Close()
				_ = db.Close()
				t.Fatalf("scan v2-only fixture table: %v", err)
			}
			names = append(names, name)
		}
		if err := rows.Close(); err != nil {
			_ = conn.Close()
			_ = db.Close()
			t.Fatalf("close v2-only table list: %v", err)
		}
		for _, name := range names {
			if _, err := conn.ExecContext(context.Background(), `drop table `+name); err != nil {
				_ = conn.Close()
				_ = db.Close()
				t.Fatalf("drop v2-only fixture table %s: %v", name, err)
			}
		}
	}
	// The cache-accounting revision and the latency-covering index arrived after
	// the pinned release; a source that carries them is a different source than
	// the one this pin accepts, so the fixture must not have them. The
	// deleted-raw summary is not one of them: every 1.14.x release creates it,
	// the pinned one included, so it stays.
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
	if err := os.WriteFile(filepath.Join(root, "usage-archives", "2026-03", "segment-000001.jsonl"), []byte(`{"events":1}`+"\n"), 0o600); err != nil {
		t.Fatalf("write fixture archive: %v", err)
	}
	if err := os.WriteFile(databasePath+".manager.lock", nil, 0o600); err != nil {
		t.Fatalf("write fixture source lock: %v", err)
	}
	return root
}

func TestRunCopiesAStoppedSourceAndRecordsTheStage(t *testing.T) {
	source := copyReadySource(t)
	target := t.TempDir()
	bundle := filepath.Join(t.TempDir(), "backup")

	code, stdout, stderr := run(t, "run", "--json", "--legacy", source, "--target", target, "--bundle", bundle)
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
	code, again, stderr := run(t, "run", "--json", "--legacy", source, "--target", target, "--bundle", bundle)
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
	source := copyReadySource(t)
	// Removing the ownership marker makes the source unprovably stopped.
	if err := os.Remove(filepath.Join(source, "usage.sqlite.manager.lock")); err != nil {
		t.Fatalf("remove source lock: %v", err)
	}
	target := t.TempDir()
	code, stdout, stderr := run(t, "run", "--json", "--legacy", source, "--target", target)
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
	source := copyReadySource(t)
	target, bundle := reconcileRoots(t)
	if code, _, stderr := run(t, "run", "--json", "--legacy", source, "--target", target, "--bundle", bundle); code != 0 {
		t.Fatalf("copy in exit code = %d, want 0; stderr: %s", code, stderr)
	}

	code, stdout, stderr := run(t, "reconcile", "--json", "--legacy", source, "--target", target, "--bundle", bundle)
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
}

func TestReconcileExitsOneOnAnUnacknowledgedMismatch(t *testing.T) {
	source := copyReadySource(t)
	target, bundle := reconcileRoots(t)
	if code, _, stderr := run(t, "run", "--json", "--legacy", source, "--target", target, "--bundle", bundle); code != 0 {
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

	code, stdout, stderr := run(t, "reconcile", "--json", "--legacy", source, "--target", target, "--bundle", bundle)
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
	code, stdout, stderr = run(t, "reconcile", "--json", "--legacy", source, "--target", target,
		"--bundle", bundle, "--acknowledge", day)
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

func TestTargetDefaultsToTheManagerDataDirectory(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("USAGE_DATA_DIR", dataDir)
	t.Setenv("USAGE_DB_PATH", "")
	t.Setenv("CPA_MANAGER_CONFIG", filepath.Join(t.TempDir(), "absent-config.json"))
	root, err := resolveTargetRoot("")
	if err != nil {
		t.Fatalf("resolve default target: %v", err)
	}
	if root != dataDir {
		t.Fatalf("default target = %q, want the Manager data directory %q", root, dataDir)
	}
}
