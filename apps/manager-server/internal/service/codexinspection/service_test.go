package codexinspection

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	managerconfigsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

func TestRunPersistsLogsResultsAndDetail(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if result.Run.Status != model.CodexInspectionStatusCompleted {
		t.Fatalf("run status = %q", result.Run.Status)
	}
	if len(result.Results) != 1 {
		t.Fatalf("results = %#v", result.Results)
	}
	if result.Results[0].RunID != result.Run.ID {
		t.Fatalf("result run id = %d, want %d", result.Results[0].RunID, result.Run.ID)
	}
	if result.Results[0].Action != "delete" {
		t.Fatalf("result action = %q", result.Results[0].Action)
	}
	if result.Results[0].ActionStatus != model.CodexInspectionActionStatusSuccess || result.Results[0].ExecutedAction != "delete" {
		t.Fatalf("result action audit = %#v", result.Results[0])
	}
	if result.Run.DeleteCount != 1 || result.Run.KeepCount != 0 {
		t.Fatalf("run counts delete=%d keep=%d, want 1/0", result.Run.DeleteCount, result.Run.KeepCount)
	}
	if len(result.Logs) == 0 {
		t.Fatal("expected persisted logs")
	}
	foundStart := false
	for _, logEntry := range result.Logs {
		if logEntry.Message == "Codex 巡检开始" {
			foundStart = true
			if logEntry.Detail == nil {
				t.Fatalf("start log detail is nil: %#v", logEntry)
			}
			break
		}
	}
	if !foundStart {
		t.Fatalf("logs = %#v", result.Logs)
	}
}

func TestRunPersistsPlanQuotaWindowsAndErrorDetail(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready","plan_type":"plus"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{
				"status_code":402,
				"body":{
					"message":"short window exhausted but monthly quota remains",
					"plan_type":"team",
					"rate_limit":{
						"primary_window":{"used_percent":100,"limit_window_seconds":18000,"reset_after_seconds":3600},
						"secondary_window":{"used_percent":72,"limit_window_seconds":2592000,"reset_at":1782895966}
					},
					"code_review_rate_limit":{
						"primary_window":{"used_percent":22,"limit_window_seconds":18000}
					},
					"additional_rate_limits":[{
						"limit_name":"credits",
						"rate_limit":{
							"primary_window":{"used_percent":44,"limit_window_seconds":604800}
						}
					}]
				}
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("results = %#v, want 1", result.Results)
	}
	item := result.Results[0]
	if item.PlanType != "team" {
		t.Fatalf("plan type = %q, want team", item.PlanType)
	}
	if item.ErrorKind != "http_status" || !strings.Contains(item.ErrorDetail, "short window exhausted") {
		t.Fatalf("error detail = kind %q detail %q, want HTTP detail", item.ErrorKind, item.ErrorDetail)
	}
	windowsByID := map[string]model.CodexInspectionQuotaWindow{}
	for _, window := range item.QuotaWindows {
		windowsByID[window.ID] = window
	}
	for _, id := range []string{"five-hour", "monthly", "code-review-five-hour", "credits-weekly-0"} {
		if _, ok := windowsByID[id]; !ok {
			t.Fatalf("quota windows missing %q: %#v", id, item.QuotaWindows)
		}
	}
	if windowsByID["monthly"].UsedPercent == nil || *windowsByID["monthly"].UsedPercent != 72 {
		t.Fatalf("monthly window = %#v, want used percent 72", windowsByID["monthly"])
	}
	if windowsByID["monthly"].ResetLabel == "" || windowsByID["monthly"].ResetLabel == "-" {
		t.Fatalf("monthly reset label = %q, want concrete reset label", windowsByID["monthly"].ResetLabel)
	}
	if windowsByID["credits-weekly-0"].LabelParams["name"] != "credits" {
		t.Fatalf("additional window params = %#v, want credits name", windowsByID["credits-weekly-0"].LabelParams)
	}

	stored, err := db.ListCodexInspectionResults(context.Background(), result.Run.ID)
	if err != nil {
		t.Fatalf("list stored results: %v", err)
	}
	if len(stored) != 1 || stored[0].PlanType != "team" || len(stored[0].QuotaWindows) != len(item.QuotaWindows) {
		t.Fatalf("stored result = %#v, want persisted enhanced fields", stored)
	}
	if stored[0].ErrorKind != "http_status" || !strings.Contains(stored[0].ErrorDetail, "short window exhausted") {
		t.Fatalf("stored error detail = %#v, want persisted HTTP detail", stored[0])
	}
}

