package usagemonitoring

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

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
			eventSourceOptions{},
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
		eventSourceOptions{AfterID: state.CoverageEventID, UseAfter: true},
		&accumulator,
	); err != nil {
		return Aggregate{}, err
	}
	markerFilter := tailFilter
	if err := mergeProjectedAggregate(
		ctx,
		tx,
		projectionCoverageEventID,
		projectionComplete,
		markerFilter,
		eventSourceOptions{
			MaxID:           state.CoverageEventID,
			UseMax:          true,
			CodexMarkerOnly: true,
		},
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
			eventSourceOptions{},
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
			eventSourceOptions{},
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
		coalesce(sum(input_tokens), 0),
		coalesce(sum(output_tokens), 0),
		coalesce(sum(reasoning_tokens), 0),
		coalesce(sum(cached_tokens), 0),
		coalesce(sum(cache_read_tokens), 0),
		coalesce(sum(cache_creation_tokens), 0),
		coalesce(sum(total_tokens), 0),
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
	options eventSourceOptions,
	accumulator *dailyAggregateAccumulator,
) error {
	if filter.FromMS >= filter.ToMS {
		return nil
	}
	source, args := filteredEventSourceSQL(
		filter,
		projectionCoverageEventID,
		`p.failed, p.normalized_total_input_tokens, p.output_tokens,
		p.reasoning_tokens, p.cached_tokens, p.cache_tokens,
		p.cache_read_tokens, p.cache_creation_tokens, p.total_tokens,
		p.latency_ms`,
		`coalesce(e.failed, 0),
		coalesce(e.normalized_total_input_tokens, e.input_tokens, 0),
		coalesce(e.output_tokens, 0), coalesce(e.reasoning_tokens, 0),
		coalesce(e.cached_tokens, 0), coalesce(e.cache_tokens, 0),
		coalesce(e.cache_read_tokens, 0), coalesce(e.cache_creation_tokens, 0),
		coalesce(e.total_tokens, 0), e.latency_ms`,
		eventSourceOptions{
			AfterID:            options.AfterID,
			UseAfter:           options.UseAfter,
			MaxID:              options.MaxID,
			UseMax:             options.UseMax,
			CodexMarkerOnly:    options.CodexMarkerOnly,
			ProjectionComplete: projectionComplete,
		},
	)
	return scanAggregateContribution(tx.QueryRowContext(ctx, `with filtered_events as (`+source+`)
	select
		count(*),
		coalesce(sum(case when failed = 0 then 1 else 0 end), 0),
		coalesce(sum(case when failed = 1 then 1 else 0 end), 0),
		coalesce(sum(normalized_total_input_tokens), 0),
		coalesce(sum(output_tokens), 0),
		coalesce(sum(reasoning_tokens), 0),
		coalesce(sum(max(max(cached_tokens, cache_tokens) - max(cache_read_tokens, 0) - max(cache_creation_tokens, 0), 0)), 0),
		coalesce(sum(cache_read_tokens), 0),
		coalesce(sum(cache_creation_tokens), 0),
		coalesce(sum(total_tokens), 0),
		coalesce(sum(case when total_tokens = 0 and failed = 0 then 1 else 0 end), 0),
		coalesce(sum(case when latency_ms is not null and latency_ms != 0 then latency_ms else 0 end), 0),
		count(nullif(latency_ms, 0))
	from filtered_events`, args...), accumulator)
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
		&contribution.ReasoningTokens,
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
	accumulator.value.InputTokens += contribution.InputTokens
	accumulator.value.OutputTokens += contribution.OutputTokens
	accumulator.value.ReasoningTokens += contribution.ReasoningTokens
	accumulator.value.CachedTokens += contribution.CachedTokens
	accumulator.value.CacheReadTokens += contribution.CacheReadTokens
	accumulator.value.CacheCreationTokens += contribution.CacheCreationTokens
	accumulator.value.TotalTokens += contribution.TotalTokens
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
		if err := mergeProjectedModelStats(ctx, tx, projectionCoverageEventID, projectionComplete, filter, eventSourceOptions{}, grouped); err != nil {
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
		eventSourceOptions{AfterID: state.CoverageEventID, UseAfter: true},
		grouped,
	); err != nil {
		return nil, err
	}
	if err := mergeProjectedModelStats(
		ctx,
		tx,
		projectionCoverageEventID,
		projectionComplete,
		tailFilter,
		eventSourceOptions{
			MaxID:           state.CoverageEventID,
			UseMax:          true,
			CodexMarkerOnly: true,
		},
		grouped,
	); err != nil {
		return nil, err
	}
	if filter.FromMS < fullStartMS {
		edgeFilter := filter
		edgeFilter.ToMS = fullStartMS
		if err := mergeProjectedModelStats(ctx, tx, projectionCoverageEventID, projectionComplete, edgeFilter, eventSourceOptions{}, grouped); err != nil {
			return nil, err
		}
	}
	if fullEndMS < filter.ToMS {
		edgeFilter := filter
		edgeFilter.FromMS = fullEndMS
		if err := mergeProjectedModelStats(ctx, tx, projectionCoverageEventID, projectionComplete, edgeFilter, eventSourceOptions{}, grouped); err != nil {
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
		sum(input_tokens), sum(output_tokens), sum(reasoning_tokens),
		sum(cached_tokens), sum(cache_read_tokens), sum(cache_creation_tokens),
		sum(long_input_tokens), sum(long_output_tokens), sum(long_cached_tokens),
		sum(long_cache_read_tokens), sum(long_cache_creation_tokens),
		sum(total_tokens)
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
	options eventSourceOptions,
	grouped map[dailyModelStatKey]*ModelStat,
) error {
	if filter.FromMS >= filter.ToMS {
		return nil
	}
	source, args := filteredEventSourceSQL(
		filter,
		projectionCoverageEventID,
		`p.requested_model as model, p.analytics_model, p.resolved_model, p.service_tier, p.failed,
		p.normalized_total_input_tokens, p.output_tokens, p.reasoning_tokens,
		p.cached_tokens, p.cache_tokens, p.cache_read_tokens,
		p.cache_creation_tokens, p.total_tokens`,
		usageidentity.SQLEffectiveRequestedModelExpression("e.model", "e.requested_model")+`, `+usageidentity.SQLRequestAnalyticsModelExpression("e.model", "e.requested_model")+`, coalesce(e.resolved_model, ''),
		coalesce(e.service_tier, ''), coalesce(e.failed, 0),
		coalesce(e.normalized_total_input_tokens, e.input_tokens, 0),
		coalesce(e.output_tokens, 0), coalesce(e.reasoning_tokens, 0),
		coalesce(e.cached_tokens, 0), coalesce(e.cache_tokens, 0),
		coalesce(e.cache_read_tokens, 0), coalesce(e.cache_creation_tokens, 0),
		coalesce(e.total_tokens, 0)`,
		eventSourceOptions{
			AfterID:            options.AfterID,
			UseAfter:           options.UseAfter,
			MaxID:              options.MaxID,
			UseMax:             options.UseMax,
			CodexMarkerOnly:    options.CodexMarkerOnly,
			ProjectionComplete: projectionComplete,
		},
	)
	query := fmt.Sprintf(`%s
	select
		analytics_model, billing_model_value, pricing_model_value,
		context_threshold_tokens_value, service_tier,
		count(*), coalesce(sum(case when failed = 0 then 1 else 0 end), 0),
		coalesce(sum(normalized_total_input_tokens), 0),
		coalesce(sum(output_tokens), 0), coalesce(sum(reasoning_tokens), 0),
		coalesce(sum(compatible_cached_tokens_value), 0),
		coalesce(sum(cache_read_tokens), 0), coalesce(sum(cache_creation_tokens), 0),
		coalesce(sum(case when normalized_total_input_tokens > ? then normalized_total_input_tokens else 0 end), 0),
		coalesce(sum(case when normalized_total_input_tokens > ? then output_tokens else 0 end), 0),
		coalesce(sum(case when normalized_total_input_tokens > ? then compatible_cached_tokens_value else 0 end), 0),
		coalesce(sum(case when normalized_total_input_tokens > ? then cache_read_tokens else 0 end), 0),
		coalesce(sum(case when normalized_total_input_tokens > ? then cache_creation_tokens else 0 end), 0),
		coalesce(sum(total_tokens), 0)
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
