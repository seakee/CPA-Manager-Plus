package codexinspection

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestAcquireRunUsesSingleGlobalLease(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repositories := []Repository{New(db), New(db)}
	start := make(chan struct{})
	errorsByIndex := make([]error, len(repositories))
	results := make([]AcquireRunResult, len(repositories))
	var wg sync.WaitGroup
	for index := range repositories {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errorsByIndex[index] = repositories[index].AcquireRun(
				context.Background(),
				newLifecycleTestRun(model.CodexInspectionTriggerManual, "manual"),
				"owner-"+string(rune('a'+index)),
				time.Minute,
			)
		}(index)
	}
	close(start)
	wg.Wait()

	successes := 0
	alreadyActive := 0
	for index, err := range errorsByIndex {
		switch {
		case err == nil:
			successes++
			if results[index].Run.ID <= 0 {
				t.Fatalf("acquired run = %#v", results[index].Run)
			}
		case errors.Is(err, ErrLeaseAlreadyActive):
			alreadyActive++
		default:
			t.Fatalf("acquire error %d = %v", index, err)
		}
	}
	if successes != 1 || alreadyActive != 1 {
		t.Fatalf("acquire outcomes success=%d active=%d errors=%v", successes, alreadyActive, errorsByIndex)
	}
	runs, err := repositories[0].ListRuns(context.Background(), 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %#v, want one", runs)
	}
}

func TestHistoricalRunWritesCannotBypassActiveLeaseLifecycle(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)

	if _, err := repository.CreateRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "active-bypass")); !errors.Is(err, ErrActiveRunRequiresLease) {
		t.Fatalf("create active run error = %v, want ErrActiveRunRequiresLease", err)
	}

	acquired, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "leased"), "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire run: %v", err)
	}
	activeUpdate := acquired.Run
	activeUpdate.Status = model.CodexInspectionStatusCompleted
	activeUpdate.FinishedAtMS = time.Now().UnixMilli()
	if err := repository.UpdateRun(context.Background(), activeUpdate); !errors.Is(err, ErrRunStateConflict) {
		t.Fatalf("update leased run error = %v, want ErrRunStateConflict", err)
	}

	terminalInput := newLifecycleTestRun(model.CodexInspectionTriggerManual, "terminal")
	terminalInput.Status = model.CodexInspectionStatusCompleted
	terminalInput.FinishedAtMS = time.Now().UnixMilli()
	terminal, err := repository.CreateRun(context.Background(), terminalInput)
	if err != nil {
		t.Fatalf("create terminal run: %v", err)
	}
	terminal.Status = model.CodexInspectionStatusFailed
	if err := repository.UpdateRun(context.Background(), terminal); !errors.Is(err, ErrRunStateConflict) {
		t.Fatalf("transition terminal run error = %v, want ErrRunStateConflict", err)
	}
}

func TestAcquireRunReclaimsExpiredLeaseAndInterruptsOldRun(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	first, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "manual"), "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire first run: %v", err)
	}
	if _, err := db.Exec(`update codex_inspection_leases set heartbeat_at_ms = 0, lease_expires_at_ms = 0 where id = 1`); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	second, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "manual"), "owner-b", time.Minute)
	if err != nil {
		t.Fatalf("acquire replacement run: %v", err)
	}
	if second.RecoveredRun == nil || second.RecoveredRun.ID != first.Run.ID || second.RecoveredRun.Status != model.CodexInspectionStatusInterrupted {
		t.Fatalf("recovered run = %#v, first=%#v", second.RecoveredRun, first.Run)
	}
	oldRun, found, err := repository.GetRun(context.Background(), first.Run.ID)
	if err != nil || !found {
		t.Fatalf("get old run found=%v err=%v", found, err)
	}
	if oldRun.Status != model.CodexInspectionStatusInterrupted || oldRun.FinishedAtMS == 0 {
		t.Fatalf("old run = %#v", oldRun)
	}
	logs, err := repository.ListLogs(context.Background(), first.Run.ID)
	if err != nil || len(logs) == 0 {
		t.Fatalf("old run logs = %#v err=%v", logs, err)
	}
}

