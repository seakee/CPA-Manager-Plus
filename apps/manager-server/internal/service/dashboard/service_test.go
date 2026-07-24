package dashboard

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestSummaryReturnsContextCancellation(t *testing.T) {
	db := newDashboardTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := New(db).Summary(ctx, SummaryParams{
		TodayStartMS: 1_778_000_000_000,
		NowMS:        1_778_000_060_000,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("summary error = %v, want context canceled", err)
	}
}

func TestSummaryEmptyStore(t *testing.T) {
	db := newDashboardTestStore(t)
	service := New(db)

	resp, err := service.Summary(context.Background(), SummaryParams{
		TodayStartMS: 1_778_000_000_000,
		NowMS:        1_778_000_060_000,
	})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if resp.Today.TotalCalls != 0 || resp.Today.SuccessRate != 0 ||
		resp.Today.AverageLatencyMS != nil || resp.Rolling30M.TotalCalls != 0 {
		t.Fatalf("empty response = %#v", resp)
	}
	if len(resp.TopModelsToday) != 0 || len(resp.RecentFailures) != 0 {
		t.Fatalf("empty lists = %#v %#v", resp.TopModelsToday, resp.RecentFailures)
	}
	if len(resp.RequestHealth.Points) != healthTimelineBuckets || resp.RequestHealth.TotalCalls != 0 ||
		resp.RequestHealth.BucketMS != healthTimelineBucketMs {
		t.Fatalf("empty request health timeline = %#v", resp.RequestHealth)
	}
}

func TestSummaryAggregatesCostsAndWindows(t *testing.T) {
	db := newDashboardTestStore(t)
	ctx := context.Background()
	todayStart := int64(1_778_000_000_000)
	nowMS := todayStart + 60*60*1000
	latency100 := int64(100)
	latency200 := int64(200)

	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"gpt-a": {Prompt: 2, Completion: 4, Cache: 1},
	}); err != nil {
		t.Fatalf("save prices: %v", err)
	}
	_, err := db.InsertEvents(ctx, []usage.Event{
		dashboardEvent("event-a-1", todayStart+10*60*1000, "gpt-a", false, 1_000_000, 500_000, 0, 250_000, 0, 1_500_000, &latency100),
		dashboardEvent("event-b-1", todayStart+50*60*1000, "gpt-b", true, 0, 100, 0, 0, 0, 100, &latency200),
		dashboardEvent("event-a-2", todayStart+55*60*1000, "gpt-a", false, 0, 0, 0, 0, 0, 0, nil),
		dashboardEvent("event-outside", nowMS, "gpt-a", false, 10, 10, 0, 0, 0, 20, nil),
	})
	if err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Summary(ctx, SummaryParams{
		TodayStartMS:   todayStart,
		NowMS:          nowMS,
		TopModels:      1,
		RecentFailures: 2,
	})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}

	if resp.Today.TotalCalls != 3 || resp.Today.SuccessCalls != 2 || resp.Today.FailureCalls != 1 {
		t.Fatalf("today counts = %#v", resp.Today)
	}
	if resp.Today.TotalTokens != 1_500_100 || resp.Today.ZeroTokenCalls != 1 {
		t.Fatalf("today tokens = %#v", resp.Today)
	}
	if math.Abs(resp.Today.SuccessRate-(2.0/3.0)) > 0.000001 {
		t.Fatalf("success rate = %v", resp.Today.SuccessRate)
	}
	if resp.Today.AverageLatencyMS == nil || *resp.Today.AverageLatencyMS != 150 {
		t.Fatalf("average latency = %#v", resp.Today.AverageLatencyMS)
	}
	if math.Abs(resp.Today.TotalCost-3.75) > 0.000001 {
		t.Fatalf("total cost = %v", resp.Today.TotalCost)
	}
	if resp.Rolling30M.TotalCalls != 2 || resp.Rolling30M.TotalTokens != 100 {
		t.Fatalf("rolling = %#v", resp.Rolling30M)
	}
	if len(resp.TopModelsToday) != 1 || resp.TopModelsToday[0].Model != "gpt-a" ||
		resp.TopModelsToday[0].Calls != 2 || math.Abs(resp.TopModelsToday[0].Cost-3.75) > 0.000001 {
		t.Fatalf("top models = %#v", resp.TopModelsToday)
	}
	if len(resp.RecentFailures) != 1 || resp.RecentFailures[0].Model != "gpt-b" ||
		resp.RecentFailures[0].DurationMS == nil || *resp.RecentFailures[0].DurationMS != 200 {
		t.Fatalf("recent failures = %#v", resp.RecentFailures)
	}
	if resp.RecentFailures[0].Source != "user@example.com" ||
		resp.RecentFailures[0].FailSummary != "upstream rate limit" ||
		resp.RecentFailures[0].FailStatusCode == nil ||
		*resp.RecentFailures[0].FailStatusCode != 429 {
		t.Fatalf("recent failure details = %#v", resp.RecentFailures[0])
	}
	if len(resp.TrafficTimeline) != 24 || resp.TrafficTimeline[0].Calls != 3 ||
		resp.TrafficTimeline[0].Tokens != 1_500_100 ||
		math.Abs(resp.TrafficTimeline[0].FailureRate-(1.0/3.0)) > 0.000001 {
		t.Fatalf("traffic timeline = %#v", resp.TrafficTimeline)
	}
	if len(resp.HourlyActivity) != 24 || resp.HourlyActivity[0].Intensity != 1 {
		t.Fatalf("hourly activity = %#v", resp.HourlyActivity)
	}
	if len(resp.RequestHealth.Points) != healthTimelineBuckets ||
		resp.RequestHealth.TotalCalls != 3 ||
		resp.RequestHealth.SuccessCalls != 2 ||
		resp.RequestHealth.FailureCalls != 1 ||
		math.Abs(resp.RequestHealth.SuccessRate-(2.0/3.0)) > 0.000001 {
		t.Fatalf("request health timeline = %#v", resp.RequestHealth)
	}
	if resp.RequestHealth.Points[1].Calls != 1 || resp.RequestHealth.Points[5].Calls != 2 ||
		resp.RequestHealth.Points[7].Tone != "future" {
		t.Fatalf("request health timeline points = %#v", resp.RequestHealth.Points[:8])
	}
	if len(resp.TokenMix) != 4 || resp.TokenMix[0].Key != "input" ||
		resp.TokenMix[0].Tokens != 750_000 ||
		math.Abs(resp.TokenMix[0].Share-(750000.0/1500100.0)) > 0.000001 {
		t.Fatalf("token mix = %#v", resp.TokenMix)
	}
	if resp.TokenMix[1].Key != "cached" || resp.TokenMix[1].Tokens != 250_000 ||
		math.Abs(resp.TokenMix[1].Share-(250000.0/1500100.0)) > 0.000001 {
		t.Fatalf("token mix cached = %#v", resp.TokenMix)
	}
	if resp.TokenMix[2].Key != "output" || resp.TokenMix[2].Tokens != 500_100 ||
		math.Abs(resp.TokenMix[2].Share-(500100.0/1500100.0)) > 0.000001 {
		t.Fatalf("token mix output = %#v", resp.TokenMix)
	}
	if resp.TokenMix[3].Key != "reasoning" || resp.TokenMix[3].Tokens != 0 ||
		resp.TokenMix[3].Share != 0 {
		t.Fatalf("token mix reasoning = %#v", resp.TokenMix)
	}
	if len(resp.ModelCostRank) != 1 || resp.ModelCostRank[0].Model != "gpt-a" ||
		resp.ModelCostRank[0].CostShare != 1 {
		t.Fatalf("model cost rank = %#v", resp.ModelCostRank)
	}
	if len(resp.ChannelHealth) != 1 || resp.ChannelHealth[0].AuthIndex != "auth-1" ||
		resp.ChannelHealth[0].Failures != 1 || resp.ChannelHealth[0].Tone != "bad" {
		t.Fatalf("channel health = %#v", resp.ChannelHealth)
	}
	if resp.ChannelHealth[0].Source != "user@example.com" ||
		resp.ChannelHealth[0].AccountSnapshot != "user@example.com" {
		t.Fatalf("channel health display snapshots = %#v", resp.ChannelHealth[0])
	}
	if len(resp.FailureSources) != 1 || resp.FailureSources[0].SourceHash != "source-hash" ||
		resp.FailureSources[0].Failures != 1 {
		t.Fatalf("failure sources = %#v", resp.FailureSources)
	}
	if resp.FailureSources[0].Source != "user@example.com" ||
		resp.FailureSources[0].AccountSnapshot != "user@example.com" {
		t.Fatalf("failure source display snapshots = %#v", resp.FailureSources[0])
	}
}

