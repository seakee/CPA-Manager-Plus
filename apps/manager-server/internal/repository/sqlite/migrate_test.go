package sqlite

import (
	"context"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	quotasnapshotrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotasnapshot"
	pricingrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagepricing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

func TestUsageDataMigrationInitialStateMatchesExistingUsageData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-data-migration.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open empty sqlite: %v", err)
	}
	var status string
	if err := db.QueryRow(`select status from usage_data_migrations where name = 'usage_cache_accounting_v2'`).Scan(&status); err != nil {
		t.Fatalf("read empty migration state: %v", err)
	}
	if status != "completed" {
		t.Fatalf("empty migration status = %q, want completed", status)
	}
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, input_tokens, created_at_ms
	) values ('legacy', 1, '1', 'gpt-test', 1, 1)`); err != nil {
		t.Fatalf("insert legacy event: %v", err)
	}
	if _, err := db.Exec(`drop table usage_data_migrations`); err != nil {
		t.Fatalf("drop migration table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen legacy sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.QueryRow(`select status from usage_data_migrations where name = 'usage_cache_accounting_v2'`).Scan(&status); err != nil {
		t.Fatalf("read legacy migration state: %v", err)
	}
	if status != "discovering" {
		t.Fatalf("legacy migration status = %q, want discovering", status)
	}
	columns := migrationTableColumns(t, db, "usage_cache_accounting_v2_changes")
	for _, column := range []string{
		"event_id",
		"cache_input_mode",
		"normalized_uncached_input_tokens",
		"normalized_total_input_tokens",
		"normalized_cache_read_tokens",
		"normalized_cache_creation_tokens",
		"total_tokens",
	} {
		if !columns[column] {
			t.Fatalf("staging columns = %#v, missing %s", columns, column)
		}
	}
}

func TestMigrateWithoutUsageEventsClearsDeferredIndexLedger(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-source-deferred-index.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`insert into usage_derived_deferred_indexes (
		index_name, table_name, reason, created_at_ms, updated_at_ms
	) values ('idx_usage_account_model_rollups_last_seen',
		'usage_account_model_rollups', 'deferred_indexes', 1, 1)`); err != nil {
		_ = db.Close()
		t.Fatalf("seed deferred index ledger: %v", err)
	}
	if _, err := db.Exec(`drop table usage_events`); err != nil {
		_ = db.Close()
		t.Fatalf("drop authoritative source fixture: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close source-missing fixture: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen source-missing fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var deferredIndexes int
	if err := db.QueryRow(`select count(*) from usage_derived_deferred_indexes`).Scan(&deferredIndexes); err != nil {
		t.Fatalf("count reset deferred index ledger: %v", err)
	}
	if deferredIndexes != 0 {
		t.Fatalf("deferred index ledger after source recovery = %d, want 0", deferredIndexes)
	}
}

func TestMigrateCreatesLatestAccountRequestIndexes(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "latest-account-request-indexes.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, name := range []string{
		"idx_usage_events_latest_request_auth_file",
		"idx_usage_events_latest_request_source",
	} {
		var count int
		if err := db.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = ?`, name).Scan(&count); err != nil {
			t.Fatalf("inspect deferred usage event index %s: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("usage event index %s exists before post-listen maintenance", name)
		}
	}
	if err := RunDerivedStartupMaintenance(context.Background(), db); err != nil {
		t.Fatalf("run post-listen index preparation: %v", err)
	}

	rows, err := db.Query(`pragma index_list(usage_events)`)
	if err != nil {
		t.Fatalf("list usage event indexes: %v", err)
	}
	defer rows.Close()
	indexes := map[string]bool{}
	for rows.Next() {
		var sequence int
		var name string
		var unique int
		var origin string
		var partial int
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan usage event index: %v", err)
		}
		indexes[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate usage event indexes: %v", err)
	}
	for _, name := range []string{
		"idx_usage_events_latest_request_auth_file",
		"idx_usage_events_latest_request_source",
	} {
		if !indexes[name] {
			t.Fatalf("usage event indexes = %#v, missing %s", indexes, name)
		}
	}
}

func TestMigrateCreatesAccountQuotaSnapshotSchema(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "account-quota-snapshots.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	columns := migrationTableColumns(t, db, "account_quota_snapshots")
	for _, column := range []string{
		"observation_id",
		"logical_window_id",
		"activation_id",
		"cycle_id",
		"account_key",
		"provider",
		"provider_window_id",
		"window_kind",
		"window_mode",
		"model_scope_kind",
		"model_scope_key",
		"model_ids_json",
		"scope_fingerprint",
		"content_hash",
		"source",
		"source_observation_id",
		"observed_at_ms",
		"boundary_accuracy",
		"cycle_start_ms",
		"cycle_end_ms",
		"duration_seconds",
		"used_percent",
		"remaining_percent",
		"used_value",
		"limit_value",
		"quota_unit",
		"reset_credits_available",
		"reset_credits_json",
		"plan_type",
		"created_at_ms",
	} {
		if !columns[column] {
			t.Fatalf("account quota snapshot columns = %#v, missing %s", columns, column)
		}
	}
	for _, name := range []string{
		"idx_quota_snapshots_latest",
		"idx_quota_snapshots_observation",
		"idx_quota_snapshots_window_cycle",
		"idx_quota_snapshots_cycle_evidence",
	} {
		var count int
		if err := db.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = ?`, name).Scan(&count); err != nil {
			t.Fatalf("inspect deferred account quota snapshot index %s: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("account quota snapshot index %s exists before post-listen maintenance", name)
		}
	}
	if err := RunDerivedStartupMaintenance(context.Background(), db); err != nil {
		t.Fatalf("run post-listen quota index preparation: %v", err)
	}

	rows, err := db.Query(`pragma index_list(account_quota_snapshots)`)
	if err != nil {
		t.Fatalf("list account quota snapshot indexes: %v", err)
	}
	defer rows.Close()
	indexes := map[string]bool{}
	for rows.Next() {
		var sequence int
		var name string
		var unique int
		var origin string
		var partial int
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			t.Fatalf("scan account quota snapshot index: %v", err)
		}
		indexes[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate account quota snapshot indexes: %v", err)
	}
	for _, name := range []string{
		"idx_quota_snapshots_latest",
		"idx_quota_snapshots_observation",
		"idx_quota_snapshots_window_cycle",
		"idx_quota_snapshots_cycle_evidence",
	} {
		if !indexes[name] {
			t.Fatalf("account quota snapshot indexes = %#v, missing %s", indexes, name)
		}
	}

	for _, table := range []string{
		"account_quota_observations",
		"account_quota_windows",
		"account_quota_window_activations",
		"account_quota_cycles",
	} {
		var count int
		if err := db.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("query quota lifecycle table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("quota lifecycle table %s count = %d, want 1", table, count)
		}
	}

	observationColumns := migrationTableColumns(t, db, "account_quota_observations")
	if !observationColumns["lifecycle_applied"] {
		t.Fatalf("account quota observation columns = %#v, missing lifecycle_applied", observationColumns)
	}

	cycleColumns := migrationTableColumns(t, db, "account_quota_cycles")
	for _, column := range []string{"actual_start_ms", "actual_end_ms", "end_reason", "parent_cycle_id"} {
		if !cycleColumns[column] {
			t.Fatalf("account quota cycle columns = %#v, missing %s", cycleColumns, column)
		}
	}
}

func TestMigrateRepairsUsageMonitoringWithoutDroppingQuotaOrUsageData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "combined-quota-monitoring-migration.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`insert into usage_events (
			event_hash, timestamp_ms, timestamp, model, created_at_ms
		) values ('preserved-usage-event', 1000, '1000', 'gpt-test', 1000)`,
		`insert into account_quota_snapshots (
			account_key, provider, provider_window_id, window_kind, window_mode,
			model_scope_kind, source, observed_at_ms, boundary_accuracy, created_at_ms
		) values (
			'preserved-account', 'codex', 'weekly', 'weekly', 'fixed',
			'all', 'inspection', 1000, 'exact', 1000
		)`,
		`drop table usage_monitoring_event_projection_v1`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare combined migration fixture: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close damaged sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen damaged sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("repeat combined migration: %v", err)
	}

	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, "account_quota_snapshots", 1)
	var logicalWindowID sql.NullInt64
	if err := db.QueryRow(`select logical_window_id from account_quota_snapshots
		where account_key = 'preserved-account'`).Scan(&logicalWindowID); err != nil {
		t.Fatalf("read preserved quota lifecycle: %v", err)
	}
	if logicalWindowID.Valid {
		t.Fatal("startup migration synchronously backfilled preserved quota snapshot")
	}
	result, err := quotasnapshotrepo.BackfillLegacySnapshotsBatch(context.Background(), db, 1000)
	if err != nil {
		t.Fatalf("run deferred quota lifecycle backfill: %v", err)
	}
	if result.Processed != 1 || !result.Completed {
		t.Fatalf("deferred quota lifecycle backfill = %#v, want one completed snapshot", result)
	}
	if err := db.QueryRow(`select logical_window_id from account_quota_snapshots
		where account_key = 'preserved-account'`).Scan(&logicalWindowID); err != nil {
		t.Fatalf("read deferred quota lifecycle result: %v", err)
	}
	if !logicalWindowID.Valid {
		t.Fatal("deferred quota snapshot was not attached to lifecycle")
	}
	var projectionTables int
	if err := db.QueryRow(`select count(*) from sqlite_master
		where type = 'table' and name = 'usage_monitoring_event_projection_v1'`).Scan(&projectionTables); err != nil {
		t.Fatalf("read repaired usage monitoring projection: %v", err)
	}
	if projectionTables != 1 {
		t.Fatalf("usage monitoring projection tables = %d, want 1", projectionTables)
	}
	var projectionStatus string
	if err := db.QueryRow(`select status from usage_monitoring_rollup_state
		where rollup_name = 'projection_v1'`).Scan(&projectionStatus); err != nil {
		t.Fatalf("read repaired projection state: %v", err)
	}
	if projectionStatus != "pending" {
		t.Fatalf("repaired projection status = %q, want pending", projectionStatus)
	}
}

func TestMigrateClearsDerivedUsageWhenSourceTableWasLost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lost-usage-events.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`insert into usage_events (
			event_hash, timestamp_ms, timestamp, model, input_tokens,
			output_tokens, total_tokens, created_at_ms
		) values ('lost-source-event', 3600001, '1970-01-01T01:00:00.001Z',
			'gpt-test', 10, 5, 15, 1)`,
		`insert into usage_account_model_rollups (
			account_key, model, billing_model, service_tier, calls,
			first_seen_ms, last_seen_ms, updated_at_ms
		) values ('account-1', 'gpt-test', 'gpt-test', '', 1, 1, 1, 1)`,
		`insert into usage_dashboard_hourly_rollups (
			bucket_ms, model, billing_model, service_tier, calls, updated_at_ms
		) values (3600000, 'gpt-test', 'gpt-test', '', 1, 1)`,
		`insert into usage_rollup_checkpoints (name, last_event_id, updated_at_ms)
		values ('account_history', 1, 1), ('dashboard_hourly', 1, 1)`,
		`insert into usage_hourly_aggregate_v1 (
			bucket_ms, model, billing_model, service_tier, failed, calls, updated_at_ms
		) values (3600000, 'gpt-test', 'gpt-test', '', 0, 1, 1)`,
		`insert into usage_event_identity_ledger (
			event_hash, raw_event_id, timestamp_ms, bucket_ms,
			aggregate_schema_version, first_seen_at_ms, updated_at_ms
		) select event_hash, id, timestamp_ms, 3600000, 2, created_at_ms, 1
		from usage_events where event_hash = 'lost-source-event'`,
		`update usage_hourly_aggregate_state set
			status = 'ready', backfill_last_event_id = 1, coverage_event_id = 1,
			target_event_id = 1, processed_events = 1
		where aggregate_name = 'hourly_core'`,
		`insert into usage_pricing_hourly_rollups_v1 (
			structure_revision, bucket_ms, model, billing_model, pricing_model,
			service_tier, context_threshold_tokens, failed, calls, updated_at_ms
		) values ('revision-1', 3600000, 'gpt-test', 'gpt-test', 'gpt-test',
			'', -1, 0, 1, 1)`,
		`insert into usage_pricing_account_rollups_v1 (
			structure_revision, account_key, model, billing_model, pricing_model,
			service_tier, context_threshold_tokens, calls,
			first_seen_ms, last_seen_ms, updated_at_ms
		) values ('revision-1', 'account-1', 'gpt-test', 'gpt-test', 'gpt-test',
			'', -1, 1, 1, 1, 1)`,
		`update usage_pricing_rollup_state set
			structure_revision = 'revision-1', status = 'ready',
			backfill_last_event_id = 1, coverage_event_id = 1,
			target_event_id = 1, processed_events = 1
		where rollup_name = 'pricing_v1'`,
		`insert into usage_monitoring_event_projection_v1 (
			event_id, timestamp_ms, search_text, account_key, provider,
			executor_type, model, requested_model, analytics_model,
			resolved_model, auth_index, source, source_hash, api_key_hash,
			account_snapshot, auth_label_snapshot, auth_file_snapshot,
			auth_provider_snapshot, auth_project_id_snapshot,
			reasoning_effort, service_tier, failed, input_tokens,
			output_tokens, reasoning_tokens, cached_tokens, cache_tokens,
			cache_read_tokens, cache_creation_tokens,
			normalized_total_input_tokens, total_tokens,
			header_quota_plan_type, header_error_kind, header_error_code,
			header_trace_id, updated_at_ms
		) values (1, 3600001, 'gpt-test', 'account-1', '', '', 'gpt-test',
			'', 'gpt-test', '', '', '', '', '', '', '', '', '', '', '', '',
			0, 10, 5, 0, 0, 0, 0, 0, 10, 15, '', '', '', '', 1)`,
		`insert into usage_monitoring_account_daily_rollups_v1 (
			structure_revision, bucket_ms, account_snapshot, auth_label_snapshot,
			provider, auth_provider_snapshot, auth_index, source, source_hash,
			auth_file_snapshot, api_key_hash, executor_type, model, billing_model,
			pricing_model, service_tier, context_threshold_tokens, failed,
			calls, last_seen_ms, updated_at_ms
		) values ('revision-1', 0, '', '', '', '', '', '', '', '', '', '',
			'gpt-test', 'gpt-test', 'gpt-test', '', -1, 0, 1, 1, 1)`,
		`insert into usage_monitoring_api_key_daily_rollups_v1 (
			structure_revision, bucket_ms, api_key_hash, account_snapshot,
			auth_label_snapshot, provider, auth_provider_snapshot, auth_index,
			source, source_hash, auth_file_snapshot, executor_type, model,
			billing_model, pricing_model, service_tier, context_threshold_tokens,
			failed, calls, last_seen_ms, updated_at_ms
		) values ('revision-1', 0, '', '', '', '', '', '', '', '', '', '',
			'gpt-test', 'gpt-test', 'gpt-test', '', -1, 0, 1, 1, 1)`,
		`insert into usage_monitoring_selector_daily_rollups_v1 (
			bucket_ms, model, api_key_hash, provider, auth_file_snapshot,
			account_snapshot, auth_label_snapshot, auth_index, source,
			source_hash, updated_at_ms
		) values (0, 'gpt-test', '', '', '', '', '', '', '', '', 1)`,
		`insert into usage_monitoring_header_latest_v1 (
			snapshot_key, event_id, event_hash, timestamp_ms,
			auth_file_snapshot, auth_index, account_snapshot,
			auth_label_snapshot, auth_provider_snapshot,
			auth_project_id_snapshot, source, source_hash,
			response_metadata_json, header_quota_plan_type,
			header_error_kind, header_error_code, header_trace_id,
			updated_at_ms
		) values ('snapshot-1', 1, 'lost-source-event', 3600001,
			'', '', '', '', '', '', '', '', '{}', '', '', '', '', 1)`,
		`update usage_monitoring_rollup_state set
			structure_revision = 'revision-1', status = 'ready',
			backfill_last_event_id = 1, coverage_event_id = 1,
			target_event_id = 1, processed_events = 1`,
		`update usage_monitoring_search_index_state set ready = 1, updated_at_ms = 1
		where id = 1`,
		`update usage_data_migrations set
			status = 'completed', last_event_id = 1, target_event_id = 1,
			processed_rows = 1, changed_rows = 1,
			started_at_ms = 1, updated_at_ms = 1, finished_at_ms = 1
		where name in ('usage_cache_accounting_v1', 'usage_cache_accounting_v2')`,
		`insert into usage_cache_accounting_v2_changes (
			event_id, cache_input_mode, normalized_uncached_input_tokens,
			normalized_total_input_tokens, normalized_cache_read_tokens,
			normalized_cache_creation_tokens, total_tokens
		) values (1, 'included', 10, 10, 0, 0, 15)`,
		`alter table usage_data_migrations drop column applied_rows`,
		`alter table usage_data_migrations drop column changed_rows`,
		`drop table usage_monitoring_event_search_v1`,
		`drop table usage_events`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare lost usage source fixture: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close lost usage source fixture: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen lost usage source fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, table := range []string{
		"usage_events",
		"usage_account_model_rollups",
		"usage_dashboard_hourly_rollups",
		"usage_hourly_aggregate_v1",
		"usage_pricing_hourly_rollups_v1",
		"usage_pricing_account_rollups_v1",
		"usage_monitoring_account_daily_rollups_v1",
		"usage_monitoring_api_key_daily_rollups_v1",
		"usage_monitoring_selector_daily_rollups_v1",
		"usage_monitoring_event_projection_v1",
		"usage_monitoring_event_search_v1",
		"usage_monitoring_header_latest_v1",
		"usage_cache_accounting_v2_changes",
	} {
		assertTableCount(t, db, table, 0)
	}
	assertTableCount(t, db, "usage_event_identity_ledger", 1)
	var rawEventID sql.NullInt64
	var aggregateVersion int
	if err := db.QueryRow(`select raw_event_id, aggregate_schema_version
		from usage_event_identity_ledger where event_hash = 'lost-source-event'`).Scan(
		&rawEventID,
		&aggregateVersion,
	); err != nil {
		t.Fatalf("read detached usage identity ledger: %v", err)
	}
	if !rawEventID.Valid || rawEventID.Int64 != 1 || aggregateVersion != 2 {
		t.Fatalf("preserved legacy identity ledger = raw:%v aggregate_version:%d", rawEventID, aggregateVersion)
	}
	assertUsageHourlyAggregateState(t, db, "ready", 0)
	assertEmptyUsageDerivedState(t, db)
}

func TestMigrateRebuildsProjectionForAccountKeyIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-monitoring-projection.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, model, auth_index,
		auth_file_snapshot, account_snapshot, input_tokens, total_tokens,
		created_at_ms
	) values ('legacy-projection-event', 1000, '1000', 'codex', 'gpt-test',
		'auth-1', 'credential.json', 'legacy@example.com', 10, 10, 1000)`); err != nil {
		_ = db.Close()
		t.Fatalf("insert legacy usage event: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		_ = db.Close()
		t.Fatalf("begin projection fixture: %v", err)
	}
	if err := usageprojection.UpsertEventRange(ctx, tx, 0, 1, 1000); err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		t.Fatalf("seed projection fixture: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `update usage_monitoring_rollup_state set
		status = 'ready', backfill_last_event_id = 1, coverage_event_id = 1,
		target_event_id = 1, processed_events = 1
		where rollup_name = 'projection_v1'`); err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		t.Fatalf("seed projection state: %v", err)
	}
	if err := tx.Commit(); err != nil {
		_ = db.Close()
		t.Fatalf("commit projection fixture: %v", err)
	}
	for _, statement := range []string{
		`drop index if exists idx_usage_monitoring_event_projection_account_window`,
		`alter table usage_monitoring_event_projection_v1 drop column account_key`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare legacy projection schema: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy projection sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen legacy projection sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, usageprojection.EventTable, 0)
	_, projectionLegacy, cleanupStatus := latestMonitoringCleanupJob(t, db)
	if cleanupStatus != "online_cleanup" || projectionLegacy == "" {
		t.Fatalf("monitoring cleanup job = projection:%q status:%q", projectionLegacy, cleanupStatus)
	}
	assertTableCount(t, db, projectionLegacy, 1)
	columns := migrationTableColumns(t, db, "usage_monitoring_event_projection_v1")
	if !columns["account_key"] {
		t.Fatalf("projection columns = %#v, missing account_key", columns)
	}
	var accountWindowIndexes int
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = 'idx_usage_monitoring_event_projection_account_window'`).Scan(&accountWindowIndexes); err != nil {
		t.Fatalf("inspect account window index: %v", err)
	}
	if accountWindowIndexes != 0 {
		t.Fatalf("account window indexes before post-listen maintenance = %d, want 0", accountWindowIndexes)
	}
	var status string
	var coverageEventID, targetEventID int64
	if err := db.QueryRow(`select status, coverage_event_id, target_event_id
		from usage_monitoring_rollup_state where rollup_name = 'projection_v1'`).Scan(&status, &coverageEventID, &targetEventID); err != nil {
		t.Fatalf("read rebuilt projection state: %v", err)
	}
	if status != "pending" || coverageEventID != 0 || targetEventID != 1 {
		t.Fatalf("rebuilt projection state = status:%q coverage:%d target:%d", status, coverageEventID, targetEventID)
	}
}

func TestMigrateSeparatesCodexAccountSnapshotAndUpgradesMonitoringRollupPK(t *testing.T) {
	path := filepath.Join(t.TempDir(), "codex-account-snapshot-migration.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, model, auth_project_id_snapshot,
		created_at_ms
	) values ('legacy-codex-marker', 1000, '1000', 'codex', 'gpt-test',
		'codex-account-id:v1:historical-account', 1000)`); err != nil {
		_ = db.Close()
		t.Fatalf("insert immutable usage event: %v", err)
	}
	if _, err := db.Exec(`drop table usage_monitoring_account_daily_rollups_v1`); err != nil {
		_ = db.Close()
		t.Fatalf("drop current account rollup: %v", err)
	}
	if _, err := db.Exec(`create table usage_monitoring_account_daily_rollups_v1 (
		structure_revision text not null,
		bucket_ms integer not null,
		account_snapshot text not null,
		auth_label_snapshot text not null,
		provider text not null,
		auth_provider_snapshot text not null,
		auth_index text not null,
		source text not null,
		source_hash text not null,
		auth_file_snapshot text not null,
		api_key_hash text not null,
		executor_type text not null,
		model text not null,
		billing_model text not null,
		pricing_model text not null,
		service_tier text not null,
		context_threshold_tokens integer not null,
		failed integer not null,
		calls integer not null default 0,
		input_tokens integer not null default 0,
		output_tokens integer not null default 0,
		reasoning_tokens integer not null default 0,
		cached_tokens integer not null default 0,
		cache_read_tokens integer not null default 0,
		cache_creation_tokens integer not null default 0,
		total_tokens integer not null default 0,
		zero_token_calls integer not null default 0,
		latency_sum_ms integer not null default 0,
		latency_samples integer not null default 0,
		last_seen_ms integer not null,
		updated_at_ms integer not null,
		primary key (
			structure_revision, bucket_ms, account_snapshot, auth_label_snapshot,
			provider, auth_provider_snapshot, auth_index, source, source_hash,
			auth_file_snapshot, api_key_hash, executor_type, model, billing_model,
			pricing_model, service_tier, context_threshold_tokens, failed
		)
	)`); err != nil {
		_ = db.Close()
		t.Fatalf("create legacy account rollup: %v", err)
	}
	if _, err := db.Exec(`insert into usage_monitoring_account_daily_rollups_v1 (
		structure_revision, bucket_ms, account_snapshot, auth_label_snapshot,
		provider, auth_provider_snapshot, auth_index, source, source_hash,
		auth_file_snapshot, api_key_hash, executor_type, model, billing_model,
		pricing_model, service_tier, context_threshold_tokens, failed, calls,
		last_seen_ms, updated_at_ms
	) values ('legacy', 0, 'same@example.com', 'Same', 'codex', 'codex',
		'auth-1', 'auth.json', 'source-hash', 'auth.json', '', '', 'gpt-test',
		'gpt-test', 'gpt-test', '', -1, 0, 1, 1000, 1000)`); err != nil {
		_ = db.Close()
		t.Fatalf("insert legacy account rollup: %v", err)
	}
	var beforeProject string
	if err := db.QueryRow(`select auth_project_id_snapshot from usage_events where event_hash = 'legacy-codex-marker'`).Scan(&beforeProject); err != nil {
		_ = db.Close()
		t.Fatalf("read immutable event before migration: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen legacy sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := Migrate(db); err != nil {
		t.Fatalf("repeat account snapshot migration: %v", err)
	}

	columns := migrationTableColumns(t, db, usageMonitoringAccountDailyTable)
	if !columns["auth_account_id_snapshot"] {
		t.Fatalf("current account rollup columns = %#v, missing auth_account_id_snapshot", columns)
	}
	assertTableCount(t, db, usageMonitoringAccountDailyTable, 0)
	assertTableCount(t, db, usageMonitoringAccountIdentityLegacy, 1)
	var primaryKeyHasAccountID bool
	rows, err := db.Query(`pragma table_info(usage_monitoring_account_daily_rollups_v1)`)
	if err != nil {
		t.Fatalf("inspect current account rollup primary key: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan current account rollup schema: %v", err)
		}
		if name == "auth_account_id_snapshot" && primaryKey > 0 {
			primaryKeyHasAccountID = true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("inspect current account rollup schema: %v", err)
	}
	if !primaryKeyHasAccountID {
		t.Fatal("current account rollup primary key does not include auth_account_id_snapshot")
	}
	var afterProject, accountID sql.NullString
	if err := db.QueryRow(`select auth_project_id_snapshot, auth_account_id_snapshot
		from usage_events where event_hash = 'legacy-codex-marker'`).Scan(&afterProject, &accountID); err != nil {
		t.Fatalf("read immutable event after migration: %v", err)
	}
	if !afterProject.Valid || afterProject.String != beforeProject {
		t.Fatalf("immutable project snapshot changed from %q to %#v", beforeProject, afterProject)
	}
	if accountID.Valid {
		t.Fatalf("historical marker was backfilled into account snapshot: %#v", accountID)
	}
	var accountSnapshotNotNull int
	if err := db.QueryRow(`select "notnull" from pragma_table_info('usage_events')
		where name = 'auth_account_id_snapshot'`).Scan(&accountSnapshotNotNull); err != nil {
		t.Fatalf("inspect usage_events account snapshot nullability: %v", err)
	}
	if accountSnapshotNotNull != 0 {
		t.Fatalf("usage_events auth_account_id_snapshot notnull = %d, want nullable", accountSnapshotNotNull)
	}
}

// monitoringRollupPrimaryKeyHasColumn reports whether the monitoring rollup
// primary key includes the given column.
func monitoringRollupPrimaryKeyHasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	rows, err := db.Query(`pragma table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("inspect %s primary key: %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan %s schema: %v", table, err)
		}
		if name == column && primaryKey > 0 {
			return true
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("inspect %s schema: %v", table, err)
	}
	return false
}

