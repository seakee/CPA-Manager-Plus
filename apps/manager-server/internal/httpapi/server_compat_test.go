package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func newCompatHandler(t *testing.T, cfg config.Config, setup *store.Setup) (http.Handler, *store.Store) {
	t.Helper()
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(t.TempDir(), "usage.sqlite")
	}
	if cfg.Queue == "" {
		cfg.Queue = "usage"
	}
	if cfg.PopSide == "" {
		cfg.PopSide = "right"
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = 100
	}
	if cfg.QueryLimit == 0 {
		cfg.QueryLimit = 50000
	}
	if len(cfg.CORSOrigins) == 0 {
		cfg.CORSOrigins = []string{"*"}
	}
	if cfg.CollectorMode == "" {
		cfg.CollectorMode = "auto"
	}

	db := testutil.NewStore(t, cfg)
	if setup != nil {
		if err := db.SaveSetup(context.Background(), *setup); err != nil {
			t.Fatalf("save setup: %v", err)
		}
	}
	manager := collector.NewManager(cfg, db)
	return New(cfg, db, manager).Handler(), db
}

func TestServerCompatHealthInfoAndPanel(t *testing.T) {
	cfg := testutil.NewConfig(t)
	handler, _ := newCompatHandler(t, cfg, nil)

	healthRR := testutil.Request(t, handler, http.MethodGet, "/health", "", "")
	testutil.RequireStatus(t, healthRR, http.StatusOK)
	var health struct {
		OK      bool   `json:"ok"`
		Service string `json:"service"`
	}
	testutil.DecodeJSON(t, healthRR, &health)
	if !health.OK || health.Service == "" {
		t.Fatalf("health response = %#v", health)
	}

	infoRR := testutil.Request(t, handler, http.MethodGet, "/usage-service/info", "", "")
	testutil.RequireStatus(t, infoRR, http.StatusOK)
	var info struct {
		Service    string `json:"service"`
		Mode       string `json:"mode"`
		StartedAt  int64  `json:"startedAt"`
		Configured bool   `json:"configured"`
	}
	testutil.DecodeJSON(t, infoRR, &info)
	if info.Service != serviceID || info.Mode != "embedded" || info.StartedAt <= 0 || info.Configured {
		t.Fatalf("info response = %#v", info)
	}

	rootRR := testutil.Request(t, handler, http.MethodGet, "/", "", "")
	testutil.RequireStatus(t, rootRR, http.StatusTemporaryRedirect)
	if rootRR.Header().Get("Location") != "/management.html" {
		t.Fatalf("root location = %q", rootRR.Header().Get("Location"))
	}

	panelRR := testutil.Request(t, handler, http.MethodGet, "/management.html", "", "")
	testutil.RequireStatus(t, panelRR, http.StatusOK)
	if !strings.Contains(panelRR.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("panel content type = %q", panelRR.Header().Get("Content-Type"))
	}
	if !strings.Contains(strings.ToLower(panelRR.Body.String()), "<html") {
		t.Fatalf("panel body does not look like html")
	}
}

func TestServerCompatPanelPathOverridesEmbeddedPanel(t *testing.T) {
	cfg := testutil.NewConfig(t)
	panelPath := filepath.Join(t.TempDir(), "management.html")
	if err := osWriteFile(panelPath, []byte("<html><body>custom panel</body></html>")); err != nil {
		t.Fatalf("write panel: %v", err)
	}
	cfg.PanelPath = panelPath
	handler, _ := newCompatHandler(t, cfg, nil)

	rr := testutil.Request(t, handler, http.MethodGet, "/management.html", "", "")
	testutil.RequireStatus(t, rr, http.StatusOK)
	if rr.Body.String() != "<html><body>custom panel</body></html>" {
		t.Fatalf("panel body = %q", rr.Body.String())
	}
}

