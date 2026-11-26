package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/selection"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

// AdoptBaseline changes the selection back to the newer, verified bundled
// artifact. The acquisition mutex precedes the lifecycle mutex, as in acquire.
func (e *Executor) AdoptBaseline(ctx context.Context, expectedActiveArtifactID artifact.ID) (ArtifactStatusResponse, error) {
	if !expectedActiveArtifactID.IsValid() {
		return ArtifactStatusResponse{}, &ArtifactError{Code: ArtifactCodeInvalidRequest, Err: ErrInvalidRequest}
	}
	state, err := e.acquisitionState()
	if err != nil {
		return ArtifactStatusResponse{}, err
	}
	state.mu.Lock()
	if state.inFlight {
		state.mu.Unlock()
		return ArtifactStatusResponse{}, &ArtifactError{Code: ArtifactCodeAcquisitionInProgress, Err: ErrAcquisitionInProgress}
	}
	if !runtimeupdate.SupportedPlatform(state.goos, state.goarch) {
		state.mu.Unlock()
		return ArtifactStatusResponse{}, &ArtifactError{Code: ArtifactCodeUnsupportedPlatform, Err: runtimeupdate.ErrUnsupportedPlatform}
	}
	e.mu.Lock()
	state.mu.Unlock()
	defer e.mu.Unlock()
	if e.closed.Load() {
		return ArtifactStatusResponse{}, &ArtifactError{Code: ArtifactCodeInstallFailed, Err: ErrPersistenceUnavailable}
	}
	if e.activeSelectionAmbiguous {
		return ArtifactStatusResponse{}, &ArtifactError{Code: ArtifactCodeRecoveryRequired, Err: ErrRecoveryRequired}
	}
	store, ok := e.selections.(interface {
		Clear(selection.Descriptor) (selection.Descriptor, error)
	})
	if !ok || e.artifact == nil {
		return ArtifactStatusResponse{}, &ArtifactError{Code: ArtifactCodeUnsupportedPlatform, Err: runtimeupdate.ErrUnsupportedPlatform}
	}
	pinned, err := loadPinnedArtifactManifest(state.manifestPath)
	if err != nil {
		return ArtifactStatusResponse{}, &ArtifactError{Code: ArtifactCodeInstallFailed, Err: err}
	}
	prior := e.currentDescriptorLocked()
	observed := e.artifact.Observation()
	if prior.ArtifactID != expectedActiveArtifactID || observed == nil || observed.ArtifactID != prior.ArtifactID ||
		!newerPinnedBaseline(prior, pinned) || !hasBaselineAcquisitionProvenance(prior, e.selections) ||
		e.hasActiveUpdateOperationLocked() {
		return ArtifactStatusResponse{}, &ArtifactError{Code: ArtifactCodeArtifactConflict, Err: runtimeupdate.ErrArtifactConflict}
	}

	candidate := state.bundled
	candidate.ArtifactID = pinned.ArtifactID
	candidate.Version = pinned.Version
	verify := func() error { return verifyBaselineExecutable(candidate, pinned) }
	if err := verify(); err != nil {
		return ArtifactStatusResponse{}, &ArtifactError{Code: ArtifactCodeArtifactConflict, Err: err}
	}
	if err := e.switchBaselineLocked(ctx, state, prior, candidate, verify, func() error {
		if err := e.preserveBaselineDestinationOriginLocked(candidate.ArtifactID); err != nil {
			return err
		}
		_, err := store.Clear(candidate)
		return err
	}); err != nil {
		return ArtifactStatusResponse{}, err
	}
	return ArtifactStatusResponse{
		SchemaVersion:    ArtifactSchemaVersion,
		Pinned:           pinned,
		Active:           &ArtifactActiveStatus{ArtifactID: pinned.ArtifactID, Origin: ArtifactOriginBundled},
		BundledAvailable: true,
	}, nil
}

func newerPinnedBaseline(prior selection.Descriptor, pinned ArtifactPinnedStatus) bool {
	return prior.IsFinalizedStage() && prior.ArtifactID != pinned.ArtifactID && compareCPAVersion(pinned.Version, prior.Version) > 0
}

