// Package freshrouting owns only the first route activation of one admitted
// Fresh installation. Its checkpoint lives in the Fresh record, not a ledger.
package freshrouting

import (
	"context"
	"errors"
	"sync"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
)

var ErrBlocked = errors.New("initial routing transport unavailable")
var ErrConflict = errors.New("foreign initial routing owner or target")
var ErrUnverified = errors.New("initial routing public identity verification failed")

// State is an internal, non-secret projection of the existing Fresh routing
// checkpoint. Ingress keeps it in memory only and never reads Manager SQLite.
type State struct {
	InstallationID string                `json:"installationId"`
	Generation     string                `json:"generation"`
	OperationID    bootstrap.OperationID `json:"operationId"`
	Revision       uint64                `json:"revision"`
	Target         string                `json:"target"`
	Committed      bool                  `json:"committed"`
}

func (s State) Matches(o bootstrap.FreshOperation) bool { return s == StateFor(o) }
func StateFor(o bootstrap.FreshOperation) State {
	return State{o.Identity.InstallationID, o.Identity.Generation, o.Identity.OperationID, o.Routing.Revision, o.Routing.Target, o.Routing.Committed}
}

type Driver interface {
	Inspect(context.Context) (State, error)
	Apply(context.Context, State) error
	Verify(context.Context, State) error
}
type Service struct {
	store  freshstore.Store
	driver Driver
	mu     sync.Mutex
}

func New(store freshstore.Store, driver Driver) *Service {
	return &Service{store: store, driver: driver}
}

func (s *Service) Prepare(ctx context.Context, id bootstrap.FreshIdentity, target string) (bootstrap.FreshOperation, error) {
	return s.prepare(ctx, id, target, nil)
}
func (s *Service) PrepareAuthorized(ctx context.Context, id bootstrap.FreshIdentity, target string, validate func(freshstore.Record) error) (bootstrap.FreshOperation, error) {
	if validate == nil {
		return bootstrap.FreshOperation{}, freshstore.ErrConflict
	}
	return s.prepare(ctx, id, target, validate)
}
func (s *Service) prepare(ctx context.Context, id bootstrap.FreshIdentity, target string, validate func(freshstore.Record) error) (bootstrap.FreshOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if (routing.RoutingSnapshot{State: routing.SnapshotDesiredOnly, DesiredControlBasePath: target}).Validate() != nil {
		return bootstrap.FreshOperation{}, ErrConflict
	}
	r, err := s.store.Load(ctx)
	if err != nil {
		return bootstrap.FreshOperation{}, err
	}
	if r.Operation.Identity != id || !r.Operation.AdminInitialized {
		return bootstrap.FreshOperation{}, freshstore.ErrConflict
	}
	if validate != nil {
		if err := validate(r); err != nil {
			return r.Operation, err
		}
	}
	cp := r.Operation.Routing
	if cp.OperationID != "" {
		if cp.OperationID != id.OperationID || cp.Target != target {
			return bootstrap.FreshOperation{}, ErrConflict
		}
		return r.Operation, nil
	}
	if s.driver == nil {
		return r.Operation, ErrBlocked
	}
	// Collision inspection precedes the first durable intent. Once an intent
	// exists, its immutable owner/target is reconciled again by Activate.
	actual, err := s.driver.Inspect(ctx)
	if err != nil {
		return r.Operation, err
	}
	wanted := State{InstallationID: id.InstallationID, Generation: id.Generation,
		OperationID: id.OperationID, Revision: 1, Target: target}
	if actual != (State{}) && actual != wanted {
		return r.Operation, ErrConflict
	}
	r, err = s.store.Update(ctx, id, r.Operation.Revision, func(r *freshstore.Record) error {
		if validate != nil {
			if err := validate(*r); err != nil {
				return err
			}
		}
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: id.OperationID, Revision: 1, Target: target, Stage: bootstrap.PrefixIntentPersisted}
		return nil
	})
	return r.Operation, err
}

// Activate always inspects the original checkpoint and actual route before an
// apply/retry. A failed or lost result never creates another top-level owner.
func (s *Service) Activate(ctx context.Context, id bootstrap.FreshIdentity) (bootstrap.FreshOperation, error) {
	return s.activate(ctx, id, nil)
}

