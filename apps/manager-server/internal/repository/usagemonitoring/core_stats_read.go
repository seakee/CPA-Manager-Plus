package usagemonitoring

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

type dailyAggregateAccumulator struct {
	value          Aggregate
	latencySumMS   int64
	latencySamples int64
}

type dailyModelStatKey struct {
	model            string
	billingModel     string
	pricingModel     string
	contextThreshold int64
	serviceTier      string
}

func loadDailyAggregate(
	ctx context.Context,
	tx *sql.Tx,
	state State,
	projectionCoverageEventID int64,
	projectionComplete bool,
	revision string,
	filter AnalyticsFilter,
) (Aggregate, error) {
	var accumulator dailyAggregateAccumulator
	fullStartMS := ceilDayMS(filter.FromMS)
	fullEndMS := floorDayMS(filter.ToMS)
	if fullStartMS >= fullEndMS {
		if err := mergeProjectedAggregate(
			ctx,
			tx,
			projectionCoverageEventID,
			projectionComplete,
			filter,
			0,
			false,
			&accumulator,
		); err != nil {
			return Aggregate{}, err
		}
		return accumulator.result(), nil
	}

	if err := mergeStoredAggregate(ctx, tx, revision, filter, fullStartMS, fullEndMS, &accumulator); err != nil {
		return Aggregate{}, err
	}
	tailFilter := filter
	tailFilter.FromMS = fullStartMS
	tailFilter.ToMS = fullEndMS
	if err := mergeProjectedAggregate(
		ctx,
		tx,
		projectionCoverageEventID,
		projectionComplete,
		tailFilter,
		state.CoverageEventID,
		true,
		&accumulator,
	); err != nil {
		return Aggregate{}, err
	}
	if filter.FromMS < fullStartMS {
		edgeFilter := filter
		edgeFilter.ToMS = fullStartMS
		if err := mergeProjectedAggregate(
			ctx,
			tx,
			projectionCoverageEventID,
			projectionComplete,
			edgeFilter,
			0,
			false,
			&accumulator,
		); err != nil {
			return Aggregate{}, err
		}
	}
	if fullEndMS < filter.ToMS {
		edgeFilter := filter
		edgeFilter.FromMS = fullEndMS
		if err := mergeProjectedAggregate(
			ctx,
			tx,
			projectionCoverageEventID,
			projectionComplete,
			edgeFilter,
			0,
			false,
			&accumulator,
		); err != nil {
			return Aggregate{}, err
		}
	}
	return accumulator.result(), nil
}

func mergeStoredAggregate(
	ctx context.Context,
	tx *sql.Tx,
	revision string,
	filter AnalyticsFilter,
	fromMS int64,
	toMS int64,
	accumulator *dailyAggregateAccumulator,
) error {
	conditions, args := storedStatsConditions(filter, revision, fromMS, toMS)
	return scanAggregateContribution(tx.QueryRowContext(ctx, `select
		coalesce(sum(calls), 0),
		coalesce(sum(case when failed = 0 then calls else 0 end), 0),
		coalesce(sum(case when failed = 1 then calls else 0 end), 0),
		coalesce(cpamp_saturating_sum(display_input_tokens), 0),
		coalesce(cpamp_saturating_sum(display_output_tokens), 0),
		coalesce(cpamp_saturating_sum(display_non_reasoning_output_tokens), 0),
		coalesce(cpamp_saturating_sum(display_reasoning_tokens), 0),
		coalesce(cpamp_saturating_sum(display_unclassified_tokens), 0),
		coalesce(cpamp_saturating_sum(incomplete_accounting_calls), 0),
		coalesce(cpamp_saturating_sum(display_cached_tokens), 0),
		coalesce(cpamp_saturating_sum(display_cache_read_tokens), 0),
		coalesce(cpamp_saturating_sum(display_cache_creation_tokens), 0),
		coalesce(cpamp_saturating_sum(display_total_tokens), 0),
		coalesce(sum(zero_token_calls), 0),
		coalesce(sum(latency_sum_ms), 0),
		coalesce(sum(latency_samples), 0)
	from usage_monitoring_account_daily_rollups_v1
	where `+strings.Join(conditions, " and "), args...), accumulator)
}

