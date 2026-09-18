package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/readiness"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/selection"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

var (
	ErrUnsupportedActivation  = errors.New("update activation is unsupported")
	ErrTargetStageUnavailable = errors.New("target finalized stage is unavailable")
	ErrTargetStageCorrupt     = errors.New("target finalized stage is corrupt")
)

const (
	ActivationExecutionTimeout           = 2 * time.Minute
	ActivationCandidateReadinessTimeout  = 40 * time.Second
	ActivationRollbackReadinessTimeout   = 40 * time.Second
	ActivationRollbackReserve            = 50 * time.Second
	ActivationTerminalPersistenceTimeout = 15 * time.Second
)

var (
	activationExecutionTimeout          = ActivationExecutionTimeout
	activationCandidateReadinessTimeout = ActivationCandidateReadinessTimeout
	activationRollbackReadinessTimeout  = ActivationRollbackReadinessTimeout
	activationRollbackReserve           = ActivationRollbackReserve
	activationPollInterval              = 200 * time.Millisecond
)

type activeSelectionStore interface {
	ResolveFinalized(string) (selection.Descriptor, error)
	Revalidate(selection.Descriptor) (selection.Descriptor, error)
	Commit(selection.Descriptor) error
}

type activationReadiness interface {
	Observe(context.Context) readiness.State
}

// ActivateUpdateRequest carries only freshness fences and exact target policy.
// Executable, metadata and rollback paths remain Supervisor-private.
type ActivateUpdateRequest struct {
	OperationID               string
	ExpectedRuntimeIdentity   string
	ExpectedRuntimeGeneration uint64
	ExpectedActiveArtifactID  artifact.ID
	TargetVersion             string
}

func (r ActivateUpdateRequest) Validate() error {
	if err := validateRequest(r.OperationID, r.ExpectedRuntimeIdentity, r.ExpectedRuntimeGeneration); err != nil ||
		!r.ExpectedActiveArtifactID.IsValid() || runtimeupdate.ValidateVersion(r.TargetVersion) != nil {
		return ErrInvalidRequest
	}
	return nil
}

