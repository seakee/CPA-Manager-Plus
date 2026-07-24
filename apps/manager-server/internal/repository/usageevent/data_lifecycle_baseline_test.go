package usageevent

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const (
	usageBaselineEnabledEnv = "CPA_MANAGER_USAGE_BASELINE"
	usageBaselineCountsEnv  = "CPA_MANAGER_USAGE_BASELINE_COUNTS"
	usageBaselineRangesEnv  = "CPA_MANAGER_USAGE_BASELINE_RANGES_DAYS"
	usageBenchmarkCountsEnv = "CPA_MANAGER_USAGE_BENCH_COUNTS"
)

const usageBaselineDayMS int64 = 24 * 60 * 60 * 1000

// TestUsageDataLifecycleBaseline is intentionally opt-in. It creates the
// 100k/500k/1m fixture sizes needed to choose the Phase 2 aggregate schema and
// logs query timings and representative SQLite plans without affecting normal
// unit-test or CI duration.
func TestUsageDataLifecycleBaseline(t *testing.T) {
	if os.Getenv(usageBaselineEnabledEnv) != "1" {
		t.Skipf("set %s=1 to run the usage data lifecycle baseline", usageBaselineEnabledEnv)
	}

	counts := parsePositiveIntList(os.Getenv(usageBaselineCountsEnv), []int{100_000, 500_000, 1_000_000})
	ranges := parsePositiveIntList(os.Getenv(usageBaselineRangesEnv), []int{7, 30, 90})
	for _, count := range counts {
		t.Run(fmt.Sprintf("events_%d", count), func(t *testing.T) {
			runUsageBaselineFixture(t, count, ranges)
		})
	}
}

// BenchmarkUsageDataLifecycleRawPaths keeps a repeatable raw-query baseline
// alongside the existing Monitoring profile benchmarks. The default is 100k;
// set CPA_MANAGER_USAGE_BENCH_COUNTS=100000,500000,1000000 for larger runs.
func BenchmarkUsageDataLifecycleRawPaths(b *testing.B) {
	counts := parsePositiveIntList(os.Getenv(usageBenchmarkCountsEnv), []int{100_000})
	for _, count := range counts {
		b.Run(fmt.Sprintf("events_%d", count), func(b *testing.B) {
			db, err := sqliterepo.Open(filepath.Join(b.TempDir(), "usage.sqlite"))
			if err != nil {
				b.Fatalf("open database: %v", err)
			}
			b.Cleanup(func() { _ = db.Close() })

			ctx := context.Background()
			fromMS := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
			toMS := fromMS + 90*usageBaselineDayMS
			repo := New(db)
			insertUsageBaselineEvents(b, repo, fromMS, toMS, count)
			filter := AnalyticsFilter{FromMS: fromMS, ToMS: toMS, IncludeFailed: true}

			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := repo.AggregateWithFilter(ctx, filter); err != nil {
					b.Fatalf("aggregate: %v", err)
				}
				if _, err := repo.ModelStatsWithFilter(ctx, filter, 0); err != nil {
					b.Fatalf("model stats: %v", err)
				}
				if _, err := repo.TimelineWithFilter(ctx, filter, "day", time.UTC); err != nil {
					b.Fatalf("timeline: %v", err)
				}
			}
		})
	}
}

