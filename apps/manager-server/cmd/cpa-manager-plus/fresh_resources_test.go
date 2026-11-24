package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagearchive"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
	freshstrategy "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"gopkg.in/yaml.v3"
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
	request := httptest.NewRequest(http.MethodPut, currentInstallManagementAPIV2Prefix+"cpamp/v0/management/usage/import-sessions/resume-123/chunk?offset=17", nil)
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
			req := httptest.NewRequest(http.MethodGet, currentInstallManagementAPIV2Prefix+""+resource, nil)
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
		req := httptest.NewRequest(http.MethodGet, currentInstallManagementAPIV2Prefix+""+resource, nil)
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
			req := httptest.NewRequest(http.MethodGet, currentInstallManagementAPIV2Prefix+""+resource, nil)
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
			req := httptest.NewRequest(request.method, currentInstallManagementAPIV2Prefix+""+request.resource, nil)
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
		req := httptest.NewRequest(http.MethodGet, currentInstallManagementAPIV2Prefix+""+resource, nil)
		req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		response := httptest.NewRecorder()
		app.canonicalAPI(response, req)
		if response.Code != http.StatusNotFound {
			t.Fatalf("resource %s returned %d, want 404", resource, response.Code)
		}
	}
	req := httptest.NewRequest(http.MethodDelete, currentInstallManagementAPIV2Prefix+"cpamp/v0/management/config", nil)
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
		{resource: "v0/management/kimi-ai-auth-url", method: http.MethodGet, allowed: true},
		{resource: "v0/management/devin-auth-url", method: http.MethodGet, allowed: true},
		{resource: "v0/management/meta-auth-url", method: http.MethodGet, allowed: true},
		{resource: "v0/management/meta-api-key", method: http.MethodGet, allowed: true},
		{resource: "v0/management/meta-api-key", method: http.MethodPut, allowed: true},
		{resource: "v0/management/meta-api-key", method: http.MethodDelete, allowed: true},
		{resource: "v0/management/meta-api-key", method: http.MethodPost, allowed: false},
		{resource: "v0/management/acme-cloud-auth-url", method: http.MethodGet, allowed: true},
		{resource: "v0/management/acme-cloud-auth-url", method: http.MethodPost, allowed: false},
		{resource: "v0/management/-auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/..-auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/.foo-auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/foo.bar-auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/foo_bar-auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/--auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/UPPER-auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/-foo-auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/foo--bar-auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/model-prices-auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/api-key-aliases-auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/account-action-candidates-auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/codex-inspection-auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/usage-auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/dashboard-auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/monitoring-auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/quota-snapshots-auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/cpa-connection-auth-url", method: http.MethodGet, allowed: false},
		{resource: "v0/management/oauth-session", method: http.MethodDelete, allowed: true},
		{resource: "v0/management/oauth-session", method: http.MethodGet, allowed: false},
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
		{resource: "v0/management/usage/import-sessions", method: http.MethodGet, allowed: true},
		{resource: "v0/management/usage/import-sessions", method: http.MethodPost, allowed: true},
		{resource: "v0/management/usage/import-sessions/abc/chunk", method: http.MethodPut, allowed: true},
		{resource: "v0/management/usage/import-sessions/abc/complete", method: http.MethodPost, allowed: true},
		{resource: "v0/management/usage/maintenance", method: http.MethodGet, allowed: true},
		{resource: "v0/management/usage/maintenance", method: http.MethodPost, allowed: false},
		{resource: "v0/management/usage/archives", method: http.MethodGet, allowed: true},
		{resource: "v0/management/usage/archives", method: http.MethodPost, allowed: true},
		{resource: "v0/management/usage/archives/preview", method: http.MethodPost, allowed: true},
		{resource: "v0/management/usage/archives/preview", method: http.MethodGet, allowed: false},
		{resource: "v0/management/usage/archives/run-1", method: http.MethodGet, allowed: true},
		{resource: "v0/management/usage/archives/run-1/resume", method: http.MethodPost, allowed: true},
		{resource: "v0/management/usage/archives/run-1/verify", method: http.MethodPost, allowed: true},
		{resource: "v0/management/usage/archives/run-1/delete", method: http.MethodPost, allowed: true},
		{resource: "v0/management/usage/archives/run-1/cancel", method: http.MethodPost, allowed: true},
		{resource: "v0/management/usage/archives/run-1/delete", method: http.MethodGet, allowed: false},
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
		{resource: "v0/management/error-logs-max-files", method: http.MethodGet, allowed: true},
		{resource: "v0/management/error-logs-max-files", method: http.MethodPut, allowed: true},
		{resource: "v0/management/ws-auth", method: http.MethodPut, allowed: true},
		{resource: "v0/management/force-model-prefix", method: http.MethodGet, allowed: true},
		{resource: "v0/management/force-model-prefix", method: http.MethodPut, allowed: true},
		{resource: "v0/management/routing/strategy", method: http.MethodGet, allowed: true},
		{resource: "v0/management/routing/strategy", method: http.MethodPut, allowed: true},
		{resource: "v0/management/api-call", method: http.MethodPost, allowed: true},
		{resource: "v0/management/api-key-usage", method: http.MethodGet, allowed: true},
		{resource: "v0/management/vertex/import", method: http.MethodPost, allowed: true},
		{resource: "usage-service/runtime/updates/prepare-upload", method: http.MethodPost, allowed: true},
		{resource: "usage-service/runtime/updates/prepare-upload", method: http.MethodGet, allowed: false},
		{resource: "v0/management/debug", method: http.MethodDelete, allowed: false},
		{resource: "v0/management/error-logs-max-files", method: http.MethodDelete, allowed: false},
		{resource: "v0/management/api-call", method: http.MethodGet, allowed: false},
		{resource: "v0/management/api-key-usage", method: http.MethodPut, allowed: false},
		{resource: "v0/management/model-definitions/codex/extra", method: http.MethodGet, allowed: false},
		{resource: "v0/management/codex-inspection/runs/not-an-id/actions", method: http.MethodPost, allowed: false},
		{resource: "v0/management/account-action-candidates/1/arbitrary", method: http.MethodPost, allowed: false},
		{resource: "v0/management/request-error-logs/../manager.log", method: http.MethodGet, allowed: false},
		{resource: "v0/management/request-error-logs/%2e%2e", method: http.MethodGet, allowed: false},
		{resource: "v0/management/usage/import-sessions/a%2Fb/chunk", method: http.MethodPut, allowed: false},
		{resource: "v0/management/oauth-callback", method: http.MethodGet, allowed: false},
		{resource: "v8/management/config/api-keys", method: http.MethodGet, allowed: true},
		{resource: "v8/management/config/api-keys", method: http.MethodPut, allowed: false},
		{resource: "v8/management/config/api-keys", method: http.MethodPatch, allowed: false},
		{resource: "v8/management/config/api-keys", method: http.MethodDelete, allowed: false},
		{resource: "v8/management/config/api-keys/gemini", method: http.MethodGet, allowed: true},
		{resource: "v8/management/config/api-keys/gemini", method: http.MethodPut, allowed: true},
		{resource: "v8/management/config/api-keys/interactions", method: http.MethodGet, allowed: true},
		{resource: "v8/management/config/api-keys/interactions", method: http.MethodPut, allowed: true},
		{resource: "v8/management/config/api-keys/vertex", method: http.MethodGet, allowed: true},
		{resource: "v8/management/config/api-keys/vertex", method: http.MethodPut, allowed: true},
		{resource: "v8/management/config/api-keys/codex", method: http.MethodGet, allowed: true},
		{resource: "v8/management/config/api-keys/codex", method: http.MethodPut, allowed: true},
		{resource: "v8/management/config/api-keys/claude", method: http.MethodGet, allowed: true},
		{resource: "v8/management/config/api-keys/claude", method: http.MethodPut, allowed: true},
		{resource: "v8/management/config/api-keys/xai", method: http.MethodGet, allowed: true},
		{resource: "v8/management/config/api-keys/xai", method: http.MethodPut, allowed: true},
		{resource: "v8/management/config/api-keys/meta", method: http.MethodGet, allowed: true},
		{resource: "v8/management/config/api-keys/meta", method: http.MethodPut, allowed: true},
		{resource: "v8/management/config/api-keys/openai-compatibility", method: http.MethodGet, allowed: true},
		{resource: "v8/management/config/api-keys/openai-compatibility", method: http.MethodPut, allowed: true},
		{resource: "v8/management/config/api-keys/claude", method: http.MethodPatch, allowed: false},
		{resource: "v8/management/config/api-keys/claude", method: http.MethodDelete, allowed: false},
		{resource: "v8/management/config/api-keys/unknown", method: http.MethodGet, allowed: false},
		{resource: "v8/management/config/api-keys/unknown", method: http.MethodPut, allowed: false},
		{resource: "v8/management/config/api-keys/claude/extra", method: http.MethodGet, allowed: false},
		{resource: "v8/management/config/api-keys/claude/extra", method: http.MethodPut, allowed: false},
		{resource: "v8/management/config", method: http.MethodGet, allowed: false},
		{resource: "v8/management/config", method: http.MethodPut, allowed: false},
		{resource: "v8/management/config/access/api-keys", method: http.MethodGet, allowed: false},
		{resource: "v8/management/config/access/api-keys", method: http.MethodPut, allowed: false},
		{resource: "v8/management/credentials", method: http.MethodGet, allowed: true},
		{resource: "v8/management/credentials", method: http.MethodPost, allowed: false},
		{resource: "v8/management/credentials", method: http.MethodPut, allowed: false},
		{resource: "v8/management/credentials", method: http.MethodPatch, allowed: false},
		{resource: "v8/management/credentials", method: http.MethodDelete, allowed: false},
		{resource: "v8/management/credentials/verify", method: http.MethodPost, allowed: false},
		{resource: "v8/management/oauth/siwc/bind", method: http.MethodPost, allowed: true},
		{resource: "v8/management/oauth/siwc/bind", method: http.MethodGet, allowed: false},
		{resource: "v8/management/oauth/siwc/bind", method: http.MethodPut, allowed: false},
		{resource: "v8/management/oauth/siwc/bind", method: http.MethodDelete, allowed: false},
		{resource: "v8/management/oauth/callback", method: http.MethodPost, allowed: true},
		{resource: "v8/management/oauth/callback", method: http.MethodGet, allowed: false},
		{resource: "v8/management/oauth/callback", method: http.MethodPut, allowed: false},
		{resource: "v8/management/oauth/callback", method: http.MethodDelete, allowed: false},
		{resource: "v8/management/oauth/status", method: http.MethodGet, allowed: true},
		{resource: "v8/management/oauth/status", method: http.MethodPost, allowed: false},
		{resource: "v8/management/oauth/status", method: http.MethodPut, allowed: false},
		{resource: "v8/management/oauth/status", method: http.MethodDelete, allowed: false},
		{resource: "v8/management/oauth/session", method: http.MethodDelete, allowed: true},
		{resource: "v8/management/oauth/session", method: http.MethodGet, allowed: false},
		{resource: "v8/management/oauth/session", method: http.MethodPost, allowed: false},
		{resource: "v8/management/oauth/session", method: http.MethodPut, allowed: false},
		{resource: "v8/management/oauth", method: http.MethodGet, allowed: false},
		{resource: "v8/management/oauth/siwc", method: http.MethodPost, allowed: false},
		{resource: "v8/management/oauth/siwc/bind/extra", method: http.MethodPost, allowed: false},
		{resource: "v8/management/oauth/status/extra", method: http.MethodGet, allowed: false},
		{resource: "v8/management/oauth/unknown", method: http.MethodGet, allowed: false},
	} {
		t.Run(test.method+" "+test.resource, func(t *testing.T) {
			if got := legacyBridgePathAllowed(test.resource, test.method); got != test.allowed {
				t.Fatalf("legacyBridgePathAllowed(%q, %q)=%v, want %v", test.resource, test.method, got, test.allowed)
			}
		})
	}
}

