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
	"strconv"
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
	cpaupdateservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaupdate"
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

type staticDesiredStoppedStore struct{}

func (staticDesiredStoppedStore) LoadEmbeddedRuntimeDesiredState(context.Context) (model.EmbeddedRuntimeDesiredState, bool, error) {
	return model.EmbeddedRuntimeDesiredState{
		DesiredLifecycle: model.EmbeddedRuntimeDesiredStopped,
		Revision:         1,
		UpdatedAtMS:      time.Now().UTC().UnixMilli(),
	}, true, nil
}

type nthLoadFailingStore struct {
	freshstore.Store
	calls      atomic.Int32
	failOnCall int32
	onCall     func(n int32)
}

func (s *nthLoadFailingStore) Load(ctx context.Context) (freshstore.Record, error) {
	n := s.calls.Add(1)
	if s.onCall != nil {
		s.onCall(n)
	}
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

	t.Run("external_without_runtime_env_fails_closed_on_inner_checkpoint_read_error", func(t *testing.T) {
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

		dir := t.TempDir()
		paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
		paths.Config = filepath.Join(dir, "config.json")
		opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = opened.Close() })

		// Standalone Manager without RuntimeURL / RuntimeTokenFile: wireFreshIdentityMutations
		// installs no Embedded mutation services (deletes == nil && mutations == nil).
		app := newFreshApplication(opened, config.Config{CPAUpstreamURL: externalServer.URL}, nil)
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
		if err := setting.New(opened.DB, opened.Protector).SaveManagerConfig(t.Context(), model.ManagerConfig{
			CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: management},
		}); err != nil {
			t.Fatal(err)
		}

		baseStore := app.store
		app.store = &nthLoadFailingStore{Store: baseStore, failOnCall: 4}
		delReq := httptest.NewRequest(http.MethodDelete, "/control/api/v2/cpamp/v0/management/auth-files?name=codex-a.json", nil)
		delReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		delRec := httptest.NewRecorder()
		app.canonicalAPI(delRec, delReq)
		if delRec.Code != http.StatusServiceUnavailable {
			t.Fatalf("no-Runtime External DELETE auth-files on inner checkpoint read error = %d %s, want 503", delRec.Code, delRec.Body.String())
		}
		if writes := upstreamWrites.Load(); writes != 0 {
			t.Fatalf("no-Runtime External inner read error forwarded %d upstream writes, want 0", writes)
		}

		// Once the checkpoint store is healthy again, External forwards allowed auth-file deletes.
		app.store = baseStore
		okReq := httptest.NewRequest(http.MethodDelete, "/control/api/v2/cpamp/v0/management/auth-files?name=codex-a.json", nil)
		okReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		okRec := httptest.NewRecorder()
		app.canonicalAPI(okRec, okReq)
		if okRec.Code != http.StatusOK || upstreamWrites.Load() != 1 {
			t.Fatalf("healthy no-Runtime External DELETE auth-files = %d %s (writes=%d), want 200 (writes=1)", okRec.Code, okRec.Body.String(), upstreamWrites.Load())
		}
	})

	t.Run("restarted_external_with_runtime_env_fails_closed_on_inner_checkpoint_read_error", func(t *testing.T) {
		const management = externalAuthorityManagement
		var upstreamWrites atomic.Int32
		externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-CPA-Version", "v8.0.55")
			if r.Method != http.MethodGet {
				upstreamWrites.Add(1)
			}
			_, _ = w.Write([]byte(`{"status":"ok"}`))
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
		if err := os.WriteFile(tokenPath, []byte("restart-ext-token"), 0o600); err != nil {
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
		protected, err := opened.Protector.ProtectString(management)
		if err != nil {
			t.Fatal(err)
		}
		store := freshstore.New(opened.DB)
		rec, err := store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		rec, err = store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
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
		if err != nil {
			t.Fatal(err)
		}
		opened.Record = rec
		// Startup on an already-committed ConnectExternal skips wireFreshIdentityMutations.
		app := newFreshApplication(opened, config.Config{
			RuntimeURL:       supervisor.URL,
			RuntimeTokenFile: tokenPath,
			CPAUpstreamURL:   externalServer.URL,
		}, nil)
		app.store = &nthLoadFailingStore{Store: app.store, failOnCall: 4}
		delReq := httptest.NewRequest(http.MethodDelete, "/control/api/v2/cpamp/v0/management/auth-files?name=codex-a.json", nil)
		delReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		delRec := httptest.NewRecorder()
		app.canonicalAPI(delRec, delReq)
		if delRec.Code != http.StatusServiceUnavailable || supervisorHits.Load() != 0 || upstreamWrites.Load() != 0 {
			t.Fatalf("restarted External inner read error = %d %s (sup=%d, writes=%d), want 503 (0, 0)",
				delRec.Code, delRec.Body.String(), supervisorHits.Load(), upstreamWrites.Load())
		}
	})

	t.Run("managed_without_runtime_env_fails_closed_on_inner_read_error_and_missing_mutation_fence", func(t *testing.T) {
		const management = externalAuthorityManagement
		var upstreamRequests atomic.Int32
		managedUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			upstreamRequests.Add(1)
			w.Header().Set("X-CPA-Version", "v8.0.55")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}))
		t.Cleanup(managedUpstream.Close)

		dir := t.TempDir()
		paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
		paths.Config = filepath.Join(dir, "config.json")
		opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = opened.Close() })

		app := newFreshApplication(opened, config.Config{CPAUpstreamURL: managedUpstream.URL}, nil)
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
				Strategy:                      bootstrap.NewManaged,
				Stage:                         "committed",
				TargetIdentity:                "runtime-01",
				TargetGeneration:              1,
				TargetArtifactID:              "sha256:" + strings.Repeat("a", 64),
				DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity(managedUpstream.URL),
			}
			r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: managedUpstream.URL, ManagementKey: protected}
			r.Operation.ProductComplete = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := setting.New(opened.DB, opened.Protector).SaveManagerConfig(t.Context(), model.ManagerConfig{
			CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: managedUpstream.URL, ManagementKey: management},
		}); err != nil {
			t.Fatal(err)
		}

		// Even with a healthy store, a Manager-owned installation without Embedded
		// mutation services must refuse protected writes rather than bypassing the fence.
		for _, tc := range []struct {
			method string
			path   string
			body   string
		}{
			{http.MethodDelete, "/control/api/v2/cpamp/v0/management/auth-files?name=codex-a.json", ""},
			{http.MethodPatch, "/control/api/v2/cpamp/v0/management/api-keys", `{"old":"k1","new":"k2"}`},
		} {
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			if tc.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			out := httptest.NewRecorder()
			app.canonicalAPI(out, req)
			if out.Code != http.StatusServiceUnavailable {
				t.Fatalf("unfenced Managed %s %s = %d %s, want 503", tc.method, tc.path, out.Code, out.Body.String())
			}
		}

		// Inner checkpoint read failure also fails closed with 503 and zero upstream hits.
		baseStore := app.store
		app.store = &nthLoadFailingStore{Store: baseStore, failOnCall: 3}
		delReq := httptest.NewRequest(http.MethodDelete, "/control/api/v2/cpamp/v0/management/auth-files?name=codex-a.json", nil)
		delReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		delRec := httptest.NewRecorder()
		app.canonicalAPI(delRec, delReq)
		if delRec.Code != http.StatusServiceUnavailable || upstreamRequests.Load() != 0 {
			t.Fatalf("no-Runtime Managed inner read error = %d %s (upstream=%d), want 503 (0)",
				delRec.Code, delRec.Body.String(), upstreamRequests.Load())
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

	reconcilerStatusEntered := make(chan struct{})
	statusReadEntered := make(chan struct{})
	opWaitingOnLock := make(chan struct{})
	var statusCallCount atomic.Int32
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
			n := statusCallCount.Add(1)
			switch n {
			case 1:
				close(reconcilerStatusEntered)
				<-r.Context().Done()
			case 2:
				close(statusReadEntered)
				<-r.Context().Done()
			default:
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"offline","recovery":{"state":"inactive","attemptsRemaining":0}}`))
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
	case <-reconcilerStatusEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("reconciler did not enter Supervisor /v1/runtime/status")
	}

	// Start an in-flight GET /control/api/v2/runtime/status read so a second
	// Supervisor /v1/runtime/status RPC is concurrently active alongside the reconciler.
	statusReadDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/control/api/v2/runtime/status", nil)
		app.runtimeStatus(rec, req, preCommitCompleteOp)
		statusReadDone <- rec
	}()
	select {
	case <-statusReadEntered:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime/status did not enter Supervisor /v1/runtime/status")
	}

	// Start an in-flight POST /control/api/v2/runtime/operations ("start") using
	// the pre-commit snapshot; because freshEmbeddedDesiredStateStore forwards
	// LockEmbeddedRuntimeLifecycle, it deterministically waits on runtimeLifecycleMu
	// behind the active reconciler cycle.
	var opWaitOnce sync.Once
	app.runtimeLifecycleMu.onWait = func() {
		opWaitOnce.Do(func() { close(opWaitingOnLock) })
	}
	opDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/control/api/v2/runtime/operations",
			strings.NewReader(`{"action":"start","requestId":"11111111-1111-4111-8111-111111111111"}`))
		req.Header.Set("Content-Type", "application/json")
		app.runtimeOperations(rec, req, preCommitCompleteOp, "runtime/operations")
		opDone <- rec
	}()
	select {
	case <-opWaitingOnLock:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime/operations did not contend on runtimeLifecycleMu")
	}
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


	// Reconciler and in-flight runtime/operations must already be drained/completed.
	select {
	case <-reconcilerExited:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("reconciler did not exit before ConnectExternal commit completed")
	}
	<-statusReadDone
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

	t.Run("submit_runtime_operation_in_flight_status_cancelled_by_external_commit", func(t *testing.T) {
		opInFlightEntered := make(chan struct{})
		var statusSeq atomic.Int32
		supOp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/runtime/status" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if statusSeq.Add(1) == 1 {
				close(opInFlightEntered)
				<-r.Context().Done()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"offline","recovery":{"state":"inactive","attemptsRemaining":0}}`))
		}))
		t.Cleanup(supOp.Close)

		dirOp := t.TempDir()
		pathsOp := legacypreflight.DefaultPaths(filepath.Join(dirOp, "manager.sqlite"))
		pathsOp.Config = filepath.Join(dirOp, "config.json")
		openedOp, err := fresh.Open(t.Context(), pathsOp, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = openedOp.Close() })

		appOp := newFreshApplication(openedOp, config.Config{
			RuntimeURL:       supOp.URL,
			RuntimeTokenFile: tokenPath,
			CPAUpstreamURL:   "http://127.0.0.1:8317",
		}, nil)
		appOp.strategy = freshstrategy.New(freshstrategy.Options{
			Protector:     openedOp.Protector,
			Qualification: bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
		})
		recOp, err := appOp.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		recOp, err = appOp.store.Update(t.Context(), recOp.Operation.Identity, recOp.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: r.Operation.Identity.OperationID, Revision: 1,
				Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		staleCompleteOp := recOp.Operation
		staleCompleteOp.ProductComplete = true

		opResDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/control/api/v2/runtime/operations",
				strings.NewReader(`{"action":"start","requestId":"33333333-3333-4333-8333-333333333333"}`))
			req.Header.Set("Content-Type", "application/json")
			appOp.runtimeOperations(rec, req, staleCompleteOp, "runtime/operations")
			opResDone <- rec
		}()
		select {
		case <-opInFlightEntered:
		case <-time.After(2 * time.Second):
			t.Fatal("submitRuntimeOperation did not enter Supervisor /v1/runtime/status")
		}

		bodyOp, err := json.Marshal(struct {
			Identity         bootstrap.FreshIdentity `json:"identity"`
			ExpectedRevision uint64                  `json:"expectedRevision"`
			AttemptID        string                  `json:"attemptId"`
			freshstrategy.Request
		}{
			Identity:         recOp.Operation.Identity,
			ExpectedRevision: recOp.Operation.Revision,
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
		cReq := httptest.NewRequest(http.MethodPost, "/control/api/v2/strategy", bytes.NewReader(bodyOp))
		cReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		cReq.Header.Set("Content-Type", "application/json")
		cRec := httptest.NewRecorder()
		appOp.canonicalAPI(cRec, cReq)
		if cRec.Code != http.StatusOK {
			t.Fatalf("POST strategy (ConnectExternal) = %d %s, want 200", cRec.Code, cRec.Body.String())
		}
		inFlightRec := <-opResDone
		if inFlightRec.Code != http.StatusConflict || !strings.Contains(inFlightRec.Body.String(), "runtime_mutation_unsupported") {
			t.Fatalf("in-flight submitRuntimeOperation = %d %s, want 409 runtime_mutation_unsupported", inFlightRec.Code, inFlightRec.Body.String())
		}
	})

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