func runUsageBaselineFixture(t *testing.T, count int, ranges []int) {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	fromMS := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	toMS := fromMS + 90*usageBaselineDayMS
	repo := New(db)
	started := time.Now()
	insertUsageBaselineEvents(t, repo, fromMS, toMS, count)
	t.Logf("fixture events=%d insert=%s", count, time.Since(started))

	for _, days := range ranges {
		windowFromMS := toMS - int64(days)*usageBaselineDayMS
		filter := AnalyticsFilter{FromMS: windowFromMS, ToMS: toMS, IncludeFailed: true}
		measure := func(name string, fn func() error) {
			started := time.Now()
			if err := fn(); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			t.Logf("events=%d range_days=%d path=%s elapsed=%s", count, days, name, time.Since(started))
		}

		measure("aggregate", func() error {
			_, err := repo.AggregateWithFilter(ctx, filter)
			return err
		})
		measure("model_stats", func() error {
			_, err := repo.ModelStatsWithFilter(ctx, filter, 0)
			return err
		})
		measure("timeline_day", func() error {
			_, err := repo.TimelineWithFilter(ctx, filter, "day", time.UTC)
			return err
		})
		measure("credential_model_stats", func() error {
			_, err := repo.CredentialModelStatsWithFilter(ctx, filter)
			return err
		})
		measure("events_count", func() error {
			_, err := repo.EventsCountWithFilter(ctx, filter)
			return err
		})

		for _, plan := range []struct {
			name string
			sql  string
			args []any
		}{
			{
				name: "timestamp",
				sql:  "explain query plan select count(*) from usage_events where timestamp_ms >= ? and timestamp_ms < ?",
				args: []any{windowFromMS, toMS},
			},
			{
				name: "model_timestamp",
				sql:  "explain query plan select count(*) from usage_events where model = ? and timestamp_ms >= ? and timestamp_ms < ?",
				args: []any{"gpt-00", windowFromMS, toMS},
			},
			{
				name: "auth_timestamp",
				sql:  "explain query plan select count(*) from usage_events where auth_index = ? and timestamp_ms >= ? and timestamp_ms < ?",
				args: []any{"auth-000", windowFromMS, toMS},
			},
		} {
			lines, err := explainUsagePlan(ctx, db, plan.sql, plan.args...)
			if err != nil {
				t.Fatalf("query plan %s: %v", plan.name, err)
			}
			t.Logf("events=%d range_days=%d plan=%s detail=%s", count, days, plan.name, strings.Join(lines, " | "))
		}
	}
}

func insertUsageBaselineEvents(t testing.TB, repo Repository, fromMS, toMS int64, count int) {
	t.Helper()
	const batchSize = 1_000
	spanMS := toMS - fromMS
	stepMS := max(int64(1), spanMS/int64(count))
	ctx := context.Background()
	for offset := 0; offset < count; offset += batchSize {
		end := min(offset+batchSize, count)
		events := make([]usage.Event, 0, end-offset)
		for index := offset; index < end; index++ {
			timestampMS := fromMS + int64(index)*stepMS
			latencyMS := int64(100 + index%900)
			ttftMS := int64(20 + index%180)
			events = append(events, usage.Event{
				EventHash:            fmt.Sprintf("usage-lifecycle-baseline-%08d", index),
				TimestampMS:          timestampMS,
				Timestamp:            time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
				Provider:             []string{"codex", "claude", "gemini"}[index%3],
				Model:                fmt.Sprintf("gpt-%02d", index%12),
				ResolvedModel:        fmt.Sprintf("gpt-billing-%02d", index%12),
				AuthIndex:            fmt.Sprintf("auth-%03d", index%100),
				Source:               fmt.Sprintf("source-%03d", index%100),
				SourceHash:           fmt.Sprintf("source-hash-%03d", index%100),
				APIKeyHash:           fmt.Sprintf("api-key-%03d", index%50),
				AccountSnapshot:      fmt.Sprintf("account-%03d@example.com", index%100),
				AuthFileSnapshot:     fmt.Sprintf("account-%03d.json", index%100),
				AuthLabelSnapshot:    fmt.Sprintf("Account %03d", index%100),
				AuthProviderSnapshot: []string{"codex", "claude", "gemini"}[index%3],
				ServiceTier:          []string{"", "default", "priority"}[index%3],
				InputTokens:          int64(100 + index%300),
				OutputTokens:         int64(50 + index%150),
				ReasoningTokens:      int64(index % 40),
				CachedTokens:         int64(index % 80),
				TotalTokens:          int64(150 + index%500),
				LatencyMS:            &latencyMS,
				TTFTMS:               &ttftMS,
				Failed:               index%17 == 0,
				CreatedAtMS:          timestampMS,
			})
		}
		if _, err := repo.InsertBatch(ctx, events); err != nil {
			t.Fatalf("insert events offset=%d: %v", offset, err)
		}
	}
}

func explainUsagePlan(ctx context.Context, db *sql.DB, statement string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	plans := make([]string, 0, 2)
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			return nil, err
		}
		plans = append(plans, detail)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return plans, nil
}

func parsePositiveIntList(value string, fallback []int) []int {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	result := make([]int, 0)
	for _, part := range strings.Split(value, ",") {
		parsed, err := strconv.Atoi(strings.TrimSpace(part))
		if err == nil && parsed > 0 {
			result = append(result, parsed)
		}
	}
	if len(result) == 0 {
		return fallback
	}
	return result
}
