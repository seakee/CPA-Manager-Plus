package worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestAccountActionCandidateFromEventUsesSafeEvidence(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	event := usage.Event{
		Failed:                true,
		FailStatusCode:        401,
		EventHash:             "evt-auth",
		RequestID:             "req-1",
		Provider:              "codex",
		AuthFileSnapshot:      "codex-auth.json",
		AuthIndex:             "7",
		AccountSnapshot:       "user@example.com",
		AuthProjectIDSnapshot: "acct-123",
		FailSummary:           "authentication_error: invalidated OAuth token",
		FailBody:              `{"error":{"type":"authentication_error","code":"token_revoked","message":"secret token sk-sensitive"}}`,
		RawJSON:               `{"authorization":"Bearer secret","raw":"payload"}`,
	}
	candidate, ok := accountActionCandidateFromEvent(event, now)
	if !ok {
		t.Fatal("candidate not detected")
	}
	if candidate.ActionType != model.AccountActionTypeReauth {
		t.Fatalf("action type = %q", candidate.ActionType)
	}
	if candidate.AccountID != "acct-123" {
		t.Fatalf("account id = %q", candidate.AccountID)
	}
	if strings.Contains(candidate.EvidenceJSON, "FailBody") || strings.Contains(candidate.EvidenceJSON, "RawJSON") || strings.Contains(candidate.EvidenceJSON, "sk-sensitive") || strings.Contains(candidate.EvidenceJSON, "Bearer secret") {
		t.Fatalf("evidence leaked sensitive raw payload: %s", candidate.EvidenceJSON)
	}
	var evidence map[string]any
	if err := json.Unmarshal([]byte(candidate.EvidenceJSON), &evidence); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if evidence["errorCode"] != "token_revoked" || evidence["errorType"] != "authentication_error" {
		t.Fatalf("evidence = %#v", evidence)
	}
}

func TestAccountActionCandidateFromEventUsesHeaderErrorCode(t *testing.T) {
	event := usage.Event{
		Failed:           true,
		FailStatusCode:   http.StatusUnauthorized,
		EventHash:        "evt-header-auth",
		Provider:         "codex",
		AuthFileSnapshot: "codex-auth.json",
		AuthIndex:        "auth-1",
		AccountSnapshot:  "user@example.com",
		HeaderErrorKind:  "auth",
		HeaderErrorCode:  "token_invalidated",
		HeaderTraceID:    "req-header-auth",
	}
	candidate, ok := accountActionCandidateFromEvent(event, time.Now())
	if !ok {
		t.Fatal("candidate not detected")
	}
	if candidate.ActionType != model.AccountActionTypeReauth {
		t.Fatalf("action type = %q", candidate.ActionType)
	}
	var evidence map[string]any
	if err := json.Unmarshal([]byte(candidate.EvidenceJSON), &evidence); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if evidence["headerErrorCode"] != "token_invalidated" || evidence["headerTraceId"] != "req-header-auth" {
		t.Fatalf("evidence = %#v", evidence)
	}
}

func TestAccountActionCandidateFromEventDeletesAccountDeactivatedHeader(t *testing.T) {
	event := usage.Event{
		Failed:           true,
		FailStatusCode:   http.StatusUnauthorized,
		EventHash:        "evt-header-deactivated",
		Provider:         "codex",
		AuthFileSnapshot: "codex-auth.json",
		HeaderErrorKind:  "auth",
		HeaderErrorCode:  "account_deactivated",
	}
	candidate, ok := accountActionCandidateFromEvent(event, time.Now())
	if !ok {
		t.Fatal("candidate not detected")
	}
	if candidate.ActionType != model.AccountActionTypeDelete {
		t.Fatalf("action type = %q", candidate.ActionType)
	}
}