// TestMonitoringAccountIdentityUpgradeAvoidsGenericLegacyCollision verifies the
// auth_account_id_snapshot PK upgrade parks into the identity-specific legacy
// table even when the generic *_legacy_recovery table already exists from a
// prior derived recovery. Without the identity-specific name, parkDerivedTable
// would hard-error with "legacy derived table ... already exists" and Manager
// Server could not start.
func TestMonitoringAccountIdentityUpgradeAvoidsGenericLegacyCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitoring-account-identity-collision.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`insert into usage_events (event_hash, timestamp_ms, timestamp, model, created_at_ms)
		values ('collision-event', 1, '1', 'gpt-test', 1)`,
		`drop table usage_monitoring_account_daily_rollups_v1`,
		`create table usage_monitoring_account_daily_rollups_v1 (
			structure_revision text not null,
			bucket_ms integer not null,
			account_snapshot text not null,
			auth_label_snapshot text not null,
			provider text not null,
			auth_provider_snapshot text not null,
			auth_index text not null,
			source text not null,
			source_hash text not null,
			auth_file_snapshot text not null,
			api_key_hash text not null,
			executor_type text not null,
			model text not null,
			billing_model text not null,
			pricing_model text not null,
			service_tier text not null,
			context_threshold_tokens integer not null,
			failed integer not null,
			calls integer not null default 0,
			input_tokens integer not null default 0,
			output_tokens integer not null default 0,
			reasoning_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_read_tokens integer not null default 0,
			cache_creation_tokens integer not null default 0,
			total_tokens integer not null default 0,
			zero_token_calls integer not null default 0,
			latency_sum_ms integer not null default 0,
			latency_samples integer not null default 0,
			last_seen_ms integer not null,
			updated_at_ms integer not null,
			primary key (
				structure_revision, bucket_ms, account_snapshot, auth_label_snapshot,
				provider, auth_provider_snapshot, auth_index, source, source_hash,
				auth_file_snapshot, api_key_hash, executor_type, model, billing_model,
				pricing_model, service_tier, context_threshold_tokens, failed
			)
		)`,
		`insert into usage_monitoring_account_daily_rollups_v1 (
			structure_revision, bucket_ms, account_snapshot, auth_label_snapshot,
			provider, auth_provider_snapshot, auth_index, source, source_hash,
			auth_file_snapshot, api_key_hash, executor_type, model, billing_model,
			pricing_model, service_tier, context_threshold_tokens, failed, calls,
			last_seen_ms, updated_at_ms
		) values ('legacy', 0, 'same@example.com', 'Same', 'codex', 'codex',
			'auth-1', 'auth.json', 'source-hash', 'auth.json', '', '', 'gpt-test',
			'gpt-test', 'gpt-test', '', -1, 0, 1, 1000, 1000)`,
		// Simulate a prior derived recovery that left the generic legacy table
		// behind; the background cleaner has not drained it yet.
		`create table usage_monitoring_account_daily_rollups_v1_legacy_recovery (
			id integer primary key, marker text not null
		)`,
		`insert into usage_monitoring_account_daily_rollups_v1_legacy_recovery (id, marker) values (1, 'pre-recovery')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("setup account identity collision fixture: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("upgrade account identity sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	assertTableCount(t, db, "usage_events", 1)
	// Generic legacy recovery data must be untouched.
	assertTableCount(t, db, usageMonitoringAccountLegacy, 1)
	// The identity upgrade parked into the identity-specific legacy table.
	assertTableCount(t, db, usageMonitoringAccountIdentityLegacy, 1)
	// Current table rebuilt with auth_account_id_snapshot in the primary key.
	assertTableCount(t, db, usageMonitoringAccountDailyTable, 0)
	if !monitoringRollupPrimaryKeyHasColumn(t, db, usageMonitoringAccountDailyTable, "auth_account_id_snapshot") {
		t.Fatal("account daily rollup PK missing auth_account_id_snapshot after upgrade")
	}
	// Immutable usage_events unchanged.
	var eventCount int
	if err := db.QueryRow(`select count(*) from usage_events`).Scan(&eventCount); err != nil {
		t.Fatalf("read usage_events count: %v", err)
	}
	if eventCount != 1 {
		t.Fatalf("usage_events count = %d, want 1", eventCount)
	}

	// Re-open must succeed without a second collision.
	if err := db.Close(); err != nil {
		t.Fatalf("close upgraded sqlite: %v", err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen upgraded sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertTableCount(t, db, usageMonitoringAccountLegacy, 1)
	assertTableCount(t, db, usageMonitoringAccountIdentityLegacy, 1)
	if !monitoringRollupPrimaryKeyHasColumn(t, db, usageMonitoringAccountDailyTable, "auth_account_id_snapshot") {
		t.Fatal("account daily rollup PK missing auth_account_id_snapshot after reopen")
	}
}

// TestMonitoringAPIKeyIdentityUpgradeAvoidsGenericLegacyCollision is the API-key
// counterpart of the account collision test.
func TestMonitoringAPIKeyIdentityUpgradeAvoidsGenericLegacyCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitoring-apikey-identity-collision.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`insert into usage_events (event_hash, timestamp_ms, timestamp, model, created_at_ms)
		values ('apikey-collision-event', 1, '1', 'gpt-test', 1)`,
		`drop table usage_monitoring_api_key_daily_rollups_v1`,
		`create table usage_monitoring_api_key_daily_rollups_v1 (
			structure_revision text not null,
			bucket_ms integer not null,
			api_key_hash text not null,
			account_snapshot text not null,
			auth_label_snapshot text not null,
			provider text not null,
			auth_provider_snapshot text not null,
			auth_index text not null,
			source text not null,
			source_hash text not null,
			auth_file_snapshot text not null,
			executor_type text not null,
			model text not null,
			billing_model text not null,
			pricing_model text not null,
			service_tier text not null,
			context_threshold_tokens integer not null,
			failed integer not null,
			calls integer not null default 0,
			input_tokens integer not null default 0,
			output_tokens integer not null default 0,
			reasoning_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_read_tokens integer not null default 0,
			cache_creation_tokens integer not null default 0,
			total_tokens integer not null default 0,
			zero_token_calls integer not null default 0,
			latency_sum_ms integer not null default 0,
			latency_samples integer not null default 0,
			last_seen_ms integer not null,
			updated_at_ms integer not null,
			primary key (
				structure_revision, bucket_ms, api_key_hash, account_snapshot,
				auth_label_snapshot, provider, auth_provider_snapshot, auth_index,
				source, source_hash, auth_file_snapshot, executor_type, model,
				billing_model, pricing_model, service_tier, context_threshold_tokens, failed
			)
		)`,
		`insert into usage_monitoring_api_key_daily_rollups_v1 (
			structure_revision, bucket_ms, api_key_hash, account_snapshot,
			auth_label_snapshot, provider, auth_provider_snapshot, auth_index,
			source, source_hash, auth_file_snapshot, executor_type, model,
			billing_model, pricing_model, service_tier, context_threshold_tokens,
			failed, calls, last_seen_ms, updated_at_ms
		) values ('legacy', 0, 'key-hash', 'same@example.com', 'Same', 'codex',
			'codex', 'auth-1', 'auth.json', 'source-hash', 'auth.json', '',
			'gpt-test', 'gpt-test', 'gpt-test', '', -1, 0, 1, 1000, 1000)`,
		`create table usage_monitoring_api_key_daily_rollups_v1_legacy_recovery (
			id integer primary key, marker text not null
		)`,
		`insert into usage_monitoring_api_key_daily_rollups_v1_legacy_recovery (id, marker) values (1, 'pre-recovery')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("setup api-key identity collision fixture: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("upgrade api-key identity sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, usageMonitoringAPIKeyLegacy, 1)
	assertTableCount(t, db, usageMonitoringAPIKeyIdentityLegacy, 1)
	assertTableCount(t, db, usageMonitoringAPIKeyDailyTable, 0)
	if !monitoringRollupPrimaryKeyHasColumn(t, db, usageMonitoringAPIKeyDailyTable, "auth_account_id_snapshot") {
		t.Fatal("api-key daily rollup PK missing auth_account_id_snapshot after upgrade")
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close upgraded sqlite: %v", err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen upgraded sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertTableCount(t, db, usageMonitoringAPIKeyLegacy, 1)
	assertTableCount(t, db, usageMonitoringAPIKeyIdentityLegacy, 1)
	if !monitoringRollupPrimaryKeyHasColumn(t, db, usageMonitoringAPIKeyDailyTable, "auth_account_id_snapshot") {
		t.Fatal("api-key daily rollup PK missing auth_account_id_snapshot after reopen")
	}
}

