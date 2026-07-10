package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync/atomic"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/pricing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const (
	defaultTopModels            = 5
	defaultRecentFailures       = 5
	defaultHealthRows           = 5
	rollingWindowMinutes        = 30
	rollingWindowMs             = rollingWindowMinutes * 60 * 1000
	hourWindowMs                = 60 * 60 * 1000
	healthTimelineBucketMs      = 10 * 60 * 1000
	healthTimelineBuckets       = 24 * 6
	rollupFallbackLogIntervalMS = int64(5 * time.Minute / time.Millisecond)
)

type Service struct {
	store                   *store.Store
	hourlyRollupEnabled     bool
	lastRollupFallbackLogMS atomic.Int64
}

func New(store *store.Store, hourlyRollupEnabled ...bool) *Service {
	enabled := true
	if len(hourlyRollupEnabled) > 0 {
		enabled = hourlyRollupEnabled[0]
	}
	return &Service{store: store, hourlyRollupEnabled: enabled}
}

type SummaryParams struct {
	TodayStartMS   int64
	NowMS          int64
	TopModels      int
	RecentFailures int
}

type Window struct {
	TodayStartMS      int64 `json:"today_start_ms"`
	NowMS             int64 `json:"now_ms"`
	Rolling30MStartMS int64 `json:"rolling_30m_start_ms"`
}

type TodaySummary struct {
	TotalCalls          int64    `json:"total_calls"`
	SuccessCalls        int64    `json:"success_calls"`
	FailureCalls        int64    `json:"failure_calls"`
	SuccessRate         float64  `json:"success_rate"`
	InputTokens         int64    `json:"input_tokens"`
	OutputTokens        int64    `json:"output_tokens"`
	CachedTokens        int64    `json:"cached_tokens"`
	CacheReadTokens     int64    `json:"cache_read_tokens"`
	CacheCreationTokens int64    `json:"cache_creation_tokens"`
	ReasoningTokens     int64    `json:"reasoning_tokens"`
	TotalTokens         int64    `json:"total_tokens"`
	TotalCost           float64  `json:"total_cost"`
	AverageLatencyMS    *float64 `json:"average_latency_ms"`
	ZeroTokenCalls      int64    `json:"zero_token_calls"`
}

type RollingSummary struct {
	RPM         float64 `json:"rpm"`
	TPM         float64 `json:"tpm"`
	TotalCalls  int64   `json:"total_calls"`
	TotalTokens int64   `json:"total_tokens"`
}

type TopModel struct {
	Model       string  `json:"model"`
	Calls       int64   `json:"calls"`
	Tokens      int64   `json:"tokens"`
	Cost        float64 `json:"cost"`
	SuccessRate float64 `json:"success_rate"`
}

type TrafficPoint struct {
	BucketMS    int64   `json:"bucket_ms"`
	Calls       int64   `json:"calls"`
	Tokens      int64   `json:"tokens"`
	Success     int64   `json:"success"`
	Failure     int64   `json:"failure"`
	CallsShare  float64 `json:"calls_share"`
	TokensShare float64 `json:"tokens_share"`
	FailureRate float64 `json:"failure_rate"`
}

type HourlyActivityPoint struct {
	HourIndex int     `json:"hour_index"`
	BucketMS  int64   `json:"bucket_ms"`
	Calls     int64   `json:"calls"`
	Tokens    int64   `json:"tokens"`
	Intensity float64 `json:"intensity"`
}

type RequestHealthTimelinePoint struct {
	BucketMS    int64   `json:"bucket_ms"`
	Calls       int64   `json:"calls"`
	Tokens      int64   `json:"tokens"`
	Success     int64   `json:"success"`
	Failure     int64   `json:"failure"`
	SuccessRate float64 `json:"success_rate"`
	FailureRate float64 `json:"failure_rate"`
	Tone        string  `json:"tone"`
	Intensity   float64 `json:"intensity"`
	Future      bool    `json:"future"`
}

type RequestHealthTimeline struct {
	FromMS       int64                        `json:"from_ms"`
	ToMS         int64                        `json:"to_ms"`
	BucketMS     int64                        `json:"bucket_ms"`
	SuccessCalls int64                        `json:"success_calls"`
	FailureCalls int64                        `json:"failure_calls"`
	TotalCalls   int64                        `json:"total_calls"`
	SuccessRate  float64                      `json:"success_rate"`
	Points       []RequestHealthTimelinePoint `json:"points"`
}

type TokenMixSegment struct {
	Key    string  `json:"key"`
	Tokens int64   `json:"tokens"`
	Share  float64 `json:"share"`
}

type ModelCostRank struct {
	Model       string  `json:"model"`
	Calls       int64   `json:"calls"`
	Tokens      int64   `json:"tokens"`
	Cost        float64 `json:"cost"`
	SuccessRate float64 `json:"success_rate"`
	CostShare   float64 `json:"cost_share"`
}

