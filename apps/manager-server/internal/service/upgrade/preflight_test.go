package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const msPerDay = int64(86_400_000)

// v1SchemaStatements is the v1.14.10 DDL for the tables preflight reads, copied
// from the released migration. It is deliberately hand-written: a fixture built
// by the v2 migration is not a 1.x source, and the point of these tests is that
// a real WAL-mode source is inspected without being written to.
var v1SchemaStatements = []string{
	`create table settings (
		key text primary key,
		value text not null,
		updated_at_ms integer not null
	)`,
	`create table usage_events (
		id integer primary key autoincrement,
		request_id text,
		event_hash text not null unique,
		timestamp_ms integer not null,
		timestamp text not null,
		provider text,
		executor_type text,
		model text not null,
		endpoint text,
		method text,
		path text,
		client_ip text,
		x_forwarded_for text,
		user_agent text,
		auth_type text,
		auth_index text,
		source text,
		source_hash text,
		api_key_hash text,
		account_snapshot text,
		auth_label_snapshot text,
		auth_file_snapshot text,
		auth_provider_snapshot text,
		auth_account_id_snapshot text,
		auth_project_id_snapshot text,
		auth_snapshot_at_ms integer,
		requested_model text,
		resolved_model text,
		reasoning_effort text,
		service_tier text,
		request_service_tier text,
		response_service_tier text,
		cache_input_mode text,
		input_tokens integer not null default 0,
		output_tokens integer not null default 0,
		reasoning_tokens integer not null default 0,
		cached_tokens integer not null default 0,
		cache_tokens integer not null default 0,
		cache_read_tokens integer not null default 0,
		cache_creation_tokens integer not null default 0,
		normalized_uncached_input_tokens integer,
		normalized_total_input_tokens integer,
		normalized_cache_read_tokens integer,
		normalized_cache_creation_tokens integer,
		total_tokens integer not null default 0,
		latency_ms integer,
		ttft_ms integer,
		failed integer not null default 0,
		fail_status_code integer,
		fail_summary text,
		response_metadata_json text,
		header_quota_recover_at_ms integer,
		header_quota_used_percent real,
		header_quota_plan_type text,
		header_error_kind text,
		header_error_code text,
		header_trace_id text,
		fail_body text,
		raw_json text,
		response_model text,
		session_id text,
		parent_session_id text,
		access_token_sha256 text,
		generate integer,
		stream integer,
		created_at_ms integer not null
	)`,
	`create table usage_event_identity_ledger (
		event_hash text primary key,
		raw_event_id integer,
		timestamp_ms integer not null,
		bucket_ms integer not null,
		aggregate_schema_version integer not null default 0,
		aggregate_structure_revision text not null default '',
		first_seen_at_ms integer not null,
		updated_at_ms integer not null
	)`,
	`create table usage_monitoring_rollup_state (
		rollup_name text primary key,
		schema_version integer not null,
		structure_revision text not null default '',
		status text not null,
		backfill_last_event_id integer not null default 0,
		coverage_event_id integer not null default 0,
		target_event_id integer not null default 0,
		processed_events integer not null default 0,
		last_run_started_at_ms integer,
		updated_at_ms integer not null default 0,
		finished_at_ms integer,
		last_error text
	)`,
	`create table usage_monitoring_event_projection_v1 (
		event_id integer primary key,
		timestamp_ms integer not null,
		search_text text not null,
		account_key text not null,
		provider text not null,
		executor_type text not null,
		model text not null,
		requested_model text not null default '',
		analytics_model text not null,
		resolved_model text not null,
		auth_index text not null,
		source text not null,
		source_hash text not null,
		api_key_hash text not null,
		account_snapshot text not null,
		auth_label_snapshot text not null,
		auth_file_snapshot text not null,
		auth_provider_snapshot text not null,
		auth_account_id_snapshot text not null default '',
		auth_project_id_snapshot text not null,
		reasoning_effort text not null,
		service_tier text not null,
		failed integer not null,
		latency_ms integer,
		input_tokens integer not null,
		output_tokens integer not null,
		reasoning_tokens integer not null,
		cached_tokens integer not null,
		cache_tokens integer not null,
		cache_read_tokens integer not null,
		cache_creation_tokens integer not null,
		normalized_total_input_tokens integer not null,
		total_tokens integer not null,
		header_quota_plan_type text not null,
		header_error_kind text not null,
		header_error_code text not null,
		header_trace_id text not null,
		updated_at_ms integer not null
	)`,
	`create table usage_archive_runs (
		id text primary key,
		mode text not null default 'manual',
		schema_version integer not null,
		format text not null,
		status text not null,
		resume_status text,
		requested_stage text,
		progress_phase text,
		progress_current integer not null default 0,
		progress_total integer not null default 0,
		progress_unit text,
		progress_updated_at_ms integer,
		cutoff_timestamp_ms integer not null,
		target_event_id integer not null,
		event_count integer not null,
		estimated_bytes integer not null default 0,
		last_archived_event_id integer not null default 0,
		archived_event_count integer not null default 0,
		archived_uncompressed_bytes integer not null default 0,
		archived_compressed_bytes integer not null default 0,
		archive_digest text,
		manifest_file text,
		manifest_sha256 text,
		last_deleted_event_id integer not null default 0,
		deleted_event_count integer not null default 0,
		created_at_ms integer not null,
		updated_at_ms integer not null,
		started_at_ms integer,
		archived_at_ms integer,
		verified_at_ms integer,
		delete_started_at_ms integer,
		completed_at_ms integer,
		last_error text
	)`,
	`create index idx_usage_archive_runs_status_updated
		on usage_archive_runs(status, updated_at_ms)`,
	`create table usage_archive_segments (
		run_id text not null,
		sequence integer not null,
		status text not null,
		file_name text not null,
		first_event_id integer not null,
		last_event_id integer not null,
		min_timestamp_ms integer not null,
		max_timestamp_ms integer not null,
		event_count integer not null,
		uncompressed_bytes integer not null,
		compressed_bytes integer not null,
		content_sha256 text not null,
		event_hash_digest text not null,
		created_at_ms integer not null,
		verified_at_ms integer,
		primary key (run_id, sequence),
		unique (file_name),
		foreign key (run_id) references usage_archive_runs(id) on delete cascade
	)`,
	`create index idx_usage_archive_segments_run_event
		on usage_archive_segments(run_id, last_event_id)`,
	`create table usage_archive_event_refs (
		event_hash text primary key,
		run_id text not null,
		segment_sequence integer not null,
		raw_event_id integer not null,
		timestamp_ms integer not null,
		archived_at_ms integer not null,
		raw_deleted_at_ms integer,
		unique (run_id, raw_event_id),
		foreign key (run_id, segment_sequence)
			references usage_archive_segments(run_id, sequence) on delete cascade,
		foreign key (event_hash) references usage_event_identity_ledger(event_hash)
	)`,
	`create index idx_usage_archive_event_refs_run_deleted
		on usage_archive_event_refs(run_id, raw_deleted_at_ms, raw_event_id)`,
	`create index idx_usage_archive_event_refs_timestamp_deleted
		on usage_archive_event_refs(timestamp_ms, raw_deleted_at_ms)`,
	`create table usage_archive_deleted_coverage_daily (
		utc_day integer primary key,
		deleted_event_count integer not null,
		min_timestamp_ms integer not null,
		max_timestamp_ms integer not null
	)`,
}

