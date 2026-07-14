package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

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

func TestCodexInspectionManualActionsRoute(t *testing.T) {
	var patchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":5,"limit_window_seconds":2592000}}}}`))
		case r.URL.Path == "/v0/management/auth-files/status" && r.Method == http.MethodPatch:
			patchCalled = true
			var payload struct {
				Name     string `json:"name"`
				Disabled bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			if payload.Name != "auth-a.json" || payload.Disabled {
				t.Fatalf("patch payload = %#v, want enable auth-a.json", payload)
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
			ID int64 `json:"id"`
		} `json:"run"`
		Results []struct {
			ID     int64  `json:"id"`
			Action string `json:"action"`
		} `json:"results"`
	}
	testutil.DecodeJSON(t, runRR, &runDetail)
	if len(runDetail.Results) != 1 || runDetail.Results[0].Action != "enable" {
		t.Fatalf("run detail = %#v", runDetail)
	}

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