func TestUsageMonitoringModelFormatUpgradeRebuildsDerivedDataOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-monitoring-model-format.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`insert into usage_events (event_hash, timestamp_ms, timestamp, model, created_at_ms)
		values ('preserved-monitoring-event', 1, '1', 'deepseek-v4-flash(max)', 1)`,
		`insert into usage_monitoring_account_daily_rollups_v1 (
			structure_revision, bucket_ms, account_snapshot, auth_label_snapshot,
			provider, auth_provider_snapshot, auth_index, source, source_hash,
			auth_file_snapshot, api_key_hash, executor_type, model, billing_model,
			pricing_model, service_tier, context_threshold_tokens, failed,
			last_seen_ms, updated_at_ms
		) values ('legacy', 0, '', '', '', '', '', '', '', '', '', '',
			'deepseek-v4-flash(max)', 'deepseek-v4-flash(max)',
			'deepseek-v4-flash(max)', '', -1, 0, 1, 1)`,
		`insert into usage_monitoring_api_key_daily_rollups_v1 (
			structure_revision, bucket_ms, api_key_hash, account_snapshot,
			auth_label_snapshot, provider, auth_provider_snapshot, auth_index,
			source, source_hash, auth_file_snapshot, executor_type, model,
			billing_model, pricing_model, service_tier, context_threshold_tokens,
			failed, last_seen_ms, updated_at_ms
		) values ('legacy', 0, '', '', '', '', '', '', '', '', '', '',
			'deepseek-v4-flash(max)', 'deepseek-v4-flash(max)',
			'deepseek-v4-flash(max)', '', -1, 0, 1, 1)`,
		`insert into usage_monitoring_selector_daily_rollups_v1 (
			bucket_ms, model, api_key_hash, provider, auth_file_snapshot,
			account_snapshot, auth_label_snapshot, auth_index, source,
			source_hash, updated_at_ms
		) values (0, 'deepseek-v4-flash(max)', '', '', '', '', '', '', '', '', 1)`,
		`insert into usage_monitoring_event_projection_v1 (
			event_id, timestamp_ms, search_text, account_key, provider,
			executor_type, model, analytics_model, resolved_model, auth_index,
			source, source_hash, api_key_hash, account_snapshot,
			auth_label_snapshot, auth_file_snapshot, auth_provider_snapshot,
			auth_project_id_snapshot, reasoning_effort, service_tier, failed,
			latency_ms, input_tokens, output_tokens, reasoning_tokens,
			cached_tokens, cache_tokens, cache_read_tokens,
			cache_creation_tokens, normalized_total_input_tokens, total_tokens,
			header_quota_plan_type, header_error_kind, header_error_code,
			header_trace_id, updated_at_ms
		) values (1, 1, 'legacy', '', '', '', 'deepseek-v4-flash(max)',
			'deepseek-v4-flash(max)', '', '', '', '', '', '', '', '', '', '',
			'', '', 0, null, 0, 0, 0, 0, 0, 0, 0, 0, 0, '', '', '', '', 1)`,
		`update usage_monitoring_rollup_state set
			structure_revision = 'legacy', status = 'ready',
			backfill_last_event_id = 1, coverage_event_id = 1,
			target_event_id = 1, processed_events = 1, updated_at_ms = 1`,
		`update usage_monitoring_search_index_state set ready = 1, updated_at_ms = 1 where id = 1`,
		`delete from settings where key = 'usage_monitoring_model_format_version'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("setup legacy monitoring model format: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("upgrade sqlite: %v", err)
	}
	assertTableCount(t, db, "usage_events", 1)
	for activeTable, legacyTable := range map[string]string{
		usageMonitoringAccountDailyTable:  usageMonitoringAccountLegacy,
		usageMonitoringAPIKeyDailyTable:   usageMonitoringAPIKeyLegacy,
		usageMonitoringSelectorDailyTable: usageMonitoringSelectorLegacy,
	} {
		assertTableCount(t, db, activeTable, 0)
		assertTableCount(t, db, legacyTable, 1)
	}
	_, projectionLegacy, status := latestMonitoringCleanupJob(t, db)
	if status != "online_cleanup" || projectionLegacy == "" {
		t.Fatalf("monitoring cleanup job = projection:%q status:%q", projectionLegacy, status)
	}
	assertTableCount(t, db, usageprojection.EventTable, 0)
	assertTableCount(t, db, projectionLegacy, 1)
	var version string
	if err := db.QueryRow(`select value from settings where key = ?`, usageMonitoringModelFormatVersionKey).Scan(&version); err != nil {
		t.Fatalf("read monitoring model format version: %v", err)
	}
	if version != usageidentity.ModelFormatVersion {
		t.Fatalf("monitoring model format version = %q, want %q", version, usageidentity.ModelFormatVersion)
	}
	rows, err := db.Query(`select rollup_name, structure_revision, status,
		coverage_event_id, target_event_id, processed_events
		from usage_monitoring_rollup_state order by rollup_name`)
	if err != nil {
		t.Fatalf("read rebuilt monitoring states: %v", err)
	}
	for rows.Next() {
		var name, revision, status string
		var coverage, target, processed int64
		if err := rows.Scan(&name, &revision, &status, &coverage, &target, &processed); err != nil {
			_ = rows.Close()
			t.Fatalf("scan rebuilt monitoring state: %v", err)
		}
		wantRevision := usageidentity.ModelFormatVersion
		if name == usageMonitoringStatsRollupName {
			wantRevision = ""
		} else if name == usageMonitoringProjectionRollupName {
			wantRevision = usageidentity.MonitoringProjectionStructureRevision()
		}
		if revision != wantRevision || status != "pending" || coverage != 0 || target != 1 || processed != 0 {
			_ = rows.Close()
			t.Fatalf("rebuilt monitoring state %s = revision:%q status:%q coverage:%d target:%d processed:%d", name, revision, status, coverage, target, processed)
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close rebuilt monitoring states: %v", err)
	}
	var searchReady int
	if err := db.QueryRow(`select ready from usage_monitoring_search_index_state where id = 1`).Scan(&searchReady); err != nil {
		t.Fatalf("read rebuilt search state: %v", err)
	}
	if searchReady != 0 {
		t.Fatalf("rebuilding search ready = %d, want 0 before background catch-up", searchReady)
	}
	if _, err := db.Exec(`insert into usage_monitoring_selector_daily_rollups_v1 (
		model_format_revision, bucket_ms, model, api_key_hash, provider, auth_file_snapshot,
		account_snapshot, auth_label_snapshot, auth_index, source,
		source_hash, updated_at_ms
	) values ('1', 0, 'rebuilt', '', '', '', '', '', '', '', '', 2)`); err != nil {
		_ = db.Close()
		t.Fatalf("insert rebuilt selector: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close upgraded sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen upgraded sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, usageMonitoringSelectorDailyTable, 1)
	assertTableCount(t, db, usageMonitoringSelectorLegacy, 1)
	var currentSelectorRows int
	if err := db.QueryRow(`select count(*) from usage_monitoring_selector_daily_rollups_v1 where model_format_revision = ?`, usageidentity.ModelFormatVersion).Scan(&currentSelectorRows); err != nil {
		t.Fatalf("count current selector rows: %v", err)
	}
	if currentSelectorRows != 1 {
		t.Fatalf("current selector rows = %d, want 1", currentSelectorRows)
	}
}

func TestUsageMonitoringModelFormatUpgradeParksDerivedRowsWithoutDeletingThem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-monitoring-model-format-retry.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`insert into usage_events (event_hash, timestamp_ms, timestamp, model, created_at_ms)
		values ('preserved-retry-event', 1, '1', 'deepseek-v4-flash(max)', 1)`,
		`insert into usage_monitoring_selector_daily_rollups_v1 (
			bucket_ms, model, api_key_hash, provider, auth_file_snapshot,
			account_snapshot, auth_label_snapshot, auth_index, source,
			source_hash, updated_at_ms
		) values (0, 'legacy', '', '', '', '', '', '', '', '', 1)`,
		`delete from settings where key = 'usage_monitoring_model_format_version'`,
		`create trigger reject_monitoring_model_selector_delete
		before delete on usage_monitoring_selector_daily_rollups_v1
		begin select raise(abort, 'blocked'); end`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("setup monitoring model retry fixture: %v", err)
		}
	}

	if err := ensureUsageMonitoringProjectionIdentity(db); err != nil {
		t.Fatalf("monitoring model migration: %v", err)
	}
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, usageMonitoringSelectorDailyTable, 0)
	assertTableCount(t, db, usageMonitoringSelectorLegacy, 1)
	var settingCount int
	if err := db.QueryRow(`select count(*) from settings where key = ?`, usageMonitoringModelFormatVersionKey).Scan(&settingCount); err != nil {
		t.Fatalf("read rolled-back monitoring model setting: %v", err)
	}
	if settingCount != 1 {
		t.Fatalf("monitoring model setting count = %d, want 1", settingCount)
	}

	if _, err := db.Exec(`drop trigger reject_monitoring_model_selector_delete`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	if err := ensureUsageMonitoringProjectionIdentity(db); err != nil {
		t.Fatalf("repeat monitoring model migration: %v", err)
	}
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, usageMonitoringSelectorDailyTable, 0)
	assertTableCount(t, db, usageMonitoringSelectorLegacy, 1)
	var version string
	if err := db.QueryRow(`select value from settings where key = ?`, usageMonitoringModelFormatVersionKey).Scan(&version); err != nil {
		t.Fatalf("read retried monitoring model version: %v", err)
	}
	if version != usageidentity.ModelFormatVersion {
		t.Fatalf("retried monitoring model version = %q", version)
	}
}

func TestUsageMonitoringModelFormatUpgradeIsBoundedAt150KRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-monitoring-model-format-150k.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, triggerName := range []string{
		"usage_monitoring_event_search_v1_insert",
		"usage_monitoring_event_search_v1_update",
		"usage_monitoring_event_search_v1_delete",
	} {
		if _, err := db.Exec(`drop trigger if exists ` + triggerName); err != nil {
			t.Fatalf("drop search trigger %s: %v", triggerName, err)
		}
	}
	if _, err := db.Exec(`with recursive ids(id) as (
		select 1 union all select id + 1 from ids where id < 150000
	) insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, requested_model, created_at_ms
	) select 'event-' || id, id, cast(id as text), 'model(max)', 'model(max)', id from ids`); err != nil {
		t.Fatalf("seed 150k usage events: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin 150k projection seed: %v", err)
	}
	if err := usageprojection.UpsertEventRange(context.Background(), tx, 0, 150000, 1); err != nil {
		_ = tx.Rollback()
		t.Fatalf("seed 150k projection rows: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit 150k projection seed: %v", err)
	}
	for _, statement := range []string{
		`delete from settings where key = 'usage_monitoring_model_format_version'`,
		`create trigger reject_projection_delete before delete on usage_monitoring_event_projection_v1
		begin select raise(abort, 'startup migration must not delete projection rows'); end`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("prepare bounded migration fixture: %v", err)
		}
	}

	started := time.Now()
	if err := ensureUsageMonitoringProjectionIdentity(db); err != nil {
		t.Fatalf("run bounded monitoring migration: %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("150k startup migration took %s, want bounded metadata-only work", elapsed)
	}
	assertTableCount(t, db, "usage_events", 150000)
	assertTableCount(t, db, usageprojection.EventTable, 0)
	_, projectionLegacy, status := latestMonitoringCleanupJob(t, db)
	if status != "online_cleanup" || projectionLegacy == "" {
		t.Fatalf("150k monitoring cleanup job = projection:%q status:%q", projectionLegacy, status)
	}
	assertTableCount(t, db, projectionLegacy, 150000)
	var coverage, target int64
	if err := db.QueryRow(`select coverage_event_id, target_event_id from usage_monitoring_rollup_state where rollup_name = ?`, usageMonitoringProjectionRollupName).Scan(&coverage, &target); err != nil {
		t.Fatalf("read scheduled projection rebuild: %v", err)
	}
	if coverage != 0 || target != 150000 {
		t.Fatalf("scheduled projection rebuild = coverage:%d target:%d", coverage, target)
	}
}

func latestMonitoringCleanupJob(t testing.TB, db *sql.DB) (string, string, string) {
	t.Helper()
	var ftsTable, projectionTable, status string
	if err := db.QueryRow(`select fts_table, coalesce(projection_table, ''), status
		from usage_derived_cleanup_jobs where kind = 'monitoring_fts'
		order by generation desc limit 1`).Scan(&ftsTable, &projectionTable, &status); err != nil {
		t.Fatalf("read latest monitoring cleanup job: %v", err)
	}
	return ftsTable, projectionTable, status
}

func TestUsageMonitoringModelFormatUpgradeRecoversMissingSearchIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-monitoring-model-format-missing-search.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`insert into usage_events (event_hash, timestamp_ms, timestamp, model, created_at_ms)
		values ('missing-search-event', 1, '1', 'deepseek-v4-flash(max)', 1)`,
		`insert into usage_monitoring_event_projection_v1 (
			event_id, timestamp_ms, search_text, account_key, provider,
			executor_type, model, analytics_model, resolved_model, auth_index,
			source, source_hash, api_key_hash, account_snapshot,
			auth_label_snapshot, auth_file_snapshot, auth_provider_snapshot,
			auth_project_id_snapshot, reasoning_effort, service_tier, failed,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens,
			cache_tokens, cache_read_tokens, cache_creation_tokens,
			normalized_total_input_tokens, total_tokens, header_quota_plan_type,
			header_error_kind, header_error_code, header_trace_id, updated_at_ms
		) values (
			1, 1, 'legacy-search', '', '', '', 'deepseek-v4-flash(max)',
			'deepseek-v4-flash(max)', '', '', '', '', '', '', '', '', '', '',
			'', '', 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, '', '', '', '', 1
		)`,
		`delete from settings where key = 'usage_monitoring_model_format_version'`,
		`drop table usage_monitoring_event_search_v1`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("setup missing search fixture: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close damaged sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("recover missing search index: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, usageprojection.EventTable, 0)
	assertTableCount(t, db, usageMonitoringProjectionLegacy, 1)
	var searchIndexCount int
	if err := db.QueryRow(`select count(*) from sqlite_master
		where type = 'table' and name = 'usage_monitoring_event_search_v1'`).Scan(&searchIndexCount); err != nil {
		t.Fatalf("read recovered search index: %v", err)
	}
	if searchIndexCount != 1 {
		t.Fatalf("recovered search index count = %d, want 1", searchIndexCount)
	}
}

func TestUsageMonitoringSearchIndexCreationDefersBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-monitoring-search-retry.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `insert into usage_events (
		request_id, event_hash, timestamp_ms, timestamp, model, created_at_ms
	) values ('searchretrymarker', 'search-retry-event', 1, '1', 'gpt-test', 1)`); err != nil {
		t.Fatalf("insert search retry event: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin search retry projection: %v", err)
	}
	if err := usageprojection.UpsertEventRange(ctx, tx, 0, 1, 1); err != nil {
		_ = tx.Rollback()
		t.Fatalf("seed search retry projection: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit search retry projection: %v", err)
	}
	for _, statement := range []string{
		`drop table usage_monitoring_event_search_v1`,
		`create trigger reject_usage_monitoring_search_ready
		before update of ready on usage_monitoring_search_index_state
		when new.ready = 1
		begin select raise(abort, 'blocked'); end`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare search retry fixture: %v", err)
		}
	}

	if err := ensureUsageMonitoringSearchIndex(db); err != nil {
		t.Fatalf("create deferred search index: %v", err)
	}
	var searchIndexCount int
	if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master
		where type = 'table' and name = 'usage_monitoring_event_search_v1'`).Scan(&searchIndexCount); err != nil {
		t.Fatalf("inspect rolled-back search index: %v", err)
	}
	if searchIndexCount != 1 {
		t.Fatalf("deferred search index count = %d, want 1", searchIndexCount)
	}
	var ready int
	if err := db.QueryRowContext(ctx, `select ready from usage_monitoring_search_index_state where id = 1`).Scan(&ready); err != nil {
		t.Fatalf("read rolled-back search state: %v", err)
	}
	if ready != 0 {
		t.Fatalf("deferred search ready = %d, want 0", ready)
	}

	if _, err := db.ExecContext(ctx, `drop trigger reject_usage_monitoring_search_ready`); err != nil {
		t.Fatalf("drop search retry failure trigger: %v", err)
	}
	var matched int
	if err := db.QueryRowContext(ctx, `select count(*) from usage_monitoring_event_search_v1
		where search_text like '%searchretrymarker%'`).Scan(&matched); err != nil {
		t.Fatalf("query rebuilt search index: %v", err)
	}
	if matched != 0 {
		t.Fatalf("search matches before background catch-up = %d, want 0", matched)
	}
}