func TestServerCompatSetupConfigAndEnvLock(t *testing.T) {
	cpa := testutil.NewCPAMock(t)
	cfg := testutil.NewConfig(t)
	handler, db := newCompatHandler(t, cfg, nil)

	setupBody := `{"cpaBaseUrl":"` + cpa.URL() + `","managementKey":"management-key","requestMonitoringEnabled":false,"ensureUsageStatisticsEnabled":false}`
	setupRR := testutil.Request(t, handler, http.MethodPost, "/setup", setupBody, testutil.AdminKey)
	testutil.RequireStatus(t, setupRR, http.StatusOK)
	if !strings.Contains(setupRR.Body.String(), `"ok":true`) || !strings.Contains(setupRR.Body.String(), cpa.URL()) {
		t.Fatalf("setup body = %s", setupRR.Body.String())
	}

	infoRR := testutil.Request(t, handler, http.MethodGet, "/usage-service/info", "", "")
	testutil.RequireStatus(t, infoRR, http.StatusOK)
	var info struct {
		Configured bool `json:"configured"`
	}
	testutil.DecodeJSON(t, infoRR, &info)
	if !info.Configured {
		t.Fatalf("configured = false after setup")
	}
	state, ok, err := db.LoadBootstrapState(context.Background())
	if err != nil || !ok {
		t.Fatalf("load bootstrap state ok=%v err=%v", ok, err)
	}
	if !state.ProjectInitialized || !state.AdminReady || !state.DataKeyReady || state.Status != "ready" {
		t.Fatalf("bootstrap state after setup = %#v", state)
	}

	configRR := testutil.Request(t, handler, http.MethodGet, "/usage-service/config", "", testutil.AdminKey)
	testutil.RequireStatus(t, configRR, http.StatusOK)
	if !strings.Contains(configRR.Body.String(), `"source":"db"`) ||
		!strings.Contains(configRR.Body.String(), `"cpaBaseUrl":"`+cpa.URL()+`"`) ||
		!strings.Contains(configRR.Body.String(), `"cpaUsage"`) {
		t.Fatalf("config body = %s", configRR.Body.String())
	}

	updateBody := `{"config":{"cpaConnection":{"cpaBaseUrl":"` + cpa.URL() + `","managementKey":"management-key"},"collector":{"enabled":false,"collectorMode":"auto","queue":"usage","popSide":"right","batchSize":100,"pollIntervalMs":500,"queryLimit":50000},"externalUsageService":{"enabled":true,"serviceBase":"http://usage.local"}}}`
	updateRR := testutil.Request(t, handler, http.MethodPut, "/usage-service/config", updateBody, testutil.AdminKey)
	testutil.RequireStatus(t, updateRR, http.StatusOK)
	if !strings.Contains(updateRR.Body.String(), `"externalUsageService":{"enabled":false}`) ||
		strings.Contains(updateRR.Body.String(), "http://usage.local") {
		t.Fatalf("updated config body = %s", updateRR.Body.String())
	}

	cpa.ManagementKey = "rotated-management-key"
	rotateKeyBody := `{"config":{"cpaConnection":{"cpaBaseUrl":"` + cpa.URL() + `","managementKey":"rotated-management-key"},"collector":{"enabled":false,"collectorMode":"auto","queue":"usage","popSide":"right","batchSize":100,"pollIntervalMs":500,"queryLimit":50000}}}`
	rotateKeyRR := testutil.Request(t, handler, http.MethodPut, "/usage-service/config", rotateKeyBody, testutil.AdminKey)
	testutil.RequireStatus(t, rotateKeyRR, http.StatusOK)
	if !strings.Contains(rotateKeyRR.Body.String(), `"cpaBaseUrl":"`+cpa.URL()+`"`) {
		t.Fatalf("rotated key config body = %s", rotateKeyRR.Body.String())
	}
	rotatedSetup, ok, err := db.LoadSetup(context.Background())
	if err != nil || !ok {
		t.Fatalf("load rotated setup ok=%v err=%v", ok, err)
	}
	if rotatedSetup.CPAUpstreamURL != cpa.URL() || rotatedSetup.ManagementKey != "rotated-management-key" {
		t.Fatalf("rotated setup = %#v", rotatedSetup)
	}

	otherCPA := testutil.NewCPAMock(t)
	otherCPA.ManagementKey = "other-key"
	rebindBody := `{"config":{"cpaConnection":{"cpaBaseUrl":"` + otherCPA.URL() + `","managementKey":"other-key"},"collector":{"enabled":false}}}`
	rebindRR := testutil.Request(t, handler, http.MethodPut, "/usage-service/config", rebindBody, testutil.AdminKey)
	testutil.RequireStatus(t, rebindRR, http.StatusOK)
	if !strings.Contains(rebindRR.Body.String(), `"cpaBaseUrl":"`+otherCPA.URL()+`"`) {
		t.Fatalf("rebind body = %s", rebindRR.Body.String())
	}
	reboundSetup, ok, err := db.LoadSetup(context.Background())
	if err != nil || !ok {
		t.Fatalf("load rebound setup ok=%v err=%v", ok, err)
	}
	if reboundSetup.CPAUpstreamURL != otherCPA.URL() || reboundSetup.ManagementKey != "other-key" {
		t.Fatalf("rebound setup = %#v", reboundSetup)
	}

	envCfg := testutil.NewConfig(t)
	envCfg.CPAUpstreamURL = cpa.URL()
	envCfg.ManagementKey = "management-key"
	envHandler, _ := newCompatHandler(t, envCfg, nil)
	conflictBody := `{"config":{"cpaConnection":{"cpaBaseUrl":"http://other.local","managementKey":"other-key"},"collector":{"enabled":false}}}`
	conflictRR := testutil.Request(t, envHandler, http.MethodPut, "/usage-service/config", conflictBody, testutil.AdminKey)
	testutil.RequireStatus(t, conflictRR, http.StatusConflict)
	if !strings.Contains(conflictRR.Body.String(), `"code":"connection_env_managed"`) {
		t.Fatalf("conflict body = %s", conflictRR.Body.String())
	}
}

