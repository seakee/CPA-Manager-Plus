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
// write is already shown by the user's CPA. It never leaves this package: the
// reconcile settles the journal from that read-back and returns the operation.
var errExternalSettled = errors.New("external key step already settled")

// externalPrepare journals one revoke/replace intent for the user's CPA after
// proving, by read-back, that the CPA is in exactly the state the intent starts
// from. No step is written here: the journal is the durable record a lost reply is
// reconciled against.
func (s *Service) externalPrepare(ctx context.Context, id, target, keeper, mode string, generation uint64) (OperationResult, error) {
	result := OperationResult{RuntimeState: "effect_unknown"}
	if target == keeper || (mode != "revoke" && mode != "replace") {
		return result, freshkeys.ErrRequest
	}
	install, err := s.externalTarget(ctx, generation)
	if err != nil {
		return result, err
	}
	w := s.externalKeyWire(install)
	if w == nil {
		return result, ErrExternalUnavailable
	}
	// No step is written here, but the journal is: an intent this Manager could
	// never send would stay pending and block every later operation of the
	// connection, so the version is asked before the intent exists.
	if err = s.externalWriteGate(ctx, install); err != nil {
		return result, err
	}
	err = s.keyPair(ctx, target, keeper, false, func(old, next string) error {
		snapshot, err := w.ExternalKeySnapshot(ctx, install.ManagementKey)
		if err != nil {
			return err
		}
		// The user's CPA is administered by the user, so it may carry credentials
		// this Manager never staged. The step acts on exactly one of them: the
		// credential it removes must be carried.
		if old == next || !slices.Contains(snapshot.Keys, old) {
			return cpa.ErrSourceConflict
		}
		// A replace keeps a credential on the CPA; a revoke of the only one would
		// leave it accepting unauthenticated clients, which is the user's decision
		// and never a step of this Manager.
		if mode == "revoke" && len(snapshot.Keys) <= 1 {
			return ErrLastKey
		}
		// A revoke is only permitted when the CPA keeps a credential this Manager
		// staged. A keeper that was never installed, with a credential from outside
		// this Manager keeping the count above one, would otherwise delete the
		// target and leave the journal with no observable end state.
		if mode == "revoke" && !slices.Contains(snapshot.Keys, next) {
			return cpa.ErrSourceConflict
		}
		return nil
	})
	if err != nil {
		return result, err
	}
	steps := []repo.KeyStep{{Name: "revoke", State: "unattempted"}}
	if mode == "replace" {
		steps = append([]repo.KeyStep{{Name: "add", State: "unattempted"}}, steps...)
	}
	o := repo.Operation{
		ID: id, TargetID: target, KeeperID: keeper, Mode: mode,
		// The journal records the connection identity, not the checkpoint number
		// and not a configuration digest: the identity is what a later step is
		// bound to, and it survives every edit the user makes to their own CPA.
		RunID: install.Identity, Generation: install.Generation(),
		Revision: 1, Outcome: "pending", Steps: steps, CreatedAtMS: time.Now().UnixMilli(),
	}
	if err = s.repo.BeginOperation(ctx, o); err != nil {
		return result, err
	}
	return s.externalCheckOperation(ctx, &o, generation)
}

