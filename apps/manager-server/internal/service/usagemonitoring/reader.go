package usagemonitoring

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"
	"time"

	monitoringrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagemonitoring"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

const fallbackLogIntervalMS = int64(5 * time.Minute / time.Millisecond)

type Reader struct {
	store             *store.Store
	lastFallbackLogMS atomic.Int64
}

func New(store *store.Store) *Reader {
	return &Reader{store: store}
}

func SupportsStatsFilter(filter store.AnalyticsFilter) bool {
	return monitoringrepo.SupportsStatsFilter(filter)
}

func SupportsEventProjectionFilter(filter store.AnalyticsFilter) bool {
	return monitoringrepo.SupportsEventProjectionFilter(filter)
}

func SupportsSelectorFilter(filter store.AnalyticsFilter) bool {
	return monitoringrepo.SupportsSelectorFilter(filter)
}

func PrefersEventProjection(filter store.AnalyticsFilter) bool {
	return monitoringrepo.PrefersEventProjection(filter)
}

func (r *Reader) AccountStats(ctx context.Context, filter store.AnalyticsFilter) ([]store.AccountModelStat, bool) {
	if r == nil || r.store == nil {
		return nil, false
	}
	if !monitoringrepo.SupportsEventProjectionFilter(filter) {
		return nil, false
	}
	rows, state, available, err := r.store.UsageMonitoringAccountStats(ctx, filter)
	if err != nil {
		r.logFallback(fmt.Sprintf("account daily rollup query failed: %v", err))
		return nil, false
	}
	if !available {
		r.logFallback(fmt.Sprintf("account daily rollup unavailable: schema_version=%d status=%s", state.SchemaVersion, state.Status))
		return nil, false
	}
	return rows, true
}

func (r *Reader) AccountWindowStats(ctx context.Context, windows []store.AccountWindowUsageQuery) ([]store.AccountWindowModelStat, bool) {
	if r == nil || r.store == nil {
		return nil, false
	}
	rows, state, available, err := r.store.UsageMonitoringAccountWindowStats(ctx, windows)
	if err != nil {
		r.logFallback(fmt.Sprintf("account window projection query failed: %v", err))
		return nil, false
	}
	if !available {
		r.logFallback(fmt.Sprintf("account window projection unavailable: schema_version=%d status=%s", state.SchemaVersion, state.Status))
		return nil, false
	}
	return rows, true
}

func (r *Reader) APIKeyStats(ctx context.Context, filter store.AnalyticsFilter) ([]store.APIKeyModelStat, bool) {
	if r == nil || r.store == nil {
		return nil, false
	}
	if !monitoringrepo.SupportsEventProjectionFilter(filter) {
		return nil, false
	}
	rows, state, available, err := r.store.UsageMonitoringAPIKeyStats(ctx, filter)
	if err != nil {
		r.logFallback(fmt.Sprintf("api key daily rollup query failed: %v", err))
		return nil, false
	}
	if !available {
		r.logFallback(fmt.Sprintf("api key daily rollup unavailable: schema_version=%d status=%s", state.SchemaVersion, state.Status))
		return nil, false
	}
	return rows, true
}

func (r *Reader) FilterOptions(ctx context.Context, filter store.AnalyticsFilter) (store.FilterOptionValues, bool) {
	if r == nil || r.store == nil {
		return store.FilterOptionValues{}, false
	}
	if !monitoringrepo.SupportsEventProjectionFilter(filter) {
		return store.FilterOptionValues{}, false
	}
	values, state, available, err := r.store.UsageMonitoringFilterOptions(ctx, filter)
	if err != nil {
		r.logFallback(fmt.Sprintf("filter option projection query failed: %v", err))
		return store.FilterOptionValues{}, false
	}
	if !available {
		r.logFallback(fmt.Sprintf("filter option projection unavailable: schema_version=%d status=%s", state.SchemaVersion, state.Status))
		return store.FilterOptionValues{}, false
	}
	return values, true
}

func (r *Reader) FilterSelectors(ctx context.Context, filter store.AnalyticsFilter) (store.FilterSelectorValues, bool) {
	if r == nil || r.store == nil {
		return store.FilterSelectorValues{}, false
	}
	if !monitoringrepo.SupportsEventProjectionFilter(filter) {
		return store.FilterSelectorValues{}, false
	}
	values, state, available, err := r.store.UsageMonitoringFilterSelectors(ctx, filter)
	if err != nil {
		r.logFallback(fmt.Sprintf("selector daily rollup query failed: %v", err))
		return store.FilterSelectorValues{}, false
	}
	if !available {
		r.logFallback(fmt.Sprintf("selector daily rollup unavailable: schema_version=%d status=%s", state.SchemaVersion, state.Status))
		return store.FilterSelectorValues{}, false
	}
	return values, true
}