func TestAccountActionCandidateWorkerSavesQueueOnly(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	worker := NewAccountActionCandidateWorker(st)
	event := usage.Event{
		Failed:           true,
		FailStatusCode:   401,
		EventHash:        "evt-auth",
		Provider:         "codex",
		AuthFileSnapshot: "codex-auth.json",
		AuthIndex:        "7",
		FailSummary:      "invalidated OAuth token",
	}
	candidate, ok := accountActionCandidateFromEvent(event, time.Now())
	if !ok {
		t.Fatal("candidate not detected")
	}
	worker.handleCandidate(context.Background(), candidate)

	items, err := st.ListAccountActionCandidates(context.Background(), model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(items) != 1 || items[0].AuthFileName != "codex-auth.json" || items[0].ActionType != model.AccountActionTypeReauth {
		t.Fatalf("items = %#v", items)
	}
	if items[0].LastError != "" {
		t.Fatalf("last error = %q", items[0].LastError)
	}
}

func TestAccountActionCandidateWorkerAutoDisablesMatchingIdentity(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	var patched bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mgmt" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"name":       "codex-auth.json",
				"auth_index": "7",
				"provider":   "codex",
				"account":    "user@example.com",
				"account_id": "acct-123",
				"disabled":   false,
			}})
		case "PATCH /v0/management/auth-files/status":
			var payload struct {
				Name     string `json:"name"`
				Disabled bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if payload.Name != "codex-auth.json" || !payload.Disabled {
				t.Fatalf("patch payload = %#v", payload)
			}
			patched = true
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	worker := NewAccountActionCandidateWorker(st, true)
	worker.handleCandidate(context.Background(), accountActionCandidate{
		BaseURL:        server.URL,
		ManagementKey:  "mgmt",
		FileName:       "codex-auth.json",
		AuthIndex:      "7",
		DisplayAccount: "user@example.com",
		AccountID:      "acct-123",
		Provider:       "codex",
		ActionType:     model.AccountActionTypeDelete,
		Reason:         "token revoked",
	})

	if !patched {
		t.Fatal("expected auto-disable PATCH")
	}
	items, err := st.ListAccountActionCandidates(context.Background(), model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(items) != 1 || items[0].Status != model.AccountActionStatusPending || items[0].LastError != "" {
		t.Fatalf("items = %#v", items)
	}
}

func TestAccountActionCandidateWorkerAutoDisableRejectsIdentityMismatch(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	var patched bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"name":       "codex-auth.json",
				"auth_index": "7",
				"provider":   "codex",
				"account":    "different@example.com",
				"account_id": "acct-456",
			}})
		case "PATCH /v0/management/auth-files/status":
			patched = true
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	worker := NewAccountActionCandidateWorker(st, true)
	worker.handleCandidate(context.Background(), accountActionCandidate{
		BaseURL:        server.URL,
		ManagementKey:  "mgmt",
		FileName:       "codex-auth.json",
		AuthIndex:      "7",
		DisplayAccount: "user@example.com",
		AccountID:      "acct-123",
		Provider:       "codex",
		ActionType:     model.AccountActionTypeDelete,
		Reason:         "token revoked",
	})

	if patched {
		t.Fatal("PATCH should not be called on identity mismatch")
	}
	items, err := st.ListAccountActionCandidates(context.Background(), model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(items) != 1 || !strings.Contains(items[0].LastError, "identity mismatch") {
		t.Fatalf("items = %#v", items)
	}
}