type ChannelHealth struct {
	AuthIndex            string   `json:"auth_index"`
	Source               string   `json:"source,omitempty"`
	AccountSnapshot      string   `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot    string   `json:"auth_label_snapshot,omitempty"`
	AuthProviderSnapshot string   `json:"auth_provider_snapshot,omitempty"`
	Calls                int64    `json:"calls"`
	Failures             int64    `json:"failures"`
	FailureRate          float64  `json:"failure_rate"`
	SuccessRate          float64  `json:"success_rate"`
	Tokens               int64    `json:"tokens"`
	Cost                 float64  `json:"cost"`
	AverageLatencyMS     *float64 `json:"average_latency_ms"`
	Tone                 string   `json:"tone"`
}

type FailureSource struct {
	Source               string   `json:"source,omitempty"`
	SourceHash           string   `json:"source_hash"`
	AuthIndex            string   `json:"auth_index"`
	AccountSnapshot      string   `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot    string   `json:"auth_label_snapshot,omitempty"`
	AuthProviderSnapshot string   `json:"auth_provider_snapshot,omitempty"`
	Calls                int64    `json:"calls"`
	Failures             int64    `json:"failures"`
	FailureRate          float64  `json:"failure_rate"`
	LastSeenMS           int64    `json:"last_seen_ms"`
	AverageLatencyMS     *float64 `json:"average_latency_ms"`
	Tone                 string   `json:"tone"`
}

type RecentFailure struct {
	TimestampMS            int64                         `json:"timestamp_ms"`
	Model                  string                        `json:"model"`
	APIKeyHash             string                        `json:"api_key_hash"`
	Source                 string                        `json:"source,omitempty"`
	SourceHash             string                        `json:"source_hash"`
	AuthIndex              string                        `json:"auth_index"`
	AccountSnapshot        string                        `json:"account_snapshot,omitempty"`
	AuthLabelSnapshot      string                        `json:"auth_label_snapshot,omitempty"`
	AuthProviderSnapshot   string                        `json:"auth_provider_snapshot,omitempty"`
	AuthProjectIDSnapshot  string                        `json:"auth_project_id_snapshot,omitempty"`
	Endpoint               string                        `json:"endpoint"`
	DurationMS             *int64                        `json:"duration_ms"`
	FailStatusCode         *int64                        `json:"fail_status_code,omitempty"`
	FailSummary            string                        `json:"fail_summary,omitempty"`
	ResponseMetadata       *usage.ResponseHeaderMetadata `json:"response_metadata,omitempty"`
	HeaderQuotaRecoverAtMS *int64                        `json:"header_quota_recover_at_ms,omitempty"`
	HeaderQuotaUsedPercent *float64                      `json:"header_quota_used_percent,omitempty"`
	HeaderQuotaPlanType    string                        `json:"header_quota_plan_type,omitempty"`
	HeaderErrorKind        string                        `json:"header_error_kind,omitempty"`
	HeaderErrorCode        string                        `json:"header_error_code,omitempty"`
	HeaderTraceID          string                        `json:"header_trace_id,omitempty"`
}

type SummaryResponse struct {
	GeneratedAtMS   int64                 `json:"generated_at_ms"`
	Window          Window                `json:"window"`
	Today           TodaySummary          `json:"today"`
	Rolling30M      RollingSummary        `json:"rolling_30m"`
	TopModelsToday  []TopModel            `json:"top_models_today"`
	ModelCostRank   []ModelCostRank       `json:"model_cost_rank"`
	TrafficTimeline []TrafficPoint        `json:"traffic_timeline"`
	HourlyActivity  []HourlyActivityPoint `json:"hourly_activity"`
	RequestHealth   RequestHealthTimeline `json:"today_request_health_timeline"`
	TokenMix        []TokenMixSegment     `json:"token_mix"`
	ChannelHealth   []ChannelHealth       `json:"channel_health"`
	FailureSources  []FailureSource       `json:"failure_sources"`
	RecentFailures  []RecentFailure       `json:"recent_failures"`
}

func (s *Service) Summary(ctx context.Context, p SummaryParams) (SummaryResponse, error) {
	if p.TodayStartMS <= 0 {
		return SummaryResponse{}, errors.New("today_start_ms is required")
	}

	generatedAt := time.Now().UnixMilli()
	nowMS := p.NowMS
	if nowMS <= 0 {
		nowMS = generatedAt
	}
	if nowMS < p.TodayStartMS {
		return SummaryResponse{}, errors.New("now_ms must be greater than or equal to today_start_ms")
	}

	topLimit := p.TopModels
	if topLimit <= 0 {
		topLimit = defaultTopModels
	}
	recentLimit := p.RecentFailures
	if recentLimit <= 0 {
		recentLimit = defaultRecentFailures
	}

	todayAgg, modelStats, topStats, timeline, err := s.loadTodayMetrics(ctx, p.TodayStartMS, nowMS, topLimit)
	if err != nil {
		return SummaryResponse{}, err
	}
	rollingStartMS := nowMS - rollingWindowMs
	rollingAgg, err := s.store.AggregateBetween(ctx, rollingStartMS, nowMS)
	if err != nil {
		return SummaryResponse{}, err
	}
	recentFailures, err := s.store.RecentFailuresBetween(ctx, p.TodayStartMS, nowMS, recentLimit)
	if err != nil {
		return SummaryResponse{}, err
	}
	prices, err := s.store.LoadModelPrices(ctx)
	if err != nil {
		return SummaryResponse{}, err
	}
	filter := store.AnalyticsFilter{
		FromMS:        p.TodayStartMS,
		ToMS:          nowMS,
		IncludeFailed: true,
	}
	healthTimelineToMS := p.TodayStartMS + int64(healthTimelineBuckets)*healthTimelineBucketMs
	healthTimelinePoints, err := s.store.BucketTimelineBetween(ctx, p.TodayStartMS, nowMS, healthTimelineBucketMs)
	if err != nil {
		return SummaryResponse{}, err
	}
	channelStats, err := s.store.ChannelModelStatsWithFilter(ctx, filter)
	if err != nil {
		return SummaryResponse{}, err
	}
	failureSources, err := s.store.FailureSourcesWithFilter(ctx, filter)
	if err != nil {
		return SummaryResponse{}, err
	}
	today := buildTodaySummary(todayAgg, modelStats, prices)
	trafficTimeline := buildTrafficTimeline(p.TodayStartMS, nowMS, timeline)

	return SummaryResponse{
		GeneratedAtMS: generatedAt,
		Window: Window{
			TodayStartMS:      p.TodayStartMS,
			NowMS:             nowMS,
			Rolling30MStartMS: rollingStartMS,
		},
		Today:           today,
		Rolling30M:      buildRollingSummary(rollingAgg),
		TopModelsToday:  buildTopModels(topStats, prices),
		ModelCostRank:   buildModelCostRank(modelStats, prices, topLimit),
		TrafficTimeline: trafficTimeline,
		HourlyActivity:  buildHourlyActivity(trafficTimeline),
		RequestHealth:   buildRequestHealthTimeline(p.TodayStartMS, healthTimelineToMS, nowMS, healthTimelinePoints),
		TokenMix:        buildTokenMix(today),
		ChannelHealth:   buildChannelHealth(channelStats, prices, defaultHealthRows),
		FailureSources:  buildFailureSources(failureSources, defaultHealthRows),
		RecentFailures:  buildRecentFailures(recentFailures),
	}, nil
}

