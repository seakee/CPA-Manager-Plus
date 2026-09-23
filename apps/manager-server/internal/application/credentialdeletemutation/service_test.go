package credentialdeletemutation_test

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"sync/atomic"
	"testing"

	adapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/credentialdeletemutation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identitymutation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityreconcile"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
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
		return model.RuntimeObservedStatus{}, errors.New("runtime status unavailable")
	}
	status := s.statuses[s.calls]
	s.calls++
	return status, nil
}

type readyRuntime struct{}

func (readyRuntime) Status(context.Context) (model.RuntimeObservedStatus, error) {
	return model.RuntimeObservedStatus{State: model.RuntimeStateReady, Identity: "runtime-1", Generation: 1}, nil
}

type credentialInventory struct {
	credentials []identityinventory.CredentialObservation
	err         error
}

func (c credentialInventory) FetchAPIKeys(context.Context, string, string) ([]identityinventory.APIKeyObservation, error) {
	return nil, nil
}
func (c credentialInventory) FetchCredentials(context.Context, string, string) ([]identityinventory.CredentialObservation, error) {
	return c.credentials, c.err
}

type failOnceCompletionRepo struct {
	ports.CredentialDeleteRepository
	fail atomic.Bool
}

type selectiveCompletionRepo struct {
	ports.CredentialDeleteRepository
	failAll   bool
	badID     string
	attempted map[string]int
}

func (r *selectiveCompletionRepo) MarkCredentialDeleteForwardComplete(ctx context.Context, id, owner string, nowMS int64) error {
	r.attempted[id]++
	if r.failAll || id == r.badID {
		return errors.New("injected marker failure")
	}
	return r.CredentialDeleteRepository.MarkCredentialDeleteForwardComplete(ctx, id, owner, nowMS)
}

func TestRetryForwardCompletionsAttemptsEveryIntent(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "two-markers.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := adapter.New(db)
	if _, err := repo.ApplyPassiveSnapshot(ctx, ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1, NowMS: 1000,
		Credentials: []ports.CredentialSnapshotItem{{SourceAuthID: "A"}, {SourceAuthID: "B"}},
	}); err != nil {
		t.Fatal(err)
	}
	markers := &selectiveCompletionRepo{CredentialDeleteRepository: repo.(ports.CredentialDeleteRepository), failAll: true, attempted: make(map[string]int)}
	svc, err := credentialdeletemutation.NewService(credentialdeletemutation.Config{
		RuntimeObserver: readyRuntime{}, Repository: markers, ProcessInstanceID: "process-A", TimeSource: func() int64 { return 2000 },
	})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, source := range []string{"A", "B"} {
		id, err := svc.Prepare(ctx, source+".json", []string{source}, func(context.Context) (string, []string, error) { return source + ".json", []string{source}, nil })
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		if err := svc.MarkForwardComplete(ctx, id); err == nil {
			t.Fatal("injected marker failure missing")
		}
	}
	sort.Strings(ids)
	markers.failAll = false
	markers.badID = ids[0]
	if err := svc.RetryForwardCompletions(ctx); err == nil {
		t.Fatal("bad marker retry unexpectedly succeeded")
	}
	if markers.attempted[ids[1]] != 2 {
		t.Fatalf("second marker attempts=%d", markers.attempted[ids[1]])
	}
	var completed int64
	if err := db.QueryRow(`select forward_completed_at_ms from gateway_credential_delete_intents where id = ?`, ids[1]).Scan(&completed); err != nil || completed != 2000 {
		t.Fatalf("good marker not persisted: %d %v", completed, err)
	}
	markers.badID = ""
	if err := svc.RetryForwardCompletions(ctx); err != nil {
		t.Fatal(err)
	}
	if markers.attempted[ids[1]] != 2 {
		t.Fatal("successful marker retried again")
	}
}

func (r *failOnceCompletionRepo) MarkCredentialDeleteForwardComplete(ctx context.Context, id, owner string, nowMS int64) error {
	if r.fail.Swap(false) {
		return errors.New("temporary SQLite write failure")
	}
	return r.CredentialDeleteRepository.MarkCredentialDeleteForwardComplete(ctx, id, owner, nowMS)
}