func TestAcquireRunDoesNotStrandReplacementWhenRecoveryLogFails(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	first, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "first"), "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire first run: %v", err)
	}
	if _, err := db.Exec(`update codex_inspection_leases set heartbeat_at_ms = 0, lease_expires_at_ms = 0 where id = 1`); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	if _, err := db.Exec(`create trigger fail_codex_inspection_recovery_log before insert on codex_inspection_logs begin select raise(abort, 'forced recovery log failure'); end`); err != nil {
		t.Fatalf("create log failure trigger: %v", err)
	}

	second, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "second"), "owner-b", time.Minute)
	if err != nil {
		t.Fatalf("acquire replacement despite recovery log failure: %v", err)
	}
	if second.RecoveredRun == nil || second.RecoveredRun.ID != first.Run.ID {
		t.Fatalf("recovered run = %#v, want %d", second.RecoveredRun, first.Run.ID)
	}
	oldRun, found, err := repository.GetRun(context.Background(), first.Run.ID)
	if err != nil || !found || oldRun.Status != model.CodexInspectionStatusInterrupted || oldRun.FinishedAtMS == 0 {
		t.Fatalf("old run = %#v found=%v err=%v", oldRun, found, err)
	}
	lease, active, err := repository.GetActiveLease(context.Background(), time.Now().UnixMilli())
	if err != nil || !active || lease.RunID != second.Run.ID || lease.OwnerID != "owner-b" {
		t.Fatalf("replacement lease = %#v active=%v err=%v", lease, active, err)
	}
}

func TestFinalizeRunReleasesLease(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	acquired, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "manual"), "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire run: %v", err)
	}
	run := acquired.Run
	run.Status = model.CodexInspectionStatusCompleted
	run.FinishedAtMS = time.Now().UnixMilli()
	if err := repository.FinalizeRun(context.Background(), run, "owner-a", &model.CodexInspectionLog{RunID: run.ID, Level: "success", Message: "finished"}); err != nil {
		t.Fatalf("finalize run: %v", err)
	}
	if lease, active, err := repository.GetActiveLease(context.Background(), time.Now().UnixMilli()); err != nil || active {
		t.Fatalf("active lease = %#v active=%v err=%v", lease, active, err)
	}
	stored, found, err := repository.GetRun(context.Background(), run.ID)
	if err != nil || !found || stored.Status != model.CodexInspectionStatusCompleted {
		t.Fatalf("stored run = %#v found=%v err=%v", stored, found, err)
	}
}

func TestFinalizeRunDoesNotOverwriteCommittedCancellation(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	acquired, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "stale-finalizer"), "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire run: %v", err)
	}
	if changed, err := repository.MarkRunCancelling(context.Background(), acquired.Run.ID, "owner-a", "cancel requested"); err != nil || !changed {
		t.Fatalf("mark cancelling changed=%v err=%v", changed, err)
	}
	// Deliberately finalize with the worker's stale running/completed snapshot.
	stale := acquired.Run
	stale.Status = model.CodexInspectionStatusCompleted
	stale.KeepCount = 99
	stale.FinishedAtMS = time.Now().UnixMilli()
	if err := repository.FinalizeRun(context.Background(), stale, "owner-a", &model.CodexInspectionLog{
		RunID:   acquired.Run.ID,
		Level:   "success",
		Message: "凭证健康巡检完成",
		Detail: map[string]any{
			"status": model.CodexInspectionStatusCompleted,
			"reason": "none",
		},
	}); err != nil {
		t.Fatalf("finalize stale snapshot: %v", err)
	}
	stored, found, err := repository.GetRun(context.Background(), acquired.Run.ID)
	if err != nil || !found {
		t.Fatalf("get finalized run found=%v err=%v", found, err)
	}
	if stored.Status != model.CodexInspectionStatusCancelled {
		t.Fatalf("stored status = %q, want cancelled", stored.Status)
	}
	if stored.Error != "cancel requested" {
		t.Fatalf("stored cancellation error = %q, want original reason", stored.Error)
	}
	if stored.KeepCount != 99 {
		t.Fatalf("stored keep count = %d, want stale snapshot value", stored.KeepCount)
	}
	logs, err := repository.ListLogs(context.Background(), acquired.Run.ID)
	if err != nil || len(logs) != 1 {
		t.Fatalf("final lifecycle logs = %#v err=%v", logs, err)
	}
	if logs[0].Message != "凭证健康巡检已取消" || logs[0].Level != "warning" {
		t.Fatalf("normalized final lifecycle log = %#v", logs[0])
	}
	detail, ok := logs[0].Detail.(map[string]any)
	if !ok || detail["status"] != model.CodexInspectionStatusCancelled || detail["reason"] != "user_cancel" || detail["error"] != "cancel requested" {
		t.Fatalf("normalized final lifecycle detail = %#v", logs[0].Detail)
	}
	if _, active, err := repository.GetActiveLease(context.Background(), time.Now().UnixMilli()); err != nil || active {
		t.Fatalf("lease after cancellation finalization active=%v err=%v", active, err)
	}
}

