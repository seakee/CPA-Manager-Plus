package datamigration

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestUsageTokenAccountingMigratesV2LegacyAndUnknownRowsInBatches(t *testing.T) {
	db := openMigrationTestDB(t)
	insertTokenMigrationEvent(t, db, "valid-v2", "", "other", 999, 888, 777, 0, 0, 0, 1_664, `{
		"accounting_version":2,
		"token_breakdown":{
			"schema_version":2,"quality":"complete","total_tokens":140,
			"input":{"total_tokens":100,"uncached_tokens":70,"cache_read_tokens":20,"cache_write_tokens":10},
			"output":{"total_tokens":40,"non_reasoning_tokens":30,"reasoning_tokens":10},
			"unclassified_tokens":0
		}
	}`)
	insertTokenMigrationEvent(t, db, "legacy-openai", "openai", "gpt-5", 100, 20, 5, 30, 10, 5, 120, "")
	insertTokenMigrationEvent(t, db, "legacy-unknown", "", "other", 7, 3, 0, 0, 0, 0, 10, "")
	markTokenMigrationDiscovering(t, db)
	insertRollupFixtures(t, db)
	insertPermanentAggregateFixture(t, db, "valid-v2")
	insertPricingRollupFixtures(t, db, 1, 1, 3)

	repo := New(db)
	state, err := repo.DiscoverUsageTokenAccounting(context.Background())
	if err != nil {
		t.Fatalf("discover token migration: %v", err)
	}
	if state.Status != StatusPending || state.TargetEventID != 3 {
		t.Fatalf("discovered state = %#v", state)
	}
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, model,
		accounting_version, accounting_valid, accounting_quality,
		input_tokens, output_tokens, reasoning_tokens,
		normalized_uncached_input_tokens, normalized_total_input_tokens,
		normalized_cache_read_tokens, normalized_cache_creation_tokens,
		normalized_non_reasoning_output_tokens, normalized_reasoning_output_tokens,
		normalized_total_output_tokens, unclassified_tokens, total_tokens, created_at_ms
	) values ('new-canonical', 4, '4', 'openai', 'gpt-5', 2, 1, 'complete',
		9, 1, 0, 9, 9, 0, 0, 1, 0, 1, 0, 10, 4)`); err != nil {
		t.Fatalf("insert post-discovery event: %v", err)
	}

	first, err := repo.RunUsageTokenAccountingBatch(context.Background(), 2)
	if err != nil {
		t.Fatalf("first token batch: %v", err)
	}
	if first.Completed || first.Processed != 2 || first.State.LastEventID != 2 || first.State.ChangedRows != 2 {
		t.Fatalf("first batch = %#v", first)
	}
	assertTokenAccountingNull(t, db, "valid-v2")
	assertCount(t, db, "usage_token_accounting_v2_changes", 2)
	assertCount(t, db, "usage_account_model_rollups", 1)

	second, err := repo.RunUsageTokenAccountingBatch(context.Background(), 2)
	if err != nil {
		t.Fatalf("second token batch: %v", err)
	}
	if !second.Completed || second.Processed != 1 || second.State.ProcessedRows != 3 || second.State.ChangedRows != 3 || second.State.LastEventID != 3 {
		t.Fatalf("second batch = %#v", second)
	}
	assertTokenAccounting(t, db, "valid-v2", 2, 1, "complete", 70, 100, 20, 10, 30, 10, 40, 0, 140)
	assertTokenAccounting(t, db, "legacy-openai", 0, 0, "complete", 70, 100, 25, 5, 15, 5, 20, 0, 120)
	assertTokenAccounting(t, db, "legacy-unknown", 0, 0, "unclassified", 0, 0, 0, 0, 0, 0, 0, 10, 10)
	assertTokenAccounting(t, db, "new-canonical", 2, 1, "complete", 9, 9, 0, 0, 1, 0, 1, 0, 10)
	assertLegacyTokenColumns(t, db, "valid-v2", 999, 888, 777)
	assertCount(t, db, "usage_token_accounting_v2_changes", 0)
	assertCount(t, db, "usage_account_model_rollups", 0)
	assertCount(t, db, "usage_dashboard_hourly_rollups", 0)
	assertCount(t, db, "usage_hourly_aggregate_v1", 0)
	assertCount(t, db, "usage_pricing_hourly_rollups_v1", 0)
	assertCount(t, db, "usage_pricing_account_rollups_v1", 0)
	assertPermanentAggregateState(t, db, "pending", 0, 0, 4)
	assertPricingAggregateState(t, db, "pending", 0, 0, 4)
	assertCheckpoint(t, db, "account_history", 0)
	assertCheckpoint(t, db, "dashboard_hourly", 0)
}

func TestUsageTokenAccountingRepairsLegacyDerivedTotalWithoutExplicitRawTotal(t *testing.T) {
	db := openMigrationTestDB(t)
	insertTokenMigrationEvent(t, db, "legacy-derived-total", "openai", "gpt-5", 100, 20, 5, 0, 0, 0, 125, `{}`)
	markTokenMigrationDiscovering(t, db)

	repo := New(db)
	if _, err := repo.DiscoverUsageTokenAccounting(context.Background()); err != nil {
		t.Fatalf("discover token migration: %v", err)
	}
	result, err := repo.RunUsageTokenAccountingBatch(context.Background(), 10)
	if err != nil || !result.Completed || result.Processed != 1 {
		t.Fatalf("run token migration: result=%#v err=%v", result, err)
	}
	assertTokenAccounting(t, db, "legacy-derived-total", 0, 0, "complete", 100, 100, 0, 0, 15, 5, 20, 0, 120)
	assertLegacyTokenColumns(t, db, "legacy-derived-total", 100, 20, 5)
}

func TestUsageTokenAccountingPreservesInvalidExplicitRawTotalsAsInconsistent(t *testing.T) {
	db := openMigrationTestDB(t)
	insertTokenMigrationEvent(t, db, "explicit-zero", "openai", "gpt-5", 100, 20, 0, 0, 0, 0, 125, `{"tokens":{"total_tokens":0}}`)
	insertTokenMigrationEvent(t, db, "explicit-negative", "openai", "gpt-5", 100, 20, 0, 0, 0, 0, 120, `{"tokens":{"total_tokens":-1}}`)
	insertTokenMigrationEvent(t, db, "explicit-fractional", "openai", "gpt-5", 100, 20, 0, 0, 0, 0, 120, `{"usage":{"total_tokens":1.5}}`)
	insertTokenMigrationEvent(t, db, "explicit-wrong-type", "openai", "gpt-5", 100, 20, 0, 0, 0, 0, 120, `{"total_tokens":{}}`)
	markTokenMigrationDiscovering(t, db)

	repo := New(db)
	if _, err := repo.DiscoverUsageTokenAccounting(context.Background()); err != nil {
		t.Fatalf("discover token migration: %v", err)
	}
	result, err := repo.RunUsageTokenAccountingBatch(context.Background(), 10)
	if err != nil || !result.Completed || result.Processed != 4 {
		t.Fatalf("run token migration: result=%#v err=%v", result, err)
	}

	assertTokenAccounting(t, db, "explicit-zero", 0, 0, "complete", 100, 100, 0, 0, 20, 0, 20, 0, 120)
	for _, hash := range []string{"explicit-negative", "explicit-fractional", "explicit-wrong-type"} {
		assertTokenAccounting(t, db, hash, 0, 0, "inconsistent", 0, 0, 0, 0, 0, 0, 0, 120, 120)
	}
}

func TestUsageTokenAccountingReevaluatesDerivedCacheModeAndPreservesRawExplicitMode(t *testing.T) {
	db := openMigrationTestDB(t)
	insertTokenMigrationEvent(t, db, "derived-mode", "", "claude-alias", 100, 20, 0, 0, 20, 0, 120, "")
	insertTokenMigrationEvent(t, db, "raw-explicit-mode", "", "claude-alias", 100, 20, 0, 0, 20, 0, 120, `{"tokens":{"cache_input_mode":"separate_from_input"}}`)
	if _, err := db.Exec(`update usage_events set auth_type = 'codex', cache_input_mode = case
		when event_hash = 'derived-mode' then 'separate_from_input'
		else 'included_in_input'
	end`); err != nil {
		t.Fatalf("seed historical cache modes: %v", err)
	}
	markTokenMigrationDiscovering(t, db)

	repo := New(db)
	if _, err := repo.DiscoverUsageTokenAccounting(context.Background()); err != nil {
		t.Fatalf("discover token migration: %v", err)
	}
	result, err := repo.RunUsageTokenAccountingBatch(context.Background(), 10)
	if err != nil || !result.Completed || result.Processed != 2 {
		t.Fatalf("run token migration: result=%#v err=%v", result, err)
	}

	assertTokenAccounting(t, db, "derived-mode", 0, 0, "complete", 80, 100, 20, 0, 20, 0, 20, 0, 120)
	assertTokenAccounting(t, db, "raw-explicit-mode", 0, 0, "complete", 100, 120, 20, 0, 20, 0, 20, 0, 140)
	for hash, wantMode := range map[string]string{
		"derived-mode":      "included_in_input",
		"raw-explicit-mode": "separate_from_input",
	} {
		var mode string
		if err := db.QueryRow(`select cache_input_mode from usage_events where event_hash = ?`, hash).Scan(&mode); err != nil {
			t.Fatalf("read cache mode %s: %v", hash, err)
		}
		if mode != wantMode {
			t.Fatalf("cache mode %s = %q, want %q", hash, mode, wantMode)
		}
	}
}

func TestUsageTokenAccountingFinalizationFailsClosedOnFutureAggregateState(t *testing.T) {
	db := openMigrationTestDB(t)
	insertTokenMigrationEvent(t, db, "future-schema", "openai", "gpt-5", 100, 10, 0, 0, 0, 0, 110, "")
	markTokenMigrationDiscovering(t, db)
	insertPermanentAggregateFixture(t, db, "future-schema")
	insertPricingRollupFixtures(t, db, 1, 1, 1)
	if _, err := db.Exec(`update usage_event_identity_ledger set aggregate_schema_version = 99
		where event_hash = 'future-schema'`); err != nil {
		t.Fatalf("mark future identity schema: %v", err)
	}
	if _, err := db.Exec(`update usage_hourly_aggregate_state set
		schema_version = 99, status = 'ready', backfill_last_event_id = 7,
		coverage_event_id = 8, target_event_id = 9
		where aggregate_name = 'hourly_core'`); err != nil {
		t.Fatalf("mark future aggregate state: %v", err)
	}
	if _, err := db.Exec(`update usage_pricing_rollup_state set
		schema_version = 99, status = 'ready', backfill_last_event_id = 17,
		coverage_event_id = 18, target_event_id = 19
		where rollup_name = 'pricing_v1'`); err != nil {
		t.Fatalf("mark future pricing state: %v", err)
	}

	repo := New(db)
	if _, err := repo.DiscoverUsageTokenAccounting(context.Background()); err != nil {
		t.Fatalf("discover token migration: %v", err)
	}
	result, err := repo.RunUsageTokenAccountingBatch(context.Background(), 10)
	if err == nil || result.Completed {
		t.Fatalf("future schema migration should fail closed: result=%#v err=%v", result, err)
	}
	assertTokenAccountingNull(t, db, "future-schema")
	assertIdentityAggregateVersion(t, db, "future-schema", 99)
	assertCount(t, db, "usage_token_accounting_v2_changes", 0)
	assertCount(t, db, "usage_hourly_aggregate_v1", 1)
	assertCount(t, db, "usage_pricing_hourly_rollups_v1", 1)
	assertCount(t, db, "usage_pricing_account_rollups_v1", 1)

	var schemaVersion int
	var status string
	var checkpoint, coverage, target int64
	if err := db.QueryRow(`select schema_version, status, backfill_last_event_id, coverage_event_id, target_event_id
		from usage_hourly_aggregate_state where aggregate_name = 'hourly_core'`).Scan(
		&schemaVersion, &status, &checkpoint, &coverage, &target,
	); err != nil {
		t.Fatalf("read future aggregate state: %v", err)
	}
	if schemaVersion != 99 || status != "ready" || checkpoint != 7 || coverage != 8 || target != 9 {
		t.Fatalf("future aggregate state changed: schema=%d status=%q checkpoint=%d coverage=%d target=%d",
			schemaVersion, status, checkpoint, coverage, target)
	}
	if err := db.QueryRow(`select schema_version, status, backfill_last_event_id, coverage_event_id, target_event_id
		from usage_pricing_rollup_state where rollup_name = 'pricing_v1'`).Scan(
		&schemaVersion, &status, &checkpoint, &coverage, &target,
	); err != nil {
		t.Fatalf("read future pricing state: %v", err)
	}
	if schemaVersion != 99 || status != "ready" || checkpoint != 17 || coverage != 18 || target != 19 {
		t.Fatalf("future pricing state changed: schema=%d status=%q checkpoint=%d coverage=%d target=%d",
			schemaVersion, status, checkpoint, coverage, target)
	}
}

func TestUsageTokenAccountingRepairsFullyPopulatedInvalidCanonicalRow(t *testing.T) {
	db := openMigrationTestDB(t)
	insertTokenMigrationEvent(t, db, "invalid-canonical", "openai", "gpt-5", 100, 20, 0, 0, 0, 0, 120, `{
		"accounting_version":2,
		"token_breakdown":{
			"schema_version":2,"quality":"complete","total_tokens":120,
			"input":{"total_tokens":100,"uncached_tokens":100,"cache_read_tokens":0,"cache_write_tokens":0},
			"output":{"total_tokens":20,"non_reasoning_tokens":20,"reasoning_tokens":0},
			"unclassified_tokens":0
		}
	}`)
	if _, err := db.Exec(`update usage_events set
		accounting_version = 2, accounting_valid = 1, accounting_quality = 'complete',
		normalized_uncached_input_tokens = 100, normalized_total_input_tokens = 99,
		normalized_cache_read_tokens = 0, normalized_cache_creation_tokens = 0,
		normalized_non_reasoning_output_tokens = 20, normalized_reasoning_output_tokens = 0,
		normalized_total_output_tokens = 20, unclassified_tokens = 0, total_tokens = 119
	where event_hash = 'invalid-canonical'`); err != nil {
		t.Fatalf("seed invalid canonical accounting: %v", err)
	}
	markTokenMigrationDiscovering(t, db)

	repo := New(db)
	state, err := repo.DiscoverUsageTokenAccounting(context.Background())
	if err != nil {
		t.Fatalf("discover token migration: %v", err)
	}
	if state.Status != StatusPending || state.TargetEventID != 1 {
		t.Fatalf("discovered state = %#v", state)
	}
	result, err := repo.RunUsageTokenAccountingBatch(context.Background(), 10)
	if err != nil || !result.Completed || result.Processed != 1 {
		t.Fatalf("run token migration: result=%#v err=%v", result, err)
	}
	assertTokenAccounting(t, db, "invalid-canonical", 2, 1, "complete", 100, 100, 0, 0, 20, 0, 20, 0, 120)
}

func TestUsageTokenAccountingKeepsInvalidCanonicalWithoutRawProvenanceUnclassified(t *testing.T) {
	db := openMigrationTestDB(t)
	insertTokenMigrationEvent(t, db, "invalid-canonical-no-raw", "openai", "gpt-5", 100, 20, 0, 0, 0, 0, 120, "")
	if _, err := db.Exec(`update usage_events set
		accounting_version = 2, accounting_valid = 1, accounting_quality = 'complete',
		normalized_uncached_input_tokens = 100, normalized_total_input_tokens = 99,
		normalized_cache_read_tokens = 0, normalized_cache_creation_tokens = 0,
		normalized_non_reasoning_output_tokens = 20, normalized_reasoning_output_tokens = 0,
		normalized_total_output_tokens = 20, unclassified_tokens = 0, total_tokens = 119
	where event_hash = 'invalid-canonical-no-raw'`); err != nil {
		t.Fatalf("seed invalid canonical accounting without raw provenance: %v", err)
	}
	markTokenMigrationDiscovering(t, db)

	repo := New(db)
	state, err := repo.DiscoverUsageTokenAccounting(context.Background())
	if err != nil {
		t.Fatalf("discover token migration: %v", err)
	}
	if state.Status != StatusPending || state.TargetEventID != 1 {
		t.Fatalf("discovered state = %#v", state)
	}
	result, err := repo.RunUsageTokenAccountingBatch(context.Background(), 10)
	if err != nil || !result.Completed || result.Processed != 1 {
		t.Fatalf("run token migration: result=%#v err=%v", result, err)
	}
	assertTokenAccounting(t, db, "invalid-canonical-no-raw", 2, 0, "inconsistent", 0, 0, 0, 0, 0, 0, 0, 120, 120)
}

func TestUsageTokenAccountingFailureResumesFromStagedCheckpoint(t *testing.T) {
	db := openMigrationTestDB(t)
	insertTokenMigrationEvent(t, db, "legacy-1", "openai", "gpt-5", 100, 10, 0, 0, 0, 0, 110, "")
	insertTokenMigrationEvent(t, db, "legacy-2", "openai", "gpt-5", 200, 20, 0, 0, 0, 0, 220, "")
	markTokenMigrationDiscovering(t, db)
	repo := New(db)
	if _, err := repo.DiscoverUsageTokenAccounting(context.Background()); err != nil {
		t.Fatalf("discover token migration: %v", err)
	}
	first, err := repo.RunUsageTokenAccountingBatch(context.Background(), 1)
	if err != nil {
		t.Fatalf("first token batch: %v", err)
	}
	if first.State.LastEventID != 1 || first.State.ProcessedRows != 1 || first.State.ChangedRows != 1 {
		t.Fatalf("first batch = %#v", first)
	}
	if _, err := db.Exec(`create trigger reject_second_token_stage before insert on usage_token_accounting_v2_changes
		when new.event_id = 2 begin select raise(abort, 'blocked'); end`); err != nil {
		t.Fatalf("create staging trigger: %v", err)
	}

	batchErr := errors.New("token batch failed")
	if _, err := repo.RunUsageTokenAccountingBatch(context.Background(), 1); err == nil {
		t.Fatal("second batch error = nil, want trigger failure")
	} else {
		batchErr = err
	}
	if err := repo.RecordUsageTokenAccountingFailure(context.Background(), batchErr); err != nil {
		t.Fatalf("record token migration failure: %v", err)
	}
	failed, found, err := repo.UsageTokenAccountingState(context.Background())
	if err != nil || !found {
		t.Fatalf("failed state: found=%v err=%v", found, err)
	}
	if failed.Status != StatusFailed || failed.LastEventID != 1 || failed.ProcessedRows != 1 || failed.ChangedRows != 1 || failed.LastError == "" {
		t.Fatalf("failed state = %#v", failed)
	}
	resumed, err := repo.DiscoverUsageTokenAccounting(context.Background())
	if err != nil {
		t.Fatalf("resume token migration: %v", err)
	}
	if resumed.Status != StatusPending || resumed.LastEventID != 1 || resumed.TargetEventID != 2 {
		t.Fatalf("resumed state = %#v", resumed)
	}
	if _, err := db.Exec(`drop trigger reject_second_token_stage`); err != nil {
		t.Fatalf("drop staging trigger: %v", err)
	}
	final, err := repo.RunUsageTokenAccountingBatch(context.Background(), 1)
	if err != nil || !final.Completed {
		t.Fatalf("resumed result = %#v err=%v", final, err)
	}
	assertTokenAccounting(t, db, "legacy-1", 0, 0, "complete", 100, 100, 0, 0, 10, 0, 10, 0, 110)
	assertTokenAccounting(t, db, "legacy-2", 0, 0, "complete", 200, 200, 0, 0, 20, 0, 20, 0, 220)
}

func TestUsageTokenAccountingFinalizationIsAtomicAndSecondRunIsIdempotent(t *testing.T) {
	db := openMigrationTestDB(t)
	insertTokenMigrationEvent(t, db, "legacy-1", "openai", "gpt-5", 100, 10, 0, 0, 0, 0, 110, "")
	insertTokenMigrationEvent(t, db, "legacy-2", "openai", "gpt-5", 200, 20, 0, 0, 0, 0, 220, "")
	markTokenMigrationDiscovering(t, db)
	insertRollupFixtures(t, db)
	repo := New(db)
	if _, err := repo.DiscoverUsageTokenAccounting(context.Background()); err != nil {
		t.Fatalf("discover token migration: %v", err)
	}
	if _, err := repo.RunUsageTokenAccountingBatch(context.Background(), 1); err != nil {
		t.Fatalf("first token batch: %v", err)
	}
	if _, err := db.Exec(`create trigger reject_token_rollup_delete before delete on usage_account_model_rollups
		begin select raise(abort, 'blocked'); end`); err != nil {
		t.Fatalf("create finalization trigger: %v", err)
	}
	if _, err := repo.RunUsageTokenAccountingBatch(context.Background(), 1); err == nil {
		t.Fatal("finalization error = nil, want trigger failure")
	}
	assertTokenAccountingNull(t, db, "legacy-1")
	assertTokenAccountingNull(t, db, "legacy-2")
	assertCount(t, db, "usage_token_accounting_v2_changes", 1)
	state, _, err := repo.UsageTokenAccountingState(context.Background())
	if err != nil {
		t.Fatalf("read rolled back state: %v", err)
	}
	if state.Status != StatusRunning || state.LastEventID != 1 || state.ProcessedRows != 1 {
		t.Fatalf("rolled back state = %#v", state)
	}
	if err := repo.RecordUsageTokenAccountingFailure(context.Background(), errors.New("finalization failed")); err != nil {
		t.Fatalf("record finalization failure: %v", err)
	}
	if _, err := db.Exec(`drop trigger reject_token_rollup_delete`); err != nil {
		t.Fatalf("drop finalization trigger: %v", err)
	}
	if _, err := repo.DiscoverUsageTokenAccounting(context.Background()); err != nil {
		t.Fatalf("resume token migration: %v", err)
	}
	if result, err := repo.RunUsageTokenAccountingBatch(context.Background(), 1); err != nil || !result.Completed {
		t.Fatalf("resumed result = %#v err=%v", result, err)
	}

	insertRollupFixtures(t, db)
	markTokenMigrationDiscovering(t, db)
	state, err = repo.DiscoverUsageTokenAccounting(context.Background())
	if err != nil {
		t.Fatalf("discover second token migration: %v", err)
	}
	if state.Status != StatusCompleted || state.TargetEventID != 0 || state.ProcessedRows != 0 || state.ChangedRows != 0 {
		t.Fatalf("second discovery state = %#v", state)
	}
	assertCount(t, db, "usage_account_model_rollups", 1)
	assertCount(t, db, "usage_dashboard_hourly_rollups", 1)
}

func insertTokenMigrationEvent(
	t *testing.T,
	db *sql.DB,
	hash, provider, model string,
	input, output, reasoning, cached, cacheRead, cacheCreation, total int64,
	rawJSON string,
) {
	t.Helper()
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, model,
		input_tokens, output_tokens, reasoning_tokens, cached_tokens,
		cache_read_tokens, cache_creation_tokens, total_tokens, raw_json, created_at_ms
	) values (?, (select coalesce(max(id), 0) + 1 from usage_events), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		(select coalesce(max(id), 0) + 1 from usage_events))`,
		hash, hash, provider, model, input, output, reasoning, cached, cacheRead, cacheCreation, total, rawJSON,
	); err != nil {
		t.Fatalf("insert token migration event %s: %v", hash, err)
	}
}

