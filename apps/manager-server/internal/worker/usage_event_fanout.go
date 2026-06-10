package worker

import (
	"context"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

type UsageEventFanout struct {
	handlers []collectorpkg.UsageEventHandler
}

func NewUsageEventFanout(handlers ...collectorpkg.UsageEventHandler) *UsageEventFanout {
	filtered := make([]collectorpkg.UsageEventHandler, 0, len(handlers))
	for _, handler := range handlers {
		if handler != nil {
			filtered = append(filtered, handler)
		}
	}
	return &UsageEventFanout{handlers: filtered}
}

func (f *UsageEventFanout) HandleUsageEvents(ctx context.Context, cfg collectorpkg.RuntimeConfig, events []usage.Event) {
	if f == nil || len(events) == 0 {
		return
	}
	for _, handler := range f.handlers {
		if ctx.Err() != nil {
			return
		}
		handler.HandleUsageEvents(ctx, cfg, events)
	}
}

func (f *UsageEventFanout) UpdateRuntimeConfig(ctx context.Context, cfg collectorpkg.RuntimeConfig) {
	if f == nil {
		return
	}
	for _, handler := range f.handlers {
		if ctx.Err() != nil {
			return
		}
		runtimeHandler, ok := handler.(collectorpkg.UsageRuntimeConfigHandler)
		if !ok {
			continue
		}
		runtimeHandler.UpdateRuntimeConfig(ctx, cfg)
	}
}
