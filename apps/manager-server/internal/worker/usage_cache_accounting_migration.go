package worker

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	defaultUsageCacheAccountingMigrationBatchSize = 1000
	defaultUsageCacheAccountingMigrationDelay     = 200 * time.Millisecond
	defaultUsageCacheAccountingMigrationRetry     = 5 * time.Second
)

// UsageCacheAccountingMigrationWorker incrementally scans, corrects, and
// invalidates derived data after the HTTP server is available. Each phase uses
// committed bounded batches, so a restart resumes from the last successful
// batch.
type UsageCacheAccountingMigrationWorker struct {
	store        *store.Store
	batchSize    int
	delay        time.Duration
	retryDelay   time.Duration
	onCompletion func()
	start        sync.Once
	logStarted   sync.Once
	completion   sync.Once
	lastStatus   string
	clearedRows  int64
}

func NewUsageCacheAccountingMigrationWorker(st *store.Store, onCompletion func()) *UsageCacheAccountingMigrationWorker {
	return &UsageCacheAccountingMigrationWorker{
		store:        st,
		batchSize:    defaultUsageCacheAccountingMigrationBatchSize,
		delay:        defaultUsageCacheAccountingMigrationDelay,
		retryDelay:   defaultUsageCacheAccountingMigrationRetry,
		onCompletion: onCompletion,
	}
}

func (w *UsageCacheAccountingMigrationWorker) Start(ctx context.Context) {
	if w == nil || w.store == nil {
		return
	}
	w.start.Do(func() {
		go w.run(ctx)
	})
}

func (w *UsageCacheAccountingMigrationWorker) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}

		state, err := w.store.DiscoverUsageCacheAccounting(ctx)
		if err != nil {
			w.recordFailure(ctx, err)
			if !waitFor(ctx, w.retryDelay) {
				return
			}
			continue
		}
		if state.Status == "completed" {
			w.complete(state)
			return
		}
		w.logStarted.Do(func() {
			log.Printf("usage cache accounting migration started: last_event_id=%d target_event_id=%d batch_size=%d", state.LastEventID, state.TargetEventID, w.batchSize)
		})

		result, err := w.store.RunUsageCacheAccountingBatch(ctx, w.batchSize)
		if err != nil {
			w.recordFailure(ctx, err)
			if !waitFor(ctx, w.retryDelay) {
				return
			}
			continue
		}
		w.logProgress(result)
		if result.Completed {
			continue
		}
		if !waitFor(ctx, w.delay) {
			return
		}
	}
}

func (w *UsageCacheAccountingMigrationWorker) logProgress(result store.DataMigrationBatchResult) {
	progressLogEvery := int64(w.batchSize * 10)
	phaseChanged := result.State.Status != w.lastStatus
	switch result.State.Status {
	case "running", "applying":
		progress := result.State.ProcessedRows
		if result.State.Status == "applying" {
			progress = result.State.AppliedRows
		}
		if phaseChanged || result.Completed || (result.Processed > 0 && (progressLogEvery <= 0 || progress%progressLogEvery == 0)) {
			log.Printf("usage cache accounting migration progress: phase=%s scanned=%d changed=%d applied=%d last_event_id=%d target_event_id=%d", result.State.Status, result.State.ProcessedRows, result.State.ChangedRows, result.State.AppliedRows, result.State.LastEventID, result.State.TargetEventID)
		}
	case "clearing":
		w.clearedRows += result.Processed
		if phaseChanged || result.Completed || (result.Processed > 0 && (progressLogEvery <= 0 || w.clearedRows%progressLogEvery < result.Processed)) {
			log.Printf("usage cache accounting migration progress: phase=clearing cleared=%d changed=%d applied=%d", w.clearedRows, result.State.ChangedRows, result.State.AppliedRows)
		}
	}
	w.lastStatus = result.State.Status
}

func (w *UsageCacheAccountingMigrationWorker) complete(state store.DataMigrationState) {
	w.completion.Do(func() {
		log.Printf("usage cache accounting migration completed: processed=%d changed=%d", state.ProcessedRows, state.ChangedRows)
		if w.onCompletion != nil {
			w.onCompletion()
		}
	})
}

func (w *UsageCacheAccountingMigrationWorker) recordFailure(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	log.Printf("usage cache accounting migration failed; will retry: %v", err)
	if recordErr := w.store.RecordUsageCacheAccountingFailure(ctx, err); recordErr != nil {
		log.Printf("usage cache accounting migration failure state: %v", recordErr)
	}
}

func waitFor(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
