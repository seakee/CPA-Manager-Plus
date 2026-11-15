package freshkeyapply

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

// errExternalSettled is the in-process signal that the step a call was about to
// write is already shown and authenticated by the user's CPA. It never leaves
// this package: the reconcile settles the journal from that read-back and
// returns the operation.
var errExternalSettled = errors.New("external key step already settled")

// uniqueExternalKeys preserves first-seen order while removing duplicate entries
// that an External operator's configuration file may carry.
func uniqueExternalKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func externalKeysAfterStep(keys []string, mode, step, old, next string) []string {
	after := uniqueExternalKeys(keys)
	if step == "add" {
		added := next
		if mode == "add" {
			added = old
		}
		if added != "" && !slices.Contains(after, added) {
			after = append(after, added)
		}
		return after
	}
	return slices.DeleteFunc(after, func(key string) bool { return key == old })
}
func (s *Service) externalPreflight(ctx context.Context, target, keeper, mode string, generation uint64) error {
	install, err := s.externalTarget(ctx, generation)
	if err != nil {
		return err
	}
	return s.externalPrerequisites(ctx, install, target, keeper, mode)
}

func (s *Service) externalPrerequisites(ctx context.Context, install ExternalInstall, target, keeper, mode string) error {
	w := s.externalKeyWire(install)
	if w == nil {
		return ErrExternalUnavailable
	}
	latest, err := s.repo.LatestOperation(ctx)
	if err != nil {
		return err
	}
	if latest != nil && !latest.IsTerminal() {
		return repo.ErrConflict
	}
	if err := s.externalWriteGate(ctx, install); err != nil {
		return err
	}
	return s.operationSecrets(ctx, mode, target, keeper, false, func(old, next string) error {
		snapshot, err := w.ExternalKeySnapshot(ctx, install.ManagementKey)
		if err != nil {
			return err
		}
		if mode == "add" {
			if len(externalKeysAfterStep(snapshot.Keys, "add", "add", old, "")) == 0 {
				return cpa.ErrSourceConflict
			}
			return nil
		}
		if (next != "" && old == next) || !slices.Contains(snapshot.Keys, old) {
			return cpa.ErrSourceConflict
		}
		if mode == "revoke" {
			after := externalKeysAfterStep(snapshot.Keys, "revoke", "revoke", old, next)
			if len(after) == 0 {
				return ErrLastKey
			}
			if next != "" && !slices.Contains(after, next) {
				return cpa.ErrSourceConflict
			}
			return nil
		}
		afterAdd := externalKeysAfterStep(snapshot.Keys, "replace", "add", old, next)
		afterRevoke := externalKeysAfterStep(afterAdd, "replace", "revoke", old, next)
		if len(afterRevoke) == 0 || !slices.Contains(afterRevoke, next) {
			return ErrLastKey
		}
		return nil
	})
}

// externalApplyAdd executes or reconciles a durable "add" operation for one
// canonical staged key on an External CPA while preserving its saved canonical
// ID and secret across credential rebinds:
//   - If the newest "add" operation for target is pending, unknown (attempted),
//     or committed, idempotent retries (even with an older request ID) resolve
//     that newest operation without minting a new ID and never resend an
//     attempted PUT.
//   - Only when the newest "add" for target is terminal "aborted" (proven
//     never-attempted before a credential rebind) and requestID is already
//     bound to an aborted row does Prepare allocate one fresh journal ID inside
//     s.mu right before BeginOperation.
func (s *Service) externalApplyAdd(ctx context.Context, id, target string, generation uint64) (OperationResult, error) {
	result := OperationResult{RuntimeState: "effect_unknown"}
	s = s.active()
	if s == nil || s.repo == nil {
		return result, ErrUnavailable
	}
	if !freshkeys.ValidRequestID(id) || !freshkeys.ValidRequestID(target) {
		return result, freshkeys.ErrRequest
	}
	if !s.KeyOperationsEnabled() {
		return result, ErrUnavailable
	}
	prepared, opID, err := s.externalPrepareAdd(ctx, id, target, generation)
	if err != nil {
		return prepared, err
	}
	if prepared.Operation != nil && prepared.Operation.Outcome == repo.OutcomeCommitted {
		key, err := s.keys.Detail(ctx, target)
		if err != nil {
			return prepared, err
		}
		return s.observeCommittedAdd(ctx, prepared, key, generation)
	}
	if prepared.NextStep == "add" {
		return s.ContinueOperation(ctx, opID, generation)
	}
	return prepared, cpa.ErrApplyUnknown
}

