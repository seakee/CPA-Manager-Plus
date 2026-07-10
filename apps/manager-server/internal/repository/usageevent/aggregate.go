package usageevent

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

// Aggregate captures roll-up metrics for a usage_events window.
type Aggregate struct {
	usage.LongContextTokens
	TotalCalls          int64
	SuccessCalls        int64
	FailureCalls        int64
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
	AvgLatencyMS        sql.NullFloat64
	LatencySamples      int64
	ZeroTokenCalls      int64
}

// ModelStat aggregates per-model totals.
type ModelStat struct {
	usage.LongContextTokens
	Model               string
	BillingModel        string
	ServiceTier         string
	Calls               int64
	SuccessCalls        int64
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
}

// RecentFailure holds the columns required to display a recent failure entry.
type RecentFailure struct {
	TimestampMS            int64
	Model                  string
	APIKeyHash             string
	Source                 string
	SourceHash             string
	AuthIndex              string
	Endpoint               string
	LatencyMS              sql.NullInt64
	AccountSnapshot        string
	AuthLabelSnapshot      string
	AuthProviderSnapshot   string
	AuthProjectIDSnapshot  string
	FailStatusCode         sql.NullInt64
	FailSummary            string
	ResponseMetadata       *usage.ResponseHeaderMetadata
	HeaderQuotaRecoverAtMS sql.NullInt64
	HeaderQuotaUsedPercent sql.NullFloat64
	HeaderQuotaPlanType    string
	HeaderErrorKind        string
	HeaderErrorCode        string
	HeaderTraceID          string
}

var aggregateSQL = fmt.Sprintf(`select
	count(*),
	sum(case when failed = 0 then 1 else 0 end),
	sum(case when failed = 1 then 1 else 0 end),
	coalesce(sum(input_tokens), 0),
	coalesce(sum(output_tokens), 0),
	coalesce(sum(reasoning_tokens), 0),
	coalesce(sum(max(max(cached_tokens, cache_tokens) - max(cache_read_tokens, 0) - max(cache_creation_tokens, 0), 0)), 0),
	coalesce(sum(cache_read_tokens), 0),
	coalesce(sum(cache_creation_tokens), 0),
	coalesce(sum(case when input_tokens > %[1]d then input_tokens else 0 end), 0),
	coalesce(sum(case when input_tokens > %[1]d then output_tokens else 0 end), 0),
	coalesce(sum(case when input_tokens > %[1]d then max(max(cached_tokens, cache_tokens) - max(cache_read_tokens, 0) - max(cache_creation_tokens, 0), 0) else 0 end), 0),
	coalesce(sum(case when input_tokens > %[1]d then cache_read_tokens else 0 end), 0),
	coalesce(sum(case when input_tokens > %[1]d then cache_creation_tokens else 0 end), 0),
	coalesce(sum(total_tokens), 0),
	avg(nullif(latency_ms, 0)),
	count(nullif(latency_ms, 0)),
	coalesce(sum(case when total_tokens = 0 and failed = 0 then 1 else 0 end), 0)
from usage_events
where timestamp_ms >= ? and timestamp_ms < ?`, usage.LongContextInputTokenThreshold)

// AggregateBetween computes summary metrics over [fromMs, toMs).
func (r *repository) AggregateBetween(ctx context.Context, fromMs, toMs int64) (Aggregate, error) {
	row := r.db.QueryRowContext(ctx, aggregateSQL, fromMs, toMs)
	var agg Aggregate
	var success, failure sql.NullInt64
	if err := row.Scan(
		&agg.TotalCalls,
		&success,
		&failure,
		&agg.InputTokens,
		&agg.OutputTokens,
		&agg.ReasoningTokens,
		&agg.CachedTokens,
		&agg.CacheReadTokens,
		&agg.CacheCreationTokens,
		&agg.LongInputTokens,
		&agg.LongOutputTokens,
		&agg.LongCachedTokens,
		&agg.LongCacheReadTokens,
		&agg.LongCacheCreationTokens,
		&agg.TotalTokens,
		&agg.AvgLatencyMS,
		&agg.LatencySamples,
		&agg.ZeroTokenCalls,
	); err != nil {
		return Aggregate{}, err
	}
	agg.SuccessCalls = success.Int64
	agg.FailureCalls = failure.Int64
	return agg, nil
}

