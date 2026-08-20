package usageaggregate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestIsCurrentStructureRevision(t *testing.T) {
	for _, revision := range []string{
		StructureRevision,
		StructureRevision + ":rebuild-0123456789abcdef0123456789abcdef",
		StructureRevision + ":cache-accounting-v2-1700000000000-42",
		StructureRevision + ":rebuild-0123456789abcdef0123456789abcdef:cache-accounting-v2-1700000000000-42",
	} {
		if !IsCurrentStructureRevision(revision) {
			t.Fatalf("current structure revision rejected: %q", revision)
		}
	}
	for _, revision := range []string{
		"",
		"stale",
		StructureRevision + ":rebuild-short",
		StructureRevision + ":rebuild-0123456789abcdef0123456789abcdeg",
		StructureRevision + ":cache-accounting-v2-0-42",
		StructureRevision + ":cache-accounting-v2-1700000000000-0",
		StructureRevision + ":cache-accounting-v2-invalid-42",
		StructureRevision + ":cache-accounting-v2-1700000000000",
	} {
		if IsCurrentStructureRevision(revision) {
			t.Fatalf("invalid structure revision accepted: %q", revision)
		}
	}
}

func TestCatchUpAndLoadRowsMergeCoverageDeltaAndLateEvents(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)
	baseMS := int64(1_800_000_000_000)
	baseMS -= baseMS % hourMS
	latency100 := int64(100)
	latency300 := int64(300)
	first := aggregateTestEvent("aggregate-first", baseMS+10*60*1000, "model-a", false, 10, 5, &latency100)
	second := aggregateTestEvent("aggregate-second", baseMS+hourMS+10*60*1000, "model-b", true, 20, 7, &latency300)
	if _, err := events.InsertBatch(ctx, []usage.Event{first, second}); err != nil {
		t.Fatalf("insert initial events: %v", err)
	}

	firstCatchUp, err := repo.CatchUp(ctx, 1, baseMS+3*hourMS)
	if err != nil {
		t.Fatalf("first catch-up: %v", err)
	}
	if firstCatchUp.Processed != 1 || firstCatchUp.CoverageEventID != 1 || !firstCatchUp.Pending || firstCatchUp.Rebuilt {
		t.Fatalf("first catch-up = %#v", firstCatchUp)
	}
	rows, state, available, err := repo.LoadRows(ctx, Filter{
		FromMS:        baseMS,
		ToMS:          baseMS + 3*hourMS,
		IncludeFailed: true,
	})
	if err != nil {
		t.Fatalf("load mixed rows: %v", err)
	}
	if !available || state.CoverageEventID != 1 || sumCalls(rows) != 2 {
		t.Fatalf("mixed rows = available:%v state:%#v rows:%#v", available, state, rows)
	}

	late := aggregateTestEvent("aggregate-late", baseMS+20*60*1000, "model-a", false, 30, 9, nil)
	if _, err := events.InsertBatch(ctx, []usage.Event{late}); err != nil {
		t.Fatalf("insert late event: %v", err)
	}
	rowsBeforeCatchUp, _, available, err := repo.LoadRows(ctx, Filter{
		FromMS:        baseMS,
		ToMS:          baseMS + 3*hourMS,
		IncludeFailed: true,
	})
	if err != nil || !available {
		t.Fatalf("load late delta: available=%v err=%v", available, err)
	}
	if sumCalls(rowsBeforeCatchUp) != 3 || callsFor(rowsBeforeCatchUp, baseMS, "model-a", false) != 2 {
		t.Fatalf("late delta rows = %#v", rowsBeforeCatchUp)
	}

	for {
		result, err := repo.CatchUp(ctx, 10, baseMS+4*hourMS)
		if err != nil {
			t.Fatalf("finish catch-up: %v", err)
		}
		if !result.Pending {
			break
		}
	}
	rowsAfterCatchUp, readyState, available, err := repo.LoadRows(ctx, Filter{
		FromMS:        baseMS,
		ToMS:          baseMS + 3*hourMS,
		IncludeFailed: true,
	})
	if err != nil || !available {
		t.Fatalf("load completed rows: available=%v err=%v", available, err)
	}
	if readyState.Status != "ready" || readyState.CoverageEventID != 3 || sumCalls(rowsAfterCatchUp) != 3 {
		t.Fatalf("completed state=%#v rows=%#v", readyState, rowsAfterCatchUp)
	}
	if callsFor(rowsAfterCatchUp, baseMS, "model-a", false) != 2 {
		t.Fatalf("late event was not folded into old hour: %#v", rowsAfterCatchUp)
	}
	var ledgerReady int
	if err := db.QueryRow(`select count(*) from usage_event_identity_ledger where aggregate_schema_version = ?`, SchemaVersion).Scan(&ledgerReady); err != nil {
		t.Fatalf("count aggregate ledger rows: %v", err)
	}
	if ledgerReady != 3 {
		t.Fatalf("aggregate ledger rows = %d, want 3", ledgerReady)
	}
}