func TestForceFinalizeRunDoesNotOverwriteCommittedCancellation(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	acquired, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "stale-force-finalizer"), "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire run: %v", err)
	}
	if changed, err := repository.MarkRunCancelling(context.Background(), acquired.Run.ID, "owner-a", "cancel requested"); err != nil || !changed {
		t.Fatalf("mark cancelling changed=%v err=%v", changed, err)
	}
	stale := acquired.Run
	stale.Status = model.CodexInspectionStatusFailed
	stale.Error = "stale worker error"
	stale.FinishedAtMS = time.Now().UnixMilli()
	if err := repository.ForceFinalizeRun(context.Background(), stale, "owner-a", nil); err != nil {
		t.Fatalf("force finalize stale snapshot: %v", err)
	}
	stored, found, err := repository.GetRun(context.Background(), acquired.Run.ID)
	if err != nil || !found || stored.Status != model.CodexInspectionStatusCancelled || stored.Error != "cancel requested" {
		t.Fatalf("stored forced cancellation = %#v found=%v err=%v", stored, found, err)
	}
}

func TestFinalizeRunRejectsExpiredLease(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	acquired, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "manual"), "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire run: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `update codex_inspection_leases set lease_expires_at_ms = 0 where id = 1`); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	run := acquired.Run
	run.Status = model.CodexInspectionStatusCompleted
	run.FinishedAtMS = time.Now().UnixMilli()
	if err := repository.FinalizeRun(context.Background(), run, "owner-a", nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("finalize expired lease error = %v, want ErrLeaseLost", err)
	}
	stored, found, err := repository.GetRun(context.Background(), run.ID)
	if err != nil || !found || stored.Status != model.CodexInspectionStatusRunning {
		t.Fatalf("run after expired finalize = %#v found=%v err=%v", stored, found, err)
	}
}

func TestFinalizeRunRejectsNonTerminalStatus(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	acquired, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "invalid-terminal"), "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire run: %v", err)
	}
	run := acquired.Run
	run.Status = model.CodexInspectionStatusRunning
	if err := repository.FinalizeRun(context.Background(), run, "owner-a", nil); !errors.Is(err, ErrInvalidFinalStatus) {
		t.Fatalf("finalize running status error = %v, want ErrInvalidFinalStatus", err)
	}
	stored, found, err := repository.GetRun(context.Background(), run.ID)
	if err != nil || !found || stored.Status != model.CodexInspectionStatusRunning {
		t.Fatalf("run after invalid finalization = %#v found=%v err=%v", stored, found, err)
	}
}

func TestForceFinalizeRunRecoversExpiredLease(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	acquired, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "manual"), "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire run: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `update codex_inspection_leases set lease_expires_at_ms = 0 where id = 1`); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	run := acquired.Run
	run.Status = model.CodexInspectionStatusInterrupted
	run.Error = "租约丢失"
	run.FinishedAtMS = time.Now().UnixMilli()
	if err := repository.ForceFinalizeRun(context.Background(), run, "owner-a", &model.CodexInspectionLog{
		RunID:   run.ID,
		Level:   "warning",
		Message: "interrupted",
	}); err != nil {
		t.Fatalf("force finalize expired lease: %v", err)
	}
	stored, found, err := repository.GetRun(context.Background(), run.ID)
	if err != nil || !found || stored.Status != model.CodexInspectionStatusInterrupted || stored.FinishedAtMS == 0 {
		t.Fatalf("stored run = %#v found=%v err=%v", stored, found, err)
	}
	if _, active, err := repository.GetActiveLease(context.Background(), time.Now().UnixMilli()); err != nil || active {
		t.Fatalf("lease after force finalize active=%v err=%v", active, err)
	}
}

func TestForceFinalizeRunRequiresTheOriginalLease(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	acquired, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "force-fence"), "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire run: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `delete from codex_inspection_leases where id = 1`); err != nil {
		t.Fatalf("delete lease: %v", err)
	}
	run := acquired.Run
	run.Status = model.CodexInspectionStatusCompleted
	run.FinishedAtMS = time.Now().UnixMilli()
	if err := repository.ForceFinalizeRun(context.Background(), run, "owner-b", nil); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("force finalize without original lease error = %v, want ErrLeaseLost", err)
	}
	stored, found, err := repository.GetRun(context.Background(), run.ID)
	if err != nil || !found || stored.Status != model.CodexInspectionStatusRunning {
		t.Fatalf("run after rejected force finalize = %#v found=%v err=%v", stored, found, err)
	}
}

