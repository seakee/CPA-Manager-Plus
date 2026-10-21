package monitoring

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestAnalyticsRecoversIncompletePricingFromRetainedEvents(t *testing.T) {
	for _, test := range []struct {
		name string
		sql  string
	}{
		{"missing cache", `delete from usage_pricing_hourly_rollups_v1`},
		{"missing model", `delete from usage_pricing_hourly_rollups_v1 where model = 'tiered'`},
		{"same calls wrong tokens", `update usage_pricing_hourly_rollups_v1 set input_tokens = input_tokens + 1`},
		{"same calls wrong bucket", `update usage_pricing_hourly_rollups_v1 set bucket_ms = bucket_ms + 36000000`},
		{"unsupported cache", `update usage_pricing_rollup_state set schema_version = 0`},
		{"stale pricing revision", `update usage_pricing_rollup_state set structure_revision = 'obsolete'`},
		{"projection with raw tail", `update usage_monitoring_rollup_state set coverage_event_id = coverage_event_id - 1
			where rollup_name = 'projection_v1'; delete from usage_pricing_hourly_rollups_v1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, sqlDB, request, want := pricingCoverageFixture(t)
			ctx := context.Background()
			if _, err := sqlDB.ExecContext(ctx, test.sql); err != nil {
				t.Fatalf("damage isolated pricing cache: %v", err)
			}
			before := pricingCoverageCounts(t, sqlDB)
			got, err := New(db, true).Analytics(ctx, request)
			if err != nil {
				t.Fatalf("recover archived analytics: %v", err)
			}
			assertPricingCoverageAnalytics(t, got, want)
			if got.Coverage == nil || !got.Coverage.CoreAggregateUsed || got.Coverage.RawDeletedEventCount == 0 {
				t.Fatalf("test did not read deleted history: %#v", got.Coverage)
			}
			if after := pricingCoverageCounts(t, sqlDB); after != before {
				t.Fatalf("read modified retained history/cache: before=%v after=%v", before, after)
			}
		})
	}
}

func TestAnalyticsIncompletePricingFailsClosedWithoutCompatibleEvents(t *testing.T) {
	for _, test := range []struct {
		name string
		sql  string
	}{
		{"missing archived projection", `delete from usage_monitoring_event_projection_v1 where event_id not in (select id from usage_events)`},
		{"obsolete projection", `update usage_monitoring_rollup_state set structure_revision = 'obsolete' where rollup_name = 'projection_v1'`},
		{"unsupported projection", `update usage_monitoring_rollup_state set schema_version = 0 where rollup_name = 'projection_v1'`},
		{"clearing projection", `update usage_monitoring_rollup_state set status = 'clearing' where rollup_name = 'projection_v1'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, sqlDB, request, _ := pricingCoverageFixture(t)
			ctx := context.Background()
			if _, err := sqlDB.ExecContext(ctx, `delete from usage_pricing_hourly_rollups_v1`); err != nil {
				t.Fatal(err)
			}
			if _, err := sqlDB.ExecContext(ctx, test.sql); err != nil {
				t.Fatal(err)
			}
			got, err := New(db, true).Analytics(ctx, request)
			if !errors.Is(err, store.ErrUsagePricingCoverageIncomplete) || got.Summary != nil {
				t.Fatalf("incomplete history returned success: summary=%#v error=%v", got.Summary, err)
			}
		})
	}
}

