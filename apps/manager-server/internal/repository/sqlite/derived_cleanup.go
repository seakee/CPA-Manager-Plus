package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	quotasnapshotrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	derivedCleanupBatchLimit    = 1000
	derivedCleanupPause         = 50 * time.Millisecond
	derivedRetryDelay           = 30 * time.Second
	derivedCheckInterval        = 30 * time.Second
	derivedDeferredIndexesTable = "usage_derived_deferred_indexes"
)

var derivedLegacyTables = []string{
	usageAccountModelRollupsLegacy,
	usageAccountModelIdentityLegacy,
	usageAccountModelCodexIdentityLegacy,
	usagePricingAccountLegacy,
	usageDashboardHourlyLegacy,
	usageHourlyAggregateLegacy,
	usageMonitoringAccountLegacy,
	usageMonitoringAPIKeyLegacy,
	usageMonitoringAccountIdentityLegacy,
	usageMonitoringAPIKeyIdentityLegacy,
	usageMonitoringAccountCodexIdentityLegacy,
	usageMonitoringAPIKeyCodexIdentityLegacy,
	usageMonitoringSelectorLegacy,
	usageMonitoringHeaderLegacy,
	usageMonitoringProjectionLegacy,
	usageDashboardHourlySourceLegacy,
	usagePricingHourlySourceLegacy,
	usageCacheChangesSourceLegacy,
	usageAccountModelSourceLegacy,
	usagePricingAccountSourceLegacy,
}

type OfflineCleanupResult struct {
	CompletedJobs   int
	ProcessedRows   int64
	PreparedIndexes int
}

type derivedIndexPreparationResult struct {
	Created  int
	Deferred int
}

func ensureDerivedCleanupJobSchema(db *sql.DB) error {
	if _, err := db.Exec(`create table if not exists usage_derived_cleanup_jobs (
		id integer primary key autoincrement,
		generation integer not null unique,
		kind text not null,
		status text not null,
		projection_table text,
		fts_table text not null unique,
		processed_rows integer not null default 0,
		created_at_ms integer not null,
		updated_at_ms integer not null,
		finished_at_ms integer,
		last_error text
	)`); err != nil {
		return fmt.Errorf("create derived cleanup job schema: %w", err)
	}
	if _, err := db.Exec(`create table if not exists usage_derived_cleanup_cursors (
		target_name text primary key,
		table_name text not null,
		revision_token text not null,
		last_rowid integer not null default 0,
		updated_at_ms integer not null default 0
	)`); err != nil {
		return fmt.Errorf("create derived cleanup cursor schema: %w", err)
	}
	if _, err := db.Exec(`create table if not exists usage_derived_deferred_indexes (
		index_name text primary key,
		table_name text not null,
		reason text not null,
		created_at_ms integer not null,
		updated_at_ms integer not null
	)`); err != nil {
		return fmt.Errorf("create deferred derived index ledger: %w", err)
	}
	var ftsExists, projectionExists int
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`, usageMonitoringSearchLegacy).Scan(&ftsExists); err != nil {
		return fmt.Errorf("inspect retained legacy monitoring FTS: %w", err)
	}
	if ftsExists == 0 {
		return nil
	}
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`, usageMonitoringProjectionLegacy).Scan(&projectionExists); err != nil {
		return fmt.Errorf("inspect retained legacy monitoring projection: %w", err)
	}
	status := "offline_required"
	projectionTable := any(nil)
	if projectionExists != 0 {
		status = "online_cleanup"
		projectionTable = usageMonitoringProjectionLegacy
	}
	nowMS := time.Now().UnixMilli()
	if _, err := db.Exec(`insert or ignore into usage_derived_cleanup_jobs (
		generation, kind, status, projection_table, fts_table,
		processed_rows, created_at_ms, updated_at_ms
	) values (0, 'monitoring_fts', ?, ?, ?, 0, ?, ?)`,
		status,
		projectionTable,
		usageMonitoringSearchLegacy,
		nowMS,
		nowMS,
	); err != nil {
		return fmt.Errorf("adopt retained legacy monitoring FTS: %w", err)
	}
	return nil
}