func (s *Service) loadTodayMetrics(ctx context.Context, fromMS, toMS int64, topLimit int) (store.Aggregate, []store.ModelStat, []store.ModelStat, []store.TimelinePoint, error) {
	if agg, modelStats, timeline, ok := s.loadTodayMetricsFromRollup(ctx, fromMS, toMS); ok {
		return agg, modelStats, selectTopModelStats(modelStats, topLimit), timeline, nil
	}

	agg, err := s.store.AggregateBetween(ctx, fromMS, toMS)
	if err != nil {
		return store.Aggregate{}, nil, nil, nil, err
	}
	modelStats, err := s.store.ModelStatsBetween(ctx, fromMS, toMS)
	if err != nil {
		return store.Aggregate{}, nil, nil, nil, err
	}
	topStats, err := s.store.TopModelsBetween(ctx, fromMS, toMS, topLimit)
	if err != nil {
		return store.Aggregate{}, nil, nil, nil, err
	}
	timeline, err := s.store.HourlyTimelineBetween(ctx, fromMS, toMS)
	if err != nil {
		return store.Aggregate{}, nil, nil, nil, err
	}
	return agg, modelStats, topStats, timeline, nil
}

func (s *Service) loadTodayMetricsFromRollup(ctx context.Context, fromMS, toMS int64) (store.Aggregate, []store.ModelStat, []store.TimelinePoint, bool) {
	if !s.hourlyRollupEnabled {
		return store.Aggregate{}, nil, nil, false
	}
	if fromMS >= toMS {
		return store.Aggregate{}, []store.ModelStat{}, []store.TimelinePoint{}, true
	}
	checkpoint, err := s.store.DashboardHourlyRollupCheckpoint(ctx)
	if err != nil {
		s.logRollupFallback(fmt.Sprintf("checkpoint query failed: %v", err))
		return store.Aggregate{}, nil, nil, false
	}
	latestID, err := s.store.LatestUsageEventID(ctx)
	if err != nil {
		s.logRollupFallback(fmt.Sprintf("latest event query failed: %v", err))
		return store.Aggregate{}, nil, nil, false
	}
	if checkpoint.LastEventID < latestID {
		s.logRollupFallback(fmt.Sprintf("checkpoint pending: last_event_id=%d latest_event_id=%d", checkpoint.LastEventID, latestID))
		return store.Aggregate{}, nil, nil, false
	}

	fullStartMS := ceilHourMS(fromMS)
	fullEndMS := floorHourMS(toMS)
	if fullStartMS >= fullEndMS {
		return store.Aggregate{}, nil, nil, false
	}
	rows, err := s.store.DashboardHourlyRollupRows(ctx, fullStartMS, fullEndMS)
	if err != nil {
		s.logRollupFallback(fmt.Sprintf("hourly rows query failed: %v", err))
		return store.Aggregate{}, nil, nil, false
	}

	agg, modelStats, timeline := dashboardMetricsFromHourlyRows(rows)
	for _, edge := range dashboardRawEdges(fromMS, toMS, fullStartMS, fullEndMS) {
		edgeAgg, err := s.store.AggregateBetween(ctx, edge.fromMS, edge.toMS)
		if err != nil {
			s.logRollupFallback(fmt.Sprintf("raw edge aggregate failed: %v", err))
			return store.Aggregate{}, nil, nil, false
		}
		edgeModels, err := s.store.ModelStatsBetween(ctx, edge.fromMS, edge.toMS)
		if err != nil {
			s.logRollupFallback(fmt.Sprintf("raw edge model query failed: %v", err))
			return store.Aggregate{}, nil, nil, false
		}
		agg = mergeDashboardAggregates(agg, edgeAgg)
		modelStats = mergeDashboardModelStats(modelStats, edgeModels)
	}

	if fromMS%hourWindowMs != 0 {
		timeline, err = s.store.HourlyTimelineBetween(ctx, fromMS, toMS)
		if err != nil {
			s.logRollupFallback(fmt.Sprintf("offset timeline query failed: %v", err))
			return store.Aggregate{}, nil, nil, false
		}
	} else if fullEndMS < toMS {
		edgeTimeline, err := s.store.HourlyTimelineBetween(ctx, fullEndMS, toMS)
		if err != nil {
			s.logRollupFallback(fmt.Sprintf("raw edge timeline query failed: %v", err))
			return store.Aggregate{}, nil, nil, false
		}
		timeline = mergeDashboardTimeline(timeline, edgeTimeline)
	}

	return agg, modelStats, timeline, true
}

