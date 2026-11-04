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
		// Every 1.14.x release creates the deleted-raw summary, v1.14.0 included.
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

// exitCodeContract is the whole typed vocabulary the service exports, one row
// per code: the exit every subcommand must report for it. The table is the
// contract, so a code that moves between the decision and retryable sets — or a
// new code that ships without a row — fails the test below instead of only a
// script that branches on the value.
var exitCodeContract = []struct {
	name string
	code upgradeservice.UpgradeErrorCode
	want int
}{
	// A decision the operator has to look at: exit 1 on every subcommand.
	{name: "source busy", code: upgradeservice.UpgradeErrorCode(upgradeservice.CodeSourceBusy), want: 1},
	{name: "v2 root running", code: upgradeservice.UpgradeErrorV2Running, want: 1},
	{name: "target locked", code: upgradeservice.UpgradeErrorTargetLocked, want: 1},
	{name: "reconcile mismatch", code: upgradeservice.UpgradeErrorReconcileMismatch, want: 1},
	{name: "reconcile target incomplete", code: upgradeservice.UpgradeErrorReconcileTargetIncomplete, want: 1},
	{name: "reconcile nothing compared", code: upgradeservice.UpgradeErrorReconcileNothingCompared, want: 1},
	{name: "reconcile not copied in", code: upgradeservice.UpgradeErrorReconcileNotCopiedIn, want: 1},
	{name: "reconcile stage closed", code: upgradeservice.UpgradeErrorReconcileStageClosed, want: 1},
	{name: "reconcile record incomplete", code: upgradeservice.UpgradeErrorReconcileRecordIncomplete, want: 1},
	{name: "store schema outdated", code: upgradeservice.UpgradeErrorStoreSchemaOutdated, want: 1},
	{name: "store locked", code: upgradeservice.UpgradeErrorStoreLocked, want: 1},
	{name: "migrate not copied in", code: upgradeservice.UpgradeErrorMigrateNotCopiedIn, want: 1},
	{name: "rollback not applicable", code: upgradeservice.UpgradeErrorRollbackNotApplicable, want: 1},
	{name: "rollback unproven", code: upgradeservice.UpgradeErrorRollbackUnproven, want: 1},
	{name: "rollback image unpinned", code: upgradeservice.UpgradeErrorRollbackImageUnpinned, want: 1},
	{name: "rollback image family", code: upgradeservice.UpgradeErrorRollbackImageFamily, want: 1},
	{name: "rollback volume unbound", code: upgradeservice.UpgradeErrorRollbackVolumeUnbound, want: 1},
	{name: "rollback roots overlap", code: upgradeservice.UpgradeErrorRollbackRootsOverlap, want: 1},
	{name: "rollback evidence stale", code: upgradeservice.UpgradeErrorRollbackEvidenceStale, want: 1},
	{name: "rollback probe endpoint", code: upgradeservice.UpgradeErrorRollbackProbeEndpoint, want: 1},
	{name: "rollback identity foreign", code: upgradeservice.UpgradeErrorRollbackIdentityForeign, want: 1},
	{name: "rollback probe self", code: upgradeservice.UpgradeErrorRollbackProbeSelf, want: 1},
	{name: "rollback port collision", code: upgradeservice.UpgradeErrorRollbackPortCollision, want: 1},
	{name: "rollback port busy", code: upgradeservice.UpgradeErrorRollbackPortBusy, want: 1},
	{name: "rollback v2 running", code: upgradeservice.UpgradeErrorRollbackV2Running, want: 1},
	{name: "external version unsupported", code: upgradeservice.UpgradeErrorExternalVersionUnsupported, want: 1},
	{name: "external document invalid", code: upgradeservice.UpgradeErrorExternalConfigDocumentInvalid, want: 1},
	{name: "external management rejected", code: upgradeservice.UpgradeErrorExternalManagementRejected, want: 1},
	{name: "external target moved", code: upgradeservice.UpgradeErrorExternalTargetMoved, want: 1},
	{name: "external v2 running", code: upgradeservice.UpgradeErrorExternalV2Running, want: 1},
	// Retryable or environmental: exit 2 on every subcommand, including the
	// source that moved, which the copy-in lane has always kept retryable.
	{name: "source changed", code: upgradeservice.UpgradeErrorSourceChanged, want: 2},
	{name: "target modified", code: upgradeservice.UpgradeErrorTargetModified, want: 2},
	{name: "target conflict", code: upgradeservice.UpgradeErrorTargetConflict, want: 2},
	{name: "snapshot failed", code: upgradeservice.UpgradeErrorSnapshotFailed, want: 2},
	{name: "external authority unavailable", code: upgradeservice.UpgradeErrorExternalAuthorityUnavailable, want: 2},
	{name: "external not applicable", code: upgradeservice.UpgradeErrorExternalNotApplicable, want: 2},
}

