package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagearchive"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	freshstrategy "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
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
