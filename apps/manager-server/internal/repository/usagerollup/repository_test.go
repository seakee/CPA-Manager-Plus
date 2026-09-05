package usagerollup

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
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

	aliceKey := rollupTestAccountKey("alice@example.com", "", "auth-a")
	teamBKey := rollupTestAccountKey("", "team-b", "auth-b")
	aliceRows, err := repo.AccountHistoryRows(ctx, []string{aliceKey})
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

	rows, err := repo.AccountHistoryRows(ctx, []string{aliceKey, teamBKey})
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %#v", rows)
	}
	for _, row := range rows {
		if row.AccountKey == aliceKey && row.Calls != 2 {
			t.Fatalf("alice was double-counted: %#v", row)
		}
		if row.AccountKey == teamBKey && (row.Calls != 1 || row.BillingModel != "alias-b") {
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

func TestCatchUpAccountHistoryDistinguishesRebuildFromIncrementalCatchUp(t *testing.T) {
	db := newRollupTestDB(t)
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)
	fixtures := []usage.Event{
		rollupTestEvent("rollup-mode-1", 1_700_000_001_000, "model-a", "", "alice@example.com", "", "auth-a", false, 1, 1, 0, 0, 0, 0, 2),
		rollupTestEvent("rollup-mode-2", 1_700_000_002_000, "model-a", "", "alice@example.com", "", "auth-a", false, 1, 1, 0, 0, 0, 0, 2),
	}
	if _, err := events.InsertBatch(ctx, fixtures); err != nil {
		t.Fatalf("insert incremental fixtures: %v", err)
	}
	first, err := repo.CatchUpAccountHistory(ctx, 1, 1_700_000_010_000)
	if err != nil {
		t.Fatalf("incremental catch-up: %v", err)
	}
	if first.Rebuilt || !first.Pending {
		t.Fatalf("incremental catch-up = %#v", first)
	}
	if _, err := repo.CatchUpAccountHistory(ctx, 10, 1_700_000_011_000); err != nil {
		t.Fatalf("finish incremental catch-up: %v", err)
	}

	for _, statement := range []string{
		`delete from usage_account_model_rollups`,
		`delete from usage_rollup_checkpoints where name = 'account_history'`,
		`insert into usage_rollup_rebuild_state (name, target_event_id, updated_at_ms)
			values ('account_history', 2, 0)
			on conflict(name) do update set target_event_id = excluded.target_event_id, updated_at_ms = 0`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("prepare rebuild fixture: %v", err)
		}
	}
	rebuildFirst, err := repo.CatchUpAccountHistory(ctx, 1, 1_700_000_020_000)
	if err != nil {
		t.Fatalf("first rebuild batch: %v", err)
	}
	if !rebuildFirst.Rebuilt || rebuildFirst.RebuildTargetEventID != 2 || !rebuildFirst.Pending {
		t.Fatalf("first rebuild batch = %#v", rebuildFirst)
	}
	rebuildSecond, err := repo.CatchUpAccountHistory(ctx, 1, 1_700_000_021_000)
	if err != nil {
		t.Fatalf("second rebuild batch: %v", err)
	}
	if !rebuildSecond.Rebuilt || rebuildSecond.RebuildTargetEventID != 2 || rebuildSecond.Pending {
		t.Fatalf("second rebuild batch = %#v", rebuildSecond)
	}
	var rebuildStateRows int
	if err := db.QueryRow(`select count(*) from usage_rollup_rebuild_state where name = 'account_history'`).Scan(&rebuildStateRows); err != nil {
		t.Fatalf("count completed rebuild state: %v", err)
	}
	if rebuildStateRows != 0 {
		t.Fatalf("completed rebuild state rows = %d, want 0", rebuildStateRows)
	}

	third := rollupTestEvent("rollup-mode-3", 1_700_000_003_000, "model-a", "", "alice@example.com", "", "auth-a", false, 1, 1, 0, 0, 0, 0, 2)
	if _, err := events.InsertBatch(ctx, []usage.Event{third}); err != nil {
		t.Fatalf("insert post-rebuild event: %v", err)
	}
	postRebuild, err := repo.CatchUpAccountHistory(ctx, 10, 1_700_000_030_000)
	if err != nil {
		t.Fatalf("post-rebuild catch-up: %v", err)
	}
	if postRebuild.Rebuilt || postRebuild.Pending || postRebuild.LastEventID != 3 {
		t.Fatalf("post-rebuild catch-up = %#v", postRebuild)
	}
}