// TestAcceptedEmbeddedStartFencesExternalCommitAndPreservesOutcomeOnRollback
// verifies P1-1: once the Embedded Supervisor durably accepts a Start operation,
// disconnecting the caller HTTP request or losing the transport response does
// not allow Connect External to commit while that Start remains unresolved or
// after it spawns Embedded CPA; nor does an aborted/rejected External commit
// turn an already-dispatched Supervisor mutation into a false terminal
// runtime_mutation_unsupported failure that drops DesiredRunning.
func TestAcceptedEmbeddedStartFencesExternalCommitAndPreservesOutcomeOnRollback(t *testing.T) {
	const management = externalAuthorityManagement
	const artifactID = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	newExternalUpstream := func(t *testing.T) *httptest.Server {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-CPA-Version", "v8.0.55")
			switch r.URL.Path {
			case "/v0/management/config":
				_, _ = w.Write([]byte(`{}`))
			case "/v8/management/config":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
			case "/v8/management/config/access/api-keys":
				_, _ = w.Write([]byte(`[]`))
			default:
				_, _ = w.Write([]byte(`{"ok":true}`))
			}
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	postConnectExternal := func(t *testing.T, app *freshApplication, externalURL string) *httptest.ResponseRecorder {
		t.Helper()
		cur, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(struct {
			Identity         bootstrap.FreshIdentity `json:"identity"`
			ExpectedRevision uint64                  `json:"expectedRevision"`
			AttemptID        string                  `json:"attemptId"`
			freshstrategy.Request
		}{
			Identity:         cur.Operation.Identity,
			ExpectedRevision: cur.Operation.Revision,
			AttemptID:        strings.Repeat("e", 32),
			Request: freshstrategy.Request{
				Strategy:      bootstrap.ConnectExternal,
				BaseURL:       externalURL,
				ManagementKey: management,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/control/api/v2/strategy", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		app.canonicalAPI(rec, req)
		return rec
	}

	openIsolatedDesiredSettings := func(t *testing.T, opened *fresh.Opened) setting.Repository {
		t.Helper()
		dir := t.TempDir()
		paths := legacypreflight.DefaultPaths(filepath.Join(dir, "desired.sqlite"))
		paths.Config = filepath.Join(dir, "config.json")
		aux, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = aux.Close() })
		return setting.New(aux.DB, opened.Protector)
	}

	t.Run("accepted_start_ignores_client_disconnect_blocks_external_commit_and_preserves_succeeded_outcome", func(t *testing.T) {
		externalServer := newExternalUpstream(t)
		startAccepted := make(chan struct{})
		releaseSpawn := make(chan struct{})
		var acceptOnce sync.Once
		var releaseOnce sync.Once
		var embeddedRunning atomic.Bool

		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/runtime/status":
				state := "offline"
				if embeddedRunning.Load() {
					state = "ready"
				}
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"` + state + `","cpaObservedVersion":"v8.0.55","capabilities":["start","stop","restart"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + artifactID + `","version":"v8.0.55"},"recovery":{"state":"inactive","attemptsRemaining":0}}`))
			case "/v1/runtime/operations/start":
				var req struct {
					OperationID string `json:"operationId"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				// Durable admission completed: real Supervisor executes spawn under
				// context.Background() regardless of caller HTTP cancellation.
				acceptOnce.Do(func() { close(startAccepted) })
				<-releaseSpawn
				embeddedRunning.Store(true)
				_, _ = w.Write([]byte(`{"operationId":"` + req.OperationID + `","operationType":"start","state":"succeeded","runtimeIdentity":"runtime-01","runtimeGeneration":1}`))
			case "/v1/runtime/operations/stop":
				var req struct {
					OperationID string `json:"operationId"`
				}
				_ = json.NewDecoder(r.Body).Decode(&req)
				embeddedRunning.Store(false)
				_, _ = w.Write([]byte(`{"operationId":"` + req.OperationID + `","operationType":"stop","state":"succeeded","runtimeIdentity":"runtime-01","runtimeGeneration":1}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(supervisor.Close)
		t.Cleanup(func() { releaseOnce.Do(func() { close(releaseSpawn) }) })

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("accepted-start-token"), 0o600); err != nil {
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
		app.runtimeSettings = openIsolatedDesiredSettings(t, opened)
		app.strategy = freshstrategy.New(freshstrategy.Options{
			Protector:     opened.Protector,
			Qualification: bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
		})
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
		preCommitCompleteOp := rec.Operation
		preCommitCompleteOp.ProductComplete = true

		clientCtx, cancelClient := context.WithCancel(t.Context())
		defer cancelClient()
		opDone := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			out := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/control/api/v2/runtime/operations",
				strings.NewReader(`{"action":"start","requestId":"aaaaaaaa-1111-4111-8111-111111111111"}`)).WithContext(clientCtx)
			req.Header.Set("Content-Type", "application/json")
			app.runtimeOperations(out, req, preCommitCompleteOp, "runtime/operations")
			opDone <- out
		}()

		select {
		case <-startAccepted:
		case <-time.After(2 * time.Second):
			t.Fatal("Supervisor did not accept Start")
		}
		// Simulate the browser/client disconnecting its HTTP request after Supervisor durable admission.
		cancelClient()

		// ConnectExternal must fail closed while the accepted Start is still executing on Supervisor.
		blockedRes := postConnectExternal(t, app, externalServer.URL)
		if blockedRes.Code != http.StatusConflict || !strings.Contains(blockedRes.Body.String(), `"embedded_runtime_start_unresolved"`) {
			t.Fatalf("ConnectExternal while accepted Start in flight = %d %s, want 409 embedded_runtime_start_unresolved", blockedRes.Code, blockedRes.Body.String())
		}
		afterBlocked, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if afterBlocked.Operation.Strategy.Strategy != "" {
			t.Fatalf("strategy committed despite unresolved Supervisor Start: %+v", afterBlocked.Operation.Strategy)
		}

		// Allow Supervisor to finish spawning Embedded CPA; the operation must complete as
		// succeeded and write DesiredRunning rather than fabricating runtime_mutation_unsupported.
		releaseOnce.Do(func() { close(releaseSpawn) })
		opRec := <-opDone
		if opRec.Code != http.StatusOK || !strings.Contains(opRec.Body.String(), `"state":"succeeded"`) {
			t.Fatalf("accepted Start response after rejected External commit = %d %s, want 200 state=succeeded", opRec.Code, opRec.Body.String())
		}
		desired, found, err := app.runtimeSettings.LoadEmbeddedRuntimeDesiredState(t.Context())
		if err != nil || !found || desired.DesiredLifecycle != model.EmbeddedRuntimeDesiredRunning {
			t.Fatalf("desired lifecycle after accepted Start = (%+v, found=%v, err=%v), want running", desired, found, err)
		}

		// ConnectExternal must continue to fail closed now that Embedded CPA is running.
		runningRes := postConnectExternal(t, app, externalServer.URL)
		if runningRes.Code != http.StatusConflict || !strings.Contains(runningRes.Body.String(), `"embedded_runtime_not_stopped"`) {
			t.Fatalf("ConnectExternal while Embedded CPA running = %d %s, want 409 embedded_runtime_not_stopped", runningRes.Code, runningRes.Body.String())
		}

		// Once Embedded CPA is stopped, ConnectExternal can prove stopped state and commit.
		stopOut := httptest.NewRecorder()
		stopReq := httptest.NewRequest(http.MethodPost, "/control/api/v2/runtime/operations",
			strings.NewReader(`{"action":"stop","requestId":"bbbbbbbb-2222-4222-8222-222222222222"}`))
		stopReq.Header.Set("Content-Type", "application/json")
		app.runtimeOperations(stopOut, stopReq, preCommitCompleteOp, "runtime/operations")
		if stopOut.Code != http.StatusOK {
			t.Fatalf("POST runtime/operations (stop) = %d %s, want 200", stopOut.Code, stopOut.Body.String())
		}
		commitRes := postConnectExternal(t, app, externalServer.URL)
		if commitRes.Code != http.StatusOK {
			t.Fatalf("ConnectExternal after Embedded CPA stopped = %d %s, want 200", commitRes.Code, commitRes.Body.String())
		}
	})

	t.Run("dropped_accepted_start_records_unknown_and_fences_external_even_while_status_still_offline", func(t *testing.T) {
		externalServer := newExternalUpstream(t)
		startAccepted := make(chan struct{})
		releaseSpawn := make(chan struct{})
		spawnDone := make(chan struct{})
		var acceptOnce sync.Once
		var releaseOnce sync.Once
		var embeddedRunning atomic.Bool

		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/runtime/status":
				w.Header().Set("Content-Type", "application/json")
				state := "offline"
				if embeddedRunning.Load() {
					state = "ready"
				}
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"` + state + `","cpaObservedVersion":"v8.0.55","capabilities":["start","stop"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + artifactID + `","version":"v8.0.55"},"recovery":{"state":"inactive","attemptsRemaining":0}}`))
			case "/v1/runtime/operations/start":
				// Durably accept and continue spawning asynchronously, then sever the HTTP
				// connection so Manager's inFlight count drops to 0 before CPA is spawned.
				acceptOnce.Do(func() {
					close(startAccepted)
					go func() {
						<-releaseSpawn
						embeddedRunning.Store(true)
						close(spawnDone)
					}()
				})
				if hj, ok := w.(http.Hijacker); ok {
					if conn, _, err := hj.Hijack(); err == nil {
						_ = conn.Close()
						return
					}
				}
				w.WriteHeader(http.StatusInternalServerError)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(supervisor.Close)
		t.Cleanup(func() { releaseOnce.Do(func() { close(releaseSpawn) }) })

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("dropped-start-token"), 0o600); err != nil {
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
		app.runtimeSettings = openIsolatedDesiredSettings(t, opened)
		app.strategy = freshstrategy.New(freshstrategy.Options{
			Protector:     opened.Protector,
			Qualification: bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
		})
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
		preCommitCompleteOp := rec.Operation
		preCommitCompleteOp.ProductComplete = true

		opOut := httptest.NewRecorder()
		opReq := httptest.NewRequest(http.MethodPost, "/control/api/v2/runtime/operations",
			strings.NewReader(`{"action":"start","requestId":"cccccccc-3333-4333-8333-333333333333"}`))
		opReq.Header.Set("Content-Type", "application/json")
		app.runtimeOperations(opOut, opReq, preCommitCompleteOp, "runtime/operations")
		select {
		case <-startAccepted:
		case <-time.After(2 * time.Second):
			t.Fatalf("Supervisor did not accept Start: %d %s", opOut.Code, opOut.Body.String())
		}

		// Lost Supervisor response must be recorded as unknown (never terminal failed/unsupported)
		// and must still adopt DesiredRunning.
		if !strings.Contains(opOut.Body.String(), `"state":"unknown"`) || !strings.Contains(opOut.Body.String(), `"runtime_result_unknown"`) {
			t.Fatalf("dropped Start operation response = %d %s, want state=unknown runtime_result_unknown", opOut.Code, opOut.Body.String())
		}
		desired, found, err := app.runtimeSettings.LoadEmbeddedRuntimeDesiredState(t.Context())
		if err != nil || !found || desired.DesiredLifecycle != model.EmbeddedRuntimeDesiredRunning {
			t.Fatalf("desired lifecycle after dropped Start = (%+v, found=%v, err=%v), want running", desired, found, err)
		}

		// Even though Manager inFlightMutations == 0 AND Supervisor /v1/runtime/status is still
		// "offline" (spawn still blocked on releaseSpawn), ConnectExternal MUST refuse.
		blockedRes := postConnectExternal(t, app, externalServer.URL)
		if blockedRes.Code != http.StatusConflict || !strings.Contains(blockedRes.Body.String(), `"embedded_runtime_start_unresolved"`) {
			t.Fatalf("ConnectExternal after dropped accepted Start = %d %s, want 409 embedded_runtime_start_unresolved", blockedRes.Code, blockedRes.Body.String())
		}

		releaseOnce.Do(func() { close(releaseSpawn) })
		<-spawnDone
		afterSpawnRes := postConnectExternal(t, app, externalServer.URL)
		if afterSpawnRes.Code != http.StatusConflict {
			t.Fatalf("ConnectExternal after background spawn finished = %d %s, want 409", afterSpawnRes.Code, afterSpawnRes.Body.String())
		}
	})

	t.Run("cancelled_reconciler_start_without_journal_still_fences_external_commit", func(t *testing.T) {
		externalServer := newExternalUpstream(t)
		startAccepted := make(chan struct{})
		releaseSpawn := make(chan struct{})
		var acceptOnce sync.Once
		var releaseOnce sync.Once

		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/runtime/status":
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"offline","cpaObservedVersion":"v8.0.55","capabilities":["start"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + artifactID + `","version":"v8.0.55"},"recovery":{"state":"inactive","attemptsRemaining":0}}`))
			case "/v1/runtime/operations/start":
				acceptOnce.Do(func() { close(startAccepted) })
				<-releaseSpawn
				_, _ = w.Write([]byte(`{"operationId":"reconcile-op","operationType":"start","state":"succeeded","runtimeIdentity":"runtime-01","runtimeGeneration":1}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(supervisor.Close)
		t.Cleanup(func() { releaseOnce.Do(func() { close(releaseSpawn) }) })

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("reconciler-start-token"), 0o600); err != nil {
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
		rec, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: r.Operation.Identity.OperationID, Revision: 1,
				Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		reconciler := runtimeservice.NewReconciler(
			app.embeddedRuntimeClient(),
			staticDesiredRunningStore{},
			time.Second,
			nil,
		)
		reconCtx, cancelRecon := context.WithCancel(t.Context())
		reconDone := make(chan error, 1)
		go func() { reconDone <- reconciler.ReconcileOnce(reconCtx) }()
		select {
		case <-startAccepted:
		case err := <-reconDone:
			t.Fatalf("ReconcileOnce exited before Start was accepted: %v", err)
		case <-time.After(2 * time.Second):
			t.Fatal("Supervisor did not accept reconciler Start")
		}
		cancelRecon()
		if err := <-reconDone; err == nil {
			t.Fatal("expected cancelled ReconcileOnce to return context error")
		}

		blockedRes := postConnectExternal(t, app, externalServer.URL)
		if blockedRes.Code != http.StatusConflict || !strings.Contains(blockedRes.Body.String(), `"embedded_runtime_start_unresolved"`) {
			t.Fatalf("ConnectExternal after cancelled reconciler Start = %d %s, want 409 embedded_runtime_start_unresolved", blockedRes.Code, blockedRes.Body.String())
		}
		releaseOnce.Do(func() { close(releaseSpawn) })
	})

	t.Run("offline_with_armed_or_recovering_automatic_recovery_blocks_external_commit_until_disarmed", func(t *testing.T) {
		externalServer := newExternalUpstream(t)
		var recoveryState atomic.Value
		recoveryState.Store("armed")

		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path != "/v1/runtime/status" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			recState := recoveryState.Load().(string)
			attempts := "3"
			if recState == "inactive" || recState == "manual_intervention" {
				attempts = "0"
			}
			_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"offline","cpaObservedVersion":"v8.0.55","capabilities":["start","stop"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + artifactID + `","version":"v8.0.55"},"recovery":{"state":"` + recState + `","attemptsRemaining":` + attempts + `}}`))
		}))
		t.Cleanup(supervisor.Close)

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("armed-recovery-token"), 0o600); err != nil {
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
		rec, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: r.Operation.Identity.OperationID, Revision: 1,
				Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		for _, blockedState := range []string{"armed", "recovering"} {
			recoveryState.Store(blockedState)
			res := postConnectExternal(t, app, externalServer.URL)
			if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), `"embedded_runtime_not_stopped"`) {
				t.Fatalf("ConnectExternal with offline + recovery=%s = %d %s, want 409 embedded_runtime_not_stopped",
					blockedState, res.Code, res.Body.String())
			}
		}

		// Once automatic recovery is disarmed (manual_intervention), offline is stable and ConnectExternal commits.
		recoveryState.Store("manual_intervention")
		okRes := postConnectExternal(t, app, externalServer.URL)
		if okRes.Code != http.StatusOK {
			t.Fatalf("ConnectExternal with offline + recovery=manual_intervention = %d %s, want 200", okRes.Code, okRes.Body.String())
		}
	})

	t.Run("transient_observe_update_error_preserves_trailing_unresolved_start", func(t *testing.T) {
		externalServer := newExternalUpstream(t)
		var observeCalls atomic.Int32

		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/runtime/status":
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"offline","cpaObservedVersion":"v8.0.55","capabilities":["start","stop"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + artifactID + `","version":"v8.0.55"},"recovery":{"state":"inactive","attemptsRemaining":0}}`))
			case "/v1/runtime/operations/activate-update", "/v1/runtime/operations/start":
				if hj, ok := w.(http.Hijacker); ok {
					if conn, _, err := hj.Hijack(); err == nil {
						_ = conn.Close()
						return
					}
				}
				w.WriteHeader(http.StatusInternalServerError)
			case "/v1/runtime/operations/update":
				if observeCalls.Add(1) == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(`{"error":{"code":"operation_persistence_unavailable","message":"busy"}}`))
					return
				}
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error":{"code":"operation_not_found","message":"not found"}}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(supervisor.Close)

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("observe-preserve-token"), 0o600); err != nil {
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
		rec, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: r.Operation.Identity.OperationID, Revision: 1,
				Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		// Populate [activate_update, start] in unresolvedStarts via dropped Supervisor RPCs.
		_, _ = app.embeddedRuntimeClient().ActivateUpdate(t.Context(), model.RuntimeActivateUpdateRequest{
			RuntimeMutationRequest: model.RuntimeMutationRequest{
				OperationID:               "activate-01",
				ExpectedRuntimeIdentity:   "runtime-01",
				ExpectedRuntimeGeneration: 1,
			},
			ExpectedActiveArtifactID: artifactID,
			TargetVersion:            "8.0.56",
		})
		_, _ = app.embeddedRuntimeClient().Start(t.Context(), model.RuntimeMutationRequest{
			OperationID:               "start-02",
			ExpectedRuntimeIdentity:   "runtime-01",
			ExpectedRuntimeGeneration: 1,
			ExpectedActiveArtifactID:  artifactID,
		})

		firstRes := postConnectExternal(t, app, externalServer.URL)
		if firstRes.Code != http.StatusConflict || !strings.Contains(firstRes.Body.String(), `"embedded_supervisor_status_unavailable"`) {
			t.Fatalf("first ConnectExternal on transient ObserveUpdateOperation 503 = %d %s, want 409 embedded_supervisor_status_unavailable", firstRes.Code, firstRes.Body.String())
		}

		// On retry, activate-01 resolves as operation_not_found, while trailing start-02 MUST remain fenced.
		secondRes := postConnectExternal(t, app, externalServer.URL)
		if secondRes.Code != http.StatusConflict || !strings.Contains(secondRes.Body.String(), `"embedded_runtime_start_unresolved"`) {
			t.Fatalf("second ConnectExternal after transient observe error = %d %s, want 409 embedded_runtime_start_unresolved",
				secondRes.Code, secondRes.Body.String())
		}
	})

	t.Run("failed_activate_update_rollback_to_running_embedded_cpa_reobserves_status_and_blocks_external_commit", func(t *testing.T) {
		externalServer := newExternalUpstream(t)
		var statusCalls atomic.Int32

		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/runtime/status":
				// Initial fence probe sees offline right before ActivateUpdate finishes rolling
				// back to the prior running Embedded CPA; re-observation sees ready + armed.
				if statusCalls.Add(1) == 1 {
					_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"offline","cpaObservedVersion":"v8.0.55","capabilities":["start","stop"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + artifactID + `","version":"v8.0.55"},"recovery":{"state":"inactive","attemptsRemaining":0}}`))
					return
				}
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"ready","cpaObservedVersion":"v8.0.55","capabilities":["start","stop"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + artifactID + `","version":"v8.0.55"},"recovery":{"state":"armed","attemptsRemaining":3}}`))
			case "/v1/runtime/operations/activate-update":
				if hj, ok := w.(http.Hijacker); ok {
					if conn, _, err := hj.Hijack(); err == nil {
						_ = conn.Close()
						return
					}
				}
				w.WriteHeader(http.StatusInternalServerError)
			case "/v1/runtime/operations/update":
				_, _ = w.Write([]byte(`{"operationId":"activate-rollback-01","operationType":"activate_update","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"failed","createdAt":"2026-11-11T00:00:00Z","updatedAt":"2026-11-11T00:00:02Z","completedAt":"2026-11-11T00:00:02Z","error":{"code":"internal_error","message":"candidate failed; rolled back to prior CPA"}}`))
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(supervisor.Close)

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("activate-rollback-token"), 0o600); err != nil {
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
		rec, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: r.Operation.Identity.OperationID, Revision: 1,
				Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		_, _ = app.embeddedRuntimeClient().ActivateUpdate(t.Context(), model.RuntimeActivateUpdateRequest{
			RuntimeMutationRequest: model.RuntimeMutationRequest{
				OperationID:               "activate-rollback-01",
				ExpectedRuntimeIdentity:   "runtime-01",
				ExpectedRuntimeGeneration: 1,
			},
			ExpectedActiveArtifactID: artifactID,
			TargetVersion:            "8.0.56",
		})

		res := postConnectExternal(t, app, externalServer.URL)
		if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), `"embedded_runtime_not_stopped"`) {
			t.Fatalf("ConnectExternal after failed ActivateUpdate rollback = %d %s, want 409 embedded_runtime_not_stopped",
				res.Code, res.Body.String())
		}
	})

	t.Run("offline_without_recovery_evidence_refuses_external_commit", func(t *testing.T) {
		externalServer := newExternalUpstream(t)
		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// Valid Protocol v1 offline observation WITHOUT recovery object.
			_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"offline","cpaObservedVersion":"v8.0.55","capabilities":["start","stop"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + artifactID + `","version":"v8.0.55"}}`))
		}))
		t.Cleanup(supervisor.Close)

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("missing-recovery-token"), 0o600); err != nil {
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
		rec, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: r.Operation.Identity.OperationID, Revision: 1,
				Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		res := postConnectExternal(t, app, externalServer.URL)
		var blocker freshStrategyActionBlocker
		if res.Code != http.StatusConflict || json.Unmarshal(res.Body.Bytes(), &blocker) != nil ||
			blocker.Code != "embedded_runtime_recovery_unverified" || blocker.Action == "" {
			t.Fatalf("ConnectExternal with offline and missing recovery = %d %s, want 409 embedded_runtime_recovery_unverified with Action",
				res.Code, res.Body.String())
		}
	})

	t.Run("manager_restart_after_accepted_start_still_fences_external_commit_until_new_supervisor_generation", func(t *testing.T) {
		externalServer := newExternalUpstream(t)
		startAccepted := make(chan struct{})
		releaseSpawn := make(chan struct{})
		var acceptOnce sync.Once
		var releaseOnce sync.Once
		var supervisorGen atomic.Uint64
		supervisorGen.Store(1)

		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			gen := strconv.FormatUint(supervisorGen.Load(), 10)
			switch r.URL.Path {
			case "/v1/runtime/status":
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":` + gen + `,"state":"offline","cpaObservedVersion":"v8.0.55","capabilities":["start","stop"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + artifactID + `","version":"v8.0.55"},"recovery":{"state":"inactive","attemptsRemaining":0}}`))
			case "/v1/runtime/operations/start":
				acceptOnce.Do(func() { close(startAccepted) })
				if hj, ok := w.(http.Hijacker); ok {
					if conn, _, err := hj.Hijack(); err == nil {
						_ = conn.Close()
						return
					}
				}
				<-releaseSpawn
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(supervisor.Close)
		t.Cleanup(func() { releaseOnce.Do(func() { close(releaseSpawn) }) })

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("restart-ledger-token"), 0o600); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
		paths.Config = filepath.Join(dir, "config.json")
		opened1, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatal(err)
		}

		app1 := newFreshApplication(opened1, config.Config{
			RuntimeURL:       supervisor.URL,
			RuntimeTokenFile: tokenPath,
			CPAUpstreamURL:   "http://127.0.0.1:8317",
		}, nil)
		rec, err := app1.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app1.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: r.Operation.Identity.OperationID, Revision: 1,
				Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		// Dispatch unjournaled reconciler Start on app1; Supervisor accepts and drops connection.
		reconciler := runtimeservice.NewReconciler(
			app1.embeddedRuntimeClient(),
			staticDesiredRunningStore{},
			time.Second,
			nil,
		)
		_ = reconciler.ReconcileOnce(t.Context())
		<-startAccepted

		// Simulate full Manager process restart: close opened1 and open a brand-new
		// freshApplication (app2) on the same SQLite database (all in-memory state wiped).
		if err := opened1.Close(); err != nil {
			t.Fatal(err)
		}
		opened2, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = opened2.Close() })
		app2 := newFreshApplication(opened2, config.Config{
			RuntimeURL:       supervisor.URL,
			RuntimeTokenFile: tokenPath,
			CPAUpstreamURL:   "http://127.0.0.1:8317",
		}, nil)
		app2.strategy = freshstrategy.New(freshstrategy.Options{
			Protector:     opened2.Protector,
			Qualification: bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
		})

		blockedRes := postConnectExternal(t, app2, externalServer.URL)
		var blocker freshStrategyActionBlocker
		if blockedRes.Code != http.StatusConflict || json.Unmarshal(blockedRes.Body.Bytes(), &blocker) != nil ||
			blocker.Code != "embedded_runtime_start_unresolved" || blocker.Action == "" {
			t.Fatalf("ConnectExternal after Manager restart with unresolved gen=1 Start = %d %s, want 409 embedded_runtime_start_unresolved with Action",
				blockedRes.Code, blockedRes.Body.String())
		}

		// Once Supervisor restarts into generation 2 (offline + inactive), the prior
		// incarnation's accepted Start is provably gone and ConnectExternal commits.
		supervisorGen.Store(2)
		okRes := postConnectExternal(t, app2, externalServer.URL)
		if okRes.Code != http.StatusOK {
			t.Fatalf("ConnectExternal after Supervisor restarted to generation 2 = %d %s, want 200",
				okRes.Code, okRes.Body.String())
		}
	})

	t.Run("accepted_start_older_than_legacy_ttl_never_expires_by_wall_clock_time", func(t *testing.T) {
		externalServer := newExternalUpstream(t)
		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/runtime/status":
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"offline","cpaObservedVersion":"v8.0.55","capabilities":["start","stop"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + artifactID + `","version":"v8.0.55"},"recovery":{"state":"inactive","attemptsRemaining":0}}`))
			case "/v1/runtime/operations/start":
				if hj, ok := w.(http.Hijacker); ok {
					if conn, _, err := hj.Hijack(); err == nil {
						_ = conn.Close()
						return
					}
				}
				w.WriteHeader(http.StatusInternalServerError)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(supervisor.Close)

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("no-ttl-expiry-token"), 0o600); err != nil {
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
		app.runtimeSettings = openIsolatedDesiredSettings(t, opened)
		app.strategy = freshstrategy.New(freshstrategy.Options{
			Protector:     opened.Protector,
			Qualification: bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
		})
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
		preCommitCompleteOp := rec.Operation
		preCommitCompleteOp.ProductComplete = true

		opOut := httptest.NewRecorder()
		opReq := httptest.NewRequest(http.MethodPost, "/control/api/v2/runtime/operations",
			strings.NewReader(`{"action":"start","requestId":"dddddddd-4444-4444-8444-444444444444"}`))
		opReq.Header.Set("Content-Type", "application/json")
		app.runtimeOperations(opOut, opReq, preCommitCompleteOp, "runtime/operations")

		// Backdate BOTH the start ledger and the runtime operation journal by 10 minutes
		// (> legacy 2-minute freshRuntimeOperationUnknownBound), and let runtimeStatus
		// transition the display journal entry to unknown_expired.
		tenMinutesAgo := time.Now().UTC().Add(-10 * time.Minute)
		app.embeddedExecution.ledgerMu.Lock()
		ledger, err := app.loadEmbeddedStartLedgerLocked(t.Context())
		if err != nil {
			app.embeddedExecution.ledgerMu.Unlock()
			t.Fatal(err)
		}
		for k, v := range ledger.Unresolved {
			v.CreatedAt = tenMinutesAgo
			ledger.Unresolved[k] = v
		}
		if err := app.saveEmbeddedStartLedgerLocked(t.Context(), ledger); err != nil {
			app.embeddedExecution.ledgerMu.Unlock()
			t.Fatal(err)
		}
		app.embeddedExecution.ledgerMu.Unlock()

		jData, err := app.runtimeJournal.load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range jData.Operations {
			v.CreatedAt = tenMinutesAgo
			v.UpdatedAt = tenMinutesAgo
			jData.Operations[k] = v
		}
		if err := app.runtimeJournal.save(t.Context(), jData); err != nil {
			t.Fatal(err)
		}
		statusOut := httptest.NewRecorder()
		app.runtimeStatus(statusOut, httptest.NewRequest(http.MethodGet, "/control/api/v2/runtime/status", nil), preCommitCompleteOp)

		// ConnectExternal must STILL refuse with 409: wall-clock age never proves completion.
		res := postConnectExternal(t, app, externalServer.URL)
		if res.Code != http.StatusConflict || !strings.Contains(res.Body.String(), `"embedded_runtime_start_unresolved"`) {
			t.Fatalf("ConnectExternal 10m after unconfirmed Start = %d %s, want 409 embedded_runtime_start_unresolved",
				res.Code, res.Body.String())
		}
	})
}