// v1SeedStatements are the rows a stopped v1.14.10 Manager leaves behind: the
// derived-format markers in settings, the rollup state, a few live events, one
// archive run with a projection-covered deleted event and the matching deleted
// coverage day.
var v1SeedStatements = []string{
	`insert into settings (key, value, updated_at_ms) values
		('bootstrap_state_v1', '{"version":1,"status":"ready","adminReady":true,"projectInit":true}', 1),
		('usage_monitoring_model_format_version', '1', 1),
		('usage_account_history_identity_format_version', '` + usageidentity.AccountHistoryStructureRevision() + `', 1),
		('usage_dashboard_hourly_format_version', '3', 1),
		('usage_cache_accounting_semantics_revision', '3', 1)`,
	`insert into usage_events (id, event_hash, timestamp_ms, timestamp, model, input_tokens, output_tokens, total_tokens, created_at_ms)
		values (1, 'live-1', 1_772_000_000_000, '2026-02-01T00:00:00Z', 'gpt-5', 10, 5, 15, 1_772_000_000_000),
		       (2, 'live-2', 1_772_000_060_000, '2026-02-01T00:01:00Z', 'gpt-5', 11, 6, 17, 1_772_000_060_000)`,
	`insert into usage_monitoring_rollup_state (rollup_name, schema_version, structure_revision, status, coverage_event_id, target_event_id, updated_at_ms) values
		('codex_legacy_identity_v1', 1, '', 'ready', 0, 2, 1),
		('stats_v1', 1, '', 'ready', 0, 2, 1),
		('metadata_v1', 1, '', 'ready', 0, 2, 1),
		('projection_v1', 1, '` + usageidentity.MonitoringProjectionStructureRevision() + `', 'ready', 5, 2, 1)`,
	`insert into usage_archive_runs (id, schema_version, format, status, cutoff_timestamp_ms, target_event_id, event_count, created_at_ms, updated_at_ms)
		values ('run-1', 1, 'gzip-jsonl-v1', 'completed', 1_772_000_000_000, 3, 1, 1, 1)`,
	`insert into usage_archive_segments (run_id, sequence, status, file_name, first_event_id, last_event_id, min_timestamp_ms, max_timestamp_ms, event_count, uncompressed_bytes, compressed_bytes, content_sha256, event_hash_digest, created_at_ms)
		values ('run-1', 1, 'verified', 'usage-archives/run-1/segment-000001.jsonl.gz', 3, 3, 1_772_000_120_000, 1_772_000_120_000, 1, 100, 80, 'sha', 'digest', 1)`,
	`insert into usage_event_identity_ledger (event_hash, raw_event_id, timestamp_ms, bucket_ms, first_seen_at_ms, updated_at_ms)
		values ('deleted-1', 3, 1_772_000_120_000, 1_772_000_100_000, 1, 1)`,
	`insert into usage_archive_event_refs (event_hash, run_id, segment_sequence, raw_event_id, timestamp_ms, archived_at_ms, raw_deleted_at_ms)
		values ('deleted-1', 'run-1', 1, 3, 1_772_000_120_000, 1_772_000_200_000, 1_772_000_300_000)`,
	`insert into usage_monitoring_event_projection_v1 (event_id, timestamp_ms, search_text, account_key, provider, executor_type, model, analytics_model, resolved_model, auth_index, source, source_hash, api_key_hash, account_snapshot, auth_label_snapshot, auth_file_snapshot, auth_provider_snapshot, auth_project_id_snapshot, reasoning_effort, service_tier, failed, input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_tokens, cache_read_tokens, cache_creation_tokens, normalized_total_input_tokens, total_tokens, header_quota_plan_type, header_error_kind, header_error_code, header_trace_id, updated_at_ms)
		values (3, 1_772_000_120_000, 'deleted-1', 'acct', 'codex', 'http', 'gpt-5', 'gpt-5', 'gpt-5', '1', 'file', 'h', 'k', 'a', 'b', 'c', 'd', 'e', 'low', 'default', 0, 10, 5, 0, 0, 0, 0, 0, 15, 15, '', '', '', '', 1)`,
	`insert into usage_archive_deleted_coverage_daily (utc_day, deleted_event_count, min_timestamp_ms, max_timestamp_ms)
		select timestamp_ms / 86400000, 1, timestamp_ms, timestamp_ms from usage_archive_event_refs where raw_deleted_at_ms is not null`,
}

type sourceFixture struct {
	root   string
	dbPath string
}

// newSourceFixture builds a realistic stopped v1.14.10 data directory: a WAL-mode
// usage.sqlite with the released schema and rows, data.key, an archive tree and
// the v1 ownership lock file. The returned source has no -wal/-shm sidecars,
// exactly like a cleanly stopped v1 writer.
func newSourceFixture(t *testing.T, configure func(t *testing.T, db *sql.DB)) sourceFixture {
	t.Helper()
	root := t.TempDir()
	writeFixtureFile(t, filepath.Join(root, archivesDirName, "2026-03", "segment-000001.jsonl"), `{"events":1}`+"\n")
	writeFixtureFile(t, filepath.Join(root, archivesDirName, "2026-03", "segment-000002.jsonl"), `{"events":2}`+"\n")
	writeFixtureFile(t, filepath.Join(root, dataKeyFileName), "fixture-data-key\n")

	dbPath := filepath.Join(root, databaseFileName)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	db.SetMaxOpenConns(1)
	statements := append([]string{`pragma journal_mode = WAL`}, v1SchemaStatements...)
	statements = append(statements, v1SeedStatements...)
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("apply fixture statement: %v", err)
		}
	}
	if configure != nil {
		configure(t, db)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture database: %v", err)
	}
	writeFixtureFile(t, dbPath+".manager.lock", "")
	return sourceFixture{root: root, dbPath: dbPath}
}

func writeFixtureFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create fixture directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
}

func execFixture(t *testing.T, db *sql.DB, statement string, args ...any) {
	t.Helper()
	if _, err := db.Exec(statement, args...); err != nil {
		t.Fatalf("fixture statement failed: %v", err)
	}
}