func (s *Service) externalPrepareAdd(ctx context.Context, requestID, target string, generation uint64) (OperationResult, string, error) {
	result := OperationResult{RuntimeState: "effect_unknown"}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.KeyOperationsEnabled() {
		return result, "", ErrUnavailable
	}
	key, err := s.keys.Detail(ctx, target)
	if err != nil {
		return result, "", err
	}
	if key.Lifecycle == identity.LifecycleSuperseded {
		return result, "", repo.ErrConflict
	}

	byReq, err := s.repo.Operation(ctx, requestID)
	if err != nil {
		return result, "", err
	}
	if byReq != nil && (byReq.TargetID != target || byReq.KeeperID != "" || byReq.Mode != "add") {
		return result, "", repo.ErrConflict
	}

	latestForTarget, err := s.repo.LatestAddOperation(ctx, target, key.RequestID)
	if err != nil {
		return result, "", err
	}
	if latestForTarget == nil && byReq != nil {
		latestForTarget = byReq
	}
	if latestForTarget != nil && latestForTarget.Outcome != repo.OutcomeAborted {
		res, checkErr := s.externalCheckOperation(ctx, latestForTarget, generation)
		return res, latestForTarget.ID, checkErr
	}
	if byReq != nil && byReq.Outcome != repo.OutcomeAborted {
		res, checkErr := s.externalCheckOperation(ctx, byReq, generation)
		return res, byReq.ID, checkErr
	}

	latest, err := s.reconcileUnsettledLatestLocked(ctx, generation)
	if err != nil {
		return result, "", err
	}
	if latest != nil && !latest.IsTerminal() {
		return OperationResult{Operation: latest, RuntimeState: "effect_unknown"}, "", repo.ErrConflict
	}

	opID := requestID
	if byReq != nil {
		nextID, genErr := identity.NewAPIKeyID()
		if genErr != nil {
			return result, "", ErrExternalUnavailable
		}
		opID = string(nextID)
	}
	prepared, prepErr := s.externalPrepare(ctx, opID, target, "", "add", generation)
	return prepared, opID, prepErr
}

func (s *Service) externalPrepare(ctx context.Context, id, target, keeper, mode string, generation uint64) (OperationResult, error) {
	result := OperationResult{RuntimeState: "effect_unknown"}
	if !validOperationRequestShape(target, keeper, mode) {
		return result, freshkeys.ErrRequest
	}
	install, err := s.externalTarget(ctx, generation)
	if err != nil {
		return result, err
	}
	if err = s.externalPrerequisites(ctx, install, target, keeper, mode); err != nil {
		return result, err
	}
	var steps []repo.KeyStep
	switch mode {
	case "add":
		steps = []repo.KeyStep{{Name: "add", State: "unattempted"}}
	case "revoke":
		steps = []repo.KeyStep{{Name: "revoke", State: "unattempted"}}
	case "replace":
		steps = []repo.KeyStep{{Name: "add", State: "unattempted"}, {Name: "revoke", State: "unattempted"}}
	}
	o := repo.Operation{
		ID: id, TargetID: target, KeeperID: keeper, Mode: mode,
		RunID: install.Identity, Generation: install.Generation(),
		Revision: 1, Outcome: repo.OutcomePending, Steps: steps, CreatedAtMS: time.Now().UnixMilli(),
	}
	if err = s.repo.BeginOperation(ctx, o); err != nil {
		return result, err
	}
	return s.externalCheckOperation(ctx, &o, generation)
}

func (s *Service) externalCheckOperation(ctx context.Context, o *repo.Operation, generation uint64) (OperationResult, error) {
	r := OperationResult{Operation: o, RuntimeState: "effect_unknown"}
	if o.IsTerminal() {
		install, err := s.externalAuthority(ctx)
		if err != nil {
			return r, err
		}
		if (generation != 0 && generation != install.Checkpoint) || o.RunID != install.Identity || o.Generation != install.Generation() {
			return r, ErrGeneration
		}
		if o.Outcome == repo.OutcomeAborted {
			r.RuntimeState = "not_applied"
			return r, nil
		}
		latest, err := s.repo.LatestOperation(ctx)
		if err != nil {
			return r, err
		}
		if latest == nil {
			return r, repo.ErrConflict
		}
		if latest.ID != o.ID {
			return r, nil
		}
	}
	install, err := s.externalTarget(ctx, generation)
	if err != nil {
		return r, err
	}
	if o.RunID != install.Identity || o.Generation != install.Generation() {
		return r, ErrGeneration
	}
	w := s.externalKeyWire(install)
	if w == nil {
		return r, ErrExternalUnavailable
	}
	for i, step := range o.Steps {
		if step.State == "observed" && i < len(o.Steps)-1 {
			continue
		}
		switch {
		case step.State == "attempted" || step.State == "observed":
			err = s.operationSecrets(ctx, o.Mode, o.TargetID, o.KeeperID, o.Outcome == "committed", func(old, next string) error {
				return s.awaitExternalKeyStep(ctx, w, install, o.Mode, step.Name, old, next)
			})
			if err != nil {
				return r, err
			}
		default:
			held := false
			err = s.operationSecrets(ctx, o.Mode, o.TargetID, o.KeeperID, false, func(old, next string) error {
				var readErr error
				held, readErr = s.externalStepHeld(ctx, w, install, o.Mode, step.Name, old, next)
				return readErr
			})
			if err != nil {
				return r, err
			}
			if !held {
				r.NextStep = step.Name
				return r, nil
			}
		}
		if o.Outcome != repo.OutcomeCommitted {
			o.Steps[i].State = "observed"
			o.Outcome = repo.OutcomePending
			if step.Name == "revoke" || o.Mode == "add" {
				o.Outcome = repo.OutcomeCommitted
				o.CompletedAtMS = max(time.Now().UnixMilli(), o.CreatedAtMS)
			}
			if err = s.repo.SaveOperation(ctx, *o); err != nil {
				return r, err
			}
			o.Revision++
		}
	}
	r.RuntimeState = "effective_observed"
	r.ExpiresAtMS = time.Now().Add(5 * time.Second).UnixMilli()
	return r, nil
}

