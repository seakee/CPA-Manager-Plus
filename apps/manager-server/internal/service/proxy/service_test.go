package proxy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func testAuthFileContentSHA256(content []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(content))
}

func testVerifiedAuthFileWriteRequest(t *testing.T, name string, currentContent []byte, nextContent []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(nextContent); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	identityJSON, err := json.Marshal([]map[string]any{{
		"name":      name,
		"runtimeId": "runtime-1",
		"authIndex": "auth-1",
		"provider":  "codex",
		"accountId": "account-1",
	}})
	if err != nil {
		t.Fatalf("marshal identities: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "/v0/management/auth-files", bytes.NewReader(body.Bytes()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(authFileWriteIdentitiesHeader, url.QueryEscape(string(identityJSON)))
	req.Header.Set(authFileWriteContentSHA256Header, testAuthFileContentSHA256(currentContent))
	return req
}

func TestInspectAuthFileOwnershipMutationRestoresStatusBody(t *testing.T) {
	body := `{"name":"auth-a.json","auth_index":"auth-1","disabled":false}`
	req, err := http.NewRequest(http.MethodPatch, "/v0/management/auth-files/status", strings.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	if len(mutation.fileNames) != 1 || mutation.fileNames[0] != "auth-a.json" || mutation.clearAll || mutation.statusMutation == nil {
		t.Fatalf("mutation = %#v", mutation)
	}
	if mutation.statusMutation.selector != "auth-a.json" || mutation.statusMutation.authIndex != "auth-1" {
		t.Fatalf("status mutation = %#v", mutation.statusMutation)
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read restored body: %v", err)
	}
	if string(raw) != body {
		t.Fatalf("restored body = %q, want %q", raw, body)
	}
}

func TestInspectAuthFileOwnershipMutationDetectsLegacyDisabledPatch(t *testing.T) {
	req, err := http.NewRequest(
		http.MethodPatch,
		"/v0/management/auth-files",
		strings.NewReader(`{"name":"auth-a.json","auth_index":"auth-1","disabled":true}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	if len(mutation.fileNames) != 1 || mutation.fileNames[0] != "auth-a.json" || mutation.statusMutation == nil {
		t.Fatalf("mutation = %#v", mutation)
	}
}

func TestInspectAuthFileOwnershipMutationUsesPhysicalNameForRuntimeDeleteSelector(t *testing.T) {
	req, err := http.NewRequest(http.MethodDelete, "/v0/management/auth-files?name=runtime-auth-1", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(authFilePhysicalNameHeader, "shared.json")

	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	if len(mutation.fileNames) != 1 || mutation.fileNames[0] != "shared.json" {
		t.Fatalf("runtime delete mutation = %#v, want physical file ownership", mutation)
	}
}

func TestAuthFileMutationLockTargetsUseGlobalLockForUnverifiedSelectors(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
	}{
		{
			name:   "legacy status runtime selector",
			method: http.MethodPatch,
			path:   "/v0/management/auth-files/status",
			body:   `{"name":"runtime-auth-1","auth_index":"auth-1","disabled":true}`,
		},
		{
			name:   "legacy delete runtime selector",
			method: http.MethodDelete,
			path:   "/v0/management/auth-files?name=runtime-auth-1",
		},
		{
			name:   "legacy fields runtime selector",
			method: http.MethodPatch,
			path:   "/v0/management/auth-files/fields",
			body:   `{"name":"runtime-auth-1","priority":10}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}
			req, err := http.NewRequest(tc.method, tc.path, body)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}
			mutation, err := inspectAuthFileOwnershipMutation(req)
			if err != nil {
				t.Fatalf("inspect mutation: %v", err)
			}
			if keys, all := authFileMutationLockTargets(mutation); !all || len(keys) != 0 {
				t.Fatalf("lock targets = %v all=%t, want global lock", keys, all)
			}
		})
	}
}

