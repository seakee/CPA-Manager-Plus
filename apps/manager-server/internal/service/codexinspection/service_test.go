package codexinspection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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

const xaiCompletedInferenceAPICallResponse = `{"status_code":200,"body":{"object":"response","status":"completed","error":null,"output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}]}}`

func TestXAIClassificationMatchesSharedFixtures(t *testing.T) {
	type fixtureCase struct {
		Name       string `json:"name"`
		StatusCode int    `json:"statusCode"`
		Body       any    `json:"body"`
		Expected   struct {
			Classification string `json:"classification"`
			Action         string `json:"action"`
			ReasonCode     string `json:"reasonCode"`
		} `json:"expected"`
	}
	data, err := os.ReadFile("../../../../../tests/fixtures/xai-inspection-cases.json")
	if err != nil {
		t.Fatalf("read shared xAI fixtures: %v", err)
	}
	var fixtures []fixtureCase
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("decode shared xAI fixtures: %v", err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			classification := xaiClassification(fixture.StatusCode, fixture.Body)
			decision := xaiDecision(
				fixture.StatusCode,
				classification,
				fmt.Sprint(fixture.Body),
			)
			if decision.Classification != fixture.Expected.Classification || decision.Action != fixture.Expected.Action || decision.ReasonCode != fixture.Expected.ReasonCode {
				t.Fatalf("decision = %#v, want classification=%q action=%q reasonCode=%q", decision, fixture.Expected.Classification, fixture.Expected.Action, fixture.Expected.ReasonCode)
			}
		})
	}
}

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
		if logEntry.Message == "凭证健康巡检开始" {
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

func TestRunXAISkipsInferenceWhenDisabled(t *testing.T) {
	requestedURLs := make([]string, 0, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"xai-auth.json","auth_index":"xai-1","provider":"xai","auth_kind":"oauth","account":"xai@example.com","user":{"id":"user-1"}}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			var payload struct {
				Method string `json:"method"`
				URL    string `json:"url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode api-call payload: %v", err)
			}
			requestedURLs = append(requestedURLs, payload.URL)
			if strings.HasSuffix(payload.URL, "/responses") {
				t.Fatalf("disabled xAI inference requested %s", payload.URL)
			}
			if strings.Contains(payload.URL, "format=credits") {
				_, _ = w.Write([]byte(`{"status_code":200,"body":{"config":{"credit_usage_percent":25,"current_period":{"end":"2026-07-22T00:00:00Z"}}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"config":{"monthly_limit":10000,"used":4000,"billing_period_end":"2026-08-01T00:00:00Z"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetType = "xai"
	managerCfg.CodexInspection.XAIInferenceEnabled = false
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run xAI inspection: %v", err)
	}
	if len(requestedURLs) != 2 {
		t.Fatalf("requested URLs = %#v, want weekly and monthly billing only", requestedURLs)
	}
	if len(result.Results) != 1 || result.Results[0].Action != "keep" || result.Results[0].ErrorKind != "billing_healthy" {
		t.Fatalf("xAI billing-only result = %#v", result.Results)
	}
	if result.Results[0].StatusCode == nil || *result.Results[0].StatusCode != http.StatusOK {
		t.Fatalf("xAI billing-only status code = %#v, want %d", result.Results[0].StatusCode, http.StatusOK)
	}
	if result.Results[0].AutoRecoverEligible {
		t.Fatalf("billing-only inspection enabled auto recovery: %#v", result.Results[0])
	}
}

func TestRunXAIBillingOnlyPrioritizesBlockingFailureOverPartialSummary(t *testing.T) {
	requestedURLs := make([]string, 0, 2)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"xai-auth.json","auth_index":"xai-1","provider":"xai","account":"xai@example.com"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			var payload struct {
				URL string `json:"url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode api-call payload: %v", err)
			}
			requestedURLs = append(requestedURLs, payload.URL)
			if strings.Contains(payload.URL, "format=credits") {
				_, _ = w.Write([]byte(`{"status_code":200,"body":{"config":{"credit_usage_percent":3,"current_period":{"end":"2026-07-29T00:00:00Z"}}}}`))
				return
			}
			_, _ = w.Write([]byte(`{"status_code":402,"body":{"code":"personal-team-blocked:spending-limit","error":"You have run out of credits or need a Grok subscription."}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetType = "xai"
	managerCfg.CodexInspection.XAIInferenceEnabled = false
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run xAI inspection: %v", err)
	}
	if len(requestedURLs) != 2 {
		t.Fatalf("requested URLs = %#v, want weekly and monthly billing only", requestedURLs)
	}
	if len(result.Results) != 1 {
		t.Fatalf("xAI result = %#v", result.Results)
	}
	item := result.Results[0]
	if item.Action != "disable" || item.ErrorKind != "spending_limit" || item.StatusCode == nil || *item.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("xAI partial blocking result = %#v", item)
	}
	if len(item.QuotaWindows) != 1 || item.QuotaWindows[0].ID != "xai-weekly" {
		t.Fatalf("xAI partial blocking quota windows = %#v", item.QuotaWindows)
	}
}

func TestResolveXAIBasicInspectionResultClassifiesNonBlockingPartialBilling(t *testing.T) {
	usage := float64(25)
	result := resolveXAIBasicInspectionResult(
		model.CodexInspectionResult{},
		xaiBillingProbe{
			Summary:  &xaiBillingSummary{UsagePercent: &usage, HasWeeklyData: true},
			Failures: []xaiProbeDecision{*xaiDecision(http.StatusServiceUnavailable, "upstream_error", "monthly billing unavailable")},
			Partial:  true,
			Healthy:  true,
		},
	)
	if result.Action != "keep" || result.ErrorKind != "billing_partial" || result.ActionReason != "monitoring.xai_inspection_reason_billing_partial" {
		t.Fatalf("xAI partial billing result = %#v", result)
	}
}

func TestRunXAIUsesBillingAndInferenceEndpoints(t *testing.T) {
	const customModel = "grok-custom"
	const customPrompt = "Return a short health response."
	const customUserAgent = "xai-custom-agent"
	requestedURLs := make([]string, 0, 3)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"xai-auth.json","auth_index":"xai-1","provider":"xai","auth_kind":"oauth","account":"xai@example.com","user":{"id":"user-1"}}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			var payload struct {
				Method string            `json:"method"`
				URL    string            `json:"url"`
				Header map[string]string `json:"header"`
				Data   string            `json:"data"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode api-call payload: %v", err)
			}
			requestedURLs = append(requestedURLs, payload.URL)
			if strings.Contains(payload.URL, "chatgpt.com") {
				t.Fatalf("xAI inspection called Codex endpoint: %s", payload.URL)
			}
			if payload.Header["x-grok-client-version"] != xaiGrokVersion || payload.Header["x-userid"] != "user-1" {
				t.Fatalf("xAI headers = %#v", payload.Header)
			}
			if strings.Contains(payload.URL, "format=credits") {
				if payload.Method != http.MethodGet {
					t.Fatalf("weekly billing method = %q, want GET", payload.Method)
				}
				_, _ = w.Write([]byte(`{"status_code":200,"body":{"config":{"credit_usage_percent":25,"current_period":{"end":"2026-07-22T00:00:00Z"}}}}`))
				return
			}
			if strings.HasSuffix(payload.URL, "/responses") {
				if payload.Method != http.MethodPost {
					t.Fatalf("xAI inference method = %q, want POST", payload.Method)
				}
				if payload.Header["Accept"] != "application/json" {
					t.Fatalf("xAI inference accept = %q, want application/json", payload.Header["Accept"])
				}
				if payload.Header["User-Agent"] != customUserAgent {
					t.Fatalf("xAI inference user agent = %q, want %q", payload.Header["User-Agent"], customUserAgent)
				}
				var requestData map[string]any
				if err := json.Unmarshal([]byte(payload.Data), &requestData); err != nil {
					t.Fatalf("decode xAI inference data: %v", err)
				}
				if requestData["model"] != customModel || requestData["stream"] != false {
					t.Fatalf("xAI inference data = %#v", requestData)
				}
				if requestData["input"] != customPrompt {
					t.Fatalf("xAI inference prompt = %#v", requestData["input"])
				}
				_, _ = w.Write([]byte(xaiCompletedInferenceAPICallResponse))
				return
			}
			if payload.Method != http.MethodGet {
				t.Fatalf("monthly billing method = %q, want GET", payload.Method)
			}
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"config":{"monthly_limit":10000,"used":4000,"billing_period_end":"2026-08-01T00:00:00Z"}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetType = "xai"
	managerCfg.CodexInspection.XAIInferenceUserAgent = customUserAgent
	managerCfg.CodexInspection.XAIInferenceModel = customModel
	managerCfg.CodexInspection.XAIInferencePrompt = customPrompt
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run xAI inspection: %v", err)
	}
	if len(requestedURLs) != 3 || !strings.HasSuffix(requestedURLs[2], "/responses") {
		t.Fatalf("requested URLs = %#v, want weekly/monthly billing and inference", requestedURLs)
	}
	if len(result.Results) != 1 || result.Results[0].Provider != "xai" || result.Results[0].Action != "keep" {
		t.Fatalf("xAI result = %#v", result.Results)
	}
	if result.Results[0].ErrorKind != "inference_healthy" || len(result.Results[0].QuotaWindows) != 2 {
		t.Fatalf("xAI inference result = %#v", result.Results[0])
	}
	if result.Results[0].PlanType != "" {
		t.Fatalf("xAI plan type = %q, want empty", result.Results[0].PlanType)
	}
}

func TestResolveXAIInferenceURLMatchesRuntimeUsingAPISemantics(t *testing.T) {
	tests := []struct {
		name          string
		file          authFile
		forceOfficial bool
		wantURL       string
		wantCLI       bool
	}{
		{
			name:          "verified official identity forces official api",
			file:          authFile{},
			forceOfficial: true,
			wantURL:       xaiOfficialAPIBaseURL + "/responses",
			wantCLI:       false,
		},
		{
			name:    "missing auth metadata defaults to cli proxy",
			file:    authFile{},
			wantURL: xaiCLIChatProxyBaseURL + "/responses",
			wantCLI: true,
		},
		{
			name:    "missing auth metadata ignores official default base",
			file:    authFile{"base_url": xaiOfficialAPIBaseURL},
			wantURL: xaiCLIChatProxyBaseURL + "/responses",
			wantCLI: true,
		},
		{
			name:    "oauth defaults to cli proxy",
			file:    authFile{"auth_kind": "oauth", "base_url": xaiOfficialAPIBaseURL},
			wantURL: xaiCLIChatProxyBaseURL + "/responses",
			wantCLI: true,
		},
		{
			name:    "explicit false defaults to cli proxy without auth kind",
			file:    authFile{"using_api": false, "base_url": xaiOfficialAPIBaseURL},
			wantURL: xaiCLIChatProxyBaseURL + "/responses",
			wantCLI: true,
		},
		{
			name:    "api credential defaults to official api",
			file:    authFile{"auth_kind": "apikey"},
			wantURL: xaiOfficialAPIBaseURL + "/responses",
			wantCLI: false,
		},
		{
			name:    "custom base url is preserved",
			file:    authFile{"using_api": false, "base_url": "https://xai.example.test/v1"},
			wantURL: "https://xai.example.test/v1/responses",
			wantCLI: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotURL, gotCLI := resolveXAIInferenceURL(tt.file, tt.forceOfficial)
			if gotURL != tt.wantURL || gotCLI != tt.wantCLI {
				t.Fatalf("resolveXAIInferenceURL() = %q, %t; want %q, %t", gotURL, gotCLI, tt.wantURL, tt.wantCLI)
			}
		})
	}
}

func TestRunCombinedTargetsSamplesEachCredentialProvider(t *testing.T) {
	requestedInference := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"codex.json","auth_index":"codex-1","provider":"codex","account":"codex@example.com"},{"name":"xai-a.json","auth_index":"xai-1","provider":"xai","account":"xai-a@example.com"},{"name":"xai-b.json","auth_index":"xai-2","provider":"xai","account":"xai-b@example.com"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			var payload struct {
				URL string `json:"url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode api-call payload: %v", err)
			}
			switch {
			case strings.Contains(payload.URL, "chatgpt.com"):
				_, _ = w.Write([]byte(`{"status_code":200,"body":{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000}}}}`))
			case strings.HasSuffix(payload.URL, "/responses"):
				requestedInference++
				_, _ = w.Write([]byte(xaiCompletedInferenceAPICallResponse))
			case strings.Contains(payload.URL, "/billing"):
				_, _ = w.Write([]byte(`{"status_code":200,"body":{"config":{"credit_usage_percent":20,"current_period":{"end":"2026-07-22T00:00:00Z"}}}}`))
			default:
				t.Fatalf("unexpected provider URL %q", payload.URL)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetTypes = []string{model.CodexInspectionTargetCodex, model.CodexInspectionTargetXAI}
	managerCfg.CodexInspection.TargetType = model.CodexInspectionTargetCodex
	managerCfg.CodexInspection.SampleSize = 1
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	detail, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run combined credential inspection: %v", err)
	}
	if detail.Run.ProbeSetCount != 3 || detail.Run.SampledCount != 2 || len(detail.Results) != 2 {
		t.Fatalf("combined run = %#v, results=%#v", detail.Run, detail.Results)
	}
	providers := map[string]bool{}
	for _, result := range detail.Results {
		providers[result.Provider] = true
	}
	if !providers["codex"] || !providers["xai"] || requestedInference != 1 {
		t.Fatalf("providers=%#v inference=%d", providers, requestedInference)
	}
}

func TestRunXAIFallsBackToOfficialAPIIdentityHealth(t *testing.T) {
	requestedURLs := make([]string, 0, 3)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"paid-xai.json","auth_index":"xai-paid-1","provider":"xai","account":"paid@example.com","disabled":true,"user":{"id":"user-1"}}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			var payload struct {
				Method string            `json:"method"`
				URL    string            `json:"url"`
				Header map[string]string `json:"header"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode api-call payload: %v", err)
			}
			requestedURLs = append(requestedURLs, payload.URL)
			if strings.HasSuffix(payload.URL, "/responses") {
				if payload.Method != http.MethodPost {
					t.Fatalf("xAI inference method = %q, want POST", payload.Method)
				}
				if payload.URL != xaiOfficialAPIBaseURL+"/responses" {
					t.Fatalf("xAI inference URL = %q, want official API", payload.URL)
				}
				if payload.Header["x-xai-token-auth"] != "" || payload.Header["x-grok-client-version"] != "" || payload.Header["x-userid"] != "" {
					t.Fatalf("xAI official inference headers = %#v", payload.Header)
				}
				_, _ = w.Write([]byte(xaiCompletedInferenceAPICallResponse))
				return
			}
			if payload.Method != http.MethodGet {
				t.Fatalf("xAI billing health method = %q, want GET", payload.Method)
			}
			if payload.URL == xaiOfficialAPIMeURL {
				if payload.Header["Authorization"] != "Bearer $TOKEN$" || payload.Header["x-grok-client-version"] != "" {
					t.Fatalf("xAI official API headers = %#v", payload.Header)
				}
				_, _ = w.Write([]byte(`{"status_code":200,"body":{"user_id":"user-1","team_id":"team-1","team_blocked":false}}`))
				return
			}
			_, _ = w.Write([]byte(`{"status_code":403,"body":{"error":"Access denied"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetType = "xai"
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}

	result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run xAI inspection: %v", err)
	}
	if len(requestedURLs) != 4 || requestedURLs[2] != xaiOfficialAPIMeURL || requestedURLs[3] != xaiOfficialAPIBaseURL+"/responses" {
		t.Fatalf("requested URLs = %#v, want billing, identity fallback, and inference", requestedURLs)
	}
	if len(result.Results) != 1 {
		t.Fatalf("xAI result = %#v", result.Results)
	}
	item := result.Results[0]
	if item.Action != "keep" || item.ErrorKind != "inference_healthy" || item.StatusCode == nil || *item.StatusCode != http.StatusOK {
		t.Fatalf("xAI inference result = %#v", item)
	}
	if item.ActionReason != "monitoring.xai_inspection_reason_inference_manual_disable" {
		t.Fatalf("xAI inference action reason = %q", item.ActionReason)
	}
	if item.UsedPercent != nil || len(item.QuotaWindows) != 0 || item.AutoRecoverEligible {
		t.Fatalf("xAI official API synthesized quota or recovery = %#v", item)
	}
}

func TestResolveXAIBasicInspectionResultUsesOfficialAPIHealthyKind(t *testing.T) {
	result := resolveXAIBasicInspectionResult(
		model.CodexInspectionResult{},
		xaiBillingProbe{OfficialAPIHealthy: true},
	)
	if result.Action != "keep" || result.ErrorKind != "official_api_healthy" || result.ActionReason != "monitoring.xai_inspection_reason_official_api_healthy" {
		t.Fatalf("official API health result = %#v", result)
	}
}

func TestXAISummaryWindowsSkipsZeroOnDemandCapWithoutUsage(t *testing.T) {
	zero := float64(0)
	windows := xaiSummaryWindows(&xaiBillingSummary{OnDemandCapCents: &zero})
	for _, window := range windows {
		if window.ID == "xai-on-demand" {
			t.Fatalf("zero on-demand cap produced quota window: %#v", windows)
		}
	}
}

func TestXAISummaryWindowsDoesNotCreateMonthlyWindowFromOnDemandOnlyData(t *testing.T) {
	capCents := float64(5000)
	usedPercent := float64(20)
	windows := xaiSummaryWindows(&xaiBillingSummary{
		OnDemandCapCents:    &capCents,
		OnDemandUsedPercent: &usedPercent,
		HasMonthlyData:      true,
		BillingPeriodEnd:    "2026-08-01T00:00:00Z",
	})
	if len(windows) != 1 || windows[0].ID != "xai-on-demand" {
		t.Fatalf("on-demand-only windows = %#v, want on-demand only", windows)
	}
}

func TestParseXAIBillingSummaryDoesNotCreateMonthlyWindowFromWeeklyZeroOnDemandData(t *testing.T) {
	summary := parseXAIBillingSummary(map[string]any{
		"currentPeriod": map[string]any{
			"type": "USAGE_PERIOD_TYPE_WEEKLY",
			"end":  "2026-07-29T00:00:00+00:00",
		},
		"onDemandCap":      map[string]any{"val": 0},
		"onDemandUsed":     map[string]any{"val": 0},
		"billingPeriodEnd": "2026-07-29T00:00:00+00:00",
	})
	if summary == nil {
		t.Fatal("summary is nil")
	}
	windows := xaiSummaryWindows(summary)
	if len(windows) != 1 || windows[0].ID != "xai-weekly" {
		t.Fatalf("weekly zero on-demand windows = %#v, want weekly only", windows)
	}
}

func TestHasCompletedXAIInferenceOutput(t *testing.T) {
	tests := []struct {
		name string
		body any
		want bool
	}{
		{
			name: "completed output",
			body: map[string]any{
				"status": "completed",
				"error":  nil,
				"output": []any{map[string]any{
					"type":    "message",
					"content": []any{map[string]any{"type": "output_text", "text": "OK"}},
				}},
			},
			want: true,
		},
		{name: "empty body", body: nil, want: false},
		{name: "incomplete status", body: map[string]any{"status": "incomplete"}, want: false},
		{name: "completed without output", body: map[string]any{"status": "completed", "output": []any{}}, want: false},
		{name: "completed with error", body: map[string]any{"status": "completed", "error": map[string]any{"message": "failed"}}, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := hasCompletedXAIInferenceOutput(tc.body, "")
			if got != tc.want {
				t.Fatalf("hasCompletedXAIInferenceOutput() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestRunXAIDoesNotFallbackToOfficialAPIForExplicitBillingDenials(t *testing.T) {
	tests := []struct {
		name           string
		apiCallBody    string
		classification string
	}{
		{name: "entitlement denied", apiCallBody: `{"status_code":403,"body":{"error":"Need a Grok subscription"}}`, classification: "entitlement_denied"},
		{name: "payment required", apiCallBody: `{"status_code":402,"body":{"error":"Payment required"}}`, classification: "quota_or_entitlement_unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requestedURLs := make([]string, 0, 2)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
					_, _ = w.Write([]byte(`{"files":[{"name":"paid-xai.json","auth_index":"xai-paid-1","provider":"xai","account":"paid@example.com"}]}`))
				case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
					var payload struct {
						URL string `json:"url"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Fatalf("decode api-call payload: %v", err)
					}
					requestedURLs = append(requestedURLs, payload.URL)
					_, _ = w.Write([]byte(tc.apiCallBody))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(upstream.Close)

			db := newCodexInspectionTestStore(t)
			managerCfg := newCodexInspectionManagerConfig(upstream.URL)
			managerCfg.CodexInspection.TargetType = "xai"
			managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
			if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
				t.Fatalf("save manager config: %v", err)
			}

			result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
			if err != nil {
				t.Fatalf("run xAI inspection: %v", err)
			}
			if len(requestedURLs) != 3 || !strings.HasSuffix(requestedURLs[2], "/responses") {
				t.Fatalf("requested URLs = %#v, want billing requests followed by inference", requestedURLs)
			}
			for _, requestedURL := range requestedURLs {
				if requestedURL == xaiOfficialAPIMeURL {
					t.Fatalf("explicit billing denial called official API fallback: %#v", requestedURLs)
				}
			}
			if len(result.Results) != 1 || result.Results[0].ErrorKind != tc.classification {
				t.Fatalf("xAI result = %#v, want %q", result.Results, tc.classification)
			}
		})
	}
}

func TestRunXAIRejectsInvalidOfficialAPIIdentityPayload(t *testing.T) {
	tests := []struct {
		name        string
		apiCallBody string
	}{
		{name: "null team blocked", apiCallBody: `{"status_code":200,"body":{"user_id":"","team_id":"","team_blocked":null}}`},
		{name: "invalid team blocked", apiCallBody: `{"status_code":200,"body":{"user_id":" ","team_id":"","team_blocked":"unknown"}}`},
		{name: "numeric team blocked", apiCallBody: `{"status_code":200,"body":{"user_id":"","team_id":"","team_blocked":0}}`},
		{name: "non-string identity", apiCallBody: `{"status_code":200,"body":{"user_id":false,"team_id":"","team_blocked":null}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requestedURLs := make([]string, 0, 3)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
					_, _ = w.Write([]byte(`{"files":[{"name":"paid-xai.json","auth_index":"xai-paid-1","provider":"xai","account":"paid@example.com"}]}`))
				case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
					var payload struct {
						URL string `json:"url"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Fatalf("decode api-call payload: %v", err)
					}
					requestedURLs = append(requestedURLs, payload.URL)
					if payload.URL == xaiOfficialAPIMeURL {
						_, _ = w.Write([]byte(tc.apiCallBody))
						return
					}
					_, _ = w.Write([]byte(`{"status_code":403,"body":{"error":"Access denied"}}`))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(upstream.Close)

			db := newCodexInspectionTestStore(t)
			managerCfg := newCodexInspectionManagerConfig(upstream.URL)
			managerCfg.CodexInspection.TargetType = "xai"
			managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
			if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
				t.Fatalf("save manager config: %v", err)
			}

			result, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
			if err != nil {
				t.Fatalf("run xAI inspection: %v", err)
			}
			if len(requestedURLs) != 4 || requestedURLs[2] != xaiOfficialAPIMeURL || requestedURLs[3] != xaiCLIChatProxyBaseURL+"/responses" {
				t.Fatalf("requested URLs = %#v, want billing, rejected identity fallback, and CLI inference", requestedURLs)
			}
			if len(result.Results) != 1 || result.Results[0].ErrorKind == "official_api_healthy" {
				t.Fatalf("invalid official API payload reported healthy: %#v", result.Results)
			}
		})
	}
}

func TestRunXAIFailedBillingNeverReportsHealthyAndRetriesTransientFailures(t *testing.T) {
	tests := []struct {
		name           string
		apiCallBody    string
		classification string
		statusCode     int
	}{
		{name: "rate limited", apiCallBody: `{"status_code":429,"body":{"error":"too many requests"}}`, classification: "rate_limited", statusCode: http.StatusTooManyRequests},
		{name: "upstream error", apiCallBody: `{"status_code":503,"body":{"error":"service unavailable"}}`, classification: "upstream_error", statusCode: http.StatusServiceUnavailable},
		{name: "empty payload", apiCallBody: `{"status_code":200,"body":{"config":{}}}`, classification: "protocol_changed", statusCode: http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			requestCount := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
					_, _ = w.Write([]byte(`{"files":[{"name":"xai-auth.json","auth_index":"xai-1","provider":"xai","account":"xai@example.com"}]}`))
				case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
					requestCount++
					_, _ = w.Write([]byte(tc.apiCallBody))
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(upstream.Close)

			db := newCodexInspectionTestStore(t)
			managerCfg := newCodexInspectionManagerConfig(upstream.URL)
			managerCfg.CodexInspection.TargetType = "xai"
			managerCfg.CodexInspection.Retries = 1
			if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
				t.Fatalf("save manager config: %v", err)
			}

			detail, err := newCodexInspectionTestService(t, db).Run(context.Background(), RunRequest{TriggerType: "manual"})
			if err != nil {
				t.Fatalf("run xAI inspection: %v", err)
			}
			wantRequestCount := 6
			if requestCount != wantRequestCount {
				t.Fatalf("billing and inference requests = %d, want %d", requestCount, wantRequestCount)
			}
			if len(detail.Results) != 1 {
				t.Fatalf("results = %#v", detail.Results)
			}
			result := detail.Results[0]
			if result.ErrorKind != tc.classification || result.ErrorKind == "billing_healthy" || result.Action != "keep" {
				t.Fatalf("result = %#v, want classification %q and keep", result, tc.classification)
			}
			if tc.statusCode > 0 && (result.StatusCode == nil || *result.StatusCode != tc.statusCode) {
				t.Fatalf("status code = %#v, want %d", result.StatusCode, tc.statusCode)
			}
		})
	}
}

func TestXAIRelevantFailureUsesFrontendPriority(t *testing.T) {
	tests := []struct {
		name       string
		failures   []xaiProbeDecision
		wantClass  string
		wantAction string
	}{
		{
			name: "auth invalid over generic forbidden",
			failures: []xaiProbeDecision{
				*xaiDecision(http.StatusForbidden, "permission_unknown", "forbidden"),
				*xaiDecision(http.StatusUnauthorized, "auth_invalid", "expired"),
			},
			wantClass:  "auth_invalid",
			wantAction: "reauth",
		},
		{
			name: "entitlement denial over earlier generic forbidden",
			failures: []xaiProbeDecision{
				*xaiDecision(http.StatusForbidden, "permission_unknown", "forbidden"),
				*xaiDecision(http.StatusForbidden, "entitlement_denied", "subscription required"),
			},
			wantClass:  "entitlement_denied",
			wantAction: "disable",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			failure, ok := xaiRelevantFailure(tc.failures, true)
			if !ok || failure.Classification != tc.wantClass || failure.Action != tc.wantAction {
				t.Fatalf("selected failure = %#v, ok=%v", failure, ok)
			}
		})
	}
}

func TestParseXAIBillingSummarySupportsCentsObjectsCamelCaseAndOnDemand(t *testing.T) {
	summary := parseXAIBillingSummary(map[string]any{
		"monthlyLimit": map[string]any{"val": "10000"},
		"used":         map[string]any{"val": "15000"},
		"onDemandCap":  map[string]any{"val": "10000"},
		"productUsage": []any{map[string]any{"product": "grok", "usagePercent": 25.0}},
	})
	if summary == nil {
		t.Fatal("summary is nil")
	}
	if summary.UsedPercent == nil || *summary.UsedPercent != 100 {
		t.Fatalf("used percent = %#v, want 100", summary.UsedPercent)
	}
	if summary.MonthlyLimitCents == nil || *summary.MonthlyLimitCents != 10000 {
		t.Fatalf("monthly limit = %#v, want 10000", summary.MonthlyLimitCents)
	}
	if summary.OnDemandCapCents == nil || *summary.OnDemandCapCents != 10000 {
		t.Fatalf("on-demand cap = %#v, want 10000", summary.OnDemandCapCents)
	}
	if summary.OnDemandUsedPercent == nil || *summary.OnDemandUsedPercent != 50 {
		t.Fatalf("on-demand percent = %#v, want 50", summary.OnDemandUsedPercent)
	}
	if len(summary.ProductUsage) != 1 || summary.ProductUsage[0].Product != "grok" || summary.ProductUsage[0].UsagePercent == nil || *summary.ProductUsage[0].UsagePercent != 25 {
		t.Fatalf("product usage = %#v", summary.ProductUsage)
	}
}

func TestXAIMonthlyOnlySummaryDoesNotCreateWeeklyWindow(t *testing.T) {
	summary := parseXAIBillingSummary(map[string]any{
		"monthly_limit":      10000,
		"used":               2500,
		"billing_period_end": "2026-08-01T00:00:00Z",
	})
	if summary == nil {
		t.Fatal("summary is nil")
	}
	windows := xaiSummaryWindows(summary)
	if len(windows) != 1 || windows[0].ID != "xai-monthly" {
		t.Fatalf("monthly-only windows = %#v", windows)
	}
}

func TestExecuteManualActionsAllowsXAIReauthDeleteOverride(t *testing.T) {
	deleteCalled := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"xai-auth.json","auth_index":"xai-1","provider":"xai","account":"xai@example.com"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":401,"body":{"code":"unauthenticated:bad-credentials"}}`))
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodDelete:
			deleteCalled = true
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetType = "xai"
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)
	runDetail, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual"})
	if err != nil {
		t.Fatalf("run xAI inspection: %v", err)
	}
	if len(runDetail.Results) != 1 || runDetail.Results[0].Action != "reauth" {
		t.Fatalf("xAI reauth result = %#v", runDetail.Results)
	}

	result, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID},
		ActionOverrides: []ManualActionOverride{{
			ResultID: runDetail.Results[0].ID,
			Action:   "delete",
		}},
	})
	if err != nil {
		t.Fatalf("delete xAI reauth result: %v", err)
	}
	if !deleteCalled {
		t.Fatal("xAI reauth delete override did not delete auth file")
	}
	if len(result.Outcomes) != 1 || !result.Outcomes[0].Success || result.Outcomes[0].Action != "delete" {
		t.Fatalf("delete outcomes = %#v", result.Outcomes)
	}
	if len(result.Detail.Results) != 1 || result.Detail.Results[0].ExecutedAction != "delete" {
		t.Fatalf("updated result = %#v", result.Detail.Results)
	}

	repeated, err := svc.ExecuteManualActions(context.Background(), runDetail.Run.ID, ExecuteActionsRequest{
		ResultIDs: []int64{runDetail.Results[0].ID},
		ActionOverrides: []ManualActionOverride{{
			ResultID: runDetail.Results[0].ID,
			Action:   "delete",
		}},
	})
	if err != nil {
		t.Fatalf("repeat xAI reauth delete: %v", err)
	}
	if len(repeated.Detail.Results) != 1 || repeated.Detail.Results[0].ActionStatus != model.CodexInspectionActionStatusSuccess || repeated.Detail.Results[0].ExecutedAction != "delete" {
		t.Fatalf("repeated result lost successful delete state: %#v", repeated.Detail.Results)
	}
}

func TestMatchCurrentAccountRejectsProviderReplacement(t *testing.T) {
	result := model.CodexInspectionResult{FileName: "shared.json", Provider: "xai", AuthIndex: "shared-auth"}
	if _, ok := matchCurrentAccount([]account{{FileName: "shared.json", Provider: "codex", AuthIndex: "shared-auth"}}, result); ok {
		t.Fatal("xAI inspection result matched a Codex replacement")
	}
	if _, ok := matchCurrentAccount([]account{{FileName: "shared.json", Provider: "x-ai", AuthIndex: "shared-auth"}}, result); !ok {
		t.Fatal("normalized xAI provider alias did not match")
	}
}

func TestApplyManualActionOverridesRejectsUnsafeTransitions(t *testing.T) {
	results := []model.CodexInspectionResult{
		{ID: 1, Action: "reauth"},
		{ID: 2, Action: "keep"},
	}
	selected := map[int64]struct{}{1: {}, 2: {}}

	for _, overrides := range [][]ManualActionOverride{
		{{ResultID: 1, Action: "disable"}},
		{{ResultID: 2, Action: "delete"}},
		{{ResultID: 3, Action: "delete"}},
	} {
		if _, err := applyManualActionOverrides(results, selected, overrides); !errors.Is(err, ErrInvalidActionOverride) {
			t.Fatalf("overrides %#v error = %v, want ErrInvalidActionOverride", overrides, err)
		}
	}
}

func TestFetchAuthFilesStreamsResponsesLargerThanEightMiB(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","padding":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", 8*1024*1024)))
		_, _ = w.Write([]byte(`"}]}`))
	}))
	t.Cleanup(upstream.Close)

	svc := New(newCodexInspectionTestStore(t), nil, upstream.Client())
	files, err := svc.fetchAuthFiles(context.Background(), store.Setup{
		CPAUpstreamURL: upstream.URL,
		ManagementKey:  "management-key",
	})
	if err != nil {
		t.Fatalf("fetch auth files: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %d, want 1", len(files))
	}
	if name := readString(files[0], "name"); name != "auth-a.json" {
		t.Fatalf("file name = %q, want auth-a.json", name)
	}
}

