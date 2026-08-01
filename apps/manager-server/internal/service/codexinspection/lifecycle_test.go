package codexinspection

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	codexinspectionrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/codexinspection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type busyOnceFinalizeRepository struct {
	codexinspectionrepo.Repository
	mu       sync.Mutex
	attempts int
}

type leaseLostFinalizeRepository struct {
	codexinspectionrepo.Repository
}

type failingCancelRepository struct {
	codexinspectionrepo.Repository
	err error
}

type failingLifecycleLogRepository struct {
	codexinspectionrepo.Repository
}

type blockingFinalizeRepository struct {
	codexinspectionrepo.Repository
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

type blockingCancelRepository struct {
	codexinspectionrepo.Repository
	entered chan struct{}
	release chan struct{}
}

type blockingHeartbeatRepository struct {
	codexinspectionrepo.Repository
	once    sync.Once
	entered chan struct{}
}

type terminalBeforeCancelRepository struct {
	codexinspectionrepo.Repository
}

type deadlineFinalizeRepository struct {
	codexinspectionrepo.Repository
	mu            sync.Mutex
	finalizeCalls int
	forceCalls    int
}

type deadlineUpdateRunRepository struct {
	codexinspectionrepo.Repository
	mu          sync.Mutex
	hasDeadline bool
	deadline    time.Time
	err         error
}

type deadlineGetRunRepository struct {
	codexinspectionrepo.Repository
	once sync.Once
}

type committedCancellationRaceRepository struct {
	codexinspectionrepo.Repository
	completionOnce sync.Once
	getOnce        sync.Once
	markOnce       sync.Once
	completionLog  chan struct{}
	getEntered     chan struct{}
	releaseGet     chan struct{}
	markCommitted  chan struct{}
	releaseMark    chan struct{}
}

func (r *failingCancelRepository) MarkRunCancelling(context.Context, int64, string, string) (bool, error) {
	return false, r.err
}

func (r *failingLifecycleLogRepository) FinalizeRun(ctx context.Context, run model.CodexInspectionRun, ownerID string, finalLog *model.CodexInspectionLog) error {
	if finalLog != nil {
		return errors.New("final lifecycle log insert failed")
	}
	return r.Repository.FinalizeRun(ctx, run, ownerID, nil)
}

func (r *blockingFinalizeRepository) FinalizeRun(ctx context.Context, run model.CodexInspectionRun, ownerID string, finalLog *model.CodexInspectionLog) error {
	r.once.Do(func() { close(r.entered) })
	select {
	case <-r.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return r.Repository.FinalizeRun(ctx, run, ownerID, finalLog)
}

func (r *blockingCancelRepository) MarkRunCancelling(ctx context.Context, runID int64, ownerID string, reason string) (bool, error) {
	close(r.entered)
	select {
	case <-r.release:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	return r.Repository.MarkRunCancelling(ctx, runID, ownerID, reason)
}

func (r *blockingHeartbeatRepository) HeartbeatRun(ctx context.Context, runID int64, ownerID string, leaseDuration time.Duration) error {
	r.once.Do(func() { close(r.entered) })
	<-ctx.Done()
	return ctx.Err()
}

func (r *terminalBeforeCancelRepository) MarkRunCancelling(ctx context.Context, runID int64, ownerID string, _ string) (bool, error) {
	run, found, err := r.Repository.GetRun(ctx, runID)
	if err != nil {
		return false, err
	}
	if !found {
		return false, ErrRunNotFound
	}
	run.Status = model.CodexInspectionStatusCompleted
	run.FinishedAtMS = time.Now().UnixMilli()
	if err := r.Repository.FinalizeRun(ctx, run, ownerID, nil); err != nil {
		return false, err
	}
	return false, nil
}

func (r *deadlineFinalizeRepository) FinalizeRun(ctx context.Context, run model.CodexInspectionRun, ownerID string, finalLog *model.CodexInspectionLog) error {
	r.mu.Lock()
	r.finalizeCalls++
	r.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

func (r *deadlineFinalizeRepository) ForceFinalizeRun(ctx context.Context, run model.CodexInspectionRun, ownerID string, finalLog *model.CodexInspectionLog) error {
	r.mu.Lock()
	r.forceCalls++
	r.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

func (r *deadlineUpdateRunRepository) UpdateRun(ctx context.Context, run model.CodexInspectionRun) error {
	deadline, ok := ctx.Deadline()
	r.mu.Lock()
	r.hasDeadline = ok
	r.deadline = deadline
	r.mu.Unlock()
	return r.err
}

func (r *deadlineGetRunRepository) GetRun(ctx context.Context, runID int64) (model.CodexInspectionRun, bool, error) {
	r.once.Do(func() {})
	<-ctx.Done()
	return model.CodexInspectionRun{}, false, ctx.Err()
}

func (r *committedCancellationRaceRepository) InsertLog(ctx context.Context, entry model.CodexInspectionLog) (model.CodexInspectionLog, error) {
	stored, err := r.Repository.InsertLog(ctx, entry)
	if err == nil && entry.Message == "凭证健康巡检完成" {
		r.completionOnce.Do(func() { close(r.completionLog) })
	}
	return stored, err
}

func (r *committedCancellationRaceRepository) GetRun(ctx context.Context, runID int64) (model.CodexInspectionRun, bool, error) {
	blocked := false
	select {
	case <-r.completionLog:
		r.getOnce.Do(func() {
			blocked = true
			close(r.getEntered)
		})
	default:
	}
	if blocked {
		select {
		case <-r.releaseGet:
		case <-ctx.Done():
			return model.CodexInspectionRun{}, false, ctx.Err()
		}
	}
	return r.Repository.GetRun(ctx, runID)
}

func (r *committedCancellationRaceRepository) MarkRunCancelling(ctx context.Context, runID int64, ownerID string, reason string) (bool, error) {
	changed, err := r.Repository.MarkRunCancelling(ctx, runID, ownerID, reason)
	if err != nil {
		return changed, err
	}
	r.markOnce.Do(func() { close(r.markCommitted) })
	select {
	case <-r.releaseMark:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	return changed, nil
}

type blockingAcquireRepository struct {
	codexinspectionrepo.Repository
	acquired chan struct{}
	release  chan struct{}
}

func (r *blockingAcquireRepository) AcquireRun(ctx context.Context, run model.CodexInspectionRun, ownerID string, leaseDuration time.Duration) (codexinspectionrepo.AcquireRunResult, error) {
	result, err := r.Repository.AcquireRun(ctx, run, ownerID, leaseDuration)
	if err != nil {
		return result, err
	}
	close(r.acquired)
	<-r.release
	return result, nil
}

func (r *busyOnceFinalizeRepository) FinalizeRun(ctx context.Context, run model.CodexInspectionRun, ownerID string, finalLog *model.CodexInspectionLog) error {
	r.mu.Lock()
	r.attempts++
	attempt := r.attempts
	r.mu.Unlock()
	if attempt == 1 {
		return errors.New("database is locked (SQLITE_BUSY)")
	}
	return r.Repository.FinalizeRun(ctx, run, ownerID, finalLog)
}

func (r *leaseLostFinalizeRepository) FinalizeRun(context.Context, model.CodexInspectionRun, string, *model.CodexInspectionLog) error {
	return codexinspectionrepo.ErrLeaseLost
}

func TestServiceSingleInstanceStartIsSingleFlight(t *testing.T) {
	upstream, started := newBlockingInspectionServer(t)
	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)
	first, err := svc.Start(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual, TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("start first run: %v", err)
	}
	waitInspectionStarted(t, started)
	if _, err := svc.Start(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual, TriggerKey: "manual"}); !errors.Is(err, ErrRunAlreadyActive) {
		t.Fatalf("second start error = %v, want ErrRunAlreadyActive", err)
	}
	if _, err := svc.CancelRun(context.Background(), first.Run.ID); err != nil {
		t.Fatalf("cancel first run: %v", err)
	}
	waitInspectionStatus(t, db, first.Run.ID, model.CodexInspectionStatusCancelled)
}

func TestServicesSharingDatabaseCannotAcquireSameLease(t *testing.T) {
	upstream, started := newBlockingInspectionServer(t)
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	firstStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	t.Cleanup(func() { _ = firstStore.Close() })
	secondStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	if err := firstStore.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	firstService := newCodexInspectionTestService(t, firstStore)
	secondService := newCodexInspectionTestService(t, secondStore)
	first, err := firstService.Start(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual})
	if err != nil {
		t.Fatalf("start first service: %v", err)
	}
	waitInspectionStarted(t, started)
	if _, err := secondService.Start(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual}); !errors.Is(err, ErrRunAlreadyActive) {
		t.Fatalf("second service start error = %v, want ErrRunAlreadyActive", err)
	}
	if _, err := firstService.CancelRun(context.Background(), first.Run.ID); err != nil {
		t.Fatalf("cancel first service: %v", err)
	}
	waitInspectionStatus(t, firstStore, first.Run.ID, model.CodexInspectionStatusCancelled)
}

func TestScheduledStartRechecksDisabledConfigBeforeLeaseAcquisition(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			_, _ = w.Write([]byte(`{"files":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)
	db := newCodexInspectionTestStore(t)
	cfg := newCodexInspectionManagerConfig(upstream.URL)
	disabled := false
	cfg.CodexInspection.Enabled = &disabled
	if err := db.SaveManagerConfig(context.Background(), cfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)
	if _, err := svc.Start(context.Background(), RunRequest{
		TriggerType: model.CodexInspectionTriggerScheduled,
		TriggerKey:  "interval:60:1",
	}); !errors.Is(err, ErrScheduledRunDisabled) {
		t.Fatalf("scheduled start error = %v, want ErrScheduledRunDisabled", err)
	}
	runs, err := db.ListCodexInspectionRuns(context.Background(), 10)
	if err != nil || len(runs) != 0 {
		t.Fatalf("disabled scheduled runs = %#v err=%v", runs, err)
	}
}

func TestUserCancelIsIdempotentAndReleasesLease(t *testing.T) {
	upstream, started := newBlockingInspectionServer(t)
	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)
	startedRun, err := svc.Start(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	waitInspectionStarted(t, started)
	if _, err := svc.CancelRun(context.Background(), startedRun.Run.ID); err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	waitInspectionStatus(t, db, startedRun.Run.ID, model.CodexInspectionStatusCancelled)
	if detail, err := svc.CancelRun(context.Background(), startedRun.Run.ID); err != nil || detail.Run.Status != model.CodexInspectionStatusCancelled {
		t.Fatalf("repeat cancel detail=%#v err=%v", detail.Run, err)
	}
	if lease, active, err := db.GetActiveCodexInspectionLease(context.Background(), time.Now().UnixMilli()); err != nil || active {
		t.Fatalf("lease after cancel = %#v active=%v err=%v", lease, active, err)
	}
}

func TestCancelConflictsWhenRunFinishesBeforeCancellingTransition(t *testing.T) {
	upstream, started := newBlockingInspectionServer(t)
	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	db.CodexInspections = &terminalBeforeCancelRepository{Repository: db.CodexInspections}
	svc := newCodexInspectionTestService(t, db)
	startedRun, err := svc.Start(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	waitInspectionStarted(t, started)

	if _, err := svc.CancelRun(context.Background(), startedRun.Run.ID); !errors.Is(err, ErrRunNotCancellable) {
		t.Fatalf("cancel error = %v, want ErrRunNotCancellable", err)
	}
	stored, found, err := db.GetCodexInspectionRun(context.Background(), startedRun.Run.ID)
	if err != nil || !found || stored.Status != model.CodexInspectionStatusCompleted {
		t.Fatalf("completed run = %#v found=%v err=%v", stored, found, err)
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelShutdown()
	if err := svc.StopAndWait(shutdownCtx); err != nil {
		t.Fatalf("stop service: %v", err)
	}
}

func TestUserCancelContinuesAfterRequestContextIsCancelled(t *testing.T) {
	upstream, started := newBlockingInspectionServer(t)
	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	wrapper := &blockingCancelRepository{
		Repository: db.CodexInspections,
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	db.CodexInspections = wrapper
	svc := newCodexInspectionTestService(t, db)
	startedRun, err := svc.Start(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	waitInspectionStarted(t, started)

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	cancelResult := make(chan error, 1)
	go func() {
		_, err := svc.CancelRun(requestCtx, startedRun.Run.ID)
		cancelResult <- err
	}()
	waitInspectionStarted(t, wrapper.entered)
	cancelRequest()
	select {
	case err := <-cancelResult:
		t.Fatalf("cancel returned when request context closed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(wrapper.release)
	if err := <-cancelResult; err != nil {
		t.Fatalf("cancel after request disconnect: %v", err)
	}
	waitInspectionStatus(t, db, startedRun.Run.ID, model.CodexInspectionStatusCancelled)
}

func TestRepeatedCancelIsIdempotentWhileFinalizerIsBlocked(t *testing.T) {
	upstream, started := newBlockingInspectionServer(t)
	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	wrapper := &blockingFinalizeRepository{
		Repository: db.CodexInspections,
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	db.CodexInspections = wrapper
	svc := newCodexInspectionTestService(t, db)
	startedRun, err := svc.Start(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	waitInspectionStarted(t, started)
	first, err := svc.CancelRun(context.Background(), startedRun.Run.ID)
	if err != nil || first.Run.Status != model.CodexInspectionStatusCancelling {
		t.Fatalf("first cancel detail=%#v err=%v", first.Run, err)
	}
	waitInspectionStarted(t, wrapper.entered)

	duringFinalize, err := svc.GetRun(context.Background(), startedRun.Run.ID)
	if err != nil || duringFinalize.Run.Status != model.CodexInspectionStatusCancelling || !duringFinalize.Run.Cancellable {
		t.Fatalf("run during cancellation finalization=%#v err=%v", duringFinalize.Run, err)
	}
	second, err := svc.CancelRun(context.Background(), startedRun.Run.ID)
	if err != nil || second.Run.Status != model.CodexInspectionStatusCancelling {
		t.Fatalf("repeat cancel during finalization detail=%#v err=%v", second.Run, err)
	}

	close(wrapper.release)
	waitInspectionStatus(t, db, startedRun.Run.ID, model.CodexInspectionStatusCancelled)
}

func TestCancelWriteFailureDoesNotChangeTerminationReason(t *testing.T) {
	upstream, started := newBlockingInspectionServer(t)
	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	wrapper := &failingCancelRepository{Repository: db.CodexInspections, err: errors.New("cancel transition failed")}
	db.CodexInspections = wrapper
	svc := newCodexInspectionTestService(t, db)
	startedRun, err := svc.Start(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	waitInspectionStarted(t, started)
	if _, err := svc.CancelRun(context.Background(), startedRun.Run.ID); err == nil || err.Error() != wrapper.err.Error() {
		t.Fatalf("cancel error = %v, want %v", err, wrapper.err)
	}
	run, found, err := db.GetCodexInspectionRun(context.Background(), startedRun.Run.ID)
	if err != nil || !found || run.Status != model.CodexInspectionStatusRunning {
		t.Fatalf("run after failed cancel = %#v found=%v err=%v", run, found, err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := svc.StopAndWait(shutdownCtx); err != nil {
		t.Fatalf("stop service: %v", err)
	}
	waitInspectionStatus(t, db, startedRun.Run.ID, model.CodexInspectionStatusInterrupted)
}

func TestShutdownPreservesInFlightUserCancellation(t *testing.T) {
	started := make(chan struct{})
	cancelObserved := make(chan struct{})
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" {
			http.NotFound(w, r)
			return
		}
		close(started)
		<-r.Context().Done()
		close(cancelObserved)
		<-release
	}))
	t.Cleanup(upstream.Close)
	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)
	startedRun, err := svc.Start(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	waitInspectionStarted(t, started)
	if _, err := svc.CancelRun(context.Background(), startedRun.Run.ID); err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	waitInspectionStarted(t, cancelObserved)
	stopResult := make(chan error, 1)
	go func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		stopResult <- svc.StopAndWait(shutdownCtx)
	}()
	close(release)
	if err := <-stopResult; err != nil {
		t.Fatalf("stop service: %v", err)
	}
	waitInspectionStatus(t, db, startedRun.Run.ID, model.CodexInspectionStatusCancelled)
}

func TestCommittedCancellationWinsCompletionAndShutdownRace(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			_, _ = w.Write([]byte(`{"files":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)
	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	wrapper := &committedCancellationRaceRepository{
		Repository:    db.CodexInspections,
		completionLog: make(chan struct{}),
		getEntered:    make(chan struct{}),
		releaseGet:    make(chan struct{}),
		markCommitted: make(chan struct{}),
		releaseMark:   make(chan struct{}),
	}
	db.CodexInspections = wrapper
	svc := newCodexInspectionTestService(t, db)
	startedRun, err := svc.Start(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	waitInspectionStarted(t, wrapper.getEntered)

	cancelResult := make(chan error, 1)
	go func() {
		_, err := svc.CancelRun(context.Background(), startedRun.Run.ID)
		cancelResult <- err
	}()
	waitInspectionStarted(t, wrapper.markCommitted)

	shutdownResult := make(chan error, 1)
	go func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		shutdownResult <- svc.StopAndWait(shutdownCtx)
	}()
	close(wrapper.releaseGet)
	cancelled := waitInspectionStatus(t, db, startedRun.Run.ID, model.CodexInspectionStatusCancelled)
	if cancelled.FinishedAtMS == 0 {
		t.Fatalf("cancelled run = %#v", cancelled)
	}
	close(wrapper.releaseMark)
	if err := <-cancelResult; err != nil {
		t.Fatalf("cancel result: %v", err)
	}
	if err := <-shutdownResult; err != nil {
		t.Fatalf("shutdown result: %v", err)
	}
}

func TestShutdownWaitsForStartingRunAndFinalizesIt(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			_, _ = w.Write([]byte(`{"files":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)
	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	wrapper := &blockingAcquireRepository{
		Repository: db.CodexInspections,
		acquired:   make(chan struct{}),
		release:    make(chan struct{}),
	}
	db.CodexInspections = wrapper
	svc := newCodexInspectionTestService(t, db)
	startResult := make(chan error, 1)
	go func() {
		_, err := svc.Start(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual})
		startResult <- err
	}()
	waitInspectionStarted(t, wrapper.acquired)
	stopResult := make(chan error, 1)
	go func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		stopResult <- svc.StopAndWait(shutdownCtx)
	}()
	deadline := time.Now().Add(time.Second)
	for !svc.IsStopping() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !svc.IsStopping() {
		t.Fatal("service did not enter stopping state")
	}
	close(wrapper.release)
	if err := <-startResult; !errors.Is(err, ErrServiceStopping) {
		t.Fatalf("start error = %v, want ErrServiceStopping", err)
	}
	if err := <-stopResult; err != nil {
		t.Fatalf("stop service: %v", err)
	}
	runs, err := db.ListCodexInspectionRuns(context.Background(), 10)
	if err != nil || len(runs) != 1 || runs[0].Status != model.CodexInspectionStatusInterrupted || runs[0].FinishedAtMS == 0 {
		t.Fatalf("runs after start/shutdown race = %#v err=%v", runs, err)
	}
	if _, active, err := db.GetActiveCodexInspectionLease(context.Background(), time.Now().UnixMilli()); err != nil || active {
		t.Fatalf("lease after start/shutdown race active=%v err=%v", active, err)
	}
}

func TestStopAndWaitTracksInFlightCancellationWrite(t *testing.T) {
	upstream, started := newBlockingInspectionServer(t)
	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	wrapper := &blockingCancelRepository{
		Repository: db.CodexInspections,
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	db.CodexInspections = wrapper
	svc := newCodexInspectionTestService(t, db)
	startedRun, err := svc.Start(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	waitInspectionStarted(t, started)
	cancelResult := make(chan error, 1)
	go func() {
		_, err := svc.CancelRun(context.Background(), startedRun.Run.ID)
		cancelResult <- err
	}()
	waitInspectionStarted(t, wrapper.entered)
	stopResult := make(chan error, 1)
	go func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		stopResult <- svc.StopAndWait(shutdownCtx)
	}()
	select {
	case err := <-stopResult:
		t.Fatalf("stop returned before cancellation write completed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(wrapper.release)
	if err := <-cancelResult; !errors.Is(err, ErrRunNotCancellable) && !errors.Is(err, ErrRunNotOwned) {
		t.Fatalf("cancel during shutdown error = %v, want a shutdown conflict", err)
	}
	if err := <-stopResult; err != nil {
		t.Fatalf("stop service: %v", err)
	}
	waitInspectionStatus(t, db, startedRun.Run.ID, model.CodexInspectionStatusInterrupted)
}

func TestStopAndWaitInterruptsRunAndReleasesLease(t *testing.T) {
	upstream, started := newBlockingInspectionServer(t)
	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)
	startedRun, err := svc.Start(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	waitInspectionStarted(t, started)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := svc.StopAndWait(shutdownCtx); err != nil {
		t.Fatalf("stop and wait: %v", err)
	}
	run, found, err := db.GetCodexInspectionRun(context.Background(), startedRun.Run.ID)
	if err != nil || !found || run.Status != model.CodexInspectionStatusInterrupted || run.FinishedAtMS == 0 {
		t.Fatalf("interrupted run = %#v found=%v err=%v", run, found, err)
	}
	if lease, active, err := db.GetActiveCodexInspectionLease(context.Background(), time.Now().UnixMilli()); err != nil || active {
		t.Fatalf("lease after shutdown = %#v active=%v err=%v", lease, active, err)
	}
}

func TestStopAndWaitCancelsAndWaitsForAuxiliaryOperation(t *testing.T) {
	db := newCodexInspectionTestStore(t)
	svc := newCodexInspectionTestService(t, db)
	operationCtx, err := svc.acquireAuxiliaryRun(context.Background())
	if err != nil {
		t.Fatalf("acquire auxiliary operation: %v", err)
	}
	operationStopped := make(chan struct{})
	go func() {
		<-operationCtx.Done()
		svc.releaseRun()
		close(operationStopped)
	}()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := svc.StopAndWait(shutdownCtx); err != nil {
		t.Fatalf("stop service: %v", err)
	}
	select {
	case <-operationStopped:
	case <-time.After(time.Second):
		t.Fatal("auxiliary operation was not cancelled and drained")
	}
}

func TestManualActionPersistenceUsesOneBoundedContext(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true}]}`))
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	run, err := db.CreateCodexInspectionRun(context.Background(), model.CodexInspectionRun{
		TriggerType:  model.CodexInspectionTriggerManual,
		TriggerKey:   "manual-actions-bounded",
		Status:       model.CodexInspectionStatusCompleted,
		Settings:     model.DefaultCodexInspectionConfig(),
		SettingsJSON: model.MarshalCodexInspectionSettings(model.DefaultCodexInspectionConfig()),
	})
	if err != nil {
		t.Fatalf("create completed run: %v", err)
	}
	result, err := db.InsertCodexInspectionResult(context.Background(), model.CodexInspectionResult{
		RunID:          run.ID,
		AccountKey:     "auth-a.json::auth-1",
		FileName:       "auth-a.json",
		DisplayAccount: "alice@example.com",
		AuthIndex:      "auth-1",
		Provider:       "codex",
		Disabled:       true,
		Action:         "enable",
		ActionReason:   "test",
		ActionStatus:   model.CodexInspectionActionStatusPending,
	})
	if err != nil {
		t.Fatalf("insert action result: %v", err)
	}
	wrapper := &deadlineUpdateRunRepository{Repository: db.CodexInspections, err: errors.New("bounded update sentinel")}
	db.CodexInspections = wrapper
	svc := newCodexInspectionTestService(t, db)
	_, err = svc.ExecuteManualActions(context.Background(), run.ID, ExecuteActionsRequest{ResultIDs: []int64{result.ID}})
	if err == nil || err.Error() != wrapper.err.Error() {
		t.Fatalf("manual action error = %v, want %v", err, wrapper.err)
	}
	wrapper.mu.Lock()
	hasDeadline, deadline := wrapper.hasDeadline, wrapper.deadline
	wrapper.mu.Unlock()
	if !hasDeadline || !deadline.After(time.Now()) || deadline.After(time.Now().Add(resultPersistenceTimeout+time.Second)) {
		t.Fatalf("manual persistence deadline = %s (has=%v), want a bounded deadline near %s", deadline, hasDeadline, resultPersistenceTimeout)
	}
}

func TestCompletedAndFailedRunsReleaseLease(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v0/management/auth-files" {
				_, _ = w.Write([]byte(`{"files":[]}`))
				return
			}
			http.NotFound(w, r)
		}))
		t.Cleanup(upstream.Close)
		db := newCodexInspectionTestStore(t)
		if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
			t.Fatalf("save manager config: %v", err)
		}
		detail, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual})
		if err != nil || detail.Run.Status != model.CodexInspectionStatusCompleted {
			t.Fatalf("completed detail=%#v err=%v", detail.Run, err)
		}
		if _, active, err := db.GetActiveCodexInspectionLease(context.Background(), time.Now().UnixMilli()); err != nil || active {
			t.Fatalf("completed lease active=%v err=%v", active, err)
		}
	})

	t.Run("failed", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "upstream failed", http.StatusInternalServerError)
		}))
		t.Cleanup(upstream.Close)
		db := newCodexInspectionTestStore(t)
		if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
			t.Fatalf("save manager config: %v", err)
		}
		_, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual})
		if err == nil {
			t.Fatal("failed run error = nil")
		}
		runs, listErr := db.ListCodexInspectionRuns(context.Background(), 1)
		if listErr != nil || len(runs) != 1 || runs[0].Status != model.CodexInspectionStatusFailed {
			t.Fatalf("failed runs=%#v err=%v", runs, listErr)
		}
		if _, active, leaseErr := db.GetActiveCodexInspectionLease(context.Background(), time.Now().UnixMilli()); leaseErr != nil || active {
			t.Fatalf("failed lease active=%v err=%v", active, leaseErr)
		}
	})
}