func TestAuthFileMutationLockTargetsKeepPhysicalMetadataFileScoped(t *testing.T) {
	req, err := http.NewRequest(
		http.MethodPatch,
		"/v0/management/auth-files/status",
		strings.NewReader(`{"name":"runtime-auth-1","auth_index":"auth-1","disabled":true,"cpamp_physical_name":"shared.json"}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	keys, all := authFileMutationLockTargets(mutation)
	if all || strings.Join(keys, ",") != "runtime-auth-1,shared.json" {
		t.Fatalf("lock targets = %v all=%t, want shared.json file lock", keys, all)
	}
}

func TestPrepareAuthFileDeleteMutationVerifiesIdentityAndPreservesExplicitSourceFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":         "runtime-plugin",
			"name":       "source.json",
			"auth_index": "auth-1",
			"provider":   "gemini-cli",
			"account":    "project@example.com",
		}})
	}))
	defer server.Close()

	identityJSON, err := json.Marshal([]map[string]any{{
		"name":            "source.json",
		"runtimeId":       "runtime-plugin",
		"authIndex":       "auth-1",
		"provider":        "gemini-cli",
		"accountSnapshot": "project@example.com",
	}})
	if err != nil {
		t.Fatalf("marshal identities: %v", err)
	}
	req, err := http.NewRequest(http.MethodDelete, "/v0/management/auth-files?name=source.json", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(authFilePhysicalNameHeader, "source.json")
	req.Header.Set(authFileDeleteIdentitiesHeader, url.QueryEscape(string(identityJSON)))

	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	if mutation.deleteMutation == nil || len(mutation.deleteMutation.identities) != 1 {
		t.Fatalf("delete mutation = %#v", mutation.deleteMutation)
	}
	mutation, err = New(nil).prepareAuthFileDeleteMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, req, mutation)
	if err != nil {
		t.Fatalf("prepare delete mutation: %v", err)
	}
	if got := req.URL.Query().Get("name"); got != "source.json" {
		t.Fatalf("forward selector = %q, want explicit physical source fallback", got)
	}
	if len(mutation.fileNames) != 1 || mutation.fileNames[0] != "source.json" {
		t.Fatalf("prepared mutation = %#v", mutation)
	}
}

func TestPrepareAuthFileDeleteMutationRejectsSingleSourceRuntimeCollision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":         "runtime-plugin",
				"name":       "source.json",
				"auth_index": "auth-1",
				"provider":   "gemini-cli",
				"account":    "project@example.com",
			},
			{
				"id":         "source.json",
				"name":       "other.json",
				"auth_index": "auth-2",
				"provider":   "codex",
				"account_id": "other-account",
			},
		})
	}))
	defer server.Close()

	identityJSON, err := json.Marshal([]map[string]any{{
		"name":            "source.json",
		"runtimeId":       "runtime-plugin",
		"authIndex":       "auth-1",
		"provider":        "gemini-cli",
		"accountSnapshot": "project@example.com",
	}})
	if err != nil {
		t.Fatalf("marshal identities: %v", err)
	}
	req, err := http.NewRequest(http.MethodDelete, "/v0/management/auth-files?name=source.json", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(authFilePhysicalNameHeader, "source.json")
	req.Header.Set(authFileDeleteIdentitiesHeader, url.QueryEscape(string(identityJSON)))

	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	_, err = New(nil).prepareAuthFileDeleteMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, req, mutation)
	if !errors.Is(err, cpaauthfiles.ErrDeleteMutationScopeAmbiguous) {
		t.Fatalf("prepare single source collision error = %v, want ErrDeleteMutationScopeAmbiguous", err)
	}
}

func TestPrepareAuthFileDeleteMutationRejectsChangedIdentity(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":         "runtime-1",
			"name":       "shared.json",
			"auth_index": "auth-1",
			"provider":   "codex",
			"account_id": "replacement-account",
		}})
	}))
	defer server.Close()

	identityJSON, err := json.Marshal([]map[string]any{{
		"name":      "shared.json",
		"runtimeId": "runtime-1",
		"authIndex": "auth-1",
		"provider":  "codex",
		"accountId": "original-account",
	}})
	if err != nil {
		t.Fatalf("marshal identities: %v", err)
	}
	req, err := http.NewRequest(http.MethodDelete, "/v0/management/auth-files?name=runtime-1", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(authFilePhysicalNameHeader, "shared.json")
	req.Header.Set(authFileDeleteIdentitiesHeader, url.QueryEscape(string(identityJSON)))
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	_, err = New(nil).prepareAuthFileDeleteMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, req, mutation)
	if !errors.Is(err, cpaauthfiles.ErrIdentityMismatch) {
		t.Fatalf("prepare delete mutation error = %v, want ErrIdentityMismatch", err)
	}
}

func TestPrepareAuthFileStatusMutationTargetsSameNameCredential(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "runtime-1", "name": "shared.json", "auth_index": "auth-1", "provider": "codex", "account_id": "account-1"},
			{"id": "runtime-2", "name": "shared.json", "auth_index": "2", "provider": "codex", "account_id": "account-2"},
		})
	}))
	defer server.Close()

	req, err := http.NewRequest(
		http.MethodPatch,
		"/v0/management/auth-files/status",
		strings.NewReader(`{"name":"shared.json","auth_index":2,"disabled":true}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	mutation, err = New(nil).prepareAuthFileStatusMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, req, mutation)
	if err != nil {
		t.Fatalf("prepare status mutation: %v", err)
	}
	if len(mutation.fileNames) != 0 || len(mutation.ownershipTargets) != 1 {
		t.Fatalf("prepared mutation = %#v", mutation)
	}
	target := mutation.ownershipTargets[0]
	if target.FileName != "shared.json" || target.Provider == nil || *target.Provider != "codex" ||
		target.AuthIndex == nil || *target.AuthIndex != "2" ||
		target.AccountID == nil || *target.AccountID != "account-2" {
		t.Fatalf("ownership target = %#v", target)
	}
	var payload struct {
		Name      string `json:"name"`
		AuthIndex string `json:"auth_index"`
		Disabled  bool   `json:"disabled"`
	}
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		t.Fatalf("decode rewritten request: %v", err)
	}
	if payload.Name != "runtime-2" || payload.AuthIndex != "2" || !payload.Disabled {
		t.Fatalf("rewritten payload = %#v", payload)
	}
}

func TestPrepareAuthFileStatusMutationRejectsCPAMPIdentityReplacement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":         "runtime-1",
			"name":       "shared.json",
			"auth_index": "auth-1",
			"provider":   "codex",
			"account_id": "replacement-account",
		}})
	}))
	defer server.Close()

	req, err := http.NewRequest(
		http.MethodPatch,
		"/v0/management/auth-files/status",
		strings.NewReader(`{"name":"runtime-1","auth_index":"auth-1","disabled":true,"cpamp_physical_name":"shared.json","cpamp_runtime_id":"runtime-1","cpamp_provider":"codex","cpamp_account_id":"original-account"}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	_, err = New(nil).prepareAuthFileStatusMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, req, mutation)
	if !errors.Is(err, cpaauthfiles.ErrIdentityMismatch) {
		t.Fatalf("prepare status mutation error = %v, want ErrIdentityMismatch", err)
	}
}

func TestPrepareAuthFileFieldsMutationVerifiesIdentityAndRewritesRuntimeSelector(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":         "runtime-1",
			"name":       "shared.json",
			"auth_index": "auth-1",
			"provider":   "codex",
			"account_id": "account-1",
			"account":    "user@example.com",
		}})
	}))
	defer server.Close()

	identityJSON, err := json.Marshal([]map[string]any{{
		"name":      "shared.json",
		"runtimeId": "runtime-1",
		"authIndex": "auth-1",
		"provider":  "codex",
		"accountId": "account-1",
	}})
	if err != nil {
		t.Fatalf("marshal identity: %v", err)
	}
	req, err := http.NewRequest(
		http.MethodPatch,
		"/v0/management/auth-files/fields",
		strings.NewReader(`{"name":"shared.json","priority":10}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(authFileMutationIdentityHeader, url.QueryEscape(string(identityJSON)))

	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect fields mutation: %v", err)
	}
	if mutation.fieldsMutation == nil || len(mutation.fileNames) != 0 {
		t.Fatalf("fields mutation = %#v", mutation)
	}
	mutation, err = New(nil).prepareAuthFileFieldsMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, req, mutation)
	if err != nil {
		t.Fatalf("prepare fields mutation: %v", err)
	}
	var payload map[string]any
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		t.Fatalf("decode rewritten fields payload: %v", err)
	}
	if payload["name"] != "runtime-1" || payload["priority"] != float64(10) {
		t.Fatalf("rewritten fields payload = %#v", payload)
	}
}

