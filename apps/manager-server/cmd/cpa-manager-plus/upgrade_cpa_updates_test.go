package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	usageadapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identityusage"
	usageapp "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	collectorsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	cpaupdateservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaupdate"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	freshstrategy "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

type upgradeCPAUpdateMockRuntime struct {
	mu sync.Mutex

	status       model.RuntimeObservedStatus
	statusErr    error
	prepareFn    func(context.Context, model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error)
	uploadFn     func(context.Context, model.RuntimePrepareUpdateRequest, io.Reader, int64) (model.RuntimeOperationResult, error)
	activateFn   func(context.Context, model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error)
	observeFn    func(context.Context, model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error)
	lastUploaded []byte

	statusCalls   atomic.Int32
	startCalls    atomic.Int32
	stopCalls     atomic.Int32
	restartCalls  atomic.Int32
	prepareCalls  atomic.Int32
	uploadCalls   atomic.Int32
	activateCalls atomic.Int32
	observeCalls  atomic.Int32
}

var (
	_ runtimeservice.RuntimeClient         = (*upgradeCPAUpdateMockRuntime)(nil)
	_ runtimeservice.PrepareUpdateUploader = (*upgradeCPAUpdateMockRuntime)(nil)
)

func (r *upgradeCPAUpdateMockRuntime) setStatus(st model.RuntimeObservedStatus) {
	r.mu.Lock()
	r.status = st
	r.statusErr = nil
	r.mu.Unlock()
}

func (r *upgradeCPAUpdateMockRuntime) Status(context.Context) (model.RuntimeObservedStatus, error) {
	r.statusCalls.Add(1)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.statusErr != nil {
		return model.RuntimeObservedStatus{}, r.statusErr
	}
	return r.status, nil
}

func (r *upgradeCPAUpdateMockRuntime) Start(_ context.Context, req model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	r.startCalls.Add(1)
	return model.RuntimeOperationResult{
		OperationID:       req.OperationID,
		OperationType:     model.RuntimeOperationStart,
		RuntimeIdentity:   req.ExpectedRuntimeIdentity,
		RuntimeGeneration: req.ExpectedRuntimeGeneration,
		State:             model.RuntimeOperationSucceeded,
	}, nil
}

func (r *upgradeCPAUpdateMockRuntime) Stop(_ context.Context, req model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	r.stopCalls.Add(1)
	return model.RuntimeOperationResult{
		OperationID:       req.OperationID,
		OperationType:     model.RuntimeOperationStop,
		RuntimeIdentity:   req.ExpectedRuntimeIdentity,
		RuntimeGeneration: req.ExpectedRuntimeGeneration,
		State:             model.RuntimeOperationSucceeded,
	}, nil
}

func (r *upgradeCPAUpdateMockRuntime) Restart(_ context.Context, req model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	r.restartCalls.Add(1)
	return model.RuntimeOperationResult{
		OperationID:       req.OperationID,
		OperationType:     model.RuntimeOperationRestart,
		RuntimeIdentity:   req.ExpectedRuntimeIdentity,
		RuntimeGeneration: req.ExpectedRuntimeGeneration,
		State:             model.RuntimeOperationSucceeded,
	}, nil
}

func (r *upgradeCPAUpdateMockRuntime) PrepareUpdate(ctx context.Context, req model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
	r.prepareCalls.Add(1)
	r.mu.Lock()
	fn := r.prepareFn
	r.mu.Unlock()
	if fn != nil {
		return fn(ctx, req)
	}
	return model.RuntimeOperationResult{
		OperationID:       req.OperationID,
		OperationType:     model.RuntimeOperationPrepareUpdate,
		RuntimeIdentity:   req.ExpectedRuntimeIdentity,
		RuntimeGeneration: req.ExpectedRuntimeGeneration,
		State:             model.RuntimeOperationSucceeded,
	}, nil
}

func (r *upgradeCPAUpdateMockRuntime) PrepareUpdateUpload(
	ctx context.Context,
	req model.RuntimePrepareUpdateRequest,
	archive io.Reader,
	contentLength int64,
) (model.RuntimeOperationResult, error) {
	r.uploadCalls.Add(1)
	r.mu.Lock()
	fn := r.uploadFn
	r.mu.Unlock()
	if fn != nil {
		return fn(ctx, req, archive, contentLength)
	}
	readBytes, err := io.ReadAll(archive)
	if err != nil {
		return model.RuntimeOperationResult{}, err
	}
	r.mu.Lock()
	r.lastUploaded = readBytes
	r.mu.Unlock()
	return model.RuntimeOperationResult{
		OperationID:       req.OperationID,
		OperationType:     model.RuntimeOperationPrepareUpdate,
		RuntimeIdentity:   req.ExpectedRuntimeIdentity,
		RuntimeGeneration: req.ExpectedRuntimeGeneration,
		State:             model.RuntimeOperationSucceeded,
	}, nil
}

func (r *upgradeCPAUpdateMockRuntime) ActivateUpdate(ctx context.Context, req model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
	r.activateCalls.Add(1)
	r.mu.Lock()
	fn := r.activateFn
	r.mu.Unlock()
	if fn != nil {
		return fn(ctx, req)
	}
	return model.RuntimeOperationResult{
		OperationID:       req.OperationID,
		OperationType:     model.RuntimeOperationActivateUpdate,
		RuntimeIdentity:   req.ExpectedRuntimeIdentity,
		RuntimeGeneration: req.ExpectedRuntimeGeneration,
		State:             model.RuntimeOperationSucceeded,
	}, nil
}

func (r *upgradeCPAUpdateMockRuntime) ObserveUpdateOperation(ctx context.Context, req model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
	r.observeCalls.Add(1)
	r.mu.Lock()
	fn := r.observeFn
	r.mu.Unlock()
	if fn != nil {
		return fn(ctx, req)
	}
	created := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	completed := created.Add(time.Second)
	return model.RuntimeUpdateOperationObservation{
		OperationID:       req.OperationID,
		OperationType:     req.OperationType,
		RuntimeIdentity:   req.ExpectedRuntimeIdentity,
		RuntimeGeneration: req.ExpectedRuntimeGeneration,
		State:             model.RuntimeOperationSucceeded,
		CreatedAt:         created,
		UpdatedAt:         completed,
		CompletedAt:       &completed,
	}, nil
}

func (r *upgradeCPAUpdateMockRuntime) nonStatusCalls() int32 {
	return r.startCalls.Load() +
		r.stopCalls.Load() +
		r.restartCalls.Load() +
		r.prepareCalls.Load() +
		r.uploadCalls.Load() +
		r.activateCalls.Load() +
		r.observeCalls.Load()
}

