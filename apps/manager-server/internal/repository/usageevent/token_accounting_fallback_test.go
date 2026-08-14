package usageevent

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestAnalyticsSaturateCanonicalTokenTotalsAcrossRows(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	timestamp := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	for index := 0; index < 2; index++ {
		if _, err := db.Exec(`insert into usage_events (
			event_hash, timestamp_ms, timestamp, model,
			accounting_version, accounting_valid, accounting_quality,
			normalized_uncached_input_tokens, normalized_total_input_tokens,
			normalized_cache_read_tokens, normalized_cache_creation_tokens,
			normalized_non_reasoning_output_tokens, normalized_reasoning_output_tokens,
			normalized_total_output_tokens, unclassified_tokens, total_tokens, created_at_ms
		) values (?, ?, ?, ?, 2, 1, 'complete', ?, ?, 0, 0, 0, 0, 0, 0, ?, ?)`,
			fmt.Sprintf("saturating-canonical-%d", index),
			timestamp.UnixMilli()+int64(index),
			timestamp.Format(time.RFC3339Nano),
			"gpt-test",
			int64(math.MaxInt64),
			int64(math.MaxInt64),
			int64(math.MaxInt64),
			timestamp.UnixMilli()+int64(index),
		); err != nil {
			t.Fatalf("insert canonical event %d: %v", index, err)
		}
	}

	repo := New(db)
	fromMS := timestamp.Add(-time.Second).UnixMilli()
	toMS := timestamp.Add(time.Second).UnixMilli()
	aggregate, err := repo.AggregateWithFilter(context.Background(), AnalyticsFilter{FromMS: fromMS, ToMS: toMS})
	if err != nil {
		t.Fatalf("aggregate canonical events: %v", err)
	}
	if aggregate.TotalCalls != 2 || aggregate.InputTokens != math.MaxInt64 || aggregate.TotalTokens != math.MaxInt64 {
		t.Fatalf("aggregate = %#v", aggregate)
	}
	timeline, err := repo.TimelineWithFilter(
		context.Background(),
		AnalyticsFilter{FromMS: fromMS, ToMS: toMS},
		"hour",
		time.UTC,
	)
	if err != nil {
		t.Fatalf("timeline canonical events: %v", err)
	}
	if len(timeline) != 1 || timeline[0].Calls != 2 || timeline[0].InputTokens != math.MaxInt64 ||
		timeline[0].LongInputTokens != math.MaxInt64 || timeline[0].Tokens != math.MaxInt64 {
		t.Fatalf("timeline = %#v", timeline)
	}
}