func TestPrepareAuthFileFieldsMutationRejectsIdentityReplacement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":         "runtime-1",
			"name":       "shared.json",
			"auth_index": "auth-1",
			"provider":   "codex",
			"account_id": "replacement-account",
		}})
	}))
	defer server.Close()

	identityJSON, err := json.Marshal([]map[string]any{{
		"name":      "shared.json",
		"runtimeId": "runtime-1",
		"authIndex": "auth-1",
		"provider":  "codex",
		"accountId": "original-account",
	}})
	if err != nil {
		t.Fatalf("marshal identity: %v", err)
	}
	req, err := http.NewRequest(
		http.MethodPatch,
		"/v0/management/auth-files/fields",
		strings.NewReader(`{"name":"runtime-1","priority":10}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set(authFileMutationIdentityHeader, url.QueryEscape(string(identityJSON)))
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect fields mutation: %v", err)
	}
	_, err = New(nil).prepareAuthFileFieldsMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, req, mutation)
	if !errors.Is(err, cpaauthfiles.ErrIdentityMismatch) {
		t.Fatalf("prepare fields mutation error = %v, want ErrIdentityMismatch", err)
	}
}

func TestPrepareAuthFileStatusMutationStripsCPAMPIdentityBeforeForwarding(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":         "runtime-1",
			"name":       "shared.json",
			"auth_index": "auth-1",
			"provider":   "codex",
			"account_id": "account-1",
			"account":    "user@example.com",
		}})
	}))
	defer server.Close()

	req, err := http.NewRequest(
		http.MethodPatch,
		"/v0/management/auth-files/status",
		strings.NewReader(`{"name":"runtime-1","auth_index":"auth-1","disabled":true,"cpamp_physical_name":"shared.json","cpamp_runtime_id":"runtime-1","cpamp_provider":"codex","cpamp_account_id":"account-1","cpamp_account_snapshot":"user@example.com","cpamp_source_identities":[{"name":"shared.json","runtime_id":"runtime-1","auth_index":"auth-1","provider":"codex","account_id":"account-1"}]}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	_, err = New(nil).prepareAuthFileStatusMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, req, mutation)
	if err != nil {
		t.Fatalf("prepare status mutation: %v", err)
	}
	var payload map[string]any
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		t.Fatalf("decode rewritten request: %v", err)
	}
	for _, key := range []string{
		"cpamp_physical_name",
		"cpamp_runtime_id",
		"cpamp_provider",
		"cpamp_account_id",
		"cpamp_account_snapshot",
		"cpamp_source_identities",
	} {
		if _, exists := payload[key]; exists {
			t.Fatalf("rewritten payload leaked %s: %#v", key, payload)
		}
	}
}

func TestPrepareAuthFileStatusMutationRejectsAddedPluginSourceMember(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v0/management/auth-files" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "runtime-a", "name": "source.json", "auth_index": "auth-a", "provider": "gemini-cli", "account_id": "account-a"},
			{"id": "runtime-b", "name": "source.json", "auth_index": "auth-b", "provider": "gemini-cli", "account_id": "account-b"},
			{"id": "runtime-added", "name": "source.json", "auth_index": "auth-added", "provider": "gemini-cli", "account_id": "account-added"},
		})
	}))
	defer server.Close()

	req, err := http.NewRequest(
		http.MethodPatch,
		"/v0/management/auth-files/status",
		strings.NewReader(`{"name":"source.json","auth_index":"auth-b","disabled":true,"cpamp_source_file":true,"cpamp_physical_name":"source.json","cpamp_runtime_id":"runtime-b","cpamp_provider":"gemini-cli","cpamp_account_id":"account-b","cpamp_source_identities":[{"name":"source.json","runtime_id":"runtime-a","auth_index":"auth-a","provider":"gemini-cli","account_id":"account-a"},{"name":"source.json","runtime_id":"runtime-b","auth_index":"auth-b","provider":"gemini-cli","account_id":"account-b"}]}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	_, err = New(nil).prepareAuthFileStatusMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, req, mutation)
	if !errors.Is(err, cpaauthfiles.ErrIdentityMismatch) {
		t.Fatalf("prepare status mutation error = %v, want ErrIdentityMismatch", err)
	}
}

func TestPrepareAuthFileStatusMutationDoesNotFallBackFromRuntimeIDOnAuthIndexMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v0/management/auth-files" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "source.json", "name": "other.json", "auth_index": "auth-other"},
			{"id": "runtime-target", "name": "source.json", "auth_index": "auth-target"},
		})
	}))
	defer server.Close()

	req, err := http.NewRequest(
		http.MethodPatch,
		"/v0/management/auth-files/status",
		strings.NewReader(`{"name":"source.json","auth_index":"auth-target","disabled":true}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	_, err = New(nil).prepareAuthFileStatusMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, req, mutation)
	if !errors.Is(err, cpaauthfiles.ErrAuthFileNotFound) {
		t.Fatalf("prepare status mutation error = %v, want ErrAuthFileNotFound", err)
	}
}

func TestPrepareLegacyAuthFileStatusMutationPreservesPhysicalSelector(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "runtime-2", "name": "shared.json", "auth_index": "auth-2", "provider": "codex", "account_id": "account-2"},
		})
	}))
	defer server.Close()

	req, err := http.NewRequest(
		http.MethodPatch,
		"/v0/management/auth-files",
		strings.NewReader(`{"name":"shared.json","auth_index":"auth-2","disabled":true}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	mutation, err = New(nil).prepareAuthFileStatusMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, req, mutation)
	if err != nil {
		t.Fatalf("prepare status mutation: %v", err)
	}
	if len(mutation.ownershipTargets) != 1 {
		t.Fatalf("prepared mutation = %#v", mutation)
	}
	var payload struct {
		Name      string `json:"name"`
		AuthIndex string `json:"auth_index"`
		Disabled  bool   `json:"disabled"`
	}
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		t.Fatalf("decode rewritten request: %v", err)
	}
	if payload.Name != "shared.json" || payload.AuthIndex != "auth-2" || !payload.Disabled {
		t.Fatalf("rewritten legacy payload = %#v", payload)
	}
}

func TestPrepareAuthFileStatusMutationAllowsSharedPhysicalSourceScope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "source.json", "name": "source.json", "auth_index": "source-index"},
			{"id": "virtual-child", "name": "source.json", "auth_index": "child-index"},
		})
	}))
	defer server.Close()

	req, err := http.NewRequest(
		http.MethodPatch,
		"/v0/management/auth-files/status",
		strings.NewReader(`{"name":"source.json","auth_index":"source-index","disabled":true}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	mutation, err = New(nil).prepareAuthFileStatusMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, req, mutation)
	if err != nil {
		t.Fatalf("prepare status mutation: %v", err)
	}
	if len(mutation.fileNames) != 1 || mutation.fileNames[0] != "source.json" || len(mutation.ownershipTargets) != 0 {
		t.Fatalf("prepared mutation = %#v, want whole source file ownership scope", mutation)
	}
	var payload struct {
		Name      string `json:"name"`
		AuthIndex string `json:"auth_index"`
		Disabled  bool   `json:"disabled"`
	}
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		t.Fatalf("decode rewritten request: %v", err)
	}
	if payload.Name != "source.json" || payload.AuthIndex != "source-index" || !payload.Disabled {
		t.Fatalf("rewritten payload = %#v", payload)
	}
}