func TestServerCompatInfoIgnoresStaleUninitializedBootstrapState(t *testing.T) {
	cpa := testutil.NewCPAMock(t)
	setup := &store.Setup{CPAUpstreamURL: cpa.URL(), ManagementKey: "management-key", Queue: "usage", PopSide: "right"}
	handler, db := newCompatHandler(t, testutil.NewConfig(t), setup)
	if err := db.SaveBootstrapState(context.Background(), store.BootstrapState{
		Version:            1,
		Status:             "fresh",
		AdminReady:         true,
		ProjectInitialized: false,
		DataKeyReady:       true,
	}); err != nil {
		t.Fatalf("save stale bootstrap state: %v", err)
	}

	infoRR := testutil.Request(t, handler, http.MethodGet, "/usage-service/info", "", "")
	testutil.RequireStatus(t, infoRR, http.StatusOK)
	var info struct {
		Configured         bool `json:"configured"`
		ProjectInitialized bool `json:"projectInitialized"`
		SetupRequired      bool `json:"setupRequired"`
	}
	testutil.DecodeJSON(t, infoRR, &info)
	if !info.Configured || !info.ProjectInitialized || info.SetupRequired {
		t.Fatalf("info response = %#v", info)
	}
}

func TestServerCompatAccountProcessingPolicyPatchReloadsRuntime(t *testing.T) {
	cfg := testutil.NewConfig(t)
	db := testutil.NewStore(t, cfg)
	manager := collector.NewManager(cfg, db)
	runtime := &recordingAutomationRuntimeService{}
	server := New(cfg, db, manager, runtime)
	handler := server.Handler()

	body := `{"codexQuotaCooldownEnabled":true,"authIssueQueueEnabled":true,"authIssueAutoDisableEnabled":true}`
	rr := testutil.Request(t, handler, http.MethodPatch, "/usage-service/account-processing-policy", body, testutil.AdminKey)
	testutil.RequireStatus(t, rr, http.StatusOK)
	if runtime.reloadCount != 1 {
		t.Fatalf("reloadCount = %d, want 1", runtime.reloadCount)
	}
	var response struct {
		QuotaCooldown struct {
			Enabled bool   `json:"enabled"`
			Source  string `json:"source"`
		} `json:"codexQuotaCooldown"`
		AccountActionsAutoDisable struct {
			Enabled    bool   `json:"enabled"`
			Configured bool   `json:"configured"`
			Source     string `json:"source"`
		} `json:"authIssueAutoDisable"`
	}
	testutil.DecodeJSON(t, rr, &response)
	if !response.QuotaCooldown.Enabled || response.QuotaCooldown.Source != "database" {
		t.Fatalf("quotaCooldown response = %#v", response.QuotaCooldown)
	}
	if !response.AccountActionsAutoDisable.Enabled || !response.AccountActionsAutoDisable.Configured || response.AccountActionsAutoDisable.Source != "database" {
		t.Fatalf("auto-disable response = %#v", response.AccountActionsAutoDisable)
	}

	getRR := testutil.Request(t, handler, http.MethodGet, "/usage-service/account-processing-policy", "", testutil.AdminKey)
	testutil.RequireStatus(t, getRR, http.StatusOK)
	if !strings.Contains(getRR.Body.String(), `"source":"database"`) {
		t.Fatalf("expected persisted database source, body = %s", getRR.Body.String())
	}

	if err := db.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	runtimeSettings := server.AppContext().AccountProcessingPolicyService.RuntimeSettings(context.Background())
	if !runtimeSettings.QuotaCooldownEnabled || !runtimeSettings.AccountActionsEnabled || !runtimeSettings.AccountActionsAutoDisable {
		t.Fatalf("runtime settings should use the PATCH-updated cache after load failure, got %#v", runtimeSettings)
	}
}

func TestServerCompatAccountProcessingPolicyPatchRejectsEnvLockedField(t *testing.T) {
	cfg := testutil.NewConfig(t)
	cfg.QuotaCooldownEnvSet = true
	db := testutil.NewStore(t, cfg)
	handler := New(cfg, db, collector.NewManager(cfg, db)).Handler()

	rr := testutil.Request(t, handler, http.MethodPatch, "/usage-service/account-processing-policy", `{"codexQuotaCooldownEnabled":true}`, testutil.AdminKey)
	testutil.RequireStatus(t, rr, http.StatusConflict)
	if !strings.Contains(rr.Body.String(), `"code":"account_processing_policy_env_locked"`) {
		t.Fatalf("expected env locked error code, body = %s", rr.Body.String())
	}
}

