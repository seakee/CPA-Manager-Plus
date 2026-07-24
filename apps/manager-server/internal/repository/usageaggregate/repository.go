package usageaggregate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const (
	AggregateName = "hourly_core"
	SchemaVersion = 1
	hourMS        = int64(time.Hour / time.Millisecond)
)

var ErrUnsupportedSchema = errors.New("unsupported usage hourly aggregate schema")

type Repository interface {
	CatchUp(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error)
	RecordFailure(ctx context.Context, aggregateErr error, nowMS int64) error
	State(ctx context.Context) (State, error)
	LoadRows(ctx context.Context, filter Filter) ([]Row, State, bool, error)
}

type State struct {
	AggregateName       string
	SchemaVersion       int
	Status              string
	BackfillLastEventID int64
	CoverageEventID     int64
	TargetEventID       int64
	ProcessedEvents     int64
	MinBucketMS         sql.NullInt64
	MaxBucketMS         sql.NullInt64
	LastRunStartedAtMS  sql.NullInt64
	UpdatedAtMS         int64
	FinishedAtMS        sql.NullInt64
	LastError           string
}

type CatchUpResult struct {
	Processed       int
	LastEventID     int64
	CoverageEventID int64
	TargetEventID   int64
	Pending         bool
}

type Filter struct {
	FromMS          int64
	ToMS            int64
	Models          []string
	IncludeFailed   bool
	FailedOnly      bool
	CollapseBuckets bool
}

type Row struct {
	usage.LongContextTokens
	BucketMS            int64
	Model               string
	BillingModel        string
	ServiceTier         string
	Failed              bool
	Calls               int64
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
	LatencySumMS        int64
	LatencySamples      int64
	ZeroTokenCalls      int64
}

type rowKey struct {
	bucketMS     int64
	model        string
	billingModel string
	serviceTier  string
	failed       bool
}

type repository struct {
	db          *sql.DB
	catchUpGate chan struct{}
}

func New(db *sql.DB) Repository {
	return &repository{
		db:          db,
		catchUpGate: make(chan struct{}, 1),
	}
}

func (r *repository) CatchUp(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error) {
	if limit <= 0 {
		limit = 1000
	}
	if nowMS <= 0 {
		return CatchUpResult{}, errors.New("nowMS must be greater than 0")
	}
	if err := r.acquireCatchUp(ctx); err != nil {
		return CatchUpResult{}, err
	}
	defer r.releaseCatchUp()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return CatchUpResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	// Acquire SQLite's writer slot before reading the checkpoint so a
	// concurrent usage insert cannot invalidate a deferred read snapshot when
	// this transaction advances aggregate state.
	if _, err := tx.ExecContext(ctx, `update usage_hourly_aggregate_state set
		last_run_started_at_ms = ?
	where aggregate_name = ? and schema_version = ?`, nowMS, AggregateName, SchemaVersion); err != nil {
		return CatchUpResult{}, err
	}

	state, err := stateQuery(ctx, tx, AggregateName)
	if err != nil {
		return CatchUpResult{}, err
	}
	if state.SchemaVersion != SchemaVersion {
		return CatchUpResult{}, fmt.Errorf("%w: got %d, want %d", ErrUnsupportedSchema, state.SchemaVersion, SchemaVersion)
	}
	latestID, err := latestEventID(ctx, tx)
	if err != nil {
		return CatchUpResult{}, err
	}
	ids, err := eventIDsAfter(ctx, tx, state.BackfillLastEventID, limit)
	if err != nil {
		return CatchUpResult{}, err
	}
	if len(ids) == 0 {
		if _, err := tx.ExecContext(ctx, `update usage_hourly_aggregate_state set
			status = 'ready',
			target_event_id = max(target_event_id, ?),
			last_run_started_at_ms = ?,
			updated_at_ms = ?,
			finished_at_ms = ?,
			last_error = null
		where aggregate_name = ? and schema_version = ?`, latestID, nowMS, nowMS, nowMS, AggregateName, SchemaVersion); err != nil {
			return CatchUpResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return CatchUpResult{}, err
		}
		return CatchUpResult{
			LastEventID:     state.BackfillLastEventID,
			CoverageEventID: state.CoverageEventID,
			TargetEventID:   max(state.TargetEventID, latestID),
		}, nil
	}

	lastEventID := ids[len(ids)-1]
	if err := upsertAggregateBatch(ctx, tx, state.BackfillLastEventID, lastEventID, nowMS); err != nil {
		return CatchUpResult{}, err
	}
	newlyCoveredEvents, err := upsertIdentityLedgerBatch(ctx, tx, state.BackfillLastEventID, lastEventID, nowMS)
	if err != nil {
		return CatchUpResult{}, err
	}
	minBucket, maxBucket, err := batchBucketRange(ctx, tx, state.BackfillLastEventID, lastEventID)
	if err != nil {
		return CatchUpResult{}, err
	}
	pending := latestID > lastEventID
	status := "ready"
	if pending {
		status = "backfilling"
	}
	if _, err := tx.ExecContext(ctx, `update usage_hourly_aggregate_state set
		status = ?,
		backfill_last_event_id = ?,
		coverage_event_id = ?,
		target_event_id = max(target_event_id, ?),
		processed_events = processed_events + ?,
		min_bucket_ms = case
			when ? is null then min_bucket_ms
			when min_bucket_ms is null then ?
			else min(min_bucket_ms, ?)
		end,
		max_bucket_ms = case
			when ? is null then max_bucket_ms
			when max_bucket_ms is null then ?
			else max(max_bucket_ms, ?)
		end,
		last_run_started_at_ms = ?,
		updated_at_ms = ?,
		finished_at_ms = case when ? then null else ? end,
		last_error = null
	where aggregate_name = ? and schema_version = ?`,
		status,
		lastEventID,
		lastEventID,
		latestID,
		newlyCoveredEvents,
		nullInt64(minBucket),
		nullInt64(minBucket),
		nullInt64(minBucket),
		nullInt64(maxBucket),
		nullInt64(maxBucket),
		nullInt64(maxBucket),
		nowMS,
		nowMS,
		pending,
		nowMS,
		AggregateName,
		SchemaVersion,
	); err != nil {
		return CatchUpResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CatchUpResult{}, err
	}
	return CatchUpResult{
		Processed:       len(ids),
		LastEventID:     lastEventID,
		CoverageEventID: lastEventID,
		TargetEventID:   max(state.TargetEventID, latestID),
		Pending:         pending,
	}, nil
}