// TestUnreachableOrUnverifiedSupervisorFailsClosedWithActionable409AndAllowsStandaloneExternalCommit
// verifies that whenever an embedded Supervisor is configured (CPAMP_RUNTIME_URL),
// Connect External fails closed with 409 embedded_supervisor_status_unavailable and
// non-empty operator recovery guidance on unreachable, 401, or unverified-protocol
// Status responses even on a pristine database, while standalone deployments
// without CPAMP_RUNTIME_URL (a.runtime == nil) continue committing External without
// a Supervisor.
func TestUnreachableOrUnverifiedSupervisorFailsClosedWithActionable409AndAllowsStandaloneExternalCommit(t *testing.T) {
	const management = externalAuthorityManagement

	newExternalUpstream := func(t *testing.T) *httptest.Server {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-CPA-Version", "v8.0.55")
			switch r.URL.Path {
			case "/v0/management/config":
				_, _ = w.Write([]byte(`{}`))
			case "/v8/management/config":
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
			default:
				_, _ = w.Write([]byte(`{"ok":true}`))
			}
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	newPristineApp := func(t *testing.T, runtimeURL string, tokenPath string) *freshApplication {
		t.Helper()
		dir := t.TempDir()
		paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
		paths.Config = filepath.Join(dir, "config.json")
		opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = opened.Close() })

		app := newFreshApplication(opened, config.Config{
			RuntimeURL:       runtimeURL,
			RuntimeTokenFile: tokenPath,
			CPAUpstreamURL:   "http://127.0.0.1:8317",
		}, nil)
		app.strategy = freshstrategy.New(freshstrategy.Options{
			Protector:     opened.Protector,
			Qualification: bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
		})
		rec, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: r.Operation.Identity.OperationID, Revision: 1,
				Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return app
	}

	postConnectExternal := func(t *testing.T, app *freshApplication, externalURL string) *httptest.ResponseRecorder {
		t.Helper()
		cur, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(struct {
			Identity         bootstrap.FreshIdentity `json:"identity"`
			ExpectedRevision uint64                  `json:"expectedRevision"`
			AttemptID        string                  `json:"attemptId"`
			freshstrategy.Request
		}{
			Identity:         cur.Operation.Identity,
			ExpectedRevision: cur.Operation.Revision,
			AttemptID:        strings.Repeat("f", 32),
			Request: freshstrategy.Request{
				Strategy:      bootstrap.ConnectExternal,
				BaseURL:       externalURL,
				ManagementKey: management,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/control/api/v2/strategy", bytes.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		app.canonicalAPI(rec, req)
		return rec
	}

	assertBlockedUnavailable := func(t *testing.T, app *freshApplication, res *httptest.ResponseRecorder, label string) {
		t.Helper()
		var blocker freshStrategyActionBlocker
		if res.Code != http.StatusConflict || json.Unmarshal(res.Body.Bytes(), &blocker) != nil ||
			blocker.Code != "embedded_supervisor_status_unavailable" || strings.TrimSpace(blocker.Action) == "" {
			t.Fatalf("%s = %d %s, want 409 embedded_supervisor_status_unavailable with non-empty Action",
				label, res.Code, res.Body.String())
		}
		cur, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if cur.Operation.Strategy.Strategy != "" {
			t.Fatalf("%s committed strategy = %+v, want uncommitted", label, cur.Operation.Strategy)
		}
	}

	t.Run("pristine_db_with_unreachable_supervisor_fails_closed_with_actionable_409", func(t *testing.T) {
		externalServer := newExternalUpstream(t)
		closedSupervisor := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		closedSupervisor.Close()

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("unreachable-token"), 0o600); err != nil {
			t.Fatal(err)
		}
		app := newPristineApp(t, closedSupervisor.URL, tokenPath)

		res := postConnectExternal(t, app, externalServer.URL)
		assertBlockedUnavailable(t, app, res, "pristine ConnectExternal with unreachable Supervisor")
	})

	t.Run("pristine_db_with_supervisor_401_fails_closed_until_status_recovers", func(t *testing.T) {
		externalServer := newExternalUpstream(t)
		var healthy atomic.Bool
		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path != "/v1/runtime/status" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if !healthy.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","errorCode":"unauthorized","message":"invalid runtime token"}`))
				return
			}
			_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"offline","recovery":{"state":"inactive"}}`))
		}))
		t.Cleanup(supervisor.Close)

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("supervisor-401-token"), 0o600); err != nil {
			t.Fatal(err)
		}
		app := newPristineApp(t, supervisor.URL, tokenPath)

		blockedRes := postConnectExternal(t, app, externalServer.URL)
		assertBlockedUnavailable(t, app, blockedRes, "pristine ConnectExternal with 401 Supervisor")

		healthy.Store(true)
		okRes := postConnectExternal(t, app, externalServer.URL)
		if okRes.Code != http.StatusOK {
			t.Fatalf("ConnectExternal after Supervisor recovers = %d %s, want 200", okRes.Code, okRes.Body.String())
		}
		committed, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if committed.Operation.Strategy.Strategy != bootstrap.ConnectExternal || committed.Operation.Strategy.Stage != "committed" {
			t.Fatalf("committed strategy = %+v, want ConnectExternal/committed", committed.Operation.Strategy)
		}
	})

	t.Run("pristine_db_with_supervisor_protocol_v2_fails_closed_with_actionable_409", func(t *testing.T) {
		externalServer := newExternalUpstream(t)
		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/v1/runtime/status" {
				_, _ = w.Write([]byte(`{"protocolVersion":"v2","state":"ready"}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(supervisor.Close)

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("supervisor-v2-token"), 0o600); err != nil {
			t.Fatal(err)
		}
		app := newPristineApp(t, supervisor.URL, tokenPath)

		res := postConnectExternal(t, app, externalServer.URL)
		assertBlockedUnavailable(t, app, res, "pristine ConnectExternal with protocol v2 Supervisor")
	})

	t.Run("standalone_without_runtime_url_commits_external_without_supervisor", func(t *testing.T) {
		externalServer := newExternalUpstream(t)
		app := newPristineApp(t, "", "")

		res := postConnectExternal(t, app, externalServer.URL)
		if res.Code != http.StatusOK {
			t.Fatalf("standalone ConnectExternal without Supervisor = %d %s, want 200", res.Code, res.Body.String())
		}
		committed, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if committed.Operation.Strategy.Strategy != bootstrap.ConnectExternal || committed.Operation.Strategy.Stage != "committed" {
			t.Fatalf("committed strategy = %+v, want ConnectExternal/committed", committed.Operation.Strategy)
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

			secondParked := make(chan struct{})
			var secondOnce sync.Once
			app.legacyCredentialGate.onWait = func() {
				secondOnce.Do(func() { close(secondParked) })
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
			<-secondParked
			if got := upstreamHits.Load(); got != 1 {
				t.Fatalf("concurrent runtime/status probes reached upstream %d times while first was in flight, want 1", got)
			}
			close(release)
			first := <-firstDone
			second := <-secondDone
			if first.Code != http.StatusOK || second.Code != http.StatusOK {
				t.Fatalf("runtime/status codes = (%d, %d), want (200, 200)", first.Code, second.Code)
			}
			for idx, resp := range []*httptest.ResponseRecorder{first, second} {
				var decoded struct {
					Status    string `json:"status"`
					ErrorCode string `json:"errorCode"`
				}
				if err := json.Unmarshal(resp.Body.Bytes(), &decoded); err != nil {
					t.Fatalf("response #%d decode: %v (%s)", idx+1, err, resp.Body.String())
				}
				if decoded.Status != "unknown" || decoded.ErrorCode != freshconfig.ReasonManagementRejected {
					t.Fatalf("response #%d = %+v (%s), want status=unknown errorCode=%s",
						idx+1, decoded, resp.Body.String(), freshconfig.ReasonManagementRejected)
				}
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

		var supervisorRunning atomic.Bool
		var notifyStatusMu sync.Mutex
		var notifyNextStatus chan struct{}
		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/runtime/status":
				supervisorStatusHits.Add(1)
				notifyStatusMu.Lock()
				ch := notifyNextStatus
				notifyNextStatus = nil
				notifyStatusMu.Unlock()
				if ch != nil {
					close(ch)
				}
				state := "offline"
				if supervisorRunning.Load() {
					state = "ready"
				}
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"` + state + `","capabilities":["start","restart","stage_destination"],"cpaObservedVersion":"v8.0.55","activeGatewayArtifact":{"engine":"cpa","artifactId":"` + artifactID + `","version":"v8.0.55"},"recovery":{"state":"inactive","attemptsRemaining":0}}`))
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
				supervisorRunning.Store(true)
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
		app.desiredState = staticDesiredStoppedStore{}

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
		if instRes.Code != http.StatusOK || !strings.Contains(instRes.Body.String(), `"state":"offline"`) {
			t.Fatalf("GET installation/runtime after failed ConnectExternal = %d %s, want 200 state=offline", instRes.Code, instRes.Body.String())
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

		nextStatus := make(chan struct{})
		notifyStatusMu.Lock()
		notifyNextStatus = nextStatus
		notifyStatusMu.Unlock()
		select {
		case <-nextStatus:
		case <-time.After(2 * time.Second):
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

// TestEmbeddedReconcilerRetiresViaSignalWithoutFastPolling verifies through
// ServeHTTP, SQLite, and a fake HTTP Supervisor that runFreshEmbeddedReconciler
// retires immediately via the in-memory externalCommitted signal when
// ConnectExternal commits even when configured with a multi-hour reconcile
// interval, and makes zero post-commit Supervisor requests.
type externalCommitBoundaryStore struct {
	freshstore.Store
	onCommitted func()
}

func (s externalCommitBoundaryStore) Update(
	ctx context.Context,
	id bootstrap.FreshIdentity,
	rev uint64,
	fn func(*freshstore.Record) error,
) (freshstore.Record, error) {
	next, err := s.Store.Update(ctx, id, rev, fn)
	if err == nil && isCommittedExternalStrategy(next.Operation.Strategy) && s.onCommitted != nil {
		s.onCommitted()
	}
	return next, err
}

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

	initialStatusObserved := make(chan struct{})
	var initialOnce sync.Once
	var casBoundaryReached atomic.Bool
	var externalCommitted atomic.Bool
	var postCommitSupervisorHits atomic.Int32
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if externalCommitted.Load() {
			postCommitSupervisorHits.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"offline","recovery":{"state":"inactive","attemptsRemaining":0}}`))
		initialOnce.Do(func() { close(initialStatusObserved) })
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
	if app.pluginCapability != nil {
		app.pluginCapability.launch = func(refresh func()) { refresh() }
	}
	app.strategy = freshstrategy.New(freshstrategy.Options{
		Protector:     opened.Protector,
		Qualification: bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
	})
	app.desiredState = staticDesiredStoppedStore{}

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

	app.store = externalCommitBoundaryStore{
		Store: app.store,
		onCommitted: func() {
			// Flip at the exact durable SQLite CAS boundary, before finishExternalCommit
			// and before ServeHTTP returns.
			casBoundaryReached.Store(true)
			externalCommitted.Store(true)
		},
	}

	reconcilerCtx, cancelReconciler := context.WithCancel(t.Context())
	defer cancelReconciler()
	reconcilerExited := make(chan struct{})
	go func() {
		defer close(reconcilerExited)
		app.runFreshEmbeddedReconciler(reconcilerCtx, 10*time.Hour)
	}()
	select {
	case <-initialStatusObserved:
	case <-time.After(2 * time.Second):
		t.Fatal("10h reconciler initial Supervisor observation did not arrive")
	}

	reqBody, err := json.Marshal(struct {
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
	commitReq := httptest.NewRequest(http.MethodPost, "/control/api/v2/strategy", bytes.NewReader(reqBody))
	commitReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	commitReq.Header.Set("Content-Type", "application/json")
	commitRes := httptest.NewRecorder()
	app.ServeHTTP(commitRes, commitReq)
	if commitRes.Code != http.StatusOK {
		t.Fatalf("POST strategy (ConnectExternal) = %d %s, want 200", commitRes.Code, commitRes.Body.String())
	}
	if !casBoundaryReached.Load() {
		t.Fatal("durable External CAS boundary hook was not reached")
	}

	select {
	case <-reconcilerExited:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("reconciler did not retire immediately via externalCommitted signal")
	}

	// Drive controlled post-commit HTTP entry points before and after completion:
	// embedded installation observation and lifecycle mutations must fail closed
	// at the HTTP boundary without sending any request to the embedded Supervisor.
	installObsReq := httptest.NewRequest(http.MethodGet, "/control/api/v2/installation/runtime", nil)
	installObsReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	installObsRes := httptest.NewRecorder()
	app.ServeHTTP(installObsRes, installObsReq)
	if installObsRes.Code != http.StatusConflict {
		t.Errorf("GET installation/runtime after External commit = %d %s, want 409", installObsRes.Code, installObsRes.Body.String())
	}

	completeReq := httptest.NewRequest(http.MethodPost, "/control/api/v2/complete", nil)
	completeReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	completeRes := httptest.NewRecorder()
	app.ServeHTTP(completeRes, completeReq)
	if completeRes.Code != http.StatusOK {
		t.Fatalf("POST complete (ConnectExternal) = %d %s, want 200", completeRes.Code, completeRes.Body.String())
	}

	mutReq := httptest.NewRequest(
		http.MethodPost,
		"/control/api/v2/runtime/operations",
		strings.NewReader(`{"action":"start","requestId":"99999999-9999-4999-8999-999999999999"}`),
	)
	mutReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	mutReq.Header.Set("Content-Type", "application/json")
	mutRes := httptest.NewRecorder()
	app.ServeHTTP(mutRes, mutReq)
	if mutRes.Code != http.StatusConflict || !strings.Contains(mutRes.Body.String(), `"runtime_mutation_unsupported"`) {
		t.Errorf("POST runtime/operations after External commit = %d %s, want 409 runtime_mutation_unsupported", mutRes.Code, mutRes.Body.String())
	}

	if got := postCommitSupervisorHits.Load(); got != 0 {
		t.Fatalf("post-CAS-commit Supervisor hits = %d, want 0", got)
	}
}

// TestEmbeddedStartLedgerWriteFailureRefusesMutatingSupervisorRPCsAndRecoversOnRetry
// verifies via real HTTP entry points, real SQLite persistence, and a fake HTTP
// Supervisor that when persisting the write-ahead embedded start ledger fails,
// Start (both wizard and runtime operations API), Restart, and ActivateUpdate
// fail closed with an HTTP error and zero start/restart/activate RPCs reaching
// the Supervisor, and succeed once SQLite recovers.
func TestEmbeddedStartLedgerWriteFailureRefusesMutatingSupervisorRPCsAndRecoversOnRetry(t *testing.T) {
	const productionArtifactID = "sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"

	installLedgerWriteFailure := func(t *testing.T, app *freshApplication) (recoverStore func()) {
		t.Helper()
		db := app.runtimeJournal.db
		if _, err := db.ExecContext(t.Context(), `
			CREATE TRIGGER test_fail_start_ledger_insert
			BEFORE INSERT ON settings
			WHEN NEW.key = 'fresh_embedded_start_ledger_v1'
			BEGIN
				SELECT RAISE(ABORT, 'simulated start ledger write failure');
			END;
		`); err != nil {
			t.Fatalf("install insert trigger: %v", err)
		}
		if _, err := db.ExecContext(t.Context(), `
			CREATE TRIGGER test_fail_start_ledger_update
			BEFORE UPDATE ON settings
			WHEN NEW.key = 'fresh_embedded_start_ledger_v1'
			BEGIN
				SELECT RAISE(ABORT, 'simulated start ledger write failure');
			END;
		`); err != nil {
			t.Fatalf("install update trigger: %v", err)
		}
		return func() {
			if _, err := db.ExecContext(t.Context(), `DROP TRIGGER IF EXISTS test_fail_start_ledger_insert; DROP TRIGGER IF EXISTS test_fail_start_ledger_update;`); err != nil {
				t.Fatalf("drop start ledger failure triggers: %v", err)
			}
		}
	}

	t.Run("wizard_new_managed_start", func(t *testing.T) {
		var (
			startHits    atomic.Int32
			restartHits  atomic.Int32
			stopHits     atomic.Int32
			activateHits atomic.Int32
			prepareHits  atomic.Int32
			startedReady atomic.Bool
			stagedKeyMu  sync.Mutex
			stagedKey    string
		)

		destinationServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			stagedKeyMu.Lock()
			expectedKey := stagedKey
			stagedKeyMu.Unlock()
			if r.URL.Path != "/v8/management/config" || expectedKey == "" || r.Header.Get("Authorization") != "Bearer "+expectedKey {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`))
		}))
		t.Cleanup(destinationServer.Close)

		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/runtime/status":
				state := "offline"
				if startedReady.Load() {
					state = "ready"
				}
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"` + state + `","cpaObservedVersion":"v8.0.55","capabilities":["start","stop","stage_destination"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + productionArtifactID + `","version":"v8.0.55"},"recovery":{"state":"inactive","attemptsRemaining":0}}`))
			case "/v1/runtime/operations/stage-destination":
				var payload struct {
					OperationID   string `json:"operationId"`
					ManagementKey string `json:"managementKey"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				stagedKeyMu.Lock()
				stagedKey = payload.ManagementKey
				stagedKeyMu.Unlock()
				_, _ = w.Write([]byte(`{"operationId":"` + payload.OperationID + `","operationType":"stage_destination","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"succeeded"}`))
			case "/v1/runtime/operations/start":
				startHits.Add(1)
				var payload struct {
					OperationID string `json:"operationId"`
				}
				_ = json.NewDecoder(r.Body).Decode(&payload)
				startedReady.Store(true)
				_, _ = w.Write([]byte(`{"operationId":"` + payload.OperationID + `","operationType":"start","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"succeeded"}`))
			case "/v1/runtime/operations/restart":
				restartHits.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			case "/v1/runtime/operations/stop":
				stopHits.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			case "/v1/runtime/operations/prepare-update":
				prepareHits.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			case "/v1/runtime/operations/activate-update":
				activateHits.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))
		t.Cleanup(supervisor.Close)

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("ledger-wizard-token"), 0o600); err != nil {
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
			CPAUpstreamURL:   destinationServer.URL,
		}, nil)
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

		postNewManaged := func(attemptID string) *httptest.ResponseRecorder {
			cur, loadErr := app.store.Load(t.Context())
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			body, marshalErr := json.Marshal(struct {
				Identity         bootstrap.FreshIdentity `json:"identity"`
				ExpectedRevision uint64                  `json:"expectedRevision"`
				AttemptID        string                  `json:"attemptId"`
				freshstrategy.Request
			}{
				Identity:         cur.Operation.Identity,
				ExpectedRevision: cur.Operation.Revision,
				AttemptID:        attemptID,
				Request: freshstrategy.Request{
					Strategy:                  bootstrap.NewManaged,
					ExpectedRuntimeIdentity:   "runtime-01",
					ExpectedRuntimeGeneration: 1,
					ExpectedArtifactID:        productionArtifactID,
				},
			})
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			req := httptest.NewRequest(http.MethodPost, "/control/api/v2/strategy", bytes.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			req.Header.Set("Content-Type", "application/json")
			out := httptest.NewRecorder()
			app.ServeHTTP(out, req)
			return out
		}

		// Step 1 of wizard new_managed prepares the durable "staged" checkpoint before execution.
		stageRes := postNewManaged(strings.Repeat("1", 32))
		if stageRes.Code != http.StatusOK {
			t.Fatalf("initial new_managed stage = %d %s, want 200", stageRes.Code, stageRes.Body.String())
		}

		// Inject real SQLite write failure on fresh_embedded_start_ledger_v1 before the Start step.
		recoverStore := installLedgerWriteFailure(t, app)
		failRes := postNewManaged(strings.Repeat("2", 32))
		if failRes.Code < 400 {
			t.Fatalf("wizard new_managed with failing start ledger write = %d %s, want HTTP error", failRes.Code, failRes.Body.String())
		}
		if got := startHits.Load() + restartHits.Load() + stopHits.Load() + prepareHits.Load() + activateHits.Load(); got != 0 {
			t.Fatalf("Supervisor lifecycle mutating RPCs on failing ledger write = %d (start=%d), want 0", got, startHits.Load())
		}
		afterFail, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if afterFail.Operation.Strategy.Stage == "committed" {
			t.Fatalf("strategy stage committed despite failing ledger write: %+v", afterFail.Operation.Strategy)
		}

		// Recover SQLite and verify retry succeeds via HTTP.
		recoverStore()
		retryRes := postNewManaged(strings.Repeat("3", 32))
		if retryRes.Code != http.StatusOK {
			t.Fatalf("wizard new_managed retry after store recovery = %d %s, want 200", retryRes.Code, retryRes.Body.String())
		}
		if got := startHits.Load(); got != 1 {
			t.Fatalf("Supervisor Start RPCs after store recovery = %d, want 1", got)
		}
		afterRetry, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if afterRetry.Operation.Strategy.Strategy != bootstrap.NewManaged || afterRetry.Operation.Strategy.Stage != "committed" {
			t.Fatalf("committed strategy after retry = %+v, want committed new_managed", afterRetry.Operation.Strategy)
		}
	})

	for _, tc := range []struct {
		name         string
		initialState string
		action       string
	}{
		{name: "runtime_operations_start", initialState: "offline", action: "start"},
		{name: "runtime_operations_restart", initialState: "ready", action: "restart"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mutatingHits atomic.Int32
				startHits    atomic.Int32
				restartHits  atomic.Int32
				curState     atomic.Value
			)
			curState.Store(tc.initialState)

			supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					mutatingHits.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/v1/runtime/status":
					st := curState.Load().(string)
					_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"` + st + `","cpaObservedVersion":"v8.0.55","capabilities":["start","stop","restart"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + productionArtifactID + `","version":"v8.0.55"},"recovery":{"state":"inactive","attemptsRemaining":0}}`))
				case "/v1/runtime/operations/start":
					startHits.Add(1)
					var payload struct {
						OperationID string `json:"operationId"`
					}
					_ = json.NewDecoder(r.Body).Decode(&payload)
					curState.Store("ready")
					_, _ = w.Write([]byte(`{"operationId":"` + payload.OperationID + `","operationType":"start","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"succeeded"}`))
				case "/v1/runtime/operations/restart":
					restartHits.Add(1)
					var payload struct {
						OperationID string `json:"operationId"`
					}
					_ = json.NewDecoder(r.Body).Decode(&payload)
					curState.Store("ready")
					_, _ = w.Write([]byte(`{"operationId":"` + payload.OperationID + `","operationType":"restart","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"succeeded"}`))
				default:
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			t.Cleanup(supervisor.Close)

			tokenPath := filepath.Join(t.TempDir(), "runtime-token")
			if err := os.WriteFile(tokenPath, []byte("ledger-ops-token"), 0o600); err != nil {
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
			protected, err := opened.Protector.ProtectString(strings.Repeat("e", 64))
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
					Strategy:                      bootstrap.NewManaged,
					Stage:                         "committed",
					TargetIdentity:                "runtime-01",
					TargetGeneration:              1,
					TargetArtifactID:              productionArtifactID,
					DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity("http://127.0.0.1:8317"),
				}
				r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "http://127.0.0.1:8317", ManagementKey: protected}
				r.Operation.ProductComplete = true
				return nil
			}); err != nil {
				t.Fatal(err)
			}

			postRuntimeOp := func(requestID string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(
					http.MethodPost,
					"/control/api/v2/runtime/operations",
					strings.NewReader(`{"action":"`+tc.action+`","requestId":"`+requestID+`"}`),
				)
				req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
				req.Header.Set("Content-Type", "application/json")
				out := httptest.NewRecorder()
				app.ServeHTTP(out, req)
				return out
			}

			recoverStore := installLedgerWriteFailure(t, app)
			failRes := postRuntimeOp("44444444-4444-4444-8444-111111111111")
			if failRes.Code < 400 {
				t.Fatalf("%s with failing start ledger write = %d %s, want HTTP error", tc.action, failRes.Code, failRes.Body.String())
			}
			if got := mutatingHits.Load(); got != 0 {
				t.Fatalf("Supervisor mutating RPCs during failing %s ledger write = %d, want 0", tc.action, got)
			}
			if desired, found, err := app.runtimeSettings.LoadEmbeddedRuntimeDesiredState(t.Context()); err != nil || found {
				t.Fatalf("desired lifecycle persisted despite failing %s ledger write: (%+v, %v, %v)", tc.action, desired, found, err)
			}

			recoverStore()
			retryRes := postRuntimeOp("44444444-4444-4444-8444-222222222222")
			if retryRes.Code != http.StatusOK || !strings.Contains(retryRes.Body.String(), `"state":"succeeded"`) {
				t.Fatalf("%s retry after store recovery = %d %s, want 200 succeeded", tc.action, retryRes.Code, retryRes.Body.String())
			}
			if tc.action == "start" && startHits.Load() != 1 {
				t.Fatalf("Supervisor Start RPCs after store recovery = %d, want 1", startHits.Load())
			}
			if tc.action == "restart" && restartHits.Load() != 1 {
				t.Fatalf("Supervisor Restart RPCs after store recovery = %d, want 1", restartHits.Load())
			}
		})
	}

	t.Run("cpa_update_activate", func(t *testing.T) {
		var (
			mutatingHits atomic.Int32
			activateHits atomic.Int32
		)

		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				mutatingHits.Add(1)
			}
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v1/runtime/status":
				_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"ready","cpaObservedVersion":"8.0.55","capabilities":["prepare_update","activate_update"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + productionArtifactID + `","version":"8.0.55"},"recovery":{"state":"armed","attemptsRemaining":3}}`))
			case "/v1/runtime/operations/activate-update":
				activateHits.Add(1)
				var payload struct {
					OperationID string `json:"operationId"`
				}
				_ = json.NewDecoder(r.Body).Decode(&payload)
				_, _ = w.Write([]byte(`{"operationId":"` + payload.OperationID + `","operationType":"activate_update","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"succeeded"}`))
			default:
				w.WriteHeader(http.StatusInternalServerError)
			}
		}))
		t.Cleanup(supervisor.Close)

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("ledger-activate-token"), 0o600); err != nil {
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
		protected, err := opened.Protector.ProtectString(strings.Repeat("e", 64))
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
				Strategy:                      bootstrap.NewManaged,
				Stage:                         "committed",
				TargetIdentity:                "runtime-01",
				TargetGeneration:              1,
				TargetArtifactID:              productionArtifactID,
				DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity("http://127.0.0.1:8317"),
			}
			r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "http://127.0.0.1:8317", ManagementKey: protected}
			r.Operation.ProductComplete = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		checkedAt := time.Now().UTC()
		discoveryBytes, err := json.Marshal(cpaupdateservice.DiscoveryState{
			SchemaVersion: 1,
			LastAttemptAt: checkedAt,
			LastSuccessAt: checkedAt,
			TargetVersion: "8.0.56",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := setting.SaveCPAUpdateCheck(t.Context(), app.runtimeJournal.db, discoveryBytes); err != nil {
			t.Fatalf("seed CPA update discovery state: %v", err)
		}

		postActivate := func(requestID string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(
				http.MethodPost,
				"/control/api/v2/cpamp/usage-service/runtime/updates/activate",
				strings.NewReader(`{"request_id":"`+requestID+`","target_version":"8.0.56","expected_active_artifact_id":"`+productionArtifactID+`"}`),
			)
			req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			req.Header.Set("Content-Type", "application/json")
			out := httptest.NewRecorder()
			app.ServeHTTP(out, req)
			return out
		}

		recoverStore := installLedgerWriteFailure(t, app)
		failRes := postActivate("55555555-5555-4555-8555-111111111111")
		if failRes.Code != http.StatusServiceUnavailable {
			t.Fatalf("CPA update activate with failing start ledger write = %d %s, want 503", failRes.Code, failRes.Body.String())
		}
		if got := mutatingHits.Load(); got != 0 {
			t.Fatalf("Supervisor mutating RPCs during failing activate_update ledger write = %d, want 0", got)
		}

		recoverStore()
		retryRes := postActivate("55555555-5555-4555-8555-222222222222")
		if retryRes.Code != http.StatusOK || !strings.Contains(retryRes.Body.String(), `"state":"succeeded"`) {
			t.Fatalf("CPA update activate retry after store recovery = %d %s, want 200 succeeded", retryRes.Code, retryRes.Body.String())
		}
		if got := activateHits.Load(); got != 1 {
			t.Fatalf("Supervisor ActivateUpdate RPCs after store recovery = %d, want 1", got)
		}
	})
}