func TestForceFinalizeRunDoesNotLetLogFailureStrandState(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	acquired, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "manual"), "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire run: %v", err)
	}
	run := acquired.Run
	run.Status = model.CodexInspectionStatusFailed
	run.Error = "probe failed"
	run.FinishedAtMS = time.Now().UnixMilli()
	if err := repository.ForceFinalizeRun(context.Background(), run, "owner-a", &model.CodexInspectionLog{
		RunID:   run.ID + 1000,
		Level:   "error",
		Message: "invalid log",
	}); err != nil {
		t.Fatalf("force finalize with bad log: %v", err)
	}
	stored, found, err := repository.GetRun(context.Background(), run.ID)
	if err != nil || !found || stored.Status != model.CodexInspectionStatusFailed {
		t.Fatalf("stored run = %#v found=%v err=%v", stored, found, err)
	}
	if _, active, err := repository.GetActiveLease(context.Background(), time.Now().UnixMilli()); err != nil || active {
		t.Fatalf("lease after bad log fallback active=%v err=%v", active, err)
	}
}

func TestFinalizeRunRollsBackWhenFinalLogFails(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	acquired, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "manual"), "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire run: %v", err)
	}
	run := acquired.Run
	run.Status = model.CodexInspectionStatusCompleted
	run.FinishedAtMS = time.Now().UnixMilli()
	err = repository.FinalizeRun(context.Background(), run, "owner-a", &model.CodexInspectionLog{
		RunID:   run.ID + 1000,
		Level:   "success",
		Message: "invalid foreign key",
	})
	if err == nil {
		t.Fatal("finalize with invalid log run id error = nil")
	}
	if _, active, leaseErr := repository.GetActiveLease(context.Background(), time.Now().UnixMilli()); leaseErr != nil || !active {
		t.Fatalf("lease after rolled back finalization active=%v err=%v", active, leaseErr)
	}
	stored, found, getErr := repository.GetRun(context.Background(), run.ID)
	if getErr != nil || !found || stored.Status != model.CodexInspectionStatusRunning {
		t.Fatalf("run after rolled back finalization = %#v found=%v err=%v", stored, found, getErr)
	}
}

func TestFinalizeRunRetriesRealSQLiteWriteLock(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	locker, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open locker sqlite: %v", err)
	}
	t.Cleanup(func() { _ = locker.Close() })
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(context.Background(), `pragma busy_timeout = 1`); err != nil {
		t.Fatalf("set short busy timeout: %v", err)
	}
	repository := New(db)
	acquired, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "manual"), "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire run: %v", err)
	}
	lockConn, err := locker.Conn(context.Background())
	if err != nil {
		t.Fatalf("open lock connection: %v", err)
	}
	defer lockConn.Close()
	if _, err := lockConn.ExecContext(context.Background(), `begin immediate`); err != nil {
		t.Fatalf("begin write lock: %v", err)
	}
	releaseErr := make(chan error, 1)
	go func() {
		time.Sleep(40 * time.Millisecond)
		_, err := lockConn.ExecContext(context.Background(), `commit`)
		releaseErr <- err
	}()
	run := acquired.Run
	run.Status = model.CodexInspectionStatusCompleted
	run.FinishedAtMS = time.Now().UnixMilli()
	finalizeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := repository.FinalizeRun(finalizeCtx, run, "owner-a", &model.CodexInspectionLog{RunID: run.ID, Level: "success", Message: "finished"}); err != nil {
		t.Fatalf("finalize after transient write lock: %v", err)
	}
	if err := <-releaseErr; err != nil {
		t.Fatalf("release write lock: %v", err)
	}
	stored, found, err := repository.GetRun(context.Background(), run.ID)
	if err != nil || !found || stored.Status != model.CodexInspectionStatusCompleted {
		t.Fatalf("stored run after lock retry = %#v found=%v err=%v", stored, found, err)
	}
	if _, active, err := repository.GetActiveLease(context.Background(), time.Now().UnixMilli()); err != nil || active {
		t.Fatalf("lease after lock retry active=%v err=%v", active, err)
	}
}

