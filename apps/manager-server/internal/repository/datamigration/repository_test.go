package datamigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	usageaggregaterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageaggregate"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagemonitoring"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagepricing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagerollup"
)

func TestDiscoverUsageCacheAccountingCompletesEmptyDatabaseWithoutResettingRollups(t *testing.T) {
	db := openMigrationTestDB(t)
	insertRollupFixtures(t, db)
	insertPricingRollupFixtures(t, db, 9, 9, 9)
	if _, err := db.Exec(`insert into usage_rollup_rebuild_state (name, target_event_id, updated_at_ms)
		values ('zero-target', 0, 0)`); err != nil {
		t.Fatalf("insert zero-target rebuild state: %v", err)
	}

	state, err := New(db).DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("discover migration: %v", err)
	}
	if state.Status != StatusCompleted || state.TargetEventID != 0 || state.ProcessedRows != 0 || state.ChangedRows != 0 {
		t.Fatalf("state = %#v, want completed empty migration", state)
	}
	assertCount(t, db, "usage_account_model_rollups", 1)
	assertCount(t, db, "usage_dashboard_hourly_rollups", 1)
	assertCount(t, db, "usage_pricing_hourly_rollups_v1", 1)
	assertCount(t, db, "usage_pricing_account_rollups_v1", 1)
	assertPricingAggregateState(t, db, "backfilling", 9, 9, 9)
	assertCheckpoint(t, db, "account_history", 9)
	assertCheckpoint(t, db, "dashboard_hourly", 9)
	assertCount(t, db, "usage_rollup_rebuild_state", 0)
}

