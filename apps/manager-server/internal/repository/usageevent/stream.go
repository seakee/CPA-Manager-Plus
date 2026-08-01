package usageevent

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const (
	defaultUsageStreamLimit = 50000
	usageStreamBufferSize   = 64 * 1024
	usageExportBatchSize    = 512
)

type usageSnapshot struct {
	maxID             int64
	cutoffTimestampMS int64
	cutoffID          int64
	empty             bool
}

type compatibleUsageTotals struct {
	totalRequests int64
	successCount  int64
	failureCount  int64
	totalTokens   int64
}

type rawMetadataDetail struct {
	usage.Detail
	ResponseMetadata json.RawMessage `json:"response_metadata,omitempty"`
}

type exportRow struct {
	id               int64
	timestampMS      int64
	event            model.UsageEvent
	responseMetadata json.RawMessage
}

type rawMetadataEvent struct {
	usage.Event
	ResponseMetadata json.RawMessage `json:"response_metadata,omitempty"`
}

func (r *repository) WriteCompatibleUsage(ctx context.Context, writer io.Writer, limit int) error {
	limit = normalizeUsageStreamLimit(limit)
	snapshot, err := r.captureUsageSnapshot(ctx, limit)
	if err != nil {
		return err
	}
	totals, err := r.compatibleUsageTotals(ctx, snapshot)
	if err != nil {
		return err
	}

	buffer := bufio.NewWriterSize(writer, usageStreamBufferSize)
	if err := writeCompatibleUsageHeader(buffer, totals); err != nil {
		return err
	}
	if snapshot.empty {
		if _, err := io.WriteString(buffer, "}}\n"); err != nil {
			return err
		}
		return buffer.Flush()
	}

	rows, err := r.db.QueryContext(ctx, `select
		coalesce(nullif(endpoint, ''), '-') as group_endpoint,
		coalesce(nullif(model, ''), '-') as group_model,
		timestamp,
		coalesce(source, ''),
		coalesce(auth_index, ''),
		coalesce(api_key_hash, ''),
		coalesce(account_snapshot, ''),
		coalesce(auth_label_snapshot, ''),
		coalesce(auth_file_snapshot, ''),
		coalesce(auth_provider_snapshot, ''),
		coalesce(auth_project_id_snapshot, ''),
		auth_snapshot_at_ms,
		latency_ms,
		ttft_ms,
		coalesce(resolved_model, ''),
		coalesce(reasoning_effort, ''),
		coalesce(service_tier, ''),
		coalesce(request_service_tier, ''),
		coalesce(response_service_tier, ''),
		coalesce(cache_input_mode, ''),
		coalesce(executor_type, ''),
		input_tokens,
		output_tokens,
		reasoning_tokens,
		cached_tokens,
		cache_tokens,
		cache_read_tokens,
		cache_creation_tokens,
		total_tokens,
		failed,
		fail_status_code,
		coalesce(fail_summary, ''),
		coalesce(response_metadata_json, '')
	from usage_events
	where id <= ? and (
		timestamp_ms > ? or (timestamp_ms = ? and id >= ?)
	)
	order by group_endpoint asc, group_model asc, timestamp_ms desc, id desc`,
		snapshot.maxID,
		snapshot.cutoffTimestampMS,
		snapshot.cutoffTimestampMS,
		snapshot.cutoffID,
	)
	if err != nil {
		return err
	}
	defer rows.Close()

	var currentEndpoint string
	var currentModel string
	endpointOpen := false
	modelOpen := false
	firstEndpoint := true
	firstModel := true
	firstDetail := true

	for rows.Next() {
		endpoint, modelName, detail, err := scanCompatibleDetail(rows)
		if err != nil {
			return err
		}

		if !endpointOpen || endpoint != currentEndpoint {
			if modelOpen {
				if _, err := io.WriteString(buffer, "]}"); err != nil {
					return err
				}
				modelOpen = false
			}
			if endpointOpen {
				if _, err := io.WriteString(buffer, "}}"); err != nil {
					return err
				}
			}
			if !firstEndpoint {
				if err := buffer.WriteByte(','); err != nil {
					return err
				}
			}
			if err := writeJSONString(buffer, endpoint); err != nil {
				return err
			}
			if _, err := io.WriteString(buffer, `:{"models":{`); err != nil {
				return err
			}
			currentEndpoint = endpoint
			currentModel = ""
			endpointOpen = true
			firstEndpoint = false
			firstModel = true
		}

		if !modelOpen || modelName != currentModel {
			if modelOpen {
				if _, err := io.WriteString(buffer, "]}"); err != nil {
					return err
				}
			}
			if !firstModel {
				if err := buffer.WriteByte(','); err != nil {
					return err
				}
			}
			if err := writeJSONString(buffer, modelName); err != nil {
				return err
			}
			if _, err := io.WriteString(buffer, `:{"details":[`); err != nil {
				return err
			}
			currentModel = modelName
			modelOpen = true
			firstModel = false
			firstDetail = true
		}

		if !firstDetail {
			if err := buffer.WriteByte(','); err != nil {
				return err
			}
		}
		encoded, err := json.Marshal(detail)
		if err != nil {
			return err
		}
		if _, err := buffer.Write(encoded); err != nil {
			return err
		}
		firstDetail = false
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if modelOpen {
		if _, err := io.WriteString(buffer, "]}"); err != nil {
			return err
		}
	}
	if endpointOpen {
		if _, err := io.WriteString(buffer, "}}"); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(buffer, "}}\n"); err != nil {
		return err
	}
	return buffer.Flush()
}

func (r *repository) WriteExportJSONL(ctx context.Context, writer io.Writer, limit int) error {
	limit = normalizeUsageStreamLimit(limit)
	snapshot, err := r.captureUsageSnapshot(ctx, limit)
	if err != nil {
		return err
	}
	if snapshot.empty {
		return nil
	}

	buffer := bufio.NewWriterSize(writer, usageStreamBufferSize)
	cursorTimestampMS := snapshot.cutoffTimestampMS
	cursorID := snapshot.cutoffID - 1
	for {
		batch, err := r.exportBatch(ctx, snapshot, cursorTimestampMS, cursorID)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			break
		}
		for _, row := range batch {
			encoded, err := json.Marshal(rawMetadataEvent{
				Event:            row.event,
				ResponseMetadata: row.responseMetadata,
			})
			if err != nil {
				return err
			}
			if _, err := buffer.Write(encoded); err != nil {
				return err
			}
			if err := buffer.WriteByte('\n'); err != nil {
				return err
			}
		}
		last := batch[len(batch)-1]
		cursorTimestampMS = last.timestampMS
		cursorID = last.id
		if len(batch) < usageExportBatchSize {
			break
		}
	}
	return buffer.Flush()
}

func (r *repository) ExportJSONL(ctx context.Context) ([]byte, error) {
	var output bytes.Buffer
	if err := r.WriteExportJSONL(ctx, &output, defaultUsageStreamLimit); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (r *repository) captureUsageSnapshot(ctx context.Context, limit int) (usageSnapshot, error) {
	var snapshot usageSnapshot
	if err := r.db.QueryRowContext(ctx, `select coalesce(max(id), 0) from usage_events`).Scan(&snapshot.maxID); err != nil {
		return usageSnapshot{}, err
	}
	if snapshot.maxID == 0 {
		snapshot.empty = true
		return snapshot, nil
	}

	err := r.db.QueryRowContext(ctx, `select timestamp_ms, id
	from (
		select timestamp_ms, id
		from usage_events
		where id <= ?
		order by timestamp_ms desc, id desc
		limit ?
	)
	order by timestamp_ms asc, id asc
	limit 1`, snapshot.maxID, limit).Scan(&snapshot.cutoffTimestampMS, &snapshot.cutoffID)
	if errors.Is(err, sql.ErrNoRows) {
		snapshot.empty = true
		return snapshot, nil
	}
	if err != nil {
		return usageSnapshot{}, err
	}
	return snapshot, nil
}

func (r *repository) compatibleUsageTotals(ctx context.Context, snapshot usageSnapshot) (compatibleUsageTotals, error) {
	if snapshot.empty {
		return compatibleUsageTotals{}, nil
	}
	var totals compatibleUsageTotals
	if err := r.db.QueryRowContext(ctx, `select
		count(*),
		count(*) - coalesce(sum(case when failed <> 0 then 1 else 0 end), 0),
		coalesce(sum(case when failed <> 0 then 1 else 0 end), 0),
		coalesce(sum(total_tokens), 0)
	from usage_events
	where id <= ? and (
		timestamp_ms > ? or (timestamp_ms = ? and id >= ?)
	)`,
		snapshot.maxID,
		snapshot.cutoffTimestampMS,
		snapshot.cutoffTimestampMS,
		snapshot.cutoffID,
	).Scan(&totals.totalRequests, &totals.successCount, &totals.failureCount, &totals.totalTokens); err != nil {
		return compatibleUsageTotals{}, err
	}
	return totals, nil
}

func (r *repository) exportBatch(ctx context.Context, snapshot usageSnapshot, cursorTimestampMS, cursorID int64) ([]exportRow, error) {
	rows, err := r.db.QueryContext(ctx, `select
		id,
		request_id, event_hash, timestamp_ms, timestamp, provider, executor_type, model, endpoint, method, path,
		auth_type, auth_index, source, source_hash, api_key_hash,
		account_snapshot, auth_label_snapshot, auth_file_snapshot, auth_provider_snapshot, auth_project_id_snapshot, auth_snapshot_at_ms,
		requested_model, resolved_model, reasoning_effort, service_tier,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_tokens, cache_read_tokens, cache_creation_tokens, total_tokens,
		latency_ms, ttft_ms, failed, fail_status_code, fail_summary,
		coalesce(response_metadata_json, ''), header_quota_recover_at_ms, header_quota_used_percent, coalesce(header_quota_plan_type, ''), coalesce(header_error_kind, ''), coalesce(header_error_code, ''), coalesce(header_trace_id, ''),
		created_at_ms
	from usage_events
	where id <= ?
		and (timestamp_ms > ? or (timestamp_ms = ? and id >= ?))
		and (timestamp_ms > ? or (timestamp_ms = ? and id > ?))
	order by timestamp_ms asc, id asc
	limit ?`,
		snapshot.maxID,
		snapshot.cutoffTimestampMS,
		snapshot.cutoffTimestampMS,
		snapshot.cutoffID,
		cursorTimestampMS,
		cursorTimestampMS,
		cursorID,
		usageExportBatchSize,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	batch := make([]exportRow, 0, usageExportBatchSize)
	for rows.Next() {
		row, err := scanExportRow(rows)
		if err != nil {
			return nil, err
		}
		batch = append(batch, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return batch, nil
}

func scanCompatibleDetail(rows *sql.Rows) (string, string, rawMetadataDetail, error) {
	var endpoint string
	var modelName string
	var detail usage.Detail
	var authSnapshotAt sql.NullInt64
	var latency sql.NullInt64
	var ttft sql.NullInt64
	var failStatusCode sql.NullInt64
	var responseMetadataJSON string
	var failed int
	var cachedTokens int64
	var cacheTokens int64

	err := rows.Scan(
		&endpoint,
		&modelName,
		&detail.Timestamp,
		&detail.Source,
		&detail.AuthIndex,
		&detail.APIKeyHash,
		&detail.AccountSnapshot,
		&detail.AuthLabelSnapshot,
		&detail.AuthFileSnapshot,
		&detail.AuthProviderSnapshot,
		&detail.AuthProjectIDSnapshot,
		&authSnapshotAt,
		&latency,
		&ttft,
		&detail.ResolvedModel,
		&detail.ReasoningEffort,
		&detail.ServiceTier,
		&detail.RequestServiceTier,
		&detail.ResponseServiceTier,
		&detail.CacheInputMode,
		&detail.ExecutorType,
		&detail.Tokens.InputTokens,
		&detail.Tokens.OutputTokens,
		&detail.Tokens.ReasoningTokens,
		&cachedTokens,
		&cacheTokens,
		&detail.Tokens.CacheReadTokens,
		&detail.Tokens.CacheCreationTokens,
		&detail.Tokens.TotalTokens,
		&failed,
		&failStatusCode,
		&detail.FailSummary,
		&responseMetadataJSON,
	)
	if err != nil {
		return "", "", rawMetadataDetail{}, err
	}
	if authSnapshotAt.Valid {
		detail.AuthSnapshotAtMS = authSnapshotAt.Int64
	}
	if latency.Valid {
		value := latency.Int64
		detail.LatencyMS = &value
	}
	if ttft.Valid {
		value := ttft.Int64
		detail.TTFTMS = &value
	}
	if failStatusCode.Valid {
		detail.FailStatusCode = int(failStatusCode.Int64)
	}
	detail.Failed = failed != 0
	compatibleCachedTokens := usage.CompatibleCachedTokens(
		cachedTokens,
		cacheTokens,
		detail.Tokens.CacheReadTokens,
		detail.Tokens.CacheCreationTokens,
	)
	detail.Tokens.CachedTokens = compatibleCachedTokens
	detail.Tokens.CacheTokens = compatibleCachedTokens
	return endpoint, modelName, rawMetadataDetail{
		Detail:           detail,
		ResponseMetadata: validatedMetadataJSON(responseMetadataJSON),
	}, nil
}

func scanExportRow(rows *sql.Rows) (exportRow, error) {
	var row exportRow
	event := &row.event
	var requestID, provider, executorType, endpoint, method, path, authType, authIndex, source, sourceHash, apiKeyHash, accountSnapshot, authLabelSnapshot, authFileSnapshot, authProviderSnapshot, authProjectIDSnapshot, requestedModel, resolvedModel, reasoningEffort, serviceTier, failSummary sql.NullString
	var responseMetadataJSON, quotaPlanType, errorKind, errorCode, traceID string
	var authSnapshotAt sql.NullInt64
	var latency, ttft sql.NullInt64
	var failStatusCode sql.NullInt64
	var quotaRecoverAt sql.NullInt64
	var quotaUsedPercent sql.NullFloat64
	var failed int
	if err := rows.Scan(
		&row.id,
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
		&responseMetadataJSON,
		&quotaRecoverAt,
		&quotaUsedPercent,
		&quotaPlanType,
		&errorKind,
		&errorCode,
		&traceID,
		&event.CreatedAtMS,
	); err != nil {
		return exportRow{}, err
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
	event.FailSummary = failSummary.String
	event.HeaderQuotaPlanType = quotaPlanType
	event.HeaderErrorKind = errorKind
	event.HeaderErrorCode = errorCode
	event.HeaderTraceID = traceID
	event.Failed = failed != 0
	if authSnapshotAt.Valid {
		event.AuthSnapshotAtMS = authSnapshotAt.Int64
	}
	if latency.Valid {
		value := latency.Int64
		event.LatencyMS = &value
	}
	if ttft.Valid {
		value := ttft.Int64
		event.TTFTMS = &value
	}
	if failStatusCode.Valid {
		event.FailStatusCode = int(failStatusCode.Int64)
	}
	if quotaRecoverAt.Valid {
		event.HeaderQuotaRecoverAtMS = quotaRecoverAt.Int64
	}
	if quotaUsedPercent.Valid {
		value := quotaUsedPercent.Float64
		event.HeaderQuotaUsedPercent = &value
	}
	row.timestampMS = event.TimestampMS
	row.responseMetadata = validatedMetadataJSON(responseMetadataJSON)
	return row, nil
}

func validatedMetadataJSON(raw string) json.RawMessage {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil
	}
	metadata := json.RawMessage(trimmed)
	if !json.Valid(metadata) {
		return nil
	}
	return metadata
}

func normalizeUsageStreamLimit(limit int) int {
	if limit <= 0 {
		return defaultUsageStreamLimit
	}
	return limit
}

func writeCompatibleUsageHeader(writer io.Writer, totals compatibleUsageTotals) error {
	_, err := io.WriteString(writer,
		`{"total_requests":`+int64String(totals.totalRequests)+
			`,"success_count":`+int64String(totals.successCount)+
			`,"failure_count":`+int64String(totals.failureCount)+
			`,"total_tokens":`+int64String(totals.totalTokens)+
			`,"apis":{`,
	)
	return err
}

func int64String(value int64) string {
	return strconv.FormatInt(value, 10)
}

func writeJSONString(writer io.Writer, value string) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = writer.Write(encoded)
	return err
}
