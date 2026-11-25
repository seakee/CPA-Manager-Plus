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
)
const (
	stateName = "canonical_identity_v1"
	// collectionStartKey is the settings row recording the first durable
	// collection instant of this installation.
	collectionStartKey = "fresh_usage_collection_started_at_ms"
	// maxIdentityPlaceholders bounds one batched aggregation statement.
	maxIdentityPlaceholders = 200
)

type repository struct{ db *sql.DB }

// New returns the SQLite adapter of the identity usage read port.
func New(db *sql.DB) ports.Repository { return &repository{db: db} }

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
	deduped := make([]ports.KeyRange, 0, len(ranges))
	seen := make(map[identity.APIKeyID]int64, len(ranges))
	for _, item := range ranges {
		if toMS <= item.FromMS {
			return nil, errors.New("invalid identity usage window")
		}
		if prev, exists := seen[item.APIKeyID]; exists {
			if prev != item.FromMS {
				return nil, errors.New("invalid identity usage window")
			}
			continue
		}
		seen[item.APIKeyID] = item.FromMS
		deduped = append(deduped, item)
	}
	// One statement can only carry a bounded number of placeholders, so large
	// key lists are aggregated in batches and merged. Never truncate: a key
	// dropped here would be reported as an exact zero by its caller.
	for start := 0; start < len(deduped); start += maxIdentityPlaceholders {
		end := start + maxIdentityPlaceholders
		if end > len(deduped) {
			end = len(deduped)
		}
		if err := r.windowsBatch(ctx, result, deduped[start:end], toMS); err != nil {
			return nil, err
		}
	}
	return result, nil
}

type priorLastUsedRange struct {
	apiKeyID       identity.APIKeyID
	minFirstSeenMS int64
	beforeMS       int64
}