func TestAccountHistoryRowsRemainCompleteDuringRebuildAndRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "account-history-rebuild.sqlite")
	db, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)
	baseMS := int64(1_700_000_000_000)

	first := rollupTestEvent("account-fallback-1", baseMS+1_000, "model-a", "resolved-a", "alice@example.com", "", "auth-a", false, 272_001, 30, 5, 100, 20, 10, 272_036)
	second := rollupTestEvent("account-fallback-2", baseMS+2_000, "model-a", "resolved-a", "alice@example.com", "Alice", "auth-a", true, 10, 20, 3, 0, 0, 0, 33)
	third := rollupTestEvent("account-fallback-3", baseMS+3_000, "model-b", "resolved-b", "alice@example.com", "", "auth-a", false, 7, 8, 1, 0, 0, 0, 16)
	third.ServiceTier = "priority"
	if _, err := events.InsertBatch(ctx, []usage.Event{first, second, third}); err != nil {
		t.Fatalf("insert rebuild fixtures: %v", err)
	}
	if _, err := db.Exec(`delete from usage_account_model_rollups`); err != nil {
		t.Fatalf("clear account rollups: %v", err)
	}
	scheduleRollupRebuildForTest(t, db, AccountHistoryCheckpointName, 3)

	accountKey := rollupTestAccountKey("alice@example.com", "", "auth-a")
	expected, err := repo.AccountHistoryRows(ctx, []string{accountKey})
	if err != nil {
		t.Fatalf("read raw-backed account history: %v", err)
	}
	if len(expected) != 2 {
		t.Fatalf("raw-backed rows = %#v", expected)
	}
	byModel := make(map[string]AccountHistoryRow, len(expected))
	for _, row := range expected {
		byModel[row.Model] = row
	}
	modelA := byModel["model-a"]
	if modelA.Calls != 2 || modelA.SuccessCalls != 1 || modelA.FailureCalls != 1 ||
		modelA.InputTokens != 272_011 || modelA.OutputTokens != 50 || modelA.ReasoningTokens != 8 ||
		modelA.CachedTokens != 70 || modelA.CacheReadTokens != 20 || modelA.CacheCreationTokens != 10 ||
		modelA.LongInputTokens != 272_001 || modelA.LongOutputTokens != 30 || modelA.LongCachedTokens != 70 ||
		modelA.LongCacheReadTokens != 20 || modelA.LongCacheCreationTokens != 10 ||
		modelA.FirstSeenMS != baseMS+1_000 || modelA.LastSeenMS != baseMS+2_000 {
		t.Fatalf("raw-backed model-a row = %#v", modelA)
	}
	if row := byModel["model-b"]; row.Calls != 1 || row.ServiceTier != "priority" || row.TotalTokens != 16 {
		t.Fatalf("raw-backed model-b row = %#v", row)
	}

	firstBatch, err := repo.CatchUpAccountHistory(ctx, 1, baseMS+10_000)
	if err != nil {
		t.Fatalf("first rebuild batch: %v", err)
	}
	if !firstBatch.Rebuilt || !firstBatch.Pending || firstBatch.LastEventID != 1 {
		t.Fatalf("first rebuild batch = %#v", firstBatch)
	}
	partial, err := repo.AccountHistoryRows(ctx, []string{accountKey})
	if err != nil {
		t.Fatalf("read partially rebuilt account history: %v", err)
	}
	assertEquivalentAccountHistoryRows(t, partial, expected)

	if err := db.Close(); err != nil {
		t.Fatalf("close interrupted database: %v", err)
	}
	db, err = sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen interrupted database: %v", err)
	}
	repo = New(db)
	restarted, err := repo.AccountHistoryRows(ctx, []string{accountKey})
	if err != nil {
		t.Fatalf("read account history after restart: %v", err)
	}
	assertEquivalentAccountHistoryRows(t, restarted, expected)

	for attempt := 0; attempt < 4; attempt++ {
		result, err := repo.CatchUpAccountHistory(ctx, 1, baseMS+20_000+int64(attempt))
		if err != nil {
			t.Fatalf("resume rebuild batch %d: %v", attempt, err)
		}
		if !result.Pending {
			break
		}
	}
	completed, err := repo.AccountHistoryRows(ctx, []string{accountKey})
	if err != nil {
		t.Fatalf("read completed account history: %v", err)
	}
	assertEquivalentAccountHistoryRows(t, completed, expected)
}

