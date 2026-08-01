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
	defaultUsageHourlyAggregateBatchLimit    = 1000
	defaultUsageHourlyAggregateMaxBatches    = 10
	defaultUsageHourlyAggregateCheckInterval = 30 * time.Second
)

type UsageHourlyAggregateWorker struct {
	store             *store.Store
	wake              chan struct{}
	running           int32
	batchLimit        int
	maxBatches        int
	checkInterval     time.Duration
	continuationDelay time.Duration
}

func NewUsageHourlyAggregateWorker(store *store.Store) *UsageHourlyAggregateWorker {
	return &UsageHourlyAggregateWorker{
		store:             store,
		wake:              make(chan struct{}, 1),
		batchLimit:        defaultUsageHourlyAggregateBatchLimit,
		maxBatches:        defaultUsageHourlyAggregateMaxBatches,
		checkInterval:     defaultUsageHourlyAggregateCheckInterval,
		continuationDelay: defaultRollupContinuationDelay,
	}
}

func (w *UsageHourlyAggregateWorker) Start(ctx context.Context) {
	if w == nil || w.store == nil {
		return
	}
	go w.loop(ctx)
	w.Wake()
}

func (w *UsageHourlyAggregateWorker) HandleUsageEvents(ctx context.Context, _ collectorpkg.RuntimeConfig, events []usage.Event) {
	if w == nil || len(events) == 0 || ctx.Err() != nil {
		return
	}
	w.Wake()
}

func (w *UsageHourlyAggregateWorker) Wake() {
	if w == nil {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *UsageHourlyAggregateWorker) loop(ctx context.Context) {
	runRollupLoop(ctx, w.wake, w.checkInterval, w.continuationDelay, w.catchUp)
}

func (w *UsageHourlyAggregateWorker) catchUp(ctx context.Context) bool {
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
		result, err := w.store.CatchUpUsageHourlyAggregate(ctx, w.batchLimit, nowMS)
		if err != nil {
			log.Printf("[usage-aggregate] hourly catch-up failed: %v", err)
			if recordErr := w.store.RecordUsageHourlyAggregateFailure(ctx, err, nowMS); recordErr != nil && ctx.Err() == nil {
				log.Printf("[usage-aggregate] record hourly catch-up failure: %v", recordErr)
			}
			return false
		}
		pending = result.Pending
		if result.Processed == 0 || !result.Pending {
			return false
		}
	}
	return pending
}
