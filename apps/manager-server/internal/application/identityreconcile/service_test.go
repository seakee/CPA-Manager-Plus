package identityreconcile_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	adaptersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityreconcile"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityinventory"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

type sequenceRuntimeObserver struct {
	mu       sync.Mutex
	statuses []func() (model.RuntimeObservedStatus, error)
	callIdx  int
}

func (s *sequenceRuntimeObserver) Status(ctx context.Context) (model.RuntimeObservedStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.callIdx < len(s.statuses) {
		fn := s.statuses[s.callIdx]
		s.callIdx++
		return fn()
	}
	return model.RuntimeObservedStatus{}, errors.New("no more statuses configured")
}

type recordingIdentityRepo struct {
	mu          sync.Mutex
	applyCalls  []ports.ReconcileSnapshotParams
	applyResult ports.ReconcileSnapshotResult
	applyErr    error
}

func (r *recordingIdentityRepo) CreateAPIKey(ctx context.Context, ident identity.APIKeyIdentity, binding identity.APIKeySourceBinding) error {
	return nil
}
func (r *recordingIdentityRepo) CreateCredential(ctx context.Context, ident identity.CredentialIdentity, binding identity.CredentialSourceBinding) error {
	return nil
}
func (r *recordingIdentityRepo) LoadAPIKeyByID(ctx context.Context, id identity.APIKeyID) (identity.APIKeyIdentity, error) {
	return identity.APIKeyIdentity{}, ports.ErrNotFound
}
func (r *recordingIdentityRepo) LoadCredentialByID(ctx context.Context, id identity.CredentialID) (identity.CredentialIdentity, error) {
	return identity.CredentialIdentity{}, ports.ErrNotFound
}
func (r *recordingIdentityRepo) FindActiveAPIKeyBySource(ctx context.Context, runtimeIdentity, apiKeyHash string) (identity.APIKeyIdentity, identity.APIKeySourceBinding, error) {
	return identity.APIKeyIdentity{}, identity.APIKeySourceBinding{}, ports.ErrNotFound
}
func (r *recordingIdentityRepo) FindActiveCredentialBySource(ctx context.Context, runtimeIdentity, sourceAuthID string) (identity.CredentialIdentity, identity.CredentialSourceBinding, error) {
	return identity.CredentialIdentity{}, identity.CredentialSourceBinding{}, ports.ErrNotFound
}
func (r *recordingIdentityRepo) SetAPIKeyLifecycle(ctx context.Context, id identity.APIKeyID, expectedRevision identity.Revision, nextLifecycle identity.Lifecycle, nowMS int64) (identity.APIKeyIdentity, error) {
	return identity.APIKeyIdentity{}, nil
}
func (r *recordingIdentityRepo) SetCredentialLifecycle(ctx context.Context, id identity.CredentialID, expectedRevision identity.Revision, nextLifecycle identity.Lifecycle, nowMS int64) (identity.CredentialIdentity, error) {
	return identity.CredentialIdentity{}, nil
}
func (r *recordingIdentityRepo) ApplyPassiveSnapshot(ctx context.Context, params ports.ReconcileSnapshotParams) (ports.ReconcileSnapshotResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applyCalls = append(r.applyCalls, params)
	return r.applyResult, r.applyErr
}

type fakeInventoryClient struct {
	apiKeys    []identityinventory.APIKeyObservation
	apiKeysErr error
	creds      []identityinventory.CredentialObservation
	credsErr   error
}

func (f *fakeInventoryClient) FetchAPIKeys(ctx context.Context, baseURL string, managementKey string) ([]identityinventory.APIKeyObservation, error) {
	return f.apiKeys, f.apiKeysErr
}

func (f *fakeInventoryClient) FetchCredentials(ctx context.Context, baseURL string, managementKey string) ([]identityinventory.CredentialObservation, error) {
	return f.creds, f.credsErr
}

func validReadyStatus() model.RuntimeObservedStatus {
	return model.RuntimeObservedStatus{
		Identity:   "runtime-1",
		Generation: 10,
		State:      model.RuntimeStateReady,
	}
}

func staticConnectionResolver(baseURL, key string) identityreconcile.ConnectionResolver {
	return func(ctx context.Context) (string, string, error) {
		return baseURL, key, nil
	}
}

