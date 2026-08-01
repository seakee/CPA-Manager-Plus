package worker

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestDashboardHourlyRollupWorkerCatchUp(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	timestampMS := int64(1_800_000_001_000)
	if _, err := db.InsertEvents(ctx, []usage.Event{{
		EventHash:    "dashboard-worker-event",
		TimestampMS:  timestampMS,
		Timestamp:    time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Model:        "gpt-a",
		Endpoint:     "POST /v1/chat/completions",
		Method:       "POST",
		Path:         "/v1/chat/completions",
		InputTokens:  10,
		OutputTokens: 5,
		TotalTokens:  15,
		CreatedAtMS:  timestampMS,
	}}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	worker := NewDashboardHourlyRollupWorker(db)
	worker.batchLimit = 10
	worker.maxBatches = 2
	worker.catchUp(ctx)

	rows, err := db.DashboardHourlyRollupRows(ctx, timestampMS-timestampMS%hourWindowMS, timestampMS-timestampMS%hourWindowMS+hourWindowMS)
	if err != nil {
		t.Fatalf("query rollup: %v", err)
	}
	if len(rows) != 1 || rows[0].Calls != 1 || rows[0].TotalTokens != 15 {
		t.Fatalf("rollup rows = %#v", rows)
	}
}

func TestDashboardHourlyRollupWorkerContinuesPendingBacklog(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	baseMS := int64(1_800_000_000_000)
	events := make([]usage.Event, 0, 5)
	for index := 0; index < 5; index++ {
		timestampMS := baseMS + int64(index)*1000
		events = append(events, usage.Event{
			EventHash:   fmt.Sprintf("dashboard-worker-backlog-%d", index),
			TimestampMS: timestampMS,
			Timestamp:   time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
			Model:       "gpt-a",
			Endpoint:    "POST /v1/chat/completions",
			Method:      "POST",
			Path:        "/v1/chat/completions",
			TotalTokens: int64(index + 1),
			CreatedAtMS: timestampMS,
		})
	}
	if _, err := db.InsertEvents(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	worker := NewDashboardHourlyRollupWorker(db)
	worker.batchLimit = 1
	worker.maxBatches = 1
	worker.checkInterval = time.Hour
	worker.continuationDelay = time.Millisecond
	worker.Start(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for {
		checkpoint, err := db.DashboardHourlyRollupCheckpoint(ctx)
		if err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		if checkpoint.LastEventID == 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("backlog did not continue: checkpoint=%#v", checkpoint)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

const hourWindowMS int64 = 60 * 60 * 1000
