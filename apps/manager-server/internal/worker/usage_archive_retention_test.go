package worker

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	usagearchive "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagearchive"
	usageservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
	usageparser "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestUsageArchiveRetentionWorkerIsDisabledByNonPositiveRetentionDays(t *testing.T) {
	service := &fakeUsageArchiveRetentionService{}
	worker := NewUsageArchiveRetentionWorker(service, 0)
	worker.runOnce(context.Background())
	if len(service.calls) != 0 {
		t.Fatalf("disabled worker calls = %#v", service.calls)
	}
}

func TestUsageArchiveRetentionWorkerCompletesRetentionRun(t *testing.T) {
	service := &fakeUsageArchiveRetentionService{}
	worker := NewUsageArchiveRetentionWorker(service, 30)
	worker.now = func() time.Time { return time.UnixMilli(4_000_000_000) }
	worker.runOnce(context.Background())

	wantCalls := []string{
		"active",
		"create:1408000000",
		"resume:previewed",
		"verify:archived",
		"resume:verified",
		"delete:verified",
		"resume:completed",
	}
	if !reflect.DeepEqual(service.calls, wantCalls) {
		t.Fatalf("retention calls = %#v, want %#v", service.calls, wantCalls)
	}
	if service.run.Mode != usagearchive.RunModeRetention || service.run.Status != usagearchive.StatusCompleted {
		t.Fatalf("retention run = %#v", service.run)
	}
}

func TestUsageArchiveRetentionWorkerDoesNotTakeOverManualRun(t *testing.T) {
	service := &fakeUsageArchiveRetentionService{
		found: true,
		run: store.UsageArchiveRun{
			ID:     "manual-run",
			Mode:   usagearchive.RunModeManual,
			Status: usagearchive.StatusArchiving,
		},
	}
	worker := NewUsageArchiveRetentionWorker(service, 30)
	if !worker.runOnce(context.Background()) {
		t.Fatal("manual run did not request an early retry")
	}
	if !reflect.DeepEqual(service.calls, []string{"active"}) {
		t.Fatalf("manual run calls = %#v", service.calls)
	}
	if service.run.Status != usagearchive.StatusArchiving {
		t.Fatalf("manual run was changed = %#v", service.run)
	}
}

func TestUsageArchiveRetentionWorkerResumesFailedRetentionRun(t *testing.T) {
	for _, test := range []struct {
		name         string
		resumeStatus string
		wantCalls    []string
	}{
		{
			name:         "archiving",
			resumeStatus: usagearchive.StatusArchiving,
			wantCalls:    []string{"active", "resume:failed", "verify:archived", "resume:verified", "delete:verified", "resume:completed"},
		},
		{
			name:         "verifying",
			resumeStatus: usagearchive.StatusVerifying,
			wantCalls:    []string{"active", "resume:failed", "delete:verified", "resume:completed"},
		},
		{
			name:         "deleting",
			resumeStatus: usagearchive.StatusDeleting,
			wantCalls:    []string{"active", "resume:failed"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeUsageArchiveRetentionService{
				found: true,
				run: store.UsageArchiveRun{
					ID:           "retention-run",
					Mode:         usagearchive.RunModeRetention,
					Status:       usagearchive.StatusFailed,
					ResumeStatus: test.resumeStatus,
				},
			}
			worker := NewUsageArchiveRetentionWorker(service, 30)
			worker.runOnce(context.Background())
			if !reflect.DeepEqual(service.calls, test.wantCalls) {
				t.Fatalf("restart calls = %#v, want %#v", service.calls, test.wantCalls)
			}
			if service.run.Status != usagearchive.StatusCompleted {
				t.Fatalf("resumed retention run = %#v", service.run)
			}
		})
	}
}