func TestMarkRunCancellingRejectsExpiredLease(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	acquired, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "manual"), "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire run: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `update codex_inspection_leases set lease_expires_at_ms = 0 where id = 1`); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	if _, err := repository.MarkRunCancelling(context.Background(), acquired.Run.ID, "owner-a", "cancel"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("mark cancelling error = %v, want ErrLeaseLost", err)
	}
}

func TestHeartbeatRunCannotResurrectInterruptedRun(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	acquired, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "heartbeat-fence"), "owner-a", time.Minute)
	if err != nil {
		t.Fatalf("acquire run: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `update codex_inspection_runs set status = ?, finished_at_ms = ? where id = ?`, model.CodexInspectionStatusInterrupted, time.Now().UnixMilli(), acquired.Run.ID); err != nil {
		t.Fatalf("interrupt run: %v", err)
	}
	if err := repository.HeartbeatRun(context.Background(), acquired.Run.ID, "owner-a", time.Minute); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("heartbeat interrupted run error = %v, want ErrLeaseLost", err)
	}
	var leaseRunID int64
	if err := db.QueryRowContext(context.Background(), `select run_id from codex_inspection_leases where id = 1`).Scan(&leaseRunID); err != nil {
		t.Fatalf("read lease after rejected heartbeat: %v", err)
	}
	if leaseRunID != acquired.Run.ID {
		t.Fatalf("lease run id = %d, want %d", leaseRunID, acquired.Run.ID)
	}
}

func TestHeartbeatTimestampIsTakenAfterSQLiteWriterWait(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "heartbeat-writer-wait.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	const leaseDuration = 2 * time.Second
	acquired, err := repo.AcquireRun(
		context.Background(),
		newLifecycleTestRun(model.CodexInspectionTriggerManual, "heartbeat-writer-wait"),
		"owner-a",
		leaseDuration,
	)
	if err != nil {
		t.Fatalf("acquire run: %v", err)
	}

	locker, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("open lock connection: %v", err)
	}
	t.Cleanup(func() { _ = locker.Close() })
	if _, err := locker.ExecContext(context.Background(), `begin immediate`); err != nil {
		t.Fatalf("begin writer lock: %v", err)
	}

	heartbeatDone := make(chan error, 1)
	heartbeatCtx, cancelHeartbeat := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelHeartbeat()
	go func() {
		heartbeatDone <- repo.HeartbeatRun(heartbeatCtx, acquired.Run.ID, "owner-a", leaseDuration)
	}()
	time.Sleep(400 * time.Millisecond)
	releasedAt := time.Now()
	if _, err := locker.ExecContext(context.Background(), `rollback`); err != nil {
		t.Fatalf("release writer lock: %v", err)
	}
	if err := <-heartbeatDone; err != nil {
		t.Fatalf("heartbeat after writer wait: %v", err)
	}

	lease, active, err := repo.GetActiveLease(context.Background(), time.Now().UnixMilli())
	if err != nil || !active {
		t.Fatalf("active lease = %#v active=%v err=%v", lease, active, err)
	}
	if heartbeatAt := time.UnixMilli(lease.HeartbeatAtMS); heartbeatAt.Before(releasedAt.Add(-200 * time.Millisecond)) {
		t.Fatalf("heartbeat timestamp = %s, want timestamp after writer wait near %s", heartbeatAt, releasedAt)
	}
	if remaining := time.Until(time.UnixMilli(lease.LeaseExpiresAtMS)); remaining < leaseDuration-300*time.Millisecond {
		t.Fatalf("renewed lease remaining = %s, want nearly %s", remaining, leaseDuration)
	}
}