func TestPrepareRequiresStableFenceAndSameVerifiedMembership(t *testing.T) {
	for _, tc := range []struct {
		name         string
		statuses     []model.RuntimeObservedStatus
		physicalName string
		sources      []string
	}{
		{"generation drift", []model.RuntimeObservedStatus{
			{State: model.RuntimeStateReady, Identity: "runtime-1", Generation: 1},
			{State: model.RuntimeStateReady, Identity: "runtime-1", Generation: 2},
		}, "shared.json", []string{"A"}},
		{"membership drift", []model.RuntimeObservedStatus{
			{State: model.RuntimeStateReady, Identity: "runtime-1", Generation: 1},
			{State: model.RuntimeStateReady, Identity: "runtime-1", Generation: 1},
		}, "shared.json", []string{"B"}},
		{"physical drift", []model.RuntimeObservedStatus{
			{State: model.RuntimeStateReady, Identity: "runtime-1", Generation: 1},
			{State: model.RuntimeStateReady, Identity: "runtime-1", Generation: 1},
		}, "other.json", []string{"A"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := sqlite.Open(filepath.Join(t.TempDir(), "fence.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			repo := adapter.New(db)
			if _, err := repo.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
				RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1,
				Credentials: []ports.CredentialSnapshotItem{{SourceAuthID: "A"}}, NowMS: 1000,
			}); err != nil {
				t.Fatal(err)
			}
			svc, err := credentialdeletemutation.NewService(credentialdeletemutation.Config{
				RuntimeObserver: &runtimeSequence{statuses: tc.statuses},
				Repository:      repo.(ports.CredentialDeleteRepository), ProcessInstanceID: "process-A",
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = svc.Prepare(context.Background(), "shared.json", []string{"A"},
				func(context.Context) (string, []string, error) { return tc.physicalName, tc.sources, nil })
			if err == nil {
				t.Fatal("unsafe revalidation committed an intent")
			}
			if err := svc.CheckOverlap(context.Background(), []string{"shared.json"}, false); err != nil {
				t.Fatalf("failed preparation left intent: %v", err)
			}
		})
	}
}

func TestFailedCompletionMarkerRetriesBeforePassiveCaptureDespiteWallRollback(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "completion.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := adapter.New(db)
	if _, err := repo.ApplyPassiveSnapshot(ctx, ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1,
		Credentials: []ports.CredentialSnapshotItem{{SourceAuthID: "A"}}, NowMS: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	old, _, err := repo.FindActiveCredentialBySource(ctx, "runtime-1", "A")
	if err != nil {
		t.Fatal(err)
	}
	var wall atomic.Int64
	wall.Store(2000)
	clock := identitymutation.NewMonotonicMillis(wall.Load)
	mutationRepo := &failOnceCompletionRepo{CredentialDeleteRepository: repo.(ports.CredentialDeleteRepository)}
	mutationRepo.fail.Store(true)
	svc, err := credentialdeletemutation.NewService(credentialdeletemutation.Config{
		RuntimeObserver: readyRuntime{},
		Repository:      mutationRepo, ProcessInstanceID: "process-A", TimeSource: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := svc.Prepare(ctx, "shared.json", []string{"A"},
		func(context.Context) (string, []string, error) { return "shared.json", []string{"A"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkForwardComplete(ctx, id); err == nil {
		t.Fatal("completion marker did not fail")
	}
	wall.Store(1)
	passive, err := identityreconcile.NewService(identityreconcile.Config{
		RuntimeObserver:    readyRuntime{},
		ConnectionResolver: func(context.Context) (string, string, error) { return "http://cpa", "key", nil },
		InventoryClient:    credentialInventory{}, IdentityRepo: repo,
		ProcessInstanceID: "process-A", TimeSource: clock,
		BeforeCapture: svc.RetryForwardCompletions,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := passive.ReconcileOnce(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := repo.LoadCredentialByID(ctx, old.ID)
	if err != nil || after.Lifecycle != identity.LifecycleSuperseded || after.Revision != old.Revision+1 {
		t.Fatalf("recovered deletion: %+v %v", after, err)
	}
	if err := svc.CheckOverlap(ctx, []string{"shared.json"}, false); err != nil {
		t.Fatalf("resolved intent remains: %v", err)
	}
}

func TestStrictObservationFailureKeepsIntentUnknown(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "observation.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo := adapter.New(db)
	if _, err := repo.ApplyPassiveSnapshot(ctx, ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1,
		Credentials: []ports.CredentialSnapshotItem{{SourceAuthID: "A"}}, NowMS: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	svc, err := credentialdeletemutation.NewService(credentialdeletemutation.Config{
		RuntimeObserver: readyRuntime{},
		Repository:      repo.(ports.CredentialDeleteRepository), ProcessInstanceID: "process-A",
	})
	if err != nil {
		t.Fatal(err)
	}
	id, err := svc.Prepare(ctx, "shared.json", []string{"A"},
		func(context.Context) (string, []string, error) { return "shared.json", []string{"A"}, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.MarkForwardComplete(ctx, id); err != nil {
		t.Fatal(err)
	}
	outcome, err := svc.Observe(ctx, id, func(context.Context) ([]string, error) {
		return nil, errors.New("malformed envelope")
	})
	if err == nil || outcome != ports.CredentialDeleteUnknown {
		t.Fatalf("strict observation outcome=%q err=%v", outcome, err)
	}
	if err := svc.CheckOverlap(ctx, []string{"shared.json"}, false); !errors.Is(err, ports.ErrPendingCredentialDelete) {
		t.Fatalf("unknown intent no longer protects file: %v", err)
	}
}
