package identitymutation_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"

	adapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identitymutation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityinventory"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

type runtimeSequence struct {
	statuses []model.RuntimeObservedStatus
	calls    int
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
