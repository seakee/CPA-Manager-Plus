package worker

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
	monitoringrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagemonitoring"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const (
	defaultUsagePricingBatchLimit    = 1000
	defaultUsagePricingMaxBatches    = 10
	defaultUsagePricingCheckInterval = 30 * time.Second
)

type UsagePricingRollupWorker struct {
	store             *store.Store
	wake              chan struct{}
	running           int32
	batchLimit        int
	maxBatches        int
	checkInterval     time.Duration
	continuationDelay time.Duration
	nextTask          int
	reportedStarted   [usageDerivedTaskCount]bool
	lastReported      [usageDerivedTaskCount]int64
	lifecycle         workerLifecycle
}

const (
	usageDerivedPricingTask = iota
	usageDerivedMonitoringProjectionTask
	usageDerivedMonitoringMetadataTask
	usageDerivedMonitoringStatsTask
	usageDerivedCodexLegacyIdentityTask
	usageDerivedTaskCount
)

func NewUsagePricingRollupWorker(store *store.Store) *UsagePricingRollupWorker {
	return &UsagePricingRollupWorker{
		store:             store,
		wake:              make(chan struct{}, 1),
		batchLimit:        defaultUsagePricingBatchLimit,
		maxBatches:        defaultUsagePricingMaxBatches,
		checkInterval:     defaultUsagePricingCheckInterval,
		continuationDelay: defaultRollupContinuationDelay,
	}
}

func (w *UsagePricingRollupWorker) Start(ctx context.Context) {
	if w == nil || w.store == nil {
		return
	}
	if w.lifecycle.goRun(ctx, func() { w.loop(ctx) }) {
		w.Wake()
	}
}

func (w *UsagePricingRollupWorker) Wait() {
	if w == nil {
		return
	}
	w.lifecycle.wait()
}

func (w *UsagePricingRollupWorker) HandleUsageEvents(ctx context.Context, _ collectorpkg.RuntimeConfig, events []usage.Event) {
	if w == nil || len(events) == 0 || ctx.Err() != nil {
		return
	}
	w.Wake()
}