func TestUsageCacheAccountingMigratesInBatchesExcludesNewRowsAndInvalidatesAtCompletion(t *testing.T) {
	db := openMigrationTestDB(t)
	insertLegacyUsageEvent(t, db, "legacy-anthropic", "anthropic", "", "claude-sonnet", 100, 30, 20, 10, 0, "")
	insertLegacyUsageEvent(t, db, "legacy-xai", "xai", "", "grok-4", 100, 30, 0, 0, 0, "")
	insertLegacyUsageEvent(t, db, "legacy-generic", "", "", "other", 50, 0, 0, 0, 0, "")
	markMigrationDiscovering(t, db)
	insertRollupFixtures(t, db)
	insertPermanentAggregateFixture(t, db, "legacy-anthropic")
	insertPricingRollupFixtures(t, db, 1, 1, 3)

	repo := New(db)
	state, err := repo.DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("discover migration: %v", err)
	}
	if state.Status != StatusPending || state.TargetEventID != 3 || state.LastEventID != 0 {
		t.Fatalf("discovered state = %#v", state)
	}
	assertCount(t, db, "usage_account_model_rollups", 1)
	assertCount(t, db, "usage_dashboard_hourly_rollups", 1)
	assertCount(t, db, "usage_pricing_hourly_rollups_v1", 1)
	assertCount(t, db, "usage_pricing_account_rollups_v1", 1)
	assertPricingAggregateState(t, db, "backfilling", 1, 1, 3)

	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, model, cache_input_mode,
		input_tokens, cached_tokens, normalized_uncached_input_tokens,
		normalized_total_input_tokens, normalized_cache_read_tokens,
		normalized_cache_creation_tokens, created_at_ms
	) values ('new-normalized', 4, '4', 'openai', 'gpt-5', 'included_in_input',
		999, 999, 999, 999, 999, 999, 4)`); err != nil {
		t.Fatalf("insert post-discovery event: %v", err)
	}

	first, err := repo.RunUsageCacheAccountingBatch(context.Background(), 2)
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}
	if first.Processed != 2 || first.Completed || first.State.LastEventID != 2 || first.State.ProcessedRows != 2 || first.State.ChangedRows != 2 {
		t.Fatalf("first batch = %#v", first)
	}
	assertNormalizedTotalNull(t, db, "legacy-anthropic")
	assertNormalizedTotalNull(t, db, "legacy-xai")
	assertCount(t, db, "usage_cache_accounting_v2_changes", 2)
	assertCount(t, db, "usage_account_model_rollups", 1)
	assertCount(t, db, "usage_hourly_aggregate_v1", 1)
	assertPermanentAggregateState(t, db, "backfilling", 1, 1, 3)
	assertIdentityAggregateVersion(t, db, "legacy-anthropic", usageaggregaterepo.SchemaVersion)
	assertCount(t, db, "usage_pricing_hourly_rollups_v1", 1)
	assertCount(t, db, "usage_pricing_account_rollups_v1", 1)
	assertPricingAggregateState(t, db, "backfilling", 1, 1, 3)
	assertCheckpoint(t, db, "account_history", 9)

	second, err := repo.RunUsageCacheAccountingBatch(context.Background(), 2)
	if err != nil {
		t.Fatalf("second batch: %v", err)
	}
	if second.Processed != 1 || second.Completed || second.State.Status != StatusApplying || second.State.LastEventID != 3 || second.State.ProcessedRows != 3 || second.State.ChangedRows != 3 || second.State.AppliedRows != 0 {
		t.Fatalf("second batch = %#v", second)
	}
	assertNormalizedTotalNull(t, db, "legacy-generic")
	assertCount(t, db, "usage_cache_accounting_v2_changes", 3)
	assertCount(t, db, "usage_account_model_rollups", 1)
	assertCount(t, db, "usage_dashboard_hourly_rollups", 1)

	firstApply, err := repo.RunUsageCacheAccountingBatch(context.Background(), 2)
	if err != nil {
		t.Fatalf("first apply batch: %v", err)
	}
	if firstApply.Processed != 2 || firstApply.Completed || firstApply.State.Status != StatusApplying || firstApply.State.AppliedRows != 2 {
		t.Fatalf("first apply batch = %#v", firstApply)
	}
	assertAccounting(t, db, "legacy-anthropic", "separate_from_input", 100, 130, 20, 10, 0)
	assertAccounting(t, db, "legacy-xai", "included_in_input", 70, 100, 30, 0, 0)
	assertNormalizedTotalNull(t, db, "legacy-generic")
	assertCount(t, db, "usage_cache_accounting_v2_changes", 1)
	assertCount(t, db, "usage_account_model_rollups", 1)
	assertCount(t, db, "usage_dashboard_hourly_rollups", 1)
	assertPermanentAggregateState(t, db, "clearing", 0, 0, 4)

	secondApply, err := repo.RunUsageCacheAccountingBatch(context.Background(), 2)
	if err != nil {
		t.Fatalf("second apply batch: %v", err)
	}
	if secondApply.Processed != 1 || secondApply.Completed || secondApply.State.Status != StatusClearing || secondApply.State.AppliedRows != 3 {
		t.Fatalf("second apply batch = %#v", secondApply)
	}
	assertCount(t, db, "usage_cache_accounting_v2_changes", 0)

	firstClear, err := repo.RunUsageCacheAccountingBatch(context.Background(), 2)
	if err != nil {
		t.Fatalf("first clear batch: %v", err)
	}
	if firstClear.Processed != 2 || firstClear.Completed || firstClear.State.Status != StatusClearing {
		t.Fatalf("first clear batch = %#v", firstClear)
	}
	assertCount(t, db, "usage_account_model_rollups", 0)
	assertCount(t, db, "usage_dashboard_hourly_rollups", 0)
	assertCount(t, db, "usage_hourly_aggregate_v1", 1)

	final, err := repo.RunUsageCacheAccountingBatch(context.Background(), 2)
	if err != nil {
		t.Fatalf("final clear batch: %v", err)
	}
	if !final.Completed || final.Processed != 1 || final.State.Status != StatusCompleted || final.State.AppliedRows != 3 {
		t.Fatalf("final batch = %#v", final)
	}

	assertAccounting(t, db, "legacy-anthropic", "separate_from_input", 100, 130, 20, 10, 0)
	assertAccounting(t, db, "legacy-xai", "included_in_input", 70, 100, 30, 0, 0)
	assertAccounting(t, db, "legacy-generic", "included_in_input", 50, 50, 0, 0, 0)
	assertAccounting(t, db, "new-normalized", "included_in_input", 999, 999, 999, 999, 0)
	assertCount(t, db, "usage_account_model_rollups", 0)
	assertCount(t, db, "usage_dashboard_hourly_rollups", 0)
	assertCount(t, db, "usage_hourly_aggregate_v1", 0)
	assertCount(t, db, "usage_pricing_hourly_rollups_v1", 1)
	assertCount(t, db, "usage_pricing_account_rollups_v1", 1)
	assertPermanentAggregateState(t, db, "pending", 0, 0, 4)
	assertPricingAggregateState(t, db, "pending", 0, 0, 4)
	assertIdentityAggregateVersion(t, db, "legacy-anthropic", usageaggregaterepo.SchemaVersion)
	assertCheckpoint(t, db, "account_history", 0)
	assertCheckpoint(t, db, "dashboard_hourly", 0)
	assertCheckpoint(t, db, "unrelated", 9)
}

func TestUsageCacheAccountingRefreshesMonitoringProjectionAndInvalidatesStats(t *testing.T) {
	db := openMigrationTestDB(t)
	insertLegacyUsageEvent(t, db, "legacy-monitoring", "anthropic", "", "claude-sonnet", 100, 30, 20, 10, 0, "")
	markMigrationDiscovering(t, db)

	ctx := context.Background()
	monitoringRepo := usagemonitoring.New(db)
	if _, err := monitoringRepo.CatchUpProjection(ctx, 10, 1); err != nil {
		t.Fatalf("catch up monitoring projection: %v", err)
	}
	if _, err := monitoringRepo.CatchUpStats(ctx, 10, 1); err != nil {
		t.Fatalf("catch up monitoring stats: %v", err)
	}
	assertMonitoringProjectionTokens(t, db, "legacy-monitoring", 100, 0)
	assertCount(t, db, "usage_monitoring_account_daily_rollups_v1", 1)
	assertCount(t, db, "usage_monitoring_api_key_daily_rollups_v1", 1)

	repo := New(db)
	if _, err := repo.DiscoverUsageCacheAccounting(ctx); err != nil {
		t.Fatalf("discover migration: %v", err)
	}
	result := runUsageCacheAccountingToCompletion(t, repo, 10)
	if !result.Completed || result.State.ChangedRows != 1 {
		t.Fatalf("migration result = %#v", result)
	}

	assertMonitoringProjectionTokens(t, db, "legacy-monitoring", 130, 0)
	assertCount(t, db, "usage_monitoring_account_daily_rollups_v1", 1)
	assertCount(t, db, "usage_monitoring_api_key_daily_rollups_v1", 1)
	assertMonitoringRollupState(t, db, "stats_v1", "pending", 0, 1)
	assertMonitoringRollupState(t, db, "projection_v1", "ready", 1, 1)

	if _, err := monitoringRepo.CatchUpStats(ctx, 10, 2); err != nil {
		t.Fatalf("rebuild monitoring stats: %v", err)
	}
	var inputTokens, totalTokens int64
	if err := db.QueryRow(`select sum(input_tokens), sum(total_tokens)
		from usage_monitoring_account_daily_rollups_v1`).Scan(&inputTokens, &totalTokens); err != nil {
		t.Fatalf("read rebuilt monitoring stats: %v", err)
	}
	if inputTokens != 130 || totalTokens != 0 {
		t.Fatalf("rebuilt monitoring tokens = (%d, %d), want (130, 0)", inputTokens, totalTokens)
	}
}

func TestUsageCacheAccountingReadersUseCorrectedRawRowsDuringBoundedClearing(t *testing.T) {
	const hourMS = int64(3_600_000)
	ctx := context.Background()
	db := openMigrationTestDB(t)
	insertLegacyUsageEvent(t, db, "legacy-reader-fallback", "anthropic", "", "claude-sonnet", 100, 30, 20, 10, 0, "")
	if _, err := db.Exec(`update usage_events set
		timestamp_ms = ?, timestamp = '2026-08-15T01:00:00Z',
		account_snapshot = 'reader@example.com', auth_label_snapshot = 'Reader',
		auth_index = 'auth-reader'
	where event_hash = 'legacy-reader-fallback'`, hourMS+1); err != nil {
		t.Fatalf("update reader fallback event: %v", err)
	}

	rollupRepo := usagerollup.New(db)
	if _, err := rollupRepo.CatchUpAccountHistory(ctx, 10, 1); err != nil {
		t.Fatalf("build account history fixture: %v", err)
	}
	if _, err := rollupRepo.CatchUpDashboardHourly(ctx, 10, 1); err != nil {
		t.Fatalf("build dashboard fixture: %v", err)
	}
	aggregateRepo := usageaggregaterepo.New(db)
	if _, err := aggregateRepo.CatchUp(ctx, 10, 1); err != nil {
		t.Fatalf("build aggregate fixture: %v", err)
	}
	pricingRepo := usagepricing.New(db)
	if _, err := pricingRepo.CatchUp(ctx, 10, 1); err != nil {
		t.Fatalf("build pricing fixture: %v", err)
	}
	monitoringRepo := usagemonitoring.New(db)
	if _, err := monitoringRepo.CatchUpProjection(ctx, 10, 1); err != nil {
		t.Fatalf("build monitoring projection fixture: %v", err)
	}
	if _, err := monitoringRepo.CatchUpStats(ctx, 10, 1); err != nil {
		t.Fatalf("build monitoring stats fixture: %v", err)
	}
	for _, tableName := range []string{
		"usage_account_model_rollups",
		"usage_dashboard_hourly_rollups",
		"usage_hourly_aggregate_v1",
		"usage_pricing_hourly_rollups_v1",
		"usage_monitoring_account_daily_rollups_v1",
	} {
		assertCount(t, db, tableName, 1)
	}

	markMigrationDiscovering(t, db)
	migrationRepo := New(db)
	if _, err := migrationRepo.DiscoverUsageCacheAccounting(ctx); err != nil {
		t.Fatalf("discover migration: %v", err)
	}
	scanned, err := migrationRepo.RunUsageCacheAccountingBatch(ctx, 10)
	if err != nil || scanned.State.Status != StatusApplying {
		t.Fatalf("scan migration = %#v err=%v", scanned, err)
	}
	applied, err := migrationRepo.RunUsageCacheAccountingBatch(ctx, 10)
	if err != nil || applied.State.Status != StatusClearing || applied.State.AppliedRows != 1 {
		t.Fatalf("apply migration = %#v err=%v", applied, err)
	}
	for _, tableName := range []string{
		"usage_account_model_rollups",
		"usage_dashboard_hourly_rollups",
		"usage_hourly_aggregate_v1",
		"usage_pricing_hourly_rollups_v1",
		"usage_monitoring_account_daily_rollups_v1",
	} {
		assertCount(t, db, tableName, 1)
	}

	var accountKey string
	if err := db.QueryRow(`select account_key from usage_account_model_rollups limit 1`).Scan(&accountKey); err != nil {
		t.Fatalf("read account rollup key: %v", err)
	}
	accountRows, err := rollupRepo.AccountHistoryRows(ctx, []string{accountKey})
	if err != nil || len(accountRows) != 1 || accountRows[0].InputTokens != 130 {
		t.Fatalf("account raw fallback rows = %#v err=%v", accountRows, err)
	}
	dashboardRows, err := rollupRepo.DashboardHourlyRows(ctx, hourMS, 2*hourMS)
	if err != nil || len(dashboardRows) != 1 || dashboardRows[0].InputTokens != 130 {
		t.Fatalf("dashboard raw fallback rows = %#v err=%v", dashboardRows, err)
	}
	aggregateRows, aggregateState, available, err := aggregateRepo.LoadRows(ctx, usageaggregaterepo.Filter{
		FromMS:        hourMS,
		ToMS:          2 * hourMS,
		IncludeFailed: true,
	})
	if err != nil || !available || aggregateState.Status != "clearing" || len(aggregateRows) != 1 || aggregateRows[0].InputTokens != 130 {
		t.Fatalf("aggregate raw fallback = available:%v state:%#v rows:%#v err=%v", available, aggregateState, aggregateRows, err)
	}
	pricingRows, pricingState, available, err := pricingRepo.LoadHourlyRows(ctx, usagepricing.HourlyFilter{
		FromMS:        hourMS,
		ToMS:          2 * hourMS,
		IncludeFailed: true,
	})
	if err != nil || !available || pricingState.StructureRevision != "" || len(pricingRows) != 1 || pricingRows[0].InputTokens != 130 {
		t.Fatalf("pricing raw fallback = available:%v state:%#v rows:%#v err=%v", available, pricingState, pricingRows, err)
	}
	monitoringAggregate, monitoringState, available, err := monitoringRepo.LoadAggregate(ctx, usagemonitoring.AnalyticsFilter{
		FromMS:        hourMS,
		ToMS:          2 * hourMS,
		IncludeFailed: true,
	})
	if err != nil || !available || monitoringState.CoverageEventID != 1 || monitoringAggregate.InputTokens != 130 {
		t.Fatalf("monitoring projection fallback = available:%v state:%#v aggregate:%#v err=%v", available, monitoringState, monitoringAggregate, err)
	}

	runUsageCacheAccountingToCompletion(t, migrationRepo, 1)
}

func TestUsageCacheAccountingUsesPriorityAndPreservesExplicitProvenance(t *testing.T) {
	db := openMigrationTestDB(t)
	insertAccountingEvent(t, db, accountingFixture{
		Hash: "openai-compat-claude-alias", Executor: "OpenAICompatExecutor", Model: "claude-sonnet", RawJSON: `{}`,
		Input: 100, CacheRead: 50, Output: 20, StoredMode: "separate_from_input", StoredUncached: 100, StoredTotalInput: 150, StoredRead: 50, Total: 170,
	})
	insertAccountingEvent(t, db, accountingFixture{
		Hash: "claude-grok-alias", Executor: "ClaudeExecutor", Model: "grok-4", RawJSON: `{}`,
		Input: 100, CacheRead: 50, Output: 20, StoredMode: "included_in_input", StoredUncached: 50, StoredTotalInput: 100, StoredRead: 50, Total: 120,
	})
	insertAccountingEvent(t, db, accountingFixture{
		Hash: "explicit-separate", Executor: "XAIExecutor", Model: "grok-4", RawJSON: `{"tokens":{"cache_input_mode":"separate_from_input"}}`,
		Input: 100, CacheRead: 50, StoredMode: "included_in_input", StoredUncached: 50, StoredTotalInput: 100, StoredRead: 50, Total: 100,
	})
	insertAccountingEvent(t, db, accountingFixture{
		Hash: "explicit-total", Provider: "anthropic", Model: "claude-sonnet", RawJSON: `{"tokens":{"total_tokens":999}}`,
		Input: 100, CacheRead: 50, Output: 20, StoredMode: "included_in_input", StoredUncached: 50, StoredTotalInput: 100, StoredRead: 50, Total: 999,
	})
	insertAccountingEvent(t, db, accountingFixture{
		Hash: "unknown-total-provenance", Provider: "anthropic", Model: "claude-sonnet",
		Input: 100, CacheRead: 50, Output: 20, StoredMode: "included_in_input", StoredUncached: 50, StoredTotalInput: 100, StoredRead: 50, Total: 120,
	})
	markMigrationDiscovering(t, db)

	repo := New(db)
	if _, err := repo.DiscoverUsageCacheAccounting(context.Background()); err != nil {
		t.Fatalf("discover migration: %v", err)
	}
	result := runUsageCacheAccountingToCompletion(t, repo, 20)
	if !result.Completed || result.State.ChangedRows != 5 {
		t.Fatalf("result = %#v", result)
	}

	assertAccounting(t, db, "openai-compat-claude-alias", "included_in_input", 50, 100, 50, 0, 120)
	assertAccounting(t, db, "claude-grok-alias", "separate_from_input", 100, 150, 50, 0, 170)
	assertAccounting(t, db, "explicit-separate", "separate_from_input", 100, 150, 50, 0, 150)
	assertAccounting(t, db, "explicit-total", "separate_from_input", 100, 150, 50, 0, 999)
	assertAccounting(t, db, "unknown-total-provenance", "separate_from_input", 100, 150, 50, 0, 120)
}

func TestUsageCacheAccountingSecondRunIsIdempotentAndKeepsRollups(t *testing.T) {
	db := openMigrationTestDB(t)
	insertLegacyUsageEvent(t, db, "legacy", "xai", "XAIExecutor", "grok-4", 100, 20, 0, 0, 0, "")
	markMigrationDiscovering(t, db)
	repo := New(db)
	if _, err := repo.DiscoverUsageCacheAccounting(context.Background()); err != nil {
		t.Fatalf("discover first run: %v", err)
	}
	runUsageCacheAccountingToCompletion(t, repo, 10)

	insertRollupFixtures(t, db)
	markMigrationDiscovering(t, db)
	if _, err := repo.DiscoverUsageCacheAccounting(context.Background()); err != nil {
		t.Fatalf("discover second run: %v", err)
	}
	second, err := repo.RunUsageCacheAccountingBatch(context.Background(), 10)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !second.Completed || second.State.ProcessedRows != 1 || second.State.ChangedRows != 0 {
		t.Fatalf("second result = %#v", second)
	}
	assertCount(t, db, "usage_account_model_rollups", 1)
	assertCount(t, db, "usage_dashboard_hourly_rollups", 1)
	assertCheckpoint(t, db, "account_history", 9)
}

func TestUsageCacheAccountingFailurePreservesCheckpointAndResumes(t *testing.T) {
	db := openMigrationTestDB(t)
	insertLegacyUsageEvent(t, db, "legacy-1", "openai", "", "gpt-5", 100, 10, 0, 0, 0, "")
	insertLegacyUsageEvent(t, db, "legacy-2", "openai", "", "gpt-5", 200, 20, 0, 0, 0, "")
	markMigrationDiscovering(t, db)
	repo := New(db)

	if _, err := repo.DiscoverUsageCacheAccounting(context.Background()); err != nil {
		t.Fatalf("discover migration: %v", err)
	}
	first, err := repo.RunUsageCacheAccountingBatch(context.Background(), 1)
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}
	if first.State.LastEventID != 1 || first.State.ProcessedRows != 1 || first.State.ChangedRows != 1 {
		t.Fatalf("first batch = %#v", first)
	}
	assertNormalizedTotalNull(t, db, "legacy-1")
	assertCount(t, db, "usage_cache_accounting_v2_changes", 1)
	if _, err := db.Exec(`create trigger reject_second_usage_stage before insert on usage_cache_accounting_v2_changes
		when new.event_id = 2 begin select raise(abort, 'blocked'); end`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	batchErr := errors.New("batch failed")
	if _, err := repo.RunUsageCacheAccountingBatch(context.Background(), 1); err == nil {
		t.Fatal("second batch error = nil, want trigger failure")
	} else {
		batchErr = err
	}
	if err := repo.RecordUsageCacheAccountingFailure(context.Background(), batchErr); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	failed, found, err := repo.UsageCacheAccountingState(context.Background())
	if err != nil || !found {
		t.Fatalf("failed state: found=%v err=%v", found, err)
	}
	if failed.Status != StatusFailed || failed.LastEventID != 1 || failed.ProcessedRows != 1 || failed.ChangedRows != 1 || failed.LastError == "" {
		t.Fatalf("failed state = %#v", failed)
	}

	resumed, err := repo.DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("resume migration: %v", err)
	}
	if resumed.Status != StatusApplying || resumed.LastEventID != 1 || resumed.ProcessedRows != 1 || resumed.ChangedRows != 1 || resumed.AppliedRows != 0 || resumed.TargetEventID != 2 {
		t.Fatalf("resumed state = %#v", resumed)
	}
	assertNormalizedTotalNull(t, db, "legacy-1")
	assertNormalizedTotalNull(t, db, "legacy-2")
	applied, err := repo.RunUsageCacheAccountingBatch(context.Background(), 1)
	if err != nil {
		t.Fatalf("apply staged batch: %v", err)
	}
	if applied.State.Status != StatusRunning || applied.State.AppliedRows != 1 || applied.State.LastEventID != 1 {
		t.Fatalf("applied staged batch = %#v", applied)
	}
	assertAccounting(t, db, "legacy-1", "included_in_input", 90, 100, 10, 0, 0)
	assertNormalizedTotalNull(t, db, "legacy-2")

	secondBatchErr := errors.New("second batch failed")
	if _, err := repo.RunUsageCacheAccountingBatch(context.Background(), 1); err == nil {
		t.Fatal("second resumed scan error = nil, want trigger failure")
	} else {
		secondBatchErr = err
	}
	if err := repo.RecordUsageCacheAccountingFailure(context.Background(), secondBatchErr); err != nil {
		t.Fatalf("record second failure: %v", err)
	}
	resumedAfterApply, err := repo.DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("resume migration after applied batch: %v", err)
	}
	if resumedAfterApply.Status != StatusRunning || resumedAfterApply.LastEventID != 1 || resumedAfterApply.ProcessedRows != 1 || resumedAfterApply.ChangedRows != 1 || resumedAfterApply.AppliedRows != 1 || resumedAfterApply.TargetEventID != 2 {
		t.Fatalf("resumed state after applied batch = %#v", resumedAfterApply)
	}
	assertCount(t, db, "usage_cache_accounting_v2_changes", 0)
	if _, err := db.Exec(`drop trigger reject_second_usage_stage`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	scanned, err := repo.RunUsageCacheAccountingBatch(context.Background(), 1)
	if err != nil {
		t.Fatalf("scan resumed batch: %v", err)
	}
	if scanned.State.Status != StatusApplying || scanned.State.ProcessedRows != 2 || scanned.State.ChangedRows != 2 || scanned.State.AppliedRows != 1 {
		t.Fatalf("scanned resumed batch = %#v", scanned)
	}
	final := runUsageCacheAccountingToCompletion(t, repo, 1)
	if final.State.ProcessedRows != 2 || final.State.ChangedRows != 2 || final.State.AppliedRows != 2 || final.State.LastEventID != 2 {
		t.Fatalf("final batch = %#v", final)
	}
	assertAccounting(t, db, "legacy-2", "included_in_input", 180, 200, 20, 0, 0)
	assertCount(t, db, "usage_cache_accounting_v2_changes", 0)
}

func TestUsageCacheAccountingEmptyApplyBatchResumesIncompleteScan(t *testing.T) {
	db := openMigrationTestDB(t)
	if _, err := db.Exec(`update usage_data_migrations set
		status = ?, last_event_id = 1, target_event_id = 2,
		processed_rows = 1, changed_rows = 1, applied_rows = 1,
		started_at_ms = 1, updated_at_ms = 1, finished_at_ms = null, last_error = null
	where name = ?`, StatusApplying, UsageCacheAccountingMigrationName); err != nil {
		t.Fatalf("prepare empty apply state: %v", err)
	}

	result, err := New(db).RunUsageCacheAccountingBatch(context.Background(), 1)
	if err != nil {
		t.Fatalf("run empty apply batch: %v", err)
	}
	if result.Completed || result.Processed != 0 || result.State.Status != StatusRunning ||
		result.State.LastEventID != 1 || result.State.TargetEventID != 2 ||
		result.State.ChangedRows != 1 || result.State.AppliedRows != 1 {
		t.Fatalf("empty apply result = %#v", result)
	}
	state, found, err := New(db).UsageCacheAccountingState(context.Background())
	if err != nil || !found {
		t.Fatalf("read resumed scan state: found=%v err=%v", found, err)
	}
	if state.Status != StatusRunning || state.LastEventID != 1 || state.TargetEventID != 2 {
		t.Fatalf("persisted resumed scan state = %#v", state)
	}
}

func TestUsageCacheAccountingScansOnlyCandidates(t *testing.T) {
	db := openMigrationTestDB(t)
	for index := 0; index < 25; index++ {
		if _, err := db.Exec(`insert into usage_events (
			event_hash, timestamp_ms, timestamp, provider, model, cache_input_mode,
			input_tokens, normalized_uncached_input_tokens, normalized_total_input_tokens,
			normalized_cache_read_tokens, normalized_cache_creation_tokens, total_tokens, created_at_ms
		) values (?, ?, ?, 'openai', 'gpt-5', 'included_in_input', 10, 10, 10, 0, 0, 10, ?)`,
			fmt.Sprintf("non-candidate-%d", index), index+1, index+1, index+1); err != nil {
			t.Fatalf("insert non-candidate %d: %v", index, err)
		}
	}
	insertLegacyUsageEvent(t, db, "cache-candidate", "xai", "XAIExecutor", "grok-4", 100, 20, 0, 0, 0, "")
	markMigrationDiscovering(t, db)

	repo := New(db)
	state, err := repo.DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("discover migration: %v", err)
	}
	if state.TargetEventID != 26 {
		t.Fatalf("target event id = %d, want 26", state.TargetEventID)
	}
	result := runUsageCacheAccountingToCompletion(t, repo, 100)
	if !result.Completed || result.State.ProcessedRows != 1 || result.State.ChangedRows != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestUsageCacheAccountingClearingFailurePreservesAppliedRowsAndResumes(t *testing.T) {
	db := openMigrationTestDB(t)
	insertLegacyUsageEvent(t, db, "legacy-1", "openai", "", "gpt-5", 100, 10, 0, 0, 0, "")
	insertLegacyUsageEvent(t, db, "legacy-2", "openai", "", "gpt-5", 200, 20, 0, 0, 0, "")
	markMigrationDiscovering(t, db)
	insertRollupFixtures(t, db)
	repo := New(db)
	if _, err := repo.DiscoverUsageCacheAccounting(context.Background()); err != nil {
		t.Fatalf("discover migration: %v", err)
	}
	first, err := repo.RunUsageCacheAccountingBatch(context.Background(), 1)
	if err != nil {
		t.Fatalf("run first batch: %v", err)
	}
	if first.Completed || first.State.LastEventID != 1 || first.State.ChangedRows != 1 {
		t.Fatalf("first batch = %#v", first)
	}
	second, err := repo.RunUsageCacheAccountingBatch(context.Background(), 1)
	if err != nil {
		t.Fatalf("run second scan batch: %v", err)
	}
	if second.State.Status != StatusApplying || second.State.ChangedRows != 2 {
		t.Fatalf("second scan batch = %#v", second)
	}
	firstApply, err := repo.RunUsageCacheAccountingBatch(context.Background(), 1)
	if err != nil {
		t.Fatalf("run first apply batch: %v", err)
	}
	if firstApply.State.Status != StatusApplying || firstApply.State.AppliedRows != 1 {
		t.Fatalf("first apply batch = %#v", firstApply)
	}
	secondApply, err := repo.RunUsageCacheAccountingBatch(context.Background(), 1)
	if err != nil {
		t.Fatalf("run second apply batch: %v", err)
	}
	if secondApply.State.Status != StatusClearing || secondApply.State.AppliedRows != 2 {
		t.Fatalf("second apply batch = %#v", secondApply)
	}
	if _, err := db.Exec(`create trigger reject_rollup_delete before delete on usage_account_model_rollups
		begin select raise(abort, 'blocked'); end`); err != nil {
		t.Fatalf("create clearing failure trigger: %v", err)
	}

	clearingErr := errors.New("clearing failed")
	if _, err := repo.RunUsageCacheAccountingBatch(context.Background(), 1); err == nil {
		t.Fatal("clearing error = nil, want trigger failure")
	} else {
		clearingErr = err
	}
	assertAccounting(t, db, "legacy-1", "included_in_input", 90, 100, 10, 0, 0)
	assertAccounting(t, db, "legacy-2", "included_in_input", 180, 200, 20, 0, 0)
	assertCount(t, db, "usage_cache_accounting_v2_changes", 0)
	assertCount(t, db, "usage_account_model_rollups", 1)
	state, _, err := repo.UsageCacheAccountingState(context.Background())
	if err != nil {
		t.Fatalf("read rolled back state: %v", err)
	}
	if state.Status != StatusClearing || state.ProcessedRows != 2 || state.ChangedRows != 2 || state.AppliedRows != 2 || state.LastEventID != 2 {
		t.Fatalf("rolled back state = %#v", state)
	}
	if err := repo.RecordUsageCacheAccountingFailure(context.Background(), clearingErr); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	if _, err := db.Exec(`drop trigger reject_rollup_delete`); err != nil {
		t.Fatalf("drop finalization failure trigger: %v", err)
	}
	resumed, err := repo.DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("resume migration: %v", err)
	}
	if resumed.Status != StatusClearing || resumed.AppliedRows != 2 {
		t.Fatalf("resumed state = %#v", resumed)
	}
	result := runUsageCacheAccountingToCompletion(t, repo, 1)
	if !result.Completed || result.State.AppliedRows != 2 {
		t.Fatalf("resumed result = %#v", result)
	}
	assertCount(t, db, "usage_account_model_rollups", 0)
	assertCount(t, db, "usage_cache_accounting_v2_changes", 0)
}

func TestUsageCacheAccountingRejectsUnknownState(t *testing.T) {
	db := openMigrationTestDB(t)
	if _, err := db.Exec(`update usage_data_migrations set status = 'future-state'
		where name = ?`, UsageCacheAccountingMigrationName); err != nil {
		t.Fatalf("set unknown migration state: %v", err)
	}
	repo := New(db)

	if _, err := repo.DiscoverUsageCacheAccounting(context.Background()); err == nil {
		t.Fatal("discover unknown migration state error = nil")
	}
	if _, err := repo.RunUsageCacheAccountingBatch(context.Background(), 1); err == nil {
		t.Fatal("run unknown migration state error = nil")
	}
	if err := repo.RecordUsageCacheAccountingFailure(context.Background(), errors.New("do not overwrite")); err != nil {
		t.Fatalf("record failure for unknown state: %v", err)
	}
	state, found, err := repo.UsageCacheAccountingState(context.Background())
	if err != nil || !found {
		t.Fatalf("read unknown migration state: found=%v err=%v", found, err)
	}
	if state.Status != "future-state" {
		t.Fatalf("unknown migration status = %q, want unchanged", state.Status)
	}
}

type accountingFixture struct {
	Hash                 string
	Provider             string
	Executor             string
	AuthProviderSnapshot string
	ResolvedModel        string
	RequestedModel       string
	Model                string
	RawJSON              string
	Input                int64
	Output               int64
	Reasoning            int64
	Cached               int64
	CacheRead            int64
	CacheCreation        int64
	StoredMode           string
	StoredUncached       int64
	StoredTotalInput     int64
	StoredRead           int64
	StoredCreation       int64
	Total                int64
}

func openMigrationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func insertLegacyUsageEvent(t *testing.T, db *sql.DB, hash, provider, executor, model string, input, cached, cacheRead, cacheCreation, total int64, rawJSON string) {
	t.Helper()
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, executor_type, model, input_tokens,
		cached_tokens, cache_read_tokens, cache_creation_tokens, total_tokens, raw_json, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, hash, input, hash, provider, executor, model, input, cached, cacheRead, cacheCreation, total, rawJSON, input); err != nil {
		t.Fatalf("insert legacy usage event %s: %v", hash, err)
	}
}

func insertAccountingEvent(t *testing.T, db *sql.DB, fixture accountingFixture) {
	t.Helper()
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, executor_type, auth_provider_snapshot,
		resolved_model, requested_model, model,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens,
		cache_read_tokens, cache_creation_tokens, cache_input_mode,
		normalized_uncached_input_tokens, normalized_total_input_tokens,
		normalized_cache_read_tokens, normalized_cache_creation_tokens,
		total_tokens, raw_json, created_at_ms
	) values (?, 1, '1', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		fixture.Hash,
		fixture.Provider,
		fixture.Executor,
		fixture.AuthProviderSnapshot,
		fixture.ResolvedModel,
		fixture.RequestedModel,
		fixture.Model,
		fixture.Input,
		fixture.Output,
		fixture.Reasoning,
		fixture.Cached,
		fixture.CacheRead,
		fixture.CacheCreation,
		fixture.StoredMode,
		fixture.StoredUncached,
		fixture.StoredTotalInput,
		fixture.StoredRead,
		fixture.StoredCreation,
		fixture.Total,
		fixture.RawJSON,
	); err != nil {
		t.Fatalf("insert accounting event %s: %v", fixture.Hash, err)
	}
}

func insertRollupFixtures(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`insert into usage_account_model_rollups (
			account_key, model, billing_model, service_tier, first_seen_ms, last_seen_ms, updated_at_ms
		) values ('account', 'model', 'model', '', 1, 1, 1)`,
		`insert into usage_dashboard_hourly_rollups (
			bucket_ms, model, billing_model, service_tier, updated_at_ms
		) values (0, 'model', 'model', '', 1)`,
		`insert or replace into usage_rollup_checkpoints (name, last_event_id, updated_at_ms, last_error)
			values ('account_history', 9, 9, 'old')`,
		`insert or replace into usage_rollup_checkpoints (name, last_event_id, updated_at_ms, last_error)
			values ('dashboard_hourly', 9, 9, 'old')`,
		`insert or replace into usage_rollup_checkpoints (name, last_event_id, updated_at_ms, last_error)
			values ('unrelated', 9, 9, 'old')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("insert rollup fixture: %v", err)
		}
	}
}

