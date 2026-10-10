package usageevent

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

var ErrInvalidEventHash = usage.ErrInvalidEventHash

type Repository interface {
	InsertBatch(ctx context.Context, events []model.UsageEvent) (model.InsertResult, error)
	ExistingEventHashes(ctx context.Context, hashes []string) (map[string]struct{}, error)
	ResolveCodexLegacyAccountKey(ctx context.Context, fields usageidentity.Fields) (string, bool, error)
	ListRecent(ctx context.Context, limit int) ([]model.UsageEvent, error)
	ModelUsageSummary(ctx context.Context, limit int) (model.ModelUsageSummary, error)
	BackfillResponseMetadata(ctx context.Context, batchLimit int) (int, error)
	ResponseMetadataBackfillPending(ctx context.Context) (bool, error)
	Count(ctx context.Context) (int64, error)
	ExportJSONL(ctx context.Context) ([]byte, error)
	WriteCompatibleUsage(ctx context.Context, writer io.Writer, limit int) error
	WriteExportJSONL(ctx context.Context, writer io.Writer, limit int) error
	WriteFullExportJSONL(ctx context.Context, writer io.Writer) error
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
	AccountWindowModelStats(ctx context.Context, windows []AccountWindowUsageQuery) ([]AccountWindowModelStat, error)
	RecentAccountRequests(ctx context.Context, targets []LatestAccountRequestQuery, limit int) ([]LatestAccountRequest, error)
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

type preparedUsageEvent struct {
	event model.UsageEvent

	ledgerNowMS int64
	bucketMS    int64
	failed      int

	metadataJSON     string
	quotaRecoverAtMS int64
	quotaUsedPercent *float64
	quotaPlanType    string
	errorKind        string
	errorCode        string
	traceID          string

	failSummary string
	rawJSON     string
}

func (r *repository) queryExistingLedgerHashes(ctx context.Context, hashes []string) (map[string]struct{}, error) {
	if len(hashes) == 0 {
		return nil, nil
	}
	uniqueHashes := make([]string, 0, len(hashes))
	seen := make(map[string]struct{}, len(hashes))
	for _, h := range hashes {
		if _, ok := seen[h]; !ok {
			seen[h] = struct{}{}
			uniqueHashes = append(uniqueHashes, h)
		}
	}

	existing := make(map[string]struct{}, len(uniqueHashes))
	const chunkSize = 200
	for i := 0; i < len(uniqueHashes); i += chunkSize {
		end := i + chunkSize
		if end > len(uniqueHashes) {
			end = len(uniqueHashes)
		}
		chunk := uniqueHashes[i:end]

		placeholders := make([]string, len(chunk))
		args := make([]any, len(chunk))
		for idx, h := range chunk {
			placeholders[idx] = "?"
			args[idx] = h
		}
		query := `select event_hash from usage_event_identity_ledger where event_hash in (` + strings.Join(placeholders, ",") + `)`
		rows, err := r.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h string
			if err := rows.Scan(&h); err != nil {
				rows.Close()
				return nil, err
			}
			existing[h] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return existing, nil
}

func (r *repository) ExistingEventHashes(ctx context.Context, hashes []string) (map[string]struct{}, error) {
	if len(hashes) == 0 {
		return map[string]struct{}{}, nil
	}
	unique := make([]string, 0, len(hashes))
	seen := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		if _, ok := seen[hash]; ok {
			continue
		}
		seen[hash] = struct{}{}
		unique = append(unique, hash)
	}

	existing := make(map[string]struct{}, len(unique))
	const chunkSize = 200
	for start := 0; start < len(unique); start += chunkSize {
		end := start + chunkSize
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)*2)
		for i, hash := range chunk {
			placeholders[i] = "?"
			args = append(args, hash)
		}
		args = append(args, append([]any(nil), args...)...)
		inClause := strings.Join(placeholders, ",")
		rows, err := r.db.QueryContext(ctx, `
			select event_hash from usage_event_identity_ledger where event_hash in (`+inClause+`)
			union
			select event_hash from usage_events where event_hash in (`+inClause+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var hash string
			if err := rows.Scan(&hash); err != nil {
				rows.Close()
				return nil, err
			}
			existing[hash] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return existing, nil
}

func (r *repository) hashExistsInLedger(ctx context.Context, hash string) (bool, error) {
	var dummy int
	err := r.db.QueryRowContext(ctx, `select 1 from usage_event_identity_ledger where event_hash = ? limit 1`, hash).Scan(&dummy)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return false, nil
}

func (r *repository) hashExistsInRaw(ctx context.Context, hash string) (bool, error) {
	var dummy int
	err := r.db.QueryRowContext(ctx, `select 1 from usage_events where event_hash = ? limit 1`, hash).Scan(&dummy)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return false, nil
}

func (r *repository) prepareUsageEvent(rawEvent model.UsageEvent) (preparedUsageEvent, error) {
	event := usage.PrepareSensitiveFieldsForPersistence(rawEvent)
	usage.NormalizeRequestMetadata(&event)
	if event.PreserveArchiveDerivedFields {
		if err := usage.ValidateArchiveDerivedFields(event); err != nil {
			return preparedUsageEvent{}, fmt.Errorf("restore archive event %q: %w", event.EventHash, err)
		}
	} else {
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
	}
	failed := 0
	if event.Failed {
		failed = 1
	}
	metadataJSON, quotaRecoverAtMS, quotaUsedPercent, quotaPlanType, errorKind, errorCode, traceID := responseHeaderDerivedForInsert(event)
	failSummary := event.FailSummary
	rawJSON := event.RawJSON

	ledgerNowMS := event.CreatedAtMS
	if ledgerNowMS <= 0 {
		ledgerNowMS = time.Now().UnixMilli()
	}
	bucketMS := event.TimestampMS - event.TimestampMS%(60*60*1000)

	return preparedUsageEvent{
		event:            event,
		ledgerNowMS:      ledgerNowMS,
		bucketMS:         bucketMS,
		failed:           failed,
		metadataJSON:     metadataJSON,
		quotaRecoverAtMS: quotaRecoverAtMS,
		quotaUsedPercent: quotaUsedPercent,
		quotaPlanType:    quotaPlanType,
		errorKind:        errorKind,
		errorCode:        errorCode,
		traceID:          traceID,
		failSummary:      failSummary,
		rawJSON:          rawJSON,
	}, nil
}

func (r *repository) InsertBatch(ctx context.Context, events []model.UsageEvent) (model.InsertResult, error) {
	if len(events) == 0 {
		return model.InsertResult{}, nil
	}

	result := model.InsertResult{}

	// Phase 1: Identity validation & historical noncanonical check
	type eventCandidate struct {
		event       model.UsageEvent
		isDuplicate bool
	}
	candidates := make([]eventCandidate, len(events))
	canonicalHashesToCheck := make([]string, 0, len(events))

	for i, ev := range events {
		if usage.IsCanonicalSHA256Hex(ev.EventHash) {
			candidates[i] = eventCandidate{event: ev}
			canonicalHashesToCheck = append(canonicalHashesToCheck, ev.EventHash)
		} else {
			// Noncanonical hash: check if exists in DB as historical duplicate
			existsInLedger, err := r.hashExistsInLedger(ctx, ev.EventHash)
			if err != nil {
				return model.InsertResult{}, err
			}
			if existsInLedger {
				candidates[i] = eventCandidate{event: ev, isDuplicate: true}
				result.Skipped++
				continue
			}
			existsInRaw, err := r.hashExistsInRaw(ctx, ev.EventHash)
			if err != nil {
				return model.InsertResult{}, err
			}
			if existsInRaw {
				candidates[i] = eventCandidate{event: ev, isDuplicate: false}
			} else {
				return model.InsertResult{}, ErrInvalidEventHash
			}
		}
	}

	// Phase 2: Duplicate preflight for canonical hashes outside write transaction
	if len(canonicalHashesToCheck) > 0 {
		existingLedgerHashes, err := r.queryExistingLedgerHashes(ctx, canonicalHashesToCheck)
		if err != nil {
			return model.InsertResult{}, err
		}
		for i := range candidates {
			if candidates[i].isDuplicate {
				continue
			}
			if _, found := existingLedgerHashes[candidates[i].event.EventHash]; found {
				candidates[i].isDuplicate = true
				result.Skipped++
			}
		}
	}

	// Phase 3: Short-circuit if all events are duplicates (skip tx entirely)
	allDuplicates := true
	for _, c := range candidates {
		if !c.isDuplicate {
			allDuplicates = false
			break
		}
	}
	if allDuplicates {
		return result, nil
	}

	// Phase 4: Prepare non-duplicate events OUTSIDE write transaction
	preparedList := make([]preparedUsageEvent, 0, len(candidates))
	for _, c := range candidates {
		if c.isDuplicate {
			continue
		}
		prepared, err := r.prepareUsageEvent(c.event)
		if err != nil {
			return model.InsertResult{}, err
		}
		preparedList = append(preparedList, prepared)
	}

	// Phase 5: SQLite write transaction - strictly short atomic DB writes only
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
		client_ip, x_forwarded_for, user_agent,
		auth_type, auth_index, source, source_hash, api_key_hash,
		account_snapshot, auth_label_snapshot, auth_file_snapshot, auth_provider_snapshot, auth_account_id_snapshot, auth_project_id_snapshot, auth_snapshot_at_ms,
		requested_model, resolved_model, reasoning_effort, service_tier, request_service_tier, response_service_tier, cache_input_mode,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_tokens, cache_read_tokens, cache_creation_tokens,
		normalized_uncached_input_tokens, normalized_total_input_tokens, normalized_cache_read_tokens, normalized_cache_creation_tokens, total_tokens,
		latency_ms, ttft_ms, failed, fail_status_code, fail_summary,
		response_metadata_json, header_quota_recover_at_ms, header_quota_used_percent, header_quota_plan_type, header_error_kind, header_error_code, header_trace_id,
		fail_body, raw_json,
		response_model, session_id, parent_session_id, access_token_sha256, generate, stream,
		created_at_ms
		) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return model.InsertResult{}, err
	}
	defer stmt.Close()

	for _, prep := range preparedList {
		ev := prep.event
		ledgerResult, err := ledgerStmt.ExecContext(
			ctx,
			ev.EventHash,
			ev.TimestampMS,
			prep.bucketMS,
			prep.ledgerNowMS,
			prep.ledgerNowMS,
		)
		if err != nil {
			return model.InsertResult{}, err
		}
		claimed, _ := ledgerResult.RowsAffected()
		if claimed == 0 {
			result.Skipped++
			continue
		}

		res, err := stmt.ExecContext(
			ctx,
			nullString(ev.RequestID),
			ev.EventHash,
			ev.TimestampMS,
			ev.Timestamp,
			nullString(ev.Provider),
			nullString(ev.ExecutorType),
			ev.Model,
			nullString(ev.Endpoint),
			nullString(ev.Method),
			nullString(ev.Path),
			nullString(ev.ClientIP),
			nullString(ev.XForwardedFor),
			nullString(ev.UserAgent),
			nullString(ev.AuthType),
			nullString(ev.AuthIndex),
			nullString(ev.Source),
			nullString(ev.SourceHash),
			nullString(ev.APIKeyHash),
			nullString(ev.AccountSnapshot),
			nullString(ev.AuthLabelSnapshot),
			nullString(ev.AuthFileSnapshot),
			nullString(ev.AuthProviderSnapshot),
			nullString(ev.AuthAccountIDSnapshot),
			nullString(ev.AuthProjectIDSnapshot),
			nullPositiveInt64(ev.AuthSnapshotAtMS),
			nullString(ev.RequestedModel),
			nullString(ev.ResolvedModel),
			nullString(ev.ReasoningEffort),
			nullString(ev.ServiceTier),
			nullString(ev.RequestServiceTier),
			nullString(ev.ResponseServiceTier),
			nullString(ev.CacheInputMode),
			ev.InputTokens,
			ev.OutputTokens,
			ev.ReasoningTokens,
			ev.CachedTokens,
			ev.CacheTokens,
			ev.CacheReadTokens,
			ev.CacheCreationTokens,
			ev.NormalizedUncachedInputTokens,
			ev.NormalizedTotalInputTokens,
			ev.NormalizedCacheReadTokens,
			ev.NormalizedCacheCreationTokens,
			ev.TotalTokens,
			nullInt(ev.LatencyMS),
			nullInt(ev.TTFTMS),
			prep.failed,
			nullPositiveInt64(int64(ev.FailStatusCode)),
			nullString(prep.failSummary),
			nullString(prep.metadataJSON),
			nullPositiveInt64(prep.quotaRecoverAtMS),
			nullFloat(prep.quotaUsedPercent),
			nullString(prep.quotaPlanType),
			nullString(prep.errorKind),
			nullString(prep.errorCode),
			nullString(prep.traceID),
			nullString(ev.FailBody),
			nullString(prep.rawJSON),
			nullString(ev.ResponseModel),
			nullString(ev.SessionID),
			nullString(ev.ParentSessionID),
			nullString(ev.AccessTokenSHA256),
			nullBool(ev.Generate),
			nullBool(ev.Stream),
			ev.CreatedAtMS,
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
				ev.TimestampMS,
				prep.bucketMS,
				prep.ledgerNowMS,
				ev.EventHash,
			); err != nil {
				return model.InsertResult{}, err
			}
			result.Inserted++
			result.InsertedEventHashes = append(result.InsertedEventHashes, ev.EventHash)
		} else {
			if _, err := attachExistingLedgerStmt.ExecContext(
				ctx,
				ev.EventHash,
				ev.EventHash,
				ev.EventHash,
				ev.EventHash,
				prep.ledgerNowMS,
				ev.EventHash,
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
		client_ip, x_forwarded_for, user_agent,
		auth_type, auth_index, source, source_hash, api_key_hash,
		account_snapshot, auth_label_snapshot, auth_file_snapshot, auth_provider_snapshot, auth_account_id_snapshot, auth_project_id_snapshot, auth_snapshot_at_ms,
		requested_model, resolved_model, reasoning_effort, service_tier, request_service_tier, response_service_tier, cache_input_mode,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_tokens, cache_read_tokens, cache_creation_tokens,
		normalized_uncached_input_tokens, normalized_total_input_tokens, normalized_cache_read_tokens, normalized_cache_creation_tokens, total_tokens,
		latency_ms, ttft_ms, failed, fail_status_code, fail_summary,
		coalesce(response_metadata_json, ''), header_quota_recover_at_ms, header_quota_used_percent, coalesce(header_quota_plan_type, ''), coalesce(header_error_kind, ''), coalesce(header_error_code, ''), coalesce(header_trace_id, ''),
		coalesce(response_model, ''), coalesce(session_id, ''), coalesce(parent_session_id, ''), coalesce(access_token_sha256, ''),
		generate, stream,
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
		var requestID, provider, executorType, endpoint, method, path, clientIP, xForwardedFor, userAgent, authType, authIndex, source, sourceHash, apiKeyHash, accountSnapshot, authLabelSnapshot, authFileSnapshot, authProviderSnapshot, authAccountIDSnapshot, authProjectIDSnapshot, requestedModel, resolvedModel, reasoningEffort, serviceTier, requestServiceTier, responseServiceTier, cacheInputMode, failSummary sql.NullString
		var responseMetadataJSON, quotaPlanType, errorKind, errorCode, traceID, rawJSON string
		var responseModel, sessionID, parentSessionID, accessTokenSHA256 sql.NullString
		var generateVal, streamVal sql.NullInt64
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
			&clientIP,
			&xForwardedFor,
			&userAgent,
			&authType,
			&authIndex,
			&source,
			&sourceHash,
			&apiKeyHash,
			&accountSnapshot,
			&authLabelSnapshot,
			&authFileSnapshot,
			&authProviderSnapshot,
			&authAccountIDSnapshot,
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
			&responseModel,
			&sessionID,
			&parentSessionID,
			&accessTokenSHA256,
			&generateVal,
			&streamVal,
			&rawJSON,
			&event.CreatedAtMS,
		); err != nil {
			return nil, err
		}
		event.RequestID = requestID.String
		event.AnalyticsModel = usageidentity.AnalyticsModelForRequest(event.Model, requestedModel.String)
		event.Provider = provider.String
		event.ExecutorType = executorType.String
		event.Endpoint = endpoint.String
		event.Method = method.String
		event.Path = path.String
		event.ClientIP = clientIP.String
		event.XForwardedFor = xForwardedFor.String
		event.UserAgent = userAgent.String
		event.AuthType = authType.String
		event.AuthIndex = authIndex.String
		event.Source = source.String
		event.SourceHash = sourceHash.String
		event.APIKeyHash = apiKeyHash.String
		event.AccountSnapshot = accountSnapshot.String
		event.AuthLabelSnapshot = authLabelSnapshot.String
		event.AuthFileSnapshot = authFileSnapshot.String
		event.AuthProviderSnapshot = authProviderSnapshot.String
		event.AuthAccountIDSnapshot = authAccountIDSnapshot.String
		event.AuthProjectIDSnapshot = authProjectIDSnapshot.String
		event.RequestedModel = requestedModel.String
		event.ResolvedModel = resolvedModel.String
		event.ResponseModel = responseModel.String
		event.SessionID = sessionID.String
		event.ParentSessionID = parentSessionID.String
		event.AccessTokenSHA256 = accessTokenSHA256.String
		if generateVal.Valid {
			v := generateVal.Int64 != 0
			event.Generate = &v
		}
		if streamVal.Valid {
			v := streamVal.Int64 != 0
			event.Stream = &v
		}
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

func nullBool(value *bool) any {
	if value == nil {
		return nil
	}
	if *value {
		return 1
	}
	return 0
}

func responseHeaderDerivedForInsert(event model.UsageEvent) (string, int64, *float64, string, string, string, string) {
	metadataJSON := event.ResponseMetadataJSON
	quotaRecoverAtMS := event.HeaderQuotaRecoverAtMS
	quotaUsedPercent := event.HeaderQuotaUsedPercent
	quotaPlanType := event.HeaderQuotaPlanType
	errorKind := event.HeaderErrorKind
	errorCode := event.HeaderErrorCode
	traceID := event.HeaderTraceID
	if event.PreserveArchiveDerivedFields {
		return metadataJSON, quotaRecoverAtMS, quotaUsedPercent, quotaPlanType, errorKind, errorCode, traceID
	}

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