func (r *repository) RecordFailure(ctx context.Context, aggregateErr error, nowMS int64) error {
	if aggregateErr == nil || nowMS <= 0 {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `update usage_hourly_aggregate_state set
		status = 'failed',
		updated_at_ms = ?,
		finished_at_ms = ?,
		last_error = ?
	where aggregate_name = ? and schema_version = ?`, nowMS, nowMS, aggregateErr.Error(), AggregateName, SchemaVersion)
	return err
}

func (r *repository) State(ctx context.Context) (State, error) {
	return stateQuery(ctx, r.db, AggregateName)
}

func (r *repository) LoadRows(ctx context.Context, filter Filter) ([]Row, State, bool, error) {
	if filter.FromMS >= filter.ToMS {
		state, err := r.State(ctx)
		return []Row{}, state, err == nil && state.SchemaVersion == SchemaVersion, err
	}
	fullStartMS := ceilHourMS(filter.FromMS)
	fullEndMS := floorHourMS(filter.ToMS)
	if fullStartMS >= fullEndMS {
		return nil, State{}, false, nil
	}

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, State{}, false, err
	}
	defer func() { _ = tx.Rollback() }()

	state, err := stateQuery(ctx, tx, AggregateName)
	if err != nil {
		return nil, State{}, false, err
	}
	if state.SchemaVersion != SchemaVersion {
		return nil, state, false, nil
	}

	grouped := make(map[rowKey]*Row)
	if err := mergeStoredRows(ctx, tx, filter, fullStartMS, fullEndMS, grouped); err != nil {
		return nil, State{}, false, err
	}
	preferDeltaIDScan := state.CoverageEventID > 0 && state.CoverageEventID >= state.TargetEventID
	if err := mergeRawRows(ctx, tx, filter, fullStartMS, fullEndMS, state.CoverageEventID, true, preferDeltaIDScan, grouped); err != nil {
		return nil, State{}, false, err
	}
	if filter.FromMS < fullStartMS {
		if err := mergeRawRows(ctx, tx, filter, filter.FromMS, min(fullStartMS, filter.ToMS), 0, false, false, grouped); err != nil {
			return nil, State{}, false, err
		}
	}
	if fullEndMS < filter.ToMS {
		if err := mergeRawRows(ctx, tx, filter, max(fullEndMS, filter.FromMS), filter.ToMS, 0, false, false, grouped); err != nil {
			return nil, State{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, State{}, false, err
	}
	return sortedRows(grouped), state, true, nil
}

type stateQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func stateQuery(ctx context.Context, db stateQuerier, name string) (State, error) {
	var state State
	var lastError sql.NullString
	err := db.QueryRowContext(ctx, `select
		aggregate_name,
		schema_version,
		status,
		backfill_last_event_id,
		coverage_event_id,
		target_event_id,
		processed_events,
		min_bucket_ms,
		max_bucket_ms,
		last_run_started_at_ms,
		updated_at_ms,
		finished_at_ms,
		last_error
	from usage_hourly_aggregate_state
	where aggregate_name = ?`, name).Scan(
		&state.AggregateName,
		&state.SchemaVersion,
		&state.Status,
		&state.BackfillLastEventID,
		&state.CoverageEventID,
		&state.TargetEventID,
		&state.ProcessedEvents,
		&state.MinBucketMS,
		&state.MaxBucketMS,
		&state.LastRunStartedAtMS,
		&state.UpdatedAtMS,
		&state.FinishedAtMS,
		&lastError,
	)
	if err != nil {
		return State{}, err
	}
	state.LastError = lastError.String
	return state, nil
}

func latestEventID(ctx context.Context, tx *sql.Tx) (int64, error) {
	var id int64
	if err := tx.QueryRowContext(ctx, `select coalesce(max(id), 0) from usage_events`).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func eventIDsAfter(ctx context.Context, tx *sql.Tx, lastEventID int64, limit int) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, `select id from usage_events where id > ? order by id limit ?`, lastEventID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]int64, 0, limit)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func upsertAggregateBatch(ctx context.Context, tx *sql.Tx, afterID, throughID, nowMS int64) error {
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`insert into usage_hourly_aggregate_v1 (
		bucket_ms,
		model,
		billing_model,
		service_tier,
		failed,
		calls,
		input_tokens,
		output_tokens,
		reasoning_tokens,
		cached_tokens,
		cache_read_tokens,
		cache_creation_tokens,
		long_input_tokens,
		long_output_tokens,
		long_cached_tokens,
		long_cache_read_tokens,
		long_cache_creation_tokens,
		total_tokens,
		latency_sum_ms,
		latency_samples,
		zero_token_calls,
		updated_at_ms
	)
	select
		timestamp_ms - (timestamp_ms %% %d) as bucket_ms,
		model,
		coalesce(nullif(resolved_model, ''), model) as billing_model,
		coalesce(service_tier, '') as service_tier,
		failed,
		count(*),
		coalesce(sum(coalesce(normalized_total_input_tokens, input_tokens, 0)), 0),
		coalesce(sum(output_tokens), 0),
		coalesce(sum(reasoning_tokens), 0),
		coalesce(sum(max(max(cached_tokens, cache_tokens) - max(cache_read_tokens, 0) - max(cache_creation_tokens, 0), 0)), 0),
		coalesce(sum(cache_read_tokens), 0),
		coalesce(sum(cache_creation_tokens), 0),
		coalesce(sum(case when coalesce(normalized_total_input_tokens, input_tokens, 0) > %d then coalesce(normalized_total_input_tokens, input_tokens, 0) else 0 end), 0),
		coalesce(sum(case when coalesce(normalized_total_input_tokens, input_tokens, 0) > %d then output_tokens else 0 end), 0),
		coalesce(sum(case when coalesce(normalized_total_input_tokens, input_tokens, 0) > %d then max(max(cached_tokens, cache_tokens) - max(cache_read_tokens, 0) - max(cache_creation_tokens, 0), 0) else 0 end), 0),
		coalesce(sum(case when coalesce(normalized_total_input_tokens, input_tokens, 0) > %d then cache_read_tokens else 0 end), 0),
		coalesce(sum(case when coalesce(normalized_total_input_tokens, input_tokens, 0) > %d then cache_creation_tokens else 0 end), 0),
		coalesce(sum(total_tokens), 0),
		coalesce(sum(case when latency_ms is not null and latency_ms != 0 then latency_ms else 0 end), 0),
		count(nullif(latency_ms, 0)),
		coalesce(sum(case when total_tokens = 0 and failed = 0 then 1 else 0 end), 0),
		?
	from usage_events e
	where e.id > ? and e.id <= ?
		and not exists (
			select 1 from usage_event_identity_ledger ledger
			where ledger.event_hash = e.event_hash and ledger.aggregate_schema_version >= %d
		)
	group by bucket_ms, model, coalesce(nullif(resolved_model, ''), model), coalesce(service_tier, ''), failed
	on conflict(bucket_ms, model, billing_model, service_tier, failed) do update set
		calls = usage_hourly_aggregate_v1.calls + excluded.calls,
		input_tokens = usage_hourly_aggregate_v1.input_tokens + excluded.input_tokens,
		output_tokens = usage_hourly_aggregate_v1.output_tokens + excluded.output_tokens,
		reasoning_tokens = usage_hourly_aggregate_v1.reasoning_tokens + excluded.reasoning_tokens,
		cached_tokens = usage_hourly_aggregate_v1.cached_tokens + excluded.cached_tokens,
		cache_read_tokens = usage_hourly_aggregate_v1.cache_read_tokens + excluded.cache_read_tokens,
		cache_creation_tokens = usage_hourly_aggregate_v1.cache_creation_tokens + excluded.cache_creation_tokens,
		long_input_tokens = usage_hourly_aggregate_v1.long_input_tokens + excluded.long_input_tokens,
		long_output_tokens = usage_hourly_aggregate_v1.long_output_tokens + excluded.long_output_tokens,
		long_cached_tokens = usage_hourly_aggregate_v1.long_cached_tokens + excluded.long_cached_tokens,
		long_cache_read_tokens = usage_hourly_aggregate_v1.long_cache_read_tokens + excluded.long_cache_read_tokens,
		long_cache_creation_tokens = usage_hourly_aggregate_v1.long_cache_creation_tokens + excluded.long_cache_creation_tokens,
		total_tokens = usage_hourly_aggregate_v1.total_tokens + excluded.total_tokens,
		latency_sum_ms = usage_hourly_aggregate_v1.latency_sum_ms + excluded.latency_sum_ms,
		latency_samples = usage_hourly_aggregate_v1.latency_samples + excluded.latency_samples,
		zero_token_calls = usage_hourly_aggregate_v1.zero_token_calls + excluded.zero_token_calls,
		updated_at_ms = excluded.updated_at_ms`,
		hourMS,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		SchemaVersion,
	), nowMS, afterID, throughID)
	return err
}

