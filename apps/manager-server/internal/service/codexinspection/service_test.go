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
		case r.URL.Path == "/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":401,"body":{"message":"unauthorized"}}`))
		case strings.HasPrefix(r.URL.Path, "/auth-files") && r.Method == http.MethodDelete:
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

func TestRunAutoActionNoneDoesNotExecuteActions(t *testing.T) {
	var patchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"ok":true}}`))
		case strings.HasPrefix(r.URL.Path, "/auth-files") && r.Method == http.MethodPatch:
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
		case r.URL.Path == "/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"ok":true}}`))
		case strings.HasPrefix(r.URL.Path, "/auth-files") && r.Method == http.MethodPatch:
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
		case r.URL.Path == "/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":401,"body":{"message":"Your authentication token has been invalidated."}}`))
		case strings.HasPrefix(r.URL.Path, "/auth-files") && r.Method == http.MethodPatch:
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
		case strings.HasPrefix(r.URL.Path, "/auth-files") && r.Method == http.MethodDelete:
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
		case r.URL.Path == "/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"},{"name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":401,"body":{"message":"Your authentication token has been invalidated."}}`))
		case strings.HasPrefix(r.URL.Path, "/auth-files") && r.Method == http.MethodDelete:
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
		case r.URL.Path == "/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":401,"body":{"message":"Provided authentication token is expired. Please try signing in again."}}`))
		case strings.HasPrefix(r.URL.Path, "/auth-files") && r.Method == http.MethodDelete:
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

func TestRunClassifiesInvalidatedUnauthorizedAsDelete(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/api-call" && r.Method == http.MethodPost:
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
	if result.Run.DeleteCount != 1 || result.Run.ReauthCount != 0 {
		t.Fatalf("run counts delete=%d reauth=%d, want 1/0", result.Run.DeleteCount, result.Run.ReauthCount)
	}
	if len(result.Results) != 1 || result.Results[0].Action != "delete" {
		t.Fatalf("result action = %#v, want delete", result.Results)
	}
}

func TestResolveProbeActionUsesMonthlyWindowAsLongQuota(t *testing.T) {
	item := account{DisplayAccount: "user@example.test"}
	threshold := 100.0

	t.Run("keeps healthy monthly quota", func(t *testing.T) {
		rateLimit := &codexRateLimit{
			PrimaryWindow: &codexWindow{
				UsedPercent:        ptrFloat(5),
				LimitWindowSeconds: ptrFloat(codexMonthWindow),
			},
		}
		decision := resolveProbeAction(item, http.StatusOK, "", rateLimit, deriveRateLimitUsedPercent(rateLimit), false, threshold, model.CodexInspectionShortWindowQuotaModeKeep)

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
		decision := resolveProbeAction(item, http.StatusOK, "", rateLimit, deriveRateLimitUsedPercent(rateLimit), true, threshold, model.CodexInspectionShortWindowQuotaModeKeep)

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
		decision := resolveProbeAction(item, http.StatusOK, "", rateLimit, deriveRateLimitUsedPercent(rateLimit), true, threshold, model.CodexInspectionShortWindowQuotaModeKeep)

		if decision.Action != "keep" ||
			decision.ActionReason != "5 小时额度达到阈值，但月额度仍可用，暂不禁用账号" ||
			decision.UsedPercent == nil ||
			*decision.UsedPercent != 5 ||
			decision.IsQuota {
			t.Fatalf("decision = %#v, want keep exhausted short window with healthy monthly quota", decision)
		}
	})
}

func TestResolveProbeActionShortWindowQuotaMode(t *testing.T) {
	threshold := 100.0
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

	cases := []struct {
		name       string
		disabled   bool
		mode       string
		wantAction string
		wantReason string
	}{
		{
			name:       "default keep mode keeps enabled short-window-exhausted account",
			disabled:   false,
			mode:       model.CodexInspectionShortWindowQuotaModeKeep,
			wantAction: "keep",
			wantReason: "5 小时额度达到阈值，但月额度仍可用，暂不禁用账号",
		},
		{
			name:       "default keep mode enables disabled short-window-exhausted account",
			disabled:   true,
			mode:       model.CodexInspectionShortWindowQuotaModeKeep,
			wantAction: "enable",
			wantReason: "月额度仍可用，建议立即启用账号",
		},
		{
			name:       "disable mode disables enabled short-window-exhausted account",
			disabled:   false,
			mode:       model.CodexInspectionShortWindowQuotaModeDisable,
			wantAction: "disable",
			wantReason: "5 小时额度达到阈值，但月额度仍可用，建议暂时禁用账号等待短窗口恢复",
		},
		{
			name:       "disable mode keeps disabled short-window-exhausted account disabled",
			disabled:   true,
			mode:       model.CodexInspectionShortWindowQuotaModeDisable,
			wantAction: "keep",
			wantReason: "5 小时额度达到阈值，但月额度仍可用，账号保持禁用等待短窗口恢复",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			decision := resolveProbeAction(
				account{DisplayAccount: "user@example.test", Disabled: tt.disabled},
				http.StatusOK,
				"",
				rateLimit,
				deriveRateLimitUsedPercent(rateLimit),
				true,
				threshold,
				tt.mode,
			)
			if decision.Action != tt.wantAction || decision.ActionReason != tt.wantReason {
				t.Fatalf("decision = %#v, want action=%q reason=%q", decision, tt.wantAction, tt.wantReason)
			}
			if decision.UsedPercent == nil || *decision.UsedPercent != 5 || decision.IsQuota {
				t.Fatalf("decision quota fields = %#v, want long-window percent and non-quota", decision)
			}
		})
	}
}

func TestRunFallsBackToManagementAPICallPath(t *testing.T) {
	var legacyAPICalls int
	var managementAPICalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth-files" && r.Method == http.MethodGet:
			http.NotFound(w, r)
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/api-call" && r.Method == http.MethodPost:
			legacyAPICalls++
			http.NotFound(w, r)
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			managementAPICalls++
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
	if legacyAPICalls != 1 || managementAPICalls != 1 {
		t.Fatalf("api-call counts legacy=%d management=%d, want 1/1", legacyAPICalls, managementAPICalls)
	}
}

func TestRunSendsDirectCodexAccountIDHeader(t *testing.T) {
	var accountIDHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account_id":"acct-direct","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/api-call" && r.Method == http.MethodPost:
			var payload struct {
				Header map[string]string `json:"header"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode api-call payload: %v", err)
			}
			accountIDHeader = payload.Header["Chatgpt-Account-Id"]
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
		t.Fatalf("Chatgpt-Account-Id = %q, want %q", accountIDHeader, "acct-direct")
	}
}

