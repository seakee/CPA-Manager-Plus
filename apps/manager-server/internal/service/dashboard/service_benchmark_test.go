package dashboard

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	monitoringsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/monitoring"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func BenchmarkDashboardTodayMetrics(b *testing.B) {
	db, err := store.Open(filepath.Join(b.TempDir(), "usage.sqlite"))
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	todayStart := int64(1_800_000_000_000)
	nowMS := todayStart + 24*hourWindowMs
	insertDashboardBenchmarkEvents(b, ctx, db, todayStart, 100_000)
	service := New(db)

	b.Run("raw_events_100k", func(b *testing.B) {
		b.ReportAllocs()
		for index := 0; index < b.N; index++ {
			if _, _, _, _, err := service.loadTodayMetrics(ctx, todayStart, nowMS, 5); err != nil {
				b.Fatalf("load raw metrics: %v", err)
			}
		}
	})

	for {
		result, err := db.CatchUpUsageHourlyAggregate(ctx, 5_000, time.Now().UnixMilli())
		if err != nil {
			b.Fatalf("catch up dashboard rollup: %v", err)
		}
		if !result.Pending {
			break
		}
	}

	b.Run("hourly_rollup_100k", func(b *testing.B) {
		b.ReportAllocs()
		for index := 0; index < b.N; index++ {
			if _, _, _, _, err := service.loadTodayMetrics(ctx, todayStart, nowMS, 5); err != nil {
				b.Fatalf("load rollup metrics: %v", err)
			}
		}
	})
}

func insertDashboardBenchmarkEvents(b *testing.B, ctx context.Context, db *store.Store, todayStart int64, count int) {
	b.Helper()
	const batchSize = 1_000
	for start := 0; start < count; start += batchSize {
		end := min(start+batchSize, count)
		events := make([]usage.Event, 0, end-start)
		for index := start; index < end; index++ {
			timestampMS := todayStart + int64(index%86_400)*1000
			model := fmt.Sprintf("benchmark-model-%02d", index%12)
			latency := int64(50 + index%500)
			event := dashboardEvent(
				fmt.Sprintf("dashboard-benchmark-%06d", index),
				timestampMS,
				model,
				index%20 == 0,
				int64(100+index%1_000),
				int64(50+index%500),
				int64(index%100),
				int64(index%200),
				0,
				int64(200+index%2_000),
				&latency,
			)
			if index%3 == 0 {
				event.ResolvedModel = model + "-resolved"
			}
			if index%7 == 0 {
				event.ServiceTier = "priority"
			}
			events = append(events, event)
		}
		if _, err := db.InsertEvents(ctx, events); err != nil {
			b.Fatalf("insert benchmark events: %v", err)
		}
	}
}