func TestRequestCodexUsageStreamsResponsesLargerThanEightMiB(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/api-call" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"status_code":200,"body":{"padding":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", 8*1024*1024)))
		_, _ = w.Write([]byte(`"}}`))
	}))
	t.Cleanup(upstream.Close)

	svc := New(nil, nil, upstream.Client())
	result, _, err := svc.requestCodexUsageAt(
		context.Background(),
		store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
		model.ManagerCodexInspectionConfig{},
		account{AuthIndex: "auth-1"},
		"/v0/management/api-call",
	)
	if err != nil {
		t.Fatalf("request Codex usage: %v", err)
	}
	body, ok := result.Body.(map[string]any)
	if !ok {
		t.Fatalf("body = %#v, want map", result.Body)
	}
	if padding := readString(body, "padding"); len(padding) != 8*1024*1024 {
		t.Fatalf("padding length = %d, want %d", len(padding), 8*1024*1024)
	}
}

func TestDecodeCPAAPICallResponseRejectsOversizedBody(t *testing.T) {
	body := `{"status_code":200,"body":{"padding":"` + strings.Repeat("x", 256) + `"}}`
	var raw map[string]any
	err := decodeCPAAPICallResponse(strings.NewReader(body), 128, &raw)
	if !errors.Is(err, errCPAAPICallResponseTooLarge) {
		t.Fatalf("decodeCPAAPICallResponse() error = %v, want errCPAAPICallResponseTooLarge", err)
	}
}

