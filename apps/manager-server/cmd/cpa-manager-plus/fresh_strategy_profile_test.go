package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

// TestRuntimeConfiguredInstallSelectsProfileFromCommittedStrategy covers the
// official single-image environment where cpamp-launcher always injects
// CPAMP_RUNTIME_URL and CPAMP_RUNTIME_TOKEN_FILE before the wizard chooses a
// strategy. Both in-process Connect External completion and post-restart
// startup must select ProfileExternal, while New Managed and Adopt Existing
// keep ProfileManaged.
func TestRuntimeConfiguredInstallSelectsProfileFromCommittedStrategy(t *testing.T) {
	t.Run("connect_external_in_process_intermediate_and_restart", func(t *testing.T) {
		const management = externalAuthorityManagement
		var routingStrategy atomic.Value
		routingStrategy.Store("round-robin")
		var authFileDeleted atomic.Bool
		externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+management {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("X-CPA-Version", "v8.0.55")
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/v0/management/config":
				_, _ = w.Write([]byte(`{}`))
			case r.Method == http.MethodGet && r.URL.Path == "/v0/management/usage-statistics-enabled":
				_, _ = w.Write([]byte(`{"usage-statistics-enabled":true}`))
			case r.Method == http.MethodGet && r.URL.Path == "/v0/management/usage":
				_, _ = w.Write([]byte(`{"usage":{"total_requests":0,"apis":{}}}`))
			case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
				if authFileDeleted.Load() {
					_, _ = w.Write([]byte(`{"files":[]}`))
					return
				}
				_, _ = w.Write([]byte(`{"files":[{"id":"codex-a","name":"codex-a.json","auth_index":"idx-codex-a","provider":"codex"}]}`))
			case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files/download":
				if authFileDeleted.Load() {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				_, _ = w.Write([]byte(`{"token":"external-auth-token"}`))
			case r.Method == http.MethodDelete && r.URL.Path == "/v0/management/auth-files":
				authFileDeleted.Store(true)
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config":
				current := routingStrategy.Load().(string)
				_, _ = w.Write([]byte(`{"routing":{"strategy":"` + current + `"}}`))
			case r.Method == http.MethodPatch && r.URL.Path == "/v8/management/config/routing":
				routingStrategy.Store("fill-first")
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config/access/api-keys":
				_, _ = w.Write([]byte(`[]`))
			case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config.yaml":
				_, _ = w.Write([]byte(`{"config-version":8}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(externalServer.Close)
		baseURL, err := cpa.NormalizeV8BaseURL(externalServer.URL)
		if err != nil {
			t.Fatal(err)
		}

		var supervisorHits atomic.Int32
		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			supervisorHits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(supervisor.Close)
		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("official-image-runtime-token"), 0o600); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
		paths.Config = filepath.Join(dir, "config.json")
		opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = opened.Close() })

		cfg := config.Config{
			RuntimeURL:       supervisor.URL,
			RuntimeTokenFile: tokenPath,
			CPAUpstreamURL:   "http://127.0.0.1:8317",
		}
		// 1. Fresh container boots before any strategy is committed.
		app := newFreshApplication(opened, cfg, nil)
		if gotMode := app.effectiveRuntimeMode(t.Context()); gotMode != model.RuntimeModeEmbedded {
			t.Fatalf("uncommitted effectiveRuntimeMode = %q, want embedded", gotMode)
		}

		callApp := func(target *freshApplication, method, resource, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, "/control/api/v2/"+resource, bytes.NewBufferString(body))
			req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			if method == http.MethodPut {
				req.Header.Set("Content-Type", "application/gzip")
			} else if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			target.canonicalAPI(rec, req)
			return rec
		}

		// 2. Intermediate state: Connect External committed, ProductComplete still false.
		protected, err := opened.Protector.ProtectString(management)
		if err != nil {
			t.Fatal(err)
		}
		record, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		record, err = app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: r.Operation.Identity.OperationID, Revision: 1,
				Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
				Strategy: bootstrap.ConnectExternal, Stage: "committed",
				TargetIdentity: freshkeyapply.ExternalIdentity(baseURL), TargetGeneration: 1,
			}
			r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: protected}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if app.gatewayAdmissionProven(t.Context()) {
			t.Fatal("pre-complete External install proved gateway admission")
		}
		if _, srcErr := app.externalSource(t.Context()); !errors.Is(srcErr, freshkeyapply.ErrUnavailable) {
			t.Fatalf("pre-complete externalSource err = %v, want ErrUnavailable", srcErr)
		}
		preKeys := callApp(app, http.MethodGet, "api-keys", "")
		if preKeys.Code != http.StatusOK || !strings.Contains(preKeys.Body.String(), `"presenceUnverified":true`) {
			t.Fatalf("pre-complete GET api-keys = %d %s, want 200 presenceUnverified", preKeys.Code, preKeys.Body.String())
		}
		preCfg := callApp(app, http.MethodGet, "gateway/config", "")
		if preCfg.Code != http.StatusOK || !strings.Contains(preCfg.Body.String(), `"mode":"unavailable"`) {
			t.Fatalf("pre-complete GET gateway/config = %d %s, want 200 mode=unavailable", preCfg.Code, preCfg.Body.String())
		}
		if rtRec := callApp(app, http.MethodGet, "runtime/status", ""); rtRec.Code != http.StatusPreconditionRequired {
			t.Fatalf("pre-complete GET runtime/status = %d %s, want 428", rtRec.Code, rtRec.Body.String())
		}
		if extUsageRec := callApp(app, http.MethodGet, "external/usage", ""); extUsageRec.Code != http.StatusConflict {
			t.Fatalf("pre-complete GET external/usage = %d %s, want 409", extUsageRec.Code, extUsageRec.Body.String())
		}
		if updRec := callApp(app, http.MethodGet, "cpamp/usage-service/runtime/updates", ""); updRec.Code != http.StatusPreconditionRequired && updRec.Code != http.StatusConflict {
			t.Fatalf("pre-complete GET cpamp/usage-service/runtime/updates = %d %s, want 409/428", updRec.Code, updRec.Body.String())
		}

		// 3. Complete installation in-process.
		record, err = app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.ProductComplete = true
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		identityWorkerStarts := 0
		app.startIdentityWorker = func(context.Context) { identityWorkerStarts++ }
		app.startUsageCollection(record.Operation)
		if identityWorkerStarts != 0 {
			t.Fatalf("External completion started Embedded identity worker %d times", identityWorkerStarts)
		}

		assertExternalSurfaces := func(label string, target *freshApplication, checkCredentialDelete bool) {
			t.Helper()
			call := func(method, resource, body string) *httptest.ResponseRecorder {
				return callApp(target, method, resource, body)
			}
			if target.gatewayAdmissionProven(t.Context()) {
				t.Fatalf("%s: External installation proved Embedded gateway admission", label)
			}
			if keys := call(http.MethodGet, "api-keys", ""); keys.Code != http.StatusOK {
				t.Fatalf("%s: GET api-keys = %d %s", label, keys.Code, keys.Body.String())
			}
			if usage := call(http.MethodGet, "external/usage", ""); usage.Code != http.StatusOK {
				t.Fatalf("%s: GET external/usage = %d %s", label, usage.Code, usage.Body.String())
			}
			cfgRes := call(http.MethodGet, "gateway/config", "")
			if cfgRes.Code != http.StatusOK {
				t.Fatalf("%s: GET gateway/config = %d %s", label, cfgRes.Code, cfgRes.Body.String())
			}
			var cfgView struct {
				Capability freshconfig.Capability `json:"capability"`
			}
			if err := json.Unmarshal(cfgRes.Body.Bytes(), &cfgView); err != nil {
				t.Fatal(err)
			}
			if cfgView.Capability.Mode != freshconfig.ModeExternal || !cfgView.Capability.Writable || cfgView.Capability.RulesWritable {
				t.Fatalf("%s: gateway capability = %+v", label, cfgView.Capability)
			}
			plugins := call(http.MethodGet, "plugins/runtime", "")
			if plugins.Code != http.StatusOK {
				t.Fatalf("%s: GET plugins/runtime = %d %s", label, plugins.Code, plugins.Body.String())
			}
			var pluginView struct {
				Capability freshconfig.Capability `json:"capability"`
			}
			if err := json.Unmarshal(plugins.Body.Bytes(), &pluginView); err != nil {
				t.Fatal(err)
			}
			if pluginView.Capability.Mode != freshconfig.ModeExternal || pluginView.Capability.Reason != freshconfig.ReasonPluginManagedOnly {
				t.Fatalf("%s: plugins capability = %+v", label, pluginView.Capability)
			}
			// runtime/artifact endpoints must treat External as runtime_unavailable without hitting Supervisor.
			for _, art := range []struct {
				method   string
				resource string
				body     string
			}{
				{http.MethodGet, "runtime/artifact", ""},
				{http.MethodPost, "runtime/artifact/probe", `{}`},
				{http.MethodPost, "runtime/artifact/acquire", `{"source":"github"}`},
				{http.MethodPut, "runtime/artifact/upload", "archive-bytes"},
			} {
				artRes := call(art.method, art.resource, art.body)
				if artRes.Code != http.StatusServiceUnavailable || !strings.Contains(artRes.Body.String(), "runtime_unavailable") {
					t.Fatalf("%s: %s %s = %d %s, want 503 runtime_unavailable", label, art.method, art.resource, artRes.Code, artRes.Body.String())
				}
			}
			// CPAUpdateService status must report mode=external, state=managed_externally dynamically.
			updatesRes := call(http.MethodGet, "cpamp/usage-service/runtime/updates", "")
			if updatesRes.Code != http.StatusOK {
				t.Fatalf("%s: GET runtime/updates = %d %s", label, updatesRes.Code, updatesRes.Body.String())
			}
			var updatesBody struct {
				Mode           string `json:"mode"`
				State          string `json:"state"`
				CurrentVersion string `json:"current_version"`
			}
			if err := json.Unmarshal(updatesRes.Body.Bytes(), &updatesBody); err != nil {
				t.Fatal(err)
			}
			if updatesBody.Mode != string(model.RuntimeModeExternal) || updatesBody.State != "managed_externally" || updatesBody.CurrentVersion != "v8.0.55" {
				t.Fatalf("%s: runtime/updates = %+v", label, updatesBody)
			}
			// OAuth credential deletion via ProxyService must forward to External CPA without hitting Embedded Supervisor.
			if checkCredentialDelete {
				delRes := call(http.MethodDelete, "cpamp/v0/management/auth-files?name=codex-a.json", "")
				if delRes.Code != http.StatusOK {
					t.Fatalf("%s: DELETE auth-files = %d %s", label, delRes.Code, delRes.Body.String())
				}
			}
			statusRes := call(http.MethodGet, "runtime/status", "")
			if statusRes.Code != http.StatusOK {
				t.Fatalf("%s: GET runtime/status = %d %s", label, statusRes.Code, statusRes.Body.String())
			}
			var runtimeView freshRuntimeStatusResponse
			if err := json.Unmarshal(statusRes.Body.Bytes(), &runtimeView); err != nil {
				t.Fatal(err)
			}
			if runtimeView.Mode != string(model.RuntimeModeExternal) || runtimeView.Gateway == nil || runtimeView.Gateway.VersionSupport != "supported" {
				t.Fatalf("%s: runtime status = %+v (gateway=%+v)", label, runtimeView, runtimeView.Gateway)
			}
			if startOp := call(http.MethodPost, "runtime/operations", `{"action":"start","requestId":"ext-start-1"}`); startOp.Code != http.StatusConflict {
				t.Fatalf("%s: POST runtime/operations = %d %s", label, startOp.Code, startOp.Body.String())
			}
			if hits := supervisorHits.Load(); hits != 0 {
				t.Fatalf("%s: External installation contacted Embedded Supervisor %d times", label, hits)
			}
		}
		assertExternalSurfaces("in-process", app, true)

		// 4. Container restarts on the committed Connect External database with runtime env still set.
		reloaded, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		opened.Record = reloaded
		restarted := newFreshApplication(opened, cfg, nil)
		if restarted.runtime != nil || restarted.artifact != nil || restarted.identityWorker != nil {
			t.Fatalf("restarted External app retained Embedded wiring: runtime=%v artifact=%v identityWorker=%v",
				restarted.runtime, restarted.artifact, restarted.identityWorker)
		}
		authFileDeleted.Store(false)
		assertExternalSurfaces("restarted", restarted, true)
	})

	t.Run("new_managed_with_same_runtime_env", func(t *testing.T) {
		harness := newManagedInstallHarness(t)
		if !harness.app.gatewayAdmissionProven(t.Context()) {
			t.Fatal("completed New Managed install did not prove gateway admission")
		}
		if keys := harness.call(t, http.MethodGet, "api-keys", ""); keys.Code != http.StatusOK {
			t.Fatalf("GET api-keys = %d %s", keys.Code, keys.Body.String())
		}
		if extUsage := harness.call(t, http.MethodGet, "external/usage", ""); extUsage.Code != http.StatusConflict {
			t.Fatalf("managed GET external/usage = %d, want 409", extUsage.Code)
		}
		cfgRes := harness.call(t, http.MethodGet, "gateway/config", "")
		if cfgRes.Code != http.StatusOK {
			t.Fatalf("GET gateway/config = %d %s", cfgRes.Code, cfgRes.Body.String())
		}
		var cfgView struct {
			Capability freshconfig.Capability `json:"capability"`
		}
		if err := json.Unmarshal(cfgRes.Body.Bytes(), &cfgView); err != nil {
			t.Fatal(err)
		}
		if cfgView.Capability.Mode != freshconfig.ModeManaged || !cfgView.Capability.Writable || !cfgView.Capability.RulesWritable {
			t.Fatalf("managed gateway capability = %+v", cfgView.Capability)
		}
		statusRes := harness.call(t, http.MethodGet, "runtime/status", "")
		if statusRes.Code != http.StatusOK {
			t.Fatalf("GET runtime/status = %d %s", statusRes.Code, statusRes.Body.String())
		}
		var runtimeView freshRuntimeStatusResponse
		if err := json.Unmarshal(statusRes.Body.Bytes(), &runtimeView); err != nil {
			t.Fatal(err)
		}
		if runtimeView.Mode != string(model.RuntimeModeEmbedded) {
			t.Fatalf("managed runtime status mode = %q, want embedded", runtimeView.Mode)
		}
	})

	t.Run("adopt_existing_with_same_runtime_env", func(t *testing.T) {
		harness := newManagedInstallHarnessWith(t, func(r *freshstore.Record, env managedInstallEnv) {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: r.Operation.Identity.OperationID, Revision: 1,
				Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
				Strategy:                      bootstrap.AdoptExisting,
				SourceClass:                   bootstrap.AdoptNetworkExisting,
				SourceIdentity:                "external:sha256:" + strings.Repeat("b", 64),
				Stage:                         "committed",
				TargetIdentity:                string(env.status.Identity),
				TargetGeneration:              uint64(env.status.Generation),
				TargetArtifactID:              string(env.status.ActiveGatewayArtifact.ArtifactID),
				DestinationConnectionIdentity: freshstrategy.AdoptedDestinationIdentity(env.endpoint),
				AdoptionResources: []bootstrap.ResourceRef{
					bootstrap.AdoptionRefClientAPIKeys,
					bootstrap.AdoptionRefCPAAuthFiles,
					bootstrap.AdoptionRefConfigServer,
					bootstrap.AdoptionRefProviderStateProviders,
				},
				AdoptionDecisions: []bootstrap.AdoptionResourceDecision{
					{ResourceRef: bootstrap.AdoptionRefClientAPIKeys, Kind: bootstrap.APIKeys, Scope: bootstrap.ClientAPIKeys, ReadEvidenceRef: "preflight:keys-read", ReplayEvidenceRef: "preflight:keys-replay", VerifyEvidenceRef: "preflight:keys-verify", Qualification: bootstrap.CapabilityQualified, Disposition: bootstrap.Migrate},
					{ResourceRef: bootstrap.AdoptionRefCPAAuthFiles, Kind: bootstrap.Credentials, Scope: bootstrap.CPAAuthFiles, Qualification: bootstrap.CapabilityPending, ManualAction: bootstrap.ReauthenticateAfterAdoption, Disposition: bootstrap.ManualAction},
					{ResourceRef: bootstrap.AdoptionRefConfigServer, Kind: bootstrap.CPAConfiguration, Scope: bootstrap.ManagementAPIFields, Qualification: bootstrap.CapabilityPending, ManualAction: bootstrap.ReconfigureAfterAdoption, Disposition: bootstrap.ManualAction},
					{ResourceRef: bootstrap.AdoptionRefProviderStateProviders, Kind: bootstrap.ProviderRuntimeState, Scope: bootstrap.ProviderState, Qualification: bootstrap.CapabilityPending, ManualAction: bootstrap.ReauthenticateAfterAdoption, Disposition: bootstrap.ManualAction},
				},
			}
			r.AdoptionCiphertext = env.protected
			r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: env.endpoint, ManagementKey: env.protected}
			r.Operation.ProductComplete = true
		}, true)
		if !harness.app.gatewayAdmissionProven(t.Context()) {
			t.Fatal("completed Adopt Existing install did not prove gateway admission")
		}
		if keys := harness.call(t, http.MethodGet, "api-keys", ""); keys.Code != http.StatusOK {
			t.Fatalf("GET api-keys = %d %s", keys.Code, keys.Body.String())
		}
		if extUsage := harness.call(t, http.MethodGet, "external/usage", ""); extUsage.Code != http.StatusConflict {
			t.Fatalf("adopt_existing GET external/usage = %d, want 409", extUsage.Code)
		}
		cfgRes := harness.call(t, http.MethodGet, "gateway/config", "")
		if cfgRes.Code != http.StatusOK {
			t.Fatalf("GET gateway/config = %d %s", cfgRes.Code, cfgRes.Body.String())
		}
		var cfgView struct {
			Capability freshconfig.Capability `json:"capability"`
		}
		if err := json.Unmarshal(cfgRes.Body.Bytes(), &cfgView); err != nil {
			t.Fatal(err)
		}
		if cfgView.Capability.Mode != freshconfig.ModeManaged || !cfgView.Capability.Writable || !cfgView.Capability.RulesWritable {
			t.Fatalf("adopt_existing gateway capability = %+v", cfgView.Capability)
		}
		statusRes := harness.call(t, http.MethodGet, "runtime/status", "")
		if statusRes.Code != http.StatusOK {
			t.Fatalf("GET runtime/status = %d %s", statusRes.Code, statusRes.Body.String())
		}
		var runtimeView freshRuntimeStatusResponse
		if err := json.Unmarshal(statusRes.Body.Bytes(), &runtimeView); err != nil {
			t.Fatal(err)
		}
		if runtimeView.Mode != string(model.RuntimeModeEmbedded) {
			t.Fatalf("adopt_existing runtime status mode = %q, want embedded", runtimeView.Mode)
		}
	})
}

// TestEffectiveRuntimeModeFailsClosedOnStoreError verifies that when reading
// the Fresh strategy checkpoint fails on a Runtime-configured Managed install,
// effectiveRuntimeMode returns an invalid mode rather than misreporting
// External, never contacts runtimeExternal, and causes CPAUpdateService to fail
// closed with 503 runtime_mode_invalid instead of 409 managed_externally.
type failingFreshStore struct {
	freshstore.Store
}

func (failingFreshStore) Load(context.Context) (freshstore.Record, error) {
	return freshstore.Record{}, errors.New("simulated checkpoint read failure")
}

func TestEffectiveRuntimeModeFailsClosedOnStoreError(t *testing.T) {
	harness := newManagedInstallHarness(t)
	var externalHits atomic.Int32
	harness.app.runtimeExternal = runtimeservice.NewExternalClientWithConnectionSource(func(context.Context) (string, string, error) {
		externalHits.Add(1)
		return "http://127.0.0.1:18399", "k", nil
	})

	canceledCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := harness.app.effectiveRuntimeMode(canceledCtx); got.IsValid() {
		t.Fatalf("effectiveRuntimeMode(canceledCtx) = %q, want invalid empty mode", got)
	}

	// Inject a checkpoint read failure while leaving the underlying SQLite handle
	// healthy so CPAUpdateService exercises its mode-resolver path directly.
	harness.app.store = failingFreshStore{Store: harness.app.store}

	if got := harness.app.effectiveRuntimeMode(context.Background()); got.IsValid() {
		t.Fatalf("effectiveRuntimeMode on unreadable store = %q, want invalid empty mode", got)
	}
	if client := harness.app.runtimeStatusClient(); client != nil {
		t.Fatalf("runtimeStatusClient on unreadable store = %v, want nil", client)
	}

	// Unwrap guardFreshExternalLegacy by invoking the inner router: guardFreshExternalLegacy
	// fails closed on a.store.Load for management-key paths, whereas CPAUpdateService routes
	// (/usage-service/runtime/updates*) do not present the CPA management key and therefore pass
	// straight into CPAUpdateService where runtime_mode_invalid must return 503 (never 409).
	callUpdate := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		harness.app.legacy.ServeHTTP(rec, req)
		return rec
	}

	statusRec := callUpdate(http.MethodGet, "/usage-service/runtime/updates", "")
	if statusRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET runtime/updates on store error = %d %s, want 503", statusRec.Code, statusRec.Body.String())
	}
	prepBody := `{"request_id":"11111111-1111-4111-8111-111111111111","target_version":"8.0.55","expected_active_artifact_id":"sha256:` + strings.Repeat("a", 64) + `"}`
	prepRec := callUpdate(http.MethodPost, "/usage-service/runtime/updates/prepare", prepBody)
	if prepRec.Code != http.StatusServiceUnavailable || !strings.Contains(prepRec.Body.String(), "runtime_mode_invalid") {
		t.Fatalf("POST runtime/updates/prepare on store error = %d %s, want 503 runtime_mode_invalid", prepRec.Code, prepRec.Body.String())
	}
	obsRec := callUpdate(http.MethodGet, "/usage-service/runtime/updates/operations/prepare?request_id=11111111-1111-4111-8111-111111111111&target_version=8.0.55", "")
	if obsRec.Code != http.StatusServiceUnavailable || !strings.Contains(obsRec.Body.String(), "runtime_mode_invalid") {
		t.Fatalf("GET runtime/updates/operations/prepare on store error = %d %s, want 503 runtime_mode_invalid", obsRec.Code, obsRec.Body.String())
	}
	if hits := externalHits.Load(); hits != 0 {
		t.Fatalf("store error contacted external runtime client %d times", hits)
	}
}
type staticDesiredRunningStore struct{}

func (staticDesiredRunningStore) LoadEmbeddedRuntimeDesiredState(context.Context) (model.EmbeddedRuntimeDesiredState, bool, error) {
	return model.EmbeddedRuntimeDesiredState{
		DesiredLifecycle: model.EmbeddedRuntimeDesiredRunning,
		Revision:         1,
		UpdatedAtMS:      time.Now().UTC().UnixMilli(),
	}, true, nil
}

type nthLoadFailingStore struct {
	freshstore.Store
	calls      atomic.Int32
	failOnCall int32
}
func (s *nthLoadFailingStore) Load(ctx context.Context) (freshstore.Record, error) {
	n := s.calls.Add(1)
	if s.failOnCall > 0 && n == s.failOnCall {
		return freshstore.Record{}, errors.New("simulated inner checkpoint read failure")
	}
	return s.Store.Load(ctx)
}

// TestProxyMutationFailsClosedOnInnerCheckpointReadError verifies P1-1: when
// outer request admission reads the checkpoint successfully and the subsequent
// checkpoint read inside ProxyService's mutation mode gate fails, both
// ConnectExternal and NewManaged fail closed with 503 without invoking the
// Embedded Supervisor or forwarding an unverified write to upstream CPA.
func TestProxyMutationFailsClosedOnInnerCheckpointReadError(t *testing.T) {
	t.Run("in_process_external_never_touches_supervisor_or_upstream", func(t *testing.T) {
		const management = externalAuthorityManagement
		var upstreamWrites atomic.Int32
		externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-CPA-Version", "v8.0.55")
			if r.Method != http.MethodGet {
				upstreamWrites.Add(1)
			}
			switch {
			case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
				_, _ = w.Write([]byte(`{"files":[{"id":"codex-a","name":"codex-a.json","auth_index":"idx-a","provider":"codex"}]}`))
			case r.URL.Path == "/v0/management/auth-files/download":
				_, _ = w.Write([]byte(`{"token":"t"}`))
			default:
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			}
		}))
		t.Cleanup(externalServer.Close)
		baseURL, err := cpa.NormalizeV8BaseURL(externalServer.URL)
		if err != nil {
			t.Fatal(err)
		}

		var supervisorHits atomic.Int32
		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			supervisorHits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		t.Cleanup(supervisor.Close)
		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("p1-token"), 0o600); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
		paths.Config = filepath.Join(dir, "config.json")
		opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = opened.Close() })

		app := newFreshApplication(opened, config.Config{
			RuntimeURL:       supervisor.URL,
			RuntimeTokenFile: tokenPath,
			CPAUpstreamURL:   externalServer.URL,
		}, nil)
		protected, err := opened.Protector.ProtectString(management)
		if err != nil {
			t.Fatal(err)
		}
		rec, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: r.Operation.Identity.OperationID, Revision: 1,
				Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
				Strategy: bootstrap.ConnectExternal, Stage: "committed",
				TargetIdentity: freshkeyapply.ExternalIdentity(baseURL), TargetGeneration: 1,
			}
			r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: protected}
			r.Operation.ProductComplete = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		// Call 1: authorizeCanonical; Call 2: guardFreshExternalLegacy;
		// Call 3: externalInstallSource inside externalReadGrantFor;
		// Call 4: SetEmbeddedMutationsEnabled inside ProxyService.
		baseStore := app.store
		app.store = &nthLoadFailingStore{Store: baseStore, failOnCall: 4}
		delReq := httptest.NewRequest(http.MethodDelete, "/control/api/v2/cpamp/v0/management/auth-files?name=codex-a.json", nil)
		delReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		delRec := httptest.NewRecorder()
		app.canonicalAPI(delRec, delReq)
		if delRec.Code != http.StatusServiceUnavailable {
			t.Fatalf("External DELETE auth-files on inner checkpoint read error = %d %s, want 503", delRec.Code, delRec.Body.String())
		}
		if hits := supervisorHits.Load(); hits != 0 {
			t.Fatalf("External inner read error contacted Supervisor %d times", hits)
		}
		if writes := upstreamWrites.Load(); writes != 0 {
			t.Fatalf("External inner read error forwarded %d upstream writes", writes)
		}
	})

	t.Run("managed_never_bypasses_fence_on_inner_read_error", func(t *testing.T) {
		harness := newManagedInstallHarness(t)
		baseStore := harness.app.store
		// Call 1: authorizeCanonical; Call 2: guardFreshExternalLegacy;
		// Call 3: SetEmbeddedMutationsEnabled inside ProxyService.
		harness.app.store = &nthLoadFailingStore{Store: baseStore, failOnCall: 3}
		delRec := harness.call(t, http.MethodDelete, "cpamp/v0/management/auth-files?name=codex-a.json", "")
		if delRec.Code != http.StatusServiceUnavailable {
			t.Fatalf("Managed DELETE auth-files on inner checkpoint read error = %d %s, want 503", delRec.Code, delRec.Body.String())
		}
	})
}

// TestInFlightEmbeddedOperationsExcludedByExternalCommit verifies P1-2:
// committing Connect External cancels and drains in-flight Embedded Supervisor
// observations, and every Embedded execution path re-checks the committed
// strategy at execution time so zero Supervisor RPCs occur during or after the
// External commit.
func TestInFlightEmbeddedOperationsExcludedByExternalCommit(t *testing.T) {
	const management = externalAuthorityManagement
	externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-CPA-Version", "v8.0.55")
		switch r.URL.Path {
		case "/v0/management/config":
			_, _ = w.Write([]byte(`{}`))
		case "/v8/management/config":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
		case "/v8/management/config/access/api-keys":
			_, _ = w.Write([]byte(`[]`))
		case "/v8/management/config.yaml":
			_, _ = w.Write([]byte(`{"config-version":8}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	t.Cleanup(externalServer.Close)

	statusEntered := make(chan struct{})
	releaseSupervisor := make(chan struct{})
	var statusEnteredOnce sync.Once
	var releaseOnce sync.Once
	var supervisorStartHits atomic.Int32
	var supervisorStopHits atomic.Int32
	var supervisorArtifactHits atomic.Int32
	var supervisorPrepareHits atomic.Int32
	var externalCommitted atomic.Bool
	var postCommitSupervisorHits atomic.Int32

	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if externalCommitted.Load() {
			postCommitSupervisorHits.Add(1)
		}
		switch {
		case r.URL.Path == "/v1/runtime/status":
			statusEnteredOnce.Do(func() { close(statusEntered) })
			select {
			case <-r.Context().Done():
			case <-releaseSupervisor:
			}
		case r.URL.Path == "/v1/runtime/operations/start":
			supervisorStartHits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		case r.URL.Path == "/v1/runtime/operations/stop":
			supervisorStopHits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		case r.URL.Path == "/v1/runtime/operations/prepare-update":
			supervisorPrepareHits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		case strings.HasPrefix(r.URL.Path, "/v1/runtime/artifact"):
			supervisorArtifactHits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(supervisor.Close)
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseSupervisor) }) })

	tokenPath := filepath.Join(t.TempDir(), "runtime-token")
	if err := os.WriteFile(tokenPath, []byte("interleave-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })

	app := newFreshApplication(opened, config.Config{
		RuntimeURL:       supervisor.URL,
		RuntimeTokenFile: tokenPath,
		CPAUpstreamURL:   "http://127.0.0.1:8317",
	}, nil)
	app.strategy = freshstrategy.New(freshstrategy.Options{
		Protector:     opened.Protector,
		Qualification: bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
	})

	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	record, err = app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
			OperationID: r.Operation.Identity.OperationID, Revision: 1,
			Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	preCommitOp := record.Operation
	preCommitCompleteOp := preCommitOp
	preCommitCompleteOp.ProductComplete = true

	// Provide an in-memory DesiredRunning obligation while ProductComplete is false
	// so the Embedded reconciler actively attempts to observe and start the runtime.
	app.desiredState = staticDesiredRunningStore{}
	reconcilerCtx, cancelReconciler := context.WithCancel(t.Context())
	defer cancelReconciler()
	reconcilerExited := make(chan struct{})
	go func() {
		defer close(reconcilerExited)
		app.runFreshEmbeddedReconciler(reconcilerCtx, 20*time.Millisecond)
	}()

	// Wait until the Embedded reconciler is actively blocked inside Supervisor /v1/runtime/status.
	select {
	case <-statusEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("reconciler did not enter Supervisor /v1/runtime/status")
	}

	// Also start an in-flight POST /control/api/v2/runtime/operations ("start") using the pre-commit snapshot.
	opDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/control/api/v2/runtime/operations",
			strings.NewReader(`{"action":"start","requestId":"11111111-1111-4111-8111-111111111111"}`))
		req.Header.Set("Content-Type", "application/json")
		app.runtimeOperations(rec, req, preCommitCompleteOp, "runtime/operations")
		opDone <- rec
	}()

	// Commit Connect External via the real strategy HTTP handler while Embedded work is in flight.
	commitPayload, err := json.Marshal(struct {
		Identity         bootstrap.FreshIdentity `json:"identity"`
		ExpectedRevision uint64                  `json:"expectedRevision"`
		AttemptID        string                  `json:"attemptId"`
		freshstrategy.Request
	}{
		Identity:         preCommitOp.Identity,
		ExpectedRevision: preCommitOp.Revision,
		AttemptID:        strings.Repeat("a", 32),
		Request: freshstrategy.Request{
			Strategy:      bootstrap.ConnectExternal,
			BaseURL:       externalServer.URL,
			ManagementKey: management,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	commitReq := httptest.NewRequest(http.MethodPost, "/control/api/v2/strategy", bytes.NewReader(commitPayload))
	commitReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	commitReq.Header.Set("Content-Type", "application/json")
	commitRec := httptest.NewRecorder()
	app.canonicalAPI(commitRec, commitReq)
	if commitRec.Code != http.StatusOK {
		t.Fatalf("POST installation/strategy (ConnectExternal) = %d %s, want 200", commitRec.Code, commitRec.Body.String())
	}
	externalCommitted.Store(true)
	releaseOnce.Do(func() { close(releaseSupervisor) })

	// Reconciler and in-flight runtime/operations must already be drained/completed.
	select {
	case <-reconcilerExited:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("reconciler did not exit before ConnectExternal commit completed")
	}
	opRec := <-opDone
	if opRec.Code != http.StatusConflict || !strings.Contains(opRec.Body.String(), "runtime_mutation_unsupported") {
		t.Fatalf("in-flight runtime/operations across ConnectExternal commit = %d %s, want 409 runtime_mutation_unsupported", opRec.Code, opRec.Body.String())
	}

	// Execution-time re-check on runtime/artifact* even when invoked with the stale pre-commit op snapshot.
	for _, tc := range []struct {
		method   string
		resource string
		body     string
	}{
		{http.MethodGet, "runtime/artifact", ""},
		{http.MethodPost, "runtime/artifact/probe", `{}`},
		{http.MethodPost, "runtime/artifact/acquire", `{"source":"github"}`},
		{http.MethodPut, "runtime/artifact/upload", "not-a-gzip"},
	} {
		req := httptest.NewRequest(tc.method, "/control/api/v2/"+tc.resource, strings.NewReader(tc.body))
		if tc.method == http.MethodPut {
			req.Header.Set("Content-Type", "application/gzip")
			req.ContentLength = int64(len(tc.body))
		} else if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		app.runtimeArtifact(rec, req, preCommitOp, tc.resource)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"runtime_unavailable"`) {
			t.Fatalf("stale-op %s %s after ConnectExternal commit = %d %s, want 503 runtime_unavailable", tc.method, tc.resource, rec.Code, rec.Body.String())
		}
	}

	// Execution-time re-check on CPAUpdateService prepare.
	prepBody := `{"request_id":"22222222-2222-4222-8222-222222222222","target_version":"8.0.55","expected_active_artifact_id":"sha256:` + strings.Repeat("a", 64) + `"}`
	prepReq := httptest.NewRequest(http.MethodPost, "/usage-service/runtime/updates/prepare", strings.NewReader(prepBody))
	prepReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	prepReq.Header.Set("Content-Type", "application/json")
	prepRec := httptest.NewRecorder()
	app.legacy.ServeHTTP(prepRec, prepReq)
	if prepRec.Code != http.StatusConflict {
		t.Fatalf("POST runtime/updates/prepare after ConnectExternal commit = %d %s, want 409", prepRec.Code, prepRec.Body.String())
	}

	if got := supervisorStartHits.Load(); got != 0 {
		t.Fatalf("Supervisor /v1/runtime/start hits = %d, want 0", got)
	}
	if got := supervisorStopHits.Load(); got != 0 {
		t.Fatalf("Supervisor /v1/runtime/stop hits = %d, want 0", got)
	}
	if got := supervisorArtifactHits.Load(); got != 0 {
		t.Fatalf("Supervisor /v1/runtime/artifact* hits = %d, want 0", got)
	}
	if got := supervisorPrepareHits.Load(); got != 0 {
		t.Fatalf("Supervisor /v1/runtime/updates/prepare hits = %d, want 0", got)
	}
	if got := postCommitSupervisorHits.Load(); got != 0 {
		t.Fatalf("Supervisor hits after ConnectExternal commit = %d, want 0", got)
	}

	t.Run("reconcile_once_commits_external_between_status_and_start", func(t *testing.T) {
		var startHits atomic.Int32
		var commitBetweenStatusAndStart func()
		sup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/runtime/status":
				if commitBetweenStatusAndStart != nil {
					commitBetweenStatusAndStart()
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"offline"}`))
			case "/v1/runtime/operations/start":
				startHits.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(sup.Close)

		dir2 := t.TempDir()
		paths2 := legacypreflight.DefaultPaths(filepath.Join(dir2, "manager.sqlite"))
		paths2.Config = filepath.Join(dir2, "config.json")
		opened2, err := fresh.Open(t.Context(), paths2, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = opened2.Close() })
		app2 := newFreshApplication(opened2, config.Config{
			RuntimeURL:       sup.URL,
			RuntimeTokenFile: tokenPath,
			CPAUpstreamURL:   "http://127.0.0.1:8317",
		}, nil)
		app2.desiredState = staticDesiredRunningStore{}
		protected2, err := opened2.Protector.ProtectString(management)
		if err != nil {
			t.Fatal(err)
		}
		commitBetweenStatusAndStart = func() {
			rec2, err := app2.store.Load(t.Context())
			if err != nil {
				t.Error(err)
				return
			}
			_, err = app2.store.Update(t.Context(), rec2.Operation.Identity, rec2.Operation.Revision, func(r *freshstore.Record) error {
				r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
					OperationID: r.Operation.Identity.OperationID, Revision: 1,
					Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
				}
				r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
					Strategy: bootstrap.ConnectExternal, Stage: "committed",
					TargetIdentity: freshkeyapply.ExternalIdentity(externalServer.URL), TargetGeneration: 1,
				}
				r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: externalServer.URL, ManagementKey: protected2}
				r.Operation.ProductComplete = true
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		}

		reconciler := runtimeservice.NewReconciler(
			app2.embeddedRuntimeClient(),
			freshEmbeddedDesiredStateStore{app: app2, inner: app2.embeddedDesiredStateInner()},
			time.Second,
			nil,
		)
		if err := reconciler.ReconcileOnce(t.Context()); err == nil {
			t.Fatal("expected ReconcileOnce to abort Start when ConnectExternal committed after Status")
		}
		if got := startHits.Load(); got != 0 {
			t.Fatalf("Supervisor /v1/runtime/operations/start hits = %d, want 0", got)
		}
	})
}