func TestFinalizationRetriesTransientSQLiteBusy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			_, _ = w.Write([]byte(`{"files":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)
	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	wrapped := &busyOnceFinalizeRepository{Repository: db.CodexInspections}
	db.CodexInspections = wrapped
	detail, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual})
	if err != nil || detail.Run.Status != model.CodexInspectionStatusCompleted {
		t.Fatalf("completed detail=%#v err=%v", detail.Run, err)
	}
	wrapped.mu.Lock()
	attempts := wrapped.attempts
	wrapped.mu.Unlock()
	if attempts != 2 {
		t.Fatalf("finalize attempts = %d, want 2", attempts)
	}
}

func TestFinalizationFallbacksShareOneBoundedContext(t *testing.T) {
	wrapper := &deadlineFinalizeRepository{}
	svc := NewWithOptions(&store.Store{CodexInspections: wrapper}, nil, ServiceOptions{OwnerID: "test-owner"})
	run := model.CodexInspectionRun{ID: 1, Status: model.CodexInspectionStatusCompleted}
	finalLog := &model.CodexInspectionLog{RunID: run.ID, Level: "info", Message: "finished"}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	startedAt := time.Now()
	if err := svc.finalizeInspectionRunWithContext(ctx, run, finalLog); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("finalization error = %v, want context deadline", err)
	}
	if err := svc.forceFinalizeInspectionRunWithContext(ctx, run, finalLog); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("fenced finalization error = %v, want context deadline", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("shared finalization budget elapsed = %s, want bounded single budget", elapsed)
	}
	wrapper.mu.Lock()
	finalizeCalls, forceCalls := wrapper.finalizeCalls, wrapper.forceCalls
	wrapper.mu.Unlock()
	if finalizeCalls != 1 || forceCalls != 0 {
		t.Fatalf("finalize calls=%d force calls=%d, want one primary call and no fresh force budget", finalizeCalls, forceCalls)
	}
}

func TestResultDetailReadPreservesCallerDeadline(t *testing.T) {
	service := NewWithOptions(&store.Store{
		CodexInspections: &deadlineGetRunRepository{},
	}, nil, ServiceOptions{OwnerID: "test-owner"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	startedAt := time.Now()
	if _, err := service.getRunWithResultFallback(ctx, 1, nil, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("detail read error = %v, want context deadline", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("detail read elapsed = %s, want caller deadline to be preserved", elapsed)
	}
}

func TestHeartbeatFailureFencesRunBeforeLeaseExpiry(t *testing.T) {
	var mu sync.Mutex
	activeRequests := 0
	maximumConcurrentRequests := 0
	requestNumber := 0
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	firstFinished := make(chan time.Time, 1)
	secondFinished := make(chan time.Time, 1)
	var firstStartedOnce sync.Once
	var secondStartedOnce sync.Once
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		requestNumber++
		currentRequest := requestNumber
		activeRequests++
		if activeRequests > maximumConcurrentRequests {
			maximumConcurrentRequests = activeRequests
		}
		mu.Unlock()
		if currentRequest == 1 {
			firstStartedOnce.Do(func() { close(firstStarted) })
		} else if currentRequest == 2 {
			secondStartedOnce.Do(func() { close(secondStarted) })
		}
		<-r.Context().Done()
		finishedAt := time.Now()
		mu.Lock()
		activeRequests--
		mu.Unlock()
		if currentRequest == 1 {
			firstFinished <- finishedAt
		} else if currentRequest == 2 {
			secondFinished <- finishedAt
		}
	}))
	t.Cleanup(upstream.Close)

	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	firstStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open first store: %v", err)
	}
	t.Cleanup(func() { _ = firstStore.Close() })
	secondStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("open second store: %v", err)
	}
	t.Cleanup(func() { _ = secondStore.Close() })
	if err := firstStore.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	firstService := newCodexInspectionTestService(t, firstStore)
	firstService.leaseDuration = 500 * time.Millisecond
	firstService.heartbeatInterval = 50 * time.Millisecond
	firstHeartbeat := &blockingHeartbeatRepository{
		Repository: firstStore.CodexInspections,
		entered:    make(chan struct{}),
	}
	firstStore.CodexInspections = firstHeartbeat
	secondService := newCodexInspectionTestService(t, secondStore)
	secondService.leaseDuration = firstService.leaseDuration
	secondService.heartbeatInterval = firstService.heartbeatInterval

	first, err := firstService.Start(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual, TriggerKey: "first"})
	if err != nil {
		t.Fatalf("start first run: %v", err)
	}
	waitInspectionStarted(t, firstStarted)
	select {
	case <-firstHeartbeat.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat did not start")
	}
	lease, active, err := firstStore.GetActiveCodexInspectionLease(context.Background(), time.Now().UnixMilli())
	if err != nil || !active {
		t.Fatalf("first lease active=%v lease=%#v err=%v", active, lease, err)
	}

	secondResult := make(chan struct {
		run RunDetail
		err error
	}, 1)
	go func() {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			run, startErr := secondService.Start(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual, TriggerKey: "second"})
			if startErr == nil {
				secondResult <- struct {
					run RunDetail
					err error
				}{run: run, err: nil}
				return
			}
			if !errors.Is(startErr, ErrRunAlreadyActive) {
				secondResult <- struct {
					run RunDetail
					err error
				}{err: startErr}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		secondResult <- struct {
			run RunDetail
			err error
		}{err: context.DeadlineExceeded}
	}()

	var firstFinishedAt time.Time
	select {
	case firstFinishedAt = <-firstFinished:
	case <-time.After(3 * time.Second):
		t.Fatal("first inspection request did not stop")
	}
	if !firstFinishedAt.Before(time.UnixMilli(lease.LeaseExpiresAtMS)) {
		t.Fatalf("first request finished at %s after lease expiry %d", firstFinishedAt, lease.LeaseExpiresAtMS)
	}
	firstRun := waitInspectionStatus(t, firstStore, first.Run.ID, model.CodexInspectionStatusInterrupted)
	if firstRun.FinishedAtMS == 0 {
		t.Fatalf("interrupted run = %#v", firstRun)
	}

	var second struct {
		run RunDetail
		err error
	}
	select {
	case second = <-secondResult:
	case <-time.After(3 * time.Second):
		t.Fatal("replacement inspection did not acquire the lease")
	}
	if second.err != nil {
		t.Fatalf("replacement start: %v", second.err)
	}
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement inspection did not reach the probe")
	}
	if _, err := secondService.CancelRun(context.Background(), second.run.Run.ID); err != nil {
		t.Fatalf("cancel replacement run: %v", err)
	}
	waitInspectionStatus(t, secondStore, second.run.Run.ID, model.CodexInspectionStatusCancelled)
	select {
	case <-secondFinished:
	case <-time.After(2 * time.Second):
		t.Fatal("replacement inspection request did not stop")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := firstService.StopAndWait(shutdownCtx); err != nil {
		t.Fatalf("stop first service: %v", err)
	}
	if err := secondService.StopAndWait(shutdownCtx); err != nil {
		t.Fatalf("stop second service: %v", err)
	}
	mu.Lock()
	maximum := maximumConcurrentRequests
	mu.Unlock()
	if maximum != 1 {
		t.Fatalf("maximum concurrent probe requests = %d, want 1", maximum)
	}
}

func TestFinalizationRecoversWhenPrimaryLeaseFenceIsLost(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			_, _ = w.Write([]byte(`{"files":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)
	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	db.CodexInspections = &leaseLostFinalizeRepository{Repository: db.CodexInspections}
	detail, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual})
	if err != nil || detail.Run.Status != model.CodexInspectionStatusCompleted {
		t.Fatalf("completed detail=%#v err=%v", detail.Run, err)
	}
	stored, found, err := db.GetCodexInspectionRun(context.Background(), detail.Run.ID)
	if err != nil || !found || stored.Status != model.CodexInspectionStatusCompleted || stored.FinishedAtMS == 0 {
		t.Fatalf("stored run=%#v found=%v err=%v", stored, found, err)
	}
	if _, active, err := db.GetActiveCodexInspectionLease(context.Background(), time.Now().UnixMilli()); err != nil || active {
		t.Fatalf("lease after fenced recovery active=%v err=%v", active, err)
	}
}