var topModelsSQL = fmt.Sprintf(`with top_models as (
	select
		model,
		count(*) as model_calls
	from usage_events
	where timestamp_ms >= ? and timestamp_ms < ?
	group by model
	order by model_calls desc
	limit ?
)
select
	e.model,
	coalesce(nullif(e.resolved_model, ''), e.model) as billing_model,
	coalesce(e.service_tier, '') as service_tier,
	count(*) as calls,
	sum(case when e.failed = 0 then 1 else 0 end) as success,
	coalesce(sum(e.input_tokens), 0),
	coalesce(sum(e.output_tokens), 0),
	coalesce(sum(e.reasoning_tokens), 0),
	coalesce(sum(max(max(e.cached_tokens, e.cache_tokens) - max(e.cache_read_tokens, 0) - max(e.cache_creation_tokens, 0), 0)), 0),
	coalesce(sum(e.cache_read_tokens), 0),
	coalesce(sum(e.cache_creation_tokens), 0),
	coalesce(sum(case when e.input_tokens > %[1]d then e.input_tokens else 0 end), 0),
	coalesce(sum(case when e.input_tokens > %[1]d then e.output_tokens else 0 end), 0),
	coalesce(sum(case when e.input_tokens > %[1]d then max(max(e.cached_tokens, e.cache_tokens) - max(e.cache_read_tokens, 0) - max(e.cache_creation_tokens, 0), 0) else 0 end), 0),
	coalesce(sum(case when e.input_tokens > %[1]d then e.cache_read_tokens else 0 end), 0),
	coalesce(sum(case when e.input_tokens > %[1]d then e.cache_creation_tokens else 0 end), 0),
	coalesce(sum(e.total_tokens), 0)
from usage_events e
join top_models t on t.model = e.model
where e.timestamp_ms >= ? and e.timestamp_ms < ?
group by e.model, billing_model, coalesce(e.service_tier, '')
order by max(t.model_calls) desc, e.model, calls desc`, usage.LongContextInputTokenThreshold)