func (s *Service) logRollupFallback(reason string) {
	nowMS := time.Now().UnixMilli()
	for {
		lastMS := s.lastRollupFallbackLogMS.Load()
		if lastMS > 0 && nowMS-lastMS < rollupFallbackLogIntervalMS {
			return
		}
		if s.lastRollupFallbackLogMS.CompareAndSwap(lastMS, nowMS) {
			log.Printf("[dashboard-rollup] falling back to raw usage events: %s", reason)
			return
		}
	}
}

type dashboardTimeRange struct {
	fromMS int64
	toMS   int64
}

func dashboardRawEdges(fromMS, toMS, fullStartMS, fullEndMS int64) []dashboardTimeRange {
	ranges := make([]dashboardTimeRange, 0, 2)
	if fromMS < fullStartMS {
		ranges = append(ranges, dashboardTimeRange{fromMS: fromMS, toMS: min(fullStartMS, toMS)})
	}
	if fullEndMS < toMS {
		ranges = append(ranges, dashboardTimeRange{fromMS: max(fullEndMS, fromMS), toMS: toMS})
	}
	return ranges
}

func ceilHourMS(value int64) int64 {
	if value%hourWindowMs == 0 {
		return value
	}
	return value - value%hourWindowMs + hourWindowMs
}

func floorHourMS(value int64) int64 {
	return value - value%hourWindowMs
}

func dashboardMetricsFromHourlyRows(rows []store.DashboardHourlyRollupRow) (store.Aggregate, []store.ModelStat, []store.TimelinePoint) {
	agg := store.Aggregate{}
	modelStats := make([]store.ModelStat, 0, len(rows))
	timelineByBucket := make(map[int64]*store.TimelinePoint)
	var latencySum int64
	for _, row := range rows {
		agg.TotalCalls += row.Calls
		agg.SuccessCalls += row.SuccessCalls
		agg.FailureCalls += row.FailureCalls
		agg.InputTokens += row.InputTokens
		agg.OutputTokens += row.OutputTokens
		agg.ReasoningTokens += row.ReasoningTokens
		agg.CachedTokens += row.CachedTokens
		agg.CacheReadTokens += row.CacheReadTokens
		agg.CacheCreationTokens += row.CacheCreationTokens
		agg.LongInputTokens += row.LongInputTokens
		agg.LongOutputTokens += row.LongOutputTokens
		agg.LongCachedTokens += row.LongCachedTokens
		agg.LongCacheReadTokens += row.LongCacheReadTokens
		agg.LongCacheCreationTokens += row.LongCacheCreationTokens
		agg.TotalTokens += row.TotalTokens
		agg.LatencySamples += row.LatencySamples
		agg.ZeroTokenCalls += row.ZeroTokenCalls
		latencySum += row.LatencySumMS
		modelStats = append(modelStats, store.ModelStat{
			Model:               row.Model,
			BillingModel:        row.BillingModel,
			ServiceTier:         row.ServiceTier,
			Calls:               row.Calls,
			SuccessCalls:        row.SuccessCalls,
			InputTokens:         row.InputTokens,
			OutputTokens:        row.OutputTokens,
			ReasoningTokens:     row.ReasoningTokens,
			CachedTokens:        row.CachedTokens,
			CacheReadTokens:     row.CacheReadTokens,
			CacheCreationTokens: row.CacheCreationTokens,
			LongContextTokens:   row.LongContextTokens,
			TotalTokens:         row.TotalTokens,
		})
		point := timelineByBucket[row.BucketMS]
		if point == nil {
			point = &store.TimelinePoint{BucketMS: row.BucketMS}
			timelineByBucket[row.BucketMS] = point
		}
		point.Calls += row.Calls
		point.Tokens += row.TotalTokens
		point.Success += row.SuccessCalls
		point.Failure += row.FailureCalls
	}
	if agg.LatencySamples > 0 {
		agg.AvgLatencyMS.Valid = true
		agg.AvgLatencyMS.Float64 = float64(latencySum) / float64(agg.LatencySamples)
	}
	timeline := make([]store.TimelinePoint, 0, len(timelineByBucket))
	for _, point := range timelineByBucket {
		timeline = append(timeline, *point)
	}
	sort.Slice(timeline, func(i, j int) bool { return timeline[i].BucketMS < timeline[j].BucketMS })
	return agg, modelStats, timeline
}

