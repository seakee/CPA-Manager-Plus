package usage

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagearchive"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const archiveJobScanInterval = 5 * time.Second

type archiveJobResult struct {
	status ArchiveStatus
	err    error
}

type archiveJobKey struct {
	runID string
	stage string
}

type archiveJobRunner struct {
	service *Service
	wake    chan struct{}

	mu           sync.Mutex
	started      bool
	rootCtx      context.Context
	recovered    bool
	inFlight     map[archiveJobKey]struct{}
	waiters      map[archiveJobKey][]chan archiveJobResult
	retryOnError map[archiveJobKey]bool
}

func newArchiveJobRunner(service *Service) *archiveJobRunner {
	return &archiveJobRunner{
		service:      service,
		wake:         make(chan struct{}, 1),
		inFlight:     make(map[archiveJobKey]struct{}),
		waiters:      make(map[archiveJobKey][]chan archiveJobResult),
		retryOnError: make(map[archiveJobKey]bool),
	}
}

func (s *Service) StartArchiveJobs(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := s.requireArchiveManager(); err != nil {
		return err
	}
	if s.archiveJobs == nil {
		return ErrArchiveUnavailable
	}
	runner := s.archiveJobs
	runner.mu.Lock()
	if runner.started {
		active := runner.rootCtx != nil && runner.rootCtx.Err() == nil
		runner.mu.Unlock()
		if active {
			return nil
		}
		return ErrArchiveUnavailable
	}
	runner.started = true
	runner.rootCtx = ctx
	runner.mu.Unlock()
	go runner.loop(ctx)
	runner.wakeRunner()
	return nil
}

func (s *Service) SubmitArchiveResume(
	ctx context.Context,
	runID string,
	expectedStage string,
	wait bool,
) (ArchiveStatus, bool, error) {
	status, err := s.ArchiveStatus(ctx, runID)
	if err != nil {
		return ArchiveStatus{}, false, err
	}
	currentOrder := archiveRunResumeStageOrder(status.Run)
	if expectedStage != "" {
		expectedOrder := archiveResumeStageOrder(expectedStage)
		if expectedOrder == 0 {
			return ArchiveStatus{}, false, fmt.Errorf("%w: invalid expected archive resume stage %q", ErrArchiveInvalidRequest, expectedStage)
		}
		if currentOrder > expectedOrder {
			return status, false, nil
		}
		if currentOrder == 0 || currentOrder < expectedOrder {
			return ArchiveStatus{}, false, fmt.Errorf(
				"%w: cannot resume expected %s stage from %s",
				ErrArchiveInvalidState,
				expectedStage,
				status.Run.Status,
			)
		}
	}
	stage := archiveRunRequestedStage(status.Run)
	if stage == "" {
		return status, false, nil
	}
	return s.submitArchiveStage(ctx, runID, stage, wait)
}

func (s *Service) SubmitArchiveVerification(ctx context.Context, runID string, wait bool) (ArchiveStatus, bool, error) {
	return s.submitArchiveStage(ctx, runID, usagearchive.StatusVerifying, wait)
}

func (s *Service) SubmitArchiveDeletion(ctx context.Context, runID string, wait bool) (ArchiveStatus, bool, error) {
	manager, err := s.requireArchiveManager()
	if err != nil {
		return ArchiveStatus{}, false, err
	}
	if !manager.config.AggregateReadsEnabled {
		return ArchiveStatus{}, false, ErrArchiveDeleteUnavailable
	}
	return s.submitArchiveStage(ctx, runID, usagearchive.StatusDeleting, wait)
}

func archiveRunRequestedStage(run store.UsageArchiveRun) string {
	if run.Status == usagearchive.StatusFailed {
		return run.ResumeStatus
	}
	switch run.Status {
	case usagearchive.StatusPreviewed, usagearchive.StatusArchiving:
		return usagearchive.StatusArchiving
	case usagearchive.StatusVerifying:
		return usagearchive.StatusVerifying
	case usagearchive.StatusDeleting:
		return usagearchive.StatusDeleting
	default:
		return ""
	}
}

