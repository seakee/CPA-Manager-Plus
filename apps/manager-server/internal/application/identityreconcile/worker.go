package identityreconcile

import (
	"context"
	"time"
)

// DefaultInterval is the periodic interval for identity reconciliation (30 seconds).
const DefaultInterval = 30 * time.Second

// Logger is the logging function type.
type Logger func(format string, args ...any)

// Worker runs the identity reconciliation process on a periodic loop for Embedded Runtime.
type Worker struct {
	service  *Service
	interval time.Duration
	logger   Logger

	lastFailed bool
	lastErrMsg string
}

// NewWorker creates a new reconciliation worker.
func NewWorker(service *Service, logger Logger) *Worker {
	if logger == nil {
		logger = func(format string, args ...any) {}
	}
	return &Worker{
		service:  service,
		interval: DefaultInterval,
		logger:   logger,
	}
}

// SetInterval overrides the interval (useful for tests).
func (w *Worker) SetInterval(d time.Duration) {
	if d > 0 {
		w.interval = d
	}
}

// Run starts the worker loop. It performs an immediate initial run, then repeats every interval.
// Canceling ctx exits cleanly.
func (w *Worker) Run(ctx context.Context) {
	// Startup immediate run
	w.step(ctx)

	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.step(ctx)
		}
	}
}

func (w *Worker) step(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	_, err := w.service.ReconcileOnce(ctx)
	if err == nil {
		if w.lastFailed {
			w.logger("[identity-reconcile] recovered from reconciliation failure")
			w.lastFailed = false
			w.lastErrMsg = ""
		}
		return
	}

	if ctx.Err() != nil {
		return
	}

	errMsg := err.Error()
	if !w.lastFailed {
		w.logger("[identity-reconcile] reconciliation waiting: %v", err)
		w.lastFailed = true
		w.lastErrMsg = errMsg
	} else if errMsg != w.lastErrMsg {
		w.logger("[identity-reconcile] reconciliation error changed: %v", err)
		w.lastErrMsg = errMsg
	}
	// If same error repeated, do not spam logs
}