func TestAccountActionCandidateWorkerAutoDisableRecordsVerificationTransportError(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	var patched bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			http.Error(w, "temporary CPA failure", http.StatusInternalServerError)
		case "PATCH /v0/management/auth-files/status":
			patched = true
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	worker := NewAccountActionCandidateWorker(st, true)
	worker.handleCandidate(context.Background(), accountActionCandidate{
		BaseURL:        server.URL,
		ManagementKey:  "mgmt",
		FileName:       "codex-auth.json",
		AuthIndex:      "7",
		DisplayAccount: "user@example.com",
		AccountID:      "acct-123",
		Provider:       "codex",
		ActionType:     model.AccountActionTypeDelete,
		Reason:         "token revoked",
	})

	if patched {
		t.Fatal("PATCH should not be called when verification request fails")
	}
	items, err := st.ListAccountActionCandidates(context.Background(), model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(items) != 1 || !strings.Contains(items[0].LastError, "HTTP 500") || strings.Contains(items[0].LastError, "identity verification failed") {
		t.Fatalf("items = %#v", items)
	}
}

func TestAccountActionCandidateWorkerAutoDisablesReauth(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	var patched bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"name":       "codex-auth.json",
				"auth_index": "7",
				"provider":   "codex",
				"account":    "user@example.com",
				"account_id": "acct-123",
				"disabled":   false,
			}})
		case "PATCH /v0/management/auth-files/status":
			patched = true
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	worker := NewAccountActionCandidateWorker(st, true)
	worker.handleCandidate(context.Background(), accountActionCandidate{
		BaseURL:        server.URL,
		ManagementKey:  "mgmt",
		FileName:       "codex-auth.json",
		AuthIndex:      "7",
		DisplayAccount: "user@example.com",
		AccountID:      "acct-123",
		Provider:       "codex",
		ActionType:     model.AccountActionTypeReauth,
		Reason:         "reauth required",
	})

	if !patched {
		t.Fatal("expected reauth candidate to auto-disable")
	}
	items, err := st.ListAccountActionCandidates(context.Background(), model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(items) != 1 || items[0].ActionType != model.AccountActionTypeReauth || items[0].LastError != "" {
		t.Fatalf("items = %#v", items)
	}
}

func TestAccountActionCandidateWorkerAutoDisableSkipsAlreadyDisabled(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	var patched bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"name":       "codex-auth.json",
				"auth_index": "7",
				"provider":   "codex",
				"account":    "user@example.com",
				"account_id": "acct-123",
				"disabled":   true,
			}})
		case "PATCH /v0/management/auth-files/status":
			patched = true
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	worker := NewAccountActionCandidateWorker(st, true)
	worker.handleCandidate(context.Background(), accountActionCandidate{
		BaseURL:        server.URL,
		ManagementKey:  "mgmt",
		FileName:       "codex-auth.json",
		AuthIndex:      "7",
		DisplayAccount: "user@example.com",
		AccountID:      "acct-123",
		Provider:       "codex",
		ActionType:     model.AccountActionTypeDelete,
		Reason:         "token revoked",
	})

	if patched {
		t.Fatal("PATCH should not be called when auth file is already disabled")
	}
	items, err := st.ListAccountActionCandidates(context.Background(), model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(items) != 1 || items[0].LastError != "" {
		t.Fatalf("items = %#v", items)
	}
}

