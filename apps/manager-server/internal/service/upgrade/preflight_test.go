package upgrade

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/datamigration"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const msPerDay = int64(86_400_000)

// v1SchemaStatements is the 1.14.x DDL for the tables preflight reads, copied
// from the released migration. The table set does not change across 1.14.0 to
// 1.14.10, and a real pinned-release source carries all of it. It is
// deliberately hand-written: a fixture built by the v2 migration is not a 1.x
// source, and the point of these tests is that a real WAL-mode source is
// inspected without being written to.
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

// v1SeedStatements are the rows a stopped 1.14.x Manager at the pinned release
// leaves behind: the derived-format markers in settings, the rollup state, a few
// live events, one archive run with a projection-covered deleted event and the
// matching deleted coverage day.
//
// usage_cache_accounting_semantics_revision=3 belongs to that shape. It is the
// value the pinned release's own migration writes (datamigration/repository.go:
// CurrentUsageCacheAccountingSemanticsRevision = 3, raised by v1.14.6; v1.14.4
// and v1.14.5 wrote 2), so the fixture is a source the pin must accept rather
// than one it has to refuse. 2 and an absent row are legitimate pinned shapes
// too — an install that upgraded through v1.14.4/v1.14.5 still carries 2, and
// one stopped before that migration wrote the row carries none — and
// TestPreflightAcceptsBothLegitimatePinnedShapes covers all of them.
var v1SeedStatements = []string{
	`insert into settings (key, value, updated_at_ms) values
		('bootstrap_state_v1', '{"version":1,"status":"ready","adminReady":true,"projectInit":true}', 1),
		('usage_monitoring_model_format_version', '1', 1),
		('usage_account_history_identity_format_version', '` + usageidentity.AccountHistoryStructureRevision() + `', 1),
		('usage_dashboard_hourly_format_version', '3', 1),
		('` + datamigration.UsageCacheAccountingSemanticsRevisionKey + `', '3', 1)`,
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

// v1PinnedDerivedIndexStatements are the derived indexes a v1.14.10 install
// carries once its offline `cleanup-derived` run has built them. The latency
// index is the second marker the 1.x line introduced after v1.14.4 (v1.14.7),
// so a fixture that carries it is the pinned release's real post-cleanup shape,
// not a source that moved past the pin.
var v1PinnedDerivedIndexStatements = []string{
	`create index if not exists idx_usage_events_ts_latency_ttft_cover
		on usage_events (timestamp_ms, latency_ms, ttft_ms)`,
}

type sourceFixture struct {
	root   string
	dbPath string
}

// newSourceFixture builds a realistic stopped v1.14.10 data directory: a WAL-mode
// usage.sqlite with the released schema and rows — including the revision 3
// setting and the latency covering index the pin's own shape carries — data.key,
// an archive tree and the v1 ownership lock file. The returned source has no
// -wal/-shm sidecars, exactly like a cleanly stopped v1 writer.
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
	statements = append(statements, v1PinnedDerivedIndexStatements...)
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

// setCacheAccountingRevision writes the settings row the v1 code owns. The
// fixture seeds the value the pin wrote (3); a later value replaces it the way
// the v1 migration does — insert or update the same key.
func setCacheAccountingRevision(t *testing.T, db *sql.DB, value string) {
	t.Helper()
	execFixture(t, db, `insert into settings (key, value, updated_at_ms) values (?, ?, 1)
		on conflict(key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		datamigration.UsageCacheAccountingSemanticsRevisionKey, value)
}

// removeCacheAccountingRevision drops the row entirely. A pinned install that
// was stopped before its v1.14.4/5 migration wrote the revision is legitimate
// too, so the check must not require the key to exist.
func removeCacheAccountingRevision(t *testing.T, db *sql.DB) {
	t.Helper()
	execFixture(t, db, `delete from settings where key = ?`,
		datamigration.UsageCacheAccountingSemanticsRevisionKey)
}

// removeLatencyCoveringIndex drops the index v1.14.7 added over the latency and
// TTFT columns. An install that upgraded through v1.14.7 but never ran the
// offline `cleanup-derived` command does not have it, and the pin accepts that
// shape too: the index is positive evidence only, and its absence proves
// nothing.
func removeLatencyCoveringIndex(t *testing.T, db *sql.DB) {
	t.Helper()
	execFixture(t, db, `drop index if exists idx_usage_events_ts_latency_ttft_cover`)
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

// TestPreflightIsReadyOnThePinnedReleaseSchema is the real-Docker harness
// regression: a source that carries exactly the schema the pinned release
// creates passes preflight. usage_archive_deleted_coverage_daily is part of that
// schema in every 1.14.x release, v1.14.0 included, so reading it as a marker of
// a later release refused the pinned release itself and no real 1.x install
// could pass. The fixture also carries the
// usage_cache_accounting_semantics_revision=3 row the pin's own migration
// writes and the latency covering index its offline cleanup builds, so the
// marker rule is exercised here on the accepting side too.
func TestPreflightIsReadyOnThePinnedReleaseSchema(t *testing.T) {
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
	version, found := checkByName(report, CheckSourceVersion)
	if !found || version.Status != StatusPass {
		t.Fatalf("version check = %+v, want pass", version)
	}
	if strings.Contains(version.Detail, "newer than the pinned release") {
		t.Fatalf("version detail %q reads the pinned release's own schema as newer", version.Detail)
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
	digest, err := fileContentDigest(context.Background(), path)
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
	// Nothing holds the source, but without the marker its ownership cannot be
	// established from the source itself: that is not a busy source.
	requireBlocked(t, report, CodeSourceLockUnavailable)
	// Without the fence the source is never profiled, so it stays unknown
	// rather than being described from an unfenced read.
	if report.SourceKind != SourceKindUnknown {
		t.Fatalf("source kind = %q, want %q", report.SourceKind, SourceKindUnknown)
	}
	check, found := checkByName(report, CheckSourceOwnership)
	if !found || check.Status != StatusBlocked || check.Code != CodeSourceLockUnavailable {
		t.Fatalf("ownership check = %+v, want blocked %s", check, CodeSourceLockUnavailable)
	}
	if !strings.Contains(check.Detail, "ownership marker") {
		t.Fatalf("ownership detail %q does not name the missing marker", check.Detail)
	}
	if _, err := os.Lstat(fixture.dbPath + ".manager.lock"); !os.IsNotExist(err) {
		t.Fatalf("preflight created a source lock file: %v", err)
	}
	if report.Fingerprint != "" {
		t.Fatal("an unfenced source reported a fingerprint")
	}
}

// TestPreflightReportsAHardLinkedSourceAsLockUnavailable pins L-1: the mount
// lookup and the preflight name the same condition for the same file. A
// hard-linked database is the shared file policy refusing an uninspectable file,
// not a live writer, so both report upgrade_source_lock_unavailable; before the
// fix the preflight said upgrade_source_busy and told the operator to stop a
// Manager that was not running.
func TestPreflightReportsAHardLinkedSourceAsLockUnavailable(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	if err := os.Link(fixture.dbPath, fixture.dbPath+".alias"); err != nil {
		t.Skipf("create database hard link: %v", err)
	}
	report := preflight(t, fixture.root, declared())
	requireBlocked(t, report, CodeSourceLockUnavailable)
	check, found := checkByName(report, CheckSourceOwnership)
	if !found || check.Status != StatusBlocked || check.Code != CodeSourceLockUnavailable {
		t.Fatalf("ownership check = %+v, want blocked %s", check, CodeSourceLockUnavailable)
	}
	if _, refusal := MountDatabase(fixture.root); refusal != CodeSourceLockUnavailable {
		t.Fatalf("mount lookup refusal = %q, want the same %q", refusal, CodeSourceLockUnavailable)
	}
}

func TestPreflightReportsMissingDatabase(t *testing.T) {
	root := t.TempDir()
	report := preflight(t, root, declared())
	if report.SourceKind != SourceKindMissingDatabase {
		t.Fatalf("source kind = %q, want %q", report.SourceKind, SourceKindMissingDatabase)
	}
	// An empty directory has neither the ownership marker nor the database. The
	// absent database is what the operator has to fix, so it is named as itself.
	requireBlocked(t, report, CodeSourceMissing)
	check, found := checkByName(report, CheckSourceOwnership)
	if !found || check.Status != StatusBlocked || check.Code != CodeSourceMissing {
		t.Fatalf("ownership check = %+v, want blocked %s", check, CodeSourceMissing)
	}
	if !strings.Contains(check.Detail, "missing") {
		t.Fatalf("ownership detail = %q, want the missing database named", check.Detail)
	}
	if _, err := os.Lstat(filepath.Join(root, databaseFileName+".manager.lock")); !os.IsNotExist(err) {
		t.Fatalf("preflight created a source lock file: %v", err)
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

// TestPreflightPendsAnUndeclaredVersionForAReadOnlyCaller is the wizard's
// reading of the same source: the read cannot declare the release, the copy-in
// declares it, so the check reports the pending declaration with the version it
// expects instead of blocking a source that is otherwise sound.
func TestPreflightPendsAnUndeclaredVersionForAReadOnlyCaller(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	report := preflight(t, fixture.root, Options{AcceptUndeclaredVersion: true})
	if !report.Ready {
		t.Fatalf("ready = false, blocked = %v, checks = %+v", report.Blocked, report.Checks)
	}
	if len(report.Blocked) != 0 {
		t.Fatalf("blocked = %v, want none", report.Blocked)
	}
	check, found := checkByName(report, CheckSourceVersion)
	if !found || check.Status != StatusPending || check.Code != CodeSourceVersionDeclaredAtCopyIn {
		t.Fatalf("version check = %+v, want pending %s", check, CodeSourceVersionDeclaredAtCopyIn)
	}
	if check.ExpectedVersion != pinnedSourceVersion() {
		t.Fatalf("expected version = %q, want %q", check.ExpectedVersion, pinnedSourceVersion())
	}
	if !strings.Contains(check.Detail, "--source-version "+pinnedSourceVersion()) {
		t.Fatalf("version detail %q does not name the declaration the copy-in takes", check.Detail)
	}
	// The source version is still unknown: the pending check names what the
	// copy-in must be told, never a value inferred from the database.
	if report.SourceVersion != VersionUnknown {
		t.Fatalf("source version = %q, want %q", report.SourceVersion, VersionUnknown)
	}
	// The pending check is the only non-pass observation: nothing else lost its
	// blocking power.
	for _, other := range report.Checks {
		if other.Name == CheckSourceVersion {
			continue
		}
		if other.Status == StatusBlocked || other.Status == StatusPending {
			t.Fatalf("check %q = %+v, want an answered check", other.Name, other)
		}
	}
}

// TestPreflightKeepsExactEqualityWhenTheVersionIsDeclared: accepting a pending
// declaration must not soften the comparison for a caller that did declare one.
func TestPreflightKeepsExactEqualityWhenTheVersionIsDeclared(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	other := "v9.9.9"
	if other == pinnedSourceVersion() {
		t.Fatal("the fixture's pin cannot be used as the mismatch")
	}
	report := preflight(t, fixture.root, Options{DeclaredVersion: other, AcceptUndeclaredVersion: true})
	requireBlocked(t, report, CodeSourceVersionUnsupported)
	if report.Ready {
		t.Fatal("a declared mismatch was reported ready")
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

// TestPreflightBlocksAMarkerThePinnedReleaseLacks covers both directions of the
// newer-marker rule.
//
// At the live v1.14.10 pin the acceptance side is covered by
// TestPreflightAcceptsBothLegitimatePinnedShapes; here a full preflight must
// refuse what the pin genuinely cannot produce. The pin's own migration writes
// revision 3 and its offline cleanup builds the latency index, so revision 4 —
// which no 1.x release has written — and a settings row that is not a positive
// integer at all both prove the source moved past the pin. This is the harness
// regression that matters most: an empty ladder made the live pin accept
// revision 4.
//
// The v1.14.4 ladder subtests keep the marker table itself honest at a pin the
// markers are genuinely later than: revision 3 (v1.14.6) and
// idx_usage_events_ts_latency_ttft_cover (v1.14.7) are refused and named. The
// deleted-raw summary is not a marker — it is base shape in every 1.14.x
// release — so the refusal must not blame it. The detail names the release that
// introduced each marker instead of repeating the matching declaration, which
// used to read as if the check contradicted itself.
func TestPreflightBlocksAMarkerThePinnedReleaseLacks(t *testing.T) {
	livePinCases := []struct {
		name       string
		revision   string
		wantDetail string
	}{
		{
			name:       "revision above the pin's own",
			revision:   "4",
			wantDetail: "usage_cache_accounting_semantics_revision=4 (after v1.14.10)",
		},
		{
			name:       "non-integer revision",
			revision:   "corrupt",
			wantDetail: `usage_cache_accounting_semantics_revision="corrupt" (invalid)`,
		},
		{
			name:       "zero revision",
			revision:   "0",
			wantDetail: `usage_cache_accounting_semantics_revision="0" (invalid)`,
		},
		{
			name:       "negative revision",
			revision:   "-1",
			wantDetail: `usage_cache_accounting_semantics_revision="-1" (invalid)`,
		},
	}
	for _, testCase := range livePinCases {
		t.Run("live pin: "+testCase.name, func(t *testing.T) {
			fixture := newSourceFixture(t, func(t *testing.T, db *sql.DB) {
				setCacheAccountingRevision(t, db, testCase.revision)
			})
			wantFullDetail := "source is newer than the pinned release " + pinnedSourceVersion() + "; markers from later releases: " + testCase.wantDetail
			for _, mode := range []struct {
				name string
				opts Options
			}{
				{name: "declared", opts: declared()},
				{name: "read-only undeclared", opts: Options{AcceptUndeclaredVersion: true}},
			} {
				t.Run(mode.name, func(t *testing.T) {
					report := preflight(t, fixture.root, mode.opts)
					if report.Ready {
						t.Fatalf("Ready = true for %s (%s), want false", testCase.name, mode.name)
					}
					requireBlocked(t, report, CodeSourceVersionUnsupported)
					check, found := checkByName(report, CheckSourceVersion)
					if !found || check.Status != StatusBlocked || check.Code != CodeSourceVersionUnsupported {
						t.Fatalf("version check = %+v, want blocked %s", check, CodeSourceVersionUnsupported)
					}
					if check.Detail != wantFullDetail {
						t.Fatalf("version detail = %q, want %q", check.Detail, wantFullDetail)
					}
				})
			}
		})
	}

	ladder, err := newVersionLadder("v1.14.4")
	if err != nil {
		t.Fatalf("version ladder for v1.14.4: %v", err)
	}
	cases := []struct {
		name      string
		configure func(source *sourceSchema)
		want      []string
	}{
		{
			name: "cache accounting semantics revision",
			configure: func(source *sourceSchema) {
				source.settings[datamigration.UsageCacheAccountingSemanticsRevisionKey] = "3"
			},
			want: []string{"usage_cache_accounting_semantics_revision=3 (v1.14.6)"},
		},
		{
			name: "latency covering index",
			configure: func(source *sourceSchema) {
				source.indexes["idx_usage_events_ts_latency_ttft_cover"] = true
			},
			want: []string{"idx_usage_events_ts_latency_ttft_cover (v1.14.7)"},
		},
		{
			name: "both later markers",
			configure: func(source *sourceSchema) {
				source.settings[datamigration.UsageCacheAccountingSemanticsRevisionKey] = "3"
				source.indexes["idx_usage_events_ts_latency_ttft_cover"] = true
			},
			want: []string{
				"usage_cache_accounting_semantics_revision=3 (v1.14.6)",
				"idx_usage_events_ts_latency_ttft_cover (v1.14.7)",
			},
		},
		{
			// A revision above the one the introducing release wrote is still a
			// source that moved past the pin. Matching the exact value would let
			// it through, which is a fail-open in the one direction this check
			// exists for.
			name: "revision above the recorded one",
			configure: func(source *sourceSchema) {
				source.settings[datamigration.UsageCacheAccountingSemanticsRevisionKey] = "4"
			},
			want: []string{"usage_cache_accounting_semantics_revision=4 (v1.14.6)"},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			source := sourceSchema{
				tables:   map[string]bool{"usage_archive_deleted_coverage_daily": true},
				indexes:  map[string]bool{},
				settings: map[string]string{},
			}
			testCase.configure(&source)
			newer := ladder.newer(source)
			if len(newer) == 0 {
				t.Fatalf("newer = none, want the marker the v1.14.4 pin lacks")
			}
			check := versionCheck(SourceKindLegacyManager, nil, nil, newer, "v1.14.4", "v1.14.4", false)
			if check.Status != StatusBlocked || check.Code != CodeSourceVersionUnsupported {
				t.Fatalf("version check = %+v, want blocked %s", check, CodeSourceVersionUnsupported)
			}
			if !strings.Contains(check.Detail, "source is newer than the pinned release") {
				t.Fatalf("version detail %q does not say the source is newer than the pin", check.Detail)
			}
			for _, want := range testCase.want {
				if !strings.Contains(check.Detail, want) {
					t.Fatalf("version detail %q does not name %q", check.Detail, want)
				}
			}
			if strings.Contains(check.Detail, "expected=") || strings.Contains(check.Detail, "declared=") {
				t.Fatalf("version detail %q repeats the matching declaration next to the refusal", check.Detail)
			}
			if strings.Contains(check.Detail, "usage_archive_deleted_coverage_daily") {
				t.Fatalf("version detail %q blames the pinned release's own schema", check.Detail)
			}
		})
	}
}

// TestPreflightAcceptsBothLegitimatePinnedShapes pins the acceptance rule at
// the v1.14.10 pin from the accepting side. The pin's own schema carries
// usage_cache_accounting_semantics_revision=3 (raised by v1.14.6) and
// idx_usage_events_ts_latency_ttft_cover (added by v1.14.7), so a check that
// still treats them as markers the pin lacks — or that degrades to "the key
// exists", or to a plain "value != 3" comparison — refuses a real pinned source
// and fails here. An install that upgraded through v1.14.4/v1.14.5 carries
// revision 2, and one stopped before that migration wrote the row carries none,
// so the key itself is not required either.
//
// The declaration is the one thing the pin does not accept loosely: the release
// the contract replaced is still refused, by name, with the release now
// expected.
func TestPreflightAcceptsBothLegitimatePinnedShapes(t *testing.T) {
	pinned := pinnedSourceVersion()
	if pinned != "v1.14.10" {
		t.Fatalf("pinned source version = %q, want v1.14.10", pinned)
	}
	cases := []struct {
		name      string
		configure func(t *testing.T, db *sql.DB)
	}{
		{
			// The pin's real startup state: revision 3 and the index its own
			// offline cleanup built.
			name:      "revision 3 with the latency covering index",
			configure: nil,
		},
		{
			name: "revision 2 written by the v1.14.4 migration it upgraded from",
			configure: func(t *testing.T, db *sql.DB) {
				setCacheAccountingRevision(t, db, "2")
				removeLatencyCoveringIndex(t, db)
			},
		},
		{name: "revision row absent", configure: removeCacheAccountingRevision},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newSourceFixture(t, testCase.configure)
			report := preflight(t, fixture.root, declared())
			if !report.Ready {
				t.Fatalf("ready = false, blocked = %v, checks = %+v", report.Blocked, report.Checks)
			}
			version, found := checkByName(report, CheckSourceVersion)
			if !found || version.Status != StatusPass {
				t.Fatalf("version check = %+v, want pass", version)
			}
			if strings.Contains(version.Detail, "newer than the pinned release") {
				t.Fatalf("version detail %q refuses a legitimate pinned source", version.Detail)
			}
			if report.SourceVersion != pinned {
				t.Fatalf("source version = %q, want the declaration %q", report.SourceVersion, pinned)
			}
		})
	}

	// Declaring the release the contract replaced is refused by the declaration,
	// not by a marker: the detail names the pin that is expected now.
	fixture := newSourceFixture(t, nil)
	report := preflight(t, fixture.root, Options{DeclaredVersion: "v1.14.4"})
	requireBlocked(t, report, CodeSourceVersionUnsupported)
	check, found := checkByName(report, CheckSourceVersion)
	if !found || check.Status != StatusBlocked || check.Code != CodeSourceVersionUnsupported {
		t.Fatalf("version check = %+v, want blocked %s", check, CodeSourceVersionUnsupported)
	}
	if want := "expected=v1.14.10 declared=v1.14.4"; check.Detail != want {
		t.Fatalf("version detail = %q, want %q", check.Detail, want)
	}
}

// TestVersionLadderFollowsThePin locks both halves of the rule: a marker
// introduced at or below the pin is part of the schema the pin already accepts,
// and a base table is never a marker at all. Without the release filter a re-pin
// would refuse its own source, which is exactly the defect the v1.14.10 re-pin
// would have hit: both markers the v1.14.4 pin was defined by are at or below
// the new pin.
//
// It also locks the ceiling that replaces the dropped revision floor. A pin at
// or above v1.14.6 accepts revision 3, so the marker itself leaves the ladder;
// without the ceiling the pinned ladder would be empty and a source holding
// revision 4 — or a row no 1.x release wrote — would pass.
func TestVersionLadderFollowsThePin(t *testing.T) {
	if pin := pinnedSourceVersion(); pin != "v1.14.10" {
		t.Fatalf("pinned source version = %q, want v1.14.10", pin)
	}
	baseOnly := sourceSchema{
		tables:   map[string]bool{"usage_archive_deleted_coverage_daily": true},
		indexes:  map[string]bool{},
		settings: map[string]string{},
	}
	pinnedLadder, err := newVersionLadder(pinnedSourceVersion())
	if err != nil {
		t.Fatalf("version ladder: %v", err)
	}
	if newer := pinnedLadder.newer(baseOnly); len(newer) != 0 {
		t.Fatalf("newer = %v for the base shape, want none: every 1.14.x release creates the deleted-raw summary", newer)
	}
	source := sourceSchema{
		tables:   map[string]bool{"usage_archive_deleted_coverage_daily": true},
		indexes:  map[string]bool{"idx_usage_events_ts_latency_ttft_cover": true},
		settings: map[string]string{"usage_cache_accounting_semantics_revision": "3"},
	}
	if newer := pinnedLadder.newer(source); len(newer) != 0 {
		t.Fatalf("newer against %s = %v, want none: the pinned release's own schema carries both markers", pinnedSourceVersion(), newer)
	}
	if check := versionCheck(SourceKindLegacyManager, nil, nil, pinnedLadder.newer(source), pinnedSourceVersion(), pinnedSourceVersion(), false); check.Status != StatusPass {
		t.Fatalf("version check against %s = %+v, want pass", pinnedSourceVersion(), check)
	}
	abovePin := source
	abovePin.settings = map[string]string{"usage_cache_accounting_semantics_revision": "4"}
	if newer := pinnedLadder.newer(abovePin); !reflect.DeepEqual(newer, []markerEvidence{
		{label: "usage_cache_accounting_semantics_revision=4", release: "after " + pinnedSourceVersion()},
	}) {
		t.Fatalf("newer against %s with revision 4 = %v, want the post-pin ceiling", pinnedSourceVersion(), newer)
	}
	ceilingCheck := versionCheck(SourceKindLegacyManager, nil, nil, pinnedLadder.newer(abovePin), pinnedSourceVersion(), pinnedSourceVersion(), false)
	if ceilingCheck.Status != StatusBlocked || ceilingCheck.Code != CodeSourceVersionUnsupported {
		t.Fatalf("version check for revision 4 = %+v, want blocked %s", ceilingCheck, CodeSourceVersionUnsupported)
	}
	if wantDetail := "usage_cache_accounting_semantics_revision=4 (after " + pinnedSourceVersion() + ")"; !strings.Contains(ceilingCheck.Detail, wantDetail) {
		t.Fatalf("version detail %q does not name %q", ceilingCheck.Detail, wantDetail)
	}
	unreadable := source
	unreadable.settings = map[string]string{"usage_cache_accounting_semantics_revision": "corrupt"}
	if newer := pinnedLadder.newer(unreadable); !reflect.DeepEqual(newer, []markerEvidence{
		{label: `usage_cache_accounting_semantics_revision="corrupt"`, release: "invalid"},
	}) {
		t.Fatalf("newer against %s with an unreadable revision = %v, want an invalid observation", pinnedSourceVersion(), newer)
	}
	if got := pinnedLadder.newer(unreadable)[0].String(); got != `usage_cache_accounting_semantics_revision="corrupt" (invalid)` {
		t.Fatalf("invalid marker string = %q, want single (invalid) suffix", got)
	}
	fourLadder, err := newVersionLadder("v1.14.4")
	if err != nil {
		t.Fatalf("version ladder for v1.14.4: %v", err)
	}
	want := []markerEvidence{
		{label: "usage_cache_accounting_semantics_revision=3", release: "v1.14.6"},
		{label: "idx_usage_events_ts_latency_ttft_cover", release: "v1.14.7"},
	}
	if newer := fourLadder.newer(source); !reflect.DeepEqual(newer, want) {
		t.Fatalf("newer against v1.14.4 = %v, want %v", newer, want)
	}
	sixLadder, err := newVersionLadder("v1.14.6")
	if err != nil {
		t.Fatalf("version ladder for v1.14.6: %v", err)
	}
	if newer := sixLadder.newer(source); !reflect.DeepEqual(newer, []markerEvidence{
		{label: "idx_usage_events_ts_latency_ttft_cover", release: "v1.14.7"},
	}) {
		t.Fatalf("newer against v1.14.6 = %v, want only the index", newer)
	}
	sevenLadder, err := newVersionLadder("v1.14.7")
	if err != nil {
		t.Fatalf("version ladder for v1.14.7: %v", err)
	}
	if newer := sevenLadder.newer(source); len(newer) != 0 {
		t.Fatalf("newer against v1.14.7 = %v, want none: that pin accepts both markers", newer)
	}
}

// TestVersionLadderRejectsAMalformedPinnedRelease keeps the parse an error
// instead of an empty ladder. An empty ladder means "nothing the source carries
// is newer", which accepts a source that moved past the pin — a fail-open in the
// direction that matters. Every shape below used to pass silently.
func TestVersionLadderRejectsAMalformedPinnedRelease(t *testing.T) {
	malformed := []string{
		"",               // no release at all
		"v1.14",          // two components
		"v1.14.4.1",      // four components
		"v1.14.x",        // non-numeric patch
		"v1.14.-1",       // negative patch
		"vOne.Four.Four", // bare words
		"1..4",           // empty component
		"v1.14.1000",     // component wider than the rank can pack
	}
	for _, release := range malformed {
		if _, err := newVersionLadder(release); err == nil {
			t.Fatalf("newVersionLadder(%q) accepted a malformed release instead of failing", release)
		}
	}
	if _, err := newVersionLadder(" v1.14.4 "); err != nil {
		t.Fatalf("newVersionLadder rejected a padded but valid release: %v", err)
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
	// A computable gap is operator information, not a failed inspection: it
	// asks for a later explicit decision and never blocks the source.
	if !report.Ready {
		t.Fatalf("a computable parity gap blocked the source: blocked = %v", report.Blocked)
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
	// A computable gap is operator information, not a failed inspection: it
	// asks for a later explicit decision and never blocks the source.
	if !report.Ready {
		t.Fatalf("a computable parity gap blocked the source: blocked = %v", report.Blocked)
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
		digest, err := fileContentDigest(context.Background(), path)
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

// TestPreflightFingerprintNamesTheSymlinkedFile pins L-2: a covered file that is
// a link is refused with upgrade_source_symlinked, and the detail names that
// file instead of reusing the lock package's "database inspection" wording for a
// key or a WAL sidecar.
func TestPreflightFingerprintNamesTheSymlinkedFile(t *testing.T) {
	for _, name := range []string{dataKeyFileName, databaseFileName + "-wal"} {
		t.Run(name, func(t *testing.T) {
			fixture := newSourceFixture(t, nil)
			path := filepath.Join(fixture.root, name)
			if _, err := os.Lstat(path); os.IsNotExist(err) {
				if err := os.WriteFile(path, []byte("sidecar\n"), 0o600); err != nil {
					t.Fatalf("create %s: %v", name, err)
				}
			}
			real := path + ".real"
			if err := os.Rename(path, real); err != nil {
				t.Fatalf("move %s: %v", name, err)
			}
			if err := os.Symlink(real, path); err != nil {
				t.Skipf("create %s symlink: %v", name, err)
			}

			report := preflight(t, fixture.root, Options{DeclaredVersion: pinnedSourceVersion()})
			if report.Ready {
				t.Fatalf("preflight accepted a symlinked %s", name)
			}
			check, found := checkByName(report, CheckFingerprint)
			if !found || check.Status != StatusBlocked || check.Code != CodeSourceSymlinked {
				t.Fatalf("fingerprint check = %+v, want blocked %q", check, CodeSourceSymlinked)
			}
			if !strings.Contains(check.Detail, name) {
				t.Fatalf("fingerprint detail %q does not name %s", check.Detail, name)
			}
			if strings.Contains(check.Detail, "database inspection") {
				t.Fatalf("fingerprint detail %q reuses the database wording for %s", check.Detail, name)
			}
			if !slices.Contains(report.Blocked, CodeSourceSymlinked) {
				t.Fatalf("preflight blocked %v, want %q", report.Blocked, CodeSourceSymlinked)
			}
		})
	}
}

// TestPreflightFingerprintCoversTheJournalAndNewRootFiles is the regression for
// the two gaps the fingerprint had: a rollback journal was invisible even though
// it changes what the database means on its first open, and a file appearing
// beside the source did not change the fingerprint at all.
func TestPreflightFingerprintCoversTheJournalAndNewRootFiles(t *testing.T) {
	fixture := newSourceFixture(t, nil)
	base := preflight(t, fixture.root, Options{})

	journal := filepath.Join(fixture.root, databaseFileName+"-journal")
	writeFixtureFile(t, journal, "journal-frames\n")
	if withJournal := preflight(t, fixture.root, Options{}); withJournal.Fingerprint == base.Fingerprint {
		t.Fatal("fingerprint did not change after a rollback journal appeared")
	}
	if err := os.Remove(journal); err != nil {
		t.Fatalf("remove the rollback journal: %v", err)
	}
	if restored := preflight(t, fixture.root, Options{}); restored.Fingerprint != base.Fingerprint {
		t.Fatal("fingerprint did not return to its original value after the journal was removed")
	}

	// A file beside the source is a change to the source, even though the copy
	// never carries it.
	writeFixtureFile(t, filepath.Join(fixture.root, "written-by-another-tool.bin"), "evidence\n")
	if withExtra := preflight(t, fixture.root, Options{}); withExtra.Fingerprint == base.Fingerprint {
		t.Fatal("fingerprint did not change after a new file appeared in the source root")
	}
}

// The Upgrade surface's credential read.
//
// Every authenticated request — including every status poll — proves the source
// administrator credential. These tests are the contract of that read: it proves
// the key the source holds, it reads one row instead of the whole database, it
// leaves a preserved 1.x source byte for byte untouched, and the file policy it
// applies is the same one the mount lookup and the preflight apply.
// newCredentialFixture writes a 1.x source whose administrator credential is key.
func newCredentialFixture(t *testing.T, key string) sourceFixture {
	t.Helper()
	credential, err := security.NewAdminCredential(key, "test")
	if err != nil {
		t.Fatalf("build credential: %v", err)
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatalf("encode credential: %v", err)
	}
	return newSourceFixture(t, func(t *testing.T, db *sql.DB) {
		execFixture(t, db, `insert into settings (key, value, updated_at_ms) values ('admin_credential_v1', ?, 1)`, string(raw))
	})
}

// TestVerifyAdminUsesTheSourceCredential proves the credential the bootstrap
// Upgrade entry verified is the only one this read accepts, and that a source
// with no credential proves nothing.
func TestVerifyAdminUsesTheSourceCredential(t *testing.T) {
	const key = "legacy-manager-credential"
	fixture := newCredentialFixture(t, key)
	if err := VerifyAdmin(context.Background(), fixture.root, key); err != nil {
		t.Fatalf("verify the source credential: %v", err)
	}
	for _, candidate := range []string{"", "another-Manager-key"} {
		if err := VerifyAdmin(context.Background(), fixture.root, candidate); !errors.Is(err, ErrAdminUnauthorized) {
			t.Fatalf("candidate %q = %v, want %v", candidate, err, ErrAdminUnauthorized)
		}
	}
	unrecorded := newSourceFixture(t, nil)
	if err := VerifyAdmin(context.Background(), unrecorded.root, key); !errors.Is(err, ErrAdminUnauthorized) {
		t.Fatalf("source without a credential = %v, want %v", err, ErrAdminUnauthorized)
	}
	if err := VerifyAdmin(context.Background(), filepath.Join(fixture.root, "absent"), key); err == nil {
		t.Fatal("absent root verified a credential")
	}
}

// largeCredentialSource builds a source whose database is large enough for a
// whole-file integrity scan to dominate, then corrupts the root page of its
// padding table. The integrity check must fail while the pages the credential
// row lives on stay readable, which is what separates "this read proves the
// credential" from "this read verifies the whole database".
func largeCredentialSource(t *testing.T, key string, megabytes int) sourceFixture {
	t.Helper()
	credential, err := security.NewAdminCredential(key, "test")
	if err != nil {
		t.Fatalf("build credential: %v", err)
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatalf("encode credential: %v", err)
	}
	paddingRoot := 0
	fixture := newSourceFixture(t, func(t *testing.T, db *sql.DB) {
		execFixture(t, db, `insert into settings (key, value, updated_at_ms) values ('admin_credential_v1', ?, 1)`, string(raw))
		execFixture(t, db, `create table padding (id integer primary key, payload blob)`)
		for range megabytes {
			execFixture(t, db, `insert into padding (payload) values (zeroblob(1048576))`)
		}
		if err := db.QueryRow(`select rootpage from sqlite_master where name = 'padding'`).Scan(&paddingRoot); err != nil {
			t.Fatalf("read the padding root page: %v", err)
		}
	})
	corruptDatabasePage(t, fixture.dbPath, paddingRoot)
	return fixture
}

// corruptDatabasePage overwrites one b-tree page with bytes no page type
// accepts. The damage is confined to the tree that page belongs to: the
// integrity check reports it, and every other tree still reads.
func corruptDatabasePage(t *testing.T, path string, page int) {
	t.Helper()
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture database: %v", err)
	}
	pageSize := int(binary.BigEndian.Uint16(file[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	offset := (page - 1) * pageSize
	if page < 2 || offset+pageSize > len(file) {
		t.Fatalf("page %d does not fit a %d-byte database", page, len(file))
	}
	for index := offset; index < offset+pageSize; index++ {
		file[index] = 0xff
	}
	if err := os.WriteFile(path, file, 0o600); err != nil {
		t.Fatalf("corrupt fixture database: %v", err)
	}
}

const (
	// credentialFixtureMegabytes keeps the fixture large enough that a whole-file
	// integrity scan and the private copy are both real work, without making one
	// authenticated request a multi-second test.
	credentialFixtureMegabytes = 8
)

// TestVerifyAdminReadsOnlyTheCredentialRow is the credential reader's contract:
// a source whose whole-file integrity check fails, and whose size dwarfs the one
// row the read needs, still proves the right key. Preflight, which does qualify
// the source, still runs the check and blocks on it.
//
// The source itself is read through a private copy (openCredentialSource), so
// this test no longer claims a per-read time budget: copying and digesting the
// source is what never reading it in place costs. What it still separates is the
// query — no quick_check and no schema scan — because the corrupt page outside
// the credential tree must not become a credential verdict.
func TestVerifyAdminReadsOnlyTheCredentialRow(t *testing.T) {
	const key = "legacy-manager-credential"
	fixture := largeCredentialSource(t, key, credentialFixtureMegabytes)
	// The corruption is real: the qualification path cannot read this file.
	if db, _, err := openImmutableSource(context.Background(), fixture.dbPath); err == nil {
		_ = db.Close()
		t.Fatal("the fixture passed the whole-database integrity check")
	}

	started := time.Now()
	if err := VerifyAdmin(context.Background(), fixture.root, key); err != nil {
		t.Fatalf("verify the source credential: %v", err)
	}
	t.Logf("credential read on a %d MB source (private copy included): %s", credentialFixtureMegabytes, time.Since(started))
	if err := VerifyAdmin(context.Background(), fixture.root, "another-Manager-key"); !errors.Is(err, ErrAdminUnauthorized) {
		t.Fatalf("foreign key = %v, want %v", err, ErrAdminUnauthorized)
	}

	report := preflight(t, fixture.root, declared())
	// One corruption is one refusal: the qualification path runs the whole-file
	// check and blocks on it, and nothing else about this source is refused.
	if report.Ready || len(report.Blocked) != 1 || report.Blocked[0] != CodeSourceUnreadable {
		t.Fatalf("blocked = %v, want [%s]", report.Blocked, CodeSourceUnreadable)
	}
}

// sourceEntry is one directory entry a preserved source must keep exactly: its
// kind, size, mode and modification time. Contents are compared through the
// digest, because a rewrite with the same bytes is still a write.
type sourceEntry struct {
	size  int64
	mode  os.FileMode
	mtime time.Time
	sha   string
}

// sourceEntries records every entry under a source root, including the
// directories a read must not create.
func sourceEntries(t *testing.T, root string) map[string]sourceEntry {
	t.Helper()
	entries := map[string]sourceEntry{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		record := sourceEntry{size: info.Size(), mode: info.Mode(), mtime: info.ModTime()}
		if entry.Type().IsRegular() {
			record.sha = mustDigest(t, path)
		}
		entries[path] = record
		return nil
	})
	if err != nil {
		t.Fatalf("read source entries: %v", err)
	}
	return entries
}

// requireUnchangedSource fails with the exact entry that changed, so a created
// sidecar is named instead of reported as a map difference.
func requireUnchangedSource(t *testing.T, before, after map[string]sourceEntry) {
	t.Helper()
	for path := range after {
		if _, found := before[path]; !found {
			t.Fatalf("the read created %s beside the source", filepath.Base(path))
		}
	}
	for path, expected := range before {
		actual, found := after[path]
		if !found {
			t.Fatalf("the read removed %s", filepath.Base(path))
		}
		if actual != expected {
			t.Fatalf("the read rewrote %s: %+v, want %+v", filepath.Base(path), actual, expected)
		}
	}
}

// TestCredentialReadLeavesAWALSourceUnchanged is the regression for a read that
// opened the 1.x source in place: mode=ro plus query_only forbids SQL writes but
// still lets SQLite create the WAL index, so a cleanly stopped WAL source went
// from five entries to seven on the first authenticated request. The fixture is
// exactly that cold source — WAL journal mode, writer closed, no -wal/-shm beside
// it — which is also the case the later cold-source shortcut (immutable=1) read in
// place; both now go through the private copy.
func TestCredentialReadLeavesAWALSourceUnchanged(t *testing.T) {
	const key = "legacy-manager-credential"
	fixture := newCredentialFixture(t, key)
	before := sourceEntries(t, fixture.root)

	if err := VerifyAdmin(context.Background(), fixture.root, key); err != nil {
		t.Fatalf("verify the source credential: %v", err)
	}
	requireUnchangedSource(t, before, sourceEntries(t, fixture.root))
}

// TestCredentialReadJoinsALiveWALAndLeavesItUnchanged proves the second half of
// the same invariant: a source whose committed rows are only in its WAL is still
// readable, because the read joins a private copy instead of the source, and the
// copy leaves every source entry exactly as it was.
func TestCredentialReadJoinsALiveWALAndLeavesItUnchanged(t *testing.T) {
	const key = "legacy-manager-credential"
	credential, err := security.NewAdminCredential(key, "test")
	if err != nil {
		t.Fatalf("build credential: %v", err)
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatalf("encode credential: %v", err)
	}
	fixture := newSourceFixture(t, nil)
	// Hold a live writer open with the credential still in the WAL, so the
	// committed row exists only in usage.sqlite-wal.
	writer, err := sql.Open("sqlite", fixture.dbPath)
	if err != nil {
		t.Fatalf("open live writer: %v", err)
	}
	writer.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = writer.Close() })
	execFixture(t, writer, `pragma wal_autocheckpoint = 0`)
	execFixture(t, writer, `insert into settings (key, value, updated_at_ms) values ('admin_credential_v1', ?, 1)`, string(raw))
	walPath := fixture.dbPath + "-wal"
	if info, err := os.Lstat(walPath); err != nil || info.Size() == 0 {
		t.Fatalf("fixture WAL = %v, %v; want committed rows in the WAL", info, err)
	}
	before := sourceEntries(t, fixture.root)

	if err := VerifyAdmin(context.Background(), fixture.root, key); err != nil {
		t.Fatalf("verify a credential held in the WAL: %v", err)
	}
	if err := VerifyAdmin(context.Background(), fixture.root, "another-Manager-key"); !errors.Is(err, ErrAdminUnauthorized) {
		t.Fatalf("foreign key on a WAL source = %v, want %v", err, ErrAdminUnauthorized)
	}
	requireUnchangedSource(t, before, sourceEntries(t, fixture.root))
}

// TestPreflightLeavesAWALSourceUnchanged runs the qualifying read over the same
// preserved source. The preflight takes the ownership fence and reads a private
// copy, so the fence is the only thing it may touch, and the fence belongs to the
// holder.
func TestPreflightLeavesAWALSourceUnchanged(t *testing.T) {
	fixture := newCredentialFixture(t, "legacy-manager-credential")
	before := sourceEntries(t, fixture.root)
	report := preflight(t, fixture.root, declared())
	if !report.Ready {
		t.Fatalf("ready = false, blocked = %v", report.Blocked)
	}
	requireUnchangedSource(t, before, sourceEntries(t, fixture.root))
}

// TestVerifyManagerCredentialReadsTheOwnStore covers the other file the surface
// may verify: the Manager's own store, which is this process's live database
// rather than a preserved source. The credential may still be in its WAL, so the
// read has to join one.
func TestVerifyManagerCredentialReadsTheOwnStore(t *testing.T) {
	const key = "manager-store-credential"
	credential, err := security.NewAdminCredential(key, "test")
	if err != nil {
		t.Fatalf("build credential: %v", err)
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatalf("encode credential: %v", err)
	}
	store := filepath.Join(t.TempDir(), databaseFileName)
	writer, err := sql.Open("sqlite", store)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	writer.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = writer.Close() })
	for _, statement := range []string{
		`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`,
		`pragma journal_mode = WAL`,
		`pragma wal_autocheckpoint = 0`,
	} {
		if _, err := writer.Exec(statement); err != nil {
			t.Fatalf("prepare store: %v", err)
		}
	}
	execFixture(t, writer, `insert into settings (key, value, updated_at_ms) values ('admin_credential_v1', ?, 1)`, string(raw))
	if info, err := os.Lstat(store + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("store WAL = %v, %v; want the credential in the WAL", info, err)
	}

	liveStore := CredentialSnapshotRoots{SourceKnown: true}
	if err := VerifyManagerCredential(context.Background(), store, liveStore, key); err != nil {
		t.Fatalf("verify the store credential: %v", err)
	}
	if err := VerifyManagerCredential(context.Background(), store, liveStore, "another-Manager-key"); !errors.Is(err, ErrAdminUnauthorized) {
		t.Fatalf("foreign key = %v, want %v", err, ErrAdminUnauthorized)
	}
	if err := VerifyManagerCredential(context.Background(), filepath.Join(t.TempDir(), "absent.sqlite"), liveStore, key); !errors.Is(err, ErrSourceDatabaseMissing) {
		t.Fatalf("absent store = %v, want %v", err, ErrSourceDatabaseMissing)
	}
	// A live store is read in place and needs no snapshot, so an unresolved
	// source root cannot make it refuse: only the materializing branch can.
	if err := VerifyManagerCredential(context.Background(), store, CredentialSnapshotRoots{}, key); err != nil {
		t.Fatalf("live store with an unresolved source root: %v", err)
	}
}

// TestVerifyManagerCredentialSnapshotsAMaterializingTarget is the service half
// of P1. A copy-in target that is still materializing holds the members the
// resumed move compares with its backup, including a copied WAL index. Opening
// such a store in place joins its WAL and rewrites `-shm`, so the Manager
// fallback proves the key against a private snapshot instead. Both the accepted
// key and the rejected key leave every entry of the target's data root exactly
// as they found it.
func TestVerifyManagerCredentialSnapshotsAMaterializingTarget(t *testing.T) {
	const key = "manager-target-credential"
	fixture := newCredentialFixture(t, key)
	writer, err := sql.Open("sqlite", fixture.dbPath)
	if err != nil {
		t.Fatalf("open the copied source: %v", err)
	}
	writer.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = writer.Close() })
	// Committed rows stay in the WAL, and the writer stays open, so the copied
	// target holds a live WAL and a real WAL index rather than a checkpointed
	// database with no sidecars.
	execFixture(t, writer, `pragma wal_autocheckpoint = 0`)
	execFixture(t, writer, `create table materializing_payload (value text)`)
	execFixture(t, writer, `insert into materializing_payload values ('committed-in-wal')`)
	if info, err := os.Lstat(fixture.dbPath + "-wal"); err != nil || info.Size() == 0 {
		t.Fatalf("the copied source has no WAL: info=%v err=%v", info, err)
	}

	targetRoot := t.TempDir()
	target := filepath.Join(targetRoot, databaseFileName)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		data, err := os.ReadFile(fixture.dbPath + suffix)
		if err != nil {
			t.Fatalf("read the restored member %q: %v", suffix, err)
		}
		if err := os.WriteFile(target+suffix, data, 0o600); err != nil {
			t.Fatalf("write the restored member %q: %v", suffix, err)
		}
	}
	if err := copyinmarker.Write(targetRoot, copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		Phase:                   copyinmarker.PhaseMaterializing,
		TargetDatabase:          target,
		SourceKind:              "upgraded-legacy",
		SourceFingerprintBefore: "fingerprint-before",
		SourceFingerprintAfter:  "fingerprint-after",
		BackupManifestSHA256:    "manifest",
	}); err != nil {
		t.Fatalf("publish the materializing marker: %v", err)
	}

	before := sourceEntries(t, targetRoot)
	// The source root is known to be absent for this fixture: the snapshot only
	// has to stay out of the target's own data root.
	roots := CredentialSnapshotRoots{Target: targetRoot, SourceKnown: true}
	if err := VerifyManagerCredential(context.Background(), target, roots, key); err != nil {
		t.Fatalf("verify the materializing target's credential: %v", err)
	}
	if err := VerifyManagerCredential(context.Background(), target, roots, "another-Manager-key"); !errors.Is(err, ErrAdminUnauthorized) {
		t.Fatalf("foreign key on a materializing target = %v, want %v", err, ErrAdminUnauthorized)
	}
	requireUnchangedSource(t, before, sourceEntries(t, targetRoot))
}

// TestSourceDatabaseFilePolicyIsSharedByBothPaths proves the preflight and the
// credential read apply one file policy: a source database reached through a
// symbolic link is refused by both, with the same code, because the file that is
// inspected has to be the file that is read.
func TestSourceDatabaseFilePolicyIsSharedByBothPaths(t *testing.T) {
	const key = "legacy-manager-credential"
	fixture := newCredentialFixture(t, key)
	real := fixture.dbPath + ".real"
	if err := os.Rename(fixture.dbPath, real); err != nil {
		t.Fatalf("move fixture database: %v", err)
	}
	if err := os.Symlink(real, fixture.dbPath); err != nil {
		t.Skipf("create database symlink: %v", err)
	}

	err := VerifyAdmin(context.Background(), fixture.root, key)
	if !errors.Is(err, ErrSourceSymlinked) {
		t.Fatalf("credential read through a symlink = %v, want %v", err, ErrSourceSymlinked)
	}
	if errors.Is(err, ErrAdminUnauthorized) || errors.Is(err, ErrSourceDatabaseMissing) {
		t.Fatalf("a symlinked source was misreported as %v", err)
	}

	report := preflight(t, fixture.root, declared())
	if report.Ready || len(report.Blocked) == 0 || report.Blocked[0] != CodeSourceSymlinked {
		t.Fatalf("blocked = %v, want [%s ...]", report.Blocked, CodeSourceSymlinked)
	}
	for _, code := range report.Blocked {
		if code == CodeSourceLockUnavailable || code == CodeSourceMissing || code == CodeSourceBusy || code == CodeSourceUnreadable {
			t.Fatalf("a symlinked source was reported as %s: %v", code, report.Blocked)
		}
	}
	check, found := checkByName(report, CheckSourceOwnership)
	if !found || check.Status != StatusBlocked || check.Code != CodeSourceSymlinked {
		t.Fatalf("ownership check = %+v, want blocked %s", check, CodeSourceSymlinked)
	}
}

// TestMountDatabaseAppliesTheCredentialFilePolicy proves the surface's mount
// lookup asks exactly the question the credential read will ask: the path must
// name the database itself, with content, never a link. A mount the policy
// refuses does not count as holding a database, so the one credential resolution
// falls back to the Manager store instead of pointing the read at a file it would
// refuse — which is what bricked the entry page. The refusal carries the code the
// preflight reports for the same mount.
func TestMountDatabaseAppliesTheCredentialFilePolicy(t *testing.T) {
	const (
		key          = "legacy-manager-credential"
		managerStore = "/srv/cpamp/usage.sqlite"
	)
	// No mount at all: the Manager store is the only credential file, and there is
	// no mount condition to name.
	if database, refusal := MountDatabase(""); database != "" || refusal != "" {
		t.Fatalf("MountDatabase(no mount) = %q, %q; want both empty", database, refusal)
	}
	if database := CredentialDatabase("", managerStore); database != managerStore {
		t.Fatalf("CredentialDatabase(no mount) = %q, want the Manager store %q", database, managerStore)
	}

	cases := []struct {
		name    string
		prepare func(t *testing.T, fixture sourceFixture)
		refusal string
	}{
		{name: "genuine database", prepare: func(t *testing.T, fixture sourceFixture) {}},
		{name: "symbolic link", prepare: func(t *testing.T, fixture sourceFixture) {
			real := fixture.dbPath + ".real"
			if err := os.Rename(fixture.dbPath, real); err != nil {
				t.Fatalf("move fixture database: %v", err)
			}
			if err := os.Symlink(real, fixture.dbPath); err != nil {
				t.Skipf("create database symlink: %v", err)
			}
		}, refusal: CodeSourceSymlinked},
		{name: "hard link", prepare: func(t *testing.T, fixture sourceFixture) {
			if err := os.Link(fixture.dbPath, fixture.dbPath+".alias"); err != nil {
				t.Skipf("create database hard link: %v", err)
			}
		}, refusal: CodeSourceLockUnavailable},
		{name: "empty database", prepare: func(t *testing.T, fixture sourceFixture) {
			if err := os.WriteFile(fixture.dbPath, nil, 0o600); err != nil {
				t.Fatalf("empty the fixture database: %v", err)
			}
		}, refusal: CodeSourceMissing},
		{name: "absent database", prepare: func(t *testing.T, fixture sourceFixture) {
			if err := os.Remove(fixture.dbPath); err != nil {
				t.Fatalf("remove the fixture database: %v", err)
			}
		}, refusal: CodeSourceMissing},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newCredentialFixture(t, key)
			testCase.prepare(t, fixture)

			database, refusal := MountDatabase(fixture.root)
			resolved := CredentialDatabase(fixture.root, managerStore)
			if testCase.refusal == "" {
				if refusal != "" || database != fixture.dbPath || resolved != fixture.dbPath {
					t.Fatalf("MountDatabase(genuine mount) = %q, %q; resolved %q", database, refusal, resolved)
				}
			} else {
				if refusal != testCase.refusal {
					t.Fatalf("MountDatabase refusal = %q, want %q", refusal, testCase.refusal)
				}
				if database != "" || resolved != managerStore {
					t.Fatalf("a refused mount resolved to %q (database %q); want the Manager store %q", resolved, database, managerStore)
				}
			}

			// One condition, one code: the report the operator reads names the same
			// condition the mount lookup refused.
			report := preflight(t, fixture.root, declared())
			named := false
			for _, code := range report.Blocked {
				if code == testCase.refusal {
					named = true
				}
			}
			if testCase.refusal != "" && !named {
				t.Fatalf("preflight blocked = %v, want it to name %s", report.Blocked, testCase.refusal)
			}
			if testCase.refusal == "" && !report.Ready {
				t.Fatalf("genuine mount is not ready: %v", report.Blocked)
			}
		})
	}
}

// TestVerifyAdminComparesTheCredentialItCopiedNotThePath proves the compared
// credential belongs to the source state the read copied. The private copy is
// taken before the query, so replacing the path afterwards cannot substitute a
// credential at all: the copied key still verifies, and the replacement's key is
// a foreign key — never a source-change verdict, because the copy was never
// touched by the swap.
func TestVerifyAdminComparesTheCredentialItCopiedNotThePath(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses to replace an open file, so the swap cannot be staged")
	}
	const inspectedKey = "legacy-manager-credential"
	const replacementKey = "another-manager-credential"
	fixture := newCredentialFixture(t, inspectedKey)
	replacement := newCredentialFixture(t, replacementKey)

	source, err := openCredentialSource(context.Background(), fixture.dbPath)
	if err != nil {
		t.Fatalf("open credential source: %v", err)
	}
	defer func() { _ = source.close() }()
	if err := os.Rename(replacement.dbPath, fixture.dbPath); err != nil {
		t.Fatalf("swap fixture database: %v", err)
	}
	// The copy is the inspected file. Reading it again proves the key it holds,
	// and the replacement's key cannot be proved from a file this read copied.
	if err := verifyAdminCredential(context.Background(), source, inspectedKey); err != nil {
		t.Fatalf("the copied source's own key = %v, want it verified", err)
	}
	err = verifyAdminCredential(context.Background(), source, replacementKey)
	if !errors.Is(err, ErrAdminUnauthorized) {
		t.Fatalf("the replacement's key against the copied source = %v, want %v", err, ErrAdminUnauthorized)
	}
	if errors.Is(err, ErrSourceDatabaseChanged) {
		t.Fatalf("a swap after the copy was reported as a source change: %v", err)
	}
}

// TestCredentialReadRefusesAWALThatAppearsWhileTheCopyIsTaken is the regression
// for reading a cold source in place. A source with no -wal beside it was opened
// immutable, which reads exactly the database file and never looks at a WAL that
// appears afterwards: a writer that started during the read went unnoticed and
// the key was proved against the state before it. The private copy is digested
// before and after it is taken, so the same race is a typed refusal.
func TestCredentialReadRefusesAWALThatAppearsWhileTheCopyIsTaken(t *testing.T) {
	const key = "legacy-manager-credential"
	fixture := newCredentialFixture(t, key)
	if _, err := os.Lstat(fixture.dbPath + "-wal"); !os.IsNotExist(err) {
		t.Fatalf("fixture WAL = %v; want a cleanly stopped source", err)
	}
	previous := afterSnapshotCopy
	t.Cleanup(func() { afterSnapshotCopy = previous })
	// The writer that appears at the copy boundary holds its own credential in a
	// WAL that is still there when the source is digested again.
	afterSnapshotCopy = func() {
		writer, err := sql.Open("sqlite", fixture.dbPath)
		if err != nil {
			t.Fatalf("open the writer that appears mid-copy: %v", err)
		}
		writer.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = writer.Close() })
		execFixture(t, writer, `pragma wal_autocheckpoint = 0`)
		execFixture(t, writer, `insert into settings (key, value, updated_at_ms) values ('written-by-the-new-writer', '1', 1)`)
		if info, err := os.Lstat(fixture.dbPath + "-wal"); err != nil || info.Size() == 0 {
			t.Fatalf("mid-copy WAL = %v, %v; want committed rows in the WAL", info, err)
		}
	}

	err := VerifyAdmin(context.Background(), fixture.root, key)
	if !errors.Is(err, ErrSourceDatabaseChanged) {
		t.Fatalf("a source that began writing during the copy = %v, want %v", err, ErrSourceDatabaseChanged)
	}
	if errors.Is(err, ErrAdminUnauthorized) {
		t.Fatalf("a source that began writing was reported as a credential verdict: %v", err)
	}
}

// TestVerifyAdminReportsADeadlineAsATimeout is the deadline contract: a healthy
// source that outlives the credential budget is a deadline, never an unreadable
// source and never a wrong key. The bound is placed inside the read, so the
// failure happens after the source opened.
func TestVerifyAdminReportsADeadlineAsATimeout(t *testing.T) {
	const key = "legacy-manager-credential"
	fixture := newCredentialFixture(t, key)
	previous := credentialVerifyTimeout
	credentialVerifyTimeout = -time.Second
	t.Cleanup(func() { credentialVerifyTimeout = previous })

	err := VerifyAdmin(context.Background(), fixture.root, key)
	if !errors.Is(err, ErrCredentialVerifyTimeout) {
		t.Fatalf("expired credential read = %v, want %v", err, ErrCredentialVerifyTimeout)
	}
	if errors.Is(err, ErrSourceDatabaseMissing) || errors.Is(err, ErrAdminUnauthorized) || errors.Is(err, ErrSourceDatabaseChanged) {
		t.Fatalf("a deadline was misreported as %v", err)
	}
}
