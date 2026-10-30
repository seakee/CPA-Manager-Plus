package upgrade

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
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
