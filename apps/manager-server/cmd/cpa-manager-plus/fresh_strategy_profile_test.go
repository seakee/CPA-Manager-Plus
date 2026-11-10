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
		if app.keyApply == nil || app.keyApply.Profile() != freshkeyapply.ProfileManaged {
			t.Fatalf("uncommitted profile = %v, want %q", app.keyApply, freshkeyapply.ProfileManaged)
		}
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
		if app.keyApply.Profile() != freshkeyapply.ProfileExternal {
			t.Fatalf("committed pre-complete profile = %q, want %q", app.keyApply.Profile(), freshkeyapply.ProfileExternal)
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
			if target.keyApply == nil || target.keyApply.Profile() != freshkeyapply.ProfileExternal {
				t.Fatalf("%s: keyApply profile = %v, want %q", label, target.keyApply, freshkeyapply.ProfileExternal)
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
		if harness.app.keyApply == nil || harness.app.keyApply.Profile() != freshkeyapply.ProfileManaged {
			t.Fatalf("managed profile = %v, want %q", harness.app.keyApply, freshkeyapply.ProfileManaged)
		}
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
		if harness.app.keyApply == nil || harness.app.keyApply.Profile() != freshkeyapply.ProfileManaged {
			t.Fatalf("adopt_existing profile = %v, want %q", harness.app.keyApply, freshkeyapply.ProfileManaged)
		}
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
// TestConcurrentProxyRequestsDuringInProcessExternalCommit verifies under -race
// that concurrent legacy/plugin/credential requests racing with an in-process
// Connect External strategy commit have no data race on ProxyService mutation
// wiring and never contact the Embedded Supervisor after External commits.
func TestConcurrentProxyRequestsDuringInProcessExternalCommit(t *testing.T) {
	const management = externalAuthorityManagement
	externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-CPA-Version", "v8.0.55")
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"codex-race","name":"codex-race.json","auth_index":"idx-race","provider":"codex"}]}`))
		case r.URL.Path == "/v0/management/auth-files/download":
			_, _ = w.Write([]byte(`{"token":"t"}`))
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case r.URL.Path == "/v0/management/config":
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true}`))
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
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(supervisor.Close)
	tokenPath := filepath.Join(t.TempDir(), "runtime-token")
	if err := os.WriteFile(tokenPath, []byte("race-token"), 0o600); err != nil {
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

	var wg sync.WaitGroup
	start := make(chan struct{})
	for worker := range 8 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			for i := range 20 {
				path := "/control/api/v2/cpamp/v0/management/auth-files?name=codex-race.json"
				method := http.MethodDelete
				if (id+i)%2 == 0 {
					path = "/control/api/v2/cpamp/usage-service/runtime/updates"
					method = http.MethodGet
				}
				req := httptest.NewRequest(method, path, nil)
				req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
				rec := httptest.NewRecorder()
				app.canonicalAPI(rec, req)
			}
		}(worker)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		rec, loadErr := app.store.Load(t.Context())
		if loadErr != nil {
			t.Error(loadErr)
			return
		}
		_, updateErr := app.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
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
		})
		if updateErr != nil {
			t.Error(updateErr)
		}
	}()

	close(start)
	wg.Wait()

	if hits := supervisorHits.Load(); hits != 0 {
		t.Fatalf("concurrent External transition hit Embedded Supervisor %d times", hits)
	}
}

// TestEmbeddedReconcilerRetiresWhenConnectExternalCommitsInProcess verifies
// that the Embedded desired-state reconciler started at container boot exits
// promptly once Connect External is committed in-process and stops polling the
// Embedded Supervisor.
func TestEmbeddedReconcilerRetiresWhenConnectExternalCommitsInProcess(t *testing.T) {
	var supervisorHits atomic.Int32
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		supervisorHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"protocolVersion":1,"gateway":{"state":"ready","ready":true}}`))
	}))
	t.Cleanup(supervisor.Close)
	tokenPath := filepath.Join(t.TempDir(), "runtime-token")
	if err := os.WriteFile(tokenPath, []byte("reconciler-token"), 0o600); err != nil {
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

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		app.runFreshEmbeddedReconciler(ctx, 20*time.Millisecond)
	}()

	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	protected, err := opened.Protector.ProtectString(externalAuthorityManagement)
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "http://127.0.0.1:18399"
	if _, err := app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
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

	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("runFreshEmbeddedReconciler did not exit after Connect External was committed in-process")
	}
	hitsAfterExit := supervisorHits.Load()
	time.Sleep(80 * time.Millisecond)
	if got := supervisorHits.Load(); got != hitsAfterExit {
		t.Fatalf("Supervisor hits increased after reconciler exit: before=%d after=%d", hitsAfterExit, got)
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
