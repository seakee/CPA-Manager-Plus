package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
)

func TestFreshLegacyRouterDoesNotRequireEmbeddedIdentityWorker(t *testing.T) {
	app := newInstallationTestApplication(t)
	if app.legacy == nil {
		t.Fatal("External Console router was not installed")
	}
	if app.identityWorker != nil {
		t.Fatal("External Console retained an Embedded worker")
	}
	request := httptest.NewRequest(http.MethodGet, "/usage-service/info", nil)
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	app.legacy.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("existing router read returned %d: %s", response.Code, response.Body.String())
	}
}

func TestFreshLegacyWritesFailClosedWithoutDroppingReads(t *testing.T) {
	forwarded := 0
	handler := guardFreshLegacyWrites(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded++
		w.WriteHeader(http.StatusOK)
	}), errors.New("identity journal unavailable"))
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodPost, http.MethodDelete} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/v0/management/api-keys", nil))
		if response.Code != http.StatusServiceUnavailable || forwarded != 0 {
			t.Fatalf("%s bypassed failed mutation wiring: status=%d, forwards=%d", method, response.Code, forwarded)
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/usage-service/info", nil))
	if response.Code != http.StatusOK || forwarded != 1 {
		t.Fatalf("unrelated read unavailable: status=%d, forwards=%d", response.Code, forwarded)
	}
}

func TestFreshCanonicalLegacyBridgeKeepsQuerySeparateFromPath(t *testing.T) {
	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)
	forwarded := false
	app.legacy = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = true
		if r.URL.Path != "/v0/management/usage/import-sessions/resume-123/chunk" || r.URL.RawQuery != "offset=17" || r.URL.Query().Get("offset") != "17" {
			t.Errorf("bridge changed request target: %s", r.RequestURI)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPut, "/control/api/v2/cpamp/v0/management/usage/import-sessions/resume-123/chunk?offset=17", nil)
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	app.canonicalAPI(response, request)
	if response.Code != http.StatusNoContent || !forwarded {
		t.Fatalf("query-bearing Console request rejected: %d", response.Code)
	}
}

func markFreshProductComplete(t *testing.T, app *freshApplication) {
	t.Helper()
	installManagedCheckpoint(t, app, "committed")
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(record *freshstore.Record) error {
		record.Operation.ProductComplete = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFreshCanonicalResourcesReuseCPAMPServices(t *testing.T) {
	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)
	for _, resource := range []string{"cpamp/dashboard", "cpamp/config", "cpamp/status"} {
		t.Run(resource, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/control/api/v2/"+resource, nil)
			req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			response := httptest.NewRecorder()
			app.canonicalAPI(response, req)
			if response.Code != http.StatusOK {
				t.Fatalf("resource %s returned %d: %s", resource, response.Code, response.Body.String())
			}
			if !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("resource %s was not JSON: %s", resource, response.Header().Get("Content-Type"))
			}
		})
	}
}

func TestFreshCanonicalResourcesRemainAdminProtected(t *testing.T) {
	app := newInstallationTestApplication(t)
	for _, resource := range []string{"cpamp/dashboard", "cpamp/config", "cpamp/status"} {
		req := httptest.NewRequest(http.MethodGet, "/control/api/v2/"+resource, nil)
		response := httptest.NewRecorder()
		app.canonicalAPI(response, req)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("resource %s accepted missing auth: %d", resource, response.Code)
		}
	}
}

func TestFreshCanonicalLegacyBridgeReusesCompleteRouter(t *testing.T) {
	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)
	for _, resource := range []string{
		"cpamp/v0/management/config",
		"cpamp/usage-service/info",
		"cpamp/status",
	} {
		t.Run(resource, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/control/api/v2/"+resource, nil)
			req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			response := httptest.NewRecorder()
			app.canonicalAPI(response, req)
			if response.Code != http.StatusOK && response.Code != http.StatusPreconditionRequired {
				t.Fatalf("resource %s returned %d: %s", resource, response.Code, response.Body.String())
			}
			if response.Code == http.StatusOK && !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("resource %s was not JSON: %s", resource, response.Header().Get("Content-Type"))
			}
		})
	}
}

