package usageevent

import (
	"context"
	"database/sql"
	"io"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

type Repository interface {
	InsertBatch(ctx context.Context, events []model.UsageEvent) (model.InsertResult, error)
	ListRecent(ctx context.Context, limit int) ([]model.UsageEvent, error)
	ModelUsageSummary(ctx context.Context, limit int) (model.ModelUsageSummary, error)
	BackfillResponseMetadata(ctx context.Context, batchLimit int) (int, error)
	Count(ctx context.Context) (int64, error)
	ExportJSONL(ctx context.Context) ([]byte, error)
	WriteCompatibleUsage(ctx context.Context, writer io.Writer, limit int) error
	WriteExportJSONL(ctx context.Context, writer io.Writer, limit int) error
	AggregateBetween(ctx context.Context, fromMs, toMs int64) (Aggregate, error)
	TopModelsBetween(ctx context.Context, fromMs, toMs int64, limit int) ([]ModelStat, error)
	ModelStatsBetween(ctx context.Context, fromMs, toMs int64) ([]ModelStat, error)
	RecentFailuresBetween(ctx context.Context, fromMs, toMs int64, limit int) ([]RecentFailure, error)
	HourlyTimelineBetween(ctx context.Context, fromMs, toMs int64) ([]TimelinePoint, error)
	BucketTimelineBetween(ctx context.Context, fromMs, toMs int64, bucketMs int64) ([]TimelinePoint, error)
	AggregateWithFilter(ctx context.Context, filter AnalyticsFilter) (Aggregate, error)
	ModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter, limit int) ([]ModelStat, error)
	TimelineWithFilter(ctx context.Context, filter AnalyticsFilter, granularity string, location *time.Location) ([]TimelinePoint, error)
	LatencyPercentilesWithFilter(ctx context.Context, filter AnalyticsFilter, granularity string, location *time.Location) ([]LatencyPercentiles, error)
	LatencySummaryWithFilter(ctx context.Context, filter AnalyticsFilter) (LatencySummary, error)
	HourlyDistributionWithFilter(ctx context.Context, filter AnalyticsFilter, location *time.Location) ([]HourlyPoint, error)
	FilterOptionValuesWithFilter(ctx context.Context, filter AnalyticsFilter) (FilterOptionValues, error)
	FilterSelectorValuesWithFilter(ctx context.Context, filter AnalyticsFilter) (FilterSelectorValues, error)
	HeatmapWithFilter(ctx context.Context, filter AnalyticsFilter, location *time.Location) ([]HeatmapPoint, error)
	ChannelModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]ChannelModelStat, error)
	FailureSourcesWithFilter(ctx context.Context, filter AnalyticsFilter) ([]FailureSourceStat, error)
	AccountModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]AccountModelStat, error)
	CredentialModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]CredentialModelStat, error)
	CredentialTimelineWithFilter(ctx context.Context, filter AnalyticsFilter, granularity string, location *time.Location) ([]CredentialTimelinePoint, error)
	APIKeyTimelineWithFilter(ctx context.Context, filter AnalyticsFilter, granularity string, location *time.Location) ([]APIKeyTimelinePoint, error)
	APIKeyModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]APIKeyModelStat, error)
	TaskBucketsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]TaskBucket, error)
	RecentFailuresWithFilter(ctx context.Context, filter AnalyticsFilter, limit int) ([]RecentFailure, error)
	EventsPageWithFilter(ctx context.Context, filter AnalyticsFilter, beforeMS int64, beforeID int64, limit int) (EventsPage, error)
	EventsCountWithFilter(ctx context.Context, filter AnalyticsFilter) (int64, error)
	LatestHeaderSnapshots(ctx context.Context, sinceMS int64, limit int) ([]HeaderSnapshot, error)
	ActiveDaysWithFilter(ctx context.Context, filter AnalyticsFilter, location *time.Location) (int64, error)
	ZeroTokenModelsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]string, error)
}

type repository struct {
	db *sql.DB
}

func New(db *sql.DB) Repository {
	return &repository{db: db}
}

