package credentialdeletemutation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
)

var (
	ErrRuntimeFence   = errors.New("credential delete runtime fence unavailable")
	ErrPreflight      = errors.New("credential delete preflight failed")
	ErrOutcomeUnknown = errors.New("credential_delete_outcome_unknown")
)

type RuntimeObserver interface {
	Status(context.Context) (model.RuntimeObservedStatus, error)
}

type Config struct {
	RuntimeObserver   RuntimeObserver
	Repository        ports.CredentialDeleteRepository
	ProcessInstanceID string
	TimeSource        func() int64
}

type Service struct {
	observer          RuntimeObserver
	repo              ports.CredentialDeleteRepository
	processInstanceID string
	now               func() int64
	completionMu      sync.Mutex
	forwardCompleted  map[string]int64
}

func NewService(cfg Config) (*Service, error) {
	if cfg.RuntimeObserver == nil || cfg.Repository == nil ||
		cfg.ProcessInstanceID == "" {
		return nil, errors.New("incomplete credential delete service configuration")
	}
	now := cfg.TimeSource
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &Service{observer: cfg.RuntimeObserver, repo: cfg.Repository,
		processInstanceID: cfg.ProcessInstanceID, now: now, forwardCompleted: make(map[string]int64)}, nil
}

func (s *Service) status(ctx context.Context) (model.RuntimeObservedStatus, error) {
	status, err := s.observer.Status(ctx)
	if err != nil || status.State != model.RuntimeStateReady ||
		strings.TrimSpace(string(status.Identity)) == "" || status.Generation == 0 {
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

func sameSources(a, b []string) bool {
	if len(a) == 0 || len(a) != len(b) {
		return false
	}
	set := make(map[string]struct{}, len(a))
	for _, source := range a {
		if source == "" || strings.TrimSpace(source) != source {
			return false
		}
		if _, duplicate := set[source]; duplicate {
			return false
		}
		set[source] = struct{}{}
	}
	for _, source := range b {
		if _, found := set[source]; !found {
			return false
		}
		delete(set, source)
	}
	return len(set) == 0
}

// Prepare receives only verified physical membership IDs. Selector parsing and
// credential payloads remain in the proxy boundary.
func (s *Service) Prepare(ctx context.Context, physicalName string, sourceIDs []string,
	revalidate func(context.Context) (string, []string, error)) (string, error) {
	if revalidate == nil {
		return "", ErrPreflight
	}
	var currentName string
	var currentIDs []string
	status, err := s.fenced(ctx, func(ctx context.Context) error {
		var fetchErr error
		currentName, currentIDs, fetchErr = revalidate(ctx)
		if fetchErr != nil {
			return ErrPreflight
		}
		if currentName != physicalName || !sameSources(sourceIDs, currentIDs) {
			return ErrPreflight
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	id, err := s.repo.PrepareCredentialDelete(ctx, ports.PrepareCredentialDeleteParams{
		RuntimeIdentity: string(status.Identity), ObservedRuntimeGeneration: uint64(status.Generation),
		PhysicalName: physicalName, SourceAuthIDs: currentIDs, OwnerInstance: s.processInstanceID, NowMS: s.now(),
	})
	if err != nil {
		return "", errors.Join(ErrPreflight, err)
	}
	return id, nil
}

func (s *Service) CheckOverlap(ctx context.Context, names []string, all bool) error {
	return s.repo.CheckPendingCredentialDelete(ctx, names, all)
}

func (s *Service) MarkForwardComplete(ctx context.Context, intentID string) error {
	s.completionMu.Lock()
	defer s.completionMu.Unlock()
	completedAt := s.now()
	s.forwardCompleted[intentID] = completedAt
	if err := s.repo.MarkCredentialDeleteForwardComplete(ctx, intentID, s.processInstanceID, completedAt); err != nil {
		return err
	}
	delete(s.forwardCompleted, intentID)
	return nil
}

func (s *Service) RetryForwardCompletions(ctx context.Context) error {
	s.completionMu.Lock()
	defer s.completionMu.Unlock()
	for id, completedAt := range s.forwardCompleted {
		if err := s.repo.MarkCredentialDeleteForwardComplete(ctx, id, s.processInstanceID, completedAt); err != nil {
			return err
		}
		delete(s.forwardCompleted, id)
	}
	return nil
}

// Observe accepts only strict inventory SourceAuthIDs; CPA connection secrets
// stay in the proxy's fetch closure.
func (s *Service) Observe(ctx context.Context, intentID string,
	fetch func(context.Context) ([]string, error)) (ports.CredentialDeleteOutcome, error) {
	if fetch == nil {
		return ports.CredentialDeleteUnknown, errors.New("strict credential observation unavailable")
	}
	var ids []string
	status, err := s.fenced(ctx, func(ctx context.Context) error {
		var fetchErr error
		ids, fetchErr = fetch(ctx)
		if fetchErr != nil {
			return errors.New("strict credential observation unavailable")
		}
		return nil
	})
	if err != nil {
		return ports.CredentialDeleteUnknown, err
	}
	return s.repo.ResolveCredentialDelete(ctx, ports.ResolveCredentialDeleteParams{
		RuntimeIdentity: string(status.Identity), ObservedRuntimeGeneration: uint64(status.Generation),
		ObservedSourceAuthIDs: ids, IntentID: intentID, NowMS: s.now(),
	})
}