func TestUsageMonitoringModelFormatUpgradeRejectsUnknownVersionWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-monitoring-model-format-future.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`insert into usage_events (event_hash, timestamp_ms, timestamp, model, created_at_ms)
		values ('preserved-future-event', 1, '1', 'deepseek-v4-flash(max)', 1)`,
		`insert into usage_monitoring_selector_daily_rollups_v1 (
			bucket_ms, model, api_key_hash, provider, auth_file_snapshot,
			account_snapshot, auth_label_snapshot, auth_index, source,
			source_hash, updated_at_ms
		) values (0, 'legacy', '', '', '', '', '', '', '', '', 1)`,
		`update settings set value = 'future' where key = 'usage_monitoring_model_format_version'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("setup future monitoring model fixture: %v", err)
		}
	}

	if err := ensureUsageMonitoringProjectionIdentity(db); err == nil {
		t.Fatal("monitoring model migration error = nil, want unsupported version failure")
	}
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, "usage_monitoring_selector_daily_rollups_v1", 1)
	var version string
	if err := db.QueryRow(`select value from settings where key = ?`, usageMonitoringModelFormatVersionKey).Scan(&version); err != nil {
		t.Fatalf("read preserved monitoring model version: %v", err)
	}
	if version != "future" {
		t.Fatalf("monitoring model version after rejection = %q, want future", version)
	}
}

func TestMigrateRejectsUnknownMonitoringModelFormatBeforeOtherSchemaChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-monitoring-model-format-preflight.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`drop index if exists idx_usage_events_latest_request_auth_file`,
		`drop table usage_derived_cleanup_cursors`,
		`drop table usage_derived_cleanup_jobs`,
		`update settings set value = 'future' where key = 'usage_monitoring_model_format_version'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("setup future monitoring preflight fixture: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close future monitoring preflight fixture: %v", err)
	}

	if _, err := Open(path); err == nil {
		t.Fatal("reopen future monitoring model format error = nil, want unsupported version failure")
	}
	db, err = sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatalf("open rejected monitoring preflight sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var indexCount int
	if err := db.QueryRow(`select count(*) from sqlite_master
		where type = 'index' and name = 'idx_usage_events_latest_request_auth_file'`).Scan(&indexCount); err != nil {
		t.Fatalf("inspect unrelated index after monitoring preflight rejection: %v", err)
	}
	if indexCount != 0 {
		t.Fatalf("unrelated index count after monitoring preflight rejection = %d, want 0", indexCount)
	}
	var cleanupTableCount int
	if err := db.QueryRow(`select count(*) from sqlite_master
		where type = 'table' and name in ('usage_derived_cleanup_jobs', 'usage_derived_cleanup_cursors')`).Scan(&cleanupTableCount); err != nil {
		t.Fatalf("inspect cleanup schema after monitoring preflight rejection: %v", err)
	}
	if cleanupTableCount != 0 {
		t.Fatalf("cleanup table count after monitoring preflight rejection = %d, want 0", cleanupTableCount)
	}
}

func TestMigrateRejectsUnknownAccountHistoryFormatBeforeOtherSchemaChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "account-history-format-preflight.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`drop index if exists idx_usage_events_latest_request_auth_file`,
		`update settings set value = 'future' where key = 'usage_account_history_identity_format_version'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("setup future account history preflight fixture: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close future account history preflight fixture: %v", err)
	}

	if _, err := Open(path); err == nil {
		t.Fatal("reopen future account history format error = nil, want unsupported version failure")
	}
	db, err = sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatalf("open rejected account history preflight sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var indexCount int
	if err := db.QueryRow(`select count(*) from sqlite_master
		where type = 'index' and name = 'idx_usage_events_latest_request_auth_file'`).Scan(&indexCount); err != nil {
		t.Fatalf("inspect unrelated index after account history preflight rejection: %v", err)
	}
	if indexCount != 0 {
		t.Fatalf("unrelated index count after account history preflight rejection = %d, want 0", indexCount)
	}
}

func TestMigrateRejectsUnknownDashboardFormatBeforeOtherSchemaChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard-format-preflight.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`drop index if exists idx_usage_events_latest_request_auth_file`,
		`update settings set value = 'future' where key = 'usage_dashboard_hourly_format_version'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("setup future dashboard preflight fixture: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close future dashboard preflight fixture: %v", err)
	}

	if _, err := Open(path); err == nil {
		t.Fatal("reopen future dashboard format error = nil, want unsupported version failure")
	}
	db, err = sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatalf("open rejected dashboard preflight sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var indexCount int
	if err := db.QueryRow(`select count(*) from sqlite_master
		where type = 'index' and name = 'idx_usage_events_latest_request_auth_file'`).Scan(&indexCount); err != nil {
		t.Fatalf("inspect unrelated index after dashboard preflight rejection: %v", err)
	}
	if indexCount != 0 {
		t.Fatalf("unrelated index count after dashboard preflight rejection = %d, want 0", indexCount)
	}
}

func TestMigrateDefersLegacyQuotaSnapshotLifecycleBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-account-quota-snapshots.sqlite")
	db, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatalf("open legacy quota snapshot sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`create table account_quota_snapshots (
		id integer primary key autoincrement,
		account_key text not null,
		provider text not null,
		provider_window_id text not null,
		window_kind text not null,
		window_mode text not null,
		model_scope_kind text not null,
		model_scope_key text,
		model_ids_json text,
		source text not null,
		source_observation_id text,
		observed_at_ms integer not null,
		boundary_accuracy text not null,
		cycle_start_ms integer,
		cycle_end_ms integer,
		duration_seconds integer,
		used_percent real,
		remaining_percent real,
		used_value real,
		limit_value real,
		quota_unit text,
		reset_credits_available integer,
		reset_credits_json text,
		plan_type text,
		created_at_ms integer not null
	)`); err != nil {
		t.Fatalf("create legacy quota snapshot table: %v", err)
	}
	if _, err := db.Exec(`insert into account_quota_snapshots (
		account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, source, source_observation_id, observed_at_ms,
		boundary_accuracy, cycle_start_ms, cycle_end_ms, duration_seconds,
		used_percent, remaining_percent, plan_type, created_at_ms
	) values (
		'account-1', 'codex', 'weekly', 'weekly', 'fixed',
		'all', 'inspection', 'legacy-inspection', 2000,
		'exact', 1000, 605801000, 604800, 25, 75, 'plus', 2000
	)`); err != nil {
		t.Fatalf("insert legacy quota snapshot: %v", err)
	}
	if _, err := db.Exec(`insert into account_quota_snapshots (
		account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, model_scope_key, model_ids_json, source,
		source_observation_id, observed_at_ms, boundary_accuracy,
		duration_seconds, used_value, limit_value, quota_unit, created_at_ms
	) values (
		'account-xai', 'xai', 'included-free-rolling-24h', 'rolling_24h', 'rolling',
		'models', ' Grok-4.5-Build-Free ',
		'[" GROK-4.5-BUILD-FREE ","","grok-4.5-build-free"]', 'response_body',
		'legacy-xai-body', 2000, 'estimated', 86400, 100, 1000, 'tokens', 2000
	)`); err != nil {
		t.Fatalf("insert legacy xai quota snapshot: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate legacy quota snapshots: %v", err)
	}
	var pendingBeforeWorker int
	if err := db.QueryRow(`select count(*) from account_quota_snapshots where observation_id is null`).Scan(&pendingBeforeWorker); err != nil {
		t.Fatalf("count deferred legacy quota snapshots: %v", err)
	}
	if pendingBeforeWorker != 2 {
		t.Fatalf("deferred legacy quota snapshots = %d, want 2", pendingBeforeWorker)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("repeat legacy quota snapshot migration: %v", err)
	}
	for {
		result, err := quotasnapshotrepo.BackfillLegacySnapshotsBatch(context.Background(), db, 1000)
		if err != nil {
			t.Fatalf("run deferred legacy quota snapshot batch: %v", err)
		}
		if result.Completed {
			break
		}
	}

	var observationID, logicalWindowID, activationID, cycleID sql.NullInt64
	var scopeFingerprint string
	if err := db.QueryRow(`select observation_id, logical_window_id, activation_id, cycle_id,
		scope_fingerprint from account_quota_snapshots where id = 1`).Scan(
		&observationID,
		&logicalWindowID,
		&activationID,
		&cycleID,
		&scopeFingerprint,
	); err != nil {
		t.Fatalf("read migrated quota snapshot lifecycle: %v", err)
	}
	if !observationID.Valid || !logicalWindowID.Valid || !activationID.Valid || !cycleID.Valid || scopeFingerprint == "" {
		t.Fatalf(
			"migrated quota snapshot lifecycle = observation:%#v window:%#v activation:%#v cycle:%#v scope:%q",
			observationID,
			logicalWindowID,
			activationID,
			cycleID,
			scopeFingerprint,
		)
	}
	for table, count := range map[string]int{
		"account_quota_observations":       2,
		"account_quota_windows":            2,
		"account_quota_window_activations": 2,
		"account_quota_cycles":             1,
	} {
		assertTableCount(t, db, table, count)
	}
	repository := quotasnapshotrepo.New(db)

	var legacyXAIWindowID int64
	var legacyXAIInventoryScope string
	if err := db.QueryRow(`select s.logical_window_id, w.inventory_scope_key
		from account_quota_snapshots s
		join account_quota_windows w on w.id = s.logical_window_id
		where s.account_key = 'account-xai'`).Scan(
		&legacyXAIWindowID,
		&legacyXAIInventoryScope,
	); err != nil {
		t.Fatalf("read migrated xai quota lifecycle: %v", err)
	}
	if legacyXAIInventoryScope != "xai:included-free" {
		t.Fatalf("legacy xai inventory scope = %q, want xai:included-free", legacyXAIInventoryScope)
	}

	modelIDs := []string{"grok-4.5-build-free"}
	duration := int64(86_400)
	usedValue := 200.0
	limitValue := 1_000.0
	liveXAI := model.AccountQuotaSnapshot{
		AccountKey:          "account-xai",
		Provider:            "xai",
		ProviderWindowID:    "included-free-rolling-24h",
		WindowKind:          "rolling_24h",
		WindowMode:          "rolling",
		ModelScopeKind:      "models",
		ModelScopeKey:       "grok-4.5-build-free",
		ModelIDsJSON:        `["grok-4.5-build-free"]`,
		ScopeFingerprint:    quotasnapshotrepo.ScopeFingerprint("models", "grok-4.5-build-free", modelIDs),
		ContentHash:         "live-xai-content",
		InventoryScopeKey:   "xai:included-free",
		Source:              "response_body",
		SourceObservationID: "live-xai-body",
		ObservedAtMS:        3000,
		BoundaryAccuracy:    "estimated",
		DurationSeconds:     &duration,
		UsedValue:           &usedValue,
		LimitValue:          &limitValue,
		QuotaUnit:           "tokens",
		CreatedAtMS:         3000,
	}
	if err := repository.InsertObservationWrites(context.Background(), []model.AccountQuotaObservationWrite{{
		Observation: model.AccountQuotaObservation{
			ObservationHash:     "live-xai-observation",
			AccountKey:          "account-xai",
			Provider:            "xai",
			Source:              "response_body",
			SourceObservationID: "live-xai-body",
			InventoryScopeKey:   "xai:included-free",
			InventoryMode:       "partial",
			ObservedAtMS:        3000,
			WindowCount:         1,
			CreatedAtMS:         3000,
		},
		Snapshots: []model.AccountQuotaSnapshot{liveXAI},
	}}); err != nil {
		t.Fatalf("write live xai evidence after migration: %v", err)
	}
	var liveXAIWindowID int64
	if err := db.QueryRow(`select logical_window_id from account_quota_snapshots
		where source_observation_id = 'live-xai-body'`).Scan(&liveXAIWindowID); err != nil {
		t.Fatalf("read live xai quota snapshot: %v", err)
	}
	if liveXAIWindowID != legacyXAIWindowID {
		t.Fatalf("live xai logical window = %d, want migrated window %d", liveXAIWindowID, legacyXAIWindowID)
	}

	for index, observedAtMS := range []int64{3000, 4000} {
		observation := model.AccountQuotaObservation{
			ObservationHash:   []string{"complete-empty-1", "complete-empty-2"}[index],
			AccountKey:        "account-1",
			Provider:          "codex",
			Source:            "inspection",
			InventoryScopeKey: "codex:rate-limits",
			InventoryMode:     "complete",
			ObservedAtMS:      observedAtMS,
			CreatedAtMS:       observedAtMS,
		}
		if err := repository.InsertObservationWrites(context.Background(), []model.AccountQuotaObservationWrite{{
			Observation: observation,
		}}); err != nil {
			t.Fatalf("write complete empty observation %d: %v", index+1, err)
		}
	}
	states, err := repository.ListWindowStates(context.Background(), "account-1", "codex")
	if err != nil {
		t.Fatalf("list migrated quota lifecycle: %v", err)
	}
	if len(states) != 1 || states[0].Availability != "inactive" || states[0].DeactivatedAtMS == nil ||
		*states[0].DeactivatedAtMS != 3000 {
		t.Fatalf("retired migrated quota lifecycle = %#v", states)
	}
}

func TestMigrateAddsQuotaObservationLifecycleAppliedColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "account-quota-observation-upgrade.sqlite")
	db, err := sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatalf("open legacy quota observation sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`create table account_quota_observations (
		id integer primary key autoincrement,
		observation_hash text not null unique,
		account_key text not null,
		provider text not null,
		source text not null,
		source_observation_id text,
		inventory_scope_key text not null,
		inventory_mode text not null,
		observed_at_ms integer not null,
		window_count integer not null default 0,
		created_at_ms integer not null
	)`); err != nil {
		t.Fatalf("create legacy quota observation table: %v", err)
	}
	if _, err := db.Exec(`insert into account_quota_observations (
		observation_hash, account_key, provider, source, inventory_scope_key,
		inventory_mode, observed_at_ms, window_count, created_at_ms
	) values ('legacy-observation', 'account-1', 'codex', 'inspection',
		'codex:rate-limits', 'complete', 1000, 1, 1000)`); err != nil {
		t.Fatalf("insert legacy quota observation: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("migrate legacy quota observation schema: %v", err)
	}
	if err := Migrate(db); err != nil {
		t.Fatalf("repeat quota observation migration: %v", err)
	}
	var lifecycleApplied int
	if err := db.QueryRow(`select lifecycle_applied from account_quota_observations
		where observation_hash = 'legacy-observation'`).Scan(&lifecycleApplied); err != nil {
		t.Fatalf("read migrated lifecycle marker: %v", err)
	}
	if lifecycleApplied != 1 {
		t.Fatalf("legacy lifecycle_applied = %d, want 1", lifecycleApplied)
	}
}

func TestUsageDataMigrationUpgradeAddsProgressColumnsAndPreservesV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-data-migration-upgrade.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`drop table usage_data_migrations`,
		`create table usage_data_migrations (name text primary key, status text not null, last_event_id integer not null default 0, target_event_id integer not null default 0, processed_rows integer not null default 0, started_at_ms integer, updated_at_ms integer not null default 0, finished_at_ms integer, last_error text)`,
		`insert into usage_data_migrations (name, status) values ('usage_cache_accounting_v1', 'completed')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("setup legacy sqlite: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("upgrade sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	columns := migrationTableColumns(t, db, "usage_data_migrations")
	if !columns["changed_rows"] || !columns["applied_rows"] {
		t.Fatalf("migration columns = %#v, missing progress columns", columns)
	}
	var v1Status, v2Status string
	if err := db.QueryRow(`select status from usage_data_migrations where name = 'usage_cache_accounting_v1'`).Scan(&v1Status); err != nil {
		t.Fatalf("read v1 status: %v", err)
	}
	if err := db.QueryRow(`select status from usage_data_migrations where name = 'usage_cache_accounting_v2'`).Scan(&v2Status); err != nil {
		t.Fatalf("read v2 status: %v", err)
	}
	if v1Status != "completed" || v2Status != "completed" {
		t.Fatalf("migration statuses = v1:%q v2:%q", v1Status, v2Status)
	}
}

