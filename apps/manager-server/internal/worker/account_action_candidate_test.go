package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	collectorpkg "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/credentialpolicy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

type workerRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn workerRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

type cancelAtEOFReadCloser struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (r *cancelAtEOFReadCloser) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if err == io.EOF && r.cancel != nil {
		r.cancel()
	}
	return n, err
}

func (r *cancelAtEOFReadCloser) Close() error { return nil }

func workerHTTPResponse(r *http.Request, body io.ReadCloser) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     make(http.Header),
		Body:       body,
		Request:    r,
	}
}

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
		AuthAccountIDSnapshot: "acct-123",
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
	if candidate.AccountSnapshot != "user@example.com" {
		t.Fatalf("account snapshot = %q", candidate.AccountSnapshot)
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

func TestAccountActionCandidateDoesNotPromoteDisplayFallbackToStableIdentity(t *testing.T) {
	event := usage.Event{
		Failed:            true,
		FailStatusCode:    http.StatusUnauthorized,
		EventHash:         "evt-label-only",
		Provider:          "codex",
		AuthFileSnapshot:  "shared.json",
		AuthLabelSnapshot: "Friendly account",
		Source:            "request-source",
		FailSummary:       "invalidated OAuth token",
	}
	candidate, ok := accountActionCandidateFromEvent(event, time.Now())
	if !ok {
		t.Fatal("candidate not detected")
	}
	if candidate.DisplayAccount != "Friendly account" || candidate.AccountSnapshot != "" {
		t.Fatalf("candidate identity = %#v", candidate)
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

func TestAccountActionCandidateUsesNormalizedHeaderMetadataWithoutFailBodyHeaders(t *testing.T) {
	payload := `{
		"timestamp": "2026-04-25T00:00:00Z",
		"failed": true,
		"fail": {"status_code": 401, "body": "upstream rejected request"},
		"provider": "codex",
		"model": "gpt-5.4",
		"endpoint": "POST /v1/chat/completions",
		"auth_file_snapshot": "codex-auth.json",
		"auth_index": "auth-1",
		"account_snapshot": "user@example.com",
		"response_headers": {
			"X-OpenAI-IDE-Error-Code": ["token_invalidated"]
		}
	}`
	event, err := usage.NormalizeRaw([]byte(payload))
	if err != nil {
		t.Fatalf("normalize header-only account action event: %v", err)
	}
	if event.FailBody != "upstream rejected request" || strings.Contains(event.FailBody, "token_invalidated") {
		t.Fatalf("fail body = %q", event.FailBody)
	}
	if event.HeaderErrorKind != "auth" || event.HeaderErrorCode != "token_invalidated" {
		t.Fatalf("header error = kind:%q code:%q metadata:%#v", event.HeaderErrorKind, event.HeaderErrorCode, event.ResponseMetadata)
	}

	// Ensure classification is supplied by structured metadata, not raw JSON.
	event.RawJSON = ""
	candidate, ok := accountActionCandidateFromEvent(event, time.Now())
	if !ok {
		t.Fatal("candidate not detected from structured response metadata")
	}
	if candidate.ActionType != model.AccountActionTypeReauth || candidate.ReasonCode != credentialpolicy.ReasonTokenRevoked {
		t.Fatalf("candidate = %#v", candidate)
	}
	var evidence map[string]any
	if err := json.Unmarshal([]byte(candidate.EvidenceJSON), &evidence); err != nil {
		t.Fatalf("decode evidence: %v", err)
	}
	if evidence["errorCode"] != "token_invalidated" {
		t.Fatalf("evidence = %#v", evidence)
	}
}

func TestAccountActionCandidateFromEventClassifiesXAIAuthenticationFailures(t *testing.T) {
	shouldNotRetry := false
	tests := []struct {
		name            string
		statusCode      int
		body            string
		metadata        *usage.ResponseHeaderMetadata
		wantAction      string
		wantReasonCode  string
		wantAutoDisable bool
	}{
		{
			name:            "expired credentials",
			statusCode:      http.StatusUnauthorized,
			body:            `{"error":"Invalid or expired credentials (auth_kind=bearer, x_xai_token_auth=xai-grok-cli, upstream=PermissionDenied, reason=no auth context)"}`,
			wantAction:      model.AccountActionTypeReauth,
			wantReasonCode:  credentialpolicy.ReasonInvalidCredentials,
			wantAutoDisable: true,
		},
		{
			name:       "chat endpoint permission denied",
			statusCode: http.StatusForbidden,
			body:       `{"code":"permission-denied","error":"Access to the chat endpoint is denied. Please ensure you’re using the correct credentials. If you believe this is a mistake, update the permissions or contact support."}`,
			metadata: &usage.ResponseHeaderMetadata{Errors: &usage.HeaderErrorMetadata{
				ShouldRetry: &shouldNotRetry,
			}},
			wantAction:      model.AccountActionTypeReview,
			wantReasonCode:  credentialpolicy.ReasonCredentialPermission,
			wantAutoDisable: true,
		},
		{
			name:            "regional permission denied",
			statusCode:      http.StatusForbidden,
			body:            `{"code":"permission-denied","error":"The model is not available in your region."}`,
			wantAction:      model.AccountActionTypeReview,
			wantReasonCode:  credentialpolicy.ReasonAuthenticationReview,
			wantAutoDisable: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := usage.Event{
				Failed:           true,
				FailStatusCode:   tt.statusCode,
				EventHash:        "evt-xai-auth",
				Provider:         "xai",
				AuthFileSnapshot: "xai-auth.json",
				AuthIndex:        "xai-1",
				FailBody:         tt.body,
				FailSummary:      tt.body,
				ResponseMetadata: tt.metadata,
			}
			candidate, ok := accountActionCandidateFromEvent(event, time.Now())
			if !ok {
				t.Fatal("candidate not detected")
			}
			if candidate.ActionType != tt.wantAction || candidate.ReasonCode != tt.wantReasonCode || candidate.AutoDisableEligible != tt.wantAutoDisable {
				t.Fatalf("candidate = %#v", candidate)
			}
		})
	}
}

func TestAccountActionCandidateFromEventNormalizesXAIProviderAlias(t *testing.T) {
	event := usage.Event{
		Failed:           true,
		FailStatusCode:   http.StatusUnauthorized,
		EventHash:        "evt-grok-auth",
		Provider:         "grok",
		AuthFileSnapshot: "xai-auth.json",
		AuthIndex:        "xai-1",
		FailSummary:      `{"error":"Invalid or expired credentials (reason=no auth context)"}`,
	}
	candidate, ok := accountActionCandidateFromEvent(event, time.Now())
	if !ok {
		t.Fatal("candidate not detected")
	}
	if candidate.Provider != "xai" {
		t.Fatalf("provider = %q, want xai", candidate.Provider)
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
	getCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mgmt" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			getCalls++
			runtimeID := "runtime-codex-7"
			accountID := "acct-123"
			if getCalls > 1 {
				runtimeID = "runtime-replacement"
				accountID = "acct-replacement"
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         runtimeID,
				"name":       "codex-auth.json",
				"auth_index": "7",
				"provider":   "codex",
				"account":    "user@example.com",
				"account_id": accountID,
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
			if payload.Name != "runtime-codex-7" || !payload.Disabled {
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
		BaseURL:             server.URL,
		ManagementKey:       "mgmt",
		FileName:            "codex-auth.json",
		AuthIndex:           "7",
		DisplayAccount:      "user@example.com",
		AccountID:           "acct-123",
		Provider:            "codex",
		ActionType:          model.AccountActionTypeDelete,
		AutoDisableEligible: true,
		Reason:              "token revoked",
	})

	if !patched {
		t.Fatal("expected auto-disable PATCH")
	}
	if getCalls != 1 {
		t.Fatalf("auth file reads = %d, want one verified target read", getCalls)
	}
	items, err := st.ListAccountActionCandidates(context.Background(), model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(items) != 1 || items[0].Status != model.AccountActionStatusPending || items[0].LastError != "" || items[0].AutoDisabledAtMS == 0 {
		t.Fatalf("items = %#v", items)
	}
}

func TestAccountActionCandidateWorkerRejectsAmbiguousStatusMutationScope(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"id":         "shared.json",
					"name":       "shared.json",
					"auth_index": "auth-1",
					"provider":   "codex",
					"account":    "user@example.com",
					"account_id": "acct-123",
					"disabled":   false,
				},
				{
					"id":         "runtime-auth-2",
					"name":       "shared.json",
					"auth_index": "auth-2",
					"provider":   "codex",
					"account":    "other@example.com",
					"account_id": "acct-456",
					"disabled":   false,
				},
			})
		case "PATCH /v0/management/auth-files/status":
			patchCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	NewAccountActionCandidateWorker(st, true).handleCandidate(context.Background(), accountActionCandidate{
		BaseURL:             server.URL,
		ManagementKey:       "mgmt",
		FileName:            "shared.json",
		AuthIndex:           "auth-1",
		DisplayAccount:      "user@example.com",
		AccountID:           "acct-123",
		Provider:            "codex",
		ActionType:          model.AccountActionTypeDelete,
		AutoDisableEligible: true,
		Reason:              "token revoked",
	})

	if patchCalls != 0 {
		t.Fatalf("patch calls = %d, want 0", patchCalls)
	}
	items, err := st.ListAccountActionCandidates(context.Background(), model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(items) != 1 || items[0].AutoDisabledAtMS != 0 || !strings.Contains(items[0].LastError, "scope is ambiguous") {
		t.Fatalf("items = %#v, want pending ambiguous failure", items)
	}
}

func TestAccountActionCandidateWorkerRejectsDuplicateCodexCredentialLocator(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	patchCalls := 0
	patchedName := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"id":         "runtime-alice",
					"name":       "shared.json",
					"auth_index": "shared-auth",
					"provider":   "codex",
					"account":    "alice@example.com",
					"account_id": "workspace-1",
					"disabled":   false,
				},
				{
					"id":         "runtime-bob",
					"name":       "shared.json",
					"auth_index": "shared-auth",
					"provider":   "codex",
					"account":    "bob@example.com",
					"account_id": "workspace-1",
					"disabled":   false,
				},
			})
		case "PATCH /v0/management/auth-files/status":
			var payload struct {
				Name     string `json:"name"`
				Disabled bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			patchCalls++
			patchedName = payload.Name
			if !payload.Disabled {
				http.Error(w, "expected disable", http.StatusBadRequest)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	NewAccountActionCandidateWorker(st, true).handleCandidate(context.Background(), accountActionCandidate{
		BaseURL:             server.URL,
		ManagementKey:       "mgmt",
		FileName:            "shared.json",
		AuthIndex:           "shared-auth",
		DisplayAccount:      "alice@example.com",
		AccountSnapshot:     "alice@example.com",
		AccountID:           "workspace-1",
		Provider:            "codex",
		ActionType:          model.AccountActionTypeDelete,
		AutoDisableEligible: true,
		Reason:              "token revoked",
	})

	if patchCalls != 0 || patchedName != "" {
		t.Fatalf("patch target = %q with %d calls, want no mutation for duplicate locator", patchedName, patchCalls)
	}
	items, err := st.ListAccountActionCandidates(context.Background(), model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(items) != 1 || items[0].AutoDisabledAtMS != 0 || !strings.Contains(items[0].LastError, "scope is ambiguous") {
		t.Fatalf("items = %#v, want pending ambiguous failure", items)
	}
}

func TestAccountActionCandidateWorkerRollsBackWhenAutoDisableMarkerFails(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	getCalls := 0
	patchStates := make([]bool, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			getCalls++
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-codex-7",
				"name":       "codex-auth.json",
				"auth_index": "7",
				"provider":   "codex",
				"account":    "user@example.com",
				"account_id": "acct-123",
				"disabled":   false,
			}})
		case "PATCH /v0/management/auth-files/status":
			var payload struct {
				Disabled bool `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			patchStates = append(patchStates, payload.Disabled)
			if payload.Disabled {
				_ = st.Close()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	NewAccountActionCandidateWorker(st, true).handleCandidate(context.Background(), accountActionCandidate{
		BaseURL:             server.URL,
		ManagementKey:       "mgmt",
		FileName:            "codex-auth.json",
		AuthIndex:           "7",
		DisplayAccount:      "user@example.com",
		AccountID:           "acct-123",
		Provider:            "codex",
		ActionType:          model.AccountActionTypeDelete,
		AutoDisableEligible: true,
		Reason:              "token revoked",
	})

	if len(patchStates) != 2 || !patchStates[0] || patchStates[1] {
		t.Fatalf("patch states = %#v, want [true false]", patchStates)
	}
	if getCalls != 2 {
		t.Fatalf("auth file reads = %d, want initial and rollback verification", getCalls)
	}
}

func TestAccountActionCandidateWorkerCompensatesAfterParentCancellation(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	getCalls := 0
	patchStates := make([]bool, 0, 2)
	client := &http.Client{Transport: workerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method + " " + r.URL.Path {
		case http.MethodGet + " /v0/management/auth-files":
			getCalls++
			disabled := len(patchStates) > 0 && patchStates[len(patchStates)-1]
			body := `[{"id":"runtime-codex-7","name":"codex-auth.json","auth_index":"7","provider":"codex","account":"user@example.com","account_id":"acct-123","disabled":` + fmt.Sprint(disabled) + `}]`
			return workerHTTPResponse(r, io.NopCloser(strings.NewReader(body))), nil
		case http.MethodPatch + " /v0/management/auth-files/status":
			var payload struct {
				Disabled bool `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				return nil, err
			}
			patchStates = append(patchStates, payload.Disabled)
			body := io.ReadCloser(io.NopCloser(strings.NewReader(`{"ok":true}`)))
			if payload.Disabled {
				body = &cancelAtEOFReadCloser{reader: strings.NewReader(`{"ok":true}`), cancel: cancel}
			}
			return workerHTTPResponse(r, body), nil
		default:
			return workerHTTPResponse(r, io.NopCloser(strings.NewReader(`{"error":"not found"}`))), nil
		}
	})}
	worker := NewAccountActionCandidateWorker(st, true)
	worker.client = client
	worker.handleCandidate(ctx, accountActionCandidate{
		BaseURL:             "http://cpa.test",
		ManagementKey:       "mgmt",
		FileName:            "codex-auth.json",
		AuthIndex:           "7",
		DisplayAccount:      "user@example.com",
		AccountID:           "acct-123",
		Provider:            "codex",
		ActionType:          model.AccountActionTypeDelete,
		AutoDisableEligible: true,
		Reason:              "token revoked",
	})

	if ctx.Err() != context.Canceled {
		t.Fatalf("parent context error = %v, want context.Canceled", ctx.Err())
	}
	if getCalls != 2 {
		t.Fatalf("auth file reads = %d, want initial and compensation verification", getCalls)
	}
	if len(patchStates) != 2 || !patchStates[0] || patchStates[1] {
		t.Fatalf("patch states = %#v, want [true false]", patchStates)
	}
}

