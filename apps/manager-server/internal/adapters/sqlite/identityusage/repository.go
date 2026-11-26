// Package identityusage reads Canonical-Key usage from the canonical identity
// shadow projection. Aggregation reads only counts and token sums; hashes never
// leave this adapter.
package identityusage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/coverageincomplete"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/modelprice"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/pricing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	stateName = "canonical_identity_v1"
	// collectionStartKey is the settings row recording the first durable
	// collection instant of this installation.
	collectionStartKey = "fresh_usage_collection_started_at_ms"
	// maxIdentityPlaceholders bounds one batched aggregation statement.
	maxIdentityPlaceholders = 200
	// maxSummaryTopModels bounds per-key model breakdown entries in list summaries.
	maxSummaryTopModels = 10
)

type repository struct {
	db          *sql.DB
	modelPrices modelprice.Repository
}

// New returns the SQLite adapter of the identity usage read port.
func New(db *sql.DB) ports.Repository {
	return &repository{
		db:          db,
		modelPrices: modelprice.New(db),
	}
}

func (r *repository) Binding(ctx context.Context, id identity.APIKeyID) (ports.Binding, error) {
	if err := id.Validate(); err != nil {
		return ports.Binding{}, err
	}
	// One Canonical Key owns its source-hash chain across initial creation and
	// any in-place rotations. min(b.first_seen_at_ms) returns the earliest
	// attribution start across historical and current bindings; a current hash
	// actively owned by another identity is a conflict, never a silent relay.
	var binding ports.Binding
	var conflict bool
	err := r.db.QueryRowContext(ctx, `
		select coalesce(min(b.first_seen_at_ms), 0),
		       k.origin,
		       i.created_at_ms,
		       exists (
		         select 1 from `+sqliterepo.GatewayAPIKeySourceBindingsTable+` o
		         where o.api_key_hash = k.secret_hash and o.retired_at_ms is null
		           and o.api_key_id <> k.api_key_id
		       )
		from fresh_client_api_keys k
		join `+sqliterepo.GatewayAPIKeyIdentitiesTable+` i on i.id = k.api_key_id
		left join `+sqliterepo.GatewayAPIKeySourceBindingsTable+` b on b.api_key_id = k.api_key_id
		where k.api_key_id = ?
		group by k.api_key_id, k.origin, i.created_at_ms, k.secret_hash`, string(id)).Scan(
		&binding.FirstSeenAtMS, &binding.Origin, &binding.IdentityCreatedAtMS, &conflict)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.Binding{}, ports.ErrBindingNotFound
	}
	if err != nil {
		return ports.Binding{}, fmt.Errorf("read canonical key binding: %w", err)
	}
	binding.APIKeyID = id
	if conflict {
		return ports.Binding{}, ports.ErrBindingConflict
	}
	if binding.FirstSeenAtMS <= 0 {
		return ports.Binding{}, ports.ErrBindingNotFound
	}
	return binding, nil
}

func (r *repository) ProjectionState(ctx context.Context) (ports.ProjectionState, error) {
	var state ports.ProjectionState
	err := r.db.QueryRowContext(ctx, `select status, last_processed_event_id, target_event_id,
		(select coalesce(max(id), 0) from usage_events)
		from `+sqliterepo.GatewayUsageIdentityProjectionStateTable+` where state_name = ?`, stateName).Scan(
		&state.Status, &state.LastProcessedEventID, &state.TargetEventID, &state.LatestEventID)
	if errors.Is(err, sql.ErrNoRows) {
		return ports.ProjectionState{}, errors.New("canonical identity projection state is missing")
	}
	if err != nil {
		return ports.ProjectionState{}, fmt.Errorf("read canonical identity projection state: %w", err)
	}
	return state, nil
}

