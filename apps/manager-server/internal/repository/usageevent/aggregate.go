package usageevent

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageaccountingsql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

var (
	requestedModelExpr = usageidentity.SQLEffectiveRequestedModelExpression("model", "requested_model")
	analyticsModelExpr = usageidentity.SQLRequestAnalyticsModelExpression("model", "requested_model")
	accountingSQL      = usageaccountingsql.For("")
)

func pricingBandedUsageEventsCTEWithBaseFilter(baseFilter string) string {
	whereClause := ""
	if baseFilter != "" {
		whereClause = "\n\twhere " + baseFilter
	}
	pricingSafe := accountingSQL.PricingSafe
	pricingBucket := func(expression string) string {
		return "case when " + pricingSafe + " then " + expression + " else 0 end"
	}
	pricingUnclassified := "case when " + pricingSafe + " then " + accountingSQL.Unclassified + " else " + accountingSQL.Total + " end"
	return fmt.Sprintf(`with pricing_base_events as (
	select
		usage_events.*,
		%s as requested_model_value,
		%s as analytics_model_value,
		coalesce(nullif(resolved_model, ''), %s) as billing_model_value,
		`+pricingBucket(accountingSQL.TotalInput)+` as normalized_input_tokens_value,
		`+pricingBucket(accountingSQL.TotalOutput)+` as normalized_output_tokens_value,
		`+pricingBucket(accountingSQL.NonReasoningOutput)+` as normalized_non_reasoning_output_tokens_value,
		`+pricingBucket(accountingSQL.ReasoningOutput)+` as normalized_reasoning_output_tokens_value,
		`+pricingUnclassified+` as unclassified_tokens_value,
		`+accountingSQL.Incomplete+` as incomplete_accounting_value,
		`+pricingBucket(accountingSQL.CompatibleCached)+` as compatible_cached_tokens_value,
		`+pricingBucket(accountingSQL.CacheRead)+` as normalized_cache_read_tokens_value,
		`+pricingBucket(accountingSQL.CacheCreation)+` as normalized_cache_creation_tokens_value,
		`+accountingSQL.Total+` as accounting_total_tokens_value
	from usage_events%s
), pricing_resolved_events as (
	select
		pricing_base_events.*,
		case
			when billing_price.model is not null then billing_model_value
			when analytics_price.model is not null then pricing_base_events.analytics_model_value
			when display_price.model is not null then pricing_base_events.requested_model_value
			else billing_model_value
		end as pricing_model_value
	from pricing_base_events
	left join model_prices billing_price on billing_price.model = pricing_base_events.billing_model_value
	left join model_prices analytics_price on analytics_price.model = pricing_base_events.analytics_model_value
	left join model_prices display_price on display_price.model = pricing_base_events.requested_model_value
), banded_usage_events as (
	select
		pricing_resolved_events.*,
		coalesce((
			select max(tier.threshold_tokens)
			from model_price_context_tiers tier
			where tier.model = pricing_resolved_events.pricing_model_value
				and pricing_resolved_events.normalized_input_tokens_value > tier.threshold_tokens
		), %d) as context_threshold_tokens_value
	from pricing_resolved_events
	)`, requestedModelExpr, analyticsModelExpr, analyticsModelExpr, whereClause, model.ModelPriceBaseContextThreshold)
}

var pricingBandedUsageEventsCTE = pricingBandedUsageEventsCTEWithBaseFilter("")

// Aggregate captures roll-up metrics for a usage_events window.
type Aggregate struct {
	usage.LongContextTokens
	TotalCalls                int64
	SuccessCalls              int64
	FailureCalls              int64
	InputTokens               int64
	OutputTokens              int64
	NonReasoningOutputTokens  int64
	ReasoningTokens           int64
	UnclassifiedTokens        int64
	IncompleteAccountingCalls int64
	CachedTokens              int64
	CacheReadTokens           int64
	CacheCreationTokens       int64
	TotalTokens               int64
	AvgLatencyMS              sql.NullFloat64
	LatencySamples            int64
	ZeroTokenCalls            int64
}