func TestFreshCanonicalModelPricesSyncUsesDefaultSources(t *testing.T) {
	if freshModelsDevModelPriceSyncURL == "" || freshModelPriceSyncURL == "" || freshOpenRouterModelPriceSyncURL == "" {
		t.Fatalf("fresh model price sync default URLs must be configured: models.dev=%q litellm=%q openrouter=%q",
			freshModelsDevModelPriceSyncURL, freshModelPriceSyncURL, freshOpenRouterModelPriceSyncURL)
	}

	modelsDevServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"openai":{"models":{"gpt-5.2-codex":{"cost":{"input":1.75,"output":14}}}}}`))
	}))
	t.Cleanup(modelsDevServer.Close)
	liteLLMServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"litellm-only":{"input_cost_per_token":0.000002,"output_cost_per_token":0.000006}}`))
	}))
	t.Cleanup(liteLLMServer.Close)
	openRouterServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"openrouter-only","pricing":{"prompt":"0.000003","completion":"0.000009"}}]}`))
	}))
	t.Cleanup(openRouterServer.Close)

	oldModelsDev := freshModelsDevModelPriceSyncURL
	oldLiteLLM := freshModelPriceSyncURL
	oldOpenRouter := freshOpenRouterModelPriceSyncURL
	freshModelsDevModelPriceSyncURL = modelsDevServer.URL
	freshModelPriceSyncURL = liteLLMServer.URL
	freshOpenRouterModelPriceSyncURL = openRouterServer.URL
	t.Cleanup(func() {
		freshModelsDevModelPriceSyncURL = oldModelsDev
		freshModelPriceSyncURL = oldLiteLLM
		freshOpenRouterModelPriceSyncURL = oldOpenRouter
	})

	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)

	req := httptest.NewRequest(
		http.MethodPost,
		currentInstallManagementAPIV2Prefix+"cpamp/v0/management/model-prices/sync",
		strings.NewReader(`{"models":["gpt-5.2-codex","litellm-only","openrouter-only"]}`),
	)
	req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.canonicalAPI(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("model-prices/sync returned %d: %s", response.Code, response.Body.String())
	}
	var payload struct {
		Sources  []string `json:"sources"`
		Imported int      `json:"imported"`
		Prices   map[string]struct {
			Source string  `json:"source"`
			Prompt float64 `json:"prompt"`
		} `json:"prices"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode sync response: %v", err)
	}
	if payload.Imported != 3 {
		t.Fatalf("imported = %d, want 3; body = %s", payload.Imported, response.Body.String())
	}
	for _, expected := range []string{"models.dev", "litellm", "openrouter"} {
		if !slices.Contains(payload.Sources, expected) {
			t.Fatalf("sources = %v, missing %q", payload.Sources, expected)
		}
	}
	if payload.Prices["gpt-5.2-codex"].Source != "models.dev" ||
		payload.Prices["litellm-only"].Source != "litellm" ||
		payload.Prices["openrouter-only"].Source != "openrouter" {
		t.Fatalf("synced prices = %#v", payload.Prices)
	}
}

func TestFreshBridgedUsageServiceConfigOversizedPatchRejected(t *testing.T) {
	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)

	oversizedQueue := strings.Repeat("q", 4100)
	oversizedBody := `{"collector":{"queue":"` + oversizedQueue + `"}}`
	if len(oversizedBody) <= 4096 {
		t.Fatalf("test body length = %d, want > 4096", len(oversizedBody))
	}

	for _, resource := range []string{"cpamp/usage-service/config", "cpamp/config"} {
		req := httptest.NewRequest(
			http.MethodPatch,
			currentInstallManagementAPIV2Prefix+resource,
			strings.NewReader(oversizedBody),
		)
		req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		app.canonicalAPI(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("oversized PATCH %s = %d %s, want 400", resource, rec.Code, rec.Body.String())
		}
	}

	startBefore := app.legacyCollector.StartCount()
	retentionReq := httptest.NewRequest(
		http.MethodPatch,
		currentInstallManagementAPIV2Prefix+"cpamp/usage-service/config",
		strings.NewReader(`{"retention":{"enabled":true,"retentionDays":14}}`),
	)
	retentionReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	retentionReq.Header.Set("Content-Type", "application/json")
	retentionRec := httptest.NewRecorder()
	app.canonicalAPI(retentionRec, retentionReq)
	if retentionRec.Code != http.StatusOK {
		t.Fatalf("valid retention PATCH = %d %s, want 200", retentionRec.Code, retentionRec.Body.String())
	}
	if got := app.legacyCollector.StartCount(); got != startBefore {
		t.Fatalf("collector StartCount after retention patch = %d, want %d", got, startBefore)
	}
}