var derivedIndexStatements = []struct {
	name      string
	tableName string
	sql       string
}{
	{"idx_usage_events_timestamp", "usage_events", `create index if not exists idx_usage_events_timestamp on usage_events(timestamp_ms)`},
	{"idx_usage_events_request_id", "usage_events", `create index if not exists idx_usage_events_request_id on usage_events(request_id)`},
	{"idx_usage_events_model", "usage_events", `create index if not exists idx_usage_events_model on usage_events(model)`},
	{"idx_usage_events_auth_index", "usage_events", `create index if not exists idx_usage_events_auth_index on usage_events(auth_index)`},
	{"idx_usage_events_endpoint", "usage_events", `create index if not exists idx_usage_events_endpoint on usage_events(endpoint)`},
	{"idx_usage_events_header_quota_recover", "usage_events", `create index if not exists idx_usage_events_header_quota_recover on usage_events(header_quota_recover_at_ms)`},
	{"idx_usage_events_header_error_kind", "usage_events", `create index if not exists idx_usage_events_header_error_kind on usage_events(header_error_kind)`},
	{"idx_usage_events_header_trace_id", "usage_events", `create index if not exists idx_usage_events_header_trace_id on usage_events(header_trace_id)`},
	{"idx_usage_events_latest_request_auth_file", "usage_events", `create index if not exists idx_usage_events_latest_request_auth_file on usage_events(auth_file_snapshot collate nocase, auth_index collate nocase, timestamp_ms desc, id desc)`},
	{"idx_usage_events_latest_request_source", "usage_events", `create index if not exists idx_usage_events_latest_request_source on usage_events(source collate nocase, auth_index collate nocase, timestamp_ms desc, id desc)`},
	{"idx_account_action_candidates_pending_identity_action", "account_action_candidates", `create unique index if not exists idx_account_action_candidates_pending_identity_action
		on account_action_candidates(
			auth_file_name,
			action_type,
			coalesce(trim(reason_code), ''),
			coalesce(trim(auth_index), ''),
			case when coalesce(trim(auth_index), '') <> '' then '' else coalesce(trim(account_id_snapshot), '') end,
			case when coalesce(trim(auth_index), '') <> '' then ''
				else case coalesce(lower(replace(trim(provider), '_', '-')), '')
					when 'x-ai' then 'xai'
					when 'grok' then 'xai'
					else coalesce(lower(replace(trim(provider), '_', '-')), '')
				end
			end,
			case when coalesce(trim(auth_index), '') <> '' or coalesce(trim(account_id_snapshot), '') <> '' then ''
				else coalesce(trim(account_snapshot), '')
			end
		) where status = 'pending'`},
	{"idx_account_action_candidates_status_seen", "account_action_candidates", `create index if not exists idx_account_action_candidates_status_seen on account_action_candidates(status, last_seen_at_ms)`},
	{"idx_codex_inspection_runs_started_at", "codex_inspection_runs", `create index if not exists idx_codex_inspection_runs_started_at on codex_inspection_runs(started_at_ms)`},
	{"idx_codex_inspection_runs_status", "codex_inspection_runs", `create index if not exists idx_codex_inspection_runs_status on codex_inspection_runs(status)`},
	{"idx_codex_inspection_runs_trigger", "codex_inspection_runs", `create index if not exists idx_codex_inspection_runs_trigger on codex_inspection_runs(trigger_type, trigger_key)`},
	{"idx_codex_inspection_leases_expiry", "codex_inspection_leases", `create index if not exists idx_codex_inspection_leases_expiry on codex_inspection_leases(lease_expires_at_ms)`},
	{"idx_codex_inspection_results_run", "codex_inspection_results", `create index if not exists idx_codex_inspection_results_run on codex_inspection_results(run_id)`},
	{"idx_codex_inspection_logs_run", "codex_inspection_logs", `create index if not exists idx_codex_inspection_logs_run on codex_inspection_logs(run_id, created_at_ms)`},
	{"idx_quota_cooldowns_due", "quota_cooldowns", `create index if not exists idx_quota_cooldowns_due on quota_cooldowns(status, recover_at_ms)`},
	{"idx_quota_cooldowns_active_identity", "quota_cooldowns", `create unique index if not exists idx_quota_cooldowns_active_identity
		on quota_cooldowns (
			auth_file_name,
			owner,
			coalesce(trim(auth_index), ''),
			case
				when coalesce(trim(auth_index), '') <> '' then ''
				else case coalesce(lower(replace(trim(provider), '_', '-')), '')
					when 'x-ai' then 'xai'
					when 'grok' then 'xai'
					else coalesce(lower(replace(trim(provider), '_', '-')), '')
				end
			end,
			case
				when coalesce(trim(auth_index), '') <> '' then ''
				else coalesce(trim(account_snapshot), '')
			end
		)
		where status = 'active'`},
	{"idx_quota_observations_account_time", "account_quota_observations", `create index if not exists idx_quota_observations_account_time on account_quota_observations(account_key, provider, observed_at_ms desc)`},
	{"idx_quota_observations_inventory", "account_quota_observations", `create index if not exists idx_quota_observations_inventory on account_quota_observations(account_key, provider, inventory_scope_key, observed_at_ms desc)`},
	{"idx_quota_observations_lifecycle_watermark", "account_quota_observations", `create index if not exists idx_quota_observations_lifecycle_watermark on account_quota_observations(account_key, provider, inventory_scope_key, lifecycle_applied, observed_at_ms desc)`},
	{"idx_quota_windows_account_state", "account_quota_windows", `create index if not exists idx_quota_windows_account_state on account_quota_windows(account_key, provider, availability, updated_at_ms desc)`},
	{"idx_quota_windows_inventory", "account_quota_windows", `create index if not exists idx_quota_windows_inventory on account_quota_windows(account_key, provider, inventory_scope_key, availability)`},
	{"idx_quota_activations_active", "account_quota_window_activations", `create unique index if not exists idx_quota_activations_active on account_quota_window_activations(window_id) where deactivated_at_ms is null`},
	{"idx_quota_cycles_active", "account_quota_cycles", `create unique index if not exists idx_quota_cycles_active on account_quota_cycles(activation_id) where actual_end_ms is null`},
	{"idx_quota_cycles_history", "account_quota_cycles", `create index if not exists idx_quota_cycles_history on account_quota_cycles(activation_id, actual_start_ms desc)`},
	{"idx_quota_snapshots_latest", "account_quota_snapshots", `create index if not exists idx_quota_snapshots_latest on account_quota_snapshots(account_key, provider, provider_window_id, model_scope_kind, model_scope_key, observed_at_ms desc)`},
	{"idx_quota_snapshots_observation", "account_quota_snapshots", `create index if not exists idx_quota_snapshots_observation on account_quota_snapshots(observation_id)`},
	{"idx_quota_snapshots_window_cycle", "account_quota_snapshots", `create index if not exists idx_quota_snapshots_window_cycle on account_quota_snapshots(logical_window_id, cycle_id, observed_at_ms desc)`},
	{"idx_quota_snapshots_cycle_evidence", "account_quota_snapshots", `create index if not exists idx_quota_snapshots_cycle_evidence on account_quota_snapshots(cycle_id, observed_at_ms, id)`},
	{"idx_quota_snapshots_legacy_migration", "account_quota_snapshots", `create index if not exists idx_quota_snapshots_legacy_migration on account_quota_snapshots(
		account_key, provider, observed_at_ms,
		case lower(trim(source))
			when 'response_body' then 1
			when 'api_query' then 2
			when 'inspection' then 3
			else 0
		end,
		coalesce(source_observation_id, ''), id
	) where observation_id is null`},
	{"idx_usage_account_model_rollups_last_seen", usageAccountModelRollupsTable, `create index if not exists idx_usage_account_model_rollups_last_seen on usage_account_model_rollups(last_seen_ms)`},
	{"idx_usage_account_model_rollups_auth_index", usageAccountModelRollupsTable, `create index if not exists idx_usage_account_model_rollups_auth_index on usage_account_model_rollups(auth_index)`},
	{"idx_usage_pricing_hourly_bucket", "usage_pricing_hourly_rollups_v1", `create index if not exists idx_usage_pricing_hourly_bucket on usage_pricing_hourly_rollups_v1(structure_revision, bucket_ms)`},
	{"idx_usage_pricing_account_key", usagePricingAccountRollupsTable, `create index if not exists idx_usage_pricing_account_key on usage_pricing_account_rollups_v1(structure_revision, account_key)`},
	{"idx_usage_monitoring_account_daily_bucket", usageMonitoringAccountDailyTable, `create index if not exists idx_usage_monitoring_account_daily_bucket on usage_monitoring_account_daily_rollups_v1(structure_revision, bucket_ms)`},
	{"idx_usage_monitoring_account_daily_credential_window", usageMonitoringAccountDailyTable, `create index if not exists idx_usage_monitoring_account_daily_credential_window on usage_monitoring_account_daily_rollups_v1(structure_revision, trim(auth_file_snapshot), trim(auth_index), bucket_ms)`},
	{"idx_usage_monitoring_account_daily_legacy_window", usageMonitoringAccountDailyTable, `create index if not exists idx_usage_monitoring_account_daily_legacy_window on usage_monitoring_account_daily_rollups_v1(structure_revision, trim(source), trim(auth_index), bucket_ms)`},
	{"idx_usage_monitoring_api_key_daily_bucket", usageMonitoringAPIKeyDailyTable, `create index if not exists idx_usage_monitoring_api_key_daily_bucket on usage_monitoring_api_key_daily_rollups_v1(structure_revision, bucket_ms)`},
	{"idx_usage_monitoring_selector_daily_bucket", usageMonitoringSelectorDailyTable, `create index if not exists idx_usage_monitoring_selector_daily_bucket on usage_monitoring_selector_daily_rollups_v1(bucket_ms)`},
	{"idx_usage_monitoring_selector_revision_bucket", usageMonitoringSelectorDailyTable, `create index if not exists idx_usage_monitoring_selector_revision_bucket on usage_monitoring_selector_daily_rollups_v1(model_format_revision, bucket_ms)`},
	{"idx_usage_monitoring_event_projection_timestamp", "usage_monitoring_event_projection_v1", `create index if not exists idx_usage_monitoring_event_projection_timestamp on usage_monitoring_event_projection_v1(timestamp_ms desc, event_id desc)`},
	{"idx_usage_monitoring_event_projection_account_window", "usage_monitoring_event_projection_v1", `create index if not exists idx_usage_monitoring_event_projection_account_window on usage_monitoring_event_projection_v1(account_key, timestamp_ms, event_id)`},
	{"idx_usage_monitoring_event_projection_model_timestamp", "usage_monitoring_event_projection_v1", `create index if not exists idx_usage_monitoring_event_projection_model_timestamp on usage_monitoring_event_projection_v1(analytics_model, timestamp_ms desc, event_id desc)`},
	{"idx_usage_monitoring_header_latest_timestamp", usageMonitoringHeaderLatestTable, `create index if not exists idx_usage_monitoring_header_latest_timestamp on usage_monitoring_header_latest_v1(timestamp_ms desc, event_id desc)`},
	{"idx_usage_event_identity_ledger_raw_event_id", usageEventIdentityLedger, `create index if not exists idx_usage_event_identity_ledger_raw_event_id on usage_event_identity_ledger(raw_event_id)`},
	{"idx_usage_event_identity_ledger_bucket", usageEventIdentityLedger, `create index if not exists idx_usage_event_identity_ledger_bucket on usage_event_identity_ledger(bucket_ms)`},
}