func insertPermanentAggregateFixture(t *testing.T, db *sql.DB, eventHash string) {
	t.Helper()
	statements := []struct {
		query string
		args  []any
	}{
		{
			query: `insert into usage_hourly_aggregate_v1 (
				bucket_ms, model, billing_model, service_tier, failed, calls, updated_at_ms
			) select 0, model, model, '', failed, 1, 1 from usage_events where event_hash = ?`,
			args: []any{eventHash},
		},
		{
			query: `insert into usage_event_identity_ledger (
				event_hash, raw_event_id, timestamp_ms, bucket_ms, aggregate_schema_version,
				first_seen_at_ms, updated_at_ms
			) select event_hash, id, timestamp_ms, 0, ?, created_at_ms, 1
			from usage_events where event_hash = ?`,
			args: []any{usageaggregaterepo.SchemaVersion, eventHash},
		},
		{
			query: `update usage_hourly_aggregate_state set
				status = 'backfilling',
				backfill_last_event_id = (select id from usage_events where event_hash = ?),
				coverage_event_id = (select id from usage_events where event_hash = ?),
				target_event_id = (select max(id) from usage_events),
				processed_events = 1,
				min_bucket_ms = 0,
				max_bucket_ms = 0,
				updated_at_ms = 1,
				finished_at_ms = null
			where aggregate_name = ? and schema_version = ?`,
			args: []any{
				eventHash,
				eventHash,
				usageaggregaterepo.AggregateName,
				usageaggregaterepo.SchemaVersion,
			},
		},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("insert permanent aggregate fixture: %v", err)
		}
	}
}