func TestUsageArchiveRetentionWorkerResumesActiveStages(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    string
		wantCalls []string
	}{
		{
			name:      "verifying",
			status:    usagearchive.StatusVerifying,
			wantCalls: []string{"active", "resume:verifying", "delete:verified", "resume:completed"},
		},
		{
			name:      "deleting",
			status:    usagearchive.StatusDeleting,
			wantCalls: []string{"active", "resume:deleting"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeUsageArchiveRetentionService{
				found: true,
				run: store.UsageArchiveRun{
					ID:     "retention-run",
					Mode:   usagearchive.RunModeRetention,
					Status: test.status,
				},
			}
			worker := NewUsageArchiveRetentionWorker(service, 30)
			worker.runOnce(context.Background())
			if !reflect.DeepEqual(service.calls, test.wantCalls) {
				t.Fatalf("resume calls = %#v, want %#v", service.calls, test.wantCalls)
			}
			if service.run.Status != usagearchive.StatusCompleted {
				t.Fatalf("resumed retention run = %#v", service.run)
			}
		})
	}
}

func TestUsageArchiveRetentionWorkerSkipsNoEventsWithoutCreatingRun(t *testing.T) {
	service := &fakeUsageArchiveRetentionService{createErr: usagearchive.ErrNoEvents}
	worker := NewUsageArchiveRetentionWorker(service, 30)
	worker.now = func() time.Time { return time.UnixMilli(4_000_000_000) }
	if worker.runOnce(context.Background()) {
		t.Fatal("no-events retention unexpectedly requested an early retry")
	}
	if !reflect.DeepEqual(service.calls, []string{"active", "create:1408000000"}) {
		t.Fatalf("no-events calls = %#v", service.calls)
	}
}

func TestUsageArchiveRetentionWorkerSkipsCutoffBeforeUnixEpoch(t *testing.T) {
	service := &fakeUsageArchiveRetentionService{}
	worker := NewUsageArchiveRetentionWorker(service, 30)
	worker.now = func() time.Time { return time.UnixMilli(int64(30 * 24 * time.Hour / time.Millisecond)) }
	if worker.runOnce(context.Background()) {
		t.Fatal("unsupported cutoff unexpectedly requested an early retry")
	}
	if !reflect.DeepEqual(service.calls, []string{"active"}) {
		t.Fatalf("unsupported cutoff calls = %#v", service.calls)
	}
}

func TestUsageArchiveRetentionWorkerRetriesFailedAutomaticRunSoon(t *testing.T) {
	resumeErr := errors.New("derived coverage is still catching up")
	service := &fakeUsageArchiveRetentionService{
		found:     true,
		resumeErr: resumeErr,
		run: store.UsageArchiveRun{
			ID:     "retention-run",
			Mode:   usagearchive.RunModeRetention,
			Status: usagearchive.StatusDeleting,
		},
	}
	worker := NewUsageArchiveRetentionWorker(service, 30)
	if !worker.runOnce(context.Background()) {
		t.Fatal("failed automatic run did not request an early retry")
	}
	if !reflect.DeepEqual(service.calls, []string{"active", "resume:deleting"}) {
		t.Fatalf("failed automatic run calls = %#v", service.calls)
	}
}

func TestUsageArchiveRetentionWorkerStartIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := &blockingRetentionService{
		started:  make(chan struct{}),
		returned: make(chan struct{}, 1),
		release:  make(chan struct{}),
	}
	worker := NewUsageArchiveRetentionWorker(service, 30)
	worker.interval = time.Hour
	worker.Start(ctx)
	worker.Start(ctx)

	select {
	case <-service.started:
	case <-time.After(time.Second):
		t.Fatal("retention worker did not start")
	}
	cancel()
	select {
	case <-service.returned:
	case <-time.After(time.Second):
		t.Fatal("retention worker did not propagate cancellation")
	}
	if got := service.calls.Load(); got != 1 {
		t.Fatalf("ActiveArchiveRun calls = %d, want 1", got)
	}
}

func TestUsageArchiveRetentionWorkerLoopUsesShortRetryAfterFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service := &retryingRetentionService{secondAttempt: make(chan struct{})}
	worker := NewUsageArchiveRetentionWorker(service, 30)
	worker.interval = time.Hour
	worker.retryInterval = time.Millisecond
	worker.Start(ctx)

	select {
	case <-service.secondAttempt:
	case <-time.After(time.Second):
		t.Fatal("retention worker did not use the short retry interval")
	}
	cancel()
}

