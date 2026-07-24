package worker

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestUsageHourlyAggregateWorkerCatchUp(t *testing.T) {
	db := newUsageHourlyAggregateWorkerStore(t)
	ctx := context.Background()
	timestampMS := int64(1_800_000_001_000)
	if _, err := db.InsertEvents(ctx, []usage.Event{usageHourlyAggregateWorkerEvent(
		"usage-aggregate-worker-event",
		timestampMS,
		15,
	)}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	worker := NewUsageHourlyAggregateWorker(db)
	worker.batchLimit = 10
	worker.maxBatches = 2
	if pending := worker.catchUp(ctx); pending {
		t.Fatal("completed catch-up reported pending work")
	}

	rows, state, available, err := db.UsageHourlyAggregateRows(ctx, store.UsageHourlyAggregateFilter{
		FromMS:        timestampMS - timestampMS%hourWindowMS,
		ToMS:          timestampMS - timestampMS%hourWindowMS + hourWindowMS,
		IncludeFailed: true,
	})
	if err != nil {
		t.Fatalf("query aggregate: %v", err)
	}
	if !available || state.Status != "ready" || state.CoverageEventID != 1 {
		t.Fatalf("aggregate state = available:%v state:%#v", available, state)
	}
	if len(rows) != 1 || rows[0].Calls != 1 || rows[0].TotalTokens != 15 {
		t.Fatalf("aggregate rows = %#v", rows)
	}
}

func TestUsageHourlyAggregateWorkerContinuesPendingBacklog(t *testing.T) {
	db := newUsageHourlyAggregateWorkerStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	baseMS := int64(1_800_000_000_000)
	events := make([]usage.Event, 0, 5)
	for index := 0; index < 5; index++ {
		events = append(events, usageHourlyAggregateWorkerEvent(
			fmt.Sprintf("usage-aggregate-worker-backlog-%d", index),
			baseMS+int64(index)*1000,
			int64(index+1),
		))
	}
	if _, err := db.InsertEvents(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	worker := NewUsageHourlyAggregateWorker(db)
	worker.batchLimit = 1
	worker.maxBatches = 1
	worker.checkInterval = time.Hour
	worker.continuationDelay = time.Millisecond
	worker.Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for {
		state, err := db.UsageHourlyAggregateState(ctx)
		if err != nil {
			t.Fatalf("aggregate state: %v", err)
		}
		if state.CoverageEventID == 5 && state.Status == "ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("backlog did not continue: state=%#v", state)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestUsageHourlyAggregateWorkerRecordsFailure(t *testing.T) {
	sqlDB, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db := store.New(sqlDB)
	ctx := context.Background()
	baseMS := int64(1_800_000_000_000)
	if _, err := db.InsertEvents(ctx, []usage.Event{
		usageHourlyAggregateWorkerEvent("usage-aggregate-worker-failure", baseMS, 1),
	}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `drop table usage_hourly_aggregate_v1`); err != nil {
		t.Fatalf("drop aggregate fixture: %v", err)
	}

	worker := NewUsageHourlyAggregateWorker(db)
	worker.catchUp(ctx)
	state, err := db.UsageHourlyAggregateState(ctx)
	if err != nil {
		t.Fatalf("aggregate state: %v", err)
	}
	if state.Status != "failed" || state.LastError == "" {
		t.Fatalf("failure state = %#v", state)
	}
	if ctx.Err() != nil {
		t.Fatalf("worker failure unexpectedly canceled context: %v", ctx.Err())
	}
}

func newUsageHourlyAggregateWorkerStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func usageHourlyAggregateWorkerEvent(hash string, timestampMS, totalTokens int64) usage.Event {
	return usage.Event{
		EventHash:   hash,
		TimestampMS: timestampMS,
		Timestamp:   time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Model:       "gpt-a",
		Endpoint:    "POST /v1/chat/completions",
		Method:      "POST",
		Path:        "/v1/chat/completions",
		TotalTokens: totalTokens,
		CreatedAtMS: timestampMS,
	}
}