func TestUpgradeManagedCPAUpdateAuthorityIdentityLifecycleAndRecovery(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	t.Run("incomplete-startup-operation-fences-prepopulated-cpa-update-service", func(t *testing.T) {
		st, cfg, _ := seedCompletedManagedUpgradeStore(t, "/upg-console", "/upg-gw")
		if _, err := st.SQLDB().Exec(
			`update upgrade_operations set stage = ?, last_forward_stage = ?, verification = '{}', routing_committed = 0, inference_committed = 0 where id = 1`,
			model.UpgradeStageCPAImport,
			model.UpgradeStageCPAImport,
		); err != nil {
			t.Fatal(err)
		}
		rt := &upgradeCPAUpdateMockRuntime{}
		serverApp := httpapi.New(cfg, st, nil)
		serverApp.AppContext().CPAUpdateService = cpaupdateservice.New(st, rt, model.RuntimeModeEmbedded)

		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil, upgradeCanonicalDependencies{
			Runtime: rt,
		}); err == nil {
			t.Fatal("expected attachCompletedUpgradeRouting to fail closed on incomplete stage")
		}

		rec := httptest.NewRecorder()
		serverApp.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/upg-console/api/v2/cpamp/usage-service/runtime/updates", nil))
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "upgrade_not_complete") {
			t.Fatalf("incomplete stage GET runtime/updates = %d %s, want 503 upgrade_not_complete", rec.Code, rec.Body.String())
		}

		if _, err := serverApp.AppContext().CPAUpdateService.Status(context.Background()); err == nil {
			t.Fatal("expected AppContext().CPAUpdateService.Status to fail closed after startup fence")
		}
		_, mutErr := serverApp.AppContext().CPAUpdateService.Prepare(context.Background(), cpaupdateservice.MutationRequest{
			RequestID:                "startup-fenced",
			TargetVersion:            "8.1.0",
			ExpectedActiveArtifactID: model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64)),
		})
		if kind, code, ok := cpaupdateservice.MutationErrorDetails(mutErr); !ok || kind != cpaupdateservice.MutationErrorUnavailable || code != "runtime_mode_invalid" {
			t.Fatalf("Prepare() after startup fence = %v (%s/%s), want unavailable/runtime_mode_invalid", mutErr, kind, code)
		}
		if rt.statusCalls.Load() != 0 || rt.nonStatusCalls() != 0 {
			t.Fatalf("fenced runtime saw status=%d other=%d, want 0/0", rt.statusCalls.Load(), rt.nonStatusCalls())
		}
	})

	t.Run("managed-routes-adapter-progress-and-activation-recovery", func(t *testing.T) {
		mgmtKey := strings.Repeat("b", 64)
		clientKey := "sk-managed-update-key-01"
		cpaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+mgmtKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("X-CPA-Version", "v8.0.55")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"config-version":8,"access":{"api-keys":["` + clientKey + `"]}}`))
		}))
		defer cpaServer.Close()

		st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/upg-console", "/upg-gw")
		keyBytes, _, err := security.LoadOrCreateDataKey("", cfg.DataKeyPath)
		if err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector(keyBytes)
		if err != nil {
			t.Fatal(err)
		}
		sealedDestKey, err := protector.ProtectString(mgmtKey)
		if err != nil {
			t.Fatal(err)
		}
		qualifiedArtifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
		if err != nil {
			t.Fatal(err)
		}
		updatedArtifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("9", 64))
		if updatedArtifact == qualifiedArtifact {
			updatedArtifact = model.RuntimeArtifactID("sha256:" + strings.Repeat("8", 64))
		}
		const expectedIdentity = model.RuntimeIdentity("rt-upgrade-managed")
		sourceIdentity := "external:sha256:" + strings.Repeat("c", 64)
		pluginsInit := false
		rawDest, err := json.Marshal(upgradeDestinationRecord{
			BaseURL:           cpaServer.URL,
			URLProved:         true,
			ManagementKey:     sealedDestKey,
			ServerFingerprint: "external:sha256:" + strings.Repeat("b", 64),
			Attempt:           1,
			OperationID:       upgradeManagedDestinationOperationID(sourceIdentity, 1),
			Intent: upgradeDestinationIntent{
				ExpectedRuntimeIdentity:   expectedIdentity,
				ExpectedRuntimeGeneration: 7,
				ExpectedActiveArtifactID:  qualifiedArtifact,
				SourceIdentity:            sourceIdentity,
				SourceClass:               upgradeManagedDestinationClass,
				APIKeys:                   []string{clientKey},
				PluginsEnabled:            &pluginsInit,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.SQLDB().Exec(
			`insert into settings(key, value, updated_at_ms) values(?, ?, ?)`,
			upgradeImportDestinationSettingKey,
			string(rawDest),
			time.Now().UnixMilli(),
		); err != nil {
			t.Fatal(err)
		}

		checkedAt := time.Now().UTC()
		discoveryBytes, err := json.Marshal(cpaupdateservice.DiscoveryState{
			SchemaVersion: 1,
			LastAttemptAt: checkedAt,
			LastSuccessAt: checkedAt,
			TargetVersion: "8.1.0",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SaveCPAUpdateCheck(context.Background(), discoveryBytes); err != nil {
			t.Fatal(err)
		}

		readyStatus := model.RuntimeObservedStatus{
			Identity:           expectedIdentity,
			Generation:         11,
			ProtocolVersion:    "v1",
			State:              model.RuntimeStateReady,
			CPAObservedVersion: "8.0.55",
			Capabilities: model.RuntimeCapabilities{
				model.RuntimeCapabilityStart,
				model.RuntimeCapabilityStop,
				model.RuntimeCapabilityRestart,
				model.RuntimeCapabilityPrepareUpdate,
				model.RuntimeCapabilityActivateUpdate,
				model.RuntimeCapabilityObserveUpdateOperation,
			},
			ActiveGatewayArtifact: &model.ActiveGatewayArtifact{
				Engine:     "cpa",
				ArtifactID: qualifiedArtifact,
				Version:    "8.0.55",
			},
		}
		rt := &upgradeCPAUpdateMockRuntime{status: readyStatus}
		var adoptionCalls atomic.Int32
		const supervisorToken = "runtime-token-baseline-test"
		bundledStatus := runtimeservice.ArtifactStatus{
			SchemaVersion:    1,
			Pinned:           runtimeservice.ArtifactPinned{Version: "8.0.56", ArtifactID: string(updatedArtifact)},
			Active:           &runtimeservice.ArtifactActive{ArtifactID: string(qualifiedArtifact), Origin: "acquired"},
			BundledAvailable: true, AdoptBaselineAvailable: true,
		}
		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+supervisorToken {
				t.Error("missing private Runtime authorization")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			switch r.Method + " " + r.URL.Path {
			case "GET /v1/runtime/artifact":
				_ = json.NewEncoder(w).Encode(bundledStatus)
			case "POST /v1/runtime/artifact/adopt-baseline":
				adoptionCalls.Add(1)
				var request map[string]string
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["expectedActiveArtifactId"] != string(qualifiedArtifact) {
					t.Errorf("unexpected adoption request: %v, %v", request, err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				result := bundledStatus
				result.AdoptBaselineAvailable = false
				result.Active = &runtimeservice.ArtifactActive{ArtifactID: string(updatedArtifact), Origin: "bundled"}
				_ = json.NewEncoder(w).Encode(result)
			default:
				http.NotFound(w, r)
			}
		}))
		defer supervisor.Close()
		cfg.RuntimeURL = supervisor.URL
		cfg.RuntimeTokenFile = filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(cfg.RuntimeTokenFile, []byte(supervisorToken+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		var desiredStore runtimeservice.DesiredStateStore
		serverApp := httpapi.New(cfg, st, nil)
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, func(context.Context) bool { return true }, upgradeCanonicalDependencies{
			Protector:       protector,
			Runtime:         rt,
			DesiredStateOut: &desiredStore,
		}); err != nil {
			t.Fatalf("attachCompletedUpgradeRouting: %v", err)
		}
		canonicalOwner := desiredStore.(upgradeDesiredStateStore).owner
		adapter := upgradeRuntimeClientAdapter{owner: canonicalOwner}

		ts := httptest.NewServer(serverApp.Handler())
		defer ts.Close()

		callRoute := func(method, subPath, contentType string, reqBody []byte) (int, []byte) {
			t.Helper()
			var bodyReader io.Reader
			if reqBody != nil {
				bodyReader = bytes.NewReader(reqBody)
			}
			req, reqErr := http.NewRequest(method, ts.URL+"/upg-console/api/v2/cpamp/usage-service/runtime/updates"+subPath, bodyReader)
			if reqErr != nil {
				t.Fatal(reqErr)
			}
			req.Header.Set("Authorization", "Bearer "+adminKey)
			if contentType != "" {
				req.Header.Set("Content-Type", contentType)
			}
			res, doErr := http.DefaultClient.Do(req)
			if doErr != nil {
				t.Fatal(doErr)
			}
			defer func() { _ = res.Body.Close() }()
			respBytes, readErr := io.ReadAll(res.Body)
			if readErr != nil {
				t.Fatal(readErr)
			}
			return res.StatusCode, respBytes
		}

		// 1. Status and Check enforce verified Managed identity and report actionable update.
		for _, checkCall := range []struct {
			method string
			suffix string
		}{
			{method: http.MethodGet, suffix: ""},
			{method: http.MethodPost, suffix: "/check"},
		} {
			code, rawResp := callRoute(checkCall.method, checkCall.suffix, "", nil)
			if code != http.StatusOK {
				t.Fatalf("%s %s = %d %s, want 200", checkCall.method, checkCall.suffix, code, rawResp)
			}
			var gotStatus cpaupdateservice.Status
			if err := json.Unmarshal(rawResp, &gotStatus); err != nil {
				t.Fatal(err)
			}
			if gotStatus.Mode != model.RuntimeModeEmbedded ||
				gotStatus.State != cpaupdateservice.StateUpdateAvailable ||
				gotStatus.CurrentVersion != "8.0.55" ||
				gotStatus.TargetVersion != "8.1.0" ||
				gotStatus.ActiveArtifactID != string(qualifiedArtifact) ||
				!gotStatus.PrepareSupported ||
				!gotStatus.ActivateSupported ||
				!gotStatus.Actionable {
				t.Fatalf("%s %s status = %+v", checkCall.method, checkCall.suffix, gotStatus)
			}
			if baseline := gotStatus.Baseline; baseline == nil || baseline.PinnedVersion != "8.0.56" || baseline.PinnedArtifactID != string(updatedArtifact) || !baseline.BundledAvailable || !baseline.AdoptBaselineAvailable || baseline.BaselineUpgradeAvailable {
				t.Fatalf("canonical baseline status = %+v", baseline)
			}
		}
		adoptRequest, err := json.Marshal(map[string]string{"expected_active_artifact_id": string(qualifiedArtifact)})
		if err != nil {
			t.Fatal(err)
		}
		code, raw := callRoute(http.MethodPost, "/adopt-baseline", "application/json", adoptRequest)
		var adopted cpaupdateservice.AdoptBaselineResult
		if code != http.StatusOK || json.Unmarshal(raw, &adopted) != nil || adopted.ActiveArtifactID != string(updatedArtifact) || adopted.Baseline.AdoptBaselineAvailable || adoptionCalls.Load() != 1 {
			t.Fatalf("canonical adoption = HTTP %d, %s, RPCs=%d", code, raw, adoptionCalls.Load())
		}

		// 2. Prepare and PrepareUpload forward verified ExpectedRuntimeIdentity; progress observations do not block behind prepare.
		prepCode, prepResp := callRoute(
			http.MethodPost,
			"/prepare",
			"application/json",
			[]byte(`{"request_id":"prep-req-1","target_version":"8.1.0","expected_active_artifact_id":"`+string(qualifiedArtifact)+`"}`),
		)
		if prepCode != http.StatusOK || !strings.Contains(string(prepResp), `"state":"succeeded"`) {
			t.Fatalf("POST /prepare = %d %s, want 200 succeeded", prepCode, prepResp)
		}

		uploadEntered := make(chan struct{})
		releaseUpload := make(chan struct{})
		rt.mu.Lock()
		rt.uploadFn = func(_ context.Context, req model.RuntimePrepareUpdateRequest, archive io.Reader, _ int64) (model.RuntimeOperationResult, error) {
			if req.ExpectedRuntimeIdentity != expectedIdentity {
				t.Errorf("PrepareUpdateUpload ExpectedRuntimeIdentity = %q, want %q", req.ExpectedRuntimeIdentity, expectedIdentity)
			}
			data, readErr := io.ReadAll(archive)
			if readErr != nil {
				return model.RuntimeOperationResult{}, readErr
			}
			rt.mu.Lock()
			rt.lastUploaded = data
			rt.mu.Unlock()
			close(uploadEntered)
			<-releaseUpload
			return model.RuntimeOperationResult{
				OperationID:       req.OperationID,
				OperationType:     model.RuntimeOperationPrepareUpdate,
				RuntimeIdentity:   req.ExpectedRuntimeIdentity,
				RuntimeGeneration: req.ExpectedRuntimeGeneration,
				State:             model.RuntimeOperationSucceeded,
			}, nil
		}
		rt.observeFn = func(_ context.Context, req model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
			now := time.Date(2026, 9, 18, 9, 5, 0, 0, time.UTC)
			return model.RuntimeUpdateOperationObservation{
				OperationID:       req.OperationID,
				OperationType:     req.OperationType,
				RuntimeIdentity:   req.ExpectedRuntimeIdentity,
				RuntimeGeneration: req.ExpectedRuntimeGeneration,
				State:             model.RuntimeOperationRunning,
				Phase:             "verifying",
				ReceivedBytes:     128,
				TotalBytes:        256,
				CreatedAt:         now,
				UpdatedAt:         now,
			}, nil
		}
		rt.mu.Unlock()

		archiveBytes := []byte("fake-tar-gz-stream-content")
		uploadDone := make(chan struct {
			code int
			body []byte
		}, 1)
		go func() {
			c, b := callRoute(
				http.MethodPost,
				"/prepare-upload?request_id=upload-req-1&target_version=8.1.0&expected_active_artifact_id="+string(qualifiedArtifact),
				"application/gzip",
				archiveBytes,
			)
			uploadDone <- struct {
				code int
				body []byte
			}{code: c, body: b}
		}()

		select {
		case <-uploadEntered:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for PrepareUpdateUpload to enter")
		}

		// While PrepareUpdateUpload is blocked, ObservePrepare and Status must complete without waiting.
		obsDone := make(chan struct {
			code int
			body []byte
		}, 1)
		go func() {
			c, b := callRoute(http.MethodGet, "/operations/prepare?request_id=upload-req-1&target_version=8.1.0", "", nil)
			obsDone <- struct {
				code int
				body []byte
			}{code: c, body: b}
		}()
		select {
		case obs := <-obsDone:
			if obs.code != http.StatusOK {
				t.Fatalf("concurrent GET /operations/prepare = %d %s, want 200", obs.code, obs.body)
			}
			var gotObs cpaupdateservice.ObservationResult
			if err := json.Unmarshal(obs.body, &gotObs); err != nil {
				t.Fatal(err)
			}
			if gotObs.State != cpaupdateservice.ObservationStateRunning ||
				gotObs.StagePhase != "verifying" ||
				gotObs.ReceivedBytes != 128 ||
				gotObs.TotalBytes != 256 {
				t.Fatalf("concurrent prepare progress = %+v", gotObs)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("GET /operations/prepare blocked behind in-flight prepare-upload")
		}

		statusWhilePreparingCode, _ := callRoute(http.MethodGet, "", "", nil)
		if statusWhilePreparingCode != http.StatusOK {
			t.Fatalf("concurrent GET /runtime/updates during prepare = %d, want 200", statusWhilePreparingCode)
		}

		close(releaseUpload)
		upOut := <-uploadDone
		if upOut.code != http.StatusOK || !strings.Contains(string(upOut.body), `"state":"succeeded"`) {
			t.Fatalf("POST /prepare-upload = %d %s, want 200 succeeded", upOut.code, upOut.body)
		}
		rt.mu.Lock()
		gotUploaded := append([]byte(nil), rt.lastUploaded...)
		rt.uploadFn = nil
		rt.observeFn = nil
		rt.mu.Unlock()
		if !bytes.Equal(gotUploaded, archiveBytes) {
			t.Fatalf("uploaded archive = %q, want %q", gotUploaded, archiveBytes)
		}

		// 3. Activate serializes on runtimeLifecycleMu while preserving non-blocking ObserveActivate and post-activation recovery.
		holdUnlock, err := canonicalOwner.runtimeLifecycleMu.Lock(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var activateWaitOnce sync.Once
		activateWaiting := make(chan struct{})
		canonicalOwner.runtimeLifecycleMu.onWait = func() {
			activateWaitOnce.Do(func() { close(activateWaiting) })
		}

		activateEntered := make(chan struct{})
		releaseActivate := make(chan struct{})
		rt.mu.Lock()
		rt.activateFn = func(_ context.Context, req model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
			close(activateEntered)
			<-releaseActivate
			return model.RuntimeOperationResult{
				OperationID:       req.OperationID,
				OperationType:     model.RuntimeOperationActivateUpdate,
				RuntimeIdentity:   req.ExpectedRuntimeIdentity,
				RuntimeGeneration: req.ExpectedRuntimeGeneration,
				State:             model.RuntimeOperationSucceeded,
			}, nil
		}
		rt.observeFn = func(_ context.Context, req model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
			now := time.Date(2026, 9, 18, 9, 10, 0, 0, time.UTC)
			return model.RuntimeUpdateOperationObservation{
				OperationID:       req.OperationID,
				OperationType:     req.OperationType,
				RuntimeIdentity:   req.ExpectedRuntimeIdentity,
				RuntimeGeneration: req.ExpectedRuntimeGeneration,
				State:             model.RuntimeOperationRunning,
				CreatedAt:         now,
				UpdatedAt:         now,
			}, nil
		}
		rt.mu.Unlock()

		activateDone := make(chan struct {
			code int
			body []byte
		}, 1)
		go func() {
			c, b := callRoute(
				http.MethodPost,
				"/activate",
				"application/json",
				[]byte(`{"request_id":"act-req-1","target_version":"8.1.0","expected_active_artifact_id":"`+string(qualifiedArtifact)+`"}`),
			)
			activateDone <- struct {
				code int
				body []byte
			}{code: c, body: b}
		}()

		select {
		case <-activateWaiting:
		case early := <-activateDone:
			t.Fatalf("POST /activate returned early without blocking on runtimeLifecycleMu: %d %s", early.code, early.body)
		case <-time.After(3 * time.Second):
			t.Fatal("ActivateUpdate did not serialize on runtimeLifecycleMu")
		}
		if rt.activateCalls.Load() != 0 {
			t.Fatalf("ActivateUpdate invoked while runtimeLifecycleMu held: calls=%d", rt.activateCalls.Load())
		}
		holdUnlock()

		select {
		case <-activateEntered:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for ActivateUpdate to enter after unlock")
		}

		// While ActivateUpdate holds runtimeLifecycleMu and CPA transitions through starting/offline/unknown,
		// ObserveActivate must still succeed without blocking.
		for _, transientState := range []model.RuntimeState{
			model.RuntimeStateStarting,
			model.RuntimeStateOffline,
			model.RuntimeStateUnknown,
		} {
			transient := readyStatus
			transient.State = transientState
			transient.ActiveGatewayArtifact = nil
			transient.CPAObservedVersion = ""
			rt.setStatus(transient)

			obsActDone := make(chan struct {
				code int
				body []byte
			}, 1)
			go func() {
				c, b := callRoute(http.MethodGet, "/operations/activate?request_id=act-req-1&target_version=8.1.0", "", nil)
				obsActDone <- struct {
					code int
					body []byte
				}{code: c, body: b}
			}()
			select {
			case obsAct := <-obsActDone:
				if obsAct.code != http.StatusOK || !strings.Contains(string(obsAct.body), `"state":"running"`) {
					t.Fatalf("ObserveActivate during %s = %d %s, want 200 running", transientState, obsAct.code, obsAct.body)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("ObserveActivate blocked during %s while ActivateUpdate held runtimeLifecycleMu", transientState)
			}
		}

		// Complete activation onto the new artifact version 8.1.0.
		postActivateStatus := readyStatus
		postActivateStatus.ActiveGatewayArtifact = &model.ActiveGatewayArtifact{
			Engine:     "cpa",
			ArtifactID: updatedArtifact,
			Version:    "8.1.0",
		}
		postActivateStatus.CPAObservedVersion = "8.1.0"
		rt.setStatus(postActivateStatus)
		rt.mu.Lock()
		rt.observeFn = nil
		rt.activateFn = nil
		rt.mu.Unlock()
		close(releaseActivate)

		actOut := <-activateDone
		canonicalOwner.runtimeLifecycleMu.onWait = nil
		if actOut.code != http.StatusOK || !strings.Contains(string(actOut.body), `"state":"succeeded"`) {
			t.Fatalf("POST /activate = %d %s, want 200 succeeded", actOut.code, actOut.body)
		}

		// Post-activation Status, ObserveActivate, and AlreadyApplied retry recovery must all succeed on the new artifact.
		upToDateCode, upToDateBody := callRoute(http.MethodGet, "", "", nil)
		if upToDateCode != http.StatusOK || !strings.Contains(string(upToDateBody), `"state":"up_to_date"`) {
			t.Fatalf("post-activate GET /runtime/updates = %d %s, want 200 up_to_date", upToDateCode, upToDateBody)
		}
		obsDoneCode, obsDoneBody := callRoute(http.MethodGet, "/operations/activate?request_id=act-req-1&target_version=8.1.0", "", nil)
		if obsDoneCode != http.StatusOK || !strings.Contains(string(obsDoneBody), `"state":"succeeded"`) {
			t.Fatalf("post-activate GET /operations/activate = %d %s, want 200 succeeded", obsDoneCode, obsDoneBody)
		}
		activateCallsBeforeRetry := rt.activateCalls.Load()
		retryCode, retryBody := callRoute(
			http.MethodPost,
			"/activate",
			"application/json",
			[]byte(`{"request_id":"act-req-1","target_version":"8.1.0","expected_active_artifact_id":"`+string(qualifiedArtifact)+`"}`),
		)
		if retryCode != http.StatusOK ||
			!strings.Contains(string(retryBody), `"already_applied":true`) ||
			rt.activateCalls.Load() != activateCallsBeforeRetry {
			t.Fatalf("retry POST /activate = %d %s (calls=%d), want 200 already_applied without extra RPC", retryCode, retryBody, rt.activateCalls.Load())
		}

		// Stopped desired lifecycle refuses direct adapter ActivateUpdate with operation_state_conflict.
		rt.setStatus(readyStatus)
		if _, err := st.SetEmbeddedRuntimeDesiredLifecycle(context.Background(), model.EmbeddedRuntimeDesiredStopped); err != nil {
			t.Fatal(err)
		}
		_, stoppedActErr := adapter.ActivateUpdate(context.Background(), model.RuntimeActivateUpdateRequest{
			RuntimeMutationRequest: model.RuntimeMutationRequest{
				OperationID:               "stopped-guard-op",
				ExpectedRuntimeIdentity:   expectedIdentity,
				ExpectedRuntimeGeneration: 11,
			},
			ExpectedActiveArtifactID: qualifiedArtifact,
			TargetVersion:            "8.1.0",
		})
		if code, ok := runtimeservice.ErrorCode(stoppedActErr); !ok || code != runtimeservice.ProtocolErrorOperationStateConflict {
			t.Fatalf("ActivateUpdate when desired=stopped err = %v, want operation_state_conflict", stoppedActErr)
		}
		if _, err := st.SetEmbeddedRuntimeDesiredLifecycle(context.Background(), model.EmbeddedRuntimeDesiredRunning); err != nil {
			t.Fatal(err)
		}

		// 4. Identity mismatch, wrong request identity, missing destination, and invalid UpgradeOperation fail closed.
		foreignStatus := readyStatus
		foreignStatus.Identity = "rt-foreign-unexpected"
		rt.setStatus(foreignStatus)
		beforeNonStatus := rt.nonStatusCalls()

		for _, route := range []struct {
			method      string
			suffix      string
			contentType string
			body        []byte
		}{
			{method: http.MethodGet, suffix: ""},
			{method: http.MethodPost, suffix: "/check"},
			{method: http.MethodPost, suffix: "/prepare", contentType: "application/json", body: []byte(`{"request_id":"f1","target_version":"8.1.0","expected_active_artifact_id":"` + string(qualifiedArtifact) + `"}`)},
			{method: http.MethodPost, suffix: "/prepare-upload?request_id=f2&target_version=8.1.0&expected_active_artifact_id=" + string(qualifiedArtifact), contentType: "application/gzip", body: archiveBytes},
			{method: http.MethodPost, suffix: "/activate", contentType: "application/json", body: []byte(`{"request_id":"f3","target_version":"8.1.0","expected_active_artifact_id":"` + string(qualifiedArtifact) + `"}`)},
			{method: http.MethodGet, suffix: "/operations/prepare?request_id=f1&target_version=8.1.0"},
			{method: http.MethodGet, suffix: "/operations/activate?request_id=f3&target_version=8.1.0"},
		} {
			code, rawResp := callRoute(route.method, route.suffix, route.contentType, route.body)
			if code != http.StatusServiceUnavailable {
				t.Fatalf("foreign identity %s %s = %d %s, want 503", route.method, route.suffix, code, rawResp)
			}
		}

		assertAdapterRejected := func(label string, expectedID model.RuntimeIdentity) {
			t.Helper()
			mutReq := model.RuntimeMutationRequest{
				OperationID:               "adapter-check",
				ExpectedRuntimeIdentity:   expectedID,
				ExpectedRuntimeGeneration: 11,
			}
			if _, err := adapter.Start(context.Background(), mutReq); err == nil {
				t.Fatalf("%s: adapter.Start unexpectedly succeeded", label)
			}
			if _, err := adapter.Stop(context.Background(), mutReq); err == nil {
				t.Fatalf("%s: adapter.Stop unexpectedly succeeded", label)
			}
			if _, err := adapter.Restart(context.Background(), mutReq); err == nil {
				t.Fatalf("%s: adapter.Restart unexpectedly succeeded", label)
			}
			prepReq := model.RuntimePrepareUpdateRequest{
				RuntimeMutationRequest:   mutReq,
				ExpectedActiveArtifactID: qualifiedArtifact,
				TargetVersion:            "8.1.0",
			}
			if _, err := adapter.PrepareUpdate(context.Background(), prepReq); err == nil {
				t.Fatalf("%s: adapter.PrepareUpdate unexpectedly succeeded", label)
			}
			if _, err := adapter.PrepareUpdateUpload(context.Background(), prepReq, bytes.NewReader(archiveBytes), int64(len(archiveBytes))); err == nil {
				t.Fatalf("%s: adapter.PrepareUpdateUpload unexpectedly succeeded", label)
			}
			if _, err := adapter.ActivateUpdate(context.Background(), model.RuntimeActivateUpdateRequest{
				RuntimeMutationRequest:   mutReq,
				ExpectedActiveArtifactID: qualifiedArtifact,
				TargetVersion:            "8.1.0",
			}); err == nil {
				t.Fatalf("%s: adapter.ActivateUpdate unexpectedly succeeded", label)
			}
			if _, err := adapter.ObserveUpdateOperation(context.Background(), model.RuntimeObserveUpdateOperationRequest{
				RuntimeMutationRequest: mutReq,
				OperationType:          model.RuntimeOperationPrepareUpdate,
				TargetVersion:          "8.1.0",
			}); err == nil {
				t.Fatalf("%s: adapter.ObserveUpdateOperation unexpectedly succeeded", label)
			}
		}

		if _, err := adapter.Status(context.Background()); err == nil {
			t.Fatal("foreign identity adapter.Status unexpectedly succeeded")
		}
		assertAdapterRejected("foreign live identity", expectedIdentity)
		if rt.nonStatusCalls() != beforeNonStatus {
			t.Fatalf("foreign identity invoked %d non-status RPCs, want 0", rt.nonStatusCalls()-beforeNonStatus)
		}

		// Live identity matches, but caller supplies a mismatched ExpectedRuntimeIdentity on adapter methods.
		rt.setStatus(readyStatus)
		assertAdapterRejected("mismatched request identity", "rt-wrong-request-identity")
		if rt.nonStatusCalls() != beforeNonStatus {
			t.Fatalf("mismatched request identity invoked %d non-status RPCs, want 0", rt.nonStatusCalls()-beforeNonStatus)
		}

		// Missing Managed destination record fails closed on routes and adapter.
		if _, err := st.SQLDB().Exec(`delete from settings where key = ?`, upgradeImportDestinationSettingKey); err != nil {
			t.Fatal(err)
		}
		missingDestCode, _ := callRoute(http.MethodGet, "", "", nil)
		if missingDestCode != http.StatusServiceUnavailable {
			t.Fatalf("missing destination GET /runtime/updates = %d, want 503", missingDestCode)
		}
		assertAdapterRejected("missing destination", expectedIdentity)
		if rt.nonStatusCalls() != beforeNonStatus {
			t.Fatalf("missing destination invoked %d non-status RPCs, want 0", rt.nonStatusCalls()-beforeNonStatus)
		}

		// Restore destination, then invalidate UpgradeOperation provenance: canonical routes and direct service fail closed.
		if _, err := st.SQLDB().Exec(
			`insert into settings(key, value, updated_at_ms) values(?, ?, ?)`,
			upgradeImportDestinationSettingKey,
			string(rawDest),
			time.Now().UnixMilli(),
		); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SQLDB().Exec(`update upgrade_operations set backup_manifest_sha256 = '' where id = 1`); err != nil {
			t.Fatal(err)
		}
		statusAfterInvalidOp := rt.statusCalls.Load()
		invalidRouteCode, invalidRouteBody := callRoute(http.MethodGet, "", "", nil)
		if invalidRouteCode != http.StatusServiceUnavailable || !strings.Contains(string(invalidRouteBody), "upgrade_authority_unavailable") {
			t.Fatalf("invalid UpgradeOperation GET /runtime/updates = %d %s, want 503 upgrade_authority_unavailable", invalidRouteCode, invalidRouteBody)
		}
		svc := serverApp.AppContext().CPAUpdateService
		if _, err := svc.Status(context.Background()); err == nil {
			t.Fatal("invalid UpgradeOperation svc.Status unexpectedly succeeded")
		}
		if _, err := svc.Check(context.Background()); err == nil {
			t.Fatal("invalid UpgradeOperation svc.Check unexpectedly succeeded")
		}
		_, invalidPrepErr := svc.Prepare(context.Background(), cpaupdateservice.MutationRequest{
			RequestID:                "inv-1",
			TargetVersion:            "8.1.0",
			ExpectedActiveArtifactID: qualifiedArtifact,
		})
		if kind, code, ok := cpaupdateservice.MutationErrorDetails(invalidPrepErr); !ok || kind != cpaupdateservice.MutationErrorUnavailable || code != "runtime_mode_invalid" {
			t.Fatalf("invalid UpgradeOperation svc.Prepare = %v (%s/%s), want unavailable/runtime_mode_invalid", invalidPrepErr, kind, code)
		}
		_, invalidObsErr := svc.ObservePrepare(context.Background(), cpaupdateservice.ObservationRequest{
			RequestID:     "inv-1",
			TargetVersion: "8.1.0",
		})
		if kind, code, ok := cpaupdateservice.ObservationErrorDetails(invalidObsErr); !ok || kind != cpaupdateservice.ObservationErrorUnavailable || code != "runtime_mode_invalid" {
			t.Fatalf("invalid UpgradeOperation svc.ObservePrepare = %v (%s/%s), want unavailable/runtime_mode_invalid", invalidObsErr, kind, code)
		}
		if rt.statusCalls.Load() != statusAfterInvalidOp || rt.nonStatusCalls() != beforeNonStatus {
			t.Fatalf("invalid UpgradeOperation contacted runtime: statusDelta=%d nonStatusDelta=%d",
				rt.statusCalls.Load()-statusAfterInvalidOp, rt.nonStatusCalls()-beforeNonStatus)
		}
	})
}

func TestUpgradeExternalCPAUpdateReadOnlyFencedCredentialsAndEmbeddedEnvIsolation(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	extMgmtKey := strings.Repeat("a", 64)
	var extRequests atomic.Int32
	var extHandler atomic.Value
	extHandler.Store(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+extMgmtKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.55")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"access":{"api-keys":["sk-ext-1"]}}`))
	})
	extCPAServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		extRequests.Add(1)
		extHandler.Load().(func(http.ResponseWriter, *http.Request))(w, r)
	}))
	defer extCPAServer.Close()

	var embeddedSupervisorHits atomic.Int32
	embeddedSupervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		embeddedSupervisorHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer embeddedSupervisor.Close()

	st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPAServer.URL)
	tokenFile := filepath.Join(t.TempDir(), "runtime.token")
	if err := os.WriteFile(tokenFile, []byte("unowned-embedded-supervisor-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeURL = embeddedSupervisor.URL
	cfg.RuntimeTokenFile = tokenFile

	checkedAt := time.Now().UTC()
	discoveryBytes, err := json.Marshal(cpaupdateservice.DiscoveryState{
		SchemaVersion: 1,
		LastAttemptAt: checkedAt,
		LastSuccessAt: checkedAt,
		TargetVersion: "8.1.0",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveCPAUpdateCheck(context.Background(), discoveryBytes); err != nil {
		t.Fatal(err)
	}

	// Simulate main.go having wired EmbeddedClient before attachCompletedUpgradeRouting runs.
	rawEmbeddedClient := runtimeservice.NewEmbeddedClientWithTokenSource(
		cfg.RuntimeURL,
		runtimeservice.NewFileRuntimeTokenSource(cfg.RuntimeTokenFile),
	)
	serverApp := httpapi.New(cfg, st, nil)
	serverApp.AppContext().CPAUpdateService = cpaupdateservice.New(st, rawEmbeddedClient, model.RuntimeModeEmbedded)

	var desiredStore runtimeservice.DesiredStateStore
	if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil, upgradeCanonicalDependencies{
		Runtime:         rawEmbeddedClient,
		DesiredStateOut: &desiredStore,
	}); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting: %v", err)
	}
	canonicalOwner := desiredStore.(upgradeDesiredStateStore).owner
	if canonicalOwner.runtime != nil {
		t.Fatal("External Upgrade retained non-nil Embedded runtime client")
	}
	adapter := upgradeRuntimeClientAdapter{owner: canonicalOwner}

	ts := httptest.NewServer(serverApp.Handler())
	defer ts.Close()

	callExt := func(method, path, contentType string, reqBody []byte) (int, []byte) {
		t.Helper()
		var bodyReader io.Reader
		if reqBody != nil {
			bodyReader = bytes.NewReader(reqBody)
		}
		req, reqErr := http.NewRequest(method, ts.URL+path, bodyReader)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		req.Header.Set("Authorization", "Bearer "+adminKey)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		res, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			t.Fatal(doErr)
		}
		defer func() { _ = res.Body.Close() }()
		respBytes, readErr := io.ReadAll(res.Body)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return res.StatusCode, respBytes
	}

	// 1. Status and Check report mode=external, state=managed_externally via External CPA; mutations/observations return 409 managed_externally.
	for _, checkCall := range []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: "/ext-console/api/v2/cpamp/usage-service/runtime/updates"},
		{method: http.MethodPost, path: "/ext-console/api/v2/cpamp/usage-service/runtime/updates/check"},
	} {
		code, rawResp := callExt(checkCall.method, checkCall.path, "", nil)
		if code != http.StatusOK {
			t.Fatalf("%s %s = %d %s, want 200", checkCall.method, checkCall.path, code, rawResp)
		}
		var got cpaupdateservice.Status
		if err := json.Unmarshal(rawResp, &got); err != nil {
			t.Fatal(err)
		}
		if got.Mode != model.RuntimeModeExternal ||
			got.State != cpaupdateservice.StateManagedExternally ||
			got.CurrentVersion != "v8.0.55" ||
			got.Actionable ||
			got.PrepareSupported ||
			got.ActivateSupported ||
			got.ActiveArtifactID != "" {
			t.Fatalf("%s %s = %+v", checkCall.method, checkCall.path, got)
		}
	}

	extBeforeMut := extRequests.Load()
	artifactHex := "sha256:" + strings.Repeat("a", 64)
	for _, blocked := range []struct {
		method      string
		path        string
		contentType string
		body        []byte
	}{
		{
			method:      http.MethodPost,
			path:        "/ext-console/api/v2/cpamp/usage-service/runtime/updates/prepare",
			contentType: "application/json",
			body:        []byte(`{"request_id":"ext-p1","target_version":"8.1.0","expected_active_artifact_id":"` + artifactHex + `"}`),
		},
		{
			method:      http.MethodPost,
			path:        "/ext-console/api/v2/cpamp/usage-service/runtime/updates/prepare-upload?request_id=ext-u1&target_version=8.1.0&expected_active_artifact_id=" + artifactHex,
			contentType: "application/gzip",
			body:        []byte("archive"),
		},
		{
			method:      http.MethodPost,
			path:        "/ext-console/api/v2/cpamp/usage-service/runtime/updates/activate",
			contentType: "application/json",
			body:        []byte(`{"request_id":"ext-a1","target_version":"8.1.0","expected_active_artifact_id":"` + artifactHex + `"}`),
		},
		{
			method: http.MethodGet,
			path:   "/ext-console/api/v2/cpamp/usage-service/runtime/updates/operations/prepare?request_id=ext-p1&target_version=8.1.0",
		},
		{
			method: http.MethodGet,
			path:   "/ext-console/api/v2/cpamp/usage-service/runtime/updates/operations/activate?request_id=ext-a1&target_version=8.1.0",
		},
	} {
		code, rawResp := callExt(blocked.method, blocked.path, blocked.contentType, blocked.body)
		if code != http.StatusConflict || !strings.Contains(string(rawResp), `"managed_externally"`) {
			t.Fatalf("%s %s = %d %s, want 409 managed_externally", blocked.method, blocked.path, code, rawResp)
		}
	}
	if extRequests.Load() != extBeforeMut {
		t.Fatalf("External mutations/observations contacted External CPA %d times, want 0", extRequests.Load()-extBeforeMut)
	}

	// Direct adapter calls in External mode reject mutations and update-operation observations.
	extMutReq := model.RuntimeMutationRequest{
		OperationID:               "ext-adapter-op",
		ExpectedRuntimeIdentity:   "rt-any",
		ExpectedRuntimeGeneration: 1,
	}
	if _, err := adapter.PrepareUpdate(context.Background(), model.RuntimePrepareUpdateRequest{
		RuntimeMutationRequest:   extMutReq,
		ExpectedActiveArtifactID: model.RuntimeArtifactID(artifactHex),
		TargetVersion:            "8.1.0",
	}); !errors.Is(err, runtimeservice.ErrRuntimeMutationUnsupported) {
		t.Fatalf("External adapter.PrepareUpdate err = %v, want ErrRuntimeMutationUnsupported", err)
	}
	if _, err := adapter.PrepareUpdateUpload(context.Background(), model.RuntimePrepareUpdateRequest{
		RuntimeMutationRequest:   extMutReq,
		ExpectedActiveArtifactID: model.RuntimeArtifactID(artifactHex),
		TargetVersion:            "8.1.0",
	}, strings.NewReader("archive"), 7); !errors.Is(err, runtimeservice.ErrRuntimeMutationUnsupported) {
		t.Fatalf("External adapter.PrepareUpdateUpload err = %v, want ErrRuntimeMutationUnsupported", err)
	}
	if _, err := adapter.ActivateUpdate(context.Background(), model.RuntimeActivateUpdateRequest{
		RuntimeMutationRequest:   extMutReq,
		ExpectedActiveArtifactID: model.RuntimeArtifactID(artifactHex),
		TargetVersion:            "8.1.0",
	}); !errors.Is(err, runtimeservice.ErrRuntimeMutationUnsupported) {
		t.Fatalf("External adapter.ActivateUpdate err = %v, want ErrRuntimeMutationUnsupported", err)
	}
	if _, err := adapter.ObserveUpdateOperation(context.Background(), model.RuntimeObserveUpdateOperationRequest{
		RuntimeMutationRequest: extMutReq,
		OperationType:          model.RuntimeOperationPrepareUpdate,
		TargetVersion:          "8.1.0",
	}); !errors.Is(err, runtimeservice.ErrRuntimeObservationUnsupported) {
		t.Fatalf("External adapter.ObserveUpdateOperation err = %v, want ErrRuntimeObservationUnsupported", err)
	}
	if embeddedSupervisorHits.Load() != 0 {
		t.Fatalf("Embedded Supervisor contacted %d times on External Upgrade, want 0", embeddedSupervisorHits.Load())
	}

	// 2. Fenced credential loading refuses drifted legacy setup row without contacting drifted or committed CPA.
	var driftedHits atomic.Int32
	driftedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		driftedHits.Add(1)
		w.Header().Set("X-CPA-Version", "v9.9.9")
		w.WriteHeader(http.StatusOK)
	}))
	defer driftedServer.Close()

	if err := st.SaveSetup(context.Background(), model.Setup{
		CPAUpstreamURL: driftedServer.URL,
		ManagementKey:  strings.Repeat("d", 64),
	}); err != nil {
		t.Fatal(err)
	}
	extBeforeDrift := extRequests.Load()
	driftCode, _ := callExt(http.MethodGet, "/ext-console/api/v2/cpamp/usage-service/runtime/updates", "", nil)
	if driftCode != http.StatusServiceUnavailable {
		t.Fatalf("drifted setup GET /runtime/updates = %d, want 503", driftCode)
	}
	if driftedHits.Load() != 0 || extRequests.Load() != extBeforeDrift {
		t.Fatalf("drifted setup hits: drifted=%d ext=%d, want 0/0", driftedHits.Load(), extRequests.Load()-extBeforeDrift)
	}
	if _, err := st.SQLDB().Exec(`delete from settings where key = 'setup'`); err != nil {
		t.Fatal(err)
	}

	// Hold the credential gate explicitly: status must wait for the gate
	// before loading credentials or contacting the upstream.
	if !canonicalOwner.workspace.credentialGate.acquire(context.Background()) {
		t.Fatal("could not acquire credential gate")
	}
	var releaseOnce sync.Once
	releaseGate := func() { releaseOnce.Do(canonicalOwner.workspace.credentialGate.release) }
	defer releaseGate()
	var parkedOnce sync.Once
	parked := make(chan struct{})
	canonicalOwner.workspace.credentialGate.setOnWait(func() {
		parkedOnce.Do(func() { close(parked) })
	})
	beforeReject := extRequests.Load()
	probeDone := make(chan int, 1)
	go func() {
		c, _ := callExt(http.MethodGet, "/ext-console/api/v2/cpamp/usage-service/runtime/updates", "", nil)
		probeDone <- c
	}()
	select {
	case <-parked:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for External status to park on the credential gate")
	}
	if got := extRequests.Load() - beforeReject; got != 0 {
		t.Fatalf("External CPA requests while gate held = %d, want 0", got)
	}
	extHandler.Store(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	releaseGate()
	select {
	case code := <-probeDone:
		if code != http.StatusServiceUnavailable {
			t.Fatalf("rejected External status = %d, want 503", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("External status did not complete after gate release")
	}
	canonicalOwner.workspace.credentialGate.setOnWait(nil)
	if got := extRequests.Load() - beforeReject; got != 1 {
		t.Fatalf("External CPA requests during rejected probe = %d, want 1", got)
	}

	// Shared rejection ledger blocks subsequent External console reads without contacting CPA.
	keysCode, keysBody := callExt(http.MethodGet, "/ext-console/api/v2/api-keys", "", nil)
	if keysCode != http.StatusConflict || !strings.Contains(string(keysBody), freshconfig.ReasonManagementRejected) {
		t.Fatalf("GET /api-keys after runtime/updates rejection = %d %s, want 409 management_key_rejected", keysCode, keysBody)
	}
	if got := extRequests.Load() - beforeReject; got != 1 {
		t.Fatalf("External CPA requests after refusal = %d, want 1", got)
	}

	// 4. Invalid UpgradeOperation on External Upgrade fails closed with runtime_mode_invalid (never 409 managed_externally).
	if _, err := st.SQLDB().Exec(`update upgrade_operations set control_base_path = '' where id = 1`); err != nil {
		t.Fatal(err)
	}
	svc := serverApp.AppContext().CPAUpdateService
	_, extInvalidPrepErr := svc.Prepare(context.Background(), cpaupdateservice.MutationRequest{
		RequestID:                "ext-invalid-1",
		TargetVersion:            "8.1.0",
		ExpectedActiveArtifactID: model.RuntimeArtifactID(artifactHex),
	})
	if kind, code, ok := cpaupdateservice.MutationErrorDetails(extInvalidPrepErr); !ok || kind != cpaupdateservice.MutationErrorUnavailable || code != "runtime_mode_invalid" {
		t.Fatalf("External invalid authority Prepare = %v (%s/%s), want unavailable/runtime_mode_invalid", extInvalidPrepErr, kind, code)
	}
	_, extInvalidObsErr := svc.ObservePrepare(context.Background(), cpaupdateservice.ObservationRequest{
		RequestID:     "ext-invalid-1",
		TargetVersion: "8.1.0",
	})
	if kind, code, ok := cpaupdateservice.ObservationErrorDetails(extInvalidObsErr); !ok || kind != cpaupdateservice.ObservationErrorUnavailable || code != "runtime_mode_invalid" {
		t.Fatalf("External invalid authority ObservePrepare = %v (%s/%s), want unavailable/runtime_mode_invalid", extInvalidObsErr, kind, code)
	}
	if embeddedSupervisorHits.Load() != 0 {
		t.Fatalf("Embedded Supervisor contacted %d times, want 0", embeddedSupervisorHits.Load())
	}
}

func TestUpgradeCPAUpdateStoreSaveFailsClosedWhenUpgradeOperationInvalid(t *testing.T) {
	st, cfg, _ := seedCompletedManagedUpgradeStore(t, "/upg-console", "/upg-gw")
	serverApp := httpapi.New(cfg, st, nil)
	rt := &upgradeCPAUpdateMockRuntime{}
	var desiredStore runtimeservice.DesiredStateStore
	if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil, upgradeCanonicalDependencies{
		Runtime:         rt,
		DesiredStateOut: &desiredStore,
	}); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting: %v", err)
	}
	canonicalOwner := desiredStore.(upgradeDesiredStateStore).owner
	updateStore := upgradeCPAUpdateStore{owner: canonicalOwner}

	validPayload := []byte(`{"schemaVersion":1,"targetVersion":"8.1.0"}`)
	if err := updateStore.SaveCPAUpdateCheck(context.Background(), validPayload); err != nil {
		t.Fatalf("SaveCPAUpdateCheck under valid UpgradeOperation: %v", err)
	}

	if _, err := st.SQLDB().Exec(`update upgrade_operations set control_base_path = '' where id = 1`); err != nil {
		t.Fatal(err)
	}
	corruptedPayload := []byte(`{"schemaVersion":1,"targetVersion":"9.9.9"}`)
	if err := updateStore.SaveCPAUpdateCheck(context.Background(), corruptedPayload); !errors.Is(err, errUpgradeManagedRuntimeUnavailable) {
		t.Fatalf("SaveCPAUpdateCheck under corrupted UpgradeOperation = %v, want errUpgradeManagedRuntimeUnavailable", err)
	}
	if _, err := updateStore.LoadCPAUpdateCheck(context.Background()); !errors.Is(err, errUpgradeManagedRuntimeUnavailable) {
		t.Fatalf("LoadCPAUpdateCheck under corrupted UpgradeOperation = %v, want errUpgradeManagedRuntimeUnavailable", err)
	}
	storedRaw, err := st.LoadCPAUpdateCheck(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if string(storedRaw) != string(validPayload) {
		t.Fatalf("stored CPAUpdateCheck mutated under failClosed: got %s, want %s", storedRaw, validPayload)
	}
}

func TestUpgradeCompletedGatewayConfigParityAndSourceMode(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	t.Run("managed-21-groups-88-fields-6-families-kvmap-put-and-source-mode", func(t *testing.T) {
		mgmtKey := strings.Repeat("b", 64)
		clientKey := "sk-managed-gw-key-01"
		var docMu sync.Mutex
		var putPaths []string
		var patchPrefixes []string
		currentDoc := map[string]any{
			"config-version": 8,
			"access": map[string]any{
				"api-keys": []any{clientKey},
			},
			"server": map[string]any{
				"debug": false,
			},
			"oauth": map[string]any{
				"providers": map[string]any{
					"antigravity": map[string]any{
						"sensitive-words-replace": map[string]any{
							"old-key": "old-val",
							"secret":  "[redacted]",
						},
					},
				},
			},
		}

		cpaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+mgmtKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("X-CPA-Version", "v8.0.55")
			w.Header().Set("Content-Type", "application/json")
			docMu.Lock()
			defer docMu.Unlock()
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config":
				_ = json.NewEncoder(w).Encode(currentDoc)
			case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v8/management/config/"):
				path := strings.ReplaceAll(strings.TrimPrefix(r.URL.Path, "/v8/management/config/"), "/", ".")
				putPaths = append(putPaths, path)
				raw, _ := io.ReadAll(r.Body)
				var val any
				_ = json.Unmarshal(raw, &val)
				setNestedTestValue(currentDoc, strings.Split(path, "."), val)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v8/management/config/"):
				prefix := strings.ReplaceAll(strings.TrimPrefix(r.URL.Path, "/v8/management/config/"), "/", ".")
				patchPrefixes = append(patchPrefixes, prefix)
				raw, _ := io.ReadAll(r.Body)
				var patchMap map[string]any
				_ = json.Unmarshal(raw, &patchMap)
				mergeNestedTestValue(currentDoc, strings.Split(prefix, "."), patchMap)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v8/management/config/"):
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer cpaServer.Close()

		st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/upg-console", "/upg-gw")
		keyBytes, _, err := security.LoadOrCreateDataKey("", cfg.DataKeyPath)
		if err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector(keyBytes)
		if err != nil {
			t.Fatal(err)
		}
		sealedDestKey, err := protector.ProtectString(mgmtKey)
		if err != nil {
			t.Fatal(err)
		}
		qualifiedArtifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
		if err != nil {
			t.Fatal(err)
		}
		const expectedIdentity = model.RuntimeIdentity("rt-upgrade-managed-gw")
		sourceIdentity := "external:sha256:" + strings.Repeat("c", 64)
		pluginsInit := false
		rawDest, err := json.Marshal(upgradeDestinationRecord{
			BaseURL:           cpaServer.URL,
			URLProved:         true,
			ManagementKey:     sealedDestKey,
			ServerFingerprint: "external:sha256:" + strings.Repeat("b", 64),
			Attempt:           1,
			OperationID:       upgradeManagedDestinationOperationID(sourceIdentity, 1),
			Intent: upgradeDestinationIntent{
				ExpectedRuntimeIdentity:   expectedIdentity,
				ExpectedRuntimeGeneration: 7,
				ExpectedActiveArtifactID:  qualifiedArtifact,
				SourceIdentity:            sourceIdentity,
				SourceClass:               upgradeManagedDestinationClass,
				APIKeys:                   []string{clientKey},
				PluginsEnabled:            &pluginsInit,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.SQLDB().Exec(
			`insert into settings(key, value, updated_at_ms) values(?, ?, ?)`,
			upgradeImportDestinationSettingKey,
			string(rawDest),
			time.Now().UnixMilli(),
		); err != nil {
			t.Fatal(err)
		}

		rt := &upgradeCPAUpdateMockRuntime{
			status: model.RuntimeObservedStatus{
				ProtocolVersion:    "v1",
				Identity:           expectedIdentity,
				Generation:         7,
				State:              model.RuntimeStateReady,
				CPAObservedVersion: "v8.0.55",
				Capabilities:       []model.RuntimeCapability{model.RuntimeCapabilityStart, model.RuntimeCapabilityStop},
				ActiveGatewayArtifact: &model.ActiveGatewayArtifact{
					Engine:     "cpa",
					ArtifactID: qualifiedArtifact,
					Version:    "v8.0.55",
				},
				Recovery: &model.RuntimeRecoveryObservation{
					State: model.RuntimeRecoveryStateInactive,
				},
			},
		}
		serverApp := httpapi.New(cfg, st, nil)
		var desiredStore runtimeservice.DesiredStateStore
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil, upgradeCanonicalDependencies{
			Runtime:         rt,
			DesiredStateOut: &desiredStore,
		}); err != nil {
			t.Fatalf("attachCompletedUpgradeRouting: %v", err)
		}
		canonicalOwner := desiredStore.(upgradeDesiredStateStore).owner
		ts := httptest.NewServer(serverApp.Handler())
		defer ts.Close()

		callManaged := func(method, path string, body []byte) (int, []byte) {
			t.Helper()
			var reader io.Reader
			if body != nil {
				reader = bytes.NewReader(body)
			}
			req, reqErr := http.NewRequest(method, ts.URL+path, reader)
			if reqErr != nil {
				t.Fatal(reqErr)
			}
			req.Header.Set("Authorization", "Bearer "+adminKey)
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			res, doErr := http.DefaultClient.Do(req)
			if doErr != nil {
				t.Fatal(doErr)
			}
			defer func() { _ = res.Body.Close() }()
			raw, readErr := io.ReadAll(res.Body)
			if readErr != nil {
				t.Fatal(readErr)
			}
			return res.StatusCode, raw
		}

		// 1. GET /gateway/config returns 21 groups, 88 fields, 6 families (including antigravity and replace).
		code, raw := callManaged(http.MethodGet, "/upg-console/api/v2/gateway/config", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /gateway/config = %d %s, want 200", code, raw)
		}
		var view freshconfig.View
		if err := json.Unmarshal(raw, &view); err != nil {
			t.Fatal(err)
		}
		if len(view.Groups) != 21 || len(view.Fields) != 88 || len(view.Families) != 6 {
			t.Fatalf("GET /gateway/config counts: groups=%d fields=%d families=%d, want 21/88/6",
				len(view.Groups), len(view.Fields), len(view.Families))
		}
		var antigravityGroup *freshconfig.GroupView
		for i := range view.Groups {
			if view.Groups[i].ID == "antigravity" {
				antigravityGroup = &view.Groups[i]
				break
			}
		}
		if antigravityGroup == nil || !antigravityGroup.Writable {
			t.Fatalf("antigravity group missing or not writable: %+v", antigravityGroup)
		}
		var replaceFamily *freshconfig.FamilyView
		for i := range view.Families {
			if view.Families[i].Family == "replace" {
				replaceFamily = &view.Families[i]
				break
			}
		}
		if replaceFamily == nil || !replaceFamily.Writable {
			t.Fatalf("replace family missing or not writable: %+v", replaceFamily)
		}

		// 2. SaveGroup on antigravity KVMap leaf uses replacement PutConfigValue (not PatchConfigPath).
		saveKVBody, _ := json.Marshal(map[string]any{
			"requestId": strings.Repeat("1", 32),
			"expected": map[string]any{
				"generation": view.Generation,
				"baseline":   antigravityGroup.Baseline,
			},
			"edits": []map[string]any{
				{
					"path":   "oauth.providers.antigravity.sensitive-words-replace",
					"action": "set",
					"value": map[string]string{
						"new-key": "new-val",
					},
				},
			},
		})
		code, raw = callManaged(http.MethodPost, "/upg-console/api/v2/gateway/config/groups/antigravity", saveKVBody)
		if code != http.StatusOK {
			t.Fatalf("POST /gateway/config/groups/antigravity = %d %s, want 200", code, raw)
		}
		docMu.Lock()
		gotPutPaths := append([]string(nil), putPaths...)
		gotPatchPrefixes := append([]string(nil), patchPrefixes...)
		docMu.Unlock()
		if len(gotPutPaths) != 1 || gotPutPaths[0] != "oauth.providers.antigravity.sensitive-words-replace" || len(gotPatchPrefixes) != 0 {
			t.Fatalf("KVMap SaveGroup wire calls: puts=%v patches=%v, want [oauth.providers.antigravity.sensitive-words-replace] and []", gotPutPaths, gotPatchPrefixes)
		}

		// 3. SaveFamily on replace family works on Managed.
		saveReplaceBody, _ := json.Marshal(map[string]any{
			"requestId": strings.Repeat("2", 32),
			"expected": map[string]any{
				"generation": view.Generation,
				"baseline":   replaceFamily.Baseline,
			},
			"rules": []map[string]any{
				{
					"models": []map[string]any{{"name": "gpt-*"}},
					"target": map[string]any{
						"role":      "user",
						"all":       false,
						"positions": []int{1},
					},
					"replacements": []map[string]string{
						{" ": "  "},
					},
				},
			},
		})
		code, raw = callManaged(http.MethodPost, "/upg-console/api/v2/gateway/config/rules/replace", saveReplaceBody)
		if code != http.StatusOK {
			t.Fatalf("POST /gateway/config/rules/replace = %d %s, want 200", code, raw)
		}

		// 4. GET and POST /gateway/config/source work on Managed and preserve "secret" key inside sensitive-words-replace.
		code, raw = callManaged(http.MethodGet, "/upg-console/api/v2/gateway/config/source", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /gateway/config/source = %d %s, want 200", code, raw)
		}
		var srcView freshconfig.SourceView
		if err := json.Unmarshal(raw, &srcView); err != nil {
			t.Fatal(err)
		}
		if !srcView.SourceWritable || srcView.Baseline == "" {
			t.Fatalf("GET /gateway/config/source = %+v", srcView)
		}

		saveSrcBody, _ := json.Marshal(map[string]any{
			"requestId": strings.Repeat("3", 32),
			"expected": map[string]any{
				"generation": srcView.Generation,
				"baseline":   srcView.Baseline,
			},
			"yaml": "oauth:\n  providers:\n    antigravity:\n      sensitive-words-replace:\n        secret: \"[redacted]\"\nserver:\n  debug: true\n",
		})
		code, raw = callManaged(http.MethodPost, "/upg-console/api/v2/gateway/config/source", saveSrcBody)
		if code != http.StatusOK {
			t.Fatalf("POST /gateway/config/source = %d %s, want 200", code, raw)
		}

		// 5. Mutual "unknown" fencing between ConfigSourceGroupID and group/family saves.
		if err := canonicalOwner.configService.appendOperation(context.Background(), freshconfig.ConfigSourceGroupID, freshstore.ConfigGroupOperation{
			SourceID:   string(expectedIdentity),
			Generation: srcView.Generation,
			RequestID:  strings.Repeat("4", 32),
			Group:      freshconfig.ConfigSourceGroupID,
			Target:     "group",
			Prefix:     "source:pending",
			Baseline:   srcView.Baseline,
			State:      "unknown",
		}); err != nil {
			t.Fatalf("seed unknown source op: %v", err)
		}
		code, raw = callManaged(http.MethodGet, "/upg-console/api/v2/gateway/config", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /gateway/config with unknown source op = %d %s", code, raw)
		}
		var blockedView freshconfig.View
		if err := json.Unmarshal(raw, &blockedView); err != nil {
			t.Fatal(err)
		}
		for _, g := range blockedView.Groups {
			if g.Writable || g.Reason != freshconfig.ReasonUnknownOriginal {
				t.Fatalf("group %s under unknown source op: writable=%v reason=%q", g.ID, g.Writable, g.Reason)
			}
		}
		blockedGroupSave, _ := json.Marshal(map[string]any{
			"requestId": strings.Repeat("5", 32),
			"expected": map[string]any{
				"generation": blockedView.Generation,
				"baseline":   groupBaseline(t, blockedView, "server"),
			},
			"edits": []map[string]any{
				{"path": "server.commercial-mode", "action": "set", "value": true},
			},
		})
		code, raw = callManaged(http.MethodPost, "/upg-console/api/v2/gateway/config/groups/server", blockedGroupSave)
		if code != http.StatusConflict || !strings.Contains(string(raw), freshconfig.ReasonUnknownOriginal) {
			t.Fatalf("POST /gateway/config/groups/server under unknown source op = %d %s, want 409 original_operation_unknown", code, raw)
		}
	})

	t.Run("external-source-mode-and-rules-fencing", func(t *testing.T) {
		extMgmtKey := strings.Repeat("a", 64)
		var docMu sync.Mutex
		currentDoc := map[string]any{
			"config-version": 8,
			"server": map[string]any{
				"debug": false,
			},
		}
		extCPAServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+extMgmtKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("X-CPA-Version", "v8.0.55")
			w.Header().Set("Content-Type", "application/json")
			docMu.Lock()
			defer docMu.Unlock()
			switch {
			case r.Method == http.MethodGet && (r.URL.Path == "/v8/management/config" || r.URL.Path == "/v0/management/config"):
				_ = json.NewEncoder(w).Encode(currentDoc)
			case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v8/management/config/"):
				path := strings.ReplaceAll(strings.TrimPrefix(r.URL.Path, "/v8/management/config/"), "/", ".")
				raw, _ := io.ReadAll(r.Body)
				var val any
				_ = json.Unmarshal(raw, &val)
				setNestedTestValue(currentDoc, strings.Split(path, "."), val)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v8/management/config/"):
				prefix := strings.ReplaceAll(strings.TrimPrefix(r.URL.Path, "/v8/management/config/"), "/", ".")
				raw, _ := io.ReadAll(r.Body)
				var patchMap map[string]any
				_ = json.Unmarshal(raw, &patchMap)
				mergeNestedTestValue(currentDoc, strings.Split(prefix, "."), patchMap)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		defer extCPAServer.Close()

		st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPAServer.URL)
		serverApp := httpapi.New(cfg, st, nil)
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil, upgradeCanonicalDependencies{}); err != nil {
			t.Fatalf("attachCompletedUpgradeRouting: %v", err)
		}
		ts := httptest.NewServer(serverApp.Handler())
		defer ts.Close()

		callExt := func(method, path string, body []byte) (int, []byte) {
			t.Helper()
			var reader io.Reader
			if body != nil {
				reader = bytes.NewReader(body)
			}
			req, reqErr := http.NewRequest(method, ts.URL+path, reader)
			if reqErr != nil {
				t.Fatal(reqErr)
			}
			req.Header.Set("Authorization", "Bearer "+adminKey)
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			res, doErr := http.DefaultClient.Do(req)
			if doErr != nil {
				t.Fatal(doErr)
			}
			defer func() { _ = res.Body.Close() }()
			raw, readErr := io.ReadAll(res.Body)
			if readErr != nil {
				t.Fatal(readErr)
			}
			return res.StatusCode, raw
		}

		code, raw := callExt(http.MethodGet, "/ext-console/api/v2/gateway/config", nil)
		if code != http.StatusOK {
			t.Fatalf("External GET /gateway/config = %d %s, want 200", code, raw)
		}
		var extView freshconfig.View
		if err := json.Unmarshal(raw, &extView); err != nil {
			t.Fatal(err)
		}
		if len(extView.Groups) != 21 || len(extView.Fields) != 88 || len(extView.Families) != 6 {
			t.Fatalf("External GET /gateway/config counts: groups=%d fields=%d families=%d, want 21/88/6",
				len(extView.Groups), len(extView.Fields), len(extView.Families))
		}

		code, raw = callExt(http.MethodGet, "/ext-console/api/v2/gateway/config/source", nil)
		if code != http.StatusOK {
			t.Fatalf("External GET /gateway/config/source = %d %s, want 200", code, raw)
		}
		var srcView freshconfig.SourceView
		if err := json.Unmarshal(raw, &srcView); err != nil {
			t.Fatal(err)
		}
		if !srcView.SourceWritable {
			t.Fatalf("External SourceView not writable: %+v", srcView)
		}

		// Non-rule source save succeeds on External.
		saveSrcBody, _ := json.Marshal(map[string]any{
			"requestId": strings.Repeat("6", 32),
			"expected": map[string]any{
				"generation": srcView.Generation,
				"baseline":   srcView.Baseline,
			},
			"yaml": "server:\n  debug: true\n",
		})
		code, raw = callExt(http.MethodPost, "/ext-console/api/v2/gateway/config/source", saveSrcBody)
		if code != http.StatusOK {
			t.Fatalf("External POST /gateway/config/source = %d %s, want 200", code, raw)
		}

		// Payload rules in source mode are blocked on External with 409 payload_rules_external_read_only.
		code, raw = callExt(http.MethodGet, "/ext-console/api/v2/gateway/config/source", nil)
		if code != http.StatusOK {
			t.Fatal(string(raw))
		}
		_ = json.Unmarshal(raw, &srcView)
		saveRulesSrcBody, _ := json.Marshal(map[string]any{
			"requestId": strings.Repeat("7", 32),
			"expected": map[string]any{
				"generation": srcView.Generation,
				"baseline":   srcView.Baseline,
			},
			"yaml": "requests:\n  payload:\n    replace:\n      - models:\n          - name: gpt-*\n        target:\n          role: user\n          all: true\n        replacements:\n          - foo: bar\nserver:\n  debug: true\n",
		})
		code, raw = callExt(http.MethodPost, "/ext-console/api/v2/gateway/config/source", saveRulesSrcBody)
		if code != http.StatusConflict || !strings.Contains(string(raw), "payload_rules_read_only") {
			t.Fatalf("External POST /gateway/config/source touching payload rules = %d %s, want 409 payload_rules_read_only", code, raw)
		}
	})
}

func TestUpgradeCompletedUsageServiceConfigCollectorFencingAndOversizedPatch(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	extMgmtKey := strings.Repeat("a", 64)
	var cpaHits atomic.Int32
	var rejectAuth atomic.Bool
	extCPAServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cpaHits.Add(1)
		if rejectAuth.Load() || r.Header.Get("Authorization") != "Bearer "+extMgmtKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.55")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/usage-statistics-enabled":
			_, _ = w.Write([]byte(`{"usage-statistics-enabled":true}`))
		case "/v0/management/v8/config":
			_, _ = w.Write([]byte(`{"config-version":8,"usage-statistics-enabled":true,"access":{"api-keys":["sk-ext-1"]}}`))
		default:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer extCPAServer.Close()

	st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPAServer.URL)
	cfg.CollectorMode = "http"
	cfg.PollInterval = 60 * time.Second
	rawCollector := collector.NewManager(cfg, st)
	collectorSvc := collectorsvc.New(rawCollector)
	t.Cleanup(func() {
		_ = collectorSvc.StopAndWait(context.Background())
	})
	serverApp := httpapi.New(cfg, st, rawCollector)
	if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil, upgradeCanonicalDependencies{}); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting: %v", err)
	}

	ts := httptest.NewServer(serverApp.Handler())
	defer ts.Close()

	callExt := func(method, path string, body string) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, reqErr := http.NewRequest(method, ts.URL+path, reader)
		if reqErr != nil {
			t.Fatal(reqErr)
		}
		req.Header.Set("Authorization", "Bearer "+adminKey)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			t.Fatal(doErr)
		}
		defer func() { _ = res.Body.Close() }()
		raw, readErr := io.ReadAll(res.Body)
		if readErr != nil {
			t.Fatal(readErr)
		}
		return res.StatusCode, raw
	}

	// 1. Oversized PATCH /cpamp/usage-service/config (>4096 bytes) is rejected with 400 Bad Request.
	oversizedBody := `{"collector":{"queue":"` + strings.Repeat("x", 4100) + `"}}`
	code, raw := callExt(http.MethodPatch, "/ext-console/api/v2/cpamp/usage-service/config", oversizedBody)
	if code != http.StatusBadRequest {
		t.Fatalf("oversized PATCH /cpamp/usage-service/config = %d %s, want 400", code, raw)
	}

	// 2. Enable external usage collection so collector is running, then verify retention PATCH does not restart collector or hit CPA.
	enableBody := `{"requestId":"` + strings.Repeat("a", 32) + `","confirmEnableStatistics":true}`
	code, raw = callExt(http.MethodPost, "/ext-console/api/v2/external/usage", enableBody)
	if code != http.StatusOK {
		t.Fatalf("POST /external/usage = %d %s, want 200", code, raw)
	}
	startsBeforeRetention := collectorSvc.StartCount()

	code, raw = callExt(http.MethodPatch, "/ext-console/api/v2/cpamp/usage-service/config", `{"retention":{"enabled":true,"retentionDays":21}}`)
	if code != http.StatusOK {
		t.Fatalf("retention PATCH /cpamp/usage-service/config = %d %s, want 200", code, raw)
	}
	if got := collectorSvc.StartCount(); got != startsBeforeRetention {
		t.Fatalf("collector StartCount after retention patch = %d, want %d", got, startsBeforeRetention)
	}

	// 3. Latch External credential refusal, then verify collector patch makes 0 CPA calls and does not start collector.
	rejectAuth.Store(true)
	code, _ = callExt(http.MethodGet, "/ext-console/api/v2/api-keys", "")
	if code != http.StatusConflict {
		t.Fatalf("GET /api-keys on rejected CPA = %d, want 409", code)
	}
	_ = collectorSvc.Stop(context.Background())
	startsBeforeCollectorPatch := collectorSvc.StartCount()
	hitsBeforeCollectorPatch := cpaHits.Load()

	code, raw = callExt(http.MethodPatch, "/ext-console/api/v2/cpamp/usage-service/config", `{"collector":{"pollIntervalMs":2500}}`)
	if code != http.StatusOK {
		t.Fatalf("collector PATCH under latched refusal = %d %s, want 200", code, raw)
	}
	if got := cpaHits.Load() - hitsBeforeCollectorPatch; got != 0 {
		t.Fatalf("CPA hits during collector PATCH under latched refusal = %d, want 0", got)
	}
	if got := collectorSvc.StartCount(); got != startsBeforeCollectorPatch {
		t.Fatalf("collector StartCount under latched refusal = %d, want %d", got, startsBeforeCollectorPatch)
	}
}