// TestRuntimeModeFollowsCommittedStrategyWithoutRuntimeClient verifies P2-1:
// when no Embedded Runtime client is configured (app.runtime == nil), mode
// selection still follows the committed strategy checkpoint (reporting
// mode=embedded + runtime_unavailable for NewManaged/AdoptExisting, and
// 503 runtime_mode_invalid on checkpoint read errors).
func TestRuntimeModeFollowsCommittedStrategyWithoutRuntimeClient(t *testing.T) {
	var externalHits atomic.Int32
	externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		externalHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(externalServer.Close)

	for _, tc := range []struct {
		name     string
		strategy bootstrap.CPAStrategy
		source   bootstrap.AdoptSourceClass
	}{
		{name: "new_managed_without_runtime_client", strategy: bootstrap.NewManaged},
		{name: "adopt_existing_without_runtime_client", strategy: bootstrap.AdoptExisting, source: bootstrap.AdoptNetworkExisting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
			paths.Config = filepath.Join(dir, "config.json")
			opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = opened.Close() })

			app := newFreshApplication(opened, config.Config{CPAUpstreamURL: "http://127.0.0.1:8317"}, nil)
			if app.runtime != nil {
				t.Fatal("expected nil Embedded runtime client")
			}
			protected, err := opened.Protector.ProtectString(strings.Repeat("b", 64))
			if err != nil {
				t.Fatal(err)
			}
			rec, err := app.store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			destIdentity := freshstrategy.ManagedDestinationIdentity("http://127.0.0.1:8317")
			if tc.strategy == bootstrap.AdoptExisting {
				destIdentity = freshstrategy.AdoptedDestinationIdentity("http://127.0.0.1:8317")
			}
			if _, err := app.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
				r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
					OperationID: r.Operation.Identity.OperationID, Revision: 1,
					Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
				}
				r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
					Strategy:                      tc.strategy,
					SourceClass:                   tc.source,
					Stage:                         "committed",
					TargetIdentity:                "runtime-01",
					TargetGeneration:              1,
					TargetArtifactID:              "sha256:" + strings.Repeat("a", 64),
					DestinationConnectionIdentity: destIdentity,
				}
				if tc.strategy == bootstrap.AdoptExisting {
					r.Operation.Strategy.SourceIdentity = "external:sha256:" + strings.Repeat("b", 64)
					r.Operation.Strategy.AdoptionResources = []bootstrap.ResourceRef{
						bootstrap.AdoptionRefClientAPIKeys,
						bootstrap.AdoptionRefCPAAuthFiles,
						bootstrap.AdoptionRefConfigServer,
						bootstrap.AdoptionRefProviderStateProviders,
					}
					r.Operation.Strategy.AdoptionDecisions = []bootstrap.AdoptionResourceDecision{
						{ResourceRef: bootstrap.AdoptionRefClientAPIKeys, Kind: bootstrap.APIKeys, Scope: bootstrap.ClientAPIKeys, ReadEvidenceRef: "preflight:keys-read", ReplayEvidenceRef: "preflight:keys-replay", VerifyEvidenceRef: "preflight:keys-verify", Qualification: bootstrap.CapabilityQualified, Disposition: bootstrap.Migrate},
						{ResourceRef: bootstrap.AdoptionRefCPAAuthFiles, Kind: bootstrap.Credentials, Scope: bootstrap.CPAAuthFiles, Qualification: bootstrap.CapabilityPending, ManualAction: bootstrap.ReauthenticateAfterAdoption, Disposition: bootstrap.ManualAction},
						{ResourceRef: bootstrap.AdoptionRefConfigServer, Kind: bootstrap.CPAConfiguration, Scope: bootstrap.ManagementAPIFields, Qualification: bootstrap.CapabilityPending, ManualAction: bootstrap.ReconfigureAfterAdoption, Disposition: bootstrap.ManualAction},
						{ResourceRef: bootstrap.AdoptionRefProviderStateProviders, Kind: bootstrap.ProviderRuntimeState, Scope: bootstrap.ProviderState, Qualification: bootstrap.CapabilityPending, ManualAction: bootstrap.ReauthenticateAfterAdoption, Disposition: bootstrap.ManualAction},
					}
					r.AdoptionCiphertext = protected
				}
				r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "http://127.0.0.1:8317", ManagementKey: protected}
				r.Operation.ProductComplete = true
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			statusReq := httptest.NewRequest(http.MethodGet, "/control/api/v2/runtime/status", nil)
			statusReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			statusRec := httptest.NewRecorder()
			app.canonicalAPI(statusRec, statusReq)
			if statusRec.Code != http.StatusOK {
				t.Fatalf("GET runtime/status = %d %s", statusRec.Code, statusRec.Body.String())
			}
			var statusView freshRuntimeStatusResponse
			if err := json.Unmarshal(statusRec.Body.Bytes(), &statusView); err != nil {
				t.Fatal(err)
			}
			if statusView.Mode != string(model.RuntimeModeEmbedded) || statusView.ErrorCode != "runtime_unavailable" {
				t.Fatalf("runtime/status = %+v, want mode=embedded errorCode=runtime_unavailable", statusView)
			}

			updReq := httptest.NewRequest(http.MethodGet, "/usage-service/runtime/updates", nil)
			updReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			updRec := httptest.NewRecorder()
			app.legacy.ServeHTTP(updRec, updReq)
			if updRec.Code != http.StatusServiceUnavailable {
				t.Fatalf("GET runtime/updates with nil runtime on %s = %d %s, want 503", tc.strategy, updRec.Code, updRec.Body.String())
			}

			// Failing checkpoint read with nil runtime must also return 503 runtime_mode_invalid, never External.
			app.store = failingFreshStore{Store: app.store}
			prepBody := `{"request_id":"33333333-3333-4333-8333-333333333333","target_version":"8.0.55","expected_active_artifact_id":"sha256:` + strings.Repeat("a", 64) + `"}`
			prepReq := httptest.NewRequest(http.MethodPost, "/usage-service/runtime/updates/prepare", strings.NewReader(prepBody))
			prepReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			prepReq.Header.Set("Content-Type", "application/json")
			prepRec := httptest.NewRecorder()
			app.legacy.ServeHTTP(prepRec, prepReq)
			if prepRec.Code != http.StatusServiceUnavailable || !strings.Contains(prepRec.Body.String(), "runtime_mode_invalid") {
				t.Fatalf("POST runtime/updates/prepare on store error with nil runtime = %d %s, want 503 runtime_mode_invalid", prepRec.Code, prepRec.Body.String())
			}
			if hits := externalHits.Load(); hits != 0 {
				t.Fatalf("External client contacted %d times for %s", hits, tc.name)
			}
		})
	}
}

