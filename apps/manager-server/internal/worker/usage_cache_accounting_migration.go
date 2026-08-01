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

// UsageCacheAccountingMigrationWorker incrementally backfills legacy usage rows
// after the HTTP server is available. Its checkpoint is committed with each
// batch, so a restart resumes from the last successful batch.
type UsageCacheAccountingMigrationWorker struct {
	store        *store.Store
	batchSize    int
	delay        time.Duration
	retryDelay   time.Duration
	onCompletion func()
	start        sync.Once
	logStarted   sync.Once
	completion   sync.Once
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
		progressLogEvery := int64(w.batchSize * 10)
		if result.Processed > 0 && (result.Completed || progressLogEvery <= 0 || result.State.ProcessedRows%progressLogEvery == 0) {
			log.Printf("usage cache accounting migration progress: processed=%d changed=%d last_event_id=%d target_event_id=%d", result.State.ProcessedRows, result.State.ChangedRows, result.State.LastEventID, result.State.TargetEventID)
		}
		if result.Completed {
			w.complete(result.State)
			return
		}
		if !waitFor(ctx, w.delay) {
			return
		}
	}
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
