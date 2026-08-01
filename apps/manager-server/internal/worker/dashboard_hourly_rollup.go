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
	defaultDashboardHourlyRollupBatchLimit    = 1000
	defaultDashboardHourlyRollupMaxBatches    = 10
	defaultDashboardHourlyRollupCheckInterval = 30 * time.Second
)

type DashboardHourlyRollupWorker struct {
	store             *store.Store
	wake              chan struct{}
	running           int32
	batchLimit        int
	maxBatches        int
	checkInterval     time.Duration
	continuationDelay time.Duration
}

func NewDashboardHourlyRollupWorker(store *store.Store) *DashboardHourlyRollupWorker {
	return &DashboardHourlyRollupWorker{
		store:             store,
		wake:              make(chan struct{}, 1),
		batchLimit:        defaultDashboardHourlyRollupBatchLimit,
		maxBatches:        defaultDashboardHourlyRollupMaxBatches,
		checkInterval:     defaultDashboardHourlyRollupCheckInterval,
		continuationDelay: defaultRollupContinuationDelay,
	}
}

func (w *DashboardHourlyRollupWorker) Start(ctx context.Context) {
	if w == nil || w.store == nil {
		return
	}
	go w.loop(ctx)
	w.Wake()
}

func (w *DashboardHourlyRollupWorker) HandleUsageEvents(ctx context.Context, _ collectorpkg.RuntimeConfig, events []usage.Event) {
	if w == nil || len(events) == 0 || ctx.Err() != nil {
		return
	}
	w.Wake()
}

func (w *DashboardHourlyRollupWorker) Wake() {
	if w == nil {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *DashboardHourlyRollupWorker) loop(ctx context.Context) {
	runRollupLoop(ctx, w.wake, w.checkInterval, w.continuationDelay, w.catchUp)
}

func (w *DashboardHourlyRollupWorker) catchUp(ctx context.Context) bool {
	if !atomic.CompareAndSwapInt32(&w.running, 0, 1) {
		return false
	}
	defer atomic.StoreInt32(&w.running, 0)

	pending := false
	for batch := 0; batch < w.maxBatches; batch++ {
		if ctx.Err() != nil {
			return false
		}
		result, err := w.store.CatchUpDashboardHourlyRollups(ctx, w.batchLimit, time.Now().UnixMilli())
		if err != nil {
			log.Printf("[usage-rollup] dashboard hourly catch-up failed: %v", err)
			return false
		}
		pending = result.Pending
		if result.Processed == 0 || !result.Pending {
			return false
		}
	}
	return pending
}