func setNestedTestValue(root map[string]any, parts []string, value any) {
	cursor := root
	for _, part := range parts[:len(parts)-1] {
		next, ok := cursor[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			cursor[part] = next
		}
		cursor = next
	}
	cursor[parts[len(parts)-1]] = value
}

func mergeNestedTestValue(root map[string]any, parts []string, patch map[string]any) {
	cursor := root
	for _, part := range parts {
		if part == "" {
			continue
		}
		next, ok := cursor[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			cursor[part] = next
		}
		cursor = next
	}
	for k, v := range patch {
		if subPatch, ok := v.(map[string]any); ok {
			mergeNestedTestValue(cursor, []string{k}, subPatch)
			continue
		}
		cursor[k] = v
	}
}

func TestUpgradeCompletedGatewayConfigH3M4M5Low3Invariants(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	extMgmtKey := strings.Repeat("a", 64)
	var docMu sync.Mutex
	currentDoc := map[string]any{
		"config-version": 8,
		"server": map[string]any{
			"port":  8317,
			"debug": true,
		},
		"oauth": map[string]any{
			"auth-dir": "/data/auth",
			"providers": map[string]any{
				"custom": map[string]any{
					"client-secret": "upgrade-secret",
					"enabled":       true,
				},
			},
		},
		"routing": map[string]any{
			"strategy": "round-robin",
			"extra":    "scalar-value",
		},
		"requests": map[string]any{
			"proxy-url": "http://legacy:pw@proxy.internal:8080",
			"payload": map[string]any{
				"default": []any{},
			},
		},
		"experimental-list": []any{
			map[string]any{"name": "item", "client-secret": "list-secret"},
		},
	}
	var deletedPaths []string
	var putPaths []string
	extCPAServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+extMgmtKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.55")
		w.Header().Set("Content-Type", "application/json")
		docMu.Lock()
		defer docMu.Unlock()
		switch {
		case r.Method == http.MethodGet && (r.URL.Path == "/v8/management/config" || r.URL.Path == "/v0/management/config"):
			_ = json.NewEncoder(w).Encode(currentDoc)
		case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/v8/management/config/"):
			path := strings.ReplaceAll(strings.TrimPrefix(r.URL.Path, "/v8/management/config/"), "/", ".")
			putPaths = append(putPaths, path)
			raw, _ := io.ReadAll(r.Body)
			var val any
			_ = json.Unmarshal(raw, &val)
			setNestedTestValue(currentDoc, strings.Split(path, "."), val)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v8/management/config/"):
			prefix := strings.ReplaceAll(strings.TrimPrefix(r.URL.Path, "/v8/management/config/"), "/", ".")
			raw, _ := io.ReadAll(r.Body)
			var patchMap map[string]any
			_ = json.Unmarshal(raw, &patchMap)
			mergeNestedTestValue(currentDoc, strings.Split(prefix, "."), patchMap)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v8/management/config/"):
			path := strings.ReplaceAll(strings.TrimPrefix(r.URL.Path, "/v8/management/config/"), "/", ".")
			deletedPaths = append(deletedPaths, path)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer extCPAServer.Close()

	st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPAServer.URL)
	serverApp := httpapi.New(cfg, st, nil)
	if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil, upgradeCanonicalDependencies{}); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting: %v", err)
	}
	ts := httptest.NewServer(serverApp.Handler())
	defer ts.Close()

	callExt := func(method, path string, body []byte) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req, err := http.NewRequest(method, ts.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+adminKey)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, raw
	}

	// M5: GET /plugins/runtime initializes non-nil empty Families slice.
	code, raw := callExt(http.MethodGet, "/ext-console/api/v2/plugins/runtime", nil)
	if code != http.StatusOK || !strings.Contains(string(raw), `"families":[]`) {
		t.Fatalf("GET /plugins/runtime = %d %s, want 200 with non-nil families:[]", code, raw)
	}

	// ReadSource must redact slice client-secret and credentialed requests.proxy-url.
	code, raw = callExt(http.MethodGet, "/ext-console/api/v2/gateway/config/source", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /gateway/config/source = %d %s", code, raw)
	}
	var srcView freshconfig.SourceView
	if err := json.Unmarshal(raw, &srcView); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"upgrade-secret", "list-secret", "legacy", "pw"} {
		if strings.Contains(srcView.YAML, secret) {
			t.Fatalf("ReadSource leaked %q:\n%s", secret, srcView.YAML)
		}
	}

	// M5: POST /gateway/config/source with null yaml is rejected 400.
	nullYAMLBody, _ := json.Marshal(map[string]any{
		"requestId": strings.Repeat("1", 32),
		"expected":  map[string]any{"generation": srcView.Generation, "baseline": srcView.Baseline},
		"yaml":      nil,
	})
	code, raw = callExt(http.MethodPost, "/ext-console/api/v2/gateway/config/source", nullYAMLBody)
	if code != http.StatusBadRequest {
		t.Fatalf("POST /gateway/config/source yaml:null = %d %s, want 400", code, raw)
	}

	// H3: External removing or nulling requests: with payload rules returns 409 payload_rules_read_only.
	for i, badYAML := range []string{
		"experimental-list:\n  - name: item\nrouting:\n  extra: scalar-value\n  strategy: round-robin\n",
		"experimental-list:\n  - name: item\nrequests: null\nrouting:\n  extra: scalar-value\n  strategy: round-robin\n",
	} {
		body, _ := json.Marshal(map[string]any{
			"requestId": strings.Repeat("2", 31) + string([]byte{'0' + byte(i)}),
			"expected":  map[string]any{"generation": srcView.Generation, "baseline": srcView.Baseline},
			"yaml":      badYAML,
		})
		code, raw = callExt(http.MethodPost, "/ext-console/api/v2/gateway/config/source", body)
		if code != http.StatusConflict || !strings.Contains(string(raw), "payload_rules_read_only") {
			t.Fatalf("External requests delete/null case %d = %d %s, want 409 payload_rules_read_only", i, code, raw)
		}
	}

	// H3: server: disabled / server: null / overwriting custom with client-secret are rejected 422 without CPA writes.
	for i, badYAML := range []string{
		"experimental-list:\n  - name: item\nserver: disabled\nrequests:\n  payload:\n    default: []\n",
		"experimental-list:\n  - name: item\nserver: null\nrequests:\n  payload:\n    default: []\n",
		"experimental-list:\n  - name: item\noauth:\n  providers:\n    custom: disabled\nrequests:\n  payload:\n    default: []\n",
	} {
		body, _ := json.Marshal(map[string]any{
			"requestId": strings.Repeat("3", 31) + string([]byte{'0' + byte(i)}),
			"expected":  map[string]any{"generation": srcView.Generation, "baseline": srcView.Baseline},
			"yaml":      badYAML,
		})
		code, raw = callExt(http.MethodPost, "/ext-console/api/v2/gateway/config/source", body)
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("protected overwrite case %d = %d %s, want 422", i, code, raw)
		}
	}

	// M4 + Low-3: scalar->empty map persists and preserves legacy credentialed proxy-url, server.port, and client-secret.
	editedYAML := strings.Replace(srcView.YAML, "extra: scalar-value", "extra: {}", 1)
	editBody, _ := json.Marshal(map[string]any{
		"requestId": strings.Repeat("4", 32),
		"expected":  map[string]any{"generation": srcView.Generation, "baseline": srcView.Baseline},
		"yaml":      editedYAML,
	})
	code, raw = callExt(http.MethodPost, "/ext-console/api/v2/gateway/config/source", editBody)
	if code != http.StatusOK {
		t.Fatalf("SaveSource scalar->empty map with legacy proxy = %d %s, want 200", code, raw)
	}
	docMu.Lock()
	defer docMu.Unlock()
	if m, ok := currentDoc["routing"].(map[string]any)["extra"].(map[string]any); !ok || len(m) != 0 {
		t.Fatalf("expected routing.extra replaced with {}, got %#v", currentDoc["routing"])
	}
	if currentDoc["requests"].(map[string]any)["proxy-url"] != "http://legacy:pw@proxy.internal:8080" {
		t.Fatalf("legacy requests.proxy-url overwritten: %v", currentDoc["requests"])
	}
	if currentDoc["server"].(map[string]any)["port"] != 8317 {
		t.Fatalf("server.port wiped: %v", currentDoc["server"])
	}
	if currentDoc["oauth"].(map[string]any)["providers"].(map[string]any)["custom"].(map[string]any)["client-secret"] != "upgrade-secret" {
		t.Fatalf("custom.client-secret wiped: %v", currentDoc["oauth"])
	}
}