func TestFinalizationFallsBackWhenLifecycleLogFails(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			_, _ = w.Write([]byte(`{"files":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)
	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	db.CodexInspections = &failingLifecycleLogRepository{Repository: db.CodexInspections}
	detail, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual})
	if err != nil || detail.Run.Status != model.CodexInspectionStatusCompleted {
		t.Fatalf("detail=%#v err=%v", detail.Run, err)
	}
	stored, found, err := db.GetCodexInspectionRun(context.Background(), detail.Run.ID)
	if err != nil || !found || stored.Status != model.CodexInspectionStatusCompleted || stored.FinishedAtMS == 0 {
		t.Fatalf("stored run=%#v found=%v err=%v", stored, found, err)
	}
	if _, active, err := db.GetActiveCodexInspectionLease(context.Background(), time.Now().UnixMilli()); err != nil || active {
		t.Fatalf("lease after lifecycle-log fallback active=%v err=%v", active, err)
	}
}

func TestCancelConflictsOnceTerminalFinalizationHasStarted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" {
			_, _ = w.Write([]byte(`{"files":[]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)
	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	wrapper := &blockingFinalizeRepository{
		Repository: db.CodexInspections,
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	db.CodexInspections = wrapper
	svc := newCodexInspectionTestService(t, db)
	started, err := svc.Start(context.Background(), RunRequest{TriggerType: model.CodexInspectionTriggerManual})
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	waitInspectionStarted(t, wrapper.entered)
	if _, err := svc.CancelRun(context.Background(), started.Run.ID); !errors.Is(err, ErrRunNotCancellable) {
		t.Fatalf("cancel during finalization error = %v, want ErrRunNotCancellable", err)
	}
	close(wrapper.release)
	completed := waitInspectionStatus(t, db, started.Run.ID, model.CodexInspectionStatusCompleted)
	if completed.FinishedAtMS == 0 {
		t.Fatalf("completed run = %#v", completed)
	}
}

func newBlockingInspectionServer(t *testing.T) (*httptest.Server, <-chan struct{}) {
	t.Helper()
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet {
			close(started)
			<-r.Context().Done()
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	return server, started
}

func waitInspectionStarted(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("inspection did not start")
	}
}

func waitInspectionStatus(t *testing.T, db *store.Store, runID int64, status string) model.CodexInspectionRun {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, found, err := db.GetCodexInspectionRun(context.Background(), runID)
		if err != nil {
			t.Fatalf("get run: %v", err)
		}
		if found && run.Status == status {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	run, _, _ := db.GetCodexInspectionRun(context.Background(), runID)
	t.Fatalf("run status = %q, want %q: %#v", run.Status, status, run)
	return model.CodexInspectionRun{}
}
