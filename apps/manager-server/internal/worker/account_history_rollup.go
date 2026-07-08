package worker

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const (
	defaultAccountHistoryRollupBatchLimit    = 1000
	defaultAccountHistoryRollupMaxBatches    = 10
	defaultAccountHistoryRollupCheckInterval = 30 * time.Second
)

type AccountHistoryRollupWorker struct {
	store         *store.Store
	wake          chan struct{}
	running       int32
	batchLimit    int
	maxBatches    int
	checkInterval time.Duration
}

func NewAccountHistoryRollupWorker(store *store.Store) *AccountHistoryRollupWorker {
	return &AccountHistoryRollupWorker{
		store:         store,
		wake:          make(chan struct{}, 1),
		batchLimit:    defaultAccountHistoryRollupBatchLimit,
		maxBatches:    defaultAccountHistoryRollupMaxBatches,
		checkInterval: defaultAccountHistoryRollupCheckInterval,
	}
}

func (w *AccountHistoryRollupWorker) Start(ctx context.Context) {
	if w == nil || w.store == nil {
		return
	}
	go w.loop(ctx)
	w.Wake()
}

func (w *AccountHistoryRollupWorker) HandleUsageEvents(ctx context.Context, _ collectorpkg.RuntimeConfig, events []usage.Event) {
	if w == nil || len(events) == 0 || ctx.Err() != nil {
		return
	}
	w.Wake()
}

func (w *AccountHistoryRollupWorker) Wake() {
	if w == nil {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *AccountHistoryRollupWorker) loop(ctx context.Context) {
	ticker := time.NewTicker(w.checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
			w.catchUp(ctx)
		case <-ticker.C:
			w.catchUp(ctx)
		}
	}
}

func (w *AccountHistoryRollupWorker) catchUp(ctx context.Context) {
	if !atomic.CompareAndSwapInt32(&w.running, 0, 1) {
		return
	}
	defer atomic.StoreInt32(&w.running, 0)

	for batch := 0; batch < w.maxBatches; batch++ {
		if ctx.Err() != nil {
			return
		}
		result, err := w.store.CatchUpAccountHistoryRollups(ctx, w.batchLimit, time.Now().UnixMilli())
		if err != nil {
			log.Printf("[usage-rollup] account history catch-up failed: %v", err)
			return
		}
		if result.Processed == 0 || !result.Pending {
			return
		}
	}
}