func TestPrepareAuthFileStatusMutationAllowsExplicitPluginSourceFallbackWithoutSourceRow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "virtual-a", "name": "source.json", "auth_index": "auth-a", "provider": "gemini-cli"},
			{"id": "virtual-b", "name": "source.json", "auth_index": "auth-b", "provider": "gemini-cli"},
		})
	}))
	defer server.Close()

	req, err := http.NewRequest(
		http.MethodPatch,
		"/v0/management/auth-files/status",
		strings.NewReader(`{"name":"source.json","auth_index":"auth-b","disabled":true,"cpamp_source_file":true,"cpamp_source_identities":[{"name":"source.json","runtime_id":"virtual-a","auth_index":"auth-a","provider":"gemini-cli"},{"name":"source.json","runtime_id":"virtual-b","auth_index":"auth-b","provider":"gemini-cli"}]}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	if mutation.statusMutation == nil || !mutation.statusMutation.sourceFile {
		t.Fatalf("status mutation = %#v, want explicit source-file fallback", mutation.statusMutation)
	}
	mutation, err = New(nil).prepareAuthFileStatusMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, req, mutation)
	if err != nil {
		t.Fatalf("prepare status mutation: %v", err)
	}
	if len(mutation.fileNames) != 1 || mutation.fileNames[0] != "source.json" || len(mutation.ownershipTargets) != 0 {
		t.Fatalf("prepared mutation = %#v, want whole source file ownership scope", mutation)
	}
	var payload map[string]any
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		t.Fatalf("decode rewritten request: %v", err)
	}
	if payload["name"] != "source.json" || payload["auth_index"] != "auth-b" || payload["disabled"] != true {
		t.Fatalf("rewritten payload = %#v", payload)
	}
	if _, exists := payload["cpamp_source_file"]; exists {
		t.Fatalf("rewritten payload leaked CPAMP fallback marker: %#v", payload)
	}
}

func TestPrepareAuthFileStatusMutationRejectsExplicitSourceFileRuntimeCollision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v0/management/auth-files" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "source.json", "name": "other.json", "auth_index": "auth-other"},
			{"id": "runtime-target", "name": "source.json", "auth_index": "auth-target", "provider": "gemini-cli"},
		})
	}))
	defer server.Close()

	req, err := http.NewRequest(
		http.MethodPatch,
		"/v0/management/auth-files/status",
		strings.NewReader(`{"name":"source.json","auth_index":"auth-target","disabled":true,"cpamp_source_file":true,"cpamp_source_identities":[{"name":"source.json","runtime_id":"runtime-target","auth_index":"auth-target","provider":"gemini-cli"}]}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	_, err = New(nil).prepareAuthFileStatusMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, req, mutation)
	if !errors.Is(err, cpaauthfiles.ErrStatusMutationScopeAmbiguous) {
		t.Fatalf("prepare source-file mutation error = %v, want ErrStatusMutationScopeAmbiguous", err)
	}
}

func TestPrepareAuthFileStatusMutationAllowsExplicitSinglePluginSourceFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id": "virtual-a", "name": "source.json", "auth_index": "auth-a", "provider": "gemini-cli",
		}})
	}))
	defer server.Close()

	req, err := http.NewRequest(
		http.MethodPatch,
		"/v0/management/auth-files/status",
		strings.NewReader(`{"name":"source.json","auth_index":"auth-a","disabled":true,"cpamp_source_file":true,"cpamp_source_identities":[{"name":"source.json","runtime_id":"virtual-a","auth_index":"auth-a","provider":"gemini-cli"}]}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	mutation, err = New(nil).prepareAuthFileStatusMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, req, mutation)
	if err != nil {
		t.Fatalf("prepare status mutation: %v", err)
	}
	if len(mutation.fileNames) != 1 || mutation.fileNames[0] != "source.json" || len(mutation.ownershipTargets) != 0 {
		t.Fatalf("prepared mutation = %#v, want whole single-member source ownership scope", mutation)
	}
	var payload map[string]any
	if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
		t.Fatalf("decode rewritten request: %v", err)
	}
	if payload["name"] != "source.json" || payload["auth_index"] != "auth-a" || payload["disabled"] != true {
		t.Fatalf("rewritten payload = %#v", payload)
	}
	if _, exists := payload["cpamp_source_file"]; exists {
		t.Fatalf("rewritten payload leaked CPAMP fallback marker: %#v", payload)
	}
}

func TestPrepareAuthFileStatusMutationRejectsExpandedChild(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("name") {
		case "virtual-child":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "virtual-child", "name": "source.json", "auth_index": "child-index"},
			})
		case "source.json":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "source.json", "name": "source.json", "auth_index": "source-index"},
				{"id": "virtual-child", "name": "source.json", "auth_index": "child-index"},
			})
		default:
			t.Fatalf("unexpected query = %q", r.URL.RawQuery)
		}
	}))
	defer server.Close()

	req, err := http.NewRequest(
		http.MethodPatch,
		"/v0/management/auth-files/status",
		strings.NewReader(`{"name":"virtual-child","auth_index":"child-index","disabled":true}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	_, err = New(nil).prepareAuthFileStatusMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, req, mutation)
	if !errors.Is(err, cpaauthfiles.ErrStatusMutationScopeAmbiguous) {
		t.Fatalf("prepare error = %v, want ErrStatusMutationScopeAmbiguous", err)
	}
}

func TestInspectAuthFileOwnershipMutationReadsMultipartUpload(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "auth-a.json")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte(`{"type":"codex"}`)); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "/v0/management/auth-files", bytes.NewReader(body.Bytes()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect mutation: %v", err)
	}
	if len(mutation.fileNames) != 1 || mutation.fileNames[0] != "auth-a.json" {
		t.Fatalf("mutation = %#v", mutation)
	}
	restored, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read restored request: %v", err)
	}
	if !bytes.Equal(restored, body.Bytes()) {
		t.Fatal("multipart request body was not restored")
	}
}

func TestPrepareAuthFileWriteMutationVerifiesCompleteSourceMembership(t *testing.T) {
	sourceContent := []byte(`[{"auth_index":"auth-1"},{"auth_index":"auth-2"}]`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "runtime-1", "name": "shared.json", "auth_index": "auth-1", "provider": "codex", "account_id": "account-1"},
				{"id": "runtime-2", "name": "shared.json", "auth_index": "auth-2", "provider": "codex", "account_id": "account-2"},
			})
		case "/v0/management/auth-files/download":
			if got := r.URL.Query().Get("name"); got != "shared.json" {
				t.Fatalf("download name = %q", got)
			}
			_, _ = w.Write(sourceContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "shared.json")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write(sourceContent); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	identityJSON, err := json.Marshal([]map[string]any{
		{"name": "shared.json", "runtimeId": "runtime-1", "authIndex": "auth-1", "provider": "codex", "accountId": "account-1"},
		{"name": "shared.json", "runtimeId": "runtime-2", "authIndex": "auth-2", "provider": "codex", "accountId": "account-2"},
	})
	if err != nil {
		t.Fatalf("marshal identities: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "/v0/management/auth-files", bytes.NewReader(body.Bytes()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(authFileWriteIdentitiesHeader, url.QueryEscape(string(identityJSON)))
	req.Header.Set(authFileWriteContentSHA256Header, testAuthFileContentSHA256(sourceContent))
	mutation, err := inspectAuthFileOwnershipMutation(req)
	if err != nil {
		t.Fatalf("inspect write mutation: %v", err)
	}
	if mutation.writeMutation == nil || len(mutation.fileNames) != 1 {
		t.Fatalf("write mutation = %#v", mutation)
	}
	prepared, err := New(nil).prepareAuthFileWriteMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, mutation)
	if err != nil {
		t.Fatalf("prepare write mutation: %v", err)
	}
	if len(prepared.fileNames) != 0 || len(prepared.ownershipTargets) != 0 {
		t.Fatalf("verified fields write ownership mutation = %#v, want none", prepared)
	}
}