func TestFreshCanonicalLegacyBridgeRequiresProductCompletion(t *testing.T) {
	app := newInstallationTestApplication(t)
	for _, request := range []struct {
		method   string
		resource string
	}{
		{http.MethodGet, "cpamp/v0/management/config"},
		{http.MethodPut, "cpamp/usage-service/config"},
		{http.MethodPost, "cpamp/setup"},
	} {
		t.Run(request.method+" "+request.resource, func(t *testing.T) {
			req := httptest.NewRequest(request.method, "/control/api/v2/"+request.resource, nil)
			req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
			response := httptest.NewRecorder()
			app.canonicalAPI(response, req)
			if response.Code != http.StatusPreconditionRequired {
				t.Fatalf("resource %s returned %d, want %d", request.resource, response.Code, http.StatusPreconditionRequired)
			}
		})
	}
}

func TestFreshCanonicalLegacyBridgeRejectsUnknownOrEscapingPaths(t *testing.T) {
	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)
	for _, resource := range []string{
		"cpamp/unknown",
		"cpamp/v0/management/collector/restart",
		"cpamp/setup",
		"cpamp/v0/management/../status",
		"cpamp/v0/management/%2e%2e/status",
		"cpamp/internal/routing/fresh-authority",
	} {
		req := httptest.NewRequest(http.MethodGet, "/control/api/v2/"+resource, nil)
		req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		response := httptest.NewRecorder()
		app.canonicalAPI(response, req)
		if response.Code != http.StatusNotFound {
			t.Fatalf("resource %s returned %d, want 404", resource, response.Code)
		}
	}
	req := httptest.NewRequest(http.MethodDelete, "/control/api/v2/cpamp/v0/management/config", nil)
	req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	app.canonicalAPI(response, req)
	if response.Code != http.StatusNotFound {
		t.Fatalf("disallowed config method returned %d, want 404", response.Code)
	}
}

