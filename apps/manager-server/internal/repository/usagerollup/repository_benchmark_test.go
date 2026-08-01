package usagerollup

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const (
	benchmarkAccountCount = 200
	benchmarkModelCount   = 8
	benchmarkBaseMS       = int64(1_700_000_000_000)
	benchmarkNowMS        = int64(1_700_100_000_000)
)

func BenchmarkCatchUpAccountHistoryInitial(b *testing.B) {
	for _, eventCount := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("events_%d", eventCount), func(b *testing.B) {
			b.ReportAllocs()
			ctx := context.Background()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				db, closeDB := openRollupBenchmarkDB(b)
				events := usageevent.New(db)
				repo := New(db)
				insertRollupBenchmarkEvents(b, ctx, events, benchmarkRollupEvents("initial", i*eventCount, eventCount))
				b.StartTimer()

				result, err := repo.CatchUpAccountHistory(ctx, eventCount, benchmarkNowMS)

				b.StopTimer()
				if err != nil {
					closeDB()
					b.Fatalf("catch-up: %v", err)
				}
				if result.Processed != eventCount {
					closeDB()
					b.Fatalf("processed = %d, want %d", result.Processed, eventCount)
				}
				closeDB()
			}
		})
	}
}

func BenchmarkCatchUpDashboardHourlyInitial(b *testing.B) {
	for _, eventCount := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("events_%d", eventCount), func(b *testing.B) {
			b.ReportAllocs()
			ctx := context.Background()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				db, closeDB := openRollupBenchmarkDB(b)
				events := usageevent.New(db)
				repo := New(db)
				insertRollupBenchmarkEvents(b, ctx, events, benchmarkRollupEvents("dashboard-initial", i*eventCount, eventCount))
				b.StartTimer()

				result, err := repo.CatchUpDashboardHourly(ctx, eventCount, benchmarkNowMS)

				b.StopTimer()
				if err != nil {
					closeDB()
					b.Fatalf("catch-up: %v", err)
				}
				if result.Processed != eventCount {
					closeDB()
					b.Fatalf("processed = %d, want %d", result.Processed, eventCount)
				}
				closeDB()
			}
		})
	}
}

func BenchmarkCatchUpAccountHistoryIncremental(b *testing.B) {
	b.ReportAllocs()
	ctx := context.Background()
	db, closeDB := openRollupBenchmarkDB(b)
	defer closeDB()
	events := usageevent.New(db)
	repo := New(db)
	insertRollupBenchmarkEvents(b, ctx, events, benchmarkRollupEvents("baseline", 0, 10000))
	if result, err := repo.CatchUpAccountHistory(ctx, 10000, benchmarkNowMS); err != nil {
		b.Fatalf("baseline catch-up: %v", err)
	} else if result.Processed != 10000 {
		b.Fatalf("baseline processed = %d", result.Processed)
	}

	const batchSize = 1000
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		start := 10000 + i*batchSize
		insertRollupBenchmarkEvents(b, ctx, events, benchmarkRollupEvents("incremental", start, batchSize))
		b.StartTimer()

		result, err := repo.CatchUpAccountHistory(ctx, batchSize, benchmarkNowMS+int64(i+1))

		b.StopTimer()
		if err != nil {
			b.Fatalf("incremental catch-up: %v", err)
		}
		if result.Processed != batchSize {
			b.Fatalf("processed = %d, want %d", result.Processed, batchSize)
		}
	}
}

func BenchmarkAccountHistoryRows(b *testing.B) {
	ctx := context.Background()
	db, closeDB := openRollupBenchmarkDB(b)
	defer closeDB()
	events := usageevent.New(db)
	repo := New(db)
	insertRollupBenchmarkEvents(b, ctx, events, benchmarkRollupEvents("rows", 0, 20000))
	if _, err := repo.CatchUpAccountHistory(ctx, 20000, benchmarkNowMS); err != nil {
		b.Fatalf("catch-up: %v", err)
	}

	for _, targetCount := range []int{20, 100, 200} {
		keys := benchmarkAccountKeys(targetCount)
		b.Run(fmt.Sprintf("targets_%d", targetCount), func(b *testing.B) {
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				rows, err := repo.AccountHistoryRows(ctx, keys)
				if err != nil {
					b.Fatalf("rows: %v", err)
				}
				if len(rows) == 0 {
					b.Fatalf("expected rows")
				}
			}
		})
	}
}

func openRollupBenchmarkDB(b *testing.B) (*sql.DB, func()) {
	b.Helper()
	db, err := sqliterepo.Open(filepath.Join(b.TempDir(), "usage.sqlite"))
	if err != nil {
		b.Fatalf("open sqlite: %v", err)
	}
	return db, func() {
		_ = db.Close()
	}
}

func insertRollupBenchmarkEvents(b *testing.B, ctx context.Context, repo usageevent.Repository, events []usage.Event) {
	b.Helper()
	if _, err := repo.InsertBatch(ctx, events); err != nil {
		b.Fatalf("insert events: %v", err)
	}
}

func benchmarkRollupEvents(prefix string, start int, count int) []usage.Event {
	result := make([]usage.Event, 0, count)
	for offset := 0; offset < count; offset++ {
		seq := start + offset
		accountIndex := seq % benchmarkAccountCount
		cycle := seq / benchmarkAccountCount
		modelIndex := (accountIndex + cycle) % benchmarkModelCount
		event := rollupTestEvent(
			fmt.Sprintf("%s-%08d", prefix, seq),
			benchmarkBaseMS+int64(seq),
			fmt.Sprintf("alias-%02d", modelIndex),
			fmt.Sprintf("resolved-%02d", modelIndex),
			fmt.Sprintf("account-%04d@example.com", accountIndex),
			"",
			fmt.Sprintf("auth-%04d", accountIndex),
			seq%17 == 0,
			int64(100+seq%97),
			int64(40+seq%53),
			int64(seq%11),
			int64(seq%7),
			int64(seq%5),
			int64(seq%3),
			int64(140+seq%151),
		)
		if seq%5 == 0 {
			event.ServiceTier = "priority"
		}
		result = append(result, event)
	}
	return result
}

func benchmarkAccountKeys(count int) []string {
	keys := make([]string, 0, count)
	for index := 0; index < count; index++ {
		keys = append(keys, fmt.Sprintf("account-%04d@example.com", index))
	}
	return keys
}