func TestPrepareAuthFileWriteMutationRejectsChangedSourceContent(t *testing.T) {
	currentContent := []byte(`[{"auth_index":"auth-1","note":"new"}]`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-1",
				"name":       "shared.json",
				"auth_index": "auth-1",
				"provider":   "codex",
				"account_id": "account-1",
			}})
		case "/v0/management/auth-files/download":
			_, _ = w.Write(currentContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	mutation := authFileOwnershipMutation{
		fileNames: []string{"shared.json"},
		writeMutation: &authFileWriteMutation{
			physicalName: "shared.json",
			identities: []cpaauthfiles.Identity{{
				AuthFileName:      "shared.json",
				RuntimeID:         "runtime-1",
				AuthIndex:         "auth-1",
				Provider:          "codex",
				AccountIDSnapshot: "account-1",
			}},
			contentSHA256: testAuthFileContentSHA256(
				[]byte(`[{"auth_index":"auth-1","note":"old"}]`),
			),
		},
	}

	_, err := New(nil).prepareAuthFileWriteMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, mutation)
	if !errors.Is(err, cpaauthfiles.ErrIdentityMismatch) || !strings.Contains(err.Error(), "content changed") {
		t.Fatalf("prepare write mutation error = %v, want content identity mismatch", err)
	}
}

func TestProxyVerifiedAuthFileWritesRecheckContentAfterSameFileMutation(t *testing.T) {
	initialContent := []byte(`[{"auth_index":"auth-1","note":"old"}]`)
	firstContent := []byte(`[{"auth_index":"auth-1","note":"first"}]`)
	secondContent := []byte(`[{"auth_index":"auth-1","note":"second"}]`)
	firstPostStarted := make(chan struct{})
	allowFirstPost := make(chan struct{})
	secondPostStarted := make(chan struct{})
	var firstPostOnce sync.Once
	var secondPostOnce sync.Once
	var stateMu sync.Mutex
	currentContent := append([]byte(nil), initialContent...)
	downloadCalls := 0
	postCalls := 0

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-1",
				"name":       "shared.json",
				"auth_index": "auth-1",
				"provider":   "codex",
				"account_id": "account-1",
			}})
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files/download":
			stateMu.Lock()
			downloadCalls++
			content := append([]byte(nil), currentContent...)
			stateMu.Unlock()
			_, _ = w.Write(content)
		case r.Method == http.MethodPost && r.URL.Path == "/v0/management/auth-files":
			file, _, err := r.FormFile("file")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			uploaded, err := io.ReadAll(file)
			_ = file.Close()
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			stateMu.Lock()
			postCalls++
			call := postCalls
			stateMu.Unlock()
			if call == 1 {
				firstPostOnce.Do(func() { close(firstPostStarted) })
				<-allowFirstPost
			} else {
				secondPostOnce.Do(func() { close(secondPostStarted) })
			}
			stateMu.Lock()
			currentContent = append(currentContent[:0], uploaded...)
			stateMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)

	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.SaveSetup(context.Background(), store.Setup{
		CPAUpstreamURL: upstream.URL,
		ManagementKey:  "management-key",
	}); err != nil {
		t.Fatalf("save setup: %v", err)
	}
	service := New(managerconfig.New(config.Config{}, st, nil), st)
	writeError := func(w http.ResponseWriter, status int, err error) {
		http.Error(w, err.Error(), status)
	}
	run := func(req *http.Request) <-chan *httptest.ResponseRecorder {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			recorder := httptest.NewRecorder()
			service.ProxyManagement(recorder, req, writeError)
			done <- recorder
		}()
		return done
	}

	firstDone := run(testVerifiedAuthFileWriteRequest(t, "shared.json", initialContent, firstContent))
	<-firstPostStarted
	secondDone := run(testVerifiedAuthFileWriteRequest(t, "shared.json", initialContent, secondContent))
	concurrentForward := false
	select {
	case <-secondPostStarted:
		concurrentForward = true
	case <-time.After(150 * time.Millisecond):
	}
	close(allowFirstPost)
	firstResponse := <-firstDone
	secondResponse := <-secondDone

	if concurrentForward {
		t.Fatal("second same-file upload reached CPA before the first mutation completed")
	}
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("first response status = %d body=%q", firstResponse.Code, firstResponse.Body.String())
	}
	if secondResponse.Code != http.StatusConflict || !strings.Contains(secondResponse.Body.String(), "content changed") {
		t.Fatalf("second response status = %d body=%q, want content conflict", secondResponse.Code, secondResponse.Body.String())
	}
	stateMu.Lock()
	defer stateMu.Unlock()
	if postCalls != 1 || downloadCalls != 2 {
		t.Fatalf("CPA calls post=%d download=%d, want 1 post and 2 downloads", postCalls, downloadCalls)
	}
}

func TestInspectAuthFileOwnershipMutationRequiresWriteContentSHA256(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "shared.json")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := part.Write([]byte(`{"type":"codex"}`)); err != nil {
		t.Fatalf("write form file: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	identityJSON, err := json.Marshal([]map[string]any{{
		"name":      "shared.json",
		"runtimeId": "runtime-1",
	}})
	if err != nil {
		t.Fatalf("marshal identities: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "/v0/management/auth-files", bytes.NewReader(body.Bytes()))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set(authFileWriteIdentitiesHeader, url.QueryEscape(string(identityJSON)))

	_, err = inspectAuthFileOwnershipMutation(req)
	if err == nil || !strings.Contains(err.Error(), "content SHA-256 is required") {
		t.Fatalf("inspect error = %v, want missing content SHA-256", err)
	}
}

func TestPrepareAuthFileWriteMutationRejectsAddedSourceMember(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "runtime-1", "name": "shared.json", "auth_index": "auth-1", "provider": "codex", "account_id": "account-1"},
			{"id": "runtime-2", "name": "shared.json", "auth_index": "auth-2", "provider": "codex", "account_id": "account-2"},
			{"id": "runtime-3", "name": "shared.json", "auth_index": "auth-3", "provider": "codex", "account_id": "account-3"},
		})
	}))
	defer server.Close()

	identityJSON, err := json.Marshal([]map[string]any{
		{"name": "shared.json", "runtimeId": "runtime-1", "authIndex": "auth-1", "provider": "codex", "accountId": "account-1"},
		{"name": "shared.json", "runtimeId": "runtime-2", "authIndex": "auth-2", "provider": "codex", "accountId": "account-2"},
	})
	if err != nil {
		t.Fatalf("marshal identities: %v", err)
	}
	mutation := authFileOwnershipMutation{
		fileNames: []string{"shared.json"},
		writeMutation: &authFileWriteMutation{
			physicalName: "shared.json",
			identities: func() []cpaauthfiles.Identity {
				req, reqErr := http.NewRequest(http.MethodPost, "/", nil)
				if reqErr != nil {
					t.Fatalf("new identity request: %v", reqErr)
				}
				req.Header.Set(authFileWriteIdentitiesHeader, url.QueryEscape(string(identityJSON)))
				identities, readErr := readAuthFileIdentitiesHeader(req, authFileWriteIdentitiesHeader, "write")
				if readErr != nil {
					t.Fatalf("read identities: %v", readErr)
				}
				return identities
			}(),
		},
	}
	_, err = New(nil).prepareAuthFileWriteMutation(context.Background(), store.Setup{
		CPAUpstreamURL: server.URL,
		ManagementKey:  "mgmt",
	}, mutation)
	if !errors.Is(err, cpaauthfiles.ErrDeleteMutationScopeAmbiguous) {
		t.Fatalf("prepare write mutation error = %v, want ErrDeleteMutationScopeAmbiguous", err)
	}
}