func (w *UsagePricingRollupWorker) Wake() {
	if w == nil {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *UsagePricingRollupWorker) loop(ctx context.Context) {
	runRollupLoop(ctx, w.wake, w.checkInterval, w.continuationDelay, w.catchUp)
}

func (w *UsagePricingRollupWorker) catchUp(ctx context.Context) bool {
	if !atomic.CompareAndSwapInt32(&w.running, 0, 1) {
		return false
	}
	defer atomic.StoreInt32(&w.running, 0)

	pendingByTask := [usageDerivedTaskCount]bool{}
	idleByTask := [usageDerivedTaskCount]bool{}
	for batch := 0; batch < w.maxBatches; batch++ {
		if ctx.Err() != nil {
			return false
		}
		if allUsageDerivedTasksIdle(idleByTask) {
			break
		}
		task := w.nextRunnableTask(idleByTask)
		w.nextTask = (task + 1) % usageDerivedTaskCount
		nowMS := time.Now().UnixMilli()
		result, err := w.catchUpTask(ctx, task, nowMS)
		if err != nil {
			log.Printf("[usage-derived] %s catch-up failed: %v", usageDerivedTaskName(task), err)
			if recordErr := w.recordTaskFailure(ctx, task, err, nowMS); recordErr != nil && ctx.Err() == nil {
				log.Printf("[usage-derived] record %s catch-up failure: %v", usageDerivedTaskName(task), recordErr)
			}
			idleByTask[task] = true
			pendingByTask[task] = false
			continue
		}
		w.logTaskProgress(task, result)
		pendingByTask[task] = result.Pending && (result.Processed > 0 || result.ContinueSoon)
		if !result.Pending || (result.Processed == 0 && !result.ContinueSoon) {
			idleByTask[task] = true
		}
	}
	for _, pending := range pendingByTask {
		if pending {
			return true
		}
	}
	return false
}

type usageDerivedCatchUpResult struct {
	Processed       int
	CoverageEventID int64
	TargetEventID   int64
	Pending         bool
	Rebuilt         bool
	ContinueSoon    bool
}

func (w *UsagePricingRollupWorker) nextRunnableTask(idle [usageDerivedTaskCount]bool) int {
	for offset := 0; offset < usageDerivedTaskCount; offset++ {
		task := (w.nextTask + offset) % usageDerivedTaskCount
		if !idle[task] {
			return task
		}
	}
	return w.nextTask % usageDerivedTaskCount
}

func allUsageDerivedTasksIdle(idle [usageDerivedTaskCount]bool) bool {
	for _, taskIdle := range idle {
		if !taskIdle {
			return false
		}
	}
	return true
}

func (w *UsagePricingRollupWorker) catchUpTask(ctx context.Context, task int, nowMS int64) (usageDerivedCatchUpResult, error) {
	switch task {
	case usageDerivedMonitoringProjectionTask:
		result, err := w.store.CatchUpUsageMonitoringProjection(ctx, w.batchLimit, nowMS)
		return usageDerivedCatchUpResult{Processed: result.Processed, CoverageEventID: result.CoverageEventID, TargetEventID: result.TargetEventID, Pending: result.Pending, Rebuilt: result.Rebuilt, ContinueSoon: result.ContinueSoon}, err
	case usageDerivedMonitoringMetadataTask:
		result, err := w.store.CatchUpUsageMonitoringMetadata(ctx, w.batchLimit, nowMS)
		return usageDerivedCatchUpResult{Processed: result.Processed, CoverageEventID: result.CoverageEventID, TargetEventID: result.TargetEventID, Pending: result.Pending, Rebuilt: result.Rebuilt, ContinueSoon: result.ContinueSoon}, err
	case usageDerivedMonitoringStatsTask:
		result, err := w.store.CatchUpUsageMonitoringStats(ctx, w.batchLimit, nowMS)
		return usageDerivedCatchUpResult{Processed: result.Processed, CoverageEventID: result.CoverageEventID, TargetEventID: result.TargetEventID, Pending: result.Pending, Rebuilt: result.Rebuilt, ContinueSoon: result.ContinueSoon}, err
	case usageDerivedCodexLegacyIdentityTask:
		result, err := w.store.CatchUpCodexLegacyIdentityEvidence(ctx, w.batchLimit, nowMS)
		return usageDerivedCatchUpResult{Processed: result.Processed, CoverageEventID: result.CoverageEventID, TargetEventID: result.TargetEventID, Pending: result.Pending, Rebuilt: result.Rebuilt, ContinueSoon: result.ContinueSoon}, err
	default:
		result, err := w.store.CatchUpUsagePricing(ctx, w.batchLimit, nowMS)
		return usageDerivedCatchUpResult{Processed: result.Processed, CoverageEventID: result.CoverageEventID, TargetEventID: result.TargetEventID, Pending: result.Pending, Rebuilt: result.Rebuilt, ContinueSoon: result.ContinueSoon}, err
	}
}

func (w *UsagePricingRollupWorker) logTaskProgress(task int, result usageDerivedCatchUpResult) {
	if result.Rebuilt && !w.reportedStarted[task] {
		log.Printf("[usage-derived] %s rebuild started targetEventID=%d batchSize=%d", usageDerivedTaskName(task), result.TargetEventID, w.batchLimit)
		w.reportedStarted[task] = true
		w.lastReported[task] = 0
	}
	rebuildCompleted := result.Rebuilt &&
		((result.TargetEventID > 0 && result.CoverageEventID >= result.TargetEventID) ||
			(result.TargetEventID == 0 && !result.Pending && !result.ContinueSoon))
	shouldReport := w.reportedStarted[task] && result.Rebuilt && result.Processed > 0 &&
		(result.CoverageEventID-w.lastReported[task] >= 10000 || rebuildCompleted)
	if shouldReport {
		log.Printf("[usage-derived] %s rebuild progress coverageEventID=%d targetEventID=%d pending=%t", usageDerivedTaskName(task), result.CoverageEventID, result.TargetEventID, result.Pending)
		w.lastReported[task] = result.CoverageEventID
	}
	if w.reportedStarted[task] && rebuildCompleted {
		log.Printf("[usage-derived] %s rebuild completed coverageEventID=%d", usageDerivedTaskName(task), result.CoverageEventID)
		w.reportedStarted[task] = false
		w.lastReported[task] = 0
	}
}

func (w *UsagePricingRollupWorker) recordTaskFailure(ctx context.Context, task int, rollupErr error, nowMS int64) error {
	switch task {
	case usageDerivedMonitoringProjectionTask:
		return w.store.RecordUsageMonitoringFailure(ctx, monitoringrepo.ProjectionRollupName, rollupErr, nowMS)
	case usageDerivedMonitoringMetadataTask:
		return w.store.RecordUsageMonitoringFailure(ctx, monitoringrepo.MetadataRollupName, rollupErr, nowMS)
	case usageDerivedMonitoringStatsTask:
		return w.store.RecordUsageMonitoringFailure(ctx, monitoringrepo.StatsRollupName, rollupErr, nowMS)
	case usageDerivedCodexLegacyIdentityTask:
		return w.store.RecordUsageMonitoringFailure(ctx, usageevent.CodexLegacyIdentityRollupName, rollupErr, nowMS)
	default:
		return w.store.RecordUsagePricingFailure(ctx, rollupErr, nowMS)
	}
}

func usageDerivedTaskName(task int) string {
	switch task {
	case usageDerivedMonitoringProjectionTask:
		return "monitoring projection"
	case usageDerivedMonitoringMetadataTask:
		return "monitoring metadata"
	case usageDerivedMonitoringStatsTask:
		return "monitoring stats"
	case usageDerivedCodexLegacyIdentityTask:
		return "Codex legacy identity evidence"
	default:
		return "pricing"
	}
}