// RunDerivedStartupMaintenance creates only indexes whose target tables are
// empty and whose names are not retained by parked tables. Any index that can
// grow with stored data is deferred to the offline cleanup command so collector
// startup cannot be delayed by unbounded DDL.
func RunDerivedStartupMaintenance(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return nil
	}
	if err := ensureDerivedCleanupJobSchema(db); err != nil {
		return err
	}
	log.Printf("[derived-migration] post-listen index preparation started")
	indexResult, err := prepareDerivedIndexes(ctx, db, false)
	if err != nil {
		return err
	}
	actionIndexRemoved, actionIndexDeferred, err := ensureAccountActionCandidateIdentityIndex(ctx, db, false)
	if err != nil {
		return fmt.Errorf("prepare account action candidate identity index: %w", err)
	}
	quotaIndexRemoved, quotaIndexDeferred, err := ensureQuotaCooldownIdentityIndex(ctx, db, false)
	if err != nil {
		return fmt.Errorf("prepare quota cooldown identity index: %w", err)
	}
	var offlineJobs int
	if err := db.QueryRowContext(ctx, `select count(*) from usage_derived_cleanup_jobs
		where kind = 'monitoring_fts' and status = 'offline_required'`).Scan(&offlineJobs); err != nil {
		return fmt.Errorf("inspect offline derived cleanup jobs: %w", err)
	}
	if offlineJobs > 0 {
		log.Printf("[derived-migration] cleanup requires offline finalization jobs=%d command=cleanup-derived", offlineJobs)
	}
	if indexResult.Deferred > 0 || actionIndexDeferred || quotaIndexDeferred {
		log.Printf("[derived-migration] deferred index preparation indexes=%d accountActionLegacy=%t quotaLegacy=%t command=cleanup-derived", indexResult.Deferred, actionIndexDeferred, quotaIndexDeferred)
	}
	log.Printf("[derived-migration] post-listen index preparation completed created=%d removedAccountActionLegacy=%t removedQuotaLegacy=%t", indexResult.Created, actionIndexRemoved, quotaIndexRemoved)
	return nil
}