func TestCatchUpAccountHistoryUsesAnalyticsModel(t *testing.T) {
	db := newRollupTestDB(t)
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)
	low := rollupTestEvent("account-canonical-low", 1_700_000_001_000, "deepseek-v4-flash(low)", "", "alice@example.com", "", "auth-a", false, 1, 2, 0, 0, 0, 0, 3)
	max := rollupTestEvent("account-canonical-max", 1_700_000_002_000, "deepseek-v4-flash(max)", "", "alice@example.com", "", "auth-a", false, 4, 5, 0, 0, 0, 0, 9)
	unknown := rollupTestEvent("account-canonical-unknown", 1_700_000_003_000, "deepseek-v4-flash(region-us)", "", "alice@example.com", "", "auth-a", false, 6, 7, 0, 0, 0, 0, 13)
	if _, err := events.InsertBatch(ctx, []usage.Event{low, max, unknown}); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	if _, err := repo.CatchUpAccountHistory(ctx, 10, 1_700_000_010_000); err != nil {
		t.Fatalf("catch up: %v", err)
	}
	rows, err := repo.AccountHistoryRows(ctx, []string{rollupTestAccountKey("alice@example.com", "", "auth-a")})
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %#v, want canonical and unknown rows", rows)
	}
	byModel := make(map[string]AccountHistoryRow, len(rows))
	for _, row := range rows {
		byModel[row.Model] = row
	}
	if byModel["deepseek-v4-flash"].Calls != 2 || byModel["deepseek-v4-flash"].TotalTokens != 12 {
		t.Fatalf("canonical row = %#v", byModel["deepseek-v4-flash"])
	}
	if byModel["deepseek-v4-flash(region-us)"].Calls != 1 {
		t.Fatalf("unknown row = %#v", byModel["deepseek-v4-flash(region-us)"])
	}
}

func TestCatchUpAccountHistorySeparatesAnalyticsModelsSharingBillingModel(t *testing.T) {
	db := newRollupTestDB(t)
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)
	first := rollupTestEvent("account-shared-billing-a", 1_700_000_001_000, "model-a", "resolved-x", "alice@example.com", "", "auth-a", false, 10, 1, 0, 0, 0, 0, 11)
	second := rollupTestEvent("account-shared-billing-b", 1_700_000_002_000, "model-b", "resolved-x", "alice@example.com", "", "auth-a", false, 20, 2, 0, 0, 0, 0, 22)
	if _, err := events.InsertBatch(ctx, []usage.Event{first, second}); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	if _, err := repo.CatchUpAccountHistory(ctx, 10, 1_700_000_010_000); err != nil {
		t.Fatalf("catch up: %v", err)
	}
	rows, err := repo.AccountHistoryRows(ctx, []string{rollupTestAccountKey("alice@example.com", "", "auth-a")})
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %#v, want one row per analytics model", rows)
	}
	byModel := make(map[string]AccountHistoryRow, len(rows))
	for _, row := range rows {
		byModel[row.Model] = row
	}
	if row := byModel["model-a"]; row.BillingModel != "resolved-x" || row.Calls != 1 || row.TotalTokens != 11 {
		t.Fatalf("model-a row = %#v", row)
	}
	if row := byModel["model-b"]; row.BillingModel != "resolved-x" || row.Calls != 1 || row.TotalTokens != 22 {
		t.Fatalf("model-b row = %#v", row)
	}
}