func TestLegacyBridgePathAllowlistCoversExistingReadAndAuthFlows(t *testing.T) {
	for _, test := range []struct {
		resource string
		method   string
		allowed  bool
	}{
		{resource: "v0/management/logs", method: http.MethodGet, allowed: true},
		{resource: "v0/management/logs", method: http.MethodDelete, allowed: true},
		{resource: "v0/management/request-error-logs/app.log", method: http.MethodGet, allowed: true},
		{resource: "v0/management/request-log-by-id/123", method: http.MethodGet, allowed: true},
		{resource: "v0/management/codex-auth-url", method: http.MethodGet, allowed: true},
		{resource: "v0/management/get-auth-status", method: http.MethodGet, allowed: true},
		{resource: "v0/management/oauth-callback", method: http.MethodPost, allowed: true},
		{resource: "usage-service/account-processing-policy", method: http.MethodGet, allowed: true},
		{resource: "usage-service/account-processing-policy", method: http.MethodPatch, allowed: true},
		{resource: "usage-service/quota-cooldowns", method: http.MethodGet, allowed: true},
		{resource: "usage-service/quota-cooldowns/recover", method: http.MethodPost, allowed: true},
		{resource: "v0/management/monitoring/analytics", method: http.MethodPost, allowed: true},
		{resource: "v0/management/monitoring/header-snapshots", method: http.MethodGet, allowed: true},
		{resource: "v0/management/monitoring/account-history", method: http.MethodPost, allowed: true},
		{resource: "v0/management/monitoring/account-window-usage", method: http.MethodPost, allowed: true},
		{resource: "v0/management/quota-snapshots", method: http.MethodPost, allowed: true},
		{resource: "v0/management/quota-snapshots/query", method: http.MethodPost, allowed: true},
		{resource: "v0/management/account-action-candidates", method: http.MethodGet, allowed: true},
		{resource: "v0/management/account-action-candidates/12/resolve", method: http.MethodPost, allowed: true},
		{resource: "v0/management/codex-inspection/runs", method: http.MethodGet, allowed: true},
		{resource: "v0/management/codex-inspection/runs/12/actions", method: http.MethodPost, allowed: true},
		{resource: "v0/management/usage/import", method: http.MethodPost, allowed: true},
		{resource: "v0/management/usage/import-sessions/abc/chunk", method: http.MethodPut, allowed: true},
		{resource: "v0/management/usage/import-sessions/abc/complete", method: http.MethodPost, allowed: true},
		{resource: "v0/management/auth-files/fields", method: http.MethodPatch, allowed: true},
		{resource: "v0/management/oauth-excluded-models", method: http.MethodPatch, allowed: true},
		{resource: "v0/management/oauth-model-alias", method: http.MethodPatch, allowed: true},
		{resource: "v0/management/reset-quota", method: http.MethodPost, allowed: true},
		{resource: "v0/management/model-definitions/codex", method: http.MethodGet, allowed: true},
		{resource: "v0/management/debug", method: http.MethodPut, allowed: true},
		{resource: "v0/management/proxy-url", method: http.MethodPut, allowed: true},
		{resource: "v0/management/proxy-url", method: http.MethodDelete, allowed: true},
		{resource: "v0/management/request-retry", method: http.MethodPut, allowed: true},
		{resource: "v0/management/quota-exceeded/switch-project", method: http.MethodPut, allowed: true},
		{resource: "v0/management/quota-exceeded/switch-preview-model", method: http.MethodPut, allowed: true},
		{resource: "v0/management/request-log", method: http.MethodPut, allowed: true},
		{resource: "v0/management/logging-to-file", method: http.MethodPut, allowed: true},
		{resource: "v0/management/logs-max-total-size-mb", method: http.MethodGet, allowed: true},
		{resource: "v0/management/logs-max-total-size-mb", method: http.MethodPut, allowed: true},
		{resource: "v0/management/ws-auth", method: http.MethodPut, allowed: true},
		{resource: "v0/management/force-model-prefix", method: http.MethodGet, allowed: true},
		{resource: "v0/management/force-model-prefix", method: http.MethodPut, allowed: true},
		{resource: "v0/management/routing/strategy", method: http.MethodGet, allowed: true},
		{resource: "v0/management/routing/strategy", method: http.MethodPut, allowed: true},
		{resource: "v0/management/api-call", method: http.MethodPost, allowed: true},
		{resource: "v0/management/api-key-usage", method: http.MethodGet, allowed: true},
		{resource: "v0/management/vertex/import", method: http.MethodPost, allowed: true},
		{resource: "v0/management/debug", method: http.MethodDelete, allowed: false},
		{resource: "v0/management/api-call", method: http.MethodGet, allowed: false},
		{resource: "v0/management/api-key-usage", method: http.MethodPut, allowed: false},
		{resource: "v0/management/model-definitions/codex/extra", method: http.MethodGet, allowed: false},
		{resource: "v0/management/codex-inspection/runs/not-an-id/actions", method: http.MethodPost, allowed: false},
		{resource: "v0/management/account-action-candidates/1/arbitrary", method: http.MethodPost, allowed: false},
		{resource: "v0/management/request-error-logs/../manager.log", method: http.MethodGet, allowed: false},
		{resource: "v0/management/request-error-logs/%2e%2e", method: http.MethodGet, allowed: false},
		{resource: "v0/management/usage/import-sessions/a%2Fb/chunk", method: http.MethodPut, allowed: false},
		{resource: "v0/management/oauth-callback", method: http.MethodGet, allowed: false},
	} {
		t.Run(test.method+" "+test.resource, func(t *testing.T) {
			if got := legacyBridgePathAllowed(test.resource, test.method); got != test.allowed {
				t.Fatalf("legacyBridgePathAllowed(%q, %q)=%v, want %v", test.resource, test.method, got, test.allowed)
			}
		})
	}
}

func TestFreshProviderPausePreservesReadsAndUnrelatedWrites(t *testing.T) {
	forwards := 0
	handler := guardFreshLegacyWrites(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forwards++; w.WriteHeader(http.StatusNoContent) }), nil)
	for _, request := range []struct{ method, path string }{
		{http.MethodGet, "/v0/management/codex-api-key"},
		{http.MethodHead, "/v0/management/openai-compatibility"},
		{http.MethodPut, "/v0/management/api-keys"},
		{http.MethodDelete, "/v0/management/auth-files?name=synthetic.json"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(request.method, request.path, nil))
		if response.Code != http.StatusNoContent {
			t.Fatalf("unrelated request blocked: %+v", request)
		}
	}
	if forwards != 4 {
		t.Fatalf("forwarded %d unrelated requests", forwards)
	}
}
