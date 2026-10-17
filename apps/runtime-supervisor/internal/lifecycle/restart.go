package lifecycle

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
)

// RestartRequest has an empty typed payload. The executable, child target and
// hard termination policy belong exclusively to the Supervisor.
type RestartRequest struct {
	OperationID               string `json:"operationId"`
	ExpectedRuntimeIdentity   string `json:"expectedRuntimeIdentity"`
	ExpectedRuntimeGeneration uint64 `json:"expectedRuntimeGeneration"`
}

func (r RestartRequest) Validate() error {
	return validateRequest(r.OperationID, r.ExpectedRuntimeIdentity, r.ExpectedRuntimeGeneration)
}

// Restart serializes with Start and Stop through the Executor gate. One
// durable operation binds termination of the exact owned child to spawning its
// replacement; retained evidence never resumes either side effect.
func (e *Executor) Restart(ctx context.Context, request RestartRequest) (journal.Operation, error) {
	if err := request.Validate(); err != nil {
		return journal.Operation{}, err
	}
	if e.closed.Load() {
		return journal.Operation{}, ErrPersistenceUnavailable
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return journal.Operation{}, err
	}
	if e.closed.Load() {
		return journal.Operation{}, ErrPersistenceUnavailable
	}
	intent := journal.Intent{
		OperationID:               request.OperationID,
		OperationType:             "restart",
		ExpectedRuntimeIdentity:   request.ExpectedRuntimeIdentity,
		ExpectedRuntimeGeneration: request.ExpectedRuntimeGeneration,
		RequestFingerprint:        sha256.Sum256([]byte("runtime.restart/v1:{}")),
	}
	operation, found, err := e.journal.Resolve(ctx, e.authority, intent)
	if err != nil {
		// Resolve is read-only and cannot supersede existing recovery authority.
		return journal.Operation{}, submissionError(err)
	}
	if found {
		// Retained accepted/running evidence is replayed, never resumed. In
		// particular, it cannot terminate a replacement child or spawn again.
		return operation, nil
	}
	if e.activeSelectionAmbiguous {
		return journal.Operation{}, journal.ErrOperationStateConflict
	}
	target, err := e.process.PrepareStop()
	if err != nil {
		if errors.Is(err, cpaprocess.ErrStateConflict) {
			return journal.Operation{}, journal.ErrOperationStateConflict
		}
		return journal.Operation{}, fmt.Errorf("%w: prepare Restart: %w", ErrExecutionFailed, err)
	}
	defer target.Release()

	operation, created, err := e.journal.Begin(ctx, e.authority, intent)
	if err != nil {
		e.failRecoveryForAmbiguousBegin(err)
		return journal.Operation{}, submissionError(err)
	}
	if !created {
		return operation, nil
	}

	// Durable acceptance transfers execution to the Supervisor. A single
	// intent covers both exact-child termination and replacement spawn. If
	// either step or terminal persistence fails, replay remains side-effect
	// free; recovery requires a separately identified operation.
	executionCtx := context.Background()
	operation, err = e.journal.MarkRunning(executionCtx, e.authority.RuntimeIdentity, intent.OperationID)
	if err != nil {
		e.disableRecovery(RecoveryStateManualIntervention)
		return journal.Operation{}, fmt.Errorf("%w: %w", ErrPersistenceUnavailable, err)
	}
	// The old child's termination is expected once running evidence is durable.
	// Fence its exit before Terminate; only durable replacement success re-arms.
	e.disableRecovery(RecoveryStateInactive)
	if _, err := target.Terminate(executionCtx); err != nil {
		return e.completeRestartFailure(executionCtx, operation, intent.OperationID, "process_restart_stop_failed", err)
	}
	// The concrete StopTarget releases its exact-child reservation after
	// confirmed reap. Release is idempotent and makes that requirement explicit
	// for alternative process implementations before replacement Start.
	target.Release()
	started, err := e.startProcess(executionCtx, e.currentExecutableLocked())
	if err != nil {
		return e.completeRestartFailure(executionCtx, operation, intent.OperationID, "process_restart_start_failed", err)
	}
	result, err := e.journal.Complete(executionCtx, e.authority.RuntimeIdentity, intent.OperationID, journal.StateSucceeded, "")
	if err != nil {
		// The replacement may already be running. Never stop it or spawn again
		// to compensate for missing terminal evidence.
		e.disableRecovery(RecoveryStateManualIntervention)
		return operation, fmt.Errorf("%w: record result: %w", ErrExecutionFailed, err)
	}
	e.armFreshRecovery(started)
	// Success means confirmed old-child reap plus replacement spawn and
	// ownership publication, not Runtime readiness.
	return result, nil
}

func (e *Executor) completeRestartFailure(
	ctx context.Context,
	operation journal.Operation,
	operationID string,
	failureCode string,
	executionErr error,
) (journal.Operation, error) {
	result, err := e.journal.Complete(ctx, e.authority.RuntimeIdentity, operationID, journal.StateFailed, failureCode)
	if err != nil {
		e.disableRecovery(RecoveryStateManualIntervention)
		return operation, fmt.Errorf("%w: record result: %w", ErrExecutionFailed, err)
	}
	e.disableRecovery(RecoveryStateInactive)
	return result, fmt.Errorf("%w: %w", ErrExecutionFailed, executionErr)
}