// TestLegacyEverDispatchedStartLedgerRowCompatibilityAcrossRestart verifies
// persistent backward compatibility with rows written by commit a6ee4b71
// (containing top-level "everDispatched" alongside "unresolved" entries):
// rebuilding freshApplication over that SQLite row fences Connect External with
// 409 embedded_runtime_start_unresolved until a new Supervisor generation is
// observed, and allows Connect External when "unresolved" is empty and the
// Supervisor reports offline with disarmed recovery.
func TestLegacyEverDispatchedStartLedgerRowCompatibilityAcrossRestart(t *testing.T) {
	const (
		management           = externalAuthorityManagement
		productionArtifactID = "sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"
	)

	newExternalServer := func(t *testing.T) *httptest.Server {
		t.Helper()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+management {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v8/management/config":
				_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/data/auth"},"access":{"api-keys":["ext-key-1"]}}`))
			default:
				_, _ = w.Write([]byte(`{}`))
			}
		}))
		t.Cleanup(srv.Close)
		return srv
	}

	postConnectExternalViaHTTP := func(t *testing.T, app *freshApplication, externalURL, attemptID string) *httptest.ResponseRecorder {
		t.Helper()
		cur, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(struct {
			Identity         bootstrap.FreshIdentity `json:"identity"`
			ExpectedRevision uint64                  `json:"expectedRevision"`
			AttemptID        string                  `json:"attemptId"`
			freshstrategy.Request
		}{
			Identity:         cur.Operation.Identity,
			ExpectedRevision: cur.Operation.Revision,
			AttemptID:        attemptID,
			Request: freshstrategy.Request{
				Strategy:      bootstrap.ConnectExternal,
				BaseURL:       externalURL,
				ManagementKey: management,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/control/api/v2/strategy", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		app.ServeHTTP(out, req)
		return out
	}

	t.Run("legacy_ever_dispatched_with_unresolved_start_blocks_until_new_supervisor_generation", func(t *testing.T) {
		externalServer := newExternalServer(t)
		var supervisorGen atomic.Uint64
		supervisorGen.Store(1)

		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/runtime/status" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			gen := strconv.FormatUint(supervisorGen.Load(), 10)
			_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":` + gen + `,"state":"offline","cpaObservedVersion":"v8.0.55","capabilities":["start","stop"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + productionArtifactID + `","version":"v8.0.55"},"recovery":{"state":"inactive","attemptsRemaining":0}}`))
		}))
		t.Cleanup(supervisor.Close)

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("legacy-ledger-token-1"), 0o600); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
		paths.Config = filepath.Join(dir, "config.json")
		opened1, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatal(err)
		}
		app1 := newFreshApplication(opened1, config.Config{
			RuntimeURL:       supervisor.URL,
			RuntimeTokenFile: tokenPath,
			CPAUpstreamURL:   "http://127.0.0.1:8317",
		}, nil)
		rec, err := app1.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app1.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: r.Operation.Identity.OperationID, Revision: 1,
				Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		// Insert exact legacy JSON shape written by a6ee4b71 (top-level everDispatched + unresolved map).
		const legacyUnresolvedJSON = `{"everDispatched":true,"unresolved":{"start:legacy-start-01:runtime-01:1":{"key":"start:legacy-start-01:runtime-01:1","operationId":"legacy-start-01","operationType":"start","runtimeIdentity":"runtime-01","runtimeGeneration":1,"createdAt":"2026-11-10T12:00:00Z"}}}`
		if _, err := app1.runtimeJournal.db.ExecContext(
			t.Context(),
			`INSERT INTO settings(key, value, updated_at_ms) VALUES(?, ?, ?)`,
			freshEmbeddedStartLedgerKey,
			legacyUnresolvedJSON,
			time.Now().UnixMilli(),
		); err != nil {
			t.Fatalf("insert legacy start ledger row: %v", err)
		}
		if err := opened1.Close(); err != nil {
			t.Fatal(err)
		}

		// Rebuild freshApplication over the same SQLite database.
		opened2, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = opened2.Close() })
		app2 := newFreshApplication(opened2, config.Config{
			RuntimeURL:       supervisor.URL,
			RuntimeTokenFile: tokenPath,
			CPAUpstreamURL:   "http://127.0.0.1:8317",
		}, nil)

		blockedRes := postConnectExternalViaHTTP(t, app2, externalServer.URL, strings.Repeat("a", 32))
		var blocker freshStrategyActionBlocker
		if blockedRes.Code != http.StatusConflict || json.Unmarshal(blockedRes.Body.Bytes(), &blocker) != nil ||
			blocker.Code != "embedded_runtime_start_unresolved" || blocker.Action == "" {
			t.Fatalf("ConnectExternal with legacy unresolved Start = %d %s, want 409 embedded_runtime_start_unresolved", blockedRes.Code, blockedRes.Body.String())
		}

		// Advancing Supervisor to a new generation releases the legacy unresolved entry.
		supervisorGen.Store(2)
		okRes := postConnectExternalViaHTTP(t, app2, externalServer.URL, strings.Repeat("b", 32))
		if okRes.Code != http.StatusOK {
			t.Fatalf("ConnectExternal after Supervisor generation 2 = %d %s, want 200", okRes.Code, okRes.Body.String())
		}
		after, err := app2.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if after.Operation.Strategy.Strategy != bootstrap.ConnectExternal || after.Operation.Strategy.Stage != "committed" {
			t.Fatalf("committed strategy = %+v, want committed connect_external", after.Operation.Strategy)
		}
	})

	t.Run("legacy_ever_dispatched_with_empty_unresolved_commits_when_supervisor_offline_and_disarmed", func(t *testing.T) {
		externalServer := newExternalServer(t)
		supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/runtime/status" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"offline","cpaObservedVersion":"v8.0.55","capabilities":["start","stop"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + productionArtifactID + `","version":"v8.0.55"},"recovery":{"state":"inactive","attemptsRemaining":0}}`))
		}))
		t.Cleanup(supervisor.Close)

		tokenPath := filepath.Join(t.TempDir(), "runtime-token")
		if err := os.WriteFile(tokenPath, []byte("legacy-ledger-token-2"), 0o600); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
		paths.Config = filepath.Join(dir, "config.json")
		opened1, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatal(err)
		}
		app1 := newFreshApplication(opened1, config.Config{
			RuntimeURL:       supervisor.URL,
			RuntimeTokenFile: tokenPath,
			CPAUpstreamURL:   "http://127.0.0.1:8317",
		}, nil)
		rec, err := app1.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := app1.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
				OperationID: r.Operation.Identity.OperationID, Revision: 1,
				Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		const legacySettledJSON = `{"everDispatched":true,"unresolved":{}}`
		if _, err := app1.runtimeJournal.db.ExecContext(
			t.Context(),
			`INSERT INTO settings(key, value, updated_at_ms) VALUES(?, ?, ?)`,
			freshEmbeddedStartLedgerKey,
			legacySettledJSON,
			time.Now().UnixMilli(),
		); err != nil {
			t.Fatalf("insert legacy settled start ledger row: %v", err)
		}
		if err := opened1.Close(); err != nil {
			t.Fatal(err)
		}

		opened2, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = opened2.Close() })
		app2 := newFreshApplication(opened2, config.Config{
			RuntimeURL:       supervisor.URL,
			RuntimeTokenFile: tokenPath,
			CPAUpstreamURL:   "http://127.0.0.1:8317",
		}, nil)

		okRes := postConnectExternalViaHTTP(t, app2, externalServer.URL, strings.Repeat("c", 32))
		if okRes.Code != http.StatusOK {
			t.Fatalf("ConnectExternal with legacy everDispatched:true + empty unresolved = %d %s, want 200", okRes.Code, okRes.Body.String())
		}
		after, err := app2.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if after.Operation.Strategy.Strategy != bootstrap.ConnectExternal || after.Operation.Strategy.Stage != "committed" {
			t.Fatalf("committed strategy = %+v, want committed connect_external", after.Operation.Strategy)
		}
	})
}