func TestAnalyticsPrimaryPeriodRecoveryQueryErrorFailsFatal(t *testing.T) {
	db, sqlDB, request, _ := pricingCoverageFixture(t)
	ctx := context.Background()
	if _, err := sqlDB.ExecContext(ctx, `delete from usage_pricing_hourly_rollups_v1`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(ctx, `alter table usage_monitoring_event_projection_v1 rename column normalized_total_input_tokens to unavailable_tokens`); err != nil {
		t.Fatal(err)
	}
	got, err := New(db, true).Analytics(ctx, request)
	if err == nil || !errors.Is(err, store.ErrUsagePricingRecoveryFailed) || errors.Is(err, store.ErrUsagePricingCoverageIncomplete) {
		t.Fatalf("expected ErrUsagePricingRecoveryFailed, got err=%v", err)
	}
	if got.Summary != nil {
		t.Fatalf("query error returned summary: %#v", got.Summary)
	}
}

func TestAnalyticsPricingCoverageChecksComparisonPeriod(t *testing.T) {
	db, sqlDB, request, _ := pricingCoverageFixture(t)
	ctx := context.Background()
	request.FromMS += 2 * time.Hour.Milliseconds()
	request.ToMS = request.FromMS + time.Hour.Milliseconds()
	if _, err := sqlDB.ExecContext(ctx, `delete from usage_pricing_hourly_rollups_v1 where bucket_ms < ?;
		delete from usage_monitoring_event_projection_v1 where timestamp_ms < ?`, request.FromMS, request.FromMS); err != nil {
		t.Fatal(err)
	}
	// Compare the online hour with an archived hour. Neither a successful current
	// snapshot nor its raw tail may hide an incomplete comparison result.
	request.Include.SummaryComparison = false
	if current, err := New(db, true).Analytics(ctx, request); err != nil || current.Summary == nil || current.Summary.TotalCalls != 1 {
		t.Fatalf("current period must be complete: %#v error=%v", current.Summary, err)
	}
	request.Include.SummaryComparison = true
	got, err := New(db, true).Analytics(ctx, request)
	if err != nil {
		t.Fatalf("analytics failed with incomplete comparison pricing: %v", err)
	}
	if got.Summary == nil || got.Summary.TotalCalls != 1 {
		t.Fatalf("current summary expected 1 call, got: %#v", got.Summary)
	}
	if got.SummaryComparison != nil {
		t.Fatalf("summary comparison must be omitted when incomplete: %#v", got.SummaryComparison)
	}
	if got.Coverage == nil {
		t.Fatalf("coverage must be populated")
	}
	hasLimitation := false
	for _, lim := range got.Coverage.FidelityLimitations {
		if lim == "summary_comparison_requires_raw_events" {
			hasLimitation = true
			break
		}
	}
	if !hasLimitation {
		t.Fatalf("coverage limitations must contain summary_comparison_requires_raw_events: %#v", got.Coverage.FidelityLimitations)
	}
}

func TestAnalyticsComparisonPeriodRecoveryQueryErrorFailsFatal(t *testing.T) {
	db, sqlDB, request, _ := pricingCoverageFixture(t)
	ctx := context.Background()
	request.FromMS += 2 * time.Hour.Milliseconds()
	request.ToMS = request.FromMS + time.Hour.Milliseconds()
	if _, err := sqlDB.ExecContext(ctx, `delete from usage_pricing_hourly_rollups_v1 where bucket_ms < ?;
		alter table usage_monitoring_event_projection_v1 rename column normalized_total_input_tokens to unavailable_tokens`, request.FromMS); err != nil {
		t.Fatal(err)
	}
	request.Include.SummaryComparison = true
	got, err := New(db, true).Analytics(ctx, request)
	if err == nil || !errors.Is(err, store.ErrUsagePricingRecoveryFailed) || errors.Is(err, store.ErrUsagePricingCoverageIncomplete) {
		t.Fatalf("expected ErrUsagePricingRecoveryFailed on comparison recovery, got err=%v", err)
	}
	if got.Summary != nil {
		t.Fatalf("query error returned summary: %#v", got.Summary)
	}
}

func TestAnalyticsPricingRecoveryPreservesFiltersAndCollapsedBuckets(t *testing.T) {
	includeFailed := false
	for _, test := range []struct {
		name     string
		filters  Filters
		timeline bool
	}{
		{"success only", Filters{IncludeFailed: &includeFailed}, true},
		{"failed only", Filters{FailedOnly: true}, true},
		{"single model", Filters{Models: []string{"tiered"}}, true},
		{"summary only", Filters{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, sqlDB, request, _ := pricingCoverageFixture(t)
			ctx := context.Background()
			request.Filters = test.filters
			request.Include.Timeline = test.timeline
			want, err := New(db, true).Analytics(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := sqlDB.ExecContext(ctx, `delete from usage_pricing_hourly_rollups_v1`); err != nil {
				t.Fatal(err)
			}
			got, err := New(db, true).Analytics(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			assertPricingCoverageAnalytics(t, got, want)
		})
	}
}

func TestPricingCatchUpRebuildsArchivedHistoryFromRetainedProjection(t *testing.T) {
	for _, test := range []struct {
		name string
		sql  string
	}{
		{"revision change", `update usage_pricing_rollup_state set structure_revision = 'obsolete'`},
		{"resumed clearing", `update usage_pricing_rollup_state set status = 'clearing', coverage_event_id = 0, backfill_last_event_id = 0`},
		{"resumed rebuilding", `delete from usage_pricing_hourly_rollups_v1;
			delete from usage_pricing_account_rollups_v1;
			update usage_pricing_rollup_state set status = 'rebuilding', coverage_event_id = 0, backfill_last_event_id = 0`},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, sqlDB, _, _ := pricingCoverageFixture(t)
			ctx := context.Background()
			before := pricingCoverageCounts(t, sqlDB)
			if _, err := sqlDB.ExecContext(ctx, test.sql); err != nil {
				t.Fatal(err)
			}
			result, err := db.CatchUpUsagePricing(ctx, 100, time.Now().UnixMilli())
			if err != nil {
				t.Fatalf("rebuild from retained projection: %v", err)
			}
			if !result.Rebuilt || result.Pending || result.CoverageEventID < result.TargetEventID {
				t.Fatalf("unexpected retained rebuild result: %#v", result)
			}
			stateAfter, err := db.UsagePricingState(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if stateAfter.Status != "ready" || stateAfter.CoverageEventID < stateAfter.TargetEventID {
				t.Fatalf("retained rebuild state = %#v", stateAfter)
			}
			if after := pricingCoverageCounts(t, sqlDB); after != before {
				t.Fatalf("retained rebuild changed historical coverage: before=%v after=%v", before, after)
			}
		})
	}
}

func TestAccountHistoryRecoversIncompleteArchivedPricing(t *testing.T) {
	for _, test := range []struct {
		name string
		sql  string
	}{
		{"missing cache", `delete from usage_pricing_account_rollups_v1`},
		{"same calls wrong tokens", `update usage_pricing_account_rollups_v1 set input_tokens = input_tokens + 1`},
		{"same calls wrong date", `update usage_pricing_account_rollups_v1 set first_seen_ms = first_seen_ms + 1`},
		{"projection with raw tail", `update usage_monitoring_rollup_state set coverage_event_id = coverage_event_id - 1
			where rollup_name = 'projection_v1'; delete from usage_pricing_account_rollups_v1`},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, sqlDB, _, analytics := pricingCoverageFixture(t)
			ctx := context.Background()
			request := pricingCoverageAccountRequest()
			want, err := New(db).AccountHistory(ctx, request)
			if err != nil || len(want.Items) != 2 {
				t.Fatalf("baseline account history = %#v, %v", want, err)
			}
			if calls := want.Items[0].TotalRequests + want.Items[1].TotalRequests; calls != 8 {
				t.Fatalf("baseline history calls = %d, want 8", calls)
			}
			wantCost := analytics.Summary.TotalCost + analytics.SummaryComparison.TotalCost
			if want.Items[0].TotalCost == nil || want.Items[1].TotalCost == nil {
				t.Fatalf("baseline account costs unavailable: %#v", want.Items)
			}
			if delta := *want.Items[0].TotalCost + *want.Items[1].TotalCost - wantCost; delta < -0.000001 || delta > 0.000001 {
				t.Fatalf("baseline account costs differ from hourly costs: %v", delta)
			}
			if _, err := sqlDB.ExecContext(ctx, test.sql); err != nil {
				t.Fatal(err)
			}
			before := pricingCoverageCounts(t, sqlDB)
			got, err := New(db).AccountHistory(ctx, request)
			if err != nil || !reflect.DeepEqual(got.Items, want.Items) {
				t.Fatalf("recovered account history differs: got=%#v want=%#v error=%v", got.Items, want.Items, err)
			}
			if after := pricingCoverageCounts(t, sqlDB); after != before {
				t.Fatalf("account history read changed data: before=%v after=%v", before, after)
			}
		})
	}
}