func upsertIdentityLedgerBatch(ctx context.Context, tx *sql.Tx, afterID, throughID, nowMS int64) (int64, error) {
	insertResult, err := tx.ExecContext(ctx, fmt.Sprintf(`insert or ignore into usage_event_identity_ledger (
		event_hash,
		raw_event_id,
		timestamp_ms,
		bucket_ms,
		aggregate_schema_version,
		first_seen_at_ms,
		updated_at_ms
	)
	select event_hash, id, timestamp_ms, timestamp_ms - (timestamp_ms %% %d), ?,
		case when created_at_ms > 0 then created_at_ms else ? end,
		?
	from usage_events
	where id > ? and id <= ?`, hourMS), SchemaVersion, nowMS, nowMS, afterID, throughID)
	if err != nil {
		return 0, err
	}
	inserted, err := insertResult.RowsAffected()
	if err != nil {
		return 0, err
	}
	updateResult, err := tx.ExecContext(ctx, fmt.Sprintf(`update usage_event_identity_ledger as ledger set
		raw_event_id = e.id,
		timestamp_ms = e.timestamp_ms,
		bucket_ms = e.timestamp_ms - (e.timestamp_ms %% %d),
		aggregate_schema_version = ?,
		updated_at_ms = ?
	from usage_events as e
	where ledger.event_hash = e.event_hash
		and e.id > ? and e.id <= ?
		and ledger.aggregate_schema_version < ?`, hourMS),
		SchemaVersion,
		nowMS,
		afterID, throughID,
		SchemaVersion,
	)
	if err != nil {
		return 0, err
	}
	updated, err := updateResult.RowsAffected()
	if err != nil {
		return 0, err
	}
	return inserted + updated, nil
}