func TestFreshDerivedRollupAndArchiveWorkersEnableArchiveDeletionAndRetention(t *testing.T) {
	dbDir, err := os.MkdirTemp(t.TempDir(), "fresh-archive-workers-*")
	if err != nil {
		t.Fatal(err)
	}
	paths := legacypreflight.DefaultPaths(filepath.Join(dbDir, "manager.sqlite"))
	paths.Config = filepath.Join(dbDir, "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{
		Initialize: true,
		AdminKey:   strategyTestAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = opened.Close()
	})

	archiveDir := filepath.Join(dbDir, "usage-archives")
	cfg := config.Config{
		DBPath:                       paths.Database,
		UsageArchiveDir:              archiveDir,
		DashboardHourlyRollupEnabled: true,
		UsageArchiveRetentionEnabled: false,
		UsageArchiveRetentionDays:    7,
	}
	workerBaseCtx, cancelWorker := context.WithCancel(t.Context())
	app := newFreshApplication(opened, cfg, nil)
	workerCtx := app.lifecycleContext(workerBaseCtx)
	app.runCtx = workerCtx
	t.Cleanup(func() {
		cancelWorker()
		app.Close()
	})
	if app.retentionWorker == nil {
		t.Fatal("expected retentionWorker wired when DashboardHourlyRollupEnabled is true")
	}
	app.startDatabaseMaintenance(workerCtx)
	sum := sha256.Sum256([]byte("fresh-archive-event-1"))
	eventTS := time.Now().Add(-15 * 24 * time.Hour).UnixMilli()
	inserted, err := app.legacyStore.InsertEvents(t.Context(), []usage.Event{
		{
			RequestID:    "fresh-archive-req-1",
			EventHash:    hex.EncodeToString(sum[:]),
			TimestampMS:  eventTS,
			Timestamp:    time.UnixMilli(eventTS).UTC().Format(time.RFC3339Nano),
			Provider:     "codex",
			ExecutorType: "CodexExecutor",
			Model:        "gpt-5.2-codex",
			InputTokens:  120,
			OutputTokens: 40,
			TotalTokens:  160,
			CreatedAtMS:  eventTS,
		},
	})
	if err != nil || inserted.Inserted != 1 {
		t.Fatalf("InsertEvents = %#v, err = %v", inserted, err)
	}
	app.accountHistoryRollupWorker.Wake()
	app.usageDerivedRollupWorker.Wake()
	app.usageHourlyAggregateWorker.Wake()
	app.usageProjection.Wake()

	cutoffMS := time.Now().Add(-7 * 24 * time.Hour).UnixMilli()
	created, err := app.legacyApp.UsageService.CreateArchive(t.Context(), cutoffMS)
	if err != nil {
		t.Fatalf("CreateArchive: %v", err)
	}
	if _, err := app.legacyApp.UsageService.ResumeArchive(t.Context(), created.Run.ID); err != nil {
		t.Fatalf("ResumeArchive (archive stage): %v", err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var verifyErr error
	for time.Now().Before(deadline) {
		_, verifyErr = app.legacyApp.UsageService.VerifyArchive(t.Context(), created.Run.ID)
		if verifyErr == nil {
			if _, verifyErr = app.legacyApp.UsageService.ResumeArchive(t.Context(), created.Run.ID); verifyErr == nil {
				break
			}
		}
		app.accountHistoryRollupWorker.Wake()
		app.usageDerivedRollupWorker.Wake()
		app.usageHourlyAggregateWorker.Wake()
		app.usageProjection.Wake()
		time.Sleep(25 * time.Millisecond)
	}
	if verifyErr != nil {
		t.Fatalf("VerifyArchive did not become ready in Fresh mode: %v", verifyErr)
	}
	var deleteErr error
	deleteDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deleteDeadline) {
		_, deleteErr = app.legacyApp.UsageService.DeleteArchive(t.Context(), created.Run.ID)
		if deleteErr == nil {
			break
		}
		app.usageProjection.Wake()
		time.Sleep(25 * time.Millisecond)
	}
	if deleteErr != nil {
		t.Fatalf("DeleteArchive failed in Fresh mode: %v", deleteErr)
	}
	for time.Now().Before(deadline) {
		status, runErr := app.legacyApp.UsageService.ArchiveStatus(t.Context(), created.Run.ID)
		if runErr == nil && status.Run.Status == usagearchive.StatusCompleted && status.Run.DeletedEventCount == 1 {
			break
		}
		_, _ = app.legacyApp.UsageService.ResumeArchive(t.Context(), created.Run.ID)
		time.Sleep(25 * time.Millisecond)
	}
	finalRun, err := app.legacyApp.UsageService.ArchiveStatus(t.Context(), created.Run.ID)
	if err != nil {
		t.Fatalf("GetArchiveRun: %v", err)
	}
	if finalRun.Run.Status != usagearchive.StatusCompleted || finalRun.Run.DeletedEventCount != 1 {
		t.Fatalf("final archive run = %#v, want completed with 1 deleted event", finalRun.Run)
	}
}

func TestMainRetentionWorkerHonorsDynamicDisableWhenConfigJSONEnabled(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		DBPath:                       filepath.Join(dir, "manager.sqlite"),
		DataKey:                      strings.Repeat("a", 64),
		AdminKey:                     strategyTestAdmin,
		UsageArchiveDir:              filepath.Join(dir, "archives"),
		DashboardHourlyRollupEnabled: true,
		UsageArchiveRetentionEnabled: true,
		UsageArchiveRetentionDays:    14,
	}
	resolvedKey, _, err := security.LoadOrCreateDataKey(cfg.DataKey, "")
	if err != nil {
		t.Fatalf("security.LoadOrCreateDataKey: %v", err)
	}
	protector, err := security.NewProtector(resolvedKey)
	if err != nil {
		t.Fatalf("security.NewProtector: %v", err)
	}
	st, err := store.Open(cfg.DBPath, protector)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	serverApp := httpapi.New(cfg, st, nil)

	retentionWorker := newUsageArchiveRetentionWorker(cfg, serverApp)
	if retentionWorker == nil {
		t.Fatal("expected non-nil retentionWorker when DashboardHourlyRollupEnabled is true")
	}

	enabled, days, err := serverApp.AppContext().ManagerConfigService.ResolveRetention(t.Context())
	if err != nil || !enabled || days != 14 {
		t.Fatalf("initial ResolveRetention = (%v, %d, %v), want (true, 14, nil)", enabled, days, err)
	}

	disable := false
	if _, err := serverApp.AppContext().ManagerConfigService.PatchRetention(t.Context(), store.ManagerRetentionPatch{
		Enabled: &disable,
	}); err != nil {
		t.Fatalf("PatchRetention(enabled=false): %v", err)
	}
	enabledAfter, _, err := serverApp.AppContext().ManagerConfigService.ResolveRetention(t.Context())
	if err != nil || enabledAfter {
		t.Fatalf("ResolveRetention after disable = (%v, %v), want (false, nil)", enabledAfter, err)
	}

	workerCtx, cancelWorker := context.WithCancel(t.Context())
	retentionWorker.Start(workerCtx)
	time.Sleep(40 * time.Millisecond)
	cancelWorker()
	stopCtx, cancelStop := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelStop()
	if err := retentionWorker.StopAndWait(stopCtx); err != nil {
		t.Fatalf("retentionWorker.StopAndWait: %v", err)
	}

	if _, found, err := serverApp.AppContext().UsageService.ActiveArchiveRun(t.Context()); err != nil || found {
		t.Fatalf("ActiveArchiveRun after disabled retention worker = found:%v err:%v, want false/nil", found, err)
	}
}