func mergeProjectedAggregate(
	ctx context.Context,
	tx *sql.Tx,
	projectionCoverageEventID int64,
	projectionComplete bool,
	filter AnalyticsFilter,
	afterID int64,
	useAfterID bool,
	accumulator *dailyAggregateAccumulator,
) error {
	if filter.FromMS >= filter.ToMS {
		return nil
	}
	source, args := filteredEventSourceSQL(
		filter,
		projectionCoverageEventID,
		`p.requested_model as model, p.analytics_model, p.resolved_model, p.service_tier, p.failed,
		p.accounting_version, p.accounting_valid, p.accounting_quality,
		p.input_tokens, p.output_tokens, p.reasoning_tokens, p.cached_tokens,
		p.cache_tokens, p.cache_read_tokens, p.cache_creation_tokens,
		p.normalized_uncached_input_tokens, p.normalized_total_input_tokens,
		p.normalized_cache_read_tokens, p.normalized_cache_creation_tokens,
		p.normalized_non_reasoning_output_tokens, p.normalized_reasoning_output_tokens,
		p.normalized_total_output_tokens, p.unclassified_tokens, p.total_tokens, p.latency_ms`,
		usageidentity.SQLEffectiveRequestedModelExpression("e.model", "e.requested_model")+`, `+usageidentity.SQLRequestAnalyticsModelExpression("e.model", "e.requested_model")+`, coalesce(e.resolved_model, ''),
		coalesce(e.service_tier, ''), coalesce(e.failed, 0),
		coalesce(e.accounting_version, 0), coalesce(e.accounting_valid, 0),
		coalesce(e.accounting_quality, ''), coalesce(e.input_tokens, 0),
		coalesce(e.output_tokens, 0), coalesce(e.reasoning_tokens, 0),
		coalesce(e.cached_tokens, 0), coalesce(e.cache_tokens, 0),
		coalesce(e.cache_read_tokens, 0), coalesce(e.cache_creation_tokens, 0),
		e.normalized_uncached_input_tokens, e.normalized_total_input_tokens,
		e.normalized_cache_read_tokens, e.normalized_cache_creation_tokens,
		e.normalized_non_reasoning_output_tokens, e.normalized_reasoning_output_tokens,
		e.normalized_total_output_tokens, e.unclassified_tokens, e.total_tokens, e.latency_ms`,
		eventSourceOptions{
			AfterID:            afterID,
			UseAfter:           useAfterID,
			ProjectionComplete: projectionComplete,
		},
	)
	return scanAggregateContribution(tx.QueryRowContext(ctx, monitoringBandedProjectedEventsCTE(source)+`
	select
		count(*),
		coalesce(cpamp_saturating_sum(case when failed = 0 then 1 else 0 end), 0),
		coalesce(cpamp_saturating_sum(case when failed = 1 then 1 else 0 end), 0),
		coalesce(cpamp_saturating_sum(normalized_input_tokens_value), 0),
		coalesce(cpamp_saturating_sum(normalized_output_tokens_value), 0),
		coalesce(cpamp_saturating_sum(non_reasoning_output_tokens_value), 0),
		coalesce(cpamp_saturating_sum(reasoning_tokens_value), 0),
		coalesce(cpamp_saturating_sum(unclassified_tokens_value), 0),
		coalesce(cpamp_saturating_sum(incomplete_accounting_value), 0),
		coalesce(cpamp_saturating_sum(compatible_cached_tokens_value), 0),
		coalesce(cpamp_saturating_sum(normalized_cache_read_tokens_value), 0),
		coalesce(cpamp_saturating_sum(normalized_cache_creation_tokens_value), 0),
		coalesce(cpamp_saturating_sum(total_tokens_value), 0),
		coalesce(cpamp_saturating_sum(case when total_tokens_value = 0 and failed = 0 then 1 else 0 end), 0),
		coalesce(cpamp_saturating_sum(case when latency_ms is not null and latency_ms != 0 then latency_ms else 0 end), 0),
		count(nullif(latency_ms, 0))
	from banded_events`, args...), accumulator)
}