func mergeDashboardAggregates(left, right store.Aggregate) store.Aggregate {
	latencySum := left.AvgLatencyMS.Float64*float64(left.LatencySamples) + right.AvgLatencyMS.Float64*float64(right.LatencySamples)
	left.TotalCalls += right.TotalCalls
	left.SuccessCalls += right.SuccessCalls
	left.FailureCalls += right.FailureCalls
	left.InputTokens += right.InputTokens
	left.OutputTokens += right.OutputTokens
	left.ReasoningTokens += right.ReasoningTokens
	left.CachedTokens += right.CachedTokens
	left.CacheReadTokens += right.CacheReadTokens
	left.CacheCreationTokens += right.CacheCreationTokens
	left.LongInputTokens += right.LongInputTokens
	left.LongOutputTokens += right.LongOutputTokens
	left.LongCachedTokens += right.LongCachedTokens
	left.LongCacheReadTokens += right.LongCacheReadTokens
	left.LongCacheCreationTokens += right.LongCacheCreationTokens
	left.TotalTokens += right.TotalTokens
	left.LatencySamples += right.LatencySamples
	left.ZeroTokenCalls += right.ZeroTokenCalls
	left.AvgLatencyMS.Valid = left.LatencySamples > 0
	if left.AvgLatencyMS.Valid {
		left.AvgLatencyMS.Float64 = latencySum / float64(left.LatencySamples)
	}
	return left
}

func mergeDashboardModelStats(left, right []store.ModelStat) []store.ModelStat {
	type key struct {
		model        string
		billingModel string
		serviceTier  string
	}
	grouped := make(map[key]*store.ModelStat, len(left)+len(right))
	order := make([]key, 0, len(left)+len(right))
	for _, stat := range append(append([]store.ModelStat{}, left...), right...) {
		mapKey := key{model: stat.Model, billingModel: stat.BillingModel, serviceTier: stat.ServiceTier}
		entry := grouped[mapKey]
		if entry == nil {
			copy := stat
			grouped[mapKey] = &copy
			order = append(order, mapKey)
			continue
		}
		entry.Calls += stat.Calls
		entry.SuccessCalls += stat.SuccessCalls
		entry.InputTokens += stat.InputTokens
		entry.OutputTokens += stat.OutputTokens
		entry.ReasoningTokens += stat.ReasoningTokens
		entry.CachedTokens += stat.CachedTokens
		entry.CacheReadTokens += stat.CacheReadTokens
		entry.CacheCreationTokens += stat.CacheCreationTokens
		entry.LongInputTokens += stat.LongInputTokens
		entry.LongOutputTokens += stat.LongOutputTokens
		entry.LongCachedTokens += stat.LongCachedTokens
		entry.LongCacheReadTokens += stat.LongCacheReadTokens
		entry.LongCacheCreationTokens += stat.LongCacheCreationTokens
		entry.TotalTokens += stat.TotalTokens
	}
	result := make([]store.ModelStat, 0, len(order))
	for _, mapKey := range order {
		result = append(result, *grouped[mapKey])
	}
	return result
}

func mergeDashboardTimeline(left, right []store.TimelinePoint) []store.TimelinePoint {
	grouped := make(map[int64]*store.TimelinePoint, len(left)+len(right))
	for _, point := range append(append([]store.TimelinePoint{}, left...), right...) {
		entry := grouped[point.BucketMS]
		if entry == nil {
			copy := point
			grouped[point.BucketMS] = &copy
			continue
		}
		entry.Calls += point.Calls
		entry.Tokens += point.Tokens
		entry.Success += point.Success
		entry.Failure += point.Failure
	}
	result := make([]store.TimelinePoint, 0, len(grouped))
	for _, point := range grouped {
		result = append(result, *point)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].BucketMS < result[j].BucketMS })
	return result
}

func selectTopModelStats(stats []store.ModelStat, limit int) []store.ModelStat {
	if limit <= 0 {
		limit = defaultTopModels
	}
	callsByModel := make(map[string]int64)
	for _, stat := range stats {
		callsByModel[stat.Model] += stat.Calls
	}
	models := make([]string, 0, len(callsByModel))
	for model := range callsByModel {
		models = append(models, model)
	}
	sort.SliceStable(models, func(i, j int) bool {
		if callsByModel[models[i]] != callsByModel[models[j]] {
			return callsByModel[models[i]] > callsByModel[models[j]]
		}
		return models[i] < models[j]
	})
	if len(models) > limit {
		models = models[:limit]
	}
	selected := make(map[string]bool, len(models))
	for _, model := range models {
		selected[model] = true
	}
	result := make([]store.ModelStat, 0)
	for _, stat := range stats {
		if selected[stat.Model] {
			result = append(result, stat)
		}
	}
	return result
}

func buildTodaySummary(agg store.Aggregate, modelStats []store.ModelStat, prices map[string]store.ModelPrice) TodaySummary {
	return TodaySummary{
		TotalCalls:          agg.TotalCalls,
		SuccessCalls:        agg.SuccessCalls,
		FailureCalls:        agg.FailureCalls,
		SuccessRate:         rate(agg.SuccessCalls, agg.TotalCalls),
		InputTokens:         agg.InputTokens,
		OutputTokens:        agg.OutputTokens,
		CachedTokens:        agg.CachedTokens,
		CacheReadTokens:     agg.CacheReadTokens,
		CacheCreationTokens: agg.CacheCreationTokens,
		ReasoningTokens:     agg.ReasoningTokens,
		TotalTokens:         agg.TotalTokens,
		TotalCost:           totalCost(modelStats, prices),
		AverageLatencyMS:    nullableFloat(agg.AvgLatencyMS.Valid, agg.AvgLatencyMS.Float64),
		ZeroTokenCalls:      agg.ZeroTokenCalls,
	}
}

func buildRollingSummary(agg store.Aggregate) RollingSummary {
	return RollingSummary{
		RPM:         float64(agg.TotalCalls) / rollingWindowMinutes,
		TPM:         float64(agg.TotalTokens) / rollingWindowMinutes,
		TotalCalls:  agg.TotalCalls,
		TotalTokens: agg.TotalTokens,
	}
}

