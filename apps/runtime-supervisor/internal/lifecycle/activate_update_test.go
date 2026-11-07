package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/readiness"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/selection"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

const (
	activationPathA = "/runtime/image/cli-proxy-api"
	activationPathB = "/runtime/supervisor/artifacts/cpa/7.3.4/cli-proxy-api"
)

type activationObserver struct {
	events      *[]string
	byPath      map[string]artifact.ID
	observation *artifact.Observation
}

func (o *activationObserver) Refresh() error {
	return errors.New("static refresh must not be used for configured activation")
}

func (o *activationObserver) RefreshFrom(executablePath, _ string) error {
	*o.events = append(*o.events, "artifact:"+executablePath)
	id, ok := o.byPath[executablePath]
	if !ok {
		o.observation = nil
		return artifact.ErrExecutableUnavailable
	}
	o.observation = &artifact.Observation{Engine: artifact.EngineCPA, ArtifactID: id}
	return nil
}

func (o *activationObserver) Observation() *artifact.Observation {
	if o.observation == nil {
		return nil
	}
	copy := *o.observation
	return &copy
}

type activationSelections struct {
	events          *[]string
	candidate       selection.Descriptor
	resolveErr      error
	revalidateCalls int
	revalidateErrAt int
	commitErr       error
	committed       bool
}

func (s *activationSelections) ResolveFinalized(version string) (selection.Descriptor, error) {
	*s.events = append(*s.events, "resolve-stage:"+version)
	if s.resolveErr != nil {
		return selection.Descriptor{}, s.resolveErr
	}
	return s.candidate, nil
}

func (s *activationSelections) Revalidate(descriptor selection.Descriptor) (selection.Descriptor, error) {
	*s.events = append(*s.events, "revalidate:"+descriptor.ExecutablePath)
	s.revalidateCalls++
	if s.revalidateErrAt == s.revalidateCalls {
		return selection.Descriptor{}, runtimeupdate.ErrStageConflict
	}
	return descriptor, nil
}

func (s *activationSelections) Commit(descriptor selection.Descriptor) error {
	*s.events = append(*s.events, "commit:"+descriptor.ExecutablePath)
	if s.commitErr != nil {
		return s.commitErr
	}
	s.committed = true
	return nil
}

type activationReady struct {
	events  *[]string
	process *activationProcess
	states  map[string]readiness.State
	hook    func(string)
}

func (r *activationReady) Observe(context.Context) readiness.State {
	path := r.process.lastSpec.Executable
	*r.events = append(*r.events, "readiness:"+path)
	if r.hook != nil {
		r.hook(path)
	}
	if state, ok := r.states[path]; ok {
		return state
	}
	return readiness.Ready
}

type activationFixture struct {
	*startFixture
	child      *activationProcess
	observer   *activationObserver
	selections *activationSelections
	ready      *activationReady
	prior      selection.Descriptor
	candidate  selection.Descriptor
}

func newActivationFixture(t *testing.T) *activationFixture {
	t.Helper()
	base := newStartFixture(t)
	child := &activationProcess{
		events: &base.events, nextInstance: 1,
		observation: cpaprocess.Observation{State: cpaprocess.StateRunning, InstanceID: 1, PID: 101},
	}
	base.starter.process = child
	prior := selection.Bundled(activationPathA, "/runtime/image/artifact.json")
	prior.ArtifactID = activeArtifactA
	candidate := selection.Descriptor{
		Source: selection.SourceFinalized, Version: "7.3.4", ArtifactID: activeArtifactB,
		ExecutablePath: activationPathB, MetadataPath: "/runtime/supervisor/artifacts/cpa/7.3.4/artifact.json",
	}
	f := &activationFixture{startFixture: base, child: child, prior: prior, candidate: candidate}
	f.observer = &activationObserver{
		events: &f.events,
		byPath: map[string]artifact.ID{activationPathA: activeArtifactA, activationPathB: activeArtifactB},
	}
	f.selections = &activationSelections{events: &f.events, candidate: candidate}
	f.ready = &activationReady{events: &f.events, process: f.child, states: map[string]readiness.State{}}
	f.child.onStart = func(_ context.Context, spec cpaprocess.StartSpec) error {
		return f.observer.RefreshFrom(spec.Executable, "")
	}
	if err := f.starter.EnableActivateUpdate(prior, f.observer, f.selections, f.ready); err != nil {
		t.Fatal(err)
	}
	f.events = nil
	return f
}