func TestBuildTokenMixRestoresFourVisibleBuckets(t *testing.T) {
	mix := buildTokenMix(TodaySummary{
		InputTokens:         1_800,
		OutputTokens:        200,
		CachedTokens:        300,
		CacheReadTokens:     400,
		CacheCreationTokens: 100,
		ReasoningTokens:     150,
		TotalTokens:         2_150,
	})

	if len(mix) != 4 {
		t.Fatalf("token mix length = %d, want 4: %#v", len(mix), mix)
	}
	if mix[0].Key != "input" || mix[0].Tokens != 1_000 ||
		math.Abs(mix[0].Share-(1000.0/2150.0)) > 0.000001 {
		t.Fatalf("input mix = %#v", mix[0])
	}
	if mix[1].Key != "cached" || mix[1].Tokens != 800 ||
		math.Abs(mix[1].Share-(800.0/2150.0)) > 0.000001 {
		t.Fatalf("cached mix = %#v", mix[1])
	}
	if mix[2].Key != "output" || mix[2].Tokens != 200 ||
		math.Abs(mix[2].Share-(200.0/2150.0)) > 0.000001 {
		t.Fatalf("output mix = %#v", mix[2])
	}
	if mix[3].Key != "reasoning" || mix[3].Tokens != 150 ||
		math.Abs(mix[3].Share-(150.0/2150.0)) > 0.000001 {
		t.Fatalf("reasoning mix = %#v", mix[3])
	}
}