func TestFreshUsageRuntimeConfigSparsePatchAndMixedCaseBodyKeysPreserveTLSSkipVerify(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()

	mgmtKey := strings.Repeat("a", 64)
	const baseURL = "https://cpa.local:8317"
	opened, app := newUsageCompletionApplication(t, hostCtx, config.Config{
		CPAUpstreamURL: baseURL,
		CollectorMode:  "http",
		PollInterval:   50 * time.Millisecond,
		TLSSkipVerify:  true,
	})
	current, err := app.store.Load(hostCtx)
	if err != nil {
		t.Fatal(err)
	}
	protectedKey, err := opened.Protector.ProtectString(mgmtKey)
	if err != nil {
		t.Fatal(err)
	}
	runtimeObs := validInstallationRuntime()
	if _, err := app.store.Update(hostCtx, current.Operation.Identity, current.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy:                      bootstrap.NewManaged,
			Stage:                         "committed",
			TargetIdentity:                string(runtimeObs.Identity),
			TargetGeneration:              uint64(runtimeObs.Generation),
			TargetArtifactID:              string(runtimeObs.ActiveGatewayArtifact.ArtifactID),
			DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity(baseURL),
		}
		r.Operation.ProductComplete = true
		r.Connection = &model.ManagerCPAConnectionConfig{
			CPABaseURL:    baseURL,
			ManagementKey: protectedKey,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	callPatch := func(body string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPatch, currentInstallManagementAPIV2Prefix+"cpamp/config", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		app.canonicalAPI(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("PATCH %s = %d %s, want 200", body, rec.Code, rec.Body.String())
		}
	}

	// Sparse collector PATCH without tlsSkipVerify preserves process TLSSkipVerify=true.
	callPatch(`{"collector":{"queue":"custom-queue"}}`)
	rtCfg, err := app.usageRuntimeConfig(hostCtx, baseURL, mgmtKey)
	if err != nil {
		t.Fatalf("usageRuntimeConfig after sparse collector patch: %v", err)
	}
	if rtCfg.Queue != "custom-queue" || !rtCfg.TLSSkipVerify {
		t.Fatalf("after sparse collector patch: got Queue=%q TLSSkipVerify=%v, want custom-queue / true", rtCfg.Queue, rtCfg.TLSSkipVerify)
	}

	// Sparse retention PATCH also preserves process TLSSkipVerify=true.
	callPatch(`{"retention":{"retentionDays":10}}`)
	rtCfg, err = app.usageRuntimeConfig(hostCtx, baseURL, mgmtKey)
	if err != nil {
		t.Fatalf("usageRuntimeConfig after retention patch: %v", err)
	}
	if !rtCfg.TLSSkipVerify {
		t.Fatalf("after sparse retention patch: TLSSkipVerify = false, want true")
	}

	// Explicit tlsSkipVerify=false overrides process TLSSkipVerify=true.
	callPatch(`{"collector":{"tlsSkipVerify":false}}`)
	rtCfg, err = app.usageRuntimeConfig(hostCtx, baseURL, mgmtKey)
	if err != nil {
		t.Fatalf("usageRuntimeConfig after explicit tlsSkipVerify=false patch: %v", err)
	}
	if rtCfg.TLSSkipVerify {
		t.Fatalf("after explicit tlsSkipVerify=false patch: TLSSkipVerify = true, want false")
	}

	// Defense-in-depth: controller rejects mixed-case body cleanly when routed through serveLegacyResource.
	var muxRec httptest.ResponseRecorder
	req := httptest.NewRequest(http.MethodPatch, currentInstallManagementAPIV2Prefix+"cpamp/config", bytes.NewBufferString(`{"Collector":{"queue":"mixed-case-q"}}`))
	req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	req.Header.Set("Content-Type", "application/json")
	clone := httptest.NewRequest(http.MethodPatch, "/usage-service/config", bytes.NewBufferString(`{"Collector":{"queue":"mixed-case-q"}}`))
	clone.Header = req.Header.Clone()
	opRec, err := app.store.Load(hostCtx)
	if err != nil {
		t.Fatal(err)
	}
	app.serveLegacyResource(&muxRec, req, clone, opRec.Operation, "usage-service/config")
	if muxRec.Code != http.StatusBadRequest {
		t.Fatalf("expected controller to reject mixed-case body cleanly, got %d %s", muxRec.Code, muxRec.Body.String())
	}
}

func TestFreshManagedBridgedV8APIKeysReadAndConditionalWrite(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()

	mgmtKey := strings.Repeat("b", 64)
	currentETag := `"managed-etag-v1"`
	var lastIfMatch string
	var lastBody string
	cpaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+mgmtKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config/api-keys":
			w.Header().Set("ETag", currentETag)
			_, _ = w.Write([]byte(`{"claude":[{"name":"claude-prod"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config/api-keys/claude":
			w.Header().Set("ETag", currentETag)
			_, _ = w.Write([]byte(`[{"name":"claude-prod"}]`))
		case r.Method == http.MethodPut && r.URL.Path == "/v8/management/config/api-keys/claude":
			raw, _ := io.ReadAll(r.Body)
			lastBody = string(raw)
			lastIfMatch = r.Header.Get("If-Match")
			if lastIfMatch != currentETag {
				w.Header().Set("ETag", currentETag)
				w.WriteHeader(http.StatusPreconditionFailed)
				_, _ = w.Write([]byte(`{"error":"precondition_failed"}`))
				return
			}
			currentETag = `"managed-etag-v2"`
			w.Header().Set("ETag", currentETag)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer cpaServer.Close()

	opened, app := newUsageCompletionApplication(t, hostCtx, config.Config{
		CPAUpstreamURL: cpaServer.URL,
		CollectorMode:  "http",
		PollInterval:   50 * time.Millisecond,
	})
	current, err := app.store.Load(hostCtx)
	if err != nil {
		t.Fatal(err)
	}
	protectedKey, err := opened.Protector.ProtectString(mgmtKey)
	if err != nil {
		t.Fatal(err)
	}
	runtimeObs := validInstallationRuntime()
	if _, err := app.store.Update(hostCtx, current.Operation.Identity, current.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy:                      bootstrap.NewManaged,
			Stage:                         "committed",
			TargetIdentity:                string(runtimeObs.Identity),
			TargetGeneration:              uint64(runtimeObs.Generation),
			TargetArtifactID:              string(runtimeObs.ActiveGatewayArtifact.ArtifactID),
			DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity(cpaServer.URL),
		}
		r.Operation.ProductComplete = true
		r.Connection = &model.ManagerCPAConnectionConfig{
			CPABaseURL:    cpaServer.URL,
			ManagementKey: protectedKey,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	callBridge := func(method, resource, body, ifMatch string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, currentInstallManagementAPIV2Prefix+resource, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if ifMatch != "" {
			req.Header.Set("If-Match", ifMatch)
		}
		rec := httptest.NewRecorder()
		app.canonicalAPI(rec, req)
		return rec
	}

	rootRes := callBridge(http.MethodGet, "cpamp/v8/management/config/api-keys", "", "")
	if rootRes.Code != http.StatusOK || rootRes.Header().Get("ETag") != `"managed-etag-v1"` {
		t.Fatalf("GET root = %d etag=%q body=%s", rootRes.Code, rootRes.Header().Get("ETag"), rootRes.Body.String())
	}

	staleRes := callBridge(http.MethodPut, "cpamp/v8/management/config/api-keys/claude", `[]`, `"wrong-etag"`)
	if staleRes.Code != http.StatusPreconditionFailed || staleRes.Header().Get("ETag") != `"managed-etag-v1"` {
		t.Fatalf("stale PUT = %d etag=%q body=%s", staleRes.Code, staleRes.Header().Get("ETag"), staleRes.Body.String())
	}

	putBody := `[{"name":"claude-prod-2","keys":[{"api-key":"sk-ant-2"}]}]`
	putRes := callBridge(http.MethodPut, "cpamp/v8/management/config/api-keys/claude", putBody, `"managed-etag-v1"`)
	if putRes.Code != http.StatusOK || putRes.Header().Get("ETag") != `"managed-etag-v2"` {
		t.Fatalf("PUT family = %d etag=%q body=%s", putRes.Code, putRes.Header().Get("ETag"), putRes.Body.String())
	}
	if lastIfMatch != `"managed-etag-v1"` || lastBody != putBody {
		t.Fatalf("bridged PUT If-Match=%q body=%q", lastIfMatch, lastBody)
	}

	for _, blocked := range []struct {
		method   string
		resource string
	}{
		{http.MethodPut, "cpamp/v8/management/config/api-keys"},
		{http.MethodPatch, "cpamp/v8/management/config/api-keys/claude"},
		{http.MethodDelete, "cpamp/v8/management/config/api-keys/claude"},
		{http.MethodGet, "cpamp/v8/management/config/api-keys/unknown"},
		{http.MethodPut, "cpamp/v8/management/config/api-keys/unknown"},
		{http.MethodGet, "cpamp/v8/management/config"},
		{http.MethodGet, "cpamp/v8/management/config/access/api-keys"},
	} {
		res := callBridge(blocked.method, blocked.resource, `{}`, "")
		if res.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d %s, want 404", blocked.method, blocked.resource, res.Code, res.Body.String())
		}
	}
}

func TestV8OAuthLegacyBridgeGateAndProviderWaitHelpers(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{path: "/v8/management/oauth/callback", want: true},
		{path: "/v8/management/oauth/callback/", want: true},
		{path: "/v0/management/oauth-callback", want: true},
		{path: "/v0/management/api-call", want: true},
		{path: "/v8/management/oauth/siwc/bind", want: false},
		{path: "/v8/management/oauth/status", want: false},
		{path: "/v8/management/oauth/session", want: false},
		{path: "/v8/management/models/refresh", want: false},
		{path: "/v8/management/credentials", want: false},
	} {
		if got := externalLegacyWaitsOnProvider(tc.path); got != tc.want {
			t.Fatalf("externalLegacyWaitsOnProvider(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}

	var app freshApplication
	if got := app.externalCredentialHoldBudget("/v8/management/oauth/callback"); got != 0 {
		t.Fatalf("externalCredentialHoldBudget(v8 oauth callback) = %v, want 0", got)
	}
	if got := app.externalCredentialHoldBudget("/v8/management/oauth/status"); got <= 0 {
		t.Fatalf("externalCredentialHoldBudget(v8 oauth status) = %v, want > 0", got)
	}
	if got := app.externalCredentialHoldBudget("/v8/management/credentials"); got <= 0 {
		t.Fatalf("externalCredentialHoldBudget(v8 credentials) = %v, want > 0", got)
	}

	for _, tc := range []struct {
		name   string
		method string
		target string
		want   bool
	}{
		{name: "canonical credentials", method: http.MethodGet, target: "/v8/management/credentials", want: true},
		{name: "canonical credentials with query", method: http.MethodGet, target: "/v8/management/credentials?name=codex.json", want: true},
		{name: "credentials wrong method", method: http.MethodPost, target: "/v8/management/credentials", want: false},
		{name: "credentials trailing slash", method: http.MethodGet, target: "/v8/management/credentials/", want: false},
		{name: "credentials encoded unreserved", method: http.MethodGet, target: "/v8/management/%63redentials", want: false},
		{name: "canonical siwc bind", method: http.MethodPost, target: "/v8/management/oauth/siwc/bind", want: true},
		{name: "canonical callback", method: http.MethodPost, target: "/v8/management/oauth/callback", want: true},
		{name: "canonical status with state", method: http.MethodGet, target: "/v8/management/oauth/status?state=st_1", want: true},
		{name: "canonical session with state", method: http.MethodDelete, target: "/v8/management/oauth/session?state=st_1", want: true},
		{name: "bind wrong method", method: http.MethodGet, target: "/v8/management/oauth/siwc/bind", want: false},
		{name: "callback wrong method", method: http.MethodGet, target: "/v8/management/oauth/callback", want: false},
		{name: "status wrong method", method: http.MethodPost, target: "/v8/management/oauth/status?state=st_1", want: false},
		{name: "session wrong method", method: http.MethodGet, target: "/v8/management/oauth/session?state=st_1", want: false},
		{name: "options on status", method: http.MethodOptions, target: "/v8/management/oauth/status", want: false},
		{name: "trailing slash", method: http.MethodGet, target: "/v8/management/oauth/status/", want: false},
		{name: "duplicate slash", method: http.MethodGet, target: "/v8//management/oauth/status", want: false},
		{name: "encoded slash", method: http.MethodPost, target: "/v8/management/oauth/siwc%2Fbind", want: false},
		{name: "extra segment", method: http.MethodPost, target: "/v8/management/oauth/siwc/bind/extra", want: false},
		{name: "oauth root", method: http.MethodGet, target: "/v8/management/oauth", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.target, nil)
			if got := legacyBridgePresentsSavedKey(req); got != tc.want {
				t.Fatalf("legacyBridgePresentsSavedKey(%s %s) = %v, want %v", tc.method, tc.target, got, tc.want)
			}
		})
	}
}

func TestFreshManagedBridgedV8OAuthEndToEnd(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()

	mgmtKey := strings.Repeat("c", 64)
	type upstreamHit struct {
		method string
		path   string
		query  string
		body   string
	}
	var hits []upstreamHit
	cpaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+mgmtKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		hits = append(hits, upstreamHit{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			body:   string(raw),
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer cpaServer.Close()

	opened, app := newUsageCompletionApplication(t, hostCtx, config.Config{
		CPAUpstreamURL: cpaServer.URL,
		CollectorMode:  "http",
		PollInterval:   50 * time.Millisecond,
	})
	current, err := app.store.Load(hostCtx)
	if err != nil {
		t.Fatal(err)
	}
	protectedKey, err := opened.Protector.ProtectString(mgmtKey)
	if err != nil {
		t.Fatal(err)
	}
	runtimeObs := validInstallationRuntime()
	if _, err := app.store.Update(hostCtx, current.Operation.Identity, current.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy:                      bootstrap.NewManaged,
			Stage:                         "committed",
			TargetIdentity:                string(runtimeObs.Identity),
			TargetGeneration:              uint64(runtimeObs.Generation),
			TargetArtifactID:              string(runtimeObs.ActiveGatewayArtifact.ArtifactID),
			DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity(cpaServer.URL),
		}
		r.Operation.ProductComplete = true
		r.Connection = &model.ManagerCPAConnectionConfig{
			CPABaseURL:    cpaServer.URL,
			ManagementKey: protectedKey,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	app.routing = freshrouting.New(app.store, activeAdoptRoutingDriver{store: app.store})

	const apiBase = currentInstallManagementAPIV2Prefix
	serveRequest := func(method, target, body, auth string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec
	}

	for idx, tc := range []struct {
		method    string
		resource  string
		body      string
		wantPath  string
		wantQuery string
	}{
		{
			method:    http.MethodGet,
			resource:  "cpamp/v8/management/credentials?name=codex-acct.json",
			wantPath:  "/v8/management/credentials",
			wantQuery: "name=codex-acct.json",
		},
		{
			method:   http.MethodPost,
			resource: "cpamp/v8/management/oauth/siwc/bind",
			body:     `{"name":"codex-acct.json"}`,
			wantPath: "/v8/management/oauth/siwc/bind",
		},
		{
			method:   http.MethodPost,
			resource: "cpamp/v8/management/oauth/callback",
			body:     `{"provider":"codex","redirect_url":"http://127.0.0.1:1455/auth/callback?state=st_1&code=cd_1"}`,
			wantPath: "/v8/management/oauth/callback",
		},
		{
			method:    http.MethodGet,
			resource:  "cpamp/v8/management/oauth/status?state=st_1",
			wantPath:  "/v8/management/oauth/status",
			wantQuery: "state=st_1",
		},
		{
			method:    http.MethodDelete,
			resource:  "cpamp/v8/management/oauth/session?state=st_1",
			wantPath:  "/v8/management/oauth/session",
			wantQuery: "state=st_1",
		},
	} {
		res := serveRequest(tc.method, apiBase+tc.resource, tc.body, "Bearer "+strategyTestAdmin, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("%s %s = %d %s, want 200", tc.method, tc.resource, res.Code, res.Body.String())
		}
		if len(hits) != idx+1 {
			t.Fatalf("hits len = %d, want %d", len(hits), idx+1)
		}
		got := hits[idx]
		if got.method != tc.method || got.path != tc.wantPath || got.query != tc.wantQuery || got.body != tc.body {
			t.Fatalf("hit %d = %#v, want method=%q path=%q query=%q body=%q", idx, got, tc.method, tc.wantPath, tc.wantQuery, tc.body)
		}
	}

	// Cross-site requests through app.ServeHTTP fail closed with 403 before reaching CPA.
	for _, crossSite := range []struct {
		method string
		target string
	}{
		{http.MethodGet, apiBase + "cpamp/v8/management/credentials"},
		{http.MethodPost, apiBase + "cpamp/v8/management/oauth/siwc/bind"},
		{http.MethodPost, apiBase + "cpamp/v8/management/oauth/callback"},
		{http.MethodGet, apiBase + "cpamp/v8/management/oauth/status?state=st_1"},
		{http.MethodDelete, apiBase + "cpamp/v8/management/oauth/session?state=st_1"},
	} {
		before := len(hits)
		res := serveRequest(crossSite.method, crossSite.target, "", "Bearer "+strategyTestAdmin, map[string]string{
			"Sec-Fetch-Site": "cross-site",
			"Sec-Fetch-Dest": "empty",
		})
		if res.Code != http.StatusForbidden {
			t.Fatalf("cross-site %s %s = %d %s, want 403", crossSite.method, crossSite.target, res.Code, res.Body.String())
		}
		if len(hits) != before {
			t.Fatalf("cross-site %s %s reached upstream unexpectedly", crossSite.method, crossSite.target)
		}
	}

	// Missing/wrong/raw-CPA credentials fail closed with 401 before reaching CPA.
	for _, unauth := range []struct {
		name   string
		method string
		target string
		auth   string
	}{
		{name: "missing auth on credentials", method: http.MethodGet, target: apiBase + "cpamp/v8/management/credentials", auth: ""},
		{name: "wrong auth on callback", method: http.MethodPost, target: apiBase + "cpamp/v8/management/oauth/callback", auth: "Bearer wrong-admin-key"},
		{name: "raw CPA key on bind", method: http.MethodPost, target: apiBase + "cpamp/v8/management/oauth/siwc/bind", auth: "Bearer " + mgmtKey},
		{name: "missing auth on session", method: http.MethodDelete, target: apiBase + "cpamp/v8/management/oauth/session?state=st_1", auth: ""},
	} {
		t.Run(unauth.name, func(t *testing.T) {
			before := len(hits)
			res := serveRequest(unauth.method, unauth.target, "", unauth.auth, nil)
			if res.Code != http.StatusUnauthorized {
				t.Fatalf("%s = %d %s, want 401", unauth.name, res.Code, res.Body.String())
			}
			if len(hits) != before {
				t.Fatalf("%s reached upstream unexpectedly", unauth.name)
			}
		})
	}

	// Non-canonical and disallowed paths/methods fail closed with 404 before reaching CPA.
	for _, blocked := range []struct {
		method string
		target string
	}{
		{http.MethodPost, apiBase + "cpamp/v8/management/credentials"},
		{http.MethodGet, apiBase + "cpamp/v8/management/credentials/"},
		{http.MethodPost, apiBase + "cpamp/v8/management/credentials/verify"},
		{http.MethodGet, apiBase + "cpamp/v8/management/%63redentials"},
		{http.MethodGet, apiBase + "cpamp/v8/management/oauth/siwc/bind"},
		{http.MethodGet, apiBase + "cpamp/v8/management/oauth/callback"},
		{http.MethodPost, apiBase + "cpamp/v8/management/oauth/status?state=st_1"},
		{http.MethodGet, apiBase + "cpamp/v8/management/oauth/session?state=st_1"},
		{http.MethodPost, apiBase + "cpamp/v8/management/oauth/siwc/bind/extra"},
		{http.MethodGet, apiBase + "cpamp/v8/management/oauth"},
		{http.MethodGet, apiBase + "cpamp/v8/management/oauth//status"},
		{http.MethodGet, apiBase + "cpamp/v8/management/oauth/%73tatus"},
		{http.MethodPost, apiBase + "cpamp/v8/management/oauth/siwc/%62ind"},
		{http.MethodGet, apiBase + "cpamp/v8/management/oauth/%2E%2E/config"},
		{http.MethodPost, apiBase + "cpamp/v8/management/oauth/siwc%2fbind"},
	} {
		before := len(hits)
		res := serveRequest(blocked.method, blocked.target, "", "Bearer "+strategyTestAdmin, nil)
		if res.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d %s, want 404", blocked.method, blocked.target, res.Code, res.Body.String())
		}
		if res.Header().Get("Location") != "" {
			t.Fatalf("%s %s redirected to %q", blocked.method, blocked.target, res.Header().Get("Location"))
		}
		if len(hits) != before {
			t.Fatalf("%s %s reached upstream unexpectedly", blocked.method, blocked.target)
		}
	}
}

func TestFreshExternalBridgedV8OAuthAndCredentialsGate(t *testing.T) {
	newRoutedExternalApp := func(t *testing.T, handler http.HandlerFunc) *freshApplication {
		t.Helper()
		srv := httptest.NewServer(handler)
		t.Cleanup(srv.Close)
		app := newExternalApp(t, srv.URL, externalAuthorityManagement)
		app.routing = freshrouting.New(app.store, activeAdoptRoutingDriver{store: app.store})
		return app
	}

	doExternal := func(t *testing.T, app *freshApplication, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rr := httptest.NewRecorder()
		app.ServeHTTP(rr, req)
		return rr
	}

	t.Run("business 400 404 409 pass through unchanged without fencing callback or credentials", func(t *testing.T) {
		var nextBindStatus atomic.Int32
		nextBindStatus.Store(http.StatusOK)
		var nextBindBody atomic.Value
		nextBindBody.Store(`{"status":"ok"}`)
		var forwardedCalls atomic.Int64
		app := newRoutedExternalApp(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+externalAuthorityManagement {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("X-CPA-Version", "v8.0.55")
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v8/management/config.yaml":
				_, _ = w.Write([]byte(`{"config-version":8}`))
			case "/v8/management/config/access/api-keys":
				_, _ = w.Write([]byte("[]"))
			case "/v8/management/oauth/siwc/bind":
				forwardedCalls.Add(1)
				w.WriteHeader(int(nextBindStatus.Load()))
				_, _ = w.Write([]byte(nextBindBody.Load().(string)))
			case "/v8/management/oauth/callback":
				forwardedCalls.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"status":"ok","bound":true}`))
			case "/v8/management/credentials":
				forwardedCalls.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"files":[{"name":"codex.json","siwc_bind_supported":true}]}`))
			default:
				http.NotFound(w, r)
			}
		})

		for _, biz := range []struct {
			status int
			body   string
		}{
			{status: http.StatusBadRequest, body: `{"error":"invalid_credential_name"}`},
			{status: http.StatusNotFound, body: `{"error":"credential_not_found"}`},
			{status: http.StatusConflict, body: `{"error":"siwc_not_supported"}`},
		} {
			nextBindStatus.Store(int32(biz.status))
			nextBindBody.Store(biz.body)
			res := doExternal(t, app, http.MethodPost, currentInstallManagementAPIV2Prefix+"cpamp/v8/management/oauth/siwc/bind", `{"name":"codex.json"}`)
			if res.Code != biz.status || strings.TrimSpace(res.Body.String()) != biz.body {
				t.Fatalf("business %d bind = %d %q, want %d %q", biz.status, res.Code, res.Body.String(), biz.status, biz.body)
			}
		}

		before := forwardedCalls.Load()
		cbRes := doExternal(t, app, http.MethodPost, currentInstallManagementAPIV2Prefix+"cpamp/v8/management/oauth/callback", `{"provider":"codex","redirect_url":"http://127.0.0.1:1455/auth/callback?state=st_1&code=cd_1"}`)
		if cbRes.Code != http.StatusOK {
			t.Fatalf("callback after business errors = %d %s, want 200", cbRes.Code, cbRes.Body.String())
		}
		credRes := doExternal(t, app, http.MethodGet, currentInstallManagementAPIV2Prefix+"cpamp/v8/management/credentials", "")
		if credRes.Code != http.StatusOK {
			t.Fatalf("credentials after business errors = %d %s, want 200", credRes.Code, credRes.Body.String())
		}
		if got := forwardedCalls.Load() - before; got != 2 {
			t.Fatalf("forwarded calls = %d, want 2", got)
		}
	})

	t.Run("pre-v8 CPA 404 on v8 credentials falls back to v0 auth-files without fencing", func(t *testing.T) {
		var authFilesCalls atomic.Int64
		app := newRoutedExternalApp(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+externalAuthorityManagement {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("X-CPA-Version", "v7.9.0")
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Path {
			case "/v8/management/config.yaml":
				_, _ = w.Write([]byte(`{"config-version":8}`))
			case "/v8/management/config/access/api-keys":
				_, _ = w.Write([]byte("[]"))
			case "/v8/management/credentials":
				http.NotFound(w, r)
			case "/v0/management/auth-files":
				authFilesCalls.Add(1)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"files":[{"name":"codex-legacy.json","type":"codex"}]}`))
			default:
				http.NotFound(w, r)
			}
		})

		v8Res := doExternal(t, app, http.MethodGet, currentInstallManagementAPIV2Prefix+"cpamp/v8/management/credentials", "")
		if v8Res.Code != http.StatusNotFound {
			t.Fatalf("pre-v8 GET /v8/management/credentials = %d %s, want 404", v8Res.Code, v8Res.Body.String())
		}

		v0Res := doExternal(t, app, http.MethodGet, currentInstallManagementAPIV2Prefix+"cpamp/v0/management/auth-files", "")
		if v0Res.Code != http.StatusOK || !strings.Contains(v0Res.Body.String(), "codex-legacy.json") {
			t.Fatalf("fallback GET /v0/management/auth-files = %d %s, want 200 with codex-legacy.json", v0Res.Code, v0Res.Body.String())
		}
		if got := authFilesCalls.Load(); got != 1 {
			t.Fatalf("v0 auth-files calls = %d, want 1", got)
		}
	})

	for _, refusal := range []struct {
		name       string
		cpaStatus  int
		wantDetail string
	}{
		{name: "cpa 401 rejected", cpaStatus: http.StatusUnauthorized, wantDetail: ""},
		{name: "cpa 403 forbidden", cpaStatus: http.StatusForbidden, wantDetail: externalRebindForbiddenDetail},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			var totalCalls atomic.Int64
			app := newRoutedExternalApp(t, func(w http.ResponseWriter, r *http.Request) {
				totalCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(refusal.cpaStatus)
				_, _ = w.Write([]byte(`{"error":"denied"}`))
			})

			bindRes := doExternal(t, app, http.MethodPost, currentInstallManagementAPIV2Prefix+"cpamp/v8/management/oauth/siwc/bind", `{"name":"codex.json"}`)
			if bindRes.Code != http.StatusConflict {
				t.Fatalf("bind status = %d %s, want 409", bindRes.Code, bindRes.Body.String())
			}
			var payload struct {
				Code   string `json:"code"`
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(bindRes.Body.Bytes(), &payload); err != nil {
				t.Fatalf("unmarshal refusal %q: %v", bindRes.Body.String(), err)
			}
			if payload.Code != freshconfig.ReasonManagementRejected || payload.Detail != refusal.wantDetail {
				t.Fatalf("refusal payload = %+v, want code=%q detail=%q", payload, freshconfig.ReasonManagementRejected, refusal.wantDetail)
			}
			if got := totalCalls.Load(); got != 1 {
				t.Fatalf("bind upstream calls = %d, want 1", got)
			}

			before := totalCalls.Load()
			cbRes := doExternal(t, app, http.MethodPost, currentInstallManagementAPIV2Prefix+"cpamp/v8/management/oauth/callback", `{"provider":"codex","redirect_url":"http://127.0.0.1:1455/auth/callback?state=st_1&code=cd_1"}`)
			if cbRes.Code != http.StatusConflict || errorCode(t, cbRes) != freshconfig.ReasonManagementRejected {
				t.Fatalf("fenced callback = %d %s, want 409 %s", cbRes.Code, cbRes.Body.String(), freshconfig.ReasonManagementRejected)
			}
			credRes := doExternal(t, app, http.MethodGet, currentInstallManagementAPIV2Prefix+"cpamp/v8/management/credentials", "")
			if credRes.Code != http.StatusConflict || errorCode(t, credRes) != freshconfig.ReasonManagementRejected {
				t.Fatalf("fenced credentials = %d %s, want 409 %s", credRes.Code, credRes.Body.String(), freshconfig.ReasonManagementRejected)
			}
			if got := totalCalls.Load() - before; got != 0 {
				t.Fatalf("fenced requests reached CPA %d times, want 0", got)
			}
		})
	}
}

