package monitoring

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestAnalyticsQueryGroupBoundsConcurrency(t *testing.T) {
	group := newAnalyticsQueryGroup(context.Background(), 2)
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	var current atomic.Int64
	var maximum atomic.Int64
	for range 4 {
		group.Go(func(ctx context.Context) error {
			running := current.Add(1)
			defer current.Add(-1)
			for {
				observed := maximum.Load()
				if running <= observed || maximum.CompareAndSwap(observed, running) {
					break
				}
			}
			started <- struct{}{}
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}
	<-started
	<-started
	close(release)
	if err := group.Wait(); err != nil {
		t.Fatalf("wait query group: %v", err)
	}
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum concurrency = %d, want 2", got)
	}
}

func TestAnalyticsQueryGroupCancelsSiblingsOnError(t *testing.T) {
	group := newAnalyticsQueryGroup(context.Background(), 2)
	started := make(chan struct{})
	cancelled := make(chan struct{})
	group.Go(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return nil
	})
	<-started
	wantErr := errors.New("query failed")
	group.Go(func(context.Context) error { return wantErr })
	if err := group.Wait(); !errors.Is(err, wantErr) {
		t.Fatalf("wait error = %v, want %v", err, wantErr)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("sibling query did not observe cancellation")
	}
}

func TestAnalyticsQueryGroupReturnsRequestCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	group := newAnalyticsQueryGroup(ctx, 1)
	started := make(chan struct{})
	group.Go(func(queryCtx context.Context) error {
		close(started)
		<-queryCtx.Done()
		return nil
	})
	<-started
	cancel()
	if err := group.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("wait error = %v, want context canceled", err)
	}
}

func TestAnalyticsBuildsIncludedSections(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_000_000_000)
	toMS := fromMS + 2*60*60*1000
	latency := int64(250)

	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"gpt-a": {Prompt: 1, Completion: 2, Cache: 0.5},
	}); err != nil {
		t.Fatalf("save model prices: %v", err)
	}
	_, err := db.InsertEvents(ctx, []usage.Event{
		monitoringEvent("analytics-a", fromMS+1_000, "gpt-a", "auth-1", "source-a", false, 1_000_000, 500_000, 0, 100, 1_500_100, &latency),
		monitoringEvent("analytics-b", fromMS+2_000, "gpt-b", "auth-2", "source-b", true, 10, 20, 0, 0, 30, nil),
		monitoringEvent("analytics-outside", toMS, "gpt-a", "auth-1", "source-a", false, 1, 1, 0, 0, 2, nil),
	})
	if err != nil {
		t.Fatalf("insert events: %v", err)
	}

	includeFailed := true
	resp, err := New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   toMS,
		NowMS:  toMS,
		Filters: Filters{
			IncludeFailed: &includeFailed,
		},
		Include: Include{
			Summary:            true,
			Timeline:           true,
			HourlyDistribution: true,
			ModelShare:         true,
			ChannelShare:       true,
			ModelStats:         true,
			FailureSources:     true,
			TaskBuckets:        true,
			RecentFailures:     5,
			EventsPage:         &EventsPage{Limit: 1},
			Granularity:        "hour",
		},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}

	if resp.Summary == nil || resp.Summary.TotalCalls != 2 || resp.Summary.FailureCalls != 1 {
		t.Fatalf("summary = %#v", resp.Summary)
	}
	if resp.Summary.TotalCost <= 0 {
		t.Fatalf("summary cost = %v", resp.Summary.TotalCost)
	}
	if len(resp.Timeline) == 0 || len(resp.HourlyDistribution) == 0 {
		t.Fatalf("timeline = %#v hourly = %#v", resp.Timeline, resp.HourlyDistribution)
	}
	if len(resp.Timeline) != 1 {
		t.Fatalf("timeline buckets = %#v", resp.Timeline)
	}
	timelinePoint := resp.Timeline[0]
	if timelinePoint.Calls != 2 || timelinePoint.Success != 1 || timelinePoint.Failure != 1 ||
		timelinePoint.InputTokens != 1_000_010 || timelinePoint.OutputTokens != 500_020 ||
		timelinePoint.CachedTokens != 100 || timelinePoint.TotalTokens != 1_500_130 {
		t.Fatalf("timeline metrics = %#v", timelinePoint)
	}
	if timelinePoint.AvgLatencyMS == nil || math.Abs(*timelinePoint.AvgLatencyMS-250) > 0.000001 {
		t.Fatalf("timeline latency = %#v", timelinePoint.AvgLatencyMS)
	}
	if math.Abs(timelinePoint.Cost-1.99995) > 0.000001 {
		t.Fatalf("timeline cost = %v", timelinePoint.Cost)
	}
	if len(resp.ModelStats) != 2 || len(resp.ModelShare) != 2 {
		t.Fatalf("model stats/share = %#v %#v", resp.ModelStats, resp.ModelShare)
	}
	if len(resp.ChannelShare) != 2 {
		t.Fatalf("channel share = %#v", resp.ChannelShare)
	}
	if len(resp.FailureSources) != 1 || resp.FailureSources[0].SourceHash == "" {
		t.Fatalf("failure sources = %#v", resp.FailureSources)
	}
	if len(resp.TaskBuckets) != 2 {
		t.Fatalf("task buckets = %#v", resp.TaskBuckets)
	}
	if len(resp.RecentFailures) != 1 || resp.RecentFailures[0].Model != "gpt-b" {
		t.Fatalf("recent failures = %#v", resp.RecentFailures)
	}
	if resp.Events == nil || len(resp.Events.Items) != 1 || !resp.Events.HasMore {
		t.Fatalf("events page = %#v", resp.Events)
	}
}

func TestAnalyticsHeatmapIncludesTopContributors(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := time.Date(2026, 6, 8, 9, 0, 0, 0, time.UTC).UnixMilli()
	toMS := fromMS + 60*60*1000

	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"gpt-a": {Prompt: 1},
		"gpt-b": {Prompt: 2},
	}); err != nil {
		t.Fatalf("save model prices: %v", err)
	}

	first := monitoringEvent("heatmap-contrib-a1", fromMS+1_000, "gpt-a", "auth-1", "source-a", false, 1_000_000, 0, 0, 0, 1_000_000, nil)
	first.AuthProviderSnapshot = "openai"
	second := monitoringEvent("heatmap-contrib-a2", fromMS+2_000, "gpt-a", "auth-1", "source-a", true, 1_000_000, 0, 0, 0, 1_000_000, nil)
	second.AuthProviderSnapshot = "openai"
	third := monitoringEvent("heatmap-contrib-b1", fromMS+3_000, "gpt-b", "auth-2", "source-b", false, 1_000_000, 0, 0, 0, 1_000_000, nil)
	third.Provider = "anthropic"
	if _, err := db.InsertEvents(ctx, []usage.Event{first, second, third}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS:  fromMS,
		ToMS:    toMS,
		Include: Include{Heatmap: true},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if len(resp.Heatmap) != 1 {
		t.Fatalf("heatmap = %#v", resp.Heatmap)
	}
	point := resp.Heatmap[0]
	if point.Calls != 3 || point.Success != 2 || point.Failure != 1 || point.Tokens != 3_000_000 {
		t.Fatalf("heatmap totals = %#v", point)
	}
	if math.Abs(point.Cost-4) > 0.000001 {
		t.Fatalf("heatmap cost = %v", point.Cost)
	}
	if len(point.ModelContributors) != 2 || point.ModelContributors[0].Key != "gpt-a" {
		t.Fatalf("model contributors = %#v", point.ModelContributors)
	}
	topModel := point.ModelContributors[0]
	if topModel.Calls != 2 || topModel.Success != 1 || topModel.Failure != 1 ||
		math.Abs(topModel.FailureRate-0.5) > 0.000001 || math.Abs(topModel.Share-2.0/3.0) > 0.000001 ||
		math.Abs(topModel.Cost-2) > 0.000001 {
		t.Fatalf("top model contributor = %#v", topModel)
	}
	if len(point.APIKeyContributors) != 2 || point.APIKeyContributors[0].Key != "api-key-auth-1" ||
		point.APIKeyContributors[0].Calls != 2 {
		t.Fatalf("api key contributors = %#v", point.APIKeyContributors)
	}
	if len(point.ProviderContributors) != 2 || point.ProviderContributors[0].Key != "openai" ||
		point.ProviderContributors[0].Calls != 2 {
		t.Fatalf("provider contributors = %#v", point.ProviderContributors)
	}
}

func TestAnalyticsCredentialTimelineBuildsPerCredentialBuckets(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_000_000_000)
	toMS := fromMS + 3*60*60*1000
	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"gpt-a": {Prompt: 1},
	}); err != nil {
		t.Fatalf("save model prices: %v", err)
	}

	first := monitoringEvent("credential-timeline-a1", fromMS+1_000, "gpt-a", "auth-1", "source-a", false, 1_000_000, 0, 0, 0, 1_000_000, nil)
	first.AuthFileSnapshot = "prod.json"
	first.AuthLabelSnapshot = "prod-auth"
	second := monitoringEvent("credential-timeline-a2", fromMS+60*60*1000+1_000, "gpt-a", "auth-1", "source-a", true, 2_000_000, 0, 0, 0, 2_000_000, nil)
	second.AuthFileSnapshot = "prod.json"
	second.AuthLabelSnapshot = "prod-auth"
	third := monitoringEvent("credential-timeline-b1", fromMS+60*60*1000+2_000, "gpt-a", "auth-2", "source-b", false, 3_000_000, 0, 0, 0, 3_000_000, nil)
	third.AuthFileSnapshot = "dev.json"
	third.AuthLabelSnapshot = "dev-auth"
	if _, err := db.InsertEvents(ctx, []usage.Event{first, second, third}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   toMS,
		Include: Include{
			CredentialTimeline: true,
			Granularity:        "hour",
		},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if len(resp.CredentialTimeline) != 3 {
		t.Fatalf("credential timeline = %#v", resp.CredentialTimeline)
	}
	if resp.CredentialTimeline[0].ID != "prod.json" || resp.CredentialTimeline[0].Calls != 1 || resp.CredentialTimeline[0].Failure != 0 {
		t.Fatalf("first credential bucket = %#v", resp.CredentialTimeline[0])
	}
	if resp.CredentialTimeline[1].ID != "prod.json" || resp.CredentialTimeline[1].Calls != 1 || resp.CredentialTimeline[1].Failure != 1 {
		t.Fatalf("second credential bucket = %#v", resp.CredentialTimeline[1])
	}
	if resp.CredentialTimeline[2].ID != "dev.json" || resp.CredentialTimeline[2].Calls != 1 || resp.CredentialTimeline[2].Success != 1 {
		t.Fatalf("third credential bucket = %#v", resp.CredentialTimeline[2])
	}
	if resp.CredentialTimeline[1].Cost <= resp.CredentialTimeline[0].Cost {
		t.Fatalf("credential timeline cost = %#v", resp.CredentialTimeline)
	}
}

func TestAnalyticsAPIKeyTimelineBuildsExactPerKeyBuckets(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_000_000_000)
	toMS := fromMS + 3*60*60*1000
	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"gpt-a": {Prompt: 1},
	}); err != nil {
		t.Fatalf("save model prices: %v", err)
	}

	first := monitoringEvent("api-key-timeline-a1", fromMS+1_000, "gpt-a", "auth-1", "source-a", false, 1_000_000, 0, 0, 0, 1_000_000, nil)
	second := monitoringEvent("api-key-timeline-a2", fromMS+2_000, "gpt-a", "auth-1", "source-a", true, 2_000_000, 0, 0, 0, 2_000_000, nil)
	third := monitoringEvent("api-key-timeline-b1", fromMS+60*60*1000+1_000, "gpt-a", "auth-2", "source-b", false, 3_000_000, 0, 0, 0, 3_000_000, nil)
	excluded := monitoringEvent("api-key-timeline-c1", fromMS+60*60*1000+2_000, "gpt-a", "auth-3", "source-c", false, 4_000_000, 0, 0, 0, 4_000_000, nil)
	if _, err := db.InsertEvents(ctx, []usage.Event{first, second, third, excluded}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   toMS,
		Filters: Filters{
			APIKeyHashes: []string{"api-key-auth-1", "api-key-auth-2"},
		},
		Include: Include{
			APIKeyTimeline: true,
			Granularity:    "hour",
		},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if len(resp.APIKeyTimeline) != 2 {
		t.Fatalf("api key timeline = %#v", resp.APIKeyTimeline)
	}
	byKeyBucket := make(map[string]APIKeyTimelinePoint, len(resp.APIKeyTimeline))
	for _, point := range resp.APIKeyTimeline {
		byKeyBucket[fmt.Sprintf("%s/%d", point.APIKeyHash, point.BucketMS)] = point
	}
	firstBucketMS := time.UnixMilli(fromMS).UTC().Truncate(time.Hour).UnixMilli()
	secondBucketMS := time.UnixMilli(fromMS + 60*60*1000).UTC().Truncate(time.Hour).UnixMilli()
	firstBucket := byKeyBucket[fmt.Sprintf("api-key-auth-1/%d", firstBucketMS)]
	if firstBucket.Calls != 2 || firstBucket.Success != 1 || firstBucket.Failure != 1 || firstBucket.TotalTokens != 3_000_000 {
		t.Fatalf("first api key bucket = %#v", firstBucket)
	}
	if firstBucket.Cost <= 0 {
		t.Fatalf("first api key bucket cost = %#v", firstBucket)
	}
	secondBucket := byKeyBucket[fmt.Sprintf("api-key-auth-2/%d", secondBucketMS)]
	if secondBucket.Calls != 1 || secondBucket.Success != 1 || secondBucket.Failure != 0 || secondBucket.TotalTokens != 3_000_000 {
		t.Fatalf("second api key bucket = %#v", secondBucket)
	}
}

