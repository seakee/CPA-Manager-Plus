package worker

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestUsageCacheAccountingMigrationWorkerRunsBatchesBeforeCompletion(t *testing.T) {
	rawDB, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	st := store.New(rawDB)
	t.Cleanup(func() { _ = st.Close() })
	for _, hash := range []string{"legacy-1", "legacy-2"} {
		if _, err := rawDB.Exec(`insert into usage_events (
			event_hash, timestamp_ms, timestamp, provider, model, input_tokens,
			cached_tokens, created_at_ms
		) values (?, 1, '1', 'openai', 'gpt-5', 100, 10, 1)`, hash); err != nil {
			t.Fatalf("insert %s: %v", hash, err)
		}
	}
	if _, err := rawDB.Exec(`update usage_data_migrations set
		status = 'discovering', last_event_id = 0, target_event_id = 0,
		processed_rows = 0, changed_rows = 0, started_at_ms = null, updated_at_ms = 0,
		finished_at_ms = null, last_error = null
		where name = 'usage_cache_accounting_v2'`); err != nil {
		t.Fatalf("reset migration state: %v", err)
	}

	completed := make(chan struct{}, 1)
	w := NewUsageCacheAccountingMigrationWorker(st, func() { completed <- struct{}{} })
	w.batchSize = 1
	w.delay = time.Millisecond
	w.retryDelay = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w.Start(ctx)

	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("migration completion callback timed out")
	}
	state, err := st.UsageCacheAccountingMigrationState(context.Background())
	if err != nil {
		t.Fatalf("read migration state: %v", err)
	}
	if state.Status != "completed" || state.ProcessedRows != 2 || state.ChangedRows != 2 || state.LastEventID != 2 {
		t.Fatalf("migration state = %#v", state)
	}
	var remaining int
	if err := rawDB.QueryRow(`select count(*) from usage_events
		where normalized_total_input_tokens is null`).Scan(&remaining); err != nil {
		t.Fatalf("count remaining legacy rows: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("remaining legacy rows = %d, want 0", remaining)
	}
}

func TestUsageCacheAccountingMigrationWorkerCompletesOnce(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	completed := make(chan struct{}, 1)
	var calls int32
	w := NewUsageCacheAccountingMigrationWorker(db, func() {
		atomic.AddInt32(&calls, 1)
		completed <- struct{}{}
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w.Start(ctx)
	w.Start(ctx)

	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("migration completion callback timed out")
	}
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("completion callback calls = %d, want 1", got)
	}
}
