package usagemonitoring

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

type accountStatKey struct {
	accountSnapshot       string
	authLabelSnapshot     string
	authProviderSnapshot  string
	authAccountIDSnapshot string
	authIndex             string
	sourceHash            string
	model                 string
	billingModel          string
	pricingModel          string
	contextThreshold      int64
	serviceTier           string
}

type accountStatAccumulator struct {
	row          AccountModelStat
	latencySumMS int64
}

type apiKeyStatKey struct {
	apiKeyHash            string
	accountSnapshot       string
	authLabelSnapshot     string
	authProviderSnapshot  string
	authAccountIDSnapshot string
	authIndex             string
	sourceHash            string
	model                 string
	billingModel          string
	pricingModel          string
	contextThreshold      int64
	serviceTier           string
}

type apiKeyStatAccumulator struct {
	row          APIKeyModelStat
	latencySumMS int64
}

func monitoringBandedProjectedEventsCTE(source string) string {
	return fmt.Sprintf(`with base_events as (%s), priced_events as (
		select
			base_events.*,
			coalesce(nullif(base_events.resolved_model, ''), base_events.analytics_model) as billing_model_value,
			case
				when billing_price.model is not null then coalesce(nullif(base_events.resolved_model, ''), base_events.analytics_model)
				when analytics_price.model is not null then base_events.analytics_model
				when display_price.model is not null then base_events.model
				else coalesce(nullif(base_events.resolved_model, ''), base_events.analytics_model)
			end as pricing_model_value,
			max(
				max(coalesce(cached_tokens, 0), coalesce(cache_tokens, 0)) -
				max(coalesce(cache_read_tokens, 0), 0) -
				max(coalesce(cache_creation_tokens, 0), 0),
				0
			) as compatible_cached_tokens_value,
			normalized_total_input_tokens as normalized_input_tokens_value
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
	)`, source, model.ModelPriceBaseContextThreshold)
}

