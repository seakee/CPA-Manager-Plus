package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/collector"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/config"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/store"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/testutil"
	"github.com/seakee/cpa-manager-plus/usage-service/internal/usage"
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
	handler, _ := newCompatHandler(t, cfg, nil)

	setupBody := `{"cpaBaseUrl":"` + cpa.URL() + `","managementKey":"management-key","requestMonitoringEnabled":false,"ensureUsageStatisticsEnabled":false}`
	setupRR := testutil.Request(t, handler, http.MethodPost, "/setup", setupBody, "")
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

	configRR := testutil.Request(t, handler, http.MethodGet, "/usage-service/config", "", "management-key")
	testutil.RequireStatus(t, configRR, http.StatusOK)
	if !strings.Contains(configRR.Body.String(), `"source":"db"`) ||
		!strings.Contains(configRR.Body.String(), `"cpaBaseUrl":"`+cpa.URL()+`"`) ||
		!strings.Contains(configRR.Body.String(), `"cpaUsage"`) {
		t.Fatalf("config body = %s", configRR.Body.String())
	}

	updateBody := `{"config":{"cpaConnection":{"cpaBaseUrl":"` + cpa.URL() + `","managementKey":"management-key"},"collector":{"enabled":false,"collectorMode":"auto","queue":"usage","popSide":"right","batchSize":100,"pollIntervalMs":500,"queryLimit":50000},"externalUsageService":{"enabled":true,"serviceBase":"http://usage.local"}}}`
	updateRR := testutil.Request(t, handler, http.MethodPut, "/usage-service/config", updateBody, "management-key")
	testutil.RequireStatus(t, updateRR, http.StatusOK)
	if !strings.Contains(updateRR.Body.String(), `"enabled":false`) ||
		!strings.Contains(updateRR.Body.String(), `"serviceBase":"http://usage.local"`) {
		t.Fatalf("updated config body = %s", updateRR.Body.String())
	}

	envCfg := testutil.NewConfig(t)
	envCfg.CPAUpstreamURL = cpa.URL()
	envCfg.ManagementKey = "management-key"
	envHandler, _ := newCompatHandler(t, envCfg, nil)
	conflictBody := `{"config":{"cpaConnection":{"cpaBaseUrl":"http://other.local","managementKey":"other-key"},"collector":{"enabled":false}}}`
	conflictRR := testutil.Request(t, envHandler, http.MethodPut, "/usage-service/config", conflictBody, "management-key")
	testutil.RequireStatus(t, conflictRR, http.StatusConflict)
	if !strings.Contains(conflictRR.Body.String(), `"code":"connection_env_managed"`) {
		t.Fatalf("conflict body = %s", conflictRR.Body.String())
	}
}

func TestServerCompatStatusAuthAndCounts(t *testing.T) {
	cfg := testutil.NewConfig(t)
	unconfiguredHandler, _ := newCompatHandler(t, cfg, nil)
	openRR := testutil.Request(t, unconfiguredHandler, http.MethodGet, "/status", "", "")
	testutil.RequireStatus(t, openRR, http.StatusOK)

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

	statusRR := testutil.Request(t, configuredHandler, http.MethodGet, "/status", "", "management-key")
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

	emptyRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage", "", "management-key")
	testutil.RequireStatus(t, emptyRR, http.StatusOK)
	if !strings.Contains(emptyRR.Body.String(), `"total_requests":0`) {
		t.Fatalf("empty usage body = %s", emptyRR.Body.String())
	}

	_, err := db.InsertEvents(context.Background(), []usage.Event{compatEvent("usage-event-1", 10)})
	if err != nil {
		t.Fatalf("insert usage event: %v", err)
	}
	usageRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage", "", "management-key")
	testutil.RequireStatus(t, usageRR, http.StatusOK)
	if !strings.Contains(usageRR.Body.String(), `"total_requests":1`) ||
		!strings.Contains(usageRR.Body.String(), `"gpt-test"`) {
		t.Fatalf("usage body = %s", usageRR.Body.String())
	}

	exportRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage/export", "", "management-key")
	testutil.RequireStatus(t, exportRR, http.StatusOK)
	if !strings.Contains(exportRR.Header().Get("Content-Type"), "application/x-ndjson") ||
		!strings.Contains(exportRR.Body.String(), `"event_hash":"usage-event-1"`) {
		t.Fatalf("export content type = %q body = %s", exportRR.Header().Get("Content-Type"), exportRR.Body.String())
	}

	importLine := `{"event_hash":"usage-event-2","timestamp_ms":1778000001000,"timestamp":"2026-05-06T00:00:01Z","model":"gpt-test","endpoint":"POST /v1/chat/completions","input_tokens":2,"output_tokens":3,"total_tokens":5,"failed":false}`
	importRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/import", importLine+"\n", "management-key")
	testutil.RequireStatus(t, importRR, http.StatusOK)
	if !strings.Contains(importRR.Body.String(), `"format":"usage_service_jsonl"`) ||
		!strings.Contains(importRR.Body.String(), `"added":1`) {
		t.Fatalf("import body = %s", importRR.Body.String())
	}
}