func buildTopModels(stats []store.ModelStat, prices map[string]store.ModelPrice) []TopModel {
	aggregated := aggregateModelStats(stats, prices)
	result := make([]TopModel, 0, len(aggregated))
	for _, stat := range aggregated {
		result = append(result, TopModel{
			Model:       stat.Model,
			Calls:       stat.Calls,
			Tokens:      stat.TotalTokens,
			Cost:        stat.Cost,
			SuccessRate: rate(stat.SuccessCalls, stat.Calls),
		})
	}
	return result
}

func buildTrafficTimeline(todayStartMS int64, nowMS int64, points []store.TimelinePoint) []TrafficPoint {
	pointByBucket := make(map[int64]store.TimelinePoint, len(points))
	for _, point := range points {
		pointByBucket[point.BucketMS] = point
	}

	hours := 24
	result := make([]TrafficPoint, 0, hours)
	var maxCalls int64
	var maxTokens int64
	for i := 0; i < hours; i++ {
		bucketMS := todayStartMS + int64(i)*hourWindowMs
		point := pointByBucket[bucketMS]
		row := TrafficPoint{
			BucketMS: bucketMS,
			Calls:    point.Calls,
			Tokens:   point.Tokens,
			Success:  point.Success,
			Failure:  point.Failure,
		}
		row.FailureRate = rate(row.Failure, row.Calls)
		if row.Calls > maxCalls {
			maxCalls = row.Calls
		}
		if row.Tokens > maxTokens {
			maxTokens = row.Tokens
		}
		result = append(result, row)
	}

	for index := range result {
		result[index].CallsShare = rate(result[index].Calls, maxCalls)
		result[index].TokensShare = rate(result[index].Tokens, maxTokens)
	}
	return result
}

func buildHourlyActivity(points []TrafficPoint) []HourlyActivityPoint {
	result := make([]HourlyActivityPoint, 0, len(points))
	for index, point := range points {
		intensity := point.CallsShare
		if point.TokensShare > intensity {
			intensity = point.TokensShare
		}
		result = append(result, HourlyActivityPoint{
			HourIndex: index,
			BucketMS:  point.BucketMS,
			Calls:     point.Calls,
			Tokens:    point.Tokens,
			Intensity: intensity,
		})
	}
	return result
}

func buildRequestHealthTimeline(fromMS int64, toMS int64, nowMS int64, points []store.TimelinePoint) RequestHealthTimeline {
	pointByBucket := make(map[int64]store.TimelinePoint, len(points))
	var maxCalls int64
	for _, point := range points {
		pointByBucket[point.BucketMS] = point
		if point.Calls > maxCalls {
			maxCalls = point.Calls
		}
	}

	result := RequestHealthTimeline{
		FromMS:   fromMS,
		ToMS:     toMS,
		BucketMS: healthTimelineBucketMs,
		Points:   make([]RequestHealthTimelinePoint, 0, healthTimelineBuckets),
	}

	for index := 0; index < healthTimelineBuckets; index++ {
		bucketMS := fromMS + int64(index)*healthTimelineBucketMs
		isFuture := bucketMS > nowMS
		point := pointByBucket[bucketMS]
		failureRate := rate(point.Failure, point.Calls)
		row := RequestHealthTimelinePoint{
			BucketMS:    bucketMS,
			Calls:       point.Calls,
			Tokens:      point.Tokens,
			Success:     point.Success,
			Failure:     point.Failure,
			SuccessRate: rate(point.Success, point.Calls),
			FailureRate: failureRate,
			Tone:        requestHealthTone(point.Calls, failureRate, isFuture),
			Intensity:   rate(point.Calls, maxCalls),
			Future:      isFuture,
		}
		result.SuccessCalls += row.Success
		result.FailureCalls += row.Failure
		result.TotalCalls += row.Calls
		result.Points = append(result.Points, row)
	}
	result.SuccessRate = rate(result.SuccessCalls, result.TotalCalls)
	return result
}

func requestHealthTone(calls int64, failureRate float64, future bool) string {
	switch {
	case future:
		return "future"
	case calls == 0:
		return "empty"
	case failureRate >= 0.1:
		return "bad"
	case failureRate > 0:
		return "warn"
	default:
		return "good"
	}
}

func buildTokenMix(today TodaySummary) []TokenMixSegment {
	inputTokens := max(today.InputTokens, int64(0))
	cachedTokens := max(today.CachedTokens, int64(0)) +
		max(today.CacheReadTokens, int64(0)) +
		max(today.CacheCreationTokens, int64(0))
	outputTokens := max(today.OutputTokens, int64(0))
	reasoningTokens := max(today.ReasoningTokens, int64(0))

	if today.TotalTokens > 0 {
		overflow := inputTokens + cachedTokens + outputTokens + reasoningTokens - today.TotalTokens
		if overflow > 0 {
			inputDeduction := min(inputTokens, overflow)
			inputTokens -= inputDeduction
			overflow -= inputDeduction
		}
		if overflow > 0 {
			outputDeduction := min(outputTokens, overflow)
			outputTokens -= outputDeduction
		}
	}

	total := inputTokens + cachedTokens + outputTokens + reasoningTokens
	return []TokenMixSegment{
		{Key: "input", Tokens: inputTokens, Share: rate(inputTokens, total)},
		{Key: "cached", Tokens: cachedTokens, Share: rate(cachedTokens, total)},
		{Key: "output", Tokens: outputTokens, Share: rate(outputTokens, total)},
		{Key: "reasoning", Tokens: reasoningTokens, Share: rate(reasoningTokens, total)},
	}
}