func TestBuildTokenMixDeduplicatesNestedCacheAndReasoning(t *testing.T) {
	mix := buildTokenMix(TodaySummary{
		InputTokens:     20_700,
		OutputTokens:    245,
		CachedTokens:    18_300,
		ReasoningTokens: 90,
		TotalTokens:     20_945,
	})

	if len(mix) != 4 {
		t.Fatalf("token mix length = %d, want 4: %#v", len(mix), mix)
	}
	if mix[0].Key != "input" || mix[0].Tokens != 2_400 {
		t.Fatalf("input mix = %#v", mix[0])
	}
	if mix[1].Key != "cached" || mix[1].Tokens != 18_300 {
		t.Fatalf("cached mix = %#v", mix[1])
	}
	if mix[2].Key != "output" || mix[2].Tokens != 155 {
		t.Fatalf("output mix = %#v", mix[2])
	}
	if mix[3].Key != "reasoning" || mix[3].Tokens != 90 {
		t.Fatalf("reasoning mix = %#v", mix[3])
	}
}

func TestBuildTokenMixDeduplicatesOnlyNestedReasoning(t *testing.T) {
	mix := buildTokenMix(TodaySummary{
		InputTokens:     1_500,
		OutputTokens:    400,
		CachedTokens:    500,
		ReasoningTokens: 150,
		TotalTokens:     1_950,
	})

	if mix[0].Tokens != 1_000 || mix[1].Tokens != 500 ||
		mix[2].Tokens != 300 || mix[3].Tokens != 150 {
		t.Fatalf("token mix = %#v", mix)
	}
	for _, segment := range mix {
		if math.Abs(segment.Share-float64(segment.Tokens)/1950.0) > 0.000001 {
			t.Fatalf("token mix share = %#v", segment)
		}
	}
}