func TestAccountHistoryDegradesWhenArchivedPricingCannotBeRecovered(t *testing.T) {
	for _, test := range []struct {
		name    string
		catchUp bool
		sql     string
	}{
		{name: "read", sql: `delete from usage_pricing_account_rollups_v1;
			delete from usage_monitoring_event_projection_v1 where event_id not in (select id from usage_events)`},
		{name: "catch up", catchUp: true, sql: `delete from usage_pricing_account_rollups_v1;
			delete from usage_monitoring_event_projection_v1 where event_id not in (select id from usage_events);
			update usage_pricing_rollup_state set structure_revision = 'obsolete'`},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, sqlDB, _, _ := pricingCoverageFixture(t)
			ctx := context.Background()
			request := pricingCoverageAccountRequest()
			want, err := New(db).AccountHistory(ctx, request)
			if err != nil || len(want.Items) != 2 {
				t.Fatalf("baseline account history = %#v, %v", want, err)
			}
			if _, err := sqlDB.ExecContext(ctx, test.sql); err != nil {
				t.Fatal(err)
			}
			request.CatchUp = test.catchUp
			got, err := New(db).AccountHistory(ctx, request)
			if err != nil || len(got.Items) != len(want.Items) {
				t.Fatalf("degraded account history = %#v, %v", got, err)
			}
			for index := range got.Items {
				item, baseline := got.Items[index], want.Items[index]
				if !item.Matched || item.TotalCost != nil {
					t.Fatalf("degraded item availability = %#v", item)
				}
				if item.TotalRequests != baseline.TotalRequests || item.SuccessCalls != baseline.SuccessCalls ||
					item.FailureCalls != baseline.FailureCalls || item.TotalTokens != baseline.TotalTokens ||
					!reflect.DeepEqual(item.SuccessRate, baseline.SuccessRate) ||
					!reflect.DeepEqual(item.FirstSeenMS, baseline.FirstSeenMS) ||
					!reflect.DeepEqual(item.LastSeenMS, baseline.LastSeenMS) {
					t.Fatalf("degraded core history differs: got=%#v want=%#v", item, baseline)
				}
				if !reflect.DeepEqual(item.LatestRequest, baseline.LatestRequest) ||
					!reflect.DeepEqual(item.RecentRequests, baseline.RecentRequests) {
					t.Fatalf("degraded recent requests differ: got=%#v want=%#v", item, baseline)
				}
			}
		})
	}
}

