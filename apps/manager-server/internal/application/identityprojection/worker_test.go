package identityprojection

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

type mockRepo struct {
	catchUpCount  int32
	pending       bool
	catchUpErr    error
	recordFailErr error
}

func (m *mockRepo) CatchUp(ctx context.Context, limit int, nowMS int64) (ports.CatchUpResult, error) {
	atomic.AddInt32(&m.catchUpCount, 1)
	if m.catchUpErr != nil {
		return ports.CatchUpResult{}, m.catchUpErr
	}
	return ports.CatchUpResult{
		Processed:            limit,
		LastProcessedEventID: int64(limit),
		TargetEventID:        int64(limit),
		Pending:              m.pending,
	}, nil
}

func (m *mockRepo) RecordFailure(ctx context.Context, err error, nowMS int64) error {
	return m.recordFailErr
}

func (m *mockRepo) GetState(ctx context.Context) (ports.State, error) {
	return ports.State{}, nil
}

func (m *mockRepo) GetProjectionByEventID(ctx context.Context, eventID int64) (*ports.UsageIdentityProjection, error) {
	return nil, nil
}

func (m *mockRepo) GetProjectionByEventHash(ctx context.Context, eventHash string) (*ports.UsageIdentityProjection, error) {
	return nil, nil
}

func (m *mockRepo) Reset(ctx context.Context) error {
	return nil
}

func TestWorkerLifecycle(t *testing.T) {
	repo := &mockRepo{pending: false}
	worker := NewWorker(repo)
	worker.SetBatchLimits(10, 2)
	worker.checkInterval = 50 * time.Millisecond
	worker.continuationDelay = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	worker.Start(ctx)

	// Wait for worker to catch up initially
	time.Sleep(30 * time.Millisecond)
	count1 := atomic.LoadInt32(&repo.catchUpCount)
	if count1 == 0 {
		t.Fatal("expected at least 1 catch-up call on start")
	}

	// Trigger via HandleUsageEvents
	worker.HandleUsageEvents(ctx, []usage.Event{{EventHash: "h1"}})
	time.Sleep(30 * time.Millisecond)
	count2 := atomic.LoadInt32(&repo.catchUpCount)
	if count2 <= count1 {
		t.Fatalf("expected catch-up count to increase after HandleUsageEvents, was %d now %d", count1, count2)
	}

	// Cancel context and verify shutdown
	cancel()
	time.Sleep(20 * time.Millisecond)
}