func TestSuccessfulAuthFileOwnershipMutationKeepsOnlySuccessfulFiles(t *testing.T) {
	response := &http.Response{
		Body: io.NopCloser(strings.NewReader(`{"files":["auth-a.json"],"failed":[{"name":"auth-b.json"}]}`)),
	}
	mutation, err := successfulAuthFileOwnershipMutation(response, authFileOwnershipMutation{
		fileNames: []string{"auth-a.json", "auth-b.json"},
	})
	if err != nil {
		t.Fatalf("resolve successful mutation: %v", err)
	}
	if len(mutation.fileNames) != 1 || mutation.fileNames[0] != "auth-a.json" {
		t.Fatalf("mutation = %#v", mutation)
	}
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read restored response: %v", err)
	}
	if !bytes.Contains(raw, []byte("auth-b.json")) {
		t.Fatalf("restored response = %q", raw)
	}
}

func TestSuccessfulAuthFileOwnershipMutationDerivesClearAllPartialSuccess(t *testing.T) {
	response := &http.Response{
		Body: io.NopCloser(strings.NewReader(`{"deleted":1,"failed":[{"name":"auth-b.json"}]}`)),
	}
	mutation, err := successfulAuthFileOwnershipMutation(response, authFileOwnershipMutation{
		fileNames: []string{"auth-a.json", "auth-b.json"},
		clearAll:  true,
	})
	if err != nil {
		t.Fatalf("resolve clear-all mutation: %v", err)
	}
	if mutation.clearAll || len(mutation.fileNames) != 1 || mutation.fileNames[0] != "auth-a.json" {
		t.Fatalf("mutation = %#v", mutation)
	}
}

func TestSuccessfulAuthFileOwnershipMutationRejectsLogicalFailure(t *testing.T) {
	response := &http.Response{
		Body: io.NopCloser(strings.NewReader(`{"status":"error","deleted":0}`)),
	}
	mutation, err := successfulAuthFileOwnershipMutation(response, authFileOwnershipMutation{
		fileNames: []string{"auth-a.json"},
	})
	if err != nil {
		t.Fatalf("resolve logical failure: %v", err)
	}
	if mutation.clearAll || len(mutation.fileNames) != 0 {
		t.Fatalf("logical failure mutation = %#v, want empty", mutation)
	}
}

func TestSuccessfulAuthFileOwnershipMutationKeepsOwnershipRevokedWithoutSuccessEvidence(t *testing.T) {
	want := authFileOwnershipMutation{fileNames: []string{"auth-a.json"}}
	for _, body := range []string{
		`not-json`,
		`{}`,
		`{"status":"partial"}`,
	} {
		t.Run(body, func(t *testing.T) {
			response := &http.Response{Body: io.NopCloser(strings.NewReader(body))}
			mutation, err := successfulAuthFileOwnershipMutation(response, want)
			if err != nil {
				t.Fatalf("resolve uncertain mutation: %v", err)
			}
			if mutation.clearAll || len(mutation.fileNames) != 1 || mutation.fileNames[0] != "auth-a.json" {
				t.Fatalf("uncertain response %q mutation = %#v, want original mutation", body, mutation)
			}
		})
	}
}

func TestSuccessfulAuthFileOwnershipMutationAcceptsCanonicalStatusResponse(t *testing.T) {
	provider := "codex"
	authIndex := "auth-1"
	accountID := "account-1"
	response := &http.Response{
		Body: io.NopCloser(strings.NewReader(`{"status":"ok","disabled":true}`)),
	}
	want := authFileOwnershipMutation{
		ownershipTargets: []model.CodexInspectionDisableOwnershipTarget{{
			FileName:  "auth-a.json",
			Provider:  &provider,
			AuthIndex: &authIndex,
			AccountID: &accountID,
		}},
	}
	mutation, err := successfulAuthFileOwnershipMutation(response, want)
	if err != nil {
		t.Fatalf("resolve canonical status mutation: %v", err)
	}
	if len(mutation.ownershipTargets) != 1 || mutation.ownershipTargets[0].FileName != "auth-a.json" {
		t.Fatalf("canonical status mutation = %#v, want target retained", mutation)
	}
}

func TestSuccessfulAuthFileOwnershipMutationDoesNotExpandCredentialTargetFromResponseFiles(t *testing.T) {
	provider := "codex"
	authIndex := "auth-1"
	accountID := "account-1"
	response := &http.Response{
		Body: io.NopCloser(strings.NewReader(`{"status":"ok","files":["shared.json"],"disabled":true}`)),
	}
	mutation, err := successfulAuthFileOwnershipMutation(response, authFileOwnershipMutation{
		ownershipTargets: []model.CodexInspectionDisableOwnershipTarget{{
			FileName:  "shared.json",
			Provider:  &provider,
			AuthIndex: &authIndex,
			AccountID: &accountID,
		}},
	})
	if err != nil {
		t.Fatalf("resolve credential status mutation: %v", err)
	}
	if len(mutation.fileNames) != 0 || len(mutation.ownershipTargets) != 1 {
		t.Fatalf("credential status mutation = %#v, want exact target only", mutation)
	}
}

func TestSuccessfulAuthFileOwnershipMutationKeepsOwnershipRevokedForEncodedResponse(t *testing.T) {
	response := &http.Response{
		Header: http.Header{"Content-Encoding": []string{"gzip"}},
		Body:   io.NopCloser(strings.NewReader("compressed")),
	}
	mutation, err := successfulAuthFileOwnershipMutation(response, authFileOwnershipMutation{
		fileNames: []string{"auth-a.json"},
	})
	if err != nil {
		t.Fatalf("resolve encoded response: %v", err)
	}
	if len(mutation.fileNames) != 1 || mutation.fileNames[0] != "auth-a.json" {
		t.Fatalf("encoded response mutation = %#v, want original mutation", mutation)
	}
}

