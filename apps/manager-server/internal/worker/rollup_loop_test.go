package worker

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestWorkerLifecycleGoRunIsIdempotentAndHonoursStopAndCanceledContext(t *testing.T) {
	t.Run("nil and canceled contexts do not mark started and subsequent live Start runs once", func(t *testing.T) {
		var l workerLifecycle
		var runs atomic.Int32
		startedCh := make(chan struct{})
		releaseCh := make(chan struct{})

		var nilCtx context.Context
		if l.goRun(nilCtx, func() { runs.Add(1) }) {
			t.Fatal("goRun with nil context returned true, want false")
		}

		canceledCtx, cancel := context.WithCancel(context.Background())
		cancel()
		if l.goRun(canceledCtx, func() { runs.Add(1) }) {
			t.Fatal("goRun with canceled context returned true, want false")
		}
		liveCtx, cancelLive := context.WithCancel(context.Background())
		defer cancelLive()
		if !l.goRun(liveCtx, func() {
			runs.Add(1)
			close(startedCh)
			<-releaseCh
		}) {
			t.Fatal("first live goRun returned false, want true")
		}
		if l.goRun(liveCtx, func() { runs.Add(1) }) {
			t.Fatal("second live goRun returned true, want false (already started)")
		}

		<-startedCh
		close(releaseCh)
		l.wait()

		if l.goRun(context.Background(), func() { runs.Add(1) }) {
			t.Fatal("goRun after wait() returned true, want false")
		}
		if got := runs.Load(); got != 1 {
			t.Fatalf("goroutine run count = %d, want 1", got)
		}
	})

	t.Run("rollup worker Start called repeatedly spawns only one loop", func(t *testing.T) {
		st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
		if err != nil {
			t.Fatalf("open store: %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })

		ctx, cancel := context.WithCancel(context.Background())
		w := NewDashboardHourlyRollupWorker(st)
		w.checkInterval = time.Hour
		w.Start(ctx)
		w.Start(ctx)
		w.Start(ctx)

		cancel()
		done := make(chan struct{})
		go func() {
			w.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("DashboardHourlyRollupWorker.Wait() hung after repeated Start calls")
		}
	})
}