func buildModelCostRank(stats []store.ModelStat, prices map[string]store.ModelPrice, limit int) []ModelCostRank {
	aggregated := aggregateModelStats(stats, prices)
	rows := make([]ModelCostRank, 0, len(aggregated))
	var maxCost float64
	for _, stat := range aggregated {
		if stat.Cost > maxCost {
			maxCost = stat.Cost
		}
		rows = append(rows, ModelCostRank{
			Model:       stat.Model,
			Calls:       stat.Calls,
			Tokens:      stat.TotalTokens,
			Cost:        stat.Cost,
			SuccessRate: rate(stat.SuccessCalls, stat.Calls),
		})
	}
	for index := range rows {
		rows[index].CostShare = rateFloat(rows[index].Cost, maxCost)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Cost != rows[j].Cost {
			return rows[i].Cost > rows[j].Cost
		}
		return rows[i].Calls > rows[j].Calls
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func buildChannelHealth(stats []store.ChannelModelStat, prices map[string]store.ModelPrice, limit int) []ChannelHealth {
	type accumulator struct {
		row        ChannelHealth
		latencySum float64
		latencyN   int64
	}
	grouped := map[string]*accumulator{}
	for _, stat := range stats {
		authIndex := stat.AuthIndex
		if authIndex == "" {
			authIndex = "-"
		}
		entry := grouped[authIndex]
		if entry == nil {
			entry = &accumulator{row: ChannelHealth{
				AuthIndex:            authIndex,
				Source:               stat.Source,
				AccountSnapshot:      stat.AccountSnapshot,
				AuthLabelSnapshot:    stat.AuthLabelSnapshot,
				AuthProviderSnapshot: stat.AuthProviderSnapshot,
			}}
			grouped[authIndex] = entry
		}
		fillChannelHealthSnapshots(&entry.row, stat)
		entry.row.Calls += stat.Calls
		entry.row.Failures += stat.FailureCalls
		entry.row.Tokens += stat.TotalTokens
		entry.row.Cost += costForChannelStat(stat, prices)
		if stat.AvgLatencyMS.Valid && stat.LatencySamples > 0 {
			entry.latencySum += stat.AvgLatencyMS.Float64 * float64(stat.LatencySamples)
			entry.latencyN += stat.LatencySamples
		}
	}

	rows := make([]ChannelHealth, 0, len(grouped))
	for _, entry := range grouped {
		success := entry.row.Calls - entry.row.Failures
		entry.row.SuccessRate = rate(success, entry.row.Calls)
		entry.row.FailureRate = rate(entry.row.Failures, entry.row.Calls)
		if entry.latencyN > 0 {
			value := entry.latencySum / float64(entry.latencyN)
			entry.row.AverageLatencyMS = &value
		}
		entry.row.Tone = healthTone(entry.row.SuccessRate, entry.row.Failures, entry.row.AverageLatencyMS)
		rows = append(rows, entry.row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		leftSeverity := toneSeverity(rows[i].Tone)
		rightSeverity := toneSeverity(rows[j].Tone)
		if leftSeverity != rightSeverity {
			return leftSeverity > rightSeverity
		}
		if rows[i].Failures != rows[j].Failures {
			return rows[i].Failures > rows[j].Failures
		}
		return rows[i].Calls > rows[j].Calls
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func buildFailureSources(stats []store.FailureSourceStat, limit int) []FailureSource {
	rows := make([]FailureSource, 0, len(stats))
	for _, stat := range stats {
		tone := "warn"
		failureRate := rate(stat.FailureCalls, stat.Calls)
		if failureRate >= 0.5 || stat.FailureCalls >= 5 {
			tone = "bad"
		}
		rows = append(rows, FailureSource{
			Source:               stat.Source,
			SourceHash:           stat.SourceHash,
			AuthIndex:            stat.AuthIndex,
			AccountSnapshot:      stat.AccountSnapshot,
			AuthLabelSnapshot:    stat.AuthLabelSnapshot,
			AuthProviderSnapshot: stat.AuthProviderSnapshot,
			Calls:                stat.Calls,
			Failures:             stat.FailureCalls,
			FailureRate:          failureRate,
			LastSeenMS:           stat.LastSeenMS,
			AverageLatencyMS:     nullableFloat(stat.AvgLatencyMS.Valid, stat.AvgLatencyMS.Float64),
			Tone:                 tone,
		})
	}
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

func fillChannelHealthSnapshots(row *ChannelHealth, stat store.ChannelModelStat) {
	if row.Source == "" {
		row.Source = stat.Source
	}
	if row.AccountSnapshot == "" {
		row.AccountSnapshot = stat.AccountSnapshot
	}
	if row.AuthLabelSnapshot == "" {
		row.AuthLabelSnapshot = stat.AuthLabelSnapshot
	}
	if row.AuthProviderSnapshot == "" {
		row.AuthProviderSnapshot = stat.AuthProviderSnapshot
	}
}

func buildRecentFailures(failures []store.RecentFailure) []RecentFailure {
	result := make([]RecentFailure, 0, len(failures))
	for _, failure := range failures {
		result = append(result, RecentFailure{
			TimestampMS:            failure.TimestampMS,
			Model:                  failure.Model,
			APIKeyHash:             failure.APIKeyHash,
			Source:                 failure.Source,
			SourceHash:             failure.SourceHash,
			AuthIndex:              failure.AuthIndex,
			AccountSnapshot:        failure.AccountSnapshot,
			AuthLabelSnapshot:      failure.AuthLabelSnapshot,
			AuthProviderSnapshot:   failure.AuthProviderSnapshot,
			AuthProjectIDSnapshot:  failure.AuthProjectIDSnapshot,
			Endpoint:               failure.Endpoint,
			DurationMS:             nullableInt(failure.LatencyMS.Valid, failure.LatencyMS.Int64),
			FailStatusCode:         nullableInt(failure.FailStatusCode.Valid, failure.FailStatusCode.Int64),
			FailSummary:            failure.FailSummary,
			ResponseMetadata:       failure.ResponseMetadata,
			HeaderQuotaRecoverAtMS: nullableInt(failure.HeaderQuotaRecoverAtMS.Valid, failure.HeaderQuotaRecoverAtMS.Int64),
			HeaderQuotaUsedPercent: nullableFloat(failure.HeaderQuotaUsedPercent.Valid, failure.HeaderQuotaUsedPercent.Float64),
			HeaderQuotaPlanType:    failure.HeaderQuotaPlanType,
			HeaderErrorKind:        failure.HeaderErrorKind,
			HeaderErrorCode:        failure.HeaderErrorCode,
			HeaderTraceID:          failure.HeaderTraceID,
		})
	}
	return result
}

func totalCost(stats []store.ModelStat, prices map[string]store.ModelPrice) float64 {
	total := 0.0
	for _, stat := range stats {
		total += costForStat(stat, prices)
	}
	return total
}

type aggregatedModelStat struct {
	Model        string
	Calls        int64
	SuccessCalls int64
	TotalTokens  int64
	Cost         float64
}

func aggregateModelStats(stats []store.ModelStat, prices map[string]store.ModelPrice) []aggregatedModelStat {
	grouped := make(map[string]*aggregatedModelStat, len(stats))
	order := make([]string, 0, len(stats))
	for _, stat := range stats {
		entry := grouped[stat.Model]
		if entry == nil {
			entry = &aggregatedModelStat{Model: stat.Model}
			grouped[stat.Model] = entry
			order = append(order, stat.Model)
		}
		entry.Calls += stat.Calls
		entry.SuccessCalls += stat.SuccessCalls
		entry.TotalTokens += stat.TotalTokens
		entry.Cost += costForStat(stat, prices)
	}
	result := make([]aggregatedModelStat, 0, len(order))
	for _, model := range order {
		result = append(result, *grouped[model])
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Calls > result[j].Calls
	})
	return result
}

func costForStat(stat store.ModelStat, prices map[string]store.ModelPrice) float64 {
	return pricing.CostForModelCandidatesWithServiceTier([]string{stat.BillingModel, stat.Model}, stat.ServiceTier, pricing.ModelTokens{
		InputTokens:             stat.InputTokens,
		OutputTokens:            stat.OutputTokens,
		CachedTokens:            stat.CachedTokens,
		CacheReadTokens:         stat.CacheReadTokens,
		CacheCreationTokens:     stat.CacheCreationTokens,
		LongInputTokens:         stat.LongInputTokens,
		LongOutputTokens:        stat.LongOutputTokens,
		LongCachedTokens:        stat.LongCachedTokens,
		LongCacheReadTokens:     stat.LongCacheReadTokens,
		LongCacheCreationTokens: stat.LongCacheCreationTokens,
	}, prices)
}

func costForChannelStat(stat store.ChannelModelStat, prices map[string]store.ModelPrice) float64 {
	return pricing.CostForModelCandidatesWithServiceTier([]string{stat.BillingModel, stat.Model}, stat.ServiceTier, pricing.ModelTokens{
		InputTokens:             stat.InputTokens,
		OutputTokens:            stat.OutputTokens,
		CachedTokens:            stat.CachedTokens,
		CacheReadTokens:         stat.CacheReadTokens,
		CacheCreationTokens:     stat.CacheCreationTokens,
		LongInputTokens:         stat.LongInputTokens,
		LongOutputTokens:        stat.LongOutputTokens,
		LongCachedTokens:        stat.LongCachedTokens,
		LongCacheReadTokens:     stat.LongCacheReadTokens,
		LongCacheCreationTokens: stat.LongCacheCreationTokens,
	}, prices)
}

func rate(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(part) / float64(total)
}

func rateFloat(part, total float64) float64 {
	if total <= 0 {
		return 0
	}
	return part / total
}

func healthTone(successRate float64, failures int64, averageLatencyMS *float64) string {
	latency := 0.0
	if averageLatencyMS != nil {
		latency = *averageLatencyMS
	}
	if successRate < 0.85 || failures >= 5 || latency >= 30000 {
		return "bad"
	}
	if successRate < 0.95 || failures > 0 || latency >= 15000 {
		return "warn"
	}
	return "good"
}

func toneSeverity(tone string) int {
	switch tone {
	case "bad":
		return 3
	case "warn":
		return 2
	default:
		return 1
	}
}

func nullableFloat(valid bool, value float64) *float64 {
	if !valid {
		return nil
	}
	return &value
}

func nullableInt(valid bool, value int64) *int64 {
	if !valid {
		return nil
	}
	return &value
}
