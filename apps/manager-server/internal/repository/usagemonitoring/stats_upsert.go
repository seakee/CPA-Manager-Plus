package usagemonitoring

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageaccountingsql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

var monitoringAccountingSQL = usageaccountingsql.For("e")

func monitoringBandedEventsCTE(whereClause string) string {
	requestedModelExpression := usageidentity.SQLEffectiveRequestedModelExpression("e.model", "e.requested_model")
	analyticsModelExpression := usageidentity.SQLRequestAnalyticsModelExpression("e.model", "e.requested_model")
	pricingBucket := func(expression string) string {
		return "case when " + monitoringAccountingSQL.PricingSafe + " then " + expression + " else 0 end"
	}
	pricingUnclassified := "case when " + monitoringAccountingSQL.PricingSafe + " then " + monitoringAccountingSQL.Unclassified + " else " + monitoringAccountingSQL.Total + " end"
	return fmt.Sprintf(`with base_events as (
		select
			e.*,
			%s as requested_model_value,
			%s as analytics_model_value,
			coalesce(nullif(e.resolved_model, ''), %s) as billing_model_value,
			%s as normalized_input_tokens_value,
			%s as normalized_output_tokens_value,
			%s as non_reasoning_output_tokens_value,
			%s as reasoning_tokens_value,
			%s as unclassified_tokens_value,
			%s as incomplete_accounting_value,
			%s as compatible_cached_tokens_value,
			%s as cache_read_tokens_value,
			%s as cache_creation_tokens_value,
			%s as total_tokens_value,
			%s as pricing_input_tokens_value,
			%s as pricing_output_tokens_value,
			%s as pricing_non_reasoning_output_tokens_value,
			%s as pricing_reasoning_tokens_value,
			%s as pricing_unclassified_tokens_value,
			%s as pricing_compatible_cached_tokens_value,
			%s as pricing_cache_read_tokens_value,
			%s as pricing_cache_creation_tokens_value
		from usage_events e
		where %s
	), priced_events as (
		select
			base_events.*,
			case
				when billing_price.model is not null then billing_model_value
				when analytics_price.model is not null then base_events.analytics_model_value
				when display_price.model is not null then base_events.requested_model_value
				else billing_model_value
			end as pricing_model_value
		from base_events
		left join model_prices billing_price on billing_price.model = base_events.billing_model_value
		left join model_prices analytics_price on analytics_price.model = base_events.analytics_model_value
		left join model_prices display_price on display_price.model = base_events.requested_model_value
	), banded_events as (
		select
			priced_events.*,
			coalesce((
				select max(tier.threshold_tokens)
				from model_price_context_tiers tier
				where tier.model = priced_events.pricing_model_value
					and priced_events.pricing_input_tokens_value > tier.threshold_tokens
			), %d) as context_threshold_tokens_value
		from priced_events
	)`, requestedModelExpression, analyticsModelExpression, analyticsModelExpression,
		monitoringAccountingSQL.TotalInput,
		monitoringAccountingSQL.TotalOutput,
		monitoringAccountingSQL.NonReasoningOutput,
		monitoringAccountingSQL.ReasoningOutput,
		monitoringAccountingSQL.Unclassified,
		monitoringAccountingSQL.Incomplete,
		monitoringAccountingSQL.CompatibleCached,
		monitoringAccountingSQL.CacheRead,
		monitoringAccountingSQL.CacheCreation,
		monitoringAccountingSQL.Total,
		pricingBucket(monitoringAccountingSQL.TotalInput),
		pricingBucket(monitoringAccountingSQL.TotalOutput),
		pricingBucket(monitoringAccountingSQL.NonReasoningOutput),
		pricingBucket(monitoringAccountingSQL.ReasoningOutput),
		pricingUnclassified,
		pricingBucket(monitoringAccountingSQL.CompatibleCached),
		pricingBucket(monitoringAccountingSQL.CacheRead),
		pricingBucket(monitoringAccountingSQL.CacheCreation),
		whereClause, model.ModelPriceBaseContextThreshold)
}