func TestAcquireTimestampIsTakenAfterSQLiteWriterWait(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "acquire-writer-wait.sqlite")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	lockerDB, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open lock sqlite: %v", err)
	}
	t.Cleanup(func() { _ = lockerDB.Close() })

	locker, err := lockerDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("open lock connection: %v", err)
	}
	t.Cleanup(func() { _ = locker.Close() })
	if _, err := locker.ExecContext(context.Background(), `begin immediate`); err != nil {
		t.Fatalf("begin writer lock: %v", err)
	}

	const leaseDuration = 2 * time.Second
	type acquireOutcome struct {
		result AcquireRunResult
		err    error
	}
	acquireDone := make(chan acquireOutcome, 1)
	acquireCtx, cancelAcquire := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelAcquire()
	go func() {
		result, err := New(db).AcquireRun(
			acquireCtx,
			newLifecycleTestRun(model.CodexInspectionTriggerManual, "acquire-writer-wait"),
			"owner-a",
			leaseDuration,
		)
		acquireDone <- acquireOutcome{result: result, err: err}
	}()
	// AcquireRun uses a deferred transaction, so SQLite can return BUSY quickly
	// while it upgrades the read snapshot to a writer. Keep the real lock within
	// the repository's finite retry window while still proving the successful
	// claim uses a post-conflict timestamp.
	time.Sleep(100 * time.Millisecond)
	releasedAt := time.Now()
	if _, err := locker.ExecContext(context.Background(), `rollback`); err != nil {
		t.Fatalf("release writer lock: %v", err)
	}
	outcome := <-acquireDone
	if outcome.err != nil {
		t.Fatalf("acquire after writer wait: %v", outcome.err)
	}
	if startedAt := time.UnixMilli(outcome.result.Run.UpdatedAtMS); startedAt.Before(releasedAt.Add(-200 * time.Millisecond)) {
		t.Fatalf("run claim timestamp = %s, want timestamp after writer wait near %s", startedAt, releasedAt)
	}
	lease, active, err := New(db).GetActiveLease(context.Background(), time.Now().UnixMilli())
	if err != nil || !active {
		t.Fatalf("active lease = %#v active=%v err=%v", lease, active, err)
	}
	if remaining := time.Until(time.UnixMilli(lease.LeaseExpiresAtMS)); remaining < leaseDuration-300*time.Millisecond {
		t.Fatalf("acquired lease remaining = %s, want nearly %s", remaining, leaseDuration)
	}
}

func TestUpdateRunProgressPreservesCancellingAndRejectsReclaimedLease(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	acquired, err := repository.AcquireRun(
		context.Background(),
		newLifecycleTestRun(model.CodexInspectionTriggerManual, "manual"),
		"owner-a",
		time.Minute,
	)
	if err != nil {
		t.Fatalf("acquire run: %v", err)
	}
	if changed, err := repository.MarkRunCancelling(context.Background(), acquired.Run.ID, "owner-a", "cancel requested"); err != nil || !changed {
		t.Fatalf("mark cancelling changed=%v err=%v", changed, err)
	}
	progress := acquired.Run
	progress.TotalFiles = 3
	progress.SampledCount = 2
	if err := repository.UpdateRunProgress(context.Background(), progress, "owner-a"); err != nil {
		t.Fatalf("update cancelling progress: %v", err)
	}
	stored, found, err := repository.GetRun(context.Background(), acquired.Run.ID)
	if err != nil || !found || stored.Status != model.CodexInspectionStatusCancelling || stored.TotalFiles != 3 {
		t.Fatalf("stored cancelling progress = %#v found=%v err=%v", stored, found, err)
	}

	if _, err := db.ExecContext(context.Background(), `update codex_inspection_leases set lease_expires_at_ms = 0 where id = 1`); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	if _, err := repository.AcquireRun(
		context.Background(),
		newLifecycleTestRun(model.CodexInspectionTriggerManual, "replacement"),
		"owner-b",
		time.Minute,
	); err != nil {
		t.Fatalf("acquire replacement: %v", err)
	}
	progress.TotalFiles = 9
	if err := repository.UpdateRunProgress(context.Background(), progress, "owner-a"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale progress error = %v, want ErrLeaseLost", err)
	}
	stored, found, err = repository.GetRun(context.Background(), acquired.Run.ID)
	if err != nil || !found || stored.Status != model.CodexInspectionStatusInterrupted || stored.TotalFiles != 3 {
		t.Fatalf("reclaimed run after stale progress = %#v found=%v err=%v", stored, found, err)
	}
}

func TestGetActiveLeaseIgnoresUnboundLeaseRow(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(
		context.Background(),
		`insert into codex_inspection_leases(id, run_id, owner_id, heartbeat_at_ms, lease_expires_at_ms) values (1, null, ?, ?, ?)`,
		"owner-a",
		time.Now().UnixMilli(),
		time.Now().Add(time.Minute).UnixMilli(),
	); err != nil {
		t.Fatalf("insert unbound lease: %v", err)
	}
	lease, active, err := New(db).GetActiveLease(context.Background(), time.Now().UnixMilli())
	if err != nil || active || lease.RunID != 0 {
		t.Fatalf("unbound lease = %#v active=%v err=%v", lease, active, err)
	}
}

