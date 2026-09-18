package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/selection"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

var (
	ErrActiveArtifactUnavailable = errors.New("active artifact is unavailable")
	ErrActiveArtifactMismatch    = errors.New("active artifact does not match expected identity")
	ErrUnsupportedStaging        = errors.New("update staging is unsupported on this platform")
	ErrReleaseMetadataInvalid    = errors.New("official release metadata is unavailable or invalid")
)

const (
	// PrepareUpdateExecutionTimeout bounds the whole prepare execution window,
	// including one bounded metadata request, one bounded asset request, local
	// extraction and filesystem publication. The stage context uses the same
	// absolute deadline after durable running evidence, independent of caller
	// cancel.
	PrepareUpdateExecutionTimeout = 11 * time.Minute
	// PrepareUpdateTerminalPersistenceTimeout is a separate bounded budget for
	// terminal journal evidence after staging completes or fails.
	PrepareUpdateTerminalPersistenceTimeout = 15 * time.Second
)

var prepareUpdateExecutionTimeout = PrepareUpdateExecutionTimeout

type activeArtifactRefresher interface {
	Refresh() error
	Observation() *artifact.Observation
}

type selectedArtifactRefresher interface {
	RefreshFrom(executablePath, metadataPath string) error
}

type updatePreparer interface {
	Resolve(context.Context, string) (runtimeupdate.Release, error)
	Stage(context.Context, runtimeupdate.Release) (runtimeupdate.Metadata, error)
}

// PrepareUpdateRequest contains only exact target policy and freshness fences.
// Release authority and all filesystem paths remain Supervisor-owned.
type PrepareUpdateRequest struct {
	OperationID               string
	ExpectedRuntimeIdentity   string
	ExpectedRuntimeGeneration uint64
	ExpectedActiveArtifactID  artifact.ID
	TargetVersion             string
}

func (r PrepareUpdateRequest) Validate() error {
	if err := validateRequest(r.OperationID, r.ExpectedRuntimeIdentity, r.ExpectedRuntimeGeneration); err != nil ||
		!r.ExpectedActiveArtifactID.IsValid() || runtimeupdate.ValidateVersion(r.TargetVersion) != nil {
		return ErrInvalidRequest
	}
	return nil
}