func TestCodexInspectionAutoRecoverySchema(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "codex-inspection.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	columns := migrationTableColumns(t, db, "codex_inspection_results")
	if !columns["auto_recover_eligible"] {
		t.Fatalf("codex inspection results columns = %#v, want auto_recover_eligible", columns)
	}
	ownershipColumns := migrationTableColumns(t, db, "codex_inspection_disable_ownership")
	for _, column := range []string{"file_name", "provider", "auth_index", "account_id", "account_snapshot", "disabled_at_ms", "updated_at_ms"} {
		if !ownershipColumns[column] {
			t.Fatalf("ownership columns = %#v, missing %s", ownershipColumns, column)
		}
	}
	ownershipPrimaryKey := migrationTablePrimaryKey(t, db, "codex_inspection_disable_ownership")
	for index, column := range []string{"file_name", "provider", "auth_index", "account_id", "account_snapshot"} {
		if ownershipPrimaryKey[column] != index+1 {
			t.Fatalf("ownership primary key = %#v, want %s at position %d", ownershipPrimaryKey, column, index+1)
		}
	}
	leaseColumns := migrationTableColumns(t, db, "codex_inspection_leases")
	for _, column := range []string{"id", "run_id", "owner_id", "heartbeat_at_ms", "lease_expires_at_ms"} {
		if !leaseColumns[column] {
			t.Fatalf("lease columns = %#v, missing %s", leaseColumns, column)
		}
	}
	accountActionColumns := migrationTableColumns(t, db, "account_action_candidates")
	for _, column := range []string{"reason_code", "auto_disable_eligible", "auto_disabled_at_ms"} {
		if !accountActionColumns[column] {
			t.Fatalf("account action columns = %#v, missing %s", accountActionColumns, column)
		}
	}
	cooldownColumns := migrationTableColumns(t, db, "quota_cooldowns")
	for _, column := range []string{"reason_code", "window_kind", "evidence_json"} {
		if !cooldownColumns[column] {
			t.Fatalf("quota cooldown columns = %#v, missing %s", cooldownColumns, column)
		}
	}
	var identityIndexCount int
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'index'
		and name = 'idx_account_action_candidates_pending_identity_action'`).Scan(&identityIndexCount); err != nil {
		t.Fatalf("inspect deferred account action identity index: %v", err)
	}
	if identityIndexCount != 0 {
		t.Fatalf("account action identity index count before post-listen maintenance = %d, want 0", identityIndexCount)
	}
	for index, account := range []string{"alice@example.com", "bob@example.com"} {
		if _, err := db.Exec(`insert into quota_cooldowns (
			auth_file_name, account_snapshot, provider, recover_at_ms, owner,
			pre_disabled_state, status, disabled_at_ms, created_at_ms, updated_at_ms
		) values (?, ?, 'codex', ?, 'cpamp_usage_429', 0, 'active', ?, ?, ?)`,
			"shared.json",
			account,
			int64(1_000+index),
			int64(900+index),
			int64(800+index),
			int64(800+index),
		); err != nil {
			t.Fatalf("insert shared-file cooldown %q: %v", account, err)
		}
	}
}

func TestMigrateReplacesLegacyQuotaCooldownOwnerIndex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-quota-cooldown-index.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := RunDerivedStartupMaintenance(context.Background(), db); err != nil {
		_ = db.Close()
		t.Fatalf("prepare current indexes: %v", err)
	}
	for _, statement := range []string{
		`drop index idx_quota_cooldowns_active_identity`,
		`create unique index idx_quota_cooldowns_active_owner on quota_cooldowns(auth_file_name, owner) where status = 'active'`,
		`insert into quota_cooldowns (
			auth_file_name, account_snapshot, provider, recover_at_ms, owner,
			pre_disabled_state, status, disabled_at_ms, created_at_ms, updated_at_ms
		) values ('shared.json', 'seed@example.com', 'codex', 900,
			'cpamp_usage_429', 0, 'active', 1, 1, 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare legacy index: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen migrated sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var legacyIndexCount int
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = 'idx_quota_cooldowns_active_owner'`).Scan(&legacyIndexCount); err != nil {
		t.Fatalf("read legacy index state: %v", err)
	}
	if legacyIndexCount != 1 {
		t.Fatalf("legacy quota cooldown index changed before post-listen maintenance")
	}
	if err := RunDerivedStartupMaintenance(context.Background(), db); err != nil {
		t.Fatalf("run bounded quota cooldown index preparation: %v", err)
	}
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = 'idx_quota_cooldowns_active_owner'`).Scan(&legacyIndexCount); err != nil {
		t.Fatalf("read post-listen legacy index state: %v", err)
	}
	if legacyIndexCount != 1 {
		t.Fatalf("legacy quota cooldown index changed on non-empty startup")
	}
	var replacementIndexCount int
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = 'idx_quota_cooldowns_active_identity'`).Scan(&replacementIndexCount); err != nil {
		t.Fatalf("read deferred replacement index state: %v", err)
	}
	if replacementIndexCount != 0 {
		t.Fatalf("replacement quota cooldown index exists before offline cleanup")
	}
	result, err := CleanupDerivedOffline(context.Background(), db)
	if err != nil {
		t.Fatalf("replace legacy quota cooldown index offline: %v", err)
	}
	if result.PreparedIndexes == 0 {
		t.Fatalf("offline cleanup result = %+v, want index changes", result)
	}
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = 'idx_quota_cooldowns_active_owner'`).Scan(&legacyIndexCount); err != nil {
		t.Fatalf("read offline legacy index state: %v", err)
	}
	if legacyIndexCount != 0 {
		t.Fatalf("legacy quota cooldown index still exists after offline cleanup")
	}

	for index, account := range []string{"alice@example.com", "bob@example.com"} {
		if _, err := db.Exec(`insert into quota_cooldowns (
			auth_file_name, account_snapshot, provider, recover_at_ms, owner,
			pre_disabled_state, status, disabled_at_ms, created_at_ms, updated_at_ms
		) values ('shared.json', ?, 'codex', ?, 'cpamp_usage_429', 0, 'active', 1, 1, 1)`,
			account,
			int64(1_000+index),
		); err != nil {
			t.Fatalf("insert migrated shared-file cooldown %q: %v", account, err)
		}
	}
}

func TestEnsureCodexInspectionOwnershipColumnsMigratesLegacyPrimaryKey(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "legacy-ownership.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`create table codex_inspection_disable_ownership (
		file_name text primary key,
		auth_index text,
		account_id text,
		disabled_at_ms integer not null,
		updated_at_ms integer not null
	)`); err != nil {
		t.Fatalf("create legacy ownership table: %v", err)
	}
	if _, err := db.Exec(`insert into codex_inspection_disable_ownership (
		file_name, auth_index, account_id, disabled_at_ms, updated_at_ms
	) values ('shared.json', 'auth-1', 'account-1', 10, 20)`); err != nil {
		t.Fatalf("insert legacy ownership: %v", err)
	}

	if err := ensureCodexInspectionOwnershipColumns(db); err != nil {
		t.Fatalf("migrate ownership table: %v", err)
	}
	if err := ensureCodexInspectionOwnershipColumns(db); err != nil {
		t.Fatalf("repeat ownership migration: %v", err)
	}
	ownershipPrimaryKey := migrationTablePrimaryKey(t, db, "codex_inspection_disable_ownership")
	for index, column := range []string{"file_name", "provider", "auth_index", "account_id", "account_snapshot"} {
		if ownershipPrimaryKey[column] != index+1 {
			t.Fatalf("migrated ownership primary key = %#v, want %s at position %d", ownershipPrimaryKey, column, index+1)
		}
	}
	if _, err := db.Exec(`insert into codex_inspection_disable_ownership (
		file_name, provider, auth_index, account_id, account_snapshot, disabled_at_ms, updated_at_ms
	) values ('shared.json', 'codex', 'auth-2', 'account-2', 'bob@example.com', 30, 40)`); err != nil {
		t.Fatalf("insert second same-file ownership: %v", err)
	}
	assertTableCount(t, db, "codex_inspection_disable_ownership", 2)
	var provider, authIndex, accountID, accountSnapshot string
	var disabledAtMS, updatedAtMS int64
	if err := db.QueryRow(`select provider, auth_index, account_id, account_snapshot, disabled_at_ms, updated_at_ms
		from codex_inspection_disable_ownership where auth_index = 'auth-1'`).Scan(
		&provider,
		&authIndex,
		&accountID,
		&accountSnapshot,
		&disabledAtMS,
		&updatedAtMS,
	); err != nil {
		t.Fatalf("read migrated ownership: %v", err)
	}
	if provider != "codex" || authIndex != "auth-1" || accountID != "account-1" || accountSnapshot != "" || disabledAtMS != 10 || updatedAtMS != 20 {
		t.Fatalf("migrated ownership = %q/%q/%q/%q/%d/%d", provider, authIndex, accountID, accountSnapshot, disabledAtMS, updatedAtMS)
	}
}

func TestEnsureAutomationColumnsAddsDecisionMetadata(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "legacy-automation.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`create table account_action_candidates (
		id integer primary key,
		status text,
		auth_file_name text,
		action_type text,
		auth_index text,
		provider text,
		account_snapshot text
	)`); err != nil {
		t.Fatalf("create account action table: %v", err)
	}
	if _, err := db.Exec(`create table quota_cooldowns (id integer primary key)`); err != nil {
		t.Fatalf("create quota cooldown table: %v", err)
	}
	if err := ensureAccountActionCandidateColumns(db); err != nil {
		t.Fatalf("migrate account action columns: %v", err)
	}
	if err := ensureQuotaCooldownColumns(db); err != nil {
		t.Fatalf("migrate quota cooldown columns: %v", err)
	}
	for _, column := range []string{"reason_code", "auto_disable_eligible", "auto_disabled_at_ms"} {
		if !migrationTableColumns(t, db, "account_action_candidates")[column] {
			t.Fatalf("missing account action column %s", column)
		}
	}
	for _, column := range []string{"reason_code", "window_kind", "evidence_json"} {
		if !migrationTableColumns(t, db, "quota_cooldowns")[column] {
			t.Fatalf("missing quota cooldown column %s", column)
		}
	}
	if _, err := db.Exec(`insert into account_action_candidates (
		id, status, auth_file_name, action_type, auth_index, reason_code, auto_disable_eligible
	) values
		(1, 'pending', 'xai.json', 'review', '1', 'credential_permission_denied', 1),
		(2, 'pending', 'xai.json', 'review', '1', 'authentication_review', 0)`); err != nil {
		t.Fatalf("insert distinct pending reason codes: %v", err)
	}
	if _, err := db.Exec(`insert into account_action_candidates (
		id, status, auth_file_name, action_type, auth_index, account_id_snapshot, provider, reason_code
	) values
		(3, 'pending', 'shared.json', 'reauth', null, 'shared-account', 'codex', 'token_revoked'),
		(4, 'pending', 'shared.json', 'reauth', null, 'shared-account', 'xai', 'token_revoked')`); err != nil {
		t.Fatalf("insert cross-provider account identities: %v", err)
	}
}

func TestEnsureCodexInspectionResultColumnsAddsIdentityAndAutoRecoveryColumns(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "legacy-codex-inspection.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`create table codex_inspection_results (id integer primary key)`); err != nil {
		t.Fatalf("create legacy results table: %v", err)
	}

	if err := ensureCodexInspectionResultColumns(db); err != nil {
		t.Fatalf("migrate codex inspection results: %v", err)
	}
	columns := migrationTableColumns(t, db, "codex_inspection_results")
	if !columns["account_snapshot"] || !columns["auto_recover_eligible"] {
		t.Fatalf("legacy results columns = %#v, want account_snapshot and auto_recover_eligible", columns)
	}
}

func TestEnsureUsageRollupLongContextColumnsParksPopulatedTablesAtomically(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "rollup-migration.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, statement := range []string{
		`create table usage_account_model_rollups (id integer primary key)`,
		`create table usage_dashboard_hourly_rollups (id integer primary key)`,
		`create table usage_rollup_checkpoints (name text primary key)`,
		`insert into usage_account_model_rollups (id) values (1)`,
		`insert into usage_dashboard_hourly_rollups (id) values (1)`,
		`insert into usage_rollup_checkpoints (name) values ('account_history'), ('dashboard_hourly')`,
		`create table ` + usageDashboardHourlyLegacy + ` (id integer primary key)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("setup migration fixture: %v", err)
		}
	}

	if err := ensureUsageRollupLongContextColumns(db); err == nil {
		t.Fatal("migration error = nil, want parked-table name conflict")
	}
	for _, table := range []string{"usage_account_model_rollups", "usage_dashboard_hourly_rollups"} {
		columns := migrationTableColumns(t, db, table)
		if columns["long_input_tokens"] {
			t.Fatalf("%s columns committed after failed migration: %#v", table, columns)
		}
	}
	assertTableCount(t, db, "usage_account_model_rollups", 1)
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 1)
	assertTableCount(t, db, "usage_rollup_checkpoints", 2)
	var accountLegacyCount int
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`, usageAccountModelRollupsLegacy).Scan(&accountLegacyCount); err != nil {
		t.Fatalf("inspect rolled-back account legacy table: %v", err)
	}
	if accountLegacyCount != 0 {
		t.Fatalf("account legacy table count after rollback = %d, want 0", accountLegacyCount)
	}

	if _, err := db.Exec(`drop table ` + usageDashboardHourlyLegacy); err != nil {
		t.Fatalf("drop conflicting legacy table: %v", err)
	}
	if err := ensureUsageRollupLongContextColumns(db); err != nil {
		t.Fatalf("retry migration: %v", err)
	}
	for _, table := range []string{"usage_account_model_rollups", "usage_dashboard_hourly_rollups"} {
		columns := migrationTableColumns(t, db, table)
		for _, column := range []string{
			"long_input_tokens",
			"long_output_tokens",
			"long_cached_tokens",
			"long_cache_read_tokens",
			"long_cache_creation_tokens",
		} {
			if !columns[column] {
				t.Fatalf("%s missing column %s after retry: %#v", table, column, columns)
			}
		}
	}
	assertTableCount(t, db, "usage_account_model_rollups", 0)
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 0)
	assertTableCount(t, db, usageAccountModelRollupsLegacy, 1)
	assertTableCount(t, db, usageDashboardHourlyLegacy, 1)
	assertTableCount(t, db, "usage_rollup_checkpoints", 0)
}

func TestUsageAccountModelRollupPrimaryKeyUpgradeRebuildsDerivedData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-account-model-primary-key.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`insert into usage_events (event_hash, timestamp_ms, timestamp, model, created_at_ms)
		values ('preserved-model-event', 1, '1', 'model-a', 1)`,
		`drop table usage_account_model_rollups`,
		`create table usage_account_model_rollups (
			account_key text not null, auth_index text, model text not null, billing_model text not null,
			service_tier text not null, calls integer not null default 0,
			first_seen_ms integer not null, last_seen_ms integer not null,
			updated_at_ms integer not null,
			primary key (account_key, billing_model, service_tier)
		)`,
		`insert into usage_account_model_rollups (
			account_key, auth_index, model, billing_model, service_tier, calls,
			first_seen_ms, last_seen_ms, updated_at_ms
		) values ('account-1', 'auth-a', 'model-a', 'resolved-x', '', 1, 1, 1, 1)`,
		`insert or replace into usage_rollup_checkpoints (name, last_event_id, updated_at_ms)
		values ('account_history', 1, 1)`,
		`drop table usage_pricing_account_rollups_v1`,
		`create table usage_pricing_account_rollups_v1 (
			structure_revision text not null, account_key text not null,
			model text not null, billing_model text not null, pricing_model text not null,
			service_tier text not null, context_threshold_tokens integer not null,
			calls integer not null default 0, first_seen_ms integer not null,
			last_seen_ms integer not null, updated_at_ms integer not null,
			primary key (structure_revision, account_key, billing_model, pricing_model,
				service_tier, context_threshold_tokens)
		)`,
		`insert into usage_pricing_account_rollups_v1 (
			structure_revision, account_key, model, billing_model, pricing_model,
			service_tier, context_threshold_tokens, calls,
			first_seen_ms, last_seen_ms, updated_at_ms
		) values ('legacy', 'account-1', 'model-a', 'resolved-x', 'resolved-x', '', -1, 1, 1, 1, 1)`,
		`insert into usage_pricing_hourly_rollups_v1 (
			structure_revision, bucket_ms, model, billing_model, pricing_model,
			service_tier, context_threshold_tokens, failed, calls, updated_at_ms
		) values ('legacy', 0, 'model-a', 'resolved-x', 'resolved-x', '', -1, 0, 1, 1)`,
		`update usage_pricing_rollup_state set
			structure_revision = 'legacy', status = 'ready',
			backfill_last_event_id = 1, coverage_event_id = 1,
			target_event_id = 1, processed_events = 1
		where rollup_name = 'pricing_v1'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("setup legacy primary key fixture: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy primary key fixture: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("upgrade primary keys: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if got := migrationTablePrimaryKey(t, db, usageAccountModelRollupsTable); got["model"] != 2 {
		t.Fatalf("account model rollup primary key = %#v", got)
	}
	if got := migrationTablePrimaryKey(t, db, usagePricingAccountRollupsTable); got["model"] != 3 {
		t.Fatalf("pricing account rollup primary key = %#v", got)
	}
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, usageAccountModelRollupsTable, 0)
	assertTableCount(t, db, usagePricingAccountRollupsTable, 0)
	assertTableCount(t, db, "usage_pricing_hourly_rollups_v1", 1)
	assertTableCount(t, db, usageAccountModelRollupsLegacy, 1)
	assertTableCount(t, db, usagePricingAccountLegacy, 1)
	var accountCheckpoints int
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints where name = 'account_history'`).Scan(&accountCheckpoints); err != nil {
		t.Fatalf("read account checkpoint: %v", err)
	}
	if accountCheckpoints != 0 {
		t.Fatalf("account checkpoint count = %d, want 0", accountCheckpoints)
	}
	var revision, status string
	var backfillLastEventID, coverageEventID, targetEventID, processedEvents int64
	if err := db.QueryRow(`select structure_revision, status, backfill_last_event_id,
		coverage_event_id, target_event_id, processed_events
		from usage_pricing_rollup_state where rollup_name = 'pricing_v1'`).Scan(
		&revision, &status, &backfillLastEventID, &coverageEventID, &targetEventID, &processedEvents,
	); err != nil {
		t.Fatalf("read pricing state: %v", err)
	}
	if revision != "" || status != "pending" || backfillLastEventID != 0 || coverageEventID != 0 || targetEventID != 1 || processedEvents != 0 {
		t.Fatalf("pricing state = revision:%q status:%q backfill:%d coverage:%d target:%d processed:%d",
			revision, status, backfillLastEventID, coverageEventID, targetEventID, processedEvents)
	}
}

func TestUsageAccountModelRollupPrimaryKeyUpgradeDoesNotDeleteDerivedRows(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "usage-account-model-primary-key-retry.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`create table usage_events (id integer primary key)`,
		`insert into usage_events (id) values (1)`,
		`create table usage_rollup_checkpoints (name text primary key)`,
		`insert into usage_rollup_checkpoints (name) values ('account_history')`,
		`create table usage_account_model_rollups (
			account_key text not null, model text not null, billing_model text not null,
			service_tier text not null, first_seen_ms integer not null,
			last_seen_ms integer not null, updated_at_ms integer not null,
			primary key (account_key, billing_model, service_tier)
		)`,
		`insert into usage_account_model_rollups values ('account-1', 'model-a', 'resolved-x', '', 1, 1, 1)`,
		`create table usage_pricing_hourly_rollups_v1 (id integer primary key)`,
		`insert into usage_pricing_hourly_rollups_v1 (id) values (1)`,
		`create table usage_pricing_account_rollups_v1 (
			structure_revision text not null, account_key text not null,
			model text not null, billing_model text not null, pricing_model text not null,
			service_tier text not null, context_threshold_tokens integer not null,
			first_seen_ms integer not null, last_seen_ms integer not null,
			updated_at_ms integer not null,
			primary key (structure_revision, account_key, billing_model, pricing_model,
				service_tier, context_threshold_tokens)
		)`,
		`insert into usage_pricing_account_rollups_v1 values ('legacy', 'account-1', 'model-a', 'resolved-x', 'resolved-x', '', -1, 1, 1, 1)`,
		`create table usage_pricing_rollup_state (
			rollup_name text primary key, structure_revision text not null,
			status text not null, backfill_last_event_id integer not null,
			coverage_event_id integer not null, target_event_id integer not null,
			processed_events integer not null, min_bucket_ms integer, max_bucket_ms integer,
			last_run_started_at_ms integer, updated_at_ms integer not null,
			finished_at_ms integer, last_error text
		)`,
		`insert into usage_pricing_rollup_state values ('pricing_v1', 'legacy', 'ready', 1, 1, 1, 1, null, null, null, 1, 1, null)`,
		`create trigger reject_pricing_hourly_delete before delete on usage_pricing_hourly_rollups_v1
		begin select raise(abort, 'blocked'); end`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("setup primary key retry fixture: %v", err)
		}
	}

	if err := ensureUsageAccountModelRollupPrimaryKeys(db); err != nil {
		t.Fatalf("primary key migration: %v", err)
	}
	if got := migrationTablePrimaryKey(t, db, usageAccountModelRollupsTable); got["model"] != 2 {
		t.Fatalf("account primary key = %#v", got)
	}
	if got := migrationTablePrimaryKey(t, db, usagePricingAccountRollupsTable); got["model"] != 3 {
		t.Fatalf("pricing primary key = %#v", got)
	}
	assertTableCount(t, db, usageAccountModelRollupsTable, 0)
	assertTableCount(t, db, usagePricingAccountRollupsTable, 0)
	assertTableCount(t, db, usageAccountModelRollupsLegacy, 1)
	assertTableCount(t, db, usagePricingAccountLegacy, 1)
	assertTableCount(t, db, "usage_pricing_hourly_rollups_v1", 1)
	assertTableCount(t, db, "usage_rollup_checkpoints", 0)

	if _, err := db.Exec(`drop trigger reject_pricing_hourly_delete`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	if err := ensureUsageAccountModelRollupPrimaryKeys(db); err != nil {
		t.Fatalf("repeat primary key migration: %v", err)
	}
	if got := migrationTablePrimaryKey(t, db, usageAccountModelRollupsTable); got["model"] != 2 {
		t.Fatalf("account primary key after retry = %#v", got)
	}
	if got := migrationTablePrimaryKey(t, db, usagePricingAccountRollupsTable); got["model"] != 3 {
		t.Fatalf("pricing primary key after retry = %#v", got)
	}
	assertTableCount(t, db, usageAccountModelRollupsTable, 0)
	assertTableCount(t, db, usagePricingAccountRollupsTable, 0)
	assertTableCount(t, db, "usage_pricing_hourly_rollups_v1", 1)
	assertTableCount(t, db, "usage_rollup_checkpoints", 0)
}

func TestAccountHistoryIdentityFormatUpgradeRebuildsDerivedDataOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "account-history-identity-format.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`insert into usage_events (event_hash, timestamp_ms, timestamp, model, created_at_ms)
		values ('preserved-account-event', 1, '1', '-', 1)`,
		`insert into usage_account_model_rollups (
			account_key, model, billing_model, service_tier, first_seen_ms, last_seen_ms, updated_at_ms
		) values ('legacy', '-', '-', '', 1, 1, 1)`,
		`create table usage_account_model_rollups_legacy_v1120_rc2 (id integer primary key)`,
		`insert into usage_account_model_rollups_legacy_v1120_rc2 (id) values (1)`,
		`create table usage_account_model_rollups_legacy_identity_v3 (id integer primary key)`,
		`insert into usage_account_model_rollups_legacy_identity_v3 (id) values (1)`,
		`insert into usage_dashboard_hourly_rollups (
			bucket_ms, model, billing_model, service_tier, updated_at_ms
		) values (0, '-', '-', '', 1)`,
		`insert into usage_rollup_checkpoints (name, last_event_id, updated_at_ms)
		values ('account_history', 1, 1), ('dashboard_hourly', 1, 1)`,
		`update settings set value = 'identity-2:model-1' where key = 'usage_account_history_identity_format_version'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("setup legacy account history identity: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("upgrade sqlite: %v", err)
	}
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, "usage_account_model_rollups", 0)
	assertTableCount(t, db, usageAccountModelRollupsLegacy, 1)
	assertTableCount(t, db, usageAccountModelIdentityLegacy, 1)
	assertTableCount(t, db, usageAccountModelCodexIdentityLegacy, 1)
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 1)
	var accountCheckpoints, dashboardCheckpoints int
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints where name = 'account_history'`).Scan(&accountCheckpoints); err != nil {
		t.Fatalf("read account checkpoint count: %v", err)
	}
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints where name = 'dashboard_hourly'`).Scan(&dashboardCheckpoints); err != nil {
		t.Fatalf("read dashboard checkpoint count: %v", err)
	}
	if accountCheckpoints != 0 || dashboardCheckpoints != 1 {
		t.Fatalf("checkpoint counts = account:%d dashboard:%d", accountCheckpoints, dashboardCheckpoints)
	}
	var version string
	if err := db.QueryRow(`select value from settings where key = ?`, accountHistoryIdentityFormatVersionKey).Scan(&version); err != nil {
		t.Fatalf("read account history identity version: %v", err)
	}
	if version != usageidentity.AccountHistoryStructureRevision() {
		t.Fatalf("identity version = %q, want %q", version, usageidentity.AccountHistoryStructureRevision())
	}
	var projectionRevision, projectionStatus string
	var projectionCoverage, projectionTarget int64
	if err := db.QueryRow(`select structure_revision, status, coverage_event_id, target_event_id
		from usage_monitoring_rollup_state where rollup_name = ?`, usageMonitoringProjectionRollupName).
		Scan(&projectionRevision, &projectionStatus, &projectionCoverage, &projectionTarget); err != nil {
		t.Fatalf("read projection identity rebuild state: %v", err)
	}
	if projectionRevision != usageidentity.MonitoringProjectionStructureRevision() || projectionStatus != "pending" || projectionCoverage != 0 || projectionTarget != 1 {
		t.Fatalf("projection identity rebuild state = revision:%q status:%q coverage:%d target:%d", projectionRevision, projectionStatus, projectionCoverage, projectionTarget)
	}
	for _, statement := range []string{
		`insert into usage_account_model_rollups (
			account_key, model, billing_model, service_tier, first_seen_ms, last_seen_ms, updated_at_ms
		) values ('rebuilt', '-', '-', '', 1, 1, 2)`,
		`insert into usage_rollup_checkpoints (name, last_event_id, updated_at_ms)
		values ('account_history', 1, 2)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("insert rebuilt account history data: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close upgraded sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen upgraded sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, "usage_account_model_rollups", 1)
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints where name = 'account_history'`).Scan(&accountCheckpoints); err != nil {
		t.Fatalf("read preserved account checkpoint: %v", err)
	}
	if accountCheckpoints != 1 {
		t.Fatalf("account checkpoint count after idempotent reopen = %d, want 1", accountCheckpoints)
	}
}