// archivedRef inserts one archived event whose raw row is gone.
func archivedRef(t *testing.T, db *sql.DB, eventHash string, rawEventID int64, timestampMS int64) {
	t.Helper()
	execFixture(t, db, `insert into usage_event_identity_ledger (event_hash, raw_event_id, timestamp_ms, bucket_ms, first_seen_at_ms, updated_at_ms)
		values (?, ?, ?, 0, 1, 1)`, eventHash, rawEventID, timestampMS)
	execFixture(t, db, `insert into usage_archive_event_refs (
		event_hash, run_id, segment_sequence, raw_event_id, timestamp_ms, archived_at_ms, raw_deleted_at_ms
	) values (?, 'run-1', 1, ?, ?, ?, ?)`, eventHash, rawEventID, timestampMS, timestampMS, timestampMS)
}

func addProjection(t *testing.T, db *sql.DB, eventID int64, timestampMS int64) {
	t.Helper()
	execFixture(t, db, `insert into usage_monitoring_event_projection_v1 (event_id, timestamp_ms, search_text, account_key, provider, executor_type, model, analytics_model, resolved_model, auth_index, source, source_hash, api_key_hash, account_snapshot, auth_label_snapshot, auth_file_snapshot, auth_provider_snapshot, auth_project_id_snapshot, reasoning_effort, service_tier, failed, input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_tokens, cache_read_tokens, cache_creation_tokens, normalized_total_input_tokens, total_tokens, header_quota_plan_type, header_error_kind, header_error_code, header_trace_id, updated_at_ms)
		values (?, ?, 'search', 'acct', 'codex', 'http', 'gpt-5', 'gpt-5', 'gpt-5', '1', 'file', 'h', 'k', 'a', 'b', 'c', 'd', 'e', 'low', 'default', 0, 1, 1, 0, 0, 0, 0, 0, 1, 1, '', '', '', '', 1)`, eventID, timestampMS)
}

// rebuildDeletedCoverage makes the per-day deleted coverage table agree with the
// archive refs, so a test that adds refs does not also report a coverage gap.
func rebuildDeletedCoverage(t *testing.T, db *sql.DB) {
	t.Helper()
	execFixture(t, db, `delete from usage_archive_deleted_coverage_daily`)
	execFixture(t, db, `insert into usage_archive_deleted_coverage_daily
		(utc_day, deleted_event_count, min_timestamp_ms, max_timestamp_ms)
		select timestamp_ms / 86400000, count(*), min(timestamp_ms), max(timestamp_ms)
		from usage_archive_event_refs where raw_deleted_at_ms is not null
		group by timestamp_ms / 86400000`)
}

func setWatermark(t *testing.T, db *sql.DB, coverageEventID int64) {
	t.Helper()
	execFixture(t, db, `update usage_monitoring_rollup_state
		set coverage_event_id = ? where rollup_name = 'projection_v1'`, coverageEventID)
}

func setProjectionState(t *testing.T, db *sql.DB, revision string, status string) {
	t.Helper()
	execFixture(t, db, `update usage_monitoring_rollup_state
		set structure_revision = ?, status = ? where rollup_name = 'projection_v1'`, revision, status)
}

func declared() Options {
	return Options{DeclaredVersion: pinnedSourceVersion()}
}

func preflight(t *testing.T, root string, options Options) Report {
	t.Helper()
	options.LegacyRoot = root
	report, err := Preflight(context.Background(), options)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	return report
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

func checkByName(report Report, name string) (Check, bool) {
	for _, check := range report.Checks {
		if check.Name == name {
			return check, true
		}
	}
	return Check{}, false
}

func requireBlocked(t *testing.T, report Report, want ...string) {
	t.Helper()
	if report.Ready {
		t.Fatalf("ready = true, want blocked %v", want)
	}
	if len(report.Blocked) != len(want) {
		t.Fatalf("blocked = %v, want %v", report.Blocked, want)
	}
	for index, code := range want {
		if report.Blocked[index] != code {
			t.Fatalf("blocked = %v, want %v", report.Blocked, want)
		}
	}
}

func TestPreflightIsReadyOnAV11410SourceWithThePinnedDeclaration(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	report := preflight(t, fixture.root, declared())
	if !report.Ready {
		t.Fatalf("ready = false, blocked = %v, checks = %+v", report.Blocked, report.Checks)
	}
	if report.SourceKind != SourceKindLegacyManager {
		t.Fatalf("source kind = %q, want %q", report.SourceKind, SourceKindLegacyManager)
	}
	if report.SourceVersion != pinnedSourceVersion() {
		t.Fatalf("source version = %q, want %q", report.SourceVersion, pinnedSourceVersion())
	}
	if len(report.Fingerprint) != 64 {
		t.Fatalf("fingerprint = %q, want 64 hex characters", report.Fingerprint)
	}
	wantChecks := []string{CheckSourceOwnership, CheckSourceVersion, CheckFingerprint, CheckDisk, CheckCPAClassification, CheckParity}
	if len(report.Checks) != len(wantChecks) {
		t.Fatalf("checks = %d, want %d: %+v", len(report.Checks), len(wantChecks), report.Checks)
	}
	for index, name := range wantChecks {
		if report.Checks[index].Name != name {
			t.Fatalf("check %d = %q, want %q", index, report.Checks[index].Name, name)
		}
		if report.Checks[index].Status != StatusPass {
			t.Fatalf("check %q status = %q, want pass: %+v", name, report.Checks[index].Status, report.Checks[index])
		}
	}
	if report.ParityGaps.any() || len(report.ParityGaps.AffectedDates) != 0 {
		t.Fatalf("parity gaps = %+v, want none", report.ParityGaps)
	}
	if !report.ParityGaps.ProjectionStateUsable {
		t.Fatalf("projection state unusable: %s", report.ParityGaps.ProjectionStateReason)
	}
	if report.ParityGaps.DeletedEventRefs != 1 {
		t.Fatalf("deleted event refs = %d, want 1", report.ParityGaps.DeletedEventRefs)
	}
}

func TestPreflightFixtureIsARealWalDatabase(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	header := make([]byte, 20)
	file, err := os.Open(fixture.dbPath)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	if _, err := file.ReadAt(header, 0); err != nil {
		_ = file.Close()
		t.Fatalf("read fixture header: %v", err)
	}
	_ = file.Close()
	if string(header[0:16]) != "SQLite format 3\x00" {
		t.Fatalf("fixture header = %q, want a SQLite database", header[0:16])
	}
	if header[18] != 2 || header[19] != 2 {
		t.Fatalf("write/read version = %d/%d, want 2/2 (WAL)", header[18], header[19])
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(fixture.dbPath + suffix); !os.IsNotExist(err) {
			t.Fatalf("fixture sidecar %s exists, want a cleanly stopped writer", suffix)
		}
	}
}

func TestPreflightDoesNotWriteTheSource(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	scratch := t.TempDir()
	before := snapshotTree(t, fixture.root)
	report := preflight(t, fixture.root, Options{
		DeclaredVersion: pinnedSourceVersion(),
		ScratchDir:      scratch,
	})
	if report.Fingerprint == "" {
		t.Fatal("preflight produced no fingerprint")
	}
	after := snapshotTree(t, fixture.root)
	if len(before) != len(after) {
		t.Fatalf("source tree entry count changed: %d -> %d (%v -> %v)", len(before), len(after), before, after)
	}
	for path, digest := range before {
		if after[path] != digest {
			t.Fatalf("source entry %s changed: %q -> %q", path, digest, after[path])
		}
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(fixture.dbPath + suffix); !os.IsNotExist(err) {
			t.Fatalf("preflight created %s beside the source", filepath.Base(fixture.dbPath+suffix))
		}
	}
	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatalf("read scratch: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch directory was not cleaned up: %v", entries)
	}
}