func (r *repository) InsertBatch(ctx context.Context, events []model.UsageEvent) (model.InsertResult, error) {
	if len(events) == 0 {
		return model.InsertResult{}, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return model.InsertResult{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	ledgerStmt, err := tx.PrepareContext(ctx, `insert or ignore into usage_event_identity_ledger (
		event_hash,
		raw_event_id,
		timestamp_ms,
		bucket_ms,
		aggregate_schema_version,
		first_seen_at_ms,
		updated_at_ms
	) values (?, null, ?, ?, 0, ?, ?)`)
	if err != nil {
		return model.InsertResult{}, err
	}
	defer ledgerStmt.Close()

	attachLedgerStmt, err := tx.PrepareContext(ctx, `update usage_event_identity_ledger set
		raw_event_id = ?,
		timestamp_ms = ?,
		bucket_ms = ?,
		updated_at_ms = ?
	where event_hash = ?`)
	if err != nil {
		return model.InsertResult{}, err
	}
	defer attachLedgerStmt.Close()

	attachExistingLedgerStmt, err := tx.PrepareContext(ctx, `update usage_event_identity_ledger set
		raw_event_id = (select id from usage_events where event_hash = ?),
		timestamp_ms = (select timestamp_ms from usage_events where event_hash = ?),
		bucket_ms = (select timestamp_ms - (timestamp_ms % 3600000) from usage_events where event_hash = ?),
		first_seen_at_ms = coalesce((select case when created_at_ms > 0 then created_at_ms end from usage_events where event_hash = ?), first_seen_at_ms),
		updated_at_ms = ?
	where event_hash = ?`)
	if err != nil {
		return model.InsertResult{}, err
	}
	defer attachExistingLedgerStmt.Close()

	stmt, err := tx.PrepareContext(ctx, `insert or ignore into usage_events (
		request_id, event_hash, timestamp_ms, timestamp, provider, executor_type, model, endpoint, method, path,
		auth_type, auth_index, source, source_hash, api_key_hash,
		account_snapshot, auth_label_snapshot, auth_file_snapshot, auth_provider_snapshot, auth_project_id_snapshot, auth_snapshot_at_ms,
		requested_model, resolved_model, reasoning_effort, service_tier, request_service_tier, response_service_tier, cache_input_mode,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_tokens, cache_read_tokens, cache_creation_tokens,
		normalized_uncached_input_tokens, normalized_total_input_tokens, normalized_cache_read_tokens, normalized_cache_creation_tokens, total_tokens,
		latency_ms, ttft_ms, failed, fail_status_code, fail_summary,
		response_metadata_json, header_quota_recover_at_ms, header_quota_used_percent, header_quota_plan_type, header_error_kind, header_error_code, header_trace_id,
		fail_body, raw_json, created_at_ms
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return model.InsertResult{}, err
	}
	defer stmt.Close()

	result := model.InsertResult{}
	for _, event := range events {
		ledgerNowMS := event.CreatedAtMS
		if ledgerNowMS <= 0 {
			ledgerNowMS = time.Now().UnixMilli()
		}
		bucketMS := event.TimestampMS - event.TimestampMS%(60*60*1000)
		ledgerResult, err := ledgerStmt.ExecContext(
			ctx,
			event.EventHash,
			event.TimestampMS,
			bucketMS,
			ledgerNowMS,
			ledgerNowMS,
		)
		if err != nil {
			return model.InsertResult{}, err
		}
		claimed, _ := ledgerResult.RowsAffected()
		if claimed == 0 {
			result.Skipped++
			continue
		}

		accounting := usage.NormalizeCacheAccounting(usage.CacheInputContext{
			ExplicitMode:     event.CacheInputMode,
			ExecutorType:     event.ExecutorType,
			Provider:         event.Provider,
			ProviderSnapshot: event.AuthProviderSnapshot,
			ResolvedModel:    event.ResolvedModel,
			RequestedModel:   event.RequestedModel,
			DisplayModel:     event.Model,
		}, event.InputTokens, event.CachedTokens, event.CacheTokens, event.CacheReadTokens, event.CacheCreationTokens)
		event.CacheInputMode = accounting.Mode
		event.NormalizedUncachedInputTokens = accounting.UncachedInputTokens
		event.NormalizedTotalInputTokens = accounting.TotalInputTokens
		event.NormalizedCacheReadTokens = accounting.CacheReadTokens
		event.NormalizedCacheCreationTokens = accounting.CacheCreationTokens
		if event.TotalTokens <= 0 {
			event.TotalTokens = accounting.TotalInputTokens + max(event.OutputTokens, int64(0)) + max(event.ReasoningTokens, int64(0))
		}
		if event.RequestServiceTier == "" {
			event.RequestServiceTier = event.ServiceTier
		}
		event.ServiceTier = usage.EffectiveServiceTier(usage.CacheInputContext{
			ExecutorType:     event.ExecutorType,
			Provider:         event.Provider,
			ProviderSnapshot: event.AuthProviderSnapshot,
			AuthType:         event.AuthType,
		}, event.RequestServiceTier, event.ServiceTier, event.ResponseServiceTier)
		failed := 0
		if event.Failed {
			failed = 1
		}
		metadataJSON, quotaRecoverAtMS, quotaUsedPercent, quotaPlanType, errorKind, errorCode, traceID := responseHeaderDerivedForInsert(event)
		failSummarySource := event.FailSummary
		if failSummarySource == "" {
			failSummarySource = event.FailBody
		}
		failSummary := usage.FailSummaryFromBody(failSummarySource)
		rawJSON := usage.SafeRawJSON(event.RawJSON)
		res, err := stmt.ExecContext(
			ctx,
			nullString(event.RequestID),
			event.EventHash,
			event.TimestampMS,
			event.Timestamp,
			nullString(event.Provider),
			nullString(event.ExecutorType),
			event.Model,
			nullString(event.Endpoint),
			nullString(event.Method),
			nullString(event.Path),
			nullString(event.AuthType),
			nullString(event.AuthIndex),
			nullString(event.Source),
			nullString(event.SourceHash),
			nullString(event.APIKeyHash),
			nullString(event.AccountSnapshot),
			nullString(event.AuthLabelSnapshot),
			nullString(event.AuthFileSnapshot),
			nullString(event.AuthProviderSnapshot),
			nullString(event.AuthProjectIDSnapshot),
			nullPositiveInt64(event.AuthSnapshotAtMS),
			nullString(event.RequestedModel),
			nullString(event.ResolvedModel),
			nullString(event.ReasoningEffort),
			nullString(event.ServiceTier),
			nullString(event.RequestServiceTier),
			nullString(event.ResponseServiceTier),
			nullString(event.CacheInputMode),
			event.InputTokens,
			event.OutputTokens,
			event.ReasoningTokens,
			event.CachedTokens,
			event.CacheTokens,
			event.CacheReadTokens,
			event.CacheCreationTokens,
			event.NormalizedUncachedInputTokens,
			event.NormalizedTotalInputTokens,
			event.NormalizedCacheReadTokens,
			event.NormalizedCacheCreationTokens,
			event.TotalTokens,
			nullInt(event.LatencyMS),
			nullInt(event.TTFTMS),
			failed,
			nullPositiveInt64(int64(event.FailStatusCode)),
			nullString(failSummary),
			nullString(metadataJSON),
			nullPositiveInt64(quotaRecoverAtMS),
			nullFloat(quotaUsedPercent),
			nullString(quotaPlanType),
			nullString(errorKind),
			nullString(errorCode),
			nullString(traceID),
			nullString(event.FailBody),
			nullString(rawJSON),
			event.CreatedAtMS,
		)
		if err != nil {
			return model.InsertResult{}, err
		}
		affected, _ := res.RowsAffected()
		if affected > 0 {
			rawEventID, err := res.LastInsertId()
			if err != nil {
				return model.InsertResult{}, err
			}
			if _, err := attachLedgerStmt.ExecContext(
				ctx,
				rawEventID,
				event.TimestampMS,
				bucketMS,
				ledgerNowMS,
				event.EventHash,
			); err != nil {
				return model.InsertResult{}, err
			}
			result.Inserted++
			result.InsertedEventHashes = append(result.InsertedEventHashes, event.EventHash)
		} else {
			if _, err := attachExistingLedgerStmt.ExecContext(
				ctx,
				event.EventHash,
				event.EventHash,
				event.EventHash,
				event.EventHash,
				ledgerNowMS,
				event.EventHash,
			); err != nil {
				return model.InsertResult{}, err
			}
			result.Skipped++
		}
	}
	if err := tx.Commit(); err != nil {
		return model.InsertResult{}, err
	}
	return result, nil
}

func (r *repository) ListRecent(ctx context.Context, limit int) ([]model.UsageEvent, error) {
	if limit <= 0 {
		limit = 50000
	}
	rows, err := r.db.QueryContext(ctx, `select
		request_id, event_hash, timestamp_ms, timestamp, provider, executor_type, model, endpoint, method, path,
		auth_type, auth_index, source, source_hash, api_key_hash,
		account_snapshot, auth_label_snapshot, auth_file_snapshot, auth_provider_snapshot, auth_project_id_snapshot, auth_snapshot_at_ms,
		requested_model, resolved_model, reasoning_effort, service_tier, request_service_tier, response_service_tier, cache_input_mode,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_tokens, cache_read_tokens, cache_creation_tokens,
		normalized_uncached_input_tokens, normalized_total_input_tokens, normalized_cache_read_tokens, normalized_cache_creation_tokens, total_tokens,
		latency_ms, ttft_ms, failed, fail_status_code, fail_summary,
		coalesce(response_metadata_json, ''), header_quota_recover_at_ms, header_quota_used_percent, coalesce(header_quota_plan_type, ''), coalesce(header_error_kind, ''), coalesce(header_error_code, ''), coalesce(header_trace_id, ''),
		coalesce(raw_json, ''), created_at_ms
		from usage_events
		order by timestamp_ms desc, id desc
		limit ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]model.UsageEvent, 0)
	for rows.Next() {
		var event model.UsageEvent
		var requestID, provider, executorType, endpoint, method, path, authType, authIndex, source, sourceHash, apiKeyHash, accountSnapshot, authLabelSnapshot, authFileSnapshot, authProviderSnapshot, authProjectIDSnapshot, requestedModel, resolvedModel, reasoningEffort, serviceTier, requestServiceTier, responseServiceTier, cacheInputMode, failSummary sql.NullString
		var responseMetadataJSON, quotaPlanType, errorKind, errorCode, traceID, rawJSON string
		var authSnapshotAt sql.NullInt64
		var latency, ttft sql.NullInt64
		var failStatusCode sql.NullInt64
		var quotaRecoverAt sql.NullInt64
		var quotaUsedPercent sql.NullFloat64
		var normalizedUncachedInput, normalizedTotalInput, normalizedCacheRead, normalizedCacheCreation sql.NullInt64
		var failed int
		if err := rows.Scan(
			&requestID,
			&event.EventHash,
			&event.TimestampMS,
			&event.Timestamp,
			&provider,
			&executorType,
			&event.Model,
			&endpoint,
			&method,
			&path,
			&authType,
			&authIndex,
			&source,
			&sourceHash,
			&apiKeyHash,
			&accountSnapshot,
			&authLabelSnapshot,
			&authFileSnapshot,
			&authProviderSnapshot,
			&authProjectIDSnapshot,
			&authSnapshotAt,
			&requestedModel,
			&resolvedModel,
			&reasoningEffort,
			&serviceTier,
			&requestServiceTier,
			&responseServiceTier,
			&cacheInputMode,
			&event.InputTokens,
			&event.OutputTokens,
			&event.ReasoningTokens,
			&event.CachedTokens,
			&event.CacheTokens,
			&event.CacheReadTokens,
			&event.CacheCreationTokens,
			&normalizedUncachedInput,
			&normalizedTotalInput,
			&normalizedCacheRead,
			&normalizedCacheCreation,
			&event.TotalTokens,
			&latency,
			&ttft,
			&failed,
			&failStatusCode,
			&failSummary,
			&responseMetadataJSON,
			&quotaRecoverAt,
			&quotaUsedPercent,
			&quotaPlanType,
			&errorKind,
			&errorCode,
			&traceID,
			&rawJSON,
			&event.CreatedAtMS,
		); err != nil {
			return nil, err
		}
		event.RequestID = requestID.String
		event.Provider = provider.String
		event.ExecutorType = executorType.String
		event.Endpoint = endpoint.String
		event.Method = method.String
		event.Path = path.String
		event.AuthType = authType.String
		event.AuthIndex = authIndex.String
		event.Source = source.String
		event.SourceHash = sourceHash.String
		event.APIKeyHash = apiKeyHash.String
		event.AccountSnapshot = accountSnapshot.String
		event.AuthLabelSnapshot = authLabelSnapshot.String
		event.AuthFileSnapshot = authFileSnapshot.String
		event.AuthProviderSnapshot = authProviderSnapshot.String
		event.AuthProjectIDSnapshot = authProjectIDSnapshot.String
		event.RequestedModel = requestedModel.String
		event.ResolvedModel = resolvedModel.String
		event.ReasoningEffort = reasoningEffort.String
		event.ServiceTier = serviceTier.String
		event.RequestServiceTier = requestServiceTier.String
		event.ResponseServiceTier = responseServiceTier.String
		hints := usage.RawCacheAccountingHintsFromJSON(rawJSON)
		accounting := usage.NormalizeCacheAccounting(usage.CacheInputContext{
			ExplicitMode:     hints.ExplicitMode,
			ExecutorType:     event.ExecutorType,
			Provider:         event.Provider,
			ProviderSnapshot: event.AuthProviderSnapshot,
			ResolvedModel:    event.ResolvedModel,
			RequestedModel:   event.RequestedModel,
			DisplayModel:     event.Model,
		}, event.InputTokens, event.CachedTokens, event.CacheTokens, event.CacheReadTokens, event.CacheCreationTokens)
		event.CacheInputMode = accounting.Mode
		event.NormalizedUncachedInputTokens = accounting.UncachedInputTokens
		event.NormalizedTotalInputTokens = accounting.TotalInputTokens
		event.NormalizedCacheReadTokens = accounting.CacheReadTokens
		event.NormalizedCacheCreationTokens = accounting.CacheCreationTokens
		if normalizedUncachedInput.Valid {
			event.NormalizedUncachedInputTokens = normalizedUncachedInput.Int64
		}
		if normalizedTotalInput.Valid {
			event.NormalizedTotalInputTokens = normalizedTotalInput.Int64
		}
		if normalizedCacheRead.Valid {
			event.NormalizedCacheReadTokens = normalizedCacheRead.Int64
		}
		if normalizedCacheCreation.Valid {
			event.NormalizedCacheCreationTokens = normalizedCacheCreation.Int64
		}
		if authSnapshotAt.Valid {
			event.AuthSnapshotAtMS = authSnapshotAt.Int64
		}
		if failStatusCode.Valid {
			event.FailStatusCode = int(failStatusCode.Int64)
		}
		event.FailSummary = failSummary.String
		event.ResponseMetadataJSON = responseMetadataJSON
		event.ResponseMetadata = usage.ResponseHeaderMetadataFromJSON(responseMetadataJSON)
		if quotaRecoverAt.Valid {
			event.HeaderQuotaRecoverAtMS = quotaRecoverAt.Int64
		}
		if quotaUsedPercent.Valid {
			value := quotaUsedPercent.Float64
			event.HeaderQuotaUsedPercent = &value
		}
		event.HeaderQuotaPlanType = quotaPlanType
		event.HeaderErrorKind = errorKind
		event.HeaderErrorCode = errorCode
		event.HeaderTraceID = traceID
		event.Failed = failed != 0
		if latency.Valid {
			value := latency.Int64
			event.LatencyMS = &value
		}
		if ttft.Valid {
			value := ttft.Int64
			event.TTFTMS = &value
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (r *repository) Count(ctx context.Context) (int64, error) {
	var count int64
	if err := r.db.QueryRowContext(ctx, `select count(*) from usage_events`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func nullString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullPositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

func responseHeaderDerivedForInsert(event model.UsageEvent) (string, int64, *float64, string, string, string, string) {
	metadataJSON := event.ResponseMetadataJSON
	quotaRecoverAtMS := event.HeaderQuotaRecoverAtMS
	quotaUsedPercent := event.HeaderQuotaUsedPercent
	quotaPlanType := event.HeaderQuotaPlanType
	errorKind := event.HeaderErrorKind
	errorCode := event.HeaderErrorCode
	traceID := event.HeaderTraceID

	derived := usage.DeriveResponseHeaderMetadata(event.ResponseMetadata)
	if metadataJSON == "" {
		metadataJSON = derived.MetadataJSON
	}
	if quotaRecoverAtMS == 0 {
		quotaRecoverAtMS = derived.QuotaRecoverAtMS
	}
	if quotaUsedPercent == nil {
		quotaUsedPercent = derived.QuotaUsedPercent
	}
	if quotaPlanType == "" {
		quotaPlanType = derived.QuotaPlanType
	}
	if errorKind == "" {
		errorKind = derived.ErrorKind
	}
	if errorCode == "" {
		errorCode = derived.ErrorCode
	}
	if traceID == "" {
		traceID = derived.TraceID
	}
	return metadataJSON, quotaRecoverAtMS, quotaUsedPercent, quotaPlanType, errorKind, errorCode, traceID
}
