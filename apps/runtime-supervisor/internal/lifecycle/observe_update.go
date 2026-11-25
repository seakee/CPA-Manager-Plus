package lifecycle

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

const (
	UpdateOperationPrepare  = "prepare_update"
	UpdateOperationActivate = "activate_update"
)

// ObserveUpdateOperationRequest identifies one exact retained update intent.
// ExpectedRuntimeGeneration fences the query against the current Supervisor
// incarnation; a matching record may have been created by an older generation.
type ObserveUpdateOperationRequest struct {
	OperationID               string
	ExpectedRuntimeIdentity   string
	ExpectedRuntimeGeneration uint64
	OperationType             string
	TargetVersion             string
}

func (r ObserveUpdateOperationRequest) Validate() error {
	if err := validateRequest(r.OperationID, r.ExpectedRuntimeIdentity, r.ExpectedRuntimeGeneration); err != nil {
		return err
	}
	if r.OperationType != UpdateOperationPrepare && r.OperationType != UpdateOperationActivate {
		return ErrInvalidRequest
	}
	if runtimeupdate.ValidateVersion(r.TargetVersion) != nil {
		return ErrInvalidRequest
	}
	return nil
}

// ObserveUpdateOperation reads durable evidence without taking an execution
// gate and without creating or transitioning a journal record.
func (e *Executor) ObserveUpdateOperation(ctx context.Context, request ObserveUpdateOperationRequest) (journal.Operation, error) {
	if err := request.Validate(); err != nil {
		return journal.Operation{}, err
	}
	if e == nil || e.closed.Load() {
		return journal.Operation{}, ErrPersistenceUnavailable
	}
	operation, found, err := e.journal.Resolve(ctx, e.authority, journal.Intent{
		OperationID:               request.OperationID,
		OperationType:             request.OperationType,
		ExpectedRuntimeIdentity:   request.ExpectedRuntimeIdentity,
		ExpectedRuntimeGeneration: request.ExpectedRuntimeGeneration,
		RequestFingerprint:        updateOperationFingerprint(request.OperationType, request.TargetVersion),
	})
	if err != nil {
		return journal.Operation{}, submissionError(err)
	}
	if !found {
		return journal.Operation{}, journal.ErrOperationNotFound
	}
	if request.OperationType == UpdateOperationActivate && operation.State == journal.StateSucceeded && e.isActivateUpdateConfigured() {
		intentStore, ok := e.resolveActivationIntentStore()
		if !ok {
			return journal.Operation{}, fmt.Errorf("%w: activation intent store unavailable", ErrPersistenceUnavailable)
		}
		record, loadErr := intentStore.LoadActivationIntent(request.ExpectedRuntimeIdentity, request.OperationID)
		if loadErr != nil {
			return journal.Operation{}, fmt.Errorf("%w: %w", ErrPersistenceUnavailable, loadErr)
		}
		if record.OperationID != request.OperationID ||
			record.RuntimeIdentity != request.ExpectedRuntimeIdentity ||
			record.TargetVersion != request.TargetVersion ||
			!record.TargetArtifactID.IsValid() ||
			!record.PreviousArtifactID.IsValid() ||
			record.TargetArtifactID == record.PreviousArtifactID {
			return journal.Operation{}, fmt.Errorf("%w: activation intent mismatch", ErrPersistenceUnavailable)
		}
		if operation.RuntimeGeneration == e.authority.RuntimeGeneration {
			sel, hasSel, selErr := intentStore.LoadPersistedSelection()
			if selErr != nil {
				return journal.Operation{}, fmt.Errorf("%w: %w", ErrPersistenceUnavailable, selErr)
			}
			if !hasSel {
				return journal.Operation{}, fmt.Errorf("%w: active selection missing for succeeded activation", ErrPersistenceUnavailable)
			}
			if sel.Version == record.TargetVersion && sel.ArtifactID != record.TargetArtifactID {
				return journal.Operation{}, fmt.Errorf("%w: active selection digest mismatches activation intent target", journal.ErrOperationStateConflict)
			}
		}
	}
	return operation, nil
}

func updateOperationFingerprint(operationType, targetVersion string) journal.RequestFingerprint {
	prefix := "runtime.prepare_update/v1:{targetVersion:"
	if operationType == UpdateOperationActivate {
		prefix = "runtime.activate_update/v1:{targetVersion:"
	}
	return sha256.Sum256([]byte(prefix + targetVersion + "}"))
}