func scanAggregateContribution(row *sql.Row, accumulator *dailyAggregateAccumulator) error {
	var contribution Aggregate
	var latencySumMS int64
	var latencySamples int64
	if err := row.Scan(
		&contribution.TotalCalls,
		&contribution.SuccessCalls,
		&contribution.FailureCalls,
		&contribution.InputTokens,
		&contribution.OutputTokens,
		&contribution.NonReasoningOutputTokens,
		&contribution.ReasoningTokens,
		&contribution.UnclassifiedTokens,
		&contribution.IncompleteAccountingCalls,
		&contribution.CachedTokens,
		&contribution.CacheReadTokens,
		&contribution.CacheCreationTokens,
		&contribution.TotalTokens,
		&contribution.ZeroTokenCalls,
		&latencySumMS,
		&latencySamples,
	); err != nil {
		return err
	}
	accumulator.value.TotalCalls += contribution.TotalCalls
	accumulator.value.SuccessCalls += contribution.SuccessCalls
	accumulator.value.FailureCalls += contribution.FailureCalls
	accumulator.value.InputTokens = usage.SaturatingTokenSum(accumulator.value.InputTokens, contribution.InputTokens)
	accumulator.value.OutputTokens = usage.SaturatingTokenSum(accumulator.value.OutputTokens, contribution.OutputTokens)
	accumulator.value.NonReasoningOutputTokens = usage.SaturatingTokenSum(accumulator.value.NonReasoningOutputTokens, contribution.NonReasoningOutputTokens)
	accumulator.value.ReasoningTokens = usage.SaturatingTokenSum(accumulator.value.ReasoningTokens, contribution.ReasoningTokens)
	accumulator.value.UnclassifiedTokens = usage.SaturatingTokenSum(accumulator.value.UnclassifiedTokens, contribution.UnclassifiedTokens)
	accumulator.value.IncompleteAccountingCalls = usage.SaturatingTokenSum(accumulator.value.IncompleteAccountingCalls, contribution.IncompleteAccountingCalls)
	accumulator.value.CachedTokens = usage.SaturatingTokenSum(accumulator.value.CachedTokens, contribution.CachedTokens)
	accumulator.value.CacheReadTokens = usage.SaturatingTokenSum(accumulator.value.CacheReadTokens, contribution.CacheReadTokens)
	accumulator.value.CacheCreationTokens = usage.SaturatingTokenSum(accumulator.value.CacheCreationTokens, contribution.CacheCreationTokens)
	accumulator.value.TotalTokens = usage.SaturatingTokenSum(accumulator.value.TotalTokens, contribution.TotalTokens)
	accumulator.value.ZeroTokenCalls += contribution.ZeroTokenCalls
	accumulator.latencySumMS += latencySumMS
	accumulator.latencySamples += latencySamples
	return nil
}

func (accumulator dailyAggregateAccumulator) result() Aggregate {
	if accumulator.latencySamples > 0 {
		accumulator.value.AvgLatencyMS.Valid = true
		accumulator.value.AvgLatencyMS.Float64 = float64(accumulator.latencySumMS) / float64(accumulator.latencySamples)
	}
	return accumulator.value
}

