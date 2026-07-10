package usagerollup

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const dashboardHourMS int64 = 60 * 60 * 1000

type DashboardHourlyRow struct {
	usage.LongContextTokens
	BucketMS            int64
	Model               string
	BillingModel        string
	ServiceTier         string
	Calls               int64
	SuccessCalls        int64
	FailureCalls        int64
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
	LatencySumMS        int64
	LatencySamples      int64
	ZeroTokenCalls      int64
	UpdatedAtMS         int64
}

type dashboardHourlyKey struct {
	BucketMS     int64
	Model        string
	BillingModel string
	ServiceTier  string
}

type dashboardEventRow struct {
	ID                  int64
	TimestampMS         int64
	Model               string
	BillingModel        string
	ServiceTier         string
	Failed              bool
	InputTokens         int64
	OutputTokens        int64
	ReasoningTokens     int64
	CachedTokens        int64
	CacheTokens         int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	TotalTokens         int64
	LatencyMS           sql.NullInt64
}

func (r *repository) CatchUpDashboardHourly(ctx context.Context, limit int, nowMS int64) (CatchUpResult, error) {
	if limit <= 0 {
		limit = 1000
	}
	if nowMS <= 0 {
		return CatchUpResult{}, errors.New("nowMS must be greater than 0")
	}
	if err := r.acquireCatchUp(ctx); err != nil {
		return CatchUpResult{}, err
	}
	defer r.releaseCatchUp()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return CatchUpResult{}, err
	}
	defer func() {
		_ = tx.Rollback()
	}()

	checkpoint, err := checkpointInTx(ctx, tx, DashboardHourlyCheckpointName)
	if err != nil {
		return CatchUpResult{}, err
	}
	events, err := dashboardEventsAfterCheckpoint(ctx, tx, checkpoint.LastEventID, limit)
	if err != nil {
		return CatchUpResult{}, err
	}
	latestID, err := latestEventIDInTx(ctx, tx)
	if err != nil {
		return CatchUpResult{}, err
	}
	if len(events) == 0 {
		if err := upsertCheckpoint(ctx, tx, DashboardHourlyCheckpointName, checkpoint.LastEventID, nowMS, nowMS, nowMS, ""); err != nil {
			return CatchUpResult{}, err
		}
		if err := tx.Commit(); err != nil {
			return CatchUpResult{}, err
		}
		return CatchUpResult{LastEventID: checkpoint.LastEventID, Pending: latestID > checkpoint.LastEventID}, nil
	}

	rows := aggregateDashboardHourly(events, nowMS)
	if err := upsertDashboardHourlyRows(ctx, tx, rows); err != nil {
		return CatchUpResult{}, err
	}
	lastEventID := events[len(events)-1].ID
	if err := upsertCheckpoint(ctx, tx, DashboardHourlyCheckpointName, lastEventID, nowMS, nowMS, nowMS, ""); err != nil {
		return CatchUpResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CatchUpResult{}, err
	}
	return CatchUpResult{
		Processed:   len(events),
		LastEventID: lastEventID,
		Pending:     latestID > lastEventID,
	}, nil
}

