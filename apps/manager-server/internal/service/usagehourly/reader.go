package usagehourly

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const (
	hourMS                    = int64(time.Hour / time.Millisecond)
	fallbackLogIntervalMS     = int64(5 * time.Minute / time.Millisecond)
	defaultFallbackLogContext = "usage-hourly-rollup"
)

type Reader struct {
	store              *store.Store
	enabled            bool
	lastFallbackLogMS  atomic.Int64
	fallbackLogContext string
}

type Snapshot struct {
	Aggregate  store.Aggregate
	ModelStats []store.ModelStat

	rows                   []store.UsageHourlyAggregateRow
	fromMS                 int64
	toMS                   int64
	dashboardTimelineReady bool
	analyticsTimelineReady bool
}

type modelStatKey struct {
	model        string
	billingModel string
	serviceTier  string
}

type analyticsTimelineKey struct {
	bucketMS     int64
	model        string
	billingModel string
	serviceTier  string
}

type analyticsTimelineAccumulator struct {
	point        store.TimelinePoint
	latencySumMS int64
}

func New(store *store.Store, enabled bool, logContext ...string) *Reader {
	contextName := defaultFallbackLogContext
	if len(logContext) > 0 && logContext[0] != "" {
		contextName = logContext[0]
	}
	return &Reader{
		store:              store,
		enabled:            enabled,
		fallbackLogContext: contextName,
	}
}

func (r *Reader) Load(ctx context.Context, fromMS, toMS int64) (Snapshot, bool) {
	return r.loadRows(ctx, store.AnalyticsFilter{
		FromMS:        fromMS,
		ToMS:          toMS,
		IncludeFailed: true,
	}, true, true)
}

func (r *Reader) LoadAnalytics(
	ctx context.Context,
	filter store.AnalyticsFilter,
	granularity string,
	location *time.Location,
	needsTimeline bool,
) (Snapshot, bool) {
	if !SupportsAnalyticsFilter(filter) {
		return Snapshot{}, false
	}
	analyticsTimelineReady := needsTimeline && r.CanRepresentAnalyticsTimeline(filter.FromMS, filter.ToMS, granularity, location)
	return r.loadRows(ctx, filter, false, analyticsTimelineReady)
}

// SupportsAnalyticsFilter reports whether the permanent hourly aggregate
// persists every dimension required by the supplied analytics filter.
func SupportsAnalyticsFilter(filter store.AnalyticsFilter) bool {
	return strings.TrimSpace(filter.SearchQuery) == "" &&
		strings.TrimSpace(filter.SearchAPIKeyHash) == "" &&
		len(filter.Providers) == 0 &&
		len(filter.Accounts) == 0 &&
		len(filter.CredentialIDs) == 0 &&
		len(filter.AuthFiles) == 0 &&
		len(filter.AuthIndices) == 0 &&
		len(filter.APIKeyHashes) == 0 &&
		len(filter.SourceHashes) == 0 &&
		len(filter.ProjectIDs) == 0 &&
		len(filter.RequestTypes) == 0 &&
		len(filter.HeaderErrorKinds) == 0 &&
		len(filter.HeaderErrorCodes) == 0 &&
		len(filter.HeaderQuotaPlans) == 0 &&
		len(filter.HeaderTraceIDs) == 0 &&
		filter.MinLatencyMS == 0 &&
		strings.TrimSpace(filter.CacheStatus) == ""
}

func (r *Reader) loadRows(ctx context.Context, filter store.AnalyticsFilter, dashboardTimelineReady bool, analyticsTimelineReady bool) (Snapshot, bool) {
	if !r.enabled {
		return Snapshot{}, false
	}
	if filter.FromMS >= filter.ToMS {
		return Snapshot{
			ModelStats: []store.ModelStat{},
		}, true
	}
	fullStartMS := ceilHourMS(filter.FromMS)
	fullEndMS := floorHourMS(filter.ToMS)
	if fullStartMS >= fullEndMS {
		return Snapshot{}, false
	}

	rows, state, available, err := r.store.UsageHourlyAggregateRows(ctx, store.UsageHourlyAggregateFilter{
		FromMS:          filter.FromMS,
		ToMS:            filter.ToMS,
		Models:          filter.Models,
		IncludeFailed:   filter.IncludeFailed,
		FailedOnly:      filter.FailedOnly,
		CollapseBuckets: !dashboardTimelineReady && !analyticsTimelineReady,
	})
	if err != nil {
		r.logFallback(fmt.Sprintf("permanent hourly rows query failed: %v", err))
		return Snapshot{}, false
	}
	if !available {
		r.logFallback(fmt.Sprintf("permanent hourly aggregate unavailable: schema_version=%d status=%s", state.SchemaVersion, state.Status))
		return Snapshot{}, false
	}
	agg, modelStats := coreFromRows(rows)

	return Snapshot{
		Aggregate:              agg,
		ModelStats:             modelStats,
		rows:                   rows,
		fromMS:                 filter.FromMS,
		toMS:                   filter.ToMS,
		dashboardTimelineReady: dashboardTimelineReady,
		analyticsTimelineReady: analyticsTimelineReady,
	}, true
}

