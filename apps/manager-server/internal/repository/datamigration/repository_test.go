package datamigration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	if state.Status != StatusCompleted || state.TargetEventID != 0 || state.ProcessedRows != 0 || state.ChangedRows != 0 {
		t.Fatalf("state = %#v, want completed empty migration", state)
	}
	assertCount(t, db, "usage_account_model_rollups", 1)
	assertCount(t, db, "usage_dashboard_hourly_rollups", 1)
	assertCheckpoint(t, db, "account_history", 9)
	assertCheckpoint(t, db, "dashboard_hourly", 9)
}

func TestUsageCacheAccountingMigratesInBatchesExcludesNewRowsAndInvalidatesAtCompletion(t *testing.T) {
	db := openMigrationTestDB(t)
	insertLegacyUsageEvent(t, db, "legacy-anthropic", "anthropic", "", "claude-sonnet", 100, 30, 20, 10, 0, "")
	insertLegacyUsageEvent(t, db, "legacy-xai", "xai", "", "grok-4", 100, 30, 0, 0, 0, "")
	insertLegacyUsageEvent(t, db, "legacy-generic", "", "", "other", 50, 0, 0, 0, 0, "")
	markMigrationDiscovering(t, db)
	insertRollupFixtures(t, db)
	insertPermanentAggregateFixture(t, db, "legacy-anthropic")

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
	assertCheckpoint(t, db, "account_history", 9)

	second, err := repo.RunUsageCacheAccountingBatch(context.Background(), 2)
	if err != nil {
		t.Fatalf("second batch: %v", err)
	}
	if second.Processed != 1 || !second.Completed || second.State.Status != StatusCompleted || second.State.LastEventID != 3 || second.State.ProcessedRows != 3 || second.State.ChangedRows != 3 {
		t.Fatalf("second batch = %#v", second)
	}

	assertAccounting(t, db, "legacy-anthropic", "separate_from_input", 100, 130, 20, 10, 0)
	assertAccounting(t, db, "legacy-xai", "included_in_input", 70, 100, 30, 0, 0)
	assertAccounting(t, db, "legacy-generic", "included_in_input", 50, 50, 0, 0, 0)
	assertAccounting(t, db, "new-normalized", "included_in_input", 999, 999, 999, 999, 0)
	assertCount(t, db, "usage_account_model_rollups", 0)
	assertCount(t, db, "usage_dashboard_hourly_rollups", 0)
	assertCount(t, db, "usage_hourly_aggregate_v1", 0)
	assertPermanentAggregateState(t, db, "pending", 0, 0, 4)
	assertIdentityAggregateVersion(t, db, "legacy-anthropic", 0)
	assertCheckpoint(t, db, "account_history", 0)
	assertCheckpoint(t, db, "dashboard_hourly", 0)
	assertCheckpoint(t, db, "unrelated", 9)
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
	result, err := repo.RunUsageCacheAccountingBatch(context.Background(), 20)
	if err != nil {
		t.Fatalf("run migration: %v", err)
	}
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
	if _, err := repo.RunUsageCacheAccountingBatch(context.Background(), 10); err != nil {
		t.Fatalf("first run: %v", err)
	}

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
	if resumed.Status != StatusPending || resumed.LastEventID != 1 || resumed.ProcessedRows != 1 || resumed.ChangedRows != 1 || resumed.TargetEventID != 2 {
		t.Fatalf("resumed state = %#v", resumed)
	}
	assertNormalizedTotalNull(t, db, "legacy-1")
	assertNormalizedTotalNull(t, db, "legacy-2")
	if _, err := db.Exec(`drop trigger reject_second_usage_stage`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	final, err := repo.RunUsageCacheAccountingBatch(context.Background(), 1)
	if err != nil {
		t.Fatalf("resumed batch: %v", err)
	}
	if !final.Completed || final.State.ProcessedRows != 2 || final.State.ChangedRows != 2 || final.State.LastEventID != 2 {
		t.Fatalf("final batch = %#v", final)
	}
	assertAccounting(t, db, "legacy-1", "included_in_input", 90, 100, 10, 0, 0)
	assertAccounting(t, db, "legacy-2", "included_in_input", 180, 200, 20, 0, 0)
	assertCount(t, db, "usage_cache_accounting_v2_changes", 0)
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
	result, err := repo.RunUsageCacheAccountingBatch(context.Background(), 100)
	if err != nil {
		t.Fatalf("run migration: %v", err)
	}
	if !result.Completed || result.State.ProcessedRows != 1 || result.State.ChangedRows != 1 {
		t.Fatalf("result = %#v", result)
	}
}

func TestUsageCacheAccountingFinalizationFailureRollsBackLastBatch(t *testing.T) {
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
	if _, err := db.Exec(`create trigger reject_rollup_delete before delete on usage_account_model_rollups
		begin select raise(abort, 'blocked'); end`); err != nil {
		t.Fatalf("create finalization failure trigger: %v", err)
	}

	finalizationErr := errors.New("finalization failed")
	if _, err := repo.RunUsageCacheAccountingBatch(context.Background(), 1); err == nil {
		t.Fatal("finalization error = nil, want trigger failure")
	} else {
		finalizationErr = err
	}
	assertNormalizedTotalNull(t, db, "legacy-1")
	assertNormalizedTotalNull(t, db, "legacy-2")
	assertCount(t, db, "usage_cache_accounting_v2_changes", 1)
	state, _, err := repo.UsageCacheAccountingState(context.Background())
	if err != nil {
		t.Fatalf("read rolled back state: %v", err)
	}
	if state.Status != StatusRunning || state.ProcessedRows != 1 || state.ChangedRows != 1 || state.LastEventID != 1 {
		t.Fatalf("rolled back state = %#v", state)
	}
	if err := repo.RecordUsageCacheAccountingFailure(context.Background(), finalizationErr); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	if _, err := db.Exec(`drop trigger reject_rollup_delete`); err != nil {
		t.Fatalf("drop finalization failure trigger: %v", err)
	}
	if _, err := repo.DiscoverUsageCacheAccounting(context.Background()); err != nil {
		t.Fatalf("resume migration: %v", err)
	}
	result, err := repo.RunUsageCacheAccountingBatch(context.Background(), 1)
	if err != nil || !result.Completed {
		t.Fatalf("resumed result = %#v err=%v", result, err)
	}
	assertAccounting(t, db, "legacy-1", "included_in_input", 90, 100, 10, 0, 0)
	assertAccounting(t, db, "legacy-2", "included_in_input", 180, 200, 20, 0, 0)
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
	Hash             string
	Provider         string
	Executor         string
	Model            string
	RawJSON          string
	Input            int64
	Output           int64
	Reasoning        int64
	Cached           int64
	CacheRead        int64
	CacheCreation    int64
	StoredMode       string
	StoredUncached   int64
	StoredTotalInput int64
	StoredRead       int64
	StoredCreation   int64
	Total            int64
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
		event_hash, timestamp_ms, timestamp, provider, executor_type, model,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens,
		cache_read_tokens, cache_creation_tokens, cache_input_mode,
		normalized_uncached_input_tokens, normalized_total_input_tokens,
		normalized_cache_read_tokens, normalized_cache_creation_tokens,
		total_tokens, raw_json, created_at_ms
	) values (?, 1, '1', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)`,
		fixture.Hash,
		fixture.Provider,
		fixture.Executor,
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
			) select event_hash, id, timestamp_ms, 0, 1, created_at_ms, 1
			from usage_events where event_hash = ?`,
			args: []any{eventHash},
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
			where aggregate_name = 'hourly_core' and schema_version = 1`,
			args: []any{eventHash, eventHash},
		},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("insert permanent aggregate fixture: %v", err)
		}
	}
}

func markMigrationDiscovering(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`update usage_data_migrations set
		status = 'discovering', last_event_id = 0, target_event_id = 0,
		processed_rows = 0, changed_rows = 0, started_at_ms = null, updated_at_ms = 0,
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
	var status string
	var checkpoint, coverage, target int64
	if err := db.QueryRow(`select status, backfill_last_event_id, coverage_event_id, target_event_id
		from usage_hourly_aggregate_state where aggregate_name = 'hourly_core'`).Scan(
		&status,
		&checkpoint,
		&coverage,
		&target,
	); err != nil {
		t.Fatalf("read permanent aggregate state: %v", err)
	}
	if status != wantStatus || checkpoint != wantCheckpoint || coverage != wantCoverage || target != wantTarget {
		t.Fatalf(
			"permanent aggregate state = status:%q checkpoint:%d coverage:%d target:%d, want status:%q checkpoint:%d coverage:%d target:%d",
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