func TestAnalyticsSummaryComparisonReturnsPreviousPeriod(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"gpt-a": {Prompt: 1, Completion: 2, Cache: 0.5},
	}); err != nil {
		t.Fatalf("save model prices: %v", err)
	}
	fromMS := int64(1_778_000_000_000)
	toMS := fromMS + 2*60*60*1000
	windowMS := toMS - fromMS
	prevFrom := fromMS - windowMS

	// Current window: 2 calls. Previous window: 3 calls (2 success, 1 failure).
	if _, err := db.InsertEvents(ctx, []usage.Event{
		monitoringEvent("cur-1", fromMS+1_000, "gpt-a", "auth-1", "src-a", false, 100, 50, 0, 0, 150, nil),
		monitoringEvent("cur-2", fromMS+2_000, "gpt-a", "auth-1", "src-a", false, 100, 50, 0, 0, 150, nil),
		monitoringEvent("prev-1", prevFrom+1_000, "gpt-a", "auth-1", "src-a", false, 1_000, 500, 0, 0, 1_500, nil),
		monitoringEvent("prev-2", prevFrom+2_000, "gpt-a", "auth-1", "src-a", false, 1_000, 500, 0, 0, 1_500, nil),
		monitoringEvent("prev-3", prevFrom+3_000, "gpt-a", "auth-1", "src-a", true, 1_000, 500, 0, 0, 1_500, nil),
	}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS:  fromMS,
		ToMS:    toMS,
		NowMS:   toMS,
		Include: Include{Summary: true, SummaryComparison: true},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if resp.Summary == nil || resp.Summary.TotalCalls != 2 {
		t.Fatalf("current summary = %#v", resp.Summary)
	}
	cmp := resp.SummaryComparison
	if cmp == nil {
		t.Fatalf("summary_comparison is nil")
	}
	if cmp.FromMS != prevFrom || cmp.ToMS != fromMS {
		t.Fatalf("comparison window = [%d,%d), want [%d,%d)", cmp.FromMS, cmp.ToMS, prevFrom, fromMS)
	}
	if cmp.TotalCalls != 3 || cmp.SuccessCalls != 2 || cmp.FailureCalls != 1 {
		t.Fatalf("comparison calls = %#v", cmp)
	}
	if cmp.TotalTokens != 4_500 {
		t.Fatalf("comparison tokens = %d", cmp.TotalTokens)
	}
	if cmp.TotalCost <= 0 {
		t.Fatalf("comparison cost = %v", cmp.TotalCost)
	}
	if math.Abs(cmp.SuccessRate-2.0/3.0) > 0.000001 {
		t.Fatalf("comparison success rate = %v", cmp.SuccessRate)
	}

	// Without the explicit flag, no comparison is computed.
	respNoCmp, err := New(db).Analytics(ctx, Request{
		FromMS:  fromMS,
		ToMS:    toMS,
		NowMS:   toMS,
		Include: Include{Summary: true},
	})
	if err != nil {
		t.Fatalf("analytics (no comparison): %v", err)
	}
	if respNoCmp.SummaryComparison != nil {
		t.Fatalf("expected nil comparison, got %#v", respNoCmp.SummaryComparison)
	}
}

func TestAnalyticsCompactSummaryPreservesCoreAndSkipsFullMetrics(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"gpt-a":    {Prompt: 1, Completion: 2, Cache: 0.5},
		"gpt-zero": {Prompt: 1, Completion: 2, Cache: 0.5},
	}); err != nil {
		t.Fatalf("save model prices: %v", err)
	}
	fromMS := int64(1_778_000_000_000)
	toMS := fromMS + 48*60*60*1000
	prevFromMS := fromMS - (toMS - fromMS)
	latency100 := int64(100)
	latency300 := int64(300)
	first := monitoringEvent("compact-first", fromMS+1_000, "gpt-a", "auth-1", "src-a", false, 100, 50, 0, 0, 150, &latency100)
	first.TTFTMS = &latency100
	first.CacheReadTokens = 20
	first.CacheCreationTokens = 5
	second := monitoringEvent("compact-second", toMS-60_000, "gpt-zero", "auth-2", "src-b", false, 0, 0, 0, 0, 0, &latency300)
	second.TTFTMS = &latency300
	previous := monitoringEvent("compact-previous", prevFromMS+1_000, "gpt-a", "auth-1", "src-a", false, 200, 100, 10, 0, 310, &latency300)
	previous.TTFTMS = &latency300
	if _, err := db.InsertEvents(ctx, []usage.Event{first, second, previous}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	service := New(db, false)
	request := func(profile string, percentiles bool, taskBuckets bool) Response {
		t.Helper()
		resp, err := service.Analytics(ctx, Request{
			FromMS: fromMS,
			ToMS:   toMS,
			NowMS:  toMS,
			Include: Include{
				Summary:            true,
				SummaryProfile:     profile,
				SummaryPercentiles: percentiles,
				SummaryComparison:  true,
				TaskBuckets:        taskBuckets,
			},
		})
		if err != nil {
			t.Fatalf("analytics profile %q: %v", profile, err)
		}
		return resp
	}

	full := request("", false, false)
	compact := request("compact", true, false)
	compactWithoutPercentiles := request("compact", false, false)
	unknown := request("future-profile", false, false)
	if full.Summary == nil || compact.Summary == nil || unknown.Summary == nil {
		t.Fatalf("missing summary: full=%#v compact=%#v unknown=%#v", full.Summary, compact.Summary, unknown.Summary)
	}
	coreSummary := func(summary *Summary) Summary {
		core := *summary
		core.RPM30M = 0
		core.TPM30M = 0
		core.AvgDailyRequests = 0
		core.AvgDailyTokens = 0
		core.ApproxTasks = 0
		core.ApproxTaskFailures = 0
		core.ApproxTaskSuccessRate = 0
		core.ZeroTokenModels = nil
		return core
	}
	if !reflect.DeepEqual(coreSummary(full.Summary), coreSummary(compact.Summary)) {
		t.Fatalf("compact core mismatch: full=%#v compact=%#v", full.Summary, compact.Summary)
	}
	if full.Summary.TotalCost <= 0 || full.Summary.AverageCostPerCall <= 0 || full.Summary.AverageLatencyMS == nil || full.Summary.CacheHitRate <= 0 {
		t.Fatalf("full core fixture is not meaningful: %#v", full.Summary)
	}
	if full.SummaryComparison == nil || compact.SummaryComparison == nil {
		t.Fatalf("missing comparison: full=%#v compact=%#v", full.SummaryComparison, compact.SummaryComparison)
	}
	if !reflect.DeepEqual(full.SummaryComparison, compact.SummaryComparison) {
		t.Fatalf("compact comparison mismatch: full=%#v compact=%#v", full.SummaryComparison, compact.SummaryComparison)
	}
	if full.SummaryComparison.TotalCost <= 0 {
		t.Fatalf("comparison fixture has no cost: %#v", full.SummaryComparison)
	}
	if full.Summary.RPM30M <= 0 || full.Summary.AvgDailyRequests <= 0 || full.Summary.ApproxTasks <= 0 || len(full.Summary.ZeroTokenModels) != 1 {
		t.Fatalf("full summary metrics = %#v", full.Summary)
	}
	if compact.Summary.RPM30M != 0 || compact.Summary.TPM30M != 0 || compact.Summary.AvgDailyRequests != 0 ||
		compact.Summary.AvgDailyTokens != 0 || compact.Summary.ApproxTasks != 0 ||
		compact.Summary.ApproxTaskFailures != 0 || compact.Summary.ApproxTaskSuccessRate != 0 ||
		compact.Summary.ZeroTokenModels != nil {
		t.Fatalf("compact full-only metrics = %#v", compact.Summary)
	}
	if !reflect.DeepEqual(unknown.Summary, full.Summary) {
		t.Fatalf("unknown profile did not preserve full behavior: unknown=%#v full=%#v", unknown.Summary, full.Summary)
	}
	if compactWithoutPercentiles.Summary == nil || compactWithoutPercentiles.Summary.P95LatencyMS != nil || compactWithoutPercentiles.Summary.P95TTFTMS != nil {
		t.Fatalf("compact summary unexpectedly computed percentiles: %#v", compactWithoutPercentiles.Summary)
	}

	compactWithTasks := request("compact", false, true)
	if len(compactWithTasks.TaskBuckets) == 0 {
		t.Fatal("compact summary dropped explicitly requested task buckets")
	}
	if compactWithTasks.Summary == nil || compactWithTasks.Summary.ApproxTasks != 0 {
		t.Fatalf("compact summary leaked task diagnostics: %#v", compactWithTasks.Summary)
	}
}

func TestCacheHitRateMatchesWebClient(t *testing.T) {
	// Repository aggregates expose normalized total input for Anthropic-style usage.
	anthropic := cacheHitRate(TimelinePoint{
		InputTokens:         450,
		CacheReadTokens:     300,
		CacheCreationTokens: 50,
	})
	if math.Abs(anthropic-300.0/450.0) > 1e-9 {
		t.Fatalf("anthropic cache hit rate = %v, want %v", anthropic, 300.0/450.0)
	}
	// OpenAI-style: InputTokens already includes cache; cacheRead falls back to cachedTokens.
	openai := cacheHitRate(TimelinePoint{
		InputTokens:  1000,
		CachedTokens: 400,
	})
	if math.Abs(openai-0.4) > 1e-9 {
		t.Fatalf("openai cache hit rate = %v, want 0.4", openai)
	}
	// No input -> 0; malformed cached > input clamps to 1.
	if r := cacheHitRate(TimelinePoint{}); r != 0 {
		t.Fatalf("empty cache hit rate = %v, want 0", r)
	}
	if r := cacheHitRate(TimelinePoint{InputTokens: 10, CachedTokens: 1000}); r != 1 {
		t.Fatalf("clamped cache hit rate = %v, want 1", r)
	}

	gpt56 := cacheHitRateForModelStats([]store.ModelStat{{
		Model:               "alias-fast",
		BillingModel:        "openai/gpt-5.6-sol",
		InputTokens:         152_600,
		CacheReadTokens:     151_000,
		CacheCreationTokens: 1_000,
	}})
	if math.Abs(gpt56-151_000.0/152_600.0) > 1e-9 {
		t.Fatalf("gpt-5.6 cache hit rate = %v, want %v", gpt56, 151_000.0/152_600.0)
	}
	timeline := buildTimeline([]store.TimelinePoint{{
		BucketMS:            1_000,
		Model:               "alias-fast",
		BillingModel:        "openai/gpt-5.6-sol",
		Calls:               1,
		Success:             1,
		InputTokens:         152_600,
		CacheReadTokens:     151_000,
		CacheCreationTokens: 1_000,
	}}, nil, "hour", time.UTC, nil)
	if len(timeline) != 1 || math.Abs(timeline[0].CacheHitRate-151_000.0/152_600.0) > 1e-9 {
		t.Fatalf("gpt-5.6 timeline cache hit rate = %#v", timeline)
	}
}