// EnableActivateUpdate installs the selected startup authority and the local
// stage/selection dependencies before protocol admission begins.
func (e *Executor) EnableActivateUpdate(
	selected selection.Descriptor,
	observer activeArtifactRefresher,
	selections activeSelectionStore,
	ready activationReadiness,
) error {
	if selected.ExecutablePath == "" || observer == nil || selections == nil || ready == nil {
		return errors.New("activate-update requires active selection, artifact observation, selection storage and readiness")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed.Load() {
		return ErrPersistenceUnavailable
	}
	if selected.IsFinalizedStage() {
		resolved, err := selections.Revalidate(selected)
		if err != nil {
			return fmt.Errorf("configure selected finalized stage: %w", err)
		}
		selected = resolved
	}
	if err := refreshSelectedDescriptor(observer, &selected); err != nil {
		if selected.IsFinalizedStage() || !errors.Is(err, ErrActiveArtifactUnavailable) {
			return fmt.Errorf("configure selected active artifact: %w", err)
		}
	}
	e.active = selected
	e.executable = selected.ExecutablePath
	e.artifact = observer
	e.selections = selections
	e.readiness = ready
	return nil
}

func refreshSelectedDescriptor(observer activeArtifactRefresher, selected *selection.Descriptor) error {
	if observer == nil || selected == nil || selected.ExecutablePath == "" {
		return ErrActiveArtifactUnavailable
	}
	var refreshErr error
	if dynamic, ok := observer.(selectedArtifactRefresher); ok {
		refreshErr = dynamic.RefreshFrom(selected.ExecutablePath, selected.MetadataPath)
	} else {
		refreshErr = observer.Refresh()
	}
	observed := observer.Observation()
	if errors.Is(refreshErr, artifact.ErrExecutableUnavailable) || observed == nil || !observed.ArtifactID.IsValid() {
		return ErrActiveArtifactUnavailable
	}
	if selected.ArtifactID.IsValid() && observed.ArtifactID != selected.ArtifactID {
		return ErrActiveArtifactMismatch
	}
	selected.ArtifactID = observed.ArtifactID
	return nil
}

// ActivateUpdate performs one bounded A -> candidate B transaction while the
// shared lifecycle gate excludes Start/Stop/Restart/automatic recovery.
func (e *Executor) ActivateUpdate(ctx context.Context, request ActivateUpdateRequest) (journal.Operation, error) {
	if err := request.Validate(); err != nil {
		return journal.Operation{}, err
	}
	if e.closed.Load() {
		return journal.Operation{}, ErrPersistenceUnavailable
	}
	operationDeadline := time.Now().Add(activationExecutionTimeout)
	preconditionCtx, cancelPreconditions := context.WithDeadline(ctx, operationDeadline)
	defer cancelPreconditions()

	e.mu.Lock()
	defer e.mu.Unlock()
	if err := preconditionCtx.Err(); err != nil {
		return journal.Operation{}, err
	}
	if e.closed.Load() {
		return journal.Operation{}, ErrPersistenceUnavailable
	}
	observer, selections, ready := e.artifact, e.selections, e.readiness
	if observer == nil || selections == nil || ready == nil {
		return journal.Operation{}, ErrUnsupportedActivation
	}
	intent := journal.Intent{
		OperationID:               request.OperationID,
		OperationType:             UpdateOperationActivate,
		ExpectedRuntimeIdentity:   request.ExpectedRuntimeIdentity,
		ExpectedRuntimeGeneration: request.ExpectedRuntimeGeneration,
		RequestFingerprint:        updateOperationFingerprint(UpdateOperationActivate, request.TargetVersion),
	}
	operation, found, err := e.journal.Resolve(preconditionCtx, e.authority, intent)
	if err != nil {
		return journal.Operation{}, submissionError(err)
	}
	if found {
		return operation, nil
	}
	if e.activeSelectionAmbiguous {
		return journal.Operation{}, journal.ErrOperationStateConflict
	}

	prior := e.currentDescriptorLocked()
	if prior.IsFinalizedStage() {
		prior, err = selections.Revalidate(prior)
		if err != nil {
			return journal.Operation{}, ErrActiveArtifactUnavailable
		}
	}
	if err := refreshExpectedActiveArtifact(observer, prior, request.ExpectedActiveArtifactID); err != nil {
		return journal.Operation{}, err
	}
	prior.ArtifactID = request.ExpectedActiveArtifactID

	candidate, err := selections.ResolveFinalized(request.TargetVersion)
	if err != nil {
		return journal.Operation{}, mapTargetStageError(err)
	}
	if candidate.ArtifactID == prior.ArtifactID {
		return journal.Operation{}, journal.ErrOperationStateConflict
	}

	oldTarget, err := e.process.PrepareStop()
	if err != nil {
		if errors.Is(err, cpaprocess.ErrStateConflict) {
			return journal.Operation{}, journal.ErrOperationStateConflict
		}
		return journal.Operation{}, fmt.Errorf("%w: reserve active child: %v", ErrExecutionFailed, err)
	}
	defer oldTarget.Release()
	oldInstance := e.process.Observe().InstanceID

	operation, created, err := e.journal.Begin(preconditionCtx, e.authority, intent)
	if err != nil {
		e.failRecoveryForAmbiguousBegin(err)
		return journal.Operation{}, submissionError(err)
	}
	if !created {
		return operation, nil
	}
	executionCtx, cancelExecution := context.WithDeadline(context.Background(), operationDeadline)
	defer cancelExecution()
	operation, err = e.journal.MarkRunning(executionCtx, e.authority.RuntimeIdentity, intent.OperationID)
	if err != nil {
		e.disableRecovery(RecoveryStateManualIntervention)
		return journal.Operation{}, fmt.Errorf("%w: %w", ErrPersistenceUnavailable, err)
	}

	// Candidate ownership starts only after durable running evidence. Recovery
	// stays disabled until either terminal success for B or terminal rollback
	// evidence for A has committed.
	e.disableRecovery(RecoveryStateInactive)
	if _, err := oldTarget.Terminate(executionCtx); err != nil {
		oldTarget.Release()
		return e.rollbackActivation(operation, intent.OperationID, prior, oldInstance, 0,
			"activation_stop_failed", err, operationDeadline)
	}
	oldTarget.Release()

	candidate, err = selections.Revalidate(candidate)
	if err != nil {
		return e.rollbackActivation(operation, intent.OperationID, prior, oldInstance, 0,
			"activation_target_changed", err, operationDeadline)
	}
	started, err := e.process.Start(executionCtx, cpaprocess.StartSpec{Executable: candidate.ExecutablePath})
	if err != nil {
		return e.rollbackActivation(operation, intent.OperationID, prior, oldInstance, 0,
			"activation_start_failed", err, operationDeadline)
	}
	if err := e.waitExactReady(executionCtx, started.InstanceID, candidateReadinessBudget(operationDeadline)); err != nil {
		return e.rollbackActivation(operation, intent.OperationID, prior, oldInstance, started.InstanceID,
			"activation_readiness_failed", err, operationDeadline)
	}
	if _, err := selections.Revalidate(candidate); err != nil {
		return e.rollbackActivation(operation, intent.OperationID, prior, oldInstance, started.InstanceID,
			"activation_target_changed", err, operationDeadline)
	}

	if err := selections.Commit(candidate); err != nil {
		if errors.Is(err, selection.ErrSelectionNotPublished) {
			return e.rollbackActivation(operation, intent.OperationID, prior, oldInstance, started.InstanceID,
				"active_selection_persistence_failed", err, operationDeadline)
		}
		return e.completeManualIntervention(operation, intent.OperationID,
			"active_selection_persistence_failed", err)
	}

	// The durable selection commit is the point of no return. Every later
	// ordinary lifecycle/recovery spawn consumes B, and no terminal evidence
	// failure is allowed to roll the process back to A.
	e.active = candidate
	e.executable = candidate.ExecutablePath
	result, err := e.completeActivation(operation, intent.OperationID, journal.StateSucceeded, "")
	if err != nil {
		e.disableRecovery(RecoveryStateManualIntervention)
		return operation, fmt.Errorf("%w: record committed activation result: %w", ErrExecutionFailed, err)
	}
	e.armFreshRecovery(started)
	return result, nil
}

func mapTargetStageError(err error) error {
	switch {
	case errors.Is(err, runtimeupdate.ErrStageUnavailable):
		return fmt.Errorf("%w: %v", ErrTargetStageUnavailable, err)
	case errors.Is(err, runtimeupdate.ErrStageConflict), errors.Is(err, selection.ErrSelectionInvalid):
		return fmt.Errorf("%w: %v", ErrTargetStageCorrupt, err)
	default:
		return fmt.Errorf("%w: %v", ErrTargetStageCorrupt, err)
	}
}

func candidateReadinessBudget(operationDeadline time.Time) time.Duration {
	remainingForCandidate := time.Until(operationDeadline.Add(-activationRollbackReserve))
	if remainingForCandidate < 0 {
		return 0
	}
	if remainingForCandidate < activationCandidateReadinessTimeout {
		return remainingForCandidate
	}
	return activationCandidateReadinessTimeout
}

func (e *Executor) waitExactReady(ctx context.Context, instanceID uint64, budget time.Duration) error {
	if instanceID == 0 || budget <= 0 {
		return context.DeadlineExceeded
	}
	readyCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	for {
		observed := e.process.Observe()
		if observed.InstanceID != instanceID {
			return errors.New("exact child ownership changed")
		}
		switch observed.State {
		case cpaprocess.StateExited:
			return errors.New("exact child exited before readiness")
		case cpaprocess.StateUnknown:
			return errors.New("exact child ownership is unknown")
		case cpaprocess.StateRunning:
		default:
			return errors.New("exact child is not running")
		}
		state := e.readiness.Observe(readyCtx)
		if state == readiness.Ready {
			after := e.process.Observe()
			if after.State == cpaprocess.StateRunning && after.InstanceID == instanceID {
				return nil
			}
			return errors.New("exact child changed after readiness")
		}
		if state == readiness.Unknown || state == readiness.Offline {
			return fmt.Errorf("exact child readiness became %s", state)
		}
		timer := time.NewTimer(activationPollInterval)
		select {
		case <-readyCtx.Done():
			timer.Stop()
			return readyCtx.Err()
		case <-timer.C:
		}
	}
}

func (e *Executor) rollbackActivation(
	operation journal.Operation,
	operationID string,
	prior selection.Descriptor,
	priorInstance uint64,
	candidateInstance uint64,
	failureCode string,
	executionErr error,
	operationDeadline time.Time,
) (journal.Operation, error) {
	rollbackCtx, cancel := context.WithDeadline(context.Background(), operationDeadline)
	defer cancel()
	observed := e.process.Observe()

	if candidateInstance != 0 {
		if observed.InstanceID != candidateInstance {
			return e.failRollback(operation, operationID, errors.New("candidate ownership changed"))
		}
		switch observed.State {
		case cpaprocess.StateRunning:
			target, err := e.process.PrepareStop()
			if err != nil {
				return e.failRollback(operation, operationID, err)
			}
			if _, err := target.Terminate(rollbackCtx); err != nil {
				target.Release()
				return e.failRollback(operation, operationID, err)
			}
			target.Release()
		case cpaprocess.StateExited:
		case cpaprocess.StateUnknown:
			return e.failRollback(operation, operationID, errors.New("candidate ownership is unknown"))
		default:
			return e.failRollback(operation, operationID, errors.New("candidate ownership is not confirmed"))
		}
	}

	if prior.IsFinalizedStage() {
		resolved, err := e.selections.Revalidate(prior)
		if err != nil {
			return e.failRollback(operation, operationID, err)
		}
		prior = resolved
	}
	if err := refreshSelectedDescriptor(e.artifact, &prior); err != nil {
		return e.failRollback(operation, operationID, err)
	}

	observed = e.process.Observe()
	var restored cpaprocess.Observation
	if observed.State == cpaprocess.StateRunning && observed.InstanceID == priorInstance && candidateInstance == 0 {
		restored = observed
	} else {
		if observed.State == cpaprocess.StateUnknown || observed.State == cpaprocess.StateRunning {
			return e.failRollback(operation, operationID, errors.New("cannot safely spawn rollback child"))
		}
		var err error
		restored, err = e.process.Start(rollbackCtx, cpaprocess.StartSpec{Executable: prior.ExecutablePath})
		if err != nil {
			return e.failRollback(operation, operationID, err)
		}
	}
	if err := e.waitExactReady(rollbackCtx, restored.InstanceID, activationRollbackReadinessTimeout); err != nil {
		return e.failRollback(operation, operationID, err)
	}
	result, err := e.completeActivation(operation, operationID, journal.StateFailed, failureCode)
	if err != nil {
		e.disableRecovery(RecoveryStateManualIntervention)
		return operation, fmt.Errorf("%w: record rollback result: %w", ErrExecutionFailed, err)
	}
	e.armFreshRecovery(restored)
	_ = executionErr // failureCode is the stable journal/protocol contract.
	return result, nil
}

func (e *Executor) failRollback(operation journal.Operation, operationID string, rollbackErr error) (journal.Operation, error) {
	result, err := e.completeActivation(operation, operationID, journal.StateFailed, "activation_rollback_failed")
	e.disableRecovery(RecoveryStateManualIntervention)
	if err != nil {
		return operation, fmt.Errorf("%w: rollback and terminal persistence failed: %v; %w", ErrExecutionFailed, rollbackErr, err)
	}
	return result, nil
}

func (e *Executor) completeManualIntervention(
	operation journal.Operation,
	operationID string,
	failureCode string,
	executionErr error,
) (journal.Operation, error) {
	e.activeSelectionAmbiguous = true
	result, err := e.completeActivation(operation, operationID, journal.StateFailed, failureCode)
	e.disableRecovery(RecoveryStateManualIntervention)
	if err != nil {
		return operation, fmt.Errorf("%w: ambiguous activation and terminal persistence: %v; %w", ErrExecutionFailed, executionErr, err)
	}
	return result, nil
}

func (e *Executor) completeActivation(
	operation journal.Operation,
	operationID string,
	state journal.State,
	failureCode string,
) (journal.Operation, error) {
	completeCtx, cancel := context.WithTimeout(context.Background(), ActivationTerminalPersistenceTimeout)
	defer cancel()
	result, err := e.journal.Complete(completeCtx, e.authority.RuntimeIdentity, operationID, state, failureCode)
	if err != nil {
		return operation, err
	}
	return result, nil
}
