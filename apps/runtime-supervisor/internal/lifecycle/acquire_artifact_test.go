package lifecycle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/readiness"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/selection"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

type acquireTestEnv struct {
	root           string
	supervisorRoot string
	stageRoot      string
	selectionRoot  string
	manifestPath   string
	missingBundled string
	pinnedVersion  string
	pinnedID       artifact.ID
	archive        []byte
	stageStore     *runtimeupdate.Store
	selectionStore *selection.Store
	observer       *artifact.Observer
	process        *recordingAcquireProcess
	executor       *Executor
}

type recordingAcquireProcess struct {
	mu        sync.Mutex
	state     cpaprocess.Observation
	startSpec cpaprocess.StartSpec
	starts    int
	stops     int
	onStart   func(cpaprocess.StartSpec) error
}

func (p *recordingAcquireProcess) Observe() cpaprocess.Observation {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state.State == "" {
		return cpaprocess.Observation{State: cpaprocess.StateNotStarted}
	}
	return p.state
}

func (p *recordingAcquireProcess) Start(_ context.Context, spec cpaprocess.StartSpec) (cpaprocess.Observation, error) {
	if p.onStart != nil {
		if err := p.onStart(spec); err != nil {
			return cpaprocess.Observation{State: cpaprocess.StateNotStarted}, err
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.starts++
	p.startSpec = spec
	p.state = cpaprocess.Observation{
		State:      cpaprocess.StateRunning,
		PID:        4242,
		InstanceID: uint64(p.starts),
	}
	return p.state, nil
}

func (p *recordingAcquireProcess) PrepareStop() (cpaprocess.StopTarget, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stops++
	return nil, cpaprocess.ErrStateConflict
}

func (p *recordingAcquireProcess) ExitEvents() <-chan cpaprocess.ExitEvent {
	return nil
}

func newSlimAcquireTestEnv(t *testing.T, opts ...AcquisitionOption) *acquireTestEnv {
	t.Helper()
	root := t.TempDir()
	supervisorRoot := filepath.Join(root, "supervisor")
	stageRoot := filepath.Join(supervisorRoot, "artifacts", "cpa")
	selectionRoot := filepath.Join(supervisorRoot, "active", "cpa")
	missingBundled := filepath.Join(root, "missing-bundled-cpa")
	manifestPath := filepath.Join(root, "cpa-artifact.json")

	binary := []byte("#!/bin/sh\necho s2a-acquired-gateway\n")
	sum := sha256.Sum256(binary)
	pinnedID := artifact.ID("sha256:" + hex.EncodeToString(sum[:]))
	pinnedVersion := "8.0.55"
	license := []byte("MIT License\nCopyright (c) CLIProxyAPI\n")
	archive := buildAcquireTarGzip(t,
		acquireArchiveEntry{name: "cli-proxy-api", typeflag: tar.TypeReg, data: binary},
		acquireArchiveEntry{name: "LICENSE", typeflag: tar.TypeReg, data: license},
	)

	manifestJSON := fmt.Sprintf(
		"{\"schemaVersion\":1,\"engine\":\"cpa\",\"version\":%q,\"artifactId\":%q}\n",
		pinnedVersion,
		pinnedID,
	)
	if err := os.WriteFile(manifestPath, []byte(manifestJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	stageStore, err := runtimeupdate.NewStore(stageRoot)
	if err != nil {
		t.Fatal(err)
	}
	selectionStore, err := selection.NewStore(selectionRoot, stageStore)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := selectionStore.Load(selection.Bundled(missingBundled, manifestPath))
	if err != nil {
		t.Fatal(err)
	}
	observer := artifact.NewObserver(selected.ExecutablePath, selected.MetadataPath)
	_ = observer.RefreshFrom(selected.ExecutablePath, selected.MetadataPath)

	proc := &recordingAcquireProcess{}
	proc.onStart = func(spec cpaprocess.StartSpec) error {
		meta := manifestPath
		if spec.Executable != missingBundled {
			meta = filepath.Join(filepath.Dir(spec.Executable), "artifact.json")
		}
		return observer.RefreshFrom(spec.Executable, meta)
	}
	store, err := journal.Open(t.Context(), filepath.Join(supervisorRoot, "operations.sqlite"), journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(
		journal.Authority{RuntimeIdentity: "runtime-s2a", RuntimeGeneration: 1},
		store,
		proc,
		selected.ExecutablePath,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = executor.Close() })

	if err := executor.EnableActivateUpdate(selected, observer, selectionStore, readinessFunc(func(context.Context) readiness.State {
		return readiness.Ready
	})); err != nil {
		t.Fatal(err)
	}
	allOpts := append([]AcquisitionOption{
		WithAcquisitionPlatform("linux", "amd64"),
		WithAcquisitionCDNAddrPolicy(runtimeupdate.AllowLoopbackCDNAddr),
	}, opts...)
	if err := executor.EnableArtifactAcquisition(manifestPath, allOpts...); err != nil {
		t.Fatal(err)
	}

	return &acquireTestEnv{
		root:           root,
		supervisorRoot: supervisorRoot,
		stageRoot:      stageRoot,
		selectionRoot:  selectionRoot,
		manifestPath:   manifestPath,
		missingBundled: missingBundled,
		pinnedVersion:  pinnedVersion,
		pinnedID:       pinnedID,
		archive:        archive,
		stageStore:     stageStore,
		selectionStore: selectionStore,
		observer:       observer,
		process:        proc,
		executor:       executor,
	}
}

type readinessFunc func(context.Context) readiness.State

func (f readinessFunc) Observe(ctx context.Context) readiness.State { return f(ctx) }

func TestAcquireArtifactViaGitHubAndCDNSourcesRestartAndStartSpec(t *testing.T) {
	officialPath := "/router-for-me/CLIProxyAPI/releases/download/v8.0.55/CLIProxyAPI_8.0.55_linux_amd64.tar.gz"
	var archivePayload []byte
	var probeReachable = true

	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case officialPath:
			if !probeReachable {
				http.Error(w, "upstream unavailable", http.StatusBadGateway)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(archivePayload)))
			_, _ = w.Write(archivePayload)
		case "/cdn/https://github.com" + officialPath:
			w.Header().Set("Content-Length", strconv.Itoa(len(archivePayload)))
			_, _ = w.Write(archivePayload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer tlsServer.Close()

	client := newOfficialAndCDNTLSClient(tlsServer)
	env := newSlimAcquireTestEnv(t, WithAcquisitionHTTPClient(client))
	archivePayload = env.archive

	// Initial slim status: active is nil, required is true.
	initialStatus, err := env.executor.ArtifactStatus()
	if err != nil {
		t.Fatalf("initial ArtifactStatus() error = %v", err)
	}
	if initialStatus.SchemaVersion != 1 || initialStatus.Pinned.Version != "8.0.55" ||
		initialStatus.Pinned.ArtifactID != env.pinnedID || initialStatus.Active != nil ||
		!initialStatus.Required || initialStatus.Operation != nil {
		t.Fatalf("initial ArtifactStatus() = %+v", initialStatus)
	}

	// Probe reachable vs unreachable.
	probeOK, err := env.executor.ProbeArtifactSource(t.Context())
	if err != nil || !probeOK.Reachable || probeOK.Code != "" {
		t.Fatalf("ProbeArtifactSource(reachable) = %+v, %v", probeOK, err)
	}
	probeReachable = false
	probeFail, err := env.executor.ProbeArtifactSource(t.Context())
	if err != nil || probeFail.Reachable || probeFail.Code != ArtifactCodeSourceUnreachable {
		t.Fatalf("ProbeArtifactSource(unreachable) = %+v, %v", probeFail, err)
	}
	probeReachable = true

	// Acquire via official GitHub source.
	op, started, err := env.executor.AcquireArtifact(t.Context(), ArtifactAcquireRequest{Source: ArtifactSourceGitHub})
	if err != nil || !started || op.Source != ArtifactSourceGitHub {
		t.Fatalf("AcquireArtifact(github) = %+v, started=%v, err=%v", op, started, err)
	}
	finalStatus := awaitAcquisitionTerminal(t, env.executor)
	if finalStatus.Operation == nil || finalStatus.Operation.Phase != ArtifactPhaseSucceeded ||
		finalStatus.Operation.Code != "" || finalStatus.Required ||
		finalStatus.Active == nil || finalStatus.Active.ArtifactID != env.pinnedID ||
		finalStatus.Active.Origin != ArtifactOriginAcquired {
		t.Fatalf("settled ArtifactStatus() = %+v", finalStatus)
	}
	if env.process.starts != 0 || env.process.stops != 0 {
		t.Fatalf("acquisition touched child process: starts=%d stops=%d", env.process.starts, env.process.stops)
	}

	// Idempotent second call returns started=false (200) and phase succeeded.
	idempotentOp, startedAgain, err := env.executor.AcquireArtifact(t.Context(), ArtifactAcquireRequest{
		Source:    ArtifactSourceCDN,
		CDNPrefix: tlsServer.URL + "/cdn",
	})
	if err != nil || startedAgain || idempotentOp.Phase != ArtifactPhaseSucceeded || idempotentOp.Code != "" {
		t.Fatalf("idempotent AcquireArtifact() = %+v, started=%v, err=%v", idempotentOp, startedAgain, err)
	}

	// Restart semantics: a new selection store + observer constructed from disk resolves the acquired artifact with no network.
	restartedStages, err := runtimeupdate.NewStore(env.stageRoot)
	if err != nil {
		t.Fatal(err)
	}
	restartedSelections, err := selection.NewStore(env.selectionRoot, restartedStages)
	if err != nil {
		t.Fatal(err)
	}
	restartedSelected, err := restartedSelections.Load(selection.Bundled(env.missingBundled, env.manifestPath))
	if err != nil {
		t.Fatalf("restarted selectionStore.Load() error = %v", err)
	}
	if !restartedSelected.IsFinalizedStage() || restartedSelected.ArtifactID != env.pinnedID || restartedSelected.Version != "8.0.55" {
		t.Fatalf("restarted selection descriptor = %+v", restartedSelected)
	}
	restartedObserver := artifact.NewObserver(restartedSelected.ExecutablePath, restartedSelected.MetadataPath)
	if err := restartedObserver.Refresh(); err != nil {
		t.Fatalf("restarted observer.Refresh() error = %v", err)
	}
	if obs := restartedObserver.Observation(); obs == nil || obs.ArtifactID != env.pinnedID || obs.Version != "8.0.55" {
		t.Fatalf("restarted observation = %+v", obs)
	}

	// Executor's Start spec uses the staged executable.
	if _, err := env.executor.Start(t.Context(), StartRequest{
		OperationID:               "start-after-acquire",
		ExpectedRuntimeIdentity:   "runtime-s2a",
		ExpectedRuntimeGeneration: 1,
		ExpectedActiveArtifactID:  env.pinnedID,
	}); err != nil {
		t.Fatalf("Start after acquire error = %v", err)
	}
	wantExecutable := filepath.Join(env.stageRoot, "8.0.55", "cli-proxy-api")
	if env.process.starts != 1 || env.process.startSpec.Executable != wantExecutable {
		t.Fatalf("Start spec executable = %q, want %q", env.process.startSpec.Executable, wantExecutable)
	}

	// Also verify fresh acquisition via CDN transport in a clean slim environment.
	cdnEnv := newSlimAcquireTestEnv(t, WithAcquisitionHTTPClient(client))
	destRoot := filepath.Join(cdnEnv.supervisorRoot, "destination")
	gatewayAuth := filepath.Join(cdnEnv.root, "gateway", "auth")
	if err := cdnEnv.executor.EnableDestination(destRoot, gatewayAuth, "127.0.0.1:8317", nil, false); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(destRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	stagedReq := DestinationRequest{
		OperationID:               "stage-op-1",
		ExpectedRuntimeIdentity:   "runtime-s2a",
		ExpectedRuntimeGeneration: 1,
		ExpectedActiveArtifactID:  artifact.ID("sha256:1111111111111111111111111111111111111111111111111111111111111111"),
		SourceIdentity:            "managed:sha256:2222222222222222222222222222222222222222222222222222222222222222",
		SourceClass:               SourceClassNewManaged,
		APIKeys:                   []string{"sk-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		ManagementKey:             "3333333333333333333333333333333333333333333333333333333333333333",
	}
	if err := cdnEnv.executor.writeAtomicDestinationJSONLocked("stage.json", stagedReq); err != nil {
		t.Fatal(err)
	}
	cdnOp, cdnStarted, err := cdnEnv.executor.AcquireArtifact(t.Context(), ArtifactAcquireRequest{
		Source:    ArtifactSourceCDN,
		CDNPrefix: tlsServer.URL + "/cdn/",
	})
	if err != nil || !cdnStarted || cdnOp.Source != ArtifactSourceCDN {
		t.Fatalf("AcquireArtifact(cdn) = %+v, started=%v, err=%v", cdnOp, cdnStarted, err)
	}
	cdnFinal := awaitAcquisitionTerminal(t, cdnEnv.executor)
	if cdnFinal.Operation == nil || cdnFinal.Operation.Phase != ArtifactPhaseSucceeded ||
		cdnFinal.Active == nil || cdnFinal.Active.Origin != ArtifactOriginAcquired ||
		cdnFinal.Active.ArtifactID != cdnEnv.pinnedID || cdnFinal.Required {
		t.Fatalf("CDN settled status = %+v", cdnFinal)
	}
	updatedStage, err := cdnEnv.executor.destination.load()
	if err != nil || updatedStage == nil || updatedStage.ExpectedActiveArtifactID != cdnEnv.pinnedID {
		t.Fatalf("destination stage after acquire = %+v, %v; want artifact %s", updatedStage, err, cdnEnv.pinnedID)
	}
}

func TestAcquireArtifactFailureCleanupSingleFlightAndConflict(t *testing.T) {
	officialPath := "/router-for-me/CLIProxyAPI/releases/download/v8.0.55/CLIProxyAPI_8.0.55_linux_amd64.tar.gz"
	var (
		mu         sync.Mutex
		responseFn func(w http.ResponseWriter, r *http.Request)
		serverURL  string
	)
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fn := responseFn
		mu.Unlock()
		fn(w, r)
	}))
	defer tlsServer.Close()
	serverURL = tlsServer.URL
	client := newOfficialAndCDNTLSClient(tlsServer)

	failureCases := []struct {
		name     string
		req      ArtifactAcquireRequest
		respond  func(w http.ResponseWriter, r *http.Request)
		wantCode string
	}{
		{
			name: "tampered executable",
			req:  ArtifactAcquireRequest{Source: ArtifactSourceCDN, CDNPrefix: serverURL + "/cdn"},
			respond: func(w http.ResponseWriter, _ *http.Request) {
				bad := buildAcquireTarGzip(t,
					acquireArchiveEntry{name: "cli-proxy-api", typeflag: tar.TypeReg, data: []byte("wrong-binary")},
					acquireArchiveEntry{name: "LICENSE", typeflag: tar.TypeReg, data: []byte("MIT")},
				)
				_, _ = w.Write(bad)
			},
			wantCode: ArtifactCodeArtifactMismatch,
		},
		{
			name: "CDN returns HTML",
			req:  ArtifactAcquireRequest{Source: ArtifactSourceCDN, CDNPrefix: serverURL + "/cdn"},
			respond: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/html")
				_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
			},
			wantCode: ArtifactCodeArchiveInvalid,
		},
		{
			name: "CDN redirects to plain http",
			req:  ArtifactAcquireRequest{Source: ArtifactSourceCDN, CDNPrefix: serverURL + "/cdn"},
			respond: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "http://127.0.0.1/insecure.tar.gz", http.StatusFound)
			},
			wantCode: ArtifactCodeDownloadFailed,
		},
		{
			name: "path traversal archive",
			req:  ArtifactAcquireRequest{Source: ArtifactSourceGitHub},
			respond: func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != officialPath {
					http.NotFound(w, r)
					return
				}
				bad := buildAcquireTarGzip(t,
					acquireArchiveEntry{name: "../escape", typeflag: tar.TypeReg, data: []byte("x")},
				)
				_, _ = w.Write(bad)
			},
			wantCode: ArtifactCodeArchiveInvalid,
		},
	}

	for _, tc := range failureCases {
		t.Run(tc.name, func(t *testing.T) {
			env := newSlimAcquireTestEnv(t, WithAcquisitionHTTPClient(client))
			mu.Lock()
			responseFn = tc.respond
			mu.Unlock()

			_, started, err := env.executor.AcquireArtifact(t.Context(), tc.req)
			if err != nil || !started {
				t.Fatalf("AcquireArtifact() started=%v, err=%v", started, err)
			}
			settled := awaitAcquisitionTerminal(t, env.executor)
			if settled.Operation == nil || settled.Operation.Phase != ArtifactPhaseFailed ||
				settled.Operation.Code != tc.wantCode || settled.Active != nil || !settled.Required {
				t.Fatalf("settled status = %+v, want failed with %q", settled, tc.wantCode)
			}
			assertNoStageOrSelection(t, env)
		})
	}

	// Single-flight: concurrent acquire while one is in progress returns acquisition_in_progress.
	env := newSlimAcquireTestEnv(t, WithAcquisitionHTTPClient(client))
	enteredDownload := make(chan struct{})
	releaseDownload := make(chan struct{})
	mu.Lock()
	responseFn = func(w http.ResponseWriter, _ *http.Request) {
		close(enteredDownload)
		<-releaseDownload
		_, _ = w.Write(env.archive)
	}
	mu.Unlock()

	if _, started, err := env.executor.AcquireArtifact(t.Context(), ArtifactAcquireRequest{Source: ArtifactSourceGitHub}); err != nil || !started {
		t.Fatalf("first AcquireArtifact() = %v, %v", started, err)
	}
	<-enteredDownload

	if _, _, err := env.executor.AcquireArtifact(t.Context(), ArtifactAcquireRequest{Source: ArtifactSourceGitHub}); ArtifactErrorCode(err) != ArtifactCodeAcquisitionInProgress {
		t.Fatalf("concurrent AcquireArtifact() error = %v (code %q), want %q", err, ArtifactErrorCode(err), ArtifactCodeAcquisitionInProgress)
	}
	if _, err := env.executor.UploadArtifact(t.Context(), bytes.NewReader(env.archive), int64(len(env.archive))); ArtifactErrorCode(err) != ArtifactCodeAcquisitionInProgress {
		t.Fatalf("concurrent UploadArtifact() error = %v (code %q), want %q", err, ArtifactErrorCode(err), ArtifactCodeAcquisitionInProgress)
	}
	close(releaseDownload)
	settled := awaitAcquisitionTerminal(t, env.executor)
	if settled.Operation == nil || settled.Operation.Phase != ArtifactPhaseSucceeded {
		t.Fatalf("settled after single-flight = %+v", settled)
	}

	// Different active artifact -> refused with artifact_conflict.
	conflictEnv := newSlimAcquireTestEnv(t, WithAcquisitionHTTPClient(client))
	if err := os.WriteFile(conflictEnv.missingBundled, []byte("different-bundled-executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = conflictEnv.observer.RefreshFrom(conflictEnv.missingBundled, conflictEnv.manifestPath)
	if _, _, err := conflictEnv.executor.AcquireArtifact(t.Context(), ArtifactAcquireRequest{Source: ArtifactSourceGitHub}); ArtifactErrorCode(err) != ArtifactCodeArtifactConflict {
		t.Fatalf("different active artifact AcquireArtifact() error = %v, want %q", err, ArtifactCodeArtifactConflict)
	}
	assertNoStageOrSelection(t, conflictEnv)
}

func assertNoStageOrSelection(t *testing.T, env *acquireTestEnv) {
	t.Helper()
	stageEntries, err := os.ReadDir(env.stageRoot)
	if err != nil || len(stageEntries) != 0 {
		t.Fatalf("expected empty stage root, got %v (err=%v)", stageEntries, err)
	}
	if _, err := os.Stat(filepath.Join(env.selectionRoot, "selection.json")); !os.IsNotExist(err) {
		t.Fatalf("selection.json should not exist after failed acquisition: %v", err)
	}
}

func awaitAcquisitionTerminal(t *testing.T, executor *Executor) ArtifactStatusResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, err := executor.ArtifactStatus()
		if err != nil {
			t.Fatalf("ArtifactStatus() error = %v", err)
		}
		if status.Operation != nil &&
			(status.Operation.Phase == ArtifactPhaseSucceeded || status.Operation.Phase == ArtifactPhaseFailed) {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for acquisition operation to reach terminal phase")
	return ArtifactStatusResponse{}
}

func TestAcquireArtifactCDNStrictNonPublicRefusalAndPreparedStageLicenseBackfill(t *testing.T) {
	// 1. Default strict CDN policy refuses literal private/loopback prefixes and dial-time resolution to 127.0.0.1.
	strictEnv := newSlimAcquireTestEnv(
		t,
		WithAcquisitionCDNAddrPolicy(runtimeupdate.IsPublicCDNAddr),
		WithAcquisitionCDNLookupIP(func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		}),
	)
	for _, rawPrefix := range []string{
		"https://127.0.0.1/mirror",
		"https://10.0.0.1/mirror",
		"https://192.168.1.1/mirror",
		"https://100.64.0.1/mirror",
		"https://[::1]/mirror",
		"https://[::ffff:127.0.0.1]/mirror",
	} {
		if _, _, err := strictEnv.executor.AcquireArtifact(t.Context(), ArtifactAcquireRequest{
			Source:    ArtifactSourceCDN,
			CDNPrefix: rawPrefix,
		}); ArtifactErrorCode(err) != ArtifactCodeInvalidRequest {
			t.Fatalf("AcquireArtifact(%q) error = %v (code %q), want %q", rawPrefix, err, ArtifactErrorCode(err), ArtifactCodeInvalidRequest)
		}
	}

	if _, started, err := strictEnv.executor.AcquireArtifact(t.Context(), ArtifactAcquireRequest{
		Source:    ArtifactSourceCDN,
		CDNPrefix: "https://cdn.example.com/mirror",
	}); err != nil || !started {
		t.Fatalf("AcquireArtifact(DNS -> 127.0.0.1) started=%v, err=%v", started, err)
	}
	strictSettled := awaitAcquisitionTerminal(t, strictEnv.executor)
	if strictSettled.Operation == nil || strictSettled.Operation.Phase != ArtifactPhaseFailed ||
		strictSettled.Operation.Code != ArtifactCodeDownloadFailed {
		t.Fatalf("DNS -> 127.0.0.1 settled = %+v, want failed with %q", strictSettled, ArtifactCodeDownloadFailed)
	}
	assertNoStageOrSelection(t, strictEnv)

	// 2. When a stage was created by Store.Stage without LICENSE, UploadArtifact / AcquireArtifact backfills LICENSE before committing selection.
	backfillEnv := newSlimAcquireTestEnv(t)
	binary := []byte("#!/bin/sh\necho s2a-acquired-gateway\n")
	archiveWithoutLicense := buildAcquireTarGzip(t,
		acquireArchiveEntry{name: "cli-proxy-api", typeflag: tar.TypeReg, data: binary},
	)
	archiveSum := sha256.Sum256(archiveWithoutLicense)
	if _, err := backfillEnv.stageStore.Stage(t.Context(), runtimeupdate.Release{
		Version:       backfillEnv.pinnedVersion,
		AssetName:     "CLIProxyAPI_8.0.55_linux_amd64.tar.gz",
		AssetURL:      "https://github.com/router-for-me/CLIProxyAPI/releases/download/v8.0.55/CLIProxyAPI_8.0.55_linux_amd64.tar.gz",
		ArchiveDigest: "sha256:" + hex.EncodeToString(archiveSum[:]),
		ArchiveSize:   int64(len(archiveWithoutLicense)),
	}, func(_ context.Context, _ runtimeupdate.Release, dst io.Writer) error {
		_, writeErr := dst.Write(archiveWithoutLicense)
		return writeErr
	}); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if _, err := backfillEnv.executor.UploadArtifact(t.Context(), bytes.NewReader(archiveWithoutLicense), int64(len(archiveWithoutLicense))); ArtifactErrorCode(err) != ArtifactCodeArchiveInvalid {
		t.Fatalf("UploadArtifact(without LICENSE) error = %v, want %q", err, ArtifactCodeArchiveInvalid)
	}
	if _, err := os.Stat(filepath.Join(backfillEnv.selectionRoot, "selection.json")); !os.IsNotExist(err) {
		t.Fatalf("selection.json created when LICENSE was missing: %v", err)
	}
	op, err := backfillEnv.executor.UploadArtifact(t.Context(), bytes.NewReader(backfillEnv.archive), int64(len(backfillEnv.archive)))
	if err != nil || op.Phase != ArtifactPhaseSucceeded {
		t.Fatalf("UploadArtifact(with LICENSE) = %+v, %v", op, err)
	}
	if _, err := os.Stat(filepath.Join(backfillEnv.stageRoot, backfillEnv.pinnedVersion, "LICENSE")); err != nil {
		t.Fatalf("LICENSE missing after UploadArtifact backfill: %v", err)
	}
}

func newOfficialAndCDNTLSClient(server *httptest.Server) *http.Client {
	baseTransport := server.Client().Transport.(*http.Transport).Clone()
	targetAddr := server.Listener.Addr().String()
	baseTransport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, targetAddr)
	}
	if baseTransport.TLSClientConfig == nil {
		baseTransport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		baseTransport.TLSClientConfig = baseTransport.TLSClientConfig.Clone()
	}
	baseTransport.TLSClientConfig.ServerName = "example.com"
	return &http.Client{Transport: baseTransport}
}

type acquireArchiveEntry struct {
	name     string
	typeflag byte
	data     []byte
	size     int64
	linkname string
}

func buildAcquireTarGzip(t *testing.T, entries ...acquireArchiveEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range entries {
		tf := entry.typeflag
		if tf == 0 {
			tf = tar.TypeReg
		}
		sz := int64(len(entry.data))
		if entry.size != 0 {
			sz = entry.size
		}
		hdr := &tar.Header{
			Name:     entry.name,
			Typeflag: tf,
			Mode:     0o755,
			Size:     sz,
			Linkname: entry.linkname,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if len(entry.data) > 0 {
			if _, err := tw.Write(entry.data); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