func TestServerCompatCPAPanelKeyCannotUseManagerOnlyRoutes(t *testing.T) {
	cpa := testutil.NewCPAMock(t)
	cfg := testutil.NewConfig(t)
	handler, db := newCompatHandler(t, cfg, nil)

	openConfigRR := testutil.Request(t, handler, http.MethodGet, "/usage-service/config", "", "")
	testutil.RequireStatus(t, openConfigRR, http.StatusOK)

	configBody := `{"config":{"cpaConnection":{"cpaBaseUrl":"` + cpa.URL() + `","managementKey":"management-key"},"collector":{"enabled":false,"collectorMode":"auto","queue":"usage","popSide":"right","batchSize":100,"pollIntervalMs":500,"queryLimit":50000},"externalUsageService":{"enabled":true,"serviceBase":"http://usage.local"}}}`
	saveRR := testutil.Request(t, handler, http.MethodPut, "/usage-service/config", configBody, testutil.AdminKey)
	testutil.RequireStatus(t, saveRR, http.StatusOK)
	if !strings.Contains(saveRR.Body.String(), `"externalUsageService":{"enabled":false}`) ||
		strings.Contains(saveRR.Body.String(), "http://usage.local") {
		t.Fatalf("save body = %s", saveRR.Body.String())
	}

	cpaKeyConfigRR := testutil.Request(t, handler, http.MethodGet, "/usage-service/config", "", "management-key")
	testutil.RequireStatus(t, cpaKeyConfigRR, http.StatusUnauthorized)
	if !strings.Contains(cpaKeyConfigRR.Body.String(), `"code":"invalid_admin_key"`) {
		t.Fatalf("CPA key config body = %s", cpaKeyConfigRR.Body.String())
	}

	configRR := testutil.Request(t, handler, http.MethodGet, "/usage-service/config", "", testutil.AdminKey)
	testutil.RequireStatus(t, configRR, http.StatusOK)
	if !strings.Contains(configRR.Body.String(), `"source":"db"`) ||
		!strings.Contains(configRR.Body.String(), `"cpaBaseUrl":"`+cpa.URL()+`"`) {
		t.Fatalf("config body = %s", configRR.Body.String())
	}

	if _, err := db.InsertEvents(context.Background(), []usage.Event{compatEvent("external-panel-usage", 10)}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	usageRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage", "", "management-key")
	testutil.RequireStatus(t, usageRR, http.StatusUnauthorized)
	if !strings.Contains(usageRR.Body.String(), `"code":"invalid_admin_key"`) {
		t.Fatalf("usage body = %s", usageRR.Body.String())
	}

	proxyRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/config", "", "management-key")
	testutil.RequireStatus(t, proxyRR, http.StatusUnauthorized)
	if !strings.Contains(proxyRR.Body.String(), `"code":"invalid_admin_key"`) {
		t.Fatalf("proxy body = %s", proxyRR.Body.String())
	}
}

func TestServerCompatStatusAuthAndCounts(t *testing.T) {
	cfg := testutil.NewConfig(t)
	unconfiguredHandler, _ := newCompatHandler(t, cfg, nil)
	openRR := testutil.Request(t, unconfiguredHandler, http.MethodGet, "/status", "", "")
	testutil.RequireStatus(t, openRR, http.StatusUnauthorized)
	authorizedOpenRR := testutil.Request(t, unconfiguredHandler, http.MethodGet, "/status", "", testutil.AdminKey)
	testutil.RequireStatus(t, authorizedOpenRR, http.StatusOK)

	cpa := testutil.NewCPAMock(t)
	setup := &store.Setup{CPAUpstreamURL: cpa.URL(), ManagementKey: "management-key", Queue: "usage", PopSide: "right"}
	configuredHandler, db := newCompatHandler(t, testutil.NewConfig(t), setup)
	if err := db.AddDeadLetter(context.Background(), `{"bad":true}`, errors.New("parse failed")); err != nil {
		t.Fatalf("add dead letter: %v", err)
	}
	_, err := db.InsertEvents(context.Background(), []usage.Event{compatEvent("status-event", 1)})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}

	unauthorizedRR := testutil.Request(t, configuredHandler, http.MethodGet, "/status", "", "")
	testutil.RequireStatus(t, unauthorizedRR, http.StatusUnauthorized)

	statusRR := testutil.Request(t, configuredHandler, http.MethodGet, "/status", "", testutil.AdminKey)
	testutil.RequireStatus(t, statusRR, http.StatusOK)
	if !strings.Contains(statusRR.Body.String(), `"events":1`) ||
		!strings.Contains(statusRR.Body.String(), `"deadLetters":1`) ||
		!strings.Contains(statusRR.Body.String(), `"collector"`) {
		t.Fatalf("status body = %s", statusRR.Body.String())
	}
}