func TestEnableActivateUpdateAllowsUnavailableBundledBaselineButRejectsUnavailableSelectedStage(t *testing.T) {
	base := newStartFixture(t)
	observer := &activationObserver{events: &base.events, byPath: map[string]artifact.ID{}}
	selections := &activationSelections{events: &base.events}
	ready := &activationReady{events: &base.events, process: &activationProcess{events: &base.events}}

	if err := base.starter.EnableActivateUpdate(selection.Bundled(activationPathA, ""), observer, selections, ready); err != nil {
		t.Fatalf("unavailable bundled baseline should defer failure to Start/Activate: %v", err)
	}
	selected := selection.Descriptor{
		Source: selection.SourceFinalized, Version: "7.3.4", ArtifactID: activeArtifactB,
		ExecutablePath: activationPathB, MetadataPath: "/runtime/stage/artifact.json",
	}
	if err := base.starter.EnableActivateUpdate(selected, observer, selections, ready); !errors.Is(err, ErrActiveArtifactUnavailable) {
		t.Fatalf("unavailable persisted selection error = %v", err)
	}
}

type activationProcess struct {
	events       *[]string
	observation  cpaprocess.Observation
	nextInstance uint64
	starts       int
	stops        int
	lastSpec     cpaprocess.StartSpec
	specs        []cpaprocess.StartSpec
	onStart      func(context.Context, cpaprocess.StartSpec) error
	onStop       func(context.Context) error
}

func (p *activationProcess) Observe() cpaprocess.Observation {
	*p.events = append(*p.events, "observe")
	return p.observation
}

func (p *activationProcess) Start(ctx context.Context, spec cpaprocess.StartSpec) (cpaprocess.Observation, error) {
	*p.events = append(*p.events, "spawn")
	p.starts++
	p.lastSpec = spec
	p.specs = append(p.specs, spec)
	if p.onStart != nil {
		if err := p.onStart(ctx, spec); err != nil {
			return p.observation, err
		}
	}
	p.nextInstance++
	p.observation = cpaprocess.Observation{State: cpaprocess.StateRunning, InstanceID: p.nextInstance, PID: 100 + int(p.nextInstance)}
	return p.observation, nil
}

func (p *activationProcess) PrepareStop() (cpaprocess.StopTarget, error) {
	*p.events = append(*p.events, "prepare-stop")
	if p.observation.State != cpaprocess.StateRunning {
		return nil, cpaprocess.ErrStateConflict
	}
	return &activationStopTarget{process: p, instanceID: p.observation.InstanceID}, nil
}

func (p *activationProcess) ExitEvents() <-chan cpaprocess.ExitEvent { return nil }

type activationStopTarget struct {
	process    *activationProcess
	instanceID uint64
}

func (t *activationStopTarget) Terminate(ctx context.Context) (cpaprocess.Observation, error) {
	*t.process.events = append(*t.process.events, "terminate")
	if t.process.observation.InstanceID != t.instanceID || t.process.observation.State != cpaprocess.StateRunning {
		return t.process.observation, cpaprocess.ErrStopFailed
	}
	t.process.stops++
	if t.process.onStop != nil {
		if err := t.process.onStop(ctx); err != nil {
			return t.process.observation, err
		}
	}
	t.process.observation = cpaprocess.Observation{State: cpaprocess.StateExited, InstanceID: t.instanceID}
	return t.process.observation, nil
}

func (*activationStopTarget) Release() {}

func activateRequest(id, version string) ActivateUpdateRequest {
	return ActivateUpdateRequest{
		OperationID: id, ExpectedRuntimeIdentity: "runtime-01", ExpectedRuntimeGeneration: 41,
		ExpectedActiveArtifactID: activeArtifactA, TargetVersion: version,
	}
}