func TestBuildTimelineIsIndependentOfPointOrder(t *testing.T) {
	points := []store.TimelinePoint{
		{BucketMS: 1_000, Model: "z-big", BillingModel: "z-big", Calls: 1, Success: 1, InputTokens: 10_000_000_000_000_000},
		{BucketMS: 1_000, Model: "a-small", BillingModel: "a-small", Calls: 1, Success: 1, InputTokens: 1},
		{BucketMS: 1_000, Model: "b-small", BillingModel: "b-small", Calls: 1, Success: 1, InputTokens: 1},
	}
	prices := map[string]store.ModelPrice{
		"z-big":   {Prompt: 1_000_000},
		"a-small": {Prompt: 1_000_000},
		"b-small": {Prompt: 1_000_000},
	}

	forward := buildTimeline(points, nil, "hour", time.UTC, prices)
	reverse := buildTimeline([]store.TimelinePoint{points[2], points[1], points[0]}, nil, "hour", time.UTC, prices)
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("timeline changed with point order\nforward=%#v\nreverse=%#v", forward, reverse)
	}
}

func TestModelCacheHitRateUsesBillingModelBeforeAliasAggregation(t *testing.T) {
	stats := []store.ModelStat{
		{
			Model:           "internal-fast",
			BillingModel:    "openai/gpt-5.6-sol",
			Calls:           1,
			SuccessCalls:    1,
			InputTokens:     100,
			CacheReadTokens: 90,
		},
		{
			Model:               "internal-fast",
			BillingModel:        "claude-sonnet-4",
			Calls:               1,
			SuccessCalls:        1,
			InputTokens:         200,
			CacheReadTokens:     50,
			CacheCreationTokens: 50,
		},
	}

	rows := buildModelStats(stats, nil)
	if len(rows) != 1 || rows[0].CacheHitTokens != 140 || rows[0].CacheHitInputTokens != 300 ||
		math.Abs(rows[0].CacheHitRate-140.0/300.0) > 1e-9 {
		t.Fatalf("model cache hit metrics = %#v", rows)
	}

	models := map[string]*AccountModelStatRow{}
	addAccountModelStat(models, "internal-fast", "openai/gpt-5.6-sol", 1, 1, 0, 100, 0, 0, 90, 0, 100, 0, 1)
	if model := models["internal-fast"]; model == nil || model.CacheHitInputTokens != 100 ||
		math.Abs(model.CacheHitRate-0.9) > 1e-9 {
		t.Fatalf("account model cache hit metrics = %#v", model)
	}
}

func TestAnalyticsExposesCPA7118UsageFields(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_000_000_000)
	toMS := fromMS + 60*60*1000
	latency := int64(1500)
	ttft := int64(450)
	event := monitoringEvent("cpa-7118-fields", fromMS+1_000, "client-gpt", "auth-1", "source-a", true, 10, 20, 3, 5, 33, &latency)
	event.ResolvedModel = "gpt-5.4"
	event.ExecutorType = "codex"
	event.ReasoningEffort = "medium"
	event.ServiceTier = "priority"
	event.CacheReadTokens = 4
	event.CacheCreationTokens = 1
	event.TTFTMS = &ttft
	event.FailStatusCode = 429
	event.FailBody = "rate limit exceeded"
	event.FailSummary = "rate limit exceeded"

	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   toMS,
		NowMS:  toMS,
		Include: Include{
			Summary:     true,
			ModelStats:  true,
			TaskBuckets: true,
			EventsPage:  &EventsPage{Limit: 10},
		},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if resp.Summary == nil || resp.Summary.CacheReadTokens != 4 ||
		resp.Summary.CacheCreationTokens != 1 || resp.Summary.CachedTokens != 0 {
		t.Fatalf("summary = %#v", resp.Summary)
	}
	if len(resp.TaskBuckets) != 1 || resp.TaskBuckets[0].CacheReadTokens != 4 ||
		resp.TaskBuckets[0].CacheCreationTokens != 1 || resp.TaskBuckets[0].CachedTokens != 0 {
		t.Fatalf("task buckets = %#v", resp.TaskBuckets)
	}
	if len(resp.ModelStats) != 1 || resp.ModelStats[0].CacheReadTokens != 4 ||
		resp.ModelStats[0].CacheCreationTokens != 1 || resp.ModelStats[0].CachedTokens != 0 {
		t.Fatalf("model stats = %#v", resp.ModelStats)
	}
	if resp.Events == nil || len(resp.Events.Items) != 1 {
		t.Fatalf("events = %#v", resp.Events)
	}
	item := resp.Events.Items[0]
	if item.ExecutorType != "codex" || item.ReasoningEffort != "medium" ||
		item.ServiceTier != "priority" || item.CacheReadTokens != 4 ||
		item.CacheCreationTokens != 1 || item.CachedTokens != 0 || item.FailStatusCode == nil ||
		*item.FailStatusCode != 429 || item.FailSummary != "rate limit exceeded" ||
		item.LatencyMS == nil || *item.LatencyMS != 1500 || item.TTFTMS == nil ||
		*item.TTFTMS != 450 {
		t.Fatalf("event item = %#v", item)
	}
}

func TestAnalyticsKeepsCompatCachedSeparateFromFineGrainedCache(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_000_000_000)
	toMS := fromMS + 60*60*1000
	event := monitoringEvent("claude-cache-mirror", fromMS+1_000, "claude-sonnet", "auth-1", "source-a", false, 100, 20, 0, 500, 120, nil)
	event.CacheReadTokens = 500

	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   toMS,
		NowMS:  toMS,
		Include: Include{
			Summary:     true,
			ModelStats:  true,
			TaskBuckets: true,
			EventsPage:  &EventsPage{Limit: 10},
		},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if resp.Summary == nil || resp.Summary.CachedTokens != 0 || resp.Summary.CacheReadTokens != 500 {
		t.Fatalf("summary cache fields = %#v", resp.Summary)
	}
	if len(resp.ModelStats) != 1 || resp.ModelStats[0].CachedTokens != 0 ||
		resp.ModelStats[0].CacheReadTokens != 500 {
		t.Fatalf("model stats cache fields = %#v", resp.ModelStats)
	}
	if len(resp.TaskBuckets) != 1 || resp.TaskBuckets[0].CachedTokens != 0 ||
		resp.TaskBuckets[0].CacheReadTokens != 500 {
		t.Fatalf("task buckets cache fields = %#v", resp.TaskBuckets)
	}
	if resp.Events == nil || len(resp.Events.Items) != 1 || resp.Events.Items[0].CachedTokens != 0 ||
		resp.Events.Items[0].CacheReadTokens != 500 {
		t.Fatalf("events cache fields = %#v", resp.Events)
	}
}

func TestAnalyticsDoesNotExposeOrSearchRawFailBody(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_000_000_000)
	toMS := fromMS + 60*60*1000
	event := monitoringEvent("raw-fail-body", fromMS+1_000, "client-gpt", "auth-1", "source-a", true, 1, 1, 0, 0, 2, nil)
	event.FailStatusCode = 500
	event.FailBody = "upstream stack raw-secret-marker sk-test-secret-value"
	event.FailSummary = "upstream stack [redacted]"

	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS:      fromMS,
		ToMS:        toMS,
		SearchQuery: "raw-secret-marker",
		Include:     Include{EventsPage: &EventsPage{Limit: 10}},
	})
	if err != nil {
		t.Fatalf("analytics raw body search: %v", err)
	}
	if resp.Events != nil && len(resp.Events.Items) != 0 {
		t.Fatalf("raw fail body should not be searchable: %#v", resp.Events)
	}

	resp, err = New(db).Analytics(ctx, Request{
		FromMS:      fromMS,
		ToMS:        toMS,
		SearchQuery: "upstream stack",
		Include:     Include{EventsPage: &EventsPage{Limit: 10}},
	})
	if err != nil {
		t.Fatalf("analytics summary search: %v", err)
	}
	if resp.Events == nil || len(resp.Events.Items) != 1 {
		t.Fatalf("summary search events = %#v", resp.Events)
	}
	item := resp.Events.Items[0]
	if item.FailSummary != "upstream stack [redacted]" {
		t.Fatalf("fail summary = %#v", item)
	}
}

func TestAnalyticsUsesResolvedModelPricingInAggregates(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_000_000_000)
	toMS := fromMS + 60*60*1000

	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"gpt-resolved-a": {Prompt: 1},
		"gpt-resolved-b": {Completion: 2},
	}); err != nil {
		t.Fatalf("save model prices: %v", err)
	}
	first := monitoringEvent("resolved-cost-a", fromMS+1_000, "alias-fast", "auth-1", "source-a", false, 1_000_000, 0, 0, 0, 1_000_000, nil)
	first.ResolvedModel = "gpt-resolved-a"
	second := monitoringEvent("resolved-cost-b", fromMS+2_000, "alias-fast", "auth-1", "source-a", false, 0, 1_000_000, 0, 0, 1_000_000, nil)
	second.ResolvedModel = "gpt-resolved-b"
	if _, err := db.InsertEvents(ctx, []usage.Event{first, second}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   toMS,
		Include: Include{
			Summary:      true,
			ModelShare:   true,
			ModelStats:   true,
			ChannelShare: true,
			Timeline:     true,
		},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}

	if resp.Summary == nil || math.Abs(resp.Summary.TotalCost-3) > 0.000001 {
		t.Fatalf("summary cost = %#v", resp.Summary)
	}
	if len(resp.ModelStats) != 1 || resp.ModelStats[0].Model != "alias-fast" ||
		resp.ModelStats[0].Calls != 2 || math.Abs(resp.ModelStats[0].Cost-3) > 0.000001 {
		t.Fatalf("model stats = %#v", resp.ModelStats)
	}
	if len(resp.ModelShare) != 1 || resp.ModelShare[0].Model != "alias-fast" ||
		math.Abs(resp.ModelShare[0].Cost-3) > 0.000001 {
		t.Fatalf("model share = %#v", resp.ModelShare)
	}
	if len(resp.ChannelShare) != 1 || resp.ChannelShare[0].AuthIndex != "auth-1" ||
		math.Abs(resp.ChannelShare[0].Cost-3) > 0.000001 {
		t.Fatalf("channel share = %#v", resp.ChannelShare)
	}
	if len(resp.Timeline) != 1 || math.Abs(resp.Timeline[0].Cost-3) > 0.000001 {
		t.Fatalf("timeline = %#v", resp.Timeline)
	}
	if resp.ChannelShare[0].Source != "user@example.com" ||
		resp.ChannelShare[0].AccountSnapshot != "user@example.com" {
		t.Fatalf("channel share snapshots = %#v", resp.ChannelShare[0])
	}
}

func TestAnalyticsFallsBackToRequestedModelPriceWhenResolvedPriceIsMissing(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_005_000_000)
	toMS := fromMS + 60*60*1000

	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"GLM-5.2": {Prompt: 3},
	}); err != nil {
		t.Fatalf("save model prices: %v", err)
	}
	event := monitoringEvent("alias-fallback-cost", fromMS+1_000, "GLM-5.2", "auth-1", "source-a", false, 1_000_000, 0, 0, 0, 1_000_000, nil)
	event.RequestedModel = "GLM-5.2"
	event.ResolvedModel = "zai/glm-5.2"
	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   toMS,
		Include: Include{
			Summary:      true,
			ModelShare:   true,
			ModelStats:   true,
			ChannelShare: true,
			Timeline:     true,
		},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}

	if resp.Summary == nil || math.Abs(resp.Summary.TotalCost-3) > 0.000001 {
		t.Fatalf("summary cost = %#v", resp.Summary)
	}
	if len(resp.ModelStats) != 1 || resp.ModelStats[0].Model != "GLM-5.2" ||
		math.Abs(resp.ModelStats[0].Cost-3) > 0.000001 {
		t.Fatalf("model stats = %#v", resp.ModelStats)
	}
	if len(resp.ModelShare) != 1 || resp.ModelShare[0].Model != "GLM-5.2" ||
		math.Abs(resp.ModelShare[0].Cost-3) > 0.000001 {
		t.Fatalf("model share = %#v", resp.ModelShare)
	}
	if len(resp.ChannelShare) != 1 || resp.ChannelShare[0].AuthIndex != "auth-1" ||
		math.Abs(resp.ChannelShare[0].Cost-3) > 0.000001 {
		t.Fatalf("channel share = %#v", resp.ChannelShare)
	}
	if len(resp.Timeline) != 1 || math.Abs(resp.Timeline[0].Cost-3) > 0.000001 {
		t.Fatalf("timeline = %#v", resp.Timeline)
	}
}

