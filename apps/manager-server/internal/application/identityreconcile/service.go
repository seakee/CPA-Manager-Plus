package identityreconcile

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityinventory"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
)

// RuntimeObserver reports observed runtime status.
type RuntimeObserver interface {
	Status(ctx context.Context) (model.RuntimeObservedStatus, error)
}

// ConnectionResolver resolves CPA management credentials once per reconciliation run.
type ConnectionResolver func(ctx context.Context) (baseURL string, managementKey string, err error)

// TimeSource returns the current time in milliseconds.
type TimeSource func() int64

// Service coordinates runtime-fenced passive identity reconciliation.
type Service struct {
	runtimeObserver    RuntimeObserver
	connectionResolver ConnectionResolver
	inventoryClient    identityinventory.Client
	identityRepo       ports.Repository
	timeSource         TimeSource
	processInstanceID  string
	beforeCapture      func(context.Context) error
}

// Config holds dependencies for Service.
type Config struct {
	RuntimeObserver    RuntimeObserver
	ConnectionResolver ConnectionResolver
	InventoryClient    identityinventory.Client
	IdentityRepo       ports.Repository
	TimeSource         TimeSource
	ProcessInstanceID  string
	BeforeCapture      func(context.Context) error
}

// NewService creates a new identity reconciliation service.
func NewService(cfg Config) (*Service, error) {
	if cfg.RuntimeObserver == nil {
		return nil, fmt.Errorf("RuntimeObserver is required")
	}
	if cfg.ConnectionResolver == nil {
		return nil, fmt.Errorf("ConnectionResolver is required")
	}
	if cfg.InventoryClient == nil {
		return nil, fmt.Errorf("InventoryClient is required")
	}
	if cfg.IdentityRepo == nil {
		return nil, fmt.Errorf("IdentityRepo is required")
	}
	ts := cfg.TimeSource
	if ts == nil {
		ts = func() int64 {
			return time.Now().UnixMilli()
		}
	}
	return &Service{
		runtimeObserver:    cfg.RuntimeObserver,
		connectionResolver: cfg.ConnectionResolver,
		inventoryClient:    cfg.InventoryClient,
		identityRepo:       cfg.IdentityRepo,
		timeSource:         ts,
		processInstanceID:  cfg.ProcessInstanceID,
		beforeCapture:      cfg.BeforeCapture,
	}, nil
}

