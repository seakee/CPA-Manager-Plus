package sqlite

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageprojection"
)

func TestUpgradeFromV1112LargeUsageFixtureIsMetadataBounded(t *testing.T) {
	// 100k rows exercises the required large-table startup boundary. The
	// offline index-build path is intentionally allowed to scan the table; a
	// second 500k copy makes the race suite spend many minutes rebuilding the
	// same indexes without adding migration coverage.
	for _, rowCount := range []int{100_000} {
		t.Run(fmt.Sprintf("rows_%d", rowCount), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "usage.sqlite")
			db := prepareV1112LargeUsageFixture(t, path, rowCount)
			before := readUsageEventsSummary(t, db)
			if err := db.Close(); err != nil {
				t.Fatalf("close v1.11.12 fixture: %v", err)
			}

			started := time.Now()
			db, err := Open(path)
			if err != nil {
				t.Fatalf("upgrade v1.11.12 fixture: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			if elapsed := time.Since(started); elapsed > 10*time.Second {
				t.Fatalf("v1.11.12 %d-row metadata migration took %s", rowCount, elapsed)
			}
			after := readUsageEventsSummary(t, db)
			if after != before {
				t.Fatalf("usage_events changed during upgrade: before=%#v after=%#v", before, after)
			}
			var integrity string
			if err := db.QueryRow(`pragma quick_check`).Scan(&integrity); err != nil {
				t.Fatalf("run SQLite quick_check after upgrade: %v", err)
			}
			if integrity != "ok" {
				t.Fatalf("SQLite quick_check after upgrade = %q", integrity)
			}
			assertTableCount(t, db, usageprojection.EventTable, 0)
			ftsLegacy, projectionLegacy, cleanupStatus := latestMonitoringCleanupJob(t, db)
			if cleanupStatus != "online_cleanup" || projectionLegacy == "" {
				t.Fatalf("v1.11.12 monitoring cleanup job = projection:%q status:%q", projectionLegacy, cleanupStatus)
			}
			assertTableCount(t, db, projectionLegacy, rowCount)
			searchExists, err := derivedTableExists(t.Context(), db, ftsLegacy)
			if err != nil {
				t.Fatalf("inspect parked v1.11.12 search index: %v", err)
			}
			if !searchExists {
				t.Fatal("populated v1.11.12 search index was not parked")
			}
			var searchDataRows int
			if err := db.QueryRow(`select count(*) from ` + ftsLegacy + `_data`).Scan(&searchDataRows); err != nil {
				t.Fatalf("inspect parked v1.11.12 search data: %v", err)
			}
			if searchDataRows <= 1 {
				t.Fatalf("parked v1.11.12 search data rows = %d, want populated index", searchDataRows)
			}

			if err := RunDerivedStartupMaintenance(t.Context(), db); err != nil {
				t.Fatalf("prepare post-listen indexes for v1.11.12 fixture: %v", err)
			}
			for _, indexName := range []string{
				"idx_usage_events_latest_request_auth_file",
				"idx_usage_events_latest_request_source",
			} {
				var count int
				if err := db.QueryRow(`select count(*) from sqlite_master
					where type = 'index' and name = ?`, indexName).Scan(&count); err != nil {
					t.Fatalf("inspect deferred upgraded index %s: %v", indexName, err)
				}
				if count != 0 {
					t.Fatalf("non-empty usage index %s was created during startup", indexName)
				}
			}
			var parkedSelectorIndexTable string
			if err := db.QueryRow(`select tbl_name from sqlite_master
				where type = 'index' and name = 'idx_usage_monitoring_selector_daily_bucket'`).Scan(&parkedSelectorIndexTable); err != nil {
				t.Fatalf("inspect parked selector index after startup: %v", err)
			}
			if parkedSelectorIndexTable == usageMonitoringSelectorDailyTable {
				t.Fatalf("startup reclaimed selector index from parked table")
			}
			indexResult, err := prepareDerivedIndexes(t.Context(), db, true)
			if err != nil {
				t.Fatalf("prepare deferred v1.11.12 indexes offline: %v", err)
			}
			if indexResult.Created == 0 {
				t.Fatalf("offline index preparation result = %+v", indexResult)
			}
			for indexName, tableName := range map[string]string{
				"idx_usage_events_latest_request_auth_file":       "usage_events",
				"idx_usage_events_latest_request_source":          "usage_events",
				"idx_usage_monitoring_event_projection_timestamp": usageprojection.EventTable,
				"idx_usage_monitoring_selector_daily_bucket":      usageMonitoringSelectorDailyTable,
			} {
				var indexedTable string
				if err := db.QueryRow(`select tbl_name from sqlite_master
					where type = 'index' and name = ?`, indexName).Scan(&indexedTable); err != nil {
					t.Fatalf("inspect upgraded index %s: %v", indexName, err)
				}
				if indexedTable != tableName {
					t.Fatalf("upgraded index %s table = %q, want %q", indexName, indexedTable, tableName)
				}
			}

			processed, handled, err := cleanupMonitoringFTSJobBatch(t.Context(), db, 1000)
			if err != nil {
				t.Fatalf("run one bounded legacy cleanup batch: %v", err)
			}
			if !handled || processed != 1000 {
				t.Fatalf("bounded legacy cleanup handled=%v processed=%d, want true/1000", handled, processed)
			}
			assertTableCount(t, db, "usage_events", rowCount)
		})
	}
}