func TestServerCompatUsageRoutes(t *testing.T) {
	cpa := testutil.NewCPAMock(t)
	setup := &store.Setup{CPAUpstreamURL: cpa.URL(), ManagementKey: "management-key", Queue: "usage", PopSide: "right"}
	handler, db := newCompatHandler(t, testutil.NewConfig(t), setup)

	emptyRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage", "", testutil.AdminKey)
	testutil.RequireStatus(t, emptyRR, http.StatusOK)
	if !strings.Contains(emptyRR.Body.String(), `"total_requests":0`) {
		t.Fatalf("empty usage body = %s", emptyRR.Body.String())
	}

	_, err := db.InsertEvents(context.Background(), []usage.Event{compatEvent("usage-event-1", 10)})
	if err != nil {
		t.Fatalf("insert usage event: %v", err)
	}
	usageRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage", "", testutil.AdminKey)
	testutil.RequireStatus(t, usageRR, http.StatusOK)
	if !strings.Contains(usageRR.Body.String(), `"total_requests":1`) ||
		!strings.Contains(usageRR.Body.String(), `"gpt-test"`) {
		t.Fatalf("usage body = %s", usageRR.Body.String())
	}

	exportRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage/export", "", testutil.AdminKey)
	testutil.RequireStatus(t, exportRR, http.StatusOK)
	if !strings.Contains(exportRR.Header().Get("Content-Type"), "application/x-ndjson") ||
		!strings.Contains(exportRR.Body.String(), `"event_hash":"usage-event-1"`) {
		t.Fatalf("export content type = %q body = %s", exportRR.Header().Get("Content-Type"), exportRR.Body.String())
	}

	importLine := `{"event_hash":"usage-event-2","timestamp_ms":1778000001000,"timestamp":"2026-05-06T00:00:01Z","model":"gpt-test","endpoint":"POST /v1/chat/completions","input_tokens":2,"output_tokens":3,"total_tokens":5,"failed":false}`
	importRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/import", importLine+"\n", testutil.AdminKey)
	testutil.RequireStatus(t, importRR, http.StatusOK)
	if !strings.Contains(importRR.Body.String(), `"format":"usage_service_jsonl"`) ||
		!strings.Contains(importRR.Body.String(), `"added":1`) {
		t.Fatalf("import body = %s", importRR.Body.String())
	}
}

func TestServerCompatDashboardSummary(t *testing.T) {
	cpa := testutil.NewCPAMock(t)
	setup := &store.Setup{CPAUpstreamURL: cpa.URL(), ManagementKey: "management-key", Queue: "usage", PopSide: "right"}
	handler, db := newCompatHandler(t, testutil.NewConfig(t), setup)
	todayStart := int64(1_778_000_000_000)
	nowMS := todayStart + 60_000
	latency := int64(88)

	if err := db.SaveModelPrices(context.Background(), map[string]store.ModelPrice{
		"gpt-test": {Prompt: 1, Completion: 2, Cache: 0.5},
	}); err != nil {
		t.Fatalf("save model prices: %v", err)
	}
	success := compatEvent("dashboard-success", 10)
	success.LatencyMS = &latency
	failure := compatEvent("dashboard-failure", 20)
	failure.Failed = true
	_, err := db.InsertEvents(context.Background(), []usage.Event{success, failure})
	if err != nil {
		t.Fatalf("insert events: %v", err)
	}

	unauthorizedRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/dashboard/summary?today_start_ms=1778000000000", "", "")
	testutil.RequireStatus(t, unauthorizedRR, http.StatusUnauthorized)

	badRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/dashboard/summary", "", testutil.AdminKey)
	testutil.RequireStatus(t, badRR, http.StatusBadRequest)

	target := "/v0/management/dashboard/summary?today_start_ms=1778000000000&now_ms=" + strconv.FormatInt(nowMS, 10)
	rr := testutil.Request(t, handler, http.MethodGet, target, "", testutil.AdminKey)
	testutil.RequireStatus(t, rr, http.StatusOK)
	var payload struct {
		Today struct {
			TotalCalls       int64    `json:"total_calls"`
			SuccessCalls     int64    `json:"success_calls"`
			FailureCalls     int64    `json:"failure_calls"`
			AverageLatencyMS *float64 `json:"average_latency_ms"`
		} `json:"today"`
		TopModelsToday []struct {
			Model string `json:"model"`
			Calls int64  `json:"calls"`
		} `json:"top_models_today"`
		RecentFailures []struct {
			Model string `json:"model"`
		} `json:"recent_failures"`
	}
	testutil.DecodeJSON(t, rr, &payload)
	if payload.Today.TotalCalls != 2 || payload.Today.SuccessCalls != 1 || payload.Today.FailureCalls != 1 ||
		payload.Today.AverageLatencyMS == nil || *payload.Today.AverageLatencyMS != 88 {
		t.Fatalf("dashboard summary = %#v", payload.Today)
	}
	if len(payload.TopModelsToday) != 1 || payload.TopModelsToday[0].Model != "gpt-test" || payload.TopModelsToday[0].Calls != 2 {
		t.Fatalf("top models = %#v", payload.TopModelsToday)
	}
	if len(payload.RecentFailures) != 1 || payload.RecentFailures[0].Model != "gpt-test" {
		t.Fatalf("recent failures = %#v", payload.RecentFailures)
	}
}

