package usageevent

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestInsertBatchUsesIdentityLedgerAfterRawDeletion(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	ctx := context.Background()
	timestampMS := int64(1_800_000_001_000)
	event := usage.Event{
		EventHash:    "identity-ledger-event",
		TimestampMS:  timestampMS,
		Timestamp:    time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Model:        "gpt-test",
		InputTokens:  10,
		OutputTokens: 5,
		TotalTokens:  15,
		CreatedAtMS:  timestampMS + 1,
	}

	first, err := repo.InsertBatch(ctx, []usage.Event{event})
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if first.Inserted != 1 || first.Skipped != 0 {
		t.Fatalf("first result = %#v", first)
	}
	var rawEventID, ledgerTimestampMS, bucketMS int64
	var aggregateVersion int
	if err := db.QueryRow(`select raw_event_id, timestamp_ms, bucket_ms, aggregate_schema_version
		from usage_event_identity_ledger where event_hash = ?`, event.EventHash).Scan(
		&rawEventID,
		&ledgerTimestampMS,
		&bucketMS,
		&aggregateVersion,
	); err != nil {
		t.Fatalf("read identity ledger: %v", err)
	}
	if rawEventID <= 0 || ledgerTimestampMS != timestampMS || bucketMS != timestampMS-timestampMS%3600000 || aggregateVersion != 0 {
		t.Fatalf("ledger row = id:%d timestamp:%d bucket:%d version:%d", rawEventID, ledgerTimestampMS, bucketMS, aggregateVersion)
	}

	if _, err := db.Exec(`delete from usage_events where event_hash = ?`, event.EventHash); err != nil {
		t.Fatalf("delete raw event fixture: %v", err)
	}
	second, err := repo.InsertBatch(ctx, []usage.Event{event})
	if err != nil {
		t.Fatalf("second insert: %v", err)
	}
	if second.Inserted != 0 || second.Skipped != 1 {
		t.Fatalf("second result = %#v", second)
	}
	var rawCount, ledgerCount int
	if err := db.QueryRow(`select count(*) from usage_events where event_hash = ?`, event.EventHash).Scan(&rawCount); err != nil {
		t.Fatalf("count raw events: %v", err)
	}
	if err := db.QueryRow(`select count(*) from usage_event_identity_ledger where event_hash = ?`, event.EventHash).Scan(&ledgerCount); err != nil {
		t.Fatalf("count ledger events: %v", err)
	}
	if rawCount != 0 || ledgerCount != 1 {
		t.Fatalf("counts after duplicate import = raw:%d ledger:%d", rawCount, ledgerCount)
	}
}

func TestInsertBatchBackfillsLedgerForLegacyDuplicate(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	ctx := context.Background()
	timestampMS := int64(1_800_000_002_000)
	originalCreatedAtMS := timestampMS - 10_000
	event := usage.Event{
		EventHash:   "legacy-ledger-event",
		TimestampMS: timestampMS,
		Timestamp:   time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Model:       "gpt-test",
		CreatedAtMS: timestampMS + 1,
	}
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, created_at_ms
	) values (?, ?, ?, ?, ?)`, event.EventHash, event.TimestampMS, event.Timestamp, event.Model, originalCreatedAtMS); err != nil {
		t.Fatalf("insert legacy raw event: %v", err)
	}

	result, err := repo.InsertBatch(ctx, []usage.Event{event})
	if err != nil {
		t.Fatalf("insert legacy duplicate: %v", err)
	}
	if result.Inserted != 0 || result.Skipped != 1 {
		t.Fatalf("legacy duplicate result = %#v", result)
	}
	var rawEventID, firstSeenAtMS int64
	if err := db.QueryRow(`select raw_event_id, first_seen_at_ms from usage_event_identity_ledger where event_hash = ?`, event.EventHash).Scan(&rawEventID, &firstSeenAtMS); err != nil {
		t.Fatalf("read legacy ledger row: %v", err)
	}
	if rawEventID <= 0 {
		t.Fatalf("legacy raw event ID = %d", rawEventID)
	}
	if firstSeenAtMS != originalCreatedAtMS {
		t.Fatalf("legacy first seen = %d, want %d", firstSeenAtMS, originalCreatedAtMS)
	}
}

func TestInsertBatchRollsBackIdentityClaimWhenRawInsertFails(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	ctx := context.Background()
	timestampMS := int64(1_800_000_003_000)
	event := usage.Event{
		EventHash:   "identity-ledger-rollback",
		TimestampMS: timestampMS,
		Timestamp:   time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Model:       "gpt-test",
		CreatedAtMS: timestampMS + 1,
	}
	if _, err := db.Exec(`create trigger reject_usage_event_insert before insert on usage_events
		begin select raise(abort, 'blocked'); end`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	if _, err := repo.InsertBatch(ctx, []usage.Event{event}); err == nil {
		t.Fatal("insert error = nil, want trigger failure")
	}
	var ledgerCount int
	if err := db.QueryRow(`select count(*) from usage_event_identity_ledger where event_hash = ?`, event.EventHash).Scan(&ledgerCount); err != nil {
		t.Fatalf("count rolled back ledger claim: %v", err)
	}
	if ledgerCount != 0 {
		t.Fatalf("ledger claim survived raw insert rollback: count=%d", ledgerCount)
	}
	if _, err := db.Exec(`drop trigger reject_usage_event_insert`); err != nil {
		t.Fatalf("drop failure trigger: %v", err)
	}
	result, err := repo.InsertBatch(ctx, []usage.Event{event})
	if err != nil {
		t.Fatalf("retry insert: %v", err)
	}
	if result.Inserted != 1 || result.Skipped != 0 {
		t.Fatalf("retry result = %#v", result)
	}
}