func TestDoCPAActionRejectsLargeBusinessFailureResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"padding":"`))
		_, _ = w.Write([]byte(strings.Repeat("x", 1024*1024)))
		_, _ = w.Write([]byte(`","failed":["denied"]}`))
	}))
	t.Cleanup(upstream.Close)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, upstream.URL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	svc := New(nil, nil, upstream.Client())
	actionErr, statusCode := svc.doCPAAction(req, "management-key")
	if actionErr == nil || !strings.Contains(actionErr.Error(), "denied") {
		t.Fatalf("action error = %v, want denied failure", actionErr)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("status code = %d, want %d", statusCode, http.StatusOK)
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
	if result.Run.EnableCount != 0 || result.Run.KeepCount != 1 {
		t.Fatalf("run counts enable=%d keep=%d, want 0/1", result.Run.EnableCount, result.Run.KeepCount)
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
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":5,"limit_window_seconds":2592000}}}}`))
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
	managerCfg.CodexInspection.AutoActionMode = model.CodexInspectionAutoActionNone
	managerCfg.CodexInspection.AutoRecoverEnabled = true
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:  "auth-a.json",
		AuthIndex: "auth-1",
	}); err != nil {
		t.Fatalf("save inspection disable ownership: %v", err)
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
		!result.Results[0].AutoRecoverEligible ||
		result.Results[0].ActionStatus != model.CodexInspectionActionStatusSuccess ||
		result.Results[0].ExecutedAction != "enable" ||
		result.Results[0].Disabled {
		t.Fatalf("result after enable = %#v", result.Results[0])
	}
	ownership, err := db.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list inspection disable ownership: %v", err)
	}
	if len(ownership) != 0 {
		t.Fatalf("ownership after enable = %#v, want empty", ownership)
	}
}

