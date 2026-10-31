package desktopmonitor

import (
	"context"
	"errors"
	"sync"
	"time"
)

type snapshotFetcher interface {
	Fetch(context.Context, time.Time) (Snapshot, error)
}
type pollResult struct {
	generation uint64
	snapshot   Snapshot
	err        error
}
type poller struct {
	mu             sync.Mutex
	fetcher        snapshotFetcher
	generation     uint64
	active, closed bool
	cancel         context.CancelFunc
	wake           chan struct{}
	stop           chan struct{}
	done           chan struct{}
	results        chan pollResult
	interval       time.Duration
}

func newPoller(interval time.Duration) *poller {
	p := &poller{wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{}), results: make(chan pollResult, 1), interval: interval}
	go p.run()
	return p
}

// Configure cancels current I/O immediately, but only the one worker ever
// executes Fetch. A replacement cannot race an old poll or post to dead HWNDs.
func (p *poller) Configure(f snapshotFetcher, g uint64, active bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if p.cancel != nil {
		p.cancel()
	}
	p.fetcher = f
	p.generation = g
	p.active = active
	select {
	case p.wake <- struct{}{}:
	default:
	}
}
func (p *poller) Close() {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		if p.cancel != nil {
			p.cancel()
		}
		close(p.stop)
	}
	p.mu.Unlock()
	<-p.done
}
func (p *poller) run() {
	defer close(p.done)
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-p.wake:
		case <-timer.C:
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return
		}
		f, g, active := p.fetcher, p.generation, p.active
		if !active || f == nil {
			p.mu.Unlock()
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		p.cancel = cancel
		p.mu.Unlock()
		snap, err := f.Fetch(ctx, time.Now())
		cancel()
		p.mu.Lock()
		current := !p.closed && p.generation == g && p.active
		if current && errors.Is(err, ErrUnauthorized) {
			p.active = false
		}
		if current {
			select {
			case <-p.results:
			default:
			}
			p.results <- pollResult{g, snap, err}
		}
		p.mu.Unlock()
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(p.interval)
	}
}
