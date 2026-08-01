package usagepricing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const (
	RollupName    = "pricing_v1"
	SchemaVersion = 1
	hourMS        = int64(time.Hour / time.Millisecond)
)

var ErrUnsupportedSchema = errors.New("unsupported usage pricing rollup schema")

type Repository interface {
	CatchUp(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error)
	RecordFailure(ctx context.Context, rollupErr error, nowMS int64) error
	State(ctx context.Context) (State, error)
	LoadHourlyRows(ctx context.Context, filter HourlyFilter) ([]HourlyRow, State, bool, error)
	LoadHourlyRowsTx(ctx context.Context, tx *sql.Tx, filter HourlyFilter) ([]HourlyRow, State, bool, error)
	LoadAccountRows(ctx context.Context, accountKeys []string) ([]AccountRow, State, bool, error)
	LoadAccountRowsTx(ctx context.Context, tx *sql.Tx, accountKeys []string) ([]AccountRow, State, bool, error)
}

type State struct {
	RollupName          string
	SchemaVersion       int
	StructureRevision   string
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
	Rebuilt         bool
}

type HourlyFilter struct {
	FromMS          int64
	ToMS            int64
	Models          []string
	IncludeFailed   bool
	FailedOnly      bool
	CollapseBuckets bool
}

type HourlyRow struct {
	usage.LongContextTokens
	usage.PricingBand
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

type AccountRow struct {
	usage.LongContextTokens
	usage.PricingBand
	AccountKey           string
	AccountSnapshot      string
	AuthLabelSnapshot    string
	AuthProviderSnapshot string
	AuthIndex            string
	Source               string
	SourceHash           string
	Model                string
	BillingModel         string
	ServiceTier          string
	Calls                int64
	SuccessCalls         int64
	FailureCalls         int64
	InputTokens          int64
	OutputTokens         int64
	ReasoningTokens      int64
	CachedTokens         int64
	CacheReadTokens      int64
	CacheCreationTokens  int64
	TotalTokens          int64
	FirstSeenMS          int64
	LastSeenMS           int64
	UpdatedAtMS          int64
}

type hourlyKey struct {
	bucketMS               int64
	model                  string
	billingModel           string
	pricingModel           string
	serviceTier            string
	contextThresholdTokens int64
	failed                 bool
}

type accountKey struct {
	accountKey             string
	billingModel           string
	pricingModel           string
	serviceTier            string
	contextThresholdTokens int64
}

type repository struct {
	db          *sql.DB
	catchUpGate chan struct{}
}

func New(db *sql.DB) Repository {
	return &repository{db: db, catchUpGate: make(chan struct{}, 1)}
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
	if _, err := tx.ExecContext(ctx, `update usage_pricing_rollup_state set
		last_run_started_at_ms = ?
		where rollup_name = ?`, nowMS, RollupName); err != nil {
		return CatchUpResult{}, err
	}

	state, err := stateQuery(ctx, tx)
	if err != nil {
		return CatchUpResult{}, err
	}
	if state.SchemaVersion != SchemaVersion {
		return CatchUpResult{}, fmt.Errorf("%w: got %d, want %d", ErrUnsupportedSchema, state.SchemaVersion, SchemaVersion)
	}
	revision, err := structureRevision(ctx, tx)
	if err != nil {
		return CatchUpResult{}, err
	}
	latestID, err := latestEventID(ctx, tx)
	if err != nil {
		return CatchUpResult{}, err
	}
	rebuilt := false
	if state.StructureRevision != revision {
		if err := resetForRevision(ctx, tx, revision, latestID, nowMS); err != nil {
			return CatchUpResult{}, err
		}
		state = State{
			RollupName:        RollupName,
			SchemaVersion:     SchemaVersion,
			StructureRevision: revision,
			Status:            "rebuilding",
			TargetEventID:     latestID,
			UpdatedAtMS:       nowMS,
		}
		rebuilt = true
	}

	ids, err := eventIDsAfter(ctx, tx, state.BackfillLastEventID, limit)
	if err != nil {
		return CatchUpResult{}, err
	}
	if len(ids) == 0 {
		if _, err := tx.ExecContext(ctx, `update usage_pricing_rollup_state set
			status = 'ready',
			target_event_id = max(target_event_id, ?),
			last_run_started_at_ms = ?,
			updated_at_ms = ?,
			finished_at_ms = ?,
			last_error = null
			where rollup_name = ? and structure_revision = ?`,
			latestID, nowMS, nowMS, nowMS, RollupName, revision,
		); err != nil {
			return CatchUpResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return CatchUpResult{}, err
		}
		return CatchUpResult{
			LastEventID:     state.BackfillLastEventID,
			CoverageEventID: state.CoverageEventID,
			TargetEventID:   max(state.TargetEventID, latestID),
			Rebuilt:         rebuilt,
		}, nil
	}

	lastEventID := ids[len(ids)-1]
	if err := upsertHourlyBatch(ctx, tx, revision, state.BackfillLastEventID, lastEventID, nowMS); err != nil {
		return CatchUpResult{}, err
	}
	if err := upsertAccountBatch(ctx, tx, revision, state.BackfillLastEventID, lastEventID, nowMS); err != nil {
		return CatchUpResult{}, err
	}
	minBucket, maxBucket, err := batchBucketRange(ctx, tx, state.BackfillLastEventID, lastEventID)
	if err != nil {
		return CatchUpResult{}, err
	}
	pending := latestID > lastEventID
	status := "ready"
	if pending {
		status = "rebuilding"
	}
	if _, err := tx.ExecContext(ctx, `update usage_pricing_rollup_state set
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
		where rollup_name = ? and structure_revision = ?`,
		status,
		lastEventID,
		lastEventID,
		latestID,
		len(ids),
		nullInt64(minBucket), nullInt64(minBucket), nullInt64(minBucket),
		nullInt64(maxBucket), nullInt64(maxBucket), nullInt64(maxBucket),
		nowMS,
		nowMS,
		pending,
		nowMS,
		RollupName,
		revision,
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
		Rebuilt:         rebuilt,
	}, nil
}

func (r *repository) RecordFailure(ctx context.Context, rollupErr error, nowMS int64) error {
	if rollupErr == nil || nowMS <= 0 {
		return nil
	}
	_, err := r.db.ExecContext(ctx, `update usage_pricing_rollup_state set
		status = 'failed', updated_at_ms = ?, finished_at_ms = ?, last_error = ?
		where rollup_name = ?`, nowMS, nowMS, rollupErr.Error(), RollupName)
	return err
}

func (r *repository) State(ctx context.Context) (State, error) {
	return stateQuery(ctx, r.db)
}

func resetForRevision(ctx context.Context, tx *sql.Tx, revision string, latestID, nowMS int64) error {
	for _, statement := range []string{
		`delete from usage_pricing_hourly_rollups_v1`,
		`delete from usage_pricing_account_rollups_v1`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	_, err := tx.ExecContext(ctx, `update usage_pricing_rollup_state set
		structure_revision = ?,
		status = 'rebuilding',
		backfill_last_event_id = 0,
		coverage_event_id = 0,
		target_event_id = ?,
		processed_events = 0,
		min_bucket_ms = null,
		max_bucket_ms = null,
		last_run_started_at_ms = ?,
		updated_at_ms = ?,
		finished_at_ms = null,
		last_error = null
		where rollup_name = ?`, revision, latestID, nowMS, nowMS, RollupName)
	return err
}

type stateQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func stateQuery(ctx context.Context, db stateQuerier) (State, error) {
	var state State
	var lastError sql.NullString
	err := db.QueryRowContext(ctx, `select
		rollup_name, schema_version, structure_revision, status,
		backfill_last_event_id, coverage_event_id, target_event_id,
		processed_events, min_bucket_ms, max_bucket_ms,
		last_run_started_at_ms, updated_at_ms, finished_at_ms, last_error
		from usage_pricing_rollup_state where rollup_name = ?`, RollupName).Scan(
		&state.RollupName,
		&state.SchemaVersion,
		&state.StructureRevision,
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

type rowQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func structureRevision(ctx context.Context, db rowQuerier) (string, error) {
	rows, err := db.QueryContext(ctx, `select p.model, t.threshold_tokens
		from model_prices p
		left join model_price_context_tiers t on t.model = p.model
		order by p.model, t.threshold_tokens`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	prices := map[string]model.ModelPrice{}
	for rows.Next() {
		var modelID string
		var threshold sql.NullInt64
		if err := rows.Scan(&modelID, &threshold); err != nil {
			return "", err
		}
		price := prices[modelID]
		if threshold.Valid {
			price.ContextTiers = append(price.ContextTiers, model.ModelPriceContextTier{ThresholdTokens: threshold.Int64})
		}
		prices[modelID] = price
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return model.ModelPriceStructureRevision(prices), nil
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

func batchBucketRange(ctx context.Context, tx *sql.Tx, afterID, throughID int64) (sql.NullInt64, sql.NullInt64, error) {
	var minBucket, maxBucket sql.NullInt64
	err := tx.QueryRowContext(ctx, fmt.Sprintf(`select
		min(timestamp_ms - (timestamp_ms %% %d)),
		max(timestamp_ms - (timestamp_ms %% %d))
		from usage_events where id > ? and id <= ?`, hourMS, hourMS), afterID, throughID).Scan(&minBucket, &maxBucket)
	return minBucket, maxBucket, err
}

func bandedEventsCTE(whereClause string) string {
	return fmt.Sprintf(`with base_events as (
		select
			e.*,
			coalesce(nullif(e.resolved_model, ''), e.model) as billing_model_value,
			coalesce(e.normalized_total_input_tokens, e.input_tokens, 0) as normalized_input_tokens_value,
			max(
				max(coalesce(e.cached_tokens, 0), coalesce(e.cache_tokens, 0)) -
				max(coalesce(e.cache_read_tokens, 0), 0) -
				max(coalesce(e.cache_creation_tokens, 0), 0),
				0
			) as compatible_cached_tokens_value,
			case
				when trim(coalesce(e.account_snapshot, '')) != '' then e.account_snapshot
				when trim(coalesce(e.auth_label_snapshot, '')) != '' then e.auth_label_snapshot
				when trim(coalesce(e.source, '')) != '' then e.source
				when trim(coalesce(e.auth_index, '')) != '' then e.auth_index
				else '-'
			end as account_key_value
		from usage_events e
		where %s
	), priced_events as (
		select
			base_events.*,
			case
				when billing_price.model is not null then billing_model_value
				when display_price.model is not null then base_events.model
				else billing_model_value
			end as pricing_model_value
		from base_events
		left join model_prices billing_price on billing_price.model = base_events.billing_model_value
		left join model_prices display_price on display_price.model = base_events.model
	), banded_events as (
		select
			priced_events.*,
			coalesce((
				select max(tier.threshold_tokens)
				from model_price_context_tiers tier
				where tier.model = priced_events.pricing_model_value
					and priced_events.normalized_input_tokens_value > tier.threshold_tokens
			), %d) as context_threshold_tokens_value
		from priced_events
	)`, whereClause, model.ModelPriceBaseContextThreshold)
}

func upsertHourlyBatch(ctx context.Context, tx *sql.Tx, revision string, afterID, throughID, nowMS int64) error {
	query := bandedEventsCTE("e.id > ? and e.id <= ?") + fmt.Sprintf(`
	insert into usage_pricing_hourly_rollups_v1 (
		structure_revision, bucket_ms, model, billing_model, pricing_model,
		service_tier, context_threshold_tokens, failed, calls,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens,
		cache_read_tokens, cache_creation_tokens,
		long_input_tokens, long_output_tokens, long_cached_tokens,
		long_cache_read_tokens, long_cache_creation_tokens,
		total_tokens, latency_sum_ms, latency_samples, zero_token_calls, updated_at_ms
	)
	select
		?,
		timestamp_ms - (timestamp_ms %% %d),
		model,
		billing_model_value,
		pricing_model_value,
		coalesce(service_tier, ''),
		context_threshold_tokens_value,
		failed,
		count(*),
		coalesce(sum(normalized_input_tokens_value), 0),
		coalesce(sum(output_tokens), 0),
		coalesce(sum(reasoning_tokens), 0),
		coalesce(sum(compatible_cached_tokens_value), 0),
		coalesce(sum(cache_read_tokens), 0),
		coalesce(sum(cache_creation_tokens), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then normalized_input_tokens_value else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then output_tokens else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then compatible_cached_tokens_value else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then cache_read_tokens else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then cache_creation_tokens else 0 end), 0),
		coalesce(sum(total_tokens), 0),
		coalesce(sum(case when latency_ms is not null and latency_ms != 0 then latency_ms else 0 end), 0),
		count(nullif(latency_ms, 0)),
		coalesce(sum(case when total_tokens = 0 and failed = 0 then 1 else 0 end), 0),
		?
	from banded_events
	group by 2, 3, 4, 5, 6, 7, 8
	on conflict(
		structure_revision, bucket_ms, model, billing_model, pricing_model,
		service_tier, context_threshold_tokens, failed
	) do update set
		calls = usage_pricing_hourly_rollups_v1.calls + excluded.calls,
		input_tokens = usage_pricing_hourly_rollups_v1.input_tokens + excluded.input_tokens,
		output_tokens = usage_pricing_hourly_rollups_v1.output_tokens + excluded.output_tokens,
		reasoning_tokens = usage_pricing_hourly_rollups_v1.reasoning_tokens + excluded.reasoning_tokens,
		cached_tokens = usage_pricing_hourly_rollups_v1.cached_tokens + excluded.cached_tokens,
		cache_read_tokens = usage_pricing_hourly_rollups_v1.cache_read_tokens + excluded.cache_read_tokens,
		cache_creation_tokens = usage_pricing_hourly_rollups_v1.cache_creation_tokens + excluded.cache_creation_tokens,
		long_input_tokens = usage_pricing_hourly_rollups_v1.long_input_tokens + excluded.long_input_tokens,
		long_output_tokens = usage_pricing_hourly_rollups_v1.long_output_tokens + excluded.long_output_tokens,
		long_cached_tokens = usage_pricing_hourly_rollups_v1.long_cached_tokens + excluded.long_cached_tokens,
		long_cache_read_tokens = usage_pricing_hourly_rollups_v1.long_cache_read_tokens + excluded.long_cache_read_tokens,
		long_cache_creation_tokens = usage_pricing_hourly_rollups_v1.long_cache_creation_tokens + excluded.long_cache_creation_tokens,
		total_tokens = usage_pricing_hourly_rollups_v1.total_tokens + excluded.total_tokens,
		latency_sum_ms = usage_pricing_hourly_rollups_v1.latency_sum_ms + excluded.latency_sum_ms,
		latency_samples = usage_pricing_hourly_rollups_v1.latency_samples + excluded.latency_samples,
		zero_token_calls = usage_pricing_hourly_rollups_v1.zero_token_calls + excluded.zero_token_calls,
		updated_at_ms = excluded.updated_at_ms`,
		hourMS,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
	)
	_, err := tx.ExecContext(ctx, query, afterID, throughID, revision, nowMS)
	return err
}

func upsertAccountBatch(ctx context.Context, tx *sql.Tx, revision string, afterID, throughID, nowMS int64) error {
	query := bandedEventsCTE("e.id > ? and e.id <= ?") + fmt.Sprintf(`
	insert into usage_pricing_account_rollups_v1 (
		structure_revision, account_key, account_snapshot, auth_label_snapshot,
		auth_provider_snapshot, auth_index, source, source_hash, model,
		billing_model, pricing_model, service_tier, context_threshold_tokens,
		calls, success_calls, failure_calls, input_tokens, output_tokens,
		reasoning_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
		long_input_tokens, long_output_tokens, long_cached_tokens,
		long_cache_read_tokens, long_cache_creation_tokens, total_tokens,
		first_seen_ms, last_seen_ms, updated_at_ms
	)
	select
		?,
		account_key_value,
		max(nullif(account_snapshot, '')),
		max(nullif(auth_label_snapshot, '')),
		max(nullif(coalesce(nullif(auth_provider_snapshot, ''), provider, ''), '')),
		max(nullif(auth_index, '')),
		max(nullif(source, '')),
		max(nullif(source_hash, '')),
		min(model),
		billing_model_value,
		pricing_model_value,
		coalesce(service_tier, ''),
		context_threshold_tokens_value,
		count(*),
		coalesce(sum(case when failed = 0 then 1 else 0 end), 0),
		coalesce(sum(case when failed = 1 then 1 else 0 end), 0),
		coalesce(sum(normalized_input_tokens_value), 0),
		coalesce(sum(output_tokens), 0),
		coalesce(sum(reasoning_tokens), 0),
		coalesce(sum(compatible_cached_tokens_value), 0),
		coalesce(sum(cache_read_tokens), 0),
		coalesce(sum(cache_creation_tokens), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then normalized_input_tokens_value else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then output_tokens else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then compatible_cached_tokens_value else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then cache_read_tokens else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then cache_creation_tokens else 0 end), 0),
		coalesce(sum(total_tokens), 0),
		min(timestamp_ms),
		max(timestamp_ms),
		?
	from banded_events
	group by account_key_value, billing_model_value, pricing_model_value,
		coalesce(service_tier, ''), context_threshold_tokens_value
	on conflict(
		structure_revision, account_key, billing_model, pricing_model,
		service_tier, context_threshold_tokens
	) do update set
		account_snapshot = coalesce(nullif(excluded.account_snapshot, ''), usage_pricing_account_rollups_v1.account_snapshot),
		auth_label_snapshot = coalesce(nullif(excluded.auth_label_snapshot, ''), usage_pricing_account_rollups_v1.auth_label_snapshot),
		auth_provider_snapshot = coalesce(nullif(excluded.auth_provider_snapshot, ''), usage_pricing_account_rollups_v1.auth_provider_snapshot),
		auth_index = coalesce(nullif(excluded.auth_index, ''), usage_pricing_account_rollups_v1.auth_index),
		source = coalesce(nullif(excluded.source, ''), usage_pricing_account_rollups_v1.source),
		source_hash = coalesce(nullif(excluded.source_hash, ''), usage_pricing_account_rollups_v1.source_hash),
		model = coalesce(nullif(excluded.model, ''), usage_pricing_account_rollups_v1.model),
		calls = usage_pricing_account_rollups_v1.calls + excluded.calls,
		success_calls = usage_pricing_account_rollups_v1.success_calls + excluded.success_calls,
		failure_calls = usage_pricing_account_rollups_v1.failure_calls + excluded.failure_calls,
		input_tokens = usage_pricing_account_rollups_v1.input_tokens + excluded.input_tokens,
		output_tokens = usage_pricing_account_rollups_v1.output_tokens + excluded.output_tokens,
		reasoning_tokens = usage_pricing_account_rollups_v1.reasoning_tokens + excluded.reasoning_tokens,
		cached_tokens = usage_pricing_account_rollups_v1.cached_tokens + excluded.cached_tokens,
		cache_read_tokens = usage_pricing_account_rollups_v1.cache_read_tokens + excluded.cache_read_tokens,
		cache_creation_tokens = usage_pricing_account_rollups_v1.cache_creation_tokens + excluded.cache_creation_tokens,
		long_input_tokens = usage_pricing_account_rollups_v1.long_input_tokens + excluded.long_input_tokens,
		long_output_tokens = usage_pricing_account_rollups_v1.long_output_tokens + excluded.long_output_tokens,
		long_cached_tokens = usage_pricing_account_rollups_v1.long_cached_tokens + excluded.long_cached_tokens,
		long_cache_read_tokens = usage_pricing_account_rollups_v1.long_cache_read_tokens + excluded.long_cache_read_tokens,
		long_cache_creation_tokens = usage_pricing_account_rollups_v1.long_cache_creation_tokens + excluded.long_cache_creation_tokens,
		total_tokens = usage_pricing_account_rollups_v1.total_tokens + excluded.total_tokens,
		first_seen_ms = min(usage_pricing_account_rollups_v1.first_seen_ms, excluded.first_seen_ms),
		last_seen_ms = max(usage_pricing_account_rollups_v1.last_seen_ms, excluded.last_seen_ms),
		updated_at_ms = excluded.updated_at_ms`,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
	)
	_, err := tx.ExecContext(ctx, query, afterID, throughID, revision, nowMS)
	return err
}

func (r *repository) LoadHourlyRows(ctx context.Context, filter HourlyFilter) ([]HourlyRow, State, bool, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, State{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, state, available, err := r.LoadHourlyRowsTx(ctx, tx, filter)
	if err != nil {
		return nil, State{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, State{}, false, err
	}
	return rows, state, available, nil
}

func (r *repository) LoadHourlyRowsTx(ctx context.Context, tx *sql.Tx, filter HourlyFilter) ([]HourlyRow, State, bool, error) {
	if filter.FromMS >= filter.ToMS {
		state, err := stateQuery(ctx, tx)
		return []HourlyRow{}, state, err == nil && state.SchemaVersion == SchemaVersion, err
	}
	state, err := stateQuery(ctx, tx)
	if err != nil {
		return nil, State{}, false, err
	}
	if state.SchemaVersion != SchemaVersion {
		return nil, state, false, nil
	}
	revision, err := structureRevision(ctx, tx)
	if err != nil {
		return nil, State{}, false, err
	}
	if state.StructureRevision != revision {
		grouped := map[hourlyKey]*HourlyRow{}
		if err := mergeRawHourlyRows(ctx, tx, filter, filter.FromMS, filter.ToMS, 0, false, grouped); err != nil {
			return nil, State{}, false, err
		}
		return sortedHourlyRows(grouped), state, true, nil
	}

	grouped := map[hourlyKey]*HourlyRow{}
	fullStartMS := ceilHourMS(filter.FromMS)
	fullEndMS := floorHourMS(filter.ToMS)
	if fullStartMS < fullEndMS {
		if err := mergeStoredHourlyRows(ctx, tx, revision, filter, fullStartMS, fullEndMS, grouped); err != nil {
			return nil, State{}, false, err
		}
		if err := mergeRawHourlyRows(ctx, tx, filter, fullStartMS, fullEndMS, state.CoverageEventID, true, grouped); err != nil {
			return nil, State{}, false, err
		}
	}
	if filter.FromMS < fullStartMS {
		if err := mergeRawHourlyRows(ctx, tx, filter, filter.FromMS, min(fullStartMS, filter.ToMS), 0, false, grouped); err != nil {
			return nil, State{}, false, err
		}
	}
	if fullEndMS < filter.ToMS {
		if err := mergeRawHourlyRows(ctx, tx, filter, max(fullEndMS, filter.FromMS), filter.ToMS, 0, false, grouped); err != nil {
			return nil, State{}, false, err
		}
	}
	return sortedHourlyRows(grouped), state, true, nil
}

func mergeStoredHourlyRows(
	ctx context.Context,
	tx *sql.Tx,
	revision string,
	filter HourlyFilter,
	fromMS int64,
	toMS int64,
	grouped map[hourlyKey]*HourlyRow,
) error {
	conditions, args := storedHourlyConditions(revision, filter, fromMS, toMS)
	bucketExpr := "bucket_ms"
	if filter.CollapseBuckets {
		bucketExpr = "0"
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`select
		%s,
		model, billing_model, pricing_model, service_tier, context_threshold_tokens, failed,
		sum(calls), sum(input_tokens), sum(output_tokens), sum(reasoning_tokens),
		sum(cached_tokens), sum(cache_read_tokens), sum(cache_creation_tokens),
		sum(long_input_tokens), sum(long_output_tokens), sum(long_cached_tokens),
		sum(long_cache_read_tokens), sum(long_cache_creation_tokens),
		sum(total_tokens), sum(latency_sum_ms), sum(latency_samples), sum(zero_token_calls)
	from usage_pricing_hourly_rollups_v1
	where %s
	group by 1, 2, 3, 4, 5, 6, 7
	order by 1, 2, 3, 4, 5, 6, 7`, bucketExpr, strings.Join(conditions, " and ")), args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanAndMergeHourlyRows(rows, grouped)
}

func mergeRawHourlyRows(
	ctx context.Context,
	tx *sql.Tx,
	filter HourlyFilter,
	fromMS int64,
	toMS int64,
	afterID int64,
	useAfterID bool,
	grouped map[hourlyKey]*HourlyRow,
) error {
	query, args := rawHourlyStatement(filter, fromMS, toMS, afterID, useAfterID)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanAndMergeHourlyRows(rows, grouped)
}

func rawHourlyStatement(filter HourlyFilter, fromMS, toMS, afterID int64, useAfterID bool) (string, []any) {
	conditions := []string{"e.timestamp_ms >= ?", "e.timestamp_ms < ?"}
	args := []any{fromMS, toMS}
	if useAfterID {
		conditions = append(conditions, "e.id > ?")
		args = append(args, afterID)
	}
	models := normalizeValues(filter.Models)
	if len(models) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(models)), ",")
		conditions = append(conditions, "e.model in ("+placeholders+")")
		for _, modelID := range models {
			args = append(args, modelID)
		}
	}
	if !filter.IncludeFailed {
		conditions = append(conditions, "e.failed = 0")
	}
	if filter.FailedOnly {
		conditions = append(conditions, "e.failed = 1")
	}
	bucketExpr := fmt.Sprintf("timestamp_ms - (timestamp_ms %% %d)", hourMS)
	if filter.CollapseBuckets {
		bucketExpr = "0"
	}
	query := bandedEventsCTE(strings.Join(conditions, " and ")) + fmt.Sprintf(`
	select
		%s,
		model, billing_model_value, pricing_model_value, coalesce(service_tier, ''),
		context_threshold_tokens_value, failed, count(*),
		coalesce(sum(normalized_input_tokens_value), 0),
		coalesce(sum(output_tokens), 0),
		coalesce(sum(reasoning_tokens), 0),
		coalesce(sum(compatible_cached_tokens_value), 0),
		coalesce(sum(cache_read_tokens), 0),
		coalesce(sum(cache_creation_tokens), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then normalized_input_tokens_value else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then output_tokens else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then compatible_cached_tokens_value else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then cache_read_tokens else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then cache_creation_tokens else 0 end), 0),
		coalesce(sum(total_tokens), 0),
		coalesce(sum(case when latency_ms is not null and latency_ms != 0 then latency_ms else 0 end), 0),
		count(nullif(latency_ms, 0)),
		coalesce(sum(case when total_tokens = 0 and failed = 0 then 1 else 0 end), 0)
	from banded_events
	group by 1, 2, 3, 4, 5, 6, 7
	order by 1, 2, 3, 4, 5, 6, 7`,
		bucketExpr,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
	)
	return query, args
}

func scanAndMergeHourlyRows(rows *sql.Rows, grouped map[hourlyKey]*HourlyRow) error {
	for rows.Next() {
		var row HourlyRow
		var failed int
		if err := rows.Scan(
			&row.BucketMS,
			&row.Model,
			&row.BillingModel,
			&row.PricingModel,
			&row.ServiceTier,
			&row.ContextThresholdTokens,
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
		mergeHourlyRow(grouped, row)
	}
	return rows.Err()
}

func mergeHourlyRow(grouped map[hourlyKey]*HourlyRow, row HourlyRow) {
	key := hourlyKey{
		bucketMS:               row.BucketMS,
		model:                  row.Model,
		billingModel:           row.BillingModel,
		pricingModel:           row.PricingModel,
		serviceTier:            row.ServiceTier,
		contextThresholdTokens: row.ContextThresholdTokens,
		failed:                 row.Failed,
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

func sortedHourlyRows(grouped map[hourlyKey]*HourlyRow) []HourlyRow {
	result := make([]HourlyRow, 0, len(grouped))
	for _, row := range grouped {
		result = append(result, *row)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.BucketMS != right.BucketMS {
			return left.BucketMS < right.BucketMS
		}
		if left.Model != right.Model {
			return left.Model < right.Model
		}
		if left.BillingModel != right.BillingModel {
			return left.BillingModel < right.BillingModel
		}
		if left.PricingModel != right.PricingModel {
			return left.PricingModel < right.PricingModel
		}
		if left.ServiceTier != right.ServiceTier {
			return left.ServiceTier < right.ServiceTier
		}
		if left.ContextThresholdTokens != right.ContextThresholdTokens {
			return left.ContextThresholdTokens < right.ContextThresholdTokens
		}
		return !left.Failed && right.Failed
	})
	return result
}

func (r *repository) LoadAccountRows(ctx context.Context, accountKeys []string) ([]AccountRow, State, bool, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, State{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, state, available, err := r.LoadAccountRowsTx(ctx, tx, accountKeys)
	if err != nil {
		return nil, State{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, State{}, false, err
	}
	return rows, state, available, nil
}

func (r *repository) LoadAccountRowsTx(ctx context.Context, tx *sql.Tx, accountKeys []string) ([]AccountRow, State, bool, error) {
	keys := normalizeValues(accountKeys)
	if len(keys) == 0 {
		state, err := stateQuery(ctx, tx)
		return []AccountRow{}, state, err == nil && state.SchemaVersion == SchemaVersion, err
	}
	state, err := stateQuery(ctx, tx)
	if err != nil {
		return nil, State{}, false, err
	}
	if state.SchemaVersion != SchemaVersion {
		return nil, state, false, nil
	}
	revision, err := structureRevision(ctx, tx)
	if err != nil {
		return nil, State{}, false, err
	}
	if state.StructureRevision != revision {
		grouped := map[accountKey]*AccountRow{}
		if err := mergeRawAccountRows(ctx, tx, 0, keys, grouped); err != nil {
			return nil, State{}, false, err
		}
		return sortedAccountRows(grouped), state, true, nil
	}

	grouped := map[accountKey]*AccountRow{}
	if err := mergeStoredAccountRows(ctx, tx, revision, keys, grouped); err != nil {
		return nil, State{}, false, err
	}
	if err := mergeRawAccountRows(ctx, tx, state.CoverageEventID, keys, grouped); err != nil {
		return nil, State{}, false, err
	}
	return sortedAccountRows(grouped), state, true, nil
}

func mergeStoredAccountRows(
	ctx context.Context,
	tx *sql.Tx,
	revision string,
	accountKeys []string,
	grouped map[accountKey]*AccountRow,
) error {
	placeholders := strings.TrimRight(strings.Repeat("?,", len(accountKeys)), ",")
	args := make([]any, 0, len(accountKeys)+1)
	args = append(args, revision)
	for _, key := range accountKeys {
		args = append(args, key)
	}
	rows, err := tx.QueryContext(ctx, `select
		account_key,
		coalesce(account_snapshot, ''),
		coalesce(auth_label_snapshot, ''),
		coalesce(auth_provider_snapshot, ''),
		coalesce(auth_index, ''),
		coalesce(source, ''),
		coalesce(source_hash, ''),
		model, billing_model, pricing_model, service_tier, context_threshold_tokens,
		calls, success_calls, failure_calls,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens,
		cache_read_tokens, cache_creation_tokens,
		long_input_tokens, long_output_tokens, long_cached_tokens,
		long_cache_read_tokens, long_cache_creation_tokens,
		total_tokens, first_seen_ms, last_seen_ms, updated_at_ms
	from usage_pricing_account_rollups_v1
	where structure_revision = ? and account_key in (`+placeholders+`)
	order by account_key, last_seen_ms desc`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanAndMergeAccountRows(rows, grouped)
}

func mergeRawAccountRows(
	ctx context.Context,
	tx *sql.Tx,
	afterID int64,
	accountKeys []string,
	grouped map[accountKey]*AccountRow,
) error {
	placeholders := strings.TrimRight(strings.Repeat("?,", len(accountKeys)), ",")
	query := bandedEventsCTE("e.id > ?") + fmt.Sprintf(`
	select
		account_key_value,
		coalesce(max(nullif(account_snapshot, '')), ''),
		coalesce(max(nullif(auth_label_snapshot, '')), ''),
		coalesce(max(nullif(coalesce(nullif(auth_provider_snapshot, ''), provider, ''), '')), ''),
		coalesce(max(nullif(auth_index, '')), ''),
		coalesce(max(nullif(source, '')), ''),
		coalesce(max(nullif(source_hash, '')), ''),
		min(model), billing_model_value, pricing_model_value, coalesce(service_tier, ''),
		context_threshold_tokens_value,
		count(*),
		coalesce(sum(case when failed = 0 then 1 else 0 end), 0),
		coalesce(sum(case when failed = 1 then 1 else 0 end), 0),
		coalesce(sum(normalized_input_tokens_value), 0),
		coalesce(sum(output_tokens), 0),
		coalesce(sum(reasoning_tokens), 0),
		coalesce(sum(compatible_cached_tokens_value), 0),
		coalesce(sum(cache_read_tokens), 0),
		coalesce(sum(cache_creation_tokens), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then normalized_input_tokens_value else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then output_tokens else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then compatible_cached_tokens_value else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then cache_read_tokens else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > %d then cache_creation_tokens else 0 end), 0),
		coalesce(sum(total_tokens), 0), min(timestamp_ms), max(timestamp_ms), 0
	from banded_events
	where account_key_value in (%s)
	group by account_key_value, billing_model_value, pricing_model_value,
		coalesce(service_tier, ''), context_threshold_tokens_value
	order by account_key_value, max(timestamp_ms) desc`,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		placeholders,
	)
	args := make([]any, 0, len(accountKeys)+1)
	args = append(args, afterID)
	for _, key := range accountKeys {
		args = append(args, key)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanAndMergeAccountRows(rows, grouped)
}

func scanAndMergeAccountRows(rows *sql.Rows, grouped map[accountKey]*AccountRow) error {
	for rows.Next() {
		var row AccountRow
		if err := rows.Scan(
			&row.AccountKey,
			&row.AccountSnapshot,
			&row.AuthLabelSnapshot,
			&row.AuthProviderSnapshot,
			&row.AuthIndex,
			&row.Source,
			&row.SourceHash,
			&row.Model,
			&row.BillingModel,
			&row.PricingModel,
			&row.ServiceTier,
			&row.ContextThresholdTokens,
			&row.Calls,
			&row.SuccessCalls,
			&row.FailureCalls,
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
			&row.FirstSeenMS,
			&row.LastSeenMS,
			&row.UpdatedAtMS,
		); err != nil {
			return err
		}
		mergeAccountRow(grouped, row)
	}
	return rows.Err()
}

func mergeAccountRow(grouped map[accountKey]*AccountRow, row AccountRow) {
	key := accountKey{
		accountKey:             row.AccountKey,
		billingModel:           row.BillingModel,
		pricingModel:           row.PricingModel,
		serviceTier:            row.ServiceTier,
		contextThresholdTokens: row.ContextThresholdTokens,
	}
	entry := grouped[key]
	if entry == nil {
		copy := row
		grouped[key] = &copy
		return
	}
	fillAccountSnapshots(entry, row)
	entry.Calls += row.Calls
	entry.SuccessCalls += row.SuccessCalls
	entry.FailureCalls += row.FailureCalls
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
	if entry.FirstSeenMS == 0 || (row.FirstSeenMS > 0 && row.FirstSeenMS < entry.FirstSeenMS) {
		entry.FirstSeenMS = row.FirstSeenMS
	}
	if row.LastSeenMS > entry.LastSeenMS {
		entry.LastSeenMS = row.LastSeenMS
	}
	if row.UpdatedAtMS > entry.UpdatedAtMS {
		entry.UpdatedAtMS = row.UpdatedAtMS
	}
}

func fillAccountSnapshots(target *AccountRow, source AccountRow) {
	if target.AccountSnapshot == "" {
		target.AccountSnapshot = source.AccountSnapshot
	}
	if target.AuthLabelSnapshot == "" {
		target.AuthLabelSnapshot = source.AuthLabelSnapshot
	}
	if target.AuthProviderSnapshot == "" {
		target.AuthProviderSnapshot = source.AuthProviderSnapshot
	}
	if target.AuthIndex == "" {
		target.AuthIndex = source.AuthIndex
	}
	if target.Source == "" {
		target.Source = source.Source
	}
	if target.SourceHash == "" {
		target.SourceHash = source.SourceHash
	}
	if target.Model == "" {
		target.Model = source.Model
	}
}

func sortedAccountRows(grouped map[accountKey]*AccountRow) []AccountRow {
	result := make([]AccountRow, 0, len(grouped))
	for _, row := range grouped {
		result = append(result, *row)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if left.AccountKey != right.AccountKey {
			return left.AccountKey < right.AccountKey
		}
		if left.LastSeenMS != right.LastSeenMS {
			return left.LastSeenMS > right.LastSeenMS
		}
		if left.BillingModel != right.BillingModel {
			return left.BillingModel < right.BillingModel
		}
		if left.PricingModel != right.PricingModel {
			return left.PricingModel < right.PricingModel
		}
		if left.ServiceTier != right.ServiceTier {
			return left.ServiceTier < right.ServiceTier
		}
		return left.ContextThresholdTokens < right.ContextThresholdTokens
	})
	return result
}

func storedHourlyConditions(revision string, filter HourlyFilter, fromMS, toMS int64) ([]string, []any) {
	conditions := []string{"structure_revision = ?", "bucket_ms >= ?", "bucket_ms < ?"}
	args := []any{revision, fromMS, toMS}
	models := normalizeValues(filter.Models)
	if len(models) > 0 {
		placeholders := strings.TrimRight(strings.Repeat("?,", len(models)), ",")
		conditions = append(conditions, "model in ("+placeholders+")")
		for _, modelID := range models {
			args = append(args, modelID)
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
