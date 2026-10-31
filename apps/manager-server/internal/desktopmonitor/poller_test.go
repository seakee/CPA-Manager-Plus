package desktopmonitor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type fetchFunc func(context.Context, time.Time) (Snapshot, error)

func (f fetchFunc) Fetch(c context.Context, n time.Time) (Snapshot, error) { return f(c, n) }
func TestPollerSerializesAndCancelsReconnectHideClose(t *testing.T) {
	var active, maxActive atomic.Int32
	entered := make(chan struct{}, 4)
	canceled := make(chan struct{}, 4)
	slow := fetchFunc(func(ctx context.Context, now time.Time) (Snapshot, error) {
		n := active.Add(1)
		if n > maxActive.Load() {
			maxActive.Store(n)
		}
		defer active.Add(-1)
		entered <- struct{}{}
		<-ctx.Done()
		canceled <- struct{}{}
		// Even an uncooperative late completion must carry its old generation.
		return Snapshot{Requests: []Request{{Model: "old"}}}, nil
	})
	p := newPoller(time.Hour)
	defer p.Close()
	p.Configure(slow, 1, true)
	<-entered
	p.Configure(slow, 2, true)
	<-canceled
	<-entered
	p.Configure(nil, 3, false)
	<-canceled
	p.Configure(slow, 4, true)
	<-entered
	p.Close()
	<-canceled
	if maxActive.Load() != 1 || active.Load() != 0 {
		t.Fatalf("overlap %d active %d", maxActive.Load(), active.Load())
	}
	select {
	case <-p.done:
	case <-time.After(time.Second):
		t.Fatal("worker alive after close")
	}
}
func TestPollerResultsAndStoppedAuth(t *testing.T) {
	p := newPoller(5 * time.Millisecond)
	defer p.Close()
	p.Configure(fetchFunc(func(ctx context.Context, n time.Time) (Snapshot, error) { return Snapshot{}, ErrUnauthorized }), 7, true)
	select {
	case r := <-p.results:
		if r.generation != 7 || !errors.Is(r.err, ErrUnauthorized) {
			t.Fatal(r)
		}
	case <-time.After(time.Second):
		t.Fatal("no result")
	}
	select {
	case <-p.results:
		t.Fatal("auth kept polling")
	case <-time.After(30 * time.Millisecond):
	}
}
