package usage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagearchive"
)

func TestArchiveJobContinuesAfterWaitingRequestIsCancelled(t *testing.T) {
	service, _, _ := newArchiveTestService(t, 1, 1, archiveTestServiceEvents(2))
	rootCtx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	if err := service.StartArchiveJobs(rootCtx); err != nil {
		t.Fatalf("start archive jobs: %v", err)
	}
	created, err := service.CreateArchive(context.Background(), 3_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	service.archive.testHook = func(point string) error {
		if point == "segment_published" {
			once.Do(func() { close(started) })
			<-release
		}
		return nil
	}
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, _, submitErr := service.SubmitArchiveResume(
			requestCtx,
			created.Run.ID,
			usagearchive.StatusArchiving,
			true,
		)
		result <- submitErr
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("archive job did not start")
	}
	persisted, err := service.ArchiveStatus(context.Background(), created.Run.ID)
	if err != nil || persisted.Run.ProgressPhase != usagearchive.ProgressArchivingRecords || persisted.Run.ProgressTotal != 2 {
		t.Fatalf("persisted active progress = %#v err=%v", persisted.Run, err)
	}
	cancelRequest()
	select {
	case submitErr := <-result:
		if !errors.Is(submitErr, context.Canceled) {
			t.Fatalf("waiting request error = %v, want context canceled", submitErr)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled waiting request did not return")
	}
	service.archiveJobs.mu.Lock()
	waiterCount := len(service.archiveJobs.waiters[archiveJobKey{
		runID: created.Run.ID,
		stage: usagearchive.StatusArchiving,
	}])
	service.archiveJobs.mu.Unlock()
	if waiterCount != 0 {
		t.Fatalf("cancelled waiting request retained %d waiters", waiterCount)
	}
	close(release)

	status := waitForArchiveRunStatus(t, service, created.Run.ID, usagearchive.StatusArchived)
	if status.Run.RequestedStage != "" {
		t.Fatalf("completed archive retained requested stage: %#v", status.Run)
	}
	if status.Run.ProgressPhase != "" {
		t.Fatalf("stable archived progress = %#v", status.Run)
	}
}

func TestArchiveBackgroundRecoveryRetriesTransientReadiness(t *testing.T) {
	service, st, rawDB, _ := newRawArchiveTestService(t, 1, 1)
	ctx := context.Background()
	if _, err := st.InsertEvents(ctx, archiveTestServiceEvents(1)); err != nil {
		t.Fatalf("insert archive event: %v", err)
	}
	if _, err := rawDB.ExecContext(ctx, `update usage_data_migrations set
		status = 'pending', last_error = null
		where name = 'usage_cache_accounting_v2'`); err != nil {
		t.Fatalf("mark migration pending: %v", err)
	}
	created, err := service.CreateArchive(ctx, 2_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	if _, newlyRequested, err := st.UsageArchives.RequestStage(
		ctx,
		created.Run.ID,
		usagearchive.StatusArchiving,
		time.Now().UnixMilli(),
	); err != nil || !newlyRequested {
		t.Fatalf("persist archive request: new=%t err=%v", newlyRequested, err)
	}

	service.archiveJobs.runAvailable(ctx)
	pending, err := service.ArchiveStatus(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("load pending archive: %v", err)
	}
	if pending.Run.Status != usagearchive.StatusPreviewed || pending.Run.RequestedStage != usagearchive.StatusArchiving {
		t.Fatalf("transient readiness cleared background request: %#v", pending.Run)
	}

	if _, err := rawDB.ExecContext(ctx, `update usage_data_migrations set
		status = 'completed', last_error = null
		where name = 'usage_cache_accounting_v2'`); err != nil {
		t.Fatalf("mark migration completed: %v", err)
	}
	service.archiveJobs.runAvailable(ctx)
	archived, err := service.ArchiveStatus(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("load retried archive: %v", err)
	}
	if archived.Run.Status != usagearchive.StatusArchived || archived.Run.RequestedStage != "" {
		t.Fatalf("retried archive = %#v", archived.Run)
	}
}

func TestArchiveSynchronousSubmissionDoesNotRetryAfterReadinessFailure(t *testing.T) {
	service, st, rawDB, _ := newRawArchiveTestService(t, 1, 1)
	ctx := context.Background()
	if _, err := st.InsertEvents(ctx, archiveTestServiceEvents(1)); err != nil {
		t.Fatalf("insert archive event: %v", err)
	}
	if _, err := rawDB.ExecContext(ctx, `update usage_data_migrations set
		status = 'pending', last_error = null
		where name = 'usage_cache_accounting_v2'`); err != nil {
		t.Fatalf("mark migration pending: %v", err)
	}
	created, err := service.CreateArchive(ctx, 2_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	rootCtx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	if err := service.StartArchiveJobs(rootCtx); err != nil {
		t.Fatalf("start archive jobs: %v", err)
	}
	if _, _, err := service.SubmitArchiveResume(
		ctx,
		created.Run.ID,
		usagearchive.StatusArchiving,
		true,
	); !errors.Is(err, ErrArchiveCoverageIncomplete) {
		t.Fatalf("synchronous archive error = %v, want coverage incomplete", err)
	}
	status, err := service.ArchiveStatus(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("load synchronous archive: %v", err)
	}
	if status.Run.Status != usagearchive.StatusPreviewed || status.Run.RequestedStage != "" {
		t.Fatalf("synchronous failure remained queued: %#v", status.Run)
	}
}

func TestArchiveStageBoundSubmissionDoesNotAdvanceStableStages(t *testing.T) {
	service, st, _ := newArchiveTestService(t, 1, 1, archiveTestServiceEvents(1))
	ctx := context.Background()
	rootCtx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	if err := service.StartArchiveJobs(rootCtx); err != nil {
		t.Fatalf("start archive jobs: %v", err)
	}
	created, err := service.CreateArchive(ctx, 2_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	archived, _, err := service.SubmitArchiveResume(ctx, created.Run.ID, usagearchive.StatusArchiving, true)
	if err != nil || archived.Run.Status != usagearchive.StatusArchived {
		t.Fatalf("archive run = %#v err=%v", archived, err)
	}
	stableArchived, queued, err := service.SubmitArchiveResume(
		ctx,
		created.Run.ID,
		usagearchive.StatusVerifying,
		true,
	)
	if err != nil || queued || stableArchived.Run.Status != usagearchive.StatusArchived {
		t.Fatalf("stable archived resume = %#v queued=%t err=%v", stableArchived, queued, err)
	}
	catchUpUsageAggregate(t, st)
	verified, _, err := service.SubmitArchiveVerification(ctx, created.Run.ID, true)
	if err != nil || verified.Run.Status != usagearchive.StatusVerified {
		t.Fatalf("verify run = %#v err=%v", verified, err)
	}
	stableVerified, queued, err := service.SubmitArchiveResume(
		ctx,
		created.Run.ID,
		usagearchive.StatusDeleting,
		true,
	)
	if err != nil || queued || stableVerified.Run.Status != usagearchive.StatusVerified || stableVerified.Run.DeletedEventCount != 0 {
		t.Fatalf("stable verified resume = %#v queued=%t err=%v", stableVerified, queued, err)
	}
}

func TestArchiveJobRecoversPersistedRequestWithoutAdvancingDestructiveStages(t *testing.T) {
	service, st, archiveDirectory := newArchiveTestService(t, 1, 1, archiveTestServiceEvents(2))
	created, err := service.CreateArchive(context.Background(), 3_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	if _, requested, err := st.UsageArchives.RequestStage(
		context.Background(),
		created.Run.ID,
		usagearchive.StatusArchiving,
		time.Now().UnixMilli(),
	); err != nil || !requested {
		t.Fatalf("persist archive request: requested=%t err=%v", requested, err)
	}

	restarted := New(st, WithArchive(ArchiveConfig{
		Directory:             archiveDirectory,
		SegmentEventLimit:     1,
		DeleteBatchSize:       1,
		AggregateReadsEnabled: true,
	}))
	rootCtx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	if err := restarted.StartArchiveJobs(rootCtx); err != nil {
		t.Fatalf("start restarted archive jobs: %v", err)
	}
	waitForArchiveRunStatus(t, restarted, created.Run.ID, usagearchive.StatusArchived)
	catchUpUsageAggregate(t, st)
	verified, _, err := restarted.SubmitArchiveVerification(context.Background(), created.Run.ID, true)
	if err != nil {
		t.Fatalf("verify recovered archive: %v", err)
	}
	if verified.Run.Status != usagearchive.StatusVerified {
		t.Fatalf("verified archive = %#v", verified)
	}

	time.Sleep(100 * time.Millisecond)
	stable, err := restarted.ArchiveStatus(context.Background(), created.Run.ID)
	if err != nil {
		t.Fatalf("load stable verified archive: %v", err)
	}
	if stable.Run.Status != usagearchive.StatusVerified || stable.Run.DeletedEventCount != 0 || stable.Run.RequestedStage != "" || stable.Run.ProgressPhase != "" {
		t.Fatalf("verified archive advanced without delete authorization: %#v", stable.Run)
	}
}

func TestArchiveJobDoesNotResumeCancelledRunAfterRestart(t *testing.T) {
	service, st, archiveDirectory := newArchiveTestService(t, 1, 1, archiveTestServiceEvents(2))
	ctx := context.Background()
	created, err := service.CreateArchive(ctx, 3_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	if _, requested, err := st.UsageArchives.RequestStage(
		ctx,
		created.Run.ID,
		usagearchive.StatusArchiving,
		time.Now().UnixMilli(),
	); err != nil || !requested {
		t.Fatalf("persist archive request: requested=%t err=%v", requested, err)
	}
	if cancelled, err := service.CancelArchive(ctx, created.Run.ID); err != nil || cancelled.Run.Status != usagearchive.StatusCancelled {
		t.Fatalf("cancel archive: status=%#v error=%v", cancelled, err)
	}

	restarted := New(st, WithArchive(ArchiveConfig{
		Directory:             archiveDirectory,
		SegmentEventLimit:     1,
		DeleteBatchSize:       1,
		AggregateReadsEnabled: true,
	}))
	rootCtx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	if err := restarted.StartArchiveJobs(rootCtx); err != nil {
		t.Fatalf("start restarted archive jobs: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	status, err := restarted.ArchiveStatus(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("load cancelled archive: %v", err)
	}
	if status.Run.Status != usagearchive.StatusCancelled || status.Run.RequestedStage != "" || len(status.Segments) != 0 {
		t.Fatalf("cancelled archive resumed after restart: %#v", status)
	}
}

func TestArchiveJobRunnerCanRestartAfterLifecycleEnds(t *testing.T) {
	service, _, _ := newArchiveTestService(t, 1, 1, archiveTestServiceEvents(1))
	if err := service.WaitArchiveJobs(context.Background()); err != nil {
		t.Fatalf("wait for unstarted runner: %v", err)
	}
	firstCtx, stopFirst := context.WithCancel(context.Background())
	if err := service.StartArchiveJobs(firstCtx); err != nil {
		t.Fatalf("start first archive job lifecycle: %v", err)
	}
	stopFirst()

	waitCtx, cancelWait := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelWait()
	if err := service.WaitArchiveJobs(waitCtx); err != nil {
		t.Fatalf("wait for first archive job lifecycle: %v", err)
	}

	secondCtx, stopSecond := context.WithCancel(context.Background())
	t.Cleanup(stopSecond)
	if err := service.StartArchiveJobs(secondCtx); err != nil {
		t.Fatalf("restart archive job lifecycle: %v", err)
	}
	created, err := service.CreateArchive(context.Background(), 2_000)
	if err != nil {
		t.Fatalf("create archive after restart: %v", err)
	}
	archived, queued, err := service.SubmitArchiveResume(
		context.Background(),
		created.Run.ID,
		usagearchive.StatusArchiving,
		true,
	)
	if err != nil || !queued || archived.Run.Status != usagearchive.StatusArchived {
		t.Fatalf("archive after runner restart = %#v queued=%t err=%v", archived, queued, err)
	}
	stopSecond()
	if err := service.WaitArchiveJobs(waitCtx); err != nil {
		t.Fatalf("wait for second archive job lifecycle: %v", err)
	}
}

func TestWaitArchiveJobsDrainsInFlightStageBeforeReturning(t *testing.T) {
	service, _, _ := newArchiveTestService(t, 1, 1, archiveTestServiceEvents(2))
	rootCtx, stop := context.WithCancel(context.Background())
	t.Cleanup(stop)
	if err := service.StartArchiveJobs(rootCtx); err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateArchive(context.Background(), 3_000)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var signalStarted, releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	service.archive.testHook = func(point string) error {
		if point == "segment_published" {
			signalStarted.Do(func() { close(started) })
			<-release
		}
		return nil
	}
	if _, _, err := service.SubmitArchiveResume(context.Background(), created.Run.ID, usagearchive.StatusArchiving, false); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("archive stage did not start")
	}
	stop()
	shortCtx, cancelShort := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShort()
	if err := service.WaitArchiveJobs(shortCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait returned while the cancelled stage was still running: %v", err)
	}
	unblock()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancelWait()
	if err := service.WaitArchiveJobs(waitCtx); err != nil {
		t.Fatalf("drain cancelled archive stage: %v", err)
	}
	service.archiveJobs.mu.Lock()
	defer service.archiveJobs.mu.Unlock()
	if service.archiveJobs.started || len(service.archiveJobs.inFlight) != 0 || len(service.archiveJobs.waiters) != 0 {
		t.Fatal("runner retained live work after WaitArchiveJobs returned")
	}
}

func waitForArchiveRunStatus(t *testing.T, service *Service, runID, want string) ArchiveStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := service.ArchiveStatus(context.Background(), runID)
		if err == nil && status.Run.Status == want && status.Run.RequestedStage == "" {
			return status
		}
		time.Sleep(10 * time.Millisecond)
	}
	status, err := service.ArchiveStatus(context.Background(), runID)
	t.Fatalf("archive status = %#v error = %v, want %s", status, err, want)
	return ArchiveStatus{}
}

func TestArchiveJobDoesNotRetryUnrestorableEventHash(t *testing.T) {
	service, st, _ := newArchiveTestService(t, 1, 1, archiveTestServiceEvents(1))
	ctx := context.Background()
	created, err := service.CreateArchive(ctx, 2_000)
	if err != nil {
		t.Fatalf("create archive: %v", err)
	}
	archived, err := service.ResumeArchiveAtStage(ctx, created.Run.ID, usagearchive.StatusArchiving)
	if err != nil || archived.Run.Status != usagearchive.StatusArchived {
		t.Fatalf("archive run: status=%#v err=%v", archived, err)
	}

	verifyingRun, err := st.UsageArchives.BeginVerification(ctx, created.Run.ID, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("begin verification: %v", err)
	}
	if verifyingRun.Status != usagearchive.StatusVerifying {
		t.Fatalf("verifying run status = %s, want %s", verifyingRun.Status, usagearchive.StatusVerifying)
	}
	if _, _, err := st.UsageArchives.RequestStage(
		ctx,
		created.Run.ID,
		usagearchive.StatusVerifying,
		time.Now().UnixMilli(),
	); err != nil {
		t.Fatalf("request verifying stage: %v", err)
	}

	key := archiveJobKey{
		runID: created.Run.ID,
		stage: usagearchive.StatusVerifying,
	}
	service.archiveJobs.mu.Lock()
	service.archiveJobs.retryOnError[key] = true
	service.archiveJobs.mu.Unlock()

	// 1. While still verifying (e.g. RecordFailure had a transient DB error),
	// errors joined with ErrArchiveUnrestorableEventHash must be retried
	simulatedDBErr := errors.Join(ErrArchiveUnrestorableEventHash, errors.New("simulated DB error"))
	if !service.archiveJobs.shouldRetry(key, simulatedDBErr) {
		t.Fatal("shouldRetry = false for verifying stage with joined unrestorable and DB error, want true")
	}

	failed, err := st.UsageArchives.RecordFailure(
		ctx,
		created.Run.ID,
		usagearchive.StatusVerifying,
		ErrArchiveUnrestorableEventHash,
		time.Now().UnixMilli(),
	)
	if err != nil {
		t.Fatalf("record failure: %v", err)
	}
	if failed.Status != usagearchive.StatusFailed || failed.ResumeStatus != usagearchive.StatusVerifying {
		t.Fatalf("failed run state = %#v", failed)
	}

	// 2. Once durable run has entered failed/verifying, ErrArchiveUnrestorableEventHash must NOT be retried
	if service.archiveJobs.shouldRetry(key, ErrArchiveUnrestorableEventHash) {
		t.Fatal("shouldRetry = true for failed/verifying with ErrArchiveUnrestorableEventHash, want false")
	}

	// 3. Ordinary transient ErrArchiveCoverageIncomplete must still be retried on failed/verifying
	if !service.archiveJobs.shouldRetry(key, ErrArchiveCoverageIncomplete) {
		t.Fatal("shouldRetry = false for failed/verifying with ErrArchiveCoverageIncomplete, want true")
	}

	// 4. Verify requested stage can be cleared and the runner will not claim it again
	if err := st.UsageArchives.ClearRequestedStage(ctx, created.Run.ID, usagearchive.StatusVerifying); err != nil {
		t.Fatalf("clear requested stage: %v", err)
	}
	persisted, err := st.UsageArchives.Run(ctx, created.Run.ID)
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if persisted.Status != usagearchive.StatusFailed || persisted.ResumeStatus != usagearchive.StatusVerifying || persisted.RequestedStage != "" {
		t.Fatalf("run state after permanent error request clear = %#v", persisted)
	}
	nextRun, _, found, err := service.archiveJobs.claimNext(ctx)
	if err != nil {
		t.Fatalf("claim next run: %v", err)
	}
	if found {
		t.Fatalf("expected no run to be claimed after request cleared, claimed %#v", nextRun)
	}
}
