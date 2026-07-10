package usagerollup

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestMigrationCreatesAccountHistoryRollupTables(t *testing.T) {
	db := newRollupTestDB(t)

	for _, table := range []string{"usage_rollup_checkpoints", "usage_account_model_rollups", "usage_dashboard_hourly_rollups"} {
		var count int
		if err := db.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("query sqlite_master for %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("expected table %s to exist", table)
		}
	}
}

func TestCatchUpAccountHistoryAggregatesByCheckpoint(t *testing.T) {
	db := newRollupTestDB(t)
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)

	if _, err := events.InsertBatch(ctx, []usage.Event{
		rollupTestEvent("rollup-a-1", 1_700_000_001_000, "alias-a", "resolved-a", "alice@example.com", "", "auth-a", false, 100, 50, 10, 40, 10, 5, 165),
		rollupTestEvent("rollup-a-2", 1_700_000_002_000, "alias-a", "resolved-a", "alice@example.com", "", "auth-a", true, 20, 10, 0, 0, 0, 0, 30),
		rollupTestEvent("rollup-b-1", 1_700_000_003_000, "alias-b", "", "", "team-b", "auth-b", false, 5, 6, 0, 0, 0, 0, 11),
	}); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	first, err := repo.CatchUpAccountHistory(ctx, 2, 1_700_000_010_000)
	if err != nil {
		t.Fatalf("first catch-up: %v", err)
	}
	if first.Processed != 2 || first.LastEventID != 2 || !first.Pending {
		t.Fatalf("first catch-up = %#v", first)
	}

	aliceRows, err := repo.AccountHistoryRows(ctx, []string{"alice@example.com"})
	if err != nil {
		t.Fatalf("query alice rows: %v", err)
	}
	if len(aliceRows) != 1 {
		t.Fatalf("alice rows = %#v", aliceRows)
	}
	alice := aliceRows[0]
	if alice.Calls != 2 || alice.SuccessCalls != 1 || alice.FailureCalls != 1 {
		t.Fatalf("alice calls = %#v", alice)
	}
	if alice.BillingModel != "resolved-a" || alice.Model != "alias-a" {
		t.Fatalf("alice model fields = %#v", alice)
	}
	if alice.InputTokens != 120 || alice.OutputTokens != 60 || alice.CachedTokens != 25 || alice.TotalTokens != 195 {
		t.Fatalf("alice token totals = %#v", alice)
	}

	second, err := repo.CatchUpAccountHistory(ctx, 10, 1_700_000_011_000)
	if err != nil {
		t.Fatalf("second catch-up: %v", err)
	}
	if second.Processed != 1 || second.LastEventID != 3 || second.Pending {
		t.Fatalf("second catch-up = %#v", second)
	}
	third, err := repo.CatchUpAccountHistory(ctx, 10, 1_700_000_012_000)
	if err != nil {
		t.Fatalf("third catch-up: %v", err)
	}
	if third.Processed != 0 || third.LastEventID != 3 || third.Pending {
		t.Fatalf("third catch-up = %#v", third)
	}

	rows, err := repo.AccountHistoryRows(ctx, []string{"alice@example.com", "team-b"})
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %#v", rows)
	}
	for _, row := range rows {
		if row.AccountKey == "alice@example.com" && row.Calls != 2 {
			t.Fatalf("alice was double-counted: %#v", row)
		}
		if row.AccountKey == "team-b" && (row.Calls != 1 || row.BillingModel != "alias-b") {
			t.Fatalf("team-b row = %#v", row)
		}
	}
	checkpoint, err := repo.Checkpoint(ctx, AccountHistoryCheckpointName)
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if checkpoint.LastEventID != 3 {
		t.Fatalf("checkpoint = %#v", checkpoint)
	}
}

