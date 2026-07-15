package datamigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// UsageCacheAccountingMigrationName identifies the resumable backfill that
// normalizes cache accounting with field-priority semantics (executor first).
// v2 reclassifies rows previously inferred by the concatenated-identity rules.
const UsageCacheAccountingMigrationName = "usage_cache_accounting_v2"

const (
	StatusDiscovering = "discovering"
	StatusPending     = "pending"
	StatusRunning     = "running"
	StatusCompleted   = "completed"
	StatusFailed      = "failed"
)

// inferCacheInputModeSQL mirrors usage.InferCacheInputMode field priority:
// executor_type → provider/auth_provider_snapshot → resolved_model/model → fine-grained fallback.
// Explicit cache_input_mode is handled by callers (only fill null / repair misclassified).
func inferCacheInputModeSQL() string {
	executor := `lower(coalesce(executor_type, ''))`
	provider := `lower(coalesce(provider, '') || ' ' || coalesce(auth_provider_snapshot, ''))`
	model := `lower(coalesce(resolved_model, '') || ' ' || coalesce(model, ''))`
	return `case
		when ` + executor + ` like '%claude%' then 'separate_from_input'
		when ` + executor + ` like '%openaicompat%'
			or ` + executor + ` like '%openai_compat%'
			or ` + executor + ` like '%openai-compat%'
			or ` + executor + ` like '%openai%'
			or ` + executor + ` like '%codex%'
			or ` + executor + ` like '%gemini%'
			or ` + executor + ` like '%aistudio%'
			or ` + executor + ` like '%ai_studio%'
			or ` + executor + ` like '%antigravity%'
			or ` + executor + ` like '%xai%'
			or ` + executor + ` like '%kimi%'
			then 'included_in_input'
		when ` + provider + ` like '%anthropic%'
			or ` + provider + ` like '%claude%'
			then 'separate_from_input'
		when ` + provider + ` like '%openai%'
			or ` + provider + ` like '%codex%'
			or ` + provider + ` like '%gemini%'
			or ` + provider + ` like '%antigravity%'
			or ` + provider + ` like '%interaction%'
			or ` + provider + ` like '%xai%'
			or ` + provider + ` like '%kimi%'
			or ` + provider + ` like '%moonshot%'
			then 'included_in_input'
		when ` + model + ` like '%anthropic%'
			or ` + model + ` like '%claude%'
			then 'separate_from_input'
		when ` + model + ` like '%gpt-%'
			or ` + model + ` like '%openai%'
			or ` + model + ` like '%codex%'
			or ` + model + ` like '%gemini%'
			or ` + model + ` like '%antigravity%'
			or ` + model + ` like '%grok%'
			or ` + model + ` like '%xai%'
			or ` + model + ` like '%kimi%'
			or ` + model + ` like '%moonshot%'
			then 'included_in_input'
		when coalesce(cache_read_tokens, 0) > 0 or coalesce(cache_creation_tokens, 0) > 0 then 'separate_from_input'
		else 'included_in_input'
	end`
}

func incompleteUsageCacheAccountingPredicate() string {
	infer := inferCacheInputModeSQL()
	return `(cache_input_mode is null
	or trim(cache_input_mode) = ''
	or normalized_uncached_input_tokens is null
	or normalized_total_input_tokens is null
	or normalized_cache_read_tokens is null
	or normalized_cache_creation_tokens is null
	or (coalesce(cache_input_mode, '') = 'separate_from_input' and (` + infer + `) = 'included_in_input'))`
}

type State struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	LastEventID   int64  `json:"lastEventId"`
	TargetEventID int64  `json:"targetEventId"`
	ProcessedRows int64  `json:"processedRows"`
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

func New(db *sql.DB) Repository {
	return &repository{db: db}
}