func (s *Service) submitArchiveStage(ctx context.Context, runID, stage string, wait bool) (ArchiveStatus, bool, error) {
	if !validArchiveRunID(runID) {
		return ArchiveStatus{}, false, ErrArchiveInvalidID
	}
	if archiveResumeStageOrder(stage) == 0 {
		return ArchiveStatus{}, false, fmt.Errorf("%w: invalid archive stage", ErrArchiveInvalidRequest)
	}
	if s.archiveJobs == nil || !s.archiveJobs.isStarted() {
		if !wait {
			return ArchiveStatus{}, false, ErrArchiveUnavailable
		}
		status, err := s.executeArchiveStage(ctx, runID, stage)
		return status, false, err
	}
	return s.archiveJobs.submit(ctx, runID, stage, wait)
}

func (s *Service) executeArchiveStage(ctx context.Context, runID, stage string) (ArchiveStatus, error) {
	switch stage {
	case usagearchive.StatusArchiving:
		return s.ResumeArchiveAtStage(ctx, runID, stage)
	case usagearchive.StatusVerifying:
		return s.VerifyArchive(ctx, runID)
	case usagearchive.StatusDeleting:
		return s.DeleteArchive(ctx, runID)
	default:
		return ArchiveStatus{}, fmt.Errorf("%w: invalid archive stage", ErrArchiveInvalidRequest)
	}
}

func (r *archiveJobRunner) isStarted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started && r.rootCtx != nil && r.rootCtx.Err() == nil
}

func (r *archiveJobRunner) submit(ctx context.Context, runID, stage string, wait bool) (ArchiveStatus, bool, error) {
	key := archiveJobKey{runID: runID, stage: stage}
	r.mu.Lock()
	if !r.started || r.rootCtx == nil || r.rootCtx.Err() != nil {
		r.mu.Unlock()
		return ArchiveStatus{}, false, ErrArchiveUnavailable
	}
	run, newlyRequested, err := r.service.archive.store.UsageArchives.RequestStage(ctx, runID, stage, time.Now().UnixMilli())
	if err != nil {
		r.mu.Unlock()
		return ArchiveStatus{}, false, err
	}
	needsRun := run.RequestedStage == stage
	if !needsRun {
		r.mu.Unlock()
		status, err := r.service.archive.status(ctx, run.ID)
		return status, false, err
	}
	if newlyRequested {
		r.retryOnError[key] = !wait
	} else if !wait {
		r.retryOnError[key] = true
	}
	var waiter chan archiveJobResult
	if wait {
		waiter = make(chan archiveJobResult, 1)
		r.waiters[key] = append(r.waiters[key], waiter)
	}
	r.mu.Unlock()
	r.wakeRunner()
	if !wait {
		return ArchiveStatus{Run: run}, true, nil
	}
	select {
	case result := <-waiter:
		return result.status, true, result.err
	case <-ctx.Done():
		r.removeWaiter(key, waiter)
		return ArchiveStatus{}, true, ctx.Err()
	}
}

func (r *archiveJobRunner) removeWaiter(key archiveJobKey, waiter chan archiveJobResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	waiters := r.waiters[key]
	for index, candidate := range waiters {
		if candidate != waiter {
			continue
		}
		waiters = append(waiters[:index], waiters[index+1:]...)
		if len(waiters) == 0 {
			delete(r.waiters, key)
		} else {
			r.waiters[key] = waiters
		}
		return
	}
}

func (r *archiveJobRunner) wakeRunner() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *archiveJobRunner) loop(ctx context.Context) {
	ticker := time.NewTicker(archiveJobScanInterval)
	defer func() {
		ticker.Stop()
		r.stop(ctx.Err())
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-ticker.C:
		}
		if !r.recoverRequestedStages(ctx) {
			continue
		}
		r.runAvailable(ctx)
	}
}

func (r *archiveJobRunner) stop(stopErr error) {
	if stopErr == nil {
		stopErr = ErrArchiveUnavailable
	}
	r.mu.Lock()
	waiters := make([]chan archiveJobResult, 0)
	for key, keyWaiters := range r.waiters {
		waiters = append(waiters, keyWaiters...)
		delete(r.waiters, key)
	}
	clear(r.inFlight)
	clear(r.retryOnError)
	r.started = false
	r.rootCtx = nil
	r.recovered = false
	r.mu.Unlock()
	for _, waiter := range waiters {
		waiter <- archiveJobResult{err: stopErr}
	}
}

