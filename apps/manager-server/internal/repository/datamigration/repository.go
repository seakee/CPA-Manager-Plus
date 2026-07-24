package datamigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const UsageCacheAccountingMigrationName = "usage_cache_accounting_v2"

const usageCacheAccountingCandidatePredicate = `(coalesce(cached_tokens, 0) != 0
	or coalesce(cache_tokens, 0) != 0
	or coalesce(cache_read_tokens, 0) != 0
	or coalesce(cache_creation_tokens, 0) != 0
	or lower(trim(coalesce(cache_input_mode, ''))) not in ('included_in_input', 'separate_from_input')
	or normalized_uncached_input_tokens is null
	or normalized_total_input_tokens is null
	or normalized_cache_read_tokens is null
	or normalized_cache_creation_tokens is null)`

const (
	StatusDiscovering = "discovering"
	StatusPending     = "pending"
	StatusRunning     = "running"
	StatusCompleted   = "completed"
	StatusFailed      = "failed"
)

type State struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	LastEventID   int64  `json:"lastEventId"`
	TargetEventID int64  `json:"targetEventId"`
	ProcessedRows int64  `json:"processedRows"`
	ChangedRows   int64  `json:"changedRows"`
	StartedAtMS   int64  `json:"startedAtMs,omitempty"`
	UpdatedAtMS   int64  `json:"updatedAtMs"`
	FinishedAtMS  int64  `json:"finishedAtMs,omitempty"`
	LastError     string `json:"lastError,omitempty"`
}

type BatchResult struct {
	State     State
	Processed int64
	Completed bool
}

type Repository interface {
	UsageCacheAccountingState(ctx context.Context) (State, bool, error)
	DiscoverUsageCacheAccounting(ctx context.Context) (State, error)
	RunUsageCacheAccountingBatch(ctx context.Context, batchSize int) (BatchResult, error)
	RecordUsageCacheAccountingFailure(ctx context.Context, err error) error
}

type repository struct {
	db *sql.DB
}

type cacheAccountingRow struct {
	ID                      int64
	Provider                string
	ExecutorType            string
	ProviderSnapshot        string
	ResolvedModel           string
	RequestedModel          string
	DisplayModel            string
	StoredMode              sql.NullString
	InputTokens             int64
	OutputTokens            int64
	ReasoningTokens         int64
	CachedTokens            int64
	CacheTokens             int64
	CacheReadTokens         int64
	CacheCreationTokens     int64
	NormalizedUncachedInput sql.NullInt64
	NormalizedTotalInput    sql.NullInt64
	NormalizedCacheRead     sql.NullInt64
	NormalizedCacheCreation sql.NullInt64
	TotalTokens             int64
	RawJSON                 string
}

func New(db *sql.DB) Repository {
	return &repository{db: db}
}

func (r *repository) UsageCacheAccountingState(ctx context.Context) (State, bool, error) {
	state, err := readState(r.db.QueryRowContext(ctx, `select
		name, status, last_event_id, target_event_id, processed_rows, changed_rows,
		started_at_ms, updated_at_ms, finished_at_ms, last_error
	from usage_data_migrations
	where name = ?`, UsageCacheAccountingMigrationName))
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	return state, true, nil
}