func TestRunAutoActionNoneDoesNotExecuteActions(t *testing.T) {
	var patchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"ok":true}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
			patchCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if result.Run.EnableCount != 1 {
		t.Fatalf("enable count = %d, want 1", result.Run.EnableCount)
	}
	if patchCalled {
		t.Fatal("server inspection executed action in none mode")
	}
}

func TestRunAutoActionEnableEnablesRecoveredDisabledAccount(t *testing.T) {
	var patchCalled bool
	var patchedDisabled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"ok":true}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
			patchCalled = true
			var payload struct {
				Name     string `json:"name"`
				Disabled bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			if payload.Name != "auth-a.json" {
				t.Fatalf("patch name = %q, want auth-a.json", payload.Name)
			}
			patchedDisabled = payload.Disabled
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionEnable
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if !patchCalled {
		t.Fatal("server inspection did not auto-enable recovered account")
	}
	if patchedDisabled {
		t.Fatal("server inspection disabled a recovered account, want enable")
	}
	if result.Run.EnableCount != 1 || result.Run.KeepCount != 0 {
		t.Fatalf("run counts enable=%d keep=%d, want 1/0", result.Run.EnableCount, result.Run.KeepCount)
	}
	if result.Results[0].Action != "enable" ||
		result.Results[0].ActionStatus != model.CodexInspectionActionStatusSuccess ||
		result.Results[0].ExecutedAction != "enable" ||
		result.Results[0].Disabled {
		t.Fatalf("result after enable = %#v", result.Results[0])
	}
}