func TestRunFallsBackToManagementAuthFileActionPath(t *testing.T) {
	var legacyDeleteCalls int
	var managementDeleteCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth-files" && r.Method == http.MethodGet:
			http.NotFound(w, r)
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/api-call" && r.Method == http.MethodPost:
			http.NotFound(w, r)
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":401,"body":{"message":"unauthorized"}}`))
		case r.URL.Path == "/auth-files" && r.Method == http.MethodDelete:
			legacyDeleteCalls++
			http.NotFound(w, r)
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodDelete:
			managementDeleteCalls++
			if r.URL.Query().Get("name") != "auth-a.json" {
				t.Fatalf("delete name = %q, want auth-a.json", r.URL.Query().Get("name"))
			}
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
	if result.Run.DeleteCount != 1 || result.Run.KeepCount != 0 {
		t.Fatalf("run counts delete=%d keep=%d, want 1/0", result.Run.DeleteCount, result.Run.KeepCount)
	}
	if result.Results[0].Action != "delete" ||
		result.Results[0].ActionStatus != model.CodexInspectionActionStatusSuccess ||
		result.Results[0].ExecutedAction != "delete" {
		t.Fatalf("result after delete = %#v", result.Results[0])
	}
	if result.Run.Error != "" {
		t.Fatalf("run error = %q", result.Run.Error)
	}
	if legacyDeleteCalls != 1 || managementDeleteCalls != 1 {
		t.Fatalf("delete counts legacy=%d management=%d, want 1/1", legacyDeleteCalls, managementDeleteCalls)
	}
}

