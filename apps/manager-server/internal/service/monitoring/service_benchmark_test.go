package monitoring

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/usagehourly"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func BenchmarkUsageAnalyticsIncludeProfiles(b *testing.B) {
	db, err := store.Open(filepath.Join(b.TempDir(), "usage.sqlite"))
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	fromMS := int64(1_800_000_000_000)
	toMS := fromMS + 30*24*60*60*1000
	insertMonitoringBenchmarkEvents(b, ctx, db, fromMS, toMS, 100_000)
	for {
		result, err := db.CatchUpUsageHourlyAggregate(ctx, 5_000, toMS)
		if err != nil {
			b.Fatalf("catch up hourly rollup: %v", err)
		}
		if !result.Pending {
			break
		}
	}
	rawService := New(db, false)
	rollupService := New(db, true)

	request := func(include Include) Request {
		return Request{
			FromMS:   fromMS,
			ToMS:     toMS,
			NowMS:    toMS,
			TimeZone: "UTC",
			Include:  include,
		}
	}
	selectors := request(Include{FilterOptions: true, FilterSelectors: true})
	selectedCredentialTimeline := request(Include{
		CredentialTimeline: true,
		Granularity:        "day",
	})
	selectedCredentialTimeline.Filters.CredentialIDs = []string{"account-000.json"}
	monitoringFull := request(Include{
		Summary:            true,
		Timeline:           true,
		HourlyDistribution: true,
		ModelShare:         true,
		ChannelShare:       true,
		ModelStats:         true,
		FailureSources:     true,
		AccountStats:       true,
		APIKeyStats:        true,
		FilterOptions:      true,
		TaskBuckets:        true,
		RecentFailures:     8,
		EventsPage:         &EventsPage{Limit: 500},
		Granularity:        "day",
	})
	monitoringCurlScope := monitoringFull
	monitoringCurlScope.FromMS = 1
	monitoringCurlScope.TimeZone = "Asia/Shanghai"
	monitoringFullWithoutFilterOptions := monitoringFull
	monitoringFullWithoutFilterOptions.Include.FilterOptions = false
	monitoringFullFiltered := monitoringFull
	monitoringFullFiltered.Filters.Models = []string{"gpt-00"}
	monitoringAccountsCompact := request(Include{
		Summary:        true,
		SummaryProfile: "compact",
		AccountStats:   true,
		EventsPage:     &EventsPage{Limit: 500},
		Granularity:    "day",
	})
	monitoringAPIKeysCompact := request(Include{
		Summary:        true,
		SummaryProfile: "compact",
		APIKeyStats:    true,
		EventsPage:     &EventsPage{Limit: 500},
		Granularity:    "day",
	})
	monitoringRealtimeCompact := request(Include{
		Summary:        true,
		SummaryProfile: "compact",
		EventsPage:     &EventsPage{Limit: 500},
		Granularity:    "day",
	})
	profiles := []struct {
		name     string
		service  *Service
		requests []Request
	}{
		{
			name:    "legacy_full",
			service: rollupService,
			requests: []Request{request(Include{
				Summary:            true,
				SummaryComparison:  true,
				Timeline:           true,
				ModelStats:         true,
				ChannelShare:       true,
				APIKeyStats:        true,
				CredentialStats:    true,
				CredentialTimeline: true,
				FilterOptions:      true,
				Heatmap:            true,
				AnomalyPoints:      true,
				Granularity:        "day",
			})},
		},
		{name: "monitoring_full", service: rollupService, requests: []Request{monitoringFull}},
		{name: "monitoring_curl_scope", service: rollupService, requests: []Request{monitoringCurlScope}},
		{name: "monitoring_full_without_filter_options", service: rollupService, requests: []Request{monitoringFullWithoutFilterOptions}},
		{name: "monitoring_full_filtered", service: rollupService, requests: []Request{monitoringFullFiltered}},
		{name: "monitoring_accounts_compact", service: rollupService, requests: []Request{monitoringAccountsCompact}},
		{name: "monitoring_api_keys_compact", service: rollupService, requests: []Request{monitoringAPIKeysCompact}},
		{name: "monitoring_realtime_compact", service: rollupService, requests: []Request{monitoringRealtimeCompact}},
		{name: "filter_options_only", service: rollupService, requests: []Request{request(Include{FilterOptions: true})}},
		{
			name:    "overview_initial",
			service: rollupService,
			requests: []Request{
				request(Include{
					Summary:            true,
					SummaryProfile:     "compact",
					SummaryPercentiles: true,
					SummaryComparison:  true,
					Timeline:           true,
					ModelStats:         true,
					ChannelShare:       true,
					APIKeyStats:        true,
					AnomalyPoints:      true,
					Granularity:        "day",
				}),
				selectors,
			},
		},
		{
			name:    "overview_tab_raw",
			service: rawService,
			requests: []Request{request(Include{
				Summary:            true,
				SummaryProfile:     "compact",
				SummaryPercentiles: true,
				SummaryComparison:  true,
				Timeline:           true,
				ModelStats:         true,
				ChannelShare:       true,
				APIKeyStats:        true,
				AnomalyPoints:      true,
				Granularity:        "day",
			})},
		},
		{
			name:    "overview_tab_rollup",
			service: rollupService,
			requests: []Request{request(Include{
				Summary:           true,
				SummaryComparison: true,
				Timeline:          true,
				ModelStats:        true,
				ChannelShare:      true,
				APIKeyStats:       true,
				AnomalyPoints:     true,
				Granularity:       "day",
			})},
		},
		{
			name:    "overview_tab_compact",
			service: rollupService,
			requests: []Request{request(Include{
				Summary:            true,
				SummaryProfile:     "compact",
				SummaryPercentiles: true,
				SummaryComparison:  true,
				Timeline:           true,
				ModelStats:         true,
				ChannelShare:       true,
				APIKeyStats:        true,
				AnomalyPoints:      true,
				Granularity:        "day",
			})},
		},
		{
			name:    "analytics_core_raw",
			service: rawService,
			requests: []Request{request(Include{
				Summary:           true,
				SummaryComparison: true,
				Timeline:          true,
				ModelStats:        true,
				Granularity:       "day",
			})},
		},
		{
			name:    "summary_full",
			service: rollupService,
			requests: []Request{request(Include{
				Summary: true,
			})},
		},
		{
			name:    "summary_compact",
			service: rollupService,
			requests: []Request{request(Include{
				Summary:            true,
				SummaryProfile:     "compact",
				SummaryPercentiles: true,
			})},
		},
		{
			name:    "summary_compact_no_percentiles",
			service: rollupService,
			requests: []Request{request(Include{
				Summary:        true,
				SummaryProfile: "compact",
			})},
		},
		{
			name:    "analytics_core_rollup",
			service: rollupService,
			requests: []Request{request(Include{
				Summary:           true,
				SummaryComparison: true,
				Timeline:          true,
				ModelStats:        true,
				Granularity:       "day",
			})},
		},
		{
			name:    "analytics_core_compact",
			service: rollupService,
			requests: []Request{request(Include{
				Summary:            true,
				SummaryProfile:     "compact",
				SummaryPercentiles: true,
				SummaryComparison:  true,
				Timeline:           true,
				ModelStats:         true,
				Granularity:        "day",
			})},
		},
		{
			name:    "analytics_core_compact_no_summary_percentiles",
			service: rollupService,
			requests: []Request{request(Include{
				Summary:           true,
				SummaryProfile:    "compact",
				SummaryComparison: true,
				Timeline:          true,
				ModelStats:        true,
				Granularity:       "day",
			})},
		},
		{
			name:    "trends_tab_request",
			service: rollupService,
			requests: []Request{request(Include{
				Summary:           true,
				SummaryProfile:    "compact",
				SummaryComparison: true,
				Timeline:          true,
				ModelStats:        true,
				APIKeyStats:       true,
				AnomalyPoints:     true,
				Granularity:       "day",
			})},
		},
		{
			name:    "models_tab_request",
			service: rollupService,
			requests: []Request{request(Include{
				Summary:        true,
				SummaryProfile: "compact",
				Timeline:       true,
				ModelStats:     true,
				APIKeyStats:    true,
				Granularity:    "day",
			})},
		},
		{
			name:    "api_keys_tab_request",
			service: rollupService,
			requests: []Request{request(Include{
				Summary:        true,
				SummaryProfile: "compact",
				APIKeyStats:    true,
				Granularity:    "day",
			})},
		},
		{
			name:    "credentials_tab_request",
			service: rollupService,
			requests: []Request{request(Include{
				Summary:         true,
				SummaryProfile:  "compact",
				CredentialStats: true,
				Granularity:     "day",
			})},
		},
		{
			name:     "selected_credential_timeline_request",
			service:  rollupService,
			requests: []Request{selectedCredentialTimeline},
		},
		{
			name:    "credentials_tab_two_stage",
			service: rollupService,
			requests: []Request{
				request(Include{
					Summary:         true,
					SummaryProfile:  "compact",
					CredentialStats: true,
					Granularity:     "day",
				}),
				selectedCredentialTimeline,
			},
		},
		{
			name:    "heatmap_tab_request",
			service: rollupService,
			requests: []Request{request(Include{
				Summary:        true,
				SummaryProfile: "compact",
				Heatmap:        true,
				Granularity:    "day",
			})},
		},
		{name: "filter_selectors", service: rollupService, requests: []Request{selectors}},
	}

	for _, profile := range profiles {
		b.Run(profile.name, func(b *testing.B) {
			b.ReportAllocs()
			for range b.N {
				for _, req := range profile.requests {
					if _, err := profile.service.Analytics(ctx, req); err != nil {
						b.Fatalf("analytics: %v", err)
					}
				}
			}
		})
	}
}