func upsertAccountDailyBatch(ctx context.Context, tx *sql.Tx, revision string, afterID, throughID, nowMS int64) error {
	query := monitoringBandedEventsCTE("e.id > ? and e.id <= ?") + fmt.Sprintf(`
	insert into usage_monitoring_account_daily_rollups_v1 (
		structure_revision, bucket_ms, account_snapshot, auth_label_snapshot,
		provider, auth_provider_snapshot, auth_index, source, source_hash,
		auth_file_snapshot, api_key_hash, executor_type, model, billing_model,
			pricing_model, service_tier, context_threshold_tokens, failed, calls,
			input_tokens, output_tokens, non_reasoning_output_tokens, reasoning_tokens,
			unclassified_tokens, incomplete_accounting_calls, cached_tokens, cache_read_tokens,
			cache_creation_tokens, long_input_tokens, long_output_tokens,
			long_cached_tokens, long_cache_read_tokens, long_cache_creation_tokens,
			total_tokens, display_input_tokens, display_output_tokens,
			display_non_reasoning_output_tokens, display_reasoning_tokens,
			display_unclassified_tokens, display_cached_tokens,
			display_cache_read_tokens, display_cache_creation_tokens,
			display_total_tokens, zero_token_calls, latency_sum_ms, latency_samples,
			last_seen_ms, updated_at_ms
	)
	select
		?,
		timestamp_ms - (timestamp_ms %% %d),
		coalesce(account_snapshot, ''),
		coalesce(auth_label_snapshot, ''),
		coalesce(provider, ''),
		coalesce(auth_provider_snapshot, ''),
		coalesce(auth_index, ''),
		coalesce(source, ''),
		coalesce(source_hash, ''),
		coalesce(auth_file_snapshot, ''),
		coalesce(api_key_hash, ''),
		coalesce(executor_type, ''),
			analytics_model_value,
		billing_model_value,
		pricing_model_value,
		coalesce(service_tier, ''),
		context_threshold_tokens_value,
		failed,
		count(*),
			coalesce(cpamp_saturating_sum(pricing_input_tokens_value), 0),
			coalesce(cpamp_saturating_sum(pricing_output_tokens_value), 0),
			coalesce(cpamp_saturating_sum(pricing_non_reasoning_output_tokens_value), 0),
			coalesce(cpamp_saturating_sum(pricing_reasoning_tokens_value), 0),
			coalesce(cpamp_saturating_sum(pricing_unclassified_tokens_value), 0),
			coalesce(cpamp_saturating_sum(incomplete_accounting_value), 0),
			coalesce(cpamp_saturating_sum(pricing_compatible_cached_tokens_value), 0),
			coalesce(cpamp_saturating_sum(pricing_cache_read_tokens_value), 0),
			coalesce(cpamp_saturating_sum(pricing_cache_creation_tokens_value), 0),
			coalesce(cpamp_saturating_sum(case when pricing_input_tokens_value > %d then pricing_input_tokens_value else 0 end), 0),
			coalesce(cpamp_saturating_sum(case when pricing_input_tokens_value > %d then pricing_output_tokens_value else 0 end), 0),
			coalesce(cpamp_saturating_sum(case when pricing_input_tokens_value > %d then pricing_compatible_cached_tokens_value else 0 end), 0),
			coalesce(cpamp_saturating_sum(case when pricing_input_tokens_value > %d then pricing_cache_read_tokens_value else 0 end), 0),
			coalesce(cpamp_saturating_sum(case when pricing_input_tokens_value > %d then pricing_cache_creation_tokens_value else 0 end), 0),
			coalesce(cpamp_saturating_sum(total_tokens_value), 0),
			coalesce(cpamp_saturating_sum(normalized_input_tokens_value), 0),
			coalesce(cpamp_saturating_sum(normalized_output_tokens_value), 0),
			coalesce(cpamp_saturating_sum(non_reasoning_output_tokens_value), 0),
			coalesce(cpamp_saturating_sum(reasoning_tokens_value), 0),
			coalesce(cpamp_saturating_sum(unclassified_tokens_value), 0),
			coalesce(cpamp_saturating_sum(compatible_cached_tokens_value), 0),
			coalesce(cpamp_saturating_sum(cache_read_tokens_value), 0),
			coalesce(cpamp_saturating_sum(cache_creation_tokens_value), 0),
			coalesce(cpamp_saturating_sum(total_tokens_value), 0),
			coalesce(cpamp_saturating_sum(case when total_tokens_value = 0 and failed = 0 then 1 else 0 end), 0),
			coalesce(cpamp_saturating_sum(case when latency_ms is not null and latency_ms != 0 then latency_ms else 0 end), 0),
		count(nullif(latency_ms, 0)),
		max(timestamp_ms),
		?
	from banded_events
	group by 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18
	on conflict(
		structure_revision, bucket_ms, account_snapshot, auth_label_snapshot,
		provider, auth_provider_snapshot, auth_index, source, source_hash,
		auth_file_snapshot, api_key_hash, executor_type, model, billing_model,
		pricing_model, service_tier, context_threshold_tokens, failed
	) do update set
		calls = usage_monitoring_account_daily_rollups_v1.calls + excluded.calls,
			input_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.input_tokens, excluded.input_tokens),
			output_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.output_tokens, excluded.output_tokens),
			non_reasoning_output_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.non_reasoning_output_tokens, excluded.non_reasoning_output_tokens),
			reasoning_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.reasoning_tokens, excluded.reasoning_tokens),
			unclassified_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.unclassified_tokens, excluded.unclassified_tokens),
			incomplete_accounting_calls = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.incomplete_accounting_calls, excluded.incomplete_accounting_calls),
			cached_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.cached_tokens, excluded.cached_tokens),
		cache_read_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.cache_read_tokens, excluded.cache_read_tokens),
		cache_creation_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.cache_creation_tokens, excluded.cache_creation_tokens),
		long_input_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.long_input_tokens, excluded.long_input_tokens),
		long_output_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.long_output_tokens, excluded.long_output_tokens),
		long_cached_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.long_cached_tokens, excluded.long_cached_tokens),
		long_cache_read_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.long_cache_read_tokens, excluded.long_cache_read_tokens),
			long_cache_creation_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.long_cache_creation_tokens, excluded.long_cache_creation_tokens),
			total_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.total_tokens, excluded.total_tokens),
			display_input_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.display_input_tokens, excluded.display_input_tokens),
			display_output_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.display_output_tokens, excluded.display_output_tokens),
			display_non_reasoning_output_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.display_non_reasoning_output_tokens, excluded.display_non_reasoning_output_tokens),
			display_reasoning_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.display_reasoning_tokens, excluded.display_reasoning_tokens),
			display_unclassified_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.display_unclassified_tokens, excluded.display_unclassified_tokens),
			display_cached_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.display_cached_tokens, excluded.display_cached_tokens),
			display_cache_read_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.display_cache_read_tokens, excluded.display_cache_read_tokens),
			display_cache_creation_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.display_cache_creation_tokens, excluded.display_cache_creation_tokens),
			display_total_tokens = cpamp_saturating_add(usage_monitoring_account_daily_rollups_v1.display_total_tokens, excluded.display_total_tokens),
			zero_token_calls = usage_monitoring_account_daily_rollups_v1.zero_token_calls + excluded.zero_token_calls,
			latency_sum_ms = usage_monitoring_account_daily_rollups_v1.latency_sum_ms + excluded.latency_sum_ms,
		latency_samples = usage_monitoring_account_daily_rollups_v1.latency_samples + excluded.latency_samples,
		last_seen_ms = max(usage_monitoring_account_daily_rollups_v1.last_seen_ms, excluded.last_seen_ms),
		updated_at_ms = excluded.updated_at_ms`,
		dayMS,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
	)
	_, err := tx.ExecContext(ctx, query, afterID, throughID, revision, nowMS)
	return err
}

