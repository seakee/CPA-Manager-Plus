package datamigration

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestDiscoverUsageCacheAccountingCompletesEmptyDatabaseWithoutResettingRollups(t *testing.T) {
	db := openMigrationTestDB(t)
	insertRollupFixtures(t, db)

	state, err := New(db).DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("discover migration: %v", err)
	}
	if state.Status != StatusCompleted || state.TargetEventID != 0 || state.ProcessedRows != 0 {
		t.Fatalf("state = %#v, want completed empty migration", state)
	}
	assertCount(t, db, "usage_account_model_rollups", 1)
	assertCount(t, db, "usage_dashboard_hourly_rollups", 1)
}

func TestUsageCacheAccountingMigratesInBatchesAndExcludesNewRows(t *testing.T) {
	db := openMigrationTestDB(t)
	insertLegacyUsageEvent(t, db, "legacy-anthropic", "anthropic", "claude-sonnet", 100, 30, 20, 10)
	insertLegacyUsageEvent(t, db, "legacy-openai", "openai", "gpt-5", 100, 30, 0, 0)
	insertLegacyUsageEvent(t, db, "legacy-generic", "", "other", 50, 0, 0, 0)
	markMigrationDiscovering(t, db)
	insertRollupFixtures(t, db)

	repo := New(db)
	state, err := repo.DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("discover migration: %v", err)
	}
	if state.Status != StatusPending || state.TargetEventID != 3 || state.LastEventID != 0 {
		t.Fatalf("discovered state = %#v", state)
	}
	assertCount(t, db, "usage_account_model_rollups", 0)
	assertCount(t, db, "usage_dashboard_hourly_rollups", 0)

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
	if first.Processed != 2 || first.Completed || first.State.LastEventID != 2 || first.State.ProcessedRows != 2 {
		t.Fatalf("first batch = %#v", first)
	}
	second, err := repo.RunUsageCacheAccountingBatch(context.Background(), 2)
	if err != nil {
		t.Fatalf("second batch: %v", err)
	}
	if second.Processed != 1 || !second.Completed || second.State.Status != StatusCompleted || second.State.LastEventID != 3 || second.State.ProcessedRows != 3 {
		t.Fatalf("second batch = %#v", second)
	}

	assertAccounting(t, db, "legacy-anthropic", "separate_from_input", 100, 130, 20, 10)
	assertAccounting(t, db, "legacy-openai", "included_in_input", 70, 100, 30, 0)
	assertAccounting(t, db, "legacy-generic", "included_in_input", 50, 50, 0, 0)
	assertAccounting(t, db, "new-normalized", "included_in_input", 999, 999, 999, 999)
}