func insertPricingRollupFixtures(t *testing.T, db *sql.DB, checkpoint, coverage, target int64) {
	t.Helper()
	statements := []struct {
		query string
		args  []any
	}{
		{
			query: `insert into usage_pricing_hourly_rollups_v1 (
				structure_revision, bucket_ms, model, billing_model, pricing_model,
				service_tier, context_threshold_tokens, failed, calls, updated_at_ms
			) values ('fixture', 0, 'model', 'model', 'model', '', -1, 0, 1, 1)`,
		},
		{
			query: `insert into usage_pricing_account_rollups_v1 (
				structure_revision, account_key, model, billing_model, pricing_model,
				service_tier, context_threshold_tokens, calls, first_seen_ms, last_seen_ms, updated_at_ms
			) values ('fixture', 'account', 'model', 'model', 'model', '', -1, 1, 1, 1, 1)`,
		},
		{
			query: `update usage_pricing_rollup_state set
				structure_revision = 'fixture', status = 'backfilling',
				backfill_last_event_id = ?, coverage_event_id = ?, target_event_id = ?,
				processed_events = 1, min_bucket_ms = 0, max_bucket_ms = 0,
				updated_at_ms = 1, finished_at_ms = null
			where rollup_name = 'pricing_v1' and schema_version = 1`,
			args: []any{checkpoint, coverage, target},
		},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("insert pricing rollup fixture: %v", err)
		}
	}
}