func TestAccountActionCandidateWorkerCompensationHasTotalTimeout(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	getCalls := 0
	rollbackCanceled := make(chan error, 1)
	client := &http.Client{Transport: workerRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.Method + " " + r.URL.Path {
		case http.MethodGet + " /v0/management/auth-files":
			getCalls++
			if getCalls > 1 {
				<-r.Context().Done()
				rollbackCanceled <- r.Context().Err()
				return nil, r.Context().Err()
			}
			return workerHTTPResponse(r, io.NopCloser(strings.NewReader(
				`[{"id":"runtime-codex-7","name":"codex-auth.json","auth_index":"7","provider":"codex","account":"user@example.com","account_id":"acct-123","disabled":false}]`,
			))), nil
		case http.MethodPatch + " /v0/management/auth-files/status":
			_ = st.Close()
			return workerHTTPResponse(r, io.NopCloser(strings.NewReader(`{"ok":true}`))), nil
		default:
			return workerHTTPResponse(r, io.NopCloser(strings.NewReader(`{"error":"not found"}`))), nil
		}
	})}
	worker := NewAccountActionCandidateWorker(st, true)
	worker.client = client
	worker.compensationTimeout = 25 * time.Millisecond
	started := time.Now()
	worker.handleCandidate(context.Background(), accountActionCandidate{
		BaseURL:             "http://cpa.test",
		ManagementKey:       "mgmt",
		FileName:            "codex-auth.json",
		AuthIndex:           "7",
		DisplayAccount:      "user@example.com",
		AccountID:           "acct-123",
		Provider:            "codex",
		ActionType:          model.AccountActionTypeDelete,
		AutoDisableEligible: true,
		Reason:              "token revoked",
	})
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("bounded compensation elapsed = %s, want <= 500ms", elapsed)
	}
	select {
	case err := <-rollbackCanceled:
		if err != context.DeadlineExceeded {
			t.Fatalf("rollback context error = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("rollback request did not observe the compensation deadline")
	}
}

func TestAccountActionCandidateWorkerRollsBackWhenCandidateLeavesPendingAfterPatch(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	getCalls := 0
	patchStates := make([]bool, 0, 2)
	var candidateID int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			getCalls++
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-codex-7",
				"name":       "codex-auth.json",
				"auth_index": "7",
				"provider":   "codex",
				"account":    "user@example.com",
				"account_id": "acct-123",
				"disabled":   len(patchStates) > 0 && patchStates[len(patchStates)-1],
			}})
		case "PATCH /v0/management/auth-files/status":
			var payload struct {
				Disabled bool `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			patchStates = append(patchStates, payload.Disabled)
			if payload.Disabled {
				items, err := st.ListAccountActionCandidates(context.Background(), model.AccountActionStatusPending, 10)
				if err != nil || len(items) != 1 {
					t.Fatalf("list pending during patch: items=%#v err=%v", items, err)
				}
				candidateID = items[0].ID
				if _, err := st.UpdateAccountActionCandidateStatus(context.Background(), candidateID, model.AccountActionStatusIgnored); err != nil {
					t.Fatalf("ignore candidate during patch: %v", err)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	NewAccountActionCandidateWorker(st, true).handleCandidate(context.Background(), accountActionCandidate{
		BaseURL:             server.URL,
		ManagementKey:       "mgmt",
		FileName:            "codex-auth.json",
		AuthIndex:           "7",
		DisplayAccount:      "user@example.com",
		AccountID:           "acct-123",
		Provider:            "codex",
		ActionType:          model.AccountActionTypeDelete,
		AutoDisableEligible: true,
		Reason:              "token revoked",
	})

	if len(patchStates) != 2 || !patchStates[0] || patchStates[1] {
		t.Fatalf("patch states = %#v, want [true false]", patchStates)
	}
	if getCalls != 2 {
		t.Fatalf("auth file reads = %d, want initial and rollback verification", getCalls)
	}
	item, ok, err := st.GetAccountActionCandidate(context.Background(), candidateID)
	if err != nil || !ok || item.Status != model.AccountActionStatusIgnored || item.AutoDisabledAtMS != 0 {
		t.Fatalf("candidate after concurrent ignore = %#v ok=%v err=%v", item, ok, err)
	}
}

func TestAccountActionCandidateWorkerDoesNotRollbackSamePathReplacement(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	getCalls := 0
	patchStates := make([]bool, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			getCalls++
			accountID := "acct-123"
			account := "user@example.com"
			runtimeID := "runtime-codex-7"
			if getCalls > 1 {
				accountID = "acct-replacement"
				account = "replacement@example.com"
				runtimeID = "runtime-replacement"
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         runtimeID,
				"name":       "codex-auth.json",
				"auth_index": "7",
				"provider":   "codex",
				"account":    account,
				"account_id": accountID,
				"disabled":   getCalls > 1,
			}})
		case "PATCH /v0/management/auth-files/status":
			var payload struct {
				Disabled bool `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			patchStates = append(patchStates, payload.Disabled)
			if payload.Disabled {
				_ = st.Close()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	NewAccountActionCandidateWorker(st, true).handleCandidate(context.Background(), accountActionCandidate{
		BaseURL:             server.URL,
		ManagementKey:       "mgmt",
		FileName:            "codex-auth.json",
		AuthIndex:           "7",
		DisplayAccount:      "user@example.com",
		AccountID:           "acct-123",
		Provider:            "codex",
		ActionType:          model.AccountActionTypeDelete,
		AutoDisableEligible: true,
		Reason:              "token revoked",
	})

	if getCalls != 2 {
		t.Fatalf("auth file reads = %d, want initial and rollback verification", getCalls)
	}
	if len(patchStates) != 1 || !patchStates[0] {
		t.Fatalf("patch states = %#v, replacement must not be enabled", patchStates)
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
				"id":         "runtime-codex-7",
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
		BaseURL:             server.URL,
		ManagementKey:       "mgmt",
		FileName:            "codex-auth.json",
		AuthIndex:           "7",
		DisplayAccount:      "user@example.com",
		AccountID:           "acct-123",
		Provider:            "codex",
		ActionType:          model.AccountActionTypeDelete,
		AutoDisableEligible: true,
		Reason:              "token revoked",
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

func TestAccountActionCandidateWorkerAutoDisableRejectsWeakIdentity(t *testing.T) {
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	requestCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCalls++
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()

	NewAccountActionCandidateWorker(st, true).handleCandidate(context.Background(), accountActionCandidate{
		BaseURL:             server.URL,
		ManagementKey:       "mgmt",
		FileName:            "codex-auth.json",
		DisplayAccount:      "codex-auth.json",
		Provider:            "codex",
		ActionType:          model.AccountActionTypeDelete,
		AutoDisableEligible: true,
		Reason:              "legacy candidate",
	})

	if requestCalls != 0 {
		t.Fatalf("CPA request calls = %d, want 0", requestCalls)
	}
	items, err := st.ListAccountActionCandidates(context.Background(), model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(items) != 1 || items[0].AutoDisabledAtMS != 0 || !strings.Contains(items[0].LastError, "auth_index") {
		t.Fatalf("items = %#v, want weak-identity failure", items)
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
		BaseURL:             server.URL,
		ManagementKey:       "mgmt",
		FileName:            "codex-auth.json",
		AuthIndex:           "7",
		DisplayAccount:      "user@example.com",
		AccountID:           "acct-123",
		Provider:            "codex",
		ActionType:          model.AccountActionTypeDelete,
		AutoDisableEligible: true,
		Reason:              "token revoked",
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
				"id":         "runtime-codex-7",
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
		BaseURL:             server.URL,
		ManagementKey:       "mgmt",
		FileName:            "codex-auth.json",
		AuthIndex:           "7",
		DisplayAccount:      "user@example.com",
		AccountID:           "acct-123",
		Provider:            "codex",
		ActionType:          model.AccountActionTypeReauth,
		AutoDisableEligible: true,
		Reason:              "reauth required",
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

func TestAccountActionCandidateWorkerAutoDisablesEligibleXAIReviewWithProviderAlias(t *testing.T) {
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
				"id":         "runtime-xai-1",
				"name":       "xai-auth.json",
				"auth_index": "xai-1",
				"provider":   "xai",
				"account":    "xai-user",
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

	shouldNotRetry := false
	event := usage.Event{
		Failed:           true,
		FailStatusCode:   http.StatusForbidden,
		EventHash:        "evt-grok-permission",
		Provider:         "grok",
		AuthFileSnapshot: "xai-auth.json",
		AuthIndex:        "xai-1",
		AccountSnapshot:  "xai-user",
		FailBody:         `{"code":"permission-denied","error":"Access to the chat endpoint is denied. Please ensure you're using the correct credentials and update the permissions."}`,
		FailSummary:      `{"code":"permission-denied","error":"Access to the chat endpoint is denied. Please ensure you're using the correct credentials and update the permissions."}`,
		ResponseMetadata: &usage.ResponseHeaderMetadata{Errors: &usage.HeaderErrorMetadata{
			ShouldRetry: &shouldNotRetry,
		}},
	}
	candidate, ok := accountActionCandidateFromEvent(event, time.Now())
	if !ok {
		t.Fatal("candidate not detected")
	}
	candidate.BaseURL = server.URL
	candidate.ManagementKey = "mgmt"

	NewAccountActionCandidateWorker(st, true).handleCandidate(context.Background(), candidate)

	if !patched {
		t.Fatal("expected eligible xAI review to auto-disable")
	}
	items, err := st.ListAccountActionCandidates(context.Background(), model.AccountActionStatusPending, 10)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	if len(items) != 1 || items[0].Provider != "xai" || items[0].ActionType != model.AccountActionTypeReview || items[0].AutoDisabledAtMS == 0 {
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
				"id":         "runtime-codex-auth",
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
		BaseURL:             server.URL,
		ManagementKey:       "mgmt",
		FileName:            "codex-auth.json",
		AuthIndex:           "7",
		DisplayAccount:      "user@example.com",
		AccountID:           "acct-123",
		Provider:            "codex",
		ActionType:          model.AccountActionTypeDelete,
		AutoDisableEligible: true,
		Reason:              "token revoked",
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
		FileName:            "codex-auth.json",
		AuthIndex:           "7",
		DisplayAccount:      "user@example.com",
		AccountID:           "acct-123",
		Provider:            "codex",
		ActionType:          model.AccountActionTypeDelete,
		AutoDisableEligible: true,
		Reason:              "token revoked",
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
