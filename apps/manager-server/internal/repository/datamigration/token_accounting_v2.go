package datamigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageaccountingsql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageaggregate"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagemonitoring"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagepricing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const UsageTokenAccountingMigrationName = "usage_token_accounting_v2"

var usageTokenAccountingCandidatePredicate = "not " + usageaccountingsql.For("").Ready

type tokenAccountingRow struct {
	ID                int64
	Provider          string
	ExecutorType      string
	ProviderSnapshot  string
	AuthType          string
	ResolvedModel     string
	RequestedModel    string
	DisplayModel      string
	AccountingVersion int
	AccountingValid   int
	AccountingQuality string
	InputTokens       int64
	OutputTokens      int64
	ReasoningTokens   int64
	CachedTokens      int64
	CacheTokens       int64
	CacheReadTokens   int64
	CacheCreation     int64
	TotalTokens       int64
	RawJSON           string
}

func (r *repository) UsageTokenAccountingState(ctx context.Context) (State, bool, error) {
	state, err := readState(r.db.QueryRowContext(ctx, `select
		name, status, last_event_id, target_event_id, processed_rows, changed_rows,
		started_at_ms, updated_at_ms, finished_at_ms, last_error
	from usage_data_migrations where name = ?`, UsageTokenAccountingMigrationName))
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	return state, true, nil
}

func tokenAccountingStateInTx(ctx context.Context, tx *sql.Tx) (State, error) {
	return readState(tx.QueryRowContext(ctx, `select
		name, status, last_event_id, target_event_id, processed_rows, changed_rows,
		started_at_ms, updated_at_ms, finished_at_ms, last_error
	from usage_data_migrations where name = ?`, UsageTokenAccountingMigrationName))
}

func (r *repository) DiscoverUsageTokenAccounting(ctx context.Context) (State, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return State{}, err
	}
	defer func() { _ = tx.Rollback() }()
	state, err := tokenAccountingStateInTx(ctx, tx)
	if err != nil {
		return State{}, err
	}
	if state.Status == StatusCompleted || state.Status == StatusPending || state.Status == StatusRunning {
		if err := tx.Commit(); err != nil {
			return State{}, err
		}
		return state, nil
	}
	if state.Status == StatusFailed {
		resume := StatusPending
		if state.TargetEventID == 0 && state.LastEventID == 0 && state.ProcessedRows == 0 {
			resume = StatusDiscovering
		}
		now := time.Now().UnixMilli()
		if _, err := tx.ExecContext(ctx, `update usage_data_migrations set status = ?, updated_at_ms = ?, last_error = null where name = ?`, resume, now, UsageTokenAccountingMigrationName); err != nil {
			return State{}, err
		}
		state.Status, state.UpdatedAtMS, state.LastError = resume, now, ""
		if resume == StatusPending {
			if err := tx.Commit(); err != nil {
				return State{}, err
			}
			return state, nil
		}
	}
	if state.Status != StatusDiscovering {
		return State{}, fmt.Errorf("unsupported token accounting migration status %q", state.Status)
	}
	if _, err := tx.ExecContext(ctx, `delete from usage_token_accounting_v2_changes`); err != nil {
		return State{}, err
	}
	var target int64
	if err := tx.QueryRowContext(ctx, `select coalesce(max(id), 0) from usage_events where `+usageTokenAccountingCandidatePredicate).Scan(&target); err != nil {
		return State{}, err
	}
	now := time.Now().UnixMilli()
	if target == 0 {
		if _, err := tx.ExecContext(ctx, `update usage_data_migrations set status = ?, last_event_id = 0, target_event_id = 0, processed_rows = 0, changed_rows = 0, started_at_ms = coalesce(started_at_ms, ?), updated_at_ms = ?, finished_at_ms = ?, last_error = null where name = ?`, StatusCompleted, now, now, now, UsageTokenAccountingMigrationName); err != nil {
			return State{}, err
		}
		state.Status, state.UpdatedAtMS, state.FinishedAtMS = StatusCompleted, now, now
		if err := tx.Commit(); err != nil {
			return State{}, err
		}
		return state, nil
	}
	if _, err := tx.ExecContext(ctx, `update usage_data_migrations set status = ?, last_event_id = 0, target_event_id = ?, processed_rows = 0, changed_rows = 0, started_at_ms = ?, updated_at_ms = ?, finished_at_ms = null, last_error = null where name = ?`, StatusPending, target, now, now, UsageTokenAccountingMigrationName); err != nil {
		return State{}, err
	}
	if err := tx.Commit(); err != nil {
		return State{}, err
	}
	return State{Name: UsageTokenAccountingMigrationName, Status: StatusPending, TargetEventID: target, StartedAtMS: now, UpdatedAtMS: now}, nil
}