func markTokenMigrationDiscovering(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`update usage_data_migrations set
		status = 'discovering', last_event_id = 0, target_event_id = 0,
		processed_rows = 0, changed_rows = 0, started_at_ms = null, updated_at_ms = 0,
		finished_at_ms = null, last_error = null
	where name = ?`, UsageTokenAccountingMigrationName); err != nil {
		t.Fatalf("mark token migration discovering: %v", err)
	}
}

func assertTokenAccountingNull(t *testing.T, db *sql.DB, hash string) {
	t.Helper()
	var value any
	if err := db.QueryRow(`select normalized_total_output_tokens from usage_events where event_hash = ?`, hash).Scan(&value); err != nil {
		t.Fatalf("read token accounting null %s: %v", hash, err)
	}
	if value != nil {
		t.Fatalf("normalized output for %s = %#v, want null before atomic apply", hash, value)
	}
}

func assertTokenAccounting(
	t *testing.T,
	db *sql.DB,
	hash string,
	version, valid int,
	quality string,
	uncached, input, cacheRead, cacheCreation, nonReasoning, reasoning, output, unclassified, total int64,
) {
	t.Helper()
	var gotVersion, gotValid int
	var gotQuality string
	var gotUncached, gotInput, gotCacheRead, gotCacheCreation int64
	var gotNonReasoning, gotReasoning, gotOutput, gotUnclassified, gotTotal int64
	if err := db.QueryRow(`select accounting_version, accounting_valid, accounting_quality,
		normalized_uncached_input_tokens, normalized_total_input_tokens,
		normalized_cache_read_tokens, normalized_cache_creation_tokens,
		normalized_non_reasoning_output_tokens, normalized_reasoning_output_tokens,
		normalized_total_output_tokens, unclassified_tokens, total_tokens
	from usage_events where event_hash = ?`, hash).Scan(
		&gotVersion, &gotValid, &gotQuality,
		&gotUncached, &gotInput, &gotCacheRead, &gotCacheCreation,
		&gotNonReasoning, &gotReasoning, &gotOutput, &gotUnclassified, &gotTotal,
	); err != nil {
		t.Fatalf("read token accounting %s: %v", hash, err)
	}
	if gotVersion != version || gotValid != valid || gotQuality != quality ||
		gotUncached != uncached || gotInput != input || gotCacheRead != cacheRead || gotCacheCreation != cacheCreation ||
		gotNonReasoning != nonReasoning || gotReasoning != reasoning || gotOutput != output ||
		gotUnclassified != unclassified || gotTotal != total {
		t.Fatalf("token accounting %s = version:%d valid:%d quality:%q input:%d/%d/%d/%d output:%d/%d/%d unclassified:%d total:%d",
			hash, gotVersion, gotValid, gotQuality, gotUncached, gotInput, gotCacheRead, gotCacheCreation,
			gotNonReasoning, gotReasoning, gotOutput, gotUnclassified, gotTotal)
	}
}

func assertLegacyTokenColumns(t *testing.T, db *sql.DB, hash string, input, output, reasoning int64) {
	t.Helper()
	var gotInput, gotOutput, gotReasoning int64
	if err := db.QueryRow(`select input_tokens, output_tokens, reasoning_tokens from usage_events where event_hash = ?`, hash).Scan(
		&gotInput, &gotOutput, &gotReasoning,
	); err != nil {
		t.Fatalf("read legacy token columns %s: %v", hash, err)
	}
	if gotInput != input || gotOutput != output || gotReasoning != reasoning {
		t.Fatalf("legacy token columns %s = %d/%d/%d, want %d/%d/%d", hash, gotInput, gotOutput, gotReasoning, input, output, reasoning)
	}
}
