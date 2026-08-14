package usageevent

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageaccountingsql"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const (
	defaultUsageStreamLimit        = 50000
	maxCompatibleUsageStreamLimit  = 50000
	usageStreamBufferSize          = 64 * 1024
	usageExportBatchSize           = 512
	compatibleUsageDetailBatchSize = 1024
)

var streamAccountingSQL = usageaccountingsql.For("")

var compatibleUsageOrderedIDsQuery = `select id
	from usage_events
	where id <= ? and (
		timestamp_ms > ? or (timestamp_ms = ? and id >= ?)
	)
	order by
		coalesce(nullif(endpoint, ''), '-') asc,
			` + compatibleUsageAnalyticsModelExpression + ` asc,
		timestamp_ms desc,
		id desc`

var compatibleUsageDetailQueryPrefix = `select
		id,
		coalesce(nullif(endpoint, ''), '-') as group_endpoint,
			` + compatibleUsageAnalyticsModelExpression + ` as group_model,
		coalesce(provider, ''),
		coalesce(auth_type, ''),
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
			coalesce(nullif(requested_model, ''), model, ''),
			coalesce(resolved_model, ''),
		coalesce(reasoning_effort, ''),
		coalesce(service_tier, ''),
		coalesce(request_service_tier, ''),
		coalesce(response_service_tier, ''),
		coalesce(cache_input_mode, ''),
		coalesce(executor_type, ''),
		coalesce(accounting_version, 0),
		coalesce(accounting_valid, 0),
		coalesce(accounting_quality, ''),
		input_tokens,
		output_tokens,
		reasoning_tokens,
		cached_tokens,
		cache_tokens,
		cache_read_tokens,
		cache_creation_tokens,
		normalized_uncached_input_tokens,
		normalized_total_input_tokens,
		normalized_cache_read_tokens,
		normalized_cache_creation_tokens,
		normalized_non_reasoning_output_tokens,
		normalized_reasoning_output_tokens,
		normalized_total_output_tokens,
		unclassified_tokens,
		total_tokens,
		coalesce(raw_json, ''),
		failed,
		fail_status_code,
		coalesce(fail_summary, ''),
		coalesce(response_metadata_json, '')
	from usage_events
		where id in (`

var compatibleUsageAnalyticsModelExpression = "coalesce(nullif(" + usageidentity.SQLRequestAnalyticsModelExpression("model", "requested_model") + ", ''), '-')"

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

type compatibleExportRow struct {
	id       int64
	endpoint string
	model    string
	detail   rawMetadataDetail
}