func TestUpgradeCompletedReconcileCollectorH7aGateUnconfirmedAndSerialization(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	extMgmtKey := strings.Repeat("a", 64)
	rotatedKey := strings.Repeat("b", 64)
	var activeKey atomic.Value
	activeKey.Store(extMgmtKey)
	var statsEnabled atomic.Bool
	statsEnabled.Store(false)
	var rebindProbeEntered atomic.Bool
	rebindGate := make(chan struct{})
	rebindRelease := make(chan struct{})

	extCPAServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/config" && r.Header.Get("Authorization") == "Bearer "+rotatedKey {
			if rebindProbeEntered.CompareAndSwap(false, true) {
				close(rebindGate)
				<-rebindRelease
			}
		}
		if r.Header.Get("Authorization") != "Bearer "+activeKey.Load().(string) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.55")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v0/management/usage-statistics-enabled":
			statsEnabled.Store(true)
			_, _ = w.Write([]byte(`{"usage-statistics-enabled":true}`))
		case r.URL.Path == "/v0/management/usage-statistics-enabled":
			if statsEnabled.Load() {
				_, _ = w.Write([]byte(`{"usage-statistics-enabled":true}`))
			} else {
				_, _ = w.Write([]byte(`{"usage-statistics-enabled":false}`))
			}
		case r.URL.Path == "/v0/management/v8/config":
			_, _ = w.Write([]byte(`{"config-version":8,"access":{"api-keys":["sk-ext-1"]}}`))
		default:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer extCPAServer.Close()

	st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPAServer.URL)
	cfg.CollectorMode = "http"
	cfg.PollInterval = 60 * time.Second
	rawCollector := collector.NewManager(cfg, st)
	collectorSvc := collectorsvc.New(rawCollector)
	t.Cleanup(func() {
		_ = collectorSvc.StopAndWait(context.Background())
	})
	serverApp := httpapi.New(cfg, st, rawCollector)
	var desiredStore runtimeservice.DesiredStateStore
	if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil, upgradeCanonicalDependencies{
		DesiredStateOut: &desiredStore,
	}); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting: %v", err)
	}
	canonicalOwner := desiredStore.(upgradeDesiredStateStore).owner
	ts := httptest.NewServer(serverApp.Handler())
	defer ts.Close()

	callExt := func(method, path string, body string) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, ts.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+adminKey)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		raw, _ := io.ReadAll(res.Body)
		return res.StatusCode, raw
	}

	// 1. Unconfirmed usage-statistics-enabled=false prevents PATCH enabled:true from starting collector.
	code, raw := callExt(http.MethodPatch, "/ext-console/api/v2/cpamp/usage-service/config", `{"enabled":true}`)
	if code != http.StatusOK {
		t.Fatalf("PATCH enabled:true when unconfirmed = %d %s", code, raw)
	}
	if collectorSvc.StartCount() != 0 {
		t.Fatalf("collector started (%d times) while external usage-statistics-enabled=false", collectorSvc.StartCount())
	}

	// 2. Confirm via POST /external/usage -> collector starts once; GET /external/usage and On->On PATCH do not restart.
	code, raw = callExt(http.MethodPost, "/ext-console/api/v2/external/usage", `{"requestId":"`+strings.Repeat("5", 32)+`","confirmEnableStatistics":true}`)
	if code != http.StatusOK {
		t.Fatalf("POST /external/usage = %d %s", code, raw)
	}
	if collectorSvc.StartCount() != 1 {
		t.Fatalf("StartCount after confirm = %d, want 1", collectorSvc.StartCount())
	}
	code, raw = callExt(http.MethodGet, "/ext-console/api/v2/external/usage", "")
	if code != http.StatusOK {
		t.Fatalf("GET /external/usage = %d %s", code, raw)
	}
	if collectorSvc.StartCount() != 1 {
		t.Fatalf("GET /external/usage incremented StartCount to %d, want 1", collectorSvc.StartCount())
	}
	code, raw = callExt(http.MethodPatch, "/ext-console/api/v2/cpamp/usage-service/config", `{"enabled":true}`)
	if code != http.StatusOK {
		t.Fatalf("On->On PATCH enabled:true = %d %s", code, raw)
	}
	if collectorSvc.StartCount() != 1 {
		t.Fatalf("On->On PATCH incremented StartCount to %d, want 1", collectorSvc.StartCount())
	}

	// 3. Concurrent serveExternalRebind + reconcileCollector stay serialized by credentialGate without deadlock.
	activeKey.Store(rotatedKey)
	rebindDone := make(chan int, 1)
	go func() {
		c, _ := callExt(http.MethodPost, "/ext-console/api/v2/external/rebind", `{"requestId":"`+strings.Repeat("6", 32)+`","managementKey":"`+rotatedKey+`"}`)
		rebindDone <- c
	}()
	select {
	case <-rebindGate:
	case rc := <-rebindDone:
		t.Fatalf("rebind exited unexpectedly with status %d before hitting CPA probe", rc)
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for rebind to reach CPA probe")
	}
	parked := make(chan struct{})
	var parkedOnce sync.Once
	canonicalOwner.workspace.credentialGate.setOnWait(func() {
		parkedOnce.Do(func() { close(parked) })
	})
	reconcileDone := make(chan error, 1)
	go func() {
		reconcileDone <- canonicalOwner.reconcileCollector(context.Background())
	}()
	select {
	case <-parked:
	case <-time.After(3 * time.Second):
		close(rebindRelease)
		t.Fatal("reconcileCollector did not serialize on credentialGate during active rebind")
	}
	close(rebindRelease)
	select {
	case rc := <-rebindDone:
		if rc != http.StatusOK {
			t.Fatalf("rebind status = %d, want 200", rc)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("rebind did not complete")
	}
	select {
	case rErr := <-reconcileDone:
		if rErr != nil {
			t.Fatalf("reconcileCollector after rebind = %v, want nil", rErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reconcileCollector did not complete after rebind")
	}
	canonicalOwner.workspace.credentialGate.setOnWait(nil)
}

func TestUpgradeRound4ExternalUsageCollectorAndReconcileInvariants(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	t.Run("H3 host POST external/usage preserves sparse/absent TLSSkipVerify and runs collector with cfg.TLSSkipVerify=true", func(t *testing.T) {
		extKey := strings.Repeat("a", 64)
		for _, tc := range []struct {
			name      string
			sparseRow string
		}{
			{name: "absent manager_config_v1"},
			{name: "sparse manager_config_v1", sparseRow: `{"collector":{"enabled":false}}`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				tlsCPA := startSelfSignedTLSCPAServer(t, extKey)
				st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", tlsCPA.url)
				cfg.CollectorMode = "resp"
				cfg.PollInterval = 40 * time.Millisecond
				cfg.TLSSkipVerify = true
				if tc.sparseRow != "" {
					if _, err := st.SQLDB().Exec(
						`insert or replace into settings (key, value, updated_at_ms) values ('manager_config_v1', ?, 1000)`,
						tc.sparseRow,
					); err != nil {
						t.Fatal(err)
					}
				}
				rawCollector := collector.NewManager(cfg, st)
				collectorSvc := collectorsvc.New(rawCollector)
				t.Cleanup(func() {
					_ = collectorSvc.StopAndWait(context.Background())
				})
				hostCtx, cancelHost := context.WithCancel(cpa.WithTrustedCARoots(context.Background(), tlsCPA.roots))
				defer cancelHost()
				serverApp := httpapi.New(cfg, st, rawCollector)
				if err := attachCompletedUpgradeRouting(hostCtx, cfg, st, serverApp, nil); err != nil {
					t.Fatalf("attachCompletedUpgradeRouting: %v", err)
				}

				req, err := http.NewRequestWithContext(
					cpa.WithTrustedCARoots(context.Background(), tlsCPA.roots),
					http.MethodPost,
					"/ext-console/api/v2/external/usage",
					strings.NewReader(`{"requestId":"`+strings.Repeat("a", 32)+`","confirmEnableStatistics":true}`),
				)
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Authorization", "Bearer "+adminKey)
				req.Header.Set("Content-Type", "application/json")
				rec := httptest.NewRecorder()
				serverApp.Handler().ServeHTTP(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("POST /external/usage = %d %s", rec.Code, rec.Body.String())
				}

				activeCfg, active := collectorSvc.ActiveRuntimeConfig()
				if !active || !activeCfg.TLSSkipVerify {
					t.Fatalf("expected active collector with TLSSkipVerify=true, got active=%t cfg=%+v", active, activeCfg)
				}
				select {
				case <-tlsCPA.respAuthed:
				case hsErr := <-tlsCPA.handshakeRejected:
					t.Fatalf("self-signed TLS RESP rejected handshake despite cfg.TLSSkipVerify=true: %s", hsErr)
				case <-time.After(5 * time.Second):
					t.Fatalf("collector did not establish authenticated TLS RESP session (status=%+v)", collectorSvc.Status())
				}

				loaded, ok, err := st.LoadManagerConfig(context.Background())
				if err != nil || !ok {
					t.Fatalf("LoadManagerConfig = (%+v, %t, %v)", loaded, ok, err)
				}
				if loaded.Collector.TLSSkipVerifyExplicit {
					t.Fatalf("expected sparse/absent row to preserve TLSSkipVerifyExplicit=false, got %+v", loaded.Collector)
				}
			})
		}
	})

	t.Run("M1 disable skips credentialGate, 2xx detaches req cancel, healthy collector survives transient probe error, header observer stops on 401", func(t *testing.T) {
		extMgmtKey := strings.Repeat("a", 64)
		var probeMode atomic.Int32 // 0=ok, 1=500 transient, 2=401 header+abort
		var statsEnabled atomic.Bool
		statsEnabled.Store(true)

		extCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch probeMode.Load() {
			case 1:
				w.WriteHeader(http.StatusInternalServerError)
				return
			case 2:
				w.WriteHeader(http.StatusUnauthorized)
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+extMgmtKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("X-CPA-Version", "v8.0.55")
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v0/management/usage-statistics-enabled":
				if statsEnabled.Load() {
					_, _ = w.Write([]byte(`{"usage-statistics-enabled":true}`))
				} else {
					_, _ = w.Write([]byte(`{"usage-statistics-enabled":false}`))
				}
			default:
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			}
		}))
		defer extCPA.Close()

		st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPA.URL)
		cfg.CollectorMode = "http"
		cfg.PollInterval = 60 * time.Second
		rawCollector := collector.NewManager(cfg, st)
		collectorSvc := collectorsvc.New(rawCollector)
		t.Cleanup(func() {
			_ = collectorSvc.StopAndWait(context.Background())
		})
		serverApp := httpapi.New(cfg, st, rawCollector)
		var desiredStore runtimeservice.DesiredStateStore
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil, upgradeCanonicalDependencies{
			DesiredStateOut: &desiredStore,
		}); err != nil {
			t.Fatalf("attachCompletedUpgradeRouting: %v", err)
		}
		canonical := desiredStore.(upgradeDesiredStateStore).owner

		// 1. After HTTP 2xx on PATCH /usage-service/config, cancelling r.Context() in ResponseWriter.Write must not abort reconcileCollector.
		reqCtx, cancelReq := context.WithCancel(context.Background())
		req := httptest.NewRequest(http.MethodPatch, "/ext-console/api/v2/cpamp/usage-service/config", strings.NewReader(`{"enabled":true}`))
		req = req.WithContext(reqCtx)
		req.Header.Set("Authorization", "Bearer "+adminKey)
		req.Header.Set("Content-Type", "application/json")
		rec := &cancelOnWriteRecorder{ResponseRecorder: httptest.NewRecorder(), onWrite: cancelReq}
		serverApp.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH usage-service/config = %d %s", rec.Code, rec.Body.String())
		}
		if _, active := collectorSvc.ActiveRuntimeConfig(); !active {
			t.Fatal("expected reconcileCollector to complete with detached host context after client canceled post-2xx")
		}

		// 2. Transient probe error (500) must NOT stop healthy collector with matching RuntimeConfig.
		probeMode.Store(1)
		if err := canonical.reconcileCollector(context.Background()); err == nil {
			t.Fatal("expected transient probe error from reconcileCollector")
		}
		if _, active := collectorSvc.ActiveRuntimeConfig(); !active {
			t.Fatal("transient probe error stopped healthy collector with matching RuntimeConfig")
		}

		// 3. Disable PATCH stops collector immediately without acquiring credentialGate even when credentialGate is held.
		if !canonical.workspace.credentialGate.acquire(context.Background()) {
			t.Fatal("failed to acquire credentialGate")
		}
		disableDone := make(chan int, 1)
		go func() {
			rDisable := httptest.NewRequest(http.MethodPatch, "/ext-console/api/v2/cpamp/usage-service/config", strings.NewReader(`{"enabled":false}`))
			rDisable.Header.Set("Authorization", "Bearer "+adminKey)
			rDisable.Header.Set("Content-Type", "application/json")
			wDisable := httptest.NewRecorder()
			serverApp.Handler().ServeHTTP(wDisable, rDisable)
			disableDone <- wDisable.Code
		}()
		select {
		case code := <-disableDone:
			if code != http.StatusOK {
				canonical.workspace.credentialGate.release()
				t.Fatalf("PATCH enabled:false status = %d, want 200", code)
			}
		case <-time.After(2 * time.Second):
			canonical.workspace.credentialGate.release()
			t.Fatal("PATCH enabled:false blocked on credentialGate")
		}
		if _, active := collectorSvc.ActiveRuntimeConfig(); active {
			canonical.workspace.credentialGate.release()
			t.Fatal("PATCH enabled:false did not stop collector")
		}
		canonical.workspace.credentialGate.release()

		// 4. Re-enable cleanly, then verify 401 header stops collector AND records refusal via WithExternalHeaderObserver.
		probeMode.Store(0)
		rEnable := httptest.NewRequest(http.MethodPatch, "/ext-console/api/v2/cpamp/usage-service/config", strings.NewReader(`{"enabled":true}`))
		rEnable.Header.Set("Authorization", "Bearer "+adminKey)
		rEnable.Header.Set("Content-Type", "application/json")
		wEnable := httptest.NewRecorder()
		serverApp.Handler().ServeHTTP(wEnable, rEnable)
		if wEnable.Code != http.StatusOK {
			t.Fatalf("PATCH enabled:true = %d %s", wEnable.Code, wEnable.Body.String())
		}
		if _, active := collectorSvc.ActiveRuntimeConfig(); !active {
			t.Fatal("collector did not re-enable")
		}
		probeMode.Store(2)
		if err := canonical.reconcileCollector(context.Background()); !errors.Is(err, cpa.ErrManagementRejected) {
			t.Fatalf("expected ErrManagementRejected on 401 probe, got %v", err)
		}
		if _, active := collectorSvc.ActiveRuntimeConfig(); active {
			t.Fatal("collector stayed active after 401 probe")
		}
		// Recorded refusal now rejects locally without hitting CPA.
		probeMode.Store(0)
		rGet := httptest.NewRequest(http.MethodGet, "/ext-console/api/v2/external/usage", nil)
		rGet.Header.Set("Authorization", "Bearer "+adminKey)
		wGet := httptest.NewRecorder()
		serverApp.Handler().ServeHTTP(wGet, rGet)
		if wGet.Code != http.StatusConflict || !strings.Contains(wGet.Body.String(), freshconfig.ReasonManagementRejected) {
			t.Fatalf("expected recorded management_credential_rejected on GET /external/usage, got %d %s", wGet.Code, wGet.Body.String())
		}
	})

	t.Run("M4 POST and GET external/usage route reconcileCollector errors and statusCaptureResponseWriter implements Unwrap", func(t *testing.T) {
		extMgmtKey := strings.Repeat("a", 64)
		var configGetCalls atomic.Int32
		var statsGetCalls atomic.Int32
		var statsPutCalls atomic.Int32
		var statsEnabled atomic.Bool
		var forceV7Always atomic.Bool
		var onStatsRead func()
		var hookMu sync.Mutex

		extCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+extMgmtKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			ver := "v8.0.55"
			if forceV7Always.Load() {
				ver = "v7.2.130"
			}
			w.Header().Set("X-CPA-Version", ver)
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/v0/management/config":
				configGetCalls.Add(1)
				_, _ = w.Write([]byte(`{"status":"ok"}`))
				return
			case r.Method == http.MethodGet && r.URL.Path == "/v0/management/usage-statistics-enabled":
				statsGetCalls.Add(1)
				hookMu.Lock()
				fn := onStatsRead
				hookMu.Unlock()
				if fn != nil {
					fn()
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"usage-statistics-enabled": statsEnabled.Load()})
				return
			case r.Method == http.MethodPut && r.URL.Path == "/v0/management/usage-statistics-enabled":
				statsPutCalls.Add(1)
				statsEnabled.Store(true)
				_, _ = w.Write([]byte(`{"status":"ok"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer extCPA.Close()

		st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPA.URL)
		rawCollector := collector.NewManager(cfg, st)
		t.Cleanup(func() {
			_ = rawCollector.StopAndWait(context.Background())
		})
		var canonical *upgradeCanonicalServer
		serverApp := httpapi.New(cfg, st, rawCollector)
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil, upgradeCanonicalDependencies{
			CanonicalOut: &canonical,
		}); err != nil {
			t.Fatalf("attachCompletedUpgradeRouting: %v", err)
		}

		// 1. POST /external/usage when statistics start disabled performs 1 version GET, 1 PUT, and 2 statistics GETs (initial + read-back), with zero duplicate GETs in reconcile.
		rEnable := httptest.NewRequest(http.MethodPost, "/ext-console/api/v2/external/usage", strings.NewReader(`{"requestId":"`+strings.Repeat("a", 32)+`","confirmEnableStatistics":true}`))
		rEnable.Header.Set("Authorization", "Bearer "+adminKey)
		rEnable.Header.Set("Content-Type", "application/json")
		wEnable := httptest.NewRecorder()
		serverApp.Handler().ServeHTTP(wEnable, rEnable)
		if wEnable.Code != http.StatusOK {
			t.Fatalf("POST /external/usage (enable) = %d %s, want 200", wEnable.Code, wEnable.Body.String())
		}
		if gotCfg, gotPut, gotStats := configGetCalls.Load(), statsPutCalls.Load(), statsGetCalls.Load(); gotCfg != 1 || gotPut != 1 || gotStats != 2 {
			t.Fatalf("POST /external/usage (false->true) CPA calls = (configGet=%d, statsPut=%d, statsGet=%d), want (1, 1, 2)", gotCfg, gotPut, gotStats)
		}

		// 2. POST /external/usage when statistics are already enabled performs exactly 1 version GET and 1 statistics GET (no redundant probe in reconcile).
		configGetCalls.Store(0)
		statsPutCalls.Store(0)
		statsGetCalls.Store(0)
		rAlreadyOn := httptest.NewRequest(http.MethodPost, "/ext-console/api/v2/external/usage", strings.NewReader(`{"requestId":"`+strings.Repeat("c", 32)+`","confirmEnableStatistics":true}`))
		rAlreadyOn.Header.Set("Authorization", "Bearer "+adminKey)
		rAlreadyOn.Header.Set("Content-Type", "application/json")
		wAlreadyOn := httptest.NewRecorder()
		serverApp.Handler().ServeHTTP(wAlreadyOn, rAlreadyOn)
		if wAlreadyOn.Code != http.StatusOK {
			t.Fatalf("POST /external/usage (already enabled) = %d %s, want 200", wAlreadyOn.Code, wAlreadyOn.Body.String())
		}
		if gotCfg, gotPut, gotStats := configGetCalls.Load(), statsPutCalls.Load(), statsGetCalls.Load(); gotCfg != 1 || gotPut != 0 || gotStats != 1 {
			t.Fatalf("POST /external/usage (true->true) CPA calls = (configGet=%d, statsPut=%d, statsGet=%d), want (1, 0, 1)", gotCfg, gotPut, gotStats)
		}

		// 3. Direct nil-probe call to enableAndReconcileExternalCollector remains compatible and returns ErrExternalVersionUnsupported on v7.
		install, credDigest, preErr := canonical.precheckExternalCredential(context.Background())
		if preErr != nil {
			t.Fatalf("precheckExternalCredential: %v", preErr)
		}
		forceV7Always.Store(true)
		if err := canonical.enableAndReconcileExternalCollector(context.Background(), install, nil); !errors.Is(err, cpa.ErrExternalVersionUnsupported) {
			t.Fatalf("enableAndReconcileExternalCollector(nil probe) on v7 = %v, want ErrExternalVersionUnsupported", err)
		}

		// 4. GET /external/usage returns reconcileCollector errors instead of swallowing them in reconcileCollectorBestEffort.
		rGet := httptest.NewRequest(http.MethodGet, "/ext-console/api/v2/external/usage", nil)
		rGet.Header.Set("Authorization", "Bearer "+adminKey)
		wGet := httptest.NewRecorder()
		serverApp.Handler().ServeHTTP(wGet, rGet)
		if wGet.Code != http.StatusConflict || !strings.Contains(wGet.Body.String(), freshconfig.ReasonVersionUnsupported) {
			t.Fatalf("GET /external/usage reconcile error = %d %s, want 409 %s", wGet.Code, wGet.Body.String(), freshconfig.ReasonVersionUnsupported)
		}

		// 5. POST /external/usage where enableAndReconcileExternalCollector encounters concurrent rejection during reconcile routes 409 management_credential_rejected, not 503.
		forceV7Always.Store(false)
		hookMu.Lock()
		onStatsRead = func() {
			_ = canonical.workspace.rejection.record(context.Background(), install.Identity, credDigest)
		}
		hookMu.Unlock()
		rPost := httptest.NewRequest(http.MethodPost, "/ext-console/api/v2/external/usage", strings.NewReader(`{"requestId":"`+strings.Repeat("b", 32)+`","confirmEnableStatistics":true}`))
		rPost.Header.Set("Authorization", "Bearer "+adminKey)
		rPost.Header.Set("Content-Type", "application/json")
		wPost := httptest.NewRecorder()
		serverApp.Handler().ServeHTTP(wPost, rPost)
		hookMu.Lock()
		onStatsRead = nil
		hookMu.Unlock()
		if wPost.Code != http.StatusConflict || !strings.Contains(wPost.Body.String(), freshconfig.ReasonManagementRejected) {
			t.Fatalf("POST /external/usage reconcile error = %d %s, want 409 %s", wPost.Code, wPost.Body.String(), freshconfig.ReasonManagementRejected)
		}

		// statusCaptureResponseWriter.Unwrap exposes underlying ResponseWriter.
		base := httptest.NewRecorder()
		wrapped := &statusCaptureResponseWriter{ResponseWriter: base, code: http.StatusOK}
		if wrapped.Unwrap() != base {
			t.Fatal("statusCaptureResponseWriter.Unwrap did not return underlying ResponseWriter")
		}
		rc := http.NewResponseController(wrapped)
		if err := rc.Flush(); err != nil {
			t.Fatalf("http.NewResponseController.Flush through statusCaptureResponseWriter failed: %v", err)
		}
	})
}

type cancelOnWriteRecorder struct {
	*httptest.ResponseRecorder
	onWrite func()
}

func (r *cancelOnWriteRecorder) Write(p []byte) (int, error) {
	n, err := r.ResponseRecorder.Write(p)
	if r.onWrite != nil {
		r.onWrite()
	}
	return n, err
}

func TestUpgradeRound4GatewayConfigM2AndM3Invariants(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	t.Run("M2 LogsCapability probes Managed ReadConfigDocument, readScope clears invalid journal, and reconcile validates requestId before credentialGate", func(t *testing.T) {
		// 1. Managed LogsCapability probes ReadConfigDocument and reports read_unavailable when CPA returns 500.
		var return500 atomic.Bool
		return500.Store(true)
		cpaFail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if return500.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("X-CPA-Version", "v8.0.55")
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodPatch {
				_, _ = w.Write([]byte(`{"status":"ok"}`))
				return
			}
			_, _ = w.Write([]byte(`{"routing":{"strategy":"round-robin"}}`))
		}))
		defer cpaFail.Close()

		st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/upg-console", "/upg-gw")
		keyBytes, _, err := security.LoadOrCreateDataKey("", cfg.DataKeyPath)
		if err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector(keyBytes)
		if err != nil {
			t.Fatal(err)
		}
		sealedSecret, err := protector.ProtectString("managed-secret")
		if err != nil {
			t.Fatal(err)
		}
		const runtimeID = model.RuntimeIdentity("rt-upgrade-managed-m2")
		sourceIdentity := "external:sha256:" + strings.Repeat("c", 64)
		qualifiedArtifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
		if err != nil {
			t.Fatal(err)
		}
		pluginsInit := false
		rawDest, err := json.Marshal(upgradeDestinationRecord{
			BaseURL:           cpaFail.URL,
			URLProved:         true,
			ManagementKey:     sealedSecret,
			ServerFingerprint: "external:sha256:" + strings.Repeat("b", 64),
			Attempt:           1,
			OperationID:       upgradeManagedDestinationOperationID(sourceIdentity, 1),
			Intent: upgradeDestinationIntent{
				ExpectedRuntimeIdentity:   runtimeID,
				ExpectedRuntimeGeneration: 7,
				ExpectedActiveArtifactID:  qualifiedArtifact,
				SourceIdentity:            sourceIdentity,
				SourceClass:               upgradeManagedDestinationClass,
				APIKeys:                   []string{"sk-m2-test"},
				PluginsEnabled:            &pluginsInit,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.SQLDB().Exec(
			`insert into settings(key, value, updated_at_ms) values(?, ?, ?)`,
			upgradeImportDestinationSettingKey,
			string(rawDest),
			time.Now().UnixMilli(),
		); err != nil {
			t.Fatal(err)
		}
		// Managed authority reads destination from upgrade_import_destination_v1 and runtime verification from rt.Status.
		rt := &upgradeCPAUpdateMockRuntime{
			status: model.RuntimeObservedStatus{
				ProtocolVersion:    "v1",
				Identity:           runtimeID,
				Generation:         7,
				State:              model.RuntimeStateReady,
				CPAObservedVersion: "v8.0.55",
				Capabilities:       []model.RuntimeCapability{model.RuntimeCapabilityStart, model.RuntimeCapabilityStop},
				ActiveGatewayArtifact: &model.ActiveGatewayArtifact{
					Engine:     "cpa",
					ArtifactID: qualifiedArtifact,
					Version:    "v8.0.55",
				},
				Recovery: &model.RuntimeRecoveryObservation{
					State: model.RuntimeRecoveryStateInactive,
				},
			},
		}
		serverApp := httpapi.New(cfg, st, nil)
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil, upgradeCanonicalDependencies{
			Runtime: rt,
		}); err != nil {
			t.Fatalf("attachCompletedUpgradeRouting: %v", err)
		}

		reqCap := httptest.NewRequest(http.MethodGet, "/upg-console/api/v2/gateway/config/logs-capability", nil)
		reqCap.Header.Set("Authorization", "Bearer "+adminKey)
		recCap := httptest.NewRecorder()
		serverApp.Handler().ServeHTTP(recCap, reqCap)
		if recCap.Code != http.StatusOK {
			t.Fatalf("GET logs-capability = %d %s", recCap.Code, recCap.Body.String())
		}
		var capWrap struct {
			Capability freshconfig.Capability `json:"capability"`
		}
		if err := json.Unmarshal(recCap.Body.Bytes(), &capWrap); err != nil {
			t.Fatal(err)
		}
		if capWrap.Capability.Readable || capWrap.Capability.Writable || capWrap.Capability.Reason != freshconfig.ReasonReadUnavailable {
			t.Fatalf("expected Managed LogsCapability read_unavailable, got %+v", capWrap.Capability)
		}

		// 2. readScope clears partial journal when journalInvalid is true so ReasonUnknownOriginal cannot overwrite ReasonJournalInvalid.
		return500.Store(false)
		reqID := strings.Repeat("1", 32)
		corruptEnv := map[string]any{
			"groups": map[string]any{
				"retry": map[string]any{
					"latest": reqID,
					"operations": map[string]any{
						reqID: freshstore.ConfigGroupOperation{
							SourceID:   "managed:" + string(qualifiedArtifact),
							Generation: "7",
							RequestID:  reqID,
							Group:      "retry",
							State:      "unknown",
						},
					},
				},
				"broken": nil,
			},
		}
		rawJournal, _ := json.Marshal(corruptEnv)
		if _, err := st.SQLDB().Exec(
			`insert or replace into settings (key, value, updated_at_ms) values (?, ?, 1000)`,
			upgradeConfigGroupsSettingsKey, string(rawJournal),
		); err != nil {
			t.Fatal(err)
		}
		reqView := httptest.NewRequest(http.MethodGet, "/upg-console/api/v2/gateway/config", nil)
		reqView.Header.Set("Authorization", "Bearer "+adminKey)
		recView := httptest.NewRecorder()
		serverApp.Handler().ServeHTTP(recView, reqView)
		if recView.Code != http.StatusOK {
			t.Fatalf("GET gateway/config = %d %s", recView.Code, recView.Body.String())
		}
		var view freshconfig.View
		if err := json.Unmarshal(recView.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if !view.GroupsInvalid {
			t.Fatalf("expected GroupsInvalid=true, got view=%+v", view)
		}
		for _, g := range view.Groups {
			if g.Operation != nil {
				t.Fatalf("expected partial journal entry cleared on invalid journal, got group %s operation=%+v", g.ID, g.Operation)
			}
			if g.Reason != freshconfig.ReasonJournalInvalid || g.Writable {
				t.Fatalf("expected group %s writable=false reason=%q (not ReasonUnknownOriginal), got writable=%t reason=%q",
					g.ID, freshconfig.ReasonJournalInvalid, g.Writable, g.Reason)
			}
		}
		for _, f := range view.Fields {
			if f.Reason != freshconfig.ReasonJournalInvalid || f.Editable {
				t.Fatalf("expected field %s editable=false reason=%q, got editable=%t reason=%q",
					f.Path, freshconfig.ReasonJournalInvalid, f.Editable, f.Reason)
			}
		}
		for _, fam := range view.Families {
			if fam.Reason != freshconfig.ReasonJournalInvalid || fam.Writable {
				t.Fatalf("expected family %s writable=false reason=%q, got writable=%t reason=%q",
					fam.Family, freshconfig.ReasonJournalInvalid, fam.Writable, fam.Reason)
			}
		}

		// Concurrent readScope + SaveGroup under -race verifies s.mu serialization.
		if _, err := st.SQLDB().Exec(`delete from settings where key = ?`, upgradeConfigGroupsSettingsKey); err != nil {
			t.Fatal(err)
		}
		rInit := httptest.NewRequest(http.MethodGet, "/upg-console/api/v2/gateway/config", nil)
		rInit.Header.Set("Authorization", "Bearer "+adminKey)
		wInit := httptest.NewRecorder()
		serverApp.Handler().ServeHTTP(wInit, rInit)
		var initView freshconfig.View
		if err := json.Unmarshal(wInit.Body.Bytes(), &initView); err != nil {
			t.Fatal(err)
		}
		var concWG sync.WaitGroup
		concWG.Add(2)
		go func() {
			defer concWG.Done()
			for range 4 {
				r := httptest.NewRequest(http.MethodGet, "/upg-console/api/v2/gateway/config", nil)
				r.Header.Set("Authorization", "Bearer "+adminKey)
				serverApp.Handler().ServeHTTP(httptest.NewRecorder(), r)
			}
		}()
		var routingBaseline string
		for _, g := range initView.Groups {
			if g.ID == "routing" {
				routingBaseline = g.Baseline
				break
			}
		}
		go func() {
			defer concWG.Done()
			body, _ := json.Marshal(map[string]any{
				"requestId": strings.Repeat("e", 32),
				"expected":  map[string]any{"generation": initView.Generation, "baseline": routingBaseline},
				"edits":     []map[string]any{{"path": "routing.strategy", "action": "set", "value": "fill-first"}},
			})
			r := httptest.NewRequest(http.MethodPost, "/upg-console/api/v2/gateway/config/groups/routing", bytes.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+adminKey)
			r.Header.Set("Content-Type", "application/json")
			serverApp.Handler().ServeHTTP(httptest.NewRecorder(), r)
		}()
		concWG.Wait()

		// 3. External reconcile validates freshkeys.ValidRequestID BEFORE enterExternalCredentialHold.
		extCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-CPA-Version", "v8.0.55")
			_, _ = w.Write([]byte(`{"routing":{"strategy":"round-robin"}}`))
		}))
		defer extCPA.Close()
		extSt, extCfg, extAdmin := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPA.URL)
		extApp := httpapi.New(extCfg, extSt, nil)
		var extDesired runtimeservice.DesiredStateStore
		if err := attachCompletedUpgradeRouting(context.Background(), extCfg, extSt, extApp, nil, upgradeCanonicalDependencies{
			DesiredStateOut: &extDesired,
		}); err != nil {
			t.Fatalf("attachCompletedUpgradeRouting external: %v", err)
		}
		extOwner := extDesired.(upgradeDesiredStateStore).owner
		if !extOwner.workspace.credentialGate.acquire(context.Background()) {
			t.Fatal("failed to acquire external credentialGate")
		}
		defer extOwner.workspace.credentialGate.release()
		reconcileDone := make(chan struct {
			code int
			body string
		}, 1)
		go func() {
			rRec := httptest.NewRequest(http.MethodPost, "/ext-console/api/v2/gateway/config/operations/invalid-id/reconcile", strings.NewReader(`{}`))
			rRec.Header.Set("Authorization", "Bearer "+extAdmin)
			rRec.Header.Set("Content-Type", "application/json")
			wRec := httptest.NewRecorder()
			extApp.Handler().ServeHTTP(wRec, rRec)
			reconcileDone <- struct {
				code int
				body string
			}{wRec.Code, wRec.Body.String()}
		}()
		select {
		case res := <-reconcileDone:
			if res.code != http.StatusBadRequest || !strings.Contains(res.body, "invalid_config_request") {
				t.Fatalf("expected 400 invalid_config_request before credentialGate, got %d %s", res.code, res.body)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("reconcile with invalid requestId blocked on credentialGate")
		}
	})

	t.Run("M3 External upgrade source preserves empty payload map and blocks deleting or nulling requests", func(t *testing.T) {
		extKey := strings.Repeat("a", 64)
		var docMu sync.Mutex
		currentDoc := map[string]any{
			"server": map[string]any{"port": 8317},
			"routing": map[string]any{
				"strategy": "round-robin",
				"empty":    map[string]any{},
			},
			"requests": map[string]any{
				"payload": map[string]any{},
			},
		}
		extCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+extKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("X-CPA-Version", "v8.0.64")
			w.Header().Set("Content-Type", "application/json")
			docMu.Lock()
			defer docMu.Unlock()
			if r.Method == http.MethodGet && r.URL.Path == "/v8/management/config" {
				_ = json.NewEncoder(w).Encode(currentDoc)
				return
			}
			if r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v8/management/config/") {
				section := strings.TrimPrefix(r.URL.Path, "/v8/management/config/")
				var patch map[string]any
				_ = json.NewDecoder(r.Body).Decode(&patch)
				mergeNestedTestValue(currentDoc, strings.Split(section, "/"), patch)
				_, _ = w.Write([]byte(`{"status":"ok"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		defer extCPA.Close()

		st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPA.URL)
		serverApp := httpapi.New(cfg, st, nil)
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil); err != nil {
			t.Fatalf("attachCompletedUpgradeRouting: %v", err)
		}
		callExt := func(method, path string, body []byte) (int, []byte) {
			t.Helper()
			var reader io.Reader
			if body != nil {
				reader = bytes.NewReader(body)
			}
			req := httptest.NewRequest(method, path, reader)
			req.Header.Set("Authorization", "Bearer "+adminKey)
			if body != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			serverApp.Handler().ServeHTTP(rec, req)
			return rec.Code, rec.Body.Bytes()
		}

		code, raw := callExt(http.MethodGet, "/ext-console/api/v2/gateway/config/source", nil)
		if code != http.StatusOK {
			t.Fatalf("GET /gateway/config/source = %d %s", code, raw)
		}
		var srcView freshconfig.SourceView
		if err := json.Unmarshal(raw, &srcView); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(srcView.YAML, "payload: {}") || !strings.Contains(srcView.YAML, "empty: {}") || strings.Contains(srcView.YAML, "server:") {
			t.Fatalf("unexpected ReadSource YAML (must preserve empty maps and drop stripped-empty server):\n%s", srcView.YAML)
		}
		for i, badYAML := range []string{
			"routing:\n  empty: {}\n  strategy: round-robin\n",
			"requests: null\nrouting:\n  empty: {}\n  strategy: round-robin\n",
			"requests: {}\nrouting:\n  empty: {}\n  strategy: round-robin\n",
		} {
			payload, _ := json.Marshal(map[string]any{
				"requestId": strings.Repeat("d", 31) + string([]byte{'0' + byte(i)}),
				"expected":  map[string]any{"generation": srcView.Generation, "baseline": srcView.Baseline},
				"yaml":      badYAML,
			})
			code, raw = callExt(http.MethodPost, "/ext-console/api/v2/gateway/config/source", payload)
			if code != http.StatusConflict || !strings.Contains(string(raw), "payload_rules_read_only") {
				t.Fatalf("case %d = %d %s, want 409 payload_rules_read_only", i, code, raw)
			}
		}
	})
}