func (r *repository) Windows(ctx context.Context, ranges []ports.KeyRange, toMS int64) (map[identity.APIKeyID]ports.Window, error) {
	result := map[identity.APIKeyID]ports.Window{}
	if len(ranges) == 0 {
		return result, nil
	}
	type seenRange struct {
		fromMS      int64
		slotsFromMS int64
	}
	deduped := make([]ports.KeyRange, 0, len(ranges))
	seen := make(map[identity.APIKeyID]seenRange, len(ranges))
	for _, item := range ranges {
		if item.SlotsFromMS > 0 && item.SlotsFromMS > item.FromMS {
			return nil, errors.New("invalid identity usage window: slots_from_ms exceeds from_ms")
		}
		slotsFromMS := item.SlotsFromMS
		if slotsFromMS <= 0 {
			slotsFromMS = item.FromMS
		}
		if toMS <= item.FromMS || toMS <= slotsFromMS {
			return nil, errors.New("invalid identity usage window")
		}
		pair := seenRange{fromMS: item.FromMS, slotsFromMS: slotsFromMS}
		if prev, exists := seen[item.APIKeyID]; exists {
			if prev != pair {
				return nil, errors.New("invalid identity usage window")
			}
			continue
		}
		seen[item.APIKeyID] = pair
		item.SlotsFromMS = slotsFromMS
		deduped = append(deduped, item)
	}
	// One statement can only carry a bounded number of placeholders, so large
	// key lists are aggregated in batches and merged. Never truncate: a key
	// dropped here would be reported as an exact zero by its caller.
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin canonical key usage snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		prices       map[string]model.ModelPrice
		pricesLoaded bool
	)
	for start := 0; start < len(deduped); start += maxIdentityPlaceholders {
		end := start + maxIdentityPlaceholders
		if end > len(deduped) {
			end = len(deduped)
		}
		if err := r.windowsBatch(ctx, tx, result, deduped[start:end], toMS, &prices, &pricesLoaded); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit canonical key usage snapshot: %w", err)
	}
	return result, nil
}

type priorLastUsedRange struct {
	apiKeyID       identity.APIKeyID
	minFirstSeenMS int64
	beforeMS       int64
}

