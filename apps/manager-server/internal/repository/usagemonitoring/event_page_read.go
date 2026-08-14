package usagemonitoring

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageaccountingsql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

var eventPageAccountingSQL = usageaccountingsql.For("")

type eventPageCandidate struct {
	ID          int64
	TimestampMS int64
}

func (r *repository) LoadEventsCount(ctx context.Context, filter AnalyticsFilter) (int64, State, bool, error) {
	if !SupportsEventProjectionFilter(filter) {
		return 0, State{}, false, nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return 0, State{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	state, available, projectionComplete, err := projectionReadState(ctx, tx)
	if err != nil || !available {
		return 0, state, available, err
	}
	source, args := filteredEventSourceSQL(
		filter,
		state.CoverageEventID,
		"p.event_id as id",
		"e.id",
		eventSourceOptions{ProjectionComplete: projectionComplete},
	)
	var total int64
	if err := tx.QueryRowContext(ctx, `with filtered_events as (`+source+`)
		select count(*) from filtered_events`, args...).Scan(&total); err != nil {
		return 0, state, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, state, false, err
	}
	return total, state, true, nil
}

func (r *repository) LoadEventsPage(
	ctx context.Context,
	filter AnalyticsFilter,
	beforeMS int64,
	beforeID int64,
	limit int,
) (EventsPage, State, bool, error) {
	if !SupportsEventProjectionFilter(filter) {
		return EventsPage{}, State{}, false, nil
	}
	if limit <= 0 {
		return EventsPage{}, State{}, true, nil
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EventsPage{}, State{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	state, available, projectionComplete, err := projectionReadState(ctx, tx)
	if err != nil || !available {
		return EventsPage{}, state, available, err
	}

	source, args := filteredEventSourceSQL(
		filter,
		state.CoverageEventID,
		"p.event_id as id, p.timestamp_ms",
		"e.id, e.timestamp_ms",
		eventSourceOptions{
			BeforeMS:           beforeMS,
			BeforeID:           beforeID,
			ProjectionComplete: projectionComplete,
		},
	)
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, `with filtered_events as (`+source+`)
		select id, timestamp_ms
		from filtered_events
		order by timestamp_ms desc, id desc
		limit ?`, args...)
	if err != nil {
		return EventsPage{}, state, false, err
	}
	candidates := make([]eventPageCandidate, 0, limit+1)
	for rows.Next() {
		var candidate eventPageCandidate
		if err := rows.Scan(&candidate.ID, &candidate.TimestampMS); err != nil {
			_ = rows.Close()
			return EventsPage{}, state, false, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return EventsPage{}, state, false, err
	}
	if err := rows.Close(); err != nil {
		return EventsPage{}, state, false, err
	}

	hasMore := len(candidates) > limit
	if hasMore {
		candidates = candidates[:limit]
	}
	items, err := loadEventPageItemsByCandidates(ctx, tx, candidates)
	if err != nil {
		return EventsPage{}, state, false, err
	}
	if err := tx.Commit(); err != nil {
		return EventsPage{}, state, false, err
	}

	nextBeforeMS := int64(0)
	nextBeforeID := int64(0)
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		nextBeforeMS = last.TimestampMS
		nextBeforeID = last.ID
	}
	return EventsPage{
		Items:        items,
		NextBeforeMS: nextBeforeMS,
		NextBeforeID: nextBeforeID,
		HasMore:      hasMore,
	}, state, true, nil
}

func loadEventPageItemsByCandidates(ctx context.Context, tx *sql.Tx, candidates []eventPageCandidate) ([]EventPageItem, error) {
	if len(candidates) == 0 {
		return []EventPageItem{}, nil
	}
	ids := make([]int64, 0, len(candidates))
	for _, candidate := range candidates {
		ids = append(ids, candidate.ID)
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `select
		id,
		coalesce(request_id, ''),
		event_hash,
		timestamp_ms,
			timestamp,
			model,
			`+usageidentity.SQLRequestAnalyticsModelExpression("model", "requested_model")+` as analytics_model,
			coalesce(nullif(requested_model, ''), model, ''),
			coalesce(resolved_model, ''),
		coalesce(endpoint, ''),
		coalesce(method, ''),
		coalesce(path, ''),
		coalesce(auth_index, ''),
		coalesce(source, ''),
		coalesce(source_hash, ''),
		coalesce(api_key_hash, ''),
		coalesce(account_snapshot, ''),
		coalesce(auth_label_snapshot, ''),
		coalesce(auth_file_snapshot, ''),
		coalesce(nullif(auth_provider_snapshot, ''), provider, ''),
		coalesce(auth_project_id_snapshot, ''),
		coalesce(reasoning_effort, ''),
		coalesce(service_tier, ''),
		coalesce(executor_type, ''),
		coalesce(cache_input_mode, ''),
		coalesce(accounting_version, 0),
		`+eventPageAccountingSQL.Valid+`,
		`+eventPageAccountingSQL.Quality+`,
		`+eventPageAccountingSQL.TotalInput+`,
		`+eventPageAccountingSQL.TotalOutput+`,
		`+eventPageAccountingSQL.NonReasoningOutput+`,
		`+eventPageAccountingSQL.CompatibleCached+`,
		`+eventPageAccountingSQL.CacheRead+`,
		`+eventPageAccountingSQL.CacheCreation+`,
		`+eventPageAccountingSQL.ReasoningOutput+`,
		`+eventPageAccountingSQL.Unclassified+`,
		`+eventPageAccountingSQL.Incomplete+`,
		`+eventPageAccountingSQL.Total+`,
		latency_ms,
		ttft_ms,
		failed,
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
	where id in (select value from json_each(?))
	order by timestamp_ms desc, id desc`, string(encoded))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]EventPageItem, 0, len(candidates))
	for rows.Next() {
		var item EventPageItem
		var failed int
		var accountingValid int
		var responseMetadataJSON string
		if err := rows.Scan(
			&item.ID,
			&item.RequestID,
			&item.EventHash,
			&item.TimestampMS,
			&item.Timestamp,
			&item.Model,
			&item.AnalyticsModel,
			&item.RequestedModel,
			&item.ResolvedModel,
			&item.Endpoint,
			&item.Method,
			&item.Path,
			&item.AuthIndex,
			&item.Source,
			&item.SourceHash,
			&item.APIKeyHash,
			&item.AccountSnapshot,
			&item.AuthLabelSnapshot,
			&item.AuthFileSnapshot,
			&item.AuthProviderSnapshot,
			&item.AuthProjectIDSnapshot,
			&item.ReasoningEffort,
			&item.ServiceTier,
			&item.ExecutorType,
			&item.CacheInputMode,
			&item.AccountingVersion,
			&accountingValid,
			&item.AccountingQuality,
			&item.InputTokens,
			&item.OutputTokens,
			&item.NonReasoningOutputTokens,
			&item.CachedTokens,
			&item.CacheReadTokens,
			&item.CacheCreationTokens,
			&item.ReasoningTokens,
			&item.UnclassifiedTokens,
			&item.IncompleteAccounting,
			&item.TotalTokens,
			&item.LatencyMS,
			&item.TTFTMS,
			&failed,
			&item.FailStatusCode,
			&item.FailSummary,
			&responseMetadataJSON,
			&item.HeaderQuotaRecoverAtMS,
			&item.HeaderQuotaUsedPercent,
			&item.HeaderQuotaPlanType,
			&item.HeaderErrorKind,
			&item.HeaderErrorCode,
			&item.HeaderTraceID,
		); err != nil {
			return nil, err
		}
		item.Failed = failed != 0
		item.AccountingValid = accountingValid != 0
		item.ResponseMetadata = usage.ResponseHeaderMetadataFromJSON(responseMetadataJSON)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