func TestRollupsPreserveLongContextTokenBuckets(t *testing.T) {
	db := newRollupTestDB(t)
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)
	baseMS := int64(1_700_000_000_000)
	hourMS := baseMS - baseMS%dashboardHourMS

	short := rollupTestEvent("long-boundary-short", hourMS+1_000, "gpt-5.6-sol", "", "alice@example.com", "", "auth-a", false, 272_000, 10, 0, 0, 20, 5, 272_010)
	long := rollupTestEvent("long-boundary-over", hourMS+2_000, "gpt-5.6-sol", "", "alice@example.com", "", "auth-a", false, 272_001, 30, 0, 0, 40, 10, 272_031)
	if _, err := events.InsertBatch(ctx, []usage.Event{short, long}); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	if _, err := repo.CatchUpAccountHistory(ctx, 10, baseMS+10_000); err != nil {
		t.Fatalf("account catch-up: %v", err)
	}
	if _, err := repo.CatchUpDashboardHourly(ctx, 10, baseMS+11_000); err != nil {
		t.Fatalf("dashboard catch-up: %v", err)
	}

	accountRows, err := repo.AccountHistoryRows(ctx, []string{"alice@example.com"})
	if err != nil || len(accountRows) != 1 {
		t.Fatalf("account rows = %#v, err = %v", accountRows, err)
	}
	account := accountRows[0]
	if account.LongInputTokens != 272_001 || account.LongOutputTokens != 30 ||
		account.LongCacheReadTokens != 40 || account.LongCacheCreationTokens != 10 {
		t.Fatalf("account long-context tokens = %#v", account.LongContextTokens)
	}

	dashboardRows, err := repo.DashboardHourlyRows(ctx, hourMS, hourMS+dashboardHourMS)
	if err != nil || len(dashboardRows) != 1 {
		t.Fatalf("dashboard rows = %#v, err = %v", dashboardRows, err)
	}
	dashboard := dashboardRows[0]
	if dashboard.LongInputTokens != 272_001 || dashboard.LongOutputTokens != 30 ||
		dashboard.LongCacheReadTokens != 40 || dashboard.LongCacheCreationTokens != 10 {
		t.Fatalf("dashboard long-context tokens = %#v", dashboard.LongContextTokens)
	}
}

func TestCatchUpAccountHistoryFailureDoesNotAdvanceCheckpoint(t *testing.T) {
	db := newRollupTestDB(t)
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)

	if _, err := events.InsertBatch(ctx, []usage.Event{
		rollupTestEvent("rollup-failure", 1_700_000_001_000, "gpt-a", "", "alice@example.com", "", "auth-a", false, 1, 1, 0, 0, 0, 0, 2),
	}); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	if _, err := db.Exec(`drop table usage_account_model_rollups`); err != nil {
		t.Fatalf("drop rollup table: %v", err)
	}
	if _, err := repo.CatchUpAccountHistory(ctx, 10, 1_700_000_010_000); err == nil {
		t.Fatalf("expected catch-up to fail")
	}
	checkpoint, err := repo.Checkpoint(ctx, AccountHistoryCheckpointName)
	if err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if checkpoint.LastEventID != 0 {
		t.Fatalf("checkpoint advanced after failed catch-up: %#v", checkpoint)
	}
}

func TestCatchUpAccountHistorySerializesConcurrentCalls(t *testing.T) {
	db := newRollupTestDB(t)
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)

	input := make([]usage.Event, 0, 25)
	for index := 0; index < 25; index++ {
		input = append(input, rollupTestEvent(
			fmt.Sprintf("rollup-concurrent-%02d", index),
			1_700_000_001_000+int64(index),
			"gpt-a",
			"",
			"concurrent@example.com",
			"",
			"auth-a",
			false,
			1,
			2,
			0,
			0,
			0,
			0,
			3,
		))
	}
	if _, err := events.InsertBatch(ctx, input); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for index := 0; index < 8; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := repo.CatchUpAccountHistory(ctx, 25, 1_700_000_010_000)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("catch-up failed: %v", err)
		}
	}

	rows, err := repo.AccountHistoryRows(ctx, []string{"concurrent@example.com"})
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	if rows[0].Calls != 25 || rows[0].InputTokens != 25 || rows[0].OutputTokens != 50 || rows[0].TotalTokens != 75 {
		t.Fatalf("concurrent rollup was not serialized: %#v", rows[0])
	}
}

func newRollupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func rollupTestEvent(
	hash string,
	timestampMS int64,
	model string,
	resolvedModel string,
	accountSnapshot string,
	authLabelSnapshot string,
	authIndex string,
	failed bool,
	inputTokens int64,
	outputTokens int64,
	reasoningTokens int64,
	cachedTokens int64,
	cacheReadTokens int64,
	cacheCreationTokens int64,
	totalTokens int64,
) usage.Event {
	return usage.Event{
		EventHash:            hash,
		TimestampMS:          timestampMS,
		Timestamp:            time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Provider:             "openai",
		Model:                model,
		ResolvedModel:        resolvedModel,
		Endpoint:             "POST /v1/chat/completions",
		Method:               "POST",
		Path:                 "/v1/chat/completions",
		AuthIndex:            authIndex,
		Source:               accountSnapshot,
		SourceHash:           "source-" + authIndex,
		AccountSnapshot:      accountSnapshot,
		AuthLabelSnapshot:    authLabelSnapshot,
		AuthProviderSnapshot: "openai",
		InputTokens:          inputTokens,
		OutputTokens:         outputTokens,
		ReasoningTokens:      reasoningTokens,
		CachedTokens:         cachedTokens,
		CacheReadTokens:      cacheReadTokens,
		CacheCreationTokens:  cacheCreationTokens,
		TotalTokens:          totalTokens,
		Failed:               failed,
		CreatedAtMS:          timestampMS,
	}
}