func (r *repository) windowsBatch(
	ctx context.Context,
	tx *sql.Tx,
	result map[identity.APIKeyID]ports.Window,
	ranges []ports.KeyRange,
	toMS int64,
	prices *map[string]model.ModelPrice,
	pricesLoaded *bool,
) error {
	rangeArgs, valuesClause, minFromMS := keyRangeArgs(ranges)
	queryArgs := append(append([]any{}, rangeArgs...), minFromMS, toMS)

	rows, err := tx.QueryContext(ctx, `
		with floors(api_key_id, from_ms) as (values `+valuesClause+`),
		     bounds as (
		         select f.api_key_id,
		                f.from_ms,
		                coalesce(min(b.first_seen_at_ms), 0) as min_first_seen_ms
		         from floors f
		         left join `+sqliterepo.GatewayAPIKeySourceBindingsTable+` b on b.api_key_id = f.api_key_id
		         group by f.api_key_id, f.from_ms
		     )
		select b.api_key_id,
		       b.from_ms,
		       b.min_first_seen_ms,
		       coalesce(w.calls, 0),
		       coalesce(w.failures, 0),
		       coalesce(w.input_tokens, 0),
		       coalesce(w.output_tokens, 0),
		       coalesce(w.total_tokens, 0),
		       coalesce(w.cached_tokens, 0),
		       coalesce(w.reasoning_tokens, 0),
		       w.last_used_at_ms
		from bounds b
		left join (
		    select p.api_key_id,
		           count(*) as calls,
		           coalesce(sum(case when u.failed <> 0 then 1 else 0 end), 0) as failures,
		           coalesce(sum(coalesce(u.normalized_total_input_tokens, u.input_tokens, 0)), 0) as input_tokens,
		           coalesce(sum(u.output_tokens), 0) as output_tokens,
		           coalesce(sum(u.total_tokens), 0) as total_tokens,
		           coalesce(sum(coalesce(u.normalized_cache_read_tokens, max(max(coalesce(u.cached_tokens, 0), coalesce(u.cache_tokens, 0)) - max(coalesce(u.cache_read_tokens, 0), 0) - max(coalesce(u.cache_creation_tokens, 0), 0), 0) + max(coalesce(u.cache_read_tokens, 0), 0))), 0) as cached_tokens,
		           coalesce(sum(u.reasoning_tokens), 0) as reasoning_tokens,
		           max(case when u.timestamp_ms >= bd.min_first_seen_ms then u.timestamp_ms end) as last_used_at_ms
		    from `+sqliterepo.GatewayUsageIdentityProjectionTable+` p
		    join bounds bd on bd.api_key_id = p.api_key_id
		    join usage_events u on u.id = p.usage_event_id
		    where p.api_key_state = 'mapped'
		      and u.timestamp_ms >= ? and u.timestamp_ms < ?
		      and u.timestamp_ms >= bd.from_ms
		    group by p.api_key_id
		) w on w.api_key_id = b.api_key_id`, queryArgs...)
	if err != nil {
		return fmt.Errorf("aggregate canonical key usage: %w", err)
	}
	defer rows.Close()
	var priors []priorLastUsedRange
	for rows.Next() {
		var rawID string
		var fromMS, minFirstSeenMS int64
		var lastUsed sql.NullInt64
		var window ports.Window
		if err := rows.Scan(&rawID, &fromMS, &minFirstSeenMS,
			&window.Totals.Calls, &window.Totals.Failure,
			&window.Totals.InputTokens, &window.Totals.OutputTokens, &window.Totals.TotalTokens,
			&window.Totals.CachedTokens, &window.Totals.ReasoningTokens,
			&lastUsed); err != nil {
			return fmt.Errorf("scan canonical key usage: %w", err)
		}
		id := identity.APIKeyID(rawID)
		if lastUsed.Valid && lastUsed.Int64 > 0 {
			ts := lastUsed.Int64
			window.Totals.LastUsedAtMS = &ts
		} else if minFirstSeenMS < fromMS {
			priors = append(priors, priorLastUsedRange{
				apiKeyID:       id,
				minFirstSeenMS: minFirstSeenMS,
				beforeMS:       fromMS,
			})
		}
		if window.Totals.Calls > 0 || window.Totals.LastUsedAtMS != nil {
			result[id] = window
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate canonical key usage: %w", err)
	}
	rows.Close()

	if len(priors) > 0 {
		priorArgs := make([]any, 0, len(priors)*3+2)
		minPriorMS := priors[0].minFirstSeenMS
		maxPriorBeforeMS := priors[0].beforeMS
		for _, item := range priors {
			priorArgs = append(priorArgs, string(item.apiKeyID), item.minFirstSeenMS, item.beforeMS)
			if item.minFirstSeenMS < minPriorMS {
				minPriorMS = item.minFirstSeenMS
			}
			if item.beforeMS > maxPriorBeforeMS {
				maxPriorBeforeMS = item.beforeMS
			}
		}
		priorValues := strings.TrimSuffix(strings.Repeat("(?,?,?),", len(priors)), ",")
		priorArgs = append(priorArgs, minPriorMS, maxPriorBeforeMS)

		priorRows, err := tx.QueryContext(ctx, `
			with priors(api_key_id, min_ms, before_ms) as (values `+priorValues+`),
			candidates as (
				select p.api_key_id, max(u.timestamp_ms) as last_used_ms, pr.before_ms
				from `+sqliterepo.GatewayUsageIdentityProjectionTable+` p
				join priors pr on pr.api_key_id = p.api_key_id
				join usage_events u on u.id = p.usage_event_id
				where p.api_key_state = 'mapped'
				  and u.timestamp_ms >= ? and u.timestamp_ms < ?
				  and u.timestamp_ms >= pr.min_ms and u.timestamp_ms < pr.before_ms
				group by p.api_key_id, pr.before_ms
			)
			select c.api_key_id, c.last_used_ms
			from candidates c
			where c.last_used_ms is not null
			  and not exists (
				select 1
				from `+sqliterepo.GatewayUsageIdentityProjectionTable+` p2
				join `+sqliterepo.GatewayAPIKeySourceBindingsTable+` b on b.api_key_hash = p2.api_key_source_hash
				where b.api_key_id = c.api_key_id
				  and p2.api_key_state <> 'mapped'
				  and p2.evidence_timestamp_ms > c.last_used_ms
				  and p2.evidence_timestamp_ms < c.before_ms
			  )`, priorArgs...)
		if err != nil {
			return fmt.Errorf("read canonical key lifetime last used: %w", err)
		}
		defer priorRows.Close()
		for priorRows.Next() {
			var rawID string
			var lastUsed sql.NullInt64
			if err := priorRows.Scan(&rawID, &lastUsed); err != nil {
				return fmt.Errorf("scan canonical key lifetime last used: %w", err)
			}
			if lastUsed.Valid && lastUsed.Int64 > 0 {
				id := identity.APIKeyID(rawID)
				window := result[id]
				ts := lastUsed.Int64
				window.Totals.LastUsedAtMS = &ts
				result[id] = window
			}
		}
		if err := priorRows.Err(); err != nil {
			return fmt.Errorf("iterate canonical key lifetime last used: %w", err)
		}
		priorRows.Close()
	}

	// Events whose source hash could not be attributed inside this key's own
	// provable [from_ms, toMS) range are positive proof that the window is
	// incomplete, even when the key currently has no calls.
	unmappedRows, err := tx.QueryContext(ctx, `
		with floors(api_key_id, from_ms) as (values `+valuesClause+`)
		select b.api_key_id, count(distinct p.usage_event_id)
		from `+sqliterepo.GatewayUsageIdentityProjectionTable+` p
		join `+sqliterepo.GatewayAPIKeySourceBindingsTable+` b on b.api_key_hash = p.api_key_source_hash
		join floors f on f.api_key_id = b.api_key_id
		where p.api_key_state <> 'mapped'
		  and p.evidence_timestamp_ms >= ? and p.evidence_timestamp_ms < ?
		  and p.evidence_timestamp_ms >= f.from_ms
		group by b.api_key_id`, queryArgs...)
	if err != nil {
		return fmt.Errorf("count unattributed canonical key usage: %w", err)
	}
	defer unmappedRows.Close()
	for unmappedRows.Next() {
		var rawID string
		var count int64
		if err := unmappedRows.Scan(&rawID, &count); err != nil {
			return fmt.Errorf("scan unattributed canonical key usage: %w", err)
		}
		window, ok := result[identity.APIKeyID(rawID)]
		if !ok {
			window = ports.Window{}
		}
		window.UnmappedEvents = count
		result[identity.APIKeyID(rawID)] = window
	}
	if err := unmappedRows.Err(); err != nil {
		return err
	}
	unmappedRows.Close()

	if err := r.populateWindowDetails(ctx, tx, result, ranges, toMS, prices, pricesLoaded); err != nil {
		return err
	}
	return nil
}

func (r *repository) populateWindowDetails(
	ctx context.Context,
	tx *sql.Tx,
	result map[identity.APIKeyID]ports.Window,
	ranges []ports.KeyRange,
	toMS int64,
	prices *map[string]model.ModelPrice,
	pricesLoaded *bool,
) error {
	if len(result) == 0 || len(ranges) == 0 {
		return nil
	}
	rangeArgs, valuesClause, minFromMS := keyRangeArgs(ranges)
	queryArgs := append(append([]any{}, rangeArgs...), minFromMS, toMS)

	if !*pricesLoaded {
		loadedPrices, err := r.modelPrices.LoadAllTx(ctx, tx)
		if err != nil {
			return fmt.Errorf("load model prices: %w", err)
		}
		*prices = loadedPrices
		*pricesLoaded = true
	}

	requestedModelExpr := usageidentity.SQLEffectiveRequestedModelExpression("nullif(trim(u.model), '')", "trim(u.requested_model)")
	analyticsModelExpr := usageidentity.SQLAnalyticsModelExpression(requestedModelExpr)
	modelQuery := fmt.Sprintf(`
		with floors(api_key_id, from_ms) as (values %s),
		     pricing_base_events as (
		         select p.api_key_id,
		                u.timestamp_ms,
		                coalesce(u.failed, 0) as failed,
		                coalesce(trim(u.service_tier), '') as service_tier_value,
		                %s as requested_model_value,
		                %s as analytics_model_value,
		                coalesce(nullif(trim(u.resolved_model), ''), %s) as billing_model_value,
		                coalesce(u.normalized_total_input_tokens, u.input_tokens, 0) as normalized_input_tokens_value,
		                coalesce(u.output_tokens, 0) as output_tokens_value,
		                max(
		                    max(coalesce(u.cached_tokens, 0), coalesce(u.cache_tokens, 0)) -
		                    max(coalesce(u.cache_read_tokens, 0), 0) -
		                    max(coalesce(u.cache_creation_tokens, 0), 0),
		                    0
		                ) as compat_cached_tokens_value,
		                coalesce(u.cache_read_tokens, 0) as cache_read_tokens_value,
		                coalesce(u.cache_creation_tokens, 0) as cache_creation_tokens_value,
		                coalesce(u.total_tokens, 0) as total_tokens_value
		         from %s p
		         join floors f on f.api_key_id = p.api_key_id
		         join usage_events u on u.id = p.usage_event_id
		         where p.api_key_state = 'mapped'
		           and u.timestamp_ms >= ? and u.timestamp_ms < ?
		           and u.timestamp_ms >= f.from_ms
		     ),
		     pricing_resolved_events as (
		         select b.*,
		                case
		                    when billing_price.model is not null then b.billing_model_value
		                    when analytics_price.model is not null then b.analytics_model_value
		                    when display_price.model is not null then b.requested_model_value
		                    else b.billing_model_value
		                end as pricing_model_value
		         from pricing_base_events b
		         left join model_prices billing_price on billing_price.model = b.billing_model_value
		         left join model_prices analytics_price on analytics_price.model = b.analytics_model_value
		         left join model_prices display_price on display_price.model = b.requested_model_value
		     ),
		     banded_usage_events as (
		         select r.*,
		                coalesce((
		                    select max(tier.threshold_tokens)
		                    from model_price_context_tiers tier
		                    where tier.model = r.pricing_model_value
		                      and r.normalized_input_tokens_value > tier.threshold_tokens
		                ), %d) as context_threshold_tokens_value
		         from pricing_resolved_events r
		     )
		select e.api_key_id,
		       e.analytics_model_value,
		       e.billing_model_value,
		       e.pricing_model_value,
		       e.context_threshold_tokens_value,
		       e.service_tier_value,
		       count(*),
		       coalesce(sum(case when e.failed = 0 then 1 else 0 end), 0),
		       coalesce(sum(e.normalized_input_tokens_value), 0),
		       coalesce(sum(e.output_tokens_value), 0),
		       coalesce(sum(e.compat_cached_tokens_value), 0),
		       coalesce(sum(e.cache_read_tokens_value), 0),
		       coalesce(sum(e.cache_creation_tokens_value), 0),
		       coalesce(sum(case when e.normalized_input_tokens_value > %d then e.normalized_input_tokens_value else 0 end), 0),
		       coalesce(sum(case when e.normalized_input_tokens_value > %d then e.output_tokens_value else 0 end), 0),
		       coalesce(sum(case when e.normalized_input_tokens_value > %d then e.compat_cached_tokens_value else 0 end), 0),
		       coalesce(sum(case when e.normalized_input_tokens_value > %d then e.cache_read_tokens_value else 0 end), 0),
		       coalesce(sum(case when e.normalized_input_tokens_value > %d then e.cache_creation_tokens_value else 0 end), 0),
		       coalesce(sum(e.total_tokens_value), 0),
		       max(e.timestamp_ms)
		from banded_usage_events e
		group by e.api_key_id,
		         e.analytics_model_value,
		         e.billing_model_value,
		         e.pricing_model_value,
		         e.context_threshold_tokens_value,
		         e.service_tier_value`,
		valuesClause,
		requestedModelExpr,
		analyticsModelExpr,
		analyticsModelExpr,
		sqliterepo.GatewayUsageIdentityProjectionTable,
		model.ModelPriceBaseContextThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
		usage.LongContextInputTokenThreshold,
	)
	modelRows, err := tx.QueryContext(ctx, modelQuery, queryArgs...)
	if err != nil {
		return fmt.Errorf("aggregate canonical key models and cost: %w", err)
	}
	defer modelRows.Close()

	type modelAccum struct {
		calls       int64
		totalTokens int64
		cost        float64
		hasPrice    bool
		hasUnpriced bool
		lastMS      int64
	}
	modelsByKey := map[identity.APIKeyID]map[string]*modelAccum{}
	costByKey := map[identity.APIKeyID]float64{}
	pricedByKey := map[identity.APIKeyID]bool{}
	unpricedByKey := map[identity.APIKeyID]bool{}

	for modelRows.Next() {
		var rawID, displayModel, billingModel, pricingModel, serviceTier string
		var contextThresholdTok, calls, successCalls, inputTok, outputTok, cachedTok, cacheReadTok, cacheCreateTok int64
		var longInputTok, longOutputTok, longCachedTok, longCacheReadTok, longCacheCreateTok, totalTok, lastMS int64
		if err := modelRows.Scan(
			&rawID, &displayModel, &billingModel, &pricingModel, &contextThresholdTok, &serviceTier,
			&calls, &successCalls, &inputTok, &outputTok, &cachedTok, &cacheReadTok, &cacheCreateTok,
			&longInputTok, &longOutputTok, &longCachedTok, &longCacheReadTok, &longCacheCreateTok,
			&totalTok, &lastMS,
		); err != nil {
			return fmt.Errorf("scan canonical key models and cost: %w", err)
		}
		id := identity.APIKeyID(rawID)
		zeroTokenFailedOnly := successCalls == 0 &&
			inputTok <= 0 && outputTok <= 0 &&
			cachedTok <= 0 && cacheReadTok <= 0 && cacheCreateTok <= 0 &&
			longInputTok <= 0 && longOutputTok <= 0 &&
			longCachedTok <= 0 && longCacheReadTok <= 0 && longCacheCreateTok <= 0 &&
			totalTok <= 0
		rowCost, matched := pricing.CostForModelCandidatesWithServiceTierMatched(
			[]string{billingModel, displayModel},
			serviceTier,
			pricing.ModelTokens{
				PricingModel:            pricingModel,
				ContextThresholdTokens:  contextThresholdTok,
				InputTokens:             inputTok,
				OutputTokens:            outputTok,
				CachedTokens:            cachedTok,
				CacheReadTokens:         cacheReadTok,
				CacheCreationTokens:     cacheCreateTok,
				LongInputTokens:         longInputTok,
				LongOutputTokens:        longOutputTok,
				LongCachedTokens:        longCachedTok,
				LongCacheReadTokens:     longCacheReadTok,
				LongCacheCreationTokens: longCacheCreateTok,
			},
			*prices,
		)
		if matched {
			pricedByKey[id] = true
			costByKey[id] += rowCost
		} else if !zeroTokenFailedOnly {
			unpricedByKey[id] = true
		}
		if displayModel != "" && displayModel != "-" {
			perModel := modelsByKey[id]
			if perModel == nil {
				perModel = map[string]*modelAccum{}
				modelsByKey[id] = perModel
			}
			acc := perModel[displayModel]
			if acc == nil {
				acc = &modelAccum{}
				perModel[displayModel] = acc
			}
			acc.calls += calls
			acc.totalTokens += totalTok
			if matched {
				acc.hasPrice = true
				acc.cost += rowCost
			} else if !zeroTokenFailedOnly {
				acc.hasUnpriced = true
			}
			if lastMS > acc.lastMS {
				acc.lastMS = lastMS
			}
		}
	}
	if err := modelRows.Err(); err != nil {
		return fmt.Errorf("iterate canonical key models and cost: %w", err)
	}
	modelRows.Close()

	for id, perModel := range modelsByKey {
		window, ok := result[id]
		if !ok {
			continue
		}
		type rankedModel struct {
			model       string
			calls       int64
			totalTokens int64
			cost        float64
			hasPrice    bool
			hasUnpriced bool
			lastMS      int64
		}
		ranked := make([]rankedModel, 0, len(perModel))
		for m, acc := range perModel {
			ranked = append(ranked, rankedModel{
				model:       m,
				calls:       acc.calls,
				totalTokens: acc.totalTokens,
				cost:        acc.cost,
				hasPrice:    acc.hasPrice,
				hasUnpriced: acc.hasUnpriced,
				lastMS:      acc.lastMS,
			})
		}
		sort.SliceStable(ranked, func(i, j int) bool {
			if ranked[i].calls != ranked[j].calls {
				return ranked[i].calls > ranked[j].calls
			}
			if ranked[i].lastMS != ranked[j].lastMS {
				return ranked[i].lastMS > ranked[j].lastMS
			}
			return ranked[i].model < ranked[j].model
		})
		window.ModelCount = len(ranked)
		limit := min(len(ranked), maxSummaryTopModels)
		topModels := make([]ports.ModelCount, 0, limit)
		for i := range limit {
			entry := ports.ModelCount{
				Model:       ranked[i].model,
				Calls:       ranked[i].calls,
				TotalTokens: ranked[i].totalTokens,
			}
			if ranked[i].hasPrice && !ranked[i].hasUnpriced {
				c := ranked[i].cost
				entry.EstimatedCostUSD = &c
			}
			topModels = append(topModels, entry)
		}
		window.TopModels = topModels
		result[id] = window
	}

	for id, hasPrice := range pricedByKey {
		if !hasPrice || unpricedByKey[id] {
			continue
		}
		window, ok := result[id]
		if !ok {
			continue
		}
		cost := costByKey[id]
		window.EstimatedCostUSD = &cost
		result[id] = window
	}

	slotFromByKey := make(map[identity.APIKeyID]int64, len(ranges))
	slotValuesArgs := make([]any, 0, len(ranges)*3)
	anyEligibleSlotRange := false
	for _, item := range ranges {
		slotFrom := item.SlotsFromMS
		if slotFrom <= 0 {
			slotFrom = item.FromMS
		}
		slotFromByKey[item.APIKeyID] = slotFrom
		if toMS-slotFrom >= ports.RecentStatusSlotCount {
			anyEligibleSlotRange = true
		}
		slotValuesArgs = append(slotValuesArgs, string(item.APIKeyID), item.FromMS, slotFrom)
	}
	if anyEligibleSlotRange {
		slotValuesClause := strings.TrimSuffix(strings.Repeat("(?,?,?),", len(ranges)), ",")
		slotArgs := append(slotValuesArgs, toMS, toMS, toMS, toMS, minFromMS, toMS)
		slotRows, err := tx.QueryContext(ctx, `
			with floors(api_key_id, from_ms, slots_from_ms) as (values `+slotValuesClause+`)
			select p.api_key_id,
			       case
			           when (? - f.slots_from_ms) <= 0 then 0
			           when (((u.timestamp_ms - f.slots_from_ms) * 20) / (? - f.slots_from_ms)) >= 20 then 19
			           when (((u.timestamp_ms - f.slots_from_ms) * 20) / (? - f.slots_from_ms)) < 0 then 0
			           else (((u.timestamp_ms - f.slots_from_ms) * 20) / (? - f.slots_from_ms))
			       end as slot_idx,
			       count(*),
			       coalesce(sum(case when u.failed <> 0 then 1 else 0 end), 0)
			from `+sqliterepo.GatewayUsageIdentityProjectionTable+` p
			join floors f on f.api_key_id = p.api_key_id
			join usage_events u on u.id = p.usage_event_id
			where p.api_key_state = 'mapped'
			  and u.timestamp_ms >= ? and u.timestamp_ms < ?
			  and u.timestamp_ms >= f.from_ms
			  and u.timestamp_ms >= f.slots_from_ms
			group by p.api_key_id, slot_idx`, slotArgs...)
		if err != nil {
			return fmt.Errorf("aggregate canonical key status slots: %w", err)
		}
		defer slotRows.Close()

		slotsByKey := map[identity.APIKeyID][]ports.StatusSlot{}
		for slotRows.Next() {
			var rawID string
			var slotIdx int
			var calls, failures int64
			if err := slotRows.Scan(&rawID, &slotIdx, &calls, &failures); err != nil {
				return fmt.Errorf("scan canonical key status slots: %w", err)
			}
			id := identity.APIKeyID(rawID)
			keyFrom, ok := slotFromByKey[id]
			if !ok || toMS-keyFrom < ports.RecentStatusSlotCount {
				continue
			}
			slots, exists := slotsByKey[id]
			if !exists {
				slots = buildStatusSlots(keyFrom, toMS)
				if slots == nil {
					continue
				}
			}
			if slotIdx >= 0 && slotIdx < ports.RecentStatusSlotCount {
				slots[slotIdx].Success += max(0, calls-failures)
				slots[slotIdx].Failure += failures
			}
			slotsByKey[id] = slots
		}
		if err := slotRows.Err(); err != nil {
			return fmt.Errorf("iterate canonical key status slots: %w", err)
		}
		slotRows.Close()

		for id, slots := range slotsByKey {
			window, ok := result[id]
			if !ok || window.Totals.Calls <= 0 {
				continue
			}
			window.RecentSlots = slots
			result[id] = window
		}
	}
	return nil
}

func buildStatusSlots(fromMS, toMS int64) []ports.StatusSlot {
	span := toMS - fromMS
	if span < ports.RecentStatusSlotCount {
		return nil
	}
	const count = int64(ports.RecentStatusSlotCount)
	slots := make([]ports.StatusSlot, ports.RecentStatusSlotCount)
	for i := range ports.RecentStatusSlotCount {
		start := fromMS
		if i > 0 {
			start = fromMS + (int64(i)*span+count-1)/count
		}
		end := toMS
		if i < ports.RecentStatusSlotCount-1 {
			end = fromMS + (int64(i+1)*span+count-1)/count
		}
		slots[i] = ports.StatusSlot{
			StartMS: start,
			EndMS:   end,
		}
	}
	return slots
}

func (r *repository) Series(ctx context.Context, id identity.APIKeyID, fromMS, toMS, bucketMS int64) ([]ports.Bucket, error) {
	if err := id.Validate(); err != nil {
		return nil, err
	}
	if toMS <= fromMS || bucketMS <= 0 {
		return nil, errors.New("invalid identity usage series window")
	}
	rows, err := r.db.QueryContext(ctx, `
		select (u.timestamp_ms / ?) * ?, count(*),
			coalesce(sum(case when u.failed <> 0 then 1 else 0 end), 0),
			coalesce(sum(u.total_tokens), 0)
		from `+sqliterepo.GatewayUsageIdentityProjectionTable+` p
		join usage_events u on u.id = p.usage_event_id
		where p.api_key_state = 'mapped' and p.api_key_id = ?
		  and u.timestamp_ms >= ? and u.timestamp_ms < ?
		group by 1 order by 1`, bucketMS, bucketMS, string(id), fromMS, toMS)
	if err != nil {
		return nil, fmt.Errorf("aggregate canonical key usage series: %w", err)
	}
	defer rows.Close()
	buckets := make([]ports.Bucket, 0)
	for rows.Next() {
		var bucket ports.Bucket
		if err := rows.Scan(&bucket.BucketMS, &bucket.Calls, &bucket.Failure, &bucket.TotalTokens); err != nil {
			return nil, fmt.Errorf("scan canonical key usage series: %w", err)
		}
		buckets = append(buckets, bucket)
	}
	return buckets, rows.Err()
}

func (r *repository) RawDeletedIn(ctx context.Context, fromMS, toMS int64) (bool, error) {
	if toMS <= fromMS {
		return false, nil
	}
	var deleted bool
	err := r.db.QueryRowContext(ctx, `select exists (
		select 1 from usage_archive_deleted_coverage_daily
		where deleted_event_count > 0 and max_timestamp_ms >= ? and min_timestamp_ms < ?
		union all
		select 1 from usage_archive_event_refs
		where raw_deleted_at_ms is not null and timestamp_ms >= ? and timestamp_ms < ?
	)`, fromMS, toMS, fromMS, toMS).Scan(&deleted)
	if err != nil {
		return false, fmt.Errorf("inspect archived raw usage coverage: %w", err)
	}
	return deleted, nil
}

func (r *repository) LatestRawDeletedIn(ctx context.Context, fromMS, toMS int64) (int64, bool, error) {
	if toMS <= fromMS {
		return 0, false, nil
	}
	var latest sql.NullInt64
	err := r.db.QueryRowContext(ctx, `select max(ts) from (
		select max(max_timestamp_ms) as ts from usage_archive_deleted_coverage_daily
		where deleted_event_count > 0 and max_timestamp_ms >= ? and min_timestamp_ms < ?
		union all
		select max(timestamp_ms) as ts from usage_archive_event_refs
		where raw_deleted_at_ms is not null and timestamp_ms >= ? and timestamp_ms < ?
	)`, fromMS, toMS, fromMS, toMS).Scan(&latest)
	if err != nil {
		return 0, false, fmt.Errorf("inspect latest archived raw usage deletion: %w", err)
	}
	if !latest.Valid {
		return 0, false, nil
	}
	return latest.Int64, true, nil
}

func (r *repository) IncompleteDaysIn(ctx context.Context, fromMS, toMS int64) ([]ports.IncompleteDay, error) {
	days := []ports.IncompleteDay{}
	if toMS <= fromMS {
		return days, nil
	}
	marks, err := coverageincomplete.DaysIn(ctx, r.db, fromMS, toMS)
	if err != nil {
		return nil, fmt.Errorf("read the incomplete coverage days: %w", err)
	}
	for _, mark := range marks {
		days = append(days, ports.IncompleteDay{Day: mark.Day, Reason: mark.Reason})
	}
	return days, nil
}

func (r *repository) CollectionStartedAtMS(ctx context.Context) (int64, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `select value from settings where key = ?`, collectionStartKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read usage collection start: %w", err)
	}
	var startedAtMS int64
	if _, err := fmt.Sscan(raw, &startedAtMS); err != nil || startedAtMS <= 0 {
		return 0, nil
	}
	return startedAtMS, nil
}

