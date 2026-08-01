package accountaction

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	collectorsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	managerconfigsvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

type accountActionRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn accountActionRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

type cancelAfterReadCloser struct {
	reader io.Reader
	cancel context.CancelFunc
	once   sync.Once
}

func (r *cancelAfterReadCloser) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n > 0 {
		r.once.Do(r.cancel)
	}
	return n, err
}

func (r *cancelAfterReadCloser) Close() error { return nil }

func accountActionResponse(
	r *http.Request,
	body io.ReadCloser,
) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       body,
		Request:    r,
	}
}

func TestEnableValidatesCurrentAuthFileBeforePatch(t *testing.T) {
	ctx := context.Background()
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
				"disabled":   true,
			}})
		case "PATCH /v0/management/auth-files/status":
			var payload struct {
				Name      string `json:"name"`
				AuthIndex string `json:"auth_index"`
				Disabled  bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			if payload.Name != "runtime-codex-7" || payload.AuthIndex != "7" || payload.Disabled {
				t.Fatalf("patch payload = %#v, want precise enable", payload)
			}
			patched = true
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := st.SaveSetup(ctx, store.Setup{CPAUpstreamURL: server.URL, ManagementKey: "mgmt"}); err != nil {
		t.Fatalf("save setup: %v", err)
	}
	item, err := st.UpsertAccountActionCandidate(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeDelete,
		Provider:          "codex",
		AuthFileName:      "codex-auth.json",
		AuthIndex:         "7",
		AccountSnapshot:   "user@example.com",
		AccountIDSnapshot: "acct-123",
		Reason:            "token revoked",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	svc := New(st, managerconfigsvc.New(config.Config{}, st, collectorsvc.New(nil)), server.Client())
	updated, err := svc.Enable(ctx, item.ID)
	if err != nil {
		t.Fatalf("enable: %v", err)
	}
	if !patched {
		t.Fatal("expected PATCH /v0/management/auth-files/status")
	}
	if updated.Status != model.AccountActionStatusResolved {
		t.Fatalf("status = %q", updated.Status)
	}
}

func TestExternalActionPersistsStatusAfterRequestCancellation(t *testing.T) {
	for _, tt := range []struct {
		name       string
		actionPath string
		wantStatus string
		run        func(context.Context, *Service, int64) (model.AccountActionCandidate, error)
	}{
		{
			name:       "enable",
			actionPath: http.MethodPatch + " /v0/management/auth-files/status",
			wantStatus: model.AccountActionStatusResolved,
			run: func(ctx context.Context, svc *Service, id int64) (model.AccountActionCandidate, error) {
				return svc.Enable(ctx, id)
			},
		},
		{
			name:       "delete",
			actionPath: http.MethodDelete + " /v0/management/auth-files",
			wantStatus: model.AccountActionStatusDeleted,
			run: func(ctx context.Context, svc *Service, id int64) (model.AccountActionCandidate, error) {
				return svc.DeleteAuthFile(ctx, id)
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st, err := store.Open(t.TempDir() + "/usage.sqlite")
			if err != nil {
				t.Fatalf("open store: %v", err)
			}
			defer st.Close()
			background := context.Background()
			if err := st.SaveSetup(background, store.Setup{
				CPAUpstreamURL: "http://cpa.test",
				ManagementKey:  "mgmt",
			}); err != nil {
				t.Fatalf("save setup: %v", err)
			}
			item, err := st.UpsertAccountActionCandidate(background, model.AccountActionCandidateUpsert{
				ActionType:        model.AccountActionTypeDelete,
				Provider:          "codex",
				AuthFileName:      "codex-auth.json",
				AuthIndex:         "7",
				AccountSnapshot:   "user@example.com",
				AccountIDSnapshot: "acct-123",
				Reason:            "token revoked",
			})
			if err != nil {
				t.Fatalf("upsert candidate: %v", err)
			}

			actionCtx, cancel := context.WithCancel(background)
			client := &http.Client{Transport: accountActionRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch r.Method + " " + r.URL.Path {
				case http.MethodGet + " /v0/management/auth-files":
					return accountActionResponse(r, io.NopCloser(strings.NewReader(
						`[{"id":"runtime-codex-7","name":"codex-auth.json","auth_index":"7","provider":"codex","account":"user@example.com","account_id":"acct-123","disabled":true}]`,
					))), nil
				case tt.actionPath:
					return accountActionResponse(r, &cancelAfterReadCloser{
						reader: strings.NewReader(`{"status":"ok"}`),
						cancel: cancel,
					}), nil
				default:
					return accountActionResponse(r, io.NopCloser(strings.NewReader(`{"error":"not found"}`))), nil
				}
			})}
			svc := New(
				st,
				managerconfigsvc.New(config.Config{}, st, collectorsvc.New(nil)),
				client,
			)

			updated, err := tt.run(actionCtx, svc, item.ID)
			if err != nil {
				t.Fatalf("%s after cancellation: %v", tt.name, err)
			}
			if !errors.Is(actionCtx.Err(), context.Canceled) {
				t.Fatalf("action context error = %v, want canceled", actionCtx.Err())
			}
			if updated.Status != tt.wantStatus {
				t.Fatalf("returned status = %q, want %q", updated.Status, tt.wantStatus)
			}
			current, ok, err := st.GetAccountActionCandidate(background, item.ID)
			if err != nil || !ok || current.Status != tt.wantStatus {
				t.Fatalf("persisted candidate = %#v ok=%t err=%v", current, ok, err)
			}
		})
	}
}

