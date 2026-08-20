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
	if stable.Run.Status != usagearchive.StatusVerified || stable.Run.DeletedEventCount != 0 || stable.Run.RequestedStage != "" {
		t.Fatalf("verified archive advanced without delete authorization: %#v", stable.Run)
	}
}

func TestArchiveJobRunnerCanRestartAfterLifecycleEnds(t *testing.T) {
	service, _, _ := newArchiveTestService(t, 1, 1, archiveTestServiceEvents(1))
	firstCtx, stopFirst := context.WithCancel(context.Background())
	if err := service.StartArchiveJobs(firstCtx); err != nil {
		t.Fatalf("start first archive job lifecycle: %v", err)
	}
	stopFirst()

	deadline := time.Now().Add(3 * time.Second)
	for {
		service.archiveJobs.mu.Lock()
		started := service.archiveJobs.started
		service.archiveJobs.mu.Unlock()
		if !started {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first archive job lifecycle did not stop")
		}
		time.Sleep(10 * time.Millisecond)
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