func (r *repository) LoadAccountStats(ctx context.Context, filter AnalyticsFilter) ([]AccountModelStat, State, bool, error) {
	if !SupportsEventProjectionFilter(filter) {
		return nil, State{}, false, nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, State{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	projectionState, projectionAvailable, projectionComplete, err := projectionReadState(ctx, tx)
	if err != nil || !projectionAvailable {
		return nil, projectionState, projectionAvailable, err
	}
	state, revision, dailyAvailable, err := statsReadState(ctx, tx)
	if err != nil {
		return nil, projectionState, false, err
	}

	grouped := map[accountStatKey]*accountStatAccumulator{}
	if dailyAvailable && SupportsStatsFilter(filter) {
		err = loadAccountRange(ctx, tx, state, projectionState.CoverageEventID, projectionComplete, revision, filter, grouped)
	} else {
		err = mergeProjectedAccountStats(ctx, tx, projectionState.CoverageEventID, projectionComplete, filter, eventSourceOptions{}, grouped)
	}
	if err != nil {
		return nil, projectionState, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, projectionState, false, err
	}
	return sortedAccountStats(grouped), projectionState, true, nil
}

func (r *repository) LoadAPIKeyStats(ctx context.Context, filter AnalyticsFilter) ([]APIKeyModelStat, State, bool, error) {
	if !SupportsEventProjectionFilter(filter) {
		return nil, State{}, false, nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, State{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	projectionState, projectionAvailable, projectionComplete, err := projectionReadState(ctx, tx)
	if err != nil || !projectionAvailable {
		return nil, projectionState, projectionAvailable, err
	}
	state, revision, dailyAvailable, err := statsReadState(ctx, tx)
	if err != nil {
		return nil, projectionState, false, err
	}

	grouped := map[apiKeyStatKey]*apiKeyStatAccumulator{}
	if dailyAvailable && SupportsStatsFilter(filter) {
		err = loadAPIKeyRange(ctx, tx, state, projectionState.CoverageEventID, projectionComplete, revision, filter, grouped)
	} else {
		err = mergeProjectedAPIKeyStats(ctx, tx, projectionState.CoverageEventID, projectionComplete, filter, 0, false, grouped)
	}
	if err != nil {
		return nil, projectionState, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, projectionState, false, err
	}
	return sortedAPIKeyStats(grouped), projectionState, true, nil
}

func statsReadState(ctx context.Context, tx *sql.Tx) (State, string, bool, error) {
	state, err := stateQuery(ctx, tx, StatsRollupName)
	if err != nil {
		return State{}, "", false, err
	}
	if state.SchemaVersion != SchemaVersion {
		return state, "", false, nil
	}
	revision, err := currentStructureRevision(ctx, tx)
	if err != nil {
		return State{}, "", false, err
	}
	if state.StructureRevision != revision || state.Status == "clearing" {
		return state, revision, false, nil
	}
	return state, revision, true, nil
}

func loadAccountRange(
	ctx context.Context,
	tx *sql.Tx,
	state State,
	projectionCoverageEventID int64,
	projectionComplete bool,
	revision string,
	filter AnalyticsFilter,
	grouped map[accountStatKey]*accountStatAccumulator,
) error {
	fullStartMS := ceilDayMS(filter.FromMS)
	fullEndMS := floorDayMS(filter.ToMS)
	if fullStartMS >= fullEndMS {
		return mergeProjectedAccountStats(ctx, tx, projectionCoverageEventID, projectionComplete, filter, eventSourceOptions{}, grouped)
	}
	if err := mergeStoredAccountStats(ctx, tx, revision, filter, fullStartMS, fullEndMS, grouped); err != nil {
		return err
	}
	tailFilter := filter
	tailFilter.FromMS = fullStartMS
	tailFilter.ToMS = fullEndMS
	if err := mergeProjectedAccountStats(ctx, tx, projectionCoverageEventID, projectionComplete, tailFilter, eventSourceOptions{AfterID: state.CoverageEventID, UseAfter: true}, grouped); err != nil {
		return err
	}
	if err := mergeProjectedAccountStats(
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
		return err
	}
	if filter.FromMS < fullStartMS {
		edgeFilter := filter
		edgeFilter.ToMS = fullStartMS
		if err := mergeProjectedAccountStats(ctx, tx, projectionCoverageEventID, projectionComplete, edgeFilter, eventSourceOptions{}, grouped); err != nil {
			return err
		}
	}
	if fullEndMS < filter.ToMS {
		edgeFilter := filter
		edgeFilter.FromMS = fullEndMS
		if err := mergeProjectedAccountStats(ctx, tx, projectionCoverageEventID, projectionComplete, edgeFilter, eventSourceOptions{}, grouped); err != nil {
			return err
		}
	}
	return nil
}

func loadAPIKeyRange(
	ctx context.Context,
	tx *sql.Tx,
	state State,
	projectionCoverageEventID int64,
	projectionComplete bool,
	revision string,
	filter AnalyticsFilter,
	grouped map[apiKeyStatKey]*apiKeyStatAccumulator,
) error {
	fullStartMS := ceilDayMS(filter.FromMS)
	fullEndMS := floorDayMS(filter.ToMS)
	if fullStartMS >= fullEndMS {
		return mergeProjectedAPIKeyStats(ctx, tx, projectionCoverageEventID, projectionComplete, filter, 0, false, grouped)
	}
	if err := mergeStoredAPIKeyStats(ctx, tx, revision, filter, fullStartMS, fullEndMS, grouped); err != nil {
		return err
	}
	tailFilter := filter
	tailFilter.FromMS = fullStartMS
	tailFilter.ToMS = fullEndMS
	if err := mergeProjectedAPIKeyStats(ctx, tx, projectionCoverageEventID, projectionComplete, tailFilter, state.CoverageEventID, true, grouped); err != nil {
		return err
	}
	if filter.FromMS < fullStartMS {
		edgeFilter := filter
		edgeFilter.ToMS = fullStartMS
		if err := mergeProjectedAPIKeyStats(ctx, tx, projectionCoverageEventID, projectionComplete, edgeFilter, 0, false, grouped); err != nil {
			return err
		}
	}
	if fullEndMS < filter.ToMS {
		edgeFilter := filter
		edgeFilter.FromMS = fullEndMS
		if err := mergeProjectedAPIKeyStats(ctx, tx, projectionCoverageEventID, projectionComplete, edgeFilter, 0, false, grouped); err != nil {
			return err
		}
	}
	return nil
}

func mergeStoredAccountStats(
	ctx context.Context,
	tx *sql.Tx,
	revision string,
	filter AnalyticsFilter,
	fromMS, toMS int64,
	grouped map[accountStatKey]*accountStatAccumulator,
) error {
	conditions, args := storedStatsConditions(filter, revision, fromMS, toMS)
	rows, err := tx.QueryContext(ctx, `select
		account_snapshot,
		auth_label_snapshot,
		coalesce(nullif(auth_provider_snapshot, ''), provider, ''),
		coalesce(max(provider), ''),
		coalesce(max(auth_provider_snapshot), ''),
		coalesce(max(auth_account_id_snapshot), ''),
		auth_index,
		max(source),
		source_hash,
		model,
		billing_model,
		pricing_model,
		context_threshold_tokens,
		service_tier,
		sum(calls),
		sum(case when failed = 0 then calls else 0 end),
		sum(case when failed = 1 then calls else 0 end),
		sum(input_tokens),
		sum(output_tokens),
		sum(cached_tokens),
		sum(cache_read_tokens),
		sum(cache_creation_tokens),
		sum(long_input_tokens),
		sum(long_output_tokens),
		sum(long_cached_tokens),
		sum(long_cache_read_tokens),
		sum(long_cache_creation_tokens),
		sum(total_tokens),
		max(last_seen_ms),
		sum(latency_sum_ms),
		sum(latency_samples)
	from usage_monitoring_account_daily_rollups_v1
	where `+strings.Join(conditions, " and ")+`
	group by account_snapshot, auth_label_snapshot,
		coalesce(nullif(auth_provider_snapshot, ''), provider, ''), auth_account_id_snapshot, auth_index,
		source_hash, model, billing_model, pricing_model,
		context_threshold_tokens, service_tier`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanAccountStats(rows, grouped)
}

func mergeProjectedAccountStats(
	ctx context.Context,
	tx *sql.Tx,
	projectionCoverageEventID int64,
	projectionComplete bool,
	filter AnalyticsFilter,
	options eventSourceOptions,
	grouped map[accountStatKey]*accountStatAccumulator,
) error {
	if filter.FromMS >= filter.ToMS {
		return nil
	}
	source, args := filteredEventSourceSQL(
		filter,
		projectionCoverageEventID,
		`p.timestamp_ms, p.account_snapshot, p.auth_label_snapshot, p.provider,
		p.auth_provider_snapshot, p.auth_account_id_snapshot, p.auth_index, p.source, p.source_hash,
		p.requested_model as model, p.analytics_model, p.resolved_model, p.service_tier, p.failed,
		p.normalized_total_input_tokens, p.output_tokens, p.cached_tokens,
		p.cache_tokens, p.cache_read_tokens, p.cache_creation_tokens,
		p.total_tokens, p.latency_ms`,
		`e.timestamp_ms, coalesce(e.account_snapshot, ''), coalesce(e.auth_label_snapshot, ''),
		coalesce(e.provider, ''), coalesce(e.auth_provider_snapshot, ''), coalesce(e.auth_account_id_snapshot, ''),
		coalesce(e.auth_index, ''), coalesce(e.source, ''),
		coalesce(e.source_hash, ''), `+usageidentity.SQLEffectiveRequestedModelExpression("e.model", "e.requested_model")+`,
		`+usageidentity.SQLRequestAnalyticsModelExpression("e.model", "e.requested_model")+`,
		coalesce(e.resolved_model, ''), coalesce(e.service_tier, ''),
		coalesce(e.failed, 0),
		coalesce(e.normalized_total_input_tokens, e.input_tokens, 0),
		coalesce(e.output_tokens, 0), coalesce(e.cached_tokens, 0),
		coalesce(e.cache_tokens, 0), coalesce(e.cache_read_tokens, 0),
		coalesce(e.cache_creation_tokens, 0), coalesce(e.total_tokens, 0),
		e.latency_ms`,
		eventSourceOptions{
			AfterID:            options.AfterID,
			UseAfter:           options.UseAfter,
			MaxID:              options.MaxID,
			UseMax:             options.UseMax,
			CodexMarkerOnly:    options.CodexMarkerOnly,
			ProjectionComplete: projectionComplete,
		},
	)
	query := monitoringBandedProjectedEventsCTE(source) + `
		select
		coalesce(account_snapshot, ''),
		coalesce(auth_label_snapshot, ''),
		coalesce(nullif(auth_provider_snapshot, ''), provider, ''),
		coalesce(max(provider), ''),
		coalesce(max(auth_provider_snapshot), ''),
		coalesce(max(auth_account_id_snapshot), ''),
		coalesce(auth_index, ''),
		coalesce(max(source), ''),
		coalesce(source_hash, ''),
			analytics_model,
		billing_model_value,
		pricing_model_value,
		context_threshold_tokens_value,
		coalesce(service_tier, ''),
		count(*),
		coalesce(sum(case when failed = 0 then 1 else 0 end), 0),
		coalesce(sum(case when failed = 1 then 1 else 0 end), 0),
		coalesce(sum(normalized_input_tokens_value), 0),
		coalesce(sum(output_tokens), 0),
		coalesce(sum(compatible_cached_tokens_value), 0),
		coalesce(sum(cache_read_tokens), 0),
		coalesce(sum(cache_creation_tokens), 0),
		coalesce(sum(case when normalized_input_tokens_value > ? then normalized_input_tokens_value else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > ? then output_tokens else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > ? then compatible_cached_tokens_value else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > ? then cache_read_tokens else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > ? then cache_creation_tokens else 0 end), 0),
		coalesce(sum(total_tokens), 0),
		max(timestamp_ms),
		coalesce(sum(case when latency_ms is not null and latency_ms != 0 then latency_ms else 0 end), 0),
		count(nullif(latency_ms, 0))
	from banded_events
	group by account_snapshot, auth_label_snapshot,
		coalesce(nullif(auth_provider_snapshot, ''), provider, ''), auth_account_id_snapshot, auth_index,
			source_hash, analytics_model, billing_model_value, pricing_model_value,
		context_threshold_tokens_value, coalesce(service_tier, '')`
	args = appendLongContextThresholdArgs(args)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanAccountStats(rows, grouped)
}

func mergeStoredAPIKeyStats(
	ctx context.Context,
	tx *sql.Tx,
	revision string,
	filter AnalyticsFilter,
	fromMS, toMS int64,
	grouped map[apiKeyStatKey]*apiKeyStatAccumulator,
) error {
	conditions, args := storedStatsConditions(filter, revision, fromMS, toMS)
	rows, err := tx.QueryContext(ctx, `select
		api_key_hash,
		account_snapshot,
		auth_label_snapshot,
		coalesce(nullif(auth_provider_snapshot, ''), provider, ''),
		coalesce(max(auth_account_id_snapshot), ''),
		auth_index,
		max(source),
		source_hash,
		model,
		billing_model,
		pricing_model,
		context_threshold_tokens,
		service_tier,
		sum(calls),
		sum(case when failed = 0 then calls else 0 end),
		sum(case when failed = 1 then calls else 0 end),
		sum(input_tokens),
		sum(output_tokens),
		sum(cached_tokens),
		sum(cache_read_tokens),
		sum(cache_creation_tokens),
		sum(long_input_tokens),
		sum(long_output_tokens),
		sum(long_cached_tokens),
		sum(long_cache_read_tokens),
		sum(long_cache_creation_tokens),
		sum(total_tokens),
		max(last_seen_ms),
		sum(latency_sum_ms),
		sum(latency_samples)
	from usage_monitoring_api_key_daily_rollups_v1
	where `+strings.Join(conditions, " and ")+`
	group by api_key_hash, account_snapshot, auth_label_snapshot,
		coalesce(nullif(auth_provider_snapshot, ''), provider, ''), auth_account_id_snapshot, auth_index,
		source_hash, model, billing_model, pricing_model,
		context_threshold_tokens, service_tier`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanAPIKeyStats(rows, grouped)
}

func mergeProjectedAPIKeyStats(
	ctx context.Context,
	tx *sql.Tx,
	projectionCoverageEventID int64,
	projectionComplete bool,
	filter AnalyticsFilter,
	afterID int64,
	useAfterID bool,
	grouped map[apiKeyStatKey]*apiKeyStatAccumulator,
) error {
	if filter.FromMS >= filter.ToMS {
		return nil
	}
	source, args := filteredEventSourceSQL(
		filter,
		projectionCoverageEventID,
		`p.timestamp_ms, p.api_key_hash, p.account_snapshot, p.auth_label_snapshot,
		p.provider, p.auth_provider_snapshot, p.auth_account_id_snapshot, p.auth_index, p.source,
		p.source_hash, p.requested_model as model, p.analytics_model, p.resolved_model, p.service_tier, p.failed,
		p.normalized_total_input_tokens, p.output_tokens, p.cached_tokens,
		p.cache_tokens, p.cache_read_tokens, p.cache_creation_tokens,
		p.total_tokens, p.latency_ms`,
		`e.timestamp_ms, coalesce(e.api_key_hash, ''), coalesce(e.account_snapshot, ''),
		coalesce(e.auth_label_snapshot, ''), coalesce(e.provider, ''),
		coalesce(e.auth_provider_snapshot, ''), coalesce(e.auth_account_id_snapshot, ''), coalesce(e.auth_index, ''),
		coalesce(e.source, ''), coalesce(e.source_hash, ''),
		`+usageidentity.SQLEffectiveRequestedModelExpression("e.model", "e.requested_model")+`, `+usageidentity.SQLRequestAnalyticsModelExpression("e.model", "e.requested_model")+`, coalesce(e.resolved_model, ''),
		coalesce(e.service_tier, ''), coalesce(e.failed, 0),
		coalesce(e.normalized_total_input_tokens, e.input_tokens, 0),
		coalesce(e.output_tokens, 0), coalesce(e.cached_tokens, 0),
		coalesce(e.cache_tokens, 0), coalesce(e.cache_read_tokens, 0),
		coalesce(e.cache_creation_tokens, 0), coalesce(e.total_tokens, 0),
		e.latency_ms`,
		eventSourceOptions{
			AfterID:            afterID,
			UseAfter:           useAfterID,
			ProjectionComplete: projectionComplete,
		},
	)
	query := monitoringBandedProjectedEventsCTE(source) + `
	select
		coalesce(api_key_hash, ''),
		coalesce(account_snapshot, ''),
		coalesce(auth_label_snapshot, ''),
		coalesce(nullif(auth_provider_snapshot, ''), provider, ''),
		coalesce(max(auth_account_id_snapshot), ''),
		coalesce(auth_index, ''),
		coalesce(max(source), ''),
		coalesce(source_hash, ''),
			analytics_model,
		billing_model_value,
		pricing_model_value,
		context_threshold_tokens_value,
		coalesce(service_tier, ''),
		count(*),
		coalesce(sum(case when failed = 0 then 1 else 0 end), 0),
		coalesce(sum(case when failed = 1 then 1 else 0 end), 0),
		coalesce(sum(normalized_input_tokens_value), 0),
		coalesce(sum(output_tokens), 0),
		coalesce(sum(compatible_cached_tokens_value), 0),
		coalesce(sum(cache_read_tokens), 0),
		coalesce(sum(cache_creation_tokens), 0),
		coalesce(sum(case when normalized_input_tokens_value > ? then normalized_input_tokens_value else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > ? then output_tokens else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > ? then compatible_cached_tokens_value else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > ? then cache_read_tokens else 0 end), 0),
		coalesce(sum(case when normalized_input_tokens_value > ? then cache_creation_tokens else 0 end), 0),
		coalesce(sum(total_tokens), 0),
		max(timestamp_ms),
		coalesce(sum(case when latency_ms is not null and latency_ms != 0 then latency_ms else 0 end), 0),
		count(nullif(latency_ms, 0))
	from banded_events
	group by api_key_hash, account_snapshot, auth_label_snapshot,
		coalesce(nullif(auth_provider_snapshot, ''), provider, ''), auth_account_id_snapshot, auth_index,
			source_hash, analytics_model, billing_model_value, pricing_model_value,
		context_threshold_tokens_value, coalesce(service_tier, '')`
	args = appendLongContextThresholdArgs(args)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return scanAPIKeyStats(rows, grouped)
}

func appendLongContextThresholdArgs(args []any) []any {
	const threshold = usage.LongContextInputTokenThreshold
	return append(args, threshold, threshold, threshold, threshold, threshold)
}

func scanAccountStats(rows *sql.Rows, grouped map[accountStatKey]*accountStatAccumulator) error {
	for rows.Next() {
		var row AccountModelStat
		var latencySumMS int64
		if err := rows.Scan(
			&row.AccountSnapshot,
			&row.AuthLabelSnapshot,
			&row.AuthProviderSnapshot,
			&row.Provider,
			&row.ExplicitAuthProviderSnapshot,
			&row.AuthAccountIDSnapshot,
			&row.AuthIndex,
			&row.Source,
			&row.SourceHash,
			&row.Model,
			&row.BillingModel,
			&row.PricingModel,
			&row.ContextThresholdTokens,
			&row.ServiceTier,
			&row.Calls,
			&row.SuccessCalls,
			&row.FailureCalls,
			&row.InputTokens,
			&row.OutputTokens,
			&row.CachedTokens,
			&row.CacheReadTokens,
			&row.CacheCreationTokens,
			&row.LongInputTokens,
			&row.LongOutputTokens,
			&row.LongCachedTokens,
			&row.LongCacheReadTokens,
			&row.LongCacheCreationTokens,
			&row.TotalTokens,
			&row.LastSeenMS,
			&latencySumMS,
			&row.LatencySamples,
		); err != nil {
			return err
		}
		row.LatencySumMS = latencySumMS
		mergeAccountStat(grouped, row, latencySumMS)
	}
	return rows.Err()
}

func scanAPIKeyStats(rows *sql.Rows, grouped map[apiKeyStatKey]*apiKeyStatAccumulator) error {
	for rows.Next() {
		var row APIKeyModelStat
		var latencySumMS int64
		if err := rows.Scan(
			&row.APIKeyHash,
			&row.AccountSnapshot,
			&row.AuthLabelSnapshot,
			&row.AuthProviderSnapshot,
			&row.AuthAccountIDSnapshot,
			&row.AuthIndex,
			&row.Source,
			&row.SourceHash,
			&row.Model,
			&row.BillingModel,
			&row.PricingModel,
			&row.ContextThresholdTokens,
			&row.ServiceTier,
			&row.Calls,
			&row.SuccessCalls,
			&row.FailureCalls,
			&row.InputTokens,
			&row.OutputTokens,
			&row.CachedTokens,
			&row.CacheReadTokens,
			&row.CacheCreationTokens,
			&row.LongInputTokens,
			&row.LongOutputTokens,
			&row.LongCachedTokens,
			&row.LongCacheReadTokens,
			&row.LongCacheCreationTokens,
			&row.TotalTokens,
			&row.LastSeenMS,
			&latencySumMS,
			&row.LatencySamples,
		); err != nil {
			return err
		}
		mergeAPIKeyStat(grouped, row, latencySumMS)
	}
	return rows.Err()
}

func mergeAccountStat(grouped map[accountStatKey]*accountStatAccumulator, row AccountModelStat, latencySumMS int64) {
	key := accountStatKey{
		accountSnapshot:       row.AccountSnapshot,
		authLabelSnapshot:     row.AuthLabelSnapshot,
		authProviderSnapshot:  row.AuthProviderSnapshot,
		authAccountIDSnapshot: row.AuthAccountIDSnapshot,
		authIndex:             row.AuthIndex,
		sourceHash:            row.SourceHash,
		model:                 row.Model,
		billingModel:          row.BillingModel,
		pricingModel:          row.PricingModel,
		contextThreshold:      row.ContextThresholdTokens,
		serviceTier:           row.ServiceTier,
	}
	entry := grouped[key]
	if entry == nil {
		grouped[key] = &accountStatAccumulator{row: row, latencySumMS: latencySumMS}
		return
	}
	mergeAccountValues(&entry.row, row)
	entry.latencySumMS += latencySumMS
}

func mergeAPIKeyStat(grouped map[apiKeyStatKey]*apiKeyStatAccumulator, row APIKeyModelStat, latencySumMS int64) {
	key := apiKeyStatKey{
		apiKeyHash:            row.APIKeyHash,
		accountSnapshot:       row.AccountSnapshot,
		authLabelSnapshot:     row.AuthLabelSnapshot,
		authProviderSnapshot:  row.AuthProviderSnapshot,
		authAccountIDSnapshot: row.AuthAccountIDSnapshot,
		authIndex:             row.AuthIndex,
		sourceHash:            row.SourceHash,
		model:                 row.Model,
		billingModel:          row.BillingModel,
		pricingModel:          row.PricingModel,
		contextThreshold:      row.ContextThresholdTokens,
		serviceTier:           row.ServiceTier,
	}
	entry := grouped[key]
	if entry == nil {
		grouped[key] = &apiKeyStatAccumulator{row: row, latencySumMS: latencySumMS}
		return
	}
	mergeAPIKeyValues(&entry.row, row)
	entry.latencySumMS += latencySumMS
}

func mergeAccountValues(target *AccountModelStat, row AccountModelStat) {
	if row.Source > target.Source {
		target.Source = row.Source
	}
	if row.Provider > target.Provider {
		target.Provider = row.Provider
	}
	if row.ExplicitAuthProviderSnapshot > target.ExplicitAuthProviderSnapshot {
		target.ExplicitAuthProviderSnapshot = row.ExplicitAuthProviderSnapshot
	}
	if row.AuthAccountIDSnapshot > target.AuthAccountIDSnapshot {
		target.AuthAccountIDSnapshot = row.AuthAccountIDSnapshot
	}
	target.Calls += row.Calls
	target.SuccessCalls += row.SuccessCalls
	target.FailureCalls += row.FailureCalls
	target.InputTokens += row.InputTokens
	target.OutputTokens += row.OutputTokens
	target.CachedTokens += row.CachedTokens
	target.CacheReadTokens += row.CacheReadTokens
	target.CacheCreationTokens += row.CacheCreationTokens
	target.LongInputTokens += row.LongInputTokens
	target.LongOutputTokens += row.LongOutputTokens
	target.LongCachedTokens += row.LongCachedTokens
	target.LongCacheReadTokens += row.LongCacheReadTokens
	target.LongCacheCreationTokens += row.LongCacheCreationTokens
	target.TotalTokens += row.TotalTokens
	target.LatencySumMS += row.LatencySumMS
	target.LatencySamples += row.LatencySamples
	if row.LastSeenMS > target.LastSeenMS {
		target.LastSeenMS = row.LastSeenMS
	}
}

func mergeAPIKeyValues(target *APIKeyModelStat, row APIKeyModelStat) {
	if row.Source > target.Source {
		target.Source = row.Source
	}
	if row.AuthAccountIDSnapshot > target.AuthAccountIDSnapshot {
		target.AuthAccountIDSnapshot = row.AuthAccountIDSnapshot
	}
	target.Calls += row.Calls
	target.SuccessCalls += row.SuccessCalls
	target.FailureCalls += row.FailureCalls
	target.InputTokens += row.InputTokens
	target.OutputTokens += row.OutputTokens
	target.CachedTokens += row.CachedTokens
	target.CacheReadTokens += row.CacheReadTokens
	target.CacheCreationTokens += row.CacheCreationTokens
	target.LongInputTokens += row.LongInputTokens
	target.LongOutputTokens += row.LongOutputTokens
	target.LongCachedTokens += row.LongCachedTokens
	target.LongCacheReadTokens += row.LongCacheReadTokens
	target.LongCacheCreationTokens += row.LongCacheCreationTokens
	target.TotalTokens += row.TotalTokens
	target.LatencySamples += row.LatencySamples
	if row.LastSeenMS > target.LastSeenMS {
		target.LastSeenMS = row.LastSeenMS
	}
}

func sortedAccountStats(grouped map[accountStatKey]*accountStatAccumulator) []AccountModelStat {
	result := make([]AccountModelStat, 0, len(grouped))
	for _, entry := range grouped {
		if entry.row.LatencySamples > 0 {
			entry.row.AvgLatencyMS.Valid = true
			entry.row.AvgLatencyMS.Float64 = float64(entry.latencySumMS) / float64(entry.row.LatencySamples)
		}
		result = append(result, entry.row)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].LastSeenMS != result[j].LastSeenMS {
			return result[i].LastSeenMS > result[j].LastSeenMS
		}
		if result[i].Calls != result[j].Calls {
			return result[i].Calls > result[j].Calls
		}
		return accountSortKey(result[i]) < accountSortKey(result[j])
	})
	return result
}

func sortedAPIKeyStats(grouped map[apiKeyStatKey]*apiKeyStatAccumulator) []APIKeyModelStat {
	result := make([]APIKeyModelStat, 0, len(grouped))
	for _, entry := range grouped {
		if entry.row.LatencySamples > 0 {
			entry.row.AvgLatencyMS.Valid = true
			entry.row.AvgLatencyMS.Float64 = float64(entry.latencySumMS) / float64(entry.row.LatencySamples)
		}
		result = append(result, entry.row)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].LastSeenMS != result[j].LastSeenMS {
			return result[i].LastSeenMS > result[j].LastSeenMS
		}
		if result[i].Calls != result[j].Calls {
			return result[i].Calls > result[j].Calls
		}
		return apiKeySortKey(result[i]) < apiKeySortKey(result[j])
	})
	return result
}

func accountSortKey(row AccountModelStat) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s",
		row.AccountSnapshot, row.AuthLabelSnapshot, row.AuthProviderSnapshot,
		row.AuthIndex, row.SourceHash, row.Model, row.BillingModel,
		row.PricingModel, row.ContextThresholdTokens, row.ServiceTier)
}

func apiKeySortKey(row APIKeyModelStat) string {
	return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s",
		row.APIKeyHash, row.AccountSnapshot, row.AuthLabelSnapshot,
		row.AuthProviderSnapshot, row.AuthIndex, row.SourceHash, row.Model,
		row.BillingModel, row.PricingModel, row.ContextThresholdTokens, row.ServiceTier)
}