func TestCatchUpBackfillsLegacyIdentityLedger(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	repo := New(db)
	timestampMS := int64(1_800_000_001_000)
	createdAtMS := timestampMS + 123
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, input_tokens, output_tokens, total_tokens, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?)`,
		"aggregate-legacy-identity",
		timestampMS,
		time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		"model-a",
		1,
		2,
		3,
		createdAtMS,
	); err != nil {
		t.Fatalf("insert legacy event: %v", err)
	}
	if _, err := repo.CatchUp(ctx, 10, timestampMS+hourMS); err != nil {
		t.Fatalf("catch up legacy event: %v", err)
	}
	var rawEventID, firstSeenAtMS int64
	var version int
	if err := db.QueryRow(`select raw_event_id, first_seen_at_ms, aggregate_schema_version
		from usage_event_identity_ledger where event_hash = ?`, "aggregate-legacy-identity").Scan(
		&rawEventID,
		&firstSeenAtMS,
		&version,
	); err != nil {
		t.Fatalf("read legacy identity ledger: %v", err)
	}
	if rawEventID != 1 || firstSeenAtMS != createdAtMS || version != SchemaVersion {
		t.Fatalf("legacy identity = raw:%d first_seen:%d version:%d", rawEventID, firstSeenAtMS, version)
	}
}

func TestCatchUpIsIdempotentWhenCheckpointIsReplayed(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)
	baseMS := int64(1_800_000_000_000)
	baseMS -= baseMS % hourMS
	if _, err := events.InsertBatch(ctx, []usage.Event{
		aggregateTestEvent("aggregate-replay", baseMS+1000, "model-a", false, 1, 2, nil),
	}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := repo.CatchUp(ctx, 10, baseMS+hourMS); err != nil {
		t.Fatalf("initial catch-up: %v", err)
	}
	if _, err := db.Exec(`update usage_hourly_aggregate_state set
		status = 'pending', backfill_last_event_id = 0, coverage_event_id = 0
		where aggregate_name = ?`, AggregateName); err != nil {
		t.Fatalf("rewind checkpoint fixture: %v", err)
	}
	rows, _, available, err := repo.LoadRows(ctx, Filter{
		FromMS:        baseMS,
		ToMS:          baseMS + hourMS,
		IncludeFailed: true,
	})
	if err != nil || !available {
		t.Fatalf("load replayed checkpoint: available=%v err=%v", available, err)
	}
	if sumCalls(rows) != 1 {
		t.Fatalf("checkpoint replay double-counted aggregate coverage: %#v", rows)
	}
	if _, err := repo.CatchUp(ctx, 10, baseMS+2*hourMS); err != nil {
		t.Fatalf("replayed catch-up: %v", err)
	}
	var calls int64
	if err := db.QueryRow(`select calls from usage_hourly_aggregate_v1`).Scan(&calls); err != nil {
		t.Fatalf("read aggregate calls: %v", err)
	}
	if calls != 1 {
		t.Fatalf("aggregate calls after replay = %d, want 1", calls)
	}
	state, err := repo.State(ctx)
	if err != nil {
		t.Fatalf("read replayed state: %v", err)
	}
	if state.ProcessedEvents != 1 {
		t.Fatalf("processed events after replay = %d, want 1", state.ProcessedEvents)
	}
}

// TestCatchUpReaggregatesAcrossRevisionWithoutDoubleCount covers a
// migration-driven structure_revision switch. The migration parks the old
// aggregate table and recreates an empty one, so rebuilding must produce one
// call, update the ledger revision, and remain idempotent on replay.
func TestCatchUpReaggregatesAcrossRevisionWithoutDoubleCount(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)
	baseMS := int64(1_800_000_000_000)
	baseMS -= baseMS % hourMS
	if _, err := events.InsertBatch(ctx, []usage.Event{
		aggregateTestEvent("revision-event-1", baseMS+1000, "model-a", false, 1, 2, nil),
		aggregateTestEvent("revision-event-2", baseMS+2000, "model-a", false, 1, 2, nil),
	}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := repo.CatchUp(ctx, 10, baseMS+hourMS); err != nil {
		t.Fatalf("initial catch-up: %v", err)
	}

	// Simulate a migration-driven revision switch: the aggregate table has been
	// recreated empty, state points at the new revision with a zero checkpoint,
	// and the preserved ledger row still carries the prior revision.
	if _, err := db.Exec(`delete from usage_hourly_aggregate_v1`); err != nil {
		t.Fatalf("clear aggregate for revision switch: %v", err)
	}
	if _, err := db.Exec(`update usage_hourly_aggregate_state set
		structure_revision = 'schema-3:model-1:rebuild-test',
		status = 'pending', backfill_last_event_id = 0,
		coverage_event_id = 0, processed_events = 0
		where aggregate_name = ?`, AggregateName); err != nil {
		t.Fatalf("switch revision fixture: %v", err)
	}

	assertLoadedCalls := func(stage string, want int64) {
		t.Helper()
		rows, _, available, loadErr := repo.LoadRows(ctx, Filter{
			FromMS:        baseMS,
			ToMS:          baseMS + hourMS,
			IncludeFailed: true,
		})
		if loadErr != nil || !available {
			t.Fatalf("%s load rows: available=%v err=%v", stage, available, loadErr)
		}
		if calls := sumCalls(rows); calls != want {
			t.Fatalf("%s loaded calls = %d, want %d: %#v", stage, calls, want, rows)
		}
	}
	assertLoadedCalls("before cross-revision catch-up", 2)

	result, err := repo.CatchUp(ctx, 1, baseMS+2*hourMS)
	if err != nil {
		t.Fatalf("cross-revision catch-up: %v", err)
	}
	if !result.Pending || result.CoverageEventID != 1 || !result.Rebuilt {
		t.Fatalf("partial cross-revision catch-up = %#v", result)
	}
	assertLoadedCalls("during cross-revision catch-up", 2)
	completed, err := repo.CatchUp(ctx, 10, baseMS+2*hourMS)
	if err != nil {
		t.Fatalf("finish cross-revision catch-up: %v", err)
	}
	if !completed.Rebuilt || completed.Pending || completed.CoverageEventID != completed.TargetEventID {
		t.Fatalf("completed cross-revision catch-up = %#v", completed)
	}
	if err := assertAggregateCallsAndLedgerRevision(db, 2, "schema-3:model-1:rebuild-test"); err != nil {
		t.Fatalf("after cross-revision catch-up: %v", err)
	}

	// Replaying the same new revision after rewinding the checkpoint remains
	// idempotent instead of doubling the aggregate.
	if _, err := db.Exec(`update usage_hourly_aggregate_state set
		status = 'pending', backfill_last_event_id = 0, coverage_event_id = 0
		where aggregate_name = ?`, AggregateName); err != nil {
		t.Fatalf("rewind checkpoint for replay: %v", err)
	}
	if _, err := repo.CatchUp(ctx, 10, baseMS+3*hourMS); err != nil {
		t.Fatalf("same-revision replay catch-up: %v", err)
	}
	if err := assertAggregateCallsAndLedgerRevision(db, 2, "schema-3:model-1:rebuild-test"); err != nil {
		t.Fatalf("after same-revision replay: %v", err)
	}
}

func TestCatchUpPreservesRebuildStateAcrossRecordedFailure(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)
	baseMS := int64(1_800_000_000_000)
	baseMS -= baseMS % hourMS
	if _, err := events.InsertBatch(ctx, []usage.Event{
		aggregateTestEvent("rebuild-resume-1", baseMS+1_000, "model-a", false, 10, 5, nil),
		aggregateTestEvent("rebuild-resume-2", baseMS+2_000, "model-a", false, 20, 7, nil),
	}); err != nil {
		t.Fatalf("insert rebuild events: %v", err)
	}
	if _, err := db.Exec(`update usage_hourly_aggregate_state set
		status = 'pending', backfill_last_event_id = 0, coverage_event_id = 0,
		target_event_id = 2, processed_events = 0, last_error = null
		where aggregate_name = ?`, AggregateName); err != nil {
		t.Fatalf("schedule aggregate rebuild: %v", err)
	}
	assertRawEquivalent := func(stage string) {
		t.Helper()
		rows, _, available, loadErr := repo.LoadRows(ctx, Filter{
			FromMS:        baseMS,
			ToMS:          baseMS + hourMS,
			IncludeFailed: true,
		})
		if loadErr != nil || !available || sumCalls(rows) != 2 {
			t.Fatalf("%s aggregate rows = available:%v err:%v rows:%#v", stage, available, loadErr, rows)
		}
	}

	if err := repo.RecordFailure(ctx, errors.New("interrupted pending rebuild"), baseMS+hourMS); err != nil {
		t.Fatalf("record pending rebuild failure: %v", err)
	}
	state, err := repo.State(ctx)
	if err != nil {
		t.Fatalf("read pending rebuild state: %v", err)
	}
	if state.Status != "pending" || state.LastError != "interrupted pending rebuild" {
		t.Fatalf("pending rebuild state = %#v", state)
	}
	assertRawEquivalent("after pending failure")

	first, err := repo.CatchUp(ctx, 1, baseMS+2*hourMS)
	if err != nil {
		t.Fatalf("run partial rebuild: %v", err)
	}
	if !first.Rebuilt || !first.Pending || first.CoverageEventID != 1 {
		t.Fatalf("partial rebuild result = %#v", first)
	}
	if err := repo.RecordFailure(ctx, errors.New("interrupted active rebuild"), baseMS+2*hourMS+1); err != nil {
		t.Fatalf("record active rebuild failure: %v", err)
	}
	state, err = repo.State(ctx)
	if err != nil {
		t.Fatalf("read active rebuild state: %v", err)
	}
	if state.Status != "backfilling" || state.LastError != "interrupted active rebuild" {
		t.Fatalf("active rebuild state = %#v", state)
	}
	assertRawEquivalent("after active failure")

	completed, err := repo.CatchUp(ctx, 10, baseMS+3*hourMS)
	if err != nil {
		t.Fatalf("resume aggregate rebuild: %v", err)
	}
	if !completed.Rebuilt || completed.Pending || completed.CoverageEventID != 2 {
		t.Fatalf("resumed rebuild result = %#v", completed)
	}
	assertRawEquivalent("after rebuild completion")
}

func TestCatchUpWaitsForExternalClearingAndPreservesFallbackOnFailure(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)
	baseMS := int64(1_800_000_000_000)
	baseMS -= baseMS % hourMS
	if _, err := events.InsertBatch(ctx, []usage.Event{
		aggregateTestEvent("external-clearing-event", baseMS+1_000, "model-a", false, 10, 5, nil),
	}); err != nil {
		t.Fatalf("insert clearing event: %v", err)
	}
	if _, err := db.Exec(`insert into usage_hourly_aggregate_v1 (
		bucket_ms, model, billing_model, service_tier, failed, calls,
		input_tokens, output_tokens, total_tokens, updated_at_ms
	) values (?, 'stale-model', 'stale-model', '', 0, 99, 99, 99, 198, 1)`, baseMS); err != nil {
		t.Fatalf("seed stale aggregate: %v", err)
	}
	if _, err := db.Exec(`update usage_hourly_aggregate_state set
		status = 'clearing', backfill_last_event_id = 0, coverage_event_id = 0,
		target_event_id = 1, processed_events = 0, last_error = null
		where aggregate_name = ?`, AggregateName); err != nil {
		t.Fatalf("mark aggregate clearing: %v", err)
	}

	result, err := repo.CatchUp(ctx, 10, baseMS+hourMS)
	if err != nil {
		t.Fatalf("catch up while external clearing is active: %v", err)
	}
	if result.Processed != 0 || !result.Pending || !result.Rebuilt || result.CoverageEventID != 0 {
		t.Fatalf("external clearing catch-up result = %#v", result)
	}
	var staleCalls int64
	if err := db.QueryRow(`select calls from usage_hourly_aggregate_v1 where model = 'stale-model'`).Scan(&staleCalls); err != nil {
		t.Fatalf("read retained stale aggregate: %v", err)
	}
	if staleCalls != 99 {
		t.Fatalf("stale aggregate calls = %d, want 99 before clearing owner resumes", staleCalls)
	}
	if err := repo.RecordFailure(ctx, errors.New("interrupted external clearing"), baseMS+hourMS+1); err != nil {
		t.Fatalf("record external clearing failure: %v", err)
	}
	state, err := repo.State(ctx)
	if err != nil {
		t.Fatalf("read external clearing state: %v", err)
	}
	if state.Status != "clearing" || state.LastError != "interrupted external clearing" {
		t.Fatalf("external clearing state = %#v", state)
	}
	rows, _, available, err := repo.LoadRows(ctx, Filter{
		FromMS:        baseMS,
		ToMS:          baseMS + hourMS,
		IncludeFailed: true,
	})
	if err != nil || !available || sumCalls(rows) != 1 {
		t.Fatalf("raw fallback during external clearing = available:%v err:%v rows:%#v", available, err, rows)
	}
}

func assertAggregateCallsAndLedgerRevision(db *sql.DB, wantCalls int64, wantRevision string) error {
	var calls int64
	if err := db.QueryRow(`select calls from usage_hourly_aggregate_v1`).Scan(&calls); err != nil {
		return fmt.Errorf("read aggregate calls: %w", err)
	}
	if calls != wantCalls {
		return fmt.Errorf("aggregate calls = %d, want %d", calls, wantCalls)
	}
	for _, eventHash := range []string{"revision-event-1", "revision-event-2"} {
		var revision string
		if err := db.QueryRow(`select aggregate_structure_revision
			from usage_event_identity_ledger where event_hash = ?`, eventHash).Scan(&revision); err != nil {
			return fmt.Errorf("read ledger revision for %s: %w", eventHash, err)
		}
		if revision != wantRevision {
			return fmt.Errorf("ledger aggregate_structure_revision for %s = %q, want %q", eventHash, revision, wantRevision)
		}
	}
	return nil
}

func TestCatchUpSerializesConcurrentCalls(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)
	baseMS := int64(1_800_000_000_000)
	fixtures := make([]usage.Event, 0, 20)
	for index := 0; index < 20; index++ {
		fixtures = append(fixtures, aggregateTestEvent(
			fmt.Sprintf("aggregate-concurrent-%d", index),
			baseMS+int64(index)*1000,
			"model-a",
			false,
			1,
			2,
			nil,
		))
	}
	if _, err := events.InsertBatch(ctx, fixtures); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	start := make(chan struct{})
	errorsByCall := make([]error, 2)
	var wait sync.WaitGroup
	wait.Add(2)
	for index := range errorsByCall {
		go func(index int) {
			defer wait.Done()
			<-start
			_, errorsByCall[index] = repo.CatchUp(ctx, 100, baseMS+hourMS)
		}(index)
	}
	close(start)
	wait.Wait()
	for index, err := range errorsByCall {
		if err != nil {
			t.Fatalf("catch-up %d: %v", index, err)
		}
	}
	var calls int64
	if err := db.QueryRow(`select coalesce(sum(calls), 0) from usage_hourly_aggregate_v1`).Scan(&calls); err != nil {
		t.Fatalf("sum aggregate calls: %v", err)
	}
	if calls != int64(len(fixtures)) {
		t.Fatalf("aggregate calls after concurrent catch-up = %d, want %d", calls, len(fixtures))
	}
}

func TestRawDeltaStatementUsesEventIDScanWhenCoverageIsCurrent(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	query, args := rawRowsStatement(Filter{IncludeFailed: true}, 0, 30*24*hourMS, 1_000_000, "schema-3:model-1", true, true)
	rows, err := db.Query(`explain query plan `+query, args...)
	if err != nil {
		t.Fatalf("explain raw delta query: %v", err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query plan rows: %v", err)
	}
	if !strings.Contains(strings.Join(details, "\n"), "INTEGER PRIMARY KEY (rowid>?)") {
		t.Fatalf("raw delta query plan did not use event ID range: %v", details)
	}
}

func TestCatchUpFailureDoesNotAdvanceCoverage(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)
	baseMS := int64(1_800_000_000_000)
	if _, err := events.InsertBatch(ctx, []usage.Event{
		aggregateTestEvent("aggregate-failure", baseMS, "model-a", false, 1, 2, nil),
	}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := db.Exec(`drop table usage_hourly_aggregate_v1`); err != nil {
		t.Fatalf("drop aggregate table fixture: %v", err)
	}
	if _, err := repo.CatchUp(ctx, 10, baseMS+hourMS); err == nil {
		t.Fatal("catch-up error = nil, want failure")
	}
	state, err := repo.State(ctx)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.BackfillLastEventID != 0 || state.CoverageEventID != 0 {
		t.Fatalf("state advanced after failure: %#v", state)
	}
	var version int
	if err := db.QueryRow(`select aggregate_schema_version from usage_event_identity_ledger where event_hash = 'aggregate-failure'`).Scan(&version); err != nil {
		t.Fatalf("read ledger version: %v", err)
	}
	if version != 0 {
		t.Fatalf("ledger version after failure = %d, want 0", version)
	}
}

func TestUnsupportedSchemaPreservesRowsAndFallsBack(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	repo := New(db)
	if _, err := db.Exec(`update usage_hourly_aggregate_state set schema_version = 99 where aggregate_name = ?`, AggregateName); err != nil {
		t.Fatalf("set future schema: %v", err)
	}
	if _, err := db.Exec(`insert into usage_hourly_aggregate_v1 (
		bucket_ms, model, billing_model, service_tier, failed, calls, updated_at_ms
	) values (0, 'preserved', 'preserved', '', 0, 1, 1)`); err != nil {
		t.Fatalf("insert preserved aggregate row: %v", err)
	}
	if _, _, available, err := repo.LoadRows(ctx, Filter{FromMS: 0, ToMS: 2 * hourMS, IncludeFailed: true}); err != nil || available {
		t.Fatalf("future schema load = available:%v err:%v", available, err)
	}
	if _, err := repo.CatchUp(ctx, 10, 1); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("future schema catch-up error = %v", err)
	}
	var calls int64
	if err := db.QueryRow(`select calls from usage_hourly_aggregate_v1 where model = 'preserved'`).Scan(&calls); err != nil {
		t.Fatalf("read preserved aggregate row: %v", err)
	}
	if calls != 1 {
		t.Fatalf("preserved calls = %d, want 1", calls)
	}
}

func TestCatchUpRollsBackAggregateWhenLedgerCoverageFails(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)
	baseMS := int64(1_800_000_000_000)
	event := aggregateTestEvent("aggregate-ledger-rollback", baseMS, "model-a", false, 1, 2, nil)
	if _, err := events.InsertBatch(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := db.Exec(`create trigger reject_aggregate_ledger_coverage
		before update of aggregate_schema_version on usage_event_identity_ledger
		when new.aggregate_schema_version = 3
		begin select raise(abort, 'blocked'); end`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	if _, err := repo.CatchUp(ctx, 10, baseMS+hourMS); err == nil {
		t.Fatal("catch-up error = nil, want ledger failure")
	}
	var aggregateRows int
	if err := db.QueryRow(`select count(*) from usage_hourly_aggregate_v1`).Scan(&aggregateRows); err != nil {
		t.Fatalf("count aggregate rows: %v", err)
	}
	if aggregateRows != 0 {
		t.Fatalf("aggregate rows survived ledger rollback: %d", aggregateRows)
	}
	state, err := repo.State(ctx)
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	if state.BackfillLastEventID != 0 || state.CoverageEventID != 0 {
		t.Fatalf("state advanced after ledger failure: %#v", state)
	}
	var version int
	if err := db.QueryRow(`select aggregate_schema_version from usage_event_identity_ledger where event_hash = ?`, event.EventHash).Scan(&version); err != nil {
		t.Fatalf("read ledger version: %v", err)
	}
	if version != 0 {
		t.Fatalf("ledger version after rollback = %d, want 0", version)
	}
}

func TestRepositoryRejectsUnknownAggregateSchema(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	repo := New(db)
	if _, err := db.Exec(`update usage_hourly_aggregate_state set schema_version = 99 where aggregate_name = ?`, AggregateName); err != nil {
		t.Fatalf("set future schema: %v", err)
	}
	if _, err := repo.CatchUp(ctx, 10, time.Now().UnixMilli()); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("catch-up error = %v, want ErrUnsupportedSchema", err)
	}
	rows, state, available, err := repo.LoadRows(ctx, Filter{
		FromMS:        int64(1_800_000_000_000),
		ToMS:          int64(1_800_000_000_000) + 2*hourMS,
		IncludeFailed: true,
	})
	if err != nil {
		t.Fatalf("load future schema rows: %v", err)
	}
	if available || state.SchemaVersion != 99 || rows != nil {
		t.Fatalf("future schema load = available:%v state:%#v rows:%#v", available, state, rows)
	}
}

func TestLoadRowsAppliesModelOutcomeAndRawEdgeFilters(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)
	baseMS := int64(1_800_000_000_000)
	baseMS -= baseMS % hourMS
	fromMS := baseMS + 15*60*1000
	toMS := baseMS + 2*hourMS + 20*60*1000
	if _, err := events.InsertBatch(ctx, []usage.Event{
		aggregateTestEvent("edge-a", fromMS+1000, "model-a", false, 1, 1, nil),
		aggregateTestEvent("full-a-failed", baseMS+hourMS+1000, "model-a", true, 2, 2, nil),
		aggregateTestEvent("full-b", baseMS+hourMS+2000, "model-b", false, 3, 3, nil),
		aggregateTestEvent("edge-a-tail", toMS-1000, "model-a", false, 4, 4, nil),
	}); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	if _, err := repo.CatchUp(ctx, 100, toMS+hourMS); err != nil {
		t.Fatalf("catch-up: %v", err)
	}
	rows, _, available, err := repo.LoadRows(ctx, Filter{
		FromMS:        fromMS,
		ToMS:          toMS,
		Models:        []string{"model-a"},
		IncludeFailed: false,
	})
	if err != nil || !available {
		t.Fatalf("load filtered rows: available=%v err=%v", available, err)
	}
	if sumCalls(rows) != 2 {
		t.Fatalf("filtered rows = %#v", rows)
	}
	for _, row := range rows {
		if row.Model != "model-a" || row.Failed {
			t.Fatalf("unexpected filtered row = %#v", row)
		}
	}
}

func TestLoadRowsCanonicalizesReasoningSuffixesAcrossStoredAndRawRows(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	events := usageevent.New(db)
	repo := New(db)
	baseMS := int64(1_800_000_000_000)
	baseMS -= baseMS % hourMS
	low := aggregateTestEvent("canonical-low", baseMS+1_000, "deepseek-v4-flash(low)", false, 1, 2, nil)
	low.ResolvedModel = ""
	max := aggregateTestEvent("canonical-max", baseMS+2_000, "deepseek-v4-flash(max)", false, 3, 4, nil)
	max.ResolvedModel = ""
	unknown := aggregateTestEvent("canonical-unknown", baseMS+3_000, "deepseek-v4-flash(region-us)", false, 5, 6, nil)
	unknown.ResolvedModel = ""
	if _, err := events.InsertBatch(ctx, []usage.Event{low, max, unknown}); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	if _, err := repo.CatchUp(ctx, 1, baseMS+hourMS); err != nil {
		t.Fatalf("partial catch-up: %v", err)
	}
	rows, _, available, err := repo.LoadRows(ctx, Filter{
		FromMS:        baseMS,
		ToMS:          baseMS + hourMS,
		Models:        []string{"deepseek-v4-flash"},
		IncludeFailed: true,
	})
	if err != nil || !available {
		t.Fatalf("load canonical rows: available=%v err=%v", available, err)
	}
	if len(rows) != 1 || rows[0].Model != "deepseek-v4-flash" || rows[0].Calls != 2 {
		t.Fatalf("canonical rows = %#v", rows)
	}
	suffixRows, _, available, err := repo.LoadRows(ctx, Filter{
		FromMS:        baseMS,
		ToMS:          baseMS + hourMS,
		Models:        []string{"deepseek-v4-flash(max)"},
		IncludeFailed: true,
	})
	if err != nil || !available || len(suffixRows) != 1 || suffixRows[0].Calls != 2 {
		t.Fatalf("suffix-filtered rows: available=%v err=%v rows=%#v", available, err, suffixRows)
	}
}

func aggregateTestEvent(hash string, timestampMS int64, model string, failed bool, inputTokens, outputTokens int64, latencyMS *int64) usage.Event {
	return usage.Event{
		EventHash:     hash,
		TimestampMS:   timestampMS,
		Timestamp:     time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Provider:      "openai",
		Model:         model,
		ResolvedModel: model + "-billing",
		ServiceTier:   "priority",
		InputTokens:   inputTokens,
		OutputTokens:  outputTokens,
		TotalTokens:   inputTokens + outputTokens,
		LatencyMS:     latencyMS,
		Failed:        failed,
		CreatedAtMS:   timestampMS + 1,
	}
}

func sumCalls(rows []Row) int64 {
	var total int64
	for _, row := range rows {
		total += row.Calls
	}
	return total
}

func callsFor(rows []Row, bucketMS int64, model string, failed bool) int64 {
	var total int64
	for _, row := range rows {
		if row.BucketMS == bucketMS && row.Model == model && row.Failed == failed {
			total += row.Calls
		}
	}
	return total
}