func TestEventsPageDowngradesInvalidPersistedCanonicalProvenance(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	timestamp := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, model,
		accounting_version, accounting_valid, accounting_quality,
		input_tokens, output_tokens, reasoning_tokens,
		normalized_uncached_input_tokens, normalized_total_input_tokens,
		normalized_cache_read_tokens, normalized_cache_creation_tokens,
		normalized_non_reasoning_output_tokens, normalized_reasoning_output_tokens,
		normalized_total_output_tokens, unclassified_tokens, total_tokens, created_at_ms
	) values (?, ?, ?, ?, 2, 1, 'complete', 10, 5, 1, 10, 10, 0, 0, 6, 1, 5, 0, 15, ?)`,
		"invalid-persisted-canonical",
		timestamp.UnixMilli(),
		timestamp.Format(time.RFC3339Nano),
		"gpt-test",
		timestamp.UnixMilli(),
	); err != nil {
		t.Fatalf("insert invalid canonical event: %v", err)
	}

	page, err := New(db).EventsPageWithFilter(context.Background(), AnalyticsFilter{
		FromMS: timestamp.Add(-time.Second).UnixMilli(),
		ToMS:   timestamp.Add(time.Second).UnixMilli(),
	}, 0, 0, 10)
	if err != nil {
		t.Fatalf("load events page: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("events page = %#v", page)
	}
	item := page.Items[0]
	if item.AccountingValid || item.AccountingQuality != usage.TokenAccountingQualityInconsistent ||
		!item.IncompleteAccounting || item.UnclassifiedTokens != 15 || item.TotalTokens != 15 {
		t.Fatalf("invalid canonical provenance was not downgraded: %#v", item)
	}
}

func TestAnalyticsKeepUnmigratedLegacyTokensUnclassified(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	timestamp := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, model, cache_input_mode,
		input_tokens, output_tokens, reasoning_tokens,
		cache_read_tokens, cache_creation_tokens, total_tokens, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"legacy-token-accounting",
		timestamp.UnixMilli(),
		timestamp.Format(time.RFC3339Nano),
		"anthropic",
		"claude-sonnet",
		"separate_from_input",
		100,
		40,
		10,
		20,
		5,
		175,
		timestamp.UnixMilli(),
	); err != nil {
		t.Fatalf("insert legacy event: %v", err)
	}

	repo := New(db)
	fromMS := timestamp.Add(-time.Second).UnixMilli()
	toMS := timestamp.Add(time.Second).UnixMilli()
	assertAggregate := func(name string, aggregate Aggregate) {
		t.Helper()
		if aggregate.InputTokens != 0 || aggregate.OutputTokens != 0 ||
			aggregate.NonReasoningOutputTokens != 0 || aggregate.ReasoningTokens != 0 ||
			aggregate.CachedTokens != 0 || aggregate.CacheReadTokens != 0 || aggregate.CacheCreationTokens != 0 ||
			aggregate.UnclassifiedTokens != 175 || aggregate.TotalTokens != 175 ||
			aggregate.IncompleteAccountingCalls != 1 {
			t.Fatalf("%s aggregate = %#v", name, aggregate)
		}
	}

	between, err := repo.AggregateBetween(context.Background(), fromMS, toMS)
	if err != nil {
		t.Fatalf("aggregate between: %v", err)
	}
	assertAggregate("between", between)

	filtered, err := repo.AggregateWithFilter(context.Background(), AnalyticsFilter{
		FromMS: fromMS,
		ToMS:   toMS,
	})
	if err != nil {
		t.Fatalf("aggregate with filter: %v", err)
	}
	assertAggregate("filtered", filtered)

	for _, query := range []string{"unclassified", "separate_from_input"} {
		searched, err := repo.AggregateWithFilter(context.Background(), AnalyticsFilter{
			FromMS:      fromMS,
			ToMS:        toMS,
			SearchQuery: query,
		})
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		assertAggregate("search "+query, searched)
	}

	models, err := repo.TopModelsBetween(context.Background(), fromMS, toMS, 5)
	if err != nil {
		t.Fatalf("top models: %v", err)
	}
	if len(models) != 1 || models[0].InputTokens != 0 || models[0].OutputTokens != 0 ||
		models[0].UnclassifiedTokens != 175 || models[0].TotalTokens != 175 ||
		models[0].IncompleteAccountingCalls != 1 {
		t.Fatalf("top models = %#v", models)
	}
}

func TestHourlyDistributionUsesConservativeTotalForUnmigratedLegacyRows(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	timestamp := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, model,
		input_tokens, output_tokens, total_tokens, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?)`,
		"legacy-hourly-distribution",
		timestamp.UnixMilli(),
		timestamp.Format(time.RFC3339Nano),
		"unknown-model",
		10,
		5,
		0,
		timestamp.UnixMilli(),
	); err != nil {
		t.Fatalf("insert legacy event: %v", err)
	}

	points, err := New(db).HourlyDistributionWithFilter(context.Background(), AnalyticsFilter{
		FromMS: timestamp.Add(-time.Second).UnixMilli(),
		ToMS:   timestamp.Add(time.Second).UnixMilli(),
	}, time.UTC)
	if err != nil {
		t.Fatalf("load hourly distribution: %v", err)
	}
	if len(points) != 1 || points[0].Calls != 1 || points[0].Tokens != 15 {
		t.Fatalf("hourly distribution = %#v", points)
	}
}

func TestAnalyticsCacheStatusPrefersCanonicalBucketsAndFallsBackForLegacyRows(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	timestamp := time.Date(2026, time.July, 31, 12, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, model,
		accounting_version, accounting_valid, accounting_quality,
		cached_tokens, cache_read_tokens, cache_creation_tokens,
		normalized_uncached_input_tokens, normalized_total_input_tokens,
		normalized_cache_read_tokens, normalized_cache_creation_tokens,
		normalized_non_reasoning_output_tokens, normalized_reasoning_output_tokens,
		normalized_total_output_tokens, unclassified_tokens, total_tokens, created_at_ms
	) values
		('canonical-hit', ?, ?, 'gpt-test', 2, 1, 'complete', 0, 0, 0, 5, 10, 5, 0, 3, 2, 5, 0, 15, ?),
		('canonical-miss', ?, ?, 'gpt-test', 2, 1, 'complete', 999, 999, 999, 10, 10, 0, 0, 3, 2, 5, 0, 15, ?),
		('legacy-hit', ?, ?, 'gpt-test', 0, 0, '', 5, 5, 0, null, null, null, null, null, null, null, null, 15, ?)`,
		timestamp.UnixMilli(), timestamp.Format(time.RFC3339Nano), timestamp.UnixMilli(),
		timestamp.UnixMilli()+1, timestamp.Add(time.Millisecond).Format(time.RFC3339Nano), timestamp.UnixMilli()+1,
		timestamp.UnixMilli()+2, timestamp.Add(2*time.Millisecond).Format(time.RFC3339Nano), timestamp.UnixMilli()+2,
	); err != nil {
		t.Fatalf("insert cache-status events: %v", err)
	}

	repo := New(db)
	baseFilter := AnalyticsFilter{
		FromMS: timestamp.Add(-time.Second).UnixMilli(),
		ToMS:   timestamp.Add(time.Second).UnixMilli(),
	}
	for status, want := range map[string]int64{"hit": 2, "miss": 1, "read": 2, "creation": 0} {
		filter := baseFilter
		filter.CacheStatus = status
		count, err := repo.EventsCountWithFilter(context.Background(), filter)
		if err != nil {
			t.Fatalf("count cache status %q: %v", status, err)
		}
		if count != want {
			t.Fatalf("cache status %q count = %d, want %d", status, count, want)
		}
	}
}