// ActivateAuthorized additionally fences the final browser mutation with the
// current Fresh session inside the same durable routing transaction.
func (s *Service) ActivateAuthorized(ctx context.Context, id bootstrap.FreshIdentity, validate func(freshstore.Record) error) (bootstrap.FreshOperation, error) {
	if validate == nil {
		return bootstrap.FreshOperation{}, freshstore.ErrConflict
	}
	return s.activate(ctx, id, validate)
}
func (s *Service) activate(ctx context.Context, id bootstrap.FreshIdentity, validate func(freshstore.Record) error) (bootstrap.FreshOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.store.Load(ctx)
	if err != nil {
		return bootstrap.FreshOperation{}, err
	}
	if r.Operation.Identity != id || r.Operation.Routing.OperationID != id.OperationID || r.Operation.Routing.Target == "" || !r.Operation.AdminInitialized {
		return bootstrap.FreshOperation{}, ErrConflict
	}
	if validate != nil {
		if err := validate(r); err != nil {
			return r.Operation, err
		}
	}
	if s.driver == nil {
		return r.Operation, ErrBlocked
	}
	wanted := StateFor(r.Operation)
	actual, err := s.driver.Inspect(ctx)
	if err != nil {
		return r.Operation, err
	}
	if actual.OperationID != "" && (actual.OperationID != wanted.OperationID || actual.InstallationID != wanted.InstallationID || actual.Generation != wanted.Generation || actual.Revision != wanted.Revision || actual.Target != wanted.Target || actual.Committed && !wanted.Committed) {
		return r.Operation, ErrConflict
	}
	if actual != wanted {
		if err = s.driver.Apply(ctx, wanted); err != nil {
			return r.Operation, err
		}
	}
	if !r.Operation.Routing.Committed && r.Operation.Routing.Stage == bootstrap.PrefixIntentPersisted {
		r, err = s.store.Update(ctx, id, r.Operation.Revision, func(r *freshstore.Record) error {
			if validate != nil {
				if err := validate(*r); err != nil {
					return err
				}
			}
			r.Operation.Routing.Stage = bootstrap.PrefixRouteActivated
			return nil
		})
		if err != nil {
			return r.Operation, err
		}
	}
	if err = s.driver.Verify(ctx, wanted); err != nil {
		return r.Operation, err
	}
	if r.Operation.Routing.Committed {
		return r.Operation, nil
	}
	r, err = s.store.Update(ctx, id, r.Operation.Revision, func(r *freshstore.Record) error {
		if validate != nil {
			if err := validate(*r); err != nil {
				return err
			}
		}
		r.Operation.Routing.Stage = bootstrap.PrefixHealthVerified
		return nil
	})
	if err != nil {
		return r.Operation, err
	}
	r, err = s.store.Update(ctx, id, r.Operation.Revision, func(r *freshstore.Record) error {
		if validate != nil {
			if err := validate(*r); err != nil {
				return err
			}
		}
		if r.Operation.Routing.OperationID != id.OperationID || r.Operation.Routing.Target != wanted.Target {
			return ErrConflict
		}
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		return nil
	})
	if err == nil { // DB commit is the auth cutover; losing this ack cannot reopen fixed routes.
		_ = s.driver.Apply(ctx, StateFor(r.Operation))
	}
	return r.Operation, err
}

func (s *Service) Snapshot(ctx context.Context) routing.RoutingSnapshot {
	r, err := s.store.Load(ctx)
	if err != nil || r.Operation.Validate() != nil || !r.Operation.Routing.Committed {
		return routing.RoutingSnapshot{State: routing.SnapshotUnavailable}
	}
	return routing.RoutingSnapshot{State: routing.SnapshotVerifiedEffective, DesiredControlBasePath: r.Operation.Routing.Target, VerifiedEffectiveControlBasePath: r.Operation.Routing.Target, Revision: r.Operation.Routing.Revision}
}

// Restore republishes only the original durable route owner after Ingress
// restart. It does not create an operation, advance a checkpoint, or reopen
// fixed authentication. Pending operations still need Activate verification.
func (s *Service) Restore(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.store.Load(ctx)
	if err != nil {
		return err
	}
	if r.Operation.Routing.Target == "" {
		return nil
	}
	if r.Operation.Validate() != nil || s.driver == nil {
		return ErrBlocked
	}
	wanted := StateFor(r.Operation)
	actual, err := s.driver.Inspect(ctx)
	if err != nil {
		return err
	}
	if actual.OperationID != "" && (actual.OperationID != wanted.OperationID || actual.InstallationID != wanted.InstallationID || actual.Generation != wanted.Generation || actual.Revision != wanted.Revision || actual.Target != wanted.Target || actual.Committed && !wanted.Committed) {
		return ErrConflict
	}
	if actual == wanted {
		return nil
	}
	return s.driver.Apply(ctx, wanted)
}