func TestAnalyticsPricesPriorityAndDefaultServiceTiersSeparately(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_010_000_000)
	toMS := fromMS + 60*60*1000

	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"gpt-5.4": {Prompt: 2.5},
	}); err != nil {
		t.Fatalf("save model prices: %v", err)
	}

	latency100 := int64(100)
	latency200 := int64(200)
	latency1000 := int64(1000)
	standard := monitoringEvent("tier-default", fromMS+1_000, "gpt-5.4", "auth-1", "source-a", false, 1_000_000, 0, 0, 0, 1_000_000, &latency100)
	standard.ServiceTier = "default"
	standard.AccountSnapshot = "team@example.com"
	standard.AuthLabelSnapshot = "Team"
	standard.APIKeyHash = "client-key"
	standardSecond := monitoringEvent("tier-default-second", fromMS+1_500, "gpt-5.4", "auth-1", "source-a", false, 0, 0, 0, 0, 0, &latency200)
	standardSecond.ServiceTier = "default"
	standardSecond.AccountSnapshot = "team@example.com"
	standardSecond.AuthLabelSnapshot = "Team"
	standardSecond.APIKeyHash = "client-key"
	priority := monitoringEvent("tier-priority", fromMS+2_000, "gpt-5.4", "auth-1", "source-a", false, 1_000_000, 0, 0, 0, 1_000_000, &latency1000)
	priority.ServiceTier = "priority"
	priority.AccountSnapshot = "team@example.com"
	priority.AuthLabelSnapshot = "Team"
	priority.APIKeyHash = "client-key"
	if _, err := db.InsertEvents(ctx, []usage.Event{standard, standardSecond, priority}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   toMS,
		Include: Include{
			Summary:      true,
			ModelShare:   true,
			ModelStats:   true,
			ChannelShare: true,
			AccountStats: true,
			APIKeyStats:  true,
		},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}

	assertCost := func(name string, got float64) {
		t.Helper()
		if math.Abs(got-10) > 0.000001 {
			t.Fatalf("%s cost = %v, want 10", name, got)
		}
	}
	if resp.Summary == nil {
		t.Fatal("summary is nil")
	}
	assertCost("summary", resp.Summary.TotalCost)
	if len(resp.ModelStats) != 1 || resp.ModelStats[0].Calls != 3 {
		t.Fatalf("model stats = %#v", resp.ModelStats)
	}
	assertCost("model stats", resp.ModelStats[0].Cost)
	if len(resp.ModelShare) != 1 {
		t.Fatalf("model share = %#v", resp.ModelShare)
	}
	assertCost("model share", resp.ModelShare[0].Cost)
	if len(resp.ChannelShare) != 1 {
		t.Fatalf("channel share = %#v", resp.ChannelShare)
	}
	assertCost("channel share", resp.ChannelShare[0].Cost)
	if resp.ChannelShare[0].AvgLatencyMS == nil || math.Abs(*resp.ChannelShare[0].AvgLatencyMS-(1300.0/3.0)) > 0.000001 {
		t.Fatalf("channel latency = %#v, want weighted 433.333333", resp.ChannelShare[0].AvgLatencyMS)
	}
	if len(resp.AccountStats) != 1 || len(resp.AccountStats[0].Models) != 1 {
		t.Fatalf("account stats = %#v", resp.AccountStats)
	}
	assertCost("account stats", resp.AccountStats[0].Cost)
	assertCost("account model stats", resp.AccountStats[0].Models[0].Cost)
	if len(resp.APIKeyStats) != 1 || len(resp.APIKeyStats[0].Models) != 1 {
		t.Fatalf("api key stats = %#v", resp.APIKeyStats)
	}
	assertCost("api key stats", resp.APIKeyStats[0].Cost)
	assertCost("api key model stats", resp.APIKeyStats[0].Models[0].Cost)
}

func TestAnalyticsPricesGPT56LongContextPerRequest(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_020_000_000)
	toMS := fromMS + 60*60*1000

	short := monitoringEvent("gpt-56-short", fromMS+1_000, "gpt-5.6-sol", "auth-1", "source-a", false, 272_000, 0, 0, 0, 272_000, nil)
	long := monitoringEvent("gpt-56-long", fromMS+2_000, "gpt-5.6-sol", "auth-1", "source-a", false, 272_001, 0, 0, 0, 272_001, nil)
	for _, event := range []*usage.Event{&short, &long} {
		event.AccountSnapshot = "team@example.com"
		event.AuthLabelSnapshot = "Team"
		event.APIKeyHash = "client-key"
	}
	if _, err := db.InsertEvents(ctx, []usage.Event{short, long}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   toMS,
		Include: Include{
			Summary:      true,
			ModelStats:   true,
			ChannelShare: true,
			Timeline:     true,
			AccountStats: true,
			APIKeyStats:  true,
		},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}

	const want = 4.08001
	assertCost := func(name string, got float64) {
		t.Helper()
		if math.Abs(got-want) > 0.000001 {
			t.Fatalf("%s cost = %v, want %v", name, got, want)
		}
	}
	if resp.Summary == nil {
		t.Fatal("summary is nil")
	}
	assertCost("summary", resp.Summary.TotalCost)
	if len(resp.ModelStats) != 1 || len(resp.ChannelShare) != 1 || len(resp.Timeline) != 1 {
		t.Fatalf("analytics rows = %#v %#v %#v", resp.ModelStats, resp.ChannelShare, resp.Timeline)
	}
	assertCost("model stats", resp.ModelStats[0].Cost)
	assertCost("channel share", resp.ChannelShare[0].Cost)
	assertCost("timeline", resp.Timeline[0].Cost)
	if len(resp.AccountStats) != 1 || len(resp.APIKeyStats) != 1 {
		t.Fatalf("identity stats = %#v %#v", resp.AccountStats, resp.APIKeyStats)
	}
	assertCost("account stats", resp.AccountStats[0].Cost)
	assertCost("api key stats", resp.APIKeyStats[0].Cost)
}

func TestAnalyticsAppliesFilters(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_000_000_000)
	toMS := fromMS + 60*60*1000
	includeFailed := false

	_, err := db.InsertEvents(ctx, []usage.Event{
		monitoringEvent("filter-a", fromMS+1_000, "gpt-a", "auth-1", "source-a", false, 1, 1, 0, 0, 2, nil),
		monitoringEvent("filter-b", fromMS+2_000, "gpt-a", "auth-1", "source-a", true, 1, 1, 0, 0, 2, nil),
		monitoringEvent("filter-c", fromMS+3_000, "gpt-b", "auth-2", "source-b", false, 1, 1, 0, 0, 2, nil),
	})
	if err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   toMS,
		Filters: Filters{
			Models:        []string{"gpt-a"},
			AuthIndices:   []string{"auth-1"},
			IncludeFailed: &includeFailed,
		},
		Include: Include{Summary: true, EventsPage: &EventsPage{Limit: 10}},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if resp.Summary == nil || resp.Summary.TotalCalls != 1 || resp.Summary.FailureCalls != 0 {
		t.Fatalf("filtered summary = %#v", resp.Summary)
	}
	if resp.Events == nil || len(resp.Events.Items) != 1 || resp.Events.Items[0].EventHash != "filter-a" {
		t.Fatalf("filtered events = %#v", resp.Events)
	}

	includeFailed = true
	resp, err = New(db).Analytics(ctx, Request{
		FromMS:           fromMS,
		ToMS:             toMS,
		SearchQuery:      "raw-api-key",
		SearchAPIKeyHash: "api-key-auth-2",
		Filters: Filters{
			IncludeFailed: &includeFailed,
		},
		Include: Include{Summary: true, EventsPage: &EventsPage{Limit: 10}},
	})
	if err != nil {
		t.Fatalf("analytics api key hash search: %v", err)
	}
	if resp.Events == nil || len(resp.Events.Items) != 1 || resp.Events.Items[0].EventHash != "filter-c" {
		t.Fatalf("api key hash search events = %#v", resp.Events)
	}
}

func TestBuildFilterIncludesCredentialIDs(t *testing.T) {
	filter := buildFilter(Request{
		FromMS: 100,
		ToMS:   200,
		Filters: Filters{
			CredentialIDs: []string{"credential-a"},
		},
	})
	if !reflect.DeepEqual(filter.CredentialIDs, []string{"credential-a"}) {
		t.Fatalf("credential ids = %#v", filter.CredentialIDs)
	}
}

func TestAnalyticsAccountAndAPIKeyStatsUseFullFilteredScope(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_050_000_000)
	toMS := fromMS + 60*60*1000

	events := []usage.Event{
		monitoringEvent("scope-a", fromMS+1_000, "gpt-a", "auth-1", "source-a", false, 10, 5, 0, 0, 15, nil),
		monitoringEvent("scope-b", fromMS+2_000, "gpt-a", "auth-1", "source-a", false, 20, 6, 0, 0, 26, nil),
		monitoringEvent("scope-c", fromMS+3_000, "gpt-b", "auth-2", "source-b", true, 1, 1, 0, 0, 2, nil),
	}
	for index := range events {
		events[index].AccountSnapshot = "team@example.com"
		events[index].AuthLabelSnapshot = "Team Account"
		events[index].AuthProviderSnapshot = "codex"
		events[index].APIKeyHash = "client-key-hash"
	}
	if _, err := db.InsertEvents(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   toMS,
		Include: Include{
			Summary:      true,
			AccountStats: true,
			APIKeyStats:  true,
			EventsPage:   &EventsPage{Limit: 1},
		},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if resp.Events == nil || len(resp.Events.Items) != 1 || !resp.Events.HasMore {
		t.Fatalf("events page = %#v", resp.Events)
	}
	if resp.Summary == nil || resp.Summary.TotalCalls != 3 || resp.Summary.FailureCalls != 1 {
		t.Fatalf("summary = %#v", resp.Summary)
	}
	if len(resp.AccountStats) != 1 || resp.AccountStats[0].Calls != 3 ||
		resp.AccountStats[0].FailureCalls != 1 || resp.AccountStats[0].TotalTokens != 43 {
		t.Fatalf("account stats = %#v", resp.AccountStats)
	}
	if len(resp.AccountStats[0].Models) != 2 {
		t.Fatalf("account model stats = %#v", resp.AccountStats[0].Models)
	}
	if len(resp.APIKeyStats) != 1 || resp.APIKeyStats[0].APIKeyHash != "client-key-hash" ||
		resp.APIKeyStats[0].Calls != 3 || resp.APIKeyStats[0].FailureCalls != 1 ||
		resp.APIKeyStats[0].TotalTokens != 43 {
		t.Fatalf("api key stats = %#v", resp.APIKeyStats)
	}
	if len(resp.APIKeyStats[0].Contexts) != 2 {
		t.Fatalf("api key contexts = %#v", resp.APIKeyStats[0].Contexts)
	}
	if resp.APIKeyStats[0].Contexts[0].AuthIndex != "auth-1" ||
		resp.APIKeyStats[0].Contexts[0].Calls != 2 ||
		resp.APIKeyStats[0].Contexts[0].FailureCalls != 0 {
		t.Fatalf("top api key context = %#v", resp.APIKeyStats[0].Contexts[0])
	}
	if resp.APIKeyStats[0].Contexts[1].AuthIndex != "auth-2" ||
		resp.APIKeyStats[0].Contexts[1].Calls != 1 ||
		resp.APIKeyStats[0].Contexts[1].FailureRate != 1 {
		t.Fatalf("second api key context = %#v", resp.APIKeyStats[0].Contexts[1])
	}
}

func TestAnalyticsSearchMatchesResolvedModelAndProjectID(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_000_000_000)
	toMS := fromMS + 60*60*1000

	event := monitoringEvent("search-new-fields", fromMS+1_000, "alias-search", "auth-1", "source-a", false, 1, 1, 0, 0, 2, nil)
	event.RequestID = "req-search-42"
	event.ResolvedModel = "gpt-resolved-search"
	event.AuthProjectIDSnapshot = "vertex-project-42"
	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	for _, query := range []string{"req-search-42", "search-new-fields", "gpt-resolved-search", "vertex-project-42"} {
		resp, err := New(db).Analytics(ctx, Request{
			FromMS:      fromMS,
			ToMS:        toMS,
			SearchQuery: query,
			Include:     Include{EventsPage: &EventsPage{Limit: 10}},
		})
		if err != nil {
			t.Fatalf("analytics search %q: %v", query, err)
		}
		if resp.Events == nil || len(resp.Events.Items) != 1 || resp.Events.Items[0].EventHash != "search-new-fields" {
			t.Fatalf("search %q events = %#v", query, resp.Events)
		}
	}
}