func TestUpgradeCompletedSourceModeValidationFloatBoundsAndMixedCaseDeletion(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	extKey := strings.Repeat("a", 64)
	var docMu sync.Mutex
	currentDoc := map[string]any{
		"server": map[string]any{"port": 8317},
		"Routing": map[string]any{
			"Strategy": "round-robin",
			"Extra":    "legacy-value",
		},
	}
	var writeCalls atomic.Int32
	var deletedPaths []string

	extCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+extKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.64")
		w.Header().Set("Content-Type", "application/json")
		docMu.Lock()
		defer docMu.Unlock()
		if r.Method == http.MethodGet && r.URL.Path == "/v8/management/config" {
			_ = json.NewEncoder(w).Encode(currentDoc)
			return
		}
		if r.Method != http.MethodGet {
			writeCalls.Add(1)
		}
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v8/management/config/") {
			section := strings.TrimPrefix(r.URL.Path, "/v8/management/config/")
			deletedPaths = append(deletedPaths, section)
			parts := strings.Split(section, "/")
			var cur any = currentDoc
			for _, part := range parts[:len(parts)-1] {
				m, ok := cur.(map[string]any)
				if !ok {
					http.NotFound(w, r)
					return
				}
				cur = m[part]
			}
			if m, ok := cur.(map[string]any); ok {
				delete(m, parts[len(parts)-1])
			}
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		if r.Method == http.MethodPatch && strings.HasPrefix(r.URL.Path, "/v8/management/config/") {
			section := strings.TrimPrefix(r.URL.Path, "/v8/management/config/")
			var patch map[string]any
			_ = json.NewDecoder(r.Body).Decode(&patch)
			mergeNestedTestValue(currentDoc, strings.Split(section, "/"), patch)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer extCPA.Close()

	st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPA.URL)
	serverApp := httpapi.New(cfg, st, nil)
	if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting: %v", err)
	}
	callExt := func(method, path string, body []byte) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Authorization", "Bearer "+adminKey)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		serverApp.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}

	code, raw := callExt(http.MethodGet, "/ext-console/api/v2/gateway/config/source", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /gateway/config/source = %d %s", code, raw)
	}
	var srcView freshconfig.SourceView
	if err := json.Unmarshal(raw, &srcView); err != nil {
		t.Fatal(err)
	}

	postSource := func(reqID string, yamlText string) (int, []byte) {
		payload, _ := json.Marshal(map[string]any{
			"requestId": reqID,
			"expected":  map[string]any{"generation": srcView.Generation, "baseline": srcView.Baseline},
			"yaml":      yamlText,
		})
		return callExt(http.MethodPost, "/ext-console/api/v2/gateway/config/source", payload)
	}

	// 1. Protected path server.port -> 422 protected_source_path, zero CPA writes.
	code, raw = postSource(strings.Repeat("1", 32), "server:\n  port: 9999\nRouting:\n  Strategy: round-robin\n  Extra: legacy-value\n")
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), freshconfig.CodeSourceProtectedPath) {
		t.Fatalf("server.port source save = %d %s, want 422 %s", code, raw, freshconfig.CodeSourceProtectedPath)
	}

	// 2. Inline credentials in requests.proxy-url -> 422 proxy_url_credentials, zero CPA writes.
	code, raw = postSource(strings.Repeat("2", 32), "requests:\n  proxy-url: http://user:pass@proxy.internal:8080\nRouting:\n  Strategy: round-robin\n  Extra: legacy-value\n")
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), freshconfig.CodeProxyURLCredentials) {
		t.Fatalf("inline-cred proxy-url source save = %d %s, want 422 %s", code, raw, freshconfig.CodeProxyURLCredentials)
	}

	// 3. .nan, .inf, -.inf, and uint64 > math.MaxInt64 -> 422 invalid_source_yaml, zero CPA writes.
	for i, badScalarYAML := range []string{
		"Routing:\n  Strategy: round-robin\n  Extra: .nan\n",
		"Routing:\n  Strategy: round-robin\n  Extra: .inf\n",
		"Routing:\n  Strategy: round-robin\n  Extra: -.inf\n",
		"Routing:\n  Strategy: round-robin\n  Extra: 9223372036854775808\n",
	} {
		code, raw = postSource(strings.Repeat("3", 31)+string([]byte{'0' + byte(i)}), badScalarYAML)
		if code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), freshconfig.CodeSourceInvalidYAML) {
			t.Fatalf("bad scalar case %d = %d %s, want 422 %s", i, code, raw, freshconfig.CodeSourceInvalidYAML)
		}
	}

	// 4. Case-colliding structural keys (both null and non-null) -> 422 invalid_source_yaml, zero CPA writes.
	code, raw = postSource(strings.Repeat("4", 32), "Routing:\n  Strategy: round-robin\n  Extra: legacy-value\n  extra: null\n")
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), freshconfig.CodeSourceInvalidYAML) {
		t.Fatalf("mismatched-case null key = %d %s, want 422 %s", code, raw, freshconfig.CodeSourceInvalidYAML)
	}
	code, raw = postSource(strings.Repeat("4", 31)+"b", "Routing:\n  Strategy: round-robin\n  Extra: legacy-value\nrouting:\n  strategy: fill-first\n")
	if code != http.StatusUnprocessableEntity || !strings.Contains(string(raw), freshconfig.CodeSourceInvalidYAML) {
		t.Fatalf("case-colliding structural keys = %d %s, want 422 %s", code, raw, freshconfig.CodeSourceInvalidYAML)
	}
	if writeCalls.Load() != 0 {
		t.Fatalf("validation failures triggered %d CPA writes, want 0", writeCalls.Load())
	}

	// 5. Exact mixed-case key deletion via explicit null succeeds and deletes Routing/Extra on CPA.
	code, raw = postSource(strings.Repeat("5", 32), "Routing:\n  Strategy: round-robin\n  Extra: null\n")
	if code != http.StatusOK {
		t.Fatalf("exact mixed-case key deletion = %d %s, want 200", code, raw)
	}
	docMu.Lock()
	_, extraStillPresent := currentDoc["Routing"].(map[string]any)["Extra"]
	gotDeleted := append([]string(nil), deletedPaths...)
	docMu.Unlock()
	if extraStillPresent || len(gotDeleted) != 1 || gotDeleted[0] != "Routing/Extra" {
		t.Fatalf("expected Routing/Extra deleted cleanly, extraStillPresent=%t deletedPaths=%v", extraStillPresent, gotDeleted)
	}
}

