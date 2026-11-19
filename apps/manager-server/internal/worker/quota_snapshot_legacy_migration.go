package worker

import (
	"context"
	"errors"
	"log"
	"time"

	quotasnapshotrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const (
	defaultLegacyQuotaSnapshotGroupLimit = 1000
	defaultLegacyQuotaSnapshotDelay      = 10 * time.Millisecond
	defaultLegacyQuotaSnapshotRetryDelay = 5 * time.Second
)

// LegacyQuotaSnapshotMigrationWorker attaches pre-lifecycle quota snapshots
// after the HTTP listener and startup database maintenance are available.
type LegacyQuotaSnapshotMigrationWorker struct {
	store      *store.Store
	groupLimit int
	delay      time.Duration
	retryDelay time.Duration
	lifecycle  workerLifecycle
}

func NewLegacyQuotaSnapshotMigrationWorker(st *store.Store) *LegacyQuotaSnapshotMigrationWorker {
	return &LegacyQuotaSnapshotMigrationWorker{
		store:      st,
		groupLimit: defaultLegacyQuotaSnapshotGroupLimit,
		delay:      defaultLegacyQuotaSnapshotDelay,
		retryDelay: defaultLegacyQuotaSnapshotRetryDelay,
	}
}

func (w *LegacyQuotaSnapshotMigrationWorker) Start(ctx context.Context) {
	if w == nil || w.store == nil {
		return
	}
	w.lifecycle.goRun(ctx, func() { w.run(ctx) })
}

func (w *LegacyQuotaSnapshotMigrationWorker) Wait() {
	if w == nil {
		return
	}
	w.lifecycle.wait()
}

func (w *LegacyQuotaSnapshotMigrationWorker) run(ctx context.Context) {
	total := 0
	started := false
	for ctx.Err() == nil {
		result, err := w.store.BackfillLegacyQuotaSnapshotsBatch(ctx, w.groupLimit)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if recordErr := w.store.RecordLegacyQuotaSnapshotBackfillFailure(ctx, err); recordErr != nil && ctx.Err() == nil {
				log.Printf("[quota-snapshot-migration] record failure: %v", recordErr)
			}
			if errors.Is(err, quotasnapshotrepo.ErrLegacySnapshotGroupTooLarge) {
				log.Printf("[quota-snapshot-migration] paused; offline cleanup required: %v", err)
				return
			}
			log.Printf("[quota-snapshot-migration] batch failed; will retry: %v", err)
			if !waitFor(ctx, w.retryDelay) {
				return
			}
			continue
		}
		if result.Processed > 0 && !started {
			log.Printf("[quota-snapshot-migration] started maxGroupSize=%d", w.groupLimit)
			started = true
		}
		total += result.Processed
		if result.Processed > 0 && (result.Completed || total%10000 < result.Processed) {
			log.Printf("[quota-snapshot-migration] progress processed=%d lastSnapshotID=%d pending=%t", total, result.LastSnapshotID, result.Pending)
		}
		if result.Completed {
			if started {
				log.Printf("[quota-snapshot-migration] completed processed=%d", total)
			}
			return
		}
		if !waitFor(ctx, w.delay) {
			return
		}
	}
}
