package usagemonitoring

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

func (r *repository) LoadAggregate(ctx context.Context, filter AnalyticsFilter) (Aggregate, State, bool, error) {
	if !SupportsEventProjectionFilter(filter) {
		return Aggregate{}, State{}, false, nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Aggregate{}, State{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	state, available, projectionComplete, err := projectionReadState(ctx, tx)
	if err != nil || !available {
		return Aggregate{}, state, available, err
	}
	statsState, revision, dailyAvailable, err := statsReadState(ctx, tx)
	if err != nil {
		return Aggregate{}, state, false, err
	}
	if dailyAvailable && SupportsStatsFilter(filter) {
		aggregate, err := loadDailyAggregate(
			ctx,
			tx,
			statsState,
			state.CoverageEventID,
			projectionComplete,
			revision,
			filter,
		)
		if err != nil {
			return Aggregate{}, state, false, err
		}
		if err := tx.Commit(); err != nil {
			return Aggregate{}, state, false, err
		}
		return aggregate, state, true, nil
	}

	source, args := filteredEventSourceSQL(
		filter,
		state.CoverageEventID,
		`p.failed, p.normalized_total_input_tokens, p.output_tokens,
		p.reasoning_tokens, p.cached_tokens, p.cache_tokens,
		p.cache_read_tokens, p.cache_creation_tokens, p.total_tokens,
		p.latency_ms`,
		`e.failed, coalesce(e.normalized_total_input_tokens, e.input_tokens, 0),
		coalesce(e.output_tokens, 0), coalesce(e.reasoning_tokens, 0),
		coalesce(e.cached_tokens, 0), coalesce(e.cache_tokens, 0),
		coalesce(e.cache_read_tokens, 0), coalesce(e.cache_creation_tokens, 0),
		coalesce(e.total_tokens, 0), e.latency_ms`,
		eventSourceOptions{ProjectionComplete: projectionComplete},
	)
	query := fmt.Sprintf(`with filtered_events as (%s)
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
		avg(nullif(latency_ms, 0)),
		coalesce(sum(case when total_tokens = 0 and failed = 0 then 1 else 0 end), 0)
	from filtered_events`, source)
	var aggregate Aggregate
	if err := tx.QueryRowContext(ctx, query, args...).Scan(
		&aggregate.TotalCalls,
		&aggregate.SuccessCalls,
		&aggregate.FailureCalls,
		&aggregate.InputTokens,
		&aggregate.OutputTokens,
		&aggregate.ReasoningTokens,
		&aggregate.CachedTokens,
		&aggregate.CacheReadTokens,
		&aggregate.CacheCreationTokens,
		&aggregate.TotalTokens,
		&aggregate.AvgLatencyMS,
		&aggregate.ZeroTokenCalls,
	); err != nil {
		return Aggregate{}, state, false, err
	}
	if err := tx.Commit(); err != nil {
		return Aggregate{}, state, false, err
	}
	return aggregate, state, true, nil
}

func (r *repository) LoadModelStats(ctx context.Context, filter AnalyticsFilter) ([]ModelStat, State, bool, error) {
	if !SupportsEventProjectionFilter(filter) {
		return nil, State{}, false, nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, State{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	state, available, projectionComplete, err := projectionReadState(ctx, tx)
	if err != nil || !available {
		return nil, state, available, err
	}
	statsState, revision, dailyAvailable, err := statsReadState(ctx, tx)
	if err != nil {
		return nil, state, false, err
	}
	if dailyAvailable && SupportsStatsFilter(filter) {
		stats, err := loadDailyModelStats(
			ctx,
			tx,
			statsState,
			state.CoverageEventID,
			projectionComplete,
			revision,
			filter,
		)
		if err != nil {
			return nil, state, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, state, false, err
		}
		return stats, state, true, nil
	}

	source, args := filteredEventSourceSQL(
		filter,
		state.CoverageEventID,
		`p.requested_model as model, p.analytics_model, p.resolved_model, p.service_tier, coalesce(nullif(p.auth_provider_snapshot, ''), p.provider, '') as provider, p.failed,
		p.normalized_total_input_tokens, p.output_tokens, p.reasoning_tokens,
		p.cached_tokens, p.cache_tokens, p.cache_read_tokens,
		p.cache_creation_tokens, p.total_tokens`,
		usageidentity.SQLEffectiveRequestedModelExpression("e.model", "e.requested_model")+`, `+usageidentity.SQLRequestAnalyticsModelExpression("e.model", "e.requested_model")+`, coalesce(e.resolved_model, ''),
		coalesce(e.service_tier, ''), coalesce(nullif(e.auth_provider_snapshot, ''), e.provider, ''), coalesce(e.failed, 0),
		coalesce(e.normalized_total_input_tokens, e.input_tokens, 0),
		coalesce(e.output_tokens, 0), coalesce(e.reasoning_tokens, 0),
		coalesce(e.cached_tokens, 0), coalesce(e.cache_tokens, 0),
		coalesce(e.cache_read_tokens, 0), coalesce(e.cache_creation_tokens, 0),
		coalesce(e.total_tokens, 0)`,
		eventSourceOptions{ProjectionComplete: projectionComplete},
	)
	query := fmt.Sprintf(`with base_events as (%s), priced_events as (
		select
			base_events.*,
			coalesce(nullif(base_events.resolved_model, ''), base_events.analytics_model) as billing_model_value,
			case
				when billing_price.model is not null then coalesce(nullif(base_events.resolved_model, ''), base_events.analytics_model)
				when analytics_price.model is not null then base_events.analytics_model
				when display_price.model is not null then base_events.model
				else coalesce(nullif(base_events.resolved_model, ''), base_events.analytics_model)
			end as pricing_model_value
		from base_events
		left join model_prices billing_price on billing_price.model = coalesce(nullif(base_events.resolved_model, ''), base_events.analytics_model)
		left join model_prices analytics_price on analytics_price.model = base_events.analytics_model
		left join model_prices display_price on display_price.model = base_events.model
	), banded_events as (
		select
			priced_events.*,
			coalesce((
				select max(tier.threshold_tokens)
				from model_price_context_tiers tier
				where tier.model = priced_events.pricing_model_value
					and priced_events.normalized_total_input_tokens > tier.threshold_tokens
			), %d) as context_threshold_tokens_value
		from priced_events
	)
	select
			analytics_model,
		billing_model_value,
		pricing_model_value,
		context_threshold_tokens_value,
		service_tier,
		provider,
		count(*),
		coalesce(sum(case when failed = 0 then 1 else 0 end), 0),
		coalesce(sum(normalized_total_input_tokens), 0),
		coalesce(sum(output_tokens), 0),
		coalesce(sum(reasoning_tokens), 0),
		coalesce(sum(max(max(cached_tokens, cache_tokens) - max(cache_read_tokens, 0) - max(cache_creation_tokens, 0), 0)), 0),
		coalesce(sum(cache_read_tokens), 0),
		coalesce(sum(cache_creation_tokens), 0),
		coalesce(sum(case when normalized_total_input_tokens > %[3]d then normalized_total_input_tokens else 0 end), 0),
		coalesce(sum(case when normalized_total_input_tokens > %[3]d then output_tokens else 0 end), 0),
		coalesce(sum(case when normalized_total_input_tokens > %[3]d then max(max(cached_tokens, cache_tokens) - max(cache_read_tokens, 0) - max(cache_creation_tokens, 0), 0) else 0 end), 0),
		coalesce(sum(case when normalized_total_input_tokens > %[3]d then cache_read_tokens else 0 end), 0),
		coalesce(sum(case when normalized_total_input_tokens > %[3]d then cache_creation_tokens else 0 end), 0),
		coalesce(sum(total_tokens), 0)
	from banded_events
		group by analytics_model, billing_model_value, pricing_model_value,
		context_threshold_tokens_value, service_tier, provider
	order by count(*) desc`, source, model.ModelPriceBaseContextThreshold, usage.LongContextInputTokenThreshold)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, state, false, err
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
			&stat.Provider,
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
			return nil, state, false, err
		}
		stats = append(stats, stat)
	}
	if err := rows.Err(); err != nil {
		return nil, state, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, state, false, err
	}
	return stats, state, true, nil
}

func projectionReadState(ctx context.Context, tx *sql.Tx) (State, bool, bool, error) {
	state, err := stateQuery(ctx, tx, ProjectionRollupName)
	if err != nil {
		return State{}, false, false, err
	}
	if state.SchemaVersion != SchemaVersion {
		return state, false, false, nil
	}
	latestID, err := latestEventID(ctx, tx)
	if err != nil {
		return State{}, false, false, err
	}
	return state, true, state.CoverageEventID >= latestID, nil
}
