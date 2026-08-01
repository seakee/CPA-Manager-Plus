package worker

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	codexinspectionservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/codexinspection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type CodexInspectionWorker struct {
	store   *store.Store
	service *codexinspectionservice.Service
	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	started bool
}

func NewCodexInspectionWorker(store *store.Store, service *codexinspectionservice.Service) *CodexInspectionWorker {
	return &CodexInspectionWorker{store: store, service: service}
}

func (w *CodexInspectionWorker) Start(ctx context.Context) {
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

func (w *CodexInspectionWorker) StopAndWait(ctx context.Context) error {
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
	var serviceErr error
	if w.service != nil {
		// Fence manual and scheduled starts immediately, before waiting for the
		// scheduler loop to observe its cancelled context.
		serviceErr = w.service.StopAndWait(ctx)
	}
	var waitErr error
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
	}
	if waitErr != nil {
		return waitErr
	}
	return serviceErr
}

func (w *CodexInspectionWorker) run(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
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

func (w *CodexInspectionWorker) tick(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := w.service.Recover(ctx); err != nil {
		if ctx.Err() == nil {
			log.Printf("recover stale codex inspection runs: %v", err)
		}
		return
	}
	cfg, configured, err := w.service.ResolveConfig(ctx)
	if err != nil {
		log.Printf("resolve codex inspection config: %v", err)
		return
	}
	if !configured || cfg.Enabled == nil || !*cfg.Enabled {
		return
	}
	now := time.Now()
	triggerKey := model.CodexInspectionTriggerKey(now, cfg)
	lastRunTime, err := w.lastScheduledRunTime(ctx)
	if err != nil {
		log.Printf("load latest scheduled codex inspection run: %v", err)
		return
	}
	if triggerKey == "" || !model.CodexInspectionScheduleDue(now, lastRunTime, cfg) {
		return
	}
	if _, active, err := w.store.GetActiveCodexInspectionLease(ctx, now.UnixMilli()); err != nil {
		log.Printf("load codex inspection lease: %v", err)
		return
	} else if active {
		log.Printf("skip scheduled codex inspection %s: another run is active", triggerKey)
		return
	}
	if err := ctx.Err(); err != nil {
		return
	}
	// Start already owns the long-running execution goroutine. Keeping lease
	// acquisition in the worker loop means StopAndWait can account for every
	// scheduler start attempt before SQLite is closed.
	if _, err := w.service.Start(ctx, codexinspectionservice.RunRequest{
		TriggerType: model.CodexInspectionTriggerScheduled,
		TriggerKey:  triggerKey,
	}); err != nil &&
		!errors.Is(err, codexinspectionservice.ErrRunAlreadyActive) &&
		!errors.Is(err, codexinspectionservice.ErrTriggerAlreadyExists) &&
		!errors.Is(err, codexinspectionservice.ErrScheduledRunDisabled) &&
		!errors.Is(err, codexinspectionservice.ErrServiceStopping) &&
		ctx.Err() == nil {
		log.Printf("run scheduled codex inspection: %v", err)
	}
}

func (w *CodexInspectionWorker) lastScheduledRunTime(ctx context.Context) (time.Time, error) {
	run, found, err := w.store.GetLatestCodexInspectionRunByTriggerType(ctx, model.CodexInspectionTriggerScheduled)
	if err != nil {
		return time.Time{}, err
	}
	if !found {
		return time.Time{}, nil
	}
	if run.FinishedAtMS > 0 {
		return time.UnixMilli(run.FinishedAtMS), nil
	}
	if run.StartedAtMS > 0 {
		return time.UnixMilli(run.StartedAtMS), nil
	}
	return time.Time{}, nil
}