func TestServerCompatModelPricesAndAliases(t *testing.T) {
	cpa := testutil.NewCPAMock(t)
	setup := &store.Setup{CPAUpstreamURL: cpa.URL(), ManagementKey: "management-key", Queue: "usage", PopSide: "right"}
	handler, _ := newCompatHandler(t, testutil.NewConfig(t), setup)

	priceRR := testutil.Request(t, handler, http.MethodPut, "/v0/management/model-prices", `{"prices":{"gpt-test":{"prompt":1,"completion":2,"cache":0.5}}}`, "management-key")
	testutil.RequireStatus(t, priceRR, http.StatusOK)
	loadPriceRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/model-prices", "", "management-key")
	testutil.RequireStatus(t, loadPriceRR, http.StatusOK)
	if !strings.Contains(loadPriceRR.Body.String(), `"gpt-test"`) ||
		!strings.Contains(loadPriceRR.Body.String(), `"prompt":1`) {
		t.Fatalf("model prices body = %s", loadPriceRR.Body.String())
	}

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failed", http.StatusInternalServerError)
	}))
	t.Cleanup(source.Close)
	oldURL := modelPriceSyncURL
	modelPriceSyncURL = source.URL
	t.Cleanup(func() {
		modelPriceSyncURL = oldURL
	})
	syncRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/model-prices/sync", `{}`, "management-key")
	testutil.RequireStatus(t, syncRR, http.StatusBadGateway)
	if !strings.Contains(syncRR.Body.String(), `"code":"model_price_sync_failed"`) {
		t.Fatalf("sync error body = %s", syncRR.Body.String())
	}

	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	aliasRR := testutil.Request(t, handler, http.MethodPut, "/v0/management/api-key-aliases", `{"items":[{"apiKeyHash":"`+hash+`","alias":"Team A"}]}`, "management-key")
	testutil.RequireStatus(t, aliasRR, http.StatusOK)
	loadAliasRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/api-key-aliases", "", "management-key")
	testutil.RequireStatus(t, loadAliasRR, http.StatusOK)
	if !strings.Contains(loadAliasRR.Body.String(), `"apiKeyHash":"`+hash+`"`) ||
		!strings.Contains(loadAliasRR.Body.String(), `"alias":"Team A"`) {
		t.Fatalf("aliases body = %s", loadAliasRR.Body.String())
	}
	deleteAliasRR := testutil.Request(t, handler, http.MethodDelete, "/v0/management/api-key-aliases/"+hash, "", "management-key")
	testutil.RequireStatus(t, deleteAliasRR, http.StatusOK)
}

func TestServerCompatProxyRoutes(t *testing.T) {
	cpa := testutil.NewCPAMock(t)
	setup := &store.Setup{CPAUpstreamURL: cpa.URL(), ManagementKey: "management-key", Queue: "usage", PopSide: "right"}
	handler, _ := newCompatHandler(t, testutil.NewConfig(t), setup)

	accountsRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/accounts?limit=10", "", "management-key")
	testutil.RequireStatus(t, accountsRR, http.StatusOK)
	accountsReq, ok := cpa.LastRequest("/v0/management/accounts")
	if !ok {
		t.Fatal("CPA mock did not receive /v0/management/accounts")
	}
	if accountsReq.Authorization != "Bearer management-key" || accountsReq.Query != "limit=10" {
		t.Fatalf("accounts proxy request = %#v", accountsReq)
	}

	reloadRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/reload", `{"force":true}`, "management-key")
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
