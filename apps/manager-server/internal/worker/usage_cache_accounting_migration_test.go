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

func TestUsageCacheAccountingMigrationWorkerRediscoverSemanticsRevision(t *testing.T) {
	rawDB, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	st := store.New(rawDB)
	t.Cleanup(func() { _ = st.Close() })

	// 插入 Devin #838 历史错误数据
	if _, err := rawDB.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, executor_type, model, input_tokens,
		cached_tokens, cache_read_tokens, cache_creation_tokens, total_tokens,
		cache_input_mode, normalized_uncached_input_tokens, normalized_total_input_tokens,
		raw_json, created_at_ms
	) values ('devin-838', 1, '1', 'devin', 'DevinExecutor', 'claude-fable-5-1', 229788,
		228021, 228021, 0, 231563,
		'separate_from_input', 229788, 457809,
		'{"tokens":{"input_tokens":229788,"output_tokens":1775,"cached_tokens":228021,"cache_read_tokens":228021,"cache_creation_tokens":0,"total_tokens":231563}}', 1)`); err != nil {
		t.Fatalf("insert devin-838 event: %v", err)
	}

	// 模拟已处于 completed 状态，但 revision 为空
	if _, err := rawDB.Exec(`update usage_data_migrations set
		status = 'completed', last_event_id = 1, target_event_id = 1,
		processed_rows = 1, changed_rows = 0, applied_rows = 0,
		started_at_ms = 1, updated_at_ms = 1, finished_at_ms = 1, last_error = null
	where name = 'usage_cache_accounting_v2'`); err != nil {
		t.Fatalf("set completed migration state: %v", err)
	}

	completed := make(chan struct{}, 1)
	w := NewUsageCacheAccountingMigrationWorker(st, func() { completed <- struct{}{} })
	w.batchSize = 10
	w.delay = time.Millisecond
	w.retryDelay = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w.Start(ctx)

	select {
	case <-completed:
	case <-time.After(3 * time.Second):
		t.Fatal("worker completion callback timed out")
	}

	state, err := st.UsageCacheAccountingMigrationState(context.Background())
	if err != nil {
		t.Fatalf("read migration state: %v", err)
	}
	if state.Status != "completed" {
		t.Fatalf("state.Status = %q, want completed", state.Status)
	}

	var mode string
	var uncached, totalInput int64
	if err := rawDB.QueryRow(`select cache_input_mode, normalized_uncached_input_tokens, normalized_total_input_tokens from usage_events where event_hash = 'devin-838'`).Scan(&mode, &uncached, &totalInput); err != nil {
		t.Fatalf("read devin-838 accounting: %v", err)
	}
	if mode != "read_included_creation_separate" || uncached != 1767 || totalInput != 229788 {
		t.Fatalf("devin-838 accounting = (%s, %d, %d), want (read_included_creation_separate, 1767, 229788)", mode, uncached, totalInput)
	}

	var revision string
	if err := rawDB.QueryRow(`select value from settings where key = 'usage_cache_accounting_semantics_revision'`).Scan(&revision); err != nil {
		t.Fatalf("read revision: %v", err)
	}
	if revision != "2" {
		t.Fatalf("revision = %q, want 2", revision)
	}
}
