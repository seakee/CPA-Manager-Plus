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

func (r *repository) LoadAccountWindowStats(
	ctx context.Context,
	windows []AccountWindowUsageQuery,
) ([]AccountWindowModelStat, State, bool, error) {
	if len(windows) == 0 {
		return []AccountWindowModelStat{}, State{}, true, nil
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

	grouped := make(map[accountWindowStatKey]*AccountWindowModelStat)
	if dailyAvailable {
		if err := mergeStoredAccountWindowStats(ctx, tx, revision, windows, grouped); err != nil {
			return nil, state, false, err
		}
	}
	if err := mergeProjectedAccountWindowStats(
		ctx,
		tx,
		windows,
		state.CoverageEventID,
		projectionComplete,
		statsState.CoverageEventID,
		dailyAvailable,
		grouped,
	); err != nil {
		return nil, state, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, state, false, err
	}
	return sortedAccountWindowStats(grouped), state, true, nil
}

type accountWindowStatKey struct {
	requestIndex     int
	model            string
	billingModel     string
	pricingModel     string
	contextThreshold int64
	serviceTier      string
}

func mergeProjectedAccountWindowStats(
	ctx context.Context,
	tx *sql.Tx,
	windows []AccountWindowUsageQuery,
	projectionCoverageEventID int64,
	projectionComplete bool,
	statsCoverageEventID int64,
	dailyAvailable bool,
	grouped map[accountWindowStatKey]*AccountWindowModelStat,
) error {
	source, args := accountWindowEventSourceSQL(
		windows,
		projectionCoverageEventID,
		projectionComplete,
		statsCoverageEventID,
		dailyAvailable,
	)
	query := fmt.Sprintf(`%s
		select
			request_index,
			analytics_model,
		billing_model_value,
		pricing_model_value,
		context_threshold_tokens_value,
		coalesce(service_tier, ''),
		count(*),
		coalesce(sum(case when failed = 0 then 1 else 0 end), 0),
		coalesce(sum(case when failed = 1 then 1 else 0 end), 0),
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
		coalesce(cpamp_saturating_sum(total_tokens_value), 0),
		max(timestamp_ms)
	from banded_events
		group by request_index, analytics_model, billing_model_value, pricing_model_value,
		context_threshold_tokens_value, coalesce(service_tier, '')`, monitoringBandedProjectedEventsCTE(source))
	args = appendLongContextThresholdArgs(args)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return mergeAccountWindowStatRows(rows, grouped)
}

func mergeStoredAccountWindowStats(
	ctx context.Context,
	tx *sql.Tx,
	revision string,
	windows []AccountWindowUsageQuery,
	grouped map[accountWindowStatKey]*AccountWindowModelStat,
) error {
	values := make([]string, 0, len(windows))
	args := make([]any, 0, len(windows)*5+1)
	for _, window := range windows {
		if !accountWindowCanUseDaily(window) {
			continue
		}
		fullStartMS := ceilDayMS(window.FromMS)
		fullEndMS := floorDayMS(window.ToMS)
		if fullStartMS >= fullEndMS {
			continue
		}
		values = append(values, "(?, ?, ?, ?, ?)")
		args = append(args,
			window.RequestIndex,
			fullStartMS,
			fullEndMS,
			strings.TrimSpace(window.AuthFileSnapshot),
			strings.TrimSpace(window.AuthIndex),
		)
	}
	if len(values) == 0 {
		return nil
	}
	args = append(args, revision)
	rows, err := tx.QueryContext(ctx, `with window_targets(
		request_index, full_start_ms, full_end_ms, auth_file_snapshot, auth_index
	) as (values `+strings.Join(values, ",")+`)
	select
		w.request_index,
		d.model,
		d.billing_model,
		d.pricing_model,
		d.context_threshold_tokens,
		coalesce(d.service_tier, ''),
		coalesce(sum(d.calls), 0),
		coalesce(sum(case when d.failed = 0 then d.calls else 0 end), 0),
		coalesce(sum(case when d.failed = 1 then d.calls else 0 end), 0),
		coalesce(cpamp_saturating_sum(d.input_tokens), 0),
		coalesce(cpamp_saturating_sum(d.output_tokens), 0),
		coalesce(cpamp_saturating_sum(d.non_reasoning_output_tokens), 0),
		coalesce(cpamp_saturating_sum(d.reasoning_tokens), 0),
		coalesce(cpamp_saturating_sum(d.unclassified_tokens), 0),
		coalesce(cpamp_saturating_sum(d.incomplete_accounting_calls), 0),
		coalesce(cpamp_saturating_sum(d.cached_tokens), 0),
		coalesce(cpamp_saturating_sum(d.cache_read_tokens), 0),
		coalesce(cpamp_saturating_sum(d.cache_creation_tokens), 0),
		coalesce(cpamp_saturating_sum(d.long_input_tokens), 0),
		coalesce(cpamp_saturating_sum(d.long_output_tokens), 0),
		coalesce(cpamp_saturating_sum(d.long_cached_tokens), 0),
		coalesce(cpamp_saturating_sum(d.long_cache_read_tokens), 0),
		coalesce(cpamp_saturating_sum(d.long_cache_creation_tokens), 0),
		coalesce(cpamp_saturating_sum(d.total_tokens), 0),
		max(d.last_seen_ms)
	from window_targets w
	join usage_monitoring_account_daily_rollups_v1 d
		on d.structure_revision = ?
		and d.bucket_ms >= w.full_start_ms
		and d.bucket_ms < w.full_end_ms
		and trim(d.auth_index) = w.auth_index
		and (
			trim(d.auth_file_snapshot) = w.auth_file_snapshot
			or (
				trim(d.auth_file_snapshot) = ''
				and trim(d.source) = w.auth_file_snapshot
				and trim(d.source) <> trim(d.account_snapshot)
				and trim(d.source) <> trim(d.auth_label_snapshot)
			)
		)
	group by w.request_index, d.model, d.billing_model, d.pricing_model,
		d.context_threshold_tokens, coalesce(d.service_tier, '')`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	return mergeAccountWindowStatRows(rows, grouped)
}

func mergeAccountWindowStatRows(rows *sql.Rows, grouped map[accountWindowStatKey]*AccountWindowModelStat) error {
	for rows.Next() {
		var stat AccountWindowModelStat
		if err := rows.Scan(
			&stat.RequestIndex,
			&stat.Model,
			&stat.BillingModel,
			&stat.PricingModel,
			&stat.ContextThresholdTokens,
			&stat.ServiceTier,
			&stat.Calls,
			&stat.SuccessCalls,
			&stat.FailureCalls,
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
			&stat.LastSeenMS,
		); err != nil {
			return err
		}
		key := accountWindowStatKey{
			requestIndex:     stat.RequestIndex,
			model:            stat.Model,
			billingModel:     stat.BillingModel,
			pricingModel:     stat.PricingModel,
			contextThreshold: stat.ContextThresholdTokens,
			serviceTier:      stat.ServiceTier,
		}
		current := grouped[key]
		if current == nil {
			copy := stat
			grouped[key] = &copy
			continue
		}
		current.Calls += stat.Calls
		current.SuccessCalls += stat.SuccessCalls
		current.FailureCalls += stat.FailureCalls
		current.InputTokens = usage.SaturatingTokenSum(current.InputTokens, stat.InputTokens)
		current.OutputTokens = usage.SaturatingTokenSum(current.OutputTokens, stat.OutputTokens)
		current.NonReasoningOutputTokens = usage.SaturatingTokenSum(current.NonReasoningOutputTokens, stat.NonReasoningOutputTokens)
		current.ReasoningTokens = usage.SaturatingTokenSum(current.ReasoningTokens, stat.ReasoningTokens)
		current.UnclassifiedTokens = usage.SaturatingTokenSum(current.UnclassifiedTokens, stat.UnclassifiedTokens)
		current.IncompleteAccountingCalls = usage.SaturatingTokenSum(current.IncompleteAccountingCalls, stat.IncompleteAccountingCalls)
		current.CachedTokens = usage.SaturatingTokenSum(current.CachedTokens, stat.CachedTokens)
		current.CacheReadTokens = usage.SaturatingTokenSum(current.CacheReadTokens, stat.CacheReadTokens)
		current.CacheCreationTokens = usage.SaturatingTokenSum(current.CacheCreationTokens, stat.CacheCreationTokens)
		current.LongInputTokens = usage.SaturatingTokenSum(current.LongInputTokens, stat.LongInputTokens)
		current.LongOutputTokens = usage.SaturatingTokenSum(current.LongOutputTokens, stat.LongOutputTokens)
		current.LongCachedTokens = usage.SaturatingTokenSum(current.LongCachedTokens, stat.LongCachedTokens)
		current.LongCacheReadTokens = usage.SaturatingTokenSum(current.LongCacheReadTokens, stat.LongCacheReadTokens)
		current.LongCacheCreationTokens = usage.SaturatingTokenSum(current.LongCacheCreationTokens, stat.LongCacheCreationTokens)
		current.TotalTokens = usage.SaturatingTokenSum(current.TotalTokens, stat.TotalTokens)
		current.LastSeenMS = max(current.LastSeenMS, stat.LastSeenMS)
	}
	return rows.Err()
}

func sortedAccountWindowStats(grouped map[accountWindowStatKey]*AccountWindowModelStat) []AccountWindowModelStat {
	stats := make([]AccountWindowModelStat, 0, len(grouped))
	for _, stat := range grouped {
		stats = append(stats, *stat)
	}
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].RequestIndex != stats[j].RequestIndex {
			return stats[i].RequestIndex < stats[j].RequestIndex
		}
		if stats[i].LastSeenMS != stats[j].LastSeenMS {
			return stats[i].LastSeenMS > stats[j].LastSeenMS
		}
		if stats[i].Model != stats[j].Model {
			return stats[i].Model < stats[j].Model
		}
		if stats[i].BillingModel != stats[j].BillingModel {
			return stats[i].BillingModel < stats[j].BillingModel
		}
		if stats[i].PricingModel != stats[j].PricingModel {
			return stats[i].PricingModel < stats[j].PricingModel
		}
		if stats[i].ContextThresholdTokens != stats[j].ContextThresholdTokens {
			return stats[i].ContextThresholdTokens < stats[j].ContextThresholdTokens
		}
		return stats[i].ServiceTier < stats[j].ServiceTier
	})
	return stats
}