func TestAccountHistoryIsolatesUnrecoverablePricingByAccount(t *testing.T) {
	db, sqlDB, _, _ := pricingCoverageFixture(t)
	ctx := context.Background()
	request := pricingCoverageAccountRequest()
	want, err := New(db).AccountHistory(ctx, request)
	if err != nil || len(want.Items) != 2 || want.Items[0].TotalCost == nil || want.Items[1].TotalCost == nil {
		t.Fatalf("baseline account history = %#v, %v", want.Items, err)
	}

	if _, err := sqlDB.ExecContext(ctx, `delete from usage_pricing_account_rollups_v1 where auth_index = 'a';
		delete from usage_monitoring_event_projection_v1
		where auth_index = 'a' and event_id not in (select id from usage_events)`); err != nil {
		t.Fatal(err)
	}
	got, err := New(db).AccountHistory(ctx, request)
	if err != nil || len(got.Items) != len(want.Items) {
		t.Fatalf("isolated account history = %#v, %v", got.Items, err)
	}

	wantA := want.Items[0]
	wantA.TotalCost = nil
	if !reflect.DeepEqual(got.Items[0], wantA) {
		t.Fatalf("incomplete account was not isolated: got=%#v want=%#v", got.Items[0], wantA)
	}
	if !reflect.DeepEqual(got.Items[1], want.Items[1]) {
		t.Fatalf("complete account pricing was degraded: got=%#v want=%#v", got.Items[1], want.Items[1])
	}
}

