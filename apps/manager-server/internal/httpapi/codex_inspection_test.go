package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

func TestCodexInspectionRoutesAreMounted(t *testing.T) {
	cfg := testutil.NewConfig(t)
	handler, db := newCompatHandler(t, cfg, nil)
	managerCfg := store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    "http://cpa.local",
			ManagementKey: "management-key",
		},
		Collector: store.ManagerCollectorConfig{
			CollectorMode:  "auto",
			Queue:          "usage",
			PopSide:        "right",
			BatchSize:      100,
			PollIntervalMS: 500,
			QueryLimit:     50000,
		},
		CodexInspection: store.DefaultCodexInspectionConfig(),
	}
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	rr := testutil.Request(t, handler, http.MethodGet, "/v0/management/codex-inspection/runs", "", testutil.AdminKey)
	testutil.RequireStatus(t, rr, http.StatusOK)
	if !strings.Contains(rr.Body.String(), `"items"`) {
		t.Fatalf("runs body = %s", rr.Body.String())
	}
}

func TestCodexInspectionActivityFieldsDistinguishStaleRunningRows(t *testing.T) {
	cfg := testutil.NewConfig(t)
	handler, db := newCompatHandler(t, cfg, nil)
	managerCfg := newCodexInspectionHTTPManagerConfig("http://cpa.local")
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	acquired, err := db.AcquireCodexInspectionRun(context.Background(), model.CodexInspectionRun{
		TriggerType:  model.CodexInspectionTriggerManual,
		TriggerKey:   "stale",
		Status:       model.CodexInspectionStatusRunning,
		Settings:     managerCfg.CodexInspection,
		SettingsJSON: model.MarshalCodexInspectionSettings(managerCfg.CodexInspection),
	}, "stale-owner", time.Millisecond)
	if err != nil {
		t.Fatalf("acquire stale run: %v", err)
	}
	stale := acquired.Run
	waitForCodexInspectionLeaseExpiry(t, db)

	rr := testutil.Request(t, handler, http.MethodGet, "/v0/management/codex-inspection/runs", "", testutil.AdminKey)
	testutil.RequireStatus(t, rr, http.StatusOK)
	var response struct {
		Items []struct {
			ID          int64 `json:"id"`
			Active      *bool `json:"active"`
			Cancellable *bool `json:"cancellable"`
		} `json:"items"`
	}
	testutil.DecodeJSON(t, rr, &response)
	if len(response.Items) != 1 || response.Items[0].ID != stale.ID {
		t.Fatalf("runs response = %#v", response.Items)
	}
	if response.Items[0].Active == nil || *response.Items[0].Active || response.Items[0].Cancellable == nil || *response.Items[0].Cancellable {
		t.Fatalf("activity fields = %#v", response.Items[0])
	}
}

func waitForCodexInspectionLeaseExpiry(t *testing.T, db *store.Store) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		_, active, err := db.GetActiveCodexInspectionLease(context.Background(), time.Now().UnixMilli())
		if err != nil {
			t.Fatalf("get active inspection lease: %v", err)
		}
		if !active {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("inspection lease did not expire")
}