func TestSummaryUsesResolvedModelPricing(t *testing.T) {
	db := newDashboardTestStore(t)
	ctx := context.Background()
	todayStart := int64(1_778_000_000_000)
	nowMS := todayStart + 60*60*1000

	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"gpt-resolved-a": {Prompt: 1},
		"gpt-resolved-b": {Completion: 2},
	}); err != nil {
		t.Fatalf("save prices: %v", err)
	}
	first := dashboardEvent("dashboard-resolved-a", todayStart+1_000, "alias-fast", false, 1_000_000, 0, 0, 0, 0, 1_000_000, nil)
	first.ResolvedModel = "gpt-resolved-a"
	second := dashboardEvent("dashboard-resolved-b", todayStart+2_000, "alias-fast", false, 0, 1_000_000, 0, 0, 0, 1_000_000, nil)
	second.ResolvedModel = "gpt-resolved-b"
	if _, err := db.InsertEvents(ctx, []usage.Event{first, second}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Summary(ctx, SummaryParams{
		TodayStartMS:   todayStart,
		NowMS:          nowMS,
		TopModels:      5,
		RecentFailures: 1,
	})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}

	if math.Abs(resp.Today.TotalCost-3) > 0.000001 {
		t.Fatalf("today cost = %v", resp.Today.TotalCost)
	}
	if len(resp.TopModelsToday) != 1 || resp.TopModelsToday[0].Model != "alias-fast" ||
		resp.TopModelsToday[0].Calls != 2 || math.Abs(resp.TopModelsToday[0].Cost-3) > 0.000001 {
		t.Fatalf("top models = %#v", resp.TopModelsToday)
	}
	if len(resp.ModelCostRank) != 1 || resp.ModelCostRank[0].Model != "alias-fast" ||
		math.Abs(resp.ModelCostRank[0].Cost-3) > 0.000001 {
		t.Fatalf("model cost rank = %#v", resp.ModelCostRank)
	}
	if len(resp.ChannelHealth) != 1 || resp.ChannelHealth[0].AuthIndex != "auth-1" ||
		math.Abs(resp.ChannelHealth[0].Cost-3) > 0.000001 {
		t.Fatalf("channel health = %#v", resp.ChannelHealth)
	}
}

func TestSummaryFallsBackToRequestedModelPriceWhenResolvedPriceIsMissing(t *testing.T) {
	db := newDashboardTestStore(t)
	ctx := context.Background()
	todayStart := int64(1_778_005_000_000)
	nowMS := todayStart + 60*60*1000

	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"GLM-5.2": {Prompt: 3},
	}); err != nil {
		t.Fatalf("save prices: %v", err)
	}
	event := dashboardEvent("dashboard-alias-fallback-cost", todayStart+1_000, "GLM-5.2", false, 1_000_000, 0, 0, 0, 0, 1_000_000, nil)
	event.RequestedModel = "GLM-5.2"
	event.ResolvedModel = "zai/glm-5.2"
	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Summary(ctx, SummaryParams{
		TodayStartMS:   todayStart,
		NowMS:          nowMS,
		TopModels:      5,
		RecentFailures: 1,
	})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}

	if math.Abs(resp.Today.TotalCost-3) > 0.000001 {
		t.Fatalf("today cost = %v", resp.Today.TotalCost)
	}
	if len(resp.TopModelsToday) != 1 || resp.TopModelsToday[0].Model != "GLM-5.2" ||
		math.Abs(resp.TopModelsToday[0].Cost-3) > 0.000001 {
		t.Fatalf("top models = %#v", resp.TopModelsToday)
	}
	if len(resp.ModelCostRank) != 1 || resp.ModelCostRank[0].Model != "GLM-5.2" ||
		math.Abs(resp.ModelCostRank[0].Cost-3) > 0.000001 {
		t.Fatalf("model cost rank = %#v", resp.ModelCostRank)
	}
	if len(resp.ChannelHealth) != 1 || resp.ChannelHealth[0].AuthIndex != "auth-1" ||
		math.Abs(resp.ChannelHealth[0].Cost-3) > 0.000001 {
		t.Fatalf("channel health = %#v", resp.ChannelHealth)
	}
}

