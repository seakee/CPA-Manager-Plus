package identitymutation_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"

	adapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identitymutation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityreconcile"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityinventory"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

type runtimeSequence struct {
	statuses []model.RuntimeObservedStatus
	calls    int
}

type readyRuntime struct{}

func (readyRuntime) Status(context.Context) (model.RuntimeObservedStatus, error) {
	return model.RuntimeObservedStatus{State: model.RuntimeStateReady, Identity: "runtime-1", Generation: 1}, nil
}

type failOnceCompletionRepo struct {
	ports.MutationRepository
	fail atomic.Bool
}

func (r *failOnceCompletionRepo) MarkAPIKeyMutationForwardComplete(ctx context.Context, id, owner string, nowMS int64) error {
	if r.fail.Swap(false) {
		return errors.New("temporary SQLite write failure")
	}
	return r.MutationRepository.MarkAPIKeyMutationForwardComplete(ctx, id, owner, nowMS)
}

func TestSameProcessRecoversFailedCompletionMarkerAfterClockRollback(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "marker-recovery.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := adapter.New(db)
	oldHash, newHash := hash("old"), hash("new")
	if _, err := repo.ApplyPassiveSnapshot(ctx, ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1,
		APIKeys: []ports.APIKeySnapshotItem{{APIKeyHash: oldHash}}, NowMS: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	before, _, err := repo.FindActiveAPIKeyBySource(ctx, "runtime-1", oldHash)
	if err != nil {
		t.Fatal(err)
	}
	var wall atomic.Int64
	wall.Store(2000)
	clock := identitymutation.NewMonotonicMillis(wall.Load)
	mutRepo := &failOnceCompletionRepo{MutationRepository: repo.(ports.MutationRepository)}
	mutRepo.fail.Store(true)
	mutationSvc, err := identitymutation.NewService(identitymutation.Config{
		RuntimeObserver: readyRuntime{}, InventoryClient: hashInventory{},
		Repository: mutRepo, ProcessInstanceID: "process-A", TimeSource: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	intentID, err := mutationSvc.Prepare(ctx, ports.APIKeyMutationRotate,
		func(context.Context) (ports.APIKeyMutationEvidence, error) {
			return ports.APIKeyMutationEvidence{
				OldHash: oldHash, NewHash: newHash, ExactOldCount: 1, NormalizedOldCount: 1,
			}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	// The upstream request has returned after applying old -> new. Its first
	// durable completion-marker write fails, while this process keeps running.
	if err := mutationSvc.MarkForwardComplete(ctx, intentID); err == nil {
		t.Fatal("completion marker write should fail once")
	}
	wall.Store(1)
	passive, err := identityreconcile.NewService(identityreconcile.Config{
		RuntimeObserver:    readyRuntime{},
		ConnectionResolver: func(context.Context) (string, string, error) { return "http://cpa", "key", nil },
		InventoryClient:    hashInventory{hashes: []identityinventory.APIKeyObservation{{KeyHash: newHash}}},
		IdentityRepo:       repo, ProcessInstanceID: "process-A", TimeSource: clock,
		BeforeCapture: mutationSvc.RetryForwardCompletions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := passive.ReconcileOnce(ctx); err != nil {
		t.Fatalf("same-process worker did not recover intent: %v", err)
	}
	after, _, err := repo.FindActiveAPIKeyBySource(ctx, "runtime-1", newHash)
	if err != nil || after.ID != before.ID || after.Revision != before.Revision+1 {
		t.Fatalf("recovered identity: %+v %v", after, err)
	}
	pending, err := mutRepo.HasPendingAPIKeyMutation(ctx, "runtime-1")
	if err != nil || pending {
		t.Fatalf("recovered intent remains: %v %v", pending, err)
	}
}

func (s *runtimeSequence) Status(context.Context) (model.RuntimeObservedStatus, error) {
	if s.calls >= len(s.statuses) {
		return model.RuntimeObservedStatus{}, errors.New("status sequence exhausted")
	}
	status := s.statuses[s.calls]
	s.calls++
	return status, nil
}

type hashInventory struct {
	hashes []identityinventory.APIKeyObservation
}

func (i hashInventory) FetchAPIKeys(context.Context, string, string) ([]identityinventory.APIKeyObservation, error) {
	return i.hashes, nil
}
func (hashInventory) FetchCredentials(context.Context, string, string) ([]identityinventory.CredentialObservation, error) {
	return nil, nil
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestMutationPreflightRuntimeFenceMakesNoDurableIntent(t *testing.T) {
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "fence.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := adapter.New(db)
	oldHash, newHash := hash("old"), hash("new")
	_, err = repo.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1,
		APIKeys: []ports.APIKeySnapshotItem{{APIKeyHash: oldHash}}, NowMS: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	observer := &runtimeSequence{statuses: []model.RuntimeObservedStatus{
		{State: model.RuntimeStateReady, Identity: "runtime-1", Generation: 1},
		{State: model.RuntimeStateReady, Identity: "runtime-1", Generation: 2},
	}}
	svc, err := identitymutation.NewService(identitymutation.Config{
		RuntimeObserver: observer, InventoryClient: hashInventory{},
		Repository: repo.(ports.MutationRepository), ProcessInstanceID: "process-A",
		TimeSource: func() int64 { return 2000 },
	})
	if err != nil {
		t.Fatal(err)
	}
	evidenceCalled := 0
	_, err = svc.Prepare(context.Background(), ports.APIKeyMutationRotate,
		func(context.Context) (ports.APIKeyMutationEvidence, error) {
			evidenceCalled++
			return ports.APIKeyMutationEvidence{
				OldHash: oldHash, NewHash: newHash, ExactOldCount: 1,
				NormalizedOldCount: 1,
			}, nil
		})
	if !errors.Is(err, identitymutation.ErrRuntimeFence) || evidenceCalled != 1 {
		t.Fatalf("runtime fence not enforced: %v calls=%d", err, evidenceCalled)
	}
	pending, err := repo.(ports.MutationRepository).HasPendingAPIKeyMutation(context.Background(), "runtime-1")
	if err != nil || pending {
		t.Fatalf("preflight wrote intent: pending=%v err=%v", pending, err)
	}
}