// externalCheckOperation reconciles one journaled External operation by reading
// the user's CPA. It never writes: an attempted step is resolved by what the CPA
// now carries, and an unattempted step is only named.
func (s *Service) externalCheckOperation(ctx context.Context, o *repo.Operation, generation uint64) (OperationResult, error) {
	r := OperationResult{Operation: o, RuntimeState: "effect_unknown"}
	if o.Outcome == "committed" {
		latest, err := s.repo.LatestOperation(ctx)
		if err != nil {
			return r, err
		}
		if latest == nil {
			return r, repo.ErrConflict
		}
		if latest.ID != o.ID {
			// A historical committed operation is a durable record, not a live
			// effectiveness claim: its former keeper may since have been revoked.
			// It is only reported for the connection and generation it was
			// journaled against.
			if o.RunID != latest.RunID || o.Generation != latest.Generation {
				return r, ErrGeneration
			}
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
			err = s.keyPair(ctx, o.TargetID, o.KeeperID, o.Outcome == "committed", func(old, next string) error {
				return s.awaitExternalKeyStep(ctx, w, install, step.Name, old, next)
			})
			if err != nil {
				return r, err
			}
		default:
			// A step that was never written is normally only named. The user's
			// CPA may however already show the fact that step would establish —
			// an edit between Prepare and Continue, or the same key revoked by
			// hand — and then the read-back is the terminal state: the journal
			// settles here instead of staying pending and blocking every later
			// operation.
			held := false
			err = s.keyPair(ctx, o.TargetID, o.KeeperID, false, func(old, next string) error {
				var readErr error
				held, readErr = s.externalStepHeld(ctx, w, install, step.Name, old, next)
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
		if o.Outcome != "committed" {
			o.Steps[i].State = "observed"
			o.Outcome = "pending"
			if step.Name == "revoke" {
				o.Outcome = "committed"
				o.CompletedAtMS = time.Now().UnixMilli()
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

// externalStepHeld reports one read of the user's CPA: a revoke step has ended
// when the credential it removes is gone, and an add step has ended when the
// credential it introduces is carried. That is the whole fact the step owns.
func (s *Service) externalStepHeld(ctx context.Context, w externalWire, install ExternalInstall, step, old, next string) (bool, error) {
	snapshot, err := w.ExternalKeySnapshot(ctx, install.ManagementKey)
	if err != nil {
		return false, err
	}
	return externalStepSettled(snapshot.Keys, step, old, next), nil
}

// externalStepSettled reports the post-state of one step: a revoke removes the
// target, and the add step of a replace carries the replacement. Membership is
// the fact this Manager owns: an External CPA may carry credentials it never
// staged, and the user may edit it at any time, so a whole-list comparison or a
// whole-document digest would report a conflict for a correct write.
func externalStepSettled(keys []string, step, old, next string) bool {
	if step == "add" {
		return slices.Contains(keys, next)
	}
	return !slices.Contains(keys, old)
}

// awaitExternalKeyStep requires the fact of one step to hold across spaced reads
// of the user's CPA, so a lost reply is resolved by what the CPA shows.
func (s *Service) awaitExternalKeyStep(ctx context.Context, w externalWire, install ExternalInstall, step, old, next string) error {
	for n := range 3 {
		held, err := s.externalStepHeld(ctx, w, install, step, old, next)
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

// externalContinue writes the next unattempted step of one journaled External
// operation. The step is journaled as attempted before the single request, so a
// lost reply is resolved by externalCheckOperation and never by resending.
func (s *Service) externalContinue(ctx context.Context, o *repo.Operation, index int, generation uint64) (OperationResult, error) {
	r := OperationResult{Operation: o, RuntimeState: "effect_unknown"}
	if o.Outcome == "committed" {
		return r, nil
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
	err = s.keyPair(ctx, o.TargetID, o.KeeperID, false, func(old, next string) error {
		snapshot, err := w.ExternalKeySnapshot(ctx, install.ManagementKey)
		if err != nil {
			return err
		}
		// The start state of this step, read at the moment of the write: the
		// credential the step acts on is carried, and a revoke never removes the
		// last one. A step whose end state the user's CPA already shows is settled
		// by read-back instead: no write is sent for work that is already done.
		if !slices.Contains(snapshot.Keys, old) || (step == "revoke" && len(snapshot.Keys) <= 1) {
			if externalStepSettled(snapshot.Keys, step, old, next) {
				return errExternalSettled
			}
			if step == "revoke" && len(snapshot.Keys) <= 1 {
				return ErrLastKey
			}
			return cpa.ErrSourceConflict
		}
		// The credential that stays must be one this Manager staged: the step is
		// never allowed to delete the target and leave the CPA's last key in the
		// hands of a credential the journal cannot name.
		if step == "revoke" && !slices.Contains(snapshot.Keys, next) {
			return cpa.ErrSourceConflict
		}
		// The document this grant was derived from, compared immediately before the
		// single PUT: an edit the user made in between is never written over.
		if snapshot.SourceSHA256 != grant.SourceSHA256 {
			return cpa.ErrSourceConflict
		}
		if !s.keyGrantHeld(ctx, grant, o.ID, step) || time.Now().UnixMilli() >= grant.ExpiresAtMS {
			return ErrExternalUnavailable
		}
		// The version is asked before the step is journaled as attempted: a CPA
		// that became unsupported — or stopped answering the probe — since Prepare
		// refuses here, while nothing has been sent. Journaling the attempt first
		// would leave a step no later read can settle, because no byte of that
		// write was ever sent, and every later operation of the connection would
		// then be refused by the unsettled journal.
		if err := s.externalWriteGate(ctx, install); err != nil {
			return err
		}
		o.Steps[index].State = "attempted"
		o.Outcome = "unknown"
		if err = s.repo.SaveOperation(ctx, *o); err != nil {
			return err
		}
		o.Revision++
		// The whole list is written because the CPA v8 route replaces it, but the
		// step is still exactly one membership change: an add introduces the keeper
		// and a revoke removes the target.
		after := slices.Clone(snapshot.Keys)
		if step == "add" {
			if !slices.Contains(after, next) {
				after = append(after, next)
			}
		} else {
			if len(after) <= 1 {
				return ErrLastKey
			}
			after = slices.DeleteFunc(after, func(key string) bool { return key == old })
		}
		writeErr := w.ExternalWriteKeySet(ctx, install.ManagementKey, after)
		o.Steps[index].Returned = true
		if errors.Is(writeErr, cpa.ErrManagementRejected) {
			// A 401 is CPA's own refusal of the management credential, and it
			// proves no byte of the list was written. The step is journaled as not
			// applied, so a rebind can take it again instead of the journal staying
			// unknown forever.
			o.Steps[index].State = "unattempted"
			o.Outcome = "pending"
		}
		// Retain the attempt on a lost client: the journal, not the reply, is the
		// evidence a later reconcile reads.
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