func TestPreflightWorksOnAReadOnlySourceTree(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	// A stopped source is normally preserved read-only, including the ownership
	// marker: preflight must not need write access to prove exclusivity.
	walkErr := filepath.WalkDir(fixture.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.Chmod(path, 0o555)
		}
		return os.Chmod(path, 0o444)
	})
	if walkErr != nil {
		t.Fatalf("make source read-only: %v", walkErr)
	}
	t.Cleanup(func() {
		_ = filepath.WalkDir(fixture.root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				return os.Chmod(path, 0o755)
			}
			return os.Chmod(path, 0o600)
		})
	})

	report := preflight(t, fixture.root, declared())
	if !report.Ready {
		t.Fatalf("ready = false on a read-only source, blocked = %v, checks = %+v", report.Blocked, report.Checks)
	}
	for _, code := range report.Blocked {
		if code == CodeSourceBusy {
			t.Fatal("read-only source was reported busy")
		}
	}
}

// TestPreflightReadsUncheckpointedWalRows proves the snapshot carries the WAL:
// a stopped writer can leave committed rows only in usage.sqlite-wal.
func TestPreflightReadsUncheckpointedWalRows(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	writer, err := sql.Open("sqlite", fixture.dbPath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer func() { _ = writer.Close() }()
	writer.SetMaxOpenConns(1)
	execFixture(t, writer, `pragma journal_mode = wal`)
	execFixture(t, writer, `pragma wal_autocheckpoint = 0`)
	setWatermark(t, writer, 100)
	archivedRef(t, writer, "wal-only", 40, time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC).UnixMilli())

	walPath := fixture.dbPath + "-wal"
	walBefore := mustDigest(t, walPath)
	report := preflight(t, fixture.root, declared())
	if report.ParityGaps.MissingProjection != 1 {
		t.Fatalf("missing projection = %d, want 1: a row that exists only in -wal was not read",
			report.ParityGaps.MissingProjection)
	}
	if got := mustDigest(t, walPath); got != walBefore {
		t.Fatalf("preflight changed the source WAL: %s -> %s", walBefore, got)
	}
}

func mustDigest(t *testing.T, path string) string {
	t.Helper()
	digest, err := fileContentDigest(path)
	if err != nil {
		t.Fatalf("digest %s: %v", filepath.Base(path), err)
	}
	return digest
}

func TestPreflightReportsSourceBusyWhenLockIsHeld(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	held, err := processlock.AcquireExisting(fixture.dbPath)
	if err != nil {
		t.Fatalf("hold source lock: %v", err)
	}
	defer func() { _ = held.Close() }()

	report := preflight(t, fixture.root, declared())
	requireBlocked(t, report, CodeSourceBusy)
	check, found := checkByName(report, CheckSourceOwnership)
	if !found {
		t.Fatal("source ownership check missing")
	}
	if check.Status != StatusBlocked || check.Code != CodeSourceBusy {
		t.Fatalf("ownership check = %+v, want blocked %s", check, CodeSourceBusy)
	}
	version, _ := checkByName(report, CheckSourceVersion)
	if version.Status != StatusSkipped {
		t.Fatalf("version check = %+v, want skipped: a busy source is never read", version)
	}
}

func TestPreflightReportsMissingLockFile(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	if err := os.Remove(fixture.dbPath + ".manager.lock"); err != nil {
		t.Fatalf("remove lock file: %v", err)
	}
	report := preflight(t, fixture.root, declared())
	requireBlocked(t, report, CodeSourceBusy)
}

func TestPreflightReportsMissingDatabase(t *testing.T) {
	root := t.TempDir()
	report := preflight(t, root, declared())
	if report.SourceKind != SourceKindMissingDatabase {
		t.Fatalf("source kind = %q, want %q", report.SourceKind, SourceKindMissingDatabase)
	}
	// An empty directory has neither the ownership marker nor the database; the
	// first failing condition is the ownership fence.
	requireBlocked(t, report, CodeSourceBusy)
	if check, found := checkByName(report, CheckSourceOwnership); !found || check.Status != StatusBlocked {
		t.Fatalf("ownership check = %+v, want blocked", check)
	}
}

func TestPreflightBlocksUndeclaredSourceVersion(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	report := preflight(t, fixture.root, Options{})
	requireBlocked(t, report, CodeSourceVersionUndeclared)
	check, found := checkByName(report, CheckSourceVersion)
	if !found || check.Status != StatusBlocked || check.Code != CodeSourceVersionUndeclared {
		t.Fatalf("version check = %+v, want blocked %s", check, CodeSourceVersionUndeclared)
	}
	if report.SourceVersion != VersionUnknown {
		t.Fatalf("source version = %q, want %q", report.SourceVersion, VersionUnknown)
	}
}

func TestPreflightBlocksDeclaredVersionThatIsNotPinned(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	pinned := pinnedSourceVersion()
	other := "v9.9.9"
	if pinned == other {
		other = "v0.0.1"
	}
	report := preflight(t, fixture.root, Options{DeclaredVersion: other})
	requireBlocked(t, report, CodeSourceVersionUnsupported)
	check, found := checkByName(report, CheckSourceVersion)
	if !found || check.Status != StatusBlocked || check.Code != CodeSourceVersionUnsupported {
		t.Fatalf("version check = %+v", check)
	}
	for _, want := range []string{pinned, other} {
		if !strings.Contains(check.Detail, want) {
			t.Fatalf("version detail %q does not carry %q", check.Detail, want)
		}
	}
}