// TestExternalKeyListSurfacesCheckpointReadFailure verifies P2-2: when GET
// /control/api/v2/api-keys encounters a checkpoint read failure while resolving
// the External key list, it never returns 200 with presenceUnverified=false.
func TestExternalKeyListSurfacesCheckpointReadFailure(t *testing.T) {
	const management = externalAuthorityManagement
	externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-CPA-Version", "v8.0.55")
		_, _ = w.Write([]byte(`[]`))
	}))
	t.Cleanup(externalServer.Close)
	baseURL, err := cpa.NormalizeV8BaseURL(externalServer.URL)
	if err != nil {
		t.Fatal(err)
	}

	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(supervisor.Close)
	tokenPath := filepath.Join(t.TempDir(), "runtime-token")
	if err := os.WriteFile(tokenPath, []byte("keylist-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })

	app := newFreshApplication(opened, config.Config{
		RuntimeURL:       supervisor.URL,
		RuntimeTokenFile: tokenPath,
		CPAUpstreamURL:   "http://127.0.0.1:8317",
	}, nil)
	if _, _, err := app.keys.Create(t.Context(), strings.Repeat("c", 32)); err != nil {
		t.Fatal(err)
	}
	protected, err := opened.Protector.ProtectString(management)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
			OperationID: r.Operation.Identity.OperationID, Revision: 1,
			Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
		}
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.ConnectExternal, Stage: "committed",
			TargetIdentity: freshkeyapply.ExternalIdentity(baseURL), TargetGeneration: 1,
		}
		r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: protected}
		r.Operation.ProductComplete = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Call 1: authorizeCanonical; Call 2: externalReadGrant; Call 3: externalSource inside ExternalKeyGrant;
	// Call 4: selector.loadOp inside ExternalKeyList -> fails closed with 503.
	baseStore := app.store
	app.store = &nthLoadFailingStore{Store: baseStore, failOnCall: 4}
	req := httptest.NewRequest(http.MethodGet, "/control/api/v2/api-keys", nil)
	req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	res := httptest.NewRecorder()
	app.canonicalAPI(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("GET api-keys on selector loadOp failure = %d %s, want 503", res.Code, res.Body.String())
	}

	// Call 5: externalSource inside ExternalKeyPresence -> returns 200 with presenceUnverified=true.
	app.store = &nthLoadFailingStore{Store: baseStore, failOnCall: 5}
	req2 := httptest.NewRequest(http.MethodGet, "/control/api/v2/api-keys", nil)
	req2.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	res2 := httptest.NewRecorder()
	app.canonicalAPI(res2, req2)
	if res2.Code != http.StatusOK || !strings.Contains(res2.Body.String(), `"presenceUnverified":true`) {
		t.Fatalf("GET api-keys on externalSource read failure = %d %s, want 200 presenceUnverified=true", res2.Code, res2.Body.String())
	}
}

