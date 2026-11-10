package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

// TestRuntimeConfiguredInstallSelectsProfileFromCommittedStrategy covers the
// official single-image environment where cpamp-launcher always injects
// CPAMP_RUNTIME_URL and CPAMP_RUNTIME_TOKEN_FILE before the wizard chooses a
// strategy. Both in-process Connect External completion and post-restart
// startup must select ProfileExternal, while New Managed keeps ProfileManaged.
func TestRuntimeConfiguredInstallSelectsProfileFromCommittedStrategy(t *testing.T) {
	t.Run("connect_external_in_process_and_restart", func(t *testing.T) {
		const management = externalAuthorityManagement
		var routingStrategy atomic.Value
		routingStrategy.Store("round-robin")
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

		// 2. Wizard commits Connect External and completes installation in-process.
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

		assertExternalSurfaces := func(label string, target *freshApplication) {
			t.Helper()
			call := func(method, resource, body string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(method, "/control/api/v2/"+resource, bytes.NewBufferString(body))
				req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
				if body != "" {
					req.Header.Set("Content-Type", "application/json")
				}
				rec := httptest.NewRecorder()
				target.canonicalAPI(rec, req)
				return rec
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
		assertExternalSurfaces("in-process", app)

		// 3. Container restarts on the committed Connect External database with runtime env still set.
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
		assertExternalSurfaces("restarted", restarted)
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
}