func TestEnableRestoresDisabledStateWhenResultPersistenceFails(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	disabled := true
	patchedStates := make([]bool, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case http.MethodGet + " /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-codex-7",
				"name":       "codex-auth.json",
				"auth_index": "7",
				"provider":   "codex",
				"account_id": "acct-123",
				"disabled":   disabled,
			}})
		case http.MethodPatch + " /v0/management/auth-files/status":
			var payload struct {
				Disabled bool `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode status payload: %v", err)
			}
			patchedStates = append(patchedStates, payload.Disabled)
			disabled = payload.Disabled
			if len(patchedStates) == 1 {
				_ = st.Close()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := st.SaveSetup(ctx, store.Setup{CPAUpstreamURL: server.URL, ManagementKey: "mgmt"}); err != nil {
		t.Fatalf("save setup: %v", err)
	}
	item, err := st.UpsertAccountActionCandidate(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeReauth,
		Provider:          "codex",
		AuthFileName:      "codex-auth.json",
		AuthIndex:         "7",
		AccountIDSnapshot: "acct-123",
		Reason:            "token revoked",
	})
	if err != nil {
		t.Fatalf("upsert candidate: %v", err)
	}
	svc := New(st, managerconfigsvc.New(config.Config{}, st, collectorsvc.New(nil)), server.Client())

	_, err = svc.Enable(ctx, item.ID)
	if err == nil || !strings.Contains(err.Error(), "restored to disabled") {
		t.Fatalf("enable error = %v, want compensated persistence failure", err)
	}
	if !disabled || len(patchedStates) != 2 || patchedStates[0] || !patchedStates[1] {
		t.Fatalf("patched disabled states = %#v, current disabled=%t", patchedStates, disabled)
	}
}

func TestDeleteReportsExternalSuccessWhenResultPersistenceFails(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case http.MethodGet + " /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-codex-7",
				"name":       "codex-auth.json",
				"auth_index": "7",
				"provider":   "codex",
				"account_id": "acct-123",
			}})
		case http.MethodDelete + " /v0/management/auth-files":
			deleteCalls++
			_ = st.Close()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := st.SaveSetup(ctx, store.Setup{CPAUpstreamURL: server.URL, ManagementKey: "mgmt"}); err != nil {
		t.Fatalf("save setup: %v", err)
	}
	item, err := st.UpsertAccountActionCandidate(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeDelete,
		Provider:          "codex",
		AuthFileName:      "codex-auth.json",
		AuthIndex:         "7",
		AccountIDSnapshot: "acct-123",
		Reason:            "token revoked",
	})
	if err != nil {
		t.Fatalf("upsert candidate: %v", err)
	}
	svc := New(st, managerconfigsvc.New(config.Config{}, st, collectorsvc.New(nil)), server.Client())

	_, err = svc.DeleteAuthFile(ctx, item.ID)
	if err == nil || !strings.Contains(
		err.Error(),
		"CPA auth file delete succeeded but candidate status persistence failed",
	) {
		t.Fatalf("delete error = %v, want explicit partial-success failure", err)
	}
	if deleteCalls != 1 {
		t.Fatalf("delete calls = %d, want 1", deleteCalls)
	}
}

func TestEnableRejectsAmbiguousStatusMutationScope(t *testing.T) {
	ctx := context.Background()
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
					"disabled":   true,
				},
				{
					"id":         "runtime-auth-2",
					"name":       "shared.json",
					"auth_index": "auth-2",
					"provider":   "codex",
					"account":    "other@example.com",
					"account_id": "acct-456",
					"disabled":   true,
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

	if err := st.SaveSetup(ctx, store.Setup{CPAUpstreamURL: server.URL, ManagementKey: "mgmt"}); err != nil {
		t.Fatalf("save setup: %v", err)
	}
	item, err := st.UpsertAccountActionCandidate(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeReauth,
		Provider:          "codex",
		AuthFileName:      "shared.json",
		AuthIndex:         "auth-1",
		AccountSnapshot:   "user@example.com",
		AccountIDSnapshot: "acct-123",
		Reason:            "token revoked",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	svc := New(st, managerconfigsvc.New(config.Config{}, st, collectorsvc.New(nil)), server.Client())
	_, err = svc.Enable(ctx, item.ID)
	if !errors.Is(err, ErrCandidateConflict) || !errors.Is(err, cpaauthfiles.ErrStatusMutationScopeAmbiguous) {
		t.Fatalf("enable error = %v, want candidate and status-scope conflict", err)
	}
	if patchCalls != 0 {
		t.Fatalf("patch calls = %d, want 0", patchCalls)
	}
	current, ok, getErr := st.GetAccountActionCandidate(ctx, item.ID)
	if getErr != nil || !ok {
		t.Fatalf("get current: %v ok=%t", getErr, ok)
	}
	if current.Status != model.AccountActionStatusPending || !strings.Contains(current.LastError, "scope is ambiguous") {
		t.Fatalf("candidate = %#v, want pending ambiguous failure", current)
	}
}

func TestDeleteRejectsMismatchedCurrentAuthFile(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	var deleted bool
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
		case "DELETE /v0/management/auth-files":
			deleted = true
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := st.SaveSetup(ctx, store.Setup{CPAUpstreamURL: server.URL, ManagementKey: "mgmt"}); err != nil {
		t.Fatalf("save setup: %v", err)
	}
	item, err := st.UpsertAccountActionCandidate(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeDelete,
		Provider:          "codex",
		AuthFileName:      "codex-auth.json",
		AuthIndex:         "7",
		AccountSnapshot:   "user@example.com",
		AccountIDSnapshot: "acct-123",
		Reason:            "token revoked",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	svc := New(st, managerconfigsvc.New(config.Config{}, st, collectorsvc.New(nil)), server.Client())
	_, err = svc.DeleteAuthFile(ctx, item.ID)
	if !errors.Is(err, ErrCandidateConflict) {
		t.Fatalf("delete error = %v, want conflict", err)
	}
	if deleted {
		t.Fatal("DELETE should not be called on mismatched auth file")
	}
	current, ok, err := st.GetAccountActionCandidate(ctx, item.ID)
	if err != nil || !ok {
		t.Fatalf("get current: %v ok=%t", err, ok)
	}
	if current.Status != model.AccountActionStatusPending {
		t.Fatalf("status = %q", current.Status)
	}
}

func TestDeleteAcceptsCurrentAuthFileProjectID(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	deletedSelector := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-antigravity-1",
				"name":       "antigravity-auth.json",
				"auth_index": "antigravity-1",
				"provider":   "antigravity",
				"account":    "vertex@example.com",
				"project_id": "vertex-project-42",
			}})
		case "DELETE /v0/management/auth-files":
			deletedSelector = r.URL.Query().Get("name")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := st.SaveSetup(ctx, store.Setup{CPAUpstreamURL: server.URL, ManagementKey: "mgmt"}); err != nil {
		t.Fatalf("save setup: %v", err)
	}
	item, err := st.UpsertAccountActionCandidate(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeDelete,
		Provider:          "antigravity",
		AuthFileName:      "antigravity-auth.json",
		AuthIndex:         "antigravity-1",
		AccountSnapshot:   "vertex@example.com",
		AccountIDSnapshot: "vertex-project-42",
		Reason:            "workspace deactivated",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	svc := New(st, managerconfigsvc.New(config.Config{}, st, collectorsvc.New(nil)), server.Client())
	updated, err := svc.DeleteAuthFile(ctx, item.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deletedSelector != "runtime-antigravity-1" {
		t.Fatalf("delete selector = %q, want runtime-antigravity-1", deletedSelector)
	}
	if updated.Status != model.AccountActionStatusDeleted {
		t.Fatalf("status = %q", updated.Status)
	}
}

func TestDeleteRejectsSharedPhysicalAuthFile(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	deleteCalls := 0
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
				},
				{
					"id":         "runtime-child",
					"name":       "shared.json",
					"auth_index": "auth-2",
					"provider":   "codex",
					"account":    "other@example.com",
					"account_id": "acct-456",
				},
			})
		case "DELETE /v0/management/auth-files":
			deleteCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := st.SaveSetup(ctx, store.Setup{CPAUpstreamURL: server.URL, ManagementKey: "mgmt"}); err != nil {
		t.Fatalf("save setup: %v", err)
	}
	item, err := st.UpsertAccountActionCandidate(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeDelete,
		Provider:          "codex",
		AuthFileName:      "shared.json",
		AuthIndex:         "auth-1",
		AccountSnapshot:   "user@example.com",
		AccountIDSnapshot: "acct-123",
		Reason:            "token revoked",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	svc := New(st, managerconfigsvc.New(config.Config{}, st, collectorsvc.New(nil)), server.Client())
	_, err = svc.DeleteAuthFile(ctx, item.ID)
	if !errors.Is(err, ErrCandidateConflict) || !strings.Contains(err.Error(), "contains 2 credentials") {
		t.Fatalf("delete shared auth file error = %v, want candidate conflict", err)
	}
	if deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want 0", deleteCalls)
	}
}

func TestAccountActionsRejectCandidateWithoutStableIdentity(t *testing.T) {
	for _, action := range []string{"enable", "delete"} {
		t.Run(action, func(t *testing.T) {
			ctx := context.Background()
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

			if err := st.SaveSetup(ctx, store.Setup{CPAUpstreamURL: server.URL, ManagementKey: "mgmt"}); err != nil {
				t.Fatalf("save setup: %v", err)
			}
			item, err := st.UpsertAccountActionCandidate(ctx, model.AccountActionCandidateUpsert{
				ActionType:      model.AccountActionTypeDelete,
				Provider:        "codex",
				AuthFileName:    "codex-auth.json",
				AccountSnapshot: "codex-auth.json",
				Reason:          "legacy candidate",
			})
			if err != nil {
				t.Fatalf("upsert: %v", err)
			}

			svc := New(st, managerconfigsvc.New(config.Config{}, st, collectorsvc.New(nil)), server.Client())
			if action == "enable" {
				_, err = svc.Enable(ctx, item.ID)
			} else {
				_, err = svc.DeleteAuthFile(ctx, item.ID)
			}
			if !errors.Is(err, ErrCandidateConflict) || !errors.Is(err, cpaauthfiles.ErrIdentityMismatch) {
				t.Fatalf("%s error = %v, want candidate identity conflict", action, err)
			}
			if requestCalls != 0 {
				t.Fatalf("CPA request calls = %d, want 0", requestCalls)
			}
			current, ok, getErr := st.GetAccountActionCandidate(ctx, item.ID)
			if getErr != nil || !ok {
				t.Fatalf("get candidate: %v ok=%t", getErr, ok)
			}
			if current.Status != model.AccountActionStatusPending || !strings.Contains(current.LastError, "no stable auth index") {
				t.Fatalf("candidate = %#v, want pending stable-identity failure", current)
			}
		})
	}
}

func TestDeleteSerializesSameCandidateStatusChanges(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(t.TempDir() + "/usage.sqlite")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer st.Close()

	deleteStarted := make(chan struct{})
	releaseDelete := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": "runtime-delete", "name": "delete.json", "auth_index": "delete-auth", "provider": "codex", "account_id": "delete-account",
			}})
		case "DELETE /v0/management/auth-files":
			close(deleteStarted)
			<-releaseDelete
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := st.SaveSetup(ctx, store.Setup{CPAUpstreamURL: server.URL, ManagementKey: "mgmt"}); err != nil {
		t.Fatalf("save setup: %v", err)
	}
	deleteCandidate, err := st.UpsertAccountActionCandidate(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeDelete,
		Provider:          "codex",
		AuthFileName:      "delete.json",
		AuthIndex:         "delete-auth",
		AccountIDSnapshot: "delete-account",
		ReasonCode:        "token_revoked",
	})
	if err != nil {
		t.Fatalf("upsert delete candidate: %v", err)
	}
	otherCandidate, err := st.UpsertAccountActionCandidate(ctx, model.AccountActionCandidateUpsert{
		ActionType:        model.AccountActionTypeReauth,
		Provider:          "codex",
		AuthFileName:      "other.json",
		AuthIndex:         "other-auth",
		AccountIDSnapshot: "other-account",
		ReasonCode:        "token_revoked",
	})
	if err != nil {
		t.Fatalf("upsert other candidate: %v", err)
	}

	svc := New(st, managerconfigsvc.New(config.Config{}, st, collectorsvc.New(nil)), server.Client())
	deleteErr := make(chan error, 1)
	go func() {
		_, err := svc.DeleteAuthFile(ctx, deleteCandidate.ID)
		deleteErr <- err
	}()
	select {
	case <-deleteStarted:
	case <-time.After(time.Second):
		t.Fatal("delete action did not start")
	}

	if _, err := svc.Ignore(ctx, otherCandidate.ID); err != nil {
		t.Fatalf("unrelated candidate action was blocked: %v", err)
	}
	ignoreErr := make(chan error, 1)
	go func() {
		_, err := svc.Ignore(ctx, deleteCandidate.ID)
		ignoreErr <- err
	}()
	select {
	case err := <-ignoreErr:
		t.Fatalf("same-candidate ignore completed during delete: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseDelete)
	if err := <-deleteErr; err != nil {
		t.Fatalf("delete action: %v", err)
	}
	if err := <-ignoreErr; !errors.Is(err, ErrCandidateNotPending) {
		t.Fatalf("concurrent ignore error = %v, want ErrCandidateNotPending", err)
	}
	current, ok, err := st.GetAccountActionCandidate(ctx, deleteCandidate.ID)
	if err != nil || !ok || current.Status != model.AccountActionStatusDeleted {
		t.Fatalf("delete candidate after race = %#v ok=%t err=%v", current, ok, err)
	}
}