// externalStepHeld requires config membership, live client authentication on
// /v1/models, and a post-sample checkpoint/source/config observation fence.
func (s *Service) externalStepHeld(ctx context.Context, w externalWire, install ExternalInstall, mode, step, old, next string) (bool, error) {
	snapshot, err := w.ExternalKeySnapshot(ctx, install.ManagementKey)
	if err != nil {
		return false, err
	}
	if !externalStepSettled(snapshot.Keys, mode, step, old, next) {
		return false, nil
	}
	sampler, ok := w.(externalAuthWire)
	if !ok {
		return false, cpa.ErrApplyUnknown
	}
	var accepted, rejected []string
	if step == "add" {
		added := next
		if mode == "add" {
			added = old
		}
		accepted = []string{added}
	} else {
		rejected = []string{old}
		if next != "" {
			accepted = []string{next}
		} else {
			unique := uniqueExternalKeys(snapshot.Keys)
			if len(unique) == 0 {
				return false, cpa.ErrApplyUnknown
			}
			accepted = []string{unique[0]}
		}
	}
	authOK, authErr := sampler.ExternalSampleKeySet(ctx, accepted, rejected)
	if err := s.externalObservationHeld(ctx, install, w, snapshot); err != nil {
		return false, err
	}
	if authErr != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, cpa.ErrApplyUnknown
	}
	if !authOK {
		return false, cpa.ErrApplyUnknown
	}
	s.authCache.recordProven(authProofVersion{
		profile:      ProfileExternal,
		target:       install.Identity,
		runID:        install.Generation(),
		sourceSHA256: snapshot.SourceSHA256,
		keysSHA256:   orderedKeysDigest(snapshot.Keys),
	}, snapshot.Keys, accepted)
	return true, nil
}

func externalStepSettled(keys []string, mode, step, old, next string) bool {
	if step == "add" {
		added := next
		if mode == "add" {
			added = old
		}
		return added != "" && slices.Contains(keys, added)
	}
	if len(uniqueExternalKeys(keys)) == 0 || slices.Contains(keys, old) {
		return false
	}
	return next == "" || slices.Contains(keys, next)
}