func TestCodexInspectionCancelRouteLifecycle(t *testing.T) {
	started := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			close(started)
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	cfg := testutil.NewConfig(t)
	handler, db := newCompatHandler(t, cfg, nil)
	managerCfg := newCodexInspectionHTTPManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	runRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/codex-inspection/run", "", testutil.AdminKey)
	testutil.RequireStatus(t, runRR, http.StatusOK)
	if !strings.Contains(runRR.Body.String(), `"results":[]`) || !strings.Contains(runRR.Body.String(), `"logs":[]`) {
		t.Fatalf("start response must preserve array fields: %s", runRR.Body.String())
	}
	var startedRun struct {
		Run model.CodexInspectionRun `json:"run"`
	}
	testutil.DecodeJSON(t, runRR, &startedRun)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("inspection did not start")
	}

	cancelPath := "/v0/management/codex-inspection/runs/" + strconv.FormatInt(startedRun.Run.ID, 10) + "/cancel"
	cancelRR := testutil.Request(t, handler, http.MethodPost, cancelPath, "", testutil.AdminKey)
	testutil.RequireStatus(t, cancelRR, http.StatusOK)

	deadline := time.Now().Add(3 * time.Second)
	var finalDetail struct {
		Run model.CodexInspectionRun `json:"run"`
	}
	for time.Now().Before(deadline) {
		detailRR := testutil.Request(t, handler, http.MethodGet, "/v0/management/codex-inspection/runs/"+strconv.FormatInt(startedRun.Run.ID, 10), "", testutil.AdminKey)
		testutil.RequireStatus(t, detailRR, http.StatusOK)
		testutil.DecodeJSON(t, detailRR, &finalDetail)
		if finalDetail.Run.Status == model.CodexInspectionStatusCancelled {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if finalDetail.Run.Status != model.CodexInspectionStatusCancelled || finalDetail.Run.FinishedAtMS == 0 {
		t.Fatalf("cancelled run = %#v", finalDetail.Run)
	}

	repeatRR := testutil.Request(t, handler, http.MethodPost, cancelPath, "", testutil.AdminKey)
	testutil.RequireStatus(t, repeatRR, http.StatusOK)

	missingRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/codex-inspection/runs/999999/cancel", "", testutil.AdminKey)
	testutil.RequireStatus(t, missingRR, http.StatusNotFound)
}

func TestCodexInspectionManualActionsRoute(t *testing.T) {
	var patchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"id":"runtime-auth-1","name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":5,"limit_window_seconds":2592000}}}}`))
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			patchCalled = true
			var payload struct {
				Name      string `json:"name"`
				AuthIndex string `json:"auth_index"`
				Disabled  bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			if payload.Name != "runtime-auth-1" || payload.AuthIndex != "auth-1" || payload.Disabled {
				t.Fatalf("patch payload = %#v, want enable runtime-auth-1", payload)
			}
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	cfg := testutil.NewConfig(t)
	handler, db := newCompatHandler(t, cfg, nil)
	managerCfg := newCodexInspectionHTTPManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	runRR := testutil.Request(t, handler, http.MethodPost, "/v0/management/codex-inspection/run", "", testutil.AdminKey)
	testutil.RequireStatus(t, runRR, http.StatusOK)
	var runDetail struct {
		Run struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
		} `json:"run"`
		Results []struct {
			ID     int64  `json:"id"`
			Action string `json:"action"`
		} `json:"results"`
	}
	testutil.DecodeJSON(t, runRR, &runDetail)
	deadline := time.Now().Add(3 * time.Second)
	for (len(runDetail.Results) == 0 || runDetail.Run.Status != model.CodexInspectionStatusCompleted) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
		detailRR := testutil.Request(
			t,
			handler,
			http.MethodGet,
			"/v0/management/codex-inspection/runs/"+strconv.FormatInt(runDetail.Run.ID, 10),
			"",
			testutil.AdminKey,
		)
		testutil.RequireStatus(t, detailRR, http.StatusOK)
		testutil.DecodeJSON(t, detailRR, &runDetail)
	}
	if runDetail.Run.Status != model.CodexInspectionStatusCompleted || len(runDetail.Results) != 1 || runDetail.Results[0].Action != "enable" {
		t.Fatalf("run detail = %#v", runDetail)
	}
	completedCancelRR := testutil.Request(
		t,
		handler,
		http.MethodPost,
		"/v0/management/codex-inspection/runs/"+strconv.FormatInt(runDetail.Run.ID, 10)+"/cancel",
		"",
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, completedCancelRR, http.StatusConflict)

	actionBody := `{"resultIds":[` + strconv.FormatInt(runDetail.Results[0].ID, 10) + `]}`
	actionRR := testutil.Request(
		t,
		handler,
		http.MethodPost,
		"/v0/management/codex-inspection/runs/"+strconv.FormatInt(runDetail.Run.ID, 10)+"/actions",
		actionBody,
		testutil.AdminKey,
	)
	testutil.RequireStatus(t, actionRR, http.StatusOK)
	if !patchCalled {
		t.Fatal("manual actions route did not patch auth file")
	}
	var actionResult struct {
		Outcomes []struct {
			Status  string `json:"status"`
			Action  string `json:"action"`
			Success bool   `json:"success"`
		} `json:"outcomes"`
		Detail struct {
			Run struct {
				EnableCount int `json:"enableCount"`
				KeepCount   int `json:"keepCount"`
			} `json:"run"`
			Results []struct {
				Action         string `json:"action"`
				ActionStatus   string `json:"actionStatus"`
				ExecutedAction string `json:"executedAction"`
				Disabled       bool   `json:"disabled"`
			} `json:"results"`
		} `json:"detail"`
	}
	testutil.DecodeJSON(t, actionRR, &actionResult)
	if len(actionResult.Outcomes) != 1 ||
		!actionResult.Outcomes[0].Success ||
		actionResult.Outcomes[0].Status != model.CodexInspectionActionStatusSuccess ||
		actionResult.Outcomes[0].Action != "enable" {
		t.Fatalf("action outcomes = %#v", actionResult.Outcomes)
	}
	if actionResult.Detail.Run.EnableCount != 1 || actionResult.Detail.Run.KeepCount != 0 {
		t.Fatalf("run counts = %#v", actionResult.Detail.Run)
	}
	if len(actionResult.Detail.Results) != 1 ||
		actionResult.Detail.Results[0].Action != "enable" ||
		actionResult.Detail.Results[0].ActionStatus != model.CodexInspectionActionStatusSuccess ||
		actionResult.Detail.Results[0].ExecutedAction != "enable" ||
		actionResult.Detail.Results[0].Disabled {
		t.Fatalf("updated results = %#v", actionResult.Detail.Results)
	}
}

func TestCodexInspectionRunReturnsPreconditionFailedWhenNotConfigured(t *testing.T) {
	cfg := testutil.NewConfig(t)
	handler, db := newCompatHandler(t, cfg, nil)
	managerCfg := newCodexInspectionHTTPManagerConfig("")
	managerCfg.CPAConnection.CPABaseURL = ""
	managerCfg.CPAConnection.ManagementKey = ""
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	rr := testutil.Request(t, handler, http.MethodPost, "/v0/management/codex-inspection/run", "", testutil.AdminKey)
	testutil.RequireStatus(t, rr, http.StatusPreconditionFailed)
}

func newCodexInspectionHTTPManagerConfig(upstreamURL string) store.ManagerConfig {
	enabled := true
	cfg := store.ManagerConfig{
		CPAConnection: store.ManagerCPAConnectionConfig{
			CPABaseURL:    upstreamURL,
			ManagementKey: "management-key",
		},
		Collector: store.ManagerCollectorConfig{
			CollectorMode:  "auto",
			Queue:          "usage",
			PopSide:        "right",
			BatchSize:      100,
			PollIntervalMS: 500,
			QueryLimit:     50000,
		},
		CodexInspection: store.DefaultCodexInspectionConfig(),
	}
	cfg.CodexInspection.Enabled = &enabled
	cfg.CodexInspection.Workers = 1
	cfg.CodexInspection.DeleteWorkers = 1
	return cfg
}