func TestAccountHistoryDoesNotHidePricingRecoveryQueryErrors(t *testing.T) {
	db, sqlDB, _, _ := pricingCoverageFixture(t)
	ctx := context.Background()
	if _, err := sqlDB.ExecContext(ctx, `delete from usage_pricing_account_rollups_v1 where auth_index = 'a';
		alter table usage_monitoring_event_projection_v1
		rename column normalized_total_input_tokens to unavailable_tokens`); err != nil {
		t.Fatal(err)
	}
	_, err := New(db).AccountHistory(ctx, pricingCoverageAccountRequest())
	if err == nil || errors.Is(err, store.ErrUsagePricingCoverageIncomplete) {
		t.Fatalf("pricing recovery query error was hidden: %v", err)
	}
}

func TestAccountHistoryDoesNotHideCoreHistoryReadErrors(t *testing.T) {
	db, sqlDB, _, _ := pricingCoverageFixture(t)
	ctx := context.Background()
	if _, err := sqlDB.ExecContext(ctx, `alter table usage_account_model_rollups rename column total_tokens to unavailable_tokens`); err != nil {
		t.Fatal(err)
	}
	_, err := New(db).AccountHistory(ctx, pricingCoverageAccountRequest())
	if err == nil || errors.Is(err, store.ErrUsagePricingCoverageIncomplete) {
		t.Fatalf("core history read error was hidden: %v", err)
	}
}

func pricingCoverageAccountRequest() AccountHistoryRequest {
	return AccountHistoryRequest{Accounts: []AccountHistoryTarget{
		{RowKey: "a", AuthFileSnapshot: "coverage.json", AuthIndex: "a", AuthProviderSnapshot: "openai", AccountSnapshot: "user@example.com"},
		{RowKey: "b", AuthFileSnapshot: "coverage.json", AuthIndex: "b", AuthProviderSnapshot: "openai", AccountSnapshot: "user@example.com"},
	}}
}