func (r *repository) DiscoverUsageCacheAccounting(ctx context.Context) (State, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return State{}, err
	}
	defer func() { _ = tx.Rollback() }()

	state, err := stateInTx(ctx, tx)
	if err != nil {
		return State{}, err
	}
	if state.Status == StatusFailed {
		nowMS := time.Now().UnixMilli()
		resumeStatus := StatusPending
		if state.TargetEventID == 0 && state.LastEventID == 0 && state.ProcessedRows == 0 && state.ChangedRows == 0 {
			resumeStatus = StatusDiscovering
		}
		if _, err := tx.ExecContext(ctx, `update usage_data_migrations set
			status = ?, updated_at_ms = ?, last_error = null
		where name = ?`, resumeStatus, nowMS, UsageCacheAccountingMigrationName); err != nil {
			return State{}, err
		}
		state.Status = resumeStatus
		state.UpdatedAtMS = nowMS
		state.LastError = ""
		if resumeStatus == StatusPending {
			if err := tx.Commit(); err != nil {
				return State{}, err
			}
			return state, nil
		}
	}
	if state.Status == StatusCompleted || state.Status == StatusPending || state.Status == StatusRunning {
		return state, nil
	}
	if state.Status != StatusDiscovering {
		return State{}, fmt.Errorf("invalid usage cache accounting migration status %q", state.Status)
	}
	if _, err := tx.ExecContext(ctx, `delete from usage_cache_accounting_v2_changes`); err != nil {
		return State{}, err
	}

	var targetEventID int64
	if err := tx.QueryRowContext(ctx, `select coalesce(max(id), 0)
	from usage_events
	where `+usageCacheAccountingCandidatePredicate).Scan(&targetEventID); err != nil {
		return State{}, err
	}
	nowMS := time.Now().UnixMilli()
	if targetEventID == 0 {
		if _, err := tx.ExecContext(ctx, `update usage_data_migrations set
			status = ?, last_event_id = 0, target_event_id = 0, processed_rows = 0, changed_rows = 0,
			started_at_ms = null, updated_at_ms = ?, finished_at_ms = ?, last_error = null
		where name = ?`, StatusCompleted, nowMS, nowMS, UsageCacheAccountingMigrationName); err != nil {
			return State{}, err
		}
		if err := tx.Commit(); err != nil {
			return State{}, err
		}
		return State{Name: UsageCacheAccountingMigrationName, Status: StatusCompleted, UpdatedAtMS: nowMS, FinishedAtMS: nowMS}, nil
	}

	if _, err := tx.ExecContext(ctx, `update usage_data_migrations set
		status = ?, last_event_id = 0, target_event_id = ?, processed_rows = 0, changed_rows = 0,
		started_at_ms = ?, updated_at_ms = ?, finished_at_ms = null, last_error = null
	where name = ?`, StatusPending, targetEventID, nowMS, nowMS, UsageCacheAccountingMigrationName); err != nil {
		return State{}, err
	}
	if err := tx.Commit(); err != nil {
		return State{}, err
	}
	return State{
		Name:          UsageCacheAccountingMigrationName,
		Status:        StatusPending,
		TargetEventID: targetEventID,
		StartedAtMS:   nowMS,
		UpdatedAtMS:   nowMS,
	}, nil
}