func (r *archiveJobRunner) recoverRequestedStages(ctx context.Context) bool {
	if r.recovered {
		return true
	}
	if err := r.service.archive.store.UsageArchives.RecoverRequestedStages(ctx); err != nil {
		if ctx.Err() == nil {
			log.Printf("[usage-archive] recover requested stages; will retry: %v", err)
		}
		return false
	}
	r.recovered = true
	return true
}

func (r *archiveJobRunner) runAvailable(ctx context.Context) {
	for ctx.Err() == nil {
		run, key, found, err := r.claimNext(ctx)
		if err != nil {
			log.Printf("[usage-archive] inspect requested stage: %v", err)
			return
		}
		if !found {
			return
		}
		status, runErr := r.service.executeArchiveStage(ctx, run.ID, key.stage)
		retry := runErr != nil && ctx.Err() == nil && r.shouldRetry(key, runErr)
		clearRequest := !retry && (runErr == nil || ctx.Err() == nil)
		clearFailed := false
		if clearRequest {
			clearCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			clearErr := r.service.archive.store.UsageArchives.ClearRequestedStage(clearCtx, run.ID, key.stage)
			cancel()
			if clearErr != nil {
				clearFailed = true
				runErr = errors.Join(runErr, fmt.Errorf("clear requested usage archive stage: %w", clearErr))
			}
		}
		if runErr != nil && ctx.Err() == nil {
			log.Printf("[usage-archive] process run %s stage %s: %v", run.ID, key.stage, runErr)
		}
		r.finish(key, archiveJobResult{status: status, err: runErr}, retry || clearFailed)
		if ctx.Err() != nil {
			return
		}
		if retry || clearFailed {
			return
		}
	}
}

func (r *archiveJobRunner) shouldRetry(key archiveJobKey, runErr error) bool {
	r.mu.Lock()
	retry, known := r.retryOnError[key]
	r.mu.Unlock()
	if known && !retry {
		return false
	}

	statusCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	run, err := r.service.archive.store.UsageArchives.Run(statusCtx, key.runID)
	if err != nil {
		log.Printf("[usage-archive] inspect retry state for run %s stage %s after %v: %v", key.runID, key.stage, runErr, err)
		return true
	}
	if run.RequestedStage != key.stage || archiveRequestedStageSatisfied(run, key.stage) {
		return false
	}
	if run.Status == usagearchive.StatusFailed {
		return run.ResumeStatus == key.stage && errors.Is(runErr, ErrArchiveCoverageIncomplete)
	}
	return archiveRunCanExecuteStage(run, key.stage)
}

func archiveRequestedStageSatisfied(run store.UsageArchiveRun, stage string) bool {
	stageOrder := archiveResumeStageOrder(stage)
	return stageOrder > 0 && archiveRunResumeStageOrder(run) > stageOrder
}

func archiveRunCanExecuteStage(run store.UsageArchiveRun, stage string) bool {
	if run.Status == usagearchive.StatusFailed {
		return run.ResumeStatus == stage
	}
	switch stage {
	case usagearchive.StatusArchiving:
		return run.Status == usagearchive.StatusPreviewed || run.Status == usagearchive.StatusArchiving
	case usagearchive.StatusVerifying:
		return run.Status == usagearchive.StatusArchived || run.Status == usagearchive.StatusVerifying
	case usagearchive.StatusDeleting:
		return run.Status == usagearchive.StatusVerified || run.Status == usagearchive.StatusDeleting
	default:
		return false
	}
}

func (r *archiveJobRunner) claimNext(ctx context.Context) (store.UsageArchiveRun, archiveJobKey, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, found, err := r.service.archive.store.UsageArchives.NextRequestedRun(ctx)
	if err != nil || !found {
		return store.UsageArchiveRun{}, archiveJobKey{}, found, err
	}
	key := archiveJobKey{runID: run.ID, stage: run.RequestedStage}
	if _, exists := r.inFlight[key]; exists {
		return store.UsageArchiveRun{}, archiveJobKey{}, false, nil
	}
	r.inFlight[key] = struct{}{}
	return run, key, true, nil
}

func (r *archiveJobRunner) finish(key archiveJobKey, result archiveJobResult, keepRetryPolicy bool) {
	r.mu.Lock()
	delete(r.inFlight, key)
	waiters := r.waiters[key]
	delete(r.waiters, key)
	if !keepRetryPolicy {
		delete(r.retryOnError, key)
	}
	r.mu.Unlock()
	for _, waiter := range waiters {
		waiter <- result
	}
}
