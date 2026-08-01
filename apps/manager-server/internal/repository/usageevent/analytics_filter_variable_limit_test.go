package usageevent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestAnalyticsLargeFilterListsStayBelowSQLiteVariableLimit(t *testing.T) {
	repo := newAnalyticsPreaggregationRepo(t)
	ctx := context.Background()
	timestamp := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	if _, err := repo.InsertBatch(ctx, []usage.Event{{
		EventHash:       "large-filter-variable-limit",
		TimestampMS:     timestamp.UnixMilli(),
		Timestamp:       timestamp.Format(time.RFC3339Nano),
		Provider:        "provider-target",
		Model:           "gpt-test",
		AuthIndex:       "auth-target",
		AccountSnapshot: "account-target",
		InputTokens:     1,
		TotalTokens:     1,
		CreatedAtMS:     timestamp.UnixMilli(),
	}}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	tests := []struct {
		name   string
		count  int
		target string
		apply  func(*AnalyticsFilter, []string)
	}{
		{
			name:   "auth indices",
			count:  32_765,
			target: "auth-target",
			apply: func(filter *AnalyticsFilter, values []string) {
				filter.AuthIndices = values
			},
		},
		{
			name:   "providers",
			count:  16_383,
			target: "PROVIDER-TARGET",
			apply: func(filter *AnalyticsFilter, values []string) {
				filter.Providers = values
			},
		},
		{
			name:   "accounts",
			count:  8_192,
			target: "ACCOUNT-TARGET",
			apply: func(filter *AnalyticsFilter, values []string) {
				filter.Accounts = values
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filter := AnalyticsFilter{
				FromMS:        timestamp.Add(-time.Hour).UnixMilli(),
				ToMS:          timestamp.Add(time.Hour).UnixMilli(),
				IncludeFailed: true,
			}
			test.apply(&filter, largeAnalyticsFilterValues(test.name, test.target, test.count))

			stats, err := repo.ModelStatsWithFilter(ctx, filter, 0)
			if err != nil {
				t.Fatalf("query model stats: %v", err)
			}
			if len(stats) != 1 || stats[0].Model != "gpt-test" {
				t.Fatalf("model stats = %#v", stats)
			}
		})
	}
}

func largeAnalyticsFilterValues(prefix, target string, count int) []string {
	values := make([]string, count)
	for index := 0; index < count-1; index++ {
		values[index] = fmt.Sprintf("%s-%d", prefix, index)
	}
	values[count-1] = target
	return values
}
