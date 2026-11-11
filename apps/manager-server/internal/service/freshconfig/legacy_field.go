package freshconfig

import (
	"context"
	"errors"
	"strconv"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// This file owns the single implementation of "resolve an unknown single-leaf
// operation" for every per-field journal: the pre-cutover entries the page
// inherited AND the live private H1/direct writes, which share the same journal.
// Nothing here writes a leaf: the original write is never replayed, only
// re-observed.

// reconcileFieldOperation resolves an unknown single-leaf operation. The H1
// profile is verified against its Runtime parent, the direct runtime-journal
// profile against its admitted source, and the manager-owned profile against
// CPA's current value.
func (s *Service) reconcileFieldOperation(ctx context.Context, r freshstore.Record, auth authority, operation freshstore.ConfigOperation) (freshstore.ConfigOperation, error) {
	if operation.Field == "" {
		return operation, ErrInvalid
	}
	source := cpa.ConfigFieldSource{SourceID: operation.SourceID, Generation: operation.Generation, Revision: operation.Revision, SHA256: operation.SHA256}
	switch {
	case operation.ParentID != "":
		// The private H1 profile keeps its Host reservation protocol; only the
		// receipt of the original parent can resolve it.
		if s.h1 == nil || s.transport == nil {
			return operation, cpa.ErrApplyUnknown
		}
		return s.reconcileFieldOperationH1(ctx, r, operation)
	case !auth.wire.HasRuntimeJournal():
		return s.resolveFieldOperationManaged(ctx, r, auth, operation)
	default:
		observed, err := auth.wire.ReconcileConfigField(ctx, source, operation.RequestID, operation.Field, operation.Value)
		if err != nil {
			return operation, err
		}
		if observed.OperationState != "persisted" && observed.OperationState != "recovered" {
			return operation, cpa.ErrApplyUnknown
		}
		next := operation
		next.State = observed.OperationState
		if err := s.updateFieldJournal(ctx, r, operation, next); err != nil {
			return operation, err
		}
		return next, nil
	}
}

// reconcileLegacyH1 proves the original H1 parent still carries exactly this
// intent before committing its receipt. A mismatch is a conflict, never a write.
func (s *Service) reconcileFieldOperationH1(ctx context.Context, r freshstore.Record, operation freshstore.ConfigOperation) (freshstore.ConfigOperation, error) {
	if operation.ParentID != "c:"+operation.RequestID || !operation.Attempted || !operation.HTTP200 {
		return operation, cpa.ErrApplyUnknown
	}
	if operation.SourceID != r.Operation.Identity.InstallationID ||
		operation.Generation != strconv.FormatUint(r.Operation.Strategy.TargetGeneration, 10) {
		return operation, cpa.ErrSourceConflict
	}
	parent, err := s.h1.ParentRecord(ctx, s.transport, operation.ParentID)
	if err != nil {
		return operation, err
	}
	if parent == nil {
		return operation, cpa.ErrApplyUnknown
	}
	intentPath, intentValue, ok := cpa.H1IntentField(parent.Intent)
	if !ok || intentPath != operation.Field || !canonicalEqual(intentValue, operation.Value) ||
		parent.SourceID != operation.SourceID || parent.Generation != operation.Generation ||
		parent.ClaimSHA256 != operation.SHA256 || parent.ClaimRevision != operation.ClaimRevision ||
		cpa.H1Terminal(parent.Intent) != operation.IntentDigest || parent.Intent.RequestID != operation.RequestID {
		return operation, cpa.ErrSourceConflict
	}
	receipt, err := s.h1.Commit(ctx, s.transport, operation.ParentID, 0)
	if err != nil {
		return operation, err
	}
	if receipt.ID != operation.ParentID+":0:"+cpa.H1IntentStepScope(parent.Intent) ||
		receipt.BeginRevision != operation.BeginRevision || receipt.Receipt == "" {
		return operation, cpa.ErrApplyUnknown
	}
	next := operation
	next.State = "persisted"
	next.Receipt = receipt.Receipt
	if err = s.updateFieldJournal(ctx, r, operation, next); err != nil {
		return operation, err
	}
	if err = s.h1.Finish(ctx, s.transport, next.ParentID, cpa.H1Terminal(next)); err != nil {
		return next, err
	}
	return next, nil
}

// resolveLegacyManaged records what a manager-owned read-back proves. An
// acknowledged write another writer already replaced is the superseded outcome,
// never a failure of the original.
func (s *Service) resolveFieldOperationManaged(ctx context.Context, r freshstore.Record, auth authority, operation freshstore.ConfigOperation) (freshstore.ConfigOperation, error) {
	presence, value, err := auth.wire.ReadConfigFieldValue(ctx, auth.secret, operation.Field)
	if err != nil {
		if auth.mode == ModeExternal && errors.Is(err, cpa.ErrManagementRejected) {
			return operation, err
		}
		return operation, cpa.ErrApplyUnknown
	}
	switch {
	case presence == "present" && canonicalDigest(value) == canonicalDigest(operation.Value):
		next := operation
		next.State = "persisted"
		if err = s.updateFieldJournal(ctx, r, operation, next); err != nil {
			return operation, err
		}
		return next, nil
	case presence == "present", presence == "missing", presence == "null":
		if operation.HTTP200 {
			next := operation
			next.State, next.Code = "persisted", "superseded"
			if err = s.updateFieldJournal(ctx, r, operation, next); err != nil {
				return operation, err
			}
			return next, nil
		}
		next := operation
		next.State, next.Code = "failed", "write_not_applied"
		if err = s.updateFieldJournal(ctx, r, operation, next); err != nil {
			return operation, err
		}
		return next, nil
	default:
		// An unreadable scalar proves nothing either way.
		return operation, cpa.ErrApplyUnknown
	}
}

func (s *Service) updateFieldJournal(ctx context.Context, r freshstore.Record, before, next freshstore.ConfigOperation) error {
	_, err := s.store.Update(ctx, r.Operation.Identity, r.Operation.Revision, func(v *freshstore.Record) error {
		journal := v.Config[before.Field]
		if journal == nil || journal.Latest != before.RequestID {
			return freshstore.ErrConflict
		}
		current, ok := journal.Operations[before.RequestID]
		if !ok || !canonicalEqual(current, before) {
			// The entry crossed the store's JSON boundary, so compare the canonical
			// form: an int and the same number reloaded are one entry.
			return freshstore.ErrConflict
		}
		journal.Operations[before.RequestID] = next
		return nil
	})
	return err
}