func (r *repository) RunUsageCacheAccountingBatch(ctx context.Context, batchSize int) (BatchResult, error) {
	if batchSize <= 0 {
		batchSize = 1000
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return BatchResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	state, err := stateInTx(ctx, tx)
	if err != nil {
		return BatchResult{}, err
	}
	switch state.Status {
	case StatusCompleted:
		return BatchResult{State: state, Completed: true}, nil
	case StatusDiscovering:
		return BatchResult{}, errors.New("usage cache accounting migration has not been discovered")
	case StatusFailed:
		return BatchResult{}, errors.New("usage cache accounting migration failure must be resumed before running a batch")
	case StatusPending, StatusRunning:
		// Continue below.
	default:
		return BatchResult{}, fmt.Errorf("invalid usage cache accounting migration status %q", state.Status)
	}
	if state.TargetEventID <= state.LastEventID {
		completed, err := completeInTx(ctx, tx, state)
		if err != nil {
			return BatchResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return BatchResult{}, err
		}
		return BatchResult{State: completed, Completed: true}, nil
	}

	rows, err := readCacheAccountingBatch(ctx, tx, state.LastEventID, state.TargetEventID, batchSize)
	if err != nil {
		return BatchResult{}, err
	}
	if len(rows) == 0 {
		completed, err := completeInTx(ctx, tx, state)
		if err != nil {
			return BatchResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return BatchResult{}, err
		}
		return BatchResult{State: completed, Completed: true}, nil
	}

	changedRows := int64(0)
	for _, row := range rows {
		changed, err := stageCacheAccountingRow(ctx, tx, row)
		if err != nil {
			return BatchResult{}, err
		}
		if changed {
			changedRows++
		}
	}

	nowMS := time.Now().UnixMilli()
	processed := int64(len(rows))
	state.LastEventID = rows[len(rows)-1].ID
	state.ProcessedRows += processed
	state.ChangedRows += changedRows
	state.Status = StatusRunning
	state.UpdatedAtMS = nowMS
	state.LastError = ""
	if state.LastEventID >= state.TargetEventID {
		completed, err := completeInTx(ctx, tx, state)
		if err != nil {
			return BatchResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return BatchResult{}, err
		}
		return BatchResult{State: completed, Processed: processed, Completed: true}, nil
	}
	if _, err := tx.ExecContext(ctx, `update usage_data_migrations set
		status = ?, last_event_id = ?, processed_rows = ?, changed_rows = ?, updated_at_ms = ?, last_error = null
	where name = ?`, state.Status, state.LastEventID, state.ProcessedRows, state.ChangedRows, nowMS, UsageCacheAccountingMigrationName); err != nil {
		return BatchResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return BatchResult{}, err
	}
	return BatchResult{State: state, Processed: processed}, nil
}

func (r *repository) RecordUsageCacheAccountingFailure(ctx context.Context, migrationErr error) error {
	message := "unknown migration error"
	if migrationErr != nil {
		message = migrationErr.Error()
	}
	_, err := r.db.ExecContext(ctx, `update usage_data_migrations set
		status = ?, updated_at_ms = ?, last_error = ?
	where name = ? and status in (?, ?, ?, ?)`,
		StatusFailed,
		time.Now().UnixMilli(),
		message,
		UsageCacheAccountingMigrationName,
		StatusDiscovering,
		StatusPending,
		StatusRunning,
		StatusFailed,
	)
	return err
}

func readCacheAccountingBatch(ctx context.Context, tx *sql.Tx, lastEventID, targetEventID int64, batchSize int) ([]cacheAccountingRow, error) {
	result, err := tx.QueryContext(ctx, `select
		id, coalesce(provider, ''), coalesce(executor_type, ''), coalesce(auth_provider_snapshot, ''),
		coalesce(resolved_model, ''), coalesce(requested_model, ''), model, cache_input_mode,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_tokens,
		cache_read_tokens, cache_creation_tokens,
		normalized_uncached_input_tokens, normalized_total_input_tokens,
		normalized_cache_read_tokens, normalized_cache_creation_tokens,
		total_tokens, coalesce(raw_json, '')
	from usage_events
	where id > ? and id <= ?
		and `+usageCacheAccountingCandidatePredicate+`
	order by id
	limit ?`, lastEventID, targetEventID, batchSize)
	if err != nil {
		return nil, err
	}
	defer result.Close()

	rows := make([]cacheAccountingRow, 0, batchSize)
	for result.Next() {
		var row cacheAccountingRow
		if err := result.Scan(
			&row.ID,
			&row.Provider,
			&row.ExecutorType,
			&row.ProviderSnapshot,
			&row.ResolvedModel,
			&row.RequestedModel,
			&row.DisplayModel,
			&row.StoredMode,
			&row.InputTokens,
			&row.OutputTokens,
			&row.ReasoningTokens,
			&row.CachedTokens,
			&row.CacheTokens,
			&row.CacheReadTokens,
			&row.CacheCreationTokens,
			&row.NormalizedUncachedInput,
			&row.NormalizedTotalInput,
			&row.NormalizedCacheRead,
			&row.NormalizedCacheCreation,
			&row.TotalTokens,
			&row.RawJSON,
		); err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	if err := result.Err(); err != nil {
		return nil, err
	}
	if err := result.Close(); err != nil {
		return nil, err
	}
	return rows, nil
}

func stageCacheAccountingRow(ctx context.Context, tx *sql.Tx, row cacheAccountingRow) (bool, error) {
	hints := usage.RawCacheAccountingHintsFromJSON(row.RawJSON)
	context := usage.CacheInputContext{
		ExplicitMode:     hints.ExplicitMode,
		ExecutorType:     row.ExecutorType,
		Provider:         row.Provider,
		ProviderSnapshot: row.ProviderSnapshot,
		ResolvedModel:    row.ResolvedModel,
		RequestedModel:   row.RequestedModel,
		DisplayModel:     row.DisplayModel,
	}
	accounting := usage.NormalizeCacheAccounting(
		context,
		row.InputTokens,
		row.CachedTokens,
		row.CacheTokens,
		row.CacheReadTokens,
		row.CacheCreationTokens,
	)
	correctedTotal := correctedDerivedTotal(row, hints, accounting)
	changed := row.StoredMode.String != accounting.Mode ||
		!equalNullableInt(row.NormalizedUncachedInput, accounting.UncachedInputTokens) ||
		!equalNullableInt(row.NormalizedTotalInput, accounting.TotalInputTokens) ||
		!equalNullableInt(row.NormalizedCacheRead, accounting.CacheReadTokens) ||
		!equalNullableInt(row.NormalizedCacheCreation, accounting.CacheCreationTokens) ||
		row.TotalTokens != correctedTotal
	if !changed {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `insert into usage_cache_accounting_v2_changes (
		event_id, cache_input_mode, normalized_uncached_input_tokens,
		normalized_total_input_tokens, normalized_cache_read_tokens,
		normalized_cache_creation_tokens, total_tokens
	) values (?, ?, ?, ?, ?, ?, ?)
		on conflict(event_id) do update set
			cache_input_mode = excluded.cache_input_mode,
			normalized_uncached_input_tokens = excluded.normalized_uncached_input_tokens,
			normalized_total_input_tokens = excluded.normalized_total_input_tokens,
			normalized_cache_read_tokens = excluded.normalized_cache_read_tokens,
			normalized_cache_creation_tokens = excluded.normalized_cache_creation_tokens,
			total_tokens = excluded.total_tokens`,
		row.ID,
		accounting.Mode,
		accounting.UncachedInputTokens,
		accounting.TotalInputTokens,
		accounting.CacheReadTokens,
		accounting.CacheCreationTokens,
		correctedTotal,
	); err != nil {
		return false, err
	}
	return true, nil
}

func correctedDerivedTotal(row cacheAccountingRow, hints usage.RawCacheAccountingHints, accounting usage.CacheAccounting) int64 {
	if !hints.ValidPayload || hints.HasExplicitTotal {
		return row.TotalTokens
	}
	oldTotalInput := int64(0)
	if row.NormalizedTotalInput.Valid {
		oldTotalInput = row.NormalizedTotalInput.Int64
	} else {
		oldTotalInput = usage.NormalizeCacheAccounting(
			usage.CacheInputContext{ExplicitMode: row.StoredMode.String},
			row.InputTokens,
			row.CachedTokens,
			row.CacheTokens,
			row.CacheReadTokens,
			row.CacheCreationTokens,
		).TotalInputTokens
	}
	oldDerived := oldTotalInput + max(row.OutputTokens, int64(0)) + max(row.ReasoningTokens, int64(0))
	if row.TotalTokens != oldDerived {
		return row.TotalTokens
	}
	return accounting.TotalInputTokens + max(row.OutputTokens, int64(0)) + max(row.ReasoningTokens, int64(0))
}

func equalNullableInt(value sql.NullInt64, want int64) bool {
	return value.Valid && value.Int64 == want
}

func stateInTx(ctx context.Context, tx *sql.Tx) (State, error) {
	return readState(tx.QueryRowContext(ctx, `select
		name, status, last_event_id, target_event_id, processed_rows, changed_rows,
		started_at_ms, updated_at_ms, finished_at_ms, last_error
	from usage_data_migrations
	where name = ?`, UsageCacheAccountingMigrationName))
}

type rowScanner interface {
	Scan(dest ...any) error
}

func readState(row rowScanner) (State, error) {
	var state State
	var startedAtMS, finishedAtMS sql.NullInt64
	var lastError sql.NullString
	if err := row.Scan(
		&state.Name,
		&state.Status,
		&state.LastEventID,
		&state.TargetEventID,
		&state.ProcessedRows,
		&state.ChangedRows,
		&startedAtMS,
		&state.UpdatedAtMS,
		&finishedAtMS,
		&lastError,
	); err != nil {
		return State{}, err
	}
	state.StartedAtMS = startedAtMS.Int64
	state.FinishedAtMS = finishedAtMS.Int64
	state.LastError = lastError.String
	return state, nil
}

func completeInTx(ctx context.Context, tx *sql.Tx, state State) (State, error) {
	if state.ChangedRows > 0 {
		for _, statement := range []string{
			`update usage_events set
				cache_input_mode = (select cache_input_mode from usage_cache_accounting_v2_changes where event_id = usage_events.id),
				normalized_uncached_input_tokens = (select normalized_uncached_input_tokens from usage_cache_accounting_v2_changes where event_id = usage_events.id),
				normalized_total_input_tokens = (select normalized_total_input_tokens from usage_cache_accounting_v2_changes where event_id = usage_events.id),
				normalized_cache_read_tokens = (select normalized_cache_read_tokens from usage_cache_accounting_v2_changes where event_id = usage_events.id),
				normalized_cache_creation_tokens = (select normalized_cache_creation_tokens from usage_cache_accounting_v2_changes where event_id = usage_events.id),
				total_tokens = (select total_tokens from usage_cache_accounting_v2_changes where event_id = usage_events.id)
			where id in (select event_id from usage_cache_accounting_v2_changes)`,
			`delete from usage_account_model_rollups`,
			`delete from usage_dashboard_hourly_rollups`,
			`update usage_rollup_checkpoints set last_event_id = 0, updated_at_ms = 0, last_error = null
					where name in ('account_history', 'dashboard_hourly')`,
			`delete from usage_hourly_aggregate_v1`,
			`update usage_event_identity_ledger set aggregate_schema_version = 0
					where aggregate_schema_version = 1`,
			`update usage_hourly_aggregate_state set
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
				where aggregate_name = 'hourly_core' and schema_version = 1`,
		} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return State{}, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `delete from usage_cache_accounting_v2_changes`); err != nil {
		return State{}, err
	}
	nowMS := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `update usage_data_migrations set
		status = ?, last_event_id = ?, processed_rows = ?, changed_rows = ?,
		updated_at_ms = ?, finished_at_ms = ?, last_error = null
	where name = ?`,
		StatusCompleted,
		state.TargetEventID,
		state.ProcessedRows,
		state.ChangedRows,
		nowMS,
		nowMS,
		UsageCacheAccountingMigrationName,
	); err != nil {
		return State{}, err
	}
	state.Status = StatusCompleted
	state.LastEventID = state.TargetEventID
	state.UpdatedAtMS = nowMS
	state.FinishedAtMS = nowMS
	state.LastError = ""
	return state, nil
}