func (r *repository) DashboardHourlyRows(ctx context.Context, fromMS, toMS int64) ([]DashboardHourlyRow, error) {
	if fromMS >= toMS {
		return []DashboardHourlyRow{}, nil
	}
	rows, err := r.db.QueryContext(ctx, `select
	bucket_ms,
	model,
	billing_model,
	service_tier,
	calls,
	success_calls,
	failure_calls,
	input_tokens,
	output_tokens,
	reasoning_tokens,
	cached_tokens,
	cache_read_tokens,
	cache_creation_tokens,
	long_input_tokens,
	long_output_tokens,
	long_cached_tokens,
	long_cache_read_tokens,
	long_cache_creation_tokens,
	total_tokens,
	latency_sum_ms,
	latency_samples,
	zero_token_calls,
	updated_at_ms
from usage_dashboard_hourly_rollups
where bucket_ms >= ? and bucket_ms < ?
order by bucket_ms, model, billing_model, service_tier`, fromMS, toMS)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make([]DashboardHourlyRow, 0)
	for rows.Next() {
		var row DashboardHourlyRow
		if err := rows.Scan(
			&row.BucketMS,
			&row.Model,
			&row.BillingModel,
			&row.ServiceTier,
			&row.Calls,
			&row.SuccessCalls,
			&row.FailureCalls,
			&row.InputTokens,
			&row.OutputTokens,
			&row.ReasoningTokens,
			&row.CachedTokens,
			&row.CacheReadTokens,
			&row.CacheCreationTokens,
			&row.LongInputTokens,
			&row.LongOutputTokens,
			&row.LongCachedTokens,
			&row.LongCacheReadTokens,
			&row.LongCacheCreationTokens,
			&row.TotalTokens,
			&row.LatencySumMS,
			&row.LatencySamples,
			&row.ZeroTokenCalls,
			&row.UpdatedAtMS,
		); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func dashboardEventsAfterCheckpoint(ctx context.Context, tx *sql.Tx, lastEventID int64, limit int) ([]dashboardEventRow, error) {
	rows, err := tx.QueryContext(ctx, `select
	id,
	timestamp_ms,
	model,
	coalesce(nullif(resolved_model, ''), model) as billing_model,
	coalesce(service_tier, '') as service_tier,
	failed,
	coalesce(input_tokens, 0),
	coalesce(output_tokens, 0),
	coalesce(reasoning_tokens, 0),
	coalesce(cached_tokens, 0),
	coalesce(cache_tokens, 0),
	coalesce(cache_read_tokens, 0),
	coalesce(cache_creation_tokens, 0),
	coalesce(total_tokens, 0),
	latency_ms
from usage_events
where id > ?
order by id
limit ?`, lastEventID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]dashboardEventRow, 0, limit)
	for rows.Next() {
		var event dashboardEventRow
		var failed int
		if err := rows.Scan(
			&event.ID,
			&event.TimestampMS,
			&event.Model,
			&event.BillingModel,
			&event.ServiceTier,
			&failed,
			&event.InputTokens,
			&event.OutputTokens,
			&event.ReasoningTokens,
			&event.CachedTokens,
			&event.CacheTokens,
			&event.CacheReadTokens,
			&event.CacheCreationTokens,
			&event.TotalTokens,
			&event.LatencyMS,
		); err != nil {
			return nil, err
		}
		event.Failed = failed != 0
		event.CachedTokens = usage.CompatibleCachedTokens(
			event.CachedTokens,
			event.CacheTokens,
			event.CacheReadTokens,
			event.CacheCreationTokens,
		)
		events = append(events, event)
	}
	return events, rows.Err()
}

func aggregateDashboardHourly(events []dashboardEventRow, nowMS int64) []DashboardHourlyRow {
	grouped := make(map[dashboardHourlyKey]*DashboardHourlyRow)
	for _, event := range events {
		model := strings.TrimSpace(event.Model)
		if model == "" {
			model = "-"
		}
		billingModel := strings.TrimSpace(event.BillingModel)
		if billingModel == "" {
			billingModel = model
		}
		key := dashboardHourlyKey{
			BucketMS:     event.TimestampMS - event.TimestampMS%dashboardHourMS,
			Model:        model,
			BillingModel: billingModel,
			ServiceTier:  strings.TrimSpace(event.ServiceTier),
		}
		row := grouped[key]
		if row == nil {
			row = &DashboardHourlyRow{
				BucketMS:     key.BucketMS,
				Model:        key.Model,
				BillingModel: key.BillingModel,
				ServiceTier:  key.ServiceTier,
				UpdatedAtMS:  nowMS,
			}
			grouped[key] = row
		}
		row.Calls++
		if event.Failed {
			row.FailureCalls++
		} else {
			row.SuccessCalls++
			if event.TotalTokens == 0 {
				row.ZeroTokenCalls++
			}
		}
		row.InputTokens += event.InputTokens
		row.OutputTokens += event.OutputTokens
		row.ReasoningTokens += event.ReasoningTokens
		row.CachedTokens += event.CachedTokens
		row.CacheReadTokens += event.CacheReadTokens
		row.CacheCreationTokens += event.CacheCreationTokens
		row.AddIfLongContext(event.InputTokens, event.OutputTokens, event.CachedTokens, event.CacheReadTokens, event.CacheCreationTokens)
		row.TotalTokens += event.TotalTokens
		if event.LatencyMS.Valid && event.LatencyMS.Int64 != 0 {
			row.LatencySumMS += event.LatencyMS.Int64
			row.LatencySamples++
		}
	}

	result := make([]DashboardHourlyRow, 0, len(grouped))
	for _, row := range grouped {
		result = append(result, *row)
	}
	return result
}

func upsertDashboardHourlyRows(ctx context.Context, tx *sql.Tx, rows []DashboardHourlyRow) error {
	if len(rows) == 0 {
		return nil
	}
	stmt, err := tx.PrepareContext(ctx, `insert into usage_dashboard_hourly_rollups (
	bucket_ms,
	model,
	billing_model,
	service_tier,
	calls,
	success_calls,
	failure_calls,
	input_tokens,
	output_tokens,
	reasoning_tokens,
	cached_tokens,
	cache_read_tokens,
	cache_creation_tokens,
	long_input_tokens,
	long_output_tokens,
	long_cached_tokens,
	long_cache_read_tokens,
	long_cache_creation_tokens,
	total_tokens,
	latency_sum_ms,
	latency_samples,
	zero_token_calls,
	updated_at_ms
) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
on conflict(bucket_ms, model, billing_model, service_tier) do update set
	calls = usage_dashboard_hourly_rollups.calls + excluded.calls,
	success_calls = usage_dashboard_hourly_rollups.success_calls + excluded.success_calls,
	failure_calls = usage_dashboard_hourly_rollups.failure_calls + excluded.failure_calls,
	input_tokens = usage_dashboard_hourly_rollups.input_tokens + excluded.input_tokens,
	output_tokens = usage_dashboard_hourly_rollups.output_tokens + excluded.output_tokens,
	reasoning_tokens = usage_dashboard_hourly_rollups.reasoning_tokens + excluded.reasoning_tokens,
	cached_tokens = usage_dashboard_hourly_rollups.cached_tokens + excluded.cached_tokens,
	cache_read_tokens = usage_dashboard_hourly_rollups.cache_read_tokens + excluded.cache_read_tokens,
	cache_creation_tokens = usage_dashboard_hourly_rollups.cache_creation_tokens + excluded.cache_creation_tokens,
	long_input_tokens = usage_dashboard_hourly_rollups.long_input_tokens + excluded.long_input_tokens,
	long_output_tokens = usage_dashboard_hourly_rollups.long_output_tokens + excluded.long_output_tokens,
	long_cached_tokens = usage_dashboard_hourly_rollups.long_cached_tokens + excluded.long_cached_tokens,
	long_cache_read_tokens = usage_dashboard_hourly_rollups.long_cache_read_tokens + excluded.long_cache_read_tokens,
	long_cache_creation_tokens = usage_dashboard_hourly_rollups.long_cache_creation_tokens + excluded.long_cache_creation_tokens,
	total_tokens = usage_dashboard_hourly_rollups.total_tokens + excluded.total_tokens,
	latency_sum_ms = usage_dashboard_hourly_rollups.latency_sum_ms + excluded.latency_sum_ms,
	latency_samples = usage_dashboard_hourly_rollups.latency_samples + excluded.latency_samples,
	zero_token_calls = usage_dashboard_hourly_rollups.zero_token_calls + excluded.zero_token_calls,
	updated_at_ms = excluded.updated_at_ms`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, row := range rows {
		if _, err := stmt.ExecContext(
			ctx,
			row.BucketMS,
			row.Model,
			row.BillingModel,
			row.ServiceTier,
			row.Calls,
			row.SuccessCalls,
			row.FailureCalls,
			row.InputTokens,
			row.OutputTokens,
			row.ReasoningTokens,
			row.CachedTokens,
			row.CacheReadTokens,
			row.CacheCreationTokens,
			row.LongInputTokens,
			row.LongOutputTokens,
			row.LongCachedTokens,
			row.LongCacheReadTokens,
			row.LongCacheCreationTokens,
			row.TotalTokens,
			row.LatencySumMS,
			row.LatencySamples,
			row.ZeroTokenCalls,
			row.UpdatedAtMS,
		); err != nil {
			return err
		}
	}
	return nil
}