func batchBucketRange(ctx context.Context, tx *sql.Tx, afterID, throughID int64) (sql.NullInt64, sql.NullInt64, error) {
	var minBucket, maxBucket sql.NullInt64
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`select
		min(timestamp_ms - (timestamp_ms %% %d)),
		max(timestamp_ms - (timestamp_ms %% %d))
	from usage_events
	where id > ? and id <= ?`, hourMS, hourMS), afterID, throughID).Scan(&minBucket, &maxBucket)
	return minBucket, maxBucket, err
}

func mergeStoredRows(ctx context.Context, tx *sql.Tx, filter Filter, fromMS, toMS int64, grouped map[rowKey]*Row) error {
	conditions, args := aggregateConditions(filter, fromMS, toMS)
	bucketExpr := "bucket_ms"
	if filter.CollapseBuckets {
		bucketExpr = "0"
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`select
		%s as bucket_ms,
		model,
		billing_model,
		service_tier,
		failed,
		sum(calls),
		sum(input_tokens),
		sum(output_tokens),
		sum(reasoning_tokens),
		sum(cached_tokens),
		sum(cache_read_tokens),
		sum(cache_creation_tokens),
		sum(long_input_tokens),
		sum(long_output_tokens),
		sum(long_cached_tokens),
		sum(long_cache_read_tokens),
		sum(long_cache_creation_tokens),
		sum(total_tokens),
		sum(latency_sum_ms),
		sum(latency_samples),
		sum(zero_token_calls)
	from usage_hourly_aggregate_v1
	where %s
	group by 1, 2, 3, 4, 5
	order by 1, 2, 3, 4, 5`, bucketExpr, strings.Join(conditions, " and ")), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanAndMergeRows(rows, grouped)
}