func TestRunAutoRecoverSkipsManuallyDisabledAccount(t *testing.T) {
	var patchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":5,"limit_window_seconds":2592000}}}}`))
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
	managerCfg.CodexInspection.AutoRecoverEnabled = true
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	svc := newCodexInspectionTestService(t, db)

	result, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"})
	if err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	if patchCalled {
		t.Fatal("auto recovery enabled a manually disabled account")
	}
	if len(result.Results) != 1 || result.Results[0].Action != "enable" || result.Results[0].AutoRecoverEligible {
		t.Fatalf("result = %#v, want manual-only enable suggestion", result.Results)
	}
	if !strings.Contains(result.Results[0].ActionReason, "仅允许手动启用") {
		t.Fatalf("action reason = %q, want manual-only explanation", result.Results[0].ActionReason)
	}
}

func TestRunWithDifferentTargetTypePreservesDisableOwnership(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)

	db := newCodexInspectionTestStore(t)
	managerCfg := newCodexInspectionManagerConfig(upstream.URL)
	managerCfg.CodexInspection.TargetType = "anthropic"
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:  "auth-a.json",
		AuthIndex: "auth-1",
	}); err != nil {
		t.Fatalf("save inspection disable ownership: %v", err)
	}

	svc := newCodexInspectionTestService(t, db)
	if _, err := svc.Run(context.Background(), RunRequest{TriggerType: "manual", TriggerKey: "manual"}); err != nil {
		t.Fatalf("run inspection: %v", err)
	}
	ownership, err := db.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list inspection disable ownership: %v", err)
	}
	if len(ownership) != 1 || ownership[0].FileName != "auth-a.json" {
		t.Fatalf("ownership = %#v, want preserved auth-a.json", ownership)
	}
}