type compatibleStreamState struct {
	currentEndpoint string
	currentModel    string
	endpointOpen    bool
	modelOpen       bool
	firstEndpoint   bool
	firstModel      bool
	firstDetail     bool
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
	limit = normalizeCompatibleUsageStreamLimit(limit)
	snapshot, err := r.captureUsageSnapshot(ctx, limit)
	if err != nil {
		return err
	}
	totals, err := r.compatibleUsageTotals(ctx, snapshot)
	if err != nil {
		return err
	}

	var orderedIDs []int64
	if !snapshot.empty {
		orderedIDs, err = r.compatibleOrderedIDs(ctx, snapshot, int(totals.totalRequests))
		if err != nil {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
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

	state := newCompatibleStreamState()
	for start := 0; start < len(orderedIDs); start += compatibleUsageDetailBatchSize {
		if err := ctx.Err(); err != nil {
			return err
		}
		end := min(start+compatibleUsageDetailBatchSize, len(orderedIDs))
		batchIDs := orderedIDs[start:end]
		rowsByID, err := r.compatibleRowsByIDs(ctx, batchIDs)
		if err != nil {
			return err
		}

		// Do not write to the HTTP writer while sql.Rows is still active.
		// Network backpressure across an open SQLite reader can pin the WAL.
		if err := writeCompatibleRows(ctx, buffer, batchIDs, rowsByID, &state); err != nil {
			return err
		}
	}
	if err := finishCompatibleStream(buffer, &state); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return buffer.Flush()
}

func (r *repository) compatibleOrderedIDs(ctx context.Context, snapshot usageSnapshot, expectedCount int) ([]int64, error) {
	rows, err := r.db.QueryContext(
		ctx,
		compatibleUsageOrderedIDsQuery,
		snapshot.maxID,
		snapshot.cutoffTimestampMS,
		snapshot.cutoffTimestampMS,
		snapshot.cutoffID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	orderedIDs := make([]int64, 0, expectedCount)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		orderedIDs = append(orderedIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return orderedIDs, nil
}

func (r *repository) compatibleRowsByIDs(ctx context.Context, ids []int64) (map[int64]compatibleExportRow, error) {
	if len(ids) == 0 {
		return map[int64]compatibleExportRow{}, nil
	}

	args := make([]any, len(ids))
	for index, id := range ids {
		args[index] = id
	}
	rows, err := r.db.QueryContext(ctx, compatibleUsageDetailQuery(len(ids)), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	rowsByID := make(map[int64]compatibleExportRow, len(ids))
	for rows.Next() {
		row, err := scanCompatibleDetail(rows)
		if err != nil {
			return nil, err
		}
		rowsByID[row.id] = row
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	for _, id := range ids {
		if _, ok := rowsByID[id]; !ok {
			return nil, fmt.Errorf("compatible usage snapshot row disappeared: id=%d", id)
		}
	}
	return rowsByID, nil
}

func compatibleUsageDetailQuery(count int) string {
	return compatibleUsageDetailQueryPrefix + strings.TrimSuffix(strings.Repeat("?,", count), ",") + ")"
}

func newCompatibleStreamState() compatibleStreamState {
	return compatibleStreamState{
		firstEndpoint: true,
		firstModel:    true,
		firstDetail:   true,
	}
}

func writeCompatibleRows(ctx context.Context, buffer *bufio.Writer, orderedIDs []int64, rowsByID map[int64]compatibleExportRow, state *compatibleStreamState) error {
	for _, id := range orderedIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, ok := rowsByID[id]
		if !ok {
			return fmt.Errorf("compatible usage snapshot row disappeared: id=%d", id)
		}

		if !state.endpointOpen || row.endpoint != state.currentEndpoint {
			if state.modelOpen {
				if _, err := io.WriteString(buffer, "]}"); err != nil {
					return err
				}
				state.modelOpen = false
			}
			if state.endpointOpen {
				if _, err := io.WriteString(buffer, "}}"); err != nil {
					return err
				}
			}
			if !state.firstEndpoint {
				if err := buffer.WriteByte(','); err != nil {
					return err
				}
			}
			if err := writeJSONString(buffer, row.endpoint); err != nil {
				return err
			}
			if _, err := io.WriteString(buffer, `:{"models":{`); err != nil {
				return err
			}
			state.currentEndpoint = row.endpoint
			state.currentModel = ""
			state.endpointOpen = true
			state.firstEndpoint = false
			state.firstModel = true
		}

		if !state.modelOpen || row.model != state.currentModel {
			if state.modelOpen {
				if _, err := io.WriteString(buffer, "]}"); err != nil {
					return err
				}
			}
			if !state.firstModel {
				if err := buffer.WriteByte(','); err != nil {
					return err
				}
			}
			if err := writeJSONString(buffer, row.model); err != nil {
				return err
			}
			if _, err := io.WriteString(buffer, `:{"details":[`); err != nil {
				return err
			}
			state.currentModel = row.model
			state.modelOpen = true
			state.firstModel = false
			state.firstDetail = true
		}

		if !state.firstDetail {
			if err := buffer.WriteByte(','); err != nil {
				return err
			}
		}
		encoded, err := json.Marshal(row.detail)
		if err != nil {
			return err
		}
		if _, err := buffer.Write(encoded); err != nil {
			return err
		}
		state.firstDetail = false
	}
	return nil
}

func finishCompatibleStream(buffer *bufio.Writer, state *compatibleStreamState) error {
	if state.modelOpen {
		if _, err := io.WriteString(buffer, "]}"); err != nil {
			return err
		}
	}
	if state.endpointOpen {
		if _, err := io.WriteString(buffer, "}}"); err != nil {
			return err
		}
	}
	_, err := io.WriteString(buffer, "}}\n")
	return err
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
	rows, err := r.db.QueryContext(ctx, `select
		failed,
		`+streamAccountingSQL.Ready+`,
		`+streamAccountingSQL.Total+`,
		coalesce(accounting_version, 0),
		coalesce(accounting_valid, 0),
		coalesce(accounting_quality, ''),
		coalesce(provider, ''),
		coalesce(executor_type, ''),
		coalesce(auth_provider_snapshot, ''),
		coalesce(auth_type, ''),
		coalesce(resolved_model, ''),
		coalesce(requested_model, ''),
		model,
		coalesce(cache_input_mode, ''),
		input_tokens,
		output_tokens,
		reasoning_tokens,
		cached_tokens,
		cache_tokens,
		cache_read_tokens,
		cache_creation_tokens,
		total_tokens,
		coalesce(raw_json, '')
	from usage_events
	where id <= ? and (
		timestamp_ms > ? or (timestamp_ms = ? and id >= ?)
	)`,
		snapshot.maxID,
		snapshot.cutoffTimestampMS,
		snapshot.cutoffTimestampMS,
		snapshot.cutoffID,
	)
	if err != nil {
		return compatibleUsageTotals{}, err
	}
	defer rows.Close()

	var totals compatibleUsageTotals
	for rows.Next() {
		var event usage.Event
		var failed, ready, accountingValid int
		var projectedTotal int64
		if err := rows.Scan(
			&failed,
			&ready,
			&projectedTotal,
			&event.AccountingVersion,
			&accountingValid,
			&event.TokenBreakdown.Quality,
			&event.Provider,
			&event.ExecutorType,
			&event.AuthProviderSnapshot,
			&event.AuthType,
			&event.ResolvedModel,
			&event.RequestedModel,
			&event.Model,
			&event.CacheInputMode,
			&event.InputTokens,
			&event.OutputTokens,
			&event.ReasoningTokens,
			&event.CachedTokens,
			&event.CacheTokens,
			&event.CacheReadTokens,
			&event.CacheCreationTokens,
			&event.TotalTokens,
			&event.RawJSON,
		); err != nil {
			return compatibleUsageTotals{}, err
		}
		event.AccountingValid = accountingValid != 0
		if ready == 0 {
			usage.ApplyTokenAccounting(&event, nil)
			projectedTotal = event.TotalTokens
		}
		totals.totalRequests++
		if failed != 0 {
			totals.failureCount++
		} else {
			totals.successCount++
		}
		totals.totalTokens = usage.SaturatingTokenSum(totals.totalTokens, projectedTotal)
	}
	if err := rows.Err(); err != nil {
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
		requested_model, resolved_model, reasoning_effort, service_tier, cache_input_mode,
		accounting_version, accounting_valid, coalesce(accounting_quality, ''),
		input_tokens, output_tokens, reasoning_tokens, cached_tokens, cache_tokens, cache_read_tokens, cache_creation_tokens, total_tokens,
		normalized_uncached_input_tokens, normalized_total_input_tokens, normalized_cache_read_tokens, normalized_cache_creation_tokens,
		normalized_non_reasoning_output_tokens, normalized_reasoning_output_tokens, normalized_total_output_tokens, unclassified_tokens,
		latency_ms, ttft_ms, failed, fail_status_code, fail_summary,
		coalesce(response_metadata_json, ''), header_quota_recover_at_ms, header_quota_used_percent, coalesce(header_quota_plan_type, ''), coalesce(header_error_kind, ''), coalesce(header_error_code, ''), coalesce(header_trace_id, ''),
		coalesce(raw_json, ''),
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

func scanCompatibleDetail(rows *sql.Rows) (compatibleExportRow, error) {
	var row compatibleExportRow
	var event usage.Event
	var provider, authType string
	var authSnapshotAt sql.NullInt64
	var latency sql.NullInt64
	var ttft sql.NullInt64
	var failStatusCode sql.NullInt64
	var responseMetadataJSON string
	var rawJSON string
	var accountingQuality string
	var accountingVersion, accountingValid int
	var failed int
	var normalizedUncachedInput, normalizedTotalInput, normalizedCacheRead, normalizedCacheCreation sql.NullInt64
	var normalizedNonReasoningOutput, normalizedReasoningOutput, normalizedTotalOutput, unclassifiedTokens sql.NullInt64
	var persistedTotalTokens int64

	err := rows.Scan(
		&row.id,
		&row.endpoint,
		&row.model,
		&provider,
		&authType,
		&event.Timestamp,
		&event.Source,
		&event.AuthIndex,
		&event.APIKeyHash,
		&event.AccountSnapshot,
		&event.AuthLabelSnapshot,
		&event.AuthFileSnapshot,
		&event.AuthProviderSnapshot,
		&event.AuthProjectIDSnapshot,
		&authSnapshotAt,
		&latency,
		&ttft,
		&event.RequestedModel,
		&event.ResolvedModel,
		&event.ReasoningEffort,
		&event.ServiceTier,
		&event.RequestServiceTier,
		&event.ResponseServiceTier,
		&event.CacheInputMode,
		&event.ExecutorType,
		&accountingVersion,
		&accountingValid,
		&accountingQuality,
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
		&normalizedNonReasoningOutput,
		&normalizedReasoningOutput,
		&normalizedTotalOutput,
		&unclassifiedTokens,
		&persistedTotalTokens,
		&rawJSON,
		&failed,
		&failStatusCode,
		&event.FailSummary,
		&responseMetadataJSON,
	)
	if err != nil {
		return compatibleExportRow{}, err
	}
	event.Provider = provider
	event.AuthType = authType
	event.Model = row.model
	if event.Model == "-" && event.RequestedModel == "" {
		event.Model = ""
	}
	event.Endpoint = row.endpoint
	event.TotalTokens = persistedTotalTokens
	event.RawJSON = rawJSON
	event.AccountingVersion = accountingVersion
	event.AccountingValid = accountingValid != 0
	event.TokenBreakdown.Quality = accountingQuality
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
	event.Failed = failed != 0
	usage.ApplyTokenAccounting(&event, nil)
	if normalizedUncachedInput.Valid && normalizedTotalInput.Valid && normalizedCacheRead.Valid && normalizedCacheCreation.Valid &&
		normalizedNonReasoningOutput.Valid && normalizedReasoningOutput.Valid && normalizedTotalOutput.Valid && unclassifiedTokens.Valid {
		fallback := event
		event.AccountingVersion = accountingVersion
		event.AccountingValid = accountingValid != 0
		event.NormalizedUncachedInputTokens = normalizedUncachedInput.Int64
		event.NormalizedTotalInputTokens = normalizedTotalInput.Int64
		event.NormalizedCacheReadTokens = normalizedCacheRead.Int64
		event.NormalizedCacheCreationTokens = normalizedCacheCreation.Int64
		event.NormalizedNonReasoningOutputTokens = normalizedNonReasoningOutput.Int64
		event.NormalizedReasoningOutputTokens = normalizedReasoningOutput.Int64
		event.NormalizedTotalOutputTokens = normalizedTotalOutput.Int64
		event.UnclassifiedTokens = unclassifiedTokens.Int64
		event.TotalTokens = persistedTotalTokens
		if !usage.RestorePersistedTokenAccounting(&event, accountingQuality) {
			event = fallback
		}
	}
	row.detail = rawMetadataDetail{
		Detail:           usage.BuildDetail(event),
		ResponseMetadata: validatedMetadataJSON(responseMetadataJSON),
	}
	return row, nil
}

func scanExportRow(rows *sql.Rows) (exportRow, error) {
	var row exportRow
	event := &row.event
	var requestID, provider, executorType, endpoint, method, path, authType, authIndex, source, sourceHash, apiKeyHash, accountSnapshot, authLabelSnapshot, authFileSnapshot, authProviderSnapshot, authProjectIDSnapshot, requestedModel, resolvedModel, reasoningEffort, serviceTier, cacheInputMode, failSummary sql.NullString
	var responseMetadataJSON, quotaPlanType, errorKind, errorCode, traceID string
	var authSnapshotAt sql.NullInt64
	var latency, ttft sql.NullInt64
	var failStatusCode sql.NullInt64
	var quotaRecoverAt sql.NullInt64
	var quotaUsedPercent sql.NullFloat64
	var normalizedUncachedInput, normalizedTotalInput, normalizedCacheRead, normalizedCacheCreation sql.NullInt64
	var normalizedNonReasoningOutput, normalizedReasoningOutput, normalizedTotalOutput, unclassifiedTokens sql.NullInt64
	var persistedTotalTokens int64
	var accountingVersion, accountingValid, failed int
	var accountingQuality string
	var rawJSON string
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
		&cacheInputMode,
		&accountingVersion,
		&accountingValid,
		&accountingQuality,
		&event.InputTokens,
		&event.OutputTokens,
		&event.ReasoningTokens,
		&event.CachedTokens,
		&event.CacheTokens,
		&event.CacheReadTokens,
		&event.CacheCreationTokens,
		&persistedTotalTokens,
		&normalizedUncachedInput,
		&normalizedTotalInput,
		&normalizedCacheRead,
		&normalizedCacheCreation,
		&normalizedNonReasoningOutput,
		&normalizedReasoningOutput,
		&normalizedTotalOutput,
		&unclassifiedTokens,
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
		return exportRow{}, err
	}
	event.RequestID = requestID.String
	event.AnalyticsModel = usageidentity.AnalyticsModelForRequest(event.Model, requestedModel.String)
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
	event.CacheInputMode = cacheInputMode.String
	event.TotalTokens = persistedTotalTokens
	event.RawJSON = rawJSON
	event.AccountingVersion = accountingVersion
	event.AccountingValid = accountingValid != 0
	event.TokenBreakdown.Quality = accountingQuality
	event.FailSummary = failSummary.String
	event.HeaderQuotaPlanType = quotaPlanType
	event.HeaderErrorKind = errorKind
	event.HeaderErrorCode = errorCode
	event.HeaderTraceID = traceID
	event.Failed = failed != 0
	usage.ApplyTokenAccounting(event, nil)
	if normalizedUncachedInput.Valid && normalizedTotalInput.Valid && normalizedCacheRead.Valid && normalizedCacheCreation.Valid &&
		normalizedNonReasoningOutput.Valid && normalizedReasoningOutput.Valid && normalizedTotalOutput.Valid && unclassifiedTokens.Valid {
		fallback := *event
		event.AccountingVersion = accountingVersion
		event.AccountingValid = accountingValid != 0
		event.NormalizedUncachedInputTokens = normalizedUncachedInput.Int64
		event.NormalizedTotalInputTokens = normalizedTotalInput.Int64
		event.NormalizedCacheReadTokens = normalizedCacheRead.Int64
		event.NormalizedCacheCreationTokens = normalizedCacheCreation.Int64
		event.NormalizedNonReasoningOutputTokens = normalizedNonReasoningOutput.Int64
		event.NormalizedReasoningOutputTokens = normalizedReasoningOutput.Int64
		event.NormalizedTotalOutputTokens = normalizedTotalOutput.Int64
		event.UnclassifiedTokens = unclassifiedTokens.Int64
		event.TotalTokens = persistedTotalTokens
		if !usage.RestorePersistedTokenAccounting(event, accountingQuality) {
			*event = fallback
		}
	}
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
	event.RawJSON = ""
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

func normalizeCompatibleUsageStreamLimit(limit int) int {
	limit = normalizeUsageStreamLimit(limit)
	// Compatible usage retains one ordered int64 ID per exported row so the
	// SQLite reader can close before HTTP output starts. Keep that snapshot
	// bounded without changing the independently streamed JSONL export limit.
	if limit > maxCompatibleUsageStreamLimit {
		return maxCompatibleUsageStreamLimit
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
