package health_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	healthcontroller "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/health"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

func TestReadyReportsCollectorAndStorageState(t *testing.T) {
	cfg := testutil.NewConfig(t)
	store := testutil.NewStore(t, cfg)
	cpa := testutil.NewCPAMock(t)
	manager := collector.NewManager(cfg, store)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	manager.Start(ctx, collector.RuntimeConfig{
		CPAUpstreamURL: cpa.URL(),
		ManagementKey:  cpa.ManagementKey,
		CollectorMode:  "http",
		Queue:          cfg.Queue,
		PopSide:        cfg.PopSide,
		BatchSize:      cfg.BatchSize,
		PollInterval:   10 * time.Millisecond,
	})

	deadline := time.Now().Add(2 * time.Second)
	for manager.Status().Collector != "running" && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if status := manager.Status(); status.Collector != "running" {
		t.Fatalf("collector status = %q, want running; last error = %q", status.Collector, status.LastError)
	}

	appCtx := app.FromExisting(cfg, store, manager, time.Now().UnixMilli(), nil, nil, nil, "test-service")
	handler := &healthcontroller.Handler{App: appCtx}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	handler.Ready(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body["ok"] != true || body["collector"] != "running" {
		t.Fatalf("response = %#v", body)
	}
	if body["events"] != float64(0) || body["totalInserted"] != float64(0) {
		t.Fatalf("unexpected counters: %#v", body)
	}
	if _, exists := body["dbPath"]; exists {
		t.Fatal("readiness response must not expose the database path")
	}
	if _, exists := body["lastError"]; exists {
		t.Fatal("readiness response must not expose collector error details")
	}
}

func TestReadyRejectsNonLoopbackRequests(t *testing.T) {
	handler := &healthcontroller.Handler{}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
	request.RemoteAddr = "203.0.113.10:12345"

	handler.Ready(recorder, request)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusForbidden)
	}
}