func TestApplyDisableOwnershipIsolatedByProvider(t *testing.T) {
	db := newCodexInspectionTestStore(t)
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:  "shared-auth.json",
		Provider:  "codex",
		AuthIndex: "shared-auth",
	}); err != nil {
		t.Fatalf("save inspection disable ownership: %v", err)
	}

	accounts := []account{
		{FileName: "shared-auth.json", Provider: "xai", AuthIndex: "shared-auth", Disabled: true},
		{FileName: "shared-auth.json", Provider: "codex", AuthIndex: "shared-auth", Disabled: true},
	}
	svc := New(db, nil)
	svc.applyDisableOwnership(context.Background(), accounts, runLogger{})

	if accounts[0].AutoRecoverOwned {
		t.Fatal("xAI account inherited Codex disable ownership")
	}
	if !accounts[1].AutoRecoverOwned {
		t.Fatal("Codex account did not retain matching disable ownership")
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
	ownership, err := db.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list inspection disable ownership: %v", err)
	}
	if len(ownership) != 1 || ownership[0].FileName != "auth-a.json" || ownership[0].AuthIndex != "auth-1" {
		t.Fatalf("ownership after auto disable = %#v", ownership)
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

	t.Run("keeps disabled account while short window remains exhausted", func(t *testing.T) {
		disabledItem := item
		disabledItem.Disabled = true
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
		decision := resolveProbeAction(disabledItem, http.StatusOK, "", rateLimit, deriveRateLimitUsedPercent(rateLimit), true, threshold)

		if decision.Action != "keep" ||
			decision.ActionReason != "5 小时额度仍达到阈值，月额度可用但继续保持禁用" ||
			decision.UsedPercent == nil ||
			*decision.UsedPercent != 5 ||
			!decision.IsQuota {
			t.Fatalf("decision = %#v, want keep disabled account until short window recovers", decision)
		}
	})

	t.Run("keeps disabled account when quota is unknown", func(t *testing.T) {
		disabledItem := item
		disabledItem.Disabled = true
		decision := resolveProbeAction(disabledItem, http.StatusOK, `{"ok":true}`, nil, nil, false, threshold)

		if decision.Action != "keep" || decision.UsedPercent != nil || decision.IsQuota {
			t.Fatalf("decision = %#v, want keep unknown quota", decision)
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

func TestExecuteManualActionsProcessesCompletedRunResults(t *testing.T) {
	var patchCalled bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/auth-files" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"files":[{"name":"auth-a.json","auth_index":"auth-1","provider":"codex","account":"alice@example.com","disabled":true,"status":"ok","state":"ready"}]}`))
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":5,"limit_window_seconds":2592000}}}}`))
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

	db := newCodexInspectionTestStore(t)
	if err := db.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:  "auth-a.json",
		AuthIndex: "auth-1",
	}); err != nil {
		t.Fatalf("save inspection disable ownership: %v", err)
	}
	svc := New(db, nil, upstream.Client())
	err := svc.executeAction(context.Background(), store.Setup{
		CPAUpstreamURL: upstream.URL,
		ManagementKey:  "management-key",
	}, model.CodexInspectionResult{
		FileName: "auth-a.json",
		Action:   "disable",
	}, false)
	if err == nil {
		t.Fatal("execute action succeeded, want patch error")
	}
	message := err.Error()
	if !strings.Contains(message, "status patch failed") {
		t.Fatalf("patch error = %q", message)
	}
	ownership, listErr := db.ListCodexInspectionDisableOwnership(context.Background())
	if listErr != nil {
		t.Fatalf("list ownership: %v", listErr)
	}
	if len(ownership) != 1 || ownership[0].FileName != "auth-a.json" {
		t.Fatalf("ownership after failed patch = %#v, want preserved", ownership)
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
				_, _ = w.Write([]byte(`{"status_code":200,"body":{"rate_limit":{"primary_window":{"used_percent":10,"limit_window_seconds":18000},"secondary_window":{"used_percent":5,"limit_window_seconds":2592000}}}}`))
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
	cfg.CodexInspection.XAIInferenceEnabled = true
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