func BenchmarkUsageAnalyticsHourlyCorePaths(b *testing.B) {
	db, err := store.Open(filepath.Join(b.TempDir(), "usage.sqlite"))
	if err != nil {
		b.Fatalf("open store: %v", err)
	}
	b.Cleanup(func() { _ = db.Close() })

	ctx := context.Background()
	fromMS := int64(1_800_000_000_000)
	toMS := fromMS + 30*24*60*60*1000
	insertMonitoringBenchmarkEvents(b, ctx, db, fromMS, toMS, 100_000)
	for {
		result, err := db.CatchUpUsageHourlyAggregate(ctx, 5_000, toMS)
		if err != nil {
			b.Fatalf("catch up hourly rollup: %v", err)
		}
		if !result.Pending {
			break
		}
	}
	filter := store.AnalyticsFilter{FromMS: fromMS, ToMS: toMS, IncludeFailed: true}
	reader := usagehourly.New(db, true)

	b.Run("raw_core", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := db.AggregateWithFilter(ctx, filter); err != nil {
				b.Fatalf("aggregate: %v", err)
			}
			if _, err := db.ModelStatsWithFilter(ctx, filter, 0); err != nil {
				b.Fatalf("model stats: %v", err)
			}
		}
	})

	b.Run("rollup_core", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, ok := reader.LoadAnalytics(ctx, filter, "day", time.UTC, false); !ok {
				b.Fatal("rollup unavailable")
			}
		}
	})

	b.Run("raw_with_timeline", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			if _, err := db.AggregateWithFilter(ctx, filter); err != nil {
				b.Fatalf("aggregate: %v", err)
			}
			if _, err := db.ModelStatsWithFilter(ctx, filter, 0); err != nil {
				b.Fatalf("model stats: %v", err)
			}
			if _, err := db.TimelineWithFilter(ctx, filter, "day", time.UTC); err != nil {
				b.Fatalf("timeline: %v", err)
			}
		}
	})

	b.Run("rollup_with_timeline", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			snapshot, ok := reader.LoadAnalytics(ctx, filter, "day", time.UTC, true)
			if !ok {
				b.Fatal("rollup unavailable")
			}
			if _, ok := reader.AnalyticsTimeline(ctx, snapshot, "day", time.UTC); !ok {
				b.Fatal("rollup timeline unavailable")
			}
		}
	})
}