func BenchmarkDashboardMonitoringRefreshPaths(b *testing.B) {
	for _, count := range []int{10_000, 100_000, 1_000_000} {
		b.Run(fmt.Sprintf("events_%dk", count/1_000), func(b *testing.B) {
			db, err := store.Open(filepath.Join(b.TempDir(), "usage.sqlite"))
			if err != nil {
				b.Fatalf("open store: %v", err)
			}
			b.Cleanup(func() { _ = db.Close() })

			ctx := context.Background()
			todayStartMS := int64(1_800_000_000_000)
			nowMS := todayStartMS + 24*hourWindowMs
			insertDashboardRecentBenchmarkEvents(b, ctx, db, todayStartMS, nowMS, count)
			for {
				result, err := db.CatchUpUsageHourlyAggregate(ctx, 10_000, nowMS)
				if err != nil {
					b.Fatalf("catch up dashboard rollup: %v", err)
				}
				if !result.Pending {
					break
				}
			}

			dashboardService := New(db)
			monitoringService := monitoringsvc.New(db, true)
			todayFilter := store.AnalyticsFilter{FromMS: todayStartMS, ToMS: nowMS, IncludeFailed: true}
			recentFromMS := nowMS - rollingWindowMs
			recentFilter := store.AnalyticsFilter{FromMS: recentFromMS, ToMS: nowMS, IncludeFailed: true}

			benchmarks := []struct {
				name string
				run  func() error
			}{
				{
					name: "dashboard_full_refresh",
					run: func() error {
						_, err := dashboardService.Summary(ctx, SummaryParams{
							TodayStartMS:   todayStartMS,
							NowMS:          nowMS,
							TopModels:      5,
							RecentFailures: 5,
						})
						return err
					},
				},
				{
					name: "rolling_30m",
					run: func() error {
						_, err := db.AggregateBetween(ctx, recentFromMS, nowMS)
						return err
					},
				},
				{
					name: "health_timeline_5m",
					run: func() error {
						_, err := db.BucketTimelineBetween(ctx, todayStartMS, nowMS, 5*60*1000)
						return err
					},
				},
				{
					name: "health_timeline_10m",
					run: func() error {
						_, err := db.BucketTimelineBetween(ctx, todayStartMS, nowMS, healthTimelineBucketMs)
						return err
					},
				},
				{
					name: "recent_failures",
					run: func() error {
						_, err := db.RecentFailuresBetween(ctx, todayStartMS, nowMS, 5)
						return err
					},
				},
				{
					name: "channel_stats",
					run: func() error {
						_, err := db.ChannelModelStatsWithFilter(ctx, todayFilter)
						return err
					},
				},
				{
					name: "failure_sources",
					run: func() error {
						_, err := db.FailureSourcesWithFilter(ctx, todayFilter)
						return err
					},
				},
				{
					name: "recent_query_combo",
					run: func() error {
						if _, err := db.AggregateWithFilter(ctx, recentFilter); err != nil {
							return err
						}
						if _, err := db.BucketTimelineBetween(ctx, recentFromMS, nowMS, 5*60*1000); err != nil {
							return err
						}
						if _, err := db.RecentFailuresWithFilter(ctx, recentFilter, 8); err != nil {
							return err
						}
						if _, err := db.ChannelModelStatsWithFilter(ctx, recentFilter); err != nil {
							return err
						}
						_, err := db.FailureSourcesWithFilter(ctx, recentFilter)
						return err
					},
				},
				{
					name: "monitoring_recent_refresh",
					run: func() error {
						_, err := monitoringService.Analytics(ctx, monitoringsvc.Request{
							FromMS:   recentFromMS,
							ToMS:     nowMS,
							NowMS:    nowMS,
							TimeZone: "UTC",
							Include: monitoringsvc.Include{
								Summary:        true,
								Timeline:       true,
								ChannelShare:   true,
								FailureSources: true,
								RecentFailures: 8,
								EventsPage:     &monitoringsvc.EventsPage{Limit: 50},
								Granularity:    "hour",
							},
						})
						return err
					},
				},
			}

			for _, benchmark := range benchmarks {
				b.Run(benchmark.name, func(b *testing.B) {
					b.ReportAllocs()
					for range b.N {
						if err := benchmark.run(); err != nil {
							b.Fatalf("%s: %v", benchmark.name, err)
						}
					}
				})
			}
		})
	}
}

func insertDashboardRecentBenchmarkEvents(b *testing.B, ctx context.Context, db *store.Store, fromMS, toMS int64, count int) {
	b.Helper()
	const batchSize = 1_000
	stepMS := max(int64(1), (toMS-fromMS)/int64(count))
	for start := 0; start < count; start += batchSize {
		end := min(start+batchSize, count)
		events := make([]usage.Event, 0, end-start)
		for index := start; index < end; index++ {
			timestampMS := fromMS + int64(index)*stepMS
			latencyMS := int64(50 + index%2_000)
			model := fmt.Sprintf("benchmark-model-%02d", index%12)
			event := dashboardEvent(
				fmt.Sprintf("dashboard-recent-benchmark-%07d", index),
				timestampMS,
				model,
				index%20 == 0,
				int64(100+index%1_000),
				int64(50+index%500),
				int64(index%100),
				int64(index%200),
				int64(index%50),
				int64(200+index%2_000),
				&latencyMS,
			)
			event.ResolvedModel = model
			event.ServiceTier = []string{"", "default", "priority"}[index%3]
			event.AuthIndex = fmt.Sprintf("auth-%03d", index%200)
			event.Source = fmt.Sprintf("source-%03d", index%200)
			event.SourceHash = fmt.Sprintf("source-hash-%03d", index%200)
			event.APIKeyHash = fmt.Sprintf("api-key-%03d", index%100)
			event.AccountSnapshot = fmt.Sprintf("account-%03d@example.com", index%200)
			event.AuthLabelSnapshot = fmt.Sprintf("Account %03d", index%200)
			event.AuthProviderSnapshot = []string{"codex", "claude", "gemini"}[index%3]
			event.AuthProjectIDSnapshot = fmt.Sprintf("project-%02d", index%20)
			event.Endpoint = "/v1/responses"
			if event.Failed {
				event.FailSummary = fmt.Sprintf("benchmark failure %d", index%10)
			}
			events = append(events, event)
		}
		if _, err := db.InsertEvents(ctx, events); err != nil {
			b.Fatalf("insert benchmark events: %v", err)
		}
	}
}