// compareCPAVersion compares canonical SemVer releases without truncating
// numeric identifiers or using build metadata in ordering.
func compareCPAVersion(left, right string) int {
	if runtimeupdate.ValidateVersion(left) != nil || runtimeupdate.ValidateVersion(right) != nil {
		return 0
	}
	left, _, _ = strings.Cut(left, "+")
	right, _, _ = strings.Cut(right, "+")
	leftCore, leftPre, hasLeftPre := strings.Cut(left, "-")
	rightCore, rightPre, hasRightPre := strings.Cut(right, "-")
	lp, rp := strings.Split(leftCore, "."), strings.Split(rightCore, ".")
	for i, l := range lp {
		if cmp := compareVersionNumerals(l, rp[i]); cmp != 0 {
			return cmp
		}
	}
	if !hasLeftPre || !hasRightPre {
		switch {
		case hasLeftPre:
			return -1
		case hasRightPre:
			return 1
		default:
			return 0
		}
	}
	lp, rp = strings.Split(leftPre, "."), strings.Split(rightPre, ".")
	for i := 0; i < min(len(lp), len(rp)); i++ {
		l, r := lp[i], rp[i]
		ln, rn := strings.Trim(l, "0123456789") == "", strings.Trim(r, "0123456789") == ""
		var cmp int
		switch {
		case ln && rn:
			cmp = compareVersionNumerals(l, r)
		case ln:
			cmp = -1
		case rn:
			cmp = 1
		default:
			cmp = strings.Compare(l, r)
		}
		if cmp != 0 {
			return cmp
		}
	}
	return len(lp) - len(rp)
}

func compareVersionNumerals(left, right string) int {
	if len(left) != len(right) {
		return len(left) - len(right)
	}
	return strings.Compare(left, right)
}

func verifyBaselineExecutable(candidate selection.Descriptor, pinned ArtifactPinnedStatus) error {
	info, err := os.Lstat(candidate.ExecutablePath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return artifact.ErrExecutableUnavailable
	}
	observer := artifact.NewObserver(candidate.ExecutablePath, candidate.MetadataPath)
	if err := observer.Refresh(); err != nil {
		return err
	}
	if observation := observer.Observation(); observation == nil || observation.ArtifactID != pinned.ArtifactID || observation.Version != pinned.Version {
		return runtimeupdate.ErrArtifactConflict
	}
	return nil
}

// switchBaselineLocked owns A -> B until selection publication. Neither the
// persistent selection nor the destination fence is changed by an unready B.
func (e *Executor) switchBaselineLocked(ctx context.Context, state *artifactAcquisitionState, prior, candidate selection.Descriptor, verify, publish func() error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return &ArtifactError{Code: ArtifactCodeInstallFailed, Err: err}
	}
	if err := e.preverifyDestinationSyncLocked(candidate.ArtifactID); err != nil {
		return &ArtifactError{Code: ArtifactCodeInstallFailed, Err: err}
	}
	old := e.process.Observe()
	var started cpaprocess.Observation
	fail := func(cause error) error {
		if old.State == cpaprocess.StateRunning {
			if err := e.restoreBaselineProcessLocked(prior, old.InstanceID, started.InstanceID); err != nil {
				e.disableRecovery(RecoveryStateManualIntervention)
				cause = errors.Join(cause, fmt.Errorf("restore prior CPA: %w", err))
			}
		}
		return &ArtifactError{Code: ArtifactCodeInstallFailed, Err: cause}
	}
	switch old.State {
	case cpaprocess.StateNotStarted, cpaprocess.StateExited:
		// Publishing does not start a stopped CPA.
	case cpaprocess.StateRunning:
		if e.readiness == nil || old.InstanceID == 0 {
			return &ArtifactError{Code: ArtifactCodeInstallFailed, Err: ErrPersistenceUnavailable}
		}
		if _, err := e.selections.Revalidate(prior); err != nil {
			return &ArtifactError{Code: ArtifactCodeInstallFailed, Err: err}
		}
		target, err := e.process.PrepareStop()
		if err != nil {
			return &ArtifactError{Code: ArtifactCodeInstallFailed, Err: err}
		}
		e.disableRecovery(RecoveryStateInactive)
		execCtx, cancel := context.WithTimeout(ctx, activationExecutionTimeout)
		defer cancel()
		_, stopErr := target.Terminate(execCtx)
		target.Release()
		if stopErr != nil {
			return fail(stopErr)
		}
		if err := verify(); err != nil {
			return fail(err)
		}
		// startProcess fences against the persisted digest. For a trial child
		// use the same destination config, but the freshly verified candidate
		// digest; do not publish it in stage.json before readiness.
		spec := cpaprocess.StartSpec{Executable: candidate.ExecutablePath}
		if e.destination != nil {
			staged, err := e.destination.load()
			if err != nil {
				return fail(err)
			}
			if staged != nil {
				spec.Args = []string{"-config", filepath.Join(e.destination.root, "config.yaml")}
			}
		}
		trial, err := e.process.Start(execCtx, spec)
		if err != nil {
			return fail(err)
		}
		started = trial
		if err := e.waitExactReady(execCtx, started.InstanceID, activationCandidateReadinessTimeout); err != nil {
			return fail(err)
		}
	default:
		return &ArtifactError{Code: ArtifactCodeArtifactConflict, Err: cpaprocess.ErrStateConflict}
	}
	if err := verify(); err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if started.InstanceID != 0 {
		if observed := e.process.Observe(); observed.State != cpaprocess.StateRunning || observed.InstanceID != started.InstanceID {
			return fail(cpaprocess.ErrStateConflict)
		}
	}
	if err := publish(); err != nil {
		if errors.Is(err, selection.ErrSelectionPublicationAmbiguous) {
			e.markAcquisitionRecoveryRequiredLocked()
			return &ArtifactError{Code: ArtifactCodeRecoveryRequired, Err: errors.Join(ErrRecoveryRequired, err)}
		}
		return fail(err)
	}
	if err := e.finishBaselineSelectionLocked(state, candidate); err != nil {
		return err
	}
	if started.InstanceID != 0 {
		e.armFreshRecovery(started)
	} else {
		e.disableRecovery(RecoveryStateInactive)
	}
	return nil
}

