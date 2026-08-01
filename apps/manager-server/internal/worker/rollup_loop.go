package worker

import (
	"context"
	"time"
)

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
