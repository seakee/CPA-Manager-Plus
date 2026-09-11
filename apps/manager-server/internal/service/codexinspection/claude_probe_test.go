package codexinspection

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func TestClaudeProbeUsesFixedUsageRequestAndStaysReadOnly(t *testing.T) {
	var apiCallCount int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost:
			apiCallCount++
			var payload struct {
				AuthIndex string            `json:"authIndex"`
				Method    string            `json:"method"`
				URL       string            `json:"url"`
				Header    map[string]string `json:"header"`
				Data      string            `json:"data"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode Claude api-call payload: %v", err)
			}
			if payload.AuthIndex != "claude-1" || payload.Method != http.MethodGet || payload.URL != claudeUsageURL {
				t.Fatalf("Claude api-call payload = %#v", payload)
			}
			wantHeaders := map[string]string{
				"Authorization":  "Bearer $TOKEN$",
				"Content-Type":   "application/json",
				"anthropic-beta": "oauth-2025-04-20",
			}
			if len(payload.Header) != len(wantHeaders) {
				t.Fatalf("Claude headers = %#v, want exactly %#v", payload.Header, wantHeaders)
			}
			for key, want := range wantHeaders {
				if got := payload.Header[key]; got != want {
					t.Fatalf("Claude header %q = %q, want %q", key, got, want)
				}
			}
			encoded, _ := json.Marshal(payload)
			if strings.Contains(string(encoded), "attacker.invalid") || strings.Contains(string(encoded), "not-a-credential") || strings.Contains(payload.URL, "chatgpt.com") {
				t.Fatalf("Claude probe leaked metadata or used Codex endpoint: %s", encoded)
			}
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"five_hour":{"utilization":125,"resets_at":"2026-08-01T01:02:03Z"},"seven_day":{"utilization":30,"resets_at":1785542400}}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	svc := New(newCodexInspectionTestStore(t), nil, upstream.Client())
	result := svc.inspectSingleAccount(
		context.Background(),
		store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
		model.ManagerCodexInspectionConfig{Retries: 0},
		account{
			Provider:  model.CodexInspectionTargetClaude,
			AuthIndex: "claude-1",
			FileName:  "claude-auth.json",
			File: authFile{"metadata": map[string]any{
				"base_url":     "https://attacker.invalid/v1",
				"access_token": "not-a-credential",
			}},
		},
		runLogger{},
	)
	if apiCallCount != 1 {
		t.Fatalf("Claude provider call count = %d, want 1", apiCallCount)
	}
	if result.Action != "keep" || result.AutoRecoverEligible || result.IsQuota {
		t.Fatalf("Claude result must remain read-only: %#v", result)
	}
	if result.UsedPercent == nil || *result.UsedPercent != 125 || !result.QuotaInventoryObserved || len(result.QuotaWindows) != 2 {
		t.Fatalf("Claude quota observation = %#v", result)
	}
	windows := map[string]model.CodexInspectionQuotaWindow{}
	for _, window := range result.QuotaWindows {
		windows[window.ID] = window
	}
	if windows["five-hour"].ResetAtMS != time.Date(2026, 8, 1, 1, 2, 3, 0, time.UTC).UnixMilli() || windows["seven-day"].ResetAtMS != 1_785_542_400_000 {
		t.Fatalf("Claude reset timestamps = %#v", windows)
	}
}

func TestClaudeProbeFailureStaysKeep(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost {
			_, _ = w.Write([]byte(`{"status_code":401,"body":{"error":"authentication failed"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)

	result := New(newCodexInspectionTestStore(t), nil, upstream.Client()).inspectSingleAccount(
		context.Background(),
		store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
		model.ManagerCodexInspectionConfig{},
		account{Provider: model.CodexInspectionTargetClaude, AuthIndex: "claude-1", FileName: "claude-auth.json"},
		runLogger{},
	)
	if result.Action != "keep" || result.AutoRecoverEligible || result.ErrorKind != "http_status" || result.StatusCode == nil || *result.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Claude 401 result = %#v", result)
	}
}

func TestUnsupportedProvidersFailClosedWithoutProviderCall(t *testing.T) {
	for _, provider := range []string{"qwen", "qoder", "iflow", "arbitrary-provider"} {
		t.Run(provider, func(t *testing.T) {
			var apiCallCount int
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost {
					apiCallCount++
					t.Fatal("unsupported provider must not receive an upstream provider call")
				}
				http.NotFound(w, r)
			}))
			t.Cleanup(upstream.Close)

			result := New(newCodexInspectionTestStore(t), nil, upstream.Client()).inspectSingleAccount(
				context.Background(),
				store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
				model.DefaultCodexInspectionConfig(),
				account{Provider: provider, AuthIndex: "auth-1", FileName: "unsupported.json"},
				runLogger{},
			)
			if apiCallCount != 0 || result.Action != "keep" || result.AutoRecoverEligible || result.ErrorKind != "unsupported_provider" {
				t.Fatalf("unsupported provider result = %#v, provider calls = %d", result, apiCallCount)
			}
		})
	}
}