func TestUsageCacheAccountingStartsAfterCompletedPrefix(t *testing.T) {
	db := openMigrationTestDB(t)
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, model, cache_input_mode,
		input_tokens, normalized_uncached_input_tokens, normalized_total_input_tokens,
		normalized_cache_read_tokens, normalized_cache_creation_tokens, created_at_ms
	) values ('already-normalized', 1, '1', 'openai', 'gpt-5', 'included_in_input',
		100, 100, 100, 0, 0, 1)`); err != nil {
		t.Fatalf("insert normalized prefix: %v", err)
	}
	insertLegacyUsageEvent(t, db, "legacy-tail", "openai", "gpt-5", 100, 10, 0, 0)
	markMigrationDiscovering(t, db)
	repo := New(db)

	state, err := repo.DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("discover migration: %v", err)
	}
	if state.Status != StatusPending || state.LastEventID != 1 || state.TargetEventID != 2 {
		t.Fatalf("discovered state = %#v", state)
	}
	result, err := repo.RunUsageCacheAccountingBatch(context.Background(), 10)
	if err != nil {
		t.Fatalf("run tail batch: %v", err)
	}
	if !result.Completed || result.Processed != 1 || result.State.ProcessedRows != 1 {
		t.Fatalf("tail batch = %#v", result)
	}
	assertAccounting(t, db, "already-normalized", "included_in_input", 100, 100, 0, 0)
	assertAccounting(t, db, "legacy-tail", "included_in_input", 90, 100, 10, 0)
}

func TestUsageCacheAccountingFailurePreservesCheckpointAndResumes(t *testing.T) {
	db := openMigrationTestDB(t)
	insertLegacyUsageEvent(t, db, "legacy-1", "openai", "gpt-5", 100, 10, 0, 0)
	insertLegacyUsageEvent(t, db, "legacy-2", "openai", "gpt-5", 200, 20, 0, 0)
	markMigrationDiscovering(t, db)
	repo := New(db)

	if _, err := repo.DiscoverUsageCacheAccounting(context.Background()); err != nil {
		t.Fatalf("discover migration: %v", err)
	}
	first, err := repo.RunUsageCacheAccountingBatch(context.Background(), 1)
	if err != nil {
		t.Fatalf("first batch: %v", err)
	}
	if first.State.LastEventID != 1 || first.State.ProcessedRows != 1 {
		t.Fatalf("first batch = %#v", first)
	}
	if _, err := db.Exec(`create trigger reject_second_usage_update before update on usage_events
		when old.id = 2 begin select raise(abort, 'blocked'); end`); err != nil {
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
	if failed.Status != StatusFailed || failed.LastEventID != 1 || failed.ProcessedRows != 1 || failed.LastError == "" {
		t.Fatalf("failed state = %#v", failed)
	}

	resumed, err := repo.DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("resume migration: %v", err)
	}
	if resumed.Status != StatusPending || resumed.LastEventID != 1 || resumed.ProcessedRows != 1 || resumed.TargetEventID != 2 {
		t.Fatalf("resumed state = %#v", resumed)
	}
	if _, err := db.Exec(`drop trigger reject_second_usage_update`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	final, err := repo.RunUsageCacheAccountingBatch(context.Background(), 1)
	if err != nil {
		t.Fatalf("resumed batch: %v", err)
	}
	if !final.Completed || final.State.ProcessedRows != 2 || final.State.LastEventID != 2 {
		t.Fatalf("final batch = %#v", final)
	}

	if err := repo.RecordUsageCacheAccountingFailure(context.Background(), errors.New("late failure")); err != nil {
		t.Fatalf("record late failure: %v", err)
	}
	completed, _, err := repo.UsageCacheAccountingState(context.Background())
	if err != nil {
		t.Fatalf("completed state: %v", err)
	}
	if completed.Status != StatusCompleted || completed.LastError != "" {
		t.Fatalf("completed state overwritten by late failure: %#v", completed)
	}
}

func TestUsageCacheAccountingDiscoveryFailureRetriesDiscovery(t *testing.T) {
	db := openMigrationTestDB(t)
	insertLegacyUsageEvent(t, db, "legacy", "openai", "gpt-5", 100, 10, 0, 0)
	markMigrationDiscovering(t, db)
	insertRollupFixtures(t, db)
	if _, err := db.Exec(`create trigger reject_rollup_delete before delete on usage_account_model_rollups
		begin select raise(abort, 'blocked'); end`); err != nil {
		t.Fatalf("create discovery failure trigger: %v", err)
	}
	repo := New(db)

	_, discoveryErr := repo.DiscoverUsageCacheAccounting(context.Background())
	if discoveryErr == nil {
		t.Fatal("discovery error = nil, want trigger failure")
	}
	if err := repo.RecordUsageCacheAccountingFailure(context.Background(), discoveryErr); err != nil {
		t.Fatalf("record discovery failure: %v", err)
	}
	if _, err := db.Exec(`drop trigger reject_rollup_delete`); err != nil {
		t.Fatalf("drop discovery failure trigger: %v", err)
	}

	state, err := repo.DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("retry discovery: %v", err)
	}
	if state.Status != StatusPending || state.TargetEventID != 1 || state.LastEventID != 0 {
		t.Fatalf("retried discovery state = %#v", state)
	}
	assertCount(t, db, "usage_account_model_rollups", 0)
	assertCount(t, db, "usage_dashboard_hourly_rollups", 0)
}

func TestUsageCacheAccountingRepairsXAISeparateWithoutTouchingClaudeExecutorAlias(t *testing.T) {
	db := openMigrationTestDB(t)
	// Misclassified by legacy concatenated-identity rules after CPA started filling cache_read.
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, executor_type, model, cache_input_mode,
		input_tokens, cached_tokens, cache_read_tokens,
		normalized_uncached_input_tokens, normalized_total_input_tokens,
		normalized_cache_read_tokens, normalized_cache_creation_tokens, created_at_ms
	) values (
		'xai-misclassified', 1, '1', 'xai', 'XAIExecutor', 'grok-4.5', 'separate_from_input',
		130482, 125824, 125824,
		130482, 256306,
		125824, 0, 1
	)`); err != nil {
		t.Fatalf("insert xai misclassified: %v", err)
	}
	// Legitimate separate: ClaudeExecutor wins over Grok model alias.
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, executor_type, model, cache_input_mode,
		input_tokens, cache_read_tokens,
		normalized_uncached_input_tokens, normalized_total_input_tokens,
		normalized_cache_read_tokens, normalized_cache_creation_tokens, created_at_ms
	) values (
		'claude-exec-grok-alias', 2, '2', 'anthropic', 'ClaudeExecutor', 'grok-4.5', 'separate_from_input',
		100, 200,
		100, 300,
		200, 0, 2
	)`); err != nil {
		t.Fatalf("insert claude executor alias: %v", err)
	}
	markMigrationDiscovering(t, db)
	insertRollupFixtures(t, db)
	repo := New(db)

	state, err := repo.DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if state.Status != StatusPending || state.TargetEventID != 2 {
		t.Fatalf("discovered state = %#v", state)
	}
	assertCount(t, db, "usage_account_model_rollups", 0)

	result, err := repo.RunUsageCacheAccountingBatch(context.Background(), 10)
	if err != nil {
		t.Fatalf("batch: %v", err)
	}
	if !result.Completed {
		t.Fatalf("batch incomplete: %#v", result)
	}

	assertAccounting(t, db, "xai-misclassified", "included_in_input", 4658, 130482, 125824, 0)
	assertAccounting(t, db, "claude-exec-grok-alias", "separate_from_input", 100, 300, 200, 0)

	// Second discover is a no-op: no incomplete rows, no rollup wipe.
	insertRollupFixtures(t, db)
	if _, err := db.Exec(`update usage_data_migrations set status = 'discovering',
		last_event_id = 0, target_event_id = 0, processed_rows = 0,
		started_at_ms = null, finished_at_ms = null, last_error = null
	where name = ?`, UsageCacheAccountingMigrationName); err != nil {
		t.Fatalf("reset discovering: %v", err)
	}
	second, err := repo.DiscoverUsageCacheAccounting(context.Background())
	if err != nil {
		t.Fatalf("second discover: %v", err)
	}
	if second.Status != StatusCompleted {
		t.Fatalf("second discover status = %#v, want completed", second)
	}
	assertCount(t, db, "usage_account_model_rollups", 1)
	assertCount(t, db, "usage_dashboard_hourly_rollups", 1)
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

func openMigrationTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func insertLegacyUsageEvent(t *testing.T, db *sql.DB, hash, provider, model string, input, cached, cacheRead, cacheCreation int64) {
	t.Helper()
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, model, input_tokens,
		cached_tokens, cache_read_tokens, cache_creation_tokens, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, hash, input, hash, provider, model, input, cached, cacheRead, cacheCreation, input); err != nil {
		t.Fatalf("insert legacy usage event %s: %v", hash, err)
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
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("insert rollup fixture: %v", err)
		}
	}
}

func markMigrationDiscovering(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`update usage_data_migrations set
		status = 'discovering', last_event_id = 0, target_event_id = 0,
		processed_rows = 0, started_at_ms = null, updated_at_ms = 0,
		finished_at_ms = null, last_error = null
	where name = ?`, UsageCacheAccountingMigrationName); err != nil {
		t.Fatalf("mark migration discovering: %v", err)
	}
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

func assertAccounting(t *testing.T, db *sql.DB, hash, mode string, uncached, total, cacheRead, cacheCreation int64) {
	t.Helper()
	var gotMode string
	var gotUncached, gotTotal, gotCacheRead, gotCacheCreation int64
	if err := db.QueryRow(`select cache_input_mode, normalized_uncached_input_tokens,
		normalized_total_input_tokens, normalized_cache_read_tokens,
		normalized_cache_creation_tokens from usage_events where event_hash = ?`, hash).Scan(
		&gotMode, &gotUncached, &gotTotal, &gotCacheRead, &gotCacheCreation,
	); err != nil {
		t.Fatalf("read accounting %s: %v", hash, err)
	}
	if gotMode != mode || gotUncached != uncached || gotTotal != total || gotCacheRead != cacheRead || gotCacheCreation != cacheCreation {
		t.Fatalf("accounting %s = (%s, %d, %d, %d, %d), want (%s, %d, %d, %d, %d)",
			hash, gotMode, gotUncached, gotTotal, gotCacheRead, gotCacheCreation,
			mode, uncached, total, cacheRead, cacheCreation)
	}
}
