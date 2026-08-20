package sqlite

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	quotasnapshotrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	accountHistoryIdentityFormatVersionKey = "usage_account_history_identity_format_version"
	dashboardHourlyRollupFormatVersionKey  = "usage_dashboard_hourly_format_version"
	dashboardHourlyRollupFormatVersion     = "3"
	usageMonitoringModelFormatVersionKey   = "usage_monitoring_model_format_version"
	usageHourlyAggregateSchemaVersion      = 3
	usageHourlyAggregateStructureRevision  = "schema-3:model-1"

	usageMonitoringAccountDailyTable  = "usage_monitoring_account_daily_rollups_v1"
	usageMonitoringAPIKeyDailyTable   = "usage_monitoring_api_key_daily_rollups_v1"
	usageMonitoringSelectorDailyTable = "usage_monitoring_selector_daily_rollups_v1"
	usageMonitoringHeaderLatestTable  = "usage_monitoring_header_latest_v1"
	usageMonitoringRollupStateTable   = "usage_monitoring_rollup_state"
	usageMonitoringSearchStateTable   = "usage_monitoring_search_index_state"

	usageMonitoringStatsRollupName      = "stats_v1"
	usageMonitoringMetadataRollupName   = "metadata_v1"
	usageMonitoringProjectionRollupName = "projection_v1"

	usageHourlyAggregateTable = "usage_hourly_aggregate_v1"
	usageHourlyAggregateState = "usage_hourly_aggregate_state"
	usageEventIdentityLedger  = "usage_event_identity_ledger"

	usageAccountModelRollupsTable         = "usage_account_model_rollups"
	usagePricingAccountRollupsTable       = "usage_pricing_account_rollups_v1"
	usageAccountModelRollupsLegacy        = "usage_account_model_rollups_legacy_v1120_rc2"
	usagePricingAccountLegacy             = "usage_pricing_account_rollups_v1_legacy_v1120_rc2"
	usageDashboardHourlyLegacy            = "usage_dashboard_hourly_rollups_legacy_v1120_rc2"
	usageHourlyAggregateLegacy            = "usage_hourly_aggregate_v1_legacy_v1120_rc2"
	usageMonitoringSearchLegacy           = "usage_monitoring_event_search_v1_legacy_v1120_rc2"
	usageMonitoringSearchLegacyPrefix     = "usage_monitoring_event_search_v1_legacy_g"
	usageMonitoringProjectionLegacyPrefix = "usage_monitoring_event_projection_v1_legacy_g"
	usageMonitoringAccountLegacy          = "usage_monitoring_account_daily_rollups_v1_legacy_recovery"
	usageMonitoringAPIKeyLegacy           = "usage_monitoring_api_key_daily_rollups_v1_legacy_recovery"
	usageMonitoringSelectorLegacy         = "usage_monitoring_selector_daily_rollups_v1_legacy_recovery"
	usageMonitoringHeaderLegacy           = "usage_monitoring_header_latest_v1_legacy_recovery"
	usageMonitoringProjectionLegacy       = "usage_monitoring_event_projection_v1_legacy_recovery"
	usageDashboardHourlySourceLegacy      = "usage_dashboard_hourly_rollups_legacy_source_recovery"
	usagePricingHourlySourceLegacy        = "usage_pricing_hourly_rollups_v1_legacy_source_recovery"
	usageCacheChangesSourceLegacy         = "usage_cache_accounting_v2_changes_legacy_source_recovery"
	usageAccountModelSourceLegacy         = "usage_account_model_rollups_legacy_source_recovery"
	usagePricingAccountSourceLegacy       = "usage_pricing_account_rollups_v1_legacy_source_recovery"

	createUsageAccountModelRollupsTable = `create table if not exists usage_account_model_rollups (
		account_key text not null,
		account_snapshot text,
		auth_label_snapshot text,
		auth_provider_snapshot text,
		auth_index text,
		source text,
		source_hash text,
		model text not null,
		billing_model text not null,
		service_tier text not null,
		calls integer not null default 0,
		success_calls integer not null default 0,
		failure_calls integer not null default 0,
		input_tokens integer not null default 0,
		output_tokens integer not null default 0,
		reasoning_tokens integer not null default 0,
		cached_tokens integer not null default 0,
		cache_read_tokens integer not null default 0,
		cache_creation_tokens integer not null default 0,
		long_input_tokens integer not null default 0,
		long_output_tokens integer not null default 0,
		long_cached_tokens integer not null default 0,
		long_cache_read_tokens integer not null default 0,
		long_cache_creation_tokens integer not null default 0,
		total_tokens integer not null default 0,
		first_seen_ms integer not null,
		last_seen_ms integer not null,
		updated_at_ms integer not null,
		primary key (account_key, model, billing_model, service_tier)
	)`

	createUsagePricingAccountRollupsTable = `create table if not exists usage_pricing_account_rollups_v1 (
		structure_revision text not null,
		account_key text not null,
		account_snapshot text,
		auth_label_snapshot text,
		auth_provider_snapshot text,
		auth_index text,
		source text,
		source_hash text,
		model text not null,
		billing_model text not null,
		pricing_model text not null,
		service_tier text not null,
		context_threshold_tokens integer not null,
		calls integer not null default 0,
		success_calls integer not null default 0,
		failure_calls integer not null default 0,
		input_tokens integer not null default 0,
		output_tokens integer not null default 0,
		reasoning_tokens integer not null default 0,
		cached_tokens integer not null default 0,
		cache_read_tokens integer not null default 0,
		cache_creation_tokens integer not null default 0,
		long_input_tokens integer not null default 0,
		long_output_tokens integer not null default 0,
		long_cached_tokens integer not null default 0,
		long_cache_read_tokens integer not null default 0,
		long_cache_creation_tokens integer not null default 0,
		total_tokens integer not null default 0,
		first_seen_ms integer not null,
		last_seen_ms integer not null,
		updated_at_ms integer not null,
		primary key (
			structure_revision, account_key, model, billing_model, pricing_model,
			service_tier, context_threshold_tokens
		)
		)`

	createUsageDashboardHourlyRollupsTable = `create table if not exists usage_dashboard_hourly_rollups (
		bucket_ms integer not null,
		model text not null,
		billing_model text not null,
		service_tier text not null,
		calls integer not null default 0,
		success_calls integer not null default 0,
		failure_calls integer not null default 0,
		input_tokens integer not null default 0,
		output_tokens integer not null default 0,
		reasoning_tokens integer not null default 0,
		cached_tokens integer not null default 0,
		cache_read_tokens integer not null default 0,
		cache_creation_tokens integer not null default 0,
		long_input_tokens integer not null default 0,
		long_output_tokens integer not null default 0,
		long_cached_tokens integer not null default 0,
		long_cache_read_tokens integer not null default 0,
		long_cache_creation_tokens integer not null default 0,
		total_tokens integer not null default 0,
		latency_sum_ms integer not null default 0,
		latency_samples integer not null default 0,
		zero_token_calls integer not null default 0,
		updated_at_ms integer not null,
		primary key (bucket_ms, model, billing_model, service_tier)
	)`

	createUsageHourlyAggregateTable = `create table if not exists usage_hourly_aggregate_v1 (
		bucket_ms integer not null,
		model text not null,
		billing_model text not null,
		service_tier text not null,
		failed integer not null,
		calls integer not null default 0,
		input_tokens integer not null default 0,
		output_tokens integer not null default 0,
		reasoning_tokens integer not null default 0,
		cached_tokens integer not null default 0,
		cache_read_tokens integer not null default 0,
		cache_creation_tokens integer not null default 0,
		long_input_tokens integer not null default 0,
		long_output_tokens integer not null default 0,
		long_cached_tokens integer not null default 0,
		long_cache_read_tokens integer not null default 0,
		long_cache_creation_tokens integer not null default 0,
		total_tokens integer not null default 0,
		latency_sum_ms integer not null default 0,
		latency_samples integer not null default 0,
		zero_token_calls integer not null default 0,
		updated_at_ms integer not null,
		primary key (bucket_ms, model, billing_model, service_tier, failed)
	)`

	createUsageRollupRebuildStateTable = `create table if not exists usage_rollup_rebuild_state (
		name text primary key,
		target_event_id integer not null default 0,
		updated_at_ms integer not null default 0
	)`
)