func mergeRawRows(ctx context.Context, tx *sql.Tx, filter Filter, fromMS, toMS, afterID int64, excludeAggregated bool, preferEventIDScan bool, grouped map[rowKey]*Row) error {
	query, args := rawRowsStatement(filter, fromMS, toMS, afterID, excludeAggregated, preferEventIDScan)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanAndMergeRows(rows, grouped)
}

func rawRowsStatement(filter Filter, fromMS, toMS, afterID int64, excludeAggregated bool, preferEventIDScan bool) (string, []any) {
	conditions, args := rawConditions(filter, fromMS, toMS, afterID, excludeAggregated)
	tableExpr := "usage_events"
	if afterID > 0 && preferEventIDScan {
		tableExpr = "usage_events not indexed"
	}
	bucketExpr := fmt.Sprintf("timestamp_ms - (timestamp_ms %% %d)", hourMS)
	if filter.CollapseBuckets {
		bucketExpr = "0"
	}
	return fmt.Sprintf(`select
		%s as bucket_ms,
		model,
		coalesce(nullif(resolved_model, ''), model) as billing_model,
		coalesce(service_tier, '') as service_tier,
		failed,
		count(*),
		coalesce(sum(coalesce(normalized_total_input_tokens, input_tokens, 0)), 0),
		coalesce(sum(output_tokens), 0),
		coalesce(sum(reasoning_tokens), 0),
		coalesce(sum(max(max(cached_tokens, cache_tokens) - max(cache_read_tokens, 0) - max(cache_creation_tokens, 0), 0)), 0),
		coalesce(sum(cache_read_tokens), 0),
		coalesce(sum(cache_creation_tokens), 0),
		coalesce(sum(case when coalesce(normalized_total_input_tokens, input_tokens, 0) > %d then coalesce(normalized_total_input_tokens, input_tokens, 0) else 0 end), 0),
		coalesce(sum(case when coalesce(normalized_total_input_tokens, input_tokens, 0) > %d then output_tokens else 0 end), 0),
		coalesce(sum(case when coalesce(normalized_total_input_tokens, input_tokens, 0) > %d then max(max(cached_tokens, cache_tokens) - max(cache_read_tokens, 0) - max(cache_creation_tokens, 0), 0) else 0 end), 0),
		coalesce(sum(case when coalesce(normalized_total_input_tokens, input_tokens, 0) > %d then cache_read_tokens else 0 end), 0),
		coalesce(sum(case when coalesce(normalized_total_input_tokens, input_tokens, 0) > %d then cache_creation_tokens else 0 end), 0),
		coalesce(sum(total_tokens), 0),
		coalesce(sum(case when latency_ms is not null and latency_ms != 0 then latency_ms else 0 end), 0),
		count(nullif(latency_ms, 0)),
		coalesce(sum(case when total_tokens = 0 and failed = 0 then 1 else 0 end), 0)
	from %s
	where %s
	group by 1, 2, 3, 4, 5
	order by 1, 2, 3, 4, 5`,
		bucketExpr,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		tableExpr,
		strings.Join(conditions, " and "),
	), args
}

