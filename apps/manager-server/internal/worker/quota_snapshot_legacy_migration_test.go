package worker

import (
	"bytes"
	"context"
	"database/sql"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	quotasnapshotrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotasnapshot"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestLegacyQuotaSnapshotMigrationWorkerCompletesAndReportsProgress(t *testing.T) {
	rawDB, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	st := store.New(rawDB)
	t.Cleanup(func() { _ = st.Close() })
	seedWorkerLegacySnapshot(t, rawDB, "group-1", 1000)
	seedWorkerLegacySnapshot(t, rawDB, "group-2", 2000)

	logs := captureWorkerLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := NewLegacyQuotaSnapshotMigrationWorker(st)
	w.groupLimit = 1
	w.delay = time.Millisecond
	w.retryDelay = time.Millisecond
	w.Start(ctx)
	w.Start(ctx)
	waitForLegacyMigrationStatus(t, rawDB, "completed")

	var processedRows, pendingSnapshots int
	if err := rawDB.QueryRow(`select processed_rows from usage_data_migrations where name = ?`,
		quotasnapshotrepo.LegacySnapshotMigrationName,
	).Scan(&processedRows); err != nil {
		t.Fatalf("read completed worker state: %v", err)
	}
	if err := rawDB.QueryRow(`select count(*) from account_quota_snapshots where observation_id is null`).Scan(&pendingSnapshots); err != nil {
		t.Fatalf("count pending worker snapshots: %v", err)
	}
	if processedRows != 2 || pendingSnapshots != 0 {
		t.Fatalf("completed worker = processed:%d pending:%d", processedRows, pendingSnapshots)
	}
	for _, fragment := range []string{
		"[quota-snapshot-migration] started",
		"[quota-snapshot-migration] progress processed=2",
		"[quota-snapshot-migration] completed processed=2",
	} {
		if !strings.Contains(logs.String(), fragment) {
			t.Fatalf("worker logs missing %q: %s", fragment, logs.String())
		}
	}
}

func TestLegacyQuotaSnapshotMigrationWorkerPausesOversizedGroupForOfflineCleanup(t *testing.T) {
	rawDB, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	st := store.New(rawDB)
	t.Cleanup(func() { _ = st.Close() })
	seedWorkerLegacySnapshot(t, rawDB, "group-1", 1000)
	seedWorkerLegacySnapshot(t, rawDB, "group-1", 1000)

	logs := captureWorkerLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	w := NewLegacyQuotaSnapshotMigrationWorker(st)
	w.groupLimit = 1
	w.retryDelay = time.Millisecond
	w.Start(ctx)
	waitForLegacyMigrationStatus(t, rawDB, "offline_required")
	time.Sleep(20 * time.Millisecond)
	cancel()

	var lastError string
	var attachedSnapshots int
	if err := rawDB.QueryRow(`select coalesce(last_error, '') from usage_data_migrations where name = ?`,
		quotasnapshotrepo.LegacySnapshotMigrationName,
	).Scan(&lastError); err != nil {
		t.Fatalf("read failed worker state: %v", err)
	}
	if err := rawDB.QueryRow(`select count(*) from account_quota_snapshots where observation_id is not null`).Scan(&attachedSnapshots); err != nil {
		t.Fatalf("count snapshots after failed worker batch: %v", err)
	}
	if !strings.Contains(lastError, "exceeds safe batch limit 1") || attachedSnapshots != 0 {
		t.Fatalf("offline worker state = error:%q attached:%d", lastError, attachedSnapshots)
	}
	if !strings.Contains(logs.String(), "[quota-snapshot-migration] paused; offline cleanup required") {
		t.Fatalf("worker offline cleanup log missing: %s", logs.String())
	}
	if strings.Contains(logs.String(), "will retry") {
		t.Fatalf("oversized group was incorrectly retried: %s", logs.String())
	}
}

func TestLegacyQuotaSnapshotMigrationWorkerIsSilentWhenNoMigrationWorkExists(t *testing.T) {
	rawDB, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	st := store.New(rawDB)
	t.Cleanup(func() { _ = st.Close() })

	logs := captureWorkerLogs(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	w := NewLegacyQuotaSnapshotMigrationWorker(st)
	w.Start(ctx)
	waitForLegacyMigrationStatus(t, rawDB, "completed")
	time.Sleep(20 * time.Millisecond)

	if strings.Contains(logs.String(), "[quota-snapshot-migration]") {
		t.Fatalf("no-op legacy migration emitted logs: %s", logs.String())
	}
}

func seedWorkerLegacySnapshot(t *testing.T, db *sql.DB, sourceObservationID string, observedAtMS int64) {
	t.Helper()
	if _, err := db.Exec(`insert into account_quota_snapshots (
		account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, source, source_observation_id, observed_at_ms,
		boundary_accuracy, duration_seconds, used_percent, remaining_percent,
		created_at_ms
	) values ('account-1', 'codex', ?, 'weekly', 'fixed', 'all',
		'inspection', ?, ?, 'exact', 3600, 25, 75, ?)`,
		"weekly-"+sourceObservationID,
		sourceObservationID,
		observedAtMS,
		observedAtMS,
	); err != nil {
		t.Fatalf("seed worker legacy snapshot: %v", err)
	}
}

func waitForLegacyMigrationStatus(t *testing.T, db *sql.DB, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		if err := db.QueryRow(`select status from usage_data_migrations where name = ?`,
			quotasnapshotrepo.LegacySnapshotMigrationName,
		).Scan(&status); err != nil {
			t.Fatalf("read legacy migration status: %v", err)
		}
		if status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("legacy migration did not reach status %q", want)
}

type lockedLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedLogBuffer) Write(payload []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(payload)
}

func (b *lockedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func (b *lockedLogBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buffer.Reset()
}

func captureWorkerLogs(t *testing.T) *lockedLogBuffer {
	t.Helper()
	logs := &lockedLogBuffer{}
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	log.SetOutput(logs)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	return logs
}