func TestAnalyticsSearchMatchesAccountSnapshotsWhenSourceIsMasked(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_060_000_000)
	toMS := fromMS + 60*60*1000

	alice := monitoringEvent("search-account-alice", fromMS+1_000, "gpt-a", "auth-a", "source-a", false, 1, 1, 0, 0, 2, nil)
	alice.Source = "ali***@example.com"
	alice.AccountSnapshot = "alice.smith@example.com"
	alice.AuthLabelSnapshot = "Alice Work Account"
	alice.AuthFileSnapshot = "alice.json"
	bob := monitoringEvent("search-account-bob", fromMS+2_000, "gpt-b", "auth-b", "source-b", false, 1, 1, 0, 0, 2, nil)
	bob.Source = "ali***@example.com"
	bob.AccountSnapshot = "alina.team@example.com"
	bob.AuthLabelSnapshot = "Alina Work Account"
	bob.AuthFileSnapshot = "alina.json"
	if _, err := db.InsertEvents(ctx, []usage.Event{alice, bob}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	for _, query := range []string{"ALICE.SMITH@example.com", "Alice Work Account", "alice.json"} {
		resp, err := New(db).Analytics(ctx, Request{
			FromMS:      fromMS,
			ToMS:        toMS,
			SearchQuery: query,
			Include:     Include{Summary: true, EventsPage: &EventsPage{Limit: 10}},
		})
		if err != nil {
			t.Fatalf("analytics search %q: %v", query, err)
		}
		if resp.Summary == nil || resp.Summary.TotalCalls != 1 {
			t.Fatalf("search %q summary = %#v", query, resp.Summary)
		}
		if resp.Events == nil || len(resp.Events.Items) != 1 || resp.Events.Items[0].EventHash != "search-account-alice" {
			t.Fatalf("search %q events = %#v", query, resp.Events)
		}
	}
}

func TestAnalyticsReportsZeroTokenModels(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_000_000_000)
	toMS := fromMS + 60*60*1000

	_, err := db.InsertEvents(ctx, []usage.Event{
		monitoringEvent("zero-a", fromMS+1_000, "gpt-zero", "auth-1", "source-a", false, 0, 0, 0, 0, 0, nil),
		monitoringEvent("zero-b", fromMS+2_000, "gpt-failed-zero", "auth-1", "source-a", true, 0, 0, 0, 0, 0, nil),
		monitoringEvent("zero-c", fromMS+3_000, "gpt-nonzero", "auth-1", "source-a", false, 1, 1, 0, 0, 2, nil),
	})
	if err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS:  fromMS,
		ToMS:    toMS,
		Include: Include{Summary: true},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if resp.Summary == nil || len(resp.Summary.ZeroTokenModels) != 1 || resp.Summary.ZeroTokenModels[0] != "gpt-zero" {
		t.Fatalf("zero token models = %#v", resp.Summary)
	}
}

func TestAnalyticsAppliesMinLatencyFilter(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_080_000_000)
	toMS := fromMS + 60*60*1000
	fastLatency := int64(2_000)
	slowLatency := int64(12_000)

	_, err := db.InsertEvents(ctx, []usage.Event{
		monitoringEvent("latency-fast", fromMS+1_000, "gpt-fast", "auth-1", "source-a", false, 1, 1, 0, 0, 2, &fastLatency),
		monitoringEvent("latency-slow", fromMS+2_000, "gpt-slow", "auth-1", "source-a", false, 1, 1, 0, 0, 2, &slowLatency),
		monitoringEvent("latency-unknown", fromMS+3_000, "gpt-unknown", "auth-1", "source-a", false, 1, 1, 0, 0, 2, nil),
	})
	if err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS:  fromMS,
		ToMS:    toMS,
		Filters: Filters{MinLatencyMS: 10_000},
		Include: Include{Summary: true, EventsPage: &EventsPage{Limit: 10}},
	})
	if err != nil {
		t.Fatalf("analytics with min latency filter: %v", err)
	}
	if resp.Summary == nil || resp.Summary.TotalCalls != 1 {
		t.Fatalf("filtered latency summary = %#v", resp.Summary)
	}
	if resp.Events == nil || len(resp.Events.Items) != 1 || resp.Events.Items[0].EventHash != "latency-slow" {
		t.Fatalf("filtered latency events = %#v", resp.Events)
	}
}

func TestAnalyticsAppliesCacheStatusFilter(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_090_000_000)
	toMS := fromMS + 60*60*1000

	cacheRead := monitoringEvent("cache-read", fromMS+1_000, "gpt-a", "auth-1", "source-a", false, 10, 5, 0, 0, 15, nil)
	cacheRead.CacheReadTokens = 4
	cacheCreation := monitoringEvent("cache-creation", fromMS+2_000, "gpt-b", "auth-1", "source-a", false, 10, 5, 0, 0, 15, nil)
	cacheCreation.CacheCreationTokens = 3
	legacyCached := monitoringEvent("cache-legacy", fromMS+3_000, "gpt-c", "auth-1", "source-a", false, 10, 5, 0, 2, 17, nil)
	cacheMiss := monitoringEvent("cache-miss", fromMS+4_000, "gpt-d", "auth-1", "source-a", false, 10, 5, 0, 0, 15, nil)
	if _, err := db.InsertEvents(ctx, []usage.Event{cacheRead, cacheCreation, legacyCached, cacheMiss}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	tests := []struct {
		name       string
		status     string
		wantHashes []string
	}{
		{name: "hit", status: "hit", wantHashes: []string{"cache-legacy", "cache-creation", "cache-read"}},
		{name: "miss", status: "miss", wantHashes: []string{"cache-miss"}},
		{name: "read", status: "read", wantHashes: []string{"cache-read"}},
		{name: "creation", status: "creation", wantHashes: []string{"cache-creation"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, err := New(db).Analytics(ctx, Request{
				FromMS:  fromMS,
				ToMS:    toMS,
				Filters: Filters{CacheStatus: tt.status},
				Include: Include{Summary: true, EventsPage: &EventsPage{Limit: 10}},
			})
			if err != nil {
				t.Fatalf("analytics with cache status filter: %v", err)
			}
			if resp.Summary == nil || int(resp.Summary.TotalCalls) != len(tt.wantHashes) {
				t.Fatalf("filtered cache summary = %#v", resp.Summary)
			}
			if resp.Events == nil || len(resp.Events.Items) != len(tt.wantHashes) {
				t.Fatalf("filtered cache events = %#v", resp.Events)
			}
			for index, want := range tt.wantHashes {
				if resp.Events.Items[index].EventHash != want {
					t.Fatalf("event %d hash = %q, want %q; events = %#v", index, resp.Events.Items[index].EventHash, want, resp.Events)
				}
			}
		})
	}
}

func TestAnalyticsAppliesFailedOnlyFilter(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_100_000_000)
	toMS := fromMS + 60*60*1000

	_, err := db.InsertEvents(ctx, []usage.Event{
		monitoringEvent("status-a", fromMS+1_000, "gpt-ok", "auth-1", "source-a", false, 10, 5, 0, 0, 15, nil),
		monitoringEvent("status-b", fromMS+2_000, "gpt-failed", "auth-1", "source-a", true, 1, 1, 0, 0, 2, nil),
	})
	if err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS:  fromMS,
		ToMS:    toMS,
		Filters: Filters{FailedOnly: true},
		Include: Include{Summary: true, EventsPage: &EventsPage{Limit: 10}},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if resp.Summary == nil || resp.Summary.TotalCalls != 1 || resp.Summary.FailureCalls != 1 {
		t.Fatalf("summary = %#v", resp.Summary)
	}
	if resp.Events == nil || len(resp.Events.Items) != 1 || !resp.Events.Items[0].Failed {
		t.Fatalf("events = %#v", resp.Events)
	}
}

func TestAnalyticsAppliesAccountFallbackFilter(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_200_000_000)
	toMS := fromMS + 60*60*1000

	alice := monitoringEvent("account-alice", fromMS+1_000, "gpt-a", "auth-a", "source-a", false, 10, 5, 0, 0, 15, nil)
	alice.AccountSnapshot = "alice@example.com"
	alice.AuthLabelSnapshot = "Alice Auth"
	alice.Source = "alice-source"
	bob := monitoringEvent("account-bob", fromMS+2_000, "gpt-b", "auth-b", "source-b", false, 10, 5, 0, 0, 15, nil)
	bob.AccountSnapshot = "bob@example.com"
	bob.AuthLabelSnapshot = "Bob Auth"
	bob.Source = "bob-source"

	if _, err := db.InsertEvents(ctx, []usage.Event{alice, bob}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   toMS,
		Filters: Filters{
			Accounts: []string{"alice@example.com"},
		},
		Include: Include{Summary: true, EventsPage: &EventsPage{Limit: 10}},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if resp.Summary == nil || resp.Summary.TotalCalls != 1 || resp.Summary.SuccessCalls != 1 {
		t.Fatalf("summary = %#v", resp.Summary)
	}
	if resp.Events == nil || len(resp.Events.Items) != 1 || resp.Events.Items[0].EventHash != "account-alice" {
		t.Fatalf("events = %#v", resp.Events)
	}

	resp, err = New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   toMS,
		Filters: Filters{
			Accounts: []string{"Alice Auth"},
		},
		Include: Include{Summary: true, EventsPage: &EventsPage{Limit: 10}},
	})
	if err != nil {
		t.Fatalf("analytics auth label account filter: %v", err)
	}
	if resp.Summary == nil || resp.Summary.TotalCalls != 1 {
		t.Fatalf("auth label summary = %#v", resp.Summary)
	}
	if resp.Events == nil || len(resp.Events.Items) != 1 || resp.Events.Items[0].EventHash != "account-alice" {
		t.Fatalf("auth label events = %#v", resp.Events)
	}
}

func TestAnalyticsUnfilteredFullResponseKeepsFilterOptionStatsInSync(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_250_000_000)
	toMS := fromMS + 60*60*1000

	alice := monitoringEvent("reuse-alice", fromMS+1_000, "gpt-a", "auth-a", "source-a", false, 10, 5, 0, 0, 15, nil)
	alice.AccountSnapshot = "alice@example.com"
	alice.AuthLabelSnapshot = "Alice Auth"
	alice.AuthProviderSnapshot = "codex"
	alice.APIKeyHash = "key-alice"
	bob := monitoringEvent("reuse-bob", fromMS+2_000, "gpt-b", "auth-b", "source-b", true, 20, 10, 0, 0, 30, nil)
	bob.AccountSnapshot = "bob@example.com"
	bob.AuthLabelSnapshot = "Bob Auth"
	bob.AuthProviderSnapshot = "gemini"
	bob.APIKeyHash = "key-bob"
	if _, err := db.InsertEvents(ctx, []usage.Event{alice, bob}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   toMS,
		Include: Include{
			ModelStats:    true,
			ChannelShare:  true,
			AccountStats:  true,
			APIKeyStats:   true,
			FilterOptions: true,
		},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if resp.FilterOptions == nil {
		t.Fatal("filter options are nil")
	}
	if !reflect.DeepEqual(resp.FilterOptions.ModelStats, resp.ModelStats) ||
		!reflect.DeepEqual(resp.FilterOptions.ChannelShare, resp.ChannelShare) ||
		!reflect.DeepEqual(resp.FilterOptions.AccountStats, resp.AccountStats) ||
		!reflect.DeepEqual(resp.FilterOptions.APIKeyStats, resp.APIKeyStats) {
		t.Fatalf("filter option stats diverged from unfiltered response: options=%#v response=%#v", resp.FilterOptions, resp)
	}
}

func TestAnalyticsFilterOptionsIgnoreActiveScopeFilters(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_300_000_000)
	toMS := fromMS + 60*60*1000

	alice := monitoringEvent("option-alice", fromMS+1_000, "gpt-a", "auth-a", "source-a", false, 10, 5, 0, 0, 15, nil)
	alice.AccountSnapshot = "alice@example.com"
	alice.AuthLabelSnapshot = "Alice Auth"
	alice.AuthProviderSnapshot = "codex"
	alice.APIKeyHash = "key-alice"
	bob := monitoringEvent("option-bob", fromMS+2_000, "gpt-b", "auth-b", "source-b", false, 10, 5, 0, 0, 15, nil)
	bob.AccountSnapshot = "bob@example.com"
	bob.AuthLabelSnapshot = "Bob Auth"
	bob.AuthProviderSnapshot = "gemini"
	bob.APIKeyHash = "key-bob"

	if _, err := db.InsertEvents(ctx, []usage.Event{alice, bob}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   toMS,
		Filters: Filters{
			Models:   []string{"gpt-a"},
			Accounts: []string{"alice@example.com"},
		},
		Include: Include{
			Summary:       true,
			FilterOptions: true,
		},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if resp.Summary == nil || resp.Summary.TotalCalls != 1 {
		t.Fatalf("summary should respect active filters: %#v", resp.Summary)
	}
	if resp.FilterOptions == nil {
		t.Fatal("filter options are nil")
	}
	if len(resp.FilterOptions.AccountStats) != 2 {
		t.Fatalf("account filter options should ignore active account/model filters: %#v", resp.FilterOptions.AccountStats)
	}
	if len(resp.FilterOptions.APIKeyStats) != 2 {
		t.Fatalf("api key filter options should ignore active account/model filters: %#v", resp.FilterOptions.APIKeyStats)
	}
	if len(resp.FilterOptions.ModelStats) != 2 {
		t.Fatalf("model filter options should ignore active account/model filters: %#v", resp.FilterOptions.ModelStats)
	}
	if len(resp.FilterOptions.ChannelShare) != 2 {
		t.Fatalf("channel/provider filter options should ignore active account/model filters: %#v", resp.FilterOptions.ChannelShare)
	}
}

