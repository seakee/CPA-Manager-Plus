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
	InstallationID     string                `json:"installationId"`
	Generation         string                `json:"generation"`
	OperationID        bootstrap.OperationID `json:"operationId"`
	Revision           uint64                `json:"revision"`
	Target             string                `json:"target"`
	InferenceTarget    string                `json:"inferenceTarget,omitempty"`
	SharedEntry        bool                  `json:"sharedEntry,omitempty"`
	Committed          bool                  `json:"committed"`
	InferenceCommitted bool                  `json:"inferenceCommitted,omitempty"`
}

// PublicIdentity is the minimal non-secret verification leaf exposed at
// GET <controlBasePath>/api/v2/bootstrap/identity. It intentionally omits
// InferenceTarget, SharedEntry, and InferenceCommitted so unauthenticated
// callers who know controlBasePath cannot discover the peer inference prefix.
type PublicIdentity struct {
	InstallationID string                `json:"installationId"`
	Generation     string                `json:"generation"`
	OperationID    bootstrap.OperationID `json:"operationId"`
	Revision       uint64                `json:"revision"`
	Target         string                `json:"target"`
	Committed      bool                  `json:"committed"`
}

func PublicIdentityForState(s State) PublicIdentity {
	return PublicIdentity{
		InstallationID: s.InstallationID,
		Generation:     s.Generation,
		OperationID:    s.OperationID,
		Revision:       s.Revision,
		Target:         s.Target,
		Committed:      s.Committed,
	}
}

func PublicIdentityFor(o bootstrap.FreshOperation) PublicIdentity {
	return PublicIdentityForState(StateFor(o))
}

func (s State) Matches(o bootstrap.FreshOperation) bool { return s == StateFor(o) }
func StateFor(o bootstrap.FreshOperation) State {
	return State{
		InstallationID:     o.Identity.InstallationID,
		Generation:         o.Identity.Generation,
		OperationID:        o.Identity.OperationID,
		Revision:           o.Routing.Revision,
		Target:             o.Routing.Target,
		InferenceTarget:    o.Routing.InferenceTarget,
		SharedEntry:        o.Routing.SharedEntry,
		Committed:          o.Routing.Committed,
		InferenceCommitted: o.Routing.InferenceCommitted,
	}
}

// AccessIntent describes a two-prefix (or external control-only) routing submission.
type AccessIntent struct {
	Target          string `json:"target"`
	InferenceTarget string `json:"inferenceTarget,omitempty"`
	SharedEntry     bool   `json:"sharedEntry,omitempty"`
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
	return s.prepareAccess(ctx, id, AccessIntent{Target: target}, nil)
}
func (s *Service) PrepareAuthorized(ctx context.Context, id bootstrap.FreshIdentity, target string, validate func(freshstore.Record) error) (bootstrap.FreshOperation, error) {
	if validate == nil {
		return bootstrap.FreshOperation{}, freshstore.ErrConflict
	}
	return s.prepareAccess(ctx, id, AccessIntent{Target: target}, validate)
}
func (s *Service) PrepareAccess(ctx context.Context, id bootstrap.FreshIdentity, intent AccessIntent) (bootstrap.FreshOperation, error) {
	return s.prepareAccess(ctx, id, intent, nil)
}
func (s *Service) PrepareAccessAuthorized(ctx context.Context, id bootstrap.FreshIdentity, intent AccessIntent, validate func(freshstore.Record) error) (bootstrap.FreshOperation, error) {
	if validate == nil {
		return bootstrap.FreshOperation{}, freshstore.ErrConflict
	}
	return s.prepareAccess(ctx, id, intent, validate)
}