func (r *repository) MarkCollectionStarted(ctx context.Context, nowMS int64) error {
	if nowMS <= 0 {
		return errors.New("invalid usage collection start")
	}
	_, err := r.db.ExecContext(ctx, `insert into settings(key, value, updated_at_ms) values(?, ?, ?)
		on conflict(key) do update set
			value = excluded.value,
			updated_at_ms = excluded.updated_at_ms
		where cast(settings.value as integer) <= 0 or cast(excluded.value as integer) < cast(settings.value as integer)`,
		collectionStartKey, fmt.Sprintf("%d", nowMS), nowMS)
	if err != nil {
		return fmt.Errorf("record usage collection start: %w", err)
	}
	return nil
}

// keyRangeArgs renders one bounded batch of (api_key_id, from_ms) pairs plus
// the batch-wide minimum floor used to bound the SQLite timestamp scan.
func keyRangeArgs(ranges []ports.KeyRange) ([]any, string, int64) {
	args := make([]any, 0, len(ranges)*2)
	minFromMS := ranges[0].FromMS
	for _, item := range ranges {
		args = append(args, string(item.APIKeyID), item.FromMS)
		if item.FromMS < minFromMS {
			minFromMS = item.FromMS
		}
	}
	return args, strings.TrimSuffix(strings.Repeat("(?,?),", len(ranges)), ","), minFromMS
}