func TestAnalyticsFilterSelectorsReturnLightweightOptions(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_350_000_000)
	toMS := fromMS + 60*60*1000

	alice := monitoringEvent("selector-alice", fromMS+1_000, "gpt-a", "auth-a", "source-a", false, 10, 5, 0, 0, 15, nil)
	alice.AccountSnapshot = "alice@example.com"
	alice.AuthProviderSnapshot = "codex"
	alice.AuthFileSnapshot = "alice.json"
	alice.APIKeyHash = "key-alice"
	bob := monitoringEvent("selector-bob", fromMS+2_000, "gpt-b", "auth-b", "source-b", false, 10, 5, 0, 0, 15, nil)
	bob.AccountSnapshot = "bob@example.com"
	bob.AuthProviderSnapshot = "gemini"
	bob.AuthFileSnapshot = "bob.json"
	bob.APIKeyHash = "key-bob"
	sourceOnly := monitoringEvent("selector-source-only", fromMS+3_000, "gpt-a", "", "source-only", false, 10, 5, 0, 0, 15, nil)
	sourceOnly.AccountSnapshot = ""
	sourceOnly.AuthLabelSnapshot = ""
	sourceOnly.AuthProviderSnapshot = "openai"
	sourceOnly.AuthFileSnapshot = ""
	sourceOnly.APIKeyHash = ""
	sourceOnly.Source = "k:upstream-key"
	sourceOnlyRotated := monitoringEvent("selector-source-only-rotated", fromMS+4_000, "gpt-b", "", "source-only", false, 10, 5, 0, 0, 15, nil)
	sourceOnlyRotated.AccountSnapshot = ""
	sourceOnlyRotated.AuthLabelSnapshot = ""
	sourceOnlyRotated.AuthProviderSnapshot = "openai"
	sourceOnlyRotated.AuthFileSnapshot = ""
	sourceOnlyRotated.APIKeyHash = ""
	sourceOnlyRotated.Source = "k:z-upstream-key"
	sourceHashOnly := monitoringEvent("selector-source-hash-only", fromMS+5_000, "gpt-a", "", "source-hash-only", false, 10, 5, 0, 0, 15, nil)
	sourceHashOnly.AccountSnapshot = ""
	sourceHashOnly.AuthLabelSnapshot = ""
	sourceHashOnly.AuthProviderSnapshot = "openai"
	sourceHashOnly.AuthFileSnapshot = ""
	sourceHashOnly.APIKeyHash = ""
	sourceHashOnly.Source = ""

	if _, err := db.InsertEvents(ctx, []usage.Event{alice, bob, sourceOnly, sourceOnlyRotated, sourceHashOnly}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   toMS,
		Filters: Filters{
			Models:   []string{"gpt-a"},
			Accounts: []string{"alice@example.com"},
		},
		Include: Include{FilterOptions: true, FilterSelectors: true},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if resp.FilterOptions == nil {
		t.Fatal("filter selectors are nil")
	}
	if !slices.Equal(resp.FilterOptions.Models, []string{"gpt-a", "gpt-b"}) {
		t.Fatalf("models = %#v", resp.FilterOptions.Models)
	}
	if !slices.Equal(resp.FilterOptions.APIKeyHashes, []string{"key-alice", "key-bob"}) {
		t.Fatalf("api key hashes = %#v", resp.FilterOptions.APIKeyHashes)
	}
	if !slices.Equal(resp.FilterOptions.Providers, []string{"codex", "gemini", "openai"}) {
		t.Fatalf("providers = %#v", resp.FilterOptions.Providers)
	}
	if !slices.Equal(resp.FilterOptions.AuthFiles, []string{"alice.json", "bob.json"}) {
		t.Fatalf("auth files = %#v", resp.FilterOptions.AuthFiles)
	}
	if !slices.Equal(resp.FilterOptions.Accounts, []string{"alice@example.com", "bob@example.com"}) {
		t.Fatalf("accounts = %#v", resp.FilterOptions.Accounts)
	}
	if resp.FilterOptions.AccountCount != 4 || resp.FilterOptions.APIKeyCount != 4 {
		t.Fatalf(
			"selector counts account=%d api_key=%d",
			resp.FilterOptions.AccountCount,
			resp.FilterOptions.APIKeyCount,
		)
	}
	if len(resp.FilterOptions.AccountStats) != 4 {
		t.Fatalf("account identity selectors = %#v", resp.FilterOptions.AccountStats)
	}
	var sourceOnlySelector *AccountStatRow
	for i := range resp.FilterOptions.AccountStats {
		row := &resp.FilterOptions.AccountStats[i]
		if slices.Contains(row.SourceHashes, "source-only") {
			sourceOnlySelector = row
			break
		}
	}
	if sourceOnlySelector == nil || !slices.Equal(sourceOnlySelector.Sources, []string{"k:z-upstream-key"}) {
		t.Fatalf("missing source-only selector: %#v", resp.FilterOptions.AccountStats)
	}
	if sourceOnlySelector.Calls != 0 || len(sourceOnlySelector.Models) != 0 {
		t.Fatalf("selector unexpectedly returned aggregate metrics: %#v", sourceOnlySelector)
	}
	var sourceHashOnlySelector *AccountStatRow
	for i := range resp.FilterOptions.AccountStats {
		row := &resp.FilterOptions.AccountStats[i]
		if slices.Contains(row.SourceHashes, "source-hash-only") {
			sourceHashOnlySelector = row
			break
		}
	}
	if sourceHashOnlySelector == nil || len(sourceHashOnlySelector.Sources) != 0 {
		t.Fatalf("missing source-hash-only selector: %#v", resp.FilterOptions.AccountStats)
	}
	if len(resp.FilterOptions.APIKeyStats) != 0 || len(resp.FilterOptions.ChannelShare) != 0 || len(resp.FilterOptions.ModelStats) != 0 {
		t.Fatalf("filter selectors returned full stats: %#v", resp.FilterOptions)
	}
}

func TestAnalyticsEventsPageReportsTotalCountWhilePaging(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_400_000_000)
	toMS := fromMS + 60*60*1000

	const total = 25
	events := make([]usage.Event, 0, total)
	for i := range total {
		events = append(events, monitoringEvent(
			fmt.Sprintf("total-%02d", i),
			fromMS+int64(i+1)*1_000,
			"gpt-a", "auth-1", "source-a", false, 1, 1, 0, 0, 2, nil,
		))
	}
	if _, err := db.InsertEvents(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	// First page with summary enabled: total_count must reflect the full match
	// count, not the page size.
	resp, err := New(db).Analytics(ctx, Request{
		FromMS:  fromMS,
		ToMS:    toMS,
		Include: Include{Summary: true, EventsPage: &EventsPage{Limit: 10}},
	})
	if err != nil {
		t.Fatalf("analytics page 1: %v", err)
	}
	if resp.Events == nil || len(resp.Events.Items) != 10 || !resp.Events.HasMore {
		t.Fatalf("page 1 = %#v", resp.Events)
	}
	if resp.Events.TotalCount != total {
		t.Fatalf("page 1 total_count = %d, want %d", resp.Events.TotalCount, total)
	}
	if resp.Events.NextBeforeMS == 0 || resp.Events.NextBeforeID == 0 {
		t.Fatalf("page 1 cursor = ms %d id %d", resp.Events.NextBeforeMS, resp.Events.NextBeforeID)
	}

	// Second page without summary exercises the standalone count(*) branch and
	// must still report the full total, not the remaining count.
	beforeMS := resp.Events.NextBeforeMS
	beforeID := resp.Events.NextBeforeID
	resp2, err := New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   toMS,
		Include: Include{
			EventsPage: &EventsPage{Limit: 10, BeforeMS: &beforeMS, BeforeID: &beforeID},
		},
	})
	if err != nil {
		t.Fatalf("analytics page 2: %v", err)
	}
	if resp2.Events == nil || len(resp2.Events.Items) != 10 || !resp2.Events.HasMore {
		t.Fatalf("page 2 = %#v", resp2.Events)
	}
	if resp2.Events.TotalCount != total {
		t.Fatalf("page 2 total_count = %d, want %d", resp2.Events.TotalCount, total)
	}
	if resp2.Events.Items[0].EventHash == resp.Events.Items[len(resp.Events.Items)-1].EventHash {
		t.Fatalf("page 2 overlaps page 1 boundary item")
	}
}

func TestAnalyticsEventsPageUsesNormalizedTotalInput(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_400_000_000)
	events := []usage.Event{
		{
			EventHash: "xai-included", TimestampMS: fromMS + 1, Timestamp: "2026-05-06T00:00:00Z",
			ExecutorType: "XAIExecutor", Model: "grok-4", InputTokens: 100, CacheReadTokens: 40, OutputTokens: 20, CreatedAtMS: fromMS + 1,
		},
		{
			EventHash: "claude-separate", TimestampMS: fromMS + 2, Timestamp: "2026-05-06T00:00:01Z",
			ExecutorType: "ClaudeExecutor", Model: "claude-sonnet", InputTokens: 100, CacheReadTokens: 40, OutputTokens: 20, CreatedAtMS: fromMS + 2,
		},
	}
	if _, err := db.InsertEvents(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS: fromMS,
		ToMS:   fromMS + 60_000,
		Include: Include{
			EventsPage: &EventsPage{Limit: 10},
		},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}
	if resp.Events == nil || len(resp.Events.Items) != 2 {
		t.Fatalf("events = %#v", resp.Events)
	}
	inputs := map[string]int64{}
	for _, item := range resp.Events.Items {
		inputs[item.EventHash] = item.InputTokens
	}
	if inputs["xai-included"] != 100 || inputs["claude-separate"] != 140 {
		t.Fatalf("normalized event inputs = %#v", inputs)
	}
}

func TestAnalyticsEventsPageTotalCountRespectsFilters(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_500_000_000)
	toMS := fromMS + 60*60*1000

	events := make([]usage.Event, 0, 11)
	for i := range 8 {
		events = append(events, monitoringEvent(fmt.Sprintf("ok-%d", i), fromMS+int64(i+1)*1_000, "gpt-a", "auth-1", "source-a", false, 1, 1, 0, 0, 2, nil))
	}
	for i := range 3 {
		events = append(events, monitoringEvent(fmt.Sprintf("fail-%d", i), fromMS+int64(100+i)*1_000, "gpt-b", "auth-2", "source-b", true, 1, 1, 0, 0, 2, nil))
	}
	if _, err := db.InsertEvents(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	all, err := New(db).Analytics(ctx, Request{FromMS: fromMS, ToMS: toMS, Include: Include{EventsPage: &EventsPage{Limit: 50}}})
	if err != nil {
		t.Fatalf("analytics all: %v", err)
	}
	if all.Events == nil || all.Events.TotalCount != 11 {
		t.Fatalf("all total_count = %#v", all.Events)
	}

	failed, err := New(db).Analytics(ctx, Request{FromMS: fromMS, ToMS: toMS, Filters: Filters{FailedOnly: true}, Include: Include{EventsPage: &EventsPage{Limit: 50}}})
	if err != nil {
		t.Fatalf("analytics failed only: %v", err)
	}
	if failed.Events == nil || failed.Events.TotalCount != 3 || len(failed.Events.Items) != 3 {
		t.Fatalf("failed total_count = %#v", failed.Events)
	}

	byModel, err := New(db).Analytics(ctx, Request{FromMS: fromMS, ToMS: toMS, Filters: Filters{Models: []string{"gpt-a"}}, Include: Include{EventsPage: &EventsPage{Limit: 50}}})
	if err != nil {
		t.Fatalf("analytics model filter: %v", err)
	}
	if byModel.Events == nil || byModel.Events.TotalCount != 8 {
		t.Fatalf("model total_count = %#v", byModel.Events)
	}
}