func TestServerCompatMonitoringAnalytics(t *testing.T) {
	cpa := testutil.NewCPAMock(t)
	setup := &store.Setup{CPAUpstreamURL: cpa.URL(), ManagementKey: "management-key", Queue: "usage", PopSide: "right"}
	handler, db := newCompatHandler(t, testutil.NewConfig(t), setup)
	event := compatEvent("monitoring-analytics-event", 10)
	_, err := db.InsertEvents(context.Background(), []usage.Event{event})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}

	unauthorizedRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/monitoring/analytics", `{"from_ms":1778000000000,"to_ms":1778000060000}`, "")
	testutil.RequireStatus(t, unauthorizedRR, http.StatusUnauthorized)

	badRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/monitoring/analytics", `{"from_ms":2,"to_ms":1}`, testutil.AdminKey)
	testutil.RequireStatus(t, badRR, http.StatusBadRequest)

	body := `{"from_ms":1778000000000,"to_ms":1778000060000,"include":{"summary":true,"events_page":{"limit":10},"recent_failures":5}}`
	rr := testutil.Request(t, handler, http.MethodPost, "/v0/management/monitoring/analytics", body, testutil.AdminKey)
	testutil.RequireStatus(t, rr, http.StatusOK)

	var payload struct {
		Summary *struct {
			TotalCalls int64 `json:"total_calls"`
		} `json:"summary"`
		Events *struct {
			Items []struct {
				EventHash string `json:"event_hash"`
			} `json:"items"`
		} `json:"events"`
	}
	testutil.DecodeJSON(t, rr, &payload)
	if payload.Summary == nil || payload.Summary.TotalCalls != 1 {
		t.Fatalf("summary = %#v", payload.Summary)
	}
	if payload.Events == nil || len(payload.Events.Items) != 1 || payload.Events.Items[0].EventHash != "monitoring-analytics-event" {
		t.Fatalf("events = %#v", payload.Events)
	}
}

func TestServerCompatModelPricesAndAliases(t *testing.T) {
	cpa := testutil.NewCPAMock(t)
	setup := &store.Setup{CPAUpstreamURL: cpa.URL(), ManagementKey: "management-key", Queue: "usage", PopSide: "right"}
	handler, _ := newCompatHandler(t, testutil.NewConfig(t), setup)

	priceRR := testutil.Request(t, handler, http.MethodPut, "/v0/management/model-prices", `{"prices":{"gpt-test":{"prompt":1,"completion":2,"cache":0.5}}}`, testutil.AdminKey)
	testutil.RequireStatus(t, priceRR, http.StatusOK)
	loadPriceRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/model-prices", "", testutil.AdminKey)
	testutil.RequireStatus(t, loadPriceRR, http.StatusOK)
	if !strings.Contains(loadPriceRR.Body.String(), `"gpt-test"`) ||
		!strings.Contains(loadPriceRR.Body.String(), `"prompt":1`) {
		t.Fatalf("model prices body = %s", loadPriceRR.Body.String())
	}

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failed", http.StatusInternalServerError)
	}))
	t.Cleanup(source.Close)
	stubModelPriceSyncURLs(t, source.URL, "")
	syncRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/model-prices/sync", `{}`, testutil.AdminKey)
	testutil.RequireStatus(t, syncRR, http.StatusBadGateway)
	if !strings.Contains(syncRR.Body.String(), `"code":"model_price_sync_failed"`) {
		t.Fatalf("sync error body = %s", syncRR.Body.String())
	}

	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	aliasRR := testutil.Request(t, handler, http.MethodPut, "/v0/management/api-key-aliases", `{"items":[{"apiKeyHash":"`+hash+`","alias":"Team A"}]}`, testutil.AdminKey)
	testutil.RequireStatus(t, aliasRR, http.StatusOK)
	loadAliasRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/api-key-aliases", "", testutil.AdminKey)
	testutil.RequireStatus(t, loadAliasRR, http.StatusOK)
	if !strings.Contains(loadAliasRR.Body.String(), `"apiKeyHash":"`+hash+`"`) ||
		!strings.Contains(loadAliasRR.Body.String(), `"alias":"Team A"`) {
		t.Fatalf("aliases body = %s", loadAliasRR.Body.String())
	}
	deleteAliasRR := testutil.Request(t, handler, http.MethodDelete, "/v0/management/api-key-aliases/"+hash, "", testutil.AdminKey)
	testutil.RequireStatus(t, deleteAliasRR, http.StatusOK)
}

