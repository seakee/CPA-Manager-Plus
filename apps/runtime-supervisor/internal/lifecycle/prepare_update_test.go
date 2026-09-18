package lifecycle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/readiness"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

const (
	activeArtifactA artifact.ID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	activeArtifactB artifact.ID = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

type prepareArtifactObserver struct {
	events      *[]string
	observation *artifact.Observation
	refresh     func()
	refreshErr  error
}

func (o *prepareArtifactObserver) Refresh() error {
	*o.events = append(*o.events, "refresh-artifact")
	if o.refresh != nil {
		o.refresh()
	}
	return o.refreshErr
}

func (o *prepareArtifactObserver) Observation() *artifact.Observation {
	if o.observation == nil {
		return nil
	}
	copy := *o.observation
	return &copy
}

type prepareUpdateFake struct {
	events       *[]string
	resolveCalls int
	stageCalls   int
	resolveErr   error
	stageErr     error
	resolveHook  func()
	stageHook    func()
	stageEntered chan struct{}
	stageRelease <-chan struct{}
	stageOnce    sync.Once
}

type persistentPrepareUpdate struct {
	store   *runtimeupdate.Store
	release runtimeupdate.Release
	archive []byte
}

func (u *persistentPrepareUpdate) Resolve(_ context.Context, version string) (runtimeupdate.Release, error) {
	if version != u.release.Version {
		return runtimeupdate.Release{}, fmt.Errorf("unexpected version %q", version)
	}
	return u.release, nil
}

func (u *persistentPrepareUpdate) Stage(ctx context.Context, release runtimeupdate.Release) (runtimeupdate.Metadata, error) {
	return u.store.Stage(ctx, release, func(_ context.Context, _ runtimeupdate.Release, destination io.Writer) error {
		_, err := destination.Write(u.archive)
		return err
	})
}

func (u *prepareUpdateFake) Resolve(_ context.Context, version string) (runtimeupdate.Release, error) {
	*u.events = append(*u.events, "resolve-release:"+version)
	u.resolveCalls++
	if u.resolveErr != nil {
		return runtimeupdate.Release{}, u.resolveErr
	}
	if u.resolveHook != nil {
		u.resolveHook()
	}
	return runtimeupdate.Release{
		Version:       version,
		AssetName:     "official",
		AssetURL:      "https://github.com/router-for-me/CLIProxyAPI/releases/download/v" + version + "/official",
		ArchiveDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		ArchiveSize:   1,
	}, nil
}

func (u *prepareUpdateFake) Stage(_ context.Context, release runtimeupdate.Release) (runtimeupdate.Metadata, error) {
	*u.events = append(*u.events, "stage:"+release.Version)
	u.stageCalls++
	if u.stageHook != nil {
		u.stageHook()
	}
	if u.stageEntered != nil {
		u.stageOnce.Do(func() { close(u.stageEntered) })
		<-u.stageRelease
	}
	if u.stageErr != nil {
		return runtimeupdate.Metadata{}, u.stageErr
	}
	return runtimeupdate.Metadata{Version: release.Version, ArtifactID: activeArtifactB}, nil
}

func TestPrepareUpdateOrdersFreshFenceBeforeNetworkAndDurableIntent(t *testing.T) {
	f := newStartFixture(t)
	f.child.observation = cpaprocess.Observation{State: cpaprocess.StateRunning, PID: 123}
	observer := &prepareArtifactObserver{events: &f.events, observation: &artifact.Observation{Engine: artifact.EngineCPA, ArtifactID: activeArtifactA}}
	preparer := &prepareUpdateFake{events: &f.events}
	if err := f.starter.EnablePrepareUpdate(observer, preparer); err != nil {
		t.Fatal(err)
	}
	beforeRecovery := f.starter.RecoveryStatus()
	result, err := f.starter.PrepareUpdate(t.Context(), prepareUpdateRequest("prepare-1", "7.3.3", activeArtifactA))
	if err != nil || result.State != journal.StateSucceeded || result.OperationType != "prepare_update" {
		t.Fatalf("PrepareUpdate() = %+v, %v", result, err)
	}
	wantEvents := []string{
		"resolve", "refresh-artifact", "resolve-release:7.3.3", "resolve", "refresh-artifact", "begin", "running", "stage:7.3.3", "complete:succeeded",
	}
	if !reflect.DeepEqual(f.events, wantEvents) {
		t.Fatalf("events = %v, want %v", f.events, wantEvents)
	}
	if f.child.starts != 0 || f.child.stops != 0 || f.child.observation.PID != 123 ||
		f.starter.authority.RuntimeGeneration != 41 || f.starter.RecoveryStatus() != beforeRecovery {
		t.Fatalf("prepare changed process/generation/recovery: child=%+v generation=%d recovery=%+v",
			f.child, f.starter.authority.RuntimeGeneration, f.starter.RecoveryStatus())
	}
}

func TestPrepareUpdateRefreshesFenceAgainAfterReleaseLookup(t *testing.T) {
	f := newPrepareFixture(t, activeArtifactA)
	f.preparer.resolveHook = func() {
		f.observer.observation.ArtifactID = activeArtifactB
	}
	_, err := f.starter.PrepareUpdate(t.Context(), prepareUpdateRequest("prepare-1", "7.3.3", activeArtifactA))
	if !errors.Is(err, ErrActiveArtifactMismatch) || f.preparer.stageCalls != 0 || containsEvent(f.events, "begin") {
		t.Fatalf("second fresh fence error=%v stage=%d events=%v", err, f.preparer.stageCalls, f.events)
	}
	wantEvents := []string{"resolve", "refresh-artifact", "resolve-release:7.3.3", "resolve", "refresh-artifact"}
	if !reflect.DeepEqual(f.events, wantEvents) {
		t.Fatalf("second fence events = %v, want %v", f.events, wantEvents)
	}
}

func TestPrepareUpdateStageDoesNotBlockAutomaticRecovery(t *testing.T) {
	f := newRecoveryFixture(t)
	var events []string
	stageEntered := make(chan struct{})
	stageRelease := make(chan struct{})
	observer := &prepareArtifactObserver{
		events:      &events,
		observation: &artifact.Observation{Engine: artifact.EngineCPA, ArtifactID: activeArtifactA},
	}
	preparer := &prepareUpdateFake{
		events:       &events,
		stageEntered: stageEntered,
		stageRelease: stageRelease,
	}
	if err := f.executor.EnablePrepareUpdate(observer, preparer); err != nil {
		t.Fatal(err)
	}
	startRecoveryEpoch(t, f, "start")
	beforeEpoch := recoveryEpoch(f.executor)
	prepareResult := make(chan struct {
		operation journal.Operation
		err       error
	}, 1)
	go func() {
		operation, err := f.executor.PrepareUpdate(t.Context(), prepareUpdateRequest("prepare-1", "7.3.3", activeArtifactA))
		prepareResult <- struct {
			operation journal.Operation
			err       error
		}{operation: operation, err: err}
	}()
	select {
	case <-stageEntered:
	case <-time.After(time.Second):
		t.Fatal("prepare stage did not enter its blocked I/O seam")
	}

	f.process.exitCurrent(cpaprocess.StateExited, true)
	waitRecoveryCondition(t, func() bool {
		return f.process.startCount() == 2 && f.executor.RecoveryStatus() == (RecoveryStatus{State: RecoveryStateArmed, AttemptsRemaining: 2})
	})
	select {
	case result := <-prepareResult:
		t.Fatalf("prepare completed while stage was blocked: %+v", result)
	default:
	}
	if got := recoveryEpoch(f.executor); got != beforeEpoch {
		t.Fatalf("prepare/recovery changed epoch during staging: before=%d after=%d", beforeEpoch, got)
	}
	if f.process.stopCount() != 0 || observer.Observation().ArtifactID != activeArtifactA {
		t.Fatalf("prepare altered active runtime authority: stops=%d observation=%+v", f.process.stopCount(), observer.Observation())
	}

	close(stageRelease)
	select {
	case result := <-prepareResult:
		if result.err != nil || result.operation.State != journal.StateSucceeded {
			t.Fatalf("prepare result after stage release = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("prepare did not complete after stage release")
	}
	if got := f.executor.RecoveryStatus(); got != (RecoveryStatus{State: RecoveryStateArmed, AttemptsRemaining: 2}) {
		t.Fatalf("prepare changed recovery budget after completion: %+v", got)
	}
}

func TestPrepareUpdateShutdownDrainsBlockedStageBeforeJournalClose(t *testing.T) {
	f := newRecoveryFixture(t)
	var events []string
	stageEntered := make(chan struct{})
	stageRelease := make(chan struct{})
	observer := &prepareArtifactObserver{
		events:      &events,
		observation: &artifact.Observation{Engine: artifact.EngineCPA, ArtifactID: activeArtifactA},
	}
	preparer := &prepareUpdateFake{events: &events, stageEntered: stageEntered, stageRelease: stageRelease}
	if err := f.executor.EnablePrepareUpdate(observer, preparer); err != nil {
		t.Fatal(err)
	}
	startRecoveryEpoch(t, f, "start")
	prepareResult := make(chan error, 1)
	go func() {
		_, err := f.executor.PrepareUpdate(t.Context(), prepareUpdateRequest("prepare-shutdown", "7.3.3", activeArtifactA))
		prepareResult <- err
	}()
	select {
	case <-stageEntered:
	case <-time.After(time.Second):
		t.Fatal("prepare stage did not enter its blocked I/O seam")
	}
	closeResult := make(chan error, 1)
	go func() { closeResult <- f.executor.Close() }()
	select {
	case err := <-closeResult:
		t.Fatalf("Close returned while prepare stage was blocked: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if f.store.closeCount() != 0 {
		t.Fatal("journal closed before accepted prepare drained")
	}

	close(stageRelease)
	select {
	case err := <-prepareResult:
		if err != nil {
			t.Fatalf("prepare completion during shutdown = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("prepare did not finish after stage release")
	}
	select {
	case err := <-closeResult:
		if err != nil {
			t.Fatalf("Close after prepare drain = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after prepare terminal evidence")
	}
	if f.store.closeCount() != 1 || !f.store.hasCompletion("prepare-shutdown", journal.StateSucceeded, "") {
		t.Fatalf("shutdown journal evidence = close=%d events=%v", f.store.closeCount(), f.store.events)
	}
}

func TestPrepareUpdatePersistsInactiveStageAndKeepsRunningGatewayAuthorityUnchanged(t *testing.T) {
	f := newStartFixture(t)
	f.child.observation = cpaprocess.Observation{State: cpaprocess.StateRunning, PID: 123, InstanceID: 9}
	activePath := filepath.Join(t.TempDir(), "active-cli-proxy-api")
	activeBytes := []byte("active executable A")
	if err := os.WriteFile(activePath, activeBytes, 0o755); err != nil {
		t.Fatal(err)
	}
	observer := artifact.NewObserver(activePath, "")
	_ = observer.Refresh()
	active := observer.Observation()
	if active == nil {
		t.Fatal("active artifact observation is unavailable")
	}
	stagedBytes := []byte("inactive staged executable B")
	archive := prepareUpdateArchive(t, stagedBytes)
	archiveDigest := sha256.Sum256(archive)
	stageRoot := filepath.Join(t.TempDir(), "artifacts", "cpa")
	store, err := runtimeupdate.NewStore(stageRoot)
	if err != nil {
		t.Fatal(err)
	}
	preparer := &persistentPrepareUpdate{
		store: store,
		release: runtimeupdate.Release{
			Version:       "7.3.3",
			AssetName:     "CLIProxyAPI_7.3.3_linux_amd64.tar.gz",
			AssetURL:      "https://github.com/router-for-me/CLIProxyAPI/releases/download/v7.3.3/CLIProxyAPI_7.3.3_linux_amd64.tar.gz",
			ArchiveDigest: fmt.Sprintf("sha256:%x", archiveDigest),
			ArchiveSize:   int64(len(archive)),
		},
		archive: archive,
	}
	if err := f.starter.EnablePrepareUpdate(observer, preparer); err != nil {
		t.Fatal(err)
	}
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodHead || request.URL.Path != "/healthz" {
			t.Errorf("health request = %s %s", request.Method, request.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer health.Close()
	availability, err := readiness.New(f.child, strings.TrimPrefix(health.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	if got := availability.Observe(t.Context()); got != readiness.Ready {
		t.Fatalf("Gateway before prepare = %s", got)
	}
	beforeProcess := f.child.observation
	beforeRecovery := f.starter.RecoveryStatus()
	beforeGeneration := f.starter.authority.RuntimeGeneration
	result, err := f.starter.PrepareUpdate(t.Context(), prepareUpdateRequest("prepare-1", "7.3.3", active.ArtifactID))
	if err != nil || result.State != journal.StateSucceeded {
		t.Fatalf("PrepareUpdate() = %+v, %v", result, err)
	}
	if got := availability.Observe(t.Context()); got != readiness.Ready {
		t.Fatalf("Gateway after prepare = %s", got)
	}
	if f.child.observation != beforeProcess || f.starter.RecoveryStatus() != beforeRecovery ||
		f.starter.authority.RuntimeGeneration != beforeGeneration || f.child.starts != 0 || f.child.stops != 0 {
		t.Fatalf("prepare changed active runtime: process=%+v recovery=%+v generation=%d", f.child.observation, f.starter.RecoveryStatus(), f.starter.authority.RuntimeGeneration)
	}
	gotStaged, err := os.ReadFile(filepath.Join(stageRoot, "7.3.3", "cli-proxy-api"))
	if err != nil || !bytes.Equal(gotStaged, stagedBytes) {
		t.Fatalf("persistent inactive stage = %q, %v", gotStaged, err)
	}
	gotActive, err := os.ReadFile(activePath)
	if err != nil || !bytes.Equal(gotActive, activeBytes) {
		t.Fatalf("prepare changed active executable = %q, %v", gotActive, err)
	}
	if current := observer.Observation(); current == nil || current.ArtifactID != active.ArtifactID {
		t.Fatalf("prepare changed active artifact observation = %+v", current)
	}
}

func TestPrepareUpdateReplayAndTargetConflictAreSideEffectFree(t *testing.T) {
	f := newPrepareFixture(t, activeArtifactA)
	request := prepareUpdateRequest("prepare-1", "7.3.3", activeArtifactA)
	first, err := f.starter.PrepareUpdate(t.Context(), request)
	if err != nil || first.State != journal.StateSucceeded {
		t.Fatalf("first PrepareUpdate() = %+v, %v", first, err)
	}
	eventsAfterFirst := len(f.events)
	replayed, err := f.starter.PrepareUpdate(t.Context(), request)
	if err != nil || replayed.State != journal.StateSucceeded || f.preparer.resolveCalls != 1 || f.preparer.stageCalls != 1 {
		t.Fatalf("replay = %+v resolve=%d stage=%d error=%v", replayed, f.preparer.resolveCalls, f.preparer.stageCalls, err)
	}
	if !reflect.DeepEqual(f.events[eventsAfterFirst:], []string{"resolve"}) {
		t.Fatalf("replay side effects = %v", f.events[eventsAfterFirst:])
	}
	conflict := request
	conflict.TargetVersion = "7.3.4"
	eventsBeforeConflict := len(f.events)
	if _, err := f.starter.PrepareUpdate(t.Context(), conflict); !errors.Is(err, journal.ErrOperationIDConflict) {
		t.Fatalf("target conflict error = %v", err)
	}
	if !reflect.DeepEqual(f.events[eventsBeforeConflict:], []string{"resolve"}) {
		t.Fatalf("target conflict side effects = %v", f.events[eventsBeforeConflict:])
	}
}

func TestPrepareUpdateFencesIdentityGenerationAndFreshArtifactBeforeNetworkOrBegin(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*PrepareUpdateRequest, *prepareFixture)
		want   error
	}{
		{name: "identity", mutate: func(r *PrepareUpdateRequest, _ *prepareFixture) { r.ExpectedRuntimeIdentity = "other" }, want: journal.ErrRuntimeIdentityMismatch},
		{name: "generation", mutate: func(r *PrepareUpdateRequest, _ *prepareFixture) { r.ExpectedRuntimeGeneration = 40 }, want: journal.ErrStaleRuntimeGeneration},
		{name: "artifact unavailable", mutate: func(_ *PrepareUpdateRequest, f *prepareFixture) { f.observer.observation = nil }, want: ErrActiveArtifactUnavailable},
		{name: "artifact refresh unavailable", mutate: func(_ *PrepareUpdateRequest, f *prepareFixture) {
			f.observer.refreshErr = artifact.ErrExecutableUnavailable
		}, want: ErrActiveArtifactUnavailable},
		{name: "artifact mismatch", mutate: func(r *PrepareUpdateRequest, _ *prepareFixture) { r.ExpectedActiveArtifactID = activeArtifactB }, want: ErrActiveArtifactMismatch},
		{name: "fresh tamper", mutate: func(_ *PrepareUpdateRequest, f *prepareFixture) {
			f.observer.refresh = func() { f.observer.observation.ArtifactID = activeArtifactB }
		}, want: ErrActiveArtifactMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newPrepareFixture(t, activeArtifactA)
			request := prepareUpdateRequest("prepare-1", "7.3.3", activeArtifactA)
			test.mutate(&request, f)
			_, err := f.starter.PrepareUpdate(t.Context(), request)
			if !errors.Is(err, test.want) {
				t.Fatalf("PrepareUpdate() error = %v, want %v", err, test.want)
			}
			if f.preparer.resolveCalls != 0 || f.preparer.stageCalls != 0 || containsEvent(f.events, "begin") {
				t.Fatalf("fence performed network/durable side effect: events=%v", f.events)
			}
		})
	}
}

func TestPrepareUpdateFreshFenceDetectsOnDiskExecutableTamper(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "cli-proxy-api")
	original := []byte("active executable A")
	if err := os.WriteFile(executable, original, 0o755); err != nil {
		t.Fatal(err)
	}
	observer := artifact.NewObserver(executable, "")
	_ = observer.Refresh()
	observed := observer.Observation()
	if observed == nil {
		t.Fatal("initial artifact observation is unavailable")
	}
	f := newStartFixture(t)
	preparer := &prepareUpdateFake{events: &f.events}
	if err := f.starter.EnablePrepareUpdate(observer, preparer); err != nil {
		t.Fatal(err)
	}
	tampered := []byte("active executable B")
	if err := os.WriteFile(executable, tampered, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := f.starter.PrepareUpdate(t.Context(), prepareUpdateRequest("prepare-1", "7.3.3", observed.ArtifactID))
	if !errors.Is(err, ErrActiveArtifactMismatch) || preparer.resolveCalls != 0 || containsEvent(f.events, "begin") {
		t.Fatalf("tamper fence error=%v events=%v", err, f.events)
	}
	wantDigest := sha256.Sum256(tampered)
	if current := observer.Observation(); current == nil || string(current.ArtifactID) != fmt.Sprintf("sha256:%x", wantDigest) {
		t.Fatalf("fresh observation = %+v", current)
	}
}

func TestPrepareUpdateReleasePreconditionsPrecedeIntent(t *testing.T) {
	f := newPrepareFixture(t, activeArtifactA)
	f.preparer.resolveErr = runtimeupdate.ErrReleaseMetadata
	_, err := f.starter.PrepareUpdate(t.Context(), prepareUpdateRequest("prepare-1", "7.3.3", activeArtifactA))
	if !errors.Is(err, ErrReleaseMetadataInvalid) || f.preparer.resolveCalls != 1 || f.preparer.stageCalls != 0 || containsEvent(f.events, "begin") {
		t.Fatalf("release precondition error=%v events=%v", err, f.events)
	}
}

func TestPrepareUpdatePersistsStableTerminalFailureAndDoesNotRetry(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{name: "download", err: runtimeupdate.ErrArchiveInvalid, code: "release_asset_invalid"},
		{name: "extraction", err: runtimeupdate.ErrStaging, code: "staging_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newPrepareFixture(t, activeArtifactA)
			f.preparer.stageErr = test.err
			request := prepareUpdateRequest("prepare-1", "7.3.3", activeArtifactA)
			result, err := f.starter.PrepareUpdate(t.Context(), request)
			if err != nil || result.State != journal.StateFailed || result.FailureCode != test.code {
				t.Fatalf("PrepareUpdate() = %+v, %v", result, err)
			}
			result, err = f.starter.PrepareUpdate(t.Context(), request)
			if err != nil || result.State != journal.StateFailed || f.preparer.stageCalls != 1 || f.preparer.resolveCalls != 1 {
				t.Fatalf("failed replay = %+v resolve=%d stage=%d error=%v", result, f.preparer.resolveCalls, f.preparer.stageCalls, err)
			}
		})
	}
}

func TestPrepareUpdateQueueWaitConsumesExecutionBudget(t *testing.T) {
	origTimeout := prepareUpdateExecutionTimeout
	prepareUpdateExecutionTimeout = 40 * time.Millisecond
	t.Cleanup(func() {
		prepareUpdateExecutionTimeout = origTimeout
	})

	f := newPrepareFixture(t, activeArtifactA)
	stageEntered := make(chan struct{})
	stageRelease := make(chan struct{})
	f.preparer.stageEntered = stageEntered
	f.preparer.stageRelease = stageRelease

	aResult := make(chan struct {
		operation journal.Operation
		err       error
	}, 1)
	go func() {
		op, err := f.starter.PrepareUpdate(t.Context(), prepareUpdateRequest("prepare-a", "7.3.3", activeArtifactA))
		aResult <- struct {
			operation journal.Operation
			err       error
		}{operation: op, err: err}
	}()

	select {
	case <-stageEntered:
	case <-time.After(time.Second):
		t.Fatal("prepare A did not enter stage")
	}

	resolveCallsBeforeB := f.preparer.resolveCalls
	stageCallsBeforeB := f.preparer.stageCalls

	bResult := make(chan struct {
		operation journal.Operation
		err       error
	}, 1)
	go func() {
		op, err := f.starter.PrepareUpdate(t.Context(), prepareUpdateRequest("prepare-b", "7.3.4", activeArtifactA))
		bResult <- struct {
			operation journal.Operation
			err       error
		}{operation: op, err: err}
	}()

	// Wait long enough for B's total execution budget (40ms) to expire while waiting in queue.
	time.Sleep(80 * time.Millisecond)

	// Release A. A finishes staging and records successful completion.
	close(stageRelease)

	select {
	case res := <-aResult:
		if res.err != nil || res.operation.State != journal.StateSucceeded {
			t.Fatalf("prepare A result = %+v", res)
		}
	case <-time.After(time.Second):
		t.Fatal("prepare A did not complete after stage release")
	}

	// B acquires prepareMu, detects that the operation budget expired while queued,
	// and exits immediately with DeadlineExceeded without executing any preconditions,
	// release lookup, or staging.
	select {
	case res := <-bResult:
		if !errors.Is(res.err, context.DeadlineExceeded) {
			t.Fatalf("prepare B error = %v, want DeadlineExceeded", res.err)
		}
		if res.operation != (journal.Operation{}) {
			t.Fatalf("prepare B returned non-empty operation: %+v", res.operation)
		}
	case <-time.After(time.Second):
		t.Fatal("prepare B did not exit after acquiring prepareMu")
	}

	// 1. Release metadata Resolve was called only once (by A, zero times by B)
	if f.preparer.resolveCalls != resolveCallsBeforeB {
		t.Fatalf("prepare B called release Resolve: calls before=%d, after=%d", resolveCallsBeforeB, f.preparer.resolveCalls)
	}
	// 2. Stage was called only once (by A, zero times by B)
	if f.preparer.stageCalls != stageCallsBeforeB {
		t.Fatalf("prepare B called Stage: calls before=%d, after=%d", stageCallsBeforeB, f.preparer.stageCalls)
	}
	// 3. Artifact Refresh was called twice by A (initial fence + second fence before begin), zero times by B
	refreshCalls := 0
	for _, ev := range f.events {
		if ev == "refresh-artifact" {
			refreshCalls++
		}
		if strings.Contains(ev, "7.3.4") {
			t.Fatalf("unexpected event for prepare-b version 7.3.4: %s", ev)
		}
	}
	if refreshCalls != 2 {
		t.Fatalf("artifact refresh calls = %d, want 2 (only A)", refreshCalls)
	}
	// 4. Zero journal Begin, MarkRunning, Complete for prepare-b
	for _, ev := range f.events {
		if strings.Contains(ev, "prepare-b") {
			t.Fatalf("unexpected event for prepare-b in events: %s", ev)
		}
	}
	_, found, err := f.journal.Resolve(t.Context(), f.starter.authority, journal.Intent{
		OperationID:               "prepare-b",
		OperationType:             "prepare_update",
		ExpectedRuntimeIdentity:   "runtime-01",
		ExpectedRuntimeGeneration: 41,
		RequestFingerprint:        sha256.Sum256([]byte("runtime.prepare_update/v1:{targetVersion:7.3.4}")),
	})
	if err != nil || found {
		t.Fatalf("prepare-b should not have durable journal record: found=%v, err=%v", found, err)
	}
}

func TestPrepareUpdatePreconditionContextPreCheck(t *testing.T) {
	f := newPrepareFixture(t, activeArtifactA)
	cancelledCtx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := f.starter.PrepareUpdate(cancelledCtx, prepareUpdateRequest("prepare-cancelled", "7.3.3", activeArtifactA))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PrepareUpdate() error = %v, want context.Canceled", err)
	}
	if len(f.events) != 0 {
		t.Fatalf("cancelled prepare produced events: %v", f.events)
	}
}

type prepareFixture struct {
	*startFixture
	observer *prepareArtifactObserver
	preparer *prepareUpdateFake
}

func newPrepareFixture(t *testing.T, activeID artifact.ID) *prepareFixture {
	t.Helper()
	base := newStartFixture(t)
	observer := &prepareArtifactObserver{
		events:      &base.events,
		observation: &artifact.Observation{Engine: artifact.EngineCPA, ArtifactID: activeID},
	}
	preparer := &prepareUpdateFake{events: &base.events}
	if err := base.starter.EnablePrepareUpdate(observer, preparer); err != nil {
		t.Fatal(err)
	}
	return &prepareFixture{startFixture: base, observer: observer, preparer: preparer}
}

func prepareUpdateRequest(id, version string, activeID artifact.ID) PrepareUpdateRequest {
	return PrepareUpdateRequest{
		OperationID:               id,
		ExpectedRuntimeIdentity:   "runtime-01",
		ExpectedRuntimeGeneration: 41,
		ExpectedActiveArtifactID:  activeID,
		TargetVersion:             version,
	}
}

func containsEvent(events []string, target string) bool {
	for _, event := range events {
		if event == target {
			return true
		}
	}
	return false
}

func recoveryEpoch(executor *Executor) uint64 {
	executor.recoveryMu.Lock()
	defer executor.recoveryMu.Unlock()
	return executor.recoveryEpoch
}

func prepareUpdateArchive(t *testing.T, executable []byte) []byte {
	t.Helper()
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := tarWriter.WriteHeader(&tar.Header{Name: "cli-proxy-api", Mode: 0o755, Size: int64(len(executable))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(executable); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}