func TestCatchUpAccountHistoryPreservesAnalyticsModelWhitespace(t *testing.T) {
	db := newRollupTestDB(t)
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)
	event := rollupTestEvent("account-model-whitespace", 1_700_000_001_000, " custom-model(max)", "", "alice@example.com", "", "auth-a", false, 10, 1, 0, 0, 0, 0, 11)
	if _, err := events.InsertBatch(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := repo.CatchUpAccountHistory(ctx, 10, 1_700_000_010_000); err != nil {
		t.Fatalf("catch up: %v", err)
	}
	rows, err := repo.AccountHistoryRows(ctx, []string{rollupTestAccountKey("alice@example.com", "", "auth-a")})
	if err != nil {
		t.Fatalf("query rows: %v", err)
	}
	if len(rows) != 1 || rows[0].Model != " custom-model" || rows[0].BillingModel != " custom-model" {
		t.Fatalf("rows = %#v, want analytics model whitespace preserved", rows)
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

	accountRows, err := repo.AccountHistoryRows(ctx, []string{
		rollupTestAccountKey("alice@example.com", "", "auth-a"),
	})
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

	rows, err := repo.AccountHistoryRows(ctx, []string{
		rollupTestAccountKey("concurrent@example.com", "", "auth-a"),
	})
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

func TestCatchUpAccountHistoryWaitsForConcurrentWriter(t *testing.T) {
	db := newRollupTestDB(t)
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)

	if _, err := events.InsertBatch(ctx, []usage.Event{
		rollupTestEvent("rollup-lock-contention", 1_700_000_001_000, "gpt-a", "", "lock-test-account", "", "auth-a", false, 1, 2, 0, 0, 0, 0, 3),
	}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	latestEventID, err := repo.LatestEventID(ctx)
	if err != nil {
		t.Fatalf("latest event id: %v", err)
	}
	if _, err := db.Exec(`create table rollup_write_lock_test (id integer primary key)`); err != nil {
		t.Fatalf("create write lock fixture: %v", err)
	}

	lockingTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin competing write: %v", err)
	}
	defer func() {
		_ = lockingTx.Rollback()
	}()
	if _, err := lockingTx.Exec(`insert into rollup_write_lock_test (id) values (1)`); err != nil {
		t.Fatalf("hold competing write lock: %v", err)
	}

	type catchUpOutcome struct {
		result CatchUpResult
		err    error
	}
	catchUpResult := make(chan catchUpOutcome, 1)
	go func() {
		result, err := repo.CatchUpAccountHistory(ctx, 10, 1_700_000_010_000)
		catchUpResult <- catchUpOutcome{result: result, err: err}
	}()

	select {
	case outcome := <-catchUpResult:
		t.Fatalf("catch-up completed before competing writer released: result=%#v err=%v", outcome.result, outcome.err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := lockingTx.Commit(); err != nil {
		t.Fatalf("commit competing write: %v", err)
	}

	var outcome catchUpOutcome
	select {
	case outcome = <-catchUpResult:
	case <-time.After(time.Second):
		t.Fatal("catch-up did not resume after competing writer released")
	}
	if outcome.err != nil {
		t.Fatalf("catch-up after competing writer released: %v", outcome.err)
	}
	if outcome.result.Processed != 1 || outcome.result.LastEventID != latestEventID || outcome.result.Pending {
		t.Fatalf("catch-up result = %#v, latest event id = %d", outcome.result, latestEventID)
	}

	rows, err := repo.AccountHistoryRows(ctx, []string{
		rollupTestAccountKey("lock-test-account", "", "auth-a"),
	})
	if err != nil {
		t.Fatalf("query account history rows: %v", err)
	}
	if len(rows) != 1 || rows[0].Calls != 1 || rows[0].TotalTokens != 3 {
		t.Fatalf("account history rows = %#v", rows)
	}
}

func TestCatchUpAccountHistorySeparatesSharedAccountByAuthIndex(t *testing.T) {
	db := newRollupTestDB(t)
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)

	first := rollupTestEvent("shared-account-a", 1_700_000_001_000, "gpt-a", "", "same@example.com", "", "auth-a", false, 10, 5, 0, 0, 0, 0, 15)
	first.AuthFileSnapshot = "shared.json"
	second := rollupTestEvent("shared-account-b", 1_700_000_002_000, "gpt-a", "", "same@example.com", "", "auth-b", false, 20, 10, 0, 0, 0, 0, 30)
	second.AuthFileSnapshot = "shared.json"
	if _, err := events.InsertBatch(ctx, []usage.Event{first, second}); err != nil {
		t.Fatalf("insert shared-account events: %v", err)
	}
	if _, err := repo.CatchUpAccountHistory(ctx, 10, 1_700_000_010_000); err != nil {
		t.Fatalf("catch up shared-account history: %v", err)
	}

	firstKey := rollupTestFileAccountKey("shared.json", "same@example.com", "auth-a")
	secondKey := rollupTestFileAccountKey("shared.json", "same@example.com", "auth-b")
	rows, err := repo.AccountHistoryRows(ctx, []string{secondKey, firstKey})
	if err != nil {
		t.Fatalf("query shared-account rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("shared-account rows = %#v", rows)
	}
	byKey := make(map[string]AccountHistoryRow, len(rows))
	for _, row := range rows {
		byKey[row.AccountKey] = row
	}
	if byKey[firstKey].Calls != 1 || byKey[firstKey].TotalTokens != 15 {
		t.Fatalf("first credential rollup = %#v", byKey[firstKey])
	}
	if byKey[secondKey].Calls != 1 || byKey[secondKey].TotalTokens != 30 {
		t.Fatalf("second credential rollup = %#v", byKey[secondKey])
	}
}

func TestCatchUpAccountHistoryRebuildSeparatesCodexWorkspaceMembers(t *testing.T) {
	db := newRollupTestDB(t)
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)

	alice := rollupTestEvent("codex-team-alice", 1_700_000_001_000, "gpt-5", "", "alice@example.com", "", "auth-alice", false, 100, 0, 0, 0, 0, 0, 100)
	alice.Provider = "codex"
	alice.AuthProviderSnapshot = "codex"
	alice.AuthFileSnapshot = "alice-free.json"
	alice.Source = alice.AuthFileSnapshot
	alice.AuthAccountIDSnapshot = "workspace-1"

	bob := rollupTestEvent("codex-team-bob", 1_700_000_002_000, "gpt-5", "", "bob@example.com", "", "auth-bob", false, 200, 0, 0, 0, 0, 0, 200)
	bob.Provider = "codex"
	bob.AuthProviderSnapshot = "codex"
	bob.AuthFileSnapshot = "bob-team.json"
	bob.Source = bob.AuthFileSnapshot
	bob.AuthAccountIDSnapshot = "workspace-1"

	if _, err := events.InsertBatch(ctx, []usage.Event{alice, bob}); err != nil {
		t.Fatalf("insert Codex team events: %v", err)
	}
	if _, err := db.Exec(`delete from usage_account_model_rollups`); err != nil {
		t.Fatalf("clear account rollups: %v", err)
	}
	scheduleRollupRebuildForTest(t, db, AccountHistoryCheckpointName, 2)

	for attempt := 0; attempt < 2; attempt++ {
		result, err := repo.CatchUpAccountHistory(ctx, 1, 1_700_000_010_000+int64(attempt))
		if err != nil {
			t.Fatalf("Codex rebuild batch %d: %v", attempt, err)
		}
		if !result.Rebuilt {
			t.Fatalf("Codex rebuild batch %d was not marked rebuilt: %#v", attempt, result)
		}
	}

	aliceKey, aliceOK := usageidentity.AccountKey(usageidentity.Fields{
		AuthFileSnapshot:      alice.AuthFileSnapshot,
		AuthIndex:             alice.AuthIndex,
		AuthProviderSnapshot:  alice.AuthProviderSnapshot,
		AuthAccountIDSnapshot: alice.AuthAccountIDSnapshot,
		AccountSnapshot:       alice.AccountSnapshot,
		Source:                alice.Source,
	})
	bobKey, bobOK := usageidentity.AccountKey(usageidentity.Fields{
		AuthFileSnapshot:      bob.AuthFileSnapshot,
		AuthIndex:             bob.AuthIndex,
		AuthProviderSnapshot:  bob.AuthProviderSnapshot,
		AuthAccountIDSnapshot: bob.AuthAccountIDSnapshot,
		AccountSnapshot:       bob.AccountSnapshot,
		Source:                bob.Source,
	})
	if !aliceOK || !bobOK || aliceKey == bobKey {
		t.Fatalf("Codex team identities = alice:%q (%v), bob:%q (%v)", aliceKey, aliceOK, bobKey, bobOK)
	}

	rows, err := repo.AccountHistoryRows(ctx, []string{aliceKey, bobKey})
	if err != nil {
		t.Fatalf("query rebuilt Codex team history: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rebuilt Codex team rows = %#v, want two member rows", rows)
	}
	byKey := make(map[string]AccountHistoryRow, len(rows))
	for _, row := range rows {
		byKey[row.AccountKey] = row
	}
	if row := byKey[aliceKey]; row.Calls != 1 || row.TotalTokens != 100 {
		t.Fatalf("rebuilt Alice history = %#v, want 100 tokens", row)
	}
	if row := byKey[bobKey]; row.Calls != 1 || row.TotalTokens != 200 {
		t.Fatalf("rebuilt Bob history = %#v, want 200 tokens", row)
	}

	var rawCount int
	if err := db.QueryRow(`select count(*) from usage_events`).Scan(&rawCount); err != nil {
		t.Fatalf("count raw events: %v", err)
	}
	if rawCount != 2 {
		t.Fatalf("raw event count = %d, want 2 after rebuild", rawCount)
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

func rollupTestAccountKey(accountSnapshot, authLabelSnapshot, authIndex string) string {
	key, valid := usageidentity.AccountKey(usageidentity.Fields{
		AuthIndex:            authIndex,
		AuthProviderSnapshot: "openai",
		AccountSnapshot:      accountSnapshot,
		AuthLabelSnapshot:    authLabelSnapshot,
		Source:               accountSnapshot,
	})
	if !valid {
		panic("invalid rollup test identity")
	}
	return key
}

func rollupTestFileAccountKey(authFileSnapshot, accountSnapshot, authIndex string) string {
	key, valid := usageidentity.AccountKey(usageidentity.Fields{
		AuthFileSnapshot:     authFileSnapshot,
		AuthIndex:            authIndex,
		AuthProviderSnapshot: "openai",
		AccountSnapshot:      accountSnapshot,
	})
	if !valid {
		panic("invalid rollup test file identity")
	}
	return key
}

func scheduleRollupRebuildForTest(t *testing.T, db *sql.DB, name string, targetEventID int64) {
	t.Helper()
	if _, err := db.Exec(`delete from usage_rollup_checkpoints where name = ?`, name); err != nil {
		t.Fatalf("delete %s checkpoint: %v", name, err)
	}
	if _, err := db.Exec(`insert into usage_rollup_rebuild_state (name, target_event_id, updated_at_ms)
		values (?, ?, 0)
		on conflict(name) do update set target_event_id = excluded.target_event_id, updated_at_ms = 0`, name, targetEventID); err != nil {
		t.Fatalf("schedule %s rebuild: %v", name, err)
	}
}

func assertEquivalentAccountHistoryRows(t *testing.T, got, want []AccountHistoryRow) {
	t.Helper()
	got = append([]AccountHistoryRow(nil), got...)
	want = append([]AccountHistoryRow(nil), want...)
	for index := range got {
		got[index].UpdatedAtMS = 0
	}
	for index := range want {
		want[index].UpdatedAtMS = 0
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("account history rows = %#v, want %#v", got, want)
	}
}