func (e *Executor) restoreBaselineProcessLocked(prior selection.Descriptor, priorInstance, candidateInstance uint64) error {
	ctx, cancel := context.WithTimeout(context.Background(), activationRollbackReserve)
	defer cancel()
	observed := e.process.Observe()
	if candidateInstance != 0 {
		if observed.InstanceID != candidateInstance {
			return errors.New("candidate ownership changed")
		}
		if observed.State == cpaprocess.StateRunning {
			target, err := e.process.PrepareStop()
			if err != nil {
				return err
			}
			_, err = target.Terminate(ctx)
			target.Release()
			if err != nil {
				return err
			}
			observed = e.process.Observe()
		}
	}
	if _, err := e.selections.Revalidate(prior); err != nil {
		return err
	}
	if err := e.syncDestinationExpectedArtifactLocked(prior.ArtifactID); err != nil {
		return err
	}
	switch {
	case observed.State == cpaprocess.StateRunning && observed.InstanceID == priorInstance && candidateInstance == 0:
		// A failed stop can leave the prior child running.
	case observed.State == cpaprocess.StateNotStarted || observed.State == cpaprocess.StateExited:
		var err error
		observed, err = e.startProcess(ctx, prior.ExecutablePath)
		if err != nil {
			return err
		}
	default:
		return cpaprocess.ErrStateConflict
	}
	if err := refreshSelectedDescriptor(e.artifact, &prior); err != nil {
		return err
	}
	if err := e.waitExactReady(ctx, observed.InstanceID, activationRollbackReadinessTimeout); err != nil {
		return err
	}
	e.armFreshRecovery(observed)
	return nil
}

// Persist the original destination request before clearing selection.json so
// even a restart between the clear and fence sync can reconcile the new pin.
func (e *Executor) preserveBaselineDestinationOriginLocked(id artifact.ID) error {
	if e.destination == nil {
		return nil
	}
	staged, err := e.destination.load()
	if err != nil || staged == nil || staged.ExpectedActiveArtifactID == id {
		return err
	}
	origin, err := e.loadOriginStageRequestLocked()
	if err != nil || origin != nil {
		return err
	}
	return e.writeAtomicDestinationJSONLocked(originStageRequestFile, *staged)
}

// A bundled selection with a matching historical destination request can
// outlive the old destination digest. Preserve that request for replay, but
// prepare ordinary spawn operations using the current verified bundled pin.
func (e *Executor) reconcileBundledBaselineDestinationLocked() (bool, error) {
	state, err := e.acquisitionState()
	if err != nil || e.destination == nil || e.currentDescriptorLocked().IsFinalizedStage() {
		return false, nil
	}
	staged, err := e.destination.load()
	if err != nil || staged == nil {
		return false, err
	}
	origin, err := e.loadOriginStageRequestLocked()
	if err != nil {
		return false, err
	}
	if origin != nil && staged.OperationID != origin.OperationID {
		return false, nil
	}
	candidate := e.currentDescriptorLocked()
	pinned, err := loadPinnedArtifactManifest(state.manifestPath)
	if err != nil {
		return false, err
	}
	if candidate.ArtifactID != pinned.ArtifactID || candidate.ExecutablePath != state.bundled.ExecutablePath {
		return false, nil
	}
	if origin == nil && staged.ExpectedActiveArtifactID == pinned.ArtifactID {
		return false, nil
	}
	if err := verifyBaselineExecutable(state.bundled, pinned); err != nil {
		return false, err
	}
	return true, e.syncDestinationExpectedArtifactLocked(pinned.ArtifactID)
}