func markMigrationDiscovering(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`update usage_data_migrations set
		status = 'discovering', last_event_id = 0, target_event_id = 0,
		processed_rows = 0, changed_rows = 0, applied_rows = 0, started_at_ms = null, updated_at_ms = 0,
		finished_at_ms = null, last_error = null
	where name = ?`, UsageCacheAccountingMigrationName); err != nil {
		t.Fatalf("mark migration discovering: %v", err)
	}
}

func runUsageCacheAccountingToCompletion(t *testing.T, repo Repository, batchSize int) BatchResult {
	t.Helper()
	for attempt := 0; attempt < 100; attempt++ {
		result, err := repo.RunUsageCacheAccountingBatch(context.Background(), batchSize)
		if err != nil {
			t.Fatalf("run usage cache accounting batch %d: %v", attempt+1, err)
		}
		if result.Completed {
			return result
		}
	}
	t.Fatal("usage cache accounting migration did not complete within 100 batches")
	return BatchResult{}
}

func assertCount(t *testing.T, db *sql.DB, table string, want int64) {
	t.Helper()
	var got int64
	if err := db.QueryRow(`select count(*) from ` + table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("count %s = %d, want %d", table, got, want)
	}
}

func assertMonitoringProjectionTokens(t *testing.T, db *sql.DB, eventHash string, wantInput, wantTotal int64) {
	t.Helper()
	var inputTokens, totalTokens int64
	if err := db.QueryRow(`select normalized_total_input_tokens, total_tokens
		from usage_monitoring_event_projection_v1
		where event_id = (select id from usage_events where event_hash = ?)`, eventHash).Scan(
		&inputTokens,
		&totalTokens,
	); err != nil {
		t.Fatalf("read monitoring projection tokens for %s: %v", eventHash, err)
	}
	if inputTokens != wantInput || totalTokens != wantTotal {
		t.Fatalf("monitoring projection tokens for %s = (%d, %d), want (%d, %d)",
			eventHash,
			inputTokens,
			totalTokens,
			wantInput,
			wantTotal,
		)
	}
}

