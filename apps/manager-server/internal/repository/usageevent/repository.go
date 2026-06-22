package usageevent

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

type Repository interface {
	InsertBatch(ctx context.Context, events []model.UsageEvent) (model.InsertResult, error)
	ListRecent(ctx context.Context, limit int) ([]model.UsageEvent, error)
	Count(ctx context.Context) (int64, error)
	ExportJSONL(ctx context.Context) ([]byte, error)
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
	HeatmapWithFilter(ctx context.Context, filter AnalyticsFilter, location *time.Location) ([]HeatmapPoint, error)
	PerformanceHeatmapWithFilter(ctx context.Context, filter AnalyticsFilter, location *time.Location) ([]PerformanceHeatmapPoint, error)
	ChannelModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]ChannelModelStat, error)
	FailureSourcesWithFilter(ctx context.Context, filter AnalyticsFilter) ([]FailureSourceStat, error)
	AccountModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]AccountModelStat, error)
	CredentialModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]CredentialModelStat, error)
	CredentialTimelineWithFilter(ctx context.Context, filter AnalyticsFilter, granularity string, location *time.Location) ([]CredentialTimelinePoint, error)
	APIKeyModelStatsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]APIKeyModelStat, error)
	TaskBucketsWithFilter(ctx context.Context, filter AnalyticsFilter) ([]TaskBucket, error)
	RecentFailuresWithFilter(ctx context.Context, filter AnalyticsFilter, limit int) ([]RecentFailure, error)
	EventsPageWithFilter(ctx context.Context, filter AnalyticsFilter, beforeMS int64, beforeID int64, limit int) (EventsPage, error)
	EventsCountWithFilter(ctx context.Context, filter AnalyticsFilter) (int64, error)
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

	stmt, err := tx.PrepareContext(ctx, `insert or ignore into usage_events (
		request_id, event_hash, timestamp_ms, timestamp, provider, executor_type, model, endpoint, method, path,
		auth_type, auth_index, source, source_hash, api_key_hash,
		account_snapshot, auth_label_snapshot, auth_file_snapshot, auth_provider_snapshot, auth_project_id_snapshot, auth_snapshot_at_ms,
		requested_model, resolved_model, reasoning_effort, service_tier,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_tokens, cache_read_tokens, cache_creation_tokens, total_tokens,
		latency_ms, ttft_ms, failed, fail_status_code, fail_summary, fail_body, raw_json, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return model.InsertResult{}, err
	}
	defer stmt.Close()

	result := model.InsertResult{}
	for _, event := range events {
		failed := 0
		if event.Failed {
			failed = 1
		}
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
			event.InputTokens,
			event.OutputTokens,
			event.ReasoningTokens,
			event.CachedTokens,
			event.CacheTokens,
			event.CacheReadTokens,
			event.CacheCreationTokens,
			event.TotalTokens,
			nullInt(event.LatencyMS),
			nullInt(event.TTFTMS),
			failed,
			nullPositiveInt64(int64(event.FailStatusCode)),
			nullString(failSummary),
			nullString(event.FailBody),
			nullString(rawJSON),
			event.CreatedAtMS,
		)
		if err != nil {
			return model.InsertResult{}, err
		}
		affected, _ := res.RowsAffected()
		if affected > 0 {
			result.Inserted++
			result.InsertedEventHashes = append(result.InsertedEventHashes, event.EventHash)
		} else {
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
		requested_model, resolved_model, reasoning_effort, service_tier,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_tokens, cache_read_tokens, cache_creation_tokens, total_tokens,
		latency_ms, ttft_ms, failed, fail_status_code, fail_summary, created_at_ms
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
		var requestID, provider, executorType, endpoint, method, path, authType, authIndex, source, sourceHash, apiKeyHash, accountSnapshot, authLabelSnapshot, authFileSnapshot, authProviderSnapshot, authProjectIDSnapshot, requestedModel, resolvedModel, reasoningEffort, serviceTier, failSummary sql.NullString
		var authSnapshotAt sql.NullInt64
		var latency, ttft sql.NullInt64
		var failStatusCode sql.NullInt64
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
			&event.InputTokens,
			&event.OutputTokens,
			&event.ReasoningTokens,
			&event.CachedTokens,
			&event.CacheTokens,
			&event.CacheReadTokens,
			&event.CacheCreationTokens,
			&event.TotalTokens,
			&latency,
			&ttft,
			&failed,
			&failStatusCode,
			&failSummary,
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
		if authSnapshotAt.Valid {
			event.AuthSnapshotAtMS = authSnapshotAt.Int64
		}
		if failStatusCode.Valid {
			event.FailStatusCode = int(failStatusCode.Int64)
		}
		event.FailSummary = failSummary.String
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

func (r *repository) ExportJSONL(ctx context.Context) ([]byte, error) {
	events, err := r.ListRecent(ctx, 0)
	if err != nil {
		return nil, err
	}
	output := make([]byte, 0)
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		// Export intentionally omits raw_json and raw fail_body. fail_summary is
		// the redacted/truncated diagnostic field intended for portable JSONL.
		event.FailBody = ""
		event.RawJSON = ""
		line, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		output = append(output, line...)
		output = append(output, '\n')
	}
	return output, nil
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

func nullPositiveInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}