func TestAnalyticsEventsPageStableCursorAvoidsSkippingSameTimestamp(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_778_600_000_000)
	toMS := fromMS + 60*60*1000

	// Every event shares one timestamp_ms so the page boundary lands inside a
	// single millisecond. A timestamp-only cursor would skip the remaining
	// rows; the compound (timestamp_ms, id) cursor must page through all of
	// them without dropping or duplicating any.
	const total = 12
	sharedTS := fromMS + 5_000
	events := make([]usage.Event, 0, total)
	for i := range total {
		events = append(events, monitoringEvent(fmt.Sprintf("same-ts-%02d", i), sharedTS, "gpt-a", "auth-1", "source-a", false, 1, 1, 0, 0, 2, nil))
	}
	if _, err := db.InsertEvents(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	svc := New(db)
	seen := make(map[string]bool, total)
	var beforeMS, beforeID int64
	pages := 0
	for {
		page := &EventsPage{Limit: 5}
		if beforeMS > 0 {
			ms := beforeMS
			id := beforeID
			page.BeforeMS = &ms
			page.BeforeID = &id
		}
		resp, err := svc.Analytics(ctx, Request{FromMS: fromMS, ToMS: toMS, Include: Include{EventsPage: page}})
		if err != nil {
			t.Fatalf("analytics page %d: %v", pages, err)
		}
		if resp.Events == nil {
			t.Fatalf("analytics page %d returned no events", pages)
		}
		if resp.Events.TotalCount != total {
			t.Fatalf("page %d total_count = %d, want %d", pages, resp.Events.TotalCount, total)
		}
		for _, item := range resp.Events.Items {
			if seen[item.EventHash] {
				t.Fatalf("duplicate event %s across pages", item.EventHash)
			}
			seen[item.EventHash] = true
		}
		pages++
		if !resp.Events.HasMore {
			break
		}
		beforeMS = resp.Events.NextBeforeMS
		beforeID = resp.Events.NextBeforeID
		if pages > total+2 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != total {
		t.Fatalf("collected %d unique events, want %d (same-timestamp rows were skipped)", len(seen), total)
	}
}

func TestAnalyticsTimelineUsesRequestedTimeZoneForDayBuckets(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	beforeLocalMidnightMS := time.Date(2026, 6, 3, 15, 30, 0, 0, time.UTC).UnixMilli()
	afterLocalMidnightMS := time.Date(2026, 6, 3, 16, 30, 0, 0, time.UTC).UnixMilli()
	fromMS := time.Date(2026, 6, 3, 14, 0, 0, 0, time.UTC).UnixMilli()
	toMS := time.Date(2026, 6, 3, 18, 0, 0, 0, time.UTC).UnixMilli()

	if _, err := db.InsertEvents(ctx, []usage.Event{
		monitoringEvent("local-day-a", beforeLocalMidnightMS, "gpt-a", "auth-1", "source-a", false, 10, 5, 0, 0, 15, nil),
		monitoringEvent("local-day-b", afterLocalMidnightMS, "gpt-a", "auth-1", "source-a", false, 20, 10, 0, 0, 30, nil),
	}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS:   fromMS,
		ToMS:     toMS,
		TimeZone: "Asia/Shanghai",
		Include: Include{
			Timeline:    true,
			Granularity: "day",
		},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}

	if len(resp.Timeline) != 2 {
		t.Fatalf("timeline buckets = %#v", resp.Timeline)
	}
	expectedFirstBucket := time.Date(2026, 6, 3, 0, 0, 0, 0, location).UnixMilli()
	expectedSecondBucket := time.Date(2026, 6, 4, 0, 0, 0, 0, location).UnixMilli()
	if resp.Timeline[0].BucketMS != expectedFirstBucket || resp.Timeline[0].Label != "06/03" ||
		resp.Timeline[0].Calls != 1 || resp.Timeline[0].TotalTokens != 15 {
		t.Fatalf("first timeline bucket = %#v", resp.Timeline[0])
	}
	if resp.Timeline[1].BucketMS != expectedSecondBucket || resp.Timeline[1].Label != "06/04" ||
		resp.Timeline[1].Calls != 1 || resp.Timeline[1].TotalTokens != 30 {
		t.Fatalf("second timeline bucket = %#v", resp.Timeline[1])
	}
}

func TestAnalyticsSummaryAndHourlyDistributionUseRequestedTimeZone(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	firstMS := time.Date(2026, 6, 3, 23, 30, 0, 0, time.UTC).UnixMilli()
	secondMS := time.Date(2026, 6, 4, 0, 30, 0, 0, time.UTC).UnixMilli()
	fromMS := time.Date(2026, 6, 3, 22, 0, 0, 0, time.UTC).UnixMilli()
	toMS := time.Date(2026, 6, 4, 2, 0, 0, 0, time.UTC).UnixMilli()

	if _, err := db.InsertEvents(ctx, []usage.Event{
		monitoringEvent("local-summary-a", firstMS, "gpt-a", "auth-1", "source-a", false, 10, 5, 0, 0, 15, nil),
		monitoringEvent("local-summary-b", secondMS, "gpt-a", "auth-1", "source-a", false, 20, 10, 0, 0, 30, nil),
	}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Analytics(ctx, Request{
		FromMS:   fromMS,
		ToMS:     toMS,
		TimeZone: "Asia/Shanghai",
		Include: Include{
			Summary:            true,
			HourlyDistribution: true,
		},
	})
	if err != nil {
		t.Fatalf("analytics: %v", err)
	}

	if resp.Summary == nil {
		t.Fatal("summary is nil")
	}
	if resp.Summary.AvgDailyRequests != 2 || resp.Summary.AvgDailyTokens != 45 {
		t.Fatalf("summary daily averages = requests %v tokens %v", resp.Summary.AvgDailyRequests, resp.Summary.AvgDailyTokens)
	}
	if len(resp.HourlyDistribution) != 2 {
		t.Fatalf("hourly distribution = %#v", resp.HourlyDistribution)
	}
	if resp.HourlyDistribution[0].Hour != 7 || resp.HourlyDistribution[0].Calls != 1 || resp.HourlyDistribution[0].Tokens != 15 {
		t.Fatalf("first hourly point = %#v", resp.HourlyDistribution[0])
	}
	if resp.HourlyDistribution[1].Hour != 8 || resp.HourlyDistribution[1].Calls != 1 || resp.HourlyDistribution[1].Tokens != 30 {
		t.Fatalf("second hourly point = %#v", resp.HourlyDistribution[1])
	}
}

func TestAccountHistoryReturnsRollupTotalsAndCost(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	baseMS := int64(1_700_000_000_000)
	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"resolved-a": {
			Prompt:        1,
			Completion:    2,
			Cache:         0.5,
			CacheRead:     0.25,
			CacheCreation: 1.5,
		},
	}); err != nil {
		t.Fatalf("save model prices: %v", err)
	}

	first := monitoringEvent("history-a-1", baseMS+1_000, "alias-a", "auth-1", "source-a", false, 1_000_000, 500_000, 0, 100_000, 1_530_000, nil)
	first.ResolvedModel = "resolved-a"
	first.AccountSnapshot = "hist@example.com"
	first.Source = "hist@example.com"
	first.CacheReadTokens = 20_000
	first.CacheCreationTokens = 10_000
	second := monitoringEvent("history-a-2", baseMS+2_000, "alias-a", "auth-1", "source-a", true, 0, 0, 0, 0, 0, nil)
	second.ResolvedModel = "resolved-a"
	second.AccountSnapshot = "hist@example.com"
	second.Source = "hist@example.com"
	if _, err := db.InsertEvents(ctx, []usage.Event{first, second}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).AccountHistory(ctx, AccountHistoryRequest{
		Accounts: []AccountHistoryTarget{
			{AccountSnapshot: "hist@example.com"},
			{AccountSnapshot: "missing@example.com"},
			{AccountKey: "hist@example.com"},
		},
		CatchUp: true,
	})
	if err != nil {
		t.Fatalf("account history: %v", err)
	}
	if resp.Checkpoint.Pending || resp.Checkpoint.LatestID != 2 || resp.Checkpoint.LastEventID != 2 || resp.Checkpoint.Processed != 2 {
		t.Fatalf("checkpoint = %#v", resp.Checkpoint)
	}
	if len(resp.Items) != 3 {
		t.Fatalf("items = %#v", resp.Items)
	}
	history := resp.Items[0]
	if history.AccountKey != "hist@example.com" || !history.Matched || history.SyncStatus != "ready" {
		t.Fatalf("history item = %#v", history)
	}
	if history.TotalRequests != 2 || history.SuccessCalls != 1 || history.FailureCalls != 1 || history.TotalTokens != 1_530_000 {
		t.Fatalf("history totals = %#v", history)
	}
	if history.SuccessRate == nil || math.Abs(*history.SuccessRate-0.5) > 0.000001 {
		t.Fatalf("success rate = %#v", history.SuccessRate)
	}
	if math.Abs(history.TotalCost-2.055) > 0.000001 {
		t.Fatalf("total cost = %v", history.TotalCost)
	}
	if history.FirstSeenMS == nil || *history.FirstSeenMS != baseMS+1_000 || history.LastSeenMS == nil || *history.LastSeenMS != baseMS+2_000 {
		t.Fatalf("seen range = %#v %#v", history.FirstSeenMS, history.LastSeenMS)
	}
	if resp.Items[1].Matched || resp.Items[1].SyncStatus != "empty" {
		t.Fatalf("missing item = %#v", resp.Items[1])
	}
	if !resp.Items[2].Matched || resp.Items[2].AccountKey != "hist@example.com" || resp.Items[2].TotalRequests != 2 {
		t.Fatalf("account_key item = %#v", resp.Items[2])
	}
}

func TestAccountHistoryEmptyTargetDoesNotMatchAnonymousBucket(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	baseMS := int64(1_700_000_000_000)
	event := monitoringEvent("history-anonymous-source", baseMS+1_000, "gpt-a", "", "source-only", false, 10, 5, 0, 0, 15, nil)
	event.AccountSnapshot = ""
	event.AuthLabelSnapshot = ""
	event.Source = ""
	event.AuthIndex = ""
	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).AccountHistory(ctx, AccountHistoryRequest{
		Accounts: []AccountHistoryTarget{
			{},
		},
		CatchUp: true,
	})
	if err != nil {
		t.Fatalf("account history: %v", err)
	}
	if len(resp.Items) != 1 {
		t.Fatalf("items = %#v", resp.Items)
	}
	if resp.Items[0].Matched || resp.Items[0].AccountKey != "" || resp.Items[0].SyncStatus != "empty" {
		t.Fatalf("empty target matched anonymous bucket: %#v", resp.Items[0])
	}
}

func TestAnalyticsHourlyRollupMatchesRawCoreComparisonAndTimeline(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	windowMS := int64(48 * time.Hour / time.Millisecond)
	fromMS := int64(1_800_000_000_000)
	toMS := fromMS + windowMS
	latency100 := int64(100)
	latency400 := int64(400)
	ttft50 := int64(50)

	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"resolved-a": {Prompt: 2, Completion: 4},
		"model-b":    {Prompt: 1, Completion: 3},
	}); err != nil {
		t.Fatalf("save prices: %v", err)
	}
	events := []usage.Event{
		monitoringEvent("rollup-prev-a", fromMS-windowMS+10*time.Minute.Milliseconds(), "alias-a", "auth-a", "source-a", false, 1_000_000, 100, 0, 0, 1_000_100, &latency100),
		monitoringEvent("rollup-prev-b", fromMS-time.Hour.Milliseconds(), "model-b", "auth-b", "source-b", true, 200, 300, 0, 0, 500, &latency400),
		monitoringEvent("rollup-current-a", fromMS+10*time.Minute.Milliseconds(), "alias-a", "auth-a", "source-a", false, 500_000, 250_000, 20, 30, 750_020, &latency100),
		monitoringEvent("rollup-current-b", fromMS+25*time.Hour.Milliseconds(), "model-b", "auth-b", "source-b", true, 400, 500, 0, 0, 900, &latency400),
		monitoringEvent("rollup-current-zero", toMS-10*time.Minute.Milliseconds(), "model-b", "auth-b", "source-b", false, 0, 0, 0, 0, 0, nil),
	}
	for index := range events {
		events[index].RequestID = fmt.Sprintf("rollup-request-%d", index)
		events[index].TTFTMS = &ttft50
	}
	events[0].ResolvedModel = "resolved-a"
	events[2].ResolvedModel = "resolved-a"
	if _, err := db.InsertEvents(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	req := Request{
		FromMS:   fromMS,
		ToMS:     toMS,
		NowMS:    toMS,
		TimeZone: "UTC",
		Include: Include{
			Summary:           true,
			SummaryComparison: true,
			Timeline:          true,
			ModelStats:        true,
			AnomalyPoints:     true,
			Granularity:       "day",
		},
	}
	raw, err := New(db, false).Analytics(ctx, req)
	if err != nil {
		t.Fatalf("raw analytics: %v", err)
	}
	catchUpMonitoringHourlyRollup(t, ctx, db)
	rolled, err := New(db, true).Analytics(ctx, req)
	if err != nil {
		t.Fatalf("rollup analytics: %v", err)
	}
	raw.GeneratedAtMS = rolled.GeneratedAtMS
	if !reflect.DeepEqual(rolled, raw) {
		t.Fatalf("analytics mismatch\nrollup=%#v\nraw=%#v", rolled, raw)
	}
	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"resolved-a": {Prompt: 4, Completion: 8},
		"model-b":    {Prompt: 2, Completion: 6},
	}); err != nil {
		t.Fatalf("update prices: %v", err)
	}
	repricedRaw, err := New(db, false).Analytics(ctx, req)
	if err != nil {
		t.Fatalf("repriced raw analytics: %v", err)
	}
	repricedRollup, err := New(db, true).Analytics(ctx, req)
	if err != nil {
		t.Fatalf("repriced rollup analytics: %v", err)
	}
	repricedRaw.GeneratedAtMS = repricedRollup.GeneratedAtMS
	if !reflect.DeepEqual(repricedRollup, repricedRaw) {
		t.Fatalf("repriced analytics mismatch\nrollup=%#v\nraw=%#v", repricedRollup, repricedRaw)
	}
	if repricedRollup.Summary == nil || rolled.Summary == nil || repricedRollup.Summary.TotalCost != rolled.Summary.TotalCost*2 {
		t.Fatalf("repriced summary = %#v, original = %#v", repricedRollup.Summary, rolled.Summary)
	}
}