func TestRunFallsBackToManagementAuthFileStatusActionPath(t *testing.T) {
	var legacyPatchCalls int
	var managementPatchCalls int
	var patchedDisabled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth-files" && r.Method == http.MethodGet:
			http.NotFound(w, r)
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/api-call" && r.Method == http.MethodPost:
			http.NotFound(w, r)
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"message":"limit reached"}}`))
		case strings.HasPrefix(r.URL.Path, "/auth-files") && r.Method == http.MethodPatch:
			legacyPatchCalls++
			http.NotFound(w, r)
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodPatch:
			managementPatchCalls++
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
	if result.Run.DisableCount != 1 || result.Run.KeepCount != 0 {
		t.Fatalf("run counts disable=%d keep=%d, want 1/0", result.Run.DisableCount, result.Run.KeepCount)
	}
	if result.Run.DisabledCount != 1 {
		t.Fatalf("disabled count = %d, want 1", result.Run.DisabledCount)
	}
	if result.Run.Error != "" {
		t.Fatalf("run error = %q", result.Run.Error)
	}
	if legacyPatchCalls != 2 || managementPatchCalls != 1 {
		t.Fatalf("patch counts legacy=%d management=%d, want 2/1", legacyPatchCalls, managementPatchCalls)
	}
	if !patchedDisabled {
		t.Fatal("management patch did not disable the auth file")
	}
	if result.Results[0].Action != "disable" ||
		result.Results[0].ActionStatus != model.CodexInspectionActionStatusSuccess ||
		result.Results[0].ExecutedAction != "disable" {
		t.Fatalf("result after disable = %#v", result.Results[0])
	}
}

func TestExecuteManualActionsProcessesCompletedRunResults(t *testing.T) {
	var patchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"ok":true}}`))
		case strings.HasPrefix(r.URL.Path, "/auth-files") && r.Method == http.MethodPatch:
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
		case r.URL.Path == "/auth-files" && r.Method == http.MethodGet:
			authFilesCalls++
			if authFilesCalls == 1 {
				_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":401,"body":{"message":"Your authentication token has been invalidated."}}`))
		case strings.HasPrefix(r.URL.Path, "/auth-files") && r.Method == http.MethodDelete:
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
		case r.URL.Path == "/auth-files" && r.Method == http.MethodGet:
			authFilesCalls++
			if authFilesCalls == 1 {
				_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
				return
			}
			_, _ = w.Write([]byte(`{"files":[]}`))
		case r.URL.Path == "/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"message":"limit reached"}}`))
		case strings.HasPrefix(r.URL.Path, "/auth-files") && r.Method == http.MethodPatch:
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
		case r.URL.Path == "/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"},{"name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":401,"body":{"message":"Your authentication token has been invalidated."}}`))
		case strings.HasPrefix(r.URL.Path, "/auth-files") && r.Method == http.MethodDelete:
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
		case r.URL.Path == "/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/api-call" && r.Method == http.MethodPost:
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

func TestExecuteActionReturnsCombinedPatchFallbackErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPatch && r.URL.Path == "/auth-files":
			http.Error(w, "primary patch failed", http.StatusInternalServerError)
		case r.Method == http.MethodPatch && r.URL.Path == "/auth-files/status":
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
		t.Fatal("execute action succeeded, want combined patch error")
	}
	message := err.Error()
	if !strings.Contains(message, "/auth-files:") ||
		!strings.Contains(message, "primary patch failed") ||
		!strings.Contains(message, "/auth-files/status:") ||
		!strings.Contains(message, "status patch failed") {
		t.Fatalf("combined patch error = %q", message)
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
		case r.URL.Path == "/auth-files" && r.Method == http.MethodGet:
			switch fixture {
			case mixedAutoActionFixtureEnableDelete:
				_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"},{"name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
			case mixedAutoActionFixtureDisableDelete:
				_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","status":"ok","state":"ready"},{"name":"auth-a.json","auth_index":"auth-2","provider":"codex","account":"bob@example.com","status":"ok","state":"ready"}]}`))
			default:
				t.Fatalf("unexpected mixed fixture %q", fixture)
			}
		case r.URL.Path == "/api-call" && r.Method == http.MethodPost:
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
				_, _ = w.Write([]byte(`{"status_code":401,"body":{"message":"Your authentication token has been invalidated."}}`))
			default:
				t.Fatalf("unexpected authIndex %q", payload.AuthIndex)
			}
		case strings.HasPrefix(r.URL.Path, "/auth-files") && r.Method == http.MethodDelete:
			*deleteCalled = true
			_, _ = w.Write([]byte(`{"ok":true}`))
		case strings.HasPrefix(r.URL.Path, "/auth-files") && r.Method == http.MethodPatch:
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
