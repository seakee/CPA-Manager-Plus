package worker

import (
	"context"
	"sync"
	"time"
)

type workerLifecycle struct {
	mu      sync.Mutex
	wg      sync.WaitGroup
	started bool
	stopped bool
}

func (l *workerLifecycle) goRun(ctx context.Context, fn func()) bool {
	if ctx == nil || ctx.Err() != nil {
		return false
	}
	l.mu.Lock()
	if l.stopped || l.started || ctx.Err() != nil {
		l.mu.Unlock()
		return false
	}
	l.started = true
	l.wg.Add(1)
	l.mu.Unlock()
	go func() {
		defer l.wg.Done()
		fn()
	}()
	return true
}

func (l *workerLifecycle) wait() {
	l.mu.Lock()
	l.stopped = true
	l.mu.Unlock()
	l.wg.Wait()
}

const defaultRollupContinuationDelay = 250 * time.Millisecond

func runRollupLoop(
	ctx context.Context,
	wake <-chan struct{},
	checkInterval time.Duration,
	continuationDelay time.Duration,
	catchUp func(context.Context) bool,
) {
	if checkInterval <= 0 {
		checkInterval = 30 * time.Second
	}
	if continuationDelay <= 0 {
		continuationDelay = defaultRollupContinuationDelay
	}
	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	var continuationTimer *time.Timer
	var continuation <-chan time.Time
	stopContinuation := func() {
		if continuationTimer == nil {
			return
		}
		if !continuationTimer.Stop() {
			select {
			case <-continuationTimer.C:
			default:
			}
		}
		continuationTimer = nil
		continuation = nil
	}
	defer stopContinuation()

	run := func() {
		stopContinuation()
		if !catchUp(ctx) || ctx.Err() != nil {
			return
		}
		continuationTimer = time.NewTimer(continuationDelay)
		continuation = continuationTimer.C
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			run()
		case <-ticker.C:
			run()
		case <-continuation:
			continuationTimer = nil
			continuation = nil
			run()
		}
	}
}