func TestPreflightBlocksSourceThatAlreadyCarriesV2Tables(t *testing.T) {
	fixture := newSourceFixture(t, func(t *testing.T, db *sql.DB) {
		execFixture(t, db, `create table gateway_api_key_identities (id integer primary key)`)
	})
	report := preflight(t, fixture.root, declared())
	requireBlocked(t, report, CodeSourceAlreadyV2)
	if report.SourceKind != SourceKindNativeV2 {
		t.Fatalf("source kind = %q, want %q", report.SourceKind, SourceKindNativeV2)
	}
	check, found := checkByName(report, CheckSourceVersion)
	if !found || check.Code != CodeSourceAlreadyV2 || !strings.Contains(check.Detail, "gateway_api_key_identities") {
		t.Fatalf("version check = %+v, want %s naming the table", check, CodeSourceAlreadyV2)
	}
}

func TestPreflightRefusesSourceChangedDuringTheSnapshot(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	before := snapshotTree(t, fixture.root)
	report := preflight(t, fixture.root, Options{
		DeclaredVersion: pinnedSourceVersion(),
		afterCopy: func() {
			// The writer was not stopped: a data file moves between the copy and
			// the second fingerprint.
			path := filepath.Join(fixture.root, dataKeyFileName)
			if err := os.WriteFile(path, []byte("rewritten-key\n"), 0o600); err != nil {
				t.Errorf("rewrite data key: %v", err)
			}
		},
	})
	requireBlocked(t, report, CodeSourceChanged)
	check, found := checkByName(report, CheckSourceOwnership)
	if !found || check.Code != CodeSourceChanged {
		t.Fatalf("ownership check = %+v, want %s", check, CodeSourceChanged)
	}
	after := snapshotTree(t, fixture.root)
	if len(after) != len(before) {
		t.Fatalf("tree entry count changed: %d -> %d", len(before), len(after))
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Fatalf("preflight added %s to the source", path)
		}
	}
}

func TestPreflightFingerprintIsDeterministicAndDetectsChanges(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	first := preflight(t, fixture.root, declared())
	second := preflight(t, fixture.root, declared())
	if first.Fingerprint == "" || first.Fingerprint != second.Fingerprint {
		t.Fatalf("fingerprints differ between identical runs: %q vs %q", first.Fingerprint, second.Fingerprint)
	}

	archive := filepath.Join(fixture.root, archivesDirName, "2026-03", "segment-000001.jsonl")
	if err := os.WriteFile(archive, []byte(`{"events":99}`+"\n"), 0o600); err != nil {
		t.Fatalf("rewrite archive file: %v", err)
	}
	afterArchive := preflight(t, fixture.root, declared())
	if afterArchive.Fingerprint == first.Fingerprint {
		t.Fatal("fingerprint did not change after an archive file changed")
	}

	if err := os.WriteFile(filepath.Join(fixture.root, dataKeyFileName), []byte("rotated-key\n"), 0o600); err != nil {
		t.Fatalf("rewrite data key: %v", err)
	}
	afterKey := preflight(t, fixture.root, declared())
	if afterKey.Fingerprint == afterArchive.Fingerprint {
		t.Fatal("fingerprint did not change after the data key changed")
	}
}

func TestPreflightDiskRequirement(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	target := t.TempDir()
	sourceBytes := coveredBytes(t, fixture.root)
	required := sourceBytes + (sourceBytes+4)/5
	if required <= sourceBytes {
		t.Fatalf("required bytes = %d, want more than source bytes %d", required, sourceBytes)
	}

	report := preflight(t, fixture.root, Options{
		DeclaredVersion: pinnedSourceVersion(),
		TargetDir:       target,
		AvailableBytes:  func(string) (int64, error) { return required - 1, nil },
	})
	requireBlocked(t, report, CodeDiskInsufficient)
	check, found := checkByName(report, CheckDisk)
	if !found || check.Status != StatusBlocked || check.Code != CodeDiskInsufficient {
		t.Fatalf("disk check = %+v", check)
	}
	// The detail states the scratch requirement beside the copy requirement: the
	// snapshot needs room before the target volume is touched at all.
	for _, want := range []string{"requiredBytes=", "availableBytes=", "scratchRequiredBytes="} {
		if !strings.Contains(check.Detail, want) {
			t.Fatalf("disk detail %q does not carry %q", check.Detail, want)
		}
	}

	report = preflight(t, fixture.root, Options{
		DeclaredVersion: pinnedSourceVersion(),
		TargetDir:       target,
		AvailableBytes:  func(string) (int64, error) { return required, nil },
	})
	if !report.Ready {
		t.Fatalf("ready = false at exactly the requirement, blocked = %v", report.Blocked)
	}
}

// TestPreflightBlocksWhenTheDiskProbeFails is the regression for M2: an
// unmeasurable volume used to be reported as "skipped" and the source stayed
// ready.
func TestPreflightBlocksWhenTheDiskProbeFails(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	var probed string
	report := preflight(t, fixture.root, Options{
		DeclaredVersion: pinnedSourceVersion(),
		TargetDir:       filepath.Join(t.TempDir(), "manager-data"),
		AvailableBytes: func(dir string) (int64, error) {
			probed = dir
			return 0, fmt.Errorf("statfs %s: permission denied", dir)
		},
	})
	requireBlocked(t, report, CodeDiskProbeFailed)
	check, found := checkByName(report, CheckDisk)
	if !found || check.Status != StatusBlocked || check.Code != CodeDiskProbeFailed {
		t.Fatalf("disk check = %+v, want blocked %s", check, CodeDiskProbeFailed)
	}
	if probed == "" {
		t.Fatal("the probe was never asked about the target volume")
	}
}

func TestPreflightParityDetectsProjectionlessRefOnly(t *testing.T) {
	healthyTimestamp := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	missingTimestamp := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC).UnixMilli()
	fixture := newSourceFixture(t, func(t *testing.T, db *sql.DB) {
		setWatermark(t, db, 30)
		archivedRef(t, db, "healthy-event", 10, healthyTimestamp)
		addProjection(t, db, 10, healthyTimestamp)
		archivedRef(t, db, "projectionless-event", 20, missingTimestamp)
		rebuildDeletedCoverage(t, db)
	})
	report := preflight(t, fixture.root, declared())
	if !report.ParityGaps.ProjectionStateUsable {
		t.Fatalf("projection state unusable: %s", report.ParityGaps.ProjectionStateReason)
	}
	if report.ParityGaps.DeletedEventRefs != 3 {
		t.Fatalf("deleted event refs = %d, want 3", report.ParityGaps.DeletedEventRefs)
	}
	if report.ParityGaps.MissingProjection != 1 {
		t.Fatalf("missing projection = %d, want 1 (the healthy rows must not be flagged)", report.ParityGaps.MissingProjection)
	}
	if report.ParityGaps.MismatchedProjection != 0 || report.ParityGaps.StaleProjection != 0 {
		t.Fatalf("mismatched/stale = %d/%d, want 0/0", report.ParityGaps.MismatchedProjection, report.ParityGaps.StaleProjection)
	}
	if report.ParityGaps.PastWatermark != 0 {
		t.Fatalf("past watermark = %d, want 0", report.ParityGaps.PastWatermark)
	}
	wantDates := []string{"2026-03-02"}
	if len(report.ParityGaps.AffectedDates) != 1 || report.ParityGaps.AffectedDates[0] != wantDates[0] {
		t.Fatalf("affected dates = %v, want %v", report.ParityGaps.AffectedDates, wantDates)
	}
	check, _ := checkByName(report, CheckParity)
	if check.Status != StatusGap {
		t.Fatalf("parity check status = %q, want %q", check.Status, StatusGap)
	}
}