func TestUsageArchiveRetentionWorkerResumesPersistedStagesAfterStoreRestart(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, *retentionWorkerFixture)
	}{
		{
			name: "failed archiving",
			prepare: func(t *testing.T, fixture *retentionWorkerFixture) {
				nowMS := time.Now().UnixMilli()
				if _, err := fixture.store.UsageArchives.BeginArchive(context.Background(), fixture.runID, nowMS); err != nil {
					t.Fatalf("begin archive: %v", err)
				}
				if _, err := fixture.store.UsageArchives.RecordFailure(
					context.Background(),
					fixture.runID,
					usagearchive.StatusArchiving,
					errors.New("simulated archiving restart"),
					nowMS+1,
				); err != nil {
					t.Fatalf("record archiving failure: %v", err)
				}
			},
		},
		{
			name: "active verification",
			prepare: func(t *testing.T, fixture *retentionWorkerFixture) {
				if _, err := fixture.service.ResumeArchive(context.Background(), fixture.runID); err != nil {
					t.Fatalf("archive before verification restart: %v", err)
				}
				if _, err := fixture.store.UsageArchives.BeginVerification(
					context.Background(),
					fixture.runID,
					time.Now().UnixMilli(),
				); err != nil {
					t.Fatalf("begin verification: %v", err)
				}
			},
		},
		{
			name: "failed bounded delete",
			prepare: func(t *testing.T, fixture *retentionWorkerFixture) {
				ctx := context.Background()
				if _, err := fixture.service.ResumeArchive(ctx, fixture.runID); err != nil {
					t.Fatalf("archive before delete restart: %v", err)
				}
				if _, err := fixture.service.VerifyArchive(ctx, fixture.runID); err != nil {
					t.Fatalf("verify before delete restart: %v", err)
				}
				nowMS := time.Now().UnixMilli()
				if _, err := fixture.store.UsageArchives.BeginDelete(ctx, fixture.runID, nowMS); err != nil {
					t.Fatalf("begin delete: %v", err)
				}
				first, err := fixture.store.UsageArchives.DeleteBatch(ctx, fixture.runID, 1, nowMS+1)
				if err != nil {
					t.Fatalf("delete first batch: %v", err)
				}
				if first.Deleted != 1 || first.Completed {
					t.Fatalf("first delete batch = %#v", first)
				}
				if _, err := fixture.store.UsageArchives.RecordFailure(
					ctx,
					fixture.runID,
					usagearchive.StatusDeleting,
					errors.New("simulated delete restart"),
					nowMS+2,
				); err != nil {
					t.Fatalf("record delete failure: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRetentionWorkerFixture(t, 3)
			test.prepare(t, fixture)
			completed := resumeRetentionWorkerFixtureAfterRestart(t, fixture)
			if completed.Status != usagearchive.StatusCompleted ||
				completed.Mode != usagearchive.RunModeRetention ||
				completed.DeletedEventCount != fixture.eventCount ||
				completed.ResumeStatus != "" || completed.LastError != "" {
				t.Fatalf("completed retention run = %#v", completed)
			}
		})
	}
}

type retentionWorkerFixture struct {
	dbPath           string
	archiveDirectory string
	store            *store.Store
	service          *usageservice.Service
	runID            string
	eventCount       int64
}

func newRetentionWorkerFixture(t *testing.T, eventCount int) *retentionWorkerFixture {
	t.Helper()
	cfg := testutil.NewConfig(t)
	st := testutil.NewStore(t, cfg)
	events := retentionWorkerEvents(eventCount)
	inserted, err := st.InsertEvents(context.Background(), events)
	if err != nil {
		t.Fatalf("insert retention events: %v", err)
	}
	if inserted.Inserted != eventCount {
		t.Fatalf("insert retention events = %#v", inserted)
	}
	catchUpRetentionWorkerReadiness(t, st)
	service := usageservice.New(st, usageservice.WithArchive(usageservice.ArchiveConfig{
		Directory:             cfg.UsageArchiveDir,
		SegmentEventLimit:     2,
		DeleteBatchSize:       1,
		AggregateReadsEnabled: true,
	}))
	created, err := service.CreateRetentionArchive(context.Background(), int64(eventCount+1)*1_000)
	if err != nil {
		t.Fatalf("create retention run: %v", err)
	}
	return &retentionWorkerFixture{
		dbPath:           cfg.DBPath,
		archiveDirectory: cfg.UsageArchiveDir,
		store:            st,
		service:          service,
		runID:            created.Run.ID,
		eventCount:       int64(eventCount),
	}
}

func resumeRetentionWorkerFixtureAfterRestart(t *testing.T, fixture *retentionWorkerFixture) store.UsageArchiveRun {
	t.Helper()
	if err := fixture.store.Close(); err != nil {
		t.Fatalf("close retention store: %v", err)
	}
	restartedStore, err := store.Open(fixture.dbPath)
	if err != nil {
		t.Fatalf("reopen retention store: %v", err)
	}
	t.Cleanup(func() { _ = restartedStore.Close() })
	restartedService := usageservice.New(restartedStore, usageservice.WithArchive(usageservice.ArchiveConfig{
		Directory:             fixture.archiveDirectory,
		SegmentEventLimit:     2,
		DeleteBatchSize:       1,
		AggregateReadsEnabled: true,
	}))
	worker := NewUsageArchiveRetentionWorker(restartedService, 30)
	if worker.runOnce(context.Background()) {
		t.Fatal("successful restarted retention run unexpectedly requested a retry")
	}
	completed, err := restartedStore.UsageArchives.Run(context.Background(), fixture.runID)
	if err != nil {
		t.Fatalf("read completed retention run: %v", err)
	}
	counts, err := restartedStore.UsageMaintenanceCounts(context.Background())
	if err != nil {
		t.Fatalf("read retention maintenance counts: %v", err)
	}
	if counts.RawEventCount != 0 || counts.RawDeletedEventCount != fixture.eventCount {
		t.Fatalf("retention maintenance counts = %#v", counts)
	}
	if active, found, err := restartedStore.ActiveUsageArchiveRun(context.Background()); err != nil || found {
		t.Fatalf("completed retention active run = %#v found=%v err=%v", active, found, err)
	}
	return completed
}

func retentionWorkerEvents(count int) []usageparser.Event {
	events := make([]usageparser.Event, 0, count)
	for index := range count {
		timestampMS := int64(index+1) * 1_000
		events = append(events, usageparser.Event{
			RequestID:    "retention-request-" + strconv.Itoa(index),
			EventHash:    "retention-event-" + strconv.Itoa(index),
			TimestampMS:  timestampMS,
			Timestamp:    time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
			Provider:     "codex",
			ExecutorType: "CodexExecutor",
			Model:        "gpt-test",
			InputTokens:  int64(100 + index),
			OutputTokens: int64(20 + index),
			TotalTokens:  int64(120 + index*2),
			CreatedAtMS:  timestampMS,
		})
	}
	return events
}

func catchUpRetentionWorkerReadiness(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	nowMS := time.Now().UnixMilli()
	aggregateComplete := false
	for attempt := 0; attempt < 100; attempt++ {
		result, err := st.CatchUpUsageHourlyAggregate(ctx, 100, nowMS+int64(attempt))
		if err != nil {
			t.Fatalf("catch up usage aggregate: %v", err)
		}
		if !result.Pending {
			aggregateComplete = true
			break
		}
	}
	if !aggregateComplete {
		t.Fatal("usage aggregate catch-up did not complete")
	}

	pricingComplete := false
	for attempt := 0; attempt < 100; attempt++ {
		result, err := st.CatchUpUsagePricing(ctx, 100, nowMS+100+int64(attempt))
		if err != nil {
			t.Fatalf("catch up usage pricing: %v", err)
		}
		if !result.Pending {
			pricingComplete = true
			break
		}
	}
	if !pricingComplete {
		t.Fatal("usage pricing catch-up did not complete")
	}

	for _, catchUp := range []struct {
		name string
		run  func(context.Context, int, int64) (store.UsageMonitoringCatchUpResult, error)
	}{
		{name: "stats", run: st.CatchUpUsageMonitoringStats},
		{name: "metadata", run: st.CatchUpUsageMonitoringMetadata},
		{name: "projection", run: st.CatchUpUsageMonitoringProjection},
	} {
		completed := false
		for attempt := 0; attempt < 100; attempt++ {
			result, err := catchUp.run(ctx, 100, nowMS+200+int64(attempt))
			if err != nil {
				t.Fatalf("catch up usage monitoring %s: %v", catchUp.name, err)
			}
			if !result.Pending {
				completed = true
				break
			}
		}
		if !completed {
			t.Fatalf("usage monitoring %s catch-up did not complete", catchUp.name)
		}
	}

	for _, catchUp := range []struct {
		name string
		run  func(context.Context, int, int64) (store.UsageRollupCatchUpResult, error)
	}{
		{name: "account history", run: st.CatchUpAccountHistoryRollups},
		{name: "dashboard hourly", run: st.CatchUpDashboardHourlyRollups},
	} {
		completed := false
		for attempt := 0; attempt < 100; attempt++ {
			result, err := catchUp.run(ctx, 100, nowMS+300+int64(attempt))
			if err != nil {
				t.Fatalf("catch up %s: %v", catchUp.name, err)
			}
			if !result.Pending {
				completed = true
				break
			}
		}
		if !completed {
			t.Fatalf("%s catch-up did not complete", catchUp.name)
		}
	}
}

type fakeUsageArchiveRetentionService struct {
	found     bool
	run       store.UsageArchiveRun
	createErr error
	resumeErr error
	calls     []string
}

type blockingRetentionService struct {
	calls    atomic.Int32
	started  chan struct{}
	returned chan struct{}
	release  chan struct{}
}

type retryingRetentionService struct {
	calls         atomic.Int32
	secondAttempt chan struct{}
}

func (f *blockingRetentionService) ActiveArchiveRun(ctx context.Context) (store.UsageArchiveRun, bool, error) {
	if f.calls.Add(1) == 1 {
		close(f.started)
	}
	select {
	case <-f.release:
	case <-ctx.Done():
	}
	select {
	case f.returned <- struct{}{}:
	default:
	}
	return store.UsageArchiveRun{}, false, errors.New("probe complete")
}

func (f *blockingRetentionService) CreateRetentionArchive(context.Context, int64) (usageservice.ArchiveStatus, error) {
	return usageservice.ArchiveStatus{}, errors.New("unexpected create")
}

func (f *blockingRetentionService) ResumeArchive(context.Context, string) (usageservice.ArchiveStatus, error) {
	return usageservice.ArchiveStatus{}, errors.New("unexpected resume")
}

func (f *blockingRetentionService) VerifyArchive(context.Context, string) (usageservice.ArchiveStatus, error) {
	return usageservice.ArchiveStatus{}, errors.New("unexpected verify")
}

func (f *blockingRetentionService) DeleteArchive(context.Context, string) (usageservice.ArchiveStatus, error) {
	return usageservice.ArchiveStatus{}, errors.New("unexpected delete")
}

func (f *retryingRetentionService) ActiveArchiveRun(context.Context) (store.UsageArchiveRun, bool, error) {
	if f.calls.Add(1) == 2 {
		close(f.secondAttempt)
	}
	return store.UsageArchiveRun{}, false, errors.New("temporary inspection failure")
}

func (f *retryingRetentionService) CreateRetentionArchive(context.Context, int64) (usageservice.ArchiveStatus, error) {
	return usageservice.ArchiveStatus{}, errors.New("unexpected create")
}

func (f *retryingRetentionService) ResumeArchive(context.Context, string) (usageservice.ArchiveStatus, error) {
	return usageservice.ArchiveStatus{}, errors.New("unexpected resume")
}

func (f *retryingRetentionService) VerifyArchive(context.Context, string) (usageservice.ArchiveStatus, error) {
	return usageservice.ArchiveStatus{}, errors.New("unexpected verify")
}

func (f *retryingRetentionService) DeleteArchive(context.Context, string) (usageservice.ArchiveStatus, error) {
	return usageservice.ArchiveStatus{}, errors.New("unexpected delete")
}

func (f *fakeUsageArchiveRetentionService) ActiveArchiveRun(context.Context) (store.UsageArchiveRun, bool, error) {
	f.calls = append(f.calls, "active")
	return f.run, f.found, nil
}

func (f *fakeUsageArchiveRetentionService) CreateRetentionArchive(_ context.Context, cutoffMS int64) (usageservice.ArchiveStatus, error) {
	f.calls = append(f.calls, "create:"+formatTestInt(cutoffMS))
	if f.createErr != nil {
		return usageservice.ArchiveStatus{}, f.createErr
	}
	f.found = true
	f.run = store.UsageArchiveRun{ID: "retention-run", Mode: usagearchive.RunModeRetention, Status: usagearchive.StatusPreviewed}
	return usageservice.ArchiveStatus{Run: f.run}, nil
}

func (f *fakeUsageArchiveRetentionService) ResumeArchive(context.Context, string) (usageservice.ArchiveStatus, error) {
	f.calls = append(f.calls, "resume:"+f.run.Status)
	if f.resumeErr != nil {
		return usageservice.ArchiveStatus{}, f.resumeErr
	}
	switch f.run.Status {
	case usagearchive.StatusPreviewed, usagearchive.StatusArchiving:
		f.run.Status = usagearchive.StatusArchived
	case usagearchive.StatusVerifying:
		f.run.Status = usagearchive.StatusVerified
	case usagearchive.StatusDeleting:
		f.run.Status = usagearchive.StatusCompleted
	case usagearchive.StatusFailed:
		if f.run.ResumeStatus != usagearchive.StatusArchiving &&
			f.run.ResumeStatus != usagearchive.StatusVerifying &&
			f.run.ResumeStatus != usagearchive.StatusDeleting {
			return usageservice.ArchiveStatus{}, errors.New("unexpected failed resume status")
		}
		if f.run.ResumeStatus == usagearchive.StatusArchiving {
			f.run.Status = usagearchive.StatusArchived
		} else if f.run.ResumeStatus == usagearchive.StatusVerifying {
			f.run.Status = usagearchive.StatusVerified
		} else {
			f.run.Status = usagearchive.StatusCompleted
		}
	case usagearchive.StatusArchived:
		// Verification is performed by the worker in the next call.
	case usagearchive.StatusVerified:
		// Deletion is performed by the worker in the next call.
	case usagearchive.StatusCompleted:
	default:
		return usageservice.ArchiveStatus{}, errors.New("unexpected retention status")
	}
	return usageservice.ArchiveStatus{Run: f.run}, nil
}

func (f *fakeUsageArchiveRetentionService) VerifyArchive(context.Context, string) (usageservice.ArchiveStatus, error) {
	f.calls = append(f.calls, "verify:"+f.run.Status)
	if f.run.Status != usagearchive.StatusArchived {
		return usageservice.ArchiveStatus{}, errors.New("verify called in unexpected state")
	}
	f.run.Status = usagearchive.StatusVerified
	return usageservice.ArchiveStatus{Run: f.run}, nil
}

func (f *fakeUsageArchiveRetentionService) DeleteArchive(context.Context, string) (usageservice.ArchiveStatus, error) {
	f.calls = append(f.calls, "delete:"+f.run.Status)
	if f.run.Status != usagearchive.StatusVerified {
		return usageservice.ArchiveStatus{}, errors.New("delete called in unexpected state")
	}
	f.run.Status = usagearchive.StatusCompleted
	return usageservice.ArchiveStatus{Run: f.run}, nil
}

func formatTestInt(value int64) string {
	return strconv.FormatInt(value, 10)
}