func Migrate(db *sql.DB) error {
	monitoringSnapshot, err := inspectUsageMonitoringMigrationSnapshot(db)
	if err != nil {
		return err
	}
	usageHourlyAggregateSnapshot, err := inspectUsageHourlyAggregateMigrationSnapshot(db)
	if err != nil {
		return err
	}
	if err := validateUsageDerivedSchemaVersions(db, usageHourlyAggregateSnapshot); err != nil {
		return err
	}
	if err := ensureDerivedCleanupJobSchema(db); err != nil {
		return err
	}
	if err := ensureUsageDataMigrationColumns(db); err != nil {
		return err
	}
	if err := resetDamagedUsageMonitoringDerivations(db, monitoringSnapshot); err != nil {
		return err
	}
	if err := resetUsageDerivedDataWithoutSource(db, monitoringSnapshot); err != nil {
		return err
	}

	statements := []string{
		`pragma journal_mode = WAL`,
		`pragma synchronous = FULL`,
		`pragma busy_timeout = 5000`,
		`pragma foreign_keys = ON`,
		`create table if not exists usage_events (
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
			created_at_ms integer not null
		)`,
		`create table if not exists usage_rollup_checkpoints (
			name text primary key,
			last_event_id integer not null default 0,
			updated_at_ms integer not null,
			last_error text,
			last_run_started_at_ms integer,
			last_run_finished_at_ms integer
		)`,
		createUsageRollupRebuildStateTable,
		createUsageAccountModelRollupsTable,
		createUsageDashboardHourlyRollupsTable,
		createUsageHourlyAggregateTable,
		`create table if not exists usage_hourly_aggregate_state (
			aggregate_name text primary key,
			schema_version integer not null,
			structure_revision text not null default '',
			status text not null,
			backfill_last_event_id integer not null default 0,
			coverage_event_id integer not null default 0,
			target_event_id integer not null default 0,
			processed_events integer not null default 0,
			min_bucket_ms integer,
			max_bucket_ms integer,
			last_run_started_at_ms integer,
			updated_at_ms integer not null default 0,
			finished_at_ms integer,
			last_error text
		)`,
		`create table if not exists usage_pricing_hourly_rollups_v1 (
			structure_revision text not null,
			bucket_ms integer not null,
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
			long_input_tokens integer not null default 0,
			long_output_tokens integer not null default 0,
			long_cached_tokens integer not null default 0,
			long_cache_read_tokens integer not null default 0,
			long_cache_creation_tokens integer not null default 0,
			total_tokens integer not null default 0,
			latency_sum_ms integer not null default 0,
			latency_samples integer not null default 0,
			zero_token_calls integer not null default 0,
			updated_at_ms integer not null,
			primary key (
				structure_revision, bucket_ms, model, billing_model, pricing_model,
				service_tier, context_threshold_tokens, failed
			)
		)`,
		createUsagePricingAccountRollupsTable,
		`create table if not exists usage_pricing_rollup_state (
			rollup_name text primary key,
			schema_version integer not null,
			structure_revision text not null default '',
			status text not null,
			backfill_last_event_id integer not null default 0,
			coverage_event_id integer not null default 0,
			target_event_id integer not null default 0,
			processed_events integer not null default 0,
			min_bucket_ms integer,
			max_bucket_ms integer,
			last_run_started_at_ms integer,
			updated_at_ms integer not null default 0,
			finished_at_ms integer,
			last_error text
		)`,
		`insert or ignore into usage_pricing_rollup_state (
			rollup_name, schema_version, structure_revision, status,
			backfill_last_event_id, coverage_event_id, target_event_id,
			processed_events, updated_at_ms
		) values ('pricing_v1', 1, '', 'pending', 0, 0, 0, 0, 0)`,
		`create table if not exists usage_monitoring_account_daily_rollups_v1 (
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
			long_input_tokens integer not null default 0,
			long_output_tokens integer not null default 0,
			long_cached_tokens integer not null default 0,
			long_cache_read_tokens integer not null default 0,
			long_cache_creation_tokens integer not null default 0,
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
		`create table if not exists usage_monitoring_api_key_daily_rollups_v1 (
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
			long_input_tokens integer not null default 0,
			long_output_tokens integer not null default 0,
			long_cached_tokens integer not null default 0,
			long_cache_read_tokens integer not null default 0,
			long_cache_creation_tokens integer not null default 0,
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
				billing_model, pricing_model, service_tier,
				context_threshold_tokens, failed
			)
		)`,
		`create table if not exists usage_monitoring_selector_daily_rollups_v1 (
			model_format_revision text not null default '',
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
		`create table if not exists usage_monitoring_event_projection_v1 (
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
		`create table if not exists usage_monitoring_header_latest_v1 (
			snapshot_key text primary key,
			event_id integer not null,
			event_hash text not null,
			timestamp_ms integer not null,
			auth_file_snapshot text not null,
			auth_index text not null,
			account_snapshot text not null,
			auth_label_snapshot text not null,
			auth_provider_snapshot text not null,
			auth_project_id_snapshot text not null,
			source text not null,
			source_hash text not null,
			response_metadata_json text not null,
			header_quota_recover_at_ms integer,
			header_quota_used_percent real,
			header_quota_plan_type text not null,
			header_error_kind text not null,
			header_error_code text not null,
			header_trace_id text not null,
			updated_at_ms integer not null
		)`,
		`create table if not exists usage_monitoring_rollup_state (
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
		`insert or ignore into usage_monitoring_rollup_state (
			rollup_name, schema_version, status, target_event_id, updated_at_ms
		) select 'stats_v1', 1,
			case when exists (select 1 from usage_events limit 1) then 'pending' else 'ready' end,
			coalesce((select max(id) from usage_events), 0), 0`,
		`insert or ignore into usage_monitoring_rollup_state (
			rollup_name, schema_version, status, target_event_id, updated_at_ms
		) select 'metadata_v1', 1,
			case when exists (select 1 from usage_events limit 1) then 'pending' else 'ready' end,
			coalesce((select max(id) from usage_events), 0), 0`,
		`insert or ignore into usage_monitoring_rollup_state (
			rollup_name, schema_version, status, target_event_id, updated_at_ms
		) select 'projection_v1', 1,
			case when exists (select 1 from usage_events limit 1) then 'pending' else 'ready' end,
			coalesce((select max(id) from usage_events), 0), 0`,
		`create table if not exists usage_event_identity_ledger (
			event_hash text primary key,
			raw_event_id integer,
			timestamp_ms integer not null,
			bucket_ms integer not null,
			aggregate_schema_version integer not null default 0,
			aggregate_structure_revision text not null default '',
			first_seen_at_ms integer not null,
			updated_at_ms integer not null
		)`,
		`create table if not exists usage_archive_runs (
			id text primary key,
			mode text not null default 'manual',
			schema_version integer not null,
			format text not null,
			status text not null,
			resume_status text,
			requested_stage text,
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
		`create index if not exists idx_usage_archive_runs_status_updated
			on usage_archive_runs(status, updated_at_ms)`,
		`create table if not exists usage_archive_segments (
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
		`create index if not exists idx_usage_archive_segments_run_event
			on usage_archive_segments(run_id, last_event_id)`,
		`create table if not exists usage_archive_event_refs (
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
		`create index if not exists idx_usage_archive_event_refs_run_deleted
			on usage_archive_event_refs(run_id, raw_deleted_at_ms, raw_event_id)`,
		`create index if not exists idx_usage_archive_event_refs_timestamp_deleted
			on usage_archive_event_refs(timestamp_ms, raw_deleted_at_ms)`,
		`create table if not exists usage_maintenance_locks (
			name text primary key,
			run_id text not null,
			operation text not null,
			acquired_at_ms integer not null,
			updated_at_ms integer not null,
			foreign key (run_id) references usage_archive_runs(id) on delete cascade
		)`,
		`create table if not exists usage_data_migrations (
			name text primary key,
			status text not null,
			last_event_id integer not null default 0,
			target_event_id integer not null default 0,
			processed_rows integer not null default 0,
			changed_rows integer not null default 0,
			applied_rows integer not null default 0,
			started_at_ms integer,
			updated_at_ms integer not null default 0,
			finished_at_ms integer,
			last_error text
		)`,
		`insert or ignore into usage_data_migrations (
			name, status, last_event_id, target_event_id, processed_rows, updated_at_ms
		) select 'usage_cache_accounting_v1',
			case when exists (select 1 from usage_events limit 1) then 'discovering' else 'completed' end,
			0, 0, 0, 0`,
		`insert or ignore into usage_data_migrations (
			name, status, last_event_id, target_event_id, processed_rows, updated_at_ms
		) select 'usage_cache_accounting_v2',
			case when exists (select 1 from usage_events limit 1) then 'discovering' else 'completed' end,
			0, 0, 0, 0`,
		`create table if not exists usage_cache_accounting_v2_changes (
			event_id integer primary key,
			cache_input_mode text not null,
			normalized_uncached_input_tokens integer not null,
			normalized_total_input_tokens integer not null,
			normalized_cache_read_tokens integer not null,
			normalized_cache_creation_tokens integer not null,
			total_tokens integer not null
		)`,
		`create table if not exists dead_letter_events (
			id integer primary key autoincrement,
			payload text not null,
			error text not null,
			created_at_ms integer not null
		)`,
		`create table if not exists settings (
			key text primary key,
			value text not null,
			updated_at_ms integer not null
		)`,
		`create table if not exists model_prices (
			model text primary key,
			prompt_per_1m real not null,
			completion_per_1m real not null,
			cache_per_1m real not null,
			cache_read_per_1m real not null default 0,
			cache_creation_per_1m real not null default 0,
			prompt_configured integer not null default 0,
			completion_configured integer not null default 0,
			cache_read_configured integer not null default 0,
			cache_creation_configured integer not null default 0,
			source text,
			source_model_id text,
			raw_json text,
			updated_at_ms integer not null,
			synced_at_ms integer
		)`,
		`create table if not exists model_price_context_tiers (
			model text not null,
			threshold_tokens integer not null,
			prompt_per_1m real not null default 0,
			completion_per_1m real not null default 0,
			cache_per_1m real not null default 0,
			cache_read_per_1m real not null default 0,
			cache_creation_per_1m real not null default 0,
			prompt_configured integer not null default 0,
			completion_configured integer not null default 0,
			cache_configured integer not null default 0,
			cache_read_configured integer not null default 0,
			cache_creation_configured integer not null default 0,
			primary key (model, threshold_tokens),
			foreign key (model) references model_prices(model) on delete cascade
		)`,
		`create table if not exists model_price_service_tiers (
			model text not null,
			mode text not null,
			service_tier text not null,
			prompt_per_1m real not null default 0,
			completion_per_1m real not null default 0,
			cache_per_1m real not null default 0,
			cache_read_per_1m real not null default 0,
			cache_creation_per_1m real not null default 0,
			prompt_configured integer not null default 0,
			completion_configured integer not null default 0,
			cache_configured integer not null default 0,
			cache_read_configured integer not null default 0,
			cache_creation_configured integer not null default 0,
			primary key (model, mode, service_tier),
			foreign key (model) references model_prices(model) on delete cascade
		)`,
		`create table if not exists api_key_aliases (
			api_key_hash text primary key,
			alias text not null,
			updated_at_ms integer not null
		)`,
		`create table if not exists account_action_candidates (
			id integer primary key autoincrement,
			action_type text not null,
			status text not null,
			provider text,
			auth_file_name text not null,
			auth_index text,
			account_snapshot text,
			account_id_snapshot text,
			auth_label text,
			reason_code text,
			reason text,
			auto_disable_eligible integer not null default 0,
			auto_disabled_at_ms integer,
			evidence_json text,
			last_error text,
			first_seen_at_ms integer not null,
			last_seen_at_ms integer not null,
			hit_count integer not null default 1,
			created_at_ms integer not null,
			updated_at_ms integer not null
		)`,
		`create table if not exists codex_inspection_runs (
			id integer primary key autoincrement,
			trigger_type text not null,
			trigger_key text,
			status text not null,
			started_at_ms integer not null,
			finished_at_ms integer,
			total_files integer not null default 0,
			probe_set_count integer not null default 0,
			sampled_count integer not null default 0,
			disabled_count integer not null default 0,
			enabled_count integer not null default 0,
			delete_count integer not null default 0,
			disable_count integer not null default 0,
			enable_count integer not null default 0,
			reauth_count integer not null default 0,
			keep_count integer not null default 0,
			error text,
			settings_json text not null,
			created_at_ms integer not null,
			updated_at_ms integer not null
		)`,
		// A single row is the database-level fencing point for all Manager Server
		// instances sharing this database. run_id is nullable so an expired lease
		// can be claimed before the replacement run is inserted in the same tx.
		`create table if not exists codex_inspection_leases (
			id integer primary key check (id = 1),
			run_id integer,
			owner_id text not null,
			heartbeat_at_ms integer not null,
			lease_expires_at_ms integer not null,
			foreign key(run_id) references codex_inspection_runs(id) on delete set null
		)`,
		`create table if not exists codex_inspection_results (
			id integer primary key autoincrement,
			run_id integer not null,
			account_key text not null,
			file_name text not null,
			display_account text not null,
			account_snapshot text,
			auth_index text,
			account_id text,
			provider text,
			disabled integer not null default 0,
			status text,
			state text,
			action text not null,
			action_reason text,
			action_status text,
			executed_action text,
			action_error text,
			status_code integer,
			used_percent real,
			is_quota integer not null default 0,
			auto_recover_eligible integer not null default 0,
			error text,
			plan_type text,
			quota_windows_json text,
			error_kind text,
			error_detail text,
			created_at_ms integer not null,
			foreign key(run_id) references codex_inspection_runs(id) on delete cascade,
			unique(run_id, account_key)
		)`,
		`create table if not exists codex_inspection_logs (
			id integer primary key autoincrement,
			run_id integer not null,
			level text not null,
			message text not null,
			detail_json text,
			created_at_ms integer not null,
			foreign key(run_id) references codex_inspection_runs(id) on delete cascade
		)`,
		`create table if not exists codex_inspection_disable_ownership (
			file_name text not null,
			provider text not null default '',
			auth_index text not null default '',
			account_id text not null default '',
			account_snapshot text not null default '',
			disabled_at_ms integer not null,
			updated_at_ms integer not null,
			primary key (file_name, provider, auth_index, account_id, account_snapshot)
		)`,
		`create table if not exists quota_cooldowns (
			id integer primary key autoincrement,
			auth_file_name text not null,
			auth_index text,
			account_snapshot text,
			provider text,
			reason_code text,
			window_kind text,
			evidence_json text,
			recover_at_ms integer not null,
			owner text not null,
			event_hash text,
			pre_disabled_state integer not null default 0,
			status text not null,
			disabled_at_ms integer not null,
			recovered_at_ms integer,
			last_error text,
			created_at_ms integer not null,
			updated_at_ms integer not null
		)`,
		`create table if not exists account_quota_observations (
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
			lifecycle_applied integer not null default 1,
			created_at_ms integer not null
		)`,
		`create table if not exists account_quota_windows (
			id integer primary key autoincrement,
			account_key text not null,
			provider text not null,
			provider_window_id text not null,
			window_kind text not null,
			window_mode text not null,
			model_scope_kind text not null,
			model_scope_key text,
			model_ids_json text,
			scope_fingerprint text not null,
			inventory_scope_key text not null,
			relationship_kind text,
			container_provider_window_id text,
			availability text not null,
			generation integer not null default 1,
			absence_count integer not null default 0,
			first_seen_at_ms integer not null,
			last_seen_at_ms integer not null,
			missing_since_ms integer,
			deactivated_at_ms integer,
			last_observation_id integer,
			created_at_ms integer not null,
			updated_at_ms integer not null,
			unique(account_key, provider, provider_window_id, scope_fingerprint),
			foreign key(last_observation_id) references account_quota_observations(id)
		)`,
		`create table if not exists account_quota_window_activations (
			id integer primary key autoincrement,
			window_id integer not null,
			generation integer not null,
			status text not null,
			activated_at_ms integer not null,
			deactivated_at_ms integer,
			activation_accuracy text not null,
			deactivation_reason text,
			activate_observation_id integer,
			deactivate_observation_id integer,
			created_at_ms integer not null,
			updated_at_ms integer not null,
			unique(window_id, generation),
			foreign key(window_id) references account_quota_windows(id),
			foreign key(activate_observation_id) references account_quota_observations(id),
			foreign key(deactivate_observation_id) references account_quota_observations(id)
		)`,
		`create table if not exists account_quota_cycles (
			id integer primary key autoincrement,
			activation_id integer not null,
			provider_cycle_key text not null,
			state text not null,
			scheduled_start_ms integer,
			scheduled_end_ms integer,
			actual_start_ms integer not null,
			actual_end_ms integer,
			duration_seconds integer,
			boundary_accuracy text not null,
			end_reason text,
			first_observation_id integer,
			last_observation_id integer,
			parent_cycle_id integer,
			created_at_ms integer not null,
			updated_at_ms integer not null,
			unique(activation_id, provider_cycle_key),
			foreign key(activation_id) references account_quota_window_activations(id),
			foreign key(first_observation_id) references account_quota_observations(id),
			foreign key(last_observation_id) references account_quota_observations(id),
			foreign key(parent_cycle_id) references account_quota_cycles(id)
		)`,
		`create table if not exists account_quota_snapshots (
			id integer primary key autoincrement,
			observation_id integer,
			logical_window_id integer,
			activation_id integer,
			cycle_id integer,
			account_key text not null,
			provider text not null,
			provider_window_id text not null,
			window_kind text not null,
			window_mode text not null,
			model_scope_kind text not null,
			model_scope_key text,
			model_ids_json text,
			scope_fingerprint text not null default '',
			content_hash text not null default '',
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
		)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	if err := ensureUsageAccountModelRollupPrimaryKeys(db); err != nil {
		return err
	}
	if err := ensureUsageMonitoringProjectionIdentity(db); err != nil {
		return err
	}
	if err := ensureUsageMonitoringSearchIndex(db); err != nil {
		return err
	}
	if err := ensureUsageDataMigrationColumns(db); err != nil {
		return err
	}
	if err := ensureUsageArchiveRunColumns(db); err != nil {
		return err
	}
	if err := ensureUsageEventSnapshotColumns(db); err != nil {
		return err
	}
	if err := ensureCodexInspectionRunColumns(db); err != nil {
		return err
	}
	if err := ensureCodexInspectionResultColumns(db); err != nil {
		return err
	}
	if err := ensureCodexInspectionOwnershipColumns(db); err != nil {
		return err
	}
	if err := ensureAccountActionCandidateColumns(db); err != nil {
		return err
	}
	if err := ensureQuotaCooldownColumns(db); err != nil {
		return err
	}
	if err := ensureQuotaSnapshotLifecycleColumns(db); err != nil {
		return err
	}
	if err := ensureLegacyQuotaSnapshotMigrationState(db); err != nil {
		return err
	}
	if err := ensureUsageRollupLongContextColumns(db); err != nil {
		return err
	}
	if err := ensureUsageHourlyAggregateRevisionColumns(db); err != nil {
		return err
	}
	if err := ensureUsageHourlyAggregateSchemaVersion(db, usageHourlyAggregateSnapshot, monitoringSnapshot.sourceTableMissing()); err != nil {
		return err
	}
	if err := ensureAccountHistoryIdentityFormatVersion(db); err != nil {
		return err
	}
	if err := ensureDashboardHourlyRollupFormatVersion(db); err != nil {
		return err
	}
	return ensureModelPriceColumns(db)
}

func ensureLegacyQuotaSnapshotMigrationState(db *sql.DB) error {
	_, err := db.Exec(`insert or ignore into usage_data_migrations (
		name, status, last_event_id, target_event_id, processed_rows,
		changed_rows, updated_at_ms
	) values (?, 'pending', 0, 0, 0, 0, 0)`, quotasnapshotrepo.LegacySnapshotMigrationName)
	if err != nil {
		return fmt.Errorf("initialize legacy quota snapshot migration state: %w", err)
	}
	return nil
}

func validateUsageDerivedSchemaVersions(db *sql.DB, hourlySnapshot usageHourlyAggregateMigrationSnapshot) error {
	var settingsTableExists int
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = 'settings'`).Scan(&settingsTableExists); err != nil {
		return fmt.Errorf("inspect usage derived settings table: %w", err)
	}
	if settingsTableExists > 0 {
		var monitoringVersion string
		err := db.QueryRow(`select value from settings where key = ?`, usageMonitoringModelFormatVersionKey).Scan(&monitoringVersion)
		switch {
		case err == nil && monitoringVersion != usageidentity.ModelFormatVersion:
			return fmt.Errorf("unsupported usage monitoring model format version %q", monitoringVersion)
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("inspect usage monitoring model format version: %w", err)
		}
		var accountHistoryVersion string
		err = db.QueryRow(`select value from settings where key = ?`, accountHistoryIdentityFormatVersionKey).Scan(&accountHistoryVersion)
		switch {
		case err == nil && accountHistoryVersion != "1" && accountHistoryVersion != usageidentity.FormatVersion && accountHistoryVersion != usageidentity.AccountHistoryStructureRevision():
			return fmt.Errorf("unsupported account history identity format version %q", accountHistoryVersion)
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("inspect account history identity format version: %w", err)
		}
		var dashboardVersion string
		err = db.QueryRow(`select value from settings where key = ?`, dashboardHourlyRollupFormatVersionKey).Scan(&dashboardVersion)
		switch {
		case err == nil && dashboardVersion != "2" && dashboardVersion != dashboardHourlyRollupFormatVersion:
			return fmt.Errorf("unsupported dashboard hourly rollup format version %q", dashboardVersion)
		case err != nil && !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("inspect dashboard hourly rollup format version: %w", err)
		}
	}

	if hourlySnapshot.stateRowExists && hourlySnapshot.stateSchemaVersion != 1 && hourlySnapshot.stateSchemaVersion != 2 && hourlySnapshot.stateSchemaVersion != usageHourlyAggregateSchemaVersion {
		return fmt.Errorf("unsupported usage hourly aggregate schema version %d", hourlySnapshot.stateSchemaVersion)
	}
	return nil
}

func ensureUsageAccountModelRollupPrimaryKeys(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin usage account model rollup primary key migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	accountMatches, err := tablePrimaryKeyMatches(tx, usageAccountModelRollupsTable, []string{
		"account_key", "model", "billing_model", "service_tier",
	})
	if err != nil {
		return err
	}
	pricingMatches, err := tablePrimaryKeyMatches(tx, usagePricingAccountRollupsTable, []string{
		"structure_revision", "account_key", "model", "billing_model", "pricing_model",
		"service_tier", "context_threshold_tokens",
	})
	if err != nil {
		return err
	}
	if accountMatches && pricingMatches {
		return tx.Commit()
	}

	if !accountMatches {
		if err := parkDerivedTable(tx, usageAccountModelRollupsTable, usageAccountModelRollupsLegacy); err != nil {
			return err
		}
		for _, statement := range []string{
			createUsageAccountModelRollupsTable,
			`delete from usage_rollup_checkpoints where name = 'account_history'`,
		} {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("rebuild usage account model rollup primary key: %w", err)
			}
		}
		if err := scheduleUsageRollupRebuild(tx, "account_history"); err != nil {
			return fmt.Errorf("schedule usage account model rollup rebuild: %w", err)
		}
	}

	if !pricingMatches {
		if err := parkDerivedTable(tx, usagePricingAccountRollupsTable, usagePricingAccountLegacy); err != nil {
			return err
		}
		for _, statement := range []string{
			createUsagePricingAccountRollupsTable,
			`update usage_pricing_rollup_state set
				structure_revision = '',
				status = case when exists (select 1 from usage_events limit 1) then 'pending' else 'ready' end,
				backfill_last_event_id = 0,
				coverage_event_id = 0,
				target_event_id = coalesce((select max(id) from usage_events), 0),
				processed_events = 0,
				min_bucket_ms = null,
				max_bucket_ms = null,
				last_run_started_at_ms = null,
				updated_at_ms = 0,
				finished_at_ms = null,
				last_error = null
				where rollup_name = 'pricing_v1'`,
		} {
			if _, err := tx.Exec(statement); err != nil {
				return fmt.Errorf("rebuild usage pricing account rollup primary key: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit usage account model rollup primary key migration: %w", err)
	}
	return nil
}

func parkDerivedTable(tx *sql.Tx, tableName, legacyTableName string) error {
	var legacyExists int
	if err := tx.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`, legacyTableName).Scan(&legacyExists); err != nil {
		return fmt.Errorf("inspect legacy derived table %s: %w", legacyTableName, err)
	}
	if legacyExists != 0 {
		return fmt.Errorf("legacy derived table %s already exists", legacyTableName)
	}
	if _, err := tx.Exec(`alter table ` + tableName + ` rename to ` + legacyTableName); err != nil {
		return fmt.Errorf("park derived table %s as %s: %w", tableName, legacyTableName, err)
	}
	return nil
}

func tablePrimaryKeyMatches(tx *sql.Tx, tableName string, expected []string) (bool, error) {
	rows, err := tx.Query(`pragma table_info(` + tableName + `)`)
	if err != nil {
		return false, fmt.Errorf("inspect %s primary key: %w", tableName, err)
	}
	defer rows.Close()

	positions := make(map[int]string, len(expected))
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var primaryKeyPosition int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKeyPosition); err != nil {
			return false, fmt.Errorf("scan %s primary key: %w", tableName, err)
		}
		if primaryKeyPosition > 0 {
			positions[primaryKeyPosition] = name
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("inspect %s primary key: %w", tableName, err)
	}
	if len(positions) != len(expected) {
		return false, nil
	}
	for index, columnName := range expected {
		if positions[index+1] != columnName {
			return false, nil
		}
	}
	return true, nil
}

type usageMonitoringMigrationSnapshot struct {
	tables        map[string]bool
	rollupStates  map[string]bool
	latestEventID int64
}

func (snapshot usageMonitoringMigrationSnapshot) sourceTableMissing() bool {
	return !snapshot.tables["usage_events"]
}

func inspectUsageMonitoringMigrationSnapshot(db *sql.DB) (usageMonitoringMigrationSnapshot, error) {
	tableNames := []string{
		"usage_events",
		usageMonitoringAccountDailyTable,
		usageMonitoringAPIKeyDailyTable,
		usageMonitoringSelectorDailyTable,
		usageprojection.EventTable,
		usageprojection.SearchIndexTable,
		usageMonitoringHeaderLatestTable,
		usageMonitoringRollupStateTable,
		usageMonitoringSearchStateTable,
	}
	snapshot := usageMonitoringMigrationSnapshot{
		tables:       make(map[string]bool, len(tableNames)),
		rollupStates: make(map[string]bool, 3),
	}
	rows, err := db.Query(`select name from sqlite_master where type = 'table' and name in (
		'usage_events',
		'usage_monitoring_account_daily_rollups_v1',
		'usage_monitoring_api_key_daily_rollups_v1',
		'usage_monitoring_selector_daily_rollups_v1',
		'usage_monitoring_event_projection_v1',
		'usage_monitoring_event_search_v1',
		'usage_monitoring_header_latest_v1',
		'usage_monitoring_rollup_state',
		'usage_monitoring_search_index_state'
	)`)
	if err != nil {
		return usageMonitoringMigrationSnapshot{}, fmt.Errorf("inspect usage monitoring tables: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return usageMonitoringMigrationSnapshot{}, fmt.Errorf("scan usage monitoring table: %w", err)
		}
		snapshot.tables[name] = true
	}
	if err := rows.Close(); err != nil {
		return usageMonitoringMigrationSnapshot{}, fmt.Errorf("close usage monitoring table inspection: %w", err)
	}
	if err := rows.Err(); err != nil {
		return usageMonitoringMigrationSnapshot{}, fmt.Errorf("inspect usage monitoring tables: %w", err)
	}

	if snapshot.tables["usage_events"] {
		if err := db.QueryRow(`select coalesce(max(id), 0) from usage_events`).Scan(&snapshot.latestEventID); err != nil {
			return usageMonitoringMigrationSnapshot{}, fmt.Errorf("inspect latest usage event for monitoring recovery: %w", err)
		}
	}
	if !snapshot.tables[usageMonitoringRollupStateTable] {
		return snapshot, nil
	}
	stateRows, err := db.Query(`select rollup_name from usage_monitoring_rollup_state where rollup_name in (?, ?, ?)`,
		usageMonitoringStatsRollupName,
		usageMonitoringMetadataRollupName,
		usageMonitoringProjectionRollupName,
	)
	if err != nil {
		return usageMonitoringMigrationSnapshot{}, fmt.Errorf("inspect usage monitoring rollup states: %w", err)
	}
	for stateRows.Next() {
		var name string
		if err := stateRows.Scan(&name); err != nil {
			_ = stateRows.Close()
			return usageMonitoringMigrationSnapshot{}, fmt.Errorf("scan usage monitoring rollup state: %w", err)
		}
		snapshot.rollupStates[name] = true
	}
	if err := stateRows.Close(); err != nil {
		return usageMonitoringMigrationSnapshot{}, fmt.Errorf("close usage monitoring rollup state inspection: %w", err)
	}
	if err := stateRows.Err(); err != nil {
		return usageMonitoringMigrationSnapshot{}, fmt.Errorf("inspect usage monitoring rollup states: %w", err)
	}
	return snapshot, nil
}

func resetDamagedUsageMonitoringDerivations(db *sql.DB, snapshot usageMonitoringMigrationSnapshot) error {
	statsDamaged := snapshot.sourceTableMissing() ||
		!snapshot.rollupStates[usageMonitoringStatsRollupName] ||
		!snapshot.tables[usageMonitoringAccountDailyTable] ||
		!snapshot.tables[usageMonitoringAPIKeyDailyTable]
	metadataDamaged := snapshot.sourceTableMissing() ||
		!snapshot.rollupStates[usageMonitoringMetadataRollupName] ||
		!snapshot.tables[usageMonitoringSelectorDailyTable] ||
		!snapshot.tables[usageMonitoringHeaderLatestTable]
	projectionDamaged := snapshot.sourceTableMissing() ||
		!snapshot.rollupStates[usageMonitoringProjectionRollupName] ||
		!snapshot.tables[usageprojection.EventTable]
	if !statsDamaged && !metadataDamaged && !projectionDamaged {
		return nil
	}
	if snapshot.sourceTableMissing() {
		projectionDamaged = projectionDamaged || snapshot.tables[usageMonitoringHeaderLatestTable]
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin usage monitoring derivation recovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if !snapshot.tables[usageprojection.SearchIndexTable] {
		if err := dropUsageMonitoringSearchTriggers(tx); err != nil {
			return err
		}
	}
	if statsDamaged {
		for tableName, legacyName := range map[string]string{
			usageMonitoringAccountDailyTable: usageMonitoringAccountLegacy,
			usageMonitoringAPIKeyDailyTable:  usageMonitoringAPIKeyLegacy,
		} {
			if snapshot.tables[tableName] {
				if err := parkDerivedTable(tx, tableName, legacyName); err != nil {
					return err
				}
			}
		}
		if err := resetUsageMonitoringRollupState(tx, snapshot, usageMonitoringStatsRollupName); err != nil {
			return err
		}
	}
	if metadataDamaged {
		for tableName, legacyName := range map[string]string{
			usageMonitoringSelectorDailyTable: usageMonitoringSelectorLegacy,
			usageMonitoringHeaderLatestTable:  usageMonitoringHeaderLegacy,
		} {
			if snapshot.tables[tableName] {
				if err := parkDerivedTable(tx, tableName, legacyName); err != nil {
					return err
				}
			}
		}
		if err := resetUsageMonitoringRollupState(tx, snapshot, usageMonitoringMetadataRollupName); err != nil {
			return err
		}
	}
	if projectionDamaged {
		parkProjection := snapshot.tables[usageprojection.EventTable] &&
			(snapshot.sourceTableMissing() || !snapshot.rollupStates[usageMonitoringProjectionRollupName])
		searchParkedWithProjection := false
		if (!snapshot.tables[usageprojection.EventTable] || parkProjection) && snapshot.tables[usageprojection.SearchIndexTable] {
			if err := dropUsageMonitoringSearchTriggers(tx); err != nil {
				return err
			}
			if _, _, err := parkUsageMonitoringSearchGeneration(tx, parkProjection); err != nil {
				return err
			}
			searchParkedWithProjection = parkProjection
		}
		if snapshot.tables[usageprojection.EventTable] && !snapshot.tables[usageprojection.SearchIndexTable] {
			if err := dropUsageMonitoringSearchTriggers(tx); err != nil {
				return err
			}
		}
		if parkProjection && !searchParkedWithProjection {
			if err := parkDerivedTable(tx, usageprojection.EventTable, usageMonitoringProjectionLegacy); err != nil {
				return err
			}
		}
		if err := resetUsageMonitoringRollupState(tx, snapshot, usageMonitoringProjectionRollupName); err != nil {
			return err
		}
		if snapshot.tables[usageMonitoringSearchStateTable] {
			if _, err := tx.Exec(`update usage_monitoring_search_index_state set ready = 0, updated_at_ms = 0 where id = 1`); err != nil {
				return fmt.Errorf("mark usage monitoring search index for recovery: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit usage monitoring derivation recovery: %w", err)
	}
	return nil
}

func dropUsageMonitoringSearchTriggers(tx *sql.Tx) error {
	for _, triggerName := range []string{
		"usage_monitoring_event_search_v1_insert",
		"usage_monitoring_event_search_v1_update",
		"usage_monitoring_event_search_v1_delete",
	} {
		if _, err := tx.Exec(`drop trigger if exists ` + triggerName); err != nil {
			return fmt.Errorf("drop stale usage monitoring search trigger %s: %w", triggerName, err)
		}
	}
	return nil
}

func resetUsageMonitoringRollupState(tx *sql.Tx, snapshot usageMonitoringMigrationSnapshot, rollupName string) error {
	if !snapshot.rollupStates[rollupName] {
		return nil
	}
	status := "pending"
	if snapshot.latestEventID == 0 {
		status = "ready"
	}
	if _, err := tx.Exec(`update usage_monitoring_rollup_state set
		status = ?, backfill_last_event_id = 0, coverage_event_id = 0,
		target_event_id = ?, processed_events = 0,
		last_run_started_at_ms = null, updated_at_ms = 0,
		finished_at_ms = null, last_error = null
		where rollup_name = ?`, status, snapshot.latestEventID, rollupName); err != nil {
		return fmt.Errorf("reset usage monitoring rollup state %s: %w", rollupName, err)
	}
	return nil
}

func resetUsageDerivedDataWithoutSource(db *sql.DB, snapshot usageMonitoringMigrationSnapshot) error {
	if !snapshot.sourceTableMissing() {
		return nil
	}
	existingTables := []string{
		"usage_account_model_rollups",
		"usage_dashboard_hourly_rollups",
		"usage_pricing_hourly_rollups_v1",
		"usage_pricing_account_rollups_v1",
		"usage_cache_accounting_v2_changes",
		"usage_rollup_checkpoints",
		"usage_rollup_rebuild_state",
		"usage_pricing_rollup_state",
		"usage_data_migrations",
	}
	placeholders := make([]string, len(existingTables))
	args := make([]any, len(existingTables))
	for index, tableName := range existingTables {
		placeholders[index] = "?"
		args[index] = tableName
	}
	rows, err := db.Query(`select name from sqlite_master where type = 'table' and name in (`+
		strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return fmt.Errorf("inspect usage derived tables without source: %w", err)
	}
	present := make(map[string]bool, len(existingTables))
	for rows.Next() {
		var tableName string
		if err := rows.Scan(&tableName); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan usage derived table without source: %w", err)
		}
		present[tableName] = true
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close usage derived table inspection without source: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect usage derived tables without source: %w", err)
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin usage source recovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for tableName, legacyName := range map[string]string{
		usageAccountModelRollupsTable:       usageAccountModelSourceLegacy,
		"usage_dashboard_hourly_rollups":    usageDashboardHourlySourceLegacy,
		"usage_pricing_hourly_rollups_v1":   usagePricingHourlySourceLegacy,
		usagePricingAccountRollupsTable:     usagePricingAccountSourceLegacy,
		"usage_cache_accounting_v2_changes": usageCacheChangesSourceLegacy,
	} {
		if present[tableName] {
			if err := parkDerivedTable(tx, tableName, legacyName); err != nil {
				return err
			}
		}
	}
	statements := make([]string, 0, len(existingTables))
	if present["usage_rollup_checkpoints"] {
		statements = append(statements, `delete from usage_rollup_checkpoints
			where name in ('account_history', 'dashboard_hourly')`)
	}
	if present["usage_rollup_rebuild_state"] {
		statements = append(statements, `delete from usage_rollup_rebuild_state
			where name in ('account_history', 'dashboard_hourly')`)
	}
	if present["usage_pricing_rollup_state"] {
		statements = append(statements, `update usage_pricing_rollup_state set
			status = 'ready', backfill_last_event_id = 0, coverage_event_id = 0,
			target_event_id = 0, processed_events = 0,
			min_bucket_ms = null, max_bucket_ms = null,
			last_run_started_at_ms = null, updated_at_ms = 0,
			finished_at_ms = null, last_error = null
			where rollup_name = 'pricing_v1'`)
	}
	if present["usage_data_migrations"] {
		statements = append(statements, `update usage_data_migrations set
				status = 'completed', last_event_id = 0, target_event_id = 0,
				processed_rows = 0, changed_rows = 0, applied_rows = 0,
				started_at_ms = null, updated_at_ms = 0,
			finished_at_ms = null, last_error = null
			where name in ('usage_cache_accounting_v1', 'usage_cache_accounting_v2')`)
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("reset usage derived data without source: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit usage source recovery: %w", err)
	}
	return nil
}

func ensureUsageMonitoringProjectionIdentity(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin usage monitoring projection identity migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var version string
	versionErr := tx.QueryRow(`select value from settings where key = ?`, usageMonitoringModelFormatVersionKey).Scan(&version)
	switch {
	case versionErr == nil && version != usageidentity.ModelFormatVersion:
		return fmt.Errorf("unsupported usage monitoring model format version %q", version)
	case versionErr != nil && !errors.Is(versionErr, sql.ErrNoRows):
		return versionErr
	}

	rows, err := tx.Query(`pragma table_info(` + usageprojection.EventTable + `)`)
	if err != nil {
		return fmt.Errorf("inspect usage monitoring projection columns: %w", err)
	}
	hasAccountKey := false
	hasRequestedModel := false
	hasAnalyticsModel := false
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan usage monitoring projection columns: %w", err)
		}
		if name == "account_key" {
			hasAccountKey = true
		}
		if name == "requested_model" {
			hasRequestedModel = true
		}
		if name == "analytics_model" {
			hasAnalyticsModel = true
		}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close usage monitoring projection column inspection: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("inspect usage monitoring projection columns: %w", err)
	}
	selectorHasRevision, err := tableHasColumn(tx, usageMonitoringSelectorDailyTable, "model_format_revision")
	if err != nil {
		return err
	}

	if !hasAccountKey {
		if _, err := tx.Exec(`alter table ` + usageprojection.EventTable + ` add column account_key text not null default ''`); err != nil {
			return fmt.Errorf("add usage monitoring projection account key: %w", err)
		}
	}
	if !hasRequestedModel {
		if _, err := tx.Exec(`alter table ` + usageprojection.EventTable + ` add column requested_model text not null default ''`); err != nil {
			return fmt.Errorf("add usage monitoring projection requested model: %w", err)
		}
	}
	if !hasAnalyticsModel {
		if _, err := tx.Exec(`alter table ` + usageprojection.EventTable + ` add column analytics_model text not null default ''`); err != nil {
			return fmt.Errorf("add usage monitoring projection analytics model: %w", err)
		}
	}
	if !selectorHasRevision {
		if _, err := tx.Exec(`alter table ` + usageMonitoringSelectorDailyTable + ` add column model_format_revision text not null default ''`); err != nil {
			return fmt.Errorf("add usage monitoring selector model format revision: %w", err)
		}
	}

	needsRebuild := versionErr != nil || !hasAccountKey || !hasRequestedModel || !hasAnalyticsModel || !selectorHasRevision
	if needsRebuild {
		if err := dropUsageMonitoringSearchTriggers(tx); err != nil {
			return err
		}
		var searchIndexExists int
		if err := tx.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`, usageprojection.SearchIndexTable).Scan(&searchIndexExists); err != nil {
			return fmt.Errorf("inspect usage monitoring search index for model rebuild: %w", err)
		}
		projectionHasRows, err := tableHasRows(tx, usageprojection.EventTable)
		if err != nil {
			return err
		}
		var projectionCreateSQL string
		if projectionHasRows {
			if err := tx.QueryRow(`select sql from sqlite_master where type = 'table' and name = ?`, usageprojection.EventTable).Scan(&projectionCreateSQL); err != nil {
				return fmt.Errorf("read usage monitoring projection schema: %w", err)
			}
		}
		projectionParkedWithSearch := false
		if searchIndexExists != 0 {
			if _, _, err := parkUsageMonitoringSearchGeneration(tx, projectionHasRows); err != nil {
				return err
			}
			projectionParkedWithSearch = projectionHasRows
		}
		for tableName, legacyName := range map[string]string{
			usageMonitoringAccountDailyTable:  usageMonitoringAccountLegacy,
			usageMonitoringAPIKeyDailyTable:   usageMonitoringAPIKeyLegacy,
			usageMonitoringSelectorDailyTable: usageMonitoringSelectorLegacy,
			usageMonitoringHeaderLatestTable:  usageMonitoringHeaderLegacy,
		} {
			hasRows, err := tableHasRows(tx, tableName)
			if err != nil {
				return err
			}
			if !hasRows {
				continue
			}
			if err := parkAndRecreateDerivedTable(tx, tableName, legacyName); err != nil {
				return err
			}
		}
		if projectionHasRows {
			if projectionParkedWithSearch {
				if _, err := tx.Exec(projectionCreateSQL); err != nil {
					return fmt.Errorf("recreate usage monitoring projection: %w", err)
				}
			} else if err := parkAndRecreateDerivedTable(tx, usageprojection.EventTable, usageMonitoringProjectionLegacy); err != nil {
				return err
			}
		}
		var latestEventID int64
		if err := tx.QueryRow(`select coalesce(max(id), 0) from usage_events`).Scan(&latestEventID); err != nil {
			return fmt.Errorf("read latest event for usage monitoring model rebuild: %w", err)
		}
		status := "pending"
		if latestEventID == 0 {
			status = "ready"
		}
		if _, err := tx.Exec(`update usage_monitoring_rollup_state set
			structure_revision = '', status = ?, backfill_last_event_id = 0,
			coverage_event_id = 0, target_event_id = ?, processed_events = 0,
			last_run_started_at_ms = null, updated_at_ms = 0,
			finished_at_ms = null, last_error = null
			where rollup_name = ?`,
			status,
			latestEventID,
			usageMonitoringStatsRollupName,
		); err != nil {
			return fmt.Errorf("reset usage monitoring stats state: %w", err)
		}
		if _, err := tx.Exec(`update usage_monitoring_rollup_state set
			structure_revision = ?, status = ?, backfill_last_event_id = 0,
			coverage_event_id = 0, target_event_id = ?, processed_events = 0,
			last_run_started_at_ms = null, updated_at_ms = 0,
			finished_at_ms = null, last_error = null
			where rollup_name in (?, ?)`,
			usageidentity.ModelFormatVersion,
			status,
			latestEventID,
			usageMonitoringMetadataRollupName,
			usageMonitoringProjectionRollupName,
		); err != nil {
			return fmt.Errorf("reset usage monitoring model derivation state: %w", err)
		}
		var searchStateExists int
		if err := tx.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`, usageMonitoringSearchStateTable).Scan(&searchStateExists); err != nil {
			return fmt.Errorf("inspect usage monitoring model search state: %w", err)
		}
		if searchStateExists > 0 {
			if _, err := tx.Exec(`update usage_monitoring_search_index_state set ready = 0, updated_at_ms = 0 where id = 1`); err != nil {
				return fmt.Errorf("reset usage monitoring model search state: %w", err)
			}
		}
	}

	if _, err := tx.Exec(`insert into settings (key, value, updated_at_ms) values (?, ?, ?)
		on conflict(key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		usageMonitoringModelFormatVersionKey,
		usageidentity.ModelFormatVersion,
		time.Now().UnixMilli(),
	); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit usage monitoring projection identity migration: %w", err)
	}
	return nil
}

func parkAndRecreateDerivedTable(tx *sql.Tx, tableName, legacyTableName string) error {
	var createSQL string
	if err := tx.QueryRow(`select sql from sqlite_master where type = 'table' and name = ?`, tableName).Scan(&createSQL); err != nil {
		return fmt.Errorf("read derived table schema %s: %w", tableName, err)
	}
	if strings.TrimSpace(createSQL) == "" {
		return fmt.Errorf("derived table %s has no reusable schema", tableName)
	}
	if err := parkDerivedTable(tx, tableName, legacyTableName); err != nil {
		return err
	}
	if _, err := tx.Exec(createSQL); err != nil {
		return fmt.Errorf("recreate derived table %s: %w", tableName, err)
	}
	var cursorTableExists int
	if err := tx.QueryRow(`select count(*) from sqlite_master where type = 'table'
		and name = 'usage_derived_cleanup_cursors'`).Scan(&cursorTableExists); err != nil {
		return fmt.Errorf("inspect derived cleanup cursor schema: %w", err)
	}
	if cursorTableExists > 0 {
		if _, err := tx.Exec(`delete from usage_derived_cleanup_cursors where table_name = ?`, tableName); err != nil {
			return fmt.Errorf("reset derived cleanup cursor for %s: %w", tableName, err)
		}
	}
	return nil
}

func parkUsageMonitoringSearchGeneration(tx *sql.Tx, pairProjection bool) (string, string, error) {
	var generation int64
	if err := tx.QueryRow(`select coalesce(max(generation), 0) + 1 from usage_derived_cleanup_jobs`).Scan(&generation); err != nil {
		return "", "", fmt.Errorf("allocate usage monitoring cleanup generation: %w", err)
	}
	ftsTable := fmt.Sprintf("%s%06d", usageMonitoringSearchLegacyPrefix, generation)
	projectionTable := ""
	if pairProjection {
		projectionTable = fmt.Sprintf("%s%06d", usageMonitoringProjectionLegacyPrefix, generation)
	}
	if err := parkDerivedTable(tx, usageprojection.SearchIndexTable, ftsTable); err != nil {
		return "", "", err
	}
	status := "offline_required"
	if projectionTable != "" {
		if err := parkDerivedTable(tx, usageprojection.EventTable, projectionTable); err != nil {
			return "", "", err
		}
		status = "online_cleanup"
	}
	nowMS := time.Now().UnixMilli()
	if _, err := tx.Exec(`insert into usage_derived_cleanup_jobs (
		generation, kind, status, projection_table, fts_table,
		processed_rows, created_at_ms, updated_at_ms
	) values (?, 'monitoring_fts', ?, ?, ?, 0, ?, ?)`,
		generation,
		status,
		nullableString(projectionTable),
		ftsTable,
		nowMS,
		nowMS,
	); err != nil {
		return "", "", fmt.Errorf("record usage monitoring cleanup generation %d: %w", generation, err)
	}
	return ftsTable, projectionTable, nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func tableHasColumn(tx *sql.Tx, tableName, columnName string) (bool, error) {
	rows, err := tx.Query(`pragma table_info(` + tableName + `)`)
	if err != nil {
		return false, fmt.Errorf("inspect %s columns: %w", tableName, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, fmt.Errorf("scan %s columns: %w", tableName, err)
		}
		if name == columnName {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("inspect %s columns: %w", tableName, err)
	}
	return false, nil
}

func ensureUsageMonitoringSearchIndex(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var indexExists int
	if err := tx.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`, usageprojection.SearchIndexTable).Scan(&indexExists); err != nil {
		return fmt.Errorf("inspect usage monitoring search index: %w", err)
	}
	createStatements := []string{
		fmt.Sprintf(`create virtual table if not exists %s using fts5(
			search_text,
			content = '%s',
			content_rowid = 'event_id',
			columnsize = 0,
			detail = 'none',
			tokenize = 'trigram'
		)`, usageprojection.SearchIndexTable, usageprojection.EventTable),
		`create table if not exists usage_monitoring_search_index_state (
			id integer primary key check (id = 1),
			ready integer not null default 0,
			updated_at_ms integer not null default 0
		)`,
		`insert or ignore into usage_monitoring_search_index_state (id) values (1)`,
		fmt.Sprintf(`create trigger if not exists usage_monitoring_event_search_v1_insert
			after insert on %s begin
			insert into %s(rowid, search_text) values (new.event_id, new.search_text);
		end`, usageprojection.EventTable, usageprojection.SearchIndexTable),
		fmt.Sprintf(`create trigger if not exists usage_monitoring_event_search_v1_update
			after update of search_text on %s begin
			insert into %s(%s, rowid, search_text) values ('delete', old.event_id, old.search_text);
			insert into %s(rowid, search_text) values (new.event_id, new.search_text);
		end`, usageprojection.EventTable, usageprojection.SearchIndexTable, usageprojection.SearchIndexTable, usageprojection.SearchIndexTable),
		fmt.Sprintf(`create trigger if not exists usage_monitoring_event_search_v1_delete
			after delete on %s begin
			insert into %s(%s, rowid, search_text) values ('delete', old.event_id, old.search_text);
		end`, usageprojection.EventTable, usageprojection.SearchIndexTable, usageprojection.SearchIndexTable),
	}
	for _, statement := range createStatements {
		if _, err := tx.Exec(statement); err != nil {
			return fmt.Errorf("create usage monitoring search index: %w", err)
		}
	}
	if indexExists == 0 {
		if _, err := tx.Exec(`update usage_monitoring_search_index_state set ready = 0, updated_at_ms = 0 where id = 1`); err != nil {
			return fmt.Errorf("mark recreated usage monitoring search index pending: %w", err)
		}
		var latestEventID int64
		if err := tx.QueryRow(`select coalesce(max(id), 0) from usage_events`).Scan(&latestEventID); err != nil {
			return fmt.Errorf("read latest event for usage monitoring search catch-up: %w", err)
		}
		status := "pending"
		if latestEventID == 0 {
			status = "ready"
		}
		if _, err := tx.Exec(`update usage_monitoring_rollup_state set
			structure_revision = ?, status = ?, backfill_last_event_id = 0,
			coverage_event_id = 0, target_event_id = ?, processed_events = 0,
			last_run_started_at_ms = null, updated_at_ms = 0,
			finished_at_ms = null, last_error = null
			where rollup_name = ?`,
			usageidentity.ModelFormatVersion,
			status,
			latestEventID,
			usageMonitoringProjectionRollupName,
		); err != nil {
			return fmt.Errorf("schedule usage monitoring search catch-up: %w", err)
		}
	}
	return tx.Commit()
}

func ensureAccountHistoryIdentityFormatVersion(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var version string
	err = tx.QueryRow(`select value from settings where key = ?`, accountHistoryIdentityFormatVersionKey).Scan(&version)
	switch {
	case err == nil && version == usageidentity.AccountHistoryStructureRevision():
		return tx.Commit()
	case err == nil && version != "1" && version != usageidentity.FormatVersion:
		return fmt.Errorf("unsupported account history identity format version %q", version)
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}

	hasRows, err := tableHasRows(tx, usageAccountModelRollupsTable)
	if err != nil {
		return err
	}
	if hasRows {
		if err := parkDerivedTable(tx, usageAccountModelRollupsTable, usageAccountModelRollupsLegacy); err != nil {
			return err
		}
		if _, err := tx.Exec(createUsageAccountModelRollupsTable); err != nil {
			return fmt.Errorf("create current account history rollup: %w", err)
		}
	}
	if _, err := tx.Exec(`delete from usage_rollup_checkpoints where name = 'account_history'`); err != nil {
		return err
	}
	if err := scheduleUsageRollupRebuild(tx, "account_history"); err != nil {
		return err
	}
	if _, err := tx.Exec(`insert into settings (key, value, updated_at_ms) values (?, ?, ?)
		on conflict(key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		accountHistoryIdentityFormatVersionKey,
		usageidentity.AccountHistoryStructureRevision(),
		time.Now().UnixMilli(),
	); err != nil {
		return err
	}
	return tx.Commit()
}

func ensureDashboardHourlyRollupFormatVersion(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var version string
	err = tx.QueryRow(`select value from settings where key = ?`, dashboardHourlyRollupFormatVersionKey).Scan(&version)
	switch {
	case err == nil && version == dashboardHourlyRollupFormatVersion:
		return tx.Commit()
	case err == nil && version != "2":
		return fmt.Errorf("unsupported dashboard hourly rollup format version %q", version)
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}

	hasRows, err := tableHasRows(tx, "usage_dashboard_hourly_rollups")
	if err != nil {
		return err
	}
	if hasRows {
		if err := parkDerivedTable(tx, "usage_dashboard_hourly_rollups", usageDashboardHourlyLegacy); err != nil {
			return err
		}
		if _, err := tx.Exec(createUsageDashboardHourlyRollupsTable); err != nil {
			return fmt.Errorf("create current dashboard hourly rollup: %w", err)
		}
	}
	if _, err := tx.Exec(`delete from usage_rollup_checkpoints where name = 'dashboard_hourly'`); err != nil {
		return err
	}
	if err := scheduleUsageRollupRebuild(tx, "dashboard_hourly"); err != nil {
		return err
	}
	if _, err := tx.Exec(`insert into settings (key, value, updated_at_ms) values (?, ?, ?)
		on conflict(key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		dashboardHourlyRollupFormatVersionKey,
		dashboardHourlyRollupFormatVersion,
		time.Now().UnixMilli(),
	); err != nil {
		return err
	}
	return tx.Commit()
}

func tableHasRows(tx *sql.Tx, tableName string) (bool, error) {
	var value int
	err := tx.QueryRow(`select 1 from ` + tableName + ` limit 1`).Scan(&value)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, fmt.Errorf("inspect derived table %s rows: %w", tableName, err)
	}
}

func scheduleUsageRollupRebuild(tx *sql.Tx, name string) error {
	if _, err := tx.Exec(createUsageRollupRebuildStateTable); err != nil {
		return err
	}
	var sourceTableExists int
	if err := tx.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = 'usage_events'`).Scan(&sourceTableExists); err != nil {
		return err
	}
	if sourceTableExists == 0 {
		_, err := tx.Exec(`delete from usage_rollup_rebuild_state where name = ?`, name)
		return err
	}
	var targetEventID int64
	if err := tx.QueryRow(`select coalesce(max(id), 0) from usage_events`).Scan(&targetEventID); err != nil {
		return err
	}
	if targetEventID == 0 {
		_, err := tx.Exec(`delete from usage_rollup_rebuild_state where name = ?`, name)
		return err
	}
	_, err := tx.Exec(`insert into usage_rollup_rebuild_state (name, target_event_id, updated_at_ms)
		values (?, ?, 0)
		on conflict(name) do update set
			target_event_id = excluded.target_event_id,
			updated_at_ms = excluded.updated_at_ms`, name, targetEventID)
	return err
}

type usageHourlyAggregateMigrationSnapshot struct {
	aggregateTableExists bool
	stateTableExists     bool
	stateRowExists       bool
	stateSchemaVersion   int
	ledgerTableExists    bool
}

func inspectUsageHourlyAggregateMigrationSnapshot(db *sql.DB) (usageHourlyAggregateMigrationSnapshot, error) {
	var snapshot usageHourlyAggregateMigrationSnapshot
	rows, err := db.Query(`select name from sqlite_master where type = 'table' and name in (?, ?, ?)`,
		usageHourlyAggregateTable,
		usageHourlyAggregateState,
		usageEventIdentityLedger,
	)
	if err != nil {
		return usageHourlyAggregateMigrationSnapshot{}, fmt.Errorf("inspect usage hourly aggregate tables: %w", err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			_ = rows.Close()
			return usageHourlyAggregateMigrationSnapshot{}, fmt.Errorf("scan usage hourly aggregate table: %w", err)
		}
		switch name {
		case usageHourlyAggregateTable:
			snapshot.aggregateTableExists = true
		case usageHourlyAggregateState:
			snapshot.stateTableExists = true
		case usageEventIdentityLedger:
			snapshot.ledgerTableExists = true
		}
	}
	if err := rows.Close(); err != nil {
		return usageHourlyAggregateMigrationSnapshot{}, fmt.Errorf("close usage hourly aggregate table inspection: %w", err)
	}
	if err := rows.Err(); err != nil {
		return usageHourlyAggregateMigrationSnapshot{}, fmt.Errorf("inspect usage hourly aggregate tables: %w", err)
	}
	if snapshot.stateTableExists {
		err := db.QueryRow(`select schema_version from usage_hourly_aggregate_state
			where aggregate_name = 'hourly_core'`).Scan(&snapshot.stateSchemaVersion)
		switch {
		case err == nil:
			snapshot.stateRowExists = true
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return usageHourlyAggregateMigrationSnapshot{}, fmt.Errorf("inspect usage hourly aggregate state: %w", err)
		}
	}
	return snapshot, nil
}

func ensureUsageHourlyAggregateRevisionColumns(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, column := range []struct {
		tableName string
		name      string
		ddl       string
	}{
		{usageHourlyAggregateState, "structure_revision", "structure_revision text not null default ''"},
		{usageEventIdentityLedger, "aggregate_structure_revision", "aggregate_structure_revision text not null default ''"},
	} {
		hasColumn, err := tableHasColumn(tx, column.tableName, column.name)
		if err != nil {
			return err
		}
		if hasColumn {
			continue
		}
		if _, err := tx.Exec(`alter table ` + column.tableName + ` add column ` + column.ddl); err != nil {
			return fmt.Errorf("add %s.%s: %w", column.tableName, column.name, err)
		}
	}
	return tx.Commit()
}

func ensureUsageHourlyAggregateSchemaVersion(
	db *sql.DB,
	snapshot usageHourlyAggregateMigrationSnapshot,
	sourceTableMissing bool,
) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var version int
	var currentRevision string
	err = tx.QueryRow(`select schema_version, structure_revision from usage_hourly_aggregate_state where aggregate_name = 'hourly_core'`).Scan(&version, &currentRevision)
	damaged := sourceTableMissing && snapshot.aggregateTableExists ||
		snapshot.aggregateTableExists != snapshot.ledgerTableExists ||
		(snapshot.stateRowExists && !snapshot.aggregateTableExists) ||
		(snapshot.aggregateTableExists && !snapshot.stateRowExists)
	switch {
	case err == nil && version == usageHourlyAggregateSchemaVersion && currentRevision != "" && !damaged:
		return tx.Commit()
	case err == nil && version != 1 && version != 2 && version != usageHourlyAggregateSchemaVersion:
		return fmt.Errorf("unsupported usage hourly aggregate schema version %d", version)
	case err != nil && !errors.Is(err, sql.ErrNoRows):
		return err
	}

	var latestEventID int64
	if err := tx.QueryRow(`select coalesce(max(id), 0) from usage_events`).Scan(&latestEventID); err != nil {
		return err
	}
	status := "pending"
	if latestEventID == 0 {
		status = "ready"
	}
	revision := usageHourlyAggregateStructureRevision
	if damaged || version == usageHourlyAggregateSchemaVersion {
		revision, err = newUsageHourlyAggregateRebuildRevision()
		if err != nil {
			return err
		}
	}
	hasRows, err := tableHasRows(tx, usageHourlyAggregateTable)
	if err != nil {
		return err
	}
	if hasRows {
		if err := parkDerivedTable(tx, usageHourlyAggregateTable, usageHourlyAggregateLegacy); err != nil {
			return err
		}
		if _, err := tx.Exec(createUsageHourlyAggregateTable); err != nil {
			return fmt.Errorf("create current usage hourly aggregate: %w", err)
		}
	}
	if _, err := tx.Exec(`insert into usage_hourly_aggregate_state (
		aggregate_name, schema_version, structure_revision, status, backfill_last_event_id,
		coverage_event_id, target_event_id, processed_events, updated_at_ms
	) values ('hourly_core', ?, ?, ?, 0, 0, ?, 0, 0)
	on conflict(aggregate_name) do update set
		schema_version = excluded.schema_version,
		structure_revision = excluded.structure_revision,
		status = excluded.status,
		backfill_last_event_id = 0,
		coverage_event_id = 0,
		target_event_id = excluded.target_event_id,
		processed_events = 0,
		min_bucket_ms = null,
		max_bucket_ms = null,
		last_run_started_at_ms = null,
		updated_at_ms = 0,
		finished_at_ms = null,
		last_error = null`, usageHourlyAggregateSchemaVersion, revision, status, latestEventID); err != nil {
		return err
	}
	return tx.Commit()
}

func newUsageHourlyAggregateRebuildRevision() (string, error) {
	var suffix [16]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("generate usage hourly aggregate rebuild revision: %w", err)
	}
	return usageHourlyAggregateStructureRevision + ":rebuild-" + hex.EncodeToString(suffix[:]), nil
}

func ensureUsageDataMigrationColumns(db *sql.DB) error {
	rows, err := db.Query(`pragma table_info(usage_data_migrations)`)
	if err != nil {
		return err
	}
	existing := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(existing) == 0 {
		return nil
	}
	for _, column := range []string{
		"changed_rows integer not null default 0",
		"applied_rows integer not null default 0",
	} {
		name := strings.Fields(column)[0]
		if _, ok := existing[name]; ok {
			continue
		}
		if _, err := db.Exec(`alter table usage_data_migrations add column ` + column); err != nil {
			return err
		}
	}
	return nil
}

func ensureUsageArchiveRunColumns(db *sql.DB) error {
	rows, err := db.Query(`pragma table_info(usage_archive_runs)`)
	if err != nil {
		return err
	}
	existing := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(existing) == 0 {
		return nil
	}
	if _, ok := existing["requested_stage"]; ok {
		return nil
	}
	_, err = db.Exec(`alter table usage_archive_runs add column requested_stage text`)
	return err
}

func ensureCodexInspectionOwnershipColumns(db *sql.DB) error {
	rows, err := db.Query(`pragma table_info(codex_inspection_disable_ownership)`)
	if err != nil {
		return err
	}
	type columnInfo struct {
		notNull int
		pk      int
	}
	existing := map[string]columnInfo{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		existing[name] = columnInfo{notNull: notNull, pk: pk}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	primaryKeyReady := existing["file_name"].pk == 1 &&
		existing["provider"].pk == 2 &&
		existing["auth_index"].pk == 3 &&
		existing["account_id"].pk == 4 &&
		existing["account_snapshot"].pk == 5 &&
		existing["file_name"].notNull == 1 &&
		existing["provider"].notNull == 1 &&
		existing["auth_index"].notNull == 1 &&
		existing["account_id"].notNull == 1 &&
		existing["account_snapshot"].notNull == 1
	if primaryKeyReady {
		return nil
	}

	providerExpression := `'codex'`
	if _, ok := existing["provider"]; ok {
		providerExpression = `case coalesce(lower(replace(trim(provider), '_', '-')), '')
			when 'x-ai' then 'xai'
			when 'grok' then 'xai'
			else lower(replace(trim(provider), '_', '-'))
		end`
	}
	authIndexExpression := `''`
	if _, ok := existing["auth_index"]; ok {
		authIndexExpression = `coalesce(trim(auth_index), '')`
	}
	accountIDExpression := `''`
	if _, ok := existing["account_id"]; ok {
		accountIDExpression = `coalesce(trim(account_id), '')`
	}
	accountSnapshotSourceExpression := `''`
	if _, ok := existing["account_snapshot"]; ok {
		accountSnapshotSourceExpression = `coalesce(trim(account_snapshot), '')`
	}
	accountSnapshotExpression := fmt.Sprintf(
		`case when %s <> '' then '' else %s end`,
		accountIDExpression,
		accountSnapshotSourceExpression,
	)

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`drop table if exists codex_inspection_disable_ownership_v2`); err != nil {
		return err
	}
	if _, err := tx.Exec(`create table codex_inspection_disable_ownership_v2 (
		file_name text not null,
		provider text not null default '',
		auth_index text not null default '',
		account_id text not null default '',
		account_snapshot text not null default '',
		disabled_at_ms integer not null,
		updated_at_ms integer not null,
		primary key (file_name, provider, auth_index, account_id, account_snapshot)
	)`); err != nil {
		return err
	}
	copyStatement := fmt.Sprintf(`insert or replace into codex_inspection_disable_ownership_v2 (
		file_name, provider, auth_index, account_id, account_snapshot, disabled_at_ms, updated_at_ms
	) select trim(file_name), %s, %s, %s, %s, disabled_at_ms, updated_at_ms
	from codex_inspection_disable_ownership`, providerExpression, authIndexExpression, accountIDExpression, accountSnapshotExpression)
	if _, err := tx.Exec(copyStatement); err != nil {
		return err
	}
	if _, err := tx.Exec(`drop table codex_inspection_disable_ownership`); err != nil {
		return err
	}
	if _, err := tx.Exec(`alter table codex_inspection_disable_ownership_v2 rename to codex_inspection_disable_ownership`); err != nil {
		return err
	}
	return tx.Commit()
}

func ensureQuotaCooldownColumns(db *sql.DB) error {
	rows, err := db.Query(`pragma table_info(quota_cooldowns)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	existing := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, column := range []struct {
		name       string
		definition string
	}{
		{name: "reason_code", definition: "text"},
		{name: "window_kind", definition: "text"},
		{name: "evidence_json", definition: "text"},
	} {
		if _, ok := existing[column.name]; ok {
			continue
		}
		if _, err := db.Exec(`alter table quota_cooldowns add column ` + column.name + ` ` + column.definition); err != nil {
			return err
		}
	}
	return nil
}

func ensureUsageRollupLongContextColumns(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	columns := []struct {
		name       string
		definition string
	}{
		{name: "long_input_tokens", definition: "integer not null default 0"},
		{name: "long_output_tokens", definition: "integer not null default 0"},
		{name: "long_cached_tokens", definition: "integer not null default 0"},
		{name: "long_cache_read_tokens", definition: "integer not null default 0"},
		{name: "long_cache_creation_tokens", definition: "integer not null default 0"},
	}
	for _, table := range []struct {
		name       string
		legacyName string
		checkpoint string
	}{
		{name: usageAccountModelRollupsTable, legacyName: usageAccountModelRollupsLegacy, checkpoint: "account_history"},
		{name: "usage_dashboard_hourly_rollups", legacyName: usageDashboardHourlyLegacy, checkpoint: "dashboard_hourly"},
	} {
		rows, err := tx.Query(`pragma table_info(` + table.name + `)`)
		if err != nil {
			return err
		}
		existing := map[string]struct{}{}
		for rows.Next() {
			var cid int
			var name, typ string
			var notNull int
			var defaultValue any
			var pk int
			if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
				_ = rows.Close()
				return err
			}
			existing[name] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		missing := make([]struct {
			name       string
			definition string
		}, 0, len(columns))
		for _, column := range columns {
			if _, ok := existing[column.name]; ok {
				continue
			}
			missing = append(missing, column)
		}
		if len(missing) == 0 {
			continue
		}
		hasRows, err := tableHasRows(tx, table.name)
		if err != nil {
			return err
		}
		if hasRows {
			if err := parkAndRecreateDerivedTable(tx, table.name, table.legacyName); err != nil {
				return err
			}
		}
		for _, column := range missing {
			if _, err := tx.Exec(fmt.Sprintf(`alter table %s add column %s %s`, table.name, column.name, column.definition)); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`delete from usage_rollup_checkpoints where name = ?`, table.checkpoint); err != nil {
			return err
		}
		if err := scheduleUsageRollupRebuild(tx, table.checkpoint); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func ensureAccountActionCandidateColumns(db *sql.DB) error {
	rows, err := db.Query(`pragma table_info(account_action_candidates)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	existing := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	columns := []struct {
		name       string
		definition string
	}{
		{name: "account_id_snapshot", definition: "text"},
		{name: "last_error", definition: "text"},
		{name: "reason_code", definition: "text"},
		{name: "auto_disable_eligible", definition: "integer not null default 0"},
		{name: "auto_disabled_at_ms", definition: "integer"},
	}
	for _, column := range columns {
		if _, ok := existing[column.name]; ok {
			continue
		}
		if _, err := db.Exec(`alter table account_action_candidates add column ` + column.name + ` ` + column.definition); err != nil {
			return err
		}
	}
	return nil
}

func ensureQuotaSnapshotLifecycleColumns(db *sql.DB) error {
	observationRows, err := db.Query(`pragma table_info(account_quota_observations)`)
	if err != nil {
		return err
	}
	observationColumns := map[string]struct{}{}
	for observationRows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := observationRows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			_ = observationRows.Close()
			return err
		}
		observationColumns[name] = struct{}{}
	}
	if err := observationRows.Err(); err != nil {
		_ = observationRows.Close()
		return err
	}
	if err := observationRows.Close(); err != nil {
		return err
	}
	if _, ok := observationColumns["lifecycle_applied"]; !ok {
		if _, err := db.Exec(`alter table account_quota_observations
			add column lifecycle_applied integer not null default 1`); err != nil {
			return err
		}
	}
	rows, err := db.Query(`pragma table_info(account_quota_snapshots)`)
	if err != nil {
		return err
	}
	existing := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			_ = rows.Close()
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	columns := []struct {
		name       string
		definition string
	}{
		{name: "observation_id", definition: "integer"},
		{name: "logical_window_id", definition: "integer"},
		{name: "activation_id", definition: "integer"},
		{name: "cycle_id", definition: "integer"},
		{name: "scope_fingerprint", definition: "text not null default ''"},
		{name: "content_hash", definition: "text not null default ''"},
	}
	for _, column := range columns {
		if _, ok := existing[column.name]; ok {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf(
			`alter table account_quota_snapshots add column %s %s`,
			column.name,
			column.definition,
		)); err != nil {
			return err
		}
	}
	return nil
}

func ensureCodexInspectionRunColumns(db *sql.DB) error {
	rows, err := db.Query(`pragma table_info(codex_inspection_runs)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	existing := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	columns := []struct {
		name       string
		definition string
	}{
		{name: "reauth_count", definition: "integer not null default 0"},
	}
	for _, column := range columns {
		if _, ok := existing[column.name]; ok {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf(
			`alter table codex_inspection_runs add column %s %s`,
			column.name,
			column.definition,
		)); err != nil {
			return err
		}
	}
	return nil
}

func ensureCodexInspectionResultColumns(db *sql.DB) error {
	rows, err := db.Query(`pragma table_info(codex_inspection_results)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	existing := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	columns := []struct {
		name       string
		definition string
	}{
		{name: "action_status", definition: "text"},
		{name: "executed_action", definition: "text"},
		{name: "action_error", definition: "text"},
		{name: "account_snapshot", definition: "text"},
		{name: "plan_type", definition: "text"},
		{name: "quota_windows_json", definition: "text"},
		{name: "error_kind", definition: "text"},
		{name: "error_detail", definition: "text"},
		{name: "auto_recover_eligible", definition: "integer not null default 0"},
	}
	for _, column := range columns {
		if _, ok := existing[column.name]; ok {
			continue
		}
		if _, err := db.Exec(fmt.Sprintf(
			`alter table codex_inspection_results add column %s %s`,
			column.name,
			column.definition,
		)); err != nil {
			return err
		}
	}
	return nil
}

func ensureUsageEventSnapshotColumns(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query(`pragma table_info(usage_events)`)
	if err != nil {
		return err
	}
	defer rows.Close()

	existing := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	columns := []struct {
		name       string
		definition string
	}{
		{name: "account_snapshot", definition: "text"},
		{name: "auth_label_snapshot", definition: "text"},
		{name: "auth_file_snapshot", definition: "text"},
		{name: "auth_provider_snapshot", definition: "text"},
		{name: "auth_project_id_snapshot", definition: "text"},
		{name: "auth_snapshot_at_ms", definition: "integer"},
		{name: "executor_type", definition: "text"},
		{name: "requested_model", definition: "text"},
		{name: "resolved_model", definition: "text"},
		{name: "client_ip", definition: "text"},
		{name: "x_forwarded_for", definition: "text"},
		{name: "user_agent", definition: "text"},
		{name: "reasoning_effort", definition: "text"},
		{name: "service_tier", definition: "text"},
		{name: "request_service_tier", definition: "text"},
		{name: "response_service_tier", definition: "text"},
		{name: "cache_input_mode", definition: "text"},
		{name: "cache_read_tokens", definition: "integer not null default 0"},
		{name: "cache_creation_tokens", definition: "integer not null default 0"},
		{name: "normalized_uncached_input_tokens", definition: "integer"},
		{name: "normalized_total_input_tokens", definition: "integer"},
		{name: "normalized_cache_read_tokens", definition: "integer"},
		{name: "normalized_cache_creation_tokens", definition: "integer"},
		{name: "ttft_ms", definition: "integer"},
		{name: "fail_status_code", definition: "integer"},
		{name: "fail_summary", definition: "text"},
		{name: "response_metadata_json", definition: "text"},
		{name: "header_quota_recover_at_ms", definition: "integer"},
		{name: "header_quota_used_percent", definition: "real"},
		{name: "header_quota_plan_type", definition: "text"},
		{name: "header_error_kind", definition: "text"},
		{name: "header_error_code", definition: "text"},
		{name: "header_trace_id", definition: "text"},
		{name: "fail_body", definition: "text"},
	}
	for _, column := range columns {
		if _, ok := existing[column.name]; ok {
			continue
		}
		if _, err := tx.Exec(fmt.Sprintf(
			`alter table usage_events add column %s %s`,
			column.name,
			column.definition,
		)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

func ensureModelPriceColumns(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.Query(`pragma table_info(model_prices)`)
	if err != nil {
		return err
	}

	existing := map[string]struct{}{}
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		existing[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	columns := []struct {
		name       string
		definition string
	}{
		{name: "cache_read_per_1m", definition: "real not null default 0"},
		{name: "cache_creation_per_1m", definition: "real not null default 0"},
		{name: "prompt_configured", definition: "integer not null default 0"},
		{name: "completion_configured", definition: "integer not null default 0"},
		{name: "cache_read_configured", definition: "integer not null default 0"},
		{name: "cache_creation_configured", definition: "integer not null default 0"},
	}
	added := map[string]bool{}
	for _, column := range columns {
		if _, ok := existing[column.name]; ok {
			continue
		}
		if _, err := tx.Exec(fmt.Sprintf(
			`alter table model_prices add column %s %s`,
			column.name,
			column.definition,
		)); err != nil {
			return err
		}
		added[column.name] = true
	}
	if added["prompt_configured"] || added["completion_configured"] {
		if _, err := tx.Exec(`update model_prices set prompt_configured = 1, completion_configured = 1`); err != nil {
			return err
		}
	}
	if added["cache_read_configured"] {
		if _, err := tx.Exec(`update model_prices set cache_read_configured = 1 where cache_read_per_1m != 0`); err != nil {
			return err
		}
	}
	if added["cache_creation_configured"] {
		if _, err := tx.Exec(`update model_prices set cache_creation_configured = 1 where cache_creation_per_1m != 0`); err != nil {
			return err
		}
	}
	return tx.Commit()
}