// TestValidatedExternalCheckpointKeepsSupervisorAndStartLedgerFenceAcrossRestart
// verifies that a pre-commit ConnectExternal checkpoint in stage "validated"
// does not omit the Embedded Runtime client when rebuilding freshApplication:
// committing ConnectExternal after restart still fails closed on a 401 or
// unreachable Supervisor or an unresolved start-ledger entry, and commits once
// the Supervisor reports healthy offline + disarmed status.
func TestValidatedExternalCheckpointKeepsSupervisorAndStartLedgerFenceAcrossRestart(t *testing.T) {
	const (
		management           = externalAuthorityManagement
		productionArtifactID = "sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"
	)

	externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+management {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v8/management/config":
			_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/data/auth"},"access":{"api-keys":["ext-key-1"]}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(externalServer.Close)

	seedValidatedExternalDB := func(t *testing.T, withUnresolvedLedger bool) string {
		t.Helper()
		dir := t.TempDir()
		paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
		paths.Config = filepath.Join(dir, "config.json")
		opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatal(err)
		}
		app := newFreshApplication(opened, config.Config{}, nil)
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
				Strategy:         bootstrap.ConnectExternal,
				TargetIdentity:   freshkeyapply.ExternalIdentity(externalServer.URL),
				TargetGeneration: 1,
				Stage:            "validated",
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if withUnresolvedLedger {
			const ledgerJSON = `{"unresolved":{"start:val-start-01:runtime-01:1":{"key":"start:val-start-01:runtime-01:1","operationId":"val-start-01","operationType":"start","runtimeIdentity":"runtime-01","runtimeGeneration":1,"createdAt":"2026-11-11T00:00:00Z"}}}`
			if _, err := app.runtimeJournal.db.ExecContext(
				t.Context(),
				`INSERT INTO settings(key, value, updated_at_ms) VALUES(?, ?, ?)`,
				freshEmbeddedStartLedgerKey,
				ledgerJSON,
				time.Now().UnixMilli(),
			); err != nil {
				t.Fatal(err)
			}
		}
		if err := opened.Close(); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	postConnectExternal := func(t *testing.T, app *freshApplication, attemptID string) *httptest.ResponseRecorder {
		t.Helper()
		cur, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		body, err := json.Marshal(struct {
			Identity         bootstrap.FreshIdentity `json:"identity"`
			ExpectedRevision uint64                  `json:"expectedRevision"`
			AttemptID        string                  `json:"attemptId"`
			freshstrategy.Request
		}{
			Identity:         cur.Operation.Identity,
			ExpectedRevision: cur.Operation.Revision,
			AttemptID:        attemptID,
			Request: freshstrategy.Request{
				Strategy:      bootstrap.ConnectExternal,
				BaseURL:       externalServer.URL,
				ManagementKey: management,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/control/api/v2/strategy", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		app.ServeHTTP(out, req)
		return out
	}

	var (
		supervisorMode atomic.Value
		supervisorGen  atomic.Uint64
	)
	supervisorMode.Store("401")
	supervisorGen.Store(1)

	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := supervisorMode.Load().(string)
		switch mode {
		case "401":
			w.WriteHeader(http.StatusUnauthorized)
			return
		case "unreachable":
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
			w.WriteHeader(http.StatusBadGateway)
			return
		default:
			w.Header().Set("Content-Type", "application/json")
			gen := strconv.FormatUint(supervisorGen.Load(), 10)
			_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":` + gen + `,"state":"offline","cpaObservedVersion":"v8.0.55","capabilities":["start","stop"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + productionArtifactID + `","version":"v8.0.55"},"recovery":{"state":"inactive","attemptsRemaining":0}}`))
		}
	}))
	t.Cleanup(supervisor.Close)

	tokenPath := filepath.Join(t.TempDir(), "runtime-token")
	if err := os.WriteFile(tokenPath, []byte("validated-external-token"), 0o600); err != nil {
		t.Fatal(err)
	}

	dbDir := seedValidatedExternalDB(t, true)
	paths := legacypreflight.DefaultPaths(filepath.Join(dbDir, "manager.sqlite"))
	paths.Config = filepath.Join(dbDir, "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })

	app := newFreshApplication(opened, config.Config{
		RuntimeURL:       supervisor.URL,
		RuntimeTokenFile: tokenPath,
		CPAUpstreamURL:   "http://127.0.0.1:8317",
	}, nil)

	// 1. Supervisor returning 401 fails closed with 409 embedded_supervisor_status_unavailable.
	res401 := postConnectExternal(t, app, strings.Repeat("1", 32))
	var blocker freshStrategyActionBlocker
	if res401.Code != http.StatusConflict || json.Unmarshal(res401.Body.Bytes(), &blocker) != nil || blocker.Code != "embedded_supervisor_status_unavailable" {
		t.Fatalf("ConnectExternal on validated checkpoint with 401 Supervisor = %d %s, want 409 embedded_supervisor_status_unavailable", res401.Code, res401.Body.String())
	}

	// 2. Unreachable Supervisor fails closed with 409 embedded_supervisor_status_unavailable.
	supervisorMode.Store("unreachable")
	resUnreachable := postConnectExternal(t, app, strings.Repeat("2", 32))
	blocker = freshStrategyActionBlocker{}
	if resUnreachable.Code != http.StatusConflict || json.Unmarshal(resUnreachable.Body.Bytes(), &blocker) != nil || blocker.Code != "embedded_supervisor_status_unavailable" {
		t.Fatalf("ConnectExternal on validated checkpoint with unreachable Supervisor = %d %s, want 409 embedded_supervisor_status_unavailable", resUnreachable.Code, resUnreachable.Body.String())
	}

	// 3. Healthy Supervisor on generation 1 is still fenced by unresolved start ledger entry.
	supervisorMode.Store("healthy")
	resLedger := postConnectExternal(t, app, strings.Repeat("3", 32))
	blocker = freshStrategyActionBlocker{}
	if resLedger.Code != http.StatusConflict || json.Unmarshal(resLedger.Body.Bytes(), &blocker) != nil || blocker.Code != "embedded_runtime_start_unresolved" {
		t.Fatalf("ConnectExternal on validated checkpoint with unresolved ledger = %d %s, want 409 embedded_runtime_start_unresolved", resLedger.Code, resLedger.Body.String())
	}
	checkStillValidated, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if checkStillValidated.Operation.Strategy.Stage != "validated" {
		t.Fatalf("strategy stage changed despite 409 blockers: %+v", checkStillValidated.Operation.Strategy)
	}

	// 4. Healthy Supervisor on generation 2 (offline + disarmed) commits ConnectExternal with 200 OK.
	supervisorGen.Store(2)
	resOK := postConnectExternal(t, app, strings.Repeat("4", 32))
	if resOK.Code != http.StatusOK {
		t.Fatalf("ConnectExternal on validated checkpoint with healthy Supervisor = %d %s, want 200", resOK.Code, resOK.Body.String())
	}
	afterCommit, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if afterCommit.Operation.Strategy.Strategy != bootstrap.ConnectExternal || afterCommit.Operation.Strategy.Stage != "committed" {
		t.Fatalf("committed strategy = %+v, want committed connect_external", afterCommit.Operation.Strategy)
	}
}

// TestRuntimeOperationDoubleWriteFailureNeverReplaysFakeAcceptedOnSameRequestIDRetry
// verifies P2-1: when reserving an operation succeeds, writing the write-ahead
// start ledger fails, AND finalizing/aborting the journal row also fails, the
// durable row remains in a pre-dispatch reserved state rather than "accepted".
// After SQLite recovers, retrying POST /control/api/v2/runtime/operations with
// the exact same requestId does not replay a phantom HTTP 200 "accepted" result;
// instead it executes the operation cleanly once and Supervisor never received
// the first failed request.
func TestRuntimeOperationDoubleWriteFailureNeverReplaysFakeAcceptedOnSameRequestIDRetry(t *testing.T) {
	const (
		productionArtifactID = "sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"
		sameRequestID        = "77777777-7777-4777-8777-777777777777"
	)

	var (
		mutatingHits atomic.Int32
		startHits    atomic.Int32
		curState     atomic.Value
	)
	curState.Store("offline")

	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutatingHits.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/runtime/status":
			st := curState.Load().(string)
			_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"` + st + `","cpaObservedVersion":"v8.0.55","capabilities":["start","stop"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + productionArtifactID + `","version":"v8.0.55"},"recovery":{"state":"inactive","attemptsRemaining":0}}`))
		case "/v1/runtime/operations/start":
			startHits.Add(1)
			var payload struct {
				OperationID string `json:"operationId"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			curState.Store("ready")
			_, _ = w.Write([]byte(`{"operationId":"` + payload.OperationID + `","operationType":"start","runtimeIdentity":"runtime-01","runtimeGeneration":1,"state":"succeeded"}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(supervisor.Close)

	tokenPath := filepath.Join(t.TempDir(), "runtime-token")
	if err := os.WriteFile(tokenPath, []byte("double-failure-token"), 0o600); err != nil {
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
	protected, err := opened.Protector.ProtectString(strings.Repeat("e", 64))
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
			Strategy:                      bootstrap.NewManaged,
			Stage:                         "committed",
			TargetIdentity:                "runtime-01",
			TargetGeneration:              1,
			TargetArtifactID:              productionArtifactID,
			DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity("http://127.0.0.1:8317"),
		}
		r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "http://127.0.0.1:8317", ManagementKey: protected}
		r.Operation.ProductComplete = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Allow initial journal reservation INSERT on fresh_runtime_operations_v1,
	// fail the write-ahead ledger write on fresh_embedded_start_ledger_v1, AND
	// fail the subsequent finalize/abort UPDATE on fresh_runtime_operations_v1.
	db := app.runtimeJournal.db
	if _, err := db.ExecContext(t.Context(), `
		CREATE TRIGGER test_fail_double_ledger_insert
		BEFORE INSERT ON settings
		WHEN NEW.key = 'fresh_embedded_start_ledger_v1'
		BEGIN
			SELECT RAISE(ABORT, 'simulated start ledger write failure');
		END;
		CREATE TRIGGER test_fail_double_ledger_update
		BEFORE UPDATE ON settings
		WHEN NEW.key = 'fresh_embedded_start_ledger_v1'
		BEGIN
			SELECT RAISE(ABORT, 'simulated start ledger write failure');
		END;
		CREATE TRIGGER test_fail_journal_finalize_update
		BEFORE UPDATE ON settings
		WHEN NEW.key = 'fresh_runtime_operations_v1'
		BEGIN
			SELECT RAISE(ABORT, 'simulated journal finalize write failure');
		END;
	`); err != nil {
		t.Fatalf("install double-failure triggers: %v", err)
	}

	postSameStart := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(
			http.MethodPost,
			"/control/api/v2/runtime/operations",
			strings.NewReader(`{"action":"start","requestId":"`+sameRequestID+`"}`),
		)
		req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		app.ServeHTTP(out, req)
		return out
	}

	// First attempt: reserve succeeded, ledger failed, finalize/abort also failed -> 503, zero RPC, no DesiredRunning.
	failRes := postSameStart()
	if failRes.Code != http.StatusServiceUnavailable {
		t.Fatalf("first attempt under double write failure = %d %s, want 503", failRes.Code, failRes.Body.String())
	}
	if got := mutatingHits.Load(); got != 0 {
		t.Fatalf("Supervisor mutating RPCs on first failed attempt = %d, want 0", got)
	}
	if desired, found, err := app.runtimeSettings.LoadEmbeddedRuntimeDesiredState(t.Context()); err != nil || found {
		t.Fatalf("DesiredRunning persisted despite double write failure: (%+v, %v, %v)", desired, found, err)
	}
	lookupReq := httptest.NewRequest(http.MethodGet, "/control/api/v2/runtime/operations/"+sameRequestID, nil)
	lookupReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	lookupRes := httptest.NewRecorder()
	app.ServeHTTP(lookupRes, lookupReq)
	if lookupRes.Code != http.StatusNotFound {
		t.Fatalf("GET runtime/operations/%s before retry = %d %s, want 404 (not phantom accepted)", sameRequestID, lookupRes.Code, lookupRes.Body.String())
	}

	// Recover SQLite and retry with the EXACT SAME requestId: must re-execute and succeed, NOT replay "accepted".
	if _, err := db.ExecContext(t.Context(), `
		DROP TRIGGER IF EXISTS test_fail_double_ledger_insert;
		DROP TRIGGER IF EXISTS test_fail_double_ledger_update;
		DROP TRIGGER IF EXISTS test_fail_journal_finalize_update;
	`); err != nil {
		t.Fatalf("drop double-failure triggers: %v", err)
	}
	retryRes := postSameStart()
	if retryRes.Code != http.StatusOK || !strings.Contains(retryRes.Body.String(), `"state":"succeeded"`) {
		t.Fatalf("same-requestId retry after store recovery = %d %s, want 200 state=succeeded (not phantom accepted)", retryRes.Code, retryRes.Body.String())
	}
	if got := startHits.Load(); got != 1 {
		t.Fatalf("Supervisor Start RPCs across both attempts = %d, want 1 (only from retry)", got)
	}
}

// TestRuntimeOperationPromotionAndLedgerCleanupFailureKeepsExternalFencedUntilGenerationChanges
// verifies R6 P3-1: when the initial write-ahead ledger INSERT succeeds, the
// subsequent journal promotion UPDATE (reserved -> accepted) fails, and the
// rollback ledger cleanup write also fails, POST /control/api/v2/runtime/operations
// fails closed with 503 and zero Supervisor mutating RPCs while the durable
// unresolved ledger row continues fencing POST /control/api/v2/strategy
// (connect_external) with 409 embedded_runtime_start_unresolved both before and
// after SQLite triggers recover, releasing only after Supervisor runtimeGeneration
// advances with offline + disarmed recovery.
func TestRuntimeOperationPromotionAndLedgerCleanupFailureKeepsExternalFencedUntilGenerationChanges(t *testing.T) {
	const (
		productionArtifactID = "sha256:e48c85c75a19adae186cbee590f6e6143fd8085417d8066b545e8533877175f7"
		management           = externalAuthorityManagement
		requestID            = "88888888-8888-4888-8888-888888888888"
	)

	externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+management {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.55")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v8/management/config":
			_, _ = w.Write([]byte(`{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"/data/auth"},"access":{"api-keys":["ext-key-1"]}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(externalServer.Close)

	var (
		supervisorGen atomic.Uint64
		mutatingHits  atomic.Int32
	)
	supervisorGen.Store(1)

	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		gen := strconv.FormatUint(supervisorGen.Load(), 10)
		switch r.URL.Path {
		case "/v1/runtime/status":
			_, _ = w.Write([]byte(`{"protocolVersion":"v1","runtimeIdentity":"runtime-01","runtimeGeneration":` + gen + `,"state":"offline","cpaObservedVersion":"v8.0.55","capabilities":["start","stop","restart"],"activeGatewayArtifact":{"engine":"cpa","artifactId":"` + productionArtifactID + `","version":"v8.0.55"},"recovery":{"state":"inactive","attemptsRemaining":0}}`))
		case "/v1/runtime/operations/start", "/v1/runtime/operations/stop", "/v1/runtime/operations/restart":
			mutatingHits.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(supervisor.Close)

	tokenPath := filepath.Join(t.TempDir(), "runtime-token")
	if err := os.WriteFile(tokenPath, []byte("promo-cleanup-token"), 0o600); err != nil {
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
	if app.pluginCapability != nil {
		app.pluginCapability.launch = func(refresh func()) { refresh() }
	}
	app.strategy = freshstrategy.New(freshstrategy.Options{
		Protector:     opened.Protector,
		Qualification: bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
	})

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
	db := app.runtimeJournal.db
	var uncommittedOpJSON string
	if err := db.QueryRowContext(t.Context(), `SELECT value FROM settings WHERE key = ?`, freshstore.OperationKey).Scan(&uncommittedOpJSON); err != nil {
		t.Fatalf("snapshot uncommitted operation row: %v", err)
	}

	protected, err := opened.Protector.ProtectString(strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Update(t.Context(), rec.Operation.Identity, rec.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy:                      bootstrap.NewManaged,
			Stage:                         "committed",
			TargetIdentity:                "runtime-01",
			TargetGeneration:              1,
			TargetArtifactID:              productionArtifactID,
			DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity("http://127.0.0.1:8317"),
		}
		r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: "http://127.0.0.1:8317", ManagementKey: protected}
		r.Operation.ProductComplete = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Allow the initial journal reservation INSERT and the first write-ahead
	// ledger INSERT, reject the journal promotion UPDATE (reserved -> accepted),
	// and reject the subsequent ledger cleanup UPDATE/DELETE.
	if _, err := db.ExecContext(t.Context(), `
		CREATE TRIGGER test_fail_journal_promotion_update
		BEFORE UPDATE ON settings
		WHEN NEW.key = 'fresh_runtime_operations_v1'
		BEGIN
			SELECT RAISE(ABORT, 'simulated journal promotion write failure');
		END;
		CREATE TRIGGER test_fail_ledger_cleanup_update
		BEFORE UPDATE ON settings
		WHEN NEW.key = 'fresh_embedded_start_ledger_v1'
		BEGIN
			SELECT RAISE(ABORT, 'simulated start ledger cleanup update failure');
		END;
		CREATE TRIGGER test_fail_ledger_cleanup_delete
		BEFORE DELETE ON settings
		WHEN OLD.key = 'fresh_embedded_start_ledger_v1'
		BEGIN
			SELECT RAISE(ABORT, 'simulated start ledger cleanup delete failure');
		END;
	`); err != nil {
		t.Fatalf("install promotion+cleanup failure triggers: %v", err)
	}

	startReq := httptest.NewRequest(
		http.MethodPost,
		"/control/api/v2/runtime/operations",
		strings.NewReader(`{"action":"start","requestId":"`+requestID+`"}`),
	)
	startReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	startReq.Header.Set("Content-Type", "application/json")
	startRes := httptest.NewRecorder()
	app.ServeHTTP(startRes, startReq)
	if startRes.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST runtime/operations under promotion+cleanup failure = %d %s, want 503", startRes.Code, startRes.Body.String())
	}
	if got := mutatingHits.Load(); got != 0 {
		t.Fatalf("Supervisor mutating RPCs under promotion+cleanup failure = %d, want 0", got)
	}
	if desired, found, err := app.runtimeSettings.LoadEmbeddedRuntimeDesiredState(t.Context()); err != nil || found {
		t.Fatalf("DesiredRunning persisted despite promotion+cleanup failure: (%+v, %v, %v)", desired, found, err)
	}
	lookupReq := httptest.NewRequest(http.MethodGet, "/control/api/v2/runtime/operations/"+requestID, nil)
	lookupReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	lookupRes := httptest.NewRecorder()
	app.ServeHTTP(lookupRes, lookupReq)
	if lookupRes.Code != http.StatusNotFound {
		t.Fatalf("GET runtime/operations/%s = %d %s, want 404", requestID, lookupRes.Code, lookupRes.Body.String())
	}

	// POST /runtime/operations requires ProductComplete == true, whereas POST /strategy
	// (ConnectExternal) requires an uncommitted Fresh operation (fresh.go:814). Restore
	// the uncommitted Fresh operation rows on the same SQLite DB while keeping all
	// triggers, fresh_embedded_start_ledger_v1, and fresh_runtime_operations_v1 intact.
	if _, err := db.ExecContext(t.Context(), `UPDATE settings SET value = ? WHERE key = ?`, uncommittedOpJSON, freshstore.OperationKey); err != nil {
		t.Fatalf("restore uncommitted operation row: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `DELETE FROM settings WHERE key IN ('manager_config_v1', 'setup')`); err != nil {
		t.Fatalf("clear completed connection rows: %v", err)
	}

	postExternal := func(attemptID string) *httptest.ResponseRecorder {
		cur, loadErr := app.store.Load(t.Context())
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		body, marshalErr := json.Marshal(struct {
			Identity         bootstrap.FreshIdentity `json:"identity"`
			ExpectedRevision uint64                  `json:"expectedRevision"`
			AttemptID        string                  `json:"attemptId"`
			freshstrategy.Request
		}{
			Identity:         cur.Operation.Identity,
			ExpectedRevision: cur.Operation.Revision,
			AttemptID:        attemptID,
			Request: freshstrategy.Request{
				Strategy:      bootstrap.ConnectExternal,
				BaseURL:       externalServer.URL,
				ManagementKey: management,
			},
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		req := httptest.NewRequest(http.MethodPost, "/control/api/v2/strategy", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		app.ServeHTTP(out, req)
		return out
	}

	// 1. Same Supervisor generation (1) with triggers still active: fenced with 409 embedded_runtime_start_unresolved.
	res1 := postExternal(strings.Repeat("a", 32))
	var blocker freshStrategyActionBlocker
	if res1.Code != http.StatusConflict || json.Unmarshal(res1.Body.Bytes(), &blocker) != nil || blocker.Code != "embedded_runtime_start_unresolved" {
		t.Fatalf("ConnectExternal in generation 1 with triggers active = %d %s, want 409 embedded_runtime_start_unresolved", res1.Code, res1.Body.String())
	}

	// 2. Drop triggers (SQLite recovers), still generation 1: fence remains active.
	if _, err := db.ExecContext(t.Context(), `
		DROP TRIGGER IF EXISTS test_fail_journal_promotion_update;
		DROP TRIGGER IF EXISTS test_fail_ledger_cleanup_update;
		DROP TRIGGER IF EXISTS test_fail_ledger_cleanup_delete;
	`); err != nil {
		t.Fatalf("drop promotion+cleanup triggers: %v", err)
	}
	res2 := postExternal(strings.Repeat("b", 32))
	blocker = freshStrategyActionBlocker{}
	if res2.Code != http.StatusConflict || json.Unmarshal(res2.Body.Bytes(), &blocker) != nil || blocker.Code != "embedded_runtime_start_unresolved" {
		t.Fatalf("ConnectExternal in generation 1 after dropping triggers = %d %s, want 409 embedded_runtime_start_unresolved", res2.Code, res2.Body.String())
	}

	// 3. Supervisor restarts onto generation 2 (offline + disarmed): ConnectExternal commits.
	supervisorGen.Store(2)
	res3 := postExternal(strings.Repeat("c", 32))
	if res3.Code != http.StatusOK || !strings.Contains(res3.Body.String(), `"stage":"committed"`) {
		t.Fatalf("ConnectExternal after Supervisor generation change = %d %s, want 200 committed", res3.Code, res3.Body.String())
	}
}

// TestUncommittedValidatedExternalCheckpointDoesNotActivateExternalSurface
// verifies that an uncommitted {Strategy: ConnectExternal, Stage: "validated"}
// checkpoint does not activate committed-External legacy write replacement,
// gateway plugin-scope External classification, or runtimeExternal fallback to
// settings.LoadManagerConfig.
func TestUncommittedValidatedExternalCheckpointDoesNotActivateExternalSurface(t *testing.T) {
	var externalHits atomic.Int32
	externalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		externalHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config":{}}`))
	}))
	defer externalServer.Close()

	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "runtime.token")
	if err := os.WriteFile(tokenPath, []byte("sup-token-uncommitted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/runtime/status" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"stopped","observedAt":"2026-04-12T00:00:00Z","supervisorGeneration":1}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer supervisor.Close()

	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = opened.Close() }()

	cfg := config.Config{
		RuntimeURL:       supervisor.URL,
		RuntimeTokenFile: tokenPath,
		CPAUpstreamURL:   "http://127.0.0.1:18398",
	}
	app := newFreshApplication(opened, cfg, nil)

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
			Strategy:         bootstrap.ConnectExternal,
			TargetIdentity:   freshkeyapply.ExternalIdentity(externalServer.URL),
			TargetGeneration: 1,
			Stage:            "validated",
		}
		r.Connection = &model.ManagerCPAConnectionConfig{
			CPABaseURL:    externalServer.URL,
			ManagementKey: protected,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// 1. Legacy management writes must not claim committed-External replacement
	// while the checkpoint is still uncommitted (Stage == "validated").
	putReq := httptest.NewRequest(http.MethodPut, "/v0/management/api-keys", strings.NewReader(`{}`))
	putReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	putReq.Header.Set("Content-Type", "application/json")
	putRes := httptest.NewRecorder()
	app.legacy.ServeHTTP(putRes, putReq)
	if putRes.Code == http.StatusConflict && strings.Contains(putRes.Body.String(), externalLegacyWriteReplaced) {
		t.Errorf("uncommitted validated External returned committed-External replacement: %d %s", putRes.Code, putRes.Body.String())
	}

	// 2. Gateway plugin scope must report unavailable rather than
	// external/plugin_managed_only on an uncommitted validated checkpoint.
	pluginView, pluginErr := app.gatewayConfig.ReadPlugins(t.Context())
	if pluginErr != nil {
		t.Fatalf("ReadPlugins on uncommitted validated External returned error: %v", pluginErr)
	}
	if pluginView.Capability.Mode == freshconfig.ModeExternal || pluginView.Capability.Reason == freshconfig.ReasonPluginManagedOnly {
		t.Errorf("ReadPlugins on uncommitted validated External = %+v, want non-External unavailable", pluginView.Capability)
	}

	// 3. runtimeExternal must resolve through externalSource (failing closed on
	// uncommitted checkpoint) rather than falling back to settings.LoadManagerConfig.
	if status, statusErr := app.runtimeExternal.Status(t.Context()); statusErr == nil {
		t.Errorf("runtimeExternal.Status on uncommitted validated External succeeded: %+v", status)
	}
	if hits := externalHits.Load(); hits != 0 {
		t.Errorf("uncommitted validated External contacted External CPA %d times, want 0", hits)
	}
}
