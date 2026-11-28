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

	identityusagerepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identityusage"
	usageapp "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	keyrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	freshstrategy "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	proxysvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/proxy"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

type closureManagedRuntime struct {
	mu           sync.Mutex
	status       model.RuntimeObservedStatus
	statusErr    error
	statusCalls  int
	startCalls   int
	stopCalls    int
	restartCalls int
	lastReq      model.RuntimeMutationRequest
	startErrOnce error
	onStart      func()
	onStop       func()
}

func (r *closureManagedRuntime) snapshotStart() (int, model.RuntimeMutationRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.startCalls, r.lastReq
}

func (r *closureManagedRuntime) setStatus(st model.RuntimeObservedStatus) {
	r.mu.Lock()
	r.status = st
	r.statusErr = nil
	r.mu.Unlock()
}

func (r *closureManagedRuntime) Status(context.Context) (model.RuntimeObservedStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.statusCalls++
	if r.statusErr != nil {
		return model.RuntimeObservedStatus{}, r.statusErr
	}
	return r.status, nil
}

func (r *closureManagedRuntime) Start(_ context.Context, req model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	r.mu.Lock()
	r.startCalls++
	r.lastReq = req
	hook := r.onStart
	errOnce := r.startErrOnce
	r.startErrOnce = nil
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	if errOnce != nil {
		return model.RuntimeOperationResult{}, errOnce
	}
	return model.RuntimeOperationResult{
		OperationID:       req.OperationID,
		OperationType:     model.RuntimeOperationStart,
		RuntimeIdentity:   req.ExpectedRuntimeIdentity,
		RuntimeGeneration: req.ExpectedRuntimeGeneration,
		State:             model.RuntimeOperationSucceeded,
	}, nil
}

func (r *closureManagedRuntime) Stop(_ context.Context, req model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	r.mu.Lock()
	r.stopCalls++
	r.lastReq = req
	hook := r.onStop
	r.mu.Unlock()
	if hook != nil {
		hook()
	}
	return model.RuntimeOperationResult{
		OperationID:       req.OperationID,
		OperationType:     model.RuntimeOperationStop,
		RuntimeIdentity:   req.ExpectedRuntimeIdentity,
		RuntimeGeneration: req.ExpectedRuntimeGeneration,
		State:             model.RuntimeOperationSucceeded,
	}, nil
}

func (r *closureManagedRuntime) Restart(_ context.Context, req model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	r.mu.Lock()
	r.restartCalls++
	r.lastReq = req
	r.mu.Unlock()
	return model.RuntimeOperationResult{
		OperationID:       req.OperationID,
		OperationType:     model.RuntimeOperationRestart,
		RuntimeIdentity:   req.ExpectedRuntimeIdentity,
		RuntimeGeneration: req.ExpectedRuntimeGeneration,
		State:             model.RuntimeOperationSucceeded,
	}, nil
}

func (r *closureManagedRuntime) PrepareUpdate(context.Context, model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, nil
}

func (r *closureManagedRuntime) ActivateUpdate(context.Context, model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, nil
}

func (r *closureManagedRuntime) ObserveUpdateOperation(context.Context, model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
	return model.RuntimeUpdateOperationObservation{}, nil
}