func TestDamagedUsageHourlyAggregateWithLargeSchema3LedgerIsMetadataBounded(t *testing.T) {
	const rowCount = 100_000
	path := filepath.Join(t.TempDir(), "usage-hourly-aggregate-large-ledger.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open large-ledger fixture: %v", err)
	}
	if _, err := db.Exec(`with recursive ids(id) as (
		select 1 union all select id + 1 from ids where id < ?
	) insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, requested_model, created_at_ms
	) select 'aggregate-ledger-event-' || id, id, cast(id as text),
		'gpt-test', 'gpt-test', id from ids`, rowCount); err != nil {
		_ = db.Close()
		t.Fatalf("seed large usage event fixture: %v", err)
	}
	if _, err := db.Exec(`insert into usage_event_identity_ledger (
		event_hash, raw_event_id, timestamp_ms, bucket_ms,
		aggregate_schema_version, aggregate_structure_revision,
		first_seen_at_ms, updated_at_ms
	) select event_hash, id, timestamp_ms, 0, 3,
		printf('schema-3:model-1:rebuild-%d', id), created_at_ms, created_at_ms
	from usage_events`); err != nil {
		_ = db.Close()
		t.Fatalf("seed large schema-3 ledger fixture: %v", err)
	}
	if _, err := db.Exec(`update usage_hourly_aggregate_state set
		structure_revision = 'schema-3:model-1:rebuild-100000', status = 'ready',
		backfill_last_event_id = ?, coverage_event_id = ?, target_event_id = ?,
		processed_events = ? where aggregate_name = 'hourly_core'`,
		rowCount, rowCount, rowCount, rowCount); err != nil {
		_ = db.Close()
		t.Fatalf("seed large aggregate state fixture: %v", err)
	}
	if _, err := db.Exec(`drop table usage_hourly_aggregate_v1`); err != nil {
		_ = db.Close()
		t.Fatalf("damage aggregate table fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close large-ledger fixture: %v", err)
	}

	started := time.Now()
	db, err = Open(path)
	if err != nil {
		t.Fatalf("recover large-ledger fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("large schema-3 ledger metadata recovery took %s", elapsed)
	}
	assertTableCount(t, db, "usage_events", rowCount)
	assertTableCount(t, db, "usage_event_identity_ledger", rowCount)
	assertTableCount(t, db, "usage_hourly_aggregate_v1", 0)
	assertUsageHourlyAggregateReset(t, db, rowCount)
}