func TestAcquireRunReclaimsUnboundLeaseRow(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(
		context.Background(),
		`insert into codex_inspection_leases(id, run_id, owner_id, heartbeat_at_ms, lease_expires_at_ms) values (1, null, ?, ?, ?)`,
		"orphan-owner",
		time.Now().UnixMilli(),
		time.Now().Add(time.Hour).UnixMilli(),
	); err != nil {
		t.Fatalf("insert unbound lease: %v", err)
	}
	acquired, err := New(db).AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "reclaim-unbound"), "owner-a", time.Minute)
	if err != nil || acquired.Run.ID <= 0 {
		t.Fatalf("acquire after unbound lease error=%v run=%#v", err, acquired.Run)
	}
	lease, active, err := New(db).GetActiveLease(context.Background(), time.Now().UnixMilli())
	if err != nil || !active || lease.RunID != acquired.Run.ID || lease.OwnerID != "owner-a" {
		t.Fatalf("reclaimed lease=%#v active=%v err=%v", lease, active, err)
	}
}

func TestAcquireRunReclaimsLeaseBoundToTerminalRun(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	oldInput := newLifecycleTestRun(model.CodexInspectionTriggerManual, "terminal-with-lease")
	oldInput.Status = model.CodexInspectionStatusCompleted
	oldInput.FinishedAtMS = time.Now().UnixMilli()
	old, err := repository.CreateRun(context.Background(), oldInput)
	if err != nil {
		t.Fatalf("create old run: %v", err)
	}
	finishedAt := old.FinishedAtMS
	if _, err := db.ExecContext(context.Background(), `insert into codex_inspection_leases(id, run_id, owner_id, heartbeat_at_ms, lease_expires_at_ms) values (1, ?, ?, ?, ?)`, old.ID, "old-owner", finishedAt, finishedAt+time.Hour.Milliseconds()); err != nil {
		t.Fatalf("insert orphaned terminal lease: %v", err)
	}
	acquired, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "replacement"), "new-owner", time.Minute)
	if err != nil {
		t.Fatalf("acquire replacement around terminal lease: %v", err)
	}
	if acquired.Run.ID == old.ID {
		t.Fatalf("replacement reused terminal run id %d", old.ID)
	}
	lease, active, err := repository.GetActiveLease(context.Background(), time.Now().UnixMilli())
	if err != nil || !active || lease.RunID != acquired.Run.ID || lease.OwnerID != "new-owner" {
		t.Fatalf("replacement lease = %#v active=%v err=%v", lease, active, err)
	}
}

func TestMarkRunCancellingRejectsUnboundLeaseRow(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	run := insertLifecycleTestRun(t, db, newLifecycleTestRun(model.CodexInspectionTriggerManual, "stale"))
	if _, err := db.ExecContext(
		context.Background(),
		`insert into codex_inspection_leases(id, run_id, owner_id, heartbeat_at_ms, lease_expires_at_ms) values (1, null, ?, ?, ?)`,
		"owner-a",
		time.Now().UnixMilli(),
		time.Now().Add(time.Minute).UnixMilli(),
	); err != nil {
		t.Fatalf("insert unbound lease: %v", err)
	}
	if _, err := repository.MarkRunCancelling(context.Background(), run.ID, "owner-a", "cancel"); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("mark cancelling error = %v, want ErrLeaseLost", err)
	}
}

func TestRecoverStaleRunsPreservesValidLeaseAndHistoricalRuns(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	staleRunning := insertLifecycleTestRun(t, db, newLifecycleTestRun(model.CodexInspectionTriggerManual, "stale-running"))
	staleCancelling := newLifecycleTestRun(model.CodexInspectionTriggerManual, "stale-cancelling")
	staleCancelling.Status = model.CodexInspectionStatusCancelling
	staleCancelling = insertLifecycleTestRun(t, db, staleCancelling)
	completed := newLifecycleTestRun(model.CodexInspectionTriggerManual, "completed")
	completed.Status = model.CodexInspectionStatusCompleted
	completed.FinishedAtMS = time.Now().UnixMilli()
	completed, err = repository.CreateRun(context.Background(), completed)
	if err != nil {
		t.Fatalf("create completed: %v", err)
	}
	active, err := repository.AcquireRun(context.Background(), newLifecycleTestRun(model.CodexInspectionTriggerManual, "active"), "owner-active", time.Minute)
	if err != nil {
		t.Fatalf("acquire active: %v", err)
	}

	recovered, err := repository.RecoverStaleRuns(context.Background(), time.Now().UnixMilli(), "服务重启或任务租约过期，巡检已中断")
	if err != nil {
		t.Fatalf("recover stale runs: %v", err)
	}
	if len(recovered) != 2 {
		t.Fatalf("recovered = %#v, want two", recovered)
	}
	for _, id := range []int64{staleRunning.ID, staleCancelling.ID} {
		run, found, err := repository.GetRun(context.Background(), id)
		if err != nil || !found || run.Status != model.CodexInspectionStatusInterrupted || run.FinishedAtMS == 0 {
			t.Fatalf("recovered run %d = %#v found=%v err=%v", id, run, found, err)
		}
	}
	activeRun, found, err := repository.GetRun(context.Background(), active.Run.ID)
	if err != nil || !found || activeRun.Status != model.CodexInspectionStatusRunning {
		t.Fatalf("valid active run = %#v found=%v err=%v", activeRun, found, err)
	}
	completedRun, found, err := repository.GetRun(context.Background(), completed.ID)
	if err != nil || !found || completedRun.Status != model.CodexInspectionStatusCompleted {
		t.Fatalf("completed run = %#v found=%v err=%v", completedRun, found, err)
	}
}