// TestExternalRuntimeStatusSerializesAndRecordsRejectedKey verifies P2-3:
// concurrent GET /control/api/v2/runtime/status requests on an External
// installation share the serialized credential gate so a rejected management
// key is sent to the External CPA only once and subsequent polls report
// cpa_management_rejected locally.
func TestExternalRuntimeStatusSerializesAndRecordsRejectedKey(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			entered := make(chan struct{})
			release := make(chan struct{})
			var once sync.Once
			var upstreamHits atomic.Int32
			externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				upstreamHits.Add(1)
				once.Do(func() { close(entered) })
				<-release
				w.WriteHeader(status)
			}))
			t.Cleanup(externalServer.Close)
			baseURL, err := cpa.NormalizeV8BaseURL(externalServer.URL)
			if err != nil {
				t.Fatal(err)
			}

			var supervisorHits atomic.Int32
			supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				supervisorHits.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(supervisor.Close)
			tokenPath := filepath.Join(t.TempDir(), "runtime-token")
			if err := os.WriteFile(tokenPath, []byte("status-gate-token"), 0o600); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
			paths.Config = filepath.Join(dir, "config.json")
			opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = opened.Close() })

			app := newFreshApplication(opened, config.Config{
				RuntimeURL:       supervisor.URL,
				RuntimeTokenFile: tokenPath,
				CPAUpstreamURL:   "http://127.0.0.1:8317",
			}, nil)
			protected, err := opened.Protector.ProtectString(externalAuthorityManagement)
			if err != nil {
				t.Fatal(err)
			}
			rec, err := app.store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := app.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
				r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
					OperationID: r.Operation.Identity.OperationID, Revision: 1,
					Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
				}
				r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
					Strategy: bootstrap.ConnectExternal, Stage: "committed",
					TargetIdentity: freshkeyapply.ExternalIdentity(baseURL), TargetGeneration: 1,
				}
				r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: protected}
				r.Operation.ProductComplete = true
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := setting.New(opened.DB, opened.Protector).SaveManagerConfig(t.Context(), model.ManagerConfig{
				CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: externalAuthorityManagement},
			}); err != nil {
				t.Fatal(err)
			}

			callStatus := func() *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodGet, "/control/api/v2/runtime/status", nil)
				req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
				out := httptest.NewRecorder()
				app.canonicalAPI(out, req)
				return out
			}

			firstDone := make(chan *httptest.ResponseRecorder, 1)
			secondDone := make(chan *httptest.ResponseRecorder, 1)
			go func() { firstDone <- callStatus() }()
			<-entered
			go func() { secondDone <- callStatus() }()
			time.Sleep(25 * time.Millisecond)
			if got := upstreamHits.Load(); got != 1 {
				t.Fatalf("concurrent runtime/status probes reached upstream %d times while first was in flight, want 1", got)
			}
			close(release)
			first := <-firstDone
			second := <-secondDone
			if first.Code != http.StatusOK || second.Code != http.StatusOK {
				t.Fatalf("runtime/status codes = (%d, %d), want (200, 200)", first.Code, second.Code)
			}
			if !strings.Contains(second.Body.String(), `"errorCode":"`+freshconfig.ReasonManagementRejected+`"`) {
				t.Fatalf("second runtime/status body = %s, want errorCode=%s", second.Body.String(), freshconfig.ReasonManagementRejected)
			}
			if got := upstreamHits.Load(); got != 1 {
				t.Fatalf("total upstream requests = %d, want 1", got)
			}
			if got := supervisorHits.Load(); got != 0 {
				t.Fatalf("Supervisor hits = %d, want 0", got)
			}
		})
	}
}