func (r *Reader) DashboardTimeline(ctx context.Context, snapshot Snapshot, fromMS, toMS int64) ([]store.TimelinePoint, bool) {
	if !snapshot.dashboardTimelineReady {
		return nil, false
	}
	if fromMS%hourMS != 0 {
		timeline, err := r.store.HourlyTimelineBetween(ctx, fromMS, toMS)
		if err != nil {
			r.logFallback(fmt.Sprintf("offset timeline query failed: %v", err))
			return nil, false
		}
		return timeline, true
	}

	return dashboardTimelineFromRows(snapshot.rows), true
}

func (r *Reader) AnalyticsTimeline(
	ctx context.Context,
	snapshot Snapshot,
	granularity string,
	location *time.Location,
) ([]store.TimelinePoint, bool) {
	if !snapshot.analyticsTimelineReady {
		return nil, false
	}
	if location == nil {
		location = time.UTC
	}
	if granularity != "day" {
		granularity = "hour"
	}
	if !r.CanRepresentAnalyticsTimeline(snapshot.fromMS, snapshot.toMS, granularity, location) {
		return nil, false
	}

	return analyticsTimelineFromRows(snapshot.rows, granularity, location), true
}

// CanRepresentAnalyticsTimeline reports whether complete UTC hourly rows can
// be mapped to the requested local buckets without splitting an hourly row.
func (r *Reader) CanRepresentAnalyticsTimeline(fromMS, toMS int64, granularity string, location *time.Location) bool {
	if !r.enabled {
		return false
	}
	if location == nil {
		location = time.UTC
	}
	if granularity != "day" {
		granularity = "hour"
	}
	fullStartMS := ceilHourMS(fromMS)
	fullEndMS := floorHourMS(toMS)
	if fullStartMS >= fullEndMS {
		return false
	}
	touchedStartMS := floorHourMS(fromMS)
	touchedEndMS := ceilHourMS(toMS)
	return usage.CanMapUTCWholeHours(touchedStartMS, touchedEndMS, granularity, location)
}

func ceilHourMS(value int64) int64 {
	if value%hourMS == 0 {
		return value
	}
	return value - value%hourMS + hourMS
}

func floorHourMS(value int64) int64 {
	return value - value%hourMS
}

func coreFromRows(rows []store.UsageHourlyAggregateRow) (store.Aggregate, []store.ModelStat) {
	agg := store.Aggregate{}
	modelStats := make(map[modelStatKey]*store.ModelStat)
	var latencySum int64
	for _, row := range rows {
		agg.TotalCalls += row.Calls
		if row.Failed {
			agg.FailureCalls += row.Calls
		} else {
			agg.SuccessCalls += row.Calls
		}
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
		successCalls := int64(0)
		if !row.Failed {
			successCalls = row.Calls
		}
		addModelStat(modelStats, store.ModelStat{
			Model:               row.Model,
			BillingModel:        row.BillingModel,
			ServiceTier:         row.ServiceTier,
			Calls:               row.Calls,
			SuccessCalls:        successCalls,
			InputTokens:         row.InputTokens,
			OutputTokens:        row.OutputTokens,
			ReasoningTokens:     row.ReasoningTokens,
			CachedTokens:        row.CachedTokens,
			CacheReadTokens:     row.CacheReadTokens,
			CacheCreationTokens: row.CacheCreationTokens,
			LongContextTokens:   row.LongContextTokens,
			TotalTokens:         row.TotalTokens,
		})
	}
	if agg.LatencySamples > 0 {
		agg.AvgLatencyMS.Valid = true
		agg.AvgLatencyMS.Float64 = float64(latencySum) / float64(agg.LatencySamples)
	}
	return agg, sortedModelStats(modelStats)
}

func addModelStat(grouped map[modelStatKey]*store.ModelStat, stat store.ModelStat) {
	mapKey := modelStatKey{model: stat.Model, billingModel: stat.BillingModel, serviceTier: stat.ServiceTier}
	entry := grouped[mapKey]
	if entry == nil {
		copy := stat
		grouped[mapKey] = &copy
		return
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

func sortedModelStats(grouped map[modelStatKey]*store.ModelStat) []store.ModelStat {
	result := make([]store.ModelStat, 0, len(grouped))
	for _, stat := range grouped {
		result = append(result, *stat)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Calls != result[j].Calls {
			return result[i].Calls > result[j].Calls
		}
		if result[i].Model != result[j].Model {
			return result[i].Model < result[j].Model
		}
		if result[i].BillingModel != result[j].BillingModel {
			return result[i].BillingModel < result[j].BillingModel
		}
		return result[i].ServiceTier < result[j].ServiceTier
	})
	return result
}

func dashboardTimelineFromRows(rows []store.UsageHourlyAggregateRow) []store.TimelinePoint {
	grouped := make(map[int64]*store.TimelinePoint)
	for _, row := range rows {
		point := grouped[row.BucketMS]
		if point == nil {
			point = &store.TimelinePoint{BucketMS: row.BucketMS}
			grouped[row.BucketMS] = point
		}
		point.Calls += row.Calls
		point.Tokens += row.TotalTokens
		if row.Failed {
			point.Failure += row.Calls
		} else {
			point.Success += row.Calls
		}
	}
	result := make([]store.TimelinePoint, 0, len(grouped))
	for _, point := range grouped {
		result = append(result, *point)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].BucketMS < result[j].BucketMS })
	return result
}