func TestRecoverStaleRunsDoesNotRollBackWhenRecoveryLogFails(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repository := New(db)
	stale := insertLifecycleTestRun(t, db, newLifecycleTestRun(model.CodexInspectionTriggerManual, "stale-log-failure"))
	if _, err := db.ExecContext(context.Background(), `create trigger fail_codex_inspection_recovery_log before insert on codex_inspection_logs begin select raise(abort, 'forced recovery log failure'); end`); err != nil {
		t.Fatalf("create log failure trigger: %v", err)
	}
	_, err = repository.RecoverStaleRuns(context.Background(), time.Now().UnixMilli(), "服务重启或任务租约过期，巡检已中断")
	if err == nil {
		t.Fatal("recover stale runs unexpectedly succeeded")
	}
	stored, found, err := repository.GetRun(context.Background(), stale.ID)
	if err != nil || !found {
		t.Fatalf("get stale run found=%v err=%v", found, err)
	}
	if stored.Status != model.CodexInspectionStatusInterrupted || stored.FinishedAtMS == 0 {
		t.Fatalf("recovery terminal state = %#v", stored)
	}
}

func TestSQLiteBusyRetryStopsAfterTransientConflict(t *testing.T) {
	for _, message := range []string{
		"database is locked (SQLITE_BUSY)",
		"database table is locked (SQLITE_LOCKED)",
	} {
		t.Run(message, func(t *testing.T) {
			attempts := 0
			if err := withSQLiteBusyRetry(context.Background(), func() error {
				attempts++
				if attempts == 1 {
					return errors.New(message)
				}
				return nil
			}); err != nil {
				t.Fatalf("retry transient busy: %v", err)
			}
			if attempts != 2 {
				t.Fatalf("attempts = %d, want 2", attempts)
			}
		})
	}
}

func newLifecycleTestRun(triggerType, triggerKey string) model.CodexInspectionRun {
	settings := model.DefaultCodexInspectionConfig()
	return model.CodexInspectionRun{
		TriggerType:  triggerType,
		TriggerKey:   triggerKey,
		Status:       model.CodexInspectionStatusRunning,
		Settings:     settings,
		SettingsJSON: model.MarshalCodexInspectionSettings(settings),
	}
}

func insertLifecycleTestRun(t *testing.T, db *sql.DB, run model.CodexInspectionRun) model.CodexInspectionRun {
	t.Helper()
	now := time.Now().UnixMilli()
	if run.StartedAtMS <= 0 {
		run.StartedAtMS = now
	}
	if run.CreatedAtMS <= 0 {
		run.CreatedAtMS = now
	}
	run.UpdatedAtMS = now
	if run.SettingsJSON == "" {
		run.SettingsJSON = model.MarshalCodexInspectionSettings(run.Settings)
	}
	if err := db.QueryRowContext(
		context.Background(),
		`insert into codex_inspection_runs (
			trigger_type, trigger_key, status, started_at_ms, finished_at_ms,
			settings_json, created_at_ms, updated_at_ms
		) values (?, ?, ?, ?, ?, ?, ?, ?) returning id`,
		run.TriggerType,
		run.TriggerKey,
		run.Status,
		run.StartedAtMS,
		nullPositiveInt64(run.FinishedAtMS),
		run.SettingsJSON,
		run.CreatedAtMS,
		run.UpdatedAtMS,
	).Scan(&run.ID); err != nil {
		t.Fatalf("insert lifecycle run fixture: %v", err)
	}
	return run
}
