package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/selection"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
	"io"
	"net/http"
	"strings"
	"time"
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

type updateOptionsPreparer interface {
	StageWithOptions(context.Context, runtimeupdate.Release, runtimeupdate.StageOptions) (runtimeupdate.Metadata, error)
}

// PrepareProgress captures transient staging progress for an in-flight prepare_update.
type PrepareProgress struct {
	Phase         string
	ReceivedBytes int64
	TotalBytes    int64
}

// PrepareUpdateRequest contains only exact target policy and freshness fences.
// Release authority and all filesystem paths remain Supervisor-owned.
type PrepareUpdateRequest struct {
	OperationID               string
	ExpectedRuntimeIdentity   string
	ExpectedRuntimeGeneration uint64
	ExpectedActiveArtifactID  artifact.ID
	TargetVersion             string
	SourceMode                string
	CDNPrefix                 string
}

func (r PrepareUpdateRequest) Validate() error {
	if err := validateRequest(r.OperationID, r.ExpectedRuntimeIdentity, r.ExpectedRuntimeGeneration); err != nil ||
		!r.ExpectedActiveArtifactID.IsValid() || runtimeupdate.ValidateVersion(r.TargetVersion) != nil {
		return ErrInvalidRequest
	}
	switch r.SourceMode {
	case "", "github":
		if strings.TrimSpace(r.CDNPrefix) != "" {
			return ErrInvalidRequest
		}
	case "cdn":
		if _, err := runtimeupdate.ValidateCDNPrefix(r.CDNPrefix); err != nil {
			return ErrInvalidRequest
		}
	default:
		return ErrInvalidRequest
	}
	return nil
}

// PrepareUpdateUploadRequest submits an uploaded .tar.gz release stream for an
// official TargetVersion while enforcing exact release metadata verification.
type PrepareUpdateUploadRequest struct {
	OperationID               string
	ExpectedRuntimeIdentity   string
	ExpectedRuntimeGeneration uint64
	ExpectedActiveArtifactID  artifact.ID
	TargetVersion             string
	Archive                   io.Reader
	ContentLength             int64 // Negative denotes an upload of unknown size.
}