// ReconcileOnce performs a single runtime-fenced passive reconciliation run.
// Strictly executes:
//  1. pre Runtime Status check (must be ready, non-empty identity, non-zero generation)
//  2. resolve ONE immutable CPA management connection for this run
//  3. fetch supported Credential inventory and top-level API-key inventory
//  4. post Runtime Status check (must be ready, same identity, same generation)
//  5. apply atomic snapshot to identity store.
//
// Any failure before step 5 results in zero Canonical writes.
func (s *Service) ReconcileOnce(ctx context.Context) (ports.ReconcileSnapshotResult, error) {
	// Step 1: Pre-capture Runtime Status fence
	preStatus, err := s.runtimeObserver.Status(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ports.ReconcileSnapshotResult{}, ctx.Err()
		}
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: %w", ErrRuntimeUnavailable, err)
	}
	if preStatus.State != model.RuntimeStateReady {
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: pre-status is %q", ErrRuntimeNotReady, preStatus.State)
	}
	rtIdentity := strings.TrimSpace(string(preStatus.Identity))
	if rtIdentity == "" {
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: empty runtime identity", ErrRuntimeObservationIncomplete)
	}
	if preStatus.Generation == 0 {
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: runtime generation is zero", ErrRuntimeObservationIncomplete)
	}

	// Step 2: Resolve ONE immutable CPA Management connection for this run
	baseURL, mgmtKey, err := s.connectionResolver(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ports.ReconcileSnapshotResult{}, ctx.Err()
		}
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: %w", ErrConnectionResolutionFailed, err)
	}
	baseURL = strings.TrimSpace(baseURL)
	mgmtKey = strings.TrimSpace(mgmtKey)
	if baseURL == "" || mgmtKey == "" {
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: missing base URL or management key", ErrConnectionResolutionFailed)
	}

	// Capture start is recorded before network reads so a snapshot captured
	// before an intent/forward-completion commit cannot resolve that intent.
	if s.beforeCapture != nil {
		// Marker persistence may still be unavailable. The pending intent then
		// suppresses its sources while unrelated sources continue reconciling.
		_ = s.beforeCapture(ctx)
	}
	captureStartedAtMS := s.timeSource()
	if captureStartedAtMS <= 0 {
		captureStartedAtMS = time.Now().UnixMilli()
	}

	// Step 3: Fetch both inventories using the resolved immutable connection
	apiKeysObs, err := s.inventoryClient.FetchAPIKeys(ctx, baseURL, mgmtKey)
	if err != nil {
		if ctx.Err() != nil {
			return ports.ReconcileSnapshotResult{}, ctx.Err()
		}
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: %w", ErrAPIKeyInventoryFailed, err)
	}

	credsObs, err := s.inventoryClient.FetchCredentials(ctx, baseURL, mgmtKey)
	if err != nil {
		if ctx.Err() != nil {
			return ports.ReconcileSnapshotResult{}, ctx.Err()
		}
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: %w", ErrCredentialInventoryFailed, err)
	}

	// Step 4: Post-capture Runtime Status fence
	postStatus, err := s.runtimeObserver.Status(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return ports.ReconcileSnapshotResult{}, ctx.Err()
		}
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: observe post-status: %w", ErrRuntimeFenceChanged, err)
	}
	if postStatus.State != model.RuntimeStateReady {
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: post-status is %q", ErrRuntimeFenceChanged, postStatus.State)
	}
	if postStatus.Identity != preStatus.Identity {
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: runtime identity changed (%q -> %q)", ErrRuntimeFenceChanged, preStatus.Identity, postStatus.Identity)
	}
	if postStatus.Generation != preStatus.Generation {
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: runtime generation changed (%d -> %d)", ErrRuntimeFenceChanged, preStatus.Generation, postStatus.Generation)
	}

	// Step 5: ONE atomic snapshot apply
	nowMS := s.timeSource()
	if nowMS <= 0 {
		nowMS = time.Now().UnixMilli()
	}

	apiKeyItems := make([]ports.APIKeySnapshotItem, len(apiKeysObs))
	for i, k := range apiKeysObs {
		apiKeyItems[i] = ports.APIKeySnapshotItem{
			APIKeyHash: k.KeyHash,
		}
	}

	credItems := make([]ports.CredentialSnapshotItem, len(credsObs))
	for i, c := range credsObs {
		credItems[i] = ports.CredentialSnapshotItem{
			SourceAuthID:      c.SourceAuthID,
			AuthIndex:         c.AuthIndex,
			Provider:          c.Provider,
			PhysicalName:      c.PhysicalName,
			AccountSnapshot:   c.AccountSnapshot,
			AccountIDSnapshot: c.AccountIDSnapshot,
			Disabled:          c.Disabled,
		}
	}

	snapshotParams := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtIdentity,
		ObservedRuntimeGeneration: uint64(preStatus.Generation),
		CaptureStartedAtMS:        captureStartedAtMS,
		ProcessInstanceID:         s.processInstanceID,
		APIKeys:                   apiKeyItems,
		Credentials:               credItems,
		NowMS:                     nowMS,
	}

	result, err := s.identityRepo.ApplyPassiveSnapshot(ctx, snapshotParams)
	if err != nil {
		if ctx.Err() != nil {
			return ports.ReconcileSnapshotResult{}, ctx.Err()
		}
		return ports.ReconcileSnapshotResult{}, fmt.Errorf("%w: %w", ErrReconciliationConflict, err)
	}

	return result, nil
}