func TestCompletedUpgradeBridgeV8CredentialsAndOAuth(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	const mgmtKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	const clientKey = "sk-upg-oauth-client-key-0001"

	type observedHit struct {
		method string
		path   string
		query  string
		body   string
	}
	var hits []observedHit
	cpaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+mgmtKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.55")
		w.Header().Set("Content-Type", "application/json")
		raw, _ := io.ReadAll(r.Body)
		hits = append(hits, observedHit{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			body:   string(raw),
		})
		switch r.URL.Path {
		case "/v8/management/config/access/api-keys":
			_, _ = w.Write([]byte(`["` + clientKey + `"]`))
		case "/v8/management/credentials":
			_, _ = w.Write([]byte(`{"files":[{"name":"codex-upg.json","siwc_bind_supported":true,"use_siwc":true}]}`))
		default:
			_, _ = w.Write([]byte(`{"status":"ok"}`))
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
	sourceIdentity := "external:sha256:" + strings.Repeat("d", 64)
	pluginsInit := false
	destPayload, _ := json.Marshal(upgradeDestinationRecord{
		BaseURL:           cpaServer.URL,
		URLProved:         true,
		ManagementKey:     sealedDestKey,
		ServerFingerprint: "external:sha256:" + strings.Repeat("e", 64),
		Attempt:           1,
		OperationID:       upgradeManagedDestinationOperationID(sourceIdentity, 1),
		Intent: upgradeDestinationIntent{
			ExpectedRuntimeIdentity:   "rt-managed-upg",
			ExpectedRuntimeGeneration: 7,
			ExpectedActiveArtifactID:  qualifiedArtifact,
			SourceIdentity:            sourceIdentity,
			SourceClass:               upgradeManagedDestinationClass,
			APIKeys:                   []string{clientKey},
			PluginsEnabled:            &pluginsInit,
		},
	})
	if _, err := st.SQLDB().Exec(`insert into settings(key, value, updated_at_ms) values(?, ?, ?)`, upgradeImportDestinationSettingKey, string(destPayload), time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetEmbeddedRuntimeDesiredLifecycle(t.Context(), model.EmbeddedRuntimeDesiredRunning); err != nil {
		t.Fatal(err)
	}

	rt := &upgradeCanonicalTestRuntime{
		status: model.RuntimeObservedStatus{
			Identity:           "rt-managed-upg",
			Generation:         19,
			ProtocolVersion:    "v1",
			State:              model.RuntimeStateReady,
			CPAObservedVersion: "v8.0.55",
			Capabilities:       model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStop, model.RuntimeCapabilityRestart},
			ActiveGatewayArtifact: &model.ActiveGatewayArtifact{
				Engine:     "cpa",
				ArtifactID: qualifiedArtifact,
				Version:    "v8.0.55",
			},
		},
	}

	manager := collector.NewManager(cfg, st)
	t.Cleanup(func() { manager.Stop() })
	serverApp := httpapi.New(cfg, st, manager)
	var canonical *upgradeCanonicalServer
	if err := attachCompletedUpgradeRouting(t.Context(), cfg, st, serverApp, func(context.Context) bool { return true }, upgradeCanonicalDependencies{
		Protector:    protector,
		Runtime:      rt,
		CanonicalOut: &canonical,
	}); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting: %v", err)
	}
	if canonical == nil {
		t.Fatal("expected upgradeCanonicalServer instance")
	}
	hits = nil

	doCanonical := func(method, target, body, auth string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		canonical.ServeHTTP(rec, req)
		return rec
	}

	const upgBase = "/upg-console/api/v2/"
	for idx, tc := range []struct {
		method    string
		resource  string
		body      string
		wantPath  string
		wantQuery string
	}{
		{
			method:   http.MethodGet,
			resource: "cpamp/v8/management/credentials",
			wantPath: "/v8/management/credentials",
		},
		{
			method:   http.MethodPost,
			resource: "cpamp/v8/management/oauth/siwc/bind",
			body:     `{"name":"codex-upg.json"}`,
			wantPath: "/v8/management/oauth/siwc/bind",
		},
		{
			method:   http.MethodPost,
			resource: "cpamp/v8/management/oauth/callback",
			body:     `{"provider":"codex","redirect_url":"http://127.0.0.1:1455/auth/callback?state=st_upg&code=cd_upg"}`,
			wantPath: "/v8/management/oauth/callback",
		},
		{
			method:    http.MethodGet,
			resource:  "cpamp/v8/management/oauth/status?state=st_upg",
			wantPath:  "/v8/management/oauth/status",
			wantQuery: "state=st_upg",
		},
		{
			method:    http.MethodDelete,
			resource:  "cpamp/v8/management/oauth/session?state=st_upg",
			wantPath:  "/v8/management/oauth/session",
			wantQuery: "state=st_upg",
		},
		{
			method:    http.MethodDelete,
			resource:  "cpamp/v0/management/oauth-session?state=st_v0",
			wantPath:  "/v0/management/oauth-session",
			wantQuery: "state=st_v0",
		},
	} {
		res := doCanonical(tc.method, upgBase+tc.resource, tc.body, "Bearer "+adminKey, nil)
		if res.Code != http.StatusOK {
			t.Fatalf("%s %s = %d %s, want 200", tc.method, tc.resource, res.Code, res.Body.String())
		}
		if len(hits) != idx+1 {
			t.Fatalf("hits len = %d, want %d", len(hits), idx+1)
		}
		got := hits[idx]
		if got.method != tc.method || got.path != tc.wantPath || got.query != tc.wantQuery || got.body != tc.body {
			t.Fatalf("hit %d = %#v, want method=%q path=%q query=%q body=%q", idx, got, tc.method, tc.wantPath, tc.wantQuery, tc.body)
		}
	}

	before := len(hits)
	crossSite := doCanonical(http.MethodPost, upgBase+"cpamp/v8/management/oauth/callback", `{}`, "Bearer "+adminKey, map[string]string{
		"Sec-Fetch-Site": "cross-site",
		"Sec-Fetch-Dest": "empty",
	})
	if crossSite.Code != http.StatusForbidden {
		t.Fatalf("cross-site upgrade callback = %d, want 403", crossSite.Code)
	}
	unauth := doCanonical(http.MethodGet, upgBase+"cpamp/v8/management/credentials", "", "", nil)
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated upgrade credentials = %d, want 401", unauth.Code)
	}
	for _, blocked := range []struct {
		method string
		target string
	}{
		{http.MethodPost, upgBase + "cpamp/v8/management/credentials"},
		{http.MethodGet, upgBase + "cpamp/v8/management/%63redentials"},
		{http.MethodGet, upgBase + "cpamp/v8/management/oauth/%73tatus"},
		{http.MethodGet, upgBase + "cpamp/v0/management/oauth-session?state=st_v0"},
	} {
		res := doCanonical(blocked.method, blocked.target, "", "Bearer "+adminKey, nil)
		if res.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d %s, want 404", blocked.method, blocked.target, res.Code, res.Body.String())
		}
	}
	if len(hits) != before {
		t.Fatalf("rejected upgrade requests reached CPA %d times, want 0", len(hits)-before)
	}
}