func TestRunAutoActionDisableExecutesDeleteSuggestionAsDisable(t *testing.T) {
	var deleteCalled bool
	var patchCalled bool
	var patchedDisabled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
			patchCalled = true
			var payload struct {
				Name     string `json:"name"`
				Disabled bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			if payload.Name != "auth-a.json" {
				t.Fatalf("patch name = %q, want auth-a.json", payload.Name)
			}
			patchedDisabled = payload.Disabled
			_, _ = w.Write([]byte(`{"ok":true}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			deleteCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionDisable
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if deleteCalled {
		t.Fatal("auto disable mode deleted a delete suggestion")
	}
	if !patchCalled || !patchedDisabled {
		t.Fatalf("auto disable patch called=%v disabled=%v, want true/true", patchCalled, patchedDisabled)
	}
	if result.Run.DeleteCount != 1 || result.Run.KeepCount != 0 {
		t.Fatalf("run counts delete=%d keep=%d, want 1/0", result.Run.DeleteCount, result.Run.KeepCount)
	}
	if result.Results[0].Action != "delete" ||
		result.Results[0].ActionStatus != model.CodexInspectionActionStatusSuccess ||
		result.Results[0].ExecutedAction != "disable" ||
		!result.Results[0].Disabled {
		t.Fatalf("result after auto disable = %#v", result.Results[0])
	}
}

func TestRunAutoActionSkipsDuplicateFileNameResults(t *testing.T) {
	var deleteCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"},{"name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			deleteCalls++
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionDelete
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("delete calls = %d, want 1", deleteCalls)
	}
	if result.Run.DeleteCount != 2 || result.Run.KeepCount != 0 {
		t.Fatalf("run counts delete=%d keep=%d, want 2/0", result.Run.DeleteCount, result.Run.KeepCount)
	}
	if len(result.Results) != 2 {
		t.Fatalf("results = %#v, want 2", result.Results)
	}

	byAuthIndex := map[string]model.CodexInspectionResult{}
	for _, item := range result.Results {
		byAuthIndex[item.AuthIndex] = item
		if item.Action != "delete" {
			t.Fatalf("result action = %q, want delete: %#v", item.Action, item)
		}
	}
	canonical := byAuthIndex["auth-1"]
	if canonical.ActionStatus != model.CodexInspectionActionStatusSuccess ||
		canonical.ExecutedAction != "delete" ||
		canonical.ActionError != "" {
		t.Fatalf("canonical result = %#v, want success delete", canonical)
	}
	duplicate := byAuthIndex["auth-2"]
	if duplicate.ActionStatus != model.CodexInspectionActionStatusSkipped ||
		duplicate.ExecutedAction != "" ||
		duplicate.ActionError == "" {
		t.Fatalf("duplicate result = %#v, want skipped with action error", duplicate)
	}
}

func TestRunAutoActionSkipsMixedActionsInSameFile(t *testing.T) {
	result := runMixedAutoActionInspection(t, model.CodexInspectionAutoActionDelete, mixedAutoActionFixtureEnableDelete)
	assertMixedNeedsReviewRun(t, result, "enable", "delete")
}

func TestRunAutoEnableSkipsMixedActionsInSameFile(t *testing.T) {
	result := runMixedAutoActionInspection(t, model.CodexInspectionAutoActionEnable, mixedAutoActionFixtureEnableDelete)
	assertMixedNeedsReviewRun(t, result, "enable", "delete")
}

func TestRunAutoDisableSkipsMixedDeleteDisableActionsInSameFile(t *testing.T) {
	result := runMixedAutoActionInspection(t, model.CodexInspectionAutoActionDisable, mixedAutoActionFixtureDisableDelete)
	assertMixedNeedsReviewRun(t, result, "disable", "delete")
}

func TestExecuteManualActionsNeedsReviewForMixedFileNameActions(t *testing.T) {
	var deleteCalled bool
	var patchCalled bool
	upstream := newMixedAutoActionServer(t, mixedAutoActionFixtureEnableDelete, &deleteCalled, &patchCalled)
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	runDetail, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if len(runDetail.Results) != 2 {
		t.Fatalf("initial results = %#v, want 2", runDetail.Results)
	}

	result, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID, runDetail.Results[1].ID},
	})
	if err != nil {
		t.Fatalf("execute manual actions: %v", err)
	}
	if deleteCalled || patchCalled {
		t.Fatalf("manual mixed same-file actions executed delete=%v patch=%v, want false/false", deleteCalled, patchCalled)
	}
	if len(result.Outcomes) != 2 {
		t.Fatalf("outcomes = %#v, want 2", result.Outcomes)
	}
	for _, outcome := range result.Outcomes {
		if !outcome.Success ||
			outcome.Status != model.CodexInspectionActionStatusNeedsReview ||
			!strings.Contains(outcome.Error, "多个不同建议动作") {
			t.Fatalf("manual mixed outcome = %#v, want needs_review", outcome)
		}
	}
	assertMixedNeedsReviewRun(t, result.Detail, "enable", "delete")
}

func TestRunClassifiesExpiredUnauthorizedAsReauth(t *testing.T) {
	var deleteCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":401,"body":{"message":"Provided authentication token is expired. Please try signing in again."}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			deleteCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if deleteCalled {
		t.Fatal("expired token reauth suggestion should not execute delete action")
	}
	if result.Run.ReauthCount != 1 || result.Run.DeleteCount != 0 || result.Run.KeepCount != 0 {
		t.Fatalf("run counts reauth=%d delete=%d keep=%d, want 1/0/0", result.Run.ReauthCount, result.Run.DeleteCount, result.Run.KeepCount)
	}
	if len(result.Results) != 1 || result.Results[0].Action != "reauth" {
		t.Fatalf("result action = %#v, want reauth", result.Results)
	}
}

func TestRunClassifiesInvalidatedUnauthorizedAsReauth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":401,"body":{"message":"Your authentication token has been invalidated. Please try signing in again."}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if result.Run.ReauthCount != 1 || result.Run.DeleteCount != 0 {
		t.Fatalf("run counts reauth=%d delete=%d, want 1/0", result.Run.ReauthCount, result.Run.DeleteCount)
	}
	if len(result.Results) != 1 || result.Results[0].Action != "reauth" {
		t.Fatalf("result action = %#v, want reauth", result.Results)
	}
}

func TestResolveProbeActionUsesMonthlyWindowAsLongQuota(t *testing.T) {
	item := account{DisplayAccount: "user@example.test"}
	threshold := 100.0

	t.Run("deletes deactivated workspace payment required response", func(t *testing.T) {
		rateLimit := &codexRateLimit{
			PrimaryWindow: &codexWindow{
				UsedPercent:        ptrFloat(5),
				LimitWindowSeconds: ptrFloat(codexMonthWindow),
			},
		}
		decision := resolveProbeAction(
			item,
			http.StatusPaymentRequired,
			`{"detail":{"code":"deactivated_workspace"}}`,
			rateLimit,
			deriveRateLimitUsedPercent(rateLimit),
			true,
			threshold,
		)

		if decision.Action != "delete" ||
			decision.ActionReason != "接口返回 402，工作区已停用，建议删除账号" ||
			decision.UsedPercent == nil ||
			*decision.UsedPercent != 5 ||
			decision.IsQuota {
			t.Fatalf("decision = %#v, want delete deactivated workspace", decision)
		}
	})

	t.Run("keeps healthy monthly quota", func(t *testing.T) {
		rateLimit := &codexRateLimit{
			PrimaryWindow: &codexWindow{
				UsedPercent:        ptrFloat(5),
				LimitWindowSeconds: ptrFloat(codexMonthWindow),
			},
		}
		decision := resolveProbeAction(item, http.StatusOK, "", rateLimit, deriveRateLimitUsedPercent(rateLimit), false, threshold)

		if decision.Action != "keep" ||
			decision.ActionReason != "月额度仍可用，无需处理" ||
			decision.UsedPercent == nil ||
			*decision.UsedPercent != 5 ||
			decision.IsQuota {
			t.Fatalf("decision = %#v, want keep healthy monthly quota", decision)
		}
	})

	t.Run("disables exhausted monthly quota", func(t *testing.T) {
		rateLimit := &codexRateLimit{
			PrimaryWindow: &codexWindow{
				UsedPercent:        ptrFloat(100),
				LimitWindowSeconds: ptrFloat(codexMonthWindow),
			},
		}
		decision := resolveProbeAction(item, http.StatusOK, "", rateLimit, deriveRateLimitUsedPercent(rateLimit), true, threshold)

		if decision.Action != "disable" ||
			decision.ActionReason != "月额度达到阈值，建议禁用账号" ||
			decision.UsedPercent == nil ||
			*decision.UsedPercent != 100 ||
			!decision.IsQuota {
			t.Fatalf("decision = %#v, want disable exhausted monthly quota", decision)
		}
	})

	t.Run("keeps exhausted short window with healthy monthly quota", func(t *testing.T) {
		rateLimit := &codexRateLimit{
			PrimaryWindow: &codexWindow{
				UsedPercent:        ptrFloat(100),
				LimitWindowSeconds: ptrFloat(codexFiveHourWindow),
			},
			SecondaryWindow: &codexWindow{
				UsedPercent:        ptrFloat(5),
				LimitWindowSeconds: ptrFloat(codexMonthWindow),
			},
		}
		decision := resolveProbeAction(item, http.StatusOK, "", rateLimit, deriveRateLimitUsedPercent(rateLimit), true, threshold)

		if decision.Action != "keep" ||
			decision.ActionReason != "5 小时额度达到阈值，但月额度仍可用，暂不禁用账号" ||
			decision.UsedPercent == nil ||
			*decision.UsedPercent != 5 ||
			decision.IsQuota {
			t.Fatalf("decision = %#v, want keep exhausted short window with healthy monthly quota", decision)
		}
	})

	t.Run("treats team secondary window without duration as monthly quota", func(t *testing.T) {
		rateLimit := &codexRateLimit{
			PrimaryWindow: &codexWindow{
				UsedPercent: ptrFloat(100),
			},
			SecondaryWindow: &codexWindow{
				UsedPercent: ptrFloat(5),
			},
		}
		decision := resolveProbeAction(item, http.StatusOK, "", rateLimit, deriveRateLimitUsedPercent(rateLimit), true, threshold, "team")

		if decision.Action != "keep" ||
			decision.ActionReason != "5 小时额度达到阈值，但月额度仍可用，暂不禁用账号" ||
			decision.UsedPercent == nil ||
			*decision.UsedPercent != 5 ||
			decision.IsQuota {
			t.Fatalf("decision = %#v, want team secondary window treated as monthly quota", decision)
		}
	})
}

func TestRunSuggestsDeleteForDeactivatedWorkspace(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if result.Run.DeleteCount != 1 || result.Run.DisableCount != 0 || result.Run.KeepCount != 0 {
		t.Fatalf("run counts delete=%d disable=%d keep=%d, want 1/0/0", result.Run.DeleteCount, result.Run.DisableCount, result.Run.KeepCount)
	}
	if len(result.Results) != 1 ||
		result.Results[0].Action != "delete" ||
		result.Results[0].ActionReason != "接口返回 402，工作区已停用，建议删除账号" ||
		result.Results[0].IsQuota {
		t.Fatalf("result = %#v, want delete deactivated workspace", result.Results)
	}
}

func TestRunSendsDirectCodexAccountIDHeader(t *testing.T) {
	var accountIDHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account_id":"acct-direct","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			var payload struct {
				Header map[string]string `json:"header"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode api-call payload: %v", err)
			}
			accountIDHeader = payload.Header["ChatGPT-Account-Id"]
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"ok":true}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	if _, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	}); err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if accountIDHeader != "acct-direct" {
		t.Fatalf("ChatGPT-Account-Id = %q, want %q", accountIDHeader, "acct-direct")
	}
}