func TestServerCompatProxyRoutes(t *testing.T) {
	cpa := testutil.NewCPAMock(t)
	setup := &store.Setup{CPAUpstreamURL: cpa.URL(), ManagementKey: "management-key", Queue: "usage", PopSide: "right"}
	handler, _ := newCompatHandler(t, testutil.NewConfig(t), setup)

	accountsRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/accounts?limit=10", "", testutil.AdminKey)
	testutil.RequireStatus(t, accountsRR, http.StatusOK)
	accountsReq, ok := cpa.LastRequest("/v0/management/accounts")
	if !ok {
		t.Fatal("CPA mock did not receive /v0/management/accounts")
	}
	if accountsReq.Authorization != "Bearer management-key" || accountsReq.Query != "limit=10" {
		t.Fatalf("accounts proxy request = %#v", accountsReq)
	}

	reloadRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/reload", `{"force":true}`, testutil.AdminKey)
	testutil.RequireStatus(t, reloadRR, http.StatusOK)
	reloadReq, ok := cpa.LastRequest("/v0/management/reload")
	if !ok {
		t.Fatal("CPA mock did not receive /v0/management/reload")
	}
	if reloadReq.Authorization != "Bearer management-key" || reloadReq.Body != `{"force":true}` {
		t.Fatalf("reload proxy request = %#v", reloadReq)
	}

	modelsReq := httptest.NewRequest(http.MethodGet, "/v1/models?limit=20", nil)
	modelsReq.Header.Set("Authorization", "Bearer upstream-key")
	modelsRR := httptest.NewRecorder()
	handler.ServeHTTP(modelsRR, modelsReq)
	testutil.RequireStatus(t, modelsRR, http.StatusOK)
	modelsProxyReq, ok := cpa.LastRequest("/v1/models")
	if !ok {
		t.Fatal("CPA mock did not receive /v1/models")
	}
	if modelsProxyReq.Authorization != "Bearer upstream-key" || modelsProxyReq.Query != "limit=20" {
		t.Fatalf("model list proxy request = %#v", modelsProxyReq)
	}
}