func (r PrepareUpdateUploadRequest) Validate() error {
	if r.ContentLength > runtimeupdate.MaxAcquisitionArchiveBytes {
		return runtimeupdate.ErrArchiveTooLarge
	}
	if r.Archive == nil || r.Archive == http.NoBody || r.ContentLength == 0 || r.ContentLength < -1 {
		return ErrInvalidRequest
	}
	return PrepareUpdateRequest{
		OperationID:               r.OperationID,
		ExpectedRuntimeIdentity:   r.ExpectedRuntimeIdentity,
		ExpectedRuntimeGeneration: r.ExpectedRuntimeGeneration,
		ExpectedActiveArtifactID:  r.ExpectedActiveArtifactID,
		TargetVersion:             r.TargetVersion,
	}.Validate()
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

// ObservePrepareProgress returns transient phase/byte progress for an active prepare_update.
func (e *Executor) ObservePrepareProgress(operationID string) (PrepareProgress, bool) {
	if e == nil || operationID == "" {
		return PrepareProgress{}, false
	}
	e.prepareProgressMu.Lock()
	defer e.prepareProgressMu.Unlock()
	if e.prepareProgress == nil {
		return PrepareProgress{}, false
	}
	progress, ok := e.prepareProgress[operationID]
	return progress, ok
}

func (e *Executor) setPrepareProgress(operationID string, progress PrepareProgress) {
	e.prepareProgressMu.Lock()
	defer e.prepareProgressMu.Unlock()
	if e.prepareProgress == nil {
		e.prepareProgress = make(map[string]PrepareProgress)
	}
	e.prepareProgress[operationID] = progress
}

func (e *Executor) clearPrepareProgress(operationID string) {
	e.prepareProgressMu.Lock()
	defer e.prepareProgressMu.Unlock()
	delete(e.prepareProgress, operationID)
}

func (e *Executor) PrepareUpdate(ctx context.Context, request PrepareUpdateRequest) (journal.Operation, error) {
	if err := request.Validate(); err != nil {
		return journal.Operation{}, err
	}
	return e.executePrepare(ctx, request, runtimeupdate.StageOptions{
		SourceMode: request.SourceMode,
		CDNPrefix:  request.CDNPrefix,
	})
}

func (e *Executor) PrepareUpdateUpload(ctx context.Context, request PrepareUpdateUploadRequest) (journal.Operation, error) {
	if err := request.Validate(); err != nil {
		return journal.Operation{}, err
	}
	return e.executePrepare(ctx, PrepareUpdateRequest{
		OperationID:               request.OperationID,
		ExpectedRuntimeIdentity:   request.ExpectedRuntimeIdentity,
		ExpectedRuntimeGeneration: request.ExpectedRuntimeGeneration,
		ExpectedActiveArtifactID:  request.ExpectedActiveArtifactID,
		TargetVersion:             request.TargetVersion,
	}, runtimeupdate.StageOptions{
		SourceMode:   "upload",
		UploadReader: request.Archive,
		UploadLength: request.ContentLength,
	})
}

func (e *Executor) executePrepare(
	ctx context.Context,
	request PrepareUpdateRequest,
	stageOpts runtimeupdate.StageOptions,
) (journal.Operation, error) {
	upload := stageOpts.SourceMode == "upload"
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
	optsPreparer, _ := preparer.(updateOptionsPreparer)
	if upload && optsPreparer == nil {
		e.mu.Unlock()
		return journal.Operation{}, ErrUnsupportedStaging
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
	e.setPrepareProgress(request.OperationID, PrepareProgress{Phase: "resolving"})
	defer e.clearPrepareProgress(request.OperationID)

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

	phase := "verifying"
	if !upload && optsPreparer == nil {
		phase = "downloading"
	}
	e.setPrepareProgress(request.OperationID, PrepareProgress{
		Phase:      phase,
		TotalBytes: release.ArchiveSize,
	})
	stageOpts.Progress = func(receivedBytes, totalBytes int64) {
		e.prepareProgressMu.Lock()
		defer e.prepareProgressMu.Unlock()
		progress, ok := e.prepareProgress[request.OperationID]
		if !ok {
			return
		}
		progress.ReceivedBytes = receivedBytes
		if totalBytes > 0 {
			progress.TotalBytes = totalBytes
		}
		e.prepareProgress[request.OperationID] = progress
	}
	stageOpts.OnPhase = func(phase string) {
		e.prepareProgressMu.Lock()
		defer e.prepareProgressMu.Unlock()
		progress, ok := e.prepareProgress[request.OperationID]
		if !ok {
			return
		}
		progress.Phase = phase
		e.prepareProgress[request.OperationID] = progress
	}

	// Durable running evidence transfers execution to the Supervisor. Only
	// reading the uploaded HTTP stream follows the caller's request context;
	// post-EOF extraction, verification, fsync, and terminal persistence use
	// the bounded Supervisor execution context.
	stageCtx, cancelStage := context.WithDeadline(context.Background(), operationDeadline)
	var cancelUpload context.CancelFunc
	if upload {
		stageOpts.UploadContext, cancelUpload = context.WithDeadline(ctx, operationDeadline)
	}
	var stageErr error
	if optsPreparer != nil {
		_, stageErr = optsPreparer.StageWithOptions(stageCtx, release, stageOpts)
	} else {
		_, stageErr = preparer.Stage(stageCtx, release)
	}
	if cancelUpload != nil {
		cancelUpload()
	}
	cancelStage()
	state, failureCode := journal.StateSucceeded, ""
	if stageErr != nil {
		state, failureCode = journal.StateFailed, mapPrepareStageFailureCode(stageErr)
	}
	completeCtx, cancelComplete := context.WithTimeout(context.Background(), PrepareUpdateTerminalPersistenceTimeout)
	result, err := e.journal.Complete(completeCtx, e.authority.RuntimeIdentity, intent.OperationID, state, failureCode)
	cancelComplete()
	if err != nil {
		return operation, fmt.Errorf("%w: record prepare-update result: %w", ErrExecutionFailed, err)
	}
	return result, nil
}

func mapPrepareStageFailureCode(err error) string {
	switch {
	case errors.Is(err, runtimeupdate.ErrArchiveTooLarge):
		return "archive_too_large"
	case errors.Is(err, runtimeupdate.ErrArchiveInvalid), errors.Is(err, runtimeupdate.ErrArtifactMismatch):
		return "release_asset_invalid"
	case errors.Is(err, runtimeupdate.ErrDownloadFailed), errors.Is(err, runtimeupdate.ErrSourceUnreachable):
		return "download_failed"
	default:
		return "staging_failed"
	}
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
