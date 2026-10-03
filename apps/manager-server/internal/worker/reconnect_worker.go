package worker

import (
	"context"
	"time"

	reconnectsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/reconnect"
)

// ReconnectWorker runs the self-service reconnect check on the configured
// interval. Settings are re-read every tick, so enabling the feature or
// changing the interval needs no restart.
type ReconnectWorker struct {
	service *reconnectsvc.Service
	tick    time.Duration
}

func NewReconnectWorker(service *reconnectsvc.Service) *ReconnectWorker {
	return &ReconnectWorker{service: service, tick: time.Minute}
}

func (w *ReconnectWorker) Start(ctx context.Context) {
	if w == nil || w.service == nil {
		return
	}
	go w.run(ctx)
}

func (w *ReconnectWorker) run(ctx context.Context) {
	ticker := time.NewTicker(w.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if w.service.CheckDue(ctx) {
				w.service.RunCheck(ctx)
			}
		}
	}
}