func upsertAPIKeyDailyBatch(ctx context.Context, tx *sql.Tx, revision string, afterID, throughID, nowMS int64) error {
	query := monitoringBandedEventsCTE("e.id > ? and e.id <= ?") + fmt.Sprintf(`
	insert into usage_monitoring_api_key_daily_rollups_v1 (
		structure_revision, bucket_ms, api_key_hash, account_snapshot,
		auth_label_snapshot, provider, auth_provider_snapshot, auth_index,
		source, source_hash, auth_file_snapshot, executor_type, model,
		billing_model, pricing_model, service_tier, context_threshold_tokens,
			failed, calls, input_tokens, output_tokens, non_reasoning_output_tokens,
			cached_tokens, reasoning_tokens, unclassified_tokens, incomplete_accounting_calls,
			cache_read_tokens, cache_creation_tokens, long_input_tokens,
			long_output_tokens, long_cached_tokens, long_cache_read_tokens,
			long_cache_creation_tokens, total_tokens, zero_token_calls, latency_sum_ms,
			latency_samples, last_seen_ms, updated_at_ms
	)
	select
		?,
		timestamp_ms - (timestamp_ms %% %d),
		coalesce(api_key_hash, ''),
		coalesce(account_snapshot, ''),
		coalesce(auth_label_snapshot, ''),
		coalesce(provider, ''),
		coalesce(auth_provider_snapshot, ''),
		coalesce(auth_index, ''),
		coalesce(source, ''),
		coalesce(source_hash, ''),
		coalesce(auth_file_snapshot, ''),
		coalesce(executor_type, ''),
			analytics_model_value,
		billing_model_value,
		pricing_model_value,
		coalesce(service_tier, ''),
		context_threshold_tokens_value,
		failed,
		count(*),
			coalesce(cpamp_saturating_sum(pricing_input_tokens_value), 0),
			coalesce(cpamp_saturating_sum(pricing_output_tokens_value), 0),
			coalesce(cpamp_saturating_sum(pricing_non_reasoning_output_tokens_value), 0),
			coalesce(cpamp_saturating_sum(pricing_compatible_cached_tokens_value), 0),
			coalesce(cpamp_saturating_sum(pricing_reasoning_tokens_value), 0),
			coalesce(cpamp_saturating_sum(pricing_unclassified_tokens_value), 0),
			coalesce(cpamp_saturating_sum(incomplete_accounting_value), 0),
			coalesce(cpamp_saturating_sum(pricing_cache_read_tokens_value), 0),
			coalesce(cpamp_saturating_sum(pricing_cache_creation_tokens_value), 0),
			coalesce(cpamp_saturating_sum(case when pricing_input_tokens_value > %d then pricing_input_tokens_value else 0 end), 0),
			coalesce(cpamp_saturating_sum(case when pricing_input_tokens_value > %d then pricing_output_tokens_value else 0 end), 0),
			coalesce(cpamp_saturating_sum(case when pricing_input_tokens_value > %d then pricing_compatible_cached_tokens_value else 0 end), 0),
			coalesce(cpamp_saturating_sum(case when pricing_input_tokens_value > %d then pricing_cache_read_tokens_value else 0 end), 0),
			coalesce(cpamp_saturating_sum(case when pricing_input_tokens_value > %d then pricing_cache_creation_tokens_value else 0 end), 0),
			coalesce(cpamp_saturating_sum(total_tokens_value), 0),
			coalesce(cpamp_saturating_sum(case when total_tokens_value = 0 and failed = 0 then 1 else 0 end), 0),
			coalesce(cpamp_saturating_sum(case when latency_ms is not null and latency_ms != 0 then latency_ms else 0 end), 0),
		count(nullif(latency_ms, 0)),
		max(timestamp_ms),
		?
	from banded_events
	group by 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18
	on conflict(
		structure_revision, bucket_ms, api_key_hash, account_snapshot,
		auth_label_snapshot, provider, auth_provider_snapshot, auth_index,
		source, source_hash, auth_file_snapshot, executor_type, model,
		billing_model, pricing_model, service_tier, context_threshold_tokens, failed
	) do update set
		calls = usage_monitoring_api_key_daily_rollups_v1.calls + excluded.calls,
			input_tokens = cpamp_saturating_add(usage_monitoring_api_key_daily_rollups_v1.input_tokens, excluded.input_tokens),
			output_tokens = cpamp_saturating_add(usage_monitoring_api_key_daily_rollups_v1.output_tokens, excluded.output_tokens),
			non_reasoning_output_tokens = cpamp_saturating_add(usage_monitoring_api_key_daily_rollups_v1.non_reasoning_output_tokens, excluded.non_reasoning_output_tokens),
			reasoning_tokens = cpamp_saturating_add(usage_monitoring_api_key_daily_rollups_v1.reasoning_tokens, excluded.reasoning_tokens),
			unclassified_tokens = cpamp_saturating_add(usage_monitoring_api_key_daily_rollups_v1.unclassified_tokens, excluded.unclassified_tokens),
			incomplete_accounting_calls = cpamp_saturating_add(usage_monitoring_api_key_daily_rollups_v1.incomplete_accounting_calls, excluded.incomplete_accounting_calls),
			cached_tokens = cpamp_saturating_add(usage_monitoring_api_key_daily_rollups_v1.cached_tokens, excluded.cached_tokens),
		cache_read_tokens = cpamp_saturating_add(usage_monitoring_api_key_daily_rollups_v1.cache_read_tokens, excluded.cache_read_tokens),
		cache_creation_tokens = cpamp_saturating_add(usage_monitoring_api_key_daily_rollups_v1.cache_creation_tokens, excluded.cache_creation_tokens),
		long_input_tokens = cpamp_saturating_add(usage_monitoring_api_key_daily_rollups_v1.long_input_tokens, excluded.long_input_tokens),
		long_output_tokens = cpamp_saturating_add(usage_monitoring_api_key_daily_rollups_v1.long_output_tokens, excluded.long_output_tokens),
		long_cached_tokens = cpamp_saturating_add(usage_monitoring_api_key_daily_rollups_v1.long_cached_tokens, excluded.long_cached_tokens),
		long_cache_read_tokens = cpamp_saturating_add(usage_monitoring_api_key_daily_rollups_v1.long_cache_read_tokens, excluded.long_cache_read_tokens),
		long_cache_creation_tokens = cpamp_saturating_add(usage_monitoring_api_key_daily_rollups_v1.long_cache_creation_tokens, excluded.long_cache_creation_tokens),
			total_tokens = cpamp_saturating_add(usage_monitoring_api_key_daily_rollups_v1.total_tokens, excluded.total_tokens),
			zero_token_calls = usage_monitoring_api_key_daily_rollups_v1.zero_token_calls + excluded.zero_token_calls,
			latency_sum_ms = usage_monitoring_api_key_daily_rollups_v1.latency_sum_ms + excluded.latency_sum_ms,
		latency_samples = usage_monitoring_api_key_daily_rollups_v1.latency_samples + excluded.latency_samples,
		last_seen_ms = max(usage_monitoring_api_key_daily_rollups_v1.last_seen_ms, excluded.last_seen_ms),
		updated_at_ms = excluded.updated_at_ms`,
		dayMS,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
	)
	_, err := tx.ExecContext(ctx, query, afterID, throughID, revision, nowMS)
	return err
}