// SaveDraftAuthorized persists operator-edited prefix inputs before routing/prepare
// locks the immutable checkpoint, allowing wizard refreshes to recover draft inputs.
func (s *Service) SaveDraftAuthorized(ctx context.Context, id bootstrap.FreshIdentity, draft AccessIntent, validate func(freshstore.Record) error) (bootstrap.FreshOperation, error) {
	if validate == nil {
		return bootstrap.FreshOperation{}, freshstore.ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if draft.Target != "" && (!routing.ValidControlBasePath(draft.Target) || routing.ReservedNamespaceCollision(draft.Target)) {
		if draft.Target != "" && !routing.ValidControlBasePath(draft.Target) {
			return bootstrap.FreshOperation{}, &bootstrap.AccessPrefixError{Code: bootstrap.CodeInvalidAccessPrefix, Detail: "draft target is not canonical"}
		}
		return bootstrap.FreshOperation{}, &bootstrap.AccessPrefixError{Code: bootstrap.CodeReservedPrefixCollision, Detail: "draft target collides with reserved route"}
	}
	if draft.InferenceTarget != "" && (!routing.ValidInferenceBasePath(draft.InferenceTarget) || routing.ReservedNamespaceCollision(draft.InferenceTarget)) {
		if !routing.ValidInferenceBasePath(draft.InferenceTarget) {
			return bootstrap.FreshOperation{}, &bootstrap.AccessPrefixError{Code: bootstrap.CodeInvalidAccessPrefix, Detail: "draft inferenceTarget is not canonical"}
		}
		return bootstrap.FreshOperation{}, &bootstrap.AccessPrefixError{Code: bootstrap.CodeReservedPrefixCollision, Detail: "draft inferenceTarget collides with reserved route"}
	}
	r, err := s.store.Load(ctx)
	if err != nil {
		return bootstrap.FreshOperation{}, err
	}
	if r.Operation.Identity != id || !r.Operation.AdminInitialized {
		return bootstrap.FreshOperation{}, freshstore.ErrConflict
	}
	if err := validate(r); err != nil {
		return r.Operation, err
	}
	if r.Operation.Routing.OperationID != "" {
		if r.Operation.Routing.Target == draft.Target &&
			r.Operation.Routing.InferenceTarget == draft.InferenceTarget &&
			r.Operation.Routing.SharedEntry == draft.SharedEntry {
			return r.Operation, nil
		}
		return bootstrap.FreshOperation{}, ErrConflict
	}
	r, err = s.store.Update(ctx, id, r.Operation.Revision, func(cur *freshstore.Record) error {
		if err := validate(*cur); err != nil {
			return err
		}
		cur.Operation.Routing.DraftControlBasePath = draft.Target
		cur.Operation.Routing.DraftInferenceBasePath = draft.InferenceTarget
		cur.Operation.Routing.DraftSharedEntry = draft.SharedEntry
		return nil
	})
	return r.Operation, err
}

func validateIntentForStrategy(strategy bootstrap.CPAStrategy, intent AccessIntent) error {
	switch strategy {
	case bootstrap.NewManaged, bootstrap.AdoptExisting:
		return routing.ValidateAccessPrefixPair(intent.Target, intent.InferenceTarget, intent.SharedEntry, false)
	case bootstrap.ConnectExternal:
		return routing.ValidateAccessPrefixPair(intent.Target, intent.InferenceTarget, intent.SharedEntry, true)
	default:
		if intent.InferenceTarget != "" || intent.SharedEntry {
			return routing.ValidateAccessPrefixPair(intent.Target, intent.InferenceTarget, intent.SharedEntry, false)
		}
		return routing.ValidateAccessPrefixPair(intent.Target, "", false, true)
	}
}

// ValidateCheckpointForStrategy enforces the access-prefix invariant between an
// owned Fresh routing checkpoint and a first-time CPA strategy selection or
// completion. Manager-owned strategies (NewManaged and AdoptExisting) require a
// valid non-empty inference prefix — and once routing is committed, require
// InferenceCommitted — whereas ConnectExternal requires control-only routing.
func ValidateCheckpointForStrategy(strategy bootstrap.CPAStrategy, cp bootstrap.FreshRoutingCheckpoint) error {
	if cp.OperationID == "" {
		return nil
	}
	if err := validateIntentForStrategy(strategy, AccessIntent{
		Target:          cp.Target,
		InferenceTarget: cp.InferenceTarget,
		SharedEntry:     cp.SharedEntry,
	}); err != nil {
		return err
	}
	switch strategy {
	case bootstrap.NewManaged, bootstrap.AdoptExisting:
		if cp.Committed && !cp.InferenceCommitted {
			return &bootstrap.AccessPrefixError{
				Code:   bootstrap.CodeInferencePrefixRequired,
				Detail: "inferenceBasePath must be committed for managed inference",
			}
		}
	case bootstrap.ConnectExternal:
		if cp.InferenceCommitted {
			return &bootstrap.AccessPrefixError{
				Code:   bootstrap.CodeExternalInferencePrefixForbidden,
				Detail: "external installations do not commit a managed inference prefix",
			}
		}
	}
	return nil
}

func (s *Service) prepareAccess(ctx context.Context, id bootstrap.FreshIdentity, intent AccessIntent, validate func(freshstore.Record) error) (bootstrap.FreshOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if intent.Target == "" || !routing.ValidControlBasePath(intent.Target) {
		return bootstrap.FreshOperation{}, &bootstrap.AccessPrefixError{Code: bootstrap.CodeInvalidAccessPrefix, Detail: "controlBasePath is missing or not canonical"}
	}
	if routing.ReservedNamespaceCollision(intent.Target) {
		return bootstrap.FreshOperation{}, &bootstrap.AccessPrefixError{Code: bootstrap.CodeReservedPrefixCollision, Detail: "controlBasePath collides with reserved route"}
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
	if err := validateIntentForStrategy(r.Operation.Strategy.Strategy, intent); err != nil {
		return bootstrap.FreshOperation{}, err
	}
	cp := r.Operation.Routing
	if cp.OperationID != "" {
		if cp.OperationID != id.OperationID || cp.Target != intent.Target || cp.InferenceTarget != intent.InferenceTarget || cp.SharedEntry != intent.SharedEntry {
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
	wanted := State{
		InstallationID:  id.InstallationID,
		Generation:      id.Generation,
		OperationID:     id.OperationID,
		Revision:        1,
		Target:          intent.Target,
		InferenceTarget: intent.InferenceTarget,
		SharedEntry:     intent.SharedEntry,
	}
	if actual != (State{}) && actual != wanted {
		return r.Operation, ErrConflict
	}
	r, err = s.store.Update(ctx, id, r.Operation.Revision, func(cur *freshstore.Record) error {
		if validate != nil {
			if err := validate(*cur); err != nil {
				return err
			}
		}
		if err := validateIntentForStrategy(cur.Operation.Strategy.Strategy, intent); err != nil {
			return err
		}
		prev := cur.Operation.Routing
		cur.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
			OperationID:                id.OperationID,
			Revision:                   1,
			Target:                     intent.Target,
			InferenceTarget:            intent.InferenceTarget,
			SharedEntry:                intent.SharedEntry,
			SuggestedControlBasePath:   prev.SuggestedControlBasePath,
			SuggestedInferenceBasePath: prev.SuggestedInferenceBasePath,
			DraftControlBasePath:       intent.Target,
			DraftInferenceBasePath:     intent.InferenceTarget,
			DraftSharedEntry:           intent.SharedEntry,
			Stage:                      bootstrap.PrefixIntentPersisted,
		}
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
	if !r.Operation.ProductComplete && !r.Operation.Routing.Committed {
		if err := validateIntentForStrategy(r.Operation.Strategy.Strategy, AccessIntent{
			Target:          r.Operation.Routing.Target,
			InferenceTarget: r.Operation.Routing.InferenceTarget,
			SharedEntry:     r.Operation.Routing.SharedEntry,
		}); err != nil {
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
	if actual.OperationID != "" && (actual.OperationID != wanted.OperationID || actual.InstallationID != wanted.InstallationID || actual.Generation != wanted.Generation || actual.Revision != wanted.Revision || actual.Target != wanted.Target || actual.InferenceTarget != wanted.InferenceTarget || actual.SharedEntry != wanted.SharedEntry || actual.Committed && !wanted.Committed || actual.InferenceCommitted && !wanted.InferenceCommitted) {
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
		if r.Operation.Routing.OperationID != id.OperationID || r.Operation.Routing.Target != wanted.Target || r.Operation.Routing.InferenceTarget != wanted.InferenceTarget || r.Operation.Routing.SharedEntry != wanted.SharedEntry {
			return ErrConflict
		}
		if !r.Operation.ProductComplete {
			if err := validateIntentForStrategy(r.Operation.Strategy.Strategy, AccessIntent{
				Target:          r.Operation.Routing.Target,
				InferenceTarget: r.Operation.Routing.InferenceTarget,
				SharedEntry:     r.Operation.Routing.SharedEntry,
			}); err != nil {
				return err
			}
		}
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		if r.Operation.Routing.InferenceTarget != "" {
			r.Operation.Routing.InferenceCommitted = true
		}
		return nil
	})
	if err == nil { // DB commit is the auth cutover; losing this ack cannot reopen fixed routes.
		_ = s.driver.Apply(ctx, StateFor(r.Operation))
	}
	return r.Operation, err
}

func (s *Service) Snapshot(ctx context.Context) routing.RoutingSnapshot {
	r, err := s.store.Load(ctx)
	if err != nil {
		return routing.RoutingSnapshot{State: routing.SnapshotUnavailable}
	}
	return SnapshotForRecord(r)
}

// SnapshotForRecord derives the read-only RoutingSnapshot from an already
// loaded freshstore.Record without rereading storage.
func SnapshotForRecord(r freshstore.Record) routing.RoutingSnapshot {
	return SnapshotForOperation(r.Operation)
}

// SnapshotForOperation derives the read-only RoutingSnapshot directly from a
// validated FreshOperation without rereading storage.
func SnapshotForOperation(op bootstrap.FreshOperation) routing.RoutingSnapshot {
	if op.Validate() != nil || !op.Routing.Committed {
		return routing.RoutingSnapshot{State: routing.SnapshotUnavailable}
	}
	snap := routing.RoutingSnapshot{
		State:                              routing.SnapshotVerifiedEffective,
		DesiredControlBasePath:             op.Routing.Target,
		VerifiedEffectiveControlBasePath:   op.Routing.Target,
		MissingInferenceCompletionRequired: op.MissingInferenceCompletionRequired(),
		Revision:                           op.Routing.Revision,
	}
	switch {
	case op.Routing.InferenceCommitted && op.Routing.InferenceTarget != "":
		snap.DesiredInferenceBasePath = op.Routing.InferenceTarget
		snap.VerifiedEffectiveInferenceBasePath = op.Routing.InferenceTarget
		snap.InferenceState = routing.SnapshotVerifiedEffective
		snap.SharedEntry = op.Routing.SharedEntry
	case op.Routing.InferenceTarget != "":
		snap.DesiredInferenceBasePath = op.Routing.InferenceTarget
		snap.InferenceState = routing.SnapshotDesiredOnly
		snap.SharedEntry = op.Routing.SharedEntry
	case op.MissingInferenceCompletionRequired():
		snap.InferenceState = routing.SnapshotUnavailable
	}
	if snap.Validate() != nil {
		return routing.RoutingSnapshot{State: routing.SnapshotUnavailable}
	}
	return snap
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
	if actual.OperationID != "" && (actual.OperationID != wanted.OperationID || actual.InstallationID != wanted.InstallationID || actual.Generation != wanted.Generation || actual.Revision > wanted.Revision || actual.Target != wanted.Target || (actual.InferenceTarget != "" && actual.InferenceTarget != wanted.InferenceTarget) || actual.Committed && !wanted.Committed || actual.InferenceCommitted && !wanted.InferenceCommitted) {
		return ErrConflict
	}
	if actual == wanted {
		return nil
	}
	return s.driver.Apply(ctx, wanted)
}