// ModelStat aggregates per-model totals.
type ModelStat struct {
	usage.LongContextTokens
	usage.PricingBand
	Model                     string
	BillingModel              string
	ServiceTier               string
	Calls                     int64
	SuccessCalls              int64
	InputTokens               int64
	OutputTokens              int64
	NonReasoningOutputTokens  int64
	ReasoningTokens           int64
	UnclassifiedTokens        int64
	IncompleteAccountingCalls int64
	CachedTokens              int64
	CacheReadTokens           int64
	CacheCreationTokens       int64
	TotalTokens               int64
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

var aggregateSQL = `select
	count(*),
	cpamp_saturating_sum(case when failed = 0 then 1 else 0 end),
	cpamp_saturating_sum(case when failed = 1 then 1 else 0 end),
	coalesce(cpamp_saturating_sum(` + accountingSQL.TotalInput + `), 0),
	coalesce(cpamp_saturating_sum(` + accountingSQL.TotalOutput + `), 0),
	coalesce(cpamp_saturating_sum(` + accountingSQL.NonReasoningOutput + `), 0),
	coalesce(cpamp_saturating_sum(` + accountingSQL.ReasoningOutput + `), 0),
	coalesce(cpamp_saturating_sum(` + accountingSQL.Unclassified + `), 0),
	coalesce(cpamp_saturating_sum(` + accountingSQL.Incomplete + `), 0),
	coalesce(cpamp_saturating_sum(` + accountingSQL.CompatibleCached + `), 0),
	coalesce(cpamp_saturating_sum(` + accountingSQL.CacheRead + `), 0),
	coalesce(cpamp_saturating_sum(` + accountingSQL.CacheCreation + `), 0),
	coalesce(cpamp_saturating_sum(case when ` + accountingSQL.TotalInput + ` > ` + fmt.Sprint(usage.LongContextInputTokenThreshold) + ` then ` + accountingSQL.TotalInput + ` else 0 end), 0),
	coalesce(cpamp_saturating_sum(case when ` + accountingSQL.TotalInput + ` > ` + fmt.Sprint(usage.LongContextInputTokenThreshold) + ` then ` + accountingSQL.TotalOutput + ` else 0 end), 0),
	coalesce(cpamp_saturating_sum(case when ` + accountingSQL.TotalInput + ` > ` + fmt.Sprint(usage.LongContextInputTokenThreshold) + ` then ` + accountingSQL.CompatibleCached + ` else 0 end), 0),
	coalesce(cpamp_saturating_sum(case when ` + accountingSQL.TotalInput + ` > ` + fmt.Sprint(usage.LongContextInputTokenThreshold) + ` then ` + accountingSQL.CacheRead + ` else 0 end), 0),
	coalesce(cpamp_saturating_sum(case when ` + accountingSQL.TotalInput + ` > ` + fmt.Sprint(usage.LongContextInputTokenThreshold) + ` then ` + accountingSQL.CacheCreation + ` else 0 end), 0),
	coalesce(cpamp_saturating_sum(` + accountingSQL.Total + `), 0),
	avg(nullif(latency_ms, 0)),
	count(nullif(latency_ms, 0)),
	coalesce(cpamp_saturating_sum(case when ` + accountingSQL.Total + ` = 0 and failed = 0 then 1 else 0 end), 0)
from usage_events
where timestamp_ms >= ? and timestamp_ms < ?`

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
		&agg.NonReasoningOutputTokens,
		&agg.ReasoningTokens,
		&agg.UnclassifiedTokens,
		&agg.IncompleteAccountingCalls,
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

var topModelsSQL = fmt.Sprintf(pricingBandedUsageEventsCTEWithBaseFilter("timestamp_ms >= ? and timestamp_ms < ?")+`, top_models as (
	select
		analytics_model_value as model,
		count(*) as model_calls
	from banded_usage_events
	group by analytics_model_value
	order by model_calls desc
	limit ?
)
select
	e.analytics_model_value as model,
	e.billing_model_value as billing_model,
	e.pricing_model_value,
	e.context_threshold_tokens_value,
	coalesce(e.service_tier, '') as service_tier,
	count(*) as calls,
	cpamp_saturating_sum(case when e.failed = 0 then 1 else 0 end) as success,
	coalesce(cpamp_saturating_sum(e.normalized_input_tokens_value), 0),
	coalesce(cpamp_saturating_sum(e.normalized_output_tokens_value), 0),
	coalesce(cpamp_saturating_sum(e.normalized_non_reasoning_output_tokens_value), 0),
	coalesce(cpamp_saturating_sum(e.normalized_reasoning_output_tokens_value), 0),
	coalesce(cpamp_saturating_sum(e.unclassified_tokens_value), 0),
	coalesce(cpamp_saturating_sum(e.incomplete_accounting_value), 0),
	coalesce(cpamp_saturating_sum(e.compatible_cached_tokens_value), 0),
	coalesce(cpamp_saturating_sum(e.normalized_cache_read_tokens_value), 0),
	coalesce(cpamp_saturating_sum(e.normalized_cache_creation_tokens_value), 0),
	coalesce(cpamp_saturating_sum(case when e.normalized_input_tokens_value > %[1]d then e.normalized_input_tokens_value else 0 end), 0),
	coalesce(cpamp_saturating_sum(case when e.normalized_input_tokens_value > %[1]d then e.normalized_output_tokens_value else 0 end), 0),
	coalesce(cpamp_saturating_sum(case when e.normalized_input_tokens_value > %[1]d then e.compatible_cached_tokens_value else 0 end), 0),
	coalesce(cpamp_saturating_sum(case when e.normalized_input_tokens_value > %[1]d then e.normalized_cache_read_tokens_value else 0 end), 0),
	coalesce(cpamp_saturating_sum(case when e.normalized_input_tokens_value > %[1]d then e.normalized_cache_creation_tokens_value else 0 end), 0),
	coalesce(cpamp_saturating_sum(e.accounting_total_tokens_value), 0)
from banded_usage_events e
join top_models t on t.model = e.analytics_model_value
group by e.analytics_model_value, billing_model, e.pricing_model_value, e.context_threshold_tokens_value, coalesce(e.service_tier, '')
order by max(t.model_calls) desc, e.analytics_model_value, calls desc`, usage.LongContextInputTokenThreshold)

// TopModelsBetween returns the most active models ordered by call count.
func (r *repository) TopModelsBetween(ctx context.Context, fromMs, toMs int64, limit int) ([]ModelStat, error) {
	if limit <= 0 {
		limit = 5
	}
	rows, err := r.db.QueryContext(ctx, topModelsSQL, fromMs, toMs, limit)
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
			&stat.PricingModel,
			&stat.ContextThresholdTokens,
			&stat.ServiceTier,
			&stat.Calls,
			&stat.SuccessCalls,
			&stat.InputTokens,
			&stat.OutputTokens,
			&stat.NonReasoningOutputTokens,
			&stat.ReasoningTokens,
			&stat.UnclassifiedTokens,
			&stat.IncompleteAccountingCalls,
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

var modelStatsSQL = fmt.Sprintf(pricingBandedUsageEventsCTE+`
select
	analytics_model_value as model,
	billing_model_value as billing_model,
	pricing_model_value,
	context_threshold_tokens_value,
	coalesce(service_tier, '') as service_tier,
	count(*) as calls,
	cpamp_saturating_sum(case when failed = 0 then 1 else 0 end) as success,
	coalesce(cpamp_saturating_sum(normalized_input_tokens_value), 0),
	coalesce(cpamp_saturating_sum(normalized_output_tokens_value), 0),
	coalesce(cpamp_saturating_sum(normalized_non_reasoning_output_tokens_value), 0),
	coalesce(cpamp_saturating_sum(normalized_reasoning_output_tokens_value), 0),
	coalesce(cpamp_saturating_sum(unclassified_tokens_value), 0),
	coalesce(cpamp_saturating_sum(incomplete_accounting_value), 0),
	coalesce(cpamp_saturating_sum(compatible_cached_tokens_value), 0),
	coalesce(cpamp_saturating_sum(normalized_cache_read_tokens_value), 0),
	coalesce(cpamp_saturating_sum(normalized_cache_creation_tokens_value), 0),
	coalesce(cpamp_saturating_sum(case when normalized_input_tokens_value > %[1]d then normalized_input_tokens_value else 0 end), 0),
	coalesce(cpamp_saturating_sum(case when normalized_input_tokens_value > %[1]d then normalized_output_tokens_value else 0 end), 0),
	coalesce(cpamp_saturating_sum(case when normalized_input_tokens_value > %[1]d then compatible_cached_tokens_value else 0 end), 0),
	coalesce(cpamp_saturating_sum(case when normalized_input_tokens_value > %[1]d then normalized_cache_read_tokens_value else 0 end), 0),
	coalesce(cpamp_saturating_sum(case when normalized_input_tokens_value > %[1]d then normalized_cache_creation_tokens_value else 0 end), 0),
	coalesce(cpamp_saturating_sum(accounting_total_tokens_value), 0)
from banded_usage_events
where timestamp_ms >= ? and timestamp_ms < ?
group by analytics_model_value, billing_model, pricing_model_value, context_threshold_tokens_value, coalesce(service_tier, '')
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
			&stat.PricingModel,
			&stat.ContextThresholdTokens,
			&stat.ServiceTier,
			&stat.Calls,
			&stat.SuccessCalls,
			&stat.InputTokens,
			&stat.OutputTokens,
			&stat.NonReasoningOutputTokens,
			&stat.ReasoningTokens,
			&stat.UnclassifiedTokens,
			&stat.IncompleteAccountingCalls,
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

var recentFailuresSQL = `select
	timestamp_ms, ` + analyticsModelExpr + ` as model,
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
	coalesce(cpamp_saturating_sum(`+accountingSQL.Total+`), 0),
	cpamp_saturating_sum(case when failed = 0 then 1 else 0 end),
	cpamp_saturating_sum(case when failed = 1 then 1 else 0 end)
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