func TestAccountHistoryIdentityFormatUpgradeDoesNotDeleteDerivedRows(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "account-history-identity-retry.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`,
		`create table usage_events (id integer primary key)`,
		`create table usage_account_model_rollups (id integer primary key)`,
		`create table usage_rollup_checkpoints (name text primary key)`,
		`insert into settings (key, value, updated_at_ms)
		values ('usage_account_history_identity_format_version', '1', 1)`,
		`insert into usage_events (id) values (1)`,
		`insert into usage_account_model_rollups (id) values (1)`,
		`insert into usage_rollup_checkpoints (name) values ('account_history'), ('dashboard_hourly')`,
		`create trigger reject_account_identity_rollup_delete before delete on usage_account_model_rollups
		begin select raise(abort, 'blocked'); end`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("setup identity migration fixture: %v", err)
		}
	}

	if err := ensureAccountHistoryIdentityFormatVersion(db); err != nil {
		t.Fatalf("identity migration: %v", err)
	}
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, "usage_account_model_rollups", 0)
	assertTableCount(t, db, usageAccountModelCodexIdentityLegacy, 1)
	assertTableAbsent(t, db, usageAccountModelIdentityLegacy)
	assertTableCount(t, db, "usage_rollup_checkpoints", 1)
	var version string
	if err := db.QueryRow(`select value from settings where key = ?`, accountHistoryIdentityFormatVersionKey).Scan(&version); err != nil {
		t.Fatalf("read rolled-back identity version: %v", err)
	}
	if version != usageidentity.AccountHistoryStructureRevision() {
		t.Fatalf("identity version = %q, want %q", version, usageidentity.AccountHistoryStructureRevision())
	}

	if _, err := db.Exec(`drop trigger reject_account_identity_rollup_delete`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	if err := ensureAccountHistoryIdentityFormatVersion(db); err != nil {
		t.Fatalf("repeat identity migration: %v", err)
	}
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, "usage_account_model_rollups", 0)
	assertTableCount(t, db, usageAccountModelCodexIdentityLegacy, 1)
	assertTableAbsent(t, db, usageAccountModelIdentityLegacy)
	var accountCheckpoints, dashboardCheckpoints int
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints where name = 'account_history'`).Scan(&accountCheckpoints); err != nil {
		t.Fatalf("read account checkpoint count: %v", err)
	}
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints where name = 'dashboard_hourly'`).Scan(&dashboardCheckpoints); err != nil {
		t.Fatalf("read dashboard checkpoint count: %v", err)
	}
	if accountCheckpoints != 0 || dashboardCheckpoints != 1 {
		t.Fatalf("checkpoint counts after retry = account:%d dashboard:%d", accountCheckpoints, dashboardCheckpoints)
	}
}

func TestAccountHistoryIdentityFormatUpgradeUsesDedicatedCodexLegacyTable(t *testing.T) {
	tests := []struct {
		name                string
		version             string
		wantLegacyTable     string
		unwantedLegacyTable string
	}{
		{
			name:                "v2-structured",
			version:             legacyAccountHistoryStructureRevisionV2,
			wantLegacyTable:     usageAccountModelCodexIdentityLegacy,
			unwantedLegacyTable: usageAccountModelIdentityLegacy,
		},
		{
			name:                "v3-structured",
			version:             legacyAccountHistoryStructureRevisionV3,
			wantLegacyTable:     usageAccountModelCodexIdentityLegacy,
			unwantedLegacyTable: usageAccountModelIdentityLegacy,
		},
		{
			name:                "v1",
			version:             "1",
			wantLegacyTable:     usageAccountModelCodexIdentityLegacy,
			unwantedLegacyTable: usageAccountModelIdentityLegacy,
		},
		{
			name:                "v2",
			version:             "2",
			wantLegacyTable:     usageAccountModelCodexIdentityLegacy,
			unwantedLegacyTable: usageAccountModelIdentityLegacy,
		},
		{
			name:                "v3",
			version:             "3",
			wantLegacyTable:     usageAccountModelCodexIdentityLegacy,
			unwantedLegacyTable: usageAccountModelIdentityLegacy,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "account-history-identity-selection.sqlite")))
			if err != nil {
				t.Fatalf("open sqlite: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			for _, statement := range []string{
				`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`,
				`create table usage_events (id integer primary key)`,
				`create table usage_account_model_rollups (id integer primary key)`,
				`create table usage_rollup_checkpoints (name text primary key)`,
				`insert into settings (key, value, updated_at_ms)
				 values ('usage_account_history_identity_format_version', '` + test.version + `', 1)`,
				`insert into usage_events (id) values (1)`,
				`insert into usage_account_model_rollups (id) values (1)`,
			} {
				if _, err := db.Exec(statement); err != nil {
					t.Fatalf("setup %s: %v", test.name, err)
				}
			}

			if err := ensureAccountHistoryIdentityFormatVersion(db); err != nil {
				t.Fatalf("migrate %s: %v", test.name, err)
			}
			assertTableCount(t, db, usageAccountModelRollupsTable, 0)
			assertTableCount(t, db, test.wantLegacyTable, 1)
			assertTableAbsent(t, db, test.unwantedLegacyTable)

			if err := ensureAccountHistoryIdentityFormatVersion(db); err != nil {
				t.Fatalf("repeat migrate %s: %v", test.name, err)
			}
			assertTableCount(t, db, test.wantLegacyTable, 1)
		})
	}
}