func TestUpgradeSourceRejectsCrossDocumentCaseConflictsBeforeJournalOrWrite(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	extKey := strings.Repeat("a", 64)
	var docMu sync.Mutex
	currentDoc := map[string]any{
		"config-version": 8,
		"server": map[string]any{
			"port": 8317,
		},
		"requests": map[string]any{
			"proxy-url": "http://user:secret@proxy.internal:8080",
			"retry":     3,
		},
		"oauth": map[string]any{
			"auth-auto-refresh-workers": 4,
		},
		"Routing": map[string]any{
			"Strategy": "round-robin",
		},
	}
	var writeCalls atomic.Int32

	extCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+extKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.64")
		w.Header().Set("Content-Type", "application/json")
		docMu.Lock()
		defer docMu.Unlock()
		if r.Method == http.MethodGet && r.URL.Path == "/v8/management/config" {
			_ = json.NewEncoder(w).Encode(currentDoc)
			return
		}
		if r.Method != http.MethodGet {
			writeCalls.Add(1)
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer extCPA.Close()

	st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPA.URL)
	serverApp := httpapi.New(cfg, st, nil)
	if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting: %v", err)
	}
	callExt := func(method, path string, body []byte) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			reader = bytes.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Authorization", "Bearer "+adminKey)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		serverApp.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}

	code, raw := callExt(http.MethodGet, "/ext-console/api/v2/gateway/config/source", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /gateway/config/source = %d %s", code, raw)
	}
	var srcView freshconfig.SourceView
	if err := json.Unmarshal(raw, &srcView); err != nil {
		t.Fatal(err)
	}
	postSource := func(reqID string, yamlText string) (int, string) {
		payload, _ := json.Marshal(map[string]any{
			"requestId": reqID,
			"expected":  map[string]any{"generation": srcView.Generation, "baseline": srcView.Baseline},
			"yaml":      yamlText,
		})
		status, resp := callExt(http.MethodPost, "/ext-console/api/v2/gateway/config/source", payload)
		var body struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(resp, &body)
		return status, body.Code
	}

	// 1. Hidden credentialed requests.proxy-url collided via Requests or Proxy-URL -> 422 protected_source_path.
	if status, errCode := postSource(strings.Repeat("a", 32), "Requests:\n  retry: 3\nRouting:\n  Strategy: round-robin\n"); status != http.StatusUnprocessableEntity || errCode != freshconfig.CodeSourceProtectedPath {
		t.Fatalf("Requests case collision against hidden credentialed proxy-url = (%d, %q), want (422, %q)", status, errCode, freshconfig.CodeSourceProtectedPath)
	}
	if status, errCode := postSource(strings.Repeat("b", 32), "requests:\n  retry: 3\n  Proxy-URL: http://clean.proxy:8080\nRouting:\n  Strategy: round-robin\n"); status != http.StatusUnprocessableEntity || errCode != freshconfig.CodeSourceProtectedPath {
		t.Fatalf("requests.Proxy-URL case collision against hidden credentialed proxy-url = (%d, %q), want (422, %q)", status, errCode, freshconfig.CodeSourceProtectedPath)
	}

	// 2. Hidden server.port collided via Server -> 422 protected_source_path.
	if status, errCode := postSource(strings.Repeat("c", 32), "Server:\n  debug: true\nrequests:\n  retry: 3\nRouting:\n  Strategy: round-robin\n"); status != http.StatusUnprocessableEntity || errCode != freshconfig.CodeSourceProtectedPath {
		t.Fatalf("Server case collision against hidden server.port = (%d, %q), want (422, %q)", status, errCode, freshconfig.CodeSourceProtectedPath)
	}

	// 2b. oauth containing only public subkeys collided via OAuth -> 422 protected_source_path.
	if status, errCode := postSource(strings.Repeat("f", 32), "OAuth:\n  auth-auto-refresh-workers: 8\nrequests:\n  retry: 3\nRouting:\n  Strategy: round-robin\n"); status != http.StatusUnprocessableEntity || errCode != freshconfig.CodeSourceProtectedPath {
		t.Fatalf("OAuth case collision against public-only oauth subtree = (%d, %q), want (422, %q)", status, errCode, freshconfig.CodeSourceProtectedPath)
	}

	// 3. Ordinary key Routing vs routing (and Strategy vs strategy) -> 422 invalid_source_yaml.
	if status, errCode := postSource(strings.Repeat("d", 32), "requests:\n  retry: 3\nrouting:\n  Strategy: round-robin\n"); status != http.StatusUnprocessableEntity || errCode != freshconfig.CodeSourceInvalidYAML {
		t.Fatalf("routing vs Routing case collision = (%d, %q), want (422, %q)", status, errCode, freshconfig.CodeSourceInvalidYAML)
	}
	if status, errCode := postSource(strings.Repeat("e", 32), "requests:\n  retry: 3\nRouting:\n  strategy: fill-first\n"); status != http.StatusUnprocessableEntity || errCode != freshconfig.CodeSourceInvalidYAML {
		t.Fatalf("Routing.strategy vs Routing.Strategy case collision = (%d, %q), want (422, %q)", status, errCode, freshconfig.CodeSourceInvalidYAML)
	}

	if got := writeCalls.Load(); got != 0 {
		t.Fatalf("CPA write calls on case-collision rejections = %d, want 0", got)
	}
	var journalCount int
	if err := st.SQLDB().QueryRow(`select count(*) from settings where key = ?`, upgradeConfigGroupsSettingsKey).Scan(&journalCount); err != nil {
		t.Fatalf("query journal row: %v", err)
	}
	if journalCount != 0 {
		t.Fatalf("journal rows persisted on 422 pre-write rejections = %d, want 0", journalCount)
	}
}