// TestFreshRuntimeClientAdapterStartRejectsExternalModeWithoutExternalCalls
// verifies that lifecycle mutations on freshRuntimeClientAdapter resolve only
// the Embedded runtime client with the caller's context, returning the
// not-configured error in Connect External mode without contacting External or
// Supervisor endpoints.
func TestFreshRuntimeClientAdapterStartRejectsExternalModeWithoutExternalCalls(t *testing.T) {
	var supervisorHits atomic.Int32
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		supervisorHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(supervisor.Close)

	var externalHits atomic.Int32
	externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		externalHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(externalServer.Close)

	tokenPath := filepath.Join(t.TempDir(), "runtime-token")
	if err := os.WriteFile(tokenPath, []byte("adapter-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })

	app := newFreshApplication(opened, config.Config{
		RuntimeURL:       supervisor.URL,
		RuntimeTokenFile: tokenPath,
		CPAUpstreamURL:   "http://127.0.0.1:8317",
	}, nil)

	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	protected, err := opened.Protector.ProtectString(externalAuthorityManagement)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
			OperationID: r.Operation.Identity.OperationID, Revision: 1,
			Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
		}
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.ConnectExternal, Stage: "committed",
			TargetIdentity: freshkeyapply.ExternalIdentity(externalServer.URL), TargetGeneration: 1,
		}
		r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: externalServer.URL, ManagementKey: protected}
		r.Operation.ProductComplete = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	adapter := freshRuntimeClientAdapter{app: app}
	_, err = adapter.Start(t.Context(), model.RuntimeMutationRequest{
		OperationID:               "op-external-start",
		ExpectedRuntimeIdentity:   "runtime-01",
		ExpectedRuntimeGeneration: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "CPA runtime client is not configured") {
		t.Fatalf("adapter.Start in ConnectExternal mode = %v, want not-configured error", err)
	}
	if got := externalHits.Load(); got != 0 {
		t.Fatalf("adapter.Start in ConnectExternal mode contacted external server %d times", got)
	}
	if got := supervisorHits.Load(); got != 0 {
		t.Fatalf("adapter.Start in ConnectExternal mode contacted Supervisor %d times", got)
	}
}

