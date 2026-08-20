package httpapi

import (
	"context"
	"encoding/json"
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
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageaggregate"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagearchive"
	usagesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/usage"
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
	server := New(cfg, db, manager)
	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	t.Cleanup(cancelWorkers)
	if err := server.AppContext().UsageService.StartImportSessionCleanup(workerCtx); err != nil {
		t.Fatalf("start import session cleanup: %v", err)
	}
	if err := server.AppContext().UsageService.StartArchiveJobs(workerCtx); err != nil {
		t.Fatalf("start archive jobs: %v", err)
	}
	return server.Handler(), db
}

type staticDatabaseMaintenanceStatus struct {
	snapshot sqliterepo.WALMaintenanceSnapshot
}

func (s staticDatabaseMaintenanceStatus) Snapshot() sqliterepo.WALMaintenanceSnapshot {
	return s.snapshot
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
	if got, want := panelRR.Header().Get("Content-Length"), strconv.Itoa(panelRR.Body.Len()); got != want {
		t.Fatalf("panel content length = %q, want %q", got, want)
	}
}

func TestServerCompatPanelPathOverridesEmbeddedPanel(t *testing.T) {
	cfg := testutil.NewConfig(t)
	panelPath := filepath.Join(t.TempDir(), "management.html")
	customPanel := "<html><body>custom panel</body></html>"
	if err := osWriteFile(panelPath, []byte(customPanel)); err != nil {
		t.Fatalf("write panel: %v", err)
	}
	cfg.PanelPath = panelPath
	handler, _ := newCompatHandler(t, cfg, nil)

	rr := testutil.Request(t, handler, http.MethodGet, "/management.html", "", "")
	testutil.RequireStatus(t, rr, http.StatusOK)
	if rr.Body.String() != customPanel {
		t.Fatalf("panel body = %q", rr.Body.String())
	}
	if got, want := rr.Header().Get("Content-Length"), strconv.Itoa(len(customPanel)); got != want {
		t.Fatalf("panel content length = %q, want %q", got, want)
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
	importSessionRR := testutil.Request(
		t,
		handler,
		http.MethodPost,
		"/v0/management/usage/import-sessions",
		`{"filename":"history.jsonl","size_bytes":1}`,
		"management-key",
	)
	testutil.RequireStatus(t, importSessionRR, http.StatusUnauthorized)
	if !strings.Contains(importSessionRR.Body.String(), `"code":"invalid_admin_key"`) {
		t.Fatalf("usage import session body = %s", importSessionRR.Body.String())
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
	configuredCfg := testutil.NewConfig(t)
	configuredHandler, db := newCompatHandler(t, configuredCfg, setup)
	if err := db.AddDeadLetter(context.Background(), `{"bad":true}`, errors.New("parse failed")); err != nil {
		t.Fatalf("add dead letter: %v", err)
	}
	_, err := db.InsertEvents(context.Background(), []usage.Event{compatEvent("status-event", 1)})
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	rawDB, err := sqliterepo.Open(configuredCfg.DBPath)
	if err != nil {
		t.Fatalf("open migration state database: %v", err)
	}
	if _, err := rawDB.Exec(`update usage_data_migrations set
		status = 'failed', last_error = 'secret migration detail'
		where name = 'usage_cache_accounting_v2'`); err != nil {
		_ = rawDB.Close()
		t.Fatalf("set failed migration state: %v", err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close migration state database: %v", err)
	}

	unauthorizedRR := testutil.Request(t, configuredHandler, http.MethodGet, "/status", "", "")
	testutil.RequireStatus(t, unauthorizedRR, http.StatusUnauthorized)

	statusRR := testutil.Request(t, configuredHandler, http.MethodGet, "/status", "", testutil.AdminKey)
	testutil.RequireStatus(t, statusRR, http.StatusOK)
	if !strings.Contains(statusRR.Body.String(), `"events":1`) ||
		!strings.Contains(statusRR.Body.String(), `"deadLetters":1`) ||
		!strings.Contains(statusRR.Body.String(), `"collector"`) ||
		!strings.Contains(statusRR.Body.String(), `"dataMigration"`) ||
		!strings.Contains(statusRR.Body.String(), `"name":"usage_cache_accounting_v2"`) ||
		!strings.Contains(statusRR.Body.String(), `"status":"failed"`) ||
		strings.Contains(statusRR.Body.String(), `"lastError"`) ||
		strings.Contains(statusRR.Body.String(), "secret migration detail") {
		t.Fatalf("status body = %s", statusRR.Body.String())
	}
}

func TestServerCompatStatusIncludesDatabaseMaintenanceSnapshot(t *testing.T) {
	cfg := testutil.NewConfig(t)
	db := testutil.NewStore(t, cfg)
	manager := collector.NewManager(cfg, db)
	server := New(cfg, db, manager)
	server.AppContext().DatabaseMaintenance = staticDatabaseMaintenanceStatus{
		snapshot: sqliterepo.WALMaintenanceSnapshot{
			DatabaseBytes:         1024,
			WALBytes:              2048,
			SHMBytes:              32,
			TotalBytes:            3104,
			JournalSizeLimitBytes: sqliterepo.WALJournalSizeLimitBytes,
			Checkpoint: sqliterepo.WALCheckpointSnapshot{
				Mode:               sqliterepo.WALCheckpointModePassive,
				Busy:               1,
				LogFrames:          20,
				CheckpointedFrames: 12,
				ExecutedAtMS:       1_786_000_000_000,
				DurationMS:         250,
				Error:              "checkpoint timed out",
			},
		},
	}

	rr := testutil.Request(t, server.Handler(), http.MethodGet, "/status", "", testutil.AdminKey)
	testutil.RequireStatus(t, rr, http.StatusOK)
	var response struct {
		Database sqliterepo.WALMaintenanceSnapshot `json:"database"`
	}
	testutil.DecodeJSON(t, rr, &response)
	if response.Database.DatabaseBytes != 1024 ||
		response.Database.WALBytes != 2048 ||
		response.Database.SHMBytes != 32 ||
		response.Database.TotalBytes != 3104 ||
		response.Database.Checkpoint.Mode != sqliterepo.WALCheckpointModePassive ||
		response.Database.Checkpoint.Busy != 1 ||
		response.Database.Checkpoint.LogFrames != 20 ||
		response.Database.Checkpoint.CheckpointedFrames != 12 ||
		response.Database.Checkpoint.DurationMS != 250 ||
		response.Database.Checkpoint.Error != "checkpoint timed out" {
		t.Fatalf("database maintenance status = %#v", response.Database)
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

func TestServerCompatUsageImportSessionRoutes(t *testing.T) {
	cfg := testutil.NewConfig(t)
	handler, _ := newCompatHandler(t, cfg, nil)
	line := `{"event_hash":"usage-session-event","timestamp_ms":1778000001000,"timestamp":"2026-05-06T00:00:01Z","model":"gpt-test","endpoint":"POST /v1/chat/completions","input_tokens":2,"output_tokens":3,"total_tokens":5,"failed":false}` + "\n"
	createBody := `{"filename":"history.jsonl","size_bytes":` + strconv.Itoa(len(line)) + `,"resume_key":"0123456789abcdef0123456789abcdef"}`

	unauthorized := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/import-sessions", createBody, "wrong-key")
	testutil.RequireStatus(t, unauthorized, http.StatusUnauthorized)

	createRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/import-sessions", createBody, testutil.AdminKey)
	testutil.RequireStatus(t, createRR, http.StatusCreated)
	var session usagesvc.ImportSession
	testutil.DecodeJSON(t, createRR, &session)
	if session.ID == "" || session.Status != usagesvc.ImportSessionStatusUploading {
		t.Fatalf("created session = %#v", session)
	}
	duplicateRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/import-sessions", createBody, testutil.AdminKey)
	testutil.RequireStatus(t, duplicateRR, http.StatusCreated)
	var duplicate usagesvc.ImportSession
	testutil.DecodeJSON(t, duplicateRR, &duplicate)
	if duplicate.ID != session.ID {
		t.Fatalf("duplicate session = %#v, want id %s", duplicate, session.ID)
	}

	uploadRR := testutil.Request(
		t,
		handler,
		http.MethodPut,
		"/v0/management/usage/import-sessions/"+session.ID+"/chunk?offset=0",
		line,
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, uploadRR, http.StatusOK)
	testutil.DecodeJSON(t, uploadRR, &session)
	if session.Status != usagesvc.ImportSessionStatusReady || session.ReceivedBytes != int64(len(line)) {
		t.Fatalf("uploaded session = %#v", session)
	}

	completeRR := testutil.Request(
		t,
		handler,
		http.MethodPost,
		"/v0/management/usage/import-sessions/"+session.ID+"/complete",
		"",
		testutil.AdminKey,
	)
	if completeRR.Code != http.StatusAccepted && completeRR.Code != http.StatusOK {
		t.Fatalf("complete status = %d body = %s", completeRR.Code, completeRR.Body.String())
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		statusRR := testutil.Request(
			t,
			handler,
			http.MethodGet,
			"/v0/management/usage/import-sessions/"+session.ID,
			"",
			testutil.AdminKey,
		)
		testutil.RequireStatus(t, statusRR, http.StatusOK)
		if err := json.Unmarshal(statusRR.Body.Bytes(), &session); err != nil {
			t.Fatalf("decode status: %v", err)
		}
		if session.Status == usagesvc.ImportSessionStatusCompleted {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if session.Status != usagesvc.ImportSessionStatusCompleted || session.Result == nil || session.Result.Added != 1 {
		t.Fatalf("completed session = %#v", session)
	}
	listRR := testutil.Request(
		t,
		handler,
		http.MethodGet,
		"/v0/management/usage/import-sessions?limit=1&status=completed",
		"",
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, listRR, http.StatusOK)
	var list usagesvc.ImportSessionList
	testutil.DecodeJSON(t, listRR, &list)
	if list.Total != 1 || len(list.Sessions) != 1 || list.Sessions[0].ID != session.ID ||
		list.StatusCounts[usagesvc.ImportSessionStatusCompleted] != 1 ||
		list.MaxSessions != cfg.UsageImportMaxSessions || list.ChunkSizeBytes != cfg.UsageImportChunkBytes ||
		list.DiskQuotaBytes != cfg.UsageImportDiskQuotaBytes || list.TTLSeconds != int64(cfg.UsageImportSessionTTL/time.Second) {
		t.Fatalf("import session list = %#v", list)
	}

	malformedRR := testutil.Request(
		t,
		handler,
		http.MethodGet,
		"/v0/management/usage/import-sessions/"+session.ID+"/unknown",
		"",
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, malformedRR, http.StatusNotFound)
}

func TestServerCompatUsageImportSessionResourceErrors(t *testing.T) {
	cfg := testutil.NewConfig(t)
	cfg.UsageImportChunkBytes = 4
	cfg.UsageImportDiskQuotaBytes = 8
	cfg.UsageImportMaxSessions = 1
	handler, _ := newCompatHandler(t, cfg, nil)

	tooLarge := testutil.Request(
		t,
		handler,
		http.MethodPost,
		"/v0/management/usage/import-sessions",
		`{"filename":"large.jsonl","size_bytes":9}`,
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, tooLarge, http.StatusRequestEntityTooLarge)
	if !strings.Contains(tooLarge.Body.String(), string(usagesvc.ImportSessionErrorTooLarge)) {
		t.Fatalf("too large body = %s", tooLarge.Body.String())
	}

	createRR := testutil.Request(
		t,
		handler,
		http.MethodPost,
		"/v0/management/usage/import-sessions",
		`{"filename":"first.jsonl","size_bytes":8}`,
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, createRR, http.StatusCreated)
	limitRR := testutil.Request(
		t,
		handler,
		http.MethodPost,
		"/v0/management/usage/import-sessions",
		`{"filename":"second.jsonl","size_bytes":1}`,
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, limitRR, http.StatusTooManyRequests)
}

func TestServerCompatUsageMaintenanceRequiresAdminKey(t *testing.T) {
	setup := &store.Setup{
		CPAUpstreamURL: "http://127.0.0.1:8317",
		ManagementKey:  "management-key",
		Queue:          "usage",
		PopSide:        "right",
	}
	handler, _ := newCompatHandler(t, testutil.NewConfig(t), setup)

	for _, test := range []struct {
		method string
		path   string
		body   string
	}{
		{method: http.MethodPost, path: "/v0/management/usage/archives/preview", body: `{"cutoff_timestamp_ms":1}`},
		{method: http.MethodPost, path: "/v0/management/usage/archives", body: `{"cutoff_timestamp_ms":1}`},
		{method: http.MethodGet, path: "/v0/management/usage/archives"},
		{method: http.MethodGet, path: "/v0/management/usage/archives/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{method: http.MethodPost, path: "/v0/management/usage/archives/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/resume"},
		{method: http.MethodPost, path: "/v0/management/usage/archives/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/verify"},
		{method: http.MethodPost, path: "/v0/management/usage/archives/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/delete"},
		{method: http.MethodGet, path: "/v0/management/usage/archives-legacy"},
		{method: http.MethodHead, path: "/v0/management/usage/maintenance"},
		{method: http.MethodGet, path: "/v0/management/usage/maintenance"},
		{method: http.MethodGet, path: "/v0/management/usage/maintenance/unknown"},
		{method: http.MethodGet, path: "/v0/management/usage/maintenance-legacy"},
	} {
		for _, key := range []string{"", "management-key"} {
			response := testutil.Request(t, handler, test.method, test.path, test.body, key)
			testutil.RequireStatus(t, response, http.StatusUnauthorized)
			if !strings.Contains(response.Body.String(), `"code":"invalid_admin_key"`) {
				t.Fatalf("%s %s with key %q body = %s", test.method, test.path, key, response.Body.String())
			}
		}
	}

	probe := testutil.Request(t, handler, http.MethodHead, "/v0/management/usage/maintenance", "", testutil.AdminKey)
	testutil.RequireStatus(t, probe, http.StatusNoContent)
	if probe.Body.Len() != 0 {
		t.Fatalf("maintenance probe body = %q", probe.Body.String())
	}
	for _, path := range []string{
		"/v0/management/usage/archives-legacy",
		"/v0/management/usage/maintenance/unknown",
		"/v0/management/usage/maintenance-legacy",
	} {
		malformed := testutil.Request(t, handler, http.MethodGet, path, "", testutil.AdminKey)
		testutil.RequireStatus(t, malformed, http.StatusNotFound)
	}
}

func TestServerCompatUsageArchiveLifecycleAndSanitizedResponses(t *testing.T) {
	cfg := testutil.NewConfig(t)
	cfg.DashboardHourlyRollupEnabled = true
	handler, db := newCompatHandler(t, cfg, nil)
	ctx := context.Background()
	baseMS := time.Date(2026, time.July, 3, 0, 0, 0, 0, time.UTC).UnixMilli()
	cutoffMS := baseMS + 2_500
	events := []usage.Event{
		compatEvent("archive-api-event-a", 1_000),
		compatEvent("archive-api-event-b", 2_000),
		compatEvent("archive-api-hot", 3_000),
	}
	for index := range events {
		events[index].TimestampMS = baseMS + int64(index+1)*1_000
		events[index].Timestamp = time.UnixMilli(events[index].TimestampMS).UTC().Format(time.RFC3339Nano)
		events[index].CreatedAtMS = events[index].TimestampMS
	}
	if _, err := db.InsertEvents(ctx, events); err != nil {
		t.Fatalf("insert archive API events: %v", err)
	}
	body := `{"cutoff_timestamp_ms":` + strconv.FormatInt(cutoffMS, 10) + `}`

	malformed := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/archives/preview", `{"cutoff_timestamp_ms":`, testutil.AdminKey)
	testutil.RequireStatus(t, malformed, http.StatusBadRequest)
	if !strings.Contains(malformed.Body.String(), `"code":"usage_archive_invalid_request"`) ||
		strings.Contains(malformed.Body.String(), "unexpected EOF") {
		t.Fatalf("archive malformed body = %s", malformed.Body.String())
	}
	for _, invalidBody := range []string{`{}`, `null`, `{"cutoff_timestamp_ms":0}`, `{"cutoff_timestamp_ms":-1}`} {
		invalid := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/archives/preview", invalidBody, testutil.AdminKey)
		testutil.RequireStatus(t, invalid, http.StatusBadRequest)
		if !strings.Contains(invalid.Body.String(), `"code":"usage_archive_invalid_request"`) {
			t.Fatalf("archive invalid cutoff body for %s = %s", invalidBody, invalid.Body.String())
		}
	}
	for _, path := range []string{
		"/v0/management/usage/archives/preview",
		"/v0/management/usage/archives",
	} {
		oversized := testutil.Request(
			t,
			handler,
			http.MethodPost,
			path,
			strings.Repeat(" ", 64*1024+1)+body,
			testutil.AdminKey,
		)
		testutil.RequireStatus(t, oversized, http.StatusRequestEntityTooLarge)
		if !strings.Contains(oversized.Body.String(), `"code":"usage_archive_request_too_large"`) {
			t.Fatalf("archive oversized body for %s = %s", path, oversized.Body.String())
		}
	}

	previewRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/archives/preview", body, testutil.AdminKey)
	testutil.RequireStatus(t, previewRR, http.StatusOK)
	var preview store.UsageArchivePreview
	testutil.DecodeJSON(t, previewRR, &preview)
	if preview.EventCount != 2 || preview.TargetEventID != 2 {
		t.Fatalf("archive preview = %#v", preview)
	}

	createRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/archives", body, testutil.AdminKey)
	testutil.RequireStatus(t, createRR, http.StatusCreated)
	assertUsageArchivePayloadSanitized(t, createRR.Body.String(), cfg.UsageArchiveDir)
	var status usagesvc.ArchiveStatusSummary
	testutil.DecodeJSON(t, createRR, &status)
	if status.Run.ID == "" || status.Run.Status != usagearchive.StatusPreviewed || status.Run.Mode != usagearchive.RunModeManual {
		t.Fatalf("created archive status = %#v", status)
	}
	for _, action := range []string{"resume", "verify", "delete"} {
		invalidBody := testutil.Request(
			t,
			handler,
			http.MethodPost,
			"/v0/management/usage/archives/"+status.Run.ID+"/"+action,
			`{"unexpected":true}`,
			testutil.AdminKey,
		)
		testutil.RequireStatus(t, invalidBody, http.StatusBadRequest)
		if !strings.Contains(invalidBody.Body.String(), `"code":"usage_archive_invalid_request"`) {
			t.Fatalf("archive %s non-empty body = %s", action, invalidBody.Body.String())
		}
	}
	oversizedAction := testutil.Request(
		t,
		handler,
		http.MethodPost,
		"/v0/management/usage/archives/"+status.Run.ID+"/resume",
		strings.Repeat(" ", 64*1024+1),
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, oversizedAction, http.StatusRequestEntityTooLarge)
	if !strings.Contains(oversizedAction.Body.String(), `"code":"usage_archive_request_too_large"`) {
		t.Fatalf("archive action oversized body = %s", oversizedAction.Body.String())
	}

	const internalError = "/private/archive/internal failure detail"
	rawDB, err := sqliterepo.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("open archive status DB: %v", err)
	}
	if _, err := rawDB.ExecContext(ctx, `update usage_archive_runs set last_error = ? where id = ?`, internalError, status.Run.ID); err != nil {
		_ = rawDB.Close()
		t.Fatalf("set internal archive error: %v", err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close archive status DB: %v", err)
	}
	statusRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage/archives/"+status.Run.ID, "", testutil.AdminKey)
	testutil.RequireStatus(t, statusRR, http.StatusOK)
	assertUsageArchivePayloadSanitized(t, statusRR.Body.String(), internalError, cfg.UsageArchiveDir)
	testutil.DecodeJSON(t, statusRR, &status)
	if !status.Run.HasError {
		t.Fatalf("archive safe error state = %#v", status.Run)
	}

	locked := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/archives", body, testutil.AdminKey)
	testutil.RequireStatus(t, locked, http.StatusConflict)
	if !strings.Contains(locked.Body.String(), `"code":"usage_archive_maintenance_locked"`) ||
		strings.Contains(locked.Body.String(), status.Run.ID) {
		t.Fatalf("archive lock body = %s", locked.Body.String())
	}

	invalidID := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage/archives/not-a-run-id", "", testutil.AdminKey)
	testutil.RequireStatus(t, invalidID, http.StatusBadRequest)
	if !strings.Contains(invalidID.Body.String(), `"code":"usage_archive_invalid_id"`) {
		t.Fatalf("archive invalid ID body = %s", invalidID.Body.String())
	}
	invalidActionID := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/archives/not-a-run-id/verify?background=true", "", testutil.AdminKey)
	testutil.RequireStatus(t, invalidActionID, http.StatusBadRequest)
	if !strings.Contains(invalidActionID.Body.String(), `"code":"usage_archive_invalid_id"`) {
		t.Fatalf("archive invalid action ID body = %s", invalidActionID.Body.String())
	}
	missingID := strings.Repeat("b", 32)
	missing := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage/archives/"+missingID, "", testutil.AdminKey)
	testutil.RequireStatus(t, missing, http.StatusNotFound)
	if !strings.Contains(missing.Body.String(), `"code":"usage_archive_not_found"`) {
		t.Fatalf("archive missing body = %s", missing.Body.String())
	}

	verifyEarly := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/archives/"+status.Run.ID+"/verify", "", testutil.AdminKey)
	testutil.RequireStatus(t, verifyEarly, http.StatusConflict)
	if !strings.Contains(verifyEarly.Body.String(), `"code":"usage_archive_invalid_state"`) ||
		strings.Contains(verifyEarly.Body.String(), usagearchive.StatusPreviewed) {
		t.Fatalf("archive early verify body = %s", verifyEarly.Body.String())
	}

	wrongStageResume := testutil.Request(
		t,
		handler,
		http.MethodPost,
		"/v0/management/usage/archives/"+status.Run.ID+"/resume?expected_stage=deleting",
		"",
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, wrongStageResume, http.StatusConflict)
	if !strings.Contains(wrongStageResume.Body.String(), `"code":"usage_archive_invalid_state"`) {
		t.Fatalf("archive wrong expected stage body = %s", wrongStageResume.Body.String())
	}

	resumeRR := testutil.Request(
		t,
		handler,
		http.MethodPost,
		"/v0/management/usage/archives/"+status.Run.ID+"/resume?expected_stage=archiving&background=true",
		" \n\t",
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, resumeRR, http.StatusAccepted)
	assertUsageArchivePayloadSanitized(t, resumeRR.Body.String(), cfg.UsageArchiveDir)
	testutil.DecodeJSON(t, resumeRR, &status)
	if resumeRR.Header().Get("Location") != "/v0/management/usage/archives/"+status.Run.ID ||
		resumeRR.Header().Get("Retry-After") != "2" || status.Run.RequestedStage != usagearchive.StatusArchiving {
		t.Fatalf("queued archive response = headers=%v status=%#v", resumeRR.Header(), status)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		statusRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage/archives/"+status.Run.ID, "", testutil.AdminKey)
		testutil.RequireStatus(t, statusRR, http.StatusOK)
		testutil.DecodeJSON(t, statusRR, &status)
		if status.Run.Status == usagearchive.StatusArchived && status.Run.RequestedStage == "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status.Run.Status != usagearchive.StatusArchived || len(status.Segments) == 0 {
		t.Fatalf("archived status = %#v", status)
	}

	catchUpCompatUsageDerived(t, db)
	verifyRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/archives/"+status.Run.ID+"/verify", "", testutil.AdminKey)
	testutil.RequireStatus(t, verifyRR, http.StatusOK)
	assertUsageArchivePayloadSanitized(t, verifyRR.Body.String(), cfg.UsageArchiveDir)
	testutil.DecodeJSON(t, verifyRR, &status)
	if status.Run.Status != usagearchive.StatusVerified {
		t.Fatalf("verified status = %#v", status)
	}
	deleteRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/archives/"+status.Run.ID+"/delete", "", testutil.AdminKey)
	testutil.RequireStatus(t, deleteRR, http.StatusOK)
	assertUsageArchivePayloadSanitized(t, deleteRR.Body.String(), cfg.UsageArchiveDir)
	testutil.DecodeJSON(t, deleteRR, &status)
	if status.Run.Status != usagearchive.StatusCompleted || status.Run.DeletedEventCount != 2 {
		t.Fatalf("completed archive status = %#v", status)
	}

	listRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage/archives?limit=1", "", testutil.AdminKey)
	testutil.RequireStatus(t, listRR, http.StatusOK)
	assertUsageArchivePayloadSanitized(t, listRR.Body.String(), internalError, cfg.UsageArchiveDir)
	var list usagesvc.ArchiveList
	testutil.DecodeJSON(t, listRR, &list)
	if len(list.Runs) != 1 || list.Runs[0].ID != status.Run.ID || list.Runs[0].Status != usagearchive.StatusCompleted ||
		list.Total != 1 || list.StatusCounts[usagearchive.StatusCompleted] != 1 || list.NextCursor != "" {
		t.Fatalf("archive list = %#v", list)
	}

	maintenanceRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage/maintenance", "", testutil.AdminKey)
	testutil.RequireStatus(t, maintenanceRR, http.StatusOK)
	assertUsageMaintenancePayloadSanitized(t, maintenanceRR.Body.String(), internalError, cfg.UsageArchiveDir)
	var maintenance usagesvc.MaintenanceStatus
	testutil.DecodeJSON(t, maintenanceRR, &maintenance)
	wantRemainingTimestampMS := events[2].TimestampMS
	if maintenance.RawEventCount != 1 || maintenance.RawMinTimestampMS != wantRemainingTimestampMS ||
		maintenance.RawMaxTimestampMS != wantRemainingTimestampMS || maintenance.RawDeletedEventCount != 2 ||
		maintenance.RawArchivedEventCount != 0 ||
		maintenance.ActiveRun != nil || maintenance.ActiveLock != nil {
		t.Fatalf("completed maintenance status = %#v", maintenance)
	}
	for _, field := range []string{`"raw_min_timestamp_ms"`, `"raw_max_timestamp_ms"`, `"raw_archived_event_count"`} {
		if !strings.Contains(maintenanceRR.Body.String(), field) {
			t.Fatalf("maintenance status omitted %s: %s", field, maintenanceRR.Body.String())
		}
	}
	if !maintenance.CompactRequiresStoppedServer || maintenance.Storage.PageSize <= 0 || maintenance.Storage.PageCount <= 0 {
		t.Fatalf("maintenance compact/storage status = %#v", maintenance)
	}
	if maintenance.Storage.DatabaseBytes <= 0 || maintenance.Storage.TotalBytes != maintenance.Storage.DatabaseBytes+maintenance.Storage.WALBytes+maintenance.Storage.SHMBytes {
		t.Fatalf("maintenance file sizes = %#v", maintenance.Storage)
	}

	for _, limit := range []string{"0", "101", "invalid"} {
		invalid := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage/archives?limit="+limit, "", testutil.AdminKey)
		testutil.RequireStatus(t, invalid, http.StatusBadRequest)
		if !strings.Contains(invalid.Body.String(), `"code":"usage_archive_invalid_request"`) || strings.Contains(invalid.Body.String(), "between 1 and 100") {
			t.Fatalf("invalid limit %s body = %s", limit, invalid.Body.String())
		}
	}
	for _, query := range []string{"status=future", "mode=future", "cursor=invalid"} {
		invalid := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage/archives?"+query, "", testutil.AdminKey)
		testutil.RequireStatus(t, invalid, http.StatusBadRequest)
		if !strings.Contains(invalid.Body.String(), `"code":"usage_archive_invalid_request"`) {
			t.Fatalf("invalid archive list query %s body = %s", query, invalid.Body.String())
		}
	}
	method := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/maintenance", "", testutil.AdminKey)
	testutil.RequireStatus(t, method, http.StatusMethodNotAllowed)
	unknownAction := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/archives/"+status.Run.ID+"/unknown", "", testutil.AdminKey)
	testutil.RequireStatus(t, unknownAction, http.StatusNotFound)
}

func TestServerCompatUsageArchiveInternalErrorsAreSanitized(t *testing.T) {
	cfg := testutil.NewConfig(t)
	handler, db := newCompatHandler(t, cfg, nil)
	ctx := context.Background()
	event := compatEvent("archive-api-internal-error", 1)
	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert archive error event: %v", err)
	}
	body := `{"cutoff_timestamp_ms":` + strconv.FormatInt(event.TimestampMS+1, 10) + `}`
	createRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/archives", body, testutil.AdminKey)
	testutil.RequireStatus(t, createRR, http.StatusCreated)
	var status usagesvc.ArchiveStatusSummary
	testutil.DecodeJSON(t, createRR, &status)
	resumeRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/archives/"+status.Run.ID+"/resume", "", testutil.AdminKey)
	testutil.RequireStatus(t, resumeRR, http.StatusOK)

	segments, err := db.UsageArchives.Segments(ctx, status.Run.ID)
	if err != nil || len(segments) == 0 {
		t.Fatalf("load archive segments: segments=%#v err=%v", segments, err)
	}
	segmentPath := filepath.Join(cfg.UsageArchiveDir, filepath.FromSlash(segments[0].FileName))
	if err := os.Remove(segmentPath); err != nil {
		t.Fatalf("remove archive segment: %v", err)
	}
	verifyRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/usage/archives/"+status.Run.ID+"/verify", "", testutil.AdminKey)
	testutil.RequireStatus(t, verifyRR, http.StatusInternalServerError)
	if !strings.Contains(verifyRR.Body.String(), `"code":"request_failed"`) ||
		!strings.Contains(verifyRR.Body.String(), `"error":"usage archive request failed"`) {
		t.Fatalf("sanitized internal error body = %s", verifyRR.Body.String())
	}
	assertUsageMaintenancePayloadSanitized(t, verifyRR.Body.String(), segmentPath, segments[0].FileName, "no such file")
}

func TestServerCompatUsageMaintenanceRejectsStaleAggregateState(t *testing.T) {
	for _, test := range []struct {
		name       string
		updateSQL  string
		updateArgs []any
	}{
		{
			name: "legacy schema",
			updateSQL: `update usage_hourly_aggregate_state set
				schema_version = ?, status = 'ready', coverage_event_id = target_event_id
				where aggregate_name = ?`,
			updateArgs: []any{usageaggregate.SchemaVersion - 1, usageaggregate.AggregateName},
		},
		{
			name: "legacy structure revision",
			updateSQL: `update usage_hourly_aggregate_state set
				schema_version = ?, structure_revision = 'legacy', status = 'ready', coverage_event_id = target_event_id
				where aggregate_name = ?`,
			updateArgs: []any{usageaggregate.SchemaVersion, usageaggregate.AggregateName},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := testutil.NewConfig(t)
			cfg.DashboardHourlyRollupEnabled = true
			handler, _ := newCompatHandler(t, cfg, nil)

			rawDB, err := sqliterepo.Open(cfg.DBPath)
			if err != nil {
				t.Fatalf("open aggregate state: %v", err)
			}
			if _, err := rawDB.Exec(test.updateSQL, test.updateArgs...); err != nil {
				_ = rawDB.Close()
				t.Fatalf("set stale aggregate state: %v", err)
			}
			if err := rawDB.Close(); err != nil {
				t.Fatalf("close aggregate state: %v", err)
			}

			response := testutil.Request(t, handler, http.MethodGet, "/v0/management/usage/maintenance", "", testutil.AdminKey)
			testutil.RequireStatus(t, response, http.StatusOK)
			var status usagesvc.MaintenanceStatus
			testutil.DecodeJSON(t, response, &status)
			if status.Readiness.HourlyAggregateReady {
				t.Fatal("stale aggregate state was reported ready")
			}
		})
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

	apiCallBody := `{"method":"GET","url":"https://api.example.com/v1/models","proxy_url":"socks5h://proxy.example:1080"}`
	apiCallRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/api-call", apiCallBody, testutil.AdminKey)
	testutil.RequireStatus(t, apiCallRR, http.StatusOK)
	apiCallReq, ok := cpa.LastRequest("/v0/management/api-call")
	if !ok {
		t.Fatal("CPA mock did not receive /v0/management/api-call")
	}
	if apiCallReq.Authorization != "Bearer management-key" || apiCallReq.Body != apiCallBody {
		t.Fatalf("api-call proxy request = %#v", apiCallReq)
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

	observed := make(chan observedRequest, 12)
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
		"/v0/management/plugin-store/demo/install?source=official&version=v1.2.3",
		`{"version":"v1.2.3"}`,
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, managementInstallRR, http.StatusOK)
	assertObserved("/v0/management/plugin-store/demo/install", observedRequest{
		method:        http.MethodPost,
		path:          "/v0/management/plugin-store/demo/install",
		query:         "source=official&version=v1.2.3",
		authorization: "Bearer management-key",
		body:          `{"version":"v1.2.3"}`,
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

	// Public plugin resources may remain reachable, but an unauthenticated
	// caller must never be elevated to the saved CPA management key.
	resourceNoAuthRR := requestWithHeaders(
		http.MethodGet,
		"/v0/resource/plugins/codex-invite/invite",
		"",
		"",
		map[string]string{
			"X-Codex-Invite-Origin": "http://localhost:18317",
		},
	)
	testutil.RequireStatus(t, resourceNoAuthRR, http.StatusOK)
	assertObserved("/v0/resource/plugins/codex-invite/invite", observedRequest{
		method:            http.MethodGet,
		path:              "/v0/resource/plugins/codex-invite/invite",
		codexInviteOrigin: upstream.URL,
	})

	resourceCallerAuthRR := requestWithHeaders(
		http.MethodGet,
		"/v0/resource/plugins/codex-invite/invite",
		"",
		"plugin-management-key",
		nil,
	)
	testutil.RequireStatus(t, resourceCallerAuthRR, http.StatusOK)
	assertObserved("/v0/resource/plugins/codex-invite/invite", observedRequest{
		method:        http.MethodGet,
		path:          "/v0/resource/plugins/codex-invite/invite",
		authorization: "Bearer plugin-management-key",
	})

	resourceAdminAuthRR := testutil.Request(
		t,
		handler,
		http.MethodGet,
		"/v0/resource/plugins/codex-invite/invite",
		"",
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, resourceAdminAuthRR, http.StatusOK)
	assertObserved("/v0/resource/plugins/codex-invite/invite", observedRequest{
		method:        http.MethodGet,
		path:          "/v0/resource/plugins/codex-invite/invite",
		authorization: "Bearer management-key",
	})

	resourceHeadNoAuthRR := testutil.Request(
		t,
		handler,
		http.MethodHead,
		"/v0/resource/plugins/codex-invite/invite",
		"",
		"",
	)
	testutil.RequireStatus(t, resourceHeadNoAuthRR, http.StatusOK)
	assertObserved("/v0/resource/plugins/codex-invite/invite", observedRequest{
		method: http.MethodHead,
		path:   "/v0/resource/plugins/codex-invite/invite",
	})
}

type recordingAutomationRuntimeService struct {
	reloadCount int
}

func (s *recordingAutomationRuntimeService) Reload(context.Context) error {
	s.reloadCount++
	return nil
}

func catchUpCompatUsageDerived(t *testing.T, st *store.Store) {
	t.Helper()
	ctx := context.Background()
	nowMS := time.Now().UnixMilli()
	for _, catchUp := range []struct {
		name string
		run  func(context.Context, int, int64) (bool, error)
	}{
		{
			name: "hourly aggregate",
			run: func(ctx context.Context, limit int, nowMS int64) (bool, error) {
				result, err := st.CatchUpUsageHourlyAggregate(ctx, limit, nowMS)
				return result.Pending, err
			},
		},
		{
			name: "pricing",
			run: func(ctx context.Context, limit int, nowMS int64) (bool, error) {
				result, err := st.CatchUpUsagePricing(ctx, limit, nowMS)
				return result.Pending, err
			},
		},
		{
			name: "monitoring stats",
			run: func(ctx context.Context, limit int, nowMS int64) (bool, error) {
				result, err := st.CatchUpUsageMonitoringStats(ctx, limit, nowMS)
				return result.Pending, err
			},
		},
		{
			name: "monitoring metadata",
			run: func(ctx context.Context, limit int, nowMS int64) (bool, error) {
				result, err := st.CatchUpUsageMonitoringMetadata(ctx, limit, nowMS)
				return result.Pending, err
			},
		},
		{
			name: "monitoring projection",
			run: func(ctx context.Context, limit int, nowMS int64) (bool, error) {
				result, err := st.CatchUpUsageMonitoringProjection(ctx, limit, nowMS)
				return result.Pending, err
			},
		},
		{
			name: "account history",
			run: func(ctx context.Context, limit int, nowMS int64) (bool, error) {
				result, err := st.CatchUpAccountHistoryRollups(ctx, limit, nowMS)
				return result.Pending, err
			},
		},
		{
			name: "dashboard hourly",
			run: func(ctx context.Context, limit int, nowMS int64) (bool, error) {
				result, err := st.CatchUpDashboardHourlyRollups(ctx, limit, nowMS)
				return result.Pending, err
			},
		},
	} {
		completed := false
		for iteration := 0; iteration < 100; iteration++ {
			pending, err := catchUp.run(ctx, 100, nowMS+int64(iteration))
			if err != nil {
				t.Fatalf("catch up usage %s: %v", catchUp.name, err)
			}
			if !pending {
				completed = true
				break
			}
		}
		if !completed {
			t.Fatalf("usage %s catch-up did not complete", catchUp.name)
		}
	}
}

func assertUsageMaintenancePayloadSanitized(t *testing.T, payload string, secrets ...string) {
	t.Helper()
	for _, key := range []string{
		`"raw_json"`,
		`"fail_body"`,
		`"format"`,
		`"archive_digest"`,
		`"manifest_file"`,
		`"manifest_sha256"`,
		`"file_name"`,
		`"content_sha256"`,
		`"event_hash_digest"`,
		`"last_error"`,
	} {
		if strings.Contains(payload, key) {
			t.Fatalf("usage maintenance payload leaked %s: %s", key, payload)
		}
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(payload, secret) {
			t.Fatalf("usage maintenance payload leaked %q: %s", secret, payload)
		}
	}
}

func assertUsageArchivePayloadSanitized(t *testing.T, payload string, secrets ...string) {
	t.Helper()
	assertUsageMaintenancePayloadSanitized(t, payload, secrets...)
	if strings.Contains(payload, `"schema_version"`) {
		t.Fatalf("usage archive payload leaked schema_version: %s", payload)
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