func TestLegacyIdentityV3UpgradePreservesRawEventsAndSchedulesAllIdentityRollups(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-identity-v3.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`insert into usage_events (
			event_hash, timestamp_ms, timestamp, model, raw_json, created_at_ms
		) values ('legacy-v3-raw', 1700000000000, '2023-11-14T22:13:20Z', 'gpt-team', '{"immutable":true}', 1700000000000)`,
		`insert into usage_account_model_rollups (
			account_key, model, billing_model, service_tier, first_seen_ms, last_seen_ms, updated_at_ms
		) values ('legacy-workspace-key', 'gpt-team', 'gpt-team', '', 1700000000000, 1700000000000, 1)`,
		`update settings set value = 'identity-3:model-1'
			where key = 'usage_account_history_identity_format_version'`,
		`update usage_monitoring_rollup_state set
			structure_revision = 'identity-3:model-1:project-v1', status = 'ready',
			coverage_event_id = 1, target_event_id = 1
			where rollup_name in ('stats_v1', 'metadata_v1', 'projection_v1')`,
		`update usage_pricing_rollup_state set
			structure_revision = 'model-1:identity-3:legacy-price', status = 'ready',
			coverage_event_id = 1, target_event_id = 1
			where rollup_name = 'pricing_v1'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("setup legacy identity v3 database: %v", err)
		}
	}
	var beforeCount int
	var beforeHash, beforeRawJSON string
	if err := db.QueryRow(`select count(*), max(event_hash), max(raw_json) from usage_events`).Scan(&beforeCount, &beforeHash, &beforeRawJSON); err != nil {
		_ = db.Close()
		t.Fatalf("capture legacy raw event: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("upgrade legacy identity v3 sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var afterCount int
	var afterHash, afterRawJSON string
	if err := db.QueryRow(`select count(*), max(event_hash), max(raw_json) from usage_events`).Scan(&afterCount, &afterHash, &afterRawJSON); err != nil {
		t.Fatalf("read upgraded raw event: %v", err)
	}
	if afterCount != beforeCount || afterHash != beforeHash || afterRawJSON != beforeRawJSON {
		t.Fatalf("raw event changed by identity migration: before=%d/%q/%q after=%d/%q/%q", beforeCount, beforeHash, beforeRawJSON, afterCount, afterHash, afterRawJSON)
	}
	assertTableCount(t, db, usageAccountModelRollupsTable, 0)
	assertTableCount(t, db, usageAccountModelCodexIdentityLegacy, 1)
	var accountRevision string
	if err := db.QueryRow(`select value from settings where key = ?`, accountHistoryIdentityFormatVersionKey).Scan(&accountRevision); err != nil {
		t.Fatalf("read upgraded account identity revision: %v", err)
	}
	if accountRevision != usageidentity.AccountHistoryStructureRevision() {
		t.Fatalf("account identity revision = %q, want %q", accountRevision, usageidentity.AccountHistoryStructureRevision())
	}
	for _, rollupName := range []string{usageMonitoringStatsRollupName, usageMonitoringMetadataRollupName, usageMonitoringProjectionRollupName} {
		var revision, status string
		if err := db.QueryRow(`select structure_revision, status from usage_monitoring_rollup_state where rollup_name = ?`, rollupName).Scan(&revision, &status); err != nil {
			t.Fatalf("read upgraded monitoring state %s: %v", rollupName, err)
		}
		if status != "pending" {
			t.Fatalf("monitoring state %s status = %q, want pending", rollupName, status)
		}
		if rollupName == usageMonitoringProjectionRollupName && revision != usageidentity.MonitoringProjectionStructureRevision() {
			t.Fatalf("monitoring projection revision = %q, want %q", revision, usageidentity.MonitoringProjectionStructureRevision())
		}
	}

	wantPricingRevision, err := pricingrepo.StructureRevision(context.Background(), db)
	if err != nil {
		t.Fatalf("calculate current pricing revision: %v", err)
	}
	pricingResult, err := pricingrepo.New(db).CatchUp(context.Background(), 1, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("start pricing identity rebuild: %v", err)
	}
	if !pricingResult.Rebuilt {
		t.Fatalf("pricing identity rebuild result = %#v, want rebuilt", pricingResult)
	}
	pricingState, err := pricingrepo.New(db).State(context.Background())
	if err != nil {
		t.Fatalf("read rebuilding pricing state: %v", err)
	}
	if pricingState.StructureRevision != wantPricingRevision {
		t.Fatalf("pricing state revision after catch-up = %q, want %q", pricingState.StructureRevision, wantPricingRevision)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("repeat identity v3 migration: %v", err)
	}
	assertTableCount(t, db, usageAccountModelCodexIdentityLegacy, 1)
	if err := db.QueryRow(`select count(*), max(event_hash), max(raw_json) from usage_events`).Scan(&afterCount, &afterHash, &afterRawJSON); err != nil {
		t.Fatalf("read raw event after repeat migration: %v", err)
	}
	if afterCount != beforeCount || afterHash != beforeHash || afterRawJSON != beforeRawJSON {
		t.Fatalf("repeat identity migration changed raw event: %d/%q/%q", afterCount, afterHash, afterRawJSON)
	}
}

func TestLegacyAccountHistoryV3UpgradeSchedulesProjectionWhenProjectionRevisionIsCurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy-account-history-v3-current-projection.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	const rawJSON = `{"immutable":true,"member":"alice@example.com"}`
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, raw_json, created_at_ms
	) values ('legacy-v3-current-projection', 1700000000000, '2023-11-14T22:13:20Z', 'gpt-team', ?, 1700000000000)`, rawJSON); err != nil {
		t.Fatalf("insert immutable raw event: %v", err)
	}
	if _, err := db.Exec(`insert into usage_account_model_rollups (
		account_key, model, billing_model, service_tier, first_seen_ms, last_seen_ms, updated_at_ms
	) values ('legacy-workspace-key', 'gpt-team', 'gpt-team', '', 1700000000000, 1700000000000, 1)`); err != nil {
		t.Fatalf("insert legacy account rollup: %v", err)
	}
	if _, err := db.Exec(`update settings set value = 'identity-3:model-1'
		where key = 'usage_account_history_identity_format_version'`); err != nil {
		t.Fatalf("set legacy account history revision: %v", err)
	}
	if _, err := db.Exec(`update usage_monitoring_rollup_state set
		structure_revision = ?, status = 'ready', coverage_event_id = 1, target_event_id = 1
		where rollup_name = ?`, usageidentity.MonitoringProjectionStructureRevision(), usageMonitoringProjectionRollupName); err != nil {
		t.Fatalf("set current monitoring projection revision: %v", err)
	}
	if _, err := db.Exec(`update usage_pricing_rollup_state set
		structure_revision = 'current-pricing', status = 'ready', coverage_event_id = 1, target_event_id = 1
		where rollup_name = 'pricing_v1'`); err != nil {
		t.Fatalf("set current pricing revision: %v", err)
	}

	var beforeCount int
	var beforeHash, beforeRawJSON string
	if err := db.QueryRow(`select count(*), max(event_hash), max(raw_json) from usage_events`).Scan(&beforeCount, &beforeHash, &beforeRawJSON); err != nil {
		t.Fatalf("capture raw event before migration: %v", err)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("upgrade mixed identity revisions: %v", err)
	}

	var afterCount int
	var afterHash, afterRawJSON string
	if err := db.QueryRow(`select count(*), max(event_hash), max(raw_json) from usage_events`).Scan(&afterCount, &afterHash, &afterRawJSON); err != nil {
		t.Fatalf("read raw event after migration: %v", err)
	}
	if afterCount != beforeCount || afterHash != beforeHash || afterRawJSON != beforeRawJSON {
		t.Fatalf("mixed identity migration changed raw event: before=%d/%q/%q after=%d/%q/%q", beforeCount, beforeHash, beforeRawJSON, afterCount, afterHash, afterRawJSON)
	}
	assertTableCount(t, db, usageAccountModelRollupsTable, 0)
	assertTableCount(t, db, usageAccountModelCodexIdentityLegacy, 1)

	var accountRevision string
	if err := db.QueryRow(`select value from settings where key = ?`, accountHistoryIdentityFormatVersionKey).Scan(&accountRevision); err != nil {
		t.Fatalf("read upgraded account history revision: %v", err)
	}
	if accountRevision != usageidentity.AccountHistoryStructureRevision() {
		t.Fatalf("account history revision = %q, want %q", accountRevision, usageidentity.AccountHistoryStructureRevision())
	}
	var projectionRevision, projectionStatus string
	var projectionCoverage, projectionTarget int64
	if err := db.QueryRow(`select structure_revision, status, coverage_event_id, target_event_id
		from usage_monitoring_rollup_state where rollup_name = ?`, usageMonitoringProjectionRollupName).
		Scan(&projectionRevision, &projectionStatus, &projectionCoverage, &projectionTarget); err != nil {
		t.Fatalf("read scheduled projection state: %v", err)
	}
	if projectionRevision != usageidentity.MonitoringProjectionStructureRevision() || projectionStatus != "pending" || projectionCoverage != 0 || projectionTarget != 1 {
		t.Fatalf("scheduled projection state = revision:%q status:%q coverage:%d target:%d", projectionRevision, projectionStatus, projectionCoverage, projectionTarget)
	}

	if err := Migrate(db); err != nil {
		t.Fatalf("repeat mixed identity migration: %v", err)
	}
	assertTableCount(t, db, usageAccountModelCodexIdentityLegacy, 1)
	if err := db.QueryRow(`select count(*), max(event_hash), max(raw_json) from usage_events`).Scan(&afterCount, &afterHash, &afterRawJSON); err != nil {
		t.Fatalf("read raw event after repeated migration: %v", err)
	}
	if afterCount != beforeCount || afterHash != beforeHash || afterRawJSON != beforeRawJSON {
		t.Fatalf("repeat mixed identity migration changed raw event: before=%d/%q/%q after=%d/%q/%q", beforeCount, beforeHash, beforeRawJSON, afterCount, afterHash, afterRawJSON)
	}
}

func TestAccountHistoryIdentityFormatUpgradeRejectsUnknownVersionWithoutMutation(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "account-history-identity-future.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`,
		`create table usage_account_model_rollups (id integer primary key)`,
		`create table usage_rollup_checkpoints (name text primary key)`,
		`insert into settings (key, value, updated_at_ms)
		values ('usage_account_history_identity_format_version', 'future', 1)`,
		`insert into usage_account_model_rollups (id) values (1)`,
		`insert into usage_rollup_checkpoints (name) values ('account_history')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("setup future identity fixture: %v", err)
		}
	}

	if err := ensureAccountHistoryIdentityFormatVersion(db); err == nil {
		t.Fatal("identity migration error = nil, want unsupported version failure")
	}
	assertTableCount(t, db, "usage_account_model_rollups", 1)
	assertTableCount(t, db, "usage_rollup_checkpoints", 1)
	var version string
	if err := db.QueryRow(`select value from settings where key = ?`, accountHistoryIdentityFormatVersionKey).Scan(&version); err != nil {
		t.Fatalf("read preserved identity version: %v", err)
	}
	if version != "future" {
		t.Fatalf("identity version after rejection = %q, want future", version)
	}
}

func TestDashboardHourlyRollupFormatUpgradeRebuildsOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dashboard-rollup-format.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`insert into usage_events (event_hash, timestamp_ms, timestamp, model, created_at_ms)
		values ('preserved-event', 1, '1', '-', 1)`,
		`insert into usage_dashboard_hourly_rollups (
			bucket_ms, model, billing_model, service_tier, updated_at_ms
		) values (0, '-', '-', '', 1)`,
		`insert into usage_rollup_checkpoints (name, last_event_id, updated_at_ms)
		values ('dashboard_hourly', 1, 1), ('account_history', 1, 1)`,
		`update settings set value = '2', updated_at_ms = 1
		where key = 'usage_dashboard_hourly_format_version'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("setup legacy rollup: %v", err)
		}
	}
	var legacyVersion string
	if err := db.QueryRow(`select value from settings where key = ?`, dashboardHourlyRollupFormatVersionKey).Scan(&legacyVersion); err != nil {
		_ = db.Close()
		t.Fatalf("read legacy rollup format version: %v", err)
	}
	if legacyVersion != "2" {
		_ = db.Close()
		t.Fatalf("legacy rollup format version = %q, want 2", legacyVersion)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("upgrade sqlite: %v", err)
	}
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 0)
	assertTableCount(t, db, usageDashboardHourlyLegacy, 1)
	var dashboardCheckpoints, accountCheckpoints int
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints where name = 'dashboard_hourly'`).Scan(&dashboardCheckpoints); err != nil {
		t.Fatalf("read dashboard checkpoint count: %v", err)
	}
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints where name = 'account_history'`).Scan(&accountCheckpoints); err != nil {
		t.Fatalf("read account checkpoint count: %v", err)
	}
	if dashboardCheckpoints != 0 || accountCheckpoints != 1 {
		t.Fatalf("checkpoint counts = dashboard:%d account:%d", dashboardCheckpoints, accountCheckpoints)
	}
	var version string
	if err := db.QueryRow(`select value from settings where key = ?`, dashboardHourlyRollupFormatVersionKey).Scan(&version); err != nil {
		t.Fatalf("read rollup format version: %v", err)
	}
	if version != dashboardHourlyRollupFormatVersion {
		t.Fatalf("rollup format version = %q, want %q", version, dashboardHourlyRollupFormatVersion)
	}
	if _, err := db.Exec(`insert into usage_dashboard_hourly_rollups (
		bucket_ms, model, billing_model, service_tier, updated_at_ms
	) values (0, '-', '-', '', 2)`); err != nil {
		_ = db.Close()
		t.Fatalf("insert rebuilt rollup: %v", err)
	}
	if _, err := db.Exec(`insert into usage_rollup_checkpoints (name, last_event_id, updated_at_ms)
		values ('dashboard_hourly', 1, 2)`); err != nil {
		_ = db.Close()
		t.Fatalf("insert rebuilt checkpoint: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close upgraded sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen upgraded sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 1)
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints where name = 'dashboard_hourly'`).Scan(&dashboardCheckpoints); err != nil {
		t.Fatalf("read preserved dashboard checkpoint: %v", err)
	}
	if dashboardCheckpoints != 1 {
		t.Fatalf("dashboard checkpoint count after idempotent reopen = %d, want 1", dashboardCheckpoints)
	}
}

func TestDashboardHourlyRollupFormatUpgradeDoesNotDeleteDerivedRows(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "dashboard-rollup-format-retry.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`,
		`create table usage_dashboard_hourly_rollups (id integer primary key)`,
		`create table usage_rollup_checkpoints (name text primary key)`,
		`insert into usage_dashboard_hourly_rollups (id) values (1)`,
		`insert into usage_rollup_checkpoints (name) values ('dashboard_hourly'), ('account_history')`,
		`create trigger reject_dashboard_rollup_delete before delete on usage_dashboard_hourly_rollups
		begin select raise(abort, 'blocked'); end`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("setup migration fixture: %v", err)
		}
	}

	if err := ensureDashboardHourlyRollupFormatVersion(db); err != nil {
		t.Fatalf("format migration: %v", err)
	}
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 0)
	assertTableCount(t, db, usageDashboardHourlyLegacy, 1)
	assertTableCount(t, db, "usage_rollup_checkpoints", 1)
	var settingCount int
	if err := db.QueryRow(`select count(*) from settings where key = ?`, dashboardHourlyRollupFormatVersionKey).Scan(&settingCount); err != nil {
		t.Fatalf("read format setting count: %v", err)
	}
	if settingCount != 1 {
		t.Fatalf("format setting count = %d, want 1", settingCount)
	}

	if _, err := db.Exec(`drop trigger reject_dashboard_rollup_delete`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	if err := ensureDashboardHourlyRollupFormatVersion(db); err != nil {
		t.Fatalf("repeat format migration: %v", err)
	}
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 0)
	assertTableCount(t, db, usageDashboardHourlyLegacy, 1)
	var dashboardCheckpoints, accountCheckpoints int
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints where name = 'dashboard_hourly'`).Scan(&dashboardCheckpoints); err != nil {
		t.Fatalf("read dashboard checkpoint count: %v", err)
	}
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints where name = 'account_history'`).Scan(&accountCheckpoints); err != nil {
		t.Fatalf("read account checkpoint count: %v", err)
	}
	if dashboardCheckpoints != 0 || accountCheckpoints != 1 {
		t.Fatalf("checkpoint counts after retry = dashboard:%d account:%d", dashboardCheckpoints, accountCheckpoints)
	}
}

func TestDashboardHourlyRollupFormatUpgradeRejectsUnknownVersionWithoutMutation(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "dashboard-rollup-format-unknown.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`create table settings (key text primary key, value text not null, updated_at_ms integer not null)`,
		`create table usage_dashboard_hourly_rollups (id integer primary key)`,
		`create table usage_rollup_checkpoints (name text primary key)`,
		`insert into settings (key, value, updated_at_ms)
		values ('usage_dashboard_hourly_format_version', 'future', 1)`,
		`insert into usage_dashboard_hourly_rollups (id) values (1)`,
		`insert into usage_rollup_checkpoints (name) values ('dashboard_hourly'), ('account_history')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("setup unknown format fixture: %v", err)
		}
	}

	if err := ensureDashboardHourlyRollupFormatVersion(db); err == nil {
		t.Fatal("format migration error = nil, want unsupported version failure")
	}
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 1)
	assertTableCount(t, db, "usage_rollup_checkpoints", 2)
	var version string
	if err := db.QueryRow(`select value from settings where key = ?`, dashboardHourlyRollupFormatVersionKey).Scan(&version); err != nil {
		t.Fatalf("read preserved format version: %v", err)
	}
	if version != "future" {
		t.Fatalf("format version after rejection = %q, want future", version)
	}
}

func TestUsageHourlyAggregateMigrationIsAdditiveAndSeedsPendingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-hourly-aggregate.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`drop table usage_event_identity_ledger`,
		`drop table usage_hourly_aggregate_state`,
		`drop table usage_hourly_aggregate_v1`,
		`insert into usage_events (event_hash, timestamp_ms, timestamp, model, created_at_ms)
		values ('aggregate-migration-event', 3600001, '1970-01-01T01:00:00.001Z', 'gpt-test', 1)`,
		`insert into usage_dashboard_hourly_rollups (
			bucket_ms, model, billing_model, service_tier, updated_at_ms
		) values (3600000, 'gpt-test', 'gpt-test', '', 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("setup legacy aggregate database: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy aggregate database: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen migrated aggregate database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, table := range []string{
		"usage_hourly_aggregate_v1",
		"usage_hourly_aggregate_state",
		"usage_event_identity_ledger",
	} {
		var count int
		if err := db.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("query sqlite_master for %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("expected table %s to exist", table)
		}
	}
	columns := migrationTableColumns(t, db, "usage_hourly_aggregate_v1")
	for _, column := range []string{"bucket_ms", "model", "billing_model", "service_tier", "failed", "latency_sum_ms", "latency_samples"} {
		if !columns[column] {
			t.Fatalf("aggregate columns = %#v, missing %s", columns, column)
		}
	}
	var version int
	var status string
	var checkpoint, coverage, target int64
	if err := db.QueryRow(`select schema_version, status, backfill_last_event_id, coverage_event_id, target_event_id
		from usage_hourly_aggregate_state where aggregate_name = 'hourly_core'`).Scan(
		&version,
		&status,
		&checkpoint,
		&coverage,
		&target,
	); err != nil {
		t.Fatalf("read aggregate state: %v", err)
	}
	if version != usageHourlyAggregateSchemaVersion || status != "pending" || checkpoint != 0 || coverage != 0 || target != 1 {
		t.Fatalf("aggregate state = version:%d status:%q checkpoint:%d coverage:%d target:%d", version, status, checkpoint, coverage, target)
	}
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 1)
}