func TestReconcileOnce_Success_ApplyExactlyOnce(t *testing.T) {
	ready := validReadyStatus()
	observer := &sequenceRuntimeObserver{
		statuses: []func() (model.RuntimeObservedStatus, error){
			func() (model.RuntimeObservedStatus, error) { return ready, nil }, // pre
			func() (model.RuntimeObservedStatus, error) { return ready, nil }, // post
		},
	}
	repo := &recordingIdentityRepo{
		applyResult: ports.ReconcileSnapshotResult{APIKeysCreated: 1, CredentialsCreated: 1},
	}
	inventory := &fakeInventoryClient{
		apiKeys: []identityinventory.APIKeyObservation{{KeyHash: strings.Repeat("a", 64)}},
		creds:   []identityinventory.CredentialObservation{{SourceAuthID: "auth-1"}},
	}

	svc, err := identityreconcile.NewService(identityreconcile.Config{
		RuntimeObserver:    observer,
		ConnectionResolver: staticConnectionResolver("http://localhost:8080", "key"),
		InventoryClient:    inventory,
		IdentityRepo:       repo,
		TimeSource:         func() int64 { return 1000 },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	res, err := svc.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce failed: %v", err)
	}

	if len(repo.applyCalls) != 1 {
		t.Fatalf("expected exactly 1 apply call, got %d", len(repo.applyCalls))
	}
	if res.APIKeysCreated != 1 || res.CredentialsCreated != 1 {
		t.Errorf("res = %+v", res)
	}
	if repo.applyCalls[0].RuntimeIdentity != "runtime-1" || repo.applyCalls[0].ObservedRuntimeGeneration != 10 {
		t.Errorf("apply params: %+v", repo.applyCalls[0])
	}
}

func TestReconcileOnce_RuntimeFence_PreChecks_ZeroApply(t *testing.T) {
	cases := []struct {
		name      string
		preStatus model.RuntimeObservedStatus
		preErr    error
		wantErr   error
	}{
		{
			name:    "pre status error",
			preErr:  errors.New("connection refused"),
			wantErr: identityreconcile.ErrRuntimeUnavailable,
		},
		{
			name: "pre not ready (starting)",
			preStatus: model.RuntimeObservedStatus{
				Identity:   "runtime-1",
				Generation: 10,
				State:      model.RuntimeStateStarting,
			},
			wantErr: identityreconcile.ErrRuntimeNotReady,
		},
		{
			name: "pre empty identity",
			preStatus: model.RuntimeObservedStatus{
				Identity:   "",
				Generation: 10,
				State:      model.RuntimeStateReady,
			},
			wantErr: identityreconcile.ErrRuntimeObservationIncomplete,
		},
		{
			name: "pre zero generation",
			preStatus: model.RuntimeObservedStatus{
				Identity:   "runtime-1",
				Generation: 0,
				State:      model.RuntimeStateReady,
			},
			wantErr: identityreconcile.ErrRuntimeObservationIncomplete,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observer := &sequenceRuntimeObserver{
				statuses: []func() (model.RuntimeObservedStatus, error){
					func() (model.RuntimeObservedStatus, error) { return tc.preStatus, tc.preErr },
				},
			}
			repo := &recordingIdentityRepo{}
			inventory := &fakeInventoryClient{}

			svc, err := identityreconcile.NewService(identityreconcile.Config{
				RuntimeObserver:    observer,
				ConnectionResolver: staticConnectionResolver("http://localhost:8080", "key"),
				InventoryClient:    inventory,
				IdentityRepo:       repo,
			})
			if err != nil {
				t.Fatalf("new service: %v", err)
			}

			_, err = svc.ReconcileOnce(context.Background())
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("got %v, want %v", err, tc.wantErr)
			}
			if len(repo.applyCalls) != 0 {
				t.Errorf("expected 0 apply calls, got %d", len(repo.applyCalls))
			}
		})
	}
}

