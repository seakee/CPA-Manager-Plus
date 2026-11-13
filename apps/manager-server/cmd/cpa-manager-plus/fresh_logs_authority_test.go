package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
)

type externalLogsGateFakeCPA struct {
	server      *httptest.Server
	management  atomic.Value
	version     atomic.Value
	probeStatus atomic.Int32
	mu          sync.Mutex
	calls       []string
	putCount    atomic.Int32
	probeCount  atomic.Int32
}

func newExternalLogsGateFakeCPA(t *testing.T, management, version string) *externalLogsGateFakeCPA {
	t.Helper()
	fake := &externalLogsGateFakeCPA{}
	fake.management.Store(management)
	fake.version.Store(version)
	fake.probeStatus.Store(http.StatusOK)
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.calls = append(fake.calls, r.Method+" "+r.URL.Path)
		fake.mu.Unlock()
		if r.URL.Path == "/v0/management/config" {
			fake.probeCount.Add(1)
			status := int(fake.probeStatus.Load())
			if status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
		}
		if r.Method == http.MethodPut {
			fake.putCount.Add(1)
		}
		if r.Header.Get("Authorization") != "Bearer "+fake.management.Load().(string) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if ver := fake.version.Load().(string); ver != "" {
			w.Header().Set("X-CPA-Version", ver)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v0/management/config", "/v8/management/config":
			_, _ = w.Write([]byte(`{"logging-to-file":false,"debug":false,"request-log":false}`))
		case "/v0/management/logs-max-total-size-mb":
			_, _ = w.Write([]byte(`{"logs-max-total-size-mb":64}`))
		case "/v0/management/error-logs-max-files":
			_, _ = w.Write([]byte(`{"error-logs-max-files":10}`))
		default:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func externalLogsGateApp(t *testing.T, version string) (*freshApplication, *externalLogsGateFakeCPA) {
	t.Helper()
	fake := newExternalLogsGateFakeCPA(t, externalAuthorityManagement, version)
	app := newExternalApp(t, fake.server.URL, externalAuthorityManagement)
	app.gatewayConfig = gatewayFor(app)
	return app, fake
}

var bridgedLogSettingWrites = []struct {
	name     string
	resource string
	body     string
}{
	{"logging-to-file", "cpamp/v0/management/logging-to-file", `{"value":true}`},
	{"debug", "cpamp/v0/management/debug", `{"value":true}`},
	{"request-log", "cpamp/v0/management/request-log", `{"value":true}`},
	{"logs-max-total-size-mb", "cpamp/v0/management/logs-max-total-size-mb", `{"value":128}`},
	{"error-logs-max-files", "cpamp/v0/management/error-logs-max-files", `{"value":15}`},
}

func TestExternalLogsBridgeGateBlocksUnsupportedCPAVersions(t *testing.T) {
	for _, version := range []string{"v7.2.130", "", "latest"} {
		t.Run("version "+version, func(t *testing.T) {
			app, fake := externalLogsGateApp(t, version)

			// Every bridged log-setting PUT must fail closed with 409
			// external_cpa_version_unsupported and forward zero PUTs to the user's CPA.
			for _, tc := range bridgedLogSettingWrites {
				beforePuts := fake.putCount.Load()
				putResp := gatewayRequest(t, app, http.MethodPut, tc.resource, tc.body, "application/json")
				if putResp.Code != http.StatusConflict {
					t.Fatalf("PUT %s status=%d body=%s", tc.resource, putResp.Code, putResp.Body.String())
				}
				if code := errorCode(t, putResp); code != freshconfig.ReasonVersionUnsupported {
					t.Fatalf("PUT %s code=%q, want %q", tc.resource, code, freshconfig.ReasonVersionUnsupported)
				}
				if forwarded := fake.putCount.Load() - beforePuts; forwarded != 0 {
					t.Fatalf("PUT %s forwarded %d PUT requests to unsupported CPA", tc.resource, forwarded)
				}
			}
		})
	}
}

func TestExternalLogsCapabilityAndBridgeGateUnsupportedCPAVersions(t *testing.T) {
	for _, version := range []string{"v7.2.130", "", "latest"} {
		t.Run("version "+version, func(t *testing.T) {
			app, _ := externalLogsGateApp(t, version)

			// Generic gateway/config still reads /v8/management/config if present,
			// so the dedicated logs-capability route must not inherit a false-positive
			// writable=true on unsupported CPA versions.
			capResp := gatewayRequest(t, app, http.MethodGet, "gateway/config/logs-capability", "", "")
			if capResp.Code != http.StatusOK {
				t.Fatalf("logs-capability status=%d body=%s", capResp.Code, capResp.Body.String())
			}
			var capBody struct {
				Capability freshconfig.Capability `json:"capability"`
			}
			if err := json.Unmarshal(capResp.Body.Bytes(), &capBody); err != nil {
				t.Fatal(err)
			}
			if capBody.Capability.Mode != freshconfig.ModeExternal ||
				capBody.Capability.Writable ||
				capBody.Capability.Reason != freshconfig.ReasonVersionUnsupported {
				t.Fatalf("logs-capability = %+v, want external non-writable %q", capBody.Capability, freshconfig.ReasonVersionUnsupported)
			}
			// Compatibility GET reads for log retention settings remain readable.
			for _, getRes := range []string{
				"cpamp/v0/management/logs-max-total-size-mb",
				"cpamp/v0/management/error-logs-max-files",
			} {
				getResp := gatewayRequest(t, app, http.MethodGet, getRes, "", "")
				if getResp.Code != http.StatusOK {
					t.Fatalf("GET %s status=%d body=%s", getRes, getResp.Code, getResp.Body.String())
				}
			}
		})
	}
}

func TestExternalLogsCapabilityAndBridgeAllowSupportedV8CPA(t *testing.T) {
	app, fake := externalLogsGateApp(t, "v8.0.13")

	capResp := gatewayRequest(t, app, http.MethodGet, "gateway/config/logs-capability", "", "")
	if capResp.Code != http.StatusOK {
		t.Fatalf("logs-capability status=%d body=%s", capResp.Code, capResp.Body.String())
	}
	var capBody struct {
		Capability freshconfig.Capability `json:"capability"`
	}
	if err := json.Unmarshal(capResp.Body.Bytes(), &capBody); err != nil {
		t.Fatal(err)
	}
	if capBody.Capability.Mode != freshconfig.ModeExternal || !capBody.Capability.Writable {
		t.Fatalf("logs-capability = %+v, want external writable=true", capBody.Capability)
	}

	for _, tc := range bridgedLogSettingWrites {
		beforePuts := fake.putCount.Load()
		putResp := gatewayRequest(t, app, http.MethodPut, tc.resource, tc.body, "application/json")
		if putResp.Code != http.StatusOK {
			t.Fatalf("PUT %s status=%d body=%s", tc.resource, putResp.Code, putResp.Body.String())
		}
		if forwarded := fake.putCount.Load() - beforePuts; forwarded != 1 {
			t.Fatalf("PUT %s forwarded %d requests, want 1", tc.resource, forwarded)
		}
	}
}

func TestExternalLogsBridgeAndCapabilityFailClosedOnRefusedCredentialMovedTargetAndUnavailableProbe(t *testing.T) {
	t.Run("rejected management key on capability and bridge PUTs", func(t *testing.T) {
		app, fake := externalLogsGateApp(t, "v8.0.13")
		fake.management.Store(strings.Repeat("f", 64))

		capResp := gatewayRequest(t, app, http.MethodGet, "gateway/config/logs-capability", "", "")
		if capResp.Code != http.StatusConflict || errorCode(t, capResp) != freshconfig.ReasonManagementRejected {
			t.Fatalf("capability on 401 = %d %s", capResp.Code, capResp.Body.String())
		}
		if !app.externalReadGrant(t.Context()).Refused {
			t.Fatal("capability 401 did not record external refusal")
		}
		probesBefore := fake.probeCount.Load()
		putsBefore := fake.putCount.Load()
		for _, tc := range bridgedLogSettingWrites {
			putResp := gatewayRequest(t, app, http.MethodPut, tc.resource, tc.body, "application/json")
			if putResp.Code != http.StatusConflict || errorCode(t, putResp) != freshconfig.ReasonManagementRejected {
				t.Fatalf("PUT %s after rejection = %d %s", tc.resource, putResp.Code, putResp.Body.String())
			}
		}
		if fake.probeCount.Load() != probesBefore || fake.putCount.Load() != putsBefore {
			t.Fatal("refused credential contacted External CPA again")
		}
	})

	t.Run("401 discovered during bridge PUT records refusal and blocks subsequent writes", func(t *testing.T) {
		app, fake := externalLogsGateApp(t, "v8.0.13")
		fake.management.Store(strings.Repeat("e", 64))

		first := gatewayRequest(t, app, http.MethodPut, bridgedLogSettingWrites[0].resource, bridgedLogSettingWrites[0].body, "application/json")
		if first.Code != http.StatusConflict || errorCode(t, first) != freshconfig.ReasonManagementRejected {
			t.Fatalf("first PUT on 401 = %d %s", first.Code, first.Body.String())
		}
		if fake.putCount.Load() != 0 {
			t.Fatalf("refused probe still forwarded %d PUTs", fake.putCount.Load())
		}
		if !app.externalReadGrant(t.Context()).Refused {
			t.Fatal("bridge PUT probe 401 did not record refusal")
		}

		probesBefore := fake.probeCount.Load()
		second := gatewayRequest(t, app, http.MethodPut, bridgedLogSettingWrites[1].resource, bridgedLogSettingWrites[1].body, "application/json")
		if second.Code != http.StatusConflict || errorCode(t, second) != freshconfig.ReasonManagementRejected {
			t.Fatalf("second PUT = %d %s", second.Code, second.Body.String())
		}
		if fake.probeCount.Load() != probesBefore || fake.putCount.Load() != 0 {
			t.Fatal("recorded refusal did not short-circuit second PUT")
		}
	})

	t.Run("403 forbidden during bridge PUT surfaces forbidden detail and stops forwarding", func(t *testing.T) {
		app, fake := externalLogsGateApp(t, "v8.0.13")
		fake.probeStatus.Store(http.StatusForbidden)

		resp := gatewayRequest(t, app, http.MethodPut, bridgedLogSettingWrites[0].resource, bridgedLogSettingWrites[0].body, "application/json")
		if resp.Code != http.StatusConflict || errorCode(t, resp) != freshconfig.ReasonManagementRejected {
			t.Fatalf("PUT on 403 = %d %s", resp.Code, resp.Body.String())
		}
		if fake.putCount.Load() != 0 {
			t.Fatalf("403 probe forwarded %d PUTs", fake.putCount.Load())
		}
		var payload struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Detail != externalRebindForbiddenDetail {
			t.Fatalf("detail = %q, want %q", payload.Detail, externalRebindForbiddenDetail)
		}
	})

	t.Run("moved target fails closed with zero CPA contact", func(t *testing.T) {
		app, fake := externalLogsGateApp(t, "v8.0.13")
		record, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Strategy.TargetIdentity = "external:sha256:" + strings.Repeat("c", 64)
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		capResp := gatewayRequest(t, app, http.MethodGet, "gateway/config/logs-capability", "", "")
		if capResp.Code != http.StatusConflict || errorCode(t, capResp) != freshconfig.ReasonTargetMoved {
			t.Fatalf("capability on moved target = %d %s", capResp.Code, capResp.Body.String())
		}
		for _, tc := range bridgedLogSettingWrites {
			putResp := gatewayRequest(t, app, http.MethodPut, tc.resource, tc.body, "application/json")
			if putResp.Code != http.StatusConflict || errorCode(t, putResp) != freshconfig.ReasonTargetMoved {
				t.Fatalf("PUT %s on moved target = %d %s", tc.resource, putResp.Code, putResp.Body.String())
			}
		}
		if fake.probeCount.Load() != 0 || fake.putCount.Load() != 0 {
			t.Fatalf("moved target contacted CPA: probes=%d puts=%d", fake.probeCount.Load(), fake.putCount.Load())
		}
	})

	t.Run("unreachable or failing probe fails closed without forwarding PUTs", func(t *testing.T) {
		app, fake := externalLogsGateApp(t, "v8.0.13")
		fake.probeStatus.Store(http.StatusBadGateway)

		capResp := gatewayRequest(t, app, http.MethodGet, "gateway/config/logs-capability", "", "")
		if capResp.Code != http.StatusOK {
			t.Fatalf("capability on 502 = %d %s", capResp.Code, capResp.Body.String())
		}
		var capBody struct {
			Capability freshconfig.Capability `json:"capability"`
		}
		if err := json.Unmarshal(capResp.Body.Bytes(), &capBody); err != nil {
			t.Fatal(err)
		}
		if capBody.Capability.Writable || capBody.Capability.Reason != freshconfig.ReasonUnavailable {
			t.Fatalf("capability on 502 = %+v, want non-writable %q", capBody.Capability, freshconfig.ReasonUnavailable)
		}

		for _, tc := range bridgedLogSettingWrites {
			putResp := gatewayRequest(t, app, http.MethodPut, tc.resource, tc.body, "application/json")
			if putResp.Code != http.StatusServiceUnavailable {
				t.Fatalf("PUT %s on 502 probe = %d %s", tc.resource, putResp.Code, putResp.Body.String())
			}
		}
		if fake.putCount.Load() != 0 {
			t.Fatalf("failed probe still forwarded %d PUTs", fake.putCount.Load())
		}
	})
}

func TestLogsCapabilityCoversManagedPrivateAndMethodGuard(t *testing.T) {
	t.Run("method guard enforces GET only", func(t *testing.T) {
		app, _ := externalLogsGateApp(t, "v8.0.13")
		resp := gatewayRequest(t, app, http.MethodPost, "gateway/config/logs-capability", `{}`, "application/json")
		if resp.Code != http.StatusMethodNotAllowed || resp.Header().Get("Allow") != http.MethodGet {
			t.Fatalf("POST logs-capability = %d allow=%q", resp.Code, resp.Header().Get("Allow"))
		}
	})

	t.Run("private profile stays read-only", func(t *testing.T) {
		app := newInstallationTestApplication(t)
		markFreshProductComplete(t, app)
		record, err := app.store.Load(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
				Strategy: bootstrap.NewManaged, Stage: "committed",
				TargetIdentity: cpa.PrivateKeyRuntime, TargetGeneration: 7, TargetArtifactID: cpa.PrivateKeyArtifact,
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		app.gatewayConfig = freshconfig.New(app.store, func(context.Context, uint64) (string, error) {
			return "management-key", nil
		}, freshkeyapply.New(nil, nil, nil).TargetsFunc(freshkeyapply.SurfaceGatewayConfig))

		resp := gatewayRequest(t, app, http.MethodGet, "gateway/config/logs-capability", "", "")
		if resp.Code != http.StatusOK {
			t.Fatalf("private logs-capability = %d %s", resp.Code, resp.Body.String())
		}
		var body struct {
			Capability freshconfig.Capability `json:"capability"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Capability.Mode != freshconfig.ModePrivate || body.Capability.Writable || body.Capability.Reason != freshconfig.ReasonPrivateFrozen {
			t.Fatalf("private capability = %+v", body.Capability)
		}
	})

	t.Run("managed and adopt profiles stay writable", func(t *testing.T) {
		adoptInstall := func(r *freshstore.Record, env managedInstallEnv) {
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
				AdoptionResources:             []bootstrap.ResourceRef{bootstrap.AdoptionRefClientAPIKeys},
				AdoptionDecisions: []bootstrap.AdoptionResourceDecision{
					{
						ResourceRef:       bootstrap.AdoptionRefClientAPIKeys,
						Kind:              bootstrap.APIKeys,
						Scope:             bootstrap.ClientAPIKeys,
						ReadEvidenceRef:   "preflight:keys-read",
						ReplayEvidenceRef: "preflight:keys-replay",
						VerifyEvidenceRef: "preflight:keys-verify",
						Qualification:     bootstrap.CapabilityQualified,
						Disposition:       bootstrap.Migrate,
					},
				},
			}
			r.AdoptionCiphertext = env.protected
			r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: env.endpoint, ManagementKey: env.protected}
			r.Operation.ProductComplete = true
		}

		for _, tc := range []struct {
			name  string
			setup managedInstallSetup
		}{
			{name: "new_managed", setup: completedManagedInstall},
			{name: "adopt_existing", setup: adoptInstall},
		} {
			t.Run(tc.name, func(t *testing.T) {
				harness := newManagedInstallHarnessWith(t, tc.setup, true)
				resp := gatewayRequest(t, harness.app, http.MethodGet, "gateway/config/logs-capability", "", "")
				if resp.Code != http.StatusOK {
					t.Fatalf("managed logs-capability = %d %s", resp.Code, resp.Body.String())
				}
				var body struct {
					Capability freshconfig.Capability `json:"capability"`
				}
				if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Capability.Mode != freshconfig.ModeManaged || !body.Capability.Writable {
					t.Fatalf("%s capability = %+v", tc.name, body.Capability)
				}
			})
		}
	})
}