func TestUsageRollupLongContextUpgradeParksLargeTables(t *testing.T) {
	const rowCount = 100_000
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "large-rollup-columns.sqlite")))
	if err != nil {
		t.Fatalf("open large rollup fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`create table usage_account_model_rollups (id integer primary key)`,
		`create table usage_dashboard_hourly_rollups (id integer primary key)`,
		`create table usage_rollup_checkpoints (name text primary key)`,
		`insert into usage_rollup_checkpoints (name) values ('account_history'), ('dashboard_hourly')`,
		`with recursive ids(id) as (
			select 1 union all select id + 1 from ids where id < 100000
		) insert into usage_account_model_rollups (id) select id from ids`,
		`with recursive ids(id) as (
			select 1 union all select id + 1 from ids where id < 100000
		) insert into usage_dashboard_hourly_rollups (id) select id from ids`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("prepare large rollup fixture: %v", err)
		}
	}

	started := time.Now()
	if err := ensureUsageRollupLongContextColumns(db); err != nil {
		t.Fatalf("upgrade large rollup columns: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("large rollup metadata migration took %s", elapsed)
	}
	assertTableCount(t, db, "usage_account_model_rollups", 0)
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 0)
	assertTableCount(t, db, usageAccountModelRollupsLegacy, rowCount)
	assertTableCount(t, db, usageDashboardHourlyLegacy, rowCount)
	assertTableCount(t, db, "usage_rollup_checkpoints", 0)
	for _, tableName := range []string{"usage_account_model_rollups", "usage_dashboard_hourly_rollups"} {
		columns := migrationTableColumns(t, db, tableName)
		for _, columnName := range []string{
			"long_input_tokens",
			"long_output_tokens",
			"long_cached_tokens",
			"long_cache_read_tokens",
			"long_cache_creation_tokens",
		} {
			if !columns[columnName] {
				t.Fatalf("%s missing %s after large metadata migration", tableName, columnName)
			}
		}
	}
}

type usageEventsSummary struct {
	Rows          int64
	IDSum         int64
	TimestampSum  int64
	EventHashSize int64
}

func readUsageEventsSummary(t *testing.T, db *sql.DB) usageEventsSummary {
	t.Helper()
	var summary usageEventsSummary
	if err := db.QueryRow(`select count(*), coalesce(sum(id), 0),
		coalesce(sum(timestamp_ms), 0), coalesce(sum(length(event_hash)), 0)
		from usage_events`).Scan(
		&summary.Rows,
		&summary.IDSum,
		&summary.TimestampSum,
		&summary.EventHashSize,
	); err != nil {
		t.Fatalf("read usage_events summary: %v", err)
	}
	return summary
}

