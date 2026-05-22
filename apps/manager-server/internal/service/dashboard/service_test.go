package dashboard

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

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
		dashboardEvent("event-a-1", todayStart+10*60*1000, "gpt-a", false, 1_000_000, 500_000, 0, 250_000, 0, 1_750_000, &latency100),
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
	if resp.Today.TotalTokens != 1_750_100 || resp.Today.ZeroTokenCalls != 1 {
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
	if len(resp.TrafficTimeline) != 24 || resp.TrafficTimeline[0].Calls != 3 ||
		resp.TrafficTimeline[0].Tokens != 1_750_100 ||
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
		resp.TokenMix[0].Tokens != 1_000_000 {
		t.Fatalf("token mix = %#v", resp.TokenMix)
	}
	if len(resp.ModelCostRank) != 1 || resp.ModelCostRank[0].Model != "gpt-a" ||
		resp.ModelCostRank[0].CostShare != 1 {
		t.Fatalf("model cost rank = %#v", resp.ModelCostRank)
	}
	if len(resp.ChannelHealth) != 1 || resp.ChannelHealth[0].AuthIndex != "auth-1" ||
		resp.ChannelHealth[0].Failures != 1 || resp.ChannelHealth[0].Tone != "bad" {
		t.Fatalf("channel health = %#v", resp.ChannelHealth)
	}
	if len(resp.FailureSources) != 1 || resp.FailureSources[0].SourceHash != "source-hash" ||
		resp.FailureSources[0].Failures != 1 {
		t.Fatalf("failure sources = %#v", resp.FailureSources)
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
		InputTokens:     inputTokens,
		OutputTokens:    outputTokens,
		ReasoningTokens: reasoningTokens,
		CachedTokens:    cachedTokens,
		CacheTokens:     cacheTokens,
		TotalTokens:     totalTokens,
		LatencyMS:       latencyMS,
		Failed:          failed,
		CreatedAtMS:     timestampMS,
	}
}