func TestParseClaudeUsagePayloadRecognizesKnownWindowsAndExplicitEmptyLimits(t *testing.T) {
	payload := map[string]any{
		"five_hour":        map[string]any{"utilization": 12.5, "resets_at": "2026-08-01T01:02:03Z"},
		"seven_day":        map[string]any{"utilization": 22.5, "resets_at": float64(1_785_542_400_000)},
		"seven_day_sonnet": map[string]any{"utilization": 32.5, "resets_at": nil},
	}
	windows, observed := parseClaudeUsagePayload(payload)
	if !observed || len(windows) != 3 {
		t.Fatalf("Claude recognized windows = %#v observed=%t", windows, observed)
	}
	byID := map[string]model.CodexInspectionQuotaWindow{}
	for _, window := range windows {
		byID[window.ID] = window
	}
	if byID["five-hour"].UsedPercent == nil || *byID["five-hour"].UsedPercent != 12.5 || byID["seven-day"].ResetAtMS != 1_785_542_400_000 {
		t.Fatalf("Claude parsed windows = %#v", byID)
	}
	if _, ok := byID["seven-day-sonnet"]; !ok {
		t.Fatalf("Claude special seven-day window missing: %#v", byID)
	}
	if byID["five-hour"].ModelScope == nil || byID["five-hour"].ModelScope.Kind != "all" ||
		byID["seven-day"].ModelScope == nil || byID["seven-day"].ModelScope.Kind != "all" ||
		byID["seven-day-sonnet"].ModelScope == nil || byID["seven-day-sonnet"].ModelScope.Kind == "all" {
		t.Fatalf("Claude scope assignment = %#v", byID)
	}

	empty, emptyObserved := parseClaudeUsagePayload(map[string]any{"limits": []any{}})
	if !emptyObserved || len(empty) != 0 {
		t.Fatalf("Claude explicit empty limits = %#v observed=%t", empty, emptyObserved)
	}
	limits, limitsObserved := parseClaudeUsagePayload(map[string]any{"limits": []any{
		map[string]any{"name": "five_hour", "utilization": 40.0, "resets_at": "2026-08-01T01:02:03Z"},
	}})
	if !limitsObserved || len(limits) != 1 || limits[0].ID != "five-hour" || limits[0].UsedPercent == nil || *limits[0].UsedPercent != 40 {
		t.Fatalf("Claude recognized limits fallback = %#v observed=%t", limits, limitsObserved)
	}
	invalidLimits, invalidLimitsObserved := parseClaudeUsagePayload(map[string]any{"limits": []any{
		map[string]any{"name": "unknown_limit", "utilization": 40.0},
	}})
	if invalidLimitsObserved || len(invalidLimits) != 0 {
		t.Fatalf("Claude invalid nonempty limits must remain unknown: %#v observed=%t", invalidLimits, invalidLimitsObserved)
	}
	unknown, unknownObserved := parseClaudeUsagePayload(map[string]any{})
	if unknownObserved || len(unknown) != 0 {
		t.Fatalf("Claude arbitrary success payload must remain unknown: %#v observed=%t", unknown, unknownObserved)
	}
}