func TestSummaryPricesPriorityAndDefaultServiceTiersSeparately(t *testing.T) {
	db := newDashboardTestStore(t)
	ctx := context.Background()
	todayStart := int64(1_778_010_000_000)
	nowMS := todayStart + 60*60*1000

	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"gpt-5.4": {Prompt: 2.5},
	}); err != nil {
		t.Fatalf("save prices: %v", err)
	}
	latency100 := int64(100)
	latency200 := int64(200)
	latency1000 := int64(1000)
	standard := dashboardEvent("dashboard-tier-default", todayStart+1_000, "gpt-5.4", false, 1_000_000, 0, 0, 0, 0, 1_000_000, &latency100)
	standard.ServiceTier = "default"
	standardSecond := dashboardEvent("dashboard-tier-default-second", todayStart+1_500, "gpt-5.4", false, 0, 0, 0, 0, 0, 0, &latency200)
	standardSecond.ServiceTier = "default"
	priority := dashboardEvent("dashboard-tier-priority", todayStart+2_000, "gpt-5.4", false, 1_000_000, 0, 0, 0, 0, 1_000_000, &latency1000)
	priority.ServiceTier = "priority"
	if _, err := db.InsertEvents(ctx, []usage.Event{standard, standardSecond, priority}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	resp, err := New(db).Summary(ctx, SummaryParams{
		TodayStartMS:   todayStart,
		NowMS:          nowMS,
		TopModels:      5,
		RecentFailures: 1,
	})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}

	if math.Abs(resp.Today.TotalCost-10) > 0.000001 {
		t.Fatalf("today cost = %v, want 10", resp.Today.TotalCost)
	}
	if len(resp.TopModelsToday) != 1 || resp.TopModelsToday[0].Calls != 3 ||
		math.Abs(resp.TopModelsToday[0].Cost-10) > 0.000001 {
		t.Fatalf("top models = %#v", resp.TopModelsToday)
	}
	if len(resp.ModelCostRank) != 1 || math.Abs(resp.ModelCostRank[0].Cost-10) > 0.000001 {
		t.Fatalf("model cost rank = %#v", resp.ModelCostRank)
	}
	if len(resp.ChannelHealth) != 1 || math.Abs(resp.ChannelHealth[0].Cost-10) > 0.000001 {
		t.Fatalf("channel health = %#v", resp.ChannelHealth)
	}
	if resp.ChannelHealth[0].AverageLatencyMS == nil ||
		math.Abs(*resp.ChannelHealth[0].AverageLatencyMS-(1300.0/3.0)) > 0.000001 {
		t.Fatalf("channel health latency = %#v, want weighted 433.333333", resp.ChannelHealth[0].AverageLatencyMS)
	}
}