func TestPreflightParityDetectsMismatchedProjectionAndWatermark(t *testing.T) {
	timestamp := time.Date(2026, 3, 3, 8, 0, 0, 0, time.UTC).UnixMilli()
	fixture := newSourceFixture(t, func(t *testing.T, db *sql.DB) {
		setWatermark(t, db, 100)
		archivedRef(t, db, "shifted-event", 10, timestamp)
		addProjection(t, db, 10, timestamp+1)
		archivedRef(t, db, "past-watermark-event", 500, timestamp)
		addProjection(t, db, 500, timestamp)
		rebuildDeletedCoverage(t, db)
	})
	report := preflight(t, fixture.root, declared())
	if report.ParityGaps.MismatchedProjection != 1 {
		t.Fatalf("mismatched projection = %d, want 1", report.ParityGaps.MismatchedProjection)
	}
	if report.ParityGaps.PastWatermark != 1 {
		t.Fatalf("past watermark = %d, want 1", report.ParityGaps.PastWatermark)
	}
	if report.ParityGaps.MissingProjection != 0 {
		t.Fatalf("missing projection = %d, want 0", report.ParityGaps.MissingProjection)
	}
	if report.ParityGaps.StaleProjection != 0 {
		t.Fatalf("stale projection = %d, want 0 for a usable state", report.ParityGaps.StaleProjection)
	}
}

// TestPreflightParityDetectsStaleSameTimestampProjection is the regression for
// M4: a projection row whose timestamp matches was trusted even when the rollup
// state carried a different structure revision.
func TestPreflightParityDetectsStaleSameTimestampProjection(t *testing.T) {
	fixture := newSourceFixture(t, func(t *testing.T, db *sql.DB) {
		setProjectionState(t, db, "identity-2:model-1:project-v1", "ready")
	})
	report := preflight(t, fixture.root, declared())
	if report.ParityGaps.ProjectionStateUsable {
		t.Fatal("projection state with a foreign structure revision was reported usable")
	}
	if !strings.Contains(report.ParityGaps.ProjectionStateReason, "structure_revision") {
		t.Fatalf("projection state reason = %q, want the revision mismatch", report.ParityGaps.ProjectionStateReason)
	}
	if report.ParityGaps.StaleProjection != 1 {
		t.Fatalf("stale projection = %d, want 1: a same-timestamp row in an unusable state is not evidence",
			report.ParityGaps.StaleProjection)
	}
	if report.ParityGaps.PastWatermark != 0 {
		t.Fatalf("past watermark = %d, want 0 for an unusable state", report.ParityGaps.PastWatermark)
	}
	check, _ := checkByName(report, CheckParity)
	if check.Status != StatusGap {
		t.Fatalf("parity check status = %q, want %q", check.Status, StatusGap)
	}
}

func TestPreflightParityDetectsClearingProjectionState(t *testing.T) {
	fixture := newSourceFixture(t, func(t *testing.T, db *sql.DB) {
		setProjectionState(t, db, usageidentity.MonitoringProjectionStructureRevision(), "clearing")
	})
	report := preflight(t, fixture.root, declared())
	if report.ParityGaps.ProjectionStateUsable {
		t.Fatal("a clearing projection state was reported usable")
	}
	if report.ParityGaps.StaleProjection != 1 {
		t.Fatalf("stale projection = %d, want 1", report.ParityGaps.StaleProjection)
	}
}

func TestPreflightParityChecksDeletedCoverageDailyBothWays(t *testing.T) {
	day1 := time.Date(2026, 4, 1, 6, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 4, 2, 6, 0, 0, 0, time.UTC)
	fixture := newSourceFixture(t, func(t *testing.T, db *sql.DB) {
		setWatermark(t, db, 100)
		execFixture(t, db, `delete from usage_archive_deleted_coverage_daily`)
		execFixture(t, db, `delete from usage_archive_event_refs`)
		execFixture(t, db, `delete from usage_event_identity_ledger`)
		for index, event := range []struct {
			hash string
			id   int64
		}{{"d1-a", 31}, {"d1-b", 32}, {"d1-c", 33}} {
			archivedRef(t, db, event.hash, event.id, day1.UnixMilli()+int64(index)*1000)
		}
		// The coverage table claims two deletions on day 1 (refs hold three) and
		// one on day 2 (refs hold none).
		execFixture(t, db, `insert into usage_archive_deleted_coverage_daily
			(utc_day, deleted_event_count, min_timestamp_ms, max_timestamp_ms) values (?, 2, ?, ?)`,
			day1.UnixMilli()/msPerDay, day1.UnixMilli(), day1.UnixMilli())
		execFixture(t, db, `insert into usage_archive_deleted_coverage_daily
			(utc_day, deleted_event_count, min_timestamp_ms, max_timestamp_ms) values (?, 1, ?, ?)`,
			day2.UnixMilli()/msPerDay, day2.UnixMilli(), day2.UnixMilli())
	})
	report := preflight(t, fixture.root, declared())
	if report.ParityGaps.CoverageDaysWithGaps != 2 {
		t.Fatalf("coverage days with gaps = %d, want 2", report.ParityGaps.CoverageDaysWithGaps)
	}
	dates := map[string]bool{}
	for _, date := range report.ParityGaps.AffectedDates {
		dates[date] = true
	}
	for _, want := range []string{"2026-04-01", "2026-04-02"} {
		if !dates[want] {
			t.Fatalf("affected dates = %v, want %s", report.ParityGaps.AffectedDates, want)
		}
	}
}

func TestModelStageOrderMatchesPreflightStages(t *testing.T) {
	// The operation model's forward order is the contract the preflight report
	// describes to the operator.
	order := []model.UpgradeStage{
		model.UpgradeStagePreflight, model.UpgradeStageCopyIn, model.UpgradeStageReconcile,
		model.UpgradeStageCPAImport, model.UpgradeStageExternalConnect, model.UpgradeStageVerify,
		model.UpgradeStageComplete,
	}
	for index := 1; index < len(order); index++ {
		if !order[index-1].CanAdvanceTo(order[index]) {
			t.Fatalf("%q cannot advance to %q", order[index-1], order[index])
		}
		if order[index].CanAdvanceTo(order[index-1]) {
			t.Fatalf("%q advanced backwards to %q", order[index], order[index-1])
		}
	}
}