func prepareV1112LargeUsageFixture(t *testing.T, path string, rowCount int) *sql.DB {
	t.Helper()
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	if _, err := db.Exec(`with recursive ids(id) as (
		select 1 union all select id + 1 from ids where id < ?
	) insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, requested_model, created_at_ms
	) select 'v1112-event-' || id, id, cast(id as text), 'gpt-test', 'gpt-test', id
	from ids`, rowCount); err != nil {
		_ = db.Close()
		t.Fatalf("seed v1.11.12 usage events: %v", err)
	}
	statements := []string{
		`drop trigger if exists usage_monitoring_event_search_v1_insert`,
		`drop trigger if exists usage_monitoring_event_search_v1_update`,
		`drop trigger if exists usage_monitoring_event_search_v1_delete`,
		`drop table usage_monitoring_event_search_v1`,
		`drop table usage_monitoring_event_projection_v1`,
		`create table usage_monitoring_event_projection_v1 (
			event_id integer primary key,
			timestamp_ms integer not null,
			search_text text not null,
			provider text not null,
			executor_type text not null,
			model text not null,
			resolved_model text not null,
			auth_index text not null,
			source text not null,
			source_hash text not null,
			api_key_hash text not null,
			account_snapshot text not null,
			auth_label_snapshot text not null,
			auth_file_snapshot text not null,
			auth_provider_snapshot text not null,
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
		`create index idx_usage_monitoring_event_projection_timestamp
			on usage_monitoring_event_projection_v1(timestamp_ms desc, event_id desc)`,
		`create virtual table usage_monitoring_event_search_v1 using fts5(
			search_text,
			content = 'usage_monitoring_event_projection_v1',
			content_rowid = 'event_id',
			columnsize = 0,
			detail = 'none',
			tokenize = 'trigram'
		)`,
		`create trigger usage_monitoring_event_search_v1_insert
			after insert on usage_monitoring_event_projection_v1 begin
			insert into usage_monitoring_event_search_v1(rowid, search_text)
			values (new.event_id, new.search_text);
		end`,
		`create trigger usage_monitoring_event_search_v1_update
			after update of search_text on usage_monitoring_event_projection_v1 begin
			insert into usage_monitoring_event_search_v1(
				usage_monitoring_event_search_v1, rowid, search_text
			) values ('delete', old.event_id, old.search_text);
			insert into usage_monitoring_event_search_v1(rowid, search_text)
			values (new.event_id, new.search_text);
		end`,
		`create trigger usage_monitoring_event_search_v1_delete
			after delete on usage_monitoring_event_projection_v1 begin
			insert into usage_monitoring_event_search_v1(
				usage_monitoring_event_search_v1, rowid, search_text
			) values ('delete', old.event_id, old.search_text);
		end`,
		`insert into usage_monitoring_event_projection_v1 (
			event_id, timestamp_ms, search_text, provider, executor_type, model,
			resolved_model, auth_index, source, source_hash, api_key_hash,
			account_snapshot, auth_label_snapshot, auth_file_snapshot,
			auth_provider_snapshot, auth_project_id_snapshot, reasoning_effort,
			service_tier, failed, latency_ms, input_tokens, output_tokens,
			reasoning_tokens, cached_tokens, cache_tokens, cache_read_tokens,
			cache_creation_tokens, normalized_total_input_tokens, total_tokens,
			header_quota_plan_type, header_error_kind, header_error_code,
			header_trace_id, updated_at_ms
		) select id, timestamp_ms, event_hash, '', '', model, model, '', '', '', '',
			'', '', '', '', '', '', '', 0, null, 0, 0, 0, 0, 0, 0, 0, 0, 0,
			'', '', '', '', created_at_ms from usage_events`,
		`drop table usage_monitoring_selector_daily_rollups_v1`,
		`create table usage_monitoring_selector_daily_rollups_v1 (
			bucket_ms integer not null,
			model text not null,
			api_key_hash text not null,
			provider text not null,
			auth_file_snapshot text not null,
			account_snapshot text not null,
			auth_label_snapshot text not null,
			auth_index text not null,
			source text not null,
			source_hash text not null,
			updated_at_ms integer not null,
			primary key (
				bucket_ms, model, api_key_hash, provider, auth_file_snapshot,
				account_snapshot, auth_label_snapshot, auth_index, source_hash
			)
		)`,
		`create index idx_usage_monitoring_selector_daily_bucket
			on usage_monitoring_selector_daily_rollups_v1(bucket_ms)`,
		`insert into usage_monitoring_selector_daily_rollups_v1 values (
			0, 'gpt-test', '', '', '', '', '', '', '', '', 1
		)`,
		`delete from settings where key in (
			'usage_monitoring_model_format_version',
			'usage_account_history_identity_format_version'
		)`,
		`insert into settings (key, value, updated_at_ms) values (
			'usage_dashboard_hourly_format_version', '2', 0
		) on conflict(key) do update set value = excluded.value, updated_at_ms = 0`,
		`update usage_monitoring_rollup_state set structure_revision = '',
			status = 'ready', backfill_last_event_id = ?, coverage_event_id = ?,
			target_event_id = ?, processed_events = ?`,
		`update usage_monitoring_search_index_state set ready = 1, updated_at_ms = 1`,
		`update usage_hourly_aggregate_state set schema_version = 1,
			structure_revision = '', status = 'ready', backfill_last_event_id = ?,
			coverage_event_id = ?, target_event_id = ?, processed_events = ?
			where aggregate_name = 'hourly_core'`,
		`alter table usage_hourly_aggregate_state drop column structure_revision`,
		`alter table usage_event_identity_ledger drop column aggregate_structure_revision`,
		`drop table account_quota_snapshots`,
		`drop table account_quota_cycles`,
		`drop table account_quota_window_activations`,
		`drop table account_quota_windows`,
		`drop table account_quota_observations`,
		`delete from usage_data_migrations where name = 'quota_snapshot_lifecycle_v1'`,
	}
	for _, statement := range statements {
		args := []any(nil)
		if strings.HasPrefix(statement, "update usage_monitoring_rollup_state") ||
			strings.HasPrefix(statement, "update usage_hourly_aggregate_state") {
			args = []any{rowCount, rowCount, rowCount, rowCount}
		}
		if _, err := db.Exec(statement, args...); err != nil {
			_ = db.Close()
			t.Fatalf("prepare v1.11.12 fixture statement %q: %v", statement, err)
		}
	}
	return db
}