func TestSanitizeBridgedConfigYAMLPreservesNullV8APIKeysWhileScrubbingAccessAPIKeys(t *testing.T) {
	raw := []byte("config-version: 8\napi-keys:\naccess:\n  api-keys:\n    - sk-client\n")
	sanitized, ct, err := sanitizeBridgedConfigYAML(raw)
	if err != nil {
		t.Fatalf("sanitizeBridgedConfigYAML: %v", err)
	}
	if !strings.HasPrefix(ct, "application/yaml") {
		t.Fatalf("content-type = %q, want application/yaml", ct)
	}
	if strings.Contains(string(sanitized), "sk-client") {
		t.Fatalf("sanitized YAML retained client secret:\n%s", string(sanitized))
	}
	var decoded map[string]any
	if err := yaml.Unmarshal(sanitized, &decoded); err != nil {
		t.Fatalf("unmarshal sanitized YAML: %v\n%s", err, string(sanitized))
	}
	if decoded["config-version"] != 8 {
		t.Fatalf("config-version = %#v, want 8", decoded["config-version"])
	}
	apiKeysVal, hasAPIKeys := decoded["api-keys"]
	if !hasAPIKeys || apiKeysVal != nil {
		t.Fatalf("top-level api-keys = %#v (present=%v), want nil\n%s", apiKeysVal, hasAPIKeys, string(sanitized))
	}
	accessMap, ok := decoded["access"].(map[string]any)
	if !ok {
		t.Fatalf("access = %#v, want map[string]any", decoded["access"])
	}
	accessKeys, ok := accessMap["api-keys"].([]any)
	if !ok || len(accessKeys) != 0 {
		t.Fatalf("access.api-keys = %#v, want empty slice", accessMap["api-keys"])
	}
}

func TestFreshCanonicalLegacyBridgeForwardsMetaAPIKeyToCPA(t *testing.T) {
	var forwarded atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+externalAuthorityManagement {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/v0/management/meta-api-key" {
			forwarded.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"meta-api-key":[{"api-key":"sk-meta-1","base-url":"https://meta.example/v1"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)

	app := newExternalApp(t, srv.URL, externalAuthorityManagement)
	app.routing = freshrouting.New(app.store, activeAdoptRoutingDriver{store: app.store})

	req := httptest.NewRequest(http.MethodGet, currentInstallManagementAPIV2Prefix+"cpamp/v0/management/meta-api-key", nil)
	req.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	rr := httptest.NewRecorder()
	app.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("GET /api/v2/cpamp/v0/management/meta-api-key = %d %s, want 200", rr.Code, rr.Body.String())
	}
	if got := forwarded.Load(); got != 1 {
		t.Fatalf("upstream CPA forwarded count = %d, want 1", got)
	}
	if !strings.Contains(rr.Body.String(), "sk-meta-1") {
		t.Fatalf("unexpected response body: %s", rr.Body.String())
	}
}