func TestExecuteManualActionsProcessesCompletedRunResults(t *testing.T) {
	var patchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"ok":true}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
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

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	runDetail, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if len(runDetail.Results) != 1 || runDetail.Results[0].Action != "enable" {
		t.Fatalf("initial results = %#v", runDetail.Results)
	}

	result, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID},
	})
	if err != nil {
		t.Fatalf("execute manual actions: %v", err)
	}
	if !patchCalled {
		t.Fatal("manual action did not patch auth file")
	}
	if len(result.Outcomes) != 1 || !result.Outcomes[0].Success || result.Outcomes[0].Action != "enable" {
		t.Fatalf("outcomes = %#v", result.Outcomes)
	}
	if result.Detail.Run.EnableCount != 1 || result.Detail.Run.KeepCount != 0 {
		t.Fatalf("run counts enable=%d keep=%d, want 1/0", result.Detail.Run.EnableCount, result.Detail.Run.KeepCount)
	}
	if result.Detail.Results[0].Action != "enable" ||
		result.Detail.Results[0].ActionStatus != model.CodexInspectionActionStatusSuccess ||
		result.Detail.Results[0].ExecutedAction != "enable" ||
		result.Detail.Results[0].Disabled {
		t.Fatalf("updated result = %#v", result.Detail.Results[0])
	}
}