func TestSummaryDashboardHourlyRollupMatchesRawWithTrailingEdge(t *testing.T) {
	db := newDashboardTestStore(t)
	ctx := context.Background()
	todayStart := int64(1_800_000_000_000)
	nowMS := todayStart + 2*hourWindowMs + 30*60*1000
	latency100 := int64(100)
	latency300 := int64(300)

	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"resolved-a": {Prompt: 2, Completion: 4},
	}); err != nil {
		t.Fatalf("save prices: %v", err)
	}
	first := dashboardEvent("dashboard-rollup-first", todayStart+10*60*1000, "alias-a", false, 1_000_000, 0, 0, 0, 0, 1_000_000, &latency100)
	first.ResolvedModel = "resolved-a"
	second := dashboardEvent("dashboard-rollup-second", todayStart+hourWindowMs+20*60*1000, "alias-a", true, 0, 500_000, 0, 0, 0, 500_000, &latency300)
	second.ResolvedModel = "resolved-a"
	trailing := dashboardEvent("dashboard-rollup-trailing", todayStart+2*hourWindowMs+10*60*1000, "alias-b", false, 10, 20, 0, 0, 0, 30, nil)
	if _, err := db.InsertEvents(ctx, []usage.Event{first, second, trailing}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	raw, err := New(db).Summary(ctx, SummaryParams{TodayStartMS: todayStart, NowMS: nowMS, TopModels: 5, RecentFailures: 5})
	if err != nil {
		t.Fatalf("raw summary: %v", err)
	}
	catchUpDashboardHourlyForTest(t, ctx, db)
	rolled, err := New(db).Summary(ctx, SummaryParams{TodayStartMS: todayStart, NowMS: nowMS, TopModels: 5, RecentFailures: 5})
	if err != nil {
		t.Fatalf("rollup summary: %v", err)
	}

	if !reflect.DeepEqual(rolled.Today, raw.Today) {
		t.Fatalf("today mismatch\nrollup=%#v\nraw=%#v", rolled.Today, raw.Today)
	}
	if !reflect.DeepEqual(rolled.TopModelsToday, raw.TopModelsToday) || !reflect.DeepEqual(rolled.ModelCostRank, raw.ModelCostRank) {
		t.Fatalf("model rows mismatch\nrollup=%#v / %#v\nraw=%#v / %#v", rolled.TopModelsToday, rolled.ModelCostRank, raw.TopModelsToday, raw.ModelCostRank)
	}
	if !reflect.DeepEqual(rolled.TrafficTimeline, raw.TrafficTimeline) || !reflect.DeepEqual(rolled.HourlyActivity, raw.HourlyActivity) {
		t.Fatalf("timeline mismatch\nrollup=%#v\nraw=%#v", rolled.TrafficTimeline, raw.TrafficTimeline)
	}
	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"resolved-a": {Prompt: 4, Completion: 8},
	}); err != nil {
		t.Fatalf("update prices: %v", err)
	}
	repriced, err := New(db).Summary(ctx, SummaryParams{TodayStartMS: todayStart, NowMS: nowMS, TopModels: 5, RecentFailures: 5})
	if err != nil {
		t.Fatalf("repriced summary: %v", err)
	}
	if repriced.Today.TotalCost != rolled.Today.TotalCost*2 {
		t.Fatalf("repriced cost = %v, want %v", repriced.Today.TotalCost, rolled.Today.TotalCost*2)
	}
}

func TestSummaryDashboardHourlyRollupKeepsOffsetTimelineCorrect(t *testing.T) {
	db := newDashboardTestStore(t)
	ctx := context.Background()
	utcHour := int64(1_800_000_000_000)
	todayStart := utcHour + 30*60*1000
	nowMS := todayStart + 2*hourWindowMs + 15*60*1000

	if _, err := db.InsertEvents(ctx, []usage.Event{
		dashboardEvent("dashboard-offset-first", todayStart+10*60*1000, "gpt-a", false, 10, 0, 0, 0, 0, 10, nil),
		dashboardEvent("dashboard-offset-second", todayStart+70*60*1000, "gpt-b", false, 20, 0, 0, 0, 0, 20, nil),
		dashboardEvent("dashboard-offset-third", todayStart+130*60*1000, "gpt-c", true, 30, 0, 0, 0, 0, 30, nil),
	}); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	raw, err := New(db).Summary(ctx, SummaryParams{TodayStartMS: todayStart, NowMS: nowMS})
	if err != nil {
		t.Fatalf("raw summary: %v", err)
	}
	catchUpDashboardHourlyForTest(t, ctx, db)
	rolled, err := New(db).Summary(ctx, SummaryParams{TodayStartMS: todayStart, NowMS: nowMS})
	if err != nil {
		t.Fatalf("rollup summary: %v", err)
	}
	if !reflect.DeepEqual(rolled.Today, raw.Today) || !reflect.DeepEqual(rolled.TrafficTimeline, raw.TrafficTimeline) {
		t.Fatalf("offset result mismatch\nrollup=%#v / %#v\nraw=%#v / %#v", rolled.Today, rolled.TrafficTimeline, raw.Today, raw.TrafficTimeline)
	}
}

