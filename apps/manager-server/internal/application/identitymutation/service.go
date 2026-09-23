package identitymutation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityinventory"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
)

var (
	ErrRuntimeFence           = errors.New("API-key mutation runtime fence unavailable")
	ErrPreflight              = errors.New("API-key mutation preflight failed")
	ErrMutationOutcomeUnknown = errors.New("api_key_mutation_outcome_unknown")
)

type RuntimeObserver interface {
	Status(ctx context.Context) (model.RuntimeObservedStatus, error)
}

type Config struct {
	RuntimeObserver   RuntimeObserver
	InventoryClient   identityinventory.Client
	Repository        ports.MutationRepository
	ProcessInstanceID string
	TimeSource        func() int64
}

type Service struct {
	observer          RuntimeObserver
	inventory         identityinventory.Client
	repo              ports.MutationRepository
	processInstanceID string
	now               func() int64
	completionMu      sync.Mutex
	forwardCompleted  map[string]int64
}

// NewMonotonicMillis gives the proxy and passive worker one ordered clock.
// It preserves capture/forward ordering even when the wall clock moves back.
func NewMonotonicMillis(wall func() int64) func() int64 {
	if wall == nil {
		wall = func() int64 { return time.Now().UnixMilli() }
	}
	var mu sync.Mutex
	var last int64
	return func() int64 {
		mu.Lock()
		defer mu.Unlock()
		current := wall()
		if current <= last {
			current = last + 1
		}
		last = current
		return current
	}
}

func NewService(cfg Config) (*Service, error) {
	if cfg.RuntimeObserver == nil || cfg.InventoryClient == nil || cfg.Repository == nil ||
		cfg.ProcessInstanceID == "" {
		return nil, errors.New("incomplete API-key mutation service configuration")
	}
	now := cfg.TimeSource
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &Service{observer: cfg.RuntimeObserver, inventory: cfg.InventoryClient,
		repo: cfg.Repository, processInstanceID: cfg.ProcessInstanceID, now: now,
		forwardCompleted: make(map[string]int64)}, nil
}

func (s *Service) status(ctx context.Context) (model.RuntimeObservedStatus, error) {
	status, err := s.observer.Status(ctx)
	if err != nil {
		return model.RuntimeObservedStatus{}, ErrRuntimeFence
	}
	if status.State != model.RuntimeStateReady || strings.TrimSpace(string(status.Identity)) == "" ||
		status.Generation == 0 {
		return model.RuntimeObservedStatus{}, ErrRuntimeFence
	}
	return status, nil
}

func (s *Service) fenced(ctx context.Context, observe func(context.Context) error) (model.RuntimeObservedStatus, error) {
	pre, err := s.status(ctx)
	if err != nil {
		return model.RuntimeObservedStatus{}, err
	}
	if err := observe(ctx); err != nil {
		return model.RuntimeObservedStatus{}, err
	}
	post, err := s.status(ctx)
	if err != nil || post.Identity != pre.Identity || post.Generation != pre.Generation {
		return model.RuntimeObservedStatus{}, ErrRuntimeFence
	}
	return pre, nil
}

// CheckAvailable fences all manager-mediated API-key mutation shapes against
// an existing pending intent, including transport-only PUT/index forms.
func (s *Service) CheckAvailable(ctx context.Context) error {
	status, err := s.status(ctx)
	if err != nil {
		return err
	}
	pending, err := s.repo.HasPendingAPIKeyMutation(ctx, string(status.Identity))
	if err != nil {
		return err
	}
	if pending {
		return ports.ErrPendingAPIKeyMutation
	}
	return nil
}

// Transport-only forms need only the pending-intent overlap guard. An
// unavailable Runtime status does not change their existing transport behavior
// when no pending API-key intent exists.
func (s *Service) CheckTransportAvailable(ctx context.Context) error {
	status, err := s.status(ctx)
	var pending bool
	if err != nil {
		pending, err = s.repo.HasAnyPendingAPIKeyMutation(ctx)
	} else {
		pending, err = s.repo.HasPendingAPIKeyMutation(ctx, string(status.Identity))
	}
	if err != nil {
		return err
	}
	if pending {
		return ports.ErrPendingAPIKeyMutation
	}
	return nil
}

// Prepare invokes the secret-bearing evidence closure between Runtime status
// observations. Only the resulting safe counts/hashes enter this package.
func (s *Service) Prepare(ctx context.Context, kind ports.APIKeyMutationKind,
	fetchEvidence func(context.Context) (ports.APIKeyMutationEvidence, error)) (string, error) {
	var evidence ports.APIKeyMutationEvidence
	status, err := s.fenced(ctx, func(ctx context.Context) error {
		var fetchErr error
		evidence, fetchErr = fetchEvidence(ctx)
		if fetchErr != nil {
			return fmt.Errorf("%w: strict CPA API-key evidence unavailable", ErrPreflight)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	id, err := s.repo.PrepareAPIKeyMutation(ctx, ports.PrepareAPIKeyMutationParams{
		Kind: kind, RuntimeIdentity: string(status.Identity),
		ObservedRuntimeGeneration: uint64(status.Generation),
		Evidence:                  evidence, OwnerInstance: s.processInstanceID, NowMS: s.now(),
	})
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrPreflight, err)
	}
	return id, nil
}

func (s *Service) MarkForwardComplete(ctx context.Context, intentID string) error {
	s.completionMu.Lock()
	defer s.completionMu.Unlock()
	completedAt := s.now()
	s.forwardCompleted[intentID] = completedAt
	if err := s.repo.MarkAPIKeyMutationForwardComplete(ctx, intentID, s.processInstanceID, completedAt); err != nil {
		return err
	}
	delete(s.forwardCompleted, intentID)
	return nil
}

// RetryForwardCompletions runs before passive inventory capture. A failed
// marker write remains in memory until SQLite accepts it or this process stops.
// A fresh capture then starts strictly after the persisted completion marker.
func (s *Service) RetryForwardCompletions(ctx context.Context) error {
	s.completionMu.Lock()
	defer s.completionMu.Unlock()
	for id, completedAt := range s.forwardCompleted {
		if err := s.repo.MarkAPIKeyMutationForwardComplete(ctx, id, s.processInstanceID, completedAt); err != nil {
			return err
		}
		delete(s.forwardCompleted, id)
	}
	return nil
}

// Observe resolves only from a fresh, Runtime-fenced authoritative hash set.
// Its connection is the immutable one already selected for this proxy request.
func (s *Service) Observe(ctx context.Context, intentID, baseURL, managementKey string) (ports.APIKeyMutationOutcome, error) {
	var observations []identityinventory.APIKeyObservation
	status, err := s.fenced(ctx, func(ctx context.Context) error {
		var fetchErr error
		observations, fetchErr = s.inventory.FetchAPIKeys(ctx, baseURL, managementKey)
		if fetchErr != nil {
			return errors.New("strict CPA API-key observation unavailable")
		}
		return nil
	})
	if err != nil {
		return ports.APIKeyMutationUnknown, err
	}
	hashes := make([]string, len(observations))
	for i, observation := range observations {
		hashes[i] = observation.KeyHash
	}
	return s.repo.ResolveAPIKeyMutation(ctx, ports.ResolveAPIKeyMutationParams{
		RuntimeIdentity: string(status.Identity), ObservedRuntimeGeneration: uint64(status.Generation),
		ObservedHashes: hashes, NowMS: s.now(), IntentID: intentID,
	})
}