func TestExecuteManualActionsRejectsChangedAuthIndex(t *testing.T) {
	var authFilesCalls int
	var deleteCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			authFilesCalls++
			if authFilesCalls == 1 {
				_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			deleteCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	runDetail, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if len(runDetail.Results) != 1 || runDetail.Results[0].Action != "delete" {
		t.Fatalf("initial results = %#v", runDetail.Results)
	}

	result, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID},
	})
	if err != nil {
		t.Fatalf("execute manual actions: %v", err)
	}
	if deleteCalled {
		t.Fatal("manual delete executed after auth_index changed")
	}
	if len(result.Outcomes) != 1 || result.Outcomes[0].Success || result.Outcomes[0].Status != model.CodexInspectionActionStatusFailed {
		t.Fatalf("outcomes = %#v", result.Outcomes)
	}
	if result.Detail.Results[0].Action != "delete" ||
		result.Detail.Results[0].ActionStatus != model.CodexInspectionActionStatusFailed ||
		result.Detail.Results[0].ActionError == "" {
		t.Fatalf("updated result = %#v", result.Detail.Results[0])
	}
}

func TestExecuteManualActionsRejectsMissingAuthFile(t *testing.T) {
	var authFilesCalls int
	var patchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			authFilesCalls++
			if authFilesCalls == 1 {
				_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"files":[]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"message":"limit reached"}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
			patchCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	runDetail, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if len(runDetail.Results) != 1 || runDetail.Results[0].Action != "disable" {
		t.Fatalf("initial results = %#v", runDetail.Results)
	}

	result, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID},
	})
	if err != nil {
		t.Fatalf("execute manual actions: %v", err)
	}
	if patchCalled {
		t.Fatal("manual disable patched a missing auth file")
	}
	if len(result.Outcomes) != 1 || result.Outcomes[0].Success || result.Outcomes[0].Status != model.CodexInspectionActionStatusFailed {
		t.Fatalf("outcomes = %#v", result.Outcomes)
	}
	if result.Detail.Results[0].ActionStatus != model.CodexInspectionActionStatusFailed ||
		result.Detail.Results[0].ActionError == "" {
		t.Fatalf("updated result = %#v", result.Detail.Results[0])
	}
}