func TestUpgradeExternalHotReconcileInheritsSetupQueueAndPopSide(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	extKey := strings.Repeat("a", 64)
	var statsEnabled atomic.Bool
	extCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+extKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.64")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/config":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"port":                                8317,
				"usage-statistics-enabled":            statsEnabled.Load(),
				"redis-usage-queue-retention-seconds": 60,
			})
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config":
			_, _ = w.Write([]byte(`{"config-version":8,"access":{"api-keys":[]}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/usage-statistics-enabled":
			_ = json.NewEncoder(w).Encode(map[string]any{"usage-statistics-enabled": statsEnabled.Load()})
		case r.Method == http.MethodPut && r.URL.Path == "/v0/management/usage-statistics-enabled":
			statsEnabled.Store(true)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/usage":
			_, _ = w.Write([]byte(`{"usage":{}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/usage-queue":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer extCPA.Close()

	st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPA.URL)
	cfg.CollectorMode = "http"
	cfg.Queue = "default-q"
	cfg.PopSide = "right"
	cfg.PollInterval = 25 * time.Millisecond

	ctx := context.Background()
	// Seed ONLY legacy setup(queue="custom-q", popSide="left") with no manager_config_v1 row.
	if _, err := st.SQLDB().Exec(`delete from settings where key = 'manager_config_v1'`); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSetup(ctx, model.Setup{
		CPAUpstreamURL: strings.TrimRight(extCPA.URL, "/"),
		ManagementKey:  extKey,
		Queue:          "custom-q",
		PopSide:        "left",
	}); err != nil {
		t.Fatalf("SaveSetup: %v", err)
	}

	keyBytes, _, err := security.LoadOrCreateDataKey("", cfg.DataKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		t.Fatal(err)
	}

	manager := collector.NewManager(cfg, st)
	t.Cleanup(func() { _ = manager.StopAndWait(context.Background()) })
	serverApp := httpapi.New(cfg, st, manager)
	usageSvc := usageapp.New(usageadapter.New(st.SQLDB()), func() usageapp.CollectionPhase {
		if manager.Status().Collector == "running" {
			return usageapp.PhaseRunning
		}
		return usageapp.PhaseStopped
	})
	ws, closeWS, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
		DB:        st.SQLDB(),
		Protector: protector,
		Usage:     usageSvc,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeWS)

	attachUpgradeUsageStartupObserver(manager, upgradeUsageStartupConfig{
		DB:                   st.SQLDB(),
		Store:                st,
		Protector:            protector,
		ManagerConfigService: serverApp.AppContext().ManagerConfigService,
		Usage:                usageSvc,
		Rejection:            ws.rejection,
	})
	if err := attachCompletedUpgradeRouting(ctx, cfg, st, serverApp, ws.gatewayAdmitted, upgradeCanonicalDependencies{
		Protector: protector,
		Workspace: ws,
	}); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting: %v", err)
	}

	callExt := func(method, path, body string) (int, string) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Authorization", "Bearer "+adminKey)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		serverApp.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	// 1. POST /external/usage inherits Queue="custom-q", PopSide="left" and proves MarkCollectionStart.
	if code, raw := callExt(http.MethodPost, "/ext-console/api/v2/external/usage", `{"requestId":"`+strings.Repeat("1", 32)+`","confirmEnableStatistics":true}`); code != http.StatusOK {
		t.Fatalf("POST /external/usage = %d %s, want 200", code, raw)
	}
	activeCfg, active := manager.ActiveRuntimeConfig()
	if !active || activeCfg.Queue != "custom-q" || activeCfg.PopSide != "left" {
		t.Fatalf("ActiveRuntimeConfig after POST /external/usage = (active=%v, queue=%q, popSide=%q), want (true, custom-q, left)", active, activeCfg.Queue, activeCfg.PopSide)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		startedAt, sErr := usageadapter.New(st.SQLDB()).CollectionStartedAtMS(ctx)
		if sErr == nil && startedAt > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("MarkCollectionStart did not advance: startedAt=%d err=%v status=%+v", startedAt, sErr, manager.Status())
		}
		time.Sleep(15 * time.Millisecond)
	}

	// Clear Queue/PopSide in manager_config_v1 to also verify reconcileCollectorWithProbe direct setup inheritance on PATCH.
	enabled := true
	if err := st.SaveManagerConfig(ctx, model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{
			CPABaseURL:    strings.TrimRight(extCPA.URL, "/"),
			ManagementKey: extKey,
		},
		Collector: model.ManagerCollectorConfig{
			Enabled:        &enabled,
			CollectorMode:  "http",
			PollIntervalMS: 25,
		},
	}); err != nil {
		t.Fatalf("SaveManagerConfig(blank queue/popSide): %v", err)
	}

	// 2. PATCH /cpamp/usage-service/config {"collector":{"batchSize":64}} keeps Queue="custom-q", PopSide="left".
	if code, raw := callExt(http.MethodPatch, "/ext-console/api/v2/cpamp/usage-service/config", `{"collector":{"batchSize":64}}`); code != http.StatusOK {
		t.Fatalf("PATCH /cpamp/usage-service/config = %d %s, want 200", code, raw)
	}
	activeCfg, active = manager.ActiveRuntimeConfig()
	if !active || activeCfg.BatchSize != 64 || activeCfg.Queue != "custom-q" || activeCfg.PopSide != "left" {
		t.Fatalf("ActiveRuntimeConfig after PATCH batchSize=64 = (active=%v, batchSize=%d, queue=%q, popSide=%q), want (true, 64, custom-q, left)", active, activeCfg.BatchSize, activeCfg.Queue, activeCfg.PopSide)
	}
}

