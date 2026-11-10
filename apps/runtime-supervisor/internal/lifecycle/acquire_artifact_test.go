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
	"log"
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

type faultSelectionStore struct {
	*selection.Store
	commitErr   error
	commitCalls int
}

func (s *faultSelectionStore) Commit(descriptor selection.Descriptor) error {
	s.commitCalls++
	if s.commitErr != nil {
		return s.commitErr
	}
	return s.Store.Commit(descriptor)
}

type countingFailReader struct {
	data  []byte
	reads int
}

func (r *countingFailReader) Read(p []byte) (int, error) {
	r.reads++
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func TestAcquireArtifactAdmissionAndCommitPreconditions(t *testing.T) {
	// 1. Finalized stage + nil observation -> artifact_conflict.
	stageNilEnv := newSlimAcquireTestEnv(t)
	if _, err := stageNilEnv.stageStore.AcquireStage(
		t.Context(),
		stageNilEnv.pinnedVersion,
		stageNilEnv.pinnedID,
		runtimeupdate.CopyUploadArchive(bytes.NewReader(stageNilEnv.archive), int64(len(stageNilEnv.archive))),
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	stagedDesc, err := stageNilEnv.selectionStore.ResolveFinalized(stageNilEnv.pinnedVersion)
	if err != nil {
		t.Fatal(err)
	}
	stageNilEnv.executor.mu.Lock()
	stageNilEnv.executor.active = stagedDesc
	stageNilEnv.executor.executable = stagedDesc.ExecutablePath
	stageNilEnv.executor.mu.Unlock()
	// stageNilEnv.observer still has nil Observation() because Refresh was not called on stagedDesc.
	if _, _, err := stageNilEnv.executor.AcquireArtifact(t.Context(), ArtifactAcquireRequest{
		Source:    ArtifactSourceCDN,
		CDNPrefix: "https://127.0.0.1:8443/cdn",
	}); ArtifactErrorCode(err) != ArtifactCodeArtifactConflict {
		t.Fatalf("finalized stage + nil observation AcquireArtifact() = %v (code %q), want %q", err, ArtifactErrorCode(err), ArtifactCodeArtifactConflict)
	}
	if _, err := stageNilEnv.executor.UploadArtifact(t.Context(), bytes.NewReader(stageNilEnv.archive), int64(len(stageNilEnv.archive))); ArtifactErrorCode(err) != ArtifactCodeArtifactConflict {
		t.Fatalf("finalized stage + nil observation UploadArtifact() = %v (code %q), want %q", err, ArtifactErrorCode(err), ArtifactCodeArtifactConflict)
	}

	// 2. Bundled selection + nil observation + existing file at executable path -> artifact_conflict.
	bundledFileEnv := newSlimAcquireTestEnv(t)
	if err := os.WriteFile(bundledFileEnv.missingBundled, []byte("#!/bin/sh\necho unexpected-file-at-bundled-path\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Do not refresh observer: observation remains nil while file exists on disk.
	if _, _, err := bundledFileEnv.executor.AcquireArtifact(t.Context(), ArtifactAcquireRequest{
		Source:    ArtifactSourceCDN,
		CDNPrefix: "https://127.0.0.1:8443/cdn",
	}); ArtifactErrorCode(err) != ArtifactCodeArtifactConflict {
		t.Fatalf("bundled + nil observation + existing file AcquireArtifact() = %v (code %q), want %q", err, ArtifactErrorCode(err), ArtifactCodeArtifactConflict)
	}
	if _, err := bundledFileEnv.executor.UploadArtifact(t.Context(), bytes.NewReader(bundledFileEnv.archive), int64(len(bundledFileEnv.archive))); ArtifactErrorCode(err) != ArtifactCodeArtifactConflict {
		t.Fatalf("bundled + nil observation + existing file UploadArtifact() = %v (code %q), want %q", err, ArtifactErrorCode(err), ArtifactCodeArtifactConflict)
	}
	assertNoStageOrSelection(t, bundledFileEnv)

	// 3. Slim gap + running process (StateRunning and non-exited state) -> artifact_conflict.
	for _, activeState := range []cpaprocess.State{cpaprocess.StateRunning, cpaprocess.State("starting")} {
		procEnv := newSlimAcquireTestEnv(t)
		procEnv.process.mu.Lock()
		procEnv.process.state = cpaprocess.Observation{State: activeState, PID: 9001, InstanceID: 1}
		procEnv.process.mu.Unlock()
		if _, _, err := procEnv.executor.AcquireArtifact(t.Context(), ArtifactAcquireRequest{
			Source:    ArtifactSourceCDN,
			CDNPrefix: "https://127.0.0.1:8443/cdn",
		}); ArtifactErrorCode(err) != ArtifactCodeArtifactConflict {
			t.Fatalf("slim gap + process %s AcquireArtifact() = %v (code %q), want %q", activeState, err, ArtifactErrorCode(err), ArtifactCodeArtifactConflict)
		}
		if _, err := procEnv.executor.UploadArtifact(t.Context(), bytes.NewReader(procEnv.archive), int64(len(procEnv.archive))); ArtifactErrorCode(err) != ArtifactCodeArtifactConflict {
			t.Fatalf("slim gap + process %s UploadArtifact() = %v (code %q), want %q", activeState, err, ArtifactErrorCode(err), ArtifactCodeArtifactConflict)
		}
		assertNoStageOrSelection(t, procEnv)
	}

	// 4. Active selection flipped to a different artifact while downloading -> Commit not called, newly created stage removed.
	enteredDownload := make(chan struct{})
	releaseDownload := make(chan struct{})
	var flipEnv *acquireTestEnv
	flipEnv = newSlimAcquireTestEnv(t, WithOfficialAcquisitionSource(&stubAcquireSource{
		downloadFn: func(_ context.Context, _ string, dst io.Writer, _ runtimeupdate.ProgressFunc) error {
			close(enteredDownload)
			<-releaseDownload
			_, err := dst.Write(flipEnv.archive)
			return err
		},
	}))
	faultStore := &faultSelectionStore{Store: flipEnv.selectionStore}
	flipEnv.executor.mu.Lock()
	flipEnv.executor.selections = faultStore
	flipEnv.executor.mu.Unlock()

	if _, started, err := flipEnv.executor.AcquireArtifact(t.Context(), ArtifactAcquireRequest{Source: ArtifactSourceGitHub}); err != nil || !started {
		t.Fatalf("AcquireArtifact() started=%v, err=%v", started, err)
	}
	<-enteredDownload

	// Flip active selection to a different bundled executable while download is in flight.
	if err := os.WriteFile(flipEnv.missingBundled, []byte("#!/bin/sh\necho different-active-artifact\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	otherSum := sha256.Sum256([]byte("#!/bin/sh\necho different-active-artifact\n"))
	otherID := artifact.ID("sha256:" + hex.EncodeToString(otherSum[:]))
	otherManifest := filepath.Join(flipEnv.root, "other-manifest.json")
	if err := os.WriteFile(otherManifest, []byte(fmt.Sprintf("{\"schemaVersion\":1,\"engine\":\"cpa\",\"version\":\"8.0.54\",\"artifactId\":%q}\n", otherID)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := flipEnv.observer.RefreshFrom(flipEnv.missingBundled, otherManifest); err != nil {
		t.Fatal(err)
	}
	close(releaseDownload)

	settled := awaitAcquisitionTerminal(t, flipEnv.executor)
	if settled.Operation == nil || settled.Operation.Phase != ArtifactPhaseFailed || settled.Operation.Code != ArtifactCodeArtifactConflict {
		t.Fatalf("flipped selection settled = %+v, want failed with %q", settled, ArtifactCodeArtifactConflict)
	}
	if faultStore.commitCalls != 0 {
		t.Fatalf("selections.Commit was called %d times after active selection flipped; want 0", faultStore.commitCalls)
	}
	assertNoStageOrSelection(t, flipEnv)
}

type stubAcquireSource struct {
	probeFn    func(ctx context.Context, version string) error
	downloadFn func(ctx context.Context, version string, destination io.Writer, progress runtimeupdate.ProgressFunc) error
}

func (s *stubAcquireSource) Probe(ctx context.Context, version string) error {
	if s.probeFn != nil {
		return s.probeFn(ctx, version)
	}
	return nil
}

func (s *stubAcquireSource) DownloadArchive(ctx context.Context, version string, destination io.Writer, progress runtimeupdate.ProgressFunc) error {
	return s.downloadFn(ctx, version, destination, progress)
}

func TestAcquireArtifactCommitIrreversibleAndRecoveryRequired(t *testing.T) {
	t.Run("observer refresh failure after Commit enters recovery_required and blocks mutating paths", func(t *testing.T) {
		env := newSlimAcquireTestEnv(t, WithAcquisitionPostCommitHooks(
			func() error { return fmt.Errorf("injected observer refresh failure") },
			nil,
		))
		destRoot := filepath.Join(env.supervisorRoot, "destination")
		gatewayAuth := filepath.Join(env.root, "gateway", "auth")
		if err := env.executor.EnableDestination(destRoot, gatewayAuth, "127.0.0.1:8317", nil, false); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(destRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		origStageReq := DestinationRequest{
			OperationID:               "stage-before-acquire",
			ExpectedRuntimeIdentity:   "runtime-s2a",
			ExpectedRuntimeGeneration: 1,
			ExpectedActiveArtifactID:  artifact.ID("sha256:1111111111111111111111111111111111111111111111111111111111111111"),
			SourceIdentity:            "managed:sha256:2222222222222222222222222222222222222222222222222222222222222222",
			SourceClass:               SourceClassNewManaged,
			APIKeys:                   []string{"sk-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
			ManagementKey:             "3333333333333333333333333333333333333333333333333333333333333333",
		}
		if err := env.executor.writeAtomicDestinationJSONLocked("stage.json", origStageReq); err != nil {
			t.Fatal(err)
		}

		op, err := env.executor.UploadArtifact(t.Context(), bytes.NewReader(env.archive), int64(len(env.archive)))
		if ArtifactErrorCode(err) != ArtifactCodeRecoveryRequired || op.Phase != ArtifactPhaseFailed || op.Code != ArtifactCodeRecoveryRequired {
			t.Fatalf("UploadArtifact() with post-commit refresh failure = %+v, %v (code %q); want recovery_required", op, err, ArtifactErrorCode(err))
		}
		// Commit was not rolled back: selection.json and stage remain on disk.
		if _, err := os.Stat(filepath.Join(env.selectionRoot, "selection.json")); err != nil {
			t.Fatalf("selection.json missing after committed selection: %v", err)
		}
		if !env.stageStore.HasAcquiredStage(env.pinnedVersion, env.pinnedID) {
			t.Fatal("committed stage was removed after post-commit failure")
		}
		env.executor.recoveryMu.Lock()
		gotRecoveryState := env.executor.recoveryState
		env.executor.recoveryMu.Unlock()
		if gotRecoveryState != RecoveryStateManualIntervention {
			t.Fatalf("recoveryState = %q, want %q", gotRecoveryState, RecoveryStateManualIntervention)
		}
		assertMutatingLifecycleBlockedAfterRecoveryRequired(t, env)
	})

	t.Run("destination sync failure after Commit enters recovery_required and blocks Start", func(t *testing.T) {
		env := newSlimAcquireTestEnv(t, WithAcquisitionPostCommitHooks(
			nil,
			func() error { return fmt.Errorf("injected destination sync failure") },
		))
		op, err := env.executor.UploadArtifact(t.Context(), bytes.NewReader(env.archive), int64(len(env.archive)))
		if ArtifactErrorCode(err) != ArtifactCodeRecoveryRequired || op.Phase != ArtifactPhaseFailed || op.Code != ArtifactCodeRecoveryRequired {
			t.Fatalf("UploadArtifact() with post-commit destination failure = %+v, %v (code %q); want recovery_required", op, err, ArtifactErrorCode(err))
		}
		if _, err := os.Stat(filepath.Join(env.selectionRoot, "selection.json")); err != nil {
			t.Fatalf("selection.json missing after committed selection: %v", err)
		}
		if !env.stageStore.HasAcquiredStage(env.pinnedVersion, env.pinnedID) {
			t.Fatal("committed stage was removed after post-commit destination failure")
		}
		assertMutatingLifecycleBlockedAfterRecoveryRequired(t, env)
	})

	t.Run("ambiguous selection Commit keeps stage and enters recovery_required", func(t *testing.T) {
		env := newSlimAcquireTestEnv(t)
		faultStore := &faultSelectionStore{
			Store:     env.selectionStore,
			commitErr: fmt.Errorf("%w: injected ambiguous directory sync", selection.ErrSelectionPublicationAmbiguous),
		}
		env.executor.mu.Lock()
		env.executor.selections = faultStore
		env.executor.mu.Unlock()

		op, err := env.executor.UploadArtifact(t.Context(), bytes.NewReader(env.archive), int64(len(env.archive)))
		if ArtifactErrorCode(err) != ArtifactCodeRecoveryRequired || op.Phase != ArtifactPhaseFailed || op.Code != ArtifactCodeRecoveryRequired {
			t.Fatalf("UploadArtifact() with ambiguous Commit = %+v, %v (code %q); want recovery_required", op, err, ArtifactErrorCode(err))
		}
		// Stage MUST be kept when Commit outcome is ambiguous.
		if !env.stageStore.HasAcquiredStage(env.pinnedVersion, env.pinnedID) {
			t.Fatal("newly created stage was removed despite ambiguous Commit")
		}
		assertMutatingLifecycleBlockedAfterRecoveryRequired(t, env)
	})
}

func assertMutatingLifecycleBlockedAfterRecoveryRequired(t *testing.T, env *acquireTestEnv) {
	t.Helper()
	if _, err := env.executor.Start(t.Context(), StartRequest{
		OperationID:               "start-blocked",
		ExpectedRuntimeIdentity:   "runtime-s2a",
		ExpectedRuntimeGeneration: 1,
		ExpectedActiveArtifactID:  env.pinnedID,
	}); !errorsIs(err, journal.ErrOperationStateConflict) {
		t.Fatalf("Start after recovery_required = %v, want ErrOperationStateConflict", err)
	}
	if _, err := env.executor.Restart(t.Context(), RestartRequest{
		OperationID:               "restart-blocked",
		ExpectedRuntimeIdentity:   "runtime-s2a",
		ExpectedRuntimeGeneration: 1,
	}); !errorsIs(err, journal.ErrOperationStateConflict) {
		t.Fatalf("Restart after recovery_required = %v, want ErrOperationStateConflict", err)
	}
	if _, _, err := env.executor.AcquireArtifact(t.Context(), ArtifactAcquireRequest{
		Source:    ArtifactSourceCDN,
		CDNPrefix: "https://127.0.0.1:8443/cdn",
	}); ArtifactErrorCode(err) != ArtifactCodeRecoveryRequired {
		t.Fatalf("AcquireArtifact after recovery_required = %v (code %q), want %q", err, ArtifactErrorCode(err), ArtifactCodeRecoveryRequired)
	}
	if _, err := env.executor.UploadArtifact(t.Context(), bytes.NewReader(env.archive), int64(len(env.archive))); ArtifactErrorCode(err) != ArtifactCodeRecoveryRequired {
		t.Fatalf("UploadArtifact after recovery_required = %v (code %q), want %q", err, ArtifactErrorCode(err), ArtifactCodeRecoveryRequired)
	}
}

func errorsIs(err, target error) bool {
	return err != nil && (err == target || bytes.Contains([]byte(err.Error()), []byte(target.Error())))
}

func TestAcquireArtifactPreCommitFailureStageCleanupOwnership(t *testing.T) {
	// 1. Selection pre-rename failure when THIS acquisition created <version>/ removes the newly created stage.
	createdEnv := newSlimAcquireTestEnv(t)
	faultCreated := &faultSelectionStore{
		Store:     createdEnv.selectionStore,
		commitErr: fmt.Errorf("%w: injected pre-rename temp file failure", selection.ErrSelectionNotPublished),
	}
	createdEnv.executor.mu.Lock()
	createdEnv.executor.selections = faultCreated
	createdEnv.executor.mu.Unlock()

	op, err := createdEnv.executor.UploadArtifact(t.Context(), bytes.NewReader(createdEnv.archive), int64(len(createdEnv.archive)))
	if ArtifactErrorCode(err) != ArtifactCodeInstallFailed || op.Phase != ArtifactPhaseFailed || op.Code != ArtifactCodeInstallFailed {
		t.Fatalf("UploadArtifact() with pre-rename commit failure = %+v, %v; want install_failed", op, err)
	}
	assertNoStageOrSelection(t, createdEnv)

	// 2. Selection pre-rename failure when <version>/ was a pre-existing prepare_update stage NEVER removes the pre-existing stage.
	existingEnv := newSlimAcquireTestEnv(t)
	binary := []byte("#!/bin/sh\necho s2a-acquired-gateway\n")
	archiveWithoutLicense := buildAcquireTarGzip(t,
		acquireArchiveEntry{name: "cli-proxy-api", typeflag: tar.TypeReg, data: binary},
	)
	archiveSum := sha256.Sum256(archiveWithoutLicense)
	if _, err := existingEnv.stageStore.Stage(t.Context(), runtimeupdate.Release{
		Version:       existingEnv.pinnedVersion,
		AssetName:     "CLIProxyAPI_8.0.55_linux_amd64.tar.gz",
		AssetURL:      "https://github.com/router-for-me/CLIProxyAPI/releases/download/v8.0.55/CLIProxyAPI_8.0.55_linux_amd64.tar.gz",
		ArchiveDigest: "sha256:" + hex.EncodeToString(archiveSum[:]),
		ArchiveSize:   int64(len(archiveWithoutLicense)),
	}, func(_ context.Context, _ runtimeupdate.Release, dst io.Writer) error {
		_, writeErr := dst.Write(archiveWithoutLicense)
		return writeErr
	}); err != nil {
		t.Fatal(err)
	}
	faultExisting := &faultSelectionStore{
		Store:     existingEnv.selectionStore,
		commitErr: fmt.Errorf("%w: injected pre-rename temp file failure", selection.ErrSelectionNotPublished),
	}
	existingEnv.executor.mu.Lock()
	existingEnv.executor.selections = faultExisting
	existingEnv.executor.mu.Unlock()

	if _, err := existingEnv.executor.UploadArtifact(t.Context(), bytes.NewReader(existingEnv.archive), int64(len(existingEnv.archive))); ArtifactErrorCode(err) != ArtifactCodeInstallFailed {
		t.Fatalf("UploadArtifact() on pre-existing stage with commit failure = %v, want install_failed", err)
	}
	if _, err := existingEnv.stageStore.ResolveFinalized(existingEnv.pinnedVersion); err != nil {
		t.Fatalf("pre-existing prepare_update stage was removed on pre-commit failure: %v", err)
	}

	// 3. Injected RemoveCreatedStage failure joins both the original pre-commit error and the cleanup error,
	// logs once on both sync UploadArtifact and async AcquireArtifact failure paths, keeps the original
	// operation code (install_failed), leaves the stage on disk, and the next acquisition safely verifies/reuses
	// matching bytes while refusing tampered or conflicting artifacts.
	var logBuf bytes.Buffer
	origLogOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(origLogOut) })

	injectedCleanupErr := fmt.Errorf("injected stage removal failure")
	cleanupFailEnv := newSlimAcquireTestEnv(t, WithAcquisitionStageCleanupHook(func(string) error {
		return injectedCleanupErr
	}))
	injectedCommitErr := fmt.Errorf("%w: injected pre-rename commit failure", selection.ErrSelectionNotPublished)
	faultCleanup := &faultSelectionStore{
		Store:     cleanupFailEnv.selectionStore,
		commitErr: injectedCommitErr,
	}
	cleanupFailEnv.executor.mu.Lock()
	cleanupFailEnv.executor.selections = faultCleanup
	cleanupFailEnv.executor.mu.Unlock()

	cleanupOp, cleanupRetErr := cleanupFailEnv.executor.UploadArtifact(
		t.Context(),
		bytes.NewReader(cleanupFailEnv.archive),
		int64(len(cleanupFailEnv.archive)),
	)
	if ArtifactErrorCode(cleanupRetErr) != ArtifactCodeInstallFailed ||
		cleanupOp.Phase != ArtifactPhaseFailed ||
		cleanupOp.Code != ArtifactCodeInstallFailed {
		t.Fatalf("UploadArtifact() with cleanup failure = %+v, %v (code %q); want install_failed", cleanupOp, cleanupRetErr, ArtifactErrorCode(cleanupRetErr))
	}
	if !errorsIs(cleanupRetErr, injectedCommitErr) || !errorsIs(cleanupRetErr, injectedCleanupErr) {
		t.Fatalf("returned error = %v; want both %v and %v", cleanupRetErr, injectedCommitErr, injectedCleanupErr)
	}
	syncLogged := logBuf.String()
	if !bytes.Contains([]byte(syncLogged), []byte(injectedCommitErr.Error())) ||
		!bytes.Contains([]byte(syncLogged), []byte(injectedCleanupErr.Error())) {
		t.Fatalf("sync failure log = %q; want both %q and %q", syncLogged, injectedCommitErr, injectedCleanupErr)
	}
	if !cleanupFailEnv.stageStore.HasAcquiredStage(cleanupFailEnv.pinnedVersion, cleanupFailEnv.pinnedID) {
		t.Fatal("expected newly created stage to remain on disk when RemoveCreatedStage failed")
	}
	if _, err := os.Stat(filepath.Join(cleanupFailEnv.selectionRoot, "selection.json")); !os.IsNotExist(err) {
		t.Fatalf("selection.json should not exist after pre-rename commit failure: %v", err)
	}

	// Restore working selection store and verify subsequent acquisition safety against the leftover stage:
	// a tampered upload is refused with artifact_mismatch, while a valid upload verifies and commits cleanly.
	cleanupFailEnv.executor.mu.Lock()
	cleanupFailEnv.executor.selections = cleanupFailEnv.selectionStore
	cleanupFailEnv.executor.mu.Unlock()
	tamperedArchive := buildAcquireTarGzip(t,
		acquireArchiveEntry{name: "cli-proxy-api", typeflag: tar.TypeReg, data: []byte("#!/bin/sh\necho tampered\n")},
		acquireArchiveEntry{name: "LICENSE", typeflag: tar.TypeReg, data: []byte("MIT License\n")},
	)
	if _, err := cleanupFailEnv.executor.UploadArtifact(t.Context(), bytes.NewReader(tamperedArchive), int64(len(tamperedArchive))); ArtifactErrorCode(err) != ArtifactCodeArtifactMismatch {
		t.Fatalf("tampered upload against leftover stage = %v (code %q), want %q", err, ArtifactErrorCode(err), ArtifactCodeArtifactMismatch)
	}
	recoveredOp, err := cleanupFailEnv.executor.UploadArtifact(t.Context(), bytes.NewReader(cleanupFailEnv.archive), int64(len(cleanupFailEnv.archive)))
	if err != nil || recoveredOp.Phase != ArtifactPhaseSucceeded {
		t.Fatalf("valid upload against leftover stage = %+v, %v; want succeeded", recoveredOp, err)
	}

	// 4. Async AcquireArtifact path also logs both the primary failure and the stage cleanup failure.
	logBuf.Reset()
	var asyncEnv *acquireTestEnv
	asyncEnv = newSlimAcquireTestEnv(
		t,
		WithOfficialAcquisitionSource(&stubAcquireSource{
			downloadFn: func(_ context.Context, _ string, dst io.Writer, _ runtimeupdate.ProgressFunc) error {
				_, writeErr := dst.Write(asyncEnv.archive)
				return writeErr
			},
		}),
		WithAcquisitionStageCleanupHook(func(string) error {
			return injectedCleanupErr
		}),
	)
	faultAsync := &faultSelectionStore{
		Store:     asyncEnv.selectionStore,
		commitErr: injectedCommitErr,
	}
	asyncEnv.executor.mu.Lock()
	asyncEnv.executor.selections = faultAsync
	asyncEnv.executor.mu.Unlock()

	if _, started, err := asyncEnv.executor.AcquireArtifact(t.Context(), ArtifactAcquireRequest{Source: ArtifactSourceGitHub}); err != nil || !started {
		t.Fatalf("async AcquireArtifact() started=%v, err=%v", started, err)
	}
	asyncSettled := awaitAcquisitionTerminal(t, asyncEnv.executor)
	if asyncSettled.Operation == nil || asyncSettled.Operation.Phase != ArtifactPhaseFailed ||
		asyncSettled.Operation.Code != ArtifactCodeInstallFailed {
		t.Fatalf("async settled = %+v, want failed with %q", asyncSettled, ArtifactCodeInstallFailed)
	}
	asyncLogged := logBuf.String()
	if !bytes.Contains([]byte(asyncLogged), []byte(injectedCommitErr.Error())) ||
		!bytes.Contains([]byte(asyncLogged), []byte(injectedCleanupErr.Error())) {
		t.Fatalf("async failure log = %q; want both %q and %q", asyncLogged, injectedCommitErr, injectedCleanupErr)
	}

	// 5. Async AcquireArtifact post-publication failure + RemoveCreatedStage failure inside AcquireStage logs both errors.
	logBuf.Reset()
	postPubErr := fmt.Errorf("injected post-publication stage failure")
	stageRemoveErr := fmt.Errorf("injected RemoveCreatedStage failure")
	runtimeupdate.SetAcquireStageHooksForTesting(
		func(string) error { return postPubErr },
		func(string) error { return stageRemoveErr },
	)
	defer runtimeupdate.SetAcquireStageHooksForTesting(nil, nil)

	var asyncPostPubEnv *acquireTestEnv
	asyncPostPubEnv = newSlimAcquireTestEnv(
		t,
		WithOfficialAcquisitionSource(&stubAcquireSource{
			downloadFn: func(_ context.Context, _ string, dst io.Writer, _ runtimeupdate.ProgressFunc) error {
				_, writeErr := dst.Write(asyncPostPubEnv.archive)
				return writeErr
			},
		}),
	)
	if _, started, err := asyncPostPubEnv.executor.AcquireArtifact(t.Context(), ArtifactAcquireRequest{Source: ArtifactSourceGitHub}); err != nil || !started {
		t.Fatalf("async post-publication AcquireArtifact() started=%v, err=%v", started, err)
	}
	postPubSettled := awaitAcquisitionTerminal(t, asyncPostPubEnv.executor)
	if postPubSettled.Operation == nil || postPubSettled.Operation.Phase != ArtifactPhaseFailed ||
		postPubSettled.Operation.Code != ArtifactCodeInstallFailed {
		t.Fatalf("async post-publication settled = %+v, want failed with %q", postPubSettled, ArtifactCodeInstallFailed)
	}
	postPubLogged := logBuf.String()
	if !bytes.Contains([]byte(postPubLogged), []byte(postPubErr.Error())) ||
		!bytes.Contains([]byte(postPubLogged), []byte(stageRemoveErr.Error())) {
		t.Fatalf("async post-publication failure log = %q; want both %q and %q", postPubLogged, postPubErr, stageRemoveErr)
	}
	runtimeupdate.SetAcquireStageHooksForTesting(nil, nil)
}

func TestUploadArtifactAdmissionAndAlwaysVerifiesUploadedBytes(t *testing.T) {
	// 1. Full image (bundled pinned binary active) + valid upload -> 409 artifact_already_active without reading body, origin stays bundled, no selection.json.
	fullEnv := newSlimAcquireTestEnv(t)
	bundledBinary := []byte("#!/bin/sh\necho s2a-acquired-gateway\n")
	if err := os.WriteFile(fullEnv.missingBundled, bundledBinary, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := fullEnv.observer.RefreshFrom(fullEnv.missingBundled, fullEnv.manifestPath); err != nil {
		t.Fatal(err)
	}
	reader := &countingFailReader{data: fullEnv.archive}
	if _, err := fullEnv.executor.UploadArtifact(t.Context(), reader, int64(len(fullEnv.archive))); ArtifactErrorCode(err) != ArtifactCodeArtifactAlreadyActive {
		t.Fatalf("full image UploadArtifact() = %v (code %q), want %q", err, ArtifactErrorCode(err), ArtifactCodeArtifactAlreadyActive)
	}
	if reader.reads != 0 {
		t.Fatalf("UploadArtifact() read %d times when bundled artifact was already active; want 0", reader.reads)
	}
	status, err := fullEnv.executor.ArtifactStatus()
	if err != nil || status.Active == nil || status.Active.Origin != ArtifactOriginBundled || status.Required {
		t.Fatalf("full image ArtifactStatus() after rejected upload = %+v, %v", status, err)
	}
	assertNoStageOrSelection(t, fullEnv)

	// 2. Existing matching stage on disk (not yet committed) + tampered upload -> artifact_mismatch.
	stagedEnv := newSlimAcquireTestEnv(t)
	if _, err := stagedEnv.stageStore.AcquireStage(
		t.Context(),
		stagedEnv.pinnedVersion,
		stagedEnv.pinnedID,
		runtimeupdate.CopyUploadArchive(bytes.NewReader(stagedEnv.archive), int64(len(stagedEnv.archive))),
		nil,
		nil,
	); err != nil {
		t.Fatal(err)
	}
	tamperedArchive := buildAcquireTarGzip(t,
		acquireArchiveEntry{name: "cli-proxy-api", typeflag: tar.TypeReg, data: []byte("#!/bin/sh\necho tampered-binary\n")},
		acquireArchiveEntry{name: "LICENSE", typeflag: tar.TypeReg, data: []byte("MIT License\n")},
	)
	if _, err := stagedEnv.executor.UploadArtifact(t.Context(), bytes.NewReader(tamperedArchive), int64(len(tamperedArchive))); ArtifactErrorCode(err) != ArtifactCodeArtifactMismatch {
		t.Fatalf("existing stage + tampered upload = %v (code %q), want %q", err, ArtifactErrorCode(err), ArtifactCodeArtifactMismatch)
	}
	if _, err := os.Stat(filepath.Join(stagedEnv.selectionRoot, "selection.json")); !os.IsNotExist(err) {
		t.Fatalf("selection.json should not exist after tampered upload: %v", err)
	}

	// 3. Valid upload succeeds, and a second upload after successful acquisition returns 409 artifact_already_active without consuming the body.
	op, err := stagedEnv.executor.UploadArtifact(t.Context(), bytes.NewReader(stagedEnv.archive), int64(len(stagedEnv.archive)))
	if err != nil || op.Phase != ArtifactPhaseSucceeded {
		t.Fatalf("valid UploadArtifact() = %+v, %v", op, err)
	}
	secondReader := &countingFailReader{data: stagedEnv.archive}
	if _, err := stagedEnv.executor.UploadArtifact(t.Context(), secondReader, int64(len(stagedEnv.archive))); ArtifactErrorCode(err) != ArtifactCodeArtifactAlreadyActive {
		t.Fatalf("second UploadArtifact() = %v (code %q), want %q", err, ArtifactErrorCode(err), ArtifactCodeArtifactAlreadyActive)
	}
	if secondReader.reads != 0 {
		t.Fatalf("second UploadArtifact() read %d times when acquired artifact was already active; want 0", secondReader.reads)
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