func TestReconcileOnce_InventoryFailure_ZeroApply(t *testing.T) {
	ready := validReadyStatus()

	t.Run("api key fetch failure", func(t *testing.T) {
		observer := &sequenceRuntimeObserver{
			statuses: []func() (model.RuntimeObservedStatus, error){
				func() (model.RuntimeObservedStatus, error) { return ready, nil },
			},
		}
		repo := &recordingIdentityRepo{}
		inventory := &fakeInventoryClient{
			apiKeysErr: errors.New("network timeout"),
		}

		svc, _ := identityreconcile.NewService(identityreconcile.Config{
			RuntimeObserver:    observer,
			ConnectionResolver: staticConnectionResolver("http://localhost:8080", "key"),
			InventoryClient:    inventory,
			IdentityRepo:       repo,
		})

		_, err := svc.ReconcileOnce(context.Background())
		if !errors.Is(err, identityreconcile.ErrAPIKeyInventoryFailed) {
			t.Errorf("got %v, want ErrAPIKeyInventoryFailed", err)
		}
		if len(repo.applyCalls) != 0 {
			t.Errorf("expected 0 apply calls, got %d", len(repo.applyCalls))
		}
	})

	t.Run("credential fetch failure", func(t *testing.T) {
		observer := &sequenceRuntimeObserver{
			statuses: []func() (model.RuntimeObservedStatus, error){
				func() (model.RuntimeObservedStatus, error) { return ready, nil },
			},
		}
		repo := &recordingIdentityRepo{}
		inventory := &fakeInventoryClient{
			credsErr: errors.New("auth-files malformed"),
		}

		svc, _ := identityreconcile.NewService(identityreconcile.Config{
			RuntimeObserver:    observer,
			ConnectionResolver: staticConnectionResolver("http://localhost:8080", "key"),
			InventoryClient:    inventory,
			IdentityRepo:       repo,
		})

		_, err := svc.ReconcileOnce(context.Background())
		if !errors.Is(err, identityreconcile.ErrCredentialInventoryFailed) {
			t.Errorf("got %v, want ErrCredentialInventoryFailed", err)
		}
		if len(repo.applyCalls) != 0 {
			t.Errorf("expected 0 apply calls, got %d", len(repo.applyCalls))
		}
	})
}

func TestReconcileOnce_RuntimeFence_PostChecks_ZeroApply(t *testing.T) {
	ready := validReadyStatus()

	cases := []struct {
		name       string
		postStatus model.RuntimeObservedStatus
		postErr    error
	}{
		{
			name:    "post status error",
			postErr: errors.New("supervisor crashed"),
		},
		{
			name: "post not ready",
			postStatus: model.RuntimeObservedStatus{
				Identity:   "runtime-1",
				Generation: 10,
				State:      model.RuntimeStateOffline,
			},
		},
		{
			name: "post identity changed",
			postStatus: model.RuntimeObservedStatus{
				Identity:   "runtime-2-different",
				Generation: 10,
				State:      model.RuntimeStateReady,
			},
		},
		{
			name: "post generation changed (restart during capture)",
			postStatus: model.RuntimeObservedStatus{
				Identity:   "runtime-1",
				Generation: 11, // restarted!
				State:      model.RuntimeStateReady,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observer := &sequenceRuntimeObserver{
				statuses: []func() (model.RuntimeObservedStatus, error){
					func() (model.RuntimeObservedStatus, error) { return ready, nil },                // pre
					func() (model.RuntimeObservedStatus, error) { return tc.postStatus, tc.postErr }, // post
				},
			}
			repo := &recordingIdentityRepo{}
			inventory := &fakeInventoryClient{
				apiKeys: []identityinventory.APIKeyObservation{{KeyHash: strings.Repeat("a", 64)}},
				creds:   []identityinventory.CredentialObservation{{SourceAuthID: "auth-1"}},
			}

			svc, _ := identityreconcile.NewService(identityreconcile.Config{
				RuntimeObserver:    observer,
				ConnectionResolver: staticConnectionResolver("http://localhost:8080", "key"),
				InventoryClient:    inventory,
				IdentityRepo:       repo,
			})

			_, err := svc.ReconcileOnce(context.Background())
			if !errors.Is(err, identityreconcile.ErrRuntimeFenceChanged) {
				t.Errorf("got %v, want ErrRuntimeFenceChanged", err)
			}
			if len(repo.applyCalls) != 0 {
				t.Errorf("expected 0 apply calls, got %d", len(repo.applyCalls))
			}
		})
	}
}