func (r *repository) windowsBatch(ctx context.Context, result map[identity.APIKeyID]ports.Window, ranges []ports.KeyRange, toMS int64) error {
	rangeArgs, valuesClause, minFromMS := keyRangeArgs(ranges)
	queryArgs := append(append([]any{}, rangeArgs...), minFromMS, toMS)

	rows, err := r.db.QueryContext(ctx, `
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
		       w.last_used_at_ms
		from bounds b
		left join (
		    select p.api_key_id,
		           count(*) as calls,
		           coalesce(sum(case when u.failed <> 0 then 1 else 0 end), 0) as failures,
		           coalesce(sum(u.input_tokens), 0) as input_tokens,
		           coalesce(sum(u.output_tokens), 0) as output_tokens,
		           coalesce(sum(u.total_tokens), 0) as total_tokens,
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

		priorRows, err := r.db.QueryContext(ctx, `
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
	unmappedRows, err := r.db.QueryContext(ctx, `
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

	if err := r.populateWindowDetails(ctx, result, ranges, toMS); err != nil {
		return err
	}
	return nil
}

func (r *repository) populateWindowDetails(
	ctx context.Context,
	result map[identity.APIKeyID]ports.Window,
	ranges []ports.KeyRange,
	toMS int64,
) error {
	if len(result) == 0 || len(ranges) == 0 {
		return nil
	}
	rangeArgs, valuesClause, minFromMS := keyRangeArgs(ranges)
	queryArgs := append(append([]any{}, rangeArgs...), minFromMS, toMS)

	prices, err := modelprice.New(r.db).LoadAll(ctx)
	if err != nil {
		prices = map[string]model.ModelPrice{}
	}

	modelRows, err := r.db.QueryContext(ctx, `
		with floors(api_key_id, from_ms) as (values `+valuesClause+`)
		select p.api_key_id,
		       coalesce(nullif(trim(u.model), ''), nullif(trim(u.requested_model), ''), ''),
		       coalesce(nullif(trim(u.resolved_model), ''), nullif(trim(u.model), ''), ''),
		       coalesce(trim(u.service_tier), ''),
		       count(*),
		       coalesce(sum(coalesce(u.normalized_total_input_tokens, u.input_tokens, 0)), 0),
		       coalesce(sum(u.output_tokens), 0),
		       coalesce(sum(max(
		           max(coalesce(u.cached_tokens, 0), coalesce(u.cache_tokens, 0)) -
		           max(coalesce(u.cache_read_tokens, 0), 0) -
		           max(coalesce(u.cache_creation_tokens, 0), 0),
		           0
		       )), 0),
		       coalesce(sum(coalesce(u.cache_read_tokens, 0)), 0),
		       coalesce(sum(coalesce(u.cache_creation_tokens, 0)), 0),
		       max(u.timestamp_ms)
		from `+sqliterepo.GatewayUsageIdentityProjectionTable+` p
		join floors f on f.api_key_id = p.api_key_id
		join usage_events u on u.id = p.usage_event_id
		where p.api_key_state = 'mapped'
		  and u.timestamp_ms >= ? and u.timestamp_ms < ?
		  and u.timestamp_ms >= f.from_ms
		group by p.api_key_id,
		         coalesce(nullif(trim(u.model), ''), nullif(trim(u.requested_model), ''), ''),
		         coalesce(nullif(trim(u.resolved_model), ''), nullif(trim(u.model), ''), ''),
		         coalesce(trim(u.service_tier), '')`, queryArgs...)
	if err != nil {
		return fmt.Errorf("aggregate canonical key models and cost: %w", err)
	}
	defer modelRows.Close()

	type modelAccum struct {
		calls  int64
		lastMS int64
	}
	modelsByKey := map[identity.APIKeyID]map[string]*modelAccum{}
	costByKey := map[identity.APIKeyID]float64{}
	pricedByKey := map[identity.APIKeyID]bool{}

	for modelRows.Next() {
		var rawID, displayModel, billingModel, serviceTier string
		var calls, inputTok, outputTok, cachedTok, cacheReadTok, cacheCreateTok, lastMS int64
		if err := modelRows.Scan(
			&rawID, &displayModel, &billingModel, &serviceTier,
			&calls, &inputTok, &outputTok, &cachedTok, &cacheReadTok, &cacheCreateTok, &lastMS,
		); err != nil {
			return fmt.Errorf("scan canonical key models and cost: %w", err)
		}
		id := identity.APIKeyID(rawID)
		if displayModel != "" {
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
			if lastMS > acc.lastMS {
				acc.lastMS = lastMS
			}
		}
		if len(prices) > 0 && hasMatchingModelPrice(prices, billingModel, displayModel) {
			pricedByKey[id] = true
			costByKey[id] += pricing.CostForModelCandidatesWithServiceTier(
				[]string{billingModel, displayModel},
				serviceTier,
				pricing.ModelTokens{
					InputTokens:         inputTok,
					OutputTokens:        outputTok,
					CachedTokens:        cachedTok,
					CacheReadTokens:     cacheReadTok,
					CacheCreationTokens: cacheCreateTok,
				},
				prices,
			)
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
		ranked := make([]struct {
			model  string
			calls  int64
			lastMS int64
		}, 0, len(perModel))
		for m, acc := range perModel {
			ranked = append(ranked, struct {
				model  string
				calls  int64
				lastMS int64
			}{model: m, calls: acc.calls, lastMS: acc.lastMS})
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
		limit := min(len(ranked), 3)
		topModels := make([]ports.ModelCount, 0, limit)
		for i := range limit {
			topModels = append(topModels, ports.ModelCount{
				Model: ranked[i].model,
				Calls: ranked[i].calls,
			})
		}
		window.TopModels = topModels
		result[id] = window
	}

	for id, hasPrice := range pricedByKey {
		if !hasPrice {
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

	recentRows, err := r.db.QueryContext(ctx, `
		with floors(api_key_id, from_ms) as (values `+valuesClause+`),
		     ranked as (
		         select p.api_key_id,
		                u.timestamp_ms,
		                u.failed,
		                coalesce(u.fail_status_code, 0) as status_code,
		                coalesce(nullif(trim(u.model), ''), nullif(trim(u.requested_model), ''), '') as model,
		                row_number() over (partition by p.api_key_id order by u.timestamp_ms desc, u.id desc) as rn
		         from `+sqliterepo.GatewayUsageIdentityProjectionTable+` p
		         join floors f on f.api_key_id = p.api_key_id
		         join usage_events u on u.id = p.usage_event_id
		         where p.api_key_state = 'mapped'
		           and u.timestamp_ms >= ? and u.timestamp_ms < ?
		           and u.timestamp_ms >= f.from_ms
		     )
		select api_key_id, timestamp_ms, failed, status_code, model
		from ranked
		where rn <= 10
		order by api_key_id, timestamp_ms desc, rn asc`, queryArgs...)
	if err != nil {
		return fmt.Errorf("read canonical key recent requests: %w", err)
	}
	defer recentRows.Close()

	for recentRows.Next() {
		var rawID, modelName string
		var ts int64
		var failedInt, statusCode int
		if err := recentRows.Scan(&rawID, &ts, &failedInt, &statusCode, &modelName); err != nil {
			return fmt.Errorf("scan canonical key recent request: %w", err)
		}
		id := identity.APIKeyID(rawID)
		window, ok := result[id]
		if !ok {
			continue
		}
		window.RecentRequests = append(window.RecentRequests, ports.RecentRequest{
			TimestampMS: ts,
			Failed:      failedInt != 0,
			StatusCode:  statusCode,
			Model:       modelName,
		})
		result[id] = window
	}
	return recentRows.Err()
}

func hasMatchingModelPrice(prices map[string]model.ModelPrice, candidates ...string) bool {
	for _, candidate := range candidates {
		trimmed := strings.TrimSpace(candidate)
		if trimmed == "" {
			continue
		}
		if _, ok := prices[trimmed]; ok {
			return true
		}
		lower := strings.ToLower(trimmed)
		for k := range prices {
			if strings.ToLower(k) == lower {
				return true
			}
		}
	}
	return false
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