func assertMonitoringRollupState(t *testing.T, db *sql.DB, name, wantStatus string, wantCoverage, wantTarget int64) {
	t.Helper()
	var status string
	var coverage, target int64
	if err := db.QueryRow(`select status, coverage_event_id, target_event_id
		from usage_monitoring_rollup_state where rollup_name = ?`, name).Scan(
		&status,
		&coverage,
		&target,
	); err != nil {
		t.Fatalf("read monitoring rollup state %s: %v", name, err)
	}
	if status != wantStatus || coverage != wantCoverage || target != wantTarget {
		t.Fatalf("monitoring rollup state %s = (%s, %d, %d), want (%s, %d, %d)",
			name,
			status,
			coverage,
			target,
			wantStatus,
			wantCoverage,
			wantTarget,
		)
	}
}

func assertCheckpoint(t *testing.T, db *sql.DB, name string, want int64) {
	t.Helper()
	var got int64
	if err := db.QueryRow(`select last_event_id from usage_rollup_checkpoints where name = ?`, name).Scan(&got); err != nil {
		t.Fatalf("checkpoint %s: %v", name, err)
	}
	if got != want {
		t.Fatalf("checkpoint %s = %d, want %d", name, got, want)
	}
}

func assertPermanentAggregateState(t *testing.T, db *sql.DB, wantStatus string, wantCheckpoint, wantCoverage, wantTarget int64) {
	t.Helper()
	var schemaVersion int
	var status string
	var checkpoint, coverage, target int64
	if err := db.QueryRow(`select schema_version, status, backfill_last_event_id, coverage_event_id, target_event_id
		from usage_hourly_aggregate_state where aggregate_name = 'hourly_core'`).Scan(
		&schemaVersion,
		&status,
		&checkpoint,
		&coverage,
		&target,
	); err != nil {
		t.Fatalf("read permanent aggregate state: %v", err)
	}
	if schemaVersion != usageaggregaterepo.SchemaVersion || status != wantStatus || checkpoint != wantCheckpoint || coverage != wantCoverage || target != wantTarget {
		t.Fatalf(
			"permanent aggregate state = schema:%d status:%q checkpoint:%d coverage:%d target:%d, want schema:%d status:%q checkpoint:%d coverage:%d target:%d",
			schemaVersion,
			status,
			checkpoint,
			coverage,
			target,
			usageaggregaterepo.SchemaVersion,
			wantStatus,
			wantCheckpoint,
			wantCoverage,
			wantTarget,
		)
	}
}

func assertPricingAggregateState(t *testing.T, db *sql.DB, wantStatus string, wantCheckpoint, wantCoverage, wantTarget int64) {
	t.Helper()
	var status string
	var checkpoint, coverage, target int64
	if err := db.QueryRow(`select status, backfill_last_event_id, coverage_event_id, target_event_id
		from usage_pricing_rollup_state where rollup_name = 'pricing_v1'`).Scan(
		&status,
		&checkpoint,
		&coverage,
		&target,
	); err != nil {
		t.Fatalf("read pricing aggregate state: %v", err)
	}
	if status != wantStatus || checkpoint != wantCheckpoint || coverage != wantCoverage || target != wantTarget {
		t.Fatalf(
			"pricing aggregate state = status:%q checkpoint:%d coverage:%d target:%d, want status:%q checkpoint:%d coverage:%d target:%d",
			status,
			checkpoint,
			coverage,
			target,
			wantStatus,
			wantCheckpoint,
			wantCoverage,
			wantTarget,
		)
	}
}

func assertIdentityAggregateVersion(t *testing.T, db *sql.DB, eventHash string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`select aggregate_schema_version from usage_event_identity_ledger where event_hash = ?`, eventHash).Scan(&got); err != nil {
		t.Fatalf("read identity aggregate version: %v", err)
	}
	if got != want {
		t.Fatalf("identity aggregate version = %d, want %d", got, want)
	}
}

func assertNormalizedTotalNull(t *testing.T, db *sql.DB, hash string) {
	t.Helper()
	var value sql.NullInt64
	if err := db.QueryRow(`select normalized_total_input_tokens from usage_events where event_hash = ?`, hash).Scan(&value); err != nil {
		t.Fatalf("read normalized total %s: %v", hash, err)
	}
	if value.Valid {
		t.Fatalf("normalized total %s = %d, want null", hash, value.Int64)
	}
}

func assertAccounting(t *testing.T, db *sql.DB, hash, mode string, uncached, total, cacheRead, cacheCreation, totalTokens int64) {
	t.Helper()
	var gotMode string
	var gotUncached, gotTotal, gotCacheRead, gotCacheCreation, gotTotalTokens int64
	if err := db.QueryRow(`select cache_input_mode, normalized_uncached_input_tokens,
		normalized_total_input_tokens, normalized_cache_read_tokens,
		normalized_cache_creation_tokens, total_tokens from usage_events where event_hash = ?`, hash).Scan(
		&gotMode, &gotUncached, &gotTotal, &gotCacheRead, &gotCacheCreation, &gotTotalTokens,
	); err != nil {
		t.Fatalf("read accounting %s: %v", hash, err)
	}
	if gotMode != mode || gotUncached != uncached || gotTotal != total || gotCacheRead != cacheRead || gotCacheCreation != cacheCreation || gotTotalTokens != totalTokens {
		t.Fatalf("accounting %s = (%s, %d, %d, %d, %d, %d), want (%s, %d, %d, %d, %d, %d)",
			hash, gotMode, gotUncached, gotTotal, gotCacheRead, gotCacheCreation, gotTotalTokens,
			mode, uncached, total, cacheRead, cacheCreation, totalTokens)
	}
}

func markMigrationCompleted(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`update usage_data_migrations set
		status = 'completed', last_event_id = 10, target_event_id = 10,
		processed_rows = 10, changed_rows = 0, applied_rows = 0,
		started_at_ms = 1, updated_at_ms = 1, finished_at_ms = 1, last_error = null
	where name = '` + UsageCacheAccountingMigrationName + `'`); err != nil {
		t.Fatalf("mark migration completed: %v", err)
	}
}

func assertRevision(t *testing.T, db *sql.DB, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`select value from settings where key = ?`, UsageCacheAccountingSemanticsRevisionKey).Scan(&got); err != nil {
		t.Fatalf("read revision: %v", err)
	}
	if got != want {
		t.Fatalf("revision = %q, want %q", got, want)
	}
}

