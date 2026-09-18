package lifecycle

import (
	"context"
	"crypto/sha256"

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
	if e.closed.Load() {
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
	return operation, nil
}

func updateOperationFingerprint(operationType, targetVersion string) journal.RequestFingerprint {
	prefix := "runtime.prepare_update/v1:{targetVersion:"
	if operationType == UpdateOperationActivate {
		prefix = "runtime.activate_update/v1:{targetVersion:"
	}
	return sha256.Sum256([]byte(prefix + targetVersion + "}"))
}