func TestReconcileOnce_ImmutableConnectionResolvedOnce(t *testing.T) {
	ready := validReadyStatus()
	observer := &sequenceRuntimeObserver{
		statuses: []func() (model.RuntimeObservedStatus, error){
			func() (model.RuntimeObservedStatus, error) { return ready, nil },
			func() (model.RuntimeObservedStatus, error) { return ready, nil },
		},
	}
	repo := &recordingIdentityRepo{}

	resolveCount := 0
	resolver := func(ctx context.Context) (string, string, error) {
		resolveCount++
		return "http://localhost:8080", "key-mgmt", nil
	}

	inventory := &fakeInventoryClient{}

	svc, _ := identityreconcile.NewService(identityreconcile.Config{
		RuntimeObserver:    observer,
		ConnectionResolver: resolver,
		InventoryClient:    inventory,
		IdentityRepo:       repo,
	})

	_, err := svc.ReconcileOnce(context.Background())
	if err != nil {
		t.Fatalf("ReconcileOnce failed: %v", err)
	}

	if resolveCount != 1 {
		t.Errorf("ConnectionResolver called %d times, want exactly 1", resolveCount)
	}
}

// TestRuntimeOnlyRegression verifies Rule 27:
// A present runtime_only entry must not be enrolled into 02A,
// and its later absence must therefore not mutate Canonical lifecycle.
func TestRuntimeOnlyRegression_WithRealSQLite(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test-runtime-only.sqlite")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	repo := adaptersqlite.New(db)
	ready := validReadyStatus()

	// Run 1: File 1 is normal supported credential.
	// File 2 is runtime_only=true, which gets excluded by the inventory adapter.
	// So inventoryClient only returns File 1.
	inventory := &fakeInventoryClient{
		creds: []identityinventory.CredentialObservation{
			{SourceAuthID: "supported-cred-1", PhysicalName: "f1.json"},
		},
	}

	observer := &sequenceRuntimeObserver{
		statuses: []func() (model.RuntimeObservedStatus, error){
			func() (model.RuntimeObservedStatus, error) { return ready, nil },
			func() (model.RuntimeObservedStatus, error) { return ready, nil },
			func() (model.RuntimeObservedStatus, error) { return ready, nil },
			func() (model.RuntimeObservedStatus, error) { return ready, nil },
		},
	}

	svc, err := identityreconcile.NewService(identityreconcile.Config{
		RuntimeObserver:    observer,
		ConnectionResolver: staticConnectionResolver("http://localhost:8080", "key"),
		InventoryClient:    inventory,
		IdentityRepo:       repo,
		TimeSource:         func() int64 { return 1000 },
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	// First run: supported-cred-1 is enrolled
	res1, err := svc.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if res1.CredentialsCreated != 1 {
		t.Errorf("expected 1 credential created, got %d", res1.CredentialsCreated)
	}

	cred1, _, err := repo.FindActiveCredentialBySource(ctx, "runtime-1", "supported-cred-1")
	if err != nil {
		t.Fatalf("find cred 1: %v", err)
	}
	if cred1.Revision != 1 || cred1.Lifecycle != identity.LifecycleActive {
		t.Errorf("cred1 state: %+v", cred1)
	}

	// Run 2: The runtime-only file has disappeared from CPA.
	// The inventory client still returns supported-cred-1.
	// Because runtime_only was never enrolled, its absence causes ZERO canonical changes!
	res2, err := svc.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if res2.CredentialsMissing != 0 || res2.CredentialsCreated != 0 {
		t.Errorf("unexpected mutations on run 2: %+v", res2)
	}

	cred1After, _, err := repo.FindActiveCredentialBySource(ctx, "runtime-1", "supported-cred-1")
	if err != nil {
		t.Fatalf("find cred 1 after run 2: %v", err)
	}
	if cred1After.Revision != 1 || cred1After.Lifecycle != identity.LifecycleActive {
		t.Errorf("cred1 revision was mutated! %+v", cred1After)
	}
}

func TestWorker_LogStateTransitionsAndDeduplication(t *testing.T) {
	var mu sync.Mutex
	statusFn := func() (model.RuntimeObservedStatus, error) {
		return model.RuntimeObservedStatus{}, errors.New("initial failure")
	}

	observer := &sequenceRuntimeObserver{}
	// dynamically get status
	dynObserver := &dynamicRuntimeObserver{
		fn: func() (model.RuntimeObservedStatus, error) {
			mu.Lock()
			defer mu.Unlock()
			return statusFn()
		},
	}

	repo := &recordingIdentityRepo{}
	inventory := &fakeInventoryClient{}

	svc, err := identityreconcile.NewService(identityreconcile.Config{
		RuntimeObserver:    dynObserver,
		ConnectionResolver: staticConnectionResolver("http://localhost:8080", "key"),
		InventoryClient:    inventory,
		IdentityRepo:       repo,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	var logMu sync.Mutex
	var logs []string
	worker := identityreconcile.NewWorker(svc, func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logs = append(logs, format)
	})
	worker.SetInterval(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	go worker.Run(ctx)

	// Step 1: Let it run with initial failure
	time.Sleep(50 * time.Millisecond)

	logMu.Lock()
	initialLogsCount := len(logs)
	logMu.Unlock()

	// Should have logged once ("reconciliation waiting: ...") and not spammed repeatedly
	if initialLogsCount != 1 {
		t.Errorf("expected 1 log for repeated failure, got %d", initialLogsCount)
	}

	// Step 2: Now transition to healthy
	mu.Lock()
	statusFn = func() (model.RuntimeObservedStatus, error) {
		return validReadyStatus(), nil
	}
	mu.Unlock()

	time.Sleep(50 * time.Millisecond)

	logMu.Lock()
	healthyLogsCount := len(logs)
	var hasRecovered bool
	for _, l := range logs {
		if strings.Contains(l, "recovered") {
			hasRecovered = true
		}
	}
	logMu.Unlock()

	if !hasRecovered {
		t.Errorf("expected recovered log, logs = %v", logs)
	}

	cancel()
	time.Sleep(20 * time.Millisecond)
	_ = observer
	_ = healthyLogsCount
}

type dynamicRuntimeObserver struct {
	fn func() (model.RuntimeObservedStatus, error)
}

func (d *dynamicRuntimeObserver) Status(ctx context.Context) (model.RuntimeObservedStatus, error) {
	return d.fn()
}

func TestReconcileOnce_ContextCanceledPreserved(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	observer := &sequenceRuntimeObserver{
		statuses: []func() (model.RuntimeObservedStatus, error){
			func() (model.RuntimeObservedStatus, error) {
				return validReadyStatus(), nil
			},
		},
	}
	repo := &recordingIdentityRepo{}
	inventory := &fakeInventoryClient{
		apiKeysErr: context.Canceled,
	}

	svc, err := identityreconcile.NewService(identityreconcile.Config{
		RuntimeObserver:    observer,
		ConnectionResolver: staticConnectionResolver("http://localhost:8080", "key"),
		InventoryClient:    inventory,
		IdentityRepo:       repo,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	cancel() // cancel context before ReconcileOnce finishes
	_, err = svc.ReconcileOnce(ctx)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected errors.Is(err, context.Canceled), got %v", err)
	}
	if len(repo.applyCalls) != 0 {
		t.Errorf("expected 0 apply calls on context cancellation, got %d", len(repo.applyCalls))
	}
}

func TestWorker_ContextCanceledNoWaitingLog(t *testing.T) {
	observer := &sequenceRuntimeObserver{
		statuses: []func() (model.RuntimeObservedStatus, error){
			func() (model.RuntimeObservedStatus, error) {
				return validReadyStatus(), nil
			},
		},
	}
	repo := &recordingIdentityRepo{}
	inventory := &fakeInventoryClient{
		apiKeysErr: context.Canceled,
	}

	svc, err := identityreconcile.NewService(identityreconcile.Config{
		RuntimeObserver:    observer,
		ConnectionResolver: staticConnectionResolver("http://localhost:8080", "key"),
		InventoryClient:    inventory,
		IdentityRepo:       repo,
	})
	if err != nil {
		t.Fatalf("new service: %v", err)
	}

	var logMu sync.Mutex
	var logs []string
	worker := identityreconcile.NewWorker(svc, func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logs = append(logs, format)
	})
	worker.SetInterval(10 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // canceled immediately

	worker.Run(ctx)

	logMu.Lock()
	defer logMu.Unlock()
	for _, l := range logs {
		if strings.Contains(l, "waiting") || strings.Contains(l, "reconciliation error changed") {
			t.Errorf("worker logged error on canceled context: %s", l)
		}
	}
}