func scanAndMergeRows(rows *sql.Rows, grouped map[rowKey]*Row) error {
	for rows.Next() {
		var row Row
		var failed int
		if err := rows.Scan(
			&row.BucketMS,
			&row.Model,
			&row.BillingModel,
			&row.ServiceTier,
			&failed,
			&row.Calls,
			&row.InputTokens,
			&row.OutputTokens,
			&row.ReasoningTokens,
			&row.CachedTokens,
			&row.CacheReadTokens,
			&row.CacheCreationTokens,
			&row.LongInputTokens,
			&row.LongOutputTokens,
			&row.LongCachedTokens,
			&row.LongCacheReadTokens,
			&row.LongCacheCreationTokens,
			&row.TotalTokens,
			&row.LatencySumMS,
			&row.LatencySamples,
			&row.ZeroTokenCalls,
		); err != nil {
			return err
		}
		row.Failed = failed != 0
		mergeRow(grouped, row)
	}
	return rows.Err()
}

func aggregateConditions(filter Filter, fromMS, toMS int64) ([]string, []any) {
	conditions := []string{"bucket_ms >= ?", "bucket_ms < ?"}
	args := []any{fromMS, toMS}
	return appendFilterConditions(conditions, args, filter)
}

func rawConditions(filter Filter, fromMS, toMS, afterID int64, excludeAggregated bool) ([]string, []any) {
	conditions := []string{"timestamp_ms >= ?", "timestamp_ms < ?"}
	args := []any{fromMS, toMS}
	if afterID > 0 {
		conditions = append(conditions, "id > ?")
		args = append(args, afterID)
	}
	if excludeAggregated {
		conditions = append(conditions, `not exists (
			select 1 from usage_event_identity_ledger ledger
			where ledger.event_hash = usage_events.event_hash
				and ledger.aggregate_schema_version >= ?
		)`)
		args = append(args, SchemaVersion)
	}
	return appendFilterConditions(conditions, args, filter)
}

func appendFilterConditions(conditions []string, args []any, filter Filter) ([]string, []any) {
	models := normalizeValues(filter.Models)
	if len(models) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(models)), ",")
		conditions = append(conditions, "model in ("+placeholders+")")
		for _, model := range models {
			args = append(args, model)
		}
	}
	if !filter.IncludeFailed {
		conditions = append(conditions, "failed = 0")
	}
	if filter.FailedOnly {
		conditions = append(conditions, "failed = 1")
	}
	return conditions, args
}

func normalizeValues(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func mergeRow(grouped map[rowKey]*Row, row Row) {
	key := rowKey{
		bucketMS:     row.BucketMS,
		model:        row.Model,
		billingModel: row.BillingModel,
		serviceTier:  row.ServiceTier,
		failed:       row.Failed,
	}
	entry := grouped[key]
	if entry == nil {
		copy := row
		grouped[key] = &copy
		return
	}
	entry.Calls += row.Calls
	entry.InputTokens += row.InputTokens
	entry.OutputTokens += row.OutputTokens
	entry.ReasoningTokens += row.ReasoningTokens
	entry.CachedTokens += row.CachedTokens
	entry.CacheReadTokens += row.CacheReadTokens
	entry.CacheCreationTokens += row.CacheCreationTokens
	entry.LongInputTokens += row.LongInputTokens
	entry.LongOutputTokens += row.LongOutputTokens
	entry.LongCachedTokens += row.LongCachedTokens
	entry.LongCacheReadTokens += row.LongCacheReadTokens
	entry.LongCacheCreationTokens += row.LongCacheCreationTokens
	entry.TotalTokens += row.TotalTokens
	entry.LatencySumMS += row.LatencySumMS
	entry.LatencySamples += row.LatencySamples
	entry.ZeroTokenCalls += row.ZeroTokenCalls
}

func sortedRows(grouped map[rowKey]*Row) []Row {
	result := make([]Row, 0, len(grouped))
	for _, row := range grouped {
		result = append(result, *row)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].BucketMS != result[j].BucketMS {
			return result[i].BucketMS < result[j].BucketMS
		}
		if result[i].Model != result[j].Model {
			return result[i].Model < result[j].Model
		}
		if result[i].BillingModel != result[j].BillingModel {
			return result[i].BillingModel < result[j].BillingModel
		}
		if result[i].ServiceTier != result[j].ServiceTier {
			return result[i].ServiceTier < result[j].ServiceTier
		}
		return !result[i].Failed && result[j].Failed
	})
	return result
}

func (r *repository) acquireCatchUp(ctx context.Context) error {
	select {
	case r.catchUpGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *repository) releaseCatchUp() {
	select {
	case <-r.catchUpGate:
	default:
	}
}

func ceilHourMS(value int64) int64 {
	if value%hourMS == 0 {
		return value
	}
	return value - value%hourMS + hourMS
}

func floorHourMS(value int64) int64 {
	return value - value%hourMS
}

func nullInt64(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}