func (s *Service) awaitExternalKeyStep(ctx context.Context, w externalWire, install ExternalInstall, mode, step, old, next string) error {
	for n := range 3 {
		held, err := s.externalStepHeld(ctx, w, install, mode, step, old, next)
		if err != nil {
			return err
		}
		if !held {
			return cpa.ErrApplyUnknown
		}
		if n < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	return nil
}

func (s *Service) externalContinue(ctx context.Context, o *repo.Operation, index int, generation uint64) (OperationResult, error) {
	r := OperationResult{Operation: o, RuntimeState: "effect_unknown"}
	if o.Outcome == repo.OutcomeCommitted {
		return r, nil
	}
	if o.Outcome == repo.OutcomeAborted {
		return r, repo.ErrConflict
	}
	install, err := s.externalTarget(ctx, generation)
	if err != nil {
		return r, err
	}
	if o.RunID != install.Identity || o.Generation != install.Generation() {
		return r, ErrGeneration
	}
	step := o.Steps[index].Name
	grant, err := s.keyPermit(ctx, o.ID, step)
	if err != nil {
		if errors.Is(err, cpa.ErrManagementRejected) || errors.Is(err, cpa.ErrExternalTargetMoved) || errors.Is(err, cpa.ErrExternalRejectionUnavailable) {
			return r, err
		}
		return r, ErrExternalUnavailable
	}
	now := time.Now().UnixMilli()
	if grant.OperationID != o.ID || grant.Step != step || grant.RunID != o.RunID || grant.Generation != o.Generation ||
		grant.IssuedAtMS < s.startedAtMS || grant.IssuedAtMS < o.CreatedAtMS || grant.IssuedAtMS > now ||
		grant.ExpiresAtMS <= now || grant.ExpiresAtMS-grant.IssuedAtMS > 60000 || !identity.IsValidSHA256Hex(grant.SourceSHA256) {
		return r, ErrExternalUnavailable
	}
	w := s.externalKeyWire(install)
	if w == nil {
		return r, ErrExternalUnavailable
	}
	err = s.operationSecrets(ctx, o.Mode, o.TargetID, o.KeeperID, false, func(old, next string) error {
		snapshot, err := w.ExternalKeySnapshot(ctx, install.ManagementKey)
		if err != nil {
			return err
		}
		if step == "add" {
			added := next
			if o.Mode == "add" {
				added = old
			}
			if slices.Contains(snapshot.Keys, added) {
				held, heldErr := s.externalStepHeld(ctx, w, install, o.Mode, step, old, next)
				if heldErr != nil {
					return heldErr
				}
				if held {
					return errExternalSettled
				}
				return cpa.ErrApplyUnknown
			}
			if o.Mode == "replace" && (old == next || !slices.Contains(snapshot.Keys, old)) {
				return cpa.ErrSourceConflict
			}
		} else {
			if !slices.Contains(snapshot.Keys, old) {
				if externalStepSettled(snapshot.Keys, o.Mode, step, old, next) {
					held, heldErr := s.externalStepHeld(ctx, w, install, o.Mode, step, old, next)
					if heldErr != nil {
						return heldErr
					}
					if held {
						return errExternalSettled
					}
					return cpa.ErrApplyUnknown
				}
				if len(uniqueExternalKeys(snapshot.Keys)) == 0 {
					return ErrLastKey
				}
				return cpa.ErrSourceConflict
			}
			if next != "" && (old == next || !slices.Contains(snapshot.Keys, next)) {
				return cpa.ErrSourceConflict
			}
		}
		after := externalKeysAfterStep(snapshot.Keys, o.Mode, step, old, next)
		if len(after) == 0 {
			return ErrLastKey
		}
		if step == "revoke" && next != "" && !slices.Contains(after, next) {
			return cpa.ErrSourceConflict
		}
		if snapshot.SourceSHA256 != grant.SourceSHA256 {
			return cpa.ErrSourceConflict
		}
		if err := s.externalKeyGrantHeld(ctx, grant, o.ID, step); err != nil {
			return err
		}
		if time.Now().UnixMilli() >= grant.ExpiresAtMS {
			return ErrExternalUnavailable
		}
		if err := s.externalWriteGate(ctx, install); err != nil {
			return err
		}
		o.Steps[index].State = "attempted"
		o.Outcome = repo.OutcomeUnknown
		if err = s.repo.SaveOperation(ctx, *o); err != nil {
			return err
		}
		o.Revision++

		writeErr := w.ExternalWriteKeySet(ctx, install.ManagementKey, after)
		if errors.Is(writeErr, cpa.ErrManagementRejected) ||
			errors.Is(writeErr, cpa.ErrExternalRejectionUnavailable) ||
			errors.Is(writeErr, cpa.ErrExternalTargetMoved) {
			o.Steps[index].Returned = false
			o.Steps[index].State = "unattempted"
			o.Outcome = repo.OutcomePending
		} else {
			o.Steps[index].Returned = true
		}
		saveCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err = s.repo.SaveOperation(saveCtx, *o); err != nil {
			return err
		}
		o.Revision++
		return writeErr
	})
	if errors.Is(err, errExternalSettled) {
		return s.externalCheckOperation(ctx, o, generation)
	}
	if err != nil {
		return OperationResult{Operation: o, RuntimeState: "effect_unknown"}, err
	}
	return s.externalCheckOperation(ctx, o, generation)
}

func (s *Service) externalKeyGrantHeld(ctx context.Context, grant KeyPermit, operationID, step string) error {
	current, err := s.keyPermit(ctx, operationID, step)
	if err != nil {
		if errors.Is(err, cpa.ErrManagementRejected) || errors.Is(err, cpa.ErrExternalTargetMoved) || errors.Is(err, cpa.ErrExternalRejectionUnavailable) {
			return err
		}
		return ErrExternalUnavailable
	}
	if current.OperationID != grant.OperationID || current.Step != grant.Step ||
		current.RunID != grant.RunID || current.Generation != grant.Generation ||
		current.SourceSHA256 != grant.SourceSHA256 {
		return ErrExternalUnavailable
	}
	return nil
}