// TestFailedOrConflictingExternalCommitPreservesEmbeddedRuntime verifies P1:
// (A) a ConnectExternal attempt that fails preflight/execution or rolls back at
// store commit does not disable Embedded runtime reads/mutations or kill the
// Embedded reconciler, so switching to NewManaged still succeeds;
// (B) a racing ConnectExternal request that ends 409 Conflict after a
// NewManaged commit leaves the committed Managed installation's runtime
// operations and reconciler working.
func TestFailedOrConflictingExternalCommitPreservesEmbeddedRuntime(t *testing.T) {
	const management = externalAuthorityManagement
	const artifactID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	t.Run("failed_preflight_and_aborted_store_commit_preserve_managed_flow", func(t *testing.T) {
		var rejectExternalAuth atomic.Bool
		rejectExternalAuth.Store(true)
		externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if rejectExternalAuth.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("X-CPA-Version", "v8.0.55")
			switch r.URL.Path {
			case "/v8/management/config":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
			case "/v8/management/config.yaml":
				_, _ = w.Write([]byte(`{"config-version":8}`))
			default:
				_, _ = w.Write([]byte(`{"ok":true}`))
			}
		}))
		t.Cleanup(externalServer.Close)

		var supervisorStatusHits atomic.Int32
		var supervisorRestartHits atomic.Int32
		var stagedManagementKey atomic.Value
		managedDest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, _ := stagedManagementKey.Load().(string)
			if key == "" || r.Header.Get("Authorization") != "Bearer "+key {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
		}))
		t.Cleanup(managedDest.Close)

		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/runtime/status":
				supervisorStatusHits.Add(1)
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"ready","capabilities":["start","restart","stage_destination"],"cpaObservedVersion":"v8.0.55","activeGatewayArtifact":{"engine":"cpa","artifactId":"` + artifactID + `","version":"v8.0.55"}}`))
			case "/v1/runtime/operations/stage-destination":
				var req struct {
					OperationID   string `json:"operationId"`
					ManagementKey string `json:"managementKey"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				stagedManagementKey.Store(req.ManagementKey)
				_, _ = w.Write([]byte(`{"operationId":"` + req.OperationID + `","operationType":"stage_destination","state":"succeeded","runtimeIdentity":"runtime-01","runtimeGeneration":1}`))
			case "/v1/runtime/operations/start":
				var req struct {
					OperationID string `json:"operationId"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				_, _ = w.Write([]byte(`{"operationId":"` + req.OperationID + `","operationType":"start","state":"succeeded","runtimeIdentity":"runtime-01","runtimeGeneration":1}`))
			case "/v1/runtime/operations/restart":
				var req struct {
					OperationID string `json:"operationId"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				supervisorRestartHits.Add(1)
				_, _ = w.Write([]byte(`{"operationId":"` + req.OperationID + `","operationType":"restart","state":"succeeded","runtimeIdentity":"runtime-01","runtimeGeneration":1}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(supervisor.Close)

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("rollback-token"), 0o600); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
		paths.Config = filepath.Join(dir, "config.json")
		opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = opened.Close() })

		app := newFreshApplication(opened, config.Config{
			RuntimeURL:       supervisor.URL,
			RuntimeTokenFile: tokenPath,
			CPAUpstreamURL:   managedDest.URL,
		}, nil)
		app.strategy = freshstrategy.New(freshstrategy.Options{
			Runtime:             freshReadyStartRuntime(app.embeddedRuntimeClient()),
			Protector:           opened.Protector,
			DestinationBaseURL:  managedDest.URL,
			Qualification:       bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
			QualifiedArtifactID: model.RuntimeArtifactID(artifactID),
			InitialClientKey:    installTestClientKey,
		})
		app.desiredState = staticDesiredRunningStore{}

		rec, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		rec, err = app.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: r.Operation.Identity.OperationID, Revision: 1,
				Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := app.keys.Create(t.Context(), strings.Repeat("d", 32)); err != nil {
			t.Fatal(err)
		}

		reconcilerCtx, cancelReconciler := context.WithCancel(t.Context())
		defer cancelReconciler()
		reconcilerExited := make(chan struct{})
		go func() {
			defer close(reconcilerExited)
			app.runFreshEmbeddedReconciler(reconcilerCtx, 30*time.Millisecond)
		}()

		// 1. Failed ConnectExternal execution (bad management key -> 401 from External CPA).
		postStrategy := func(reqBody freshstrategy.Request) *httptest.ResponseRecorder {
			cur, loadErr := app.store.Load(t.Context())
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			payload, marshalErr := json.Marshal(struct {
				Identity         bootstrap.FreshIdentity `json:"identity"`
				ExpectedRevision uint64                  `json:"expectedRevision"`
				AttemptID        string                  `json:"attemptId"`
				freshstrategy.Request
			}{
				Identity:         cur.Operation.Identity,
				ExpectedRevision: cur.Operation.Revision,
				AttemptID:        strings.Repeat("a", 32),
				Request:          reqBody,
			})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			req := httptest.NewRequest(http.MethodPost, "/control/api/v2/strategy", bytes.NewReader(payload))
			req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			req.Header.Set("Content-Type", "application/json")
			out := httptest.NewRecorder()
			app.canonicalAPI(out, req)
			return out
		}

		failedAuthRes := postStrategy(freshstrategy.Request{
			Strategy:      bootstrap.ConnectExternal,
			BaseURL:       externalServer.URL,
			ManagementKey: management,
		})
		if failedAuthRes.Code == http.StatusOK {
			t.Fatalf("expected failed ConnectExternal preflight/auth to be rejected, got 200: %s", failedAuthRes.Body.String())
		}

		// 2. ConnectExternal passes Execute, enters beginExternalCommit, and fails inside store.Update
		// (externalPermit returns true before Execute/after Execute and false inside store.Update).
		rejectExternalAuth.Store(false)
		var permitChecks atomic.Int32
		app.externalPermit = func(bootstrap.FreshOperation, uint64, string, string, string) bool {
			return permitChecks.Add(1) <= 2
		}
		revokedRes := postStrategy(freshstrategy.Request{
			Strategy:      bootstrap.ConnectExternal,
			BaseURL:       externalServer.URL,
			ManagementKey: management,
		})
		if revokedRes.Code != http.StatusServiceUnavailable {
			t.Fatalf("revoked-permit ConnectExternal = %d %s, want 503", revokedRes.Code, revokedRes.Body.String())
		}
		app.externalPermit = nil

		// Embedded reconciler must still be running after both failed ConnectExternal attempts.
		select {
		case <-reconcilerExited:
			t.Fatal("reconciler exited after failed ConnectExternal attempt")
		default:
		}

		// Embedded installation runtime probe must still succeed.
		instReq := httptest.NewRequest(http.MethodGet, "/control/api/v2/installation/runtime", nil)
		instReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		instRes := httptest.NewRecorder()
		app.canonicalAPI(instRes, instReq)
		if instRes.Code != http.StatusOK || !strings.Contains(instRes.Body.String(), `"state":"ready"`) {
			t.Fatalf("GET installation/runtime after failed ConnectExternal = %d %s, want 200 state=ready", instRes.Code, instRes.Body.String())
		}

		// Switching to NewManaged (staged -> committed) must succeed.
		managedReq := freshstrategy.Request{
			Strategy:                  bootstrap.NewManaged,
			ExpectedRuntimeIdentity:   "runtime-01",
			ExpectedRuntimeGeneration: 1,
			ExpectedArtifactID:        model.RuntimeArtifactID(artifactID),
		}
		stagedRes := postStrategy(managedReq)
		if stagedRes.Code != http.StatusOK || !strings.Contains(stagedRes.Body.String(), `"stage":"staged"`) {
			t.Fatalf("POST strategy (NewManaged stage 1) after failed ConnectExternal = %d %s, want 200 staged", stagedRes.Code, stagedRes.Body.String())
		}
		managedRes := postStrategy(managedReq)
		if managedRes.Code != http.StatusOK || !strings.Contains(managedRes.Body.String(), `"initialClientKey"`) {
			t.Fatalf("POST strategy (NewManaged stage 2) after failed ConnectExternal = %d %s, want 200 committed", managedRes.Code, managedRes.Body.String())
		}
		// Mark ProductComplete and verify Embedded runtime/status, runtime/operations, and reconciler all work.
		cur, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.store.Update(t.Context(), cur.Operation.Identity, cur.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.ProductComplete = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		statusReq := httptest.NewRequest(http.MethodGet, "/control/api/v2/runtime/status", nil)
		statusReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		statusRes := httptest.NewRecorder()
		app.canonicalAPI(statusRes, statusReq)
		if statusRes.Code != http.StatusOK || !strings.Contains(statusRes.Body.String(), `"mode":"embedded"`) || !strings.Contains(statusRes.Body.String(), `"status":"ready"`) {
			t.Fatalf("GET runtime/status after NewManaged commit = %d %s, want 200 embedded/ready", statusRes.Code, statusRes.Body.String())
		}

		opReq := httptest.NewRequest(http.MethodPost, "/control/api/v2/runtime/operations",
			strings.NewReader(`{"action":"restart","requestId":"44444444-4444-4444-8444-444444444444"}`))
		opReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		opReq.Header.Set("Content-Type", "application/json")
		opRes := httptest.NewRecorder()
		app.canonicalAPI(opRes, opReq)
		if opRes.Code != http.StatusOK || supervisorRestartHits.Load() != 1 {
			t.Fatalf("POST runtime/operations (restart) = %d %s (restartHits=%d), want 200", opRes.Code, opRes.Body.String(), supervisorRestartHits.Load())
		}

		hitsBefore := supervisorStatusHits.Load()
		time.Sleep(80 * time.Millisecond)
		if supervisorStatusHits.Load() <= hitsBefore {
			t.Fatal("expected reconciler to continue polling Supervisor after failed ConnectExternal rollback")
		}
		select {
		case <-reconcilerExited:
			t.Fatal("reconciler exited unexpectedly on committed NewManaged install")
		default:
		}
	})

	t.Run("racing_external_ending_409_after_new_managed_commit_preserves_embedded_runtime", func(t *testing.T) {
		externalEntered := make(chan struct{})
		releaseExternal := make(chan struct{})
		var enteredOnce sync.Once
		var releaseOnce sync.Once
		externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			enteredOnce.Do(func() { close(externalEntered) })
			<-releaseExternal
			w.Header().Set("X-CPA-Version", "v8.0.55")
			switch r.URL.Path {
			case "/v8/management/config":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
			case "/v8/management/config.yaml":
				_, _ = w.Write([]byte(`{"config-version":8}`))
			default:
				_, _ = w.Write([]byte(`{"ok":true}`))
			}
		}))
		t.Cleanup(externalServer.Close)
		t.Cleanup(func() { releaseOnce.Do(func() { close(releaseExternal) }) })

		var supervisorRestartHits atomic.Int32
		var stagedManagementKey atomic.Value
		managedDest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key, _ := stagedManagementKey.Load().(string)
			if key == "" || r.Header.Get("Authorization") != "Bearer "+key {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
		}))
		t.Cleanup(managedDest.Close)

		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/runtime/status":
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"ready","capabilities":["start","restart","stage_destination"],"cpaObservedVersion":"v8.0.55","activeGatewayArtifact":{"engine":"cpa","artifactId":"` + artifactID + `","version":"v8.0.55"}}`))
			case "/v1/runtime/operations/stage-destination":
				var req struct {
					OperationID   string `json:"operationId"`
					ManagementKey string `json:"managementKey"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				stagedManagementKey.Store(req.ManagementKey)
				_, _ = w.Write([]byte(`{"operationId":"` + req.OperationID + `","operationType":"stage_destination","state":"succeeded","runtimeIdentity":"runtime-01","runtimeGeneration":1}`))
			case "/v1/runtime/operations/start":
				var req struct {
					OperationID string `json:"operationId"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				_, _ = w.Write([]byte(`{"operationId":"` + req.OperationID + `","operationType":"start","state":"succeeded","runtimeIdentity":"runtime-01","runtimeGeneration":1}`))
			case "/v1/runtime/operations/restart":
				var req struct {
					OperationID string `json:"operationId"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				supervisorRestartHits.Add(1)
				_, _ = w.Write([]byte(`{"operationId":"` + req.OperationID + `","operationType":"restart","state":"succeeded","runtimeIdentity":"runtime-01","runtimeGeneration":1}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(supervisor.Close)

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("race409-token"), 0o600); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
		paths.Config = filepath.Join(dir, "config.json")
		opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = opened.Close() })

		app := newFreshApplication(opened, config.Config{
			RuntimeURL:       supervisor.URL,
			RuntimeTokenFile: tokenPath,
			CPAUpstreamURL:   managedDest.URL,
		}, nil)
		app.strategy = freshstrategy.New(freshstrategy.Options{
			Runtime:             freshReadyStartRuntime(app.embeddedRuntimeClient()),
			Protector:           opened.Protector,
			DestinationBaseURL:  managedDest.URL,
			Qualification:       bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
			QualifiedArtifactID: model.RuntimeArtifactID(artifactID),
			InitialClientKey:    installTestClientKey,
		})
		app.desiredState = staticDesiredRunningStore{}

		rec, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		rec, err = app.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: r.Operation.Identity.OperationID, Revision: 1,
				Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := app.keys.Create(t.Context(), strings.Repeat("e", 32)); err != nil {
			t.Fatal(err)
		}

		reconcilerCtx, cancelReconciler := context.WithCancel(t.Context())
		defer cancelReconciler()
		reconcilerExited := make(chan struct{})
		go func() {
			defer close(reconcilerExited)
			app.runFreshEmbeddedReconciler(reconcilerCtx, 30*time.Millisecond)
		}()

		// Launch racing ConnectExternal request that blocks inside externalServer during Execute.
		extPayload, err := json.Marshal(struct {
			Identity         bootstrap.FreshIdentity `json:"identity"`
			ExpectedRevision uint64                  `json:"expectedRevision"`
			AttemptID        string                  `json:"attemptId"`
			freshstrategy.Request
		}{
			Identity:         rec.Operation.Identity,
			ExpectedRevision: rec.Operation.Revision,
			AttemptID:        strings.Repeat("b", 32),
			Request: freshstrategy.Request{
				Strategy:      bootstrap.ConnectExternal,
				BaseURL:       externalServer.URL,
				ManagementKey: management,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		extDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			req := httptest.NewRequest(http.MethodPost, "/control/api/v2/strategy", bytes.NewReader(extPayload))
			req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			req.Header.Set("Content-Type", "application/json")
			out := httptest.NewRecorder()
			app.canonicalAPI(out, req)
			extDone <- out
		}()
		<-externalEntered

		// Commit NewManaged (staged -> committed) while ConnectExternal is in flight.
		postMgd := func(rev uint64) *httptest.ResponseRecorder {
			mgdPayload, marshalErr := json.Marshal(struct {
				Identity         bootstrap.FreshIdentity `json:"identity"`
				ExpectedRevision uint64                  `json:"expectedRevision"`
				freshstrategy.Request
			}{
				Identity:         rec.Operation.Identity,
				ExpectedRevision: rev,
				Request: freshstrategy.Request{
					Strategy:                  bootstrap.NewManaged,
					ExpectedRuntimeIdentity:   "runtime-01",
					ExpectedRuntimeGeneration: 1,
					ExpectedArtifactID:        model.RuntimeArtifactID(artifactID),
				},
			})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			mgdReq := httptest.NewRequest(http.MethodPost, "/control/api/v2/strategy", bytes.NewReader(mgdPayload))
			mgdReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			mgdReq.Header.Set("Content-Type", "application/json")
			mgdRes := httptest.NewRecorder()
			app.canonicalAPI(mgdRes, mgdReq)
			return mgdRes
		}
		if res1 := postMgd(rec.Operation.Revision); res1.Code != http.StatusOK || !strings.Contains(res1.Body.String(), `"stage":"staged"`) {
			t.Fatalf("POST strategy (NewManaged stage 1) = %d %s, want 200 staged", res1.Code, res1.Body.String())
		}
		stagedRec, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if res2 := postMgd(stagedRec.Operation.Revision); res2.Code != http.StatusOK || !strings.Contains(res2.Body.String(), `"initialClientKey"`) {
			t.Fatalf("POST strategy (NewManaged stage 2) = %d %s, want 200 committed", res2.Code, res2.Body.String())
		}
		cur, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.store.Update(t.Context(), cur.Operation.Identity, cur.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.ProductComplete = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		// Release the racing ConnectExternal request; it must end 409 Conflict without touching the Embedded gate.
		releaseOnce.Do(func() { close(releaseExternal) })
		extRes := <-extDone
		if extRes.Code != http.StatusConflict {
			t.Fatalf("racing ConnectExternal after NewManaged commit = %d %s, want 409", extRes.Code, extRes.Body.String())
		}

		// Verify runtime/operations and reconciler on the committed NewManaged install still work.
		opReq := httptest.NewRequest(http.MethodPost, "/control/api/v2/runtime/operations",
			strings.NewReader(`{"action":"restart","requestId":"55555555-5555-4555-8555-555555555555"}`))
		opReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		opReq.Header.Set("Content-Type", "application/json")
		opRes := httptest.NewRecorder()
		app.canonicalAPI(opRes, opReq)
		if opRes.Code != http.StatusOK || supervisorRestartHits.Load() != 1 {
			t.Fatalf("POST runtime/operations after racing 409 ConnectExternal = %d %s (restartHits=%d), want 200", opRes.Code, opRes.Body.String(), supervisorRestartHits.Load())
		}
		select {
		case <-reconcilerExited:
			t.Fatal("reconciler exited after racing 409 ConnectExternal")
		default:
		}
	})
}