func coveredBytes(t *testing.T, root string) int64 {
	t.Helper()
	total := int64(0)
	for _, name := range sourceFingerprintFiles {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			continue
		}
		total += info.Size()
	}
	walkErr := filepath.WalkDir(filepath.Join(root, archivesDirName), func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk archives: %v", walkErr)
	}
	return total
}

func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	walkErr := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			snapshot[relative+string(filepath.Separator)] = ""
			return nil
		}
		digest, err := fileContentDigest(path)
		if err != nil {
			return err
		}
		snapshot[relative] = fmt.Sprintf("%d:%s", entrySize(t, entry), digest)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("snapshot source tree: %v", walkErr)
	}
	return snapshot
}

func entrySize(t *testing.T, entry os.DirEntry) int64 {
	t.Helper()
	info, err := entry.Info()
	if err != nil {
		t.Fatalf("stat %s: %v", entry.Name(), err)
	}
	return info.Size()
}

// TestPreflightBlocksV113ShapeDeclaredAsThePinnedRelease is the H3 regression:
// a source that lacks usage_archive_event_refs was accepted as v1.14.4 because
// the operator's declaration was the only version check. The declaration is not
// evidence; the structural lower bound is, and a missing table names itself.
func TestPreflightBlocksV113ShapeDeclaredAsThePinnedRelease(t *testing.T) {
	fixture := newSourceFixture(t, func(t *testing.T, db *sql.DB) {
		execFixture(t, db, `drop table usage_archive_event_refs`)
	})
	report := preflight(t, fixture.root, declared())
	requireBlocked(t, report, CodeSourceVersionUnsupported)
	if report.SourceVersion != pinnedSourceVersion() {
		t.Fatalf("source version = %q, want the declaration %q", report.SourceVersion, pinnedSourceVersion())
	}
	if len(report.MissingTables) != 1 || report.MissingTables[0] != "usage_archive_event_refs" {
		t.Fatalf("missing tables = %v, want [usage_archive_event_refs]", report.MissingTables)
	}
	check, found := checkByName(report, CheckSourceVersion)
	if !found || check.Status != StatusBlocked || check.Code != CodeSourceVersionUnsupported {
		t.Fatalf("version check = %+v, want blocked %s", check, CodeSourceVersionUnsupported)
	}
	if !strings.Contains(check.Detail, "usage_archive_event_refs") {
		t.Fatalf("version detail %q does not name the missing table", check.Detail)
	}
	if !strings.Contains(check.Detail, pinnedSourceVersion()) {
		t.Fatalf("version detail %q does not carry the pinned release", check.Detail)
	}
}

// TestPreflightParityIsNotApplicableWithoutTheRetainedArchiveTables is the
// second half of H3: when the source has no archive refs or no deleted coverage
// rollup, parity is not "zero gaps" — it was never measurable, and the report
// says so with a reason.
func TestPreflightParityIsNotApplicableWithoutTheRetainedArchiveTables(t *testing.T) {
	cases := []struct {
		name      string
		statement string
		missing   string
	}{
		{name: "archive event refs", statement: `drop table usage_archive_event_refs`, missing: "usage_archive_event_refs"},
		{name: "deleted coverage daily", statement: `drop table usage_archive_deleted_coverage_daily`, missing: "usage_archive_deleted_coverage_daily"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSourceFixture(t, func(t *testing.T, db *sql.DB) {
				execFixture(t, db, testCase.statement)
			})
			report := preflight(t, fixture.root, declared())
			if report.ParityGaps.Applicable {
				t.Fatalf("parity reported applicable without %s", testCase.missing)
			}
			if !strings.Contains(report.ParityGaps.Reason, testCase.missing) {
				t.Fatalf("parity reason = %q, want it to name %s", report.ParityGaps.Reason, testCase.missing)
			}
			if report.ParityGaps.any() || report.ParityGaps.DeletedEventRefs != 0 || len(report.ParityGaps.AffectedDates) != 0 {
				t.Fatalf("parity gaps = %+v, want no measured counts", report.ParityGaps)
			}
			check, found := checkByName(report, CheckParity)
			if !found || check.Status != StatusNotApplicable {
				t.Fatalf("parity check = %+v, want status %q", check, StatusNotApplicable)
			}
			if !strings.Contains(check.Detail, testCase.missing) {
				t.Fatalf("parity detail %q does not name %s", check.Detail, testCase.missing)
			}
			// The structural bound still blocks, so no path reaches ready on a
			// source without the retained history.
			requireBlocked(t, report, CodeSourceVersionUnsupported)
		})
	}
}

// TestPreflightBlocksWhenThePostCopyFingerprintFails is N4: a second whole-tree
// fingerprint error used to be swallowed, leaving the pre-copy value as if the
// copy had been proven against it.
func TestPreflightBlocksWhenThePostCopyFingerprintFails(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	report := preflight(t, fixture.root, Options{
		DeclaredVersion: pinnedSourceVersion(),
		afterCopy: func() {
			// The database file is replaced by a directory: the pre-copy value
			// still exists, but the tree can no longer be read back.
			if err := os.Remove(fixture.dbPath); err != nil {
				t.Errorf("remove source database: %v", err)
				return
			}
			if err := os.Mkdir(fixture.dbPath, 0o755); err != nil {
				t.Errorf("replace source database with a directory: %v", err)
			}
		},
	})
	requireBlocked(t, report, CodeSourceUnreadable)
	check, found := checkByName(report, CheckFingerprint)
	if !found || check.Status != StatusBlocked || check.Code != CodeSourceUnreadable {
		t.Fatalf("fingerprint check = %+v, want blocked %s", check, CodeSourceUnreadable)
	}
	if !strings.Contains(check.Detail, "after the snapshot") {
		t.Fatalf("fingerprint detail %q does not say the second fingerprint failed", check.Detail)
	}
	if report.Fingerprint == "" {
		t.Fatal("the pre-copy fingerprint was not reported")
	}
}