func analyticsTimelineFromRows(rows []store.UsageHourlyAggregateRow, granularity string, location *time.Location) []store.TimelinePoint {
	grouped := make(map[analyticsTimelineKey]*analyticsTimelineAccumulator)
	for _, row := range rows {
		point := store.TimelinePoint{
			LongContextTokens:   row.LongContextTokens,
			BucketMS:            usage.AnalyticsBucketMS(row.BucketMS, granularity, location),
			Model:               row.Model,
			BillingModel:        row.BillingModel,
			ServiceTier:         row.ServiceTier,
			Calls:               row.Calls,
			Tokens:              row.TotalTokens,
			InputTokens:         row.InputTokens,
			OutputTokens:        row.OutputTokens,
			ReasoningTokens:     row.ReasoningTokens,
			CachedTokens:        row.CachedTokens,
			CacheReadTokens:     row.CacheReadTokens,
			CacheCreationTokens: row.CacheCreationTokens,
			LatencySamples:      row.LatencySamples,
		}
		if row.Failed {
			point.Failure = row.Calls
		} else {
			point.Success = row.Calls
		}
		addAnalyticsTimelinePoint(grouped, point, row.LatencySumMS)
	}
	return sortedAnalyticsTimeline(grouped)
}

func addAnalyticsTimelinePoint(grouped map[analyticsTimelineKey]*analyticsTimelineAccumulator, point store.TimelinePoint, latencySumMS int64) {
	mapKey := analyticsTimelineKey{
		bucketMS:     point.BucketMS,
		model:        point.Model,
		billingModel: point.BillingModel,
		serviceTier:  point.ServiceTier,
	}
	entry := grouped[mapKey]
	if entry == nil {
		grouped[mapKey] = &analyticsTimelineAccumulator{
			point:        point,
			latencySumMS: latencySumMS,
		}
		return
	}
	entry.point.Calls += point.Calls
	entry.point.Tokens += point.Tokens
	entry.point.Success += point.Success
	entry.point.Failure += point.Failure
	entry.point.InputTokens += point.InputTokens
	entry.point.OutputTokens += point.OutputTokens
	entry.point.ReasoningTokens += point.ReasoningTokens
	entry.point.CachedTokens += point.CachedTokens
	entry.point.CacheReadTokens += point.CacheReadTokens
	entry.point.CacheCreationTokens += point.CacheCreationTokens
	entry.point.LongInputTokens += point.LongInputTokens
	entry.point.LongOutputTokens += point.LongOutputTokens
	entry.point.LongCachedTokens += point.LongCachedTokens
	entry.point.LongCacheReadTokens += point.LongCacheReadTokens
	entry.point.LongCacheCreationTokens += point.LongCacheCreationTokens
	entry.point.LatencySamples += point.LatencySamples
	entry.latencySumMS += latencySumMS
}

func sortedAnalyticsTimeline(grouped map[analyticsTimelineKey]*analyticsTimelineAccumulator) []store.TimelinePoint {
	result := make([]store.TimelinePoint, 0, len(grouped))
	for _, entry := range grouped {
		if entry.point.LatencySamples > 0 {
			entry.point.AvgLatencyMS.Valid = true
			entry.point.AvgLatencyMS.Float64 = float64(entry.latencySumMS) / float64(entry.point.LatencySamples)
		}
		result = append(result, entry.point)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].BucketMS != result[j].BucketMS {
			return result[i].BucketMS < result[j].BucketMS
		}
		if result[i].Model != result[j].Model {
			return result[i].Model < result[j].Model
		}
		if result[i].BillingModel != result[j].BillingModel {
			return result[i].BillingModel < result[j].BillingModel
		}
		return result[i].ServiceTier < result[j].ServiceTier
	})
	return result
}

func (r *Reader) logFallback(reason string) {
	nowMS := time.Now().UnixMilli()
	for {
		lastMS := r.lastFallbackLogMS.Load()
		if lastMS > 0 && nowMS-lastMS < fallbackLogIntervalMS {
			return
		}
		if r.lastFallbackLogMS.CompareAndSwap(lastMS, nowMS) {
			log.Printf("[%s] falling back to raw usage events: %s", r.fallbackLogContext, reason)
			return
		}
	}
}
