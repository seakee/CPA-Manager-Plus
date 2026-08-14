package worker

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	defaultUsageTokenAccountingMigrationBatchSize = 1000
	defaultUsageTokenAccountingMigrationDelay     = 200 * time.Millisecond
	defaultUsageTokenAccountingMigrationRetry     = 5 * time.Second
)

// UsageTokenAccountingMigrationWorker incrementally materializes canonical
// accounting buckets for historical usage_events after cache normalization.
type UsageTokenAccountingMigrationWorker struct {
	store        *store.Store
	batchSize    int
	delay        time.Duration
	retryDelay   time.Duration
	onCompletion func()
	start        sync.Once
	logStarted   sync.Once
	completion   sync.Once
}

func NewUsageTokenAccountingMigrationWorker(st *store.Store, onCompletion func()) *UsageTokenAccountingMigrationWorker {
	return &UsageTokenAccountingMigrationWorker{
		store: st, batchSize: defaultUsageTokenAccountingMigrationBatchSize,
		delay: defaultUsageTokenAccountingMigrationDelay, retryDelay: defaultUsageTokenAccountingMigrationRetry,
		onCompletion: onCompletion,
	}
}

func (w *UsageTokenAccountingMigrationWorker) Start(ctx context.Context) {
	if w == nil || w.store == nil {
		return
	}
	w.start.Do(func() { go w.run(ctx) })
}

func (w *UsageTokenAccountingMigrationWorker) run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		state, err := w.store.DiscoverUsageTokenAccounting(ctx)
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
			log.Printf("usage token accounting migration started: last_event_id=%d target_event_id=%d batch_size=%d", state.LastEventID, state.TargetEventID, w.batchSize)
		})
		result, err := w.store.RunUsageTokenAccountingBatch(ctx, w.batchSize)
		if err != nil {
			w.recordFailure(ctx, err)
			if !waitFor(ctx, w.retryDelay) {
				return
			}
			continue
		}
		progressEvery := int64(w.batchSize * 10)
		if result.Processed > 0 && (result.Completed || progressEvery <= 0 || result.State.ProcessedRows%progressEvery == 0) {
			log.Printf("usage token accounting migration progress: processed=%d changed=%d last_event_id=%d target_event_id=%d", result.State.ProcessedRows, result.State.ChangedRows, result.State.LastEventID, result.State.TargetEventID)
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

func (w *UsageTokenAccountingMigrationWorker) complete(state store.DataMigrationState) {
	w.completion.Do(func() {
		log.Printf("usage token accounting migration completed: processed=%d changed=%d", state.ProcessedRows, state.ChangedRows)
		if w.onCompletion != nil {
			w.onCompletion()
		}
	})
}

func (w *UsageTokenAccountingMigrationWorker) recordFailure(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	log.Printf("usage token accounting migration failed; will retry: %v", err)
	if recordErr := w.store.RecordUsageTokenAccountingFailure(ctx, err); recordErr != nil {
		log.Printf("usage token accounting migration failure state: %v", recordErr)
	}
}