func loadDailyModelStats(
	ctx context.Context,
	tx *sql.Tx,
	state State,
	projectionCoverageEventID int64,
	projectionComplete bool,
	revision string,
	filter AnalyticsFilter,
) ([]ModelStat, error) {
	grouped := map[dailyModelStatKey]*ModelStat{}
	fullStartMS := ceilDayMS(filter.FromMS)
	fullEndMS := floorDayMS(filter.ToMS)
	if fullStartMS >= fullEndMS {
		if err := mergeProjectedModelStats(ctx, tx, projectionCoverageEventID, projectionComplete, filter, 0, false, grouped); err != nil {
			return nil, err
		}
		return sortedDailyModelStats(grouped), nil
	}

	if err := mergeStoredModelStats(ctx, tx, revision, filter, fullStartMS, fullEndMS, grouped); err != nil {
		return nil, err
	}
	tailFilter := filter
	tailFilter.FromMS = fullStartMS
	tailFilter.ToMS = fullEndMS
	if err := mergeProjectedModelStats(
		ctx,
		tx,
		projectionCoverageEventID,
		projectionComplete,
		tailFilter,
		state.CoverageEventID,
		true,
		grouped,
	); err != nil {
		return nil, err
	}
	if filter.FromMS < fullStartMS {
		edgeFilter := filter
		edgeFilter.ToMS = fullStartMS
		if err := mergeProjectedModelStats(ctx, tx, projectionCoverageEventID, projectionComplete, edgeFilter, 0, false, grouped); err != nil {
			return nil, err
		}
	}
	if fullEndMS < filter.ToMS {
		edgeFilter := filter
		edgeFilter.FromMS = fullEndMS
		if err := mergeProjectedModelStats(ctx, tx, projectionCoverageEventID, projectionComplete, edgeFilter, 0, false, grouped); err != nil {
			return nil, err
		}
	}
	return sortedDailyModelStats(grouped), nil
}