func TestAnalyticsHourlyRollupMatchesRawForModelAndOutcomeFilters(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := int64(1_800_000_000_000) + 15*time.Minute.Milliseconds()
	toMS := fromMS + 4*time.Hour.Milliseconds() + 20*time.Minute.Milliseconds()
	latency := int64(125)
	events := []usage.Event{
		monitoringEvent("filtered-edge-success-a", fromMS+time.Minute.Milliseconds(), "model-a", "auth-a", "source-a", false, 10, 1, 0, 0, 11, &latency),
		monitoringEvent("filtered-full-failed-a", fromMS+time.Hour.Milliseconds(), "model-a", "auth-a", "source-a", true, 20, 2, 0, 0, 22, &latency),
		monitoringEvent("filtered-full-success-b", fromMS+2*time.Hour.Milliseconds(), "model-b", "auth-b", "source-b", false, 30, 3, 0, 0, 33, &latency),
		monitoringEvent("filtered-edge-failed-b", toMS-time.Minute.Milliseconds(), "model-b", "auth-b", "source-b", true, 40, 4, 0, 0, 44, &latency),
	}
	if _, err := db.InsertEvents(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	catchUpMonitoringHourlyRollup(t, ctx, db)
	if _, err := db.InsertEvents(ctx, []usage.Event{
		monitoringEvent("filtered-late-success-a", fromMS+90*time.Minute.Milliseconds(), "model-a", "auth-a", "source-a", false, 50, 5, 0, 0, 55, &latency),
	}); err != nil {
		t.Fatalf("insert late event: %v", err)
	}

	includeFailed := false
	tests := []struct {
		name    string
		filters Filters
	}{
		{name: "model", filters: Filters{Models: []string{"model-a"}}},
		{name: "success only", filters: Filters{IncludeFailed: &includeFailed}},
		{name: "failed only", filters: Filters{FailedOnly: true}},
		{name: "model and failed", filters: Filters{Models: []string{"model-b"}, FailedOnly: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := Request{
				FromMS:   fromMS,
				ToMS:     toMS,
				NowMS:    toMS,
				TimeZone: "UTC",
				Filters:  test.filters,
				Include: Include{
					Summary:     true,
					Timeline:    true,
					ModelStats:  true,
					Granularity: "hour",
				},
			}
			raw, err := New(db, false).Analytics(ctx, req)
			if err != nil {
				t.Fatalf("raw analytics: %v", err)
			}
			rolled, err := New(db, true).Analytics(ctx, req)
			if err != nil {
				t.Fatalf("rollup analytics: %v", err)
			}
			raw.GeneratedAtMS = rolled.GeneratedAtMS
			if !reflect.DeepEqual(rolled, raw) {
				t.Fatalf("filtered analytics mismatch\nrollup=%#v\nraw=%#v", rolled, raw)
			}
		})
	}
}

func TestAnalyticsHourlyRollupTimelineMatchesRawAcrossDST(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := time.Date(2026, time.March, 7, 0, 0, 0, 0, time.UTC).UnixMilli()
	toMS := time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC).UnixMilli()
	latency := int64(250)
	events := make([]usage.Event, 0, 18)
	for index := 0; index < 18; index++ {
		timestampMS := fromMS + int64(index)*4*time.Hour.Milliseconds() + 10*time.Minute.Milliseconds()
		events = append(events, monitoringEvent(
			fmt.Sprintf("dst-%02d", index),
			timestampMS,
			fmt.Sprintf("model-%d", index%2),
			"auth-a",
			"source-a",
			index%5 == 0,
			int64(100+index),
			int64(50+index),
			0,
			0,
			int64(150+2*index),
			&latency,
		))
	}
	if _, err := db.InsertEvents(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	catchUpMonitoringHourlyRollup(t, ctx, db)

	for _, timeZone := range []string{"America/New_York", "Asia/Shanghai", "Asia/Kolkata"} {
		for _, granularity := range []string{"hour", "day"} {
			t.Run(timeZone+"/"+granularity, func(t *testing.T) {
				req := Request{
					FromMS:   fromMS,
					ToMS:     toMS,
					NowMS:    toMS,
					TimeZone: timeZone,
					Include: Include{
						Timeline:    true,
						Granularity: granularity,
					},
				}
				raw, err := New(db, false).Analytics(ctx, req)
				if err != nil {
					t.Fatalf("raw analytics: %v", err)
				}
				rolled, err := New(db, true).Analytics(ctx, req)
				if err != nil {
					t.Fatalf("rollup analytics: %v", err)
				}
				if !reflect.DeepEqual(rolled.Timeline, raw.Timeline) {
					t.Fatalf("timeline mismatch\nrollup=%#v\nraw=%#v", rolled.Timeline, raw.Timeline)
				}
			})
		}
	}
}

func TestAnalyticsHourlyRollupTimelineMatchesRawAcrossDSTFallBack(t *testing.T) {
	db := newMonitoringTestStore(t)
	ctx := context.Background()
	fromMS := time.Date(2026, time.October, 31, 0, 0, 0, 0, time.UTC).UnixMilli()
	toMS := time.Date(2026, time.November, 3, 0, 0, 0, 0, time.UTC).UnixMilli()
	latency := int64(180)
	events := make([]usage.Event, 0, 24)
	for index := 0; index < 24; index++ {
		timestampMS := fromMS + int64(index)*3*time.Hour.Milliseconds() + 15*time.Minute.Milliseconds()
		events = append(events, monitoringEvent(
			fmt.Sprintf("dst-fall-%02d", index),
			timestampMS,
			fmt.Sprintf("model-%d", index%3),
			"auth-a",
			"source-a",
			index%7 == 0,
			int64(90+index),
			int64(40+index),
			0,
			0,
			int64(130+2*index),
			&latency,
		))
	}
	if _, err := db.InsertEvents(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	catchUpMonitoringHourlyRollup(t, ctx, db)
	req := Request{
		FromMS:   fromMS,
		ToMS:     toMS,
		NowMS:    toMS,
		TimeZone: "America/New_York",
		Include: Include{
			Timeline:    true,
			Granularity: "hour",
		},
	}
	raw, err := New(db, false).Analytics(ctx, req)
	if err != nil {
		t.Fatalf("raw analytics: %v", err)
	}
	rolled, err := New(db, true).Analytics(ctx, req)
	if err != nil {
		t.Fatalf("rollup analytics: %v", err)
	}
	if !reflect.DeepEqual(rolled.Timeline, raw.Timeline) {
		t.Fatalf("timeline mismatch\nrollup=%#v\nraw=%#v", rolled.Timeline, raw.Timeline)
	}
}

func TestAnalyticsHourlyRollupEligibilityIsStrict(t *testing.T) {
	base := store.AnalyticsFilter{IncludeFailed: true}
	if !analyticsHourlyRollupEligible(base) {
		t.Fatal("unfiltered analytics should be rollup eligible")
	}

	tests := []struct {
		name   string
		mutate func(*store.AnalyticsFilter)
	}{
		{name: "search", mutate: func(filter *store.AnalyticsFilter) { filter.SearchQuery = "model" }},
		{name: "search api key", mutate: func(filter *store.AnalyticsFilter) { filter.SearchAPIKeyHash = "key" }},
		{name: "providers", mutate: func(filter *store.AnalyticsFilter) { filter.Providers = []string{"codex"} }},
		{name: "accounts", mutate: func(filter *store.AnalyticsFilter) { filter.Accounts = []string{"account"} }},
		{name: "credential ids", mutate: func(filter *store.AnalyticsFilter) { filter.CredentialIDs = []string{"credential"} }},
		{name: "auth files", mutate: func(filter *store.AnalyticsFilter) { filter.AuthFiles = []string{"account.json"} }},
		{name: "auth indices", mutate: func(filter *store.AnalyticsFilter) { filter.AuthIndices = []string{"auth-a"} }},
		{name: "api keys", mutate: func(filter *store.AnalyticsFilter) { filter.APIKeyHashes = []string{"key"} }},
		{name: "source hashes", mutate: func(filter *store.AnalyticsFilter) { filter.SourceHashes = []string{"source"} }},
		{name: "projects", mutate: func(filter *store.AnalyticsFilter) { filter.ProjectIDs = []string{"project"} }},
		{name: "request types", mutate: func(filter *store.AnalyticsFilter) { filter.RequestTypes = []string{"chat"} }},
		{name: "header error kinds", mutate: func(filter *store.AnalyticsFilter) { filter.HeaderErrorKinds = []string{"quota"} }},
		{name: "header error codes", mutate: func(filter *store.AnalyticsFilter) { filter.HeaderErrorCodes = []string{"429"} }},
		{name: "header quota plans", mutate: func(filter *store.AnalyticsFilter) { filter.HeaderQuotaPlans = []string{"pro"} }},
		{name: "header trace ids", mutate: func(filter *store.AnalyticsFilter) { filter.HeaderTraceIDs = []string{"trace"} }},
		{name: "minimum latency", mutate: func(filter *store.AnalyticsFilter) { filter.MinLatencyMS = 100 }},
		{name: "cache status", mutate: func(filter *store.AnalyticsFilter) { filter.CacheStatus = "hit" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filter := base
			test.mutate(&filter)
			if analyticsHourlyRollupEligible(filter) {
				t.Fatalf("filter unexpectedly eligible: %#v", filter)
			}
		})
	}

	for _, supported := range []store.AnalyticsFilter{
		{IncludeFailed: true, Models: []string{"model-a"}},
		{IncludeFailed: false},
		{IncludeFailed: true, FailedOnly: true},
	} {
		if !analyticsHourlyRollupEligible(supported) {
			t.Fatalf("supported filter unexpectedly ineligible: %#v", supported)
		}
	}
}

func catchUpMonitoringHourlyRollup(t *testing.T, ctx context.Context, db *store.Store) {
	t.Helper()
	for {
		result, err := db.CatchUpUsageHourlyAggregate(ctx, 100, time.Now().UnixMilli())
		if err != nil {
			t.Fatalf("catch up hourly rollup: %v", err)
		}
		if !result.Pending {
			return
		}
	}
}

func newMonitoringTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func monitoringEvent(
	hash string,
	timestampMS int64,
	model string,
	authIndex string,
	sourceHash string,
	failed bool,
	inputTokens int64,
	outputTokens int64,
	reasoningTokens int64,
	cachedTokens int64,
	totalTokens int64,
	latencyMS *int64,
) usage.Event {
	return usage.Event{
		EventHash:       hash,
		TimestampMS:     timestampMS,
		Timestamp:       time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Model:           model,
		Endpoint:        "POST /v1/chat/completions",
		Method:          "POST",
		Path:            "/v1/chat/completions",
		AuthIndex:       authIndex,
		Source:          "user@example.com",
		SourceHash:      sourceHash,
		APIKeyHash:      "api-key-" + authIndex,
		AccountSnapshot: "user@example.com",
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
		ReasoningTokens: reasoningTokens,
		CachedTokens:    cachedTokens,
		TotalTokens:     totalTokens,
		LatencyMS:       latencyMS,
		Failed:          failed,
		CreatedAtMS:     timestampMS,
	}
}