func TestAccountActionCandidateWorkerAutoDisableRecordsMissingRuntimeConfig(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	worker := NewAccountActionCandidateWorker(st, true)
	worker.handleCandidate(context.Background(), accountActionCandidate{
		FileName:       "codex-auth.json",
		AuthIndex:      "7",
		DisplayAccount: "user@example.com",
		AccountID:      "acct-123",
		Provider:       "codex",
		ActionType:     model.AccountActionTypeDelete,
		Reason:         "token revoked",
	})

	items, err := st.ListAccountActionCandidates(context.Background(), model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(items) != 1 || !strings.Contains(items[0].LastError, "runtime config") {
		t.Fatalf("items = %#v", items)
	}
}

func TestAccountActionCandidateWorkerAutoDisableSkipsReview(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	worker := NewAccountActionCandidateWorker(st, true)
	worker.handleCandidate(context.Background(), accountActionCandidate{
		FileName:       "codex-auth.json",
		DisplayAccount: "user@example.com",
		Provider:       "codex",
		ActionType:     model.AccountActionTypeReview,
		Reason:         "manual review",
	})

	items, err := st.ListAccountActionCandidates(context.Background(), model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(items) != 1 || items[0].LastError != "" {
		t.Fatalf("items = %#v", items)
	}
}

func TestAccountActionCandidateFromEventHandlesDeactivatedWorkspace402(t *testing.T) {
	event := usage.Event{
		Failed:           true,
		FailStatusCode:   402,
		EventHash:        "evt-402-deactivated",
		Provider:         "codex",
		AuthFileSnapshot: "codex-auth.json",
		FailSummary:      "payment required",
		FailBody:         `{"error":{"type":"deactivated_workspace","code":"deactivated_workspace","message":"workspace inactive sk-sensitive"}}`,
		RawJSON:          `{"authorization":"Bearer secret"}`,
	}
	candidate, ok := accountActionCandidateFromEvent(event, time.Now())
	if !ok {
		t.Fatal("candidate not detected")
	}
	if candidate.ActionType != model.AccountActionTypeDelete {
		t.Fatalf("action type = %q", candidate.ActionType)
	}
	if strings.Contains(candidate.EvidenceJSON, "FailBody") || strings.Contains(candidate.EvidenceJSON, "RawJSON") || strings.Contains(candidate.EvidenceJSON, "sk-sensitive") || strings.Contains(candidate.EvidenceJSON, "Bearer secret") {
		t.Fatalf("evidence leaked sensitive raw payload: %s", candidate.EvidenceJSON)
	}
}

func TestAccountActionCandidateFromEventSkipsOrdinary402(t *testing.T) {
	cases := []usage.Event{
		{
			Failed:           true,
			FailStatusCode:   402,
			EventHash:        "evt-payment-required",
			Provider:         "codex",
			AuthFileSnapshot: "codex-auth.json",
			FailBody:         `{"error":{"type":"payment_required","code":"payment_required"}}`,
		},
		{
			Failed:           true,
			FailStatusCode:   402,
			EventHash:        "evt-quota",
			Provider:         "codex",
			AuthFileSnapshot: "codex-auth.json",
			FailSummary:      "quota exceeded",
			FailBody:         `{"error":{"type":"quota_exceeded","code":"quota_exceeded"}}`,
		},
	}
	for _, event := range cases {
		if candidate, ok := accountActionCandidateFromEvent(event, time.Now()); ok {
			t.Fatalf("unexpected candidate for %s: %#v", event.EventHash, candidate)
		}
	}
}

func TestUsageEventFanoutCallsHandlers(t *testing.T) {
	first := &recordingUsageHandler{}
	second := &recordingUsageHandler{}
	fanout := NewUsageEventFanout(first, nil, second)
	fanout.HandleUsageEvents(context.Background(), collectorpkg.RuntimeConfig{CPAUpstreamURL: "http://cpa"}, []usage.Event{{EventHash: "evt"}})
	if first.count != 1 || second.count != 1 {
		t.Fatalf("counts = %d/%d", first.count, second.count)
	}
}

func TestUsageEventFanoutForwardsRuntimeConfig(t *testing.T) {
	first := &recordingUsageHandler{}
	second := &recordingUsageHandler{}
	fanout := NewUsageEventFanout(first, nil, second)
	fanout.UpdateRuntimeConfig(context.Background(), collectorpkg.RuntimeConfig{CPAUpstreamURL: "http://cpa", ManagementKey: "mgmt"})
	if first.runtimeCount != 1 || second.runtimeCount != 1 {
		t.Fatalf("runtime counts = %d/%d", first.runtimeCount, second.runtimeCount)
	}
	if first.lastRuntime.CPAUpstreamURL != "http://cpa" || second.lastRuntime.ManagementKey != "mgmt" {
		t.Fatalf("runtime configs = %#v / %#v", first.lastRuntime, second.lastRuntime)
	}
}

type recordingUsageHandler struct {
	count        int
	runtimeCount int
	lastRuntime  collectorpkg.RuntimeConfig
}

func (h *recordingUsageHandler) HandleUsageEvents(context.Context, collectorpkg.RuntimeConfig, []usage.Event) {
	h.count++
}

func (h *recordingUsageHandler) UpdateRuntimeConfig(_ context.Context, cfg collectorpkg.RuntimeConfig) {
	h.runtimeCount++
	h.lastRuntime = cfg
}