func (r *repository) UsageCacheAccountingState(ctx context.Context) (State, bool, error) {
	var state State
	var startedAtMS, finishedAtMS sql.NullInt64
	var lastError sql.NullString
	err := r.db.QueryRowContext(ctx, `select
		name, status, last_event_id, target_event_id, processed_rows,
		started_at_ms, updated_at_ms, finished_at_ms, last_error
	from usage_data_migrations
	where name = ?`, UsageCacheAccountingMigrationName).Scan(
		&state.Name,
		&state.Status,
		&state.LastEventID,
		&state.TargetEventID,
		&state.ProcessedRows,
		&startedAtMS,
		&state.UpdatedAtMS,
		&finishedAtMS,
		&lastError,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	state.StartedAtMS = startedAtMS.Int64
	state.FinishedAtMS = finishedAtMS.Int64
	state.LastError = lastError.String
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
		if state.TargetEventID == 0 && state.LastEventID == 0 && state.ProcessedRows == 0 {
			if _, err := tx.ExecContext(ctx, `update usage_data_migrations set
				status = ?, updated_at_ms = ?, last_error = null
			where name = ?`, StatusDiscovering, nowMS, UsageCacheAccountingMigrationName); err != nil {
				return State{}, err
			}
			state.Status = StatusDiscovering
			state.UpdatedAtMS = nowMS
			state.LastError = ""
		} else {
			if _, err := tx.ExecContext(ctx, `update usage_data_migrations set
				status = ?, updated_at_ms = ?, last_error = null
			where name = ?`, StatusPending, nowMS, UsageCacheAccountingMigrationName); err != nil {
				return State{}, err
			}
			state.Status = StatusPending
			state.UpdatedAtMS = nowMS
			state.LastError = ""
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

	var firstIncompleteID int64
	err = tx.QueryRowContext(ctx, `select id
	from usage_events
	where `+incompleteUsageCacheAccountingPredicate()+`
	order by id
	limit 1`).Scan(&firstIncompleteID)
	if errors.Is(err, sql.ErrNoRows) {
		nowMS := time.Now().UnixMilli()
		if _, err := tx.ExecContext(ctx, `update usage_data_migrations set
			status = ?, updated_at_ms = ?, finished_at_ms = ?, last_error = null
		where name = ?`, StatusCompleted, nowMS, nowMS, UsageCacheAccountingMigrationName); err != nil {
			return State{}, err
		}
		if err := tx.Commit(); err != nil {
			return State{}, err
		}
		return State{
			Name:         UsageCacheAccountingMigrationName,
			Status:       StatusCompleted,
			UpdatedAtMS:  nowMS,
			FinishedAtMS: nowMS,
		}, nil
	}
	if err != nil {
		return State{}, err
	}

	var targetEventID int64
	if err := tx.QueryRowContext(ctx, `select coalesce(max(id), 0) from usage_events`).Scan(&targetEventID); err != nil {
		return State{}, err
	}
	lastEventID := firstIncompleteID - 1
	if lastEventID < 0 {
		lastEventID = 0
	}
	nowMS := time.Now().UnixMilli()
	for _, statement := range []string{
		`delete from usage_account_model_rollups`,
		`delete from usage_dashboard_hourly_rollups`,
		`update usage_rollup_checkpoints set last_event_id = 0, updated_at_ms = 0, last_error = null`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return State{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `update usage_data_migrations set
		status = ?, last_event_id = ?, target_event_id = ?, processed_rows = 0,
		started_at_ms = ?, updated_at_ms = ?, finished_at_ms = null, last_error = null
	where name = ?`, StatusPending, lastEventID, targetEventID, nowMS, nowMS, UsageCacheAccountingMigrationName); err != nil {
		return State{}, err
	}
	if err := tx.Commit(); err != nil {
		return State{}, err
	}
	return State{
		Name:          UsageCacheAccountingMigrationName,
		Status:        StatusPending,
		LastEventID:   lastEventID,
		TargetEventID: targetEventID,
		ProcessedRows: 0,
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

	var firstEventID, lastEventID, count int64
	if err := tx.QueryRowContext(ctx, `select coalesce(min(id), 0), coalesce(max(id), 0), count(*)
	from (
		select id
		from usage_events
		where id > ? and id <= ?
		order by id
		limit ?
	)`, state.LastEventID, state.TargetEventID, batchSize).Scan(&firstEventID, &lastEventID, &count); err != nil {
		return BatchResult{}, err
	}
	if count == 0 {
		completed, err := completeInTx(ctx, tx, state)
		if err != nil {
			return BatchResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return BatchResult{}, err
		}
		return BatchResult{State: completed, Completed: true}, nil
	}

	if err := updateCacheAccountingInRange(ctx, tx, firstEventID, lastEventID); err != nil {
		return BatchResult{}, err
	}
	nowMS := time.Now().UnixMilli()
	state.LastEventID = lastEventID
	state.ProcessedRows += count
	state.Status = StatusRunning
	state.UpdatedAtMS = nowMS
	state.LastError = ""
	if state.LastEventID >= state.TargetEventID {
		state.Status = StatusCompleted
		state.FinishedAtMS = nowMS
		if _, err := tx.ExecContext(ctx, `update usage_data_migrations set
			status = ?, last_event_id = ?, processed_rows = ?, updated_at_ms = ?, finished_at_ms = ?, last_error = null
		where name = ?`, state.Status, state.LastEventID, state.ProcessedRows, nowMS, nowMS, UsageCacheAccountingMigrationName); err != nil {
			return BatchResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return BatchResult{}, err
		}
		return BatchResult{State: state, Processed: count, Completed: true}, nil
	}
	if _, err := tx.ExecContext(ctx, `update usage_data_migrations set
		status = ?, last_event_id = ?, processed_rows = ?, updated_at_ms = ?, last_error = null
	where name = ?`, state.Status, state.LastEventID, state.ProcessedRows, nowMS, UsageCacheAccountingMigrationName); err != nil {
		return BatchResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return BatchResult{}, err
	}
	return BatchResult{State: state, Processed: count}, nil
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

func stateInTx(ctx context.Context, tx *sql.Tx) (State, error) {
	var state State
	var startedAtMS, finishedAtMS sql.NullInt64
	var lastError sql.NullString
	if err := tx.QueryRowContext(ctx, `select
		name, status, last_event_id, target_event_id, processed_rows,
		started_at_ms, updated_at_ms, finished_at_ms, last_error
	from usage_data_migrations
	where name = ?`, UsageCacheAccountingMigrationName).Scan(
		&state.Name,
		&state.Status,
		&state.LastEventID,
		&state.TargetEventID,
		&state.ProcessedRows,
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
	nowMS := time.Now().UnixMilli()
	if _, err := tx.ExecContext(ctx, `update usage_data_migrations set
		status = ?, last_event_id = ?, updated_at_ms = ?, finished_at_ms = ?, last_error = null
	where name = ?`, StatusCompleted, state.TargetEventID, nowMS, nowMS, UsageCacheAccountingMigrationName); err != nil {
		return State{}, err
	}
	state.Status = StatusCompleted
	state.LastEventID = state.TargetEventID
	state.UpdatedAtMS = nowMS
	state.FinishedAtMS = nowMS
	state.LastError = ""
	return state, nil
}

func updateCacheAccountingInRange(ctx context.Context, tx *sql.Tx, firstEventID, lastEventID int64) error {
	infer := inferCacheInputModeSQL()
	// Fill missing modes and repair only rows that the new rules classify as included
	// (do not rewrite legitimate separate rows such as ClaudeExecutor + Grok alias).
	if _, err := tx.ExecContext(ctx, `update usage_events set
		cache_input_mode = case
			when cache_input_mode is null or trim(cache_input_mode) = '' then (`+infer+`)
			when coalesce(cache_input_mode, '') = 'separate_from_input' and (`+infer+`) = 'included_in_input' then 'included_in_input'
			else cache_input_mode
		end
	where id >= ? and id <= ?
		and (`+incompleteUsageCacheAccountingPredicate()+`)`, firstEventID, lastEventID); err != nil {
		return err
	}
	compatCache := `max(max(cached_tokens, cache_tokens) - max(cache_read_tokens, 0) - max(cache_creation_tokens, 0), 0)`
	normalizedRead := compatCache + ` + max(cache_read_tokens, 0)`
	// Recompute normalized buckets for the whole batch range so mode repairs above
	// also refresh previously double-counted totals (idempotent).
	_, err := tx.ExecContext(ctx, `update usage_events set
		normalized_cache_read_tokens = `+normalizedRead+`,
		normalized_cache_creation_tokens = max(cache_creation_tokens, 0),
		normalized_uncached_input_tokens = case
			when cache_input_mode = 'separate_from_input' then max(input_tokens, 0)
			else max(input_tokens - (`+normalizedRead+`) - max(cache_creation_tokens, 0), 0)
		end,
		normalized_total_input_tokens = case
			when cache_input_mode = 'separate_from_input' then max(input_tokens, 0) + (`+normalizedRead+`) + max(cache_creation_tokens, 0)
			else max(input_tokens, 0)
		end
	where id >= ? and id <= ?`, firstEventID, lastEventID)
	return err
}
