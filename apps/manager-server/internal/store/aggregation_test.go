package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestAggregateBetween(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	empty, err := db.AggregateBetween(context.Background(), 1_000, 2_000)
	if err != nil {
		t.Fatalf("empty aggregate: %v", err)
	}
	if empty.TotalCalls != 0 || empty.SuccessCalls != 0 || empty.FailureCalls != 0 ||
		empty.TotalTokens != 0 || empty.ZeroTokenCalls != 0 || empty.AvgLatencyMS.Valid {
		t.Fatalf("empty aggregate = %#v", empty)
	}

	latency := int64(120)
	_, err = db.InsertEvents(context.Background(), []usage.Event{
		aggregationEvent("event-a", 1_000, "gpt-a", false, 10, 20, 3, 4, 2, 37, &latency),
		aggregationEvent("event-b", 1_500, "gpt-b", true, 1, 2, 0, 1, 5, 3, nil),
		aggregationEvent("event-c", 1_999, "gpt-a", false, 0, 0, 0, 0, 0, 0, nil),
		aggregationEvent("event-outside", 2_000, "gpt-a", false, 100, 100, 0, 0, 0, 200, nil),
	})
	if err != nil {
		t.Fatalf("insert events: %v", err)
	}

	agg, err := db.AggregateBetween(context.Background(), 1_000, 2_000)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if agg.TotalCalls != 3 || agg.SuccessCalls != 2 || agg.FailureCalls != 1 {
		t.Fatalf("aggregate counts = %#v", agg)
	}
	if agg.InputTokens != 10 || agg.OutputTokens != 20 || agg.NonReasoningOutputTokens != 17 ||
		agg.ReasoningTokens != 3 || agg.UnclassifiedTokens != 14 || agg.IncompleteAccountingCalls != 2 ||
		agg.CachedTokens != 0 || agg.CacheReadTokens != 4 || agg.TotalTokens != 44 || agg.ZeroTokenCalls != 1 {
		t.Fatalf("aggregate tokens = %#v", agg)
	}
	if !agg.AvgLatencyMS.Valid || agg.AvgLatencyMS.Float64 != 120 {
		t.Fatalf("aggregate avg latency = %#v", agg.AvgLatencyMS)
	}

	top, err := db.TopModelsBetween(context.Background(), 1_000, 2_000, 1)
	if err != nil {
		t.Fatalf("top models: %v", err)
	}
	if len(top) != 1 || top[0].Model != "gpt-a" || top[0].Calls != 2 || top[0].TotalTokens != 37 {
		t.Fatalf("top models = %#v", top)
	}

	allStats, err := db.ModelStatsBetween(context.Background(), 1_000, 2_000)
	if err != nil {
		t.Fatalf("model stats: %v", err)
	}
	if len(allStats) != 2 {
		t.Fatalf("len(allStats) = %d, want 2: %#v", len(allStats), allStats)
	}

	failures, err := db.RecentFailuresBetween(context.Background(), 1_000, 2_000, 5)
	if err != nil {
		t.Fatalf("recent failures: %v", err)
	}
	if len(failures) != 1 || failures[0].Model != "gpt-b" || failures[0].TimestampMS != 1_500 {
		t.Fatalf("failures = %#v", failures)
	}
	if failures[0].Source != "user@example.com" || failures[0].FailSummary != "upstream rate limit" ||
		!failures[0].FailStatusCode.Valid || failures[0].FailStatusCode.Int64 != 429 {
		t.Fatalf("failure detail fields = %#v", failures[0])
	}

	buckets, err := db.BucketTimelineBetween(context.Background(), 1_000, 2_000, 500)
	if err != nil {
		t.Fatalf("bucket timeline: %v", err)
	}
	if len(buckets) != 2 || buckets[0].BucketMS != 1_000 || buckets[0].Calls != 1 ||
		buckets[1].BucketMS != 1_500 || buckets[1].Calls != 2 || buckets[1].Failure != 1 {
		t.Fatalf("bucket timeline = %#v", buckets)
	}
}

func TestPricingStatsExcludeInconsistentCanonicalBuckets(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	event := aggregationEvent("inconsistent-pricing", 1_000, "gpt-a", false, 999, 999, 999, 0, 0, 1_998, nil)
	event.AccountingVersion = usage.TokenAccountingSchemaVersion
	event.AccountingValid = true
	event.TokenBreakdown = usage.TokenBreakdown{
		SchemaVersion: usage.TokenAccountingSchemaVersion,
		Quality:       usage.TokenAccountingQualityInconsistent,
		TotalTokens:   110,
		Input: usage.TokenInputBreakdown{
			TotalTokens:    100,
			UncachedTokens: 100,
		},
		Output: usage.TokenOutputBreakdown{
			TotalTokens:        10,
			NonReasoningTokens: 10,
		},
	}
	if !event.TokenBreakdown.Valid() {
		t.Fatalf("test breakdown is invalid: %#v", event.TokenBreakdown)
	}
	if _, err := db.InsertEvents(context.Background(), []usage.Event{event}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	agg, err := db.AggregateBetween(context.Background(), 1_000, 2_000)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if agg.InputTokens != 100 || agg.OutputTokens != 10 || agg.NonReasoningOutputTokens != 10 ||
		agg.UnclassifiedTokens != 0 || agg.TotalTokens != 110 || agg.IncompleteAccountingCalls != 1 {
		t.Fatalf("display aggregate = %#v", agg)
	}

	assertPricingSafe := func(name string, stats []ModelStat) {
		t.Helper()
		if len(stats) != 1 {
			t.Fatalf("%s stats = %#v", name, stats)
		}
		stat := stats[0]
		if stat.InputTokens != 0 || stat.OutputTokens != 0 || stat.NonReasoningOutputTokens != 0 ||
			stat.ReasoningTokens != 0 || stat.CachedTokens != 0 || stat.CacheReadTokens != 0 ||
			stat.CacheCreationTokens != 0 || stat.UnclassifiedTokens != 110 || stat.TotalTokens != 110 ||
			stat.IncompleteAccountingCalls != 1 {
			t.Fatalf("%s pricing stat = %#v", name, stat)
		}
	}

	stats, err := db.ModelStatsBetween(context.Background(), 1_000, 2_000)
	if err != nil {
		t.Fatalf("model stats: %v", err)
	}
	assertPricingSafe("dashboard", stats)

	filtered, err := db.ModelStatsWithFilter(context.Background(), AnalyticsFilter{
		FromMS:        1_000,
		ToMS:          2_000,
		IncludeFailed: true,
	}, 0)
	if err != nil {
		t.Fatalf("filtered model stats: %v", err)
	}
	assertPricingSafe("monitoring", filtered)
}

func TestFilterOptionValuesWithFilterIncludesHeaderTraceIDs(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	event := aggregationEvent("event-trace-option", 1_000, "gpt-a", false, 10, 20, 0, 0, 0, 30, nil)
	event.HeaderTraceID = "req-filter-option"
	if _, err := db.InsertEvents(context.Background(), []usage.Event{event}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	options, err := db.FilterOptionValuesWithFilter(context.Background(), AnalyticsFilter{
		FromMS: 1_000,
		ToMS:   2_000,
	})
	if err != nil {
		t.Fatalf("filter option values: %v", err)
	}
	if len(options.HeaderTraceIDs) != 1 || options.HeaderTraceIDs[0] != "req-filter-option" {
		t.Fatalf("header trace filter options = %#v", options.HeaderTraceIDs)
	}
}

func aggregationEvent(
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
		FailStatusCode:  429,
		FailSummary:     "upstream rate limit",
		CreatedAtMS:     timestampMS,
	}
}