// TopModelsBetween returns the most active models ordered by call count.
func (r *repository) TopModelsBetween(ctx context.Context, fromMs, toMs int64, limit int) ([]ModelStat, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := r.db.QueryContext(ctx, topModelsSQL, fromMs, toMs, limit, fromMs, toMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make([]ModelStat, 0, limit)
	for rows.Next() {
		var stat ModelStat
		if err := rows.Scan(
			&stat.Model,
			&stat.BillingModel,
			&stat.ServiceTier,
			&stat.Calls,
			&stat.SuccessCalls,
			&stat.InputTokens,
			&stat.OutputTokens,
			&stat.ReasoningTokens,
			&stat.CachedTokens,
			&stat.CacheReadTokens,
			&stat.CacheCreationTokens,
			&stat.LongInputTokens,
			&stat.LongOutputTokens,
			&stat.LongCachedTokens,
			&stat.LongCacheReadTokens,
			&stat.LongCacheCreationTokens,
			&stat.TotalTokens,
		); err != nil {
			return nil, err
		}
		stats = append(stats, stat)
	}
	return stats, rows.Err()
}

var modelStatsSQL = fmt.Sprintf(`select
	model,
	coalesce(nullif(resolved_model, ''), model) as billing_model,
	coalesce(service_tier, '') as service_tier,
	count(*) as calls,
	sum(case when failed = 0 then 1 else 0 end) as success,
	coalesce(sum(input_tokens), 0),
	coalesce(sum(output_tokens), 0),
	coalesce(sum(reasoning_tokens), 0),
	coalesce(sum(max(max(cached_tokens, cache_tokens) - max(cache_read_tokens, 0) - max(cache_creation_tokens, 0), 0)), 0),
	coalesce(sum(cache_read_tokens), 0),
	coalesce(sum(cache_creation_tokens), 0),
	coalesce(sum(case when input_tokens > %[1]d then input_tokens else 0 end), 0),
	coalesce(sum(case when input_tokens > %[1]d then output_tokens else 0 end), 0),
	coalesce(sum(case when input_tokens > %[1]d then max(max(cached_tokens, cache_tokens) - max(cache_read_tokens, 0) - max(cache_creation_tokens, 0), 0) else 0 end), 0),
	coalesce(sum(case when input_tokens > %[1]d then cache_read_tokens else 0 end), 0),
	coalesce(sum(case when input_tokens > %[1]d then cache_creation_tokens else 0 end), 0),
	coalesce(sum(total_tokens), 0)
from usage_events
where timestamp_ms >= ? and timestamp_ms < ?
group by model, billing_model, coalesce(service_tier, '')
order by calls desc`, usage.LongContextInputTokenThreshold)

// ModelStatsBetween returns per-model totals for all models in a window.
func (r *repository) ModelStatsBetween(ctx context.Context, fromMs, toMs int64) ([]ModelStat, error) {
	rows, err := r.db.QueryContext(ctx, modelStatsSQL, fromMs, toMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := make([]ModelStat, 0)
	for rows.Next() {
		var stat ModelStat
		if err := rows.Scan(
			&stat.Model,
			&stat.BillingModel,
			&stat.ServiceTier,
			&stat.Calls,
			&stat.SuccessCalls,
			&stat.InputTokens,
			&stat.OutputTokens,
			&stat.ReasoningTokens,
			&stat.CachedTokens,
			&stat.CacheReadTokens,
			&stat.CacheCreationTokens,
			&stat.LongInputTokens,
			&stat.LongOutputTokens,
			&stat.LongCachedTokens,
			&stat.LongCacheReadTokens,
			&stat.LongCacheCreationTokens,
			&stat.TotalTokens,
		); err != nil {
			return nil, err
		}
		stats = append(stats, stat)
	}
	return stats, rows.Err()
}

const recentFailuresSQL = `select
	timestamp_ms, model,
	coalesce(api_key_hash, ''),
	coalesce(source, ''),
	coalesce(source_hash, ''),
	coalesce(auth_index, ''),
	coalesce(endpoint, ''),
	latency_ms,
	coalesce(account_snapshot, ''),
	coalesce(auth_label_snapshot, ''),
	coalesce(auth_provider_snapshot, ''),
	coalesce(auth_project_id_snapshot, ''),
	fail_status_code,
	coalesce(fail_summary, ''),
	coalesce(response_metadata_json, ''),
	header_quota_recover_at_ms,
	header_quota_used_percent,
	coalesce(header_quota_plan_type, ''),
	coalesce(header_error_kind, ''),
	coalesce(header_error_code, ''),
	coalesce(header_trace_id, '')
from usage_events
where failed = 1 and timestamp_ms >= ? and timestamp_ms < ?
order by timestamp_ms desc, id desc
limit ?`

// RecentFailuresBetween returns the most recent failed events.
func (r *repository) RecentFailuresBetween(ctx context.Context, fromMs, toMs int64, limit int) ([]RecentFailure, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := r.db.QueryContext(ctx, recentFailuresSQL, fromMs, toMs, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	results := make([]RecentFailure, 0, limit)
	for rows.Next() {
		var rf RecentFailure
		var responseMetadataJSON string
		if err := rows.Scan(
			&rf.TimestampMS,
			&rf.Model,
			&rf.APIKeyHash,
			&rf.Source,
			&rf.SourceHash,
			&rf.AuthIndex,
			&rf.Endpoint,
			&rf.LatencyMS,
			&rf.AccountSnapshot,
			&rf.AuthLabelSnapshot,
			&rf.AuthProviderSnapshot,
			&rf.AuthProjectIDSnapshot,
			&rf.FailStatusCode,
			&rf.FailSummary,
			&responseMetadataJSON,
			&rf.HeaderQuotaRecoverAtMS,
			&rf.HeaderQuotaUsedPercent,
			&rf.HeaderQuotaPlanType,
			&rf.HeaderErrorKind,
			&rf.HeaderErrorCode,
			&rf.HeaderTraceID,
		); err != nil {
			return nil, err
		}
		rf.ResponseMetadata = usage.ResponseHeaderMetadataFromJSON(responseMetadataJSON)
		results = append(results, rf)
	}
	return results, rows.Err()
}

// HourlyTimelineBetween returns hourly buckets relative to fromMs over [fromMs, toMs).
func (r *repository) HourlyTimelineBetween(ctx context.Context, fromMs, toMs int64) ([]TimelinePoint, error) {
	return r.BucketTimelineBetween(ctx, fromMs, toMs, 3600000)
}

// BucketTimelineBetween returns buckets relative to fromMs over [fromMs, toMs).
func (r *repository) BucketTimelineBetween(ctx context.Context, fromMs, toMs int64, bucketMs int64) ([]TimelinePoint, error) {
	if bucketMs <= 0 {
		bucketMs = 3600000
	}
	rows, err := r.db.QueryContext(ctx, `select
	cast((timestamp_ms - ?) / ? as integer) as bucket_index,
	count(*),
	coalesce(sum(total_tokens), 0),
	sum(case when failed = 0 then 1 else 0 end),
	sum(case when failed = 1 then 1 else 0 end)
from usage_events
where timestamp_ms >= ? and timestamp_ms < ?
group by bucket_index
order by bucket_index`, fromMs, bucketMs, fromMs, toMs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	points := make([]TimelinePoint, 0)
	for rows.Next() {
		var bucketIndex int64
		var point TimelinePoint
		if err := rows.Scan(&bucketIndex, &point.Calls, &point.Tokens, &point.Success, &point.Failure); err != nil {
			return nil, err
		}
		if bucketIndex < 0 {
			continue
		}
		point.BucketMS = fromMs + bucketIndex*bucketMs
		points = append(points, point)
	}
	return points, rows.Err()
}