// TestPreflightRefusesScratchInsideTheLegacyRoot is N2's first half: the whole
// database copy must never land in the tree preflight promises to preserve.
func TestPreflightRefusesScratchInsideTheLegacyRoot(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	scratch := filepath.Join(fixture.root, "upgrade-scratch")
	before := snapshotTree(t, fixture.root)

	report := preflight(t, fixture.root, Options{
		DeclaredVersion: pinnedSourceVersion(),
		ScratchDir:      scratch,
	})
	requireBlocked(t, report, CodeSnapshotFailed)
	if _, err := os.Lstat(scratch); !os.IsNotExist(err) {
		t.Fatalf("scratch directory was created inside the source root: %v", err)
	}
	after := snapshotTree(t, fixture.root)
	if len(after) != len(before) {
		t.Fatalf("source tree entry count changed: %d -> %d", len(before), len(after))
	}
	for path, digest := range before {
		if after[path] != digest {
			t.Fatalf("source entry %s changed", path)
		}
	}
	check, found := checkByName(report, CheckSourceOwnership)
	if !found || check.Code != CodeSnapshotFailed {
		t.Fatalf("ownership check = %+v, want %s", check, CodeSnapshotFailed)
	}
	if !strings.Contains(check.Detail, "legacy source root") {
		t.Fatalf("ownership detail %q does not name the legacy source root", check.Detail)
	}
}

// TestPreflightRefusesScratchInsideTheTargetRoot is N2's second half: scratch in
// the volume that will receive the copy is refused before anything is written.
func TestPreflightRefusesScratchInsideTheTargetRoot(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	target := t.TempDir()
	scratch := filepath.Join(target, "upgrade-scratch")

	report := preflight(t, fixture.root, Options{
		DeclaredVersion: pinnedSourceVersion(),
		TargetDir:       target,
		ScratchDir:      scratch,
	})
	requireBlocked(t, report, CodeSnapshotFailed)
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatalf("read target root: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("target root was written: %v", entries)
	}
	check, found := checkByName(report, CheckSourceOwnership)
	if !found || check.Code != CodeSnapshotFailed {
		t.Fatalf("ownership check = %+v, want %s", check, CodeSnapshotFailed)
	}
	if !strings.Contains(check.Detail, "target root") {
		t.Fatalf("ownership detail %q does not name the target root", check.Detail)
	}
}

// TestPreflightRefusesADefaultTemporaryDirectoryInsideTheLegacyRoot covers the
// remaining placement hole: with no --scratch-dir the OS temporary directory
// used to skip the check entirely, so a TMPDIR that points into the source tree
// received a full copy of the database and a snapshot abandoned by a killed
// process would have stayed there.
func TestPreflightRefusesADefaultTemporaryDirectoryInsideTheLegacyRoot(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	temp := filepath.Join(fixture.root, "tmp")
	if err := os.Mkdir(temp, 0o755); err != nil {
		t.Fatalf("create default temporary directory: %v", err)
	}
	setOSTempDir(t, temp)
	before := snapshotTree(t, fixture.root)

	report := preflight(t, fixture.root, declared())
	requireBlocked(t, report, CodeSnapshotFailed)
	entries, err := os.ReadDir(temp)
	if err != nil {
		t.Fatalf("read default temporary directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("scratch was created in the default temporary directory: %v", entries)
	}
	after := snapshotTree(t, fixture.root)
	if len(after) != len(before) {
		t.Fatalf("source tree entry count changed: %d -> %d", len(before), len(after))
	}
	for path, digest := range before {
		if after[path] != digest {
			t.Fatalf("source entry %s changed", path)
		}
	}
	check, found := checkByName(report, CheckSourceOwnership)
	if !found || check.Code != CodeSnapshotFailed {
		t.Fatalf("ownership check = %+v, want %s", check, CodeSnapshotFailed)
	}
	if !strings.Contains(check.Detail, "OS temporary directory") {
		t.Fatalf("ownership detail %q does not name the default directory", check.Detail)
	}
	if !strings.Contains(check.Detail, "legacy source root") {
		t.Fatalf("ownership detail %q does not name the legacy source root", check.Detail)
	}
}

// TestValidateScratchDirChecksTheDefaultTemporaryDirectory is the unit-level
// form of the same rule for both roots, plus the counter-proof that an ordinary
// OS temporary directory is still accepted.
func TestValidateScratchDirChecksTheDefaultTemporaryDirectory(t *testing.T) {
	legacy := t.TempDir()
	target := t.TempDir()
	for _, testCase := range []struct {
		name string
		root string
	}{
		{name: "the legacy source root", root: legacy},
		{name: "the target root", root: target},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			defaultScratch := filepath.Join(testCase.root, "tmp")
			if err := os.Mkdir(defaultScratch, 0o755); err != nil {
				t.Fatalf("create default temporary directory: %v", err)
			}
			setOSTempDir(t, defaultScratch)
			for _, scratch := range []string{"", "   "} {
				err := ValidateScratchDir(scratch, legacy, target)
				if !errors.Is(err, ErrSnapshotFailed) {
					t.Fatalf("scratch %q error = %v, want %s", scratch, err, ErrSnapshotFailed)
				}
				if !strings.Contains(err.Error(), testCase.name) {
					t.Fatalf("scratch %q error %q does not name %s", scratch, err, testCase.name)
				}
				if !strings.Contains(err.Error(), "OS temporary directory") {
					t.Fatalf("scratch %q error %q does not name the default directory", scratch, err)
				}
			}
			entries, err := os.ReadDir(defaultScratch)
			if err != nil {
				t.Fatalf("read default temporary directory: %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("scratch was created in the default temporary directory: %v", entries)
			}
		})
	}
	if err := ValidateScratchDir("", legacy, target); err != nil {
		t.Fatalf("an ordinary OS temporary directory was refused: %v", err)
	}
	if err := ValidateScratchDir(os.TempDir(), legacy, target); err != nil {
		t.Fatalf("the OS temporary directory named explicitly was refused: %v", err)
	}
}

// TestDiskTargetNeverMeasuresTheWorkingDirectory is N3: with no target and no
// scratch directory the probe measures the volume the snapshot would use, never
// the process working directory, which describes neither volume.
func TestDiskTargetNeverMeasuresTheWorkingDirectory(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	target, defaulted := diskTarget("", "")
	if !defaulted {
		t.Fatal("an unset target was not reported as defaulted")
	}
	if target == workingDirectory {
		t.Fatalf("disk target = %q, want the scratch volume rather than the working directory", target)
	}
	if target != os.TempDir() {
		t.Fatalf("disk target = %q, want the OS temporary directory", target)
	}
	scratch := t.TempDir()
	target, defaulted = diskTarget("", scratch)
	if !defaulted || target != scratch {
		t.Fatalf("scratch-volume target = %q defaulted = %t, want %q defaulted", target, defaulted, scratch)
	}
	target, defaulted = diskTarget(filepath.Join(scratch, "manager-data"), scratch)
	if defaulted || target != filepath.Join(scratch, "manager-data") {
		t.Fatalf("explicit target = %q defaulted = %t", target, defaulted)
	}
}