func accountWindowEventSourceSQL(
	windows []AccountWindowUsageQuery,
	coverageEventID int64,
	projectionComplete bool,
	statsCoverageEventID int64,
	dailyAvailable bool,
) (string, []any) {
	values := make([]string, 0, len(windows))
	args := make([]any, 0, len(windows)*7+4)
	for _, window := range windows {
		values = append(values, "(?, ?, ?, ?, ?, ?, ?)")
		args = append(args,
			window.RequestIndex,
			window.FromMS,
			window.ToMS,
			ceilDayMS(window.FromMS),
			floorDayMS(window.ToMS),
			accountWindowKey(window),
			accountWindowCanUseDaily(window),
		)
	}

	rawIdentity := usageidentity.SQLAccountKeyExpression("e")
	query := `with window_targets(
		request_index, from_ms, to_ms, full_start_ms, full_end_ms, account_key, use_daily
	) as (
		values ` + strings.Join(values, ",") + `
	)
	select
		w.request_index, p.requested_model as model, p.analytics_model, p.resolved_model, p.service_tier, p.failed,
		p.accounting_version, p.accounting_valid, p.accounting_quality,
		p.input_tokens, p.output_tokens, p.reasoning_tokens, p.cached_tokens,
		p.cache_tokens, p.cache_read_tokens, p.cache_creation_tokens,
		p.normalized_uncached_input_tokens, p.normalized_total_input_tokens,
		p.normalized_cache_read_tokens, p.normalized_cache_creation_tokens,
		p.normalized_non_reasoning_output_tokens, p.normalized_reasoning_output_tokens,
		p.normalized_total_output_tokens, p.unclassified_tokens, p.total_tokens, p.timestamp_ms
	from window_targets w
	join usage_monitoring_event_projection_v1 p
		on p.event_id <= ?
			and p.timestamp_ms >= w.from_ms
			and p.timestamp_ms < w.to_ms
			and p.account_key = w.account_key`
	args = append(args, coverageEventID)
	if dailyAvailable {
		query += `
			and (
				w.use_daily = 0
				or p.timestamp_ms < w.full_start_ms
				or p.timestamp_ms >= w.full_end_ms
				or p.event_id > ?
			)`
		args = append(args, statsCoverageEventID)
	}
	if projectionComplete {
		return query, args
	}

	query += `
	union all
	select
		w.request_index, ` + usageidentity.SQLEffectiveRequestedModelExpression("e.model", "e.requested_model") + `, ` + usageidentity.SQLRequestAnalyticsModelExpression("e.model", "e.requested_model") + `, coalesce(e.resolved_model, ''),
		coalesce(e.service_tier, ''), coalesce(e.failed, 0),
		coalesce(e.accounting_version, 0), coalesce(e.accounting_valid, 0),
		coalesce(e.accounting_quality, ''), coalesce(e.input_tokens, 0),
		coalesce(e.output_tokens, 0), coalesce(e.reasoning_tokens, 0),
		coalesce(e.cached_tokens, 0), coalesce(e.cache_tokens, 0),
		coalesce(e.cache_read_tokens, 0), coalesce(e.cache_creation_tokens, 0),
		e.normalized_uncached_input_tokens, e.normalized_total_input_tokens,
		e.normalized_cache_read_tokens, e.normalized_cache_creation_tokens,
		e.normalized_non_reasoning_output_tokens, e.normalized_reasoning_output_tokens,
		e.normalized_total_output_tokens, e.unclassified_tokens, e.total_tokens,
		e.timestamp_ms
	from window_targets w
	join usage_events e
		on e.id > ?
			and e.timestamp_ms >= w.from_ms
			and e.timestamp_ms < w.to_ms
			and ` + rawIdentity + ` = w.account_key`
	args = append(args, coverageEventID)
	if dailyAvailable {
		query += `
			and (
				w.use_daily = 0
				or e.timestamp_ms < w.full_start_ms
				or e.timestamp_ms >= w.full_end_ms
				or e.id > ?
			)`
		args = append(args, statsCoverageEventID)
	}
	return query, args
}

func accountWindowCanUseDaily(window AccountWindowUsageQuery) bool {
	authFile := strings.TrimSpace(window.AuthFileSnapshot)
	authIndex := strings.TrimSpace(window.AuthIndex)
	if authFile == "" || authIndex == "" {
		return false
	}
	expectedKey, valid := usageidentity.AccountKey(usageidentity.Fields{
		AuthFileSnapshot: authFile,
		AuthIndex:        authIndex,
	})
	return valid && accountWindowKey(window) == expectedKey
}

func accountWindowKey(window AccountWindowUsageQuery) string {
	if key := strings.TrimSpace(window.AccountKey); key != "" {
		return key
	}
	key, _ := usageidentity.AccountKey(usageidentity.Fields{
		AuthFileSnapshot:      window.AuthFileSnapshot,
		AuthIndex:             window.AuthIndex,
		AuthProviderSnapshot:  window.AuthProviderSnapshot,
		AuthProjectIDSnapshot: window.AuthProjectIDSnapshot,
		AccountSnapshot:       window.AccountSnapshot,
		AuthLabelSnapshot:     window.AuthLabelSnapshot,
		Source:                window.Source,
	})
	return key
}
