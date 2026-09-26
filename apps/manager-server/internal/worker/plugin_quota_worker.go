package worker

import (
	"context"
	"log"
	"sync"
	"time"

	pluginquotasvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/pluginquota"
)

// DefaultPluginQuotaRefreshInterval is how often CPAMP re-reads the quota CPA
// plugins report. The read is a cached management call per credential and never
// forces an upstream provider request, so the interval only decides how fresh
// the stored evidence is.
const DefaultPluginQuotaRefreshInterval = 10 * time.Minute

// PluginQuotaWorker keeps plugin-provided quota current without a browser
// session. It mirrors the Codex inspection worker: it runs one pass at startup
// and then on a ticker, and every pass writes through the same quota snapshot
// service the panel writes to.
type PluginQuotaWorker struct {
	service  *pluginquotasvc.Service
	interval time.Duration

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	started bool
}

func NewPluginQuotaWorker(service *pluginquotasvc.Service, interval time.Duration) *PluginQuotaWorker {
	if interval <= 0 {
		interval = DefaultPluginQuotaRefreshInterval
	}
	return &PluginQuotaWorker{service: service, interval: interval}
}

func (w *PluginQuotaWorker) Start(ctx context.Context) {
	if w == nil || w.service == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	if w.started {
		w.mu.Unlock()
		return
	}
	workerCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	w.done = make(chan struct{})
	w.started = true
	done := w.done
	w.mu.Unlock()
	go func() {
		defer close(done)
		w.run(workerCtx)
	}()
}

func (w *PluginQuotaWorker) StopAndWait(ctx context.Context) error {
	if w == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	w.mu.Lock()
	cancel := w.cancel
	done := w.done
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *PluginQuotaWorker) run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	w.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *PluginQuotaWorker) tick(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return
	}
	if !w.service.Configured(ctx) {
		return
	}
	result, err := w.service.Refresh(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[plugin-quota] refresh plugin quota: %v", err)
		}
		return
	}
	if failed := result.FailedCount(); failed > 0 {
		log.Printf(
			"[plugin-quota] refreshed %d plugin quota credentials; %d failed",
			len(result.Credentials),
			failed,
		)
	}
}