func (r *repository) RunUsageTokenAccountingBatch(ctx context.Context, batchSize int) (BatchResult, error) {
	if batchSize <= 0 {
		batchSize = 1000
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return BatchResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	state, err := tokenAccountingStateInTx(ctx, tx)
	if err != nil {
		return BatchResult{}, err
	}
	if state.Status == StatusCompleted {
		if err := tx.Commit(); err != nil {
			return BatchResult{}, err
		}
		return BatchResult{State: state, Completed: true}, nil
	}
	if state.Status != StatusPending && state.Status != StatusRunning {
		return BatchResult{}, fmt.Errorf("token accounting migration is not runnable in status %q", state.Status)
	}
	rows, err := readTokenAccountingBatch(ctx, tx, state.LastEventID, state.TargetEventID, batchSize)
	if err != nil {
		return BatchResult{}, err
	}
	for _, row := range rows {
		rawHints := usage.RawCacheAccountingHintsFromJSON(row.RawJSON)
		event := usage.Event{
			Provider: row.Provider, ExecutorType: row.ExecutorType, AuthProviderSnapshot: row.ProviderSnapshot, AuthType: row.AuthType,
			ResolvedModel: row.ResolvedModel, RequestedModel: row.RequestedModel, Model: row.DisplayModel, CacheInputMode: rawHints.ExplicitMode,
			AccountingVersion: row.AccountingVersion, AccountingValid: row.AccountingValid != 0,
			TokenBreakdown: usage.TokenBreakdown{Quality: row.AccountingQuality},
			InputTokens:    row.InputTokens, OutputTokens: row.OutputTokens, ReasoningTokens: row.ReasoningTokens,
			CachedTokens: row.CachedTokens, CacheTokens: row.CacheTokens, CacheReadTokens: row.CacheReadTokens, CacheCreationTokens: row.CacheCreation,
			TotalTokens: row.TotalTokens, RawJSON: row.RawJSON,
		}
		if rawHints.ValidPayload {
			if !rawHints.HasExplicitTotal && !rawHints.HasInvalidExplicitTotal {
				event.TotalTokens = 0
			}
		}
		usage.ApplyTokenAccounting(&event, nil)
		valid := 0
		if event.AccountingValid {
			valid = 1
		}
		if _, err := tx.ExecContext(ctx, `insert into usage_token_accounting_v2_changes (
			event_id, cache_input_mode, accounting_version, accounting_valid, accounting_quality,
			normalized_uncached_input_tokens, normalized_total_input_tokens, normalized_cache_read_tokens, normalized_cache_creation_tokens,
			normalized_non_reasoning_output_tokens, normalized_reasoning_output_tokens, normalized_total_output_tokens, unclassified_tokens, total_tokens
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		on conflict(event_id) do update set
			cache_input_mode = excluded.cache_input_mode, accounting_version = excluded.accounting_version,
			accounting_valid = excluded.accounting_valid, accounting_quality = excluded.accounting_quality,
			normalized_uncached_input_tokens = excluded.normalized_uncached_input_tokens,
			normalized_total_input_tokens = excluded.normalized_total_input_tokens,
			normalized_cache_read_tokens = excluded.normalized_cache_read_tokens,
			normalized_cache_creation_tokens = excluded.normalized_cache_creation_tokens,
			normalized_non_reasoning_output_tokens = excluded.normalized_non_reasoning_output_tokens,
			normalized_reasoning_output_tokens = excluded.normalized_reasoning_output_tokens,
			normalized_total_output_tokens = excluded.normalized_total_output_tokens,
			unclassified_tokens = excluded.unclassified_tokens, total_tokens = excluded.total_tokens`,
			row.ID, event.CacheInputMode, event.AccountingVersion, valid, event.TokenBreakdown.Quality,
			event.NormalizedUncachedInputTokens, event.NormalizedTotalInputTokens, event.NormalizedCacheReadTokens, event.NormalizedCacheCreationTokens,
			event.NormalizedNonReasoningOutputTokens, event.NormalizedReasoningOutputTokens, event.NormalizedTotalOutputTokens, event.UnclassifiedTokens, event.TotalTokens); err != nil {
			return BatchResult{}, err
		}
		state.LastEventID = row.ID
		state.ProcessedRows++
		state.ChangedRows++
	}
	state.Status = StatusRunning
	if len(rows) == 0 || state.LastEventID >= state.TargetEventID {
		completed, err := completeTokenAccountingInTx(ctx, tx, state)
		if err != nil {
			return BatchResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return BatchResult{}, err
		}
		return BatchResult{State: completed, Processed: int64(len(rows)), Completed: true}, nil
	}
	now := time.Now().UnixMilli()
	state.UpdatedAtMS = now
	if _, err := tx.ExecContext(ctx, `update usage_data_migrations set status = ?, last_event_id = ?, processed_rows = ?, changed_rows = ?, updated_at_ms = ?, last_error = null where name = ?`, state.Status, state.LastEventID, state.ProcessedRows, state.ChangedRows, now, UsageTokenAccountingMigrationName); err != nil {
		return BatchResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return BatchResult{}, err
	}
	return BatchResult{State: state, Processed: int64(len(rows))}, nil
}

func (r *repository) RecordUsageTokenAccountingFailure(ctx context.Context, migrationErr error) error {
	message := "unknown migration error"
	if migrationErr != nil {
		message = migrationErr.Error()
	}
	_, err := r.db.ExecContext(ctx, `update usage_data_migrations set status = ?, updated_at_ms = ?, last_error = ? where name = ? and status in (?, ?, ?, ?)`, StatusFailed, time.Now().UnixMilli(), message, UsageTokenAccountingMigrationName, StatusDiscovering, StatusPending, StatusRunning, StatusFailed)
	return err
}

func readTokenAccountingBatch(ctx context.Context, tx *sql.Tx, lastEventID, targetEventID int64, batchSize int) ([]tokenAccountingRow, error) {
	rows, err := tx.QueryContext(ctx, `select id, coalesce(provider, ''), coalesce(executor_type, ''), coalesce(auth_provider_snapshot, ''), coalesce(auth_type, ''), coalesce(resolved_model, ''), coalesce(requested_model, ''), model, coalesce(accounting_version, 0), coalesce(accounting_valid, 0), coalesce(accounting_quality, ''), input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_tokens, cache_read_tokens, cache_creation_tokens, total_tokens, coalesce(raw_json, '') from usage_events where id > ? and id <= ? and `+usageTokenAccountingCandidatePredicate+` order by id limit ?`, lastEventID, targetEventID, batchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]tokenAccountingRow, 0, batchSize)
	for rows.Next() {
		var row tokenAccountingRow
		if err := rows.Scan(&row.ID, &row.Provider, &row.ExecutorType, &row.ProviderSnapshot, &row.AuthType, &row.ResolvedModel, &row.RequestedModel, &row.DisplayModel, &row.AccountingVersion, &row.AccountingValid, &row.AccountingQuality, &row.InputTokens, &row.OutputTokens, &row.ReasoningTokens, &row.CachedTokens, &row.CacheTokens, &row.CacheReadTokens, &row.CacheCreation, &row.TotalTokens, &row.RawJSON); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func completeTokenAccountingInTx(ctx context.Context, tx *sql.Tx, state State) (State, error) {
	now := time.Now().UnixMilli()
	if state.ChangedRows > 0 {
		if err := validateSupportedUsageDerivedSchemasInTx(ctx, tx); err != nil {
			return State{}, err
		}
		if _, err := tx.ExecContext(ctx, `update usage_events set
			cache_input_mode = (select cache_input_mode from usage_token_accounting_v2_changes where event_id = usage_events.id),
			accounting_version = (select accounting_version from usage_token_accounting_v2_changes where event_id = usage_events.id),
			accounting_valid = (select accounting_valid from usage_token_accounting_v2_changes where event_id = usage_events.id),
			accounting_quality = (select accounting_quality from usage_token_accounting_v2_changes where event_id = usage_events.id),
			normalized_uncached_input_tokens = (select normalized_uncached_input_tokens from usage_token_accounting_v2_changes where event_id = usage_events.id),
			normalized_total_input_tokens = (select normalized_total_input_tokens from usage_token_accounting_v2_changes where event_id = usage_events.id),
			normalized_cache_read_tokens = (select normalized_cache_read_tokens from usage_token_accounting_v2_changes where event_id = usage_events.id),
			normalized_cache_creation_tokens = (select normalized_cache_creation_tokens from usage_token_accounting_v2_changes where event_id = usage_events.id),
			normalized_non_reasoning_output_tokens = (select normalized_non_reasoning_output_tokens from usage_token_accounting_v2_changes where event_id = usage_events.id),
			normalized_reasoning_output_tokens = (select normalized_reasoning_output_tokens from usage_token_accounting_v2_changes where event_id = usage_events.id),
			normalized_total_output_tokens = (select normalized_total_output_tokens from usage_token_accounting_v2_changes where event_id = usage_events.id),
			unclassified_tokens = (select unclassified_tokens from usage_token_accounting_v2_changes where event_id = usage_events.id),
			total_tokens = (select total_tokens from usage_token_accounting_v2_changes where event_id = usage_events.id)
		where id in (select event_id from usage_token_accounting_v2_changes)`); err != nil {
			return State{}, err
		}
		for _, statement := range []string{
			`delete from usage_account_model_rollups`,
			`delete from usage_dashboard_hourly_rollups`,
			`delete from usage_hourly_aggregate_v1`,
			`delete from usage_pricing_hourly_rollups_v1`,
			`delete from usage_pricing_account_rollups_v1`,
			`delete from usage_monitoring_account_daily_rollups_v1`,
			`delete from usage_monitoring_api_key_daily_rollups_v1`,
			`update usage_rollup_checkpoints set last_event_id = 0, updated_at_ms = 0, last_error = null where name in ('account_history', 'dashboard_hourly')`,
			fmt.Sprintf(`update usage_event_identity_ledger set aggregate_schema_version = 0 where aggregate_schema_version = %d`, usageaggregate.SchemaVersion),
			fmt.Sprintf(`update usage_hourly_aggregate_state set status = case when exists (select 1 from usage_events limit 1) then 'pending' else 'ready' end, backfill_last_event_id = 0, coverage_event_id = 0, target_event_id = coalesce((select max(id) from usage_events), 0), processed_events = 0, min_bucket_ms = null, max_bucket_ms = null, last_run_started_at_ms = null, updated_at_ms = 0, finished_at_ms = null, last_error = null where aggregate_name = 'hourly_core' and schema_version = %d`, usageaggregate.SchemaVersion),
			fmt.Sprintf(`update usage_pricing_rollup_state set status = case when exists (select 1 from usage_events limit 1) then 'pending' else 'ready' end, backfill_last_event_id = 0, coverage_event_id = 0, target_event_id = coalesce((select max(id) from usage_events), 0), processed_events = 0, min_bucket_ms = null, max_bucket_ms = null, last_run_started_at_ms = null, updated_at_ms = 0, finished_at_ms = null, last_error = null where rollup_name = 'pricing_v1' and schema_version = %d`, usagepricing.SchemaVersion),
			fmt.Sprintf(`update usage_monitoring_rollup_state set status = case when exists (select 1 from usage_events limit 1) then 'pending' else 'ready' end, backfill_last_event_id = 0, coverage_event_id = 0, target_event_id = coalesce((select max(id) from usage_events), 0), processed_events = 0, last_run_started_at_ms = null, updated_at_ms = 0, finished_at_ms = null, last_error = null where rollup_name = 'stats_v1' and schema_version = %d`, usagemonitoring.SchemaVersion),
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return State{}, err
			}
		}
		eventIDs, err := stagedTokenAccountingEventIDs(ctx, tx)
		if err != nil {
			return State{}, err
		}
		if err := usageprojection.UpsertEventIDs(ctx, tx, eventIDs, now); err != nil {
			return State{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `delete from usage_token_accounting_v2_changes`); err != nil {
		return State{}, err
	}
	if _, err := tx.ExecContext(ctx, `update usage_data_migrations set status = ?, last_event_id = ?, processed_rows = ?, changed_rows = ?, updated_at_ms = ?, finished_at_ms = ?, last_error = null where name = ?`, StatusCompleted, state.TargetEventID, state.ProcessedRows, state.ChangedRows, now, now, UsageTokenAccountingMigrationName); err != nil {
		return State{}, err
	}
	state.Status, state.LastEventID, state.UpdatedAtMS, state.FinishedAtMS, state.LastError = StatusCompleted, state.TargetEventID, now, now, ""
	return state, nil
}

func validateSupportedUsageDerivedSchemasInTx(ctx context.Context, tx *sql.Tx) error {
	checks := []struct {
		label     string
		query     string
		name      string
		supported int
	}{
		{
			label:     "usage hourly aggregate",
			query:     `select schema_version from usage_hourly_aggregate_state where aggregate_name = ?`,
			name:      usageaggregate.AggregateName,
			supported: usageaggregate.SchemaVersion,
		},
		{
			label:     "usage pricing rollup",
			query:     `select schema_version from usage_pricing_rollup_state where rollup_name = ?`,
			name:      usagepricing.RollupName,
			supported: usagepricing.SchemaVersion,
		},
	}
	for _, check := range checks {
		var version int
		if err := tx.QueryRowContext(ctx, check.query, check.name).Scan(&version); err != nil {
			return fmt.Errorf("inspect %s schema version: %w", check.label, err)
		}
		if version > check.supported {
			return fmt.Errorf("unsupported %s schema version %d", check.label, version)
		}
	}

	var monitoringName string
	var monitoringVersion int
	err := tx.QueryRowContext(ctx, `select rollup_name, schema_version
		from usage_monitoring_rollup_state
		where schema_version > ?
		order by rollup_name
		limit 1`, usagemonitoring.SchemaVersion).Scan(&monitoringName, &monitoringVersion)
	if err == nil {
		return fmt.Errorf("unsupported usage monitoring rollup schema version %s=%d", monitoringName, monitoringVersion)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect usage monitoring rollup schema versions: %w", err)
	}

	var futureIdentityVersion int
	if err := tx.QueryRowContext(ctx, `select coalesce(max(aggregate_schema_version), 0)
		from usage_event_identity_ledger`).Scan(&futureIdentityVersion); err != nil {
		return fmt.Errorf("inspect usage event identity schema versions: %w", err)
	}
	if futureIdentityVersion > usageaggregate.SchemaVersion {
		return fmt.Errorf("unsupported usage event identity aggregate schema version %d", futureIdentityVersion)
	}
	return nil
}

func stagedTokenAccountingEventIDs(ctx context.Context, tx *sql.Tx) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `select event_id from usage_token_accounting_v2_changes order by event_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	eventIDs := make([]int64, 0)
	for rows.Next() {
		var eventID int64
		if err := rows.Scan(&eventID); err != nil {
			return nil, err
		}
		eventIDs = append(eventIDs, eventID)
	}
	return eventIDs, rows.Err()
}