type deadlineCapturingCollectorWorker struct {
	onStop func(context.Context)
}

func (w *deadlineCapturingCollectorWorker) Stop(ctx context.Context) {
	if w.onStop != nil {
		w.onStop(ctx)
	}
}

type deadlineCapturingCollectorManager struct {
	onStop func(context.Context) error
}

func (m *deadlineCapturingCollectorManager) StopAndWait(ctx context.Context) error {
	if m.onStop != nil {
		return m.onStop(ctx)
	}
	return nil
}

func TestUpgradeShutdownLegacyRuntimeBindsCollectorStopDeadline(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	stop()
	serverResult := make(chan error)

	var workerCtx, managerCtx context.Context
	var workerHasDeadline, managerHasDeadline bool
	var workerDeadline, managerDeadline time.Time

	workerStub := &deadlineCapturingCollectorWorker{
		onStop: func(c context.Context) {
			workerCtx = c
			workerDeadline, workerHasDeadline = c.Deadline()
		},
	}
	managerStub := &deadlineCapturingCollectorManager{
		onStop: func(c context.Context) error {
			managerCtx = c
			managerDeadline, managerHasDeadline = c.Deadline()
			return nil
		},
	}

	before := time.Now()
	shutdownLegacyRuntime(
		ctx,
		stop,
		serverResult,
		nil,
		nil,
		workerStub,
		nil,
		nil,
		nil,
		nil,
		legacyRuntimeShutdownHooks{
			collectorManager: managerStub,
		},
	)

	if !workerHasDeadline || !managerHasDeadline {
		t.Fatalf("expected bounded deadline on both collector stops, got workerHasDeadline=%v managerHasDeadline=%v", workerHasDeadline, managerHasDeadline)
	}
	if workerCtx != managerCtx || !workerDeadline.Equal(managerDeadline) {
		t.Fatalf("expected shared collectorCtx across collectorWorker.Stop and collectorManager.StopAndWait, got workerDeadline=%v managerDeadline=%v", workerDeadline, managerDeadline)
	}
	budget := workerDeadline.Sub(before)
	if budget < 5*time.Second || budget > 15*time.Second {
		t.Fatalf("unexpected collector shutdown deadline budget: %v", budget)
	}
}