func TestExecuteManualActionsSkipsDuplicateFileNameSelections(t *testing.T) {
	var deleteCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"},{"name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			deleteCalls++
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	runDetail, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if len(runDetail.Results) != 2 {
		t.Fatalf("initial results = %#v", runDetail.Results)
	}

	result, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID, runDetail.Results[1].ID},
	})
	if err != nil {
		t.Fatalf("execute manual actions: %v", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("delete calls = %d, want 1", deleteCalls)
	}
	if len(result.Outcomes) != 2 {
		t.Fatalf("outcomes = %#v", result.Outcomes)
	}
	statuses := map[string]int{}
	for _, outcome := range result.Outcomes {
		statuses[outcome.Status]++
	}
	if statuses[model.CodexInspectionActionStatusSuccess] != 1 ||
		statuses[model.CodexInspectionActionStatusSkipped] != 1 {
		t.Fatalf("outcome statuses = %#v", result.Outcomes)
	}
}

func TestRunFinalizesAfterContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			cancel()
			time.Sleep(20 * time.Millisecond)
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"ok":true}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	if err := db.SaveManagerConfig(context.Background(), newCodexInspectionManagerConfig(upstream.URL)); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(ctx, RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection after cancellation: %v", err)
	}
	if result.Run.Status != model.CodexInspectionStatusFailed {
		t.Fatalf("run status = %q, want failed: %#v", result.Run.Status, result.Run)
	}

	runs, err := db.ListCodexInspectionRuns(context.Background(), 1)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %#v", runs)
	}
	if runs[0].Status != model.CodexInspectionStatusFailed || runs[0].FinishedAtMS == 0 {
		t.Fatalf("persisted run was not marked failed: %#v", runs[0])
	}
}

func TestExecuteActionReturnsPatchError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/v0/management/auth-files/status":
			http.Error(w, "status patch failed", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	svc := New(newCodexInspectionTestStore(t), nil, upstream.Client())
	err := svc.executeAction(context.Background(), store.Setup{
		CPAUpstreamURL: upstream.URL,
		ManagementKey:  "management-key",
	}, model.CodexInspectionResult{
		FileName: "auth-a.json",
		Action:   "disable",
	})
	if err == nil {
		t.Fatal("execute action succeeded, want patch error")
	}
	message := err.Error()
	if !strings.Contains(message, "status patch failed") {
		t.Fatalf("patch error = %q", message)
	}
}

func TestCodexInspectionScheduleDue(t *testing.T) {
	enabled := true
	now := mustParseTime(t, "2026-05-22T10:30:00+08:00")

	intervalCfg := model.DefaultCodexInspectionConfig()
	intervalCfg.Enabled = &enabled
	intervalCfg.Schedule.Mode = model.CodexInspectionScheduleModeInterval
	intervalCfg.Schedule.IntervalMinutes = 30
	if !model.CodexInspectionScheduleDue(now, mustParseTime(t, "2026-05-22T09:59:00+08:00"), intervalCfg) {
		t.Fatal("expected interval schedule to be due")
	}

	timePointCfg := model.DefaultCodexInspectionConfig()
	timePointCfg.Enabled = &enabled
	timePointCfg.Schedule.Mode = model.CodexInspectionScheduleModeTimePoints
	timePointCfg.Schedule.TimePoints = []string{"10:30", "18:00"}
	timePointCfg.Schedule.TimeZone = "Asia/Shanghai"
	if !model.CodexInspectionScheduleDue(now, time.Time{}, timePointCfg) {
		t.Fatal("expected time_points schedule to be due")
	}
}

func mustParseTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("parse time: %v", err)
	}
	return parsed
}

type mixedAutoActionFixture string

const (
	mixedAutoActionFixtureEnableDelete  mixedAutoActionFixture = "enable_delete"
	mixedAutoActionFixtureDisableDelete mixedAutoActionFixture = "disable_delete"
)