func TestUpgradeProvenanceAndManagedRuntimeDestinationIdentityFences(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	t.Run("incomplete-provenance-fails-closed-on-startup-attach", func(t *testing.T) {
		st, cfg, _ := seedCompletedManagedUpgradeStore(t, "/upg-console", "/upg-gw")
		if _, err := st.SQLDB().Exec(`update upgrade_operations set backup_manifest_sha256 = '' where id = 1`); err != nil {
			t.Fatal(err)
		}
		serverApp := httpapi.New(cfg, st, nil)
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil); err == nil {
			t.Fatal("expected attachCompletedUpgradeRouting to fail closed when backup_manifest_sha256 is missing")
		}
		rec := httptest.NewRecorder()
		serverApp.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/upg-console/api/v2/upgrade/operation", nil))
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "upgrade_authority_unavailable") {
			t.Fatalf("incomplete provenance status=%d body=%s, want 503 upgrade_authority_unavailable", rec.Code, rec.Body.String())
		}
	})

	t.Run("managed-runtime-status-artifact-and-operations-enforce-destination-and-live-identity", func(t *testing.T) {
		mgmtKey := strings.Repeat("b", 64)
		clientKey := "sk-managed-fence-key-01"
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

		var artifactHits atomic.Int32
		supervisorArtifactServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			artifactHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"activeArtifactId":"art-1"}`))
		}))
		defer supervisorArtifactServer.Close()

		st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/upg-console", "/upg-gw")
		tokenFile := filepath.Join(t.TempDir(), "runtime.token")
		if err := os.WriteFile(tokenFile, []byte("supervisor-test-token\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cfg.RuntimeURL = supervisorArtifactServer.URL
		cfg.RuntimeTokenFile = tokenFile

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
		sourceIdentity := "external:sha256:" + strings.Repeat("c", 64)
		pluginsInit := false
		destBytes, _ := json.Marshal(upgradeDestinationRecord{
			BaseURL:           cpaServer.URL,
			URLProved:         true,
			ManagementKey:     sealedDestKey,
			ServerFingerprint: "external:sha256:" + strings.Repeat("b", 64),
			Attempt:           1,
			OperationID:       upgradeManagedDestinationOperationID(sourceIdentity, 1),
			Intent: upgradeDestinationIntent{
				ExpectedRuntimeIdentity:   "rt-managed-expected",
				ExpectedRuntimeGeneration: 7,
				ExpectedActiveArtifactID:  qualifiedArtifact,
				SourceIdentity:            sourceIdentity,
				SourceClass:               upgradeManagedDestinationClass,
				APIKeys:                   []string{clientKey},
				PluginsEnabled:            &pluginsInit,
			},
		})
		if _, err := st.SQLDB().Exec(`insert into settings(key, value, updated_at_ms) values(?, ?, ?)`,
			upgradeImportDestinationSettingKey, string(destBytes), time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
		if _, err := st.SetEmbeddedRuntimeDesiredLifecycle(context.Background(), model.EmbeddedRuntimeDesiredRunning); err != nil {
			t.Fatal(err)
		}

		expectedStatus := model.RuntimeObservedStatus{
			Identity:           "rt-managed-expected",
			Generation:         11,
			ProtocolVersion:    "v1",
			State:              model.RuntimeStateReady,
			CPAObservedVersion: "v8.0.55",
			Capabilities:       model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStop, model.RuntimeCapabilityRestart},
			ActiveGatewayArtifact: &model.ActiveGatewayArtifact{
				Engine:     "cpa",
				ArtifactID: qualifiedArtifact,
				Version:    "v8.0.55",
			},
		}
		rt := &closureManagedRuntime{status: expectedStatus}

		serverApp := httpapi.New(cfg, st, nil)
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, func(context.Context) bool { return true }, upgradeCanonicalDependencies{
			Protector: protector,
			Runtime:   rt,
		}); err != nil {
			t.Fatalf("attachCompletedUpgradeRouting: %v", err)
		}
		ts := httptest.NewServer(serverApp.Handler())
		defer ts.Close()

		authReq := func(method, path, reqBody string) *http.Response {
			t.Helper()
			var reader io.Reader
			if reqBody != "" {
				reader = strings.NewReader(reqBody)
			}
			req, err := http.NewRequest(method, ts.URL+path, reader)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+adminKey)
			if reqBody != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			return res
		}

		opID := strings.Repeat("1", 32)
		okOp := authReq(http.MethodPost, "/upg-console/api/v2/runtime/operations", `{"requestId":"`+opID+`","action":"restart"}`)
		_ = okOp.Body.Close()
		if okOp.StatusCode != http.StatusOK || rt.restartCalls != 1 {
			t.Fatalf("verified runtime restart status=%d calls=%d, want 200/1", okOp.StatusCode, rt.restartCalls)
		}

		// Switch live Supervisor identity away from ExpectedRuntimeIdentity: status, artifact, GET op, and POST op all fail closed.
		foreignStatus := expectedStatus
		foreignStatus.Identity = "rt-foreign-unexpected"
		rt.setStatus(foreignStatus)

		stRes := authReq(http.MethodGet, "/upg-console/api/v2/runtime/status", "")
		var stView freshRuntimeStatusResponse
		_ = json.NewDecoder(stRes.Body).Decode(&stView)
		_ = stRes.Body.Close()
		if stRes.StatusCode != http.StatusOK || stView.Status != "unknown" || stView.ErrorCode != "runtime_unavailable" {
			t.Fatalf("mismatched identity runtime/status code=%d view=%+v, want unknown/runtime_unavailable", stRes.StatusCode, stView)
		}

		beforeArt := artifactHits.Load()
		artRes := authReq(http.MethodGet, "/upg-console/api/v2/runtime/artifact", "")
		_ = artRes.Body.Close()
		if artRes.StatusCode != http.StatusServiceUnavailable || artifactHits.Load() != beforeArt {
			t.Fatalf("mismatched identity runtime/artifact status=%d hits=%d, want 503 and no upstream call", artRes.StatusCode, artifactHits.Load())
		}

		getOpRes := authReq(http.MethodGet, "/upg-console/api/v2/runtime/operations/"+opID, "")
		_ = getOpRes.Body.Close()
		if getOpRes.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("mismatched identity GET runtime/operations status=%d, want 503", getOpRes.StatusCode)
		}

		postOpRes := authReq(http.MethodPost, "/upg-console/api/v2/runtime/operations", `{"requestId":"`+strings.Repeat("2", 32)+`","action":"stop"}`)
		_ = postOpRes.Body.Close()
		if postOpRes.StatusCode != http.StatusServiceUnavailable || rt.stopCalls != 0 {
			t.Fatalf("mismatched identity POST runtime/operations status=%d stopCalls=%d, want 503/0", postOpRes.StatusCode, rt.stopCalls)
		}

		// Restore live identity, then remove verified destination record: all four surfaces still fail closed.
		rt.setStatus(expectedStatus)
		if _, err := st.SQLDB().Exec(`delete from settings where key = ?`, upgradeImportDestinationSettingKey); err != nil {
			t.Fatal(err)
		}
		stMissing := authReq(http.MethodGet, "/upg-console/api/v2/runtime/status", "")
		var stMissingView freshRuntimeStatusResponse
		_ = json.NewDecoder(stMissing.Body).Decode(&stMissingView)
		_ = stMissing.Body.Close()
		if stMissingView.Status != "unknown" || stMissingView.ErrorCode != "runtime_unavailable" {
			t.Fatalf("missing destination runtime/status = %+v, want unknown/runtime_unavailable", stMissingView)
		}
		postMissing := authReq(http.MethodPost, "/upg-console/api/v2/runtime/operations", `{"requestId":"`+strings.Repeat("3", 32)+`","action":"start"}`)
		_ = postMissing.Body.Close()
		if postMissing.StatusCode != http.StatusServiceUnavailable || rt.startCalls != 0 {
			t.Fatalf("missing destination POST runtime/operations status=%d startCalls=%d, want 503/0", postMissing.StatusCode, rt.startCalls)
		}

		// Source fingerprint drift on upgrade_operations also fails closed on loadCurrentOp.
		driftedFpr := "sha256:" + strings.Repeat("9", 64)
		driftedVer, err := json.Marshal(model.UpgradeVerification{
			TargetMode:        model.UpgradeTargetManaged,
			SourceFingerprint: driftedFpr,
			Domains:           []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig},
			ObservedAtMS:      2000,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.SQLDB().Exec(
			`update upgrade_operations set source_fingerprint_before = ?, source_fingerprint_after = ?, verification = ? where id = 1`,
			driftedFpr, driftedFpr, string(driftedVer)); err != nil {
			t.Fatal(err)
		}
		opDriftRes := authReq(http.MethodGet, "/upg-console/api/v2/upgrade/operation", "")
		_ = opDriftRes.Body.Close()
		if opDriftRes.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("drifted sourceFingerprint GET upgrade/operation status=%d, want 503", opDriftRes.StatusCode)
		}
	})
}

func TestUpgradeExternalStoreFenceAtomicRebindAndRefusalHook(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	oldKey := strings.Repeat("a", 64)
	newKey := strings.Repeat("e", 64)
	var activeKey atomic.Value
	activeKey.Store(oldKey)
	var returnForbidden atomic.Bool
	var configHits atomic.Int32
	var totalHits atomic.Int32

	extCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		totalHits.Add(1)
		if r.URL.Path == "/v0/management/config" {
			configHits.Add(1)
		}
		if returnForbidden.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+activeKey.Load().(string) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.55")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/config":
			_, _ = w.Write([]byte(`{"port":8317}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config":
			_, _ = w.Write([]byte(`{"config-version":8,"access":{"api-keys":[]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer extCPA.Close()

	st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPA.URL)
	if err := st.SaveManagerConfig(context.Background(), model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{
			CPABaseURL:    extCPA.URL,
			ManagementKey: oldKey,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveSetup(context.Background(), model.Setup{
		CPAUpstreamURL: extCPA.URL,
		ManagementKey:  oldKey,
	}); err != nil {
		t.Fatal(err)
	}

	keyBytes, _, err := security.LoadOrCreateDataKey("", cfg.DataKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	ws, closeWS, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
		DB:        st.SQLDB(),
		Protector: protector,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeWS)

	var recordedCalls atomic.Int32
	var recordedIdentity atomic.Value
	ws.rejection.setOnRecord(func(_ context.Context, identity, _ string) {
		recordedCalls.Add(1)
		recordedIdentity.Store(identity)
	})

	serverApp := httpapi.New(cfg, st, nil)
	serverApp.SetUpgradeAPIKeys(ws.serveHTTP)
	var canonical *upgradeCanonicalServer
	canonicalCtx, cancelCanonical := context.WithCancel(context.Background())
	if err := attachCompletedUpgradeRouting(canonicalCtx, cfg, st, serverApp, ws.gatewayAdmitted, upgradeCanonicalDependencies{
		Workspace:    ws,
		Protector:    protector,
		CanonicalOut: &canonical,
	}); err != nil {
		cancelCanonical()
		t.Fatal(err)
	}
	if canonical != nil && canonical.pluginCapability != nil {
		canonical.pluginCapability.close()
		canonical.pluginCapability = nil
	}
	t.Cleanup(func() {
		cancelCanonical()
		if canonical != nil {
			canonical.Wait()
		}
	})
	ts := httptest.NewServer(serverApp.Handler())
	defer ts.Close()

	authReq := func(method, path, reqBody string) *http.Response {
		t.Helper()
		var reader io.Reader
		if reqBody != "" {
			reader = strings.NewReader(reqBody)
		}
		req, err := http.NewRequest(method, ts.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+adminKey)
		if reqBody != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// 1. Key workspace enforces Store URL/credential fence when manager_config_v1 drifts.
	if err := st.SaveManagerConfig(context.Background(), model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{
			CPABaseURL:    "https://drifted-store-cpa.example:8317",
			ManagementKey: oldKey,
		},
	}); err != nil {
		t.Fatal(err)
	}
	movedList := authReq(http.MethodGet, "/ext-console/api/v2/api-keys", "")
	movedBytes, _ := io.ReadAll(movedList.Body)
	_ = movedList.Body.Close()
	if movedList.StatusCode != http.StatusConflict || !strings.Contains(string(movedBytes), freshconfig.ReasonTargetMoved) {
		t.Fatalf("drifted manager_config GET api-keys = %d %s, want 409 %s", movedList.StatusCode, string(movedBytes), freshconfig.ReasonTargetMoved)
	}

	// Restore aligned manager_config_v1.
	if err := st.SaveManagerConfig(context.Background(), model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{
			CPABaseURL:    extCPA.URL,
			ManagementKey: oldKey,
		},
	}); err != nil {
		t.Fatal(err)
	}

	assertSingleJSON := func(label string, raw []byte) map[string]any {
		t.Helper()
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s invalid JSON (%v): %s", label, err, string(raw))
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		var single map[string]any
		if err := dec.Decode(&single); err != nil {
			t.Fatalf("%s decode failed (%v): %s", label, err, string(raw))
		}
		var trailing any
		if err := dec.Decode(&trailing); err != io.EOF {
			t.Fatalf("%s returned trailing JSON data (%v): %s", label, err, string(raw))
		}
		return out
	}

	// 2a. Bridged 200 request executes upstream strictly once and writes one valid JSON body.
	before200Config := configHits.Load()
	before200Total := totalHits.Load()
	okBridgeRes := authReq(http.MethodGet, "/ext-console/api/v2/cpamp/v0/management/config", "")
	okBridgeBytes, _ := io.ReadAll(okBridgeRes.Body)
	_ = okBridgeRes.Body.Close()
	if okBridgeRes.StatusCode != http.StatusOK {
		t.Fatalf("legacy 200 status=%d body=%s, want 200", okBridgeRes.StatusCode, string(okBridgeBytes))
	}
	if gotConfig, gotTotal := configHits.Load()-before200Config, totalHits.Load()-before200Total; gotConfig != 1 || gotTotal != 1 {
		t.Fatalf("legacy 200 upstream hits config=%d total=%d, want 1/1", gotConfig, gotTotal)
	}
	okPayload := assertSingleJSON("legacy 200", okBridgeBytes)
	if okPayload["port"] != float64(8317) {
		t.Fatalf("legacy 200 body=%+v, want port=8317", okPayload)
	}

	// 2b. Legacy bridge HTTP 403 executes upstream strictly once, records durable refusal, and writes one valid JSON body.
	before403Config := configHits.Load()
	before403Total := totalHits.Load()
	returnForbidden.Store(true)
	legacyRes := authReq(http.MethodGet, "/ext-console/api/v2/cpamp/v0/management/config", "")
	legacyBytes, _ := io.ReadAll(legacyRes.Body)
	_ = legacyRes.Body.Close()
	returnForbidden.Store(false)
	if legacyRes.StatusCode != http.StatusConflict || recordedCalls.Load() == 0 {
		t.Fatalf("legacy 403 status=%d hookCalls=%d body=%s, want 409 and hookCalls>0", legacyRes.StatusCode, recordedCalls.Load(), string(legacyBytes))
	}
	if gotConfig, gotTotal := configHits.Load()-before403Config, totalHits.Load()-before403Total; gotConfig != 1 || gotTotal != 1 {
		t.Fatalf("legacy 403 upstream hits config=%d total=%d, want 1/1", gotConfig, gotTotal)
	}
	errPayload := assertSingleJSON("legacy 403", legacyBytes)
	if errPayload["code"] != freshconfig.ReasonManagementRejected {
		t.Fatalf("legacy 403 code=%v, want %q (body=%s)", errPayload["code"], freshconfig.ReasonManagementRejected, string(legacyBytes))
	}
	extIdentity := freshkeyapply.ExternalIdentity(strings.TrimRight(extCPA.URL, "/"))
	if gotID, _ := recordedIdentity.Load().(string); gotID != extIdentity {
		t.Fatalf("hook recorded identity = %q, want %q", gotID, extIdentity)
	}
	after403Keys := authReq(http.MethodGet, "/ext-console/api/v2/api-keys", "")
	after403Bytes, _ := io.ReadAll(after403Keys.Body)
	_ = after403Keys.Body.Close()
	if after403Keys.StatusCode != http.StatusConflict || !strings.Contains(string(after403Bytes), freshconfig.ReasonManagementRejected) {
		t.Fatalf("post-403 GET api-keys = %d %s, want 409 %s", after403Keys.StatusCode, string(after403Bytes), freshconfig.ReasonManagementRejected)
	}

	// 3. Rebind rolls back atomically and does not return rebound success when stored settings alignment fails.
	activeKey.Store(newKey)
	if _, err := st.SQLDB().Exec(`update settings set value = '{corrupt-manager-config' where key = 'manager_config_v1'`); err != nil {
		t.Fatal(err)
	}
	failedRebind := authReq(http.MethodPost, "/ext-console/api/v2/external/rebind",
		`{"requestId":"`+strings.Repeat("1", 32)+`","managementKey":"`+newKey+`"}`)
	failedRebindBody, _ := io.ReadAll(failedRebind.Body)
	_ = failedRebind.Body.Close()
	if failedRebind.StatusCode == http.StatusOK || strings.Contains(string(failedRebindBody), `"rebound"`) {
		t.Fatalf("rebind succeeded despite broken settings alignment: status=%d body=%s", failedRebind.StatusCode, string(failedRebindBody))
	}
	bindingAfterFail, found, err := upgradeoperation.New(st.SQLDB()).ExternalBinding(context.Background())
	if err != nil || !found || bindingAfterFail.ObservedAtMS != 2000 {
		t.Fatalf("binding mutated on failed rebind alignment: found=%t binding=%+v err=%v", found, bindingAfterFail, err)
	}

	// Repair manager_config_v1 and verify atomic rebind updates binding + manager_config_v1 + setup together.
	if err := st.SaveManagerConfig(context.Background(), model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{
			CPABaseURL:    extCPA.URL,
			ManagementKey: oldKey,
		},
	}); err != nil {
		t.Fatal(err)
	}
	okRebind := authReq(http.MethodPost, "/ext-console/api/v2/external/rebind",
		`{"requestId":"`+strings.Repeat("2", 32)+`","managementKey":"`+newKey+`"}`)
	_ = okRebind.Body.Close()
	if okRebind.StatusCode != http.StatusOK {
		t.Fatalf("rebind after repairing settings status=%d, want 200", okRebind.StatusCode)
	}
	okKeys := authReq(http.MethodGet, "/ext-console/api/v2/api-keys", "")
	_ = okKeys.Body.Close()
	if okKeys.StatusCode != http.StatusOK {
		t.Fatalf("GET api-keys after atomic rebind status=%d, want 200", okKeys.StatusCode)
	}
}

func TestUpgradeKeyCreateReplayDetailFailClosedAndLocalRename(t *testing.T) {
	mgmtKey := strings.Repeat("a", 64)
	var mu sync.Mutex
	cpaKeys := []string{"existing-client-key"}
	var managementUnavailable atomic.Bool
	var failKeyWriteUnknown atomic.Bool

	extCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			if managementUnavailable.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if r.Header.Get("Authorization") != "Bearer "+mgmtKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("X-CPA-Version", "v8.0.55")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/config":
			_, _ = w.Write([]byte(`{"port":8317}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config/access/api-keys":
			mu.Lock()
			snapshot := append([]string(nil), cpaKeys...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(snapshot)
		case r.Method == http.MethodGet && (r.URL.Path == "/v8/management/config" || r.URL.Path == "/v8/management/config.yaml"):
			mu.Lock()
			snapshot := append([]string(nil), cpaKeys...)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"config-version": 8,
				"server":         map[string]any{"port": 8317},
				"oauth":          map[string]any{"auth-dir": "/isolated-auth"},
				"access":         map[string]any{"api-keys": snapshot},
			})
		case r.Method == http.MethodPut && r.URL.Path == "/v8/management/config/access/api-keys":
			if failKeyWriteUnknown.Load() {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			var next []string
			_ = json.NewDecoder(r.Body).Decode(&next)
			mu.Lock()
			cpaKeys = append([]string(nil), next...)
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			mu.Lock()
			valid := false
			for _, k := range cpaKeys {
				if k == auth {
					valid = true
					break
				}
			}
			mu.Unlock()
			if !valid {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer extCPA.Close()

	st, cfg, _ := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPA.URL)
	keyBytes, _, err := security.LoadOrCreateDataKey("", cfg.DataKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	ws, closeWS, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
		DB:        st.SQLDB(),
		Protector: protector,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeWS)

	callWS := func(ctx context.Context, method, resource, reqBody string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, currentInstallManagementAPIV2Prefix+resource, bytes.NewBufferString(reqBody)).WithContext(ctx)
		if reqBody != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		ws.serveHTTP(rec, req, resource)
		return rec
	}

	// Rename remains available while CPA management is unavailable.
	reqID1 := strings.Repeat("1", 32)
	createdRec := callWS(context.Background(), http.MethodPost, "api-keys", `{"requestId":"`+reqID1+`"}`)
	if createdRec.Code != http.StatusCreated {
		t.Fatalf("POST api-keys = %d %s, want 201", createdRec.Code, createdRec.Body.String())
	}
	var created clientKeyCreateResponse
	if err := json.Unmarshal(createdRec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	managementUnavailable.Store(true)
	renameRec := callWS(context.Background(), http.MethodPut, "api-keys/"+string(created.Key.ID), `{"name":"Renamed Local","nameRevision":1}`)
	if renameRec.Code != http.StatusOK {
		t.Fatalf("local rename with unavailable CPA = %d %s, want 200", renameRec.Code, renameRec.Body.String())
	}
	var renamedKey freshkeys.Key
	if err := json.Unmarshal(renameRec.Body.Bytes(), &renamedKey); err != nil || renamedKey.Name != "Renamed Local" || renamedKey.NameRevision != 2 {
		t.Fatalf("unexpected renamed key: %+v err=%v", renamedKey, err)
	}
	managementUnavailable.Store(false)

	// 2. Remove created key out-of-band from CPA; replay of reqID1 must NOT claim effective_observed or leak terminal OperationID.
	mu.Lock()
	cpaKeys = []string{"existing-client-key"}
	mu.Unlock()
	replayRemoved := callWS(context.Background(), http.MethodPost, "api-keys", `{"requestId":"`+reqID1+`"}`)
	if replayRemoved.Code != http.StatusConflict && replayRemoved.Code != http.StatusServiceUnavailable {
		t.Fatalf("removed-key replay must report conflict or unresolved current effect, got %d %s", replayRemoved.Code, replayRemoved.Body.String())
	}
	var replayErr clientKeyCreateErrorResponse
	if err := json.Unmarshal(replayRemoved.Body.Bytes(), &replayErr); err != nil || replayErr.Key == nil {
		t.Fatalf("decode replay error = %v body=%s", err, replayRemoved.Body.String())
	}
	if replayErr.Key.ID != created.Key.ID || replayErr.Key.OperationID != "" || (replayErr.Key.RuntimeState != "not_applied" && replayErr.Key.RuntimeState != "effect_unknown") {
		t.Fatalf("removed-key replay must preserve identity without effective or pending state: %+v", *replayErr.Key)
	}

	// 3. Create keeper + target, revoke target, then replay target's create requestId -> 409 with revoked_observed and empty OperationID.
	keeperCreate := callWS(context.Background(), http.MethodPost, "api-keys", `{"requestId":"`+strings.Repeat("5", 32)+`"}`)
	if keeperCreate.Code != http.StatusCreated {
		t.Fatalf("keeper POST api-keys = %d %s", keeperCreate.Code, keeperCreate.Body.String())
	}
	var keeperBody clientKeyCreateResponse
	_ = json.Unmarshal(keeperCreate.Body.Bytes(), &keeperBody)
	reqIDRevoked := strings.Repeat("6", 32)
	targetCreate := callWS(context.Background(), http.MethodPost, "api-keys", `{"requestId":"`+reqIDRevoked+`"}`)
	if targetCreate.Code != http.StatusCreated {
		t.Fatalf("target POST api-keys = %d %s", targetCreate.Code, targetCreate.Body.String())
	}
	var targetBody clientKeyCreateResponse
	_ = json.Unmarshal(targetCreate.Body.Bytes(), &targetBody)
	revokeOpID := strings.Repeat("7", 32)
	prepRevoke := callWS(context.Background(), http.MethodPost, "api-keys/"+string(targetBody.Key.ID)+"/revocation",
		`{"requestId":"`+revokeOpID+`","keeperId":"`+string(keeperBody.Key.ID)+`","mode":"revoke"}`)
	if prepRevoke.Code != http.StatusOK {
		t.Fatalf("prepare revoke = %d %s", prepRevoke.Code, prepRevoke.Body.String())
	}
	contRevoke := callWS(context.Background(), http.MethodPost, "api-keys/operations/"+revokeOpID+"/continue", `{}`)
	if contRevoke.Code != http.StatusOK {
		t.Fatalf("continue revoke = %d %s", contRevoke.Code, contRevoke.Body.String())
	}
	replayRevoked := callWS(context.Background(), http.MethodPost, "api-keys", `{"requestId":"`+reqIDRevoked+`"}`)
	if replayRevoked.Code != http.StatusConflict {
		t.Fatalf("replay of revoked key = %d %s, want 409", replayRevoked.Code, replayRevoked.Body.String())
	}
	var replayRevokedErr clientKeyCreateErrorResponse
	if err := json.Unmarshal(replayRevoked.Body.Bytes(), &replayRevokedErr); err != nil || replayRevokedErr.Key == nil {
		t.Fatalf("decode revoked replay error = %v body=%s", err, replayRevoked.Body.String())
	}
	if replayRevokedErr.Key.OperationID != "" || replayRevokedErr.Key.Lifecycle != identity.LifecycleSuperseded || replayRevokedErr.Key.RuntimeState == "effective_observed" {
		t.Fatalf("retired replay must preserve retired identity without effective or pending state: %+v", *replayRevokedErr.Key)
	}

	// 4. Unknown write failure keeps non-terminal OperationID on first attempt and records OutcomeUnknown in keyrepo.
	failKeyWriteUnknown.Store(true)
	reqID2 := strings.Repeat("2", 32)
	unknownCreate := callWS(context.Background(), http.MethodPost, "api-keys", `{"requestId":"`+reqID2+`"}`)
	failKeyWriteUnknown.Store(false)
	if unknownCreate.Code != http.StatusServiceUnavailable {
		t.Fatalf("unknown create = %d %s, want 503", unknownCreate.Code, unknownCreate.Body.String())
	}
	storedUnknownOp, err := keyrepo.New(st.SQLDB()).Operation(context.Background(), reqID2)
	if err != nil || storedUnknownOp == nil || storedUnknownOp.Outcome != keyrepo.OutcomeUnknown {
		t.Fatalf("expected OutcomeUnknown journal entry, got op=%+v err=%v", storedUnknownOp, err)
	}
	// 4. GET api-keys/{id} and GET api-keys/requests/{id} fail closed on cancellation, refusal, and unreadable rejection table instead of HTTP 200 effect_unknown.
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelGet := callWS(cancelledCtx, http.MethodGet, "api-keys/"+string(created.Key.ID), "")
	if cancelGet.Code == http.StatusOK {
		t.Fatalf("cancelled GET api-keys/{id} returned 200: %s", cancelGet.Body.String())
	}

	extIdentity := freshkeyapply.ExternalIdentity(strings.TrimRight(extCPA.URL, "/"))
	binding, _, err := upgradeoperation.New(st.SQLDB()).ExternalBinding(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := ws.rejection.record(context.Background(), extIdentity, envelopeDigest(binding.ManagementKey)); err != nil {
		t.Fatal(err)
	}
	refusedDetail := callWS(context.Background(), http.MethodGet, "api-keys/"+string(created.Key.ID), "")
	if refusedDetail.Code != http.StatusConflict || !strings.Contains(refusedDetail.Body.String(), freshconfig.ReasonManagementRejected) {
		t.Fatalf("refused GET api-keys/{id} = %d %s, want 409 %s", refusedDetail.Code, refusedDetail.Body.String(), freshconfig.ReasonManagementRejected)
	}
	refusedLookup := callWS(context.Background(), http.MethodGet, "api-keys/requests/"+reqID1, "")
	if refusedLookup.Code != http.StatusConflict || !strings.Contains(refusedLookup.Body.String(), freshconfig.ReasonManagementRejected) {
		t.Fatalf("refused GET api-keys/requests/{id} = %d %s, want 409 %s", refusedLookup.Code, refusedLookup.Body.String(), freshconfig.ReasonManagementRejected)
	}
	if err := ws.rejection.clear(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, err := st.SQLDB().Exec(`INSERT INTO settings(key, value, updated_at_ms) VALUES(?, '{corrupt', 1) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, externalRejectionKey); err != nil {
		t.Fatal(err)
	}
	unreadableDetail := callWS(context.Background(), http.MethodGet, "api-keys/"+string(created.Key.ID), "")
	if unreadableDetail.Code != http.StatusServiceUnavailable {
		t.Fatalf("unreadable rejection table GET api-keys/{id} = %d %s, want 503", unreadableDetail.Code, unreadableDetail.Body.String())
	}
}

func TestUpgradeExternalUsageRefusesUnconfirmedStatisticsEnable(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	mgmtKey := strings.Repeat("a", 64)

	extCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+mgmtKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.55")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/config":
			_, _ = w.Write([]byte(`{"port":8317}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/usage-statistics-enabled":
			_, _ = w.Write([]byte(`{"usage-statistics-enabled":false}`))
		case r.Method == http.MethodPut && r.URL.Path == "/v0/management/usage-statistics-enabled":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/usage":
			_, _ = w.Write([]byte(`{"usage":{}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer extCPA.Close()

	st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPA.URL)
	keyBytes, _, err := security.LoadOrCreateDataKey("", cfg.DataKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	usageRepo := identityusagerepo.New(st.SQLDB())
	usageSvc := usageapp.New(usageRepo, nil)
	ws, closeWS, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
		DB:        st.SQLDB(),
		Protector: protector,
		Usage:     usageSvc,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeWS)

	manager := collector.NewManager(cfg, st)
	t.Cleanup(func() { manager.Stop() })

	serverApp := httpapi.New(cfg, st, manager)
	serverApp.SetUpgradeAPIKeys(ws.serveHTTP)
	if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, ws.gatewayAdmitted, upgradeCanonicalDependencies{
		Workspace: ws,
		Protector: protector,
	}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(serverApp.Handler())
	defer ts.Close()

	authReq := func(method, path, reqBody string) *http.Response {
		t.Helper()
		var reader io.Reader
		if reqBody != "" {
			reader = strings.NewReader(reqBody)
		}
		req, err := http.NewRequest(method, ts.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+adminKey)
		if reqBody != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// 1. When PUT returns 200 but read-back still reports false, POST external/usage fails closed and does not start collection.
	unprovenPost := authReq(http.MethodPost, "/ext-console/api/v2/external/usage",
		`{"requestId":"`+strings.Repeat("1", 32)+`","confirmEnableStatistics":true}`)
	_ = unprovenPost.Body.Close()
	if unprovenPost.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("unproven enable read-back status=%d, want 503", unprovenPost.StatusCode)
	}

}

func TestUpgradeGatewayConfigHoldsPartialWriteReconcileAndRequestIDSemantics(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	t.Run("managed-partial-multiwrite-reconciliation-superseded-not-applied-and-requestid-reuse", func(t *testing.T) {
		mgmtKey := strings.Repeat("b", 64)
		var docMu sync.Mutex
		doc := map[string]any{
			"config-version": 8,
			"access":         map[string]any{"api-keys": []string{"sk-cfg-1"}},
			"routing": map[string]any{
				"retry": map[string]any{
					"request-retry":      3,
					"max-retry-interval": 30,
				},
			},
		}
		var failDelete atomic.Bool
		var failPatchUnknown atomic.Bool
		var overwriteAfterPatch atomic.Bool

		filterEndpoint, _ := cpa.ConfigValueEndpoint("filter")
		filterFamily, _ := cpa.LookupConfigRuleFamily("filter")
		filterSegs := strings.Split(filterFamily.Path, ".")
		cpaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+mgmtKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("X-CPA-Version", "v8.0.55")
			w.Header().Set("Content-Type", "application/json")
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config":
				docMu.Lock()
				encoded, _ := json.Marshal(doc)
				docMu.Unlock()
				_, _ = w.Write(encoded)
			case r.Method == http.MethodPatch && r.URL.Path == "/v8/management/config/routing/retry":
				if failPatchUnknown.Load() {
					w.WriteHeader(http.StatusBadGateway)
					return
				}
				var patch map[string]any
				_ = json.NewDecoder(r.Body).Decode(&patch)
				docMu.Lock()
				routingMap, _ := doc["routing"].(map[string]any)
				retryMap, _ := routingMap["retry"].(map[string]any)
				for k, v := range patch {
					if overwriteAfterPatch.Load() {
						retryMap[k] = 99
					} else {
						retryMap[k] = v
					}
				}
				docMu.Unlock()
				_, _ = w.Write([]byte(`{"ok":true}`))
			case r.Method == http.MethodDelete && r.URL.Path == "/v8/management/config/routing/retry/max-retry-interval":
				if failDelete.Load() {
					w.WriteHeader(http.StatusUnprocessableEntity)
					_, _ = w.Write([]byte(`{"error":"invalid_config"}`))
					return
				}
				docMu.Lock()
				routingMap, _ := doc["routing"].(map[string]any)
				retryMap, _ := routingMap["retry"].(map[string]any)
				delete(retryMap, "max-retry-interval")
				docMu.Unlock()
				w.WriteHeader(http.StatusNoContent)
			case r.Method == http.MethodPut && r.URL.Path == filterEndpoint:
				var rules []any
				_ = json.NewDecoder(r.Body).Decode(&rules)
				docMu.Lock()
				cur := doc
				for _, seg := range filterSegs[:len(filterSegs)-1] {
					next, ok := cur[seg].(map[string]any)
					if !ok {
						next = map[string]any{}
						cur[seg] = next
					}
					cur = next
				}
				cur[filterSegs[len(filterSegs)-1]] = rules
				docMu.Unlock()
				_, _ = w.Write([]byte(`{"ok":true}`))
			default:
				http.NotFound(w, r)
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
		sourceIdentity := "external:sha256:" + strings.Repeat("c", 64)
		pluginsInit := false
		destBytes, _ := json.Marshal(upgradeDestinationRecord{
			BaseURL:           cpaServer.URL,
			URLProved:         true,
			ManagementKey:     sealedDestKey,
			ServerFingerprint: "external:sha256:" + strings.Repeat("b", 64),
			Attempt:           1,
			OperationID:       upgradeManagedDestinationOperationID(sourceIdentity, 1),
			Intent: upgradeDestinationIntent{
				ExpectedRuntimeIdentity:   "rt-cfg-1",
				ExpectedRuntimeGeneration: 7,
				ExpectedActiveArtifactID:  qualifiedArtifact,
				SourceIdentity:            sourceIdentity,
				SourceClass:               upgradeManagedDestinationClass,
				APIKeys:                   []string{"sk-cfg-1"},
				PluginsEnabled:            &pluginsInit,
			},
		})
		if _, err := st.SQLDB().Exec(`insert into settings(key, value, updated_at_ms) values(?, ?, ?)`,
			upgradeImportDestinationSettingKey, string(destBytes), time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
		rt := &closureManagedRuntime{
			status: model.RuntimeObservedStatus{
				Identity:           "rt-cfg-1",
				Generation:         7,
				ProtocolVersion:    "v1",
				State:              model.RuntimeStateReady,
				CPAObservedVersion: "v8.0.55",
				Capabilities:       model.RuntimeCapabilities{model.RuntimeCapabilityRestart},
				ActiveGatewayArtifact: &model.ActiveGatewayArtifact{
					Engine:     "cpa",
					ArtifactID: qualifiedArtifact,
					Version:    "v8.0.55",
				},
			},
		}
		serverApp := httpapi.New(cfg, st, nil)
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, func(context.Context) bool { return true }, upgradeCanonicalDependencies{
			Protector: protector,
			Runtime:   rt,
		}); err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(serverApp.Handler())
		defer ts.Close()

		authReq := func(method, path, reqBody string) *http.Response {
			t.Helper()
			var reader io.Reader
			if reqBody != "" {
				reader = strings.NewReader(reqBody)
			}
			req, err := http.NewRequest(method, ts.URL+path, reader)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+adminKey)
			if reqBody != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			return res
		}

		readRetryBaseline := func() string {
			t.Helper()
			res := authReq(http.MethodGet, "/upg-console/api/v2/gateway/config", "")
			var view freshconfig.View
			_ = json.NewDecoder(res.Body).Decode(&view)
			_ = res.Body.Close()
			for _, g := range view.Groups {
				if g.ID == "retry" {
					return g.Baseline
				}
			}
			t.Fatal("missing retry group baseline")
			return ""
		}

		// 1. Multi-write PATCH succeeds + DELETE fails -> 409 original_operation_unknown (never failed).
		base1 := readRetryBaseline()
		reqIDPartial := strings.Repeat("1", 32)
		partialBody := `{"requestId":"` + reqIDPartial + `","expected":{"generation":"7","baseline":"` + base1 + `"},"edits":[{"path":"routing.retry.request-retry","action":"set","value":5},{"path":"routing.retry.max-retry-interval","action":"default"}]}`
		failDelete.Store(true)
		partialRes := authReq(http.MethodPost, "/upg-console/api/v2/gateway/config/groups/retry", partialBody)
		var partialErr struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(partialRes.Body).Decode(&partialErr)
		_ = partialRes.Body.Close()
		failDelete.Store(false)
		if partialRes.StatusCode != http.StatusConflict || partialErr.Code != "original_operation_unknown" {
			t.Fatalf("partial multi-write status=%d body=%+v, want 409 original_operation_unknown", partialRes.StatusCode, partialErr)
		}

		// Replaying same requestId while State=="unknown" returns 409 original_operation_unknown; differing edits returns 409 source_or_operation_conflict.
		sameUnknownReplay := authReq(http.MethodPost, "/upg-console/api/v2/gateway/config/groups/retry", partialBody)
		var sameUnknownErr struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(sameUnknownReplay.Body).Decode(&sameUnknownErr)
		_ = sameUnknownReplay.Body.Close()
		if sameUnknownReplay.StatusCode != http.StatusConflict || sameUnknownErr.Code != "original_operation_unknown" {
			t.Fatalf("same-requestId unknown replay status=%d body=%+v, want 409 original_operation_unknown", sameUnknownReplay.StatusCode, sameUnknownErr)
		}

		diffReplayBody := `{"requestId":"` + reqIDPartial + `","expected":{"generation":"7","baseline":"` + base1 + `"},"edits":[{"path":"routing.retry.request-retry","action":"set","value":9}]}`
		diffReplay := authReq(http.MethodPost, "/upg-console/api/v2/gateway/config/groups/retry", diffReplayBody)
		var diffErr struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(diffReplay.Body).Decode(&diffErr)
		_ = diffReplay.Body.Close()
		if diffReplay.StatusCode != http.StatusConflict || diffErr.Code != "source_or_operation_conflict" {
			t.Fatalf("diff-edits requestId replay status=%d body=%+v, want 409 source_or_operation_conflict", diffReplay.StatusCode, diffErr)
		}

		// Reconcile resolves partial write to State="persisted", Code="partial".
		recPartial := authReq(http.MethodPost, "/upg-console/api/v2/gateway/config/operations/"+reqIDPartial+"/reconcile", `{}`)
		var recPartialBody struct {
			Operation freshstore.ConfigGroupOperation `json:"operation"`
		}
		_ = json.NewDecoder(recPartial.Body).Decode(&recPartialBody)
		_ = recPartial.Body.Close()
		if recPartial.StatusCode != http.StatusOK || recPartialBody.Operation.State != "persisted" || recPartialBody.Operation.Code != "partial" {
			t.Fatalf("reconcile partial status=%d op=%+v, want 200 persisted/partial", recPartial.StatusCode, recPartialBody.Operation)
		}

		// 2. Unacknowledged write where another writer also changed the group resolves to failed/write_not_applied (never stuck unknown).
		base2 := readRetryBaseline()
		reqIDNotApplied := strings.Repeat("2", 32)
		failPatchUnknown.Store(true)
		notAppliedSave := authReq(http.MethodPost, "/upg-console/api/v2/gateway/config/groups/retry",
			`{"requestId":"`+reqIDNotApplied+`","expected":{"generation":"7","baseline":"`+base2+`"},"edits":[{"path":"routing.retry.request-retry","action":"set","value":8}]}`)
		_ = notAppliedSave.Body.Close()
		failPatchUnknown.Store(false)
		if notAppliedSave.StatusCode != http.StatusConflict {
			t.Fatalf("lost patch status=%d, want 409", notAppliedSave.StatusCode)
		}
		// Out-of-band change moves document away from both op.Baseline and op.Edits.
		docMu.Lock()
		doc["routing"].(map[string]any)["retry"].(map[string]any)["max-retry-interval"] = 45
		docMu.Unlock()

		// Mismatched SourceID on journaled operation refuses reconciliation with 409.
		var rawJournal string
		if err := st.SQLDB().QueryRow(`select value from settings where key = ?`, upgradeConfigGroupsSettingsKey).Scan(&rawJournal); err != nil {
			t.Fatal(err)
		}
		tampered := strings.ReplaceAll(rawJournal, `"rt-cfg-1"`, `"rt-other-source"`)
		if _, err := st.SQLDB().Exec(`update settings set value = ? where key = ?`, tampered, upgradeConfigGroupsSettingsKey); err != nil {
			t.Fatal(err)
		}
		mismatchRec := authReq(http.MethodPost, "/upg-console/api/v2/gateway/config/operations/"+reqIDNotApplied+"/reconcile", `{}`)
		_ = mismatchRec.Body.Close()
		if mismatchRec.StatusCode != http.StatusConflict {
			t.Fatalf("mismatched SourceID reconcile status=%d, want 409", mismatchRec.StatusCode)
		}
		if _, err := st.SQLDB().Exec(`update settings set value = ? where key = ?`, rawJournal, upgradeConfigGroupsSettingsKey); err != nil {
			t.Fatal(err)
		}

		recNotApplied := authReq(http.MethodPost, "/upg-console/api/v2/gateway/config/operations/"+reqIDNotApplied+"/reconcile", `{}`)
		var recNotAppliedBody struct {
			Operation freshstore.ConfigGroupOperation `json:"operation"`
		}
		_ = json.NewDecoder(recNotApplied.Body).Decode(&recNotAppliedBody)
		_ = recNotApplied.Body.Close()
		if recNotApplied.StatusCode != http.StatusOK || recNotAppliedBody.Operation.State != "failed" || recNotAppliedBody.Operation.Code != "write_not_applied" {
			t.Fatalf("reconcile unapplied status=%d op=%+v, want 200 failed/write_not_applied", recNotApplied.StatusCode, recNotAppliedBody.Operation)
		}

		// 3. Acknowledged HTTP 200 write overwritten before read-back resolves to persisted/superseded.
		base3 := readRetryBaseline()
		overwriteAfterPatch.Store(true)
		reqIDSup := strings.Repeat("3", 32)
		supRes := authReq(http.MethodPost, "/upg-console/api/v2/gateway/config/groups/retry",
			`{"requestId":"`+reqIDSup+`","expected":{"generation":"7","baseline":"`+base3+`"},"edits":[{"path":"routing.retry.request-retry","action":"set","value":12}]}`)
		var supBody struct {
			Operation freshstore.ConfigGroupOperation `json:"operation"`
		}
		_ = json.NewDecoder(supRes.Body).Decode(&supBody)
		_ = supRes.Body.Close()
		overwriteAfterPatch.Store(false)
		if supRes.StatusCode != http.StatusOK || supBody.Operation.State != "persisted" || supBody.Operation.Code != "superseded" {
			t.Fatalf("superseded save status=%d op=%+v, want 200 persisted/superseded", supRes.StatusCode, supBody.Operation)
		}

		// 4. Rule family save + requestId reuse with different rules returns 409 source_or_operation_conflict.
		gwViewRes := authReq(http.MethodGet, "/upg-console/api/v2/gateway/config", "")
		var gwView freshconfig.View
		_ = json.NewDecoder(gwViewRes.Body).Decode(&gwView)
		_ = gwViewRes.Body.Close()
		var filterBase string
		for _, fam := range gwView.Families {
			if fam.Family == "filter" {
				filterBase = fam.Baseline
			}
		}
		reqIDFam := strings.Repeat("4", 32)
		famOk := authReq(http.MethodPost, "/upg-console/api/v2/gateway/config/rules/filter",
			`{"requestId":"`+reqIDFam+`","expected":{"generation":"7","baseline":"`+filterBase+`"},"rules":[{"models":[{"name":"*"}],"params":["a"]}]}`)
		_ = famOk.Body.Close()
		if famOk.StatusCode != http.StatusOK {
			t.Fatalf("POST gateway/config/rules/filter status=%d, want 200", famOk.StatusCode)
		}
		famDiff := authReq(http.MethodPost, "/upg-console/api/v2/gateway/config/rules/filter",
			`{"requestId":"`+reqIDFam+`","expected":{"generation":"7","baseline":"`+filterBase+`"},"rules":[{"models":[{"name":"*"}],"params":["b"]}]}`)
		var famDiffErr struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(famDiff.Body).Decode(&famDiffErr)
		_ = famDiff.Body.Close()
		if famDiff.StatusCode != http.StatusConflict || famDiffErr.Code != "source_or_operation_conflict" {
			t.Fatalf("diff-rules requestId replay status=%d body=%+v, want 409 source_or_operation_conflict", famDiff.StatusCode, famDiffErr)
		}
	})

	t.Run("external-logs-capability-and-reconcile-honor-refused-credential-gate", func(t *testing.T) {
		var cpaHits atomic.Int32
		extCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cpaHits.Add(1)
			w.WriteHeader(http.StatusUnauthorized)
		}))
		defer extCPA.Close()

		st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPA.URL)
		keyBytes, _, err := security.LoadOrCreateDataKey("", cfg.DataKeyPath)
		if err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector(keyBytes)
		if err != nil {
			t.Fatal(err)
		}
		ws, closeWS, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
			DB:        st.SQLDB(),
			Protector: protector,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeWS)

		extIdentity := freshkeyapply.ExternalIdentity(strings.TrimRight(extCPA.URL, "/"))
		binding, _, err := upgradeoperation.New(st.SQLDB()).ExternalBinding(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := ws.rejection.record(context.Background(), extIdentity, envelopeDigest(binding.ManagementKey)); err != nil {
			t.Fatal(err)
		}

		serverApp := httpapi.New(cfg, st, nil)
		serverApp.SetUpgradeAPIKeys(ws.serveHTTP)
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, ws.gatewayAdmitted, upgradeCanonicalDependencies{
			Workspace: ws,
			Protector: protector,
		}); err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(serverApp.Handler())
		defer ts.Close()

		doReq := func(method, path, reqBody string) *http.Response {
			t.Helper()
			var reader io.Reader
			if reqBody != "" {
				reader = strings.NewReader(reqBody)
			}
			req, _ := http.NewRequest(method, ts.URL+path, reader)
			req.Header.Set("Authorization", "Bearer "+adminKey)
			if reqBody != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			res, reqErr := http.DefaultClient.Do(req)
			if reqErr != nil {
				t.Fatal(reqErr)
			}
			return res
		}

		capRes := doReq(http.MethodGet, "/ext-console/api/v2/gateway/config/logs-capability", "")
		capBytes, _ := io.ReadAll(capRes.Body)
		_ = capRes.Body.Close()
		if capRes.StatusCode != http.StatusConflict || !strings.Contains(string(capBytes), freshconfig.ReasonManagementRejected) {
			t.Fatalf("refused logs-capability = %d %s, want 409 %s", capRes.StatusCode, string(capBytes), freshconfig.ReasonManagementRejected)
		}

		recRes := doReq(http.MethodPost, "/ext-console/api/v2/gateway/config/operations/"+strings.Repeat("a", 32)+"/reconcile", `{}`)
		recBytes, _ := io.ReadAll(recRes.Body)
		_ = recRes.Body.Close()
		if recRes.StatusCode != http.StatusConflict || !strings.Contains(string(recBytes), freshconfig.ReasonManagementRejected) {
			t.Fatalf("refused reconcile = %d %s, want 409 %s", recRes.StatusCode, string(recBytes), freshconfig.ReasonManagementRejected)
		}

		if cpaHits.Load() != 0 {
			t.Fatalf("refused logs-capability or reconcile contacted External CPA %d times, want 0", cpaHits.Load())
		}
	})
}

type recording1xxWriter struct {
	header   http.Header
	statuses []int
	body     bytes.Buffer
}

func (w *recording1xxWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *recording1xxWriter) WriteHeader(code int) {
	w.statuses = append(w.statuses, code)
}

func (w *recording1xxWriter) Write(p []byte) (int, error) {
	return w.body.Write(p)
}

func TestUpgradeCanonicalSecurityAndStreamingRegressions(t *testing.T) {
	t.Run("servePluginResource rejects duplicate Authorization and checks admin auth before credentialGate", func(t *testing.T) {
		st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", "http://127.0.0.1:18317")
		keyBytes, _, err := security.LoadOrCreateDataKey("", cfg.DataKeyPath)
		if err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector(keyBytes)
		if err != nil {
			t.Fatal(err)
		}
		ws, closeWS, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
			DB:        st.SQLDB(),
			Protector: protector,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeWS)

		// Occupy External credentialGate so any unauthorized request reaching the gate would block/fail.
		if !ws.credentialGate.acquire(context.Background()) {
			t.Fatal("failed to acquire initial credentialGate")
		}
		defer ws.credentialGate.release()

		var innerHits atomic.Int32
		srv := &upgradeCanonicalServer{
			db:         st,
			targetMode: model.UpgradeTargetExternal,
			workspace:  ws,
			inner: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				innerHits.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
			}),
		}

		// 1) Duplicate Authorization headers -> 401 without calling inner or blocking on credentialGate.
		dupReq := httptest.NewRequest(http.MethodGet, "/v0/resource/plugins/demo/index.html", nil)
		dupReq.Header.Add("Authorization", "Bearer "+adminKey)
		dupReq.Header.Add("Authorization", "Bearer other")
		dupRR := httptest.NewRecorder()
		srv.servePluginResource(dupRR, dupReq)
		if dupRR.Code != http.StatusUnauthorized || innerHits.Load() != 0 {
			t.Fatalf("duplicate auth header = (%d, innerHits=%d), want (401, 0)", dupRR.Code, innerHits.Load())
		}

		// 2) Wrong/missing admin bearer forwards to inner auth rejection immediately without waiting on credentialGate.
		unauthReq := httptest.NewRequest(http.MethodGet, "/v0/resource/plugins/demo/index.html", nil)
		unauthReq.Header.Set("Authorization", "Bearer wrong-key")
		unauthCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		unauthRR := httptest.NewRecorder()
		srv.servePluginResource(unauthRR, unauthReq.WithContext(unauthCtx))
		if unauthRR.Code != http.StatusUnauthorized || innerHits.Load() != 1 {
			t.Fatalf("unauthenticated request = (%d, innerHits=%d), want (401, 1)", unauthRR.Code, innerHits.Load())
		}
	})

	t.Run("upgradeExternalLegacyRecorder passes 1xx without latching, intercepts 401/403, and releases gate on SSE WriteHeader", func(t *testing.T) {
		var wsGate externalCredentialGate
		if !wsGate.acquire(context.Background()) {
			t.Fatal("acquire gate failed")
		}
		hold := &externalCredentialHold{gate: &wsGate}

		markedCtx, mark := proxysvc.WithSavedKeyMark(context.Background())
		proxysvc.NoteSavedManagementKey(markedCtx)

		st, _, _ := seedCompletedExternalUpgradeStore(t, "/ext-console", "http://127.0.0.1:18317")
		srv := &upgradeCanonicalServer{
			db: st,
		}

		dst := &recording1xxWriter{}
		rec := newUpgradeExternalLegacyRecorder(
			dst,
			context.Background(),
			srv,
			hold,
			freshkeyapply.ExternalInstall{Identity: "ext-id"},
			"digest-1",
			mark,
		)

		// 103 Early Hints must not latch decided or release credentialGate.
		rec.WriteHeader(http.StatusEarlyHints)
		if rec.decided {
			t.Fatal("1xx WriteHeader unexpectedly latched decided=true")
		}
		quickCtx, cancelQuick := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancelQuick()
		if wsGate.acquire(quickCtx) {
			t.Fatal("1xx WriteHeader released credentialGate prematurely")
		}

		// Subsequent 401 with SavedKeyMark intercepts -> 409 external_management_key_rejected and releases gate.
		rec.WriteHeader(http.StatusUnauthorized)
		if len(dst.statuses) != 2 || dst.statuses[0] != http.StatusEarlyHints || dst.statuses[1] != http.StatusConflict ||
			!strings.Contains(dst.body.String(), freshconfig.ReasonManagementRejected) {
			t.Fatalf("intercepted 401 statuses=%v body=%s, want [103 409] with %s", dst.statuses, dst.body.String(), freshconfig.ReasonManagementRejected)
		}
		reacqCtx, cancelReacq := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancelReacq()
		if !wsGate.acquire(reacqCtx) {
			t.Fatal("credentialGate was not released after intercepted 401")
		}

		// On streaming 200 SSE response, WriteHeader releases gate immediately without waiting for handler return.
		hold2 := &externalCredentialHold{gate: &wsGate}
		dst2 := httptest.NewRecorder()
		rec2 := newUpgradeExternalLegacyRecorder(dst2, context.Background(), srv, hold2, freshkeyapply.ExternalInstall{}, "", nil)
		rec2.Flush()
		if !dst2.Flushed || rec2.Unwrap() != dst2 {
			t.Fatalf("Flush=%v Unwrap=%v, want Flushed=true and dst2", dst2.Flushed, rec2.Unwrap() == dst2)
		}
		reacqCtx2, cancelReacq2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancelReacq2()
		if !wsGate.acquire(reacqCtx2) {
			t.Fatal("credentialGate was not released on Flush/200 WriteHeader")
		}
		wsGate.release()
	})

	t.Run("notePluginDocumentObserved ignores stale epochs after capability cache epoch advances", func(t *testing.T) {
		probe := newPluginCapabilityProbe(pluginCapabilityTTL, pluginCapabilityProbeTimeout, func(context.Context) bool { return false })
		probe.launch = func(func()) {}
		t.Cleanup(probe.close)
		srv := &upgradeCanonicalServer{
			pluginCapability: probe,
		}
		staleEpoch := srv.currentPluginCapabilityEpoch()
		srv.pluginCapability.forget()
		srv.notePluginDocumentObserved(staleEpoch, true)
		if srv.pluginCapability.proven {
			t.Fatal("stale epoch mutated proven=true")
		}
		currentEpoch := srv.currentPluginCapabilityEpoch()
		srv.notePluginDocumentObserved(currentEpoch, true)
		if !srv.pluginCapability.proven {
			t.Fatal("current epoch failed to record proven=true")
		}
	})
}

func TestUpgradedManagedRuntimeRemainsOperationalAfterInPlaceCPAEngineUpgrade(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	dest := newManagedDestination(t, bridgeTestInitialSecret)
	rawDest := dest.serve(t)
	mgmtKey := dest.management
	destBase := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer "+mgmtKey {
			switch r.URL.Path {
			case "/v0/management/config":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"port":8317,"observability":{"usage":{"usage-statistics-enabled":true}}}`))
				return
			case "/v0/management/usage-queue":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`[]`))
				return
			}
		}
		rawDest.Config.Handler.ServeHTTP(w, r)
	}))
	defer destBase.Close()
	st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/upg-console", "/upg-gw")
	if err := st.SaveSetup(context.Background(), model.Setup{
		CPAUpstreamURL: destBase.URL,
		ManagementKey:  mgmtKey,
	}); err != nil {
		t.Fatal(err)
	}
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
	upgradedArtifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("9", 64))
	if upgradedArtifact == qualifiedArtifact {
		upgradedArtifact = model.RuntimeArtifactID("sha256:" + strings.Repeat("8", 64))
	}
	sourceIdentity := "external:sha256:" + strings.Repeat("c", 64)
	pluginsInit := false
	destBytes, _ := json.Marshal(upgradeDestinationRecord{
		BaseURL:           destBase.URL,
		URLProved:         true,
		ManagementKey:     sealedDestKey,
		ServerFingerprint: "external:sha256:" + strings.Repeat("d", 64),
		Attempt:           1,
		OperationID:       upgradeManagedDestinationOperationID(sourceIdentity, 1),
		Intent: upgradeDestinationIntent{
			ExpectedRuntimeIdentity:   "rt-managed-upgraded",
			ExpectedRuntimeGeneration: 7,
			ExpectedActiveArtifactID:  qualifiedArtifact,
			SourceIdentity:            sourceIdentity,
			SourceClass:               upgradeManagedDestinationClass,
			APIKeys:                   []string{bridgeTestInitialSecret},
			PluginsEnabled:            &pluginsInit,
		},
	})
	if _, err := st.SQLDB().Exec(`insert into settings(key, value, updated_at_ms) values(?, ?, ?)`,
		upgradeImportDestinationSettingKey, string(destBytes), time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetEmbeddedRuntimeDesiredLifecycle(context.Background(), model.EmbeddedRuntimeDesiredRunning); err != nil {
		t.Fatal(err)
	}

	upgradedReadyStatus := model.RuntimeObservedStatus{
		Identity:           "rt-managed-upgraded",
		Generation:         19,
		ProtocolVersion:    "v1",
		State:              model.RuntimeStateReady,
		CPAObservedVersion: "8.1.7",
		Capabilities:       model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStop, model.RuntimeCapabilityRestart},
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{
			Engine:     "cpa",
			ArtifactID: upgradedArtifact,
			Version:    "8.1.7",
		},
	}
	adoptedBaselineArtifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("7", 64))
	if adoptedBaselineArtifact == qualifiedArtifact || adoptedBaselineArtifact == upgradedArtifact {
		adoptedBaselineArtifact = model.RuntimeArtifactID("sha256:" + strings.Repeat("6", 64))
	}
	rt := &closureManagedRuntime{status: upgradedReadyStatus}
	var adoptBaselineCalls atomic.Int32
	artifactSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runtime/artifact":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schemaVersion":          1,
				"pinned":                 map[string]any{"version": "8.1.8", "artifactId": string(adoptedBaselineArtifact)},
				"active":                 map[string]any{"artifactId": string(upgradedArtifact), "origin": "bundled"},
				"required":               false,
				"bundledAvailable":       true,
				"adoptBaselineAvailable": true,
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/artifact/adopt-baseline":
			adoptBaselineCalls.Add(1)
			startingAdopted := upgradedReadyStatus
			startingAdopted.State = model.RuntimeStateStarting
			startingAdopted.CPAObservedVersion = "8.1.8"
			startingAdopted.ActiveGatewayArtifact = &model.ActiveGatewayArtifact{
				Engine:     "cpa",
				ArtifactID: adoptedBaselineArtifact,
				Version:    "8.1.8",
			}
			rt.setStatus(startingAdopted)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"schemaVersion":          1,
				"pinned":                 map[string]any{"version": "8.1.8", "artifactId": string(adoptedBaselineArtifact)},
				"active":                 map[string]any{"artifactId": string(adoptedBaselineArtifact), "origin": "bundled"},
				"required":               false,
				"bundledAvailable":       true,
				"adoptBaselineAvailable": false,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer artifactSrv.Close()
	ws, closeWS, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
		DB:        st.SQLDB(),
		Protector: protector,
		Runtime:   rt,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeWS)
	cfg.CPAUpstreamURL = destBase.URL
	cfg.ManagementKey = mgmtKey
	cfg.CollectorMode = "http"
	cfg.PollInterval = 15 * time.Millisecond
	collMgr := collector.NewManager(cfg, st)
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = collMgr.StopAndWait(stopCtx)
	}()
	serverApp := httpapi.New(cfg, st, collMgr)
	serverApp.SetUpgradeAPIKeys(ws.serveHTTP)
	var canonical *upgradeCanonicalServer
	canonicalCtx, cancelCanonical := context.WithCancel(context.Background())
	if err := attachCompletedUpgradeRouting(canonicalCtx, cfg, st, serverApp, ws.gatewayAdmitted, upgradeCanonicalDependencies{
		Workspace:    ws,
		Protector:    protector,
		Runtime:      rt,
		Artifact:     runtimeservice.NewArtifactClient(artifactSrv.URL, "tok"),
		CanonicalOut: &canonical,
	}); err != nil {
		cancelCanonical()
		t.Fatalf("attachCompletedUpgradeRouting: %v", err)
	}
	if canonical != nil {
		canonical.managedCollectorPollEvery = 15 * time.Millisecond
		if canonical.pluginCapability != nil {
			canonical.pluginCapability.close()
			canonical.pluginCapability = nil
		}
	}
	t.Cleanup(func() {
		cancelCanonical()
		if canonical != nil {
			canonical.Wait()
		}
	})
	ts := httptest.NewServer(serverApp.Handler())
	defer ts.Close()

	for _, checkPath := range []string{
		"/upg-console/api/v2/cpamp/v0/management/config",
		"/upg-console/api/v2/gateway/config",
		"/upg-console/api/v2/api-keys",
	} {
		req, err := http.NewRequest(http.MethodGet, ts.URL+checkPath, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+adminKey)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s after CPA upgrade = %d %s, want 200", checkPath, res.StatusCode, body)
		}
	}

	// 2. Usage startup verification succeeds against the upgraded CPA artifact so Collector clears transient errors.
	if _, _, _, err := verifyUpgradeManagedTargetForUsageStart(context.Background(), upgradeUsageStartupConfig{
		DB:                   st.SQLDB(),
		Store:                st,
		Protector:            protector,
		Runtime:              rt,
		ManagerConfigService: serverApp.AppContext().ManagerConfigService,
	}); err != nil {
		t.Fatalf("verifyUpgradeManagedTargetForUsageStart failed after in-place CPA artifact upgrade: %v", err)
	}
	waitCollectorState := func(want string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if collMgr.Status().Collector == want {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("collector state = %q (lastErr=%q), want %q", collMgr.Status().Collector, collMgr.Status().LastError, want)
	}
	if err := canonical.reconcileCollector(context.Background()); err != nil {
		t.Fatalf("initial reconcileCollector: %v", err)
	}
	waitCollectorState("running")

	offlineStatus := upgradedReadyStatus
	offlineStatus.State = model.RuntimeStateOffline
	offlineStatus.CPAObservedVersion = ""
	offlineStatus.ActiveGatewayArtifact = nil
	startingStatus := upgradedReadyStatus
	startingStatus.State = model.RuntimeStateStarting

	rt.mu.Lock()
	rt.onStop = func() { rt.setStatus(offlineStatus) }
	rt.onStart = func() { rt.setStatus(startingStatus) }
	rt.mu.Unlock()

	// 3. Stop -> Start where Supervisor reports "starting" on spawn, then transitions to "ready":
	// Start must omit stale ExpectedActiveArtifactID when Offline (ActiveGatewayArtifact == nil),
	// keep Collector stopped during "starting", and automatically restore collection on "ready".
	stopReq, _ := http.NewRequest(
		http.MethodPost,
		ts.URL+"/upg-console/api/v2/runtime/operations",
		strings.NewReader(`{"requestId":"`+strings.Repeat("1", 32)+`","action":"stop"}`),
	)
	stopReq.Header.Set("Authorization", "Bearer "+adminKey)
	stopReq.Header.Set("Content-Type", "application/json")
	stopRes, err := http.DefaultClient.Do(stopReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = stopRes.Body.Close()
	if stopRes.StatusCode != http.StatusOK {
		t.Fatalf("POST runtime stop status = %d, want 200", stopRes.StatusCode)
	}
	waitCollectorState("stopped")

	startReq, _ := http.NewRequest(
		http.MethodPost,
		ts.URL+"/upg-console/api/v2/runtime/operations",
		strings.NewReader(`{"requestId":"`+strings.Repeat("2", 32)+`","action":"start"}`),
	)
	startReq.Header.Set("Authorization", "Bearer "+adminKey)
	startReq.Header.Set("Content-Type", "application/json")
	startRes, err := http.DefaultClient.Do(startReq)
	if err != nil {
		t.Fatal(err)
	}
	startBody, _ := io.ReadAll(startRes.Body)
	_ = startRes.Body.Close()
	startCalls, lastStartReq := rt.snapshotStart()
	if startRes.StatusCode != http.StatusOK || startCalls != 1 {
		t.Fatalf("POST runtime start status=%d calls=%d body=%s, want 200/1", startRes.StatusCode, startCalls, startBody)
	}
	if lastStartReq.ExpectedActiveArtifactID != "" {
		t.Fatalf("Start ExpectedActiveArtifactID when ActiveGatewayArtifact==nil = %q, want empty", lastStartReq.ExpectedActiveArtifactID)
	}
	if collMgr.Status().Collector != "stopped" {
		t.Fatalf("collector state while Supervisor is starting = %q, want stopped", collMgr.Status().Collector)
	}
	rt.setStatus(upgradedReadyStatus)
	waitCollectorState("running")

	// 4. A transient read failure inside the readiness waiter must not abandon the pending
	// Start -> starting -> ready transition; the waiter retries on the next tick and starts collection.
	stopReq3, _ := http.NewRequest(
		http.MethodPost,
		ts.URL+"/upg-console/api/v2/runtime/operations",
		strings.NewReader(`{"requestId":"`+strings.Repeat("5", 32)+`","action":"stop"}`),
	)
	stopReq3.Header.Set("Authorization", "Bearer "+adminKey)
	stopReq3.Header.Set("Content-Type", "application/json")
	stopRes3, err := http.DefaultClient.Do(stopReq3)
	if err != nil {
		t.Fatal(err)
	}
	_ = stopRes3.Body.Close()
	if stopRes3.StatusCode != http.StatusOK {
		t.Fatalf("POST runtime stop #2 status = %d, want 200", stopRes3.StatusCode)
	}
	waitCollectorState("stopped")

	var waiterTransientReads atomic.Int32
	canonical.testManagedCollectorWaitCheck = func() error {
		if waiterTransientReads.Add(1) == 1 {
			return errors.New("sqlite busy during waiter poll")
		}
		return nil
	}

	startReq3, _ := http.NewRequest(
		http.MethodPost,
		ts.URL+"/upg-console/api/v2/runtime/operations",
		strings.NewReader(`{"requestId":"`+strings.Repeat("6", 32)+`","action":"start"}`),
	)
	startReq3.Header.Set("Authorization", "Bearer "+adminKey)
	startReq3.Header.Set("Content-Type", "application/json")
	startRes3, err := http.DefaultClient.Do(startReq3)
	if err != nil {
		t.Fatal(err)
	}
	_ = startRes3.Body.Close()
	if startRes3.StatusCode != http.StatusOK {
		t.Fatalf("POST runtime start #2 status = %d, want 200", startRes3.StatusCode)
	}
	rt.setStatus(upgradedReadyStatus)
	waitCollectorState("running")
	canonical.testManagedCollectorWaitCheck = nil
	if waiterTransientReads.Load() < 2 {
		t.Fatalf("waiter transient check count = %d, want >= 2 (one transient failure followed by retry)", waiterTransientReads.Load())
	}

	// 5. Console AdoptBundledBaseline routes through upgradeBaselineArtifactAdapter:
	// rejects foreign Runtime identity before calling Supervisor, and restores Collector after starting -> ready.
	foreignStatus := upgradedReadyStatus
	foreignStatus.Identity = "rt-foreign"
	rt.setStatus(foreignStatus)
	foreignAdoptReq, _ := http.NewRequest(
		http.MethodPost,
		ts.URL+"/upg-console/api/v2/cpamp/usage-service/runtime/updates/adopt-baseline",
		strings.NewReader(`{"expected_active_artifact_id":"`+string(upgradedArtifact)+`"}`),
	)
	foreignAdoptReq.Header.Set("Authorization", "Bearer "+adminKey)
	foreignAdoptReq.Header.Set("Content-Type", "application/json")
	foreignAdoptRes, err := http.DefaultClient.Do(foreignAdoptReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = foreignAdoptRes.Body.Close()
	if foreignAdoptRes.StatusCode == http.StatusOK || adoptBaselineCalls.Load() != 0 {
		t.Fatalf("foreign identity adopt-baseline status=%d calls=%d, want non-200 and 0 calls", foreignAdoptRes.StatusCode, adoptBaselineCalls.Load())
	}

	rt.setStatus(upgradedReadyStatus)
	adoptReq, _ := http.NewRequest(
		http.MethodPost,
		ts.URL+"/upg-console/api/v2/cpamp/usage-service/runtime/updates/adopt-baseline",
		strings.NewReader(`{"expected_active_artifact_id":"`+string(upgradedArtifact)+`"}`),
	)
	adoptReq.Header.Set("Authorization", "Bearer "+adminKey)
	adoptReq.Header.Set("Content-Type", "application/json")
	adoptRes, err := http.DefaultClient.Do(adoptReq)
	if err != nil {
		t.Fatal(err)
	}
	adoptBody, _ := io.ReadAll(adoptRes.Body)
	_ = adoptRes.Body.Close()
	if adoptRes.StatusCode != http.StatusOK || adoptBaselineCalls.Load() != 1 {
		t.Fatalf("adopt-baseline status=%d calls=%d body=%s, want 200/1", adoptRes.StatusCode, adoptBaselineCalls.Load(), adoptBody)
	}
	readyAdopted := upgradedReadyStatus
	readyAdopted.CPAObservedVersion = "8.1.8"
	readyAdopted.ActiveGatewayArtifact = &model.ActiveGatewayArtifact{
		Engine:     "cpa",
		ArtifactID: adoptedBaselineArtifact,
		Version:    "8.1.8",
	}
	rt.setStatus(readyAdopted)
	waitCollectorState("running")

	// 6. Lost lifecycle HTTP response on Start ("unknown") followed by Supervisor reaching "ready"
	// must also automatically restore usage collection without a second operator action.
	stopReq2, _ := http.NewRequest(
		http.MethodPost,
		ts.URL+"/upg-console/api/v2/runtime/operations",
		strings.NewReader(`{"requestId":"`+strings.Repeat("3", 32)+`","action":"stop"}`),
	)
	stopReq2.Header.Set("Authorization", "Bearer "+adminKey)
	stopReq2.Header.Set("Content-Type", "application/json")
	stopRes2, err := http.DefaultClient.Do(stopReq2)
	if err != nil {
		t.Fatal(err)
	}
	_ = stopRes2.Body.Close()
	if stopRes2.StatusCode != http.StatusOK {
		t.Fatalf("POST runtime stop #3 status = %d, want 200", stopRes2.StatusCode)
	}
	waitCollectorState("stopped")

	rt.mu.Lock()
	rt.startErrOnce = errors.New("lost supervisor start response")
	rt.mu.Unlock()
	lostStartReq, _ := http.NewRequest(
		http.MethodPost,
		ts.URL+"/upg-console/api/v2/runtime/operations",
		strings.NewReader(`{"requestId":"`+strings.Repeat("4", 32)+`","action":"start"}`),
	)
	lostStartReq.Header.Set("Authorization", "Bearer "+adminKey)
	lostStartReq.Header.Set("Content-Type", "application/json")
	lostStartRes, err := http.DefaultClient.Do(lostStartReq)
	if err != nil {
		t.Fatal(err)
	}
	lostStartBody, _ := io.ReadAll(lostStartRes.Body)
	_ = lostStartRes.Body.Close()
	if lostStartRes.StatusCode != http.StatusOK || !strings.Contains(string(lostStartBody), `"state":"unknown"`) {
		t.Fatalf("lost-response Start = %d %s, want 200 unknown", lostStartRes.StatusCode, lostStartBody)
	}
	rt.setStatus(readyAdopted)
	waitCollectorState("running")
}