// TestExitCodePinsEveryRefusal pins the CLI contract one row per typed code.
// The rows carry a message like the service sends, so the mapping is proven for
// a real typed error and not just for the bare sentinel.
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
		{name: "nothing compared (sentinel)", err: upgradeservice.ErrReconcileNothingCompared, want: 1},
		{name: "usage", err: ErrUsage, want: 2},
	}
	for _, contract := range exitCodeContract {
		cases = append(cases, struct {
			name string
			err  error
			want int
		}{name: contract.name, err: typed(contract.code), want: contract.want})
	}
	seen := map[upgradeservice.UpgradeErrorCode]bool{}
	for _, contract := range exitCodeContract {
		if seen[contract.code] {
			t.Fatalf("exit-code table lists %q twice", contract.code)
		}
		seen[contract.code] = true
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

// TestEverySubcommandUsesTheExitCodeTable feeds each typed code through every
// way a subcommand reports it. The reporting helpers must keep the typed code in
// the chain and mark it reported without choosing an exit code; a helper that
// mapped every typed failure to exit 1 — as the rollback path did — fails here
// even though ExitCode itself is correct.
func TestEverySubcommandUsesTheExitCodeTable(t *testing.T) {
	reporters := []struct {
		subcommands string
		report      func(io.Writer, error) error
	}{
		{subcommands: "run, status, migrate, rollback", report: refuseTyped},
		{subcommands: "external-connect", report: reportExternalConnectFailure},
		{subcommands: "reconcile", report: func(_ io.Writer, err error) error { return err }},
	}
	for _, contract := range exitCodeContract {
		typed := &upgradeservice.UpgradeError{Code: contract.code, Message: "pinned by the exit-code table"}
		want := ExitCode(typed)
		for _, reporter := range reporters {
			reported := reporter.report(io.Discard, typed)
			if got := ExitCode(reported); got != want {
				t.Fatalf("%s reported %s as exit %d, want the table's %d", reporter.subcommands, contract.code, got, want)
			}
			if got, ok := upgradeservice.UpgradeErrorCodeOf(reported); !ok || got != contract.code {
				t.Fatalf("%s lost the typed code %s (got %q)", reporter.subcommands, contract.code, got)
			}
			if reporter.subcommands != "reconcile" && !errors.Is(reported, ErrReported) {
				t.Fatalf("%s did not mark %s reported, so the entry point prints it twice", reporter.subcommands, contract.code)
			}
		}
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

// TestEverySubcommandKeepsAChangedSourceRetryable is the M-1 regression through
// the CLI dispatcher: one typed code, one exit code. A source that moved is
// `upgrade_source_changed` in run, rollback --prepare and reconcile, and every
// one of them must report the table's retryable 2. The rollback path used to
// wrap every typed failure as a refusal (exit 1), so the same code exited 1
// there and 2 in run and reconcile.
func TestEverySubcommandKeepsAChangedSourceRetryable(t *testing.T) {
	const (
		sourceChanged = string(upgradeservice.UpgradeErrorSourceChanged)
		wantExit      = 2
	)

	// run: the copy-in fingerprints the source before and after the copy, so a
	// source that changes after the backup and before the post-copy fingerprint
	// is the real refusal. The mutation starts only once the backup manifest is
	// published (after the preflight and after the pre-copy fingerprint) and
	// keeps going until Run returns, so both fingerprints cannot agree.
	t.Run("run", func(t *testing.T) {
		source := copyReadySource(t)
		// A larger archive widens the remaining copy so the mutation lands
		// before the post-copy fingerprint rather than after it.
		archive := filepath.Join(source, "usage-archives", "2026-03", "segment-000001.jsonl")
		if err := os.WriteFile(archive, bytes.Repeat([]byte("x"), 8<<20), 0o600); err != nil {
			t.Fatalf("enlarge the fixture archive: %v", err)
		}
		target := t.TempDir()
		stop := make(chan struct{})
		mutated := make(chan struct{})
		go func() {
			defer close(mutated)
			manifest := filepath.Join(target, "upgrade-backup", "manifest.json")
			for {
				if _, err := os.Stat(manifest); err == nil {
					break
				}
				select {
				case <-stop:
					return
				case <-time.After(time.Millisecond):
				}
			}
			for {
				file, err := os.OpenFile(archive, os.O_APPEND|os.O_WRONLY, 0)
				if err == nil {
					_, _ = file.WriteString("{\"events\":1}\n")
					_ = file.Close()
				}
				select {
				case <-stop:
					return
				case <-time.After(time.Millisecond):
				}
			}
		}()
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		err := Run(context.Background(), []string{"run", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease,
			"--target", target}, &stdout, &stderr)
		close(stop)
		<-mutated
		code := ExitCode(err)
		if code != wantExit {
			t.Fatalf("run exit code = %d, want %d (error %v); stderr: %s; stdout: %s", code, wantExit, err, stderr.String(), stdout.String())
		}
		if !strings.Contains(stderr.String(), sourceChanged) {
			t.Fatalf("run stderr does not name %s (error %v): %s", sourceChanged, err, stderr.String())
		}
	})

	// rollback --prepare: the recorded copy-in names a source fingerprint this
	// directory no longer reproduces, so the plan refuses before writing.
	t.Run("rollback-prepare", func(t *testing.T) {
		source := readySource(t)
		target := t.TempDir()
		db, err := sqliterepo.Open(filepath.Join(target, "usage.sqlite"))
		if err != nil {
			t.Fatalf("open the v2 store: %v", err)
		}
		repository := upgradeoperation.New(db)
		if err := repository.Put(context.Background(), model.UpgradeOperation{
			Stage:                   model.UpgradeStageCopyIn,
			DomainsDone:             []string{model.UpgradeDomainSourceCopy},
			SourceKind:              string(upgradeservice.SourceKindUpgradedLegacy),
			SourceVersion:           bootstrap.PlannedDirectRelease,
			SourceRoot:              source,
			SourceFingerprintBefore: strings.Repeat("0", 64),
			SourceFingerprintAfter:  strings.Repeat("0", 64),
			BackupManifestSHA256:    strings.Repeat("0", 64),
		}); err != nil {
			_ = db.Close()
			t.Fatalf("record the copy-in: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close the v2 store: %v", err)
		}
		code, _, stderr := run(t, "rollback", "--prepare", "--legacy", source, "--target", target)
		if code != wantExit {
			t.Fatalf("rollback --prepare exit code = %d, want %d; stderr: %s", code, wantExit, stderr)
		}
		if !strings.Contains(stderr, sourceChanged) {
			t.Fatalf("rollback --prepare stderr does not name %s: %s", sourceChanged, stderr)
		}
	})

	// reconcile: a clean copy-in, then the source drifts, so the recorded
	// fingerprint and the directory no longer agree.
	t.Run("reconcile", func(t *testing.T) {
		source := copyReadyRunSource(t)
		target, bundle := reconcileRoots(t)
		if code, _, stderr := run(t, "run", "--legacy", source, "--source-version", bootstrap.PlannedDirectRelease,
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
		code, stdout, stderr := run(t, "reconcile", "--json", "--legacy", source,
			"--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle)
		if code != wantExit {
			t.Fatalf("reconcile exit code = %d, want %d; stderr: %s; stdout: %s", code, wantExit, stderr, stdout)
		}
		var output reconcileOutput
		if err := json.Unmarshal([]byte(stdout), &output); err != nil {
			t.Fatalf("decode reconcile output: %v; output: %s", err, stdout)
		}
		if output.ErrorCode != sourceChanged {
			t.Fatalf("reconcile error code = %q, want %q; output: %s", output.ErrorCode, sourceChanged, stdout)
		}
	})
}

// TestReconcileTakesTheSourceFenceReadOnly proves the reconcile step opens the
// 1.x source for its ownership lock without write access: the marker belongs to
// the stopped source, and a source preserved on a read-only mount must still
// prove exclusivity. The check is the marker's own mode — an O_RDWR open of a
// 0400 file fails for its owner, so this run could not complete through the old
// descriptor. A read-only mount itself (EROFS) is only qualified on Linux; the
// Darwin test host has no such mount.
func TestReconcileTakesTheSourceFenceReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not restrict root")
	}
	source := copyReadyRunSource(t)
	target, bundle := reconcileRoots(t)
	if code, _, stderr := run(t, "run", "--json", "--legacy", source,
		"--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle); code != 0 {
		t.Fatalf("copy in exit code = %d, want 0; stderr: %s", code, stderr)
	}
	marker := filepath.Join(source, "usage.sqlite.manager.lock")
	if err := os.Chmod(marker, 0o400); err != nil {
		t.Fatalf("make the source fence read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(marker, 0o600) })
	before := treeDigests(t, source)

	code, stdout, stderr := run(t, "reconcile", "--json", "--legacy", source,
		"--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle)
	if code != 0 {
		t.Fatalf("reconcile exit code = %d, want 0; stderr: %s; stdout: %s", code, stderr, stdout)
	}
	if after := treeDigests(t, source); !reflect.DeepEqual(before, after) {
		t.Fatalf("the reconciliation changed the source volume: %v -> %v", before, after)
	}
}

// TestReconcileRefusesATargetInsideTheSourceBeforeItTouchesIt reproduces review
// F3: the CLI took the v2 data-root fence before the service rejected a target
// inside the source, so the fence created the child directory and its marker
// inside the source tree it promises not to touch. That new entry also entered
// the source fingerprint, so the next run with a correct target refused with
// upgrade_source_changed until the child was removed by hand.
func TestReconcileRefusesATargetInsideTheSourceBeforeItTouchesIt(t *testing.T) {
	source := readySource(t)
	before := treeDigests(t, source)
	child := filepath.Join(source, "new-child")

	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	err := Run(context.Background(), []string{"reconcile", "--json", "--legacy", source,
		"--source-version", bootstrap.PlannedDirectRelease, "--target", child}, stdout, stderr)
	if err == nil {
		t.Fatalf("reconcile succeeded, want a refusal; stdout: %s", stdout)
	}
	if code := ExitCode(err); code != 2 {
		t.Fatalf("reconcile exit code = %d, want the usage 2; stderr: %s", code, stderr)
	}
	if !strings.Contains(err.Error(), "must not contain each other") {
		t.Fatalf("refusal %q does not name the containment rule", err)
	}
	if _, statErr := os.Stat(child); !os.IsNotExist(statErr) {
		t.Fatalf("the refused run created the target inside the source: %v", statErr)
	}
	if after := treeDigests(t, source); !reflect.DeepEqual(before, after) {
		t.Fatalf("the refused run changed the source volume: %v -> %v", before, after)
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

// TestRunHonorsTheV2DataRootFence is the CLI half of the shared-target
// regression. `upgrade run` used to take only the 1.x source fence, so it
// created, restored and migrated the v2 data root while another Manager process
// held that root's process lock. The refusal is typed and exits 1, and nothing
// the copy-in would write appears in the held root.
func TestRunHonorsTheV2DataRootFence(t *testing.T) {
	source := copyReadyRunSource(t)
	target := canonicalFixtureRoot(t)
	owner, err := processlock.Acquire(filepath.Join(target, "usage.sqlite"))
	if err != nil {
		t.Fatalf("take the v2 data root fence: %v", err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	before := treeDigests(t, target)
	sourceBefore := treeDigests(t, source)

	err = Run(context.Background(), []string{"run", "--json", "--legacy", source,
		"--source-version", bootstrap.PlannedDirectRelease, "--target", target},
		&bytes.Buffer{}, &bytes.Buffer{})
	if !errors.Is(err, upgradeservice.ErrV2Running) {
		t.Fatalf("run under a held v2 data root = %v, want %s", err, upgradeservice.ErrV2Running)
	}
	if code, ok := upgradeservice.UpgradeErrorCodeOf(err); !ok || code != upgradeservice.UpgradeErrorV2Running {
		t.Fatalf("run error code = %q/%t, want %s", code, ok, upgradeservice.UpgradeErrorV2Running)
	}
	if exit := ExitCode(err); exit != 1 {
		t.Fatalf("exit code = %d, want 1", exit)
	}
	after := treeDigests(t, target)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("the refused run changed the held v2 data root: %v -> %v", before, after)
	}
	for _, absent := range []string{"usage.sqlite", "data.key", "upgrade-backup", ".upgrade-copy-in"} {
		if _, statErr := os.Lstat(filepath.Join(target, absent)); !os.IsNotExist(statErr) {
			t.Fatalf("the refused run wrote %s (err=%v)", absent, statErr)
		}
	}
	if sourceAfter := treeDigests(t, source); !reflect.DeepEqual(sourceBefore, sourceAfter) {
		t.Fatalf("the refused run changed the 1.x source: %v -> %v", sourceBefore, sourceAfter)
	}
}

// TestRunTakesTheSourceFenceReadOnly proves the 1.x source is opened for its
// ownership lock without write access: the marker is a stopped source's own
// file, and a source preserved on a read-only mount must still prove
// exclusivity. The check is the marker's own mode — an O_RDWR open of a 0400
// file fails for its owner, so this run could not complete through the old
// descriptor. A read-only mount itself (EROFS) is only qualified on Linux; the
// Darwin test host has no such mount.
func TestRunTakesTheSourceFenceReadOnly(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission bits do not restrict root")
	}
	source := copyReadyRunSource(t)
	marker := filepath.Join(source, "usage.sqlite.manager.lock")
	if err := os.Chmod(marker, 0o400); err != nil {
		t.Fatalf("make the source fence read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(marker, 0o600) })
	before := treeDigests(t, source)
	target := t.TempDir()
	bundle := filepath.Join(t.TempDir(), "backup")

	code, _, stderr := run(t, "run", "--json", "--legacy", source,
		"--source-version", bootstrap.PlannedDirectRelease, "--target", target, "--bundle", bundle)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr: %s", code, stderr)
	}
	if after := treeDigests(t, source); !reflect.DeepEqual(before, after) {
		t.Fatalf("the copy-in changed the source volume: %v -> %v", before, after)
	}
}