// EnablePrepareUpdate installs the Runtime16 dependencies before the executor
// is exposed. Unsupported builds leave this unset and omit the capability.
func (e *Executor) EnablePrepareUpdate(observer activeArtifactRefresher, preparer updatePreparer) error {
	if observer == nil || preparer == nil {
		return errors.New("prepare-update requires artifact observation and trusted staging")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed.Load() {
		return ErrPersistenceUnavailable
	}
	e.artifact = observer
	e.updates = preparer
	return nil
}

func (e *Executor) PrepareUpdate(ctx context.Context, request PrepareUpdateRequest) (journal.Operation, error) {
	if err := request.Validate(); err != nil {
		return journal.Operation{}, err
	}
	if !e.admitPrepareWorker() {
		return journal.Operation{}, ErrPersistenceUnavailable
	}
	defer e.prepareWorkers.Done()

	operationDeadline := time.Now().Add(prepareUpdateExecutionTimeout)
	preconditionCtx, cancelPreconditions := context.WithDeadline(ctx, operationDeadline)
	defer cancelPreconditions()
	if err := preconditionCtx.Err(); err != nil {
		return journal.Operation{}, err
	}

	// Only prepare operations serialize on this narrow updater gate. The shared
	// lifecycle gate is acquired around durable admission and fresh fencing, but
	// is released for release network I/O and staging so recovery can proceed.
	// Serialization queue wait consumes from the bounded execution budget above.
	e.prepareMu.Lock()
	defer e.prepareMu.Unlock()

	if err := preconditionCtx.Err(); err != nil {
		return journal.Operation{}, err
	}

	intent := journal.Intent{
		OperationID:               request.OperationID,
		OperationType:             UpdateOperationPrepare,
		ExpectedRuntimeIdentity:   request.ExpectedRuntimeIdentity,
		ExpectedRuntimeGeneration: request.ExpectedRuntimeGeneration,
		RequestFingerprint:        updateOperationFingerprint(UpdateOperationPrepare, request.TargetVersion),
	}

	// Resolve retained operation IDs before touching the active executable. A
	// replay therefore remains side-effect free, including across generations.
	e.mu.Lock()
	if err := preconditionCtx.Err(); err != nil {
		e.mu.Unlock()
		return journal.Operation{}, err
	}
	if e.closed.Load() {
		e.mu.Unlock()
		return journal.Operation{}, ErrPersistenceUnavailable
	}
	observer := e.artifact
	preparer := e.updates
	if observer == nil || preparer == nil {
		e.mu.Unlock()
		return journal.Operation{}, ErrUnsupportedStaging
	}
	operation, found, err := e.journal.Resolve(preconditionCtx, e.authority, intent)
	if err != nil {
		e.mu.Unlock()
		return journal.Operation{}, submissionError(err)
	}
	if found {
		e.mu.Unlock()
		return operation, nil
	}
	if e.activeSelectionAmbiguous {
		e.mu.Unlock()
		return journal.Operation{}, journal.ErrOperationStateConflict
	}
	if err := refreshExpectedActiveArtifact(observer, e.active, request.ExpectedActiveArtifactID); err != nil {
		e.mu.Unlock()
		return journal.Operation{}, err
	}
	e.mu.Unlock()
	if e.closed.Load() {
		return journal.Operation{}, ErrPersistenceUnavailable
	}

	// Release metadata lookup is privileged Supervisor I/O, but it must not
	// hold the shared lifecycle/recovery gate.
	release, err := preparer.Resolve(preconditionCtx, request.TargetVersion)
	if err != nil {
		return journal.Operation{}, mapPrepareResolveError(err)
	}

	// Re-enter an admission barrier before Begin. CloseAdmission either wins
	// here (so no durable intent is created) or waits until Begin/MarkRunning has
	// committed; in both cases journal draining is race-free.
	operation, execute, err := e.beginPrepareExecution(preconditionCtx, intent, observer, request.ExpectedActiveArtifactID)
	if err != nil || !execute {
		return operation, err
	}

	// Durable running evidence transfers execution to the Supervisor. The
	// caller's request context no longer controls staging or terminal evidence.
	stageCtx, cancelStage := context.WithDeadline(context.Background(), operationDeadline)
	_, stageErr := preparer.Stage(stageCtx, release)
	cancelStage()
	state, failureCode := journal.StateSucceeded, ""
	if stageErr != nil {
		state, failureCode = journal.StateFailed, "staging_failed"
		if errors.Is(stageErr, runtimeupdate.ErrArchiveInvalid) {
			failureCode = "release_asset_invalid"
		}
	}
	completeCtx, cancelComplete := context.WithTimeout(context.Background(), PrepareUpdateTerminalPersistenceTimeout)
	result, err := e.journal.Complete(completeCtx, e.authority.RuntimeIdentity, intent.OperationID, state, failureCode)
	cancelComplete()
	if err != nil {
		return operation, fmt.Errorf("%w: record prepare-update result: %w", ErrExecutionFailed, err)
	}
	return result, nil
}

func (e *Executor) admitPrepareWorker() bool {
	e.prepareAdmissionMu.Lock()
	defer e.prepareAdmissionMu.Unlock()
	if e.closed.Load() {
		return false
	}
	e.prepareWorkers.Add(1)
	return true
}

func refreshExpectedActiveArtifact(observer activeArtifactRefresher, selected selection.Descriptor, expected artifact.ID) error {
	// Manifest/version errors do not invalidate an otherwise exact executable
	// digest observation. An unavailable executable or invalid observation does.
	var refreshErr error
	if dynamic, ok := observer.(selectedArtifactRefresher); ok && selected.ExecutablePath != "" {
		refreshErr = dynamic.RefreshFrom(selected.ExecutablePath, selected.MetadataPath)
	} else {
		refreshErr = observer.Refresh()
	}
	active := observer.Observation()
	if errors.Is(refreshErr, artifact.ErrExecutableUnavailable) || active == nil || !active.ArtifactID.IsValid() {
		return ErrActiveArtifactUnavailable
	}
	if active.ArtifactID != expected {
		return ErrActiveArtifactMismatch
	}
	return nil
}

func mapPrepareResolveError(err error) error {
	switch {
	case errors.Is(err, runtimeupdate.ErrUnsupportedPlatform):
		return ErrUnsupportedStaging
	case errors.Is(err, runtimeupdate.ErrInvalidTargetVersion):
		return ErrInvalidRequest
	default:
		return fmt.Errorf("%w: %v", ErrReleaseMetadataInvalid, err)
	}
}

// beginPrepareExecution performs the second fresh fence and durable admission
// while holding the narrow prepare admission barrier and shared lifecycle gate.
// It returns execute=false for a retained/replayed operation.
func (e *Executor) beginPrepareExecution(
	ctx context.Context,
	intent journal.Intent,
	observer activeArtifactRefresher,
	expected artifact.ID,
) (operation journal.Operation, execute bool, err error) {
	e.prepareAdmissionMu.Lock()
	defer e.prepareAdmissionMu.Unlock()
	if e.closed.Load() {
		return journal.Operation{}, false, ErrPersistenceUnavailable
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return journal.Operation{}, false, err
	}
	if e.closed.Load() {
		return journal.Operation{}, false, ErrPersistenceUnavailable
	}
	operation, found, err := e.journal.Resolve(ctx, e.authority, intent)
	if err != nil {
		return journal.Operation{}, false, submissionError(err)
	}
	if found {
		return operation, false, nil
	}
	if e.activeSelectionAmbiguous {
		return journal.Operation{}, false, journal.ErrOperationStateConflict
	}
	if err := refreshExpectedActiveArtifact(observer, e.active, expected); err != nil {
		return journal.Operation{}, false, err
	}
	operation, created, err := e.journal.Begin(ctx, e.authority, intent)
	if err != nil {
		return journal.Operation{}, false, submissionError(err)
	}
	if !created {
		return operation, false, nil
	}
	markCtx, cancelMark := context.WithTimeout(context.Background(), PrepareUpdateTerminalPersistenceTimeout)
	operation, err = e.journal.MarkRunning(markCtx, e.authority.RuntimeIdentity, intent.OperationID)
	cancelMark()
	if err != nil {
		return journal.Operation{}, false, fmt.Errorf("%w: %w", ErrPersistenceUnavailable, err)
	}
	return operation, true, nil
}