func pricingCoverageFixture(t *testing.T) (*store.Store, *sql.DB, Request, Response) {
	t.Helper()
	sqlDB, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db := store.New(sqlDB)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	fromMS := time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	hour := time.Hour.Milliseconds()
	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"tiered": {
			Prompt: 1, Completion: 2, Cache: 0.1,
			ContextTiers: []store.ModelPriceContextTier{{ThresholdTokens: 100_000, Prompt: 3, PromptConfigured: true}},
		},
		"service": {
			Prompt: 2, Completion: 4,
			ServiceTiers: []store.ModelPriceServiceTier{{Mode: "fast", ServiceTier: "priority", Prompt: 7, PromptConfigured: true}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	latency := int64(321)
	events := []usage.Event{
		monitoringEvent("coverage-prior", fromMS-hour+1_000, "tiered", "a", "a", false, 250_000, 100, 10, 20, 250_100, &latency),
		monitoringEvent("coverage-base", fromMS+1_000, "tiered", "a", "a", false, 100_000, 20, 5, 10, 100_020, &latency),
		monitoringEvent("coverage-context", fromMS+2_000, "tiered", "a", "a", false, 100_001, 20, 5, 10, 100_021, &latency),
		monitoringEvent("coverage-priority", fromMS+hour+1_000, "service", "b", "b", false, 300_000, 40, 2, 0, 300_040, &latency),
		monitoringEvent("coverage-failed", fromMS+hour+2_000, "service", "b", "b", true, 80, 40, 2, 0, 120, nil),
		monitoringEvent("coverage-zero", fromMS+hour+3_000, "service", "b", "b", false, 0, 0, 0, 0, 0, nil),
		monitoringEvent("coverage-online", fromMS+2*hour+1_000, "tiered", "a", "a", false, 120_000, 30, 3, 10, 120_030, &latency),
		monitoringEvent("coverage-tail", fromMS+3*hour+1_000, "service", "b", "b", false, 250_000, 50, 1, 0, 250_050, &latency),
	}
	events[3].ServiceTier = "priority"
	for index := range events {
		events[index].AuthFileSnapshot = "coverage.json"
		events[index].Provider = "openai"
		events[index].AuthProviderSnapshot = "openai"
	}
	if _, err := db.InsertEvents(ctx, events); err != nil {
		t.Fatal(err)
	}
	catchUpMonitoringArchiveDeleteReadiness(t, ctx, db)
	request := Request{
		FromMS: fromMS, ToMS: fromMS + 4*hour, NowMS: fromMS + 4*hour, TimeZone: "Asia/Shanghai",
		Include: Include{Summary: true, SummaryProfile: "compact", SummaryComparison: true, Timeline: true, ModelStats: true, ModelShare: true, Granularity: "hour"},
	}
	want, err := New(db, true).Analytics(ctx, request)
	if err != nil || want.Summary == nil || want.Summary.TotalCalls != 7 || want.Summary.TotalCost <= 0 {
		t.Fatalf("baseline analytics: summary=%#v error=%v", want.Summary, err)
	}
	archiveMonitoringEventsThrough(t, ctx, db, fromMS+2*hour)
	// Archived hourly counters/pricing remain exact, but percentile fidelity
	// still requires raw events under the existing analytics contract.
	for index := range want.Timeline {
		if want.Timeline[index].BucketMS < fromMS+2*hour {
			want.Timeline[index].P95LatencyMS = nil
			want.Timeline[index].P95TTFTMS = nil
		}
	}
	return db, sqlDB, request, want
}

func assertPricingCoverageAnalytics(t *testing.T, got, want Response) {
	t.Helper()
	if !reflect.DeepEqual(got.Summary, want.Summary) || !reflect.DeepEqual(got.SummaryComparison, want.SummaryComparison) ||
		!reflect.DeepEqual(got.Timeline, want.Timeline) || !reflect.DeepEqual(got.ModelStats, want.ModelStats) ||
		!reflect.DeepEqual(got.ModelShare, want.ModelShare) {
		t.Fatalf("recovered pricing differs from complete baseline\nsummary=%#v / %#v\ncomparison=%#v / %#v\ntimeline=%#v / %#v\nmodels=%#v / %#v",
			got.Summary, want.Summary, got.SummaryComparison, want.SummaryComparison, got.Timeline, want.Timeline, got.ModelStats, want.ModelStats)
	}
}

func pricingCoverageCounts(t *testing.T, db *sql.DB) [6]int64 {
	t.Helper()
	var result [6]int64
	if err := db.QueryRow(`select
		(select count(*) from usage_events),
		(select count(*) from usage_monitoring_event_projection_v1),
		(select count(*) from usage_pricing_hourly_rollups_v1),
		(select coalesce(sum(calls), 0) from usage_pricing_hourly_rollups_v1),
		(select count(*) from usage_pricing_account_rollups_v1),
		(select count(*) from usage_archive_event_refs where raw_deleted_at_ms is not null)`).Scan(
		&result[0], &result[1], &result[2], &result[3], &result[4], &result[5],
	); err != nil {
		t.Fatal(err)
	}
	return result
}