func insertMonitoringBenchmarkEvents(b *testing.B, ctx context.Context, db *store.Store, fromMS, toMS int64, count int) {
	b.Helper()
	const batchSize = 1000
	stepMS := max(int64(1), (toMS-fromMS)/int64(count))
	latencyMS := int64(250)
	ttftMS := int64(50)
	for offset := 0; offset < count; offset += batchSize {
		end := min(offset+batchSize, count)
		events := make([]usage.Event, 0, end-offset)
		for index := offset; index < end; index++ {
			timestampMS := fromMS + int64(index)*stepMS
			authIndex := fmt.Sprintf("auth-%03d", index%100)
			event := monitoringEvent(
				fmt.Sprintf("analytics-benchmark-%06d", index),
				timestampMS,
				fmt.Sprintf("gpt-%02d", index%12),
				authIndex,
				fmt.Sprintf("source-%03d", index%100),
				index%20 == 0,
				int64(100+index%300),
				int64(50+index%150),
				int64(index%40),
				int64(index%80),
				int64(150+index%500),
				&latencyMS,
			)
			event.APIKeyHash = fmt.Sprintf("key-%03d", index%50)
			event.AccountSnapshot = fmt.Sprintf("account-%03d@example.com", index%100)
			event.AuthLabelSnapshot = fmt.Sprintf("Account %03d", index%100)
			event.AuthFileSnapshot = fmt.Sprintf("account-%03d.json", index%100)
			event.AuthProviderSnapshot = []string{"codex", "claude", "gemini"}[index%3]
			event.AuthProjectIDSnapshot = fmt.Sprintf("project-%02d", index%10)
			event.ServiceTier = []string{"", "default", "priority"}[index%3]
			event.TTFTMS = &ttftMS
			events = append(events, event)
		}
		if _, err := db.InsertEvents(ctx, events); err != nil {
			b.Fatalf("insert benchmark events: %v", err)
		}
	}
}