func TestRevokeAndRestoreInspectionOwnership(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:  "auth-a.json",
		AuthIndex: "auth-1",
	}); err != nil {
		t.Fatalf("save ownership: %v", err)
	}
	if err := st.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:  "auth-b.json",
		AuthIndex: "auth-2",
	}); err != nil {
		t.Fatalf("save second ownership: %v", err)
	}
	if err := st.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName:  "auth-a.json",
		AuthIndex: "auth-3",
	}); err != nil {
		t.Fatalf("save same-file ownership: %v", err)
	}
	if err := st.UpsertCodexInspectionDisableOwnership(context.Background(), model.CodexInspectionDisableOwnership{
		FileName: "auth-a.json",
	}); err != nil {
		t.Fatalf("save legacy wildcard ownership: %v", err)
	}
	service := New(nil, st)
	revoked, err := service.revokeInspectionOwnership(context.Background(), authFileOwnershipMutation{
		fileNames: []string{"auth-a.json"},
	})
	if err != nil {
		t.Fatalf("revoke ownership: %v", err)
	}
	if len(revoked) != 3 || revoked[0].FileName != "auth-a.json" || revoked[1].FileName != "auth-a.json" || revoked[2].FileName != "auth-a.json" {
		t.Fatalf("revoked ownership = %#v", revoked)
	}
	items, err := st.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list ownership: %v", err)
	}
	if len(items) != 1 || items[0].FileName != "auth-b.json" {
		t.Fatalf("ownership = %#v, want auth-b only", items)
	}
	if err := service.restoreInspectionOwnership(context.Background(), revoked); err != nil {
		t.Fatalf("restore ownership: %v", err)
	}
	items, err = st.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list ownership after restore: %v", err)
	}
	if len(items) != 4 {
		t.Fatalf("ownership after restore = %#v, want 4 items", items)
	}
}

