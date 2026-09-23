package identityprojection

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const (
	DefaultWorkerBatchLimit        = 500
	DefaultWorkerMaxBatches        = 10
	DefaultWorkerCheckInterval     = 30 * time.Second
	DefaultWorkerContinuationDelay = 250 * time.Millisecond
)

// Worker is a listener-first background worker that asynchronously catches up
// and maintains the canonical identity shadow projection.
type Worker struct {
	repo              ports.Repository
	wake              chan struct{}
	running           int32
	batchLimit        int
	maxBatches        int
	checkInterval     time.Duration
	continuationDelay time.Duration
}

// NewWorker creates a new shadow projection worker.
func NewWorker(repo ports.Repository) *Worker {
	return &Worker{
		repo:              repo,
		wake:              make(chan struct{}, 1),
		batchLimit:        DefaultWorkerBatchLimit,
		maxBatches:        DefaultWorkerMaxBatches,
		checkInterval:     DefaultWorkerCheckInterval,
		continuationDelay: DefaultWorkerContinuationDelay,
	}
}

// SetBatchLimits allows overriding batch limits (useful for focused testing).
func (w *Worker) SetBatchLimits(batchLimit, maxBatches int) {
	if batchLimit > 0 {
		w.batchLimit = batchLimit
	}
	if maxBatches > 0 {
		w.maxBatches = maxBatches
	}
}

// Start launches the background catch-up loop.
func (w *Worker) Start(ctx context.Context) {
	if w == nil || w.repo == nil {
		return
	}
	go w.loop(ctx)
	w.Wake()
}

// Wake signals the worker to catch up immediately.
func (w *Worker) Wake() {
	if w == nil {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// HandleUsageEvents wakes the worker when new events arrive.
func (w *Worker) HandleUsageEvents(ctx context.Context, events []usage.Event) {
	if w == nil || len(events) == 0 || ctx.Err() != nil {
		return
	}
	w.Wake()
}

func (w *Worker) loop(ctx context.Context) {
	ticker := time.NewTicker(w.checkInterval)
	defer ticker.Stop()

	var continuationTimer *time.Timer
	var continuation <-chan time.Time

	stopContinuation := func() {
		if continuationTimer == nil {
			return
		}
		if !continuationTimer.Stop() {
			select {
			case <-continuationTimer.C:
			default:
			}
		}
		continuationTimer = nil
		continuation = nil
	}
	defer stopContinuation()

	run := func() {
		stopContinuation()
		if !w.catchUp(ctx) || ctx.Err() != nil {
			return
		}
		continuationTimer = time.NewTimer(w.continuationDelay)
		continuation = continuationTimer.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
			run()
		case <-ticker.C:
			run()
		case <-continuation:
			continuationTimer = nil
			continuation = nil
			run()
		}
	}
}

func (w *Worker) catchUp(ctx context.Context) bool {
	if !atomic.CompareAndSwapInt32(&w.running, 0, 1) {
		return false
	}
	defer atomic.StoreInt32(&w.running, 0)

	pending := false
	for batch := 0; batch < w.maxBatches; batch++ {
		if ctx.Err() != nil {
			return false
		}
		nowMS := time.Now().UnixMilli()
		result, err := w.repo.CatchUp(ctx, w.batchLimit, nowMS)
		if err != nil {
			log.Printf("[canonical-projection] catch-up error: %v", err)
			if recErr := w.repo.RecordFailure(ctx, err, nowMS); recErr != nil && ctx.Err() == nil {
				log.Printf("[canonical-projection] record failure error: %v", recErr)
			}
			return false
		}
		if !result.Pending {
			return false
		}
		pending = result.Pending
	}
	return pending
}