// TestEmbeddedReconcilerRetiresViaSignalWithoutFastPolling verifies P2:
// runFreshEmbeddedReconciler does not poll SQLite at sub-second intervals while
// running, and retires immediately via the in-memory externalCommitted signal
// when ConnectExternal commits even when configured with a multi-hour interval.
func TestEmbeddedReconcilerRetiresViaSignalWithoutFastPolling(t *testing.T) {
	const management = externalAuthorityManagement
	externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-CPA-Version", "v8.0.55")
		switch r.URL.Path {
		case "/v8/management/config":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
		case "/v8/management/config.yaml":
			_, _ = w.Write([]byte(`{"config-version":8}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
		}
	}))
	t.Cleanup(externalServer.Close)

	initialStatus := make(chan struct{})
	var initialOnce sync.Once
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"healthy"}`))
		initialOnce.Do(func() { close(initialStatus) })
	}))
	t.Cleanup(supervisor.Close)

	tokenPath := filepath.Join(t.TempDir(), "runtime-token")
	if err := os.WriteFile(tokenPath, []byte("signal-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })

	app := newFreshApplication(opened, config.Config{
		RuntimeURL:       supervisor.URL,
		RuntimeTokenFile: tokenPath,
		CPAUpstreamURL:   "http://127.0.0.1:8317",
	}, nil)
	app.strategy = freshstrategy.New(freshstrategy.Options{
		Protector:     opened.Protector,
		Qualification: bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
	})
	app.desiredState = staticDesiredRunningStore{}

	rec, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rec, err = app.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
			OperationID: r.Operation.Identity.OperationID, Revision: 1,
			Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	countingStore := &nthLoadFailingStore{Store: app.store}
	app.store = countingStore

	reconcilerCtx, cancelReconciler := context.WithCancel(t.Context())
	defer cancelReconciler()
	reconcilerExited := make(chan struct{})
	// Use a 10-hour reconcile interval so neither the reconciler ticker nor the
	// fallback watch ticker can fire during the test.
	go func() {
		defer close(reconcilerExited)
		app.runFreshEmbeddedReconciler(reconcilerCtx, 10*time.Hour)
	}()

	select {
	case <-initialStatus:
	case <-time.After(2 * time.Second):
		t.Fatal("initial reconcile cycle did not complete")
	}
	time.Sleep(20 * time.Millisecond)
	loadsAfterInit := countingStore.calls.Load()
	time.Sleep(150 * time.Millisecond)
	if got := countingStore.calls.Load(); got != loadsAfterInit {
		t.Fatalf("store.Load calls during 150ms idle window = %d, want %d (no 25ms polling)", got, loadsAfterInit)
	}

	// Commit ConnectExternal and verify reconciler retires immediately via the in-memory signal.
	payload, err := json.Marshal(struct {
		Identity         bootstrap.FreshIdentity `json:"identity"`
		ExpectedRevision uint64                  `json:"expectedRevision"`
		AttemptID        string                  `json:"attemptId"`
		freshstrategy.Request
	}{
		Identity:         rec.Operation.Identity,
		ExpectedRevision: rec.Operation.Revision,
		AttemptID:        strings.Repeat("c", 32),
		Request: freshstrategy.Request{
			Strategy:      bootstrap.ConnectExternal,
			BaseURL:       externalServer.URL,
			ManagementKey: management,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	commitReq := httptest.NewRequest(http.MethodPost, "/control/api/v2/strategy", bytes.NewReader(payload))
	commitReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	commitReq.Header.Set("Content-Type", "application/json")
	commitRes := httptest.NewRecorder()
	app.canonicalAPI(commitRes, commitReq)
	if commitRes.Code != http.StatusOK {
		t.Fatalf("POST strategy (ConnectExternal) = %d %s, want 200", commitRes.Code, commitRes.Body.String())
	}

	select {
	case <-reconcilerExited:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("reconciler did not retire immediately via externalCommitted signal")
	}
}