func TestServerCompatPluginProxyRoutes(t *testing.T) {
	type observedRequest struct {
		method            string
		path              string
		query             string
		authorization     string
		codexInviteOrigin string
		origin            string
		body              string
	}

	observed := make(chan observedRequest, 9)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		observed <- observedRequest{
			method:            r.Method,
			path:              r.URL.Path,
			query:             r.URL.RawQuery,
			authorization:     r.Header.Get("Authorization"),
			codexInviteOrigin: r.Header.Get("X-Codex-Invite-Origin"),
			origin:            r.Header.Get("Origin"),
			body:              string(body),
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(upstream.Close)

	setup := &store.Setup{
		CPAUpstreamURL: upstream.URL,
		ManagementKey:  "management-key",
		Queue:          "usage",
		PopSide:        "right",
	}
	handler, _ := newCompatHandler(t, testutil.NewConfig(t), setup)

	assertObserved := func(path string, want observedRequest) {
		t.Helper()
		select {
		case got := <-observed:
			if got != want {
				t.Fatalf("%s proxy request = %#v, want %#v", path, got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("CPA upstream did not receive %s", path)
		}
	}
	requestWithHeaders := func(method, target, body, managementKey string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		if managementKey != "" {
			req.Header.Set("Authorization", "Bearer "+managementKey)
		}
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	managementInstallRR := testutil.Request(
		t,
		handler,
		http.MethodPost,
		"/v0/management/plugin-store/demo/install?source=official",
		"",
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, managementInstallRR, http.StatusOK)
	assertObserved("/v0/management/plugin-store/demo/install", observedRequest{
		method:        http.MethodPost,
		path:          "/v0/management/plugin-store/demo/install",
		query:         "source=official",
		authorization: "Bearer management-key",
	})

	pluginManagementRR := testutil.Request(
		t,
		handler,
		http.MethodPatch,
		"/v0/management/plugins/demo/custom?mode=full",
		`{"refresh":true}`,
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, pluginManagementRR, http.StatusOK)
	assertObserved("/v0/management/plugins/demo/custom", observedRequest{
		method:        http.MethodPatch,
		path:          "/v0/management/plugins/demo/custom",
		query:         "mode=full",
		authorization: "Bearer management-key",
		body:          `{"refresh":true}`,
	})

	pluginDynamicManagementRR := requestWithHeaders(
		http.MethodGet,
		"/v0/management/codex-invite/accounts",
		"",
		"plugin-management-key",
		map[string]string{
			"Origin":                "http://localhost:18317",
			"X-Codex-Invite-Origin": "http://localhost:18317",
		},
	)
	testutil.RequireStatus(t, pluginDynamicManagementRR, http.StatusOK)
	assertObserved("/v0/management/codex-invite/accounts", observedRequest{
		method:            http.MethodGet,
		path:              "/v0/management/codex-invite/accounts",
		authorization:     "Bearer plugin-management-key",
		codexInviteOrigin: upstream.URL,
		origin:            "http://localhost:18317",
	})

	pluginDynamicInviteRR := requestWithHeaders(
		http.MethodPost,
		"/v0/management/codex-invite/invite",
		`{"management_origin":"http://localhost:18317","refresh":true}`,
		"plugin-management-key",
		map[string]string{
			"Content-Type":          "application/json",
			"X-Codex-Invite-Origin": "http://localhost:18317",
		},
	)
	testutil.RequireStatus(t, pluginDynamicInviteRR, http.StatusOK)
	assertObserved("/v0/management/codex-invite/invite", observedRequest{
		method:            http.MethodPost,
		path:              "/v0/management/codex-invite/invite",
		authorization:     "Bearer plugin-management-key",
		codexInviteOrigin: upstream.URL,
		body:              `{"management_origin":"` + upstream.URL + `","refresh":true}`,
	})

	resourcePostNoAuthRR := testutil.Request(
		t,
		handler,
		http.MethodPost,
		"/v0/resource/plugins/codex-invite/invite",
		`{"managementKey":"plugin-key"}`,
		"",
	)
	testutil.RequireStatus(t, resourcePostNoAuthRR, http.StatusOK)
	assertObserved("/v0/resource/plugins/codex-invite/invite", observedRequest{
		method: http.MethodPost,
		path:   "/v0/resource/plugins/codex-invite/invite",
		body:   `{"managementKey":"plugin-key"}`,
	})

	resourcePostCallerAuthRR := requestWithHeaders(
		http.MethodPost,
		"/v0/resource/plugins/codex-invite/invite",
		`{"refresh":true}`,
		"plugin-management-key",
		map[string]string{
			"X-Codex-Invite-Origin": "http://localhost:18317",
		},
	)
	testutil.RequireStatus(t, resourcePostCallerAuthRR, http.StatusOK)
	assertObserved("/v0/resource/plugins/codex-invite/invite", observedRequest{
		method:            http.MethodPost,
		path:              "/v0/resource/plugins/codex-invite/invite",
		authorization:     "Bearer plugin-management-key",
		codexInviteOrigin: upstream.URL,
		body:              `{"refresh":true}`,
	})

	resourcePostRR := testutil.Request(
		t,
		handler,
		http.MethodPost,
		"/v0/resource/plugins/codex-invite/invite",
		`{"refresh":true}`,
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, resourcePostRR, http.StatusOK)
	assertObserved("/v0/resource/plugins/codex-invite/invite", observedRequest{
		method:        http.MethodPost,
		path:          "/v0/resource/plugins/codex-invite/invite",
		authorization: "Bearer management-key",
		body:          `{"refresh":true}`,
	})

	resourcePutRR := testutil.Request(
		t,
		handler,
		http.MethodPut,
		"/v0/resource/plugins/codex-invite/invite",
		`{"key":"value"}`,
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, resourcePutRR, http.StatusOK)
	assertObserved("/v0/resource/plugins/codex-invite/invite", observedRequest{
		method:        http.MethodPut,
		path:          "/v0/resource/plugins/codex-invite/invite",
		authorization: "Bearer management-key",
		body:          `{"key":"value"}`,
	})

	resourceTraceRR := testutil.Request(
		t,
		handler,
		http.MethodTrace,
		"/v0/resource/plugins/codex-invite/invite",
		"",
		"",
	)
	testutil.RequireStatus(t, resourceTraceRR, http.StatusMethodNotAllowed)

	resourceRR := requestWithHeaders(
		http.MethodGet,
		"/v0/resource/plugins/codex-invite/invite",
		"",
		"",
		map[string]string{
			"X-Codex-Invite-Origin": "http://localhost:18317",
		},
	)
	testutil.RequireStatus(t, resourceRR, http.StatusOK)
	assertObserved("/v0/resource/plugins/codex-invite/invite", observedRequest{
		method:            http.MethodGet,
		path:              "/v0/resource/plugins/codex-invite/invite",
		authorization:     "Bearer management-key",
		codexInviteOrigin: upstream.URL,
	})
}

type recordingAutomationRuntimeService struct {
	reloadCount int
}

func (s *recordingAutomationRuntimeService) Reload(context.Context) error {
	s.reloadCount++
	return nil
}

func compatEvent(hash string, offset int64) usage.Event {
	return usage.Event{
		EventHash:    hash,
		TimestampMS:  1_778_000_000_000 + offset,
		Timestamp:    time.UnixMilli(1_778_000_000_000 + offset).UTC().Format(time.RFC3339Nano),
		Model:        "gpt-test",
		Endpoint:     "POST /v1/chat/completions",
		Method:       "POST",
		Path:         "/v1/chat/completions",
		AuthIndex:    "auth-1",
		Source:       "user@example.com",
		InputTokens:  1,
		OutputTokens: 2,
		TotalTokens:  3,
		CreatedAtMS:  1_778_000_000_100 + offset,
	}
}

func osWriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}