func TestActivateUpdateCommitsOnlyAfterExactReadyAndPostReadyRevalidation(t *testing.T) {
	f := newActivationFixture(t)
	result, err := f.starter.ActivateUpdate(t.Context(), activateRequest("activate-1", "7.3.4"))
	if err != nil || result.State != journal.StateSucceeded || result.OperationType != "activate_update" {
		t.Fatalf("ActivateUpdate() = %+v, %v", result, err)
	}
	if !f.selections.committed || f.starter.currentDescriptorLocked() != f.candidate ||
		f.child.lastSpec.Executable != activationPathB || f.observer.Observation().ArtifactID != activeArtifactB {
		t.Fatalf("committed activation state: committed=%t active=%+v child=%+v artifact=%+v",
			f.selections.committed, f.starter.currentDescriptorLocked(), f.child.lastSpec, f.observer.Observation())
	}
	if got := f.starter.RecoveryStatus(); got != (RecoveryStatus{State: RecoveryStateArmed, AttemptsRemaining: 3}) {
		t.Fatalf("B recovery = %+v", got)
	}
	assertOrdered(t, f.events,
		"resolve", "artifact:"+activationPathA, "resolve-stage:7.3.4", "prepare-stop", "begin", "running",
		"terminate", "revalidate:"+activationPathB, "spawn", "artifact:"+activationPathB,
		"readiness:"+activationPathB, "revalidate:"+activationPathB, "commit:"+activationPathB, "complete:succeeded")

	beforeStarts, beforeStops, beforeEvents := f.child.starts, f.child.stops, len(f.events)
	replay, err := f.starter.ActivateUpdate(t.Context(), activateRequest("activate-1", "7.3.4"))
	if err != nil || !reflect.DeepEqual(replay, result) || f.child.starts != beforeStarts || f.child.stops != beforeStops ||
		!reflect.DeepEqual(f.events[beforeEvents:], []string{"resolve"}) {
		t.Fatalf("replay=%+v err=%v starts=%d stops=%d events=%v", replay, err, f.child.starts, f.child.stops, f.events[beforeEvents:])
	}
	conflict := activateRequest("activate-1", "7.3.5")
	if _, err := f.starter.ActivateUpdate(t.Context(), conflict); !errors.Is(err, journal.ErrOperationIDConflict) {
		t.Fatalf("target conflict error = %v", err)
	}
	if _, err := f.starter.Stop(t.Context(), stopRequest("stop-selected-b")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.starter.Start(t.Context(), startRequest("start-selected-b")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.starter.Restart(t.Context(), restartRequest("restart-selected-b")); err != nil {
		t.Fatal(err)
	}
	if len(f.child.specs) != 3 || f.child.specs[1].Executable != activationPathB || f.child.specs[2].Executable != activationPathB {
		t.Fatalf("ordinary selected spawns = %+v", f.child.specs)
	}
}

func TestActivateUpdateCandidateFailureRollsBackExactPriorAndArmsPriorRecovery(t *testing.T) {
	for name, configure := range map[string]func(*activationFixture){
		"spawn": func(f *activationFixture) {
			f.child.onStart = func(_ context.Context, spec cpaprocess.StartSpec) error {
				if spec.Executable == activationPathB {
					return cpaprocess.ErrSpawnFailed
				}
				return f.observer.RefreshFrom(spec.Executable, "")
			}
		},
		"unready": func(f *activationFixture) {
			f.ready.states[activationPathB] = readiness.Offline
		},
		"post-ready tamper": func(f *activationFixture) {
			f.selections.revalidateErrAt = 2
		},
		"pre-publish selection failure": func(f *activationFixture) {
			f.selections.commitErr = selection.ErrSelectionNotPublished
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newActivationFixture(t)
			configure(f)
			result, err := f.starter.ActivateUpdate(t.Context(), activateRequest("activate-1", "7.3.4"))
			if err != nil || result.State != journal.StateFailed || result.FailureCode == "" {
				t.Fatalf("ActivateUpdate() = %+v, %v", result, err)
			}
			if f.selections.committed || f.starter.currentDescriptorLocked() != f.prior ||
				f.child.observation.State != cpaprocess.StateRunning || f.child.lastSpec.Executable != activationPathA ||
				f.observer.Observation().ArtifactID != activeArtifactA {
				t.Fatalf("rollback state: committed=%t active=%+v child=%+v spec=%+v artifact=%+v",
					f.selections.committed, f.starter.currentDescriptorLocked(), f.child.observation, f.child.lastSpec, f.observer.Observation())
			}
			if got := f.starter.RecoveryStatus(); got != (RecoveryStatus{State: RecoveryStateArmed, AttemptsRemaining: 3}) {
				t.Fatalf("A recovery = %+v", got)
			}
		})
	}
}

func TestActivateUpdateAmbiguityAndPointOfNoReturnNeverBlindRollback(t *testing.T) {
	t.Run("selection publication ambiguous", func(t *testing.T) {
		f := newActivationFixture(t)
		f.selections.commitErr = selection.ErrSelectionPublicationAmbiguous
		result, err := f.starter.ActivateUpdate(t.Context(), activateRequest("activate-1", "7.3.4"))
		if err != nil || result.State != journal.StateFailed || result.FailureCode != "active_selection_persistence_failed" {
			t.Fatalf("ActivateUpdate() = %+v, %v", result, err)
		}
		if f.child.lastSpec.Executable != activationPathB || f.child.starts != 1 || f.child.stops != 1 ||
			f.starter.RecoveryStatus().State != RecoveryStateManualIntervention {
			t.Fatalf("ambiguous selection rolled back or armed recovery: child=%+v starts=%d stops=%d recovery=%+v",
				f.child.lastSpec, f.child.starts, f.child.stops, f.starter.RecoveryStatus())
		}
		if _, err := f.starter.Restart(t.Context(), restartRequest("restart-after-ambiguous-selection")); !errors.Is(err, journal.ErrOperationStateConflict) {
			t.Fatalf("Restart after ambiguous selection error = %v", err)
		}
		f.child.observation.State = cpaprocess.StateExited
		if _, err := f.starter.Start(t.Context(), startRequest("start-after-ambiguous-selection")); !errors.Is(err, journal.ErrOperationStateConflict) {
			t.Fatalf("Start after ambiguous selection error = %v", err)
		}
		if f.child.starts != 1 || f.child.stops != 1 {
			t.Fatalf("ambiguous selection allowed another lifecycle side effect: starts=%d stops=%d", f.child.starts, f.child.stops)
		}
	})

	t.Run("terminal persistence after commit", func(t *testing.T) {
		f := newActivationFixture(t)
		f.journal.failAt = "complete"
		_, err := f.starter.ActivateUpdate(t.Context(), activateRequest("activate-1", "7.3.4"))
		if !errors.Is(err, ErrExecutionFailed) || !f.selections.committed ||
			f.starter.currentDescriptorLocked() != f.candidate || f.child.lastSpec.Executable != activationPathB ||
			f.child.starts != 1 || f.child.stops != 1 || f.starter.RecoveryStatus().State != RecoveryStateManualIntervention {
			t.Fatalf("post-commit failure = %v committed=%t active=%+v child=%+v starts=%d stops=%d recovery=%+v",
				err, f.selections.committed, f.starter.currentDescriptorLocked(), f.child.lastSpec,
				f.child.starts, f.child.stops, f.starter.RecoveryStatus())
		}
	})
}

func TestActivateUpdateRejectsFencesAndTargetBeforeIntentOrStop(t *testing.T) {
	for name, test := range map[string]struct {
		configure func(*activationFixture, *ActivateUpdateRequest)
		want      error
	}{
		"stale identity":   {func(_ *activationFixture, r *ActivateUpdateRequest) { r.ExpectedRuntimeIdentity = "other" }, journal.ErrRuntimeIdentityMismatch},
		"stale generation": {func(_ *activationFixture, r *ActivateUpdateRequest) { r.ExpectedRuntimeGeneration++ }, journal.ErrStaleRuntimeGeneration},
		"active mismatch":  {func(_ *activationFixture, r *ActivateUpdateRequest) { r.ExpectedActiveArtifactID = activeArtifactB }, ErrActiveArtifactMismatch},
		"missing target": {func(f *activationFixture, _ *ActivateUpdateRequest) {
			f.selections.resolveErr = runtimeupdate.ErrStageUnavailable
		}, ErrTargetStageUnavailable},
		"corrupt target": {func(f *activationFixture, _ *ActivateUpdateRequest) {
			f.selections.resolveErr = runtimeupdate.ErrStageConflict
		}, ErrTargetStageCorrupt},
	} {
		t.Run(name, func(t *testing.T) {
			f := newActivationFixture(t)
			request := activateRequest("activate-1", "7.3.4")
			test.configure(f, &request)
			_, err := f.starter.ActivateUpdate(t.Context(), request)
			if !errors.Is(err, test.want) || f.child.starts != 0 || f.child.stops != 0 ||
				containsEvent(f.events, "begin") || containsEvent(f.events, "prepare-stop") {
				t.Fatalf("error=%v starts=%d stops=%d events=%v", err, f.child.starts, f.child.stops, f.events)
			}
		})
	}
}

func TestActivateUpdateUnknownCandidateOwnershipNeverSpawnsRollbackChild(t *testing.T) {
	f := newActivationFixture(t)
	f.ready.hook = func(path string) {
		if path == activationPathB {
			f.child.observation.State = cpaprocess.StateUnknown
		}
	}
	f.ready.states[activationPathB] = readiness.Unknown
	result, err := f.starter.ActivateUpdate(t.Context(), activateRequest("activate-1", "7.3.4"))
	if err != nil || result.State != journal.StateFailed || result.FailureCode != "activation_rollback_failed" ||
		f.child.starts != 1 || f.starter.RecoveryStatus().State != RecoveryStateManualIntervention {
		t.Fatalf("unknown ownership result=%+v err=%v starts=%d recovery=%+v", result, err, f.child.starts, f.starter.RecoveryStatus())
	}
}

func TestActivateUpdateQueueWaitConsumesTotalBudget(t *testing.T) {
	f := newActivationFixture(t)
	previous := activationExecutionTimeout
	activationExecutionTimeout = 20 * time.Millisecond
	t.Cleanup(func() { activationExecutionTimeout = previous })
	f.starter.mu.Lock()
	result := make(chan error, 1)
	go func() {
		_, err := f.starter.ActivateUpdate(t.Context(), activateRequest("activate-1", "7.3.4"))
		result <- err
	}()
	time.Sleep(40 * time.Millisecond)
	f.starter.mu.Unlock()
	if err := <-result; !errors.Is(err, context.DeadlineExceeded) || containsEvent(f.events, "resolve") {
		t.Fatalf("queued activation error=%v events=%v", err, f.events)
	}
}

func TestActivateUpdatePreservesStagedDestinationAcrossActivationAndRestart(t *testing.T) {
	f := newActivationFixture(t)
	dir := t.TempDir()
	bytesA := []byte("bundled-cpa-binary-a")
	sumA := sha256.Sum256(bytesA)
	idA := artifact.ID("sha256:" + hex.EncodeToString(sumA[:]))
	pathA := filepath.Join(dir, "cpa-a")
	if err := os.WriteFile(pathA, bytesA, 0o700); err != nil {
		t.Fatal(err)
	}
	bytesB := []byte("staged-cpa-binary-b")
	sumB := sha256.Sum256(bytesB)
	idB := artifact.ID("sha256:" + hex.EncodeToString(sumB[:]))
	pathB := filepath.Join(dir, "cpa-b")
	if err := os.WriteFile(pathB, bytesB, 0o700); err != nil {
		t.Fatal(err)
	}

	f.prior = selection.Bundled(pathA, "")
	f.prior.ArtifactID = idA
	f.candidate = selection.Descriptor{
		Source:         selection.SourceFinalized,
		Version:        "8.0.59",
		ArtifactID:     idB,
		ExecutablePath: pathB,
		MetadataPath:   filepath.Join(dir, "artifact.json"),
	}
	f.observer.byPath = map[string]artifact.ID{pathA: idA, pathB: idB}
	f.selections.candidate = f.candidate
	if err := f.starter.EnableActivateUpdate(f.prior, f.observer, f.selections, f.ready); err != nil {
		t.Fatal(err)
	}
	destRoot := filepath.Join(dir, "destination")
	if err := f.starter.EnableDestination(destRoot, filepath.Join(dir, "gateway", "auth"), "127.0.0.1:8317", nil); err != nil {
		t.Fatal(err)
	}
	f.child.observation = cpaprocess.Observation{State: cpaprocess.StateNotStarted}
	destReq := DestinationRequest{
		OperationID:               "fresh-op:managed-destination",
		ExpectedRuntimeIdentity:   "runtime-01",
		ExpectedRuntimeGeneration: 41,
		ExpectedActiveArtifactID:  idA,
		SourceIdentity:            "managed:sha256:" + strings.Repeat("a", 64),
		SourceClass:               SourceClassNewManaged,
		APIKeys:                   []string{initialClientKeyFixture()},
		ManagementKey:             strings.Repeat("c", 64),
	}
	if _, err := f.starter.StageDestination(t.Context(), destReq); err != nil {
		t.Fatalf("StageDestination() error = %v", err)
	}
	if _, err := f.starter.Start(t.Context(), StartRequest{
		OperationID:               "fresh-op:managed-destination:start",
		ExpectedRuntimeIdentity:   "runtime-01",
		ExpectedRuntimeGeneration: 41,
		ExpectedActiveArtifactID:  idA,
	}); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	wantArgs := []string{"-config", filepath.Join(destRoot, "config.yaml")}

	// 1. Candidate B start failure rolls back to A and restores stage.json to idA.
	f.child.onStart = func(_ context.Context, spec cpaprocess.StartSpec) error {
		if spec.Executable == pathB {
			return errors.New("candidate start failed")
		}
		return f.observer.RefreshFrom(spec.Executable, "")
	}
	failedResult, err := f.starter.ActivateUpdate(t.Context(), ActivateUpdateRequest{
		OperationID:               "activate-8.0.59-rollback",
		ExpectedRuntimeIdentity:   "runtime-01",
		ExpectedRuntimeGeneration: 41,
		ExpectedActiveArtifactID:  idA,
		TargetVersion:             "8.0.59",
	})
	if err != nil || failedResult.State != journal.StateFailed || failedResult.FailureCode != "activation_start_failed" {
		t.Fatalf("rollback ActivateUpdate() = %+v, %v; want failed activation_start_failed", failedResult, err)
	}
	if f.child.lastSpec.Executable != pathA || !reflect.DeepEqual(f.child.lastSpec.Args, wantArgs) {
		t.Fatalf("rolled-back child spec = %+v; want executable %q args %v", f.child.lastSpec, pathA, wantArgs)
	}
	if staged, loadErr := f.starter.destination.load(); loadErr != nil || staged == nil || staged.ExpectedActiveArtifactID != idA {
		t.Fatalf("stage.json after rollback = %+v, %v; want ExpectedActiveArtifactID %q", staged, loadErr, idA)
	}

	// 2. Retry ActivateUpdate succeeds, spawns B with -config, and persists idB in stage.json.
	f.child.onStart = func(_ context.Context, spec cpaprocess.StartSpec) error {
		return f.observer.RefreshFrom(spec.Executable, "")
	}
	result, err := f.starter.ActivateUpdate(t.Context(), ActivateUpdateRequest{
		OperationID:               "activate-8.0.59",
		ExpectedRuntimeIdentity:   "runtime-01",
		ExpectedRuntimeGeneration: 41,
		ExpectedActiveArtifactID:  idA,
		TargetVersion:             "8.0.59",
	})
	if err != nil || result.State != journal.StateSucceeded {
		t.Fatalf("ActivateUpdate() = %+v, %v; want succeeded", result, err)
	}
	if f.child.lastSpec.Executable != pathB || !reflect.DeepEqual(f.child.lastSpec.Args, wantArgs) {
		t.Fatalf("activated child spec = %+v; want executable %q args %v", f.child.lastSpec, pathB, wantArgs)
	}
	if staged, loadErr := f.starter.destination.load(); loadErr != nil || staged == nil || staged.ExpectedActiveArtifactID != idB {
		t.Fatalf("stage.json after activation = %+v, %v; want ExpectedActiveArtifactID %q", staged, loadErr, idB)
	}

	// 3. Restart after activation preserves B and -config.
	if _, err := f.starter.Restart(t.Context(), RestartRequest{
		OperationID:               "restart-after-activate",
		ExpectedRuntimeIdentity:   "runtime-01",
		ExpectedRuntimeGeneration: 41,
	}); err != nil {
		t.Fatalf("Restart() after activation error = %v", err)
	}
	if f.child.lastSpec.Executable != pathB || !reflect.DeepEqual(f.child.lastSpec.Args, wantArgs) {
		t.Fatalf("restarted child spec = %+v; want executable %q args %v", f.child.lastSpec, pathB, wantArgs)
	}

	// 4. Automatic recovery after unexpected exit preserves B and -config.
	crashedInstance := f.child.observation.InstanceID
	f.child.observation = cpaprocess.Observation{
		State:      cpaprocess.StateExited,
		InstanceID: crashedInstance,
	}
	f.starter.recoveryMu.Lock()
	epoch := f.starter.recoveryEpoch
	f.starter.recoveryState = RecoveryStateRecovering
	f.starter.recoveryMu.Unlock()
	f.starter.executeAutomaticRecovery(epoch, crashedInstance)
	if f.child.lastSpec.Executable != pathB || !reflect.DeepEqual(f.child.lastSpec.Args, wantArgs) {
		t.Fatalf("recovered child spec = %+v; want executable %q args %v", f.child.lastSpec, pathB, wantArgs)
	}

	// 5. Supervisor reconstruction preserves B and -config on Start.
	reconstructed, err := NewExecutor(f.starter.authority, f.journal, f.child, pathA)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reconstructed.CloseAdmission() })
	if err := reconstructed.EnableActivateUpdate(f.candidate, f.observer, f.selections, f.ready); err != nil {
		t.Fatal(err)
	}
	if err := reconstructed.EnableDestination(destRoot, filepath.Join(dir, "gateway", "auth"), "127.0.0.1:8317", nil); err != nil {
		t.Fatal(err)
	}
	f.child.observation = cpaprocess.Observation{State: cpaprocess.StateNotStarted}
	if _, err := reconstructed.Start(t.Context(), StartRequest{
		OperationID:               "start-after-reconstruct",
		ExpectedRuntimeIdentity:   "runtime-01",
		ExpectedRuntimeGeneration: 41,
		ExpectedActiveArtifactID:  idB,
	}); err != nil {
		t.Fatalf("reconstructed Start() error = %v", err)
	}
	if f.child.lastSpec.Executable != pathB || !reflect.DeepEqual(f.child.lastSpec.Args, wantArgs) {
		t.Fatalf("reconstructed child spec = %+v; want executable %q args %v", f.child.lastSpec, pathB, wantArgs)
	}
	// 6. Tampering with activated binary on disk is rejected with ErrActiveArtifactMismatch.
	if err := os.WriteFile(pathB, []byte("tampered-staged-cpa-binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := reconstructed.startProcess(t.Context(), pathB); !errors.Is(err, ErrActiveArtifactMismatch) {
		t.Fatalf("startProcess() with tampered activated binary = %v; want %v", err, ErrActiveArtifactMismatch)
	}
}

func assertOrdered(t *testing.T, events []string, expected ...string) {
	t.Helper()
	index := 0
	for _, event := range events {
		if index < len(expected) && event == expected[index] {
			index++
		}
	}
	if index != len(expected) {
		t.Fatalf("events %v do not contain ordered sequence %v (matched %d)", events, expected, index)
	}
}