func TestSummaryDashboardHourlyRollupMergesPendingRawDelta(t *testing.T) {
	db := newDashboardTestStore(t)
	ctx := context.Background()
	todayStart := int64(1_800_000_000_000)
	nowMS := todayStart + 2*hourWindowMs
	if _, err := db.InsertEvents(ctx, []usage.Event{
		dashboardEvent("dashboard-pending-first", todayStart+1_000, "gpt-a", false, 1, 0, 0, 0, 0, 1, nil),
	}); err != nil {
		t.Fatalf("insert first event: %v", err)
	}
	catchUpDashboardHourlyForTest(t, ctx, db)
	if _, err := db.InsertEvents(ctx, []usage.Event{
		dashboardEvent("dashboard-pending-second", todayStart+hourWindowMs+1_000, "gpt-b", false, 2, 0, 0, 0, 0, 2, nil),
	}); err != nil {
		t.Fatalf("insert pending event: %v", err)
	}
	agg, _, timeline, ok := New(db).loadTodayMetricsFromRollup(ctx, todayStart, nowMS)
	if !ok || agg.TotalCalls != 2 || agg.TotalTokens != 3 || len(timeline) != 2 {
		t.Fatalf("pending aggregate did not merge raw delta: ok=%v agg=%#v timeline=%#v", ok, agg, timeline)
	}
	resp, err := New(db).Summary(ctx, SummaryParams{TodayStartMS: todayStart, NowMS: nowMS})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if resp.Today.TotalCalls != 2 || resp.Today.TotalTokens != 3 {
		t.Fatalf("fallback summary = %#v", resp.Today)
	}
}

func TestSummaryDashboardHourlyRollupCanBeDisabled(t *testing.T) {
	db := newDashboardTestStore(t)
	ctx := context.Background()
	todayStart := int64(1_800_000_000_000)
	nowMS := todayStart + 2*hourWindowMs
	if _, err := db.InsertEvents(ctx, []usage.Event{
		dashboardEvent("dashboard-disabled", todayStart+1_000, "gpt-a", false, 5, 0, 0, 0, 0, 5, nil),
	}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	catchUpDashboardHourlyForTest(t, ctx, db)
	service := New(db, false)
	if _, _, _, ok := service.loadTodayMetricsFromRollup(ctx, todayStart, nowMS); ok {
		t.Fatal("disabled service used hourly rollup")
	}
	resp, err := service.Summary(ctx, SummaryParams{TodayStartMS: todayStart, NowMS: nowMS})
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if resp.Today.TotalCalls != 1 || resp.Today.TotalTokens != 5 {
		t.Fatalf("raw summary = %#v", resp.Today)
	}
}

func catchUpDashboardHourlyForTest(t *testing.T, ctx context.Context, db *store.Store) {
	t.Helper()
	for {
		result, err := db.CatchUpUsageHourlyAggregate(ctx, 100, time.Now().UnixMilli())
		if err != nil {
			t.Fatalf("catch up dashboard hourly: %v", err)
		}
		if !result.Pending {
			return
		}
	}
}

func newDashboardTestStore(t *testing.T) *store.Store {
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

func dashboardEvent(
	hash string,
	timestampMS int64,
	model string,
	failed bool,
	inputTokens int64,
	outputTokens int64,
	reasoningTokens int64,
	cachedTokens int64,
	cacheTokens int64,
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
		AuthIndex:       "auth-1",
		Source:          "user@example.com",
		SourceHash:      "source-hash",
		APIKeyHash:      "api-key-hash",
		AccountSnapshot: "user@example.com",
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
		ReasoningTokens: reasoningTokens,
		CachedTokens:    cachedTokens,
		CacheTokens:     cacheTokens,
		TotalTokens:     totalTokens,
		LatencyMS:       latencyMS,
		Failed:          failed,
		FailStatusCode:  429,
		FailSummary:     "upstream rate limit",
		CreatedAtMS:     timestampMS,
	}
}