func TestStatusMutationRemovesExactAndCompatibleLegacyInspectionOwnership(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	items := []model.CodexInspectionDisableOwnership{
		{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-1", AccountID: "account-1"},
		{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-2", AccountID: "account-2"},
		{FileName: "shared.json"},
		{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-1"},
		{FileName: "shared.json", Provider: "codex", AccountID: "account-1"},
	}
	for _, item := range items {
		if err := st.UpsertCodexInspectionDisableOwnership(context.Background(), item); err != nil {
			t.Fatalf("save ownership %#v: %v", item, err)
		}
	}
	provider := "codex"
	authIndex := "auth-1"
	accountID := "account-1"
	mutation := authFileOwnershipMutation{
		ownershipTargets: []model.CodexInspectionDisableOwnershipTarget{{
			FileName:  "shared.json",
			Provider:  &provider,
			AuthIndex: &authIndex,
			AccountID: &accountID,
		}},
	}
	service := New(nil, st)
	revoked, err := service.revokeInspectionOwnership(context.Background(), mutation)
	if err != nil {
		t.Fatalf("revoke exact ownership: %v", err)
	}
	if len(revoked) != 4 {
		t.Fatalf("revoked ownership = %#v, want exact and compatible legacy wildcards", revoked)
	}
	if err := service.restoreInspectionOwnership(
		context.Background(),
		ownershipItemsNotMutated(revoked, mutation),
	); err != nil {
		t.Fatalf("restore non-target ownership: %v", err)
	}
	remaining, err := st.ListCodexInspectionDisableOwnership(context.Background())
	if err != nil {
		t.Fatalf("list remaining ownership: %v", err)
	}
	if len(remaining) != 1 {
		t.Fatalf("remaining ownership = %#v, want sibling only", remaining)
	}
	for _, item := range remaining {
		if item.AuthIndex == "auth-1" && item.AccountID == "account-1" {
			t.Fatalf("target ownership was restored: %#v", remaining)
		}
		if item.AuthIndex == "" || item.AccountID == "" {
			t.Fatalf("legacy wildcard ownership was restored: %#v", remaining)
		}
	}
}

func TestReadAndRestoreRequestBodyRejectsOversizedBody(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "/v0/management/auth-files", strings.NewReader("12345"))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if _, err := readAndRestoreRequestBody(req, 4); !errors.Is(err, errAuthFileMutationBodyTooLarge) {
		t.Fatalf("read oversized body error = %v", err)
	}
}

func TestOwnershipItemsNotMutatedRestoresOnlyFailedFiles(t *testing.T) {
	items := []store.CodexInspectionDisableOwnership{
		{FileName: "auth-a.json", AuthIndex: "auth-1"},
		{FileName: "auth-a.json", AuthIndex: "auth-2"},
		{FileName: "auth-a.json"},
		{FileName: "auth-b.json"},
	}
	remaining := ownershipItemsNotMutated(items, authFileOwnershipMutation{
		fileNames: []string{"auth-a.json"},
	})
	if len(remaining) != 1 || remaining[0].FileName != "auth-b.json" {
		t.Fatalf("remaining ownership = %#v", remaining)
	}
}

func TestOwnershipItemsNotMutatedKeepsSameFileSiblingsForExactTarget(t *testing.T) {
	provider := "codex"
	authIndex := "auth-1"
	accountID := "account-1"
	items := []store.CodexInspectionDisableOwnership{
		{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-1", AccountID: "account-1"},
		{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-2", AccountID: "account-2"},
		{FileName: "shared.json"},
		{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-1"},
		{FileName: "shared.json", Provider: "codex", AccountID: "account-1"},
	}
	remaining := ownershipItemsNotMutated(items, authFileOwnershipMutation{
		ownershipTargets: []model.CodexInspectionDisableOwnershipTarget{{
			FileName:  "shared.json",
			Provider:  &provider,
			AuthIndex: &authIndex,
			AccountID: &accountID,
		}},
	})
	if len(remaining) != 1 || remaining[0].AuthIndex != "auth-2" {
		t.Fatalf("remaining ownership = %#v, want sibling only", remaining)
	}
}

func TestOwnershipItemsNotMutatedKeepsSnapshotFallbackSibling(t *testing.T) {
	provider := "codex"
	authIndex := ""
	accountID := ""
	accountSnapshot := "alice@example.com"
	items := []store.CodexInspectionDisableOwnership{
		{FileName: "shared.json", Provider: "codex", AccountSnapshot: "alice@example.com"},
		{FileName: "shared.json", Provider: "codex", AccountSnapshot: "bob@example.com"},
		{FileName: "shared.json", Provider: "codex"},
	}
	remaining := ownershipItemsNotMutated(items, authFileOwnershipMutation{
		ownershipTargets: []model.CodexInspectionDisableOwnershipTarget{{
			FileName:        "shared.json",
			Provider:        &provider,
			AuthIndex:       &authIndex,
			AccountID:       &accountID,
			AccountSnapshot: &accountSnapshot,
		}},
	})
	if len(remaining) != 1 || remaining[0].AccountSnapshot != "bob@example.com" {
		t.Fatalf("remaining ownership = %#v, want bob only", remaining)
	}
}

func TestOwnershipItemsNotMutatedKeepsDifferentSnapshotWhenTargetHasAccountID(t *testing.T) {
	provider := "codex"
	authIndex := "auth-1"
	accountID := "account-alice"
	accountSnapshot := "alice@example.com"
	items := []store.CodexInspectionDisableOwnership{
		{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-1", AccountSnapshot: "alice@example.com"},
		{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-1", AccountSnapshot: "bob@example.com"},
		{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-1"},
	}
	remaining := ownershipItemsNotMutated(items, authFileOwnershipMutation{
		ownershipTargets: []model.CodexInspectionDisableOwnershipTarget{{
			FileName:        "shared.json",
			Provider:        &provider,
			AuthIndex:       &authIndex,
			AccountID:       &accountID,
			AccountSnapshot: &accountSnapshot,
		}},
	})
	if len(remaining) != 1 || remaining[0].AccountSnapshot != "bob@example.com" {
		t.Fatalf("remaining ownership = %#v, want different snapshot only", remaining)
	}
}

func TestOwnershipItemsNotMutatedTreatsEmptyProviderAsLegacyWildcard(t *testing.T) {
	provider := "xai"
	authIndex := "auth-1"
	accountID := "account-1"
	items := []store.CodexInspectionDisableOwnership{
		{FileName: "shared.json", Provider: "xai", AuthIndex: "auth-1", AccountID: "account-1"},
		{FileName: "shared.json", AuthIndex: "auth-1", AccountID: "account-1"},
		{FileName: "shared.json", Provider: "codex", AuthIndex: "auth-1", AccountID: "account-1"},
	}
	remaining := ownershipItemsNotMutated(items, authFileOwnershipMutation{
		ownershipTargets: []model.CodexInspectionDisableOwnershipTarget{{
			FileName:  "shared.json",
			Provider:  &provider,
			AuthIndex: &authIndex,
			AccountID: &accountID,
		}},
	})
	if len(remaining) != 1 || remaining[0].Provider != "codex" {
		t.Fatalf("remaining ownership = %#v, want incompatible provider only", remaining)
	}
}

func TestIsManagementPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/v0/management", want: true},
		{path: "/v0/management/", want: true},
		{path: "/v0/management/auth-files", want: true},
		{path: "/v0/management/auth-files/status", want: true},
		{path: "/v0/management/api-call", want: true},
		{path: "/v0/management/api-key-usage", want: true},
		{path: "/v0/resource/plugins", want: true},
		{path: "/v0/resource/plugins/codex-invite/invite", want: true},
		{path: "/v0/resource/plugin", want: false},
		{path: "/v0/resource/plugin-store", want: false},
		{path: "/v1/models", want: false},
		{path: "/models", want: false},
		{path: "/auth-files", want: false},
		{path: "/api-call", want: false},
		{path: "/", want: false},
		{path: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isManagementPath(tt.path); got != tt.want {
				t.Fatalf("isManagementPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsModelListPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/v1/models", want: true},
		{path: "/v1/models/", want: true},
		{path: "/models", want: true},
		{path: "/models/", want: true},
		{path: "/v1/chat/completions", want: false},
		{path: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := isModelListPath(tt.path); got != tt.want {
				t.Fatalf("isModelListPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsCPAPluginManagementPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/v0/management/codex-invite/accounts", want: true},
		{path: "/v0/management/sample-plugin/custom/action", want: true},
		{path: "/v0/management/accounts", want: false},
		{path: "/v0/management/accounts/", want: false},
		{path: "/v0/management/config", want: false},
		{path: "/v0/management/reload", want: false},
		{path: "/v0/management/plugins/demo/custom", want: false},
		{path: "/v0/management/plugin-store/demo/install", want: false},
		{path: "/v0/management/usage", want: false},
		{path: "/v0/resource/plugins/codex-invite/invite", want: false},
		{path: "/v0/management", want: false},
		{path: "/v0/management/", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := IsCPAPluginManagementPath(tt.path); got != tt.want {
				t.Fatalf("IsCPAPluginManagementPath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestIsCPAPluginResourcePath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/v0/resource/plugins", want: true},
		{path: "/v0/resource/plugins/", want: true},
		{path: "/v0/resource/plugins/codex-invite/invite", want: true},
		{path: "/v0/resource/plugins/codex-invite/assets/app.js", want: true},
		{path: "/v0/resource/plugin", want: false},
		{path: "/v0/resource/plugin-store", want: false},
		{path: "/plugins/codex-invite/invite", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := IsCPAPluginResourcePath(tt.path); got != tt.want {
				t.Fatalf("IsCPAPluginResourcePath(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

func TestRewriteCodexInviteOrigin(t *testing.T) {
	target, err := url.Parse("http://cpa.local:8317/base")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	header := http.Header{}
	header.Set(codexInviteOriginHeader, "http://manager.local:18317")
	header.Set("Origin", "http://manager.local:18317")

	rewriteCodexInviteOrigin(header, target)

	if got := header.Get(codexInviteOriginHeader); got != "http://cpa.local:8317" {
		t.Fatalf("%s = %q", codexInviteOriginHeader, got)
	}
	if got := header.Get("Origin"); got != "http://manager.local:18317" {
		t.Fatalf("Origin = %q", got)
	}

	emptyHeader := http.Header{}
	rewriteCodexInviteOrigin(emptyHeader, target)
	if got := emptyHeader.Get(codexInviteOriginHeader); got != "" {
		t.Fatalf("empty %s = %q", codexInviteOriginHeader, got)
	}
}

func TestRewritePluginManagementOriginBody(t *testing.T) {
	target, err := url.Parse("http://cpa.local:8317")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	req, err := http.NewRequest(
		http.MethodPost,
		"/v0/management/codex-invite/invite",
		strings.NewReader(`{"management_origin":"http://manager.local:18317","refresh":true}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if err := rewritePluginManagementOriginBody(req, target); err != nil {
		t.Fatalf("rewritePluginManagementOriginBody() error = %v", err)
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	want := `{"management_origin":"http://cpa.local:8317","refresh":true}`
	if string(raw) != want {
		t.Fatalf("body = %q, want %q", raw, want)
	}
	if req.ContentLength != int64(len(want)) {
		t.Fatalf("content length = %d, want %d", req.ContentLength, len(want))
	}
}

func TestRewritePluginManagementOriginBodyLeavesOtherBodies(t *testing.T) {
	target, err := url.Parse("http://cpa.local:8317")
	if err != nil {
		t.Fatalf("parse target: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, "/v0/resource/plugins/demo", strings.NewReader(`{"refresh":true}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if err := rewritePluginManagementOriginBody(req, target); err != nil {
		t.Fatalf("rewritePluginManagementOriginBody() error = %v", err)
	}
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(raw) != `{"refresh":true}` {
		t.Fatalf("body = %q", raw)
	}
}