func TestClaudeMutationCapabilityGuardRejectsCraftedActions(t *testing.T) {
	for _, action := range []string{"delete", "disable", "enable", "reauth"} {
		t.Run(action, func(t *testing.T) {
			if providerActionAllowed(model.CodexInspectionTargetClaude, action) {
				t.Fatalf("Claude action %q unexpectedly allowed", action)
			}
		})
	}
	if !providerActionAllowed(model.CodexInspectionTargetCodex, "delete") || !providerActionAllowed(model.CodexInspectionTargetXAI, "disable") {
		t.Fatal("existing supported provider actions must remain allowed")
	}
}

func TestExecuteActionRejectsCraftedClaudeMutationBeforeUpstreamCalls(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		t.Fatalf("crafted Claude mutation must not call CPA: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(upstream.Close)

	err := New(newCodexInspectionTestStore(t), nil, upstream.Client()).executeAction(
		context.Background(),
		store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
		model.CodexInspectionResult{Provider: model.CodexInspectionTargetClaude, Action: "delete", FileName: "claude-auth.json"},
		nil,
		false,
	)
	if err == nil || !strings.Contains(err.Error(), "does not permit account mutation") || calls != 0 {
		t.Fatalf("crafted Claude mutation result = err %v calls %d", err, calls)
	}
}

func TestClaudeProbeRequiresAuthIndexWithoutProviderCall(t *testing.T) {
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		t.Fatalf("Claude probe with missing auth index must not call CPA: %s %s", r.Method, r.URL.Path)
	}))
	t.Cleanup(upstream.Close)

	result := New(newCodexInspectionTestStore(t), nil, upstream.Client()).inspectSingleAccount(
		context.Background(),
		store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
		model.DefaultCodexInspectionConfig(),
		account{Provider: model.CodexInspectionTargetClaude, FileName: "claude-auth.json"},
		runLogger{},
	)
	if calls != 0 || result.Action != "keep" || result.AutoRecoverEligible || result.ErrorKind != "missing_auth_index" {
		t.Fatalf("missing Claude auth_index result = %#v calls=%d", result, calls)
	}
}

func TestClaudeAutoActionGuardRejectsCraftedResult(t *testing.T) {
	for _, action := range []string{"delete", "disable", "enable", "reauth"} {
		if allowAutoAction(model.CodexInspectionAutoActionDelete, true, model.CodexInspectionResult{
			Provider:            model.CodexInspectionTargetClaude,
			Action:              action,
			AutoRecoverEligible: true,
		}) {
			t.Fatalf("crafted Claude automatic %q action unexpectedly allowed", action)
		}
	}
}

func TestAnthropicProviderAliasRoutesToClaudeReadOnlyProbe(t *testing.T) {
	var usageCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v0/management/api-call" && r.Method == http.MethodPost {
			usageCalls++
			var payload struct {
				URL string `json:"url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode anthropic api-call payload: %v", err)
			}
			if payload.URL != claudeUsageURL {
				t.Fatalf("anthropic alias must use the Claude usage endpoint, got %q", payload.URL)
			}
			_, _ = w.Write([]byte(`{"status_code":200,"body":{"five_hour":{"utilization":10,"resets_at":"2026-08-01T01:02:03Z"}}}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(upstream.Close)

	result := New(newCodexInspectionTestStore(t), nil, upstream.Client()).inspectSingleAccount(
		context.Background(),
		store.Setup{CPAUpstreamURL: upstream.URL, ManagementKey: "management-key"},
		model.ManagerCodexInspectionConfig{Retries: 0},
		account{Provider: "anthropic", AuthIndex: "claude-1", FileName: "anthropic-auth.json"},
		runLogger{},
	)
	if usageCalls != 1 || result.Action != "keep" || result.AutoRecoverEligible || result.ErrorKind == "unsupported_provider" {
		t.Fatalf("anthropic alias result = %#v usageCalls=%d", result, usageCalls)
	}
}

func TestAnthropicProviderAliasMutationGuardRejects(t *testing.T) {
	for _, action := range []string{"delete", "disable", "enable", "reauth"} {
		if providerActionAllowed("anthropic", action) {
			t.Fatalf("anthropic alias action %q unexpectedly allowed", action)
		}
	}
}