func TestUsageCacheAccountingSemanticsRevision18_1_CompletedDevinData(t *testing.T) {
	db := openMigrationTestDB(t)
	markMigrationCompleted(t, db)

	// 准备 #838 错误历史数据：stored mode=separate_from_input, uncached=229788, total=457809
	insertAccountingEvent(t, db, accountingFixture{
		Hash:             "devin-838",
		Provider:         "devin",
		Executor:         "DevinExecutor",
		Model:            "claude-fable-5-1",
		Input:            229788,
		CacheRead:        228021,
		Cached:           228021,
		CacheCreation:    0,
		Output:           1775,
		StoredMode:       "separate_from_input",
		StoredUncached:   229788,
		StoredTotalInput: 457809,
		StoredRead:       228021,
		StoredCreation:   0,
		Total:            231563,
		RawJSON:          `{"tokens":{"input_tokens":229788,"output_tokens":1775,"cached_tokens":228021,"cache_read_tokens":228021,"cache_creation_tokens":0,"total_tokens":231563}}`,
	})

	repo := New(db)
	state, err := repo.DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("discover migration: %v", err)
	}
	if state.Status != StatusPending || state.TargetEventID != 1 {
		t.Fatalf("discovered state = %#v, want pending target=1", state)
	}

	runUsageCacheAccountingToCompletion(t, repo, 10)

	assertAccounting(t, db, "devin-838", "read_included_creation_separate", 1767, 229788, 228021, 0, 231563)
	assertRevision(t, db, "2")
}

func TestUsageCacheAccountingSemanticsRevision18_2_NoDevinData(t *testing.T) {
	db := openMigrationTestDB(t)
	markMigrationCompleted(t, db)

	// 无 Devin 相关的 cache event
	insertAccountingEvent(t, db, accountingFixture{
		Hash:             "openai-normal",
		Provider:         "openai",
		Executor:         "OpenAICompatExecutor",
		Model:            "gpt-5",
		Input:            100,
		CacheRead:        20,
		Cached:           20,
		StoredMode:       "included_in_input",
		StoredUncached:   80,
		StoredTotalInput: 100,
		StoredRead:       20,
		Total:            100,
		RawJSON:          `{"tokens":{"input_tokens":100,"cache_read_tokens":20,"total_tokens":100}}`,
	})

	repo := New(db)
	state, err := repo.DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("discover migration: %v", err)
	}
	if state.Status != StatusCompleted {
		t.Fatalf("state.Status = %q, want %q", state.Status, StatusCompleted)
	}
	assertRevision(t, db, "2")
}

func TestUsageCacheAccountingSemanticsRevision18_3_Idempotency(t *testing.T) {
	db := openMigrationTestDB(t)
	markMigrationCompleted(t, db)

	insertAccountingEvent(t, db, accountingFixture{
		Hash:             "devin-838",
		Provider:         "devin",
		Executor:         "DevinExecutor",
		Model:            "claude-fable-5-1",
		Input:            229788,
		CacheRead:        228021,
		Cached:           228021,
		StoredMode:       "separate_from_input",
		StoredUncached:   229788,
		StoredTotalInput: 457809,
		StoredRead:       228021,
		Total:            231563,
		RawJSON:          `{"tokens":{"input_tokens":229788,"output_tokens":1775,"cached_tokens":228021,"cache_read_tokens":228021,"cache_creation_tokens":0,"total_tokens":231563}}`,
	})

	repo := New(db)
	if _, err := repo.DiscoverUsageCacheAccounting(context.Background()); err != nil {
		t.Fatalf("first discover: %v", err)
	}
	runUsageCacheAccountingToCompletion(t, repo, 10)
	assertRevision(t, db, "2")

	// 第二次调用 Discover
	state2, err := repo.DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("second discover: %v", err)
	}
	if state2.Status != StatusCompleted {
		t.Fatalf("second state.Status = %q, want %q (should remain completed without reset)", state2.Status, StatusCompleted)
	}
}

func TestUsageCacheAccountingSemanticsRevision18_4_UnappliedStagingCleared(t *testing.T) {
	db := openMigrationTestDB(t)
	insertAccountingEvent(t, db, accountingFixture{
		Hash:             "devin-838",
		Provider:         "devin",
		Executor:         "DevinExecutor",
		Model:            "claude-fable-5-1",
		Input:            229788,
		CacheRead:        228021,
		Cached:           228021,
		StoredMode:       "separate_from_input",
		StoredUncached:   229788,
		StoredTotalInput: 457809,
		StoredRead:       228021,
		Total:            231563,
		RawJSON:          `{"tokens":{"input_tokens":229788,"output_tokens":1775,"cached_tokens":228021,"cache_read_tokens":228021,"cache_creation_tokens":0,"total_tokens":231563}}`,
	})

	// 设置 running，applied_rows = 0，并在 changes 表中插入旧 staging
	if _, err := db.Exec(`update usage_data_migrations set
		status = 'running', last_event_id = 0, target_event_id = 1,
		processed_rows = 1, changed_rows = 1, applied_rows = 0
	where name = '` + UsageCacheAccountingMigrationName + `'`); err != nil {
		t.Fatalf("setup running state: %v", err)
	}
	if _, err := db.Exec(`insert into usage_cache_accounting_v2_changes (
		event_id, cache_input_mode, normalized_uncached_input_tokens,
		normalized_total_input_tokens, normalized_cache_read_tokens,
		normalized_cache_creation_tokens, total_tokens
	) values (1, 'separate_from_input', 229788, 457809, 228021, 0, 231563)`); err != nil {
		t.Fatalf("insert old staging: %v", err)
	}
	assertCount(t, db, "usage_cache_accounting_v2_changes", 1)

	repo := New(db)
	state, err := repo.DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("discover migration: %v", err)
	}
	// 旧 staging 被清空并重新从 discovering->pending 扫描
	assertCount(t, db, "usage_cache_accounting_v2_changes", 0)
	if state.Status != StatusPending || state.TargetEventID != 1 {
		t.Fatalf("state = %#v, want pending target=1", state)
	}
	assertRevision(t, db, "2")
}

func TestUsageCacheAccountingSemanticsRevision18_5_AppliedMigrationNotResetMidway(t *testing.T) {
	db := openMigrationTestDB(t)
	insertAccountingEvent(t, db, accountingFixture{
		Hash:             "devin-838",
		Provider:         "devin",
		Executor:         "DevinExecutor",
		Model:            "claude-fable-5-1",
		Input:            229788,
		CacheRead:        228021,
		Cached:           228021,
		StoredMode:       "separate_from_input",
		StoredUncached:   229788,
		StoredTotalInput: 457809,
		StoredRead:       228021,
		Total:            231563,
		RawJSON:          `{"tokens":{"input_tokens":229788,"output_tokens":1775,"cached_tokens":228021,"cache_read_tokens":228021,"cache_creation_tokens":0,"total_tokens":231563}}`,
	})

	// 设置 applied_rows = 1, status = applying, revision 为空
	if _, err := db.Exec(`update usage_data_migrations set
		status = 'applying', last_event_id = 1, target_event_id = 1,
		processed_rows = 1, changed_rows = 1, applied_rows = 1
	where name = '` + UsageCacheAccountingMigrationName + `'`); err != nil {
		t.Fatalf("setup applying state: %v", err)
	}

	repo := New(db)
	state, err := repo.DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("discover migration: %v", err)
	}
	// 不得中途 reset！保持 applying
	if state.Status != StatusClearing && state.Status != StatusApplying {
		t.Fatalf("state.Status = %q, want applying or clearing", state.Status)
	}
	// revision 暂未更新为 2
	var rev sql.NullString
	_ = db.QueryRow(`select value from settings where key = ?`, UsageCacheAccountingSemanticsRevisionKey).Scan(&rev)
	if rev.Valid && rev.String == "2" {
		t.Fatal("revision should not be updated to 2 while applied_rows > 0 in old pass")
	}

	// 完成当前 pass 的 clearing
	runUsageCacheAccountingToCompletion(t, repo, 10)

	// 旧 pass 完成后，再次调用 Discover
	state2, err := repo.DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("discover after old pass complete: %v", err)
	}
	if state2.Status != StatusPending {
		t.Fatalf("state2.Status = %q, want pending for revision 2 pass", state2.Status)
	}
	// 跑完新 pass
	runUsageCacheAccountingToCompletion(t, repo, 10)
	assertAccounting(t, db, "devin-838", "read_included_creation_separate", 1767, 229788, 228021, 0, 231563)
	assertRevision(t, db, "2")
}