func TestMigrateRejectsUnknownHourlyAggregateVersionBeforeOtherSchemaChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-hourly-aggregate-preflight.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`drop index if exists idx_usage_events_latest_request_auth_file`,
		`update usage_hourly_aggregate_state set schema_version = 99
		where aggregate_name = 'hourly_core'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("setup future hourly aggregate preflight fixture: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close future hourly aggregate preflight fixture: %v", err)
	}

	if _, err := Open(path); err == nil {
		t.Fatal("reopen future hourly aggregate error = nil, want unsupported version failure")
	}
	db, err = sql.Open("sqlite", dataSourceName(path))
	if err != nil {
		t.Fatalf("open rejected hourly aggregate preflight sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var indexCount int
	if err := db.QueryRow(`select count(*) from sqlite_master
		where type = 'index' and name = 'idx_usage_events_latest_request_auth_file'`).Scan(&indexCount); err != nil {
		t.Fatalf("inspect unrelated index after hourly preflight rejection: %v", err)
	}
	if indexCount != 0 {
		t.Fatalf("unrelated index count after hourly preflight rejection = %d, want 0", indexCount)
	}
}

func TestNewUsageHourlyAggregateRebuildRevisionIsRandomAndUnique(t *testing.T) {
	prefix := usageHourlyAggregateStructureRevision + ":rebuild-"
	revisions := make(map[string]struct{}, 2)
	for index := 0; index < 2; index++ {
		revision, err := newUsageHourlyAggregateRebuildRevision()
		if err != nil {
			t.Fatalf("generate rebuild revision: %v", err)
		}
		if !strings.HasPrefix(revision, prefix) {
			t.Fatalf("rebuild revision = %q, want prefix %q", revision, prefix)
		}
		suffix := strings.TrimPrefix(revision, prefix)
		decoded, err := hex.DecodeString(suffix)
		if err != nil || len(decoded) != 16 {
			t.Fatalf("rebuild revision suffix = %q, decoded=%d err=%v", suffix, len(decoded), err)
		}
		if _, exists := revisions[revision]; exists {
			t.Fatalf("duplicate rebuild revision = %q", revision)
		}
		revisions[revision] = struct{}{}
	}
}

func TestUsageHourlyAggregateMigrationResetsCoverageWhenAggregateTableWasLost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-hourly-aggregate-lost-table.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	seedReadyUsageHourlyAggregate(t, db)
	if _, err := db.Exec(`drop table usage_hourly_aggregate_v1`); err != nil {
		_ = db.Close()
		t.Fatalf("drop aggregate table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close damaged sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen damaged sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, "usage_hourly_aggregate_v1", 0)
	assertUsageHourlyAggregateReset(t, db, 1)
	var ledgerVersion int
	if err := db.QueryRow(`select aggregate_schema_version from usage_event_identity_ledger
		where event_hash = 'aggregate-recovery-event'`).Scan(&ledgerVersion); err != nil {
		t.Fatalf("read reset aggregate ledger: %v", err)
	}
	if ledgerVersion != usageHourlyAggregateSchemaVersion {
		t.Fatalf("aggregate ledger version = %d, want %d", ledgerVersion, usageHourlyAggregateSchemaVersion)
	}
}

func TestUsageHourlyAggregateMigrationResetsStoredRowsWhenLedgerTableWasLost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-hourly-aggregate-lost-ledger.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	seedReadyUsageHourlyAggregate(t, db)
	if _, err := db.Exec(`drop table usage_event_identity_ledger`); err != nil {
		_ = db.Close()
		t.Fatalf("drop aggregate ledger: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close damaged sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen damaged sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, "usage_hourly_aggregate_v1", 0)
	assertTableCount(t, db, "usage_event_identity_ledger", 0)
	assertUsageHourlyAggregateReset(t, db, 1)
}

func TestUsageHourlyAggregateMigrationResetsStoredRowsWhenStateTableWasLost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-hourly-aggregate-lost-state-table.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	seedReadyUsageHourlyAggregate(t, db)
	for _, statement := range []string{
		`update usage_event_identity_ledger set aggregate_schema_version = 1`,
		`drop table usage_hourly_aggregate_state`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare lost aggregate state table: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close damaged sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen damaged sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, "usage_hourly_aggregate_v1", 0)
	assertUsageHourlyAggregateReset(t, db, 1)
}

func TestUsageHourlyAggregateMigrationResetsStoredRowsWhenStateRowWasLost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-hourly-aggregate-lost-state-row.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	seedReadyUsageHourlyAggregate(t, db)
	for _, statement := range []string{
		`update usage_event_identity_ledger set aggregate_schema_version = 1`,
		`delete from usage_hourly_aggregate_state where aggregate_name = 'hourly_core'`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare lost aggregate state row: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close damaged sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen damaged sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertTableCount(t, db, "usage_events", 1)
	assertTableCount(t, db, "usage_hourly_aggregate_v1", 0)
	assertUsageHourlyAggregateReset(t, db, 1)
}

func TestUsageHourlyAggregateMigrationPreservesCompleteCoverageOnRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-hourly-aggregate-complete.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	seedReadyUsageHourlyAggregate(t, db)
	if err := db.Close(); err != nil {
		t.Fatalf("close complete sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen complete sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertTableCount(t, db, "usage_hourly_aggregate_v1", 1)
	var ledgerVersion int
	if err := db.QueryRow(`select aggregate_schema_version from usage_event_identity_ledger
		where event_hash = 'aggregate-recovery-event'`).Scan(&ledgerVersion); err != nil {
		t.Fatalf("read preserved aggregate ledger: %v", err)
	}
	if ledgerVersion != usageHourlyAggregateSchemaVersion {
		t.Fatalf("aggregate ledger version = %d, want %d", ledgerVersion, usageHourlyAggregateSchemaVersion)
	}
	var status string
	var checkpoint, coverage, target, processed int64
	if err := db.QueryRow(`select status, backfill_last_event_id, coverage_event_id,
		target_event_id, processed_events from usage_hourly_aggregate_state
		where aggregate_name = 'hourly_core'`).Scan(
		&status,
		&checkpoint,
		&coverage,
		&target,
		&processed,
	); err != nil {
		t.Fatalf("read preserved aggregate state: %v", err)
	}
	if status != "ready" || checkpoint != 1 || coverage != 1 || target != 1 || processed != 1 {
		t.Fatalf("preserved aggregate state = status:%q checkpoint:%d coverage:%d target:%d processed:%d",
			status, checkpoint, coverage, target, processed)
	}
}

func TestUsageHourlyAggregateMigrationRecoveryDoesNotDeleteDerivedRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage-hourly-aggregate-recovery-retry.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	seedReadyUsageHourlyAggregate(t, db)
	for _, statement := range []string{
		`drop table usage_event_identity_ledger`,
		`create trigger reject_usage_hourly_aggregate_recovery
		before delete on usage_hourly_aggregate_v1
		begin select raise(abort, 'blocked'); end`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare aggregate recovery retry fixture: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close damaged sqlite: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("recover aggregate without deleting legacy rows: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertTableCount(t, db, "usage_hourly_aggregate_v1", 0)
	assertTableCount(t, db, usageHourlyAggregateLegacy, 1)
	assertTableCount(t, db, "usage_event_identity_ledger", 0)
	assertUsageHourlyAggregateReset(t, db, 1)
}

func seedReadyUsageHourlyAggregate(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`insert into usage_events (
			event_hash, timestamp_ms, timestamp, model, input_tokens,
			output_tokens, total_tokens, created_at_ms
		) values (
			'aggregate-recovery-event', 3600001, '1970-01-01T01:00:00.001Z',
			'gpt-test', 10, 5, 15, 1
		)`,
		`insert into usage_hourly_aggregate_v1 (
			bucket_ms, model, billing_model, service_tier, failed, calls,
			input_tokens, output_tokens, total_tokens, updated_at_ms
		) values (3600000, 'gpt-test', 'gpt-test', '', 0, 1, 10, 5, 15, 1)`,
		`insert into usage_event_identity_ledger (
			event_hash, raw_event_id, timestamp_ms, bucket_ms,
			aggregate_schema_version, aggregate_structure_revision,
			first_seen_at_ms, updated_at_ms
		) select event_hash, id, timestamp_ms, 3600000, 3,
			'schema-3:model-1', created_at_ms, 1
		from usage_events where event_hash = 'aggregate-recovery-event'`,
		`update usage_hourly_aggregate_state set
			status = 'ready', backfill_last_event_id = 1, coverage_event_id = 1,
			target_event_id = 1, processed_events = 1,
			min_bucket_ms = 3600000, max_bucket_ms = 3600000,
			last_run_started_at_ms = 1, updated_at_ms = 1,
			finished_at_ms = 1, last_error = null
		where aggregate_name = 'hourly_core' and schema_version = 3`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed ready usage hourly aggregate: %v", err)
		}
	}
}

func assertUsageHourlyAggregateReset(t *testing.T, db *sql.DB, wantTarget int64) {
	t.Helper()
	assertUsageHourlyAggregateState(t, db, "pending", wantTarget)
}

func assertUsageHourlyAggregateState(t *testing.T, db *sql.DB, wantStatus string, wantTarget int64) {
	t.Helper()
	var version int
	var status string
	var checkpoint, coverage, target, processed int64
	var minBucket, maxBucket, lastRun, finished sql.NullInt64
	if err := db.QueryRow(`select schema_version, status, backfill_last_event_id,
		coverage_event_id, target_event_id, processed_events, min_bucket_ms,
		max_bucket_ms, last_run_started_at_ms, finished_at_ms
		from usage_hourly_aggregate_state where aggregate_name = 'hourly_core'`).Scan(
		&version,
		&status,
		&checkpoint,
		&coverage,
		&target,
		&processed,
		&minBucket,
		&maxBucket,
		&lastRun,
		&finished,
	); err != nil {
		t.Fatalf("read reset aggregate state: %v", err)
	}
	if version != usageHourlyAggregateSchemaVersion || status != wantStatus || checkpoint != 0 || coverage != 0 ||
		target != wantTarget || processed != 0 || minBucket.Valid || maxBucket.Valid ||
		lastRun.Valid || finished.Valid {
		t.Fatalf("reset aggregate state = version:%d status:%q checkpoint:%d coverage:%d target:%d processed:%d min:%v max:%v last_run:%v finished:%v",
			version, status, checkpoint, coverage, target, processed,
			minBucket, maxBucket, lastRun, finished)
	}
}

func assertEmptyUsageDerivedState(t *testing.T, db *sql.DB) {
	t.Helper()
	var rollupCheckpointCount int
	if err := db.QueryRow(`select count(*) from usage_rollup_checkpoints
		where name in ('account_history', 'dashboard_hourly')`).Scan(&rollupCheckpointCount); err != nil {
		t.Fatalf("count reset usage rollup checkpoints: %v", err)
	}
	if rollupCheckpointCount != 0 {
		t.Fatalf("reset usage rollup checkpoints = %d, want 0", rollupCheckpointCount)
	}

	var pricingStatus string
	var pricingCheckpoint, pricingCoverage, pricingTarget, pricingProcessed int64
	if err := db.QueryRow(`select status, backfill_last_event_id, coverage_event_id,
		target_event_id, processed_events from usage_pricing_rollup_state
		where rollup_name = 'pricing_v1'`).Scan(
		&pricingStatus,
		&pricingCheckpoint,
		&pricingCoverage,
		&pricingTarget,
		&pricingProcessed,
	); err != nil {
		t.Fatalf("read reset pricing state: %v", err)
	}
	if pricingStatus != "ready" || pricingCheckpoint != 0 || pricingCoverage != 0 ||
		pricingTarget != 0 || pricingProcessed != 0 {
		t.Fatalf("reset pricing state = status:%q checkpoint:%d coverage:%d target:%d processed:%d",
			pricingStatus, pricingCheckpoint, pricingCoverage, pricingTarget, pricingProcessed)
	}

	rows, err := db.Query(`select rollup_name, status, backfill_last_event_id,
		coverage_event_id, target_event_id, processed_events
		from usage_monitoring_rollup_state order by rollup_name`)
	if err != nil {
		t.Fatalf("read reset monitoring state: %v", err)
	}
	defer rows.Close()
	monitoringStates := 0
	for rows.Next() {
		var rollupName, status string
		var checkpoint, coverage, target, processed int64
		if err := rows.Scan(&rollupName, &status, &checkpoint, &coverage, &target, &processed); err != nil {
			t.Fatalf("scan reset monitoring state: %v", err)
		}
		monitoringStates++
		if status != "ready" || checkpoint != 0 || coverage != 0 || target != 0 || processed != 0 {
			t.Fatalf("reset monitoring state %s = status:%q checkpoint:%d coverage:%d target:%d processed:%d",
				rollupName, status, checkpoint, coverage, target, processed)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate reset monitoring state: %v", err)
	}
	if monitoringStates != 3 {
		t.Fatalf("reset monitoring state rows = %d, want 3", monitoringStates)
	}
	var searchReady int
	if err := db.QueryRow(`select ready from usage_monitoring_search_index_state where id = 1`).Scan(&searchReady); err != nil {
		t.Fatalf("read reset monitoring search state: %v", err)
	}
	if searchReady != 0 {
		t.Fatalf("deferred empty monitoring search state = %d, want 0", searchReady)
	}

	rows, err = db.Query(`select name, status, last_event_id, target_event_id,
		processed_rows, changed_rows, applied_rows from usage_data_migrations
		where name in ('usage_cache_accounting_v1', 'usage_cache_accounting_v2')
		order by name`)
	if err != nil {
		t.Fatalf("read reset usage data migrations: %v", err)
	}
	defer rows.Close()
	migrationStates := 0
	for rows.Next() {
		var name, status string
		var lastEventID, targetEventID, processedRows, changedRows, appliedRows int64
		if err := rows.Scan(
			&name,
			&status,
			&lastEventID,
			&targetEventID,
			&processedRows,
			&changedRows,
			&appliedRows,
		); err != nil {
			t.Fatalf("scan reset usage data migration: %v", err)
		}
		migrationStates++
		if status != "completed" || lastEventID != 0 || targetEventID != 0 ||
			processedRows != 0 || changedRows != 0 || appliedRows != 0 {
			t.Fatalf("reset usage data migration %s = status:%q last:%d target:%d processed:%d changed:%d applied:%d",
				name, status, lastEventID, targetEventID, processedRows, changedRows, appliedRows)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate reset usage data migrations: %v", err)
	}
	if migrationStates != 2 {
		t.Fatalf("reset usage data migration rows = %d, want 2", migrationStates)
	}
}

func TestEnsureUsageEventSnapshotColumnsOnlyMigratesSchema(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "usage-event-migration.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, statement := range []string{
		`create table usage_events (
			id integer primary key,
			provider text,
			model text not null,
			input_tokens integer not null default 0,
			cached_tokens integer not null default 0,
			cache_tokens integer not null default 0
		)`,
		`insert into usage_events (id, provider, model, input_tokens, cached_tokens, cache_tokens)
		values (1, 'anthropic', 'claude-sonnet-4', 100, 300, 0)`,
		`create table usage_account_model_rollups (id integer primary key)`,
		`create table usage_dashboard_hourly_rollups (id integer primary key)`,
		`create table usage_rollup_checkpoints (
			name text primary key,
			last_event_id integer not null,
			updated_at_ms integer not null,
			last_error text
		)`,
		`insert into usage_account_model_rollups (id) values (1)`,
		`insert into usage_dashboard_hourly_rollups (id) values (1)`,
		`insert into usage_rollup_checkpoints (name, last_event_id, updated_at_ms, last_error)
		values ('account_history', 1, 1, 'old')`,
		`create trigger reject_usage_event_update before update on usage_events
		begin select raise(abort, 'blocked'); end`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("setup migration fixture: %v", err)
		}
	}

	if err := ensureUsageEventSnapshotColumns(db); err != nil {
		t.Fatalf("migrate usage event schema: %v", err)
	}
	columns := migrationTableColumns(t, db, "usage_events")
	if !columns["cache_input_mode"] ||
		!columns["normalized_total_input_tokens"] ||
		!columns["client_ip"] ||
		!columns["x_forwarded_for"] ||
		!columns["user_agent"] {
		t.Fatalf("usage event schema columns = %#v", columns)
	}
	assertTableCount(t, db, "usage_account_model_rollups", 1)
	assertTableCount(t, db, "usage_dashboard_hourly_rollups", 1)
	var normalizedTotal sql.NullInt64
	if err := db.QueryRow(`select normalized_total_input_tokens from usage_events where id = 1`).Scan(&normalizedTotal); err != nil {
		t.Fatalf("read partially migrated usage event: %v", err)
	}
	if normalizedTotal.Valid {
		t.Fatalf("schema migration unexpectedly backfilled normalized total: %d", normalizedTotal.Int64)
	}
}

func TestEnsureModelPriceColumnsPreservesLegacyZeroBasePrices(t *testing.T) {
	db, err := sql.Open("sqlite", dataSourceName(filepath.Join(t.TempDir(), "model-price-migration.sqlite")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, statement := range []string{
		`create table model_prices (
			model text primary key,
			prompt_per_1m real not null,
			completion_per_1m real not null,
			cache_per_1m real not null
		)`,
		`insert into model_prices (model, prompt_per_1m, completion_per_1m, cache_per_1m)
		values ('gpt-5.6-sol', 0, 0, 0)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("setup model price fixture: %v", err)
		}
	}

	if err := ensureModelPriceColumns(db); err != nil {
		t.Fatalf("migrate model prices: %v", err)
	}
	var promptConfigured, completionConfigured, cacheReadConfigured, cacheCreationConfigured int
	if err := db.QueryRow(`select prompt_configured, completion_configured, cache_read_configured, cache_creation_configured
		from model_prices where model = 'gpt-5.6-sol'`).Scan(
		&promptConfigured,
		&completionConfigured,
		&cacheReadConfigured,
		&cacheCreationConfigured,
	); err != nil {
		t.Fatalf("read migrated price flags: %v", err)
	}
	if promptConfigured != 1 || completionConfigured != 1 || cacheReadConfigured != 0 || cacheCreationConfigured != 0 {
		t.Fatalf("configured flags = %d/%d/%d/%d", promptConfigured, completionConfigured, cacheReadConfigured, cacheCreationConfigured)
	}
}

func TestMigrateCreatesModelPriceServiceTierTableWithCascade(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "model-price-service-tier.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.Exec(`insert into model_prices (
		model, prompt_per_1m, completion_per_1m, cache_per_1m, updated_at_ms
	) values ('gpt-test', 1, 2, 0.1, 1)`); err != nil {
		t.Fatalf("insert model price: %v", err)
	}
	if _, err := db.Exec(`insert into model_price_service_tiers (
		model, mode, service_tier, prompt_per_1m, prompt_configured
	) values ('gpt-test', 'fast', 'priority', 2.5, 1)`); err != nil {
		t.Fatalf("insert model price service tier: %v", err)
	}
	if _, err := db.Exec(`delete from model_prices where model = 'gpt-test'`); err != nil {
		t.Fatalf("delete model price: %v", err)
	}
	assertTableCount(t, db, "model_price_service_tiers", 0)
}

func migrationTableColumns(t *testing.T, db *sql.DB, table string) map[string]bool {
	t.Helper()
	rows, err := db.Query(`pragma table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("read %s columns: %v", table, err)
	}
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan %s columns: %v", table, err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s columns: %v", table, err)
	}
	return columns
}

func migrationTablePrimaryKey(t *testing.T, db *sql.DB, table string) map[string]int {
	t.Helper()
	rows, err := db.Query(`pragma table_info(` + table + `)`)
	if err != nil {
		t.Fatalf("read %s primary key: %v", table, err)
	}
	defer rows.Close()

	primaryKey := map[string]int{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var position int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &position); err != nil {
			t.Fatalf("scan %s primary key: %v", table, err)
		}
		if position > 0 {
			primaryKey[name] = position
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s primary key: %v", table, err)
	}
	return primaryKey
}

func assertTableCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`select count(*) from ` + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func assertTableAbsent(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var exists int
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`, table).Scan(&exists); err != nil {
		t.Fatalf("inspect %s: %v", table, err)
	}
	if exists != 0 {
		t.Fatalf("table %s exists, want absent", table)
	}
}