func runMixedAutoActionInspection(t *testing.T, mode string, fixture mixedAutoActionFixture) RunDetail {
	t.Helper()
	var deleteCalled bool
	var patchCalled bool
	upstream := newMixedAutoActionServer(t, fixture, &deleteCalled, &patchCalled)
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.AutoActionMode = mode
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{
		TriggerType: "manual",
		TriggerKey:  "manual",
	})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if deleteCalled || patchCalled {
		t.Fatalf("mixed same-file actions executed delete=%v patch=%v, want false/false", deleteCalled, patchCalled)
	}
	return result
}

func newMixedAutoActionServer(
	t *testing.T,
	fixture mixedAutoActionFixture,
	deleteCalled *bool,
	patchCalled *bool,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			switch fixture {
			case mixedAutoActionFixtureEnableDelete:
				_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"},{"name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
			case mixedAutoActionFixtureDisableDelete:
				_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"},{"name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
			default:
				t.Fatalf("unexpected mixed fixture %q", fixture)
			}
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			var payload struct {
				AuthIndex string `json:"authIndex"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode api-call payload: %v", err)
			}
			switch payload.AuthIndex {
			case "auth-1":
				if fixture == mixedAutoActionFixtureDisableDelete {
					_, _ = w.Write([]byte(`{"status_code":402,"body":{"message":"limit reached"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"status_code":200,"body":{"ok":true}}`))
			case "auth-2":
				_, _ = w.Write([]byte(`{"status_code":402,"body":{"detail":{"code":"deactivated_workspace"}}}`))
			default:
				t.Fatalf("unexpected authIndex %q", payload.AuthIndex)
			}
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodDelete:
			*deleteCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		case strings.HasPrefix(r.URL.Path, "/v0/management/auth-files") && r.Method == http.MethodPatch:
			*patchCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
}

func assertMixedNeedsReviewRun(t *testing.T, result RunDetail, firstAction string, secondAction string) {
	t.Helper()
	if result.Run.EnableCount != boolToInt(firstAction == "enable")+boolToInt(secondAction == "enable") ||
		result.Run.DisableCount != boolToInt(firstAction == "disable")+boolToInt(secondAction == "disable") ||
		result.Run.DeleteCount != boolToInt(firstAction == "delete")+boolToInt(secondAction == "delete") ||
		result.Run.KeepCount != 0 {
		t.Fatalf("run counts enable=%d disable=%d delete=%d keep=%d",
			result.Run.EnableCount, result.Run.DisableCount, result.Run.DeleteCount, result.Run.KeepCount)
	}
	if len(result.Results) != 2 {
		t.Fatalf("results = %#v, want 2", result.Results)
	}
	byAuthIndex := map[string]model.CodexInspectionResult{}
	for _, item := range result.Results {
		byAuthIndex[item.AuthIndex] = item
		if item.ActionStatus != model.CodexInspectionActionStatusNeedsReview ||
			item.ExecutedAction != "" ||
			!strings.Contains(item.ActionError, "多个不同建议动作") {
			t.Fatalf("mixed result = %#v, want needs_review with conflict reason", item)
		}
	}
	if byAuthIndex["auth-1"].Action != firstAction {
		t.Fatalf("auth-1 action = %q, want %s", byAuthIndex["auth-1"].Action, firstAction)
	}
	if byAuthIndex["auth-2"].Action != secondAction {
		t.Fatalf("auth-2 action = %q, want %s", byAuthIndex["auth-2"].Action, secondAction)
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func newCodexInspectionManagerConfig(upstreamURL string) store.ManagerConfig {
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
	cfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionDelete
	cfg.CodexInspection.Workers = 1
	cfg.CodexInspection.DeleteWorkers = 1
	return cfg
}

func newCodexInspectionTestStore(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	testutil.EnsureAdminCredential(t, db)
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func newCodexInspectionTestService(t *testing.T, db *store.Store) *Service {
	t.Helper()
	cfg := config.Config{
		DBPath:        filepath.Join(t.TempDir(), "usage.sqlite"),
		Queue:         "usage",
		PopSide:       "right",
		BatchSize:     100,
		QueryLimit:    50000,
		CORSOrigins:   []string{"*"},
		CollectorMode: "auto",
	}
	manager := collectorpkg.NewManager(cfg, db)
	collectorService := collector.New(manager)
	managerCfg := managerconfigsvc.New(cfg, db, collectorService)
	return New(db, managerCfg, &http.Client{})
}
