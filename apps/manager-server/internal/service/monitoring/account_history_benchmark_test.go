package monitoring

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const (
	accountHistoryBenchmarkAccountCount = 200
	accountHistoryBenchmarkModelCount   = 8
	accountHistoryBenchmarkBaseMS       = int64(1_700_000_000_000)
	accountHistoryBenchmarkNowMS        = int64(1_700_100_000_000)
)

func BenchmarkAccountHistoryServiceRead(b *testing.B) {
	ctx := context.Background()
	st, closeStore := openAccountHistoryBenchmarkStore(b)
	defer closeStore()
	saveAccountHistoryBenchmarkPrices(b, ctx, st)
	insertAccountHistoryBenchmarkEvents(b, ctx, st, accountHistoryBenchmarkEvents("read", 0, 20000))
	if result, err := st.CatchUpAccountHistoryRollups(ctx, 20000, accountHistoryBenchmarkNowMS); err != nil {
		b.Fatalf("catch-up: %v", err)
	} else if result.Processed != 20000 {
		b.Fatalf("processed = %d, want 20000", result.Processed)
	}
	service := New(st)

	for _, targetCount := range []int{20, 100, 200} {
		req := AccountHistoryRequest{
			Accounts: accountHistoryBenchmarkTargets(targetCount),
		}
		b.Run(fmt.Sprintf("targets_%d", targetCount), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				resp, err := service.AccountHistory(ctx, req)
				if err != nil {
					b.Fatalf("account history: %v", err)
				}
				if len(resp.Items) != targetCount {
					b.Fatalf("items = %d, want %d", len(resp.Items), targetCount)
				}
			}
		})
	}
}

func BenchmarkAccountHistoryServiceCatchUp(b *testing.B) {
	b.ReportAllocs()
	ctx := context.Background()
	st, closeStore := openAccountHistoryBenchmarkStore(b)
	defer closeStore()
	saveAccountHistoryBenchmarkPrices(b, ctx, st)
	insertAccountHistoryBenchmarkEvents(b, ctx, st, accountHistoryBenchmarkEvents("catchup-baseline", 0, 10000))
	if result, err := st.CatchUpAccountHistoryRollups(ctx, 10000, accountHistoryBenchmarkNowMS); err != nil {
		b.Fatalf("baseline catch-up: %v", err)
	} else if result.Processed != 10000 {
		b.Fatalf("baseline processed = %d, want 10000", result.Processed)
	}
	service := New(st)
	req := AccountHistoryRequest{
		Accounts: accountHistoryBenchmarkTargets(100),
		CatchUp:  true,
	}

	const batchSize = 500
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		start := 10000 + i*batchSize
		insertAccountHistoryBenchmarkEvents(b, ctx, st, accountHistoryBenchmarkEvents("catchup-incremental", start, batchSize))
		b.StartTimer()

		resp, err := service.AccountHistory(ctx, req)

		b.StopTimer()
		if err != nil {
			b.Fatalf("account history catch-up: %v", err)
		}
		if resp.Checkpoint.Processed != batchSize {
			b.Fatalf("processed = %d, want %d", resp.Checkpoint.Processed, batchSize)
		}
	}
}

func openAccountHistoryBenchmarkStore(b *testing.B) (*store.Store, func()) {
	b.Helper()
	st, err := store.Open(filepath.Join(b.TempDir(), "usage.sqlite"))
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	return st, func() {
		_ = st.Close()
	}
}

func saveAccountHistoryBenchmarkPrices(b *testing.B, ctx context.Context, st *store.Store) {
	b.Helper()
	prices := make(map[string]store.ModelPrice, accountHistoryBenchmarkModelCount)
	for index := 0; index < accountHistoryBenchmarkModelCount; index++ {
		prices[fmt.Sprintf("resolved-%02d", index)] = store.ModelPrice{
			Prompt:        1,
			Completion:    2,
			Cache:         0.5,
			CacheRead:     0.25,
			CacheCreation: 1.5,
		}
	}
	if err := st.SaveModelPrices(ctx, prices); err != nil {
		b.Fatalf("save prices: %v", err)
	}
}

func insertAccountHistoryBenchmarkEvents(b *testing.B, ctx context.Context, st *store.Store, events []usage.Event) {
	b.Helper()
	if _, err := st.InsertEvents(ctx, events); err != nil {
		b.Fatalf("insert events: %v", err)
	}
}

func accountHistoryBenchmarkEvents(prefix string, start int, count int) []usage.Event {
	result := make([]usage.Event, 0, count)
	for offset := 0; offset < count; offset++ {
		seq := start + offset
		accountIndex := seq % accountHistoryBenchmarkAccountCount
		cycle := seq / accountHistoryBenchmarkAccountCount
		modelIndex := (accountIndex + cycle) % accountHistoryBenchmarkModelCount
		timestampMS := accountHistoryBenchmarkBaseMS + int64(seq)
		event := usage.Event{
			EventHash:            fmt.Sprintf("%s-%08d", prefix, seq),
			TimestampMS:          timestampMS,
			Timestamp:            time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
			Provider:             "openai",
			Model:                fmt.Sprintf("alias-%02d", modelIndex),
			ResolvedModel:        fmt.Sprintf("resolved-%02d", modelIndex),
			Endpoint:             "POST /v1/chat/completions",
			Method:               "POST",
			Path:                 "/v1/chat/completions",
			AuthIndex:            fmt.Sprintf("auth-%04d", accountIndex),
			Source:               fmt.Sprintf("account-%04d@example.com", accountIndex),
			SourceHash:           fmt.Sprintf("source-%04d", accountIndex),
			AccountSnapshot:      fmt.Sprintf("account-%04d@example.com", accountIndex),
			AuthLabelSnapshot:    fmt.Sprintf("Account %04d", accountIndex),
			AuthProviderSnapshot: "openai",
			InputTokens:          int64(100 + seq%97),
			OutputTokens:         int64(40 + seq%53),
			ReasoningTokens:      int64(seq % 11),
			CachedTokens:         int64(seq % 7),
			CacheReadTokens:      int64(seq % 5),
			CacheCreationTokens:  int64(seq % 3),
			TotalTokens:          int64(140 + seq%151),
			Failed:               seq%17 == 0,
			CreatedAtMS:          timestampMS,
		}
		if seq%5 == 0 {
			event.ServiceTier = "priority"
		}
		result = append(result, event)
	}
	return result
}

func accountHistoryBenchmarkTargets(count int) []AccountHistoryTarget {
	targets := make([]AccountHistoryTarget, 0, count)
	for index := 0; index < count; index++ {
		targets = append(targets, AccountHistoryTarget{
			AccountKey: fmt.Sprintf("account-%04d@example.com", index),
		})
	}
	return targets
}