// StartDerivedMaintenance periodically removes revisions that become stale
// while the service is running. Index creation and unbounded virtual-table DDL
// are intentionally excluded from this online path.
func StartDerivedMaintenance(ctx context.Context, db *sql.DB) {
	if db == nil {
		return
	}
	go runDerivedMaintenance(ctx, db)
}

func runDerivedMaintenance(ctx context.Context, db *sql.DB) {
	ticker := time.NewTicker(derivedCheckInterval)
	defer ticker.Stop()
	for {
		processed, err := cleanupDerivedUntilIdle(ctx, db)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[derived-migration] periodic cleanup failed: %v", err)
		} else if processed > 0 {
			log.Printf("[derived-migration] periodic cleanup completed processed=%d", processed)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func cleanupDerivedUntilIdle(ctx context.Context, db *sql.DB) (int64, error) {
	processed := int64(0)
	for {
		batchProcessed, pending, err := cleanupDerivedBatch(ctx, db, derivedCleanupBatchLimit)
		if err != nil {
			return processed, err
		}
		processed += batchProcessed
		if batchProcessed > 0 && processed == batchProcessed {
			log.Printf("[derived-migration] cleanup started batchSize=%d", derivedCleanupBatchLimit)
		}
		if batchProcessed > 0 && processed%10000 < batchProcessed {
			log.Printf("[derived-migration] cleanup progress processed=%d", processed)
		}
		if !pending {
			return processed, nil
		}
		if !waitDerivedMaintenance(ctx, derivedCleanupPause) {
			return processed, ctx.Err()
		}
	}
}

func prepareDerivedIndexes(ctx context.Context, db *sql.DB, allowNonEmpty bool) (derivedIndexPreparationResult, error) {
	var result derivedIndexPreparationResult
	for _, index := range derivedIndexStatements {
		var indexedTable string
		err := db.QueryRowContext(ctx, `select tbl_name from sqlite_master
			where type = 'index' and name = ?`, index.name).Scan(&indexedTable)
		if err == nil && indexedTable == index.tableName {
			if clearErr := clearDeferredDerivedIndex(ctx, db, index.name); clearErr != nil {
				return result, clearErr
			}
			continue
		}
		if err != nil && err != sql.ErrNoRows {
			return result, fmt.Errorf("inspect derived index %s: %w", index.name, err)
		}
		tableExists, tableErr := derivedTableExists(ctx, db, index.tableName)
		if tableErr != nil {
			return result, fmt.Errorf("inspect derived index target %s for %s: %w", index.tableName, index.name, tableErr)
		}
		if !tableExists {
			// Optional derived tables are created by their owning migration. There
			// is no index work to remember until that table exists and can contain
			// rows; recording this as deferred would make a fresh database appear
			// degraded forever.
			if clearErr := clearDeferredDerivedIndex(ctx, db, index.name); clearErr != nil {
				return result, clearErr
			}
			continue
		}
		if err == nil {
			if !allowNonEmpty {
				if ledgerErr := recordDeferredDerivedIndex(ctx, db, index.name, index.tableName, DerivedMaintenanceReasonLegacyIndexReplaced); ledgerErr != nil {
					return result, ledgerErr
				}
				result.Deferred++
				log.Printf("[derived-migration] deferring index %s because its name is retained by %s", index.name, indexedTable)
				continue
			}
			log.Printf("[derived-migration] removing stale index %s from %s", index.name, indexedTable)
			if _, err := db.ExecContext(ctx, `drop index `+index.name); err != nil {
				return result, fmt.Errorf("remove stale derived index %s from %s: %w", index.name, indexedTable, err)
			}
		}
		if !allowNonEmpty {
			hasRows, err := derivedIndexTableHasRows(ctx, db, index.tableName)
			if err != nil {
				return result, fmt.Errorf("inspect derived index target %s for %s: %w", index.tableName, index.name, err)
			}
			if hasRows {
				if ledgerErr := recordDeferredDerivedIndex(ctx, db, index.name, index.tableName, DerivedMaintenanceReasonDeferredIndexes); ledgerErr != nil {
					return result, ledgerErr
				}
				result.Deferred++
				log.Printf("[derived-migration] deferring index %s because %s is non-empty", index.name, index.tableName)
				continue
			}
		}
		log.Printf("[derived-migration] creating index %s", index.name)
		if ledgerErr := recordDeferredDerivedIndex(ctx, db, index.name, index.tableName, DerivedMaintenanceReasonDeferredIndexes); ledgerErr != nil {
			return result, ledgerErr
		}
		if _, err := db.ExecContext(ctx, index.sql); err != nil {
			return result, fmt.Errorf("create derived index %s: %w", index.name, err)
		}
		if clearErr := clearDeferredDerivedIndex(ctx, db, index.name); clearErr != nil {
			return result, clearErr
		}
		result.Created++
	}
	return result, nil
}

func derivedIndexTableHasRows(ctx context.Context, db *sql.DB, tableName string) (bool, error) {
	var hasRows int
	if err := db.QueryRowContext(ctx, `select exists(select 1 from `+tableName+` limit 1)`).Scan(&hasRows); err != nil {
		return false, err
	}
	return hasRows != 0, nil
}

func ensureQuotaCooldownIdentityIndex(ctx context.Context, db *sql.DB, allowNonEmpty bool) (removed bool, deferred bool, err error) {
	return ensureLegacyDerivedIndexReplaced(
		ctx,
		db,
		"quota_cooldowns",
		"idx_quota_cooldowns_active_owner",
		"idx_quota_cooldowns_active_identity",
		allowNonEmpty,
	)
}

func ensureAccountActionCandidateIdentityIndex(ctx context.Context, db *sql.DB, allowNonEmpty bool) (removed bool, deferred bool, err error) {
	return ensureLegacyDerivedIndexReplaced(
		ctx,
		db,
		"account_action_candidates",
		"idx_account_action_candidates_pending_file_action",
		"idx_account_action_candidates_pending_identity_action",
		allowNonEmpty,
	)
}

func ensureLegacyDerivedIndexReplaced(
	ctx context.Context,
	db *sql.DB,
	tableName string,
	legacyIndexName string,
	replacementIndexName string,
	allowNonEmpty bool,
) (removed bool, deferred bool, err error) {
	var legacyTable string
	err = db.QueryRowContext(ctx, `select tbl_name from sqlite_master
		where type = 'index' and name = ?`, legacyIndexName).Scan(&legacyTable)
	if errors.Is(err, sql.ErrNoRows) {
		if clearErr := clearDeferredDerivedIndex(ctx, db, replacementIndexName); clearErr != nil {
			return false, false, clearErr
		}
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if legacyTable != tableName {
		return false, false, fmt.Errorf("legacy index %s belongs to unexpected table %s", legacyIndexName, legacyTable)
	}
	var replacementTable string
	err = db.QueryRowContext(ctx, `select tbl_name from sqlite_master
		where type = 'index' and name = ?`, replacementIndexName).Scan(&replacementTable)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && replacementTable != tableName) {
		if allowNonEmpty {
			if ledgerErr := recordDeferredDerivedIndex(ctx, db, replacementIndexName, tableName, DerivedMaintenanceReasonLegacyIndexReplaced); ledgerErr != nil {
				return false, false, ledgerErr
			}
			return false, false, fmt.Errorf("replacement index %s is not ready", replacementIndexName)
		}
		if ledgerErr := recordDeferredDerivedIndex(ctx, db, replacementIndexName, tableName, DerivedMaintenanceReasonLegacyIndexReplaced); ledgerErr != nil {
			return false, false, ledgerErr
		}
		return false, true, nil
	}
	if err != nil {
		return false, false, err
	}
	if _, err := db.ExecContext(ctx, `drop index `+legacyIndexName); err != nil {
		return false, false, err
	}
	if err := clearDeferredDerivedIndex(ctx, db, replacementIndexName); err != nil {
		return false, false, err
	}
	return true, false, nil
}

func recordDeferredDerivedIndex(ctx context.Context, db *sql.DB, indexName, tableName, reason string) error {
	nowMS := time.Now().UnixMilli()
	if _, err := db.ExecContext(ctx, `insert into `+derivedDeferredIndexesTable+` (
		index_name, table_name, reason, created_at_ms, updated_at_ms
	) values (?, ?, ?, ?, ?)
	on conflict(index_name) do update set
		table_name = excluded.table_name,
		reason = excluded.reason,
		updated_at_ms = excluded.updated_at_ms`,
		indexName,
		tableName,
		reason,
		nowMS,
		nowMS,
	); err != nil {
		return fmt.Errorf("record deferred derived index %s: %w", indexName, err)
	}
	return nil
}

func clearDeferredDerivedIndex(ctx context.Context, db *sql.DB, indexName string) error {
	if _, err := db.ExecContext(ctx, `delete from `+derivedDeferredIndexesTable+` where index_name = ?`, indexName); err != nil {
		return fmt.Errorf("clear deferred derived index %s: %w", indexName, err)
	}
	return nil
}

func cleanupDerivedBatch(ctx context.Context, db *sql.DB, limit int) (int64, bool, error) {
	if limit <= 0 {
		limit = derivedCleanupBatchLimit
	}
	processed, handled, err := cleanupMonitoringFTSJobBatch(ctx, db, limit)
	if err != nil {
		return 0, false, err
	}
	if handled {
		return processed, true, nil
	}
	for _, tableName := range derivedLegacyTables {
		exists, err := derivedTableExists(ctx, db, tableName)
		if err != nil {
			return 0, false, err
		}
		if !exists {
			continue
		}
		processed, err := deleteDerivedRows(ctx, db, tableName, "", nil, limit)
		if err != nil {
			return 0, false, fmt.Errorf("clean legacy derived table %s: %w", tableName, err)
		}
		if processed > 0 {
			return processed, true, nil
		}
		if _, err := db.ExecContext(ctx, `drop table `+tableName); err != nil {
			return 0, false, fmt.Errorf("drop empty legacy derived table %s: %w", tableName, err)
		}
		log.Printf("[derived-migration] removed empty legacy table %s", tableName)
		return 0, true, nil
	}
	targets, err := staleDerivedCleanupTargets(ctx, db)
	if err != nil {
		return 0, false, err
	}
	for _, target := range targets {
		processed, advanced, err := cleanupStaleDerivedTargetBatch(ctx, db, target, limit)
		if err != nil {
			return 0, false, fmt.Errorf("clean stale derived rows in %s: %w", target.tableName, err)
		}
		if advanced {
			return processed, true, nil
		}
	}
	return 0, false, nil
}

func cleanupMonitoringFTSJobBatch(ctx context.Context, db *sql.DB, limit int) (int64, bool, error) {
	var jobID int64
	var projectionTable, ftsTable string
	err := db.QueryRowContext(ctx, `select id, projection_table, fts_table
		from usage_derived_cleanup_jobs
		where kind = 'monitoring_fts' and status = 'online_cleanup'
		order by generation limit 1`).Scan(&jobID, &projectionTable, &ftsTable)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("load monitoring FTS cleanup job: %w", err)
	}
	if !validMonitoringCleanupTable(projectionTable, usageMonitoringProjectionLegacyPrefix, usageMonitoringProjectionLegacy) ||
		!validMonitoringCleanupTable(ftsTable, usageMonitoringSearchLegacyPrefix, usageMonitoringSearchLegacy) {
		return 0, false, fmt.Errorf("monitoring FTS cleanup job %d contains invalid table names", jobID)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()
	projectionExists, err := derivedTableExists(ctx, tx, projectionTable)
	if err != nil {
		return 0, false, err
	}
	if !projectionExists {
		if err := markMonitoringCleanupOfflineTx(ctx, tx, jobID, "paired projection table is missing"); err != nil {
			return 0, false, err
		}
		if err := tx.Commit(); err != nil {
			return 0, false, err
		}
		return 0, true, nil
	}
	ftsExists, err := derivedTableExists(ctx, tx, ftsTable)
	if err != nil {
		return 0, false, err
	}
	if !ftsExists {
		if err := markMonitoringCleanupOfflineTx(ctx, tx, jobID, "paired FTS table is missing"); err != nil {
			return 0, false, err
		}
		if err := tx.Commit(); err != nil {
			return 0, false, err
		}
		return 0, true, nil
	}
	var hasRows int
	if err := tx.QueryRowContext(ctx, `select exists(select 1 from `+projectionTable+` limit 1)`).Scan(&hasRows); err != nil {
		return 0, false, fmt.Errorf("inspect monitoring cleanup projection %s: %w", projectionTable, err)
	}
	if hasRows == 0 {
		if err := markMonitoringCleanupOfflineTx(ctx, tx, jobID, ""); err != nil {
			return 0, false, err
		}
		if err := tx.Commit(); err != nil {
			return 0, false, err
		}
		return 0, true, nil
	}
	if _, err := tx.ExecContext(ctx, `insert into `+ftsTable+` (`+ftsTable+`, rowid, search_text)
		select 'delete', event_id, search_text from `+projectionTable+` order by event_id limit ?`, limit); err != nil {
		_ = tx.Rollback()
		if markErr := markMonitoringCleanupOffline(ctx, db, jobID, fmt.Sprintf("paired FTS cleanup failed: %v", err)); markErr != nil {
			return 0, false, errors.Join(err, markErr)
		}
		return 0, true, nil
	}
	result, err := tx.ExecContext(ctx, `delete from `+projectionTable+` where event_id in (
		select event_id from `+projectionTable+` order by event_id limit ?
	)`, limit)
	if err != nil {
		return 0, false, fmt.Errorf("delete paired monitoring projection rows: %w", err)
	}
	processed, err := result.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("count paired monitoring projection rows: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `update usage_derived_cleanup_jobs set
		processed_rows = processed_rows + ?, updated_at_ms = ?, last_error = null
		where id = ?`, processed, time.Now().UnixMilli(), jobID); err != nil {
		return 0, false, err
	}
	if processed < int64(limit) {
		if err := markMonitoringCleanupOfflineTx(ctx, tx, jobID, ""); err != nil {
			return 0, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return processed, true, nil
}

type tableExistenceQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func derivedTableExists(ctx context.Context, db tableExistenceQuerier, tableName string) (bool, error) {
	var count int
	if err := db.QueryRowContext(ctx, `select count(*) from sqlite_master where type = 'table' and name = ?`, tableName).Scan(&count); err != nil {
		return false, err
	}
	return count != 0, nil
}

func markMonitoringCleanupOffline(ctx context.Context, db *sql.DB, jobID int64, detail string) error {
	_, err := db.ExecContext(ctx, `update usage_derived_cleanup_jobs set
		status = 'offline_required', updated_at_ms = ?, last_error = ? where id = ?`,
		time.Now().UnixMilli(), nullableCleanupDetail(detail), jobID)
	return err
}

func markMonitoringCleanupOfflineTx(ctx context.Context, tx *sql.Tx, jobID int64, detail string) error {
	_, err := tx.ExecContext(ctx, `update usage_derived_cleanup_jobs set
		status = 'offline_required', updated_at_ms = ?, last_error = ? where id = ?`,
		time.Now().UnixMilli(), nullableCleanupDetail(detail), jobID)
	return err
}

func nullableCleanupDetail(detail string) any {
	if detail == "" {
		return nil
	}
	return detail
}

func validMonitoringCleanupTable(tableName, generatedPrefix, fixedName string) bool {
	if tableName == fixedName {
		return true
	}
	if !strings.HasPrefix(tableName, generatedPrefix) {
		return false
	}
	suffix := strings.TrimPrefix(tableName, generatedPrefix)
	if len(suffix) != 6 {
		return false
	}
	for _, character := range suffix {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func CleanupDerivedOffline(ctx context.Context, db *sql.DB) (OfflineCleanupResult, error) {
	var result OfflineCleanupResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if err := ensureDerivedCleanupJobSchema(db); err != nil {
		return result, err
	}
	indexResult, err := prepareDerivedIndexes(ctx, db, true)
	if err != nil {
		return result, err
	}
	result.PreparedIndexes += indexResult.Created
	actionIndexRemoved, _, err := ensureAccountActionCandidateIdentityIndex(ctx, db, true)
	if err != nil {
		return result, fmt.Errorf("replace legacy account action candidate identity index: %w", err)
	}
	if actionIndexRemoved {
		result.PreparedIndexes++
	}
	quotaIndexRemoved, _, err := ensureQuotaCooldownIdentityIndex(ctx, db, true)
	if err != nil {
		return result, fmt.Errorf("replace legacy quota cooldown identity index: %w", err)
	}
	if quotaIndexRemoved {
		result.PreparedIndexes++
	}
	for {
		migrationResult, err := quotasnapshotrepo.BackfillLegacySnapshotsBatch(ctx, db, offlineLegacyQuotaSnapshotGroupLimit())
		if err != nil {
			return result, fmt.Errorf("migrate legacy quota snapshots offline: %w", err)
		}
		result.ProcessedRows += int64(migrationResult.Processed)
		if migrationResult.Completed {
			break
		}
		if migrationResult.Processed == 0 {
			return result, errors.New("legacy quota snapshot offline migration made no progress")
		}
	}
	processed, err := cleanupDerivedUntilIdle(ctx, db)
	result.ProcessedRows += processed
	if err != nil {
		return result, fmt.Errorf("drain derived cleanup batches offline: %w", err)
	}
	rows, err := db.QueryContext(ctx, `select id, projection_table, fts_table
		from usage_derived_cleanup_jobs
		where kind = 'monitoring_fts' and status = 'offline_required'
		order by generation`)
	if err != nil {
		return result, err
	}
	jobs := make([]monitoringOfflineCleanupJob, 0)
	for rows.Next() {
		var job monitoringOfflineCleanupJob
		if err := rows.Scan(&job.id, &job.projectionTable, &job.ftsTable); err != nil {
			_ = rows.Close()
			return result, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Close(); err != nil {
		return result, err
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	for _, job := range jobs {
		if !validMonitoringCleanupTable(job.ftsTable, usageMonitoringSearchLegacyPrefix, usageMonitoringSearchLegacy) ||
			(job.projectionTable.Valid && !validMonitoringCleanupTable(job.projectionTable.String, usageMonitoringProjectionLegacyPrefix, usageMonitoringProjectionLegacy)) {
			return result, fmt.Errorf("monitoring FTS cleanup job %d contains invalid table names", job.id)
		}
		if err := finalizeMonitoringCleanupJob(ctx, db, job); err != nil {
			return result, err
		}
		result.CompletedJobs++
	}
	return result, nil
}

func offlineLegacyQuotaSnapshotGroupLimit() int {
	return int(^uint(0)>>1) - 1
}

type monitoringOfflineCleanupJob struct {
	id              int64
	projectionTable sql.NullString
	ftsTable        string
}

func finalizeMonitoringCleanupJob(ctx context.Context, db *sql.DB, job monitoringOfflineCleanupJob) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `rollback`)
		}
		_ = conn.Close()
	}()
	if _, err := conn.ExecContext(ctx, `begin exclusive`); err != nil {
		return fmt.Errorf("acquire exclusive SQLite cleanup lock: %w", err)
	}
	ftsExists, err := derivedTableExists(ctx, conn, job.ftsTable)
	if err != nil {
		return err
	}
	if ftsExists {
		if _, err := conn.ExecContext(ctx, `drop table `+job.ftsTable); err != nil {
			return fmt.Errorf("drop offline monitoring FTS %s: %w", job.ftsTable, err)
		}
	}
	if job.projectionTable.Valid {
		projectionExists, err := derivedTableExists(ctx, conn, job.projectionTable.String)
		if err != nil {
			return err
		}
		if projectionExists {
			if _, err := conn.ExecContext(ctx, `drop table `+job.projectionTable.String); err != nil {
				return fmt.Errorf("drop offline monitoring projection %s: %w", job.projectionTable.String, err)
			}
		}
	}
	nowMS := time.Now().UnixMilli()
	if _, err := conn.ExecContext(ctx, `update usage_derived_cleanup_jobs set
		status = 'completed', updated_at_ms = ?, finished_at_ms = ?, last_error = null
		where id = ?`, nowMS, nowMS, job.id); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `commit`); err != nil {
		return err
	}
	committed = true
	return nil
}

type derivedCleanupTarget struct {
	name           string
	tableName      string
	revisionColumn string
	activeRevision string
	stateTable     string
	stateName      string
}

func staleDerivedCleanupTargets(ctx context.Context, db *sql.DB) ([]derivedCleanupTarget, error) {
	targets := []derivedCleanupTarget{{
		name:           "monitoring_selector_model_format",
		tableName:      usageMonitoringSelectorDailyTable,
		revisionColumn: "model_format_revision",
		activeRevision: usageidentity.ModelFormatVersion,
	}}
	for _, state := range []struct {
		tableNames     []string
		stateTable     string
		stateName      string
		revisionColumn string
	}{
		{[]string{"usage_pricing_hourly_rollups_v1", usagePricingAccountRollupsTable}, "usage_pricing_rollup_state", "pricing_v1", "structure_revision"},
		{[]string{usageMonitoringAccountDailyTable, usageMonitoringAPIKeyDailyTable}, usageMonitoringRollupStateTable, usageMonitoringStatsRollupName, "structure_revision"},
	} {
		var activeRevision string
		err := db.QueryRowContext(ctx, `select trim(structure_revision) from `+state.stateTable+`
			where rollup_name = ?`, state.stateName).Scan(&activeRevision)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read active derived revision %s from %s: %w", state.stateName, state.stateTable, err)
		}
		if activeRevision == "" {
			continue
		}
		for _, tableName := range state.tableNames {
			targets = append(targets, derivedCleanupTarget{
				name:           tableName + "_revision",
				tableName:      tableName,
				revisionColumn: state.revisionColumn,
				activeRevision: activeRevision,
				stateTable:     state.stateTable,
				stateName:      state.stateName,
			})
		}
	}
	return targets, nil
}

func cleanupStaleDerivedTargetBatch(
	ctx context.Context,
	db *sql.DB,
	target derivedCleanupTarget,
	limit int,
) (int64, bool, error) {
	if limit <= 0 {
		limit = derivedCleanupBatchLimit
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = tx.Rollback() }()
	activeRevision := target.activeRevision
	if target.stateTable != "" {
		err := tx.QueryRowContext(ctx, `select trim(structure_revision) from `+target.stateTable+`
			where rollup_name = ?`, target.stateName).Scan(&activeRevision)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		if err != nil {
			return 0, false, err
		}
		if activeRevision == "" {
			return 0, false, nil
		}
	}

	var rootPage int64
	if err := tx.QueryRowContext(ctx, `select rootpage from sqlite_master
		where type = 'table' and name = ?`, target.tableName).Scan(&rootPage); err != nil {
		return 0, false, err
	}
	revisionToken := fmt.Sprintf("%s\x00%d", activeRevision, rootPage)
	lastRowID := int64(0)
	var storedToken string
	err = tx.QueryRowContext(ctx, `select revision_token, last_rowid
		from usage_derived_cleanup_cursors where target_name = ?`, target.name).Scan(&storedToken, &lastRowID)
	if errors.Is(err, sql.ErrNoRows) {
		lastRowID = 0
	} else if err != nil {
		return 0, false, err
	} else if storedToken != revisionToken {
		lastRowID = 0
	}

	rows, err := tx.QueryContext(ctx, `select rowid from `+target.tableName+`
		where rowid > ? order by rowid limit ?`, lastRowID, limit)
	if err != nil {
		return 0, false, err
	}
	scanned := 0
	throughRowID := lastRowID
	for rows.Next() {
		if err := rows.Scan(&throughRowID); err != nil {
			_ = rows.Close()
			return 0, false, err
		}
		scanned++
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, false, err
	}
	if err := rows.Close(); err != nil {
		return 0, false, err
	}

	processed := int64(0)
	if scanned > 0 {
		result, err := tx.ExecContext(ctx, `delete from `+target.tableName+`
			where rowid > ? and rowid <= ? and `+target.revisionColumn+` <> ?`,
			lastRowID,
			throughRowID,
			activeRevision,
		)
		if err != nil {
			return 0, false, err
		}
		processed, err = result.RowsAffected()
		if err != nil {
			return 0, false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `insert into usage_derived_cleanup_cursors (
		target_name, table_name, revision_token, last_rowid, updated_at_ms
	) values (?, ?, ?, ?, ?)
		on conflict(target_name) do update set
			table_name = excluded.table_name,
			revision_token = excluded.revision_token,
			last_rowid = excluded.last_rowid,
			updated_at_ms = excluded.updated_at_ms`,
		target.name,
		target.tableName,
		revisionToken,
		throughRowID,
		time.Now().UnixMilli(),
	); err != nil {
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, err
	}
	return processed, scanned > 0, nil
}

func deleteDerivedRows(ctx context.Context, db *sql.DB, tableName, condition string, args []any, limit int) (int64, error) {
	whereClause := ""
	if condition != "" {
		whereClause = " where " + condition
	}
	query := `delete from ` + tableName + ` where rowid in (
		select target.rowid from ` + tableName + ` as target` + whereClause + ` limit ?
	)`
	queryArgs := append(append([]any{}, args...), limit)
	result, err := db.ExecContext(ctx, query, queryArgs...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func waitDerivedMaintenance(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