func mergeStoredModelStats(
	ctx context.Context,
	tx *sql.Tx,
	revision string,
	filter AnalyticsFilter,
	fromMS int64,
	toMS int64,
	grouped map[dailyModelStatKey]*ModelStat,
) error {
	conditions, args := storedStatsConditions(filter, revision, fromMS, toMS)
	rows, err := tx.QueryContext(ctx, `select
		model, billing_model, pricing_model, context_threshold_tokens,
		service_tier, sum(calls),
		sum(case when failed = 0 then calls else 0 end),
		coalesce(cpamp_saturating_sum(input_tokens), 0),
		coalesce(cpamp_saturating_sum(output_tokens), 0),
		coalesce(cpamp_saturating_sum(non_reasoning_output_tokens), 0),
		coalesce(cpamp_saturating_sum(reasoning_tokens), 0),
		coalesce(cpamp_saturating_sum(unclassified_tokens), 0),
		coalesce(cpamp_saturating_sum(incomplete_accounting_calls), 0),
		coalesce(cpamp_saturating_sum(cached_tokens), 0),
		coalesce(cpamp_saturating_sum(cache_read_tokens), 0),
		coalesce(cpamp_saturating_sum(cache_creation_tokens), 0),
		coalesce(cpamp_saturating_sum(long_input_tokens), 0),
		coalesce(cpamp_saturating_sum(long_output_tokens), 0),
		coalesce(cpamp_saturating_sum(long_cached_tokens), 0),
		coalesce(cpamp_saturating_sum(long_cache_read_tokens), 0),
		coalesce(cpamp_saturating_sum(long_cache_creation_tokens), 0),
		coalesce(cpamp_saturating_sum(total_tokens), 0)
	from usage_monitoring_account_daily_rollups_v1
	where `+strings.Join(conditions, " and ")+`
	group by model, billing_model, pricing_model, context_threshold_tokens,
		service_tier`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanDailyModelStats(rows, grouped)
}

func mergeProjectedModelStats(
	ctx context.Context,
	tx *sql.Tx,
	projectionCoverageEventID int64,
	projectionComplete bool,
	filter AnalyticsFilter,
	afterID int64,
	useAfterID bool,
	grouped map[dailyModelStatKey]*ModelStat,
) error {
	if filter.FromMS >= filter.ToMS {
		return nil
	}
	source, args := filteredEventSourceSQL(
		filter,
		projectionCoverageEventID,
		`p.requested_model as model, p.analytics_model, p.resolved_model, p.service_tier, p.failed,
		p.accounting_version, p.accounting_valid, p.accounting_quality,
		p.input_tokens, p.output_tokens, p.reasoning_tokens, p.cached_tokens,
		p.cache_tokens, p.cache_read_tokens, p.cache_creation_tokens,
		p.normalized_uncached_input_tokens, p.normalized_total_input_tokens,
		p.normalized_cache_read_tokens, p.normalized_cache_creation_tokens,
		p.normalized_non_reasoning_output_tokens, p.normalized_reasoning_output_tokens,
		p.normalized_total_output_tokens, p.unclassified_tokens, p.total_tokens`,
		usageidentity.SQLEffectiveRequestedModelExpression("e.model", "e.requested_model")+`, `+usageidentity.SQLRequestAnalyticsModelExpression("e.model", "e.requested_model")+`, coalesce(e.resolved_model, ''),
		coalesce(e.service_tier, ''), coalesce(e.failed, 0),
		coalesce(e.accounting_version, 0), coalesce(e.accounting_valid, 0),
		coalesce(e.accounting_quality, ''), coalesce(e.input_tokens, 0),
		coalesce(e.output_tokens, 0), coalesce(e.reasoning_tokens, 0),
		coalesce(e.cached_tokens, 0), coalesce(e.cache_tokens, 0),
		coalesce(e.cache_read_tokens, 0), coalesce(e.cache_creation_tokens, 0),
		e.normalized_uncached_input_tokens, e.normalized_total_input_tokens,
		e.normalized_cache_read_tokens, e.normalized_cache_creation_tokens,
		e.normalized_non_reasoning_output_tokens, e.normalized_reasoning_output_tokens,
		e.normalized_total_output_tokens, e.unclassified_tokens, e.total_tokens`,
		eventSourceOptions{
			AfterID:            afterID,
			UseAfter:           useAfterID,
			ProjectionComplete: projectionComplete,
		},
	)
	query := fmt.Sprintf(`%s
	select
		analytics_model, billing_model_value, pricing_model_value,
		context_threshold_tokens_value, service_tier,
		count(*), coalesce(sum(case when failed = 0 then 1 else 0 end), 0),
		coalesce(cpamp_saturating_sum(pricing_input_tokens_value), 0),
		coalesce(cpamp_saturating_sum(pricing_output_tokens_value), 0),
		coalesce(cpamp_saturating_sum(pricing_non_reasoning_output_tokens_value), 0),
		coalesce(cpamp_saturating_sum(pricing_reasoning_tokens_value), 0),
		coalesce(cpamp_saturating_sum(pricing_unclassified_tokens_value), 0),
		coalesce(cpamp_saturating_sum(incomplete_accounting_value), 0),
		coalesce(cpamp_saturating_sum(pricing_compatible_cached_tokens_value), 0),
		coalesce(cpamp_saturating_sum(pricing_cache_read_tokens_value), 0),
		coalesce(cpamp_saturating_sum(pricing_cache_creation_tokens_value), 0),
		coalesce(cpamp_saturating_sum(case when pricing_input_tokens_value > ? then pricing_input_tokens_value else 0 end), 0),
		coalesce(cpamp_saturating_sum(case when pricing_input_tokens_value > ? then pricing_output_tokens_value else 0 end), 0),
		coalesce(cpamp_saturating_sum(case when pricing_input_tokens_value > ? then pricing_compatible_cached_tokens_value else 0 end), 0),
		coalesce(cpamp_saturating_sum(case when pricing_input_tokens_value > ? then pricing_cache_read_tokens_value else 0 end), 0),
		coalesce(cpamp_saturating_sum(case when pricing_input_tokens_value > ? then pricing_cache_creation_tokens_value else 0 end), 0),
		coalesce(cpamp_saturating_sum(total_tokens_value), 0)
	from banded_events
	group by analytics_model, billing_model_value, pricing_model_value,
		context_threshold_tokens_value, service_tier`, monitoringBandedProjectedEventsCTE(source))
	args = appendLongContextThresholdArgs(args)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanDailyModelStats(rows, grouped)
}

func scanDailyModelStats(rows *sql.Rows, grouped map[dailyModelStatKey]*ModelStat) error {
	for rows.Next() {
		var row ModelStat
		if err := rows.Scan(
			&row.Model,
			&row.BillingModel,
			&row.PricingModel,
			&row.ContextThresholdTokens,
			&row.ServiceTier,
			&row.Calls,
			&row.SuccessCalls,
			&row.InputTokens,
			&row.OutputTokens,
			&row.NonReasoningOutputTokens,
			&row.ReasoningTokens,
			&row.UnclassifiedTokens,
			&row.IncompleteAccountingCalls,
			&row.CachedTokens,
			&row.CacheReadTokens,
			&row.CacheCreationTokens,
			&row.LongInputTokens,
			&row.LongOutputTokens,
			&row.LongCachedTokens,
			&row.LongCacheReadTokens,
			&row.LongCacheCreationTokens,
			&row.TotalTokens,
		); err != nil {
			return err
		}
		key := dailyModelStatKey{
			model:            row.Model,
			billingModel:     row.BillingModel,
			pricingModel:     row.PricingModel,
			contextThreshold: row.ContextThresholdTokens,
			serviceTier:      row.ServiceTier,
		}
		entry := grouped[key]
		if entry == nil {
			copy := row
			grouped[key] = &copy
			continue
		}
		entry.Calls += row.Calls
		entry.SuccessCalls += row.SuccessCalls
		entry.InputTokens = usage.SaturatingTokenSum(entry.InputTokens, row.InputTokens)
		entry.OutputTokens = usage.SaturatingTokenSum(entry.OutputTokens, row.OutputTokens)
		entry.NonReasoningOutputTokens = usage.SaturatingTokenSum(entry.NonReasoningOutputTokens, row.NonReasoningOutputTokens)
		entry.ReasoningTokens = usage.SaturatingTokenSum(entry.ReasoningTokens, row.ReasoningTokens)
		entry.UnclassifiedTokens = usage.SaturatingTokenSum(entry.UnclassifiedTokens, row.UnclassifiedTokens)
		entry.IncompleteAccountingCalls = usage.SaturatingTokenSum(entry.IncompleteAccountingCalls, row.IncompleteAccountingCalls)
		entry.CachedTokens = usage.SaturatingTokenSum(entry.CachedTokens, row.CachedTokens)
		entry.CacheReadTokens = usage.SaturatingTokenSum(entry.CacheReadTokens, row.CacheReadTokens)
		entry.CacheCreationTokens = usage.SaturatingTokenSum(entry.CacheCreationTokens, row.CacheCreationTokens)
		entry.LongInputTokens = usage.SaturatingTokenSum(entry.LongInputTokens, row.LongInputTokens)
		entry.LongOutputTokens = usage.SaturatingTokenSum(entry.LongOutputTokens, row.LongOutputTokens)
		entry.LongCachedTokens = usage.SaturatingTokenSum(entry.LongCachedTokens, row.LongCachedTokens)
		entry.LongCacheReadTokens = usage.SaturatingTokenSum(entry.LongCacheReadTokens, row.LongCacheReadTokens)
		entry.LongCacheCreationTokens = usage.SaturatingTokenSum(entry.LongCacheCreationTokens, row.LongCacheCreationTokens)
		entry.TotalTokens = usage.SaturatingTokenSum(entry.TotalTokens, row.TotalTokens)
	}
	return rows.Err()
}

func sortedDailyModelStats(grouped map[dailyModelStatKey]*ModelStat) []ModelStat {
	result := make([]ModelStat, 0, len(grouped))
	for _, row := range grouped {
		result = append(result, *row)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Calls != result[j].Calls {
			return result[i].Calls > result[j].Calls
		}
		left := fmt.Sprintf("%s\x00%s\x00%s\x00%020d\x00%s",
			result[i].Model,
			result[i].BillingModel,
			result[i].PricingModel,
			result[i].ContextThresholdTokens,
			result[i].ServiceTier,
		)
		right := fmt.Sprintf("%s\x00%s\x00%s\x00%020d\x00%s",
			result[j].Model,
			result[j].BillingModel,
			result[j].PricingModel,
			result[j].ContextThresholdTokens,
			result[j].ServiceTier,
		)
		return left < right
	})
	return result
}