func TestUsageCacheAccountingSemanticsRevision18_6_DerivedRebuild(t *testing.T) {
	db := openMigrationTestDB(t)
	markMigrationCompleted(t, db)

	insertAccountingEvent(t, db, accountingFixture{
		Hash:             "devin-838",
		Provider:         "devin",
		Executor:         "DevinExecutor",
		Model:            "claude-fable-5-1",
		Input:            229788,
		CacheRead:        228021,
		Cached:           228021,
		StoredMode:       "separate_from_input",
		StoredUncached:   229788,
		StoredTotalInput: 457809,
		StoredRead:       228021,
		Total:            231563,
		RawJSON:          `{"tokens":{"input_tokens":229788,"output_tokens":1775,"cached_tokens":228021,"cache_read_tokens":228021,"cache_creation_tokens":0,"total_tokens":231563}}`,
	})

	insertRollupFixtures(t, db)
	insertPermanentAggregateFixture(t, db, "devin-838")
	insertPricingRollupFixtures(t, db, 1, 1, 1)

	repo := New(db)
	if _, err := repo.DiscoverUsageCacheAccounting(context.Background()); err != nil {
		t.Fatalf("discover: %v", err)
	}
	runUsageCacheAccountingToCompletion(t, repo, 10)

	assertCount(t, db, "usage_account_model_rollups", 0)
	assertCount(t, db, "usage_dashboard_hourly_rollups", 0)
	assertCheckpoint(t, db, "account_history", 0)
	assertCheckpoint(t, db, "dashboard_hourly", 0)
	assertCount(t, db, "usage_rollup_rebuild_state", 2)
}

func TestUsageCacheAccountingSemanticsRevision18_7_ActiveStatePersistsRevisionWithoutDevinData(t *testing.T) {
	statuses := []struct {
		name        string
		status      string
		appliedRows int64
	}{
		{name: "running applied 0", status: StatusRunning, appliedRows: 0},
		{name: "running applied non-zero", status: StatusRunning, appliedRows: 5},
		{name: "pending", status: StatusPending, appliedRows: 0},
		{name: "applying", status: StatusApplying, appliedRows: 2},
		{name: "clearing", status: StatusClearing, appliedRows: 2},
	}

	for _, tc := range statuses {
		t.Run(tc.name, func(t *testing.T) {
			db := openMigrationTestDB(t)

			// 数据库中无任何 Devin cache row，只有常规 OpenAI 记录
			insertAccountingEvent(t, db, accountingFixture{
				Hash:             "openai-normal",
				Provider:         "openai",
				Executor:         "OpenAICompatExecutor",
				Model:            "gpt-5",
				Input:            100,
				CacheRead:        20,
				Cached:           20,
				StoredMode:       "included_in_input",
				StoredUncached:   80,
				StoredTotalInput: 100,
				StoredRead:       20,
				Total:            100,
				RawJSON:          `{"tokens":{"input_tokens":100,"cache_read_tokens":20,"total_tokens":100}}`,
			})

			// 模拟 migration 处于活跃状态，且 revision 尚未设置
			nowMS := time.Now().UnixMilli()
			if _, err := db.Exec(`update usage_data_migrations set
				status = ?, last_event_id = 1, target_event_id = 10,
				processed_rows = 5, changed_rows = 5, applied_rows = ?,
				updated_at_ms = ?
			where name = ?`, tc.status, tc.appliedRows, nowMS, UsageCacheAccountingMigrationName); err != nil {
				t.Fatalf("setup active migration state: %v", err)
			}
			if _, err := db.Exec(`delete from settings where key = ?`, UsageCacheAccountingSemanticsRevisionKey); err != nil {
				t.Fatalf("clear revision setting: %v", err)
			}

			repo := New(db)
			state, err := repo.DiscoverUsageCacheAccounting(context.Background())
			if err != nil {
				t.Fatalf("discover migration: %v", err)
			}
			if state.Status != tc.status {
				t.Fatalf("state.Status = %q, want %q", state.Status, tc.status)
			}

			// 验证 revision 确实在事务提交后持久化（不是仅在当前未提交的事务中可见）
			assertRevision(t, db, "2")

			// 再次调用 Discover，确认状态依然保持当前 active 状态，不被重置
			state2, err := repo.DiscoverUsageCacheAccounting(context.Background())
			if err != nil {
				t.Fatalf("second discover: %v", err)
			}
			if state2.Status != tc.status {
				t.Fatalf("second state.Status = %q, want %q", state2.Status, tc.status)
			}
		})
	}
}

func TestUsageCacheAccountingSemanticsRevision18_8_HistoricalDevinPredicates(t *testing.T) {
	tests := []struct {
		name    string
		fixture accountingFixture
	}{
		{
			name: "provider prefix with custom suffix",
			fixture: accountingFixture{
				Hash:             "devin-provider-prefix",
				Provider:         "devin/custom",
				Executor:         "",
				Model:            "claude-fable-5-1",
				Input:            200,
				CacheRead:        50,
				Cached:           50,
				StoredMode:       "separate_from_input",
				StoredUncached:   200,
				StoredTotalInput: 250,
				StoredRead:       50,
				Total:            200,
				RawJSON:          `{"tokens":{"input_tokens":200,"cached_tokens":50,"cache_read_tokens":50,"total_tokens":200}}`,
			},
		},
		{
			name: "resolved model exact devin",
			fixture: accountingFixture{
				Hash:             "devin-model-exact",
				Provider:         "",
				Executor:         "",
				ResolvedModel:    "devin",
				Model:            "devin",
				Input:            300,
				CacheRead:        100,
				Cached:           100,
				StoredMode:       "separate_from_input",
				StoredUncached:   300,
				StoredTotalInput: 400,
				StoredRead:       100,
				Total:            300,
				RawJSON:          `{"tokens":{"input_tokens":300,"cached_tokens":100,"cache_read_tokens":100,"total_tokens":300}}`,
			},
		},
		{
			name: "auth provider snapshot prefix",
			fixture: accountingFixture{
				Hash:                 "devin-auth-snapshot-prefix",
				Provider:             "",
				AuthProviderSnapshot: "devin/oauth",
				Executor:             "",
				Model:                "some-model",
				Input:                150,
				CacheRead:            30,
				Cached:               30,
				StoredMode:           "separate_from_input",
				StoredUncached:       150,
				StoredTotalInput:     180,
				StoredRead:           30,
				Total:                150,
				RawJSON:              `{"tokens":{"input_tokens":150,"cached_tokens":30,"cache_read_tokens":30,"total_tokens":150}}`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := openMigrationTestDB(t)
			markMigrationCompleted(t, db)

			// 插入该历史 Devin event
			insertAccountingEvent(t, db, tt.fixture)

			// 此时 revision 尚未设为 2
			if _, err := db.Exec(`delete from settings where key = ?`, UsageCacheAccountingSemanticsRevisionKey); err != nil {
				t.Fatalf("clear revision: %v", err)
			}

			repo := New(db)
			// Discover 必须识别为 affected Devin row，并触发重新扫描（从 discovering -> pending）
			state, err := repo.DiscoverUsageCacheAccounting(context.Background())
			if err != nil {
				t.Fatalf("discover: %v", err)
			}
			if state.Status != StatusPending || state.TargetEventID != 1 {
				t.Fatalf("state = %#v, want pending target=1", state)
			}

			// 执行至完成
			runUsageCacheAccountingToCompletion(t, repo, 10)
			assertAccounting(t, db, tt.fixture.Hash, "read_included_creation_separate",
				tt.fixture.Input-tt.fixture.CacheRead,
				tt.fixture.Input,
				tt.fixture.CacheRead,
				0,
				tt.fixture.Total,
			)
			assertRevision(t, db, "2")
		})
	}
}