func (r *Reader) Aggregate(ctx context.Context, filter store.AnalyticsFilter) (store.Aggregate, bool) {
	if r == nil || r.store == nil {
		return store.Aggregate{}, false
	}
	if !monitoringrepo.SupportsEventProjectionFilter(filter) {
		return store.Aggregate{}, false
	}
	aggregate, state, available, err := r.store.UsageMonitoringAggregate(ctx, filter)
	if err != nil {
		r.logFallback(fmt.Sprintf("event projection aggregate query failed: %v", err))
		return store.Aggregate{}, false
	}
	if !available {
		r.logFallback(fmt.Sprintf("event projection aggregate unavailable: schema_version=%d status=%s", state.SchemaVersion, state.Status))
		return store.Aggregate{}, false
	}
	return aggregate, true
}

func (r *Reader) ModelStats(ctx context.Context, filter store.AnalyticsFilter) ([]store.ModelStat, bool) {
	if r == nil || r.store == nil {
		return nil, false
	}
	if !monitoringrepo.SupportsEventProjectionFilter(filter) {
		return nil, false
	}
	rows, state, available, err := r.store.UsageMonitoringModelStats(ctx, filter)
	if err != nil {
		r.logFallback(fmt.Sprintf("event projection model stats query failed: %v", err))
		return nil, false
	}
	if !available {
		r.logFallback(fmt.Sprintf("event projection model stats unavailable: schema_version=%d status=%s", state.SchemaVersion, state.Status))
		return nil, false
	}
	return rows, true
}

func (r *Reader) EventsCount(ctx context.Context, filter store.AnalyticsFilter) (int64, bool) {
	if r == nil || r.store == nil {
		return 0, false
	}
	if !monitoringrepo.SupportsEventProjectionFilter(filter) || !monitoringrepo.PrefersEventProjection(filter) {
		return 0, false
	}
	total, state, available, err := r.store.UsageMonitoringEventsCount(ctx, filter)
	if err != nil {
		r.logFallback(fmt.Sprintf("event projection count query failed: %v", err))
		return 0, false
	}
	if !available {
		r.logFallback(fmt.Sprintf("event projection count unavailable: schema_version=%d status=%s", state.SchemaVersion, state.Status))
		return 0, false
	}
	return total, true
}

func (r *Reader) EventsPage(ctx context.Context, filter store.AnalyticsFilter, beforeMS, beforeID int64, limit int) (store.EventsPage, bool) {
	if r == nil || r.store == nil {
		return store.EventsPage{}, false
	}
	if !monitoringrepo.SupportsEventProjectionFilter(filter) || !monitoringrepo.PrefersEventProjection(filter) {
		return store.EventsPage{}, false
	}
	page, state, available, err := r.store.UsageMonitoringEventsPage(ctx, filter, beforeMS, beforeID, limit)
	if err != nil {
		r.logFallback(fmt.Sprintf("event projection page query failed: %v", err))
		return store.EventsPage{}, false
	}
	if !available {
		r.logFallback(fmt.Sprintf("event projection page unavailable: schema_version=%d status=%s", state.SchemaVersion, state.Status))
		return store.EventsPage{}, false
	}
	return page, true
}

func (r *Reader) HeaderSnapshots(ctx context.Context, sinceMS int64, limit int) ([]store.HeaderSnapshot, bool) {
	if r == nil || r.store == nil {
		return nil, false
	}
	items, state, available, err := r.store.UsageMonitoringHeaderSnapshots(ctx, sinceMS, limit)
	if err != nil {
		r.logFallback(fmt.Sprintf("header latest rollup query failed: %v", err))
		return nil, false
	}
	if !available {
		r.logFallback(fmt.Sprintf("header latest rollup unavailable: schema_version=%d status=%s", state.SchemaVersion, state.Status))
		return nil, false
	}
	return items, true
}

func (r *Reader) logFallback(message string) {
	nowMS := time.Now().UnixMilli()
	lastMS := r.lastFallbackLogMS.Load()
	if lastMS > 0 && nowMS-lastMS < fallbackLogIntervalMS {
		return
	}
	if r.lastFallbackLogMS.CompareAndSwap(lastMS, nowMS) {
		log.Printf("[usage-monitoring] %s; falling back to usage_events", message)
	}
}
