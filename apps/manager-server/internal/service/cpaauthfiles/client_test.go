package cpaauthfiles

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestParseAndVerifyIdentity(t *testing.T) {
	files, err := Parse([]byte(`{"auth_files":[{"id":"runtime-codex","name":"codex-auth.json","auth_index":"7","provider":"codex","account":"user@example.com","account_id":"acct-123","disabled":"true"}]}`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("files = %#v", files)
	}
	file, err := VerifyIdentity(files, Identity{
		AuthFileName:      "codex-auth.json",
		AuthIndex:         "7",
		Provider:          "CODEX",
		AccountSnapshot:   "user@example.com",
		AccountIDSnapshot: "acct-123",
	})
	if err != nil {
		t.Fatalf("verify identity: %v", err)
	}
	if file.ID != "runtime-codex" || !file.Disabled || file.AuthIndex != "7" || file.Provider != "codex" {
		t.Fatalf("file = %#v", file)
	}
	if _, err := VerifyIdentity(files, Identity{AuthFileName: "missing.json", AuthIndex: "7"}); !errors.Is(err, ErrAuthFileNotFound) {
		t.Fatalf("not found err = %v", err)
	}
	if _, err := VerifyIdentity(files, Identity{AuthFileName: "codex-auth.json", AuthIndex: "7", AccountIDSnapshot: "acct-456"}); !errors.Is(err, ErrIdentityMismatch) || !strings.Contains(err.Error(), "account_id mismatch") {
		t.Fatalf("account id mismatch err = %v", err)
	}
	if _, err := VerifyIdentity(files, Identity{AuthFileName: "codex-auth.json", AuthIndex: "7", Provider: "gemini"}); !errors.Is(err, ErrIdentityMismatch) || !strings.Contains(err.Error(), "provider mismatch") {
		t.Fatalf("provider mismatch err = %v", err)
	}
	if _, err := VerifyIdentity(files, Identity{AuthFileName: "codex-auth.json", AuthIndex: "7", AccountSnapshot: "other@example.com"}); !errors.Is(err, ErrIdentityMismatch) || !strings.Contains(err.Error(), "account_snapshot mismatch") {
		t.Fatalf("account snapshot mismatch err = %v", err)
	}
	if _, err := VerifyIdentity(files, Identity{
		AuthFileName:      "codex-auth.json",
		AuthIndex:         "7",
		Provider:          "codex",
		AccountIDSnapshot: "acct-123",
		AccountSnapshot:   "renamed@example.com",
	}); !errors.Is(err, ErrIdentityMismatch) || !strings.Contains(err.Error(), "account_snapshot mismatch") {
		t.Fatalf("Codex member mismatch err = %v, want ErrIdentityMismatch", err)
	}
	if _, err := VerifyIdentity(files, Identity{
		AuthFileName:      "codex-auth.json",
		AuthIndex:         "7",
		Provider:          "codex",
		AccountIDSnapshot: "acct-123",
		AccountSnapshot:   "USER@EXAMPLE.COM",
	}); err != nil {
		t.Fatalf("normalized Codex member should verify: %v", err)
	}
}

func TestVerifyIdentityKeepsDuplicateCodexSelectorAmbiguous(t *testing.T) {
	files := []File{
		FromMap(map[string]any{
			"id":         "runtime-alice",
			"name":       "team.json",
			"auth_index": "auth-1",
			"provider":   "codex",
			"account_id": "workspace-1",
			"account":    "alice@example.com",
		}),
		FromMap(map[string]any{
			"id":         "runtime-bob",
			"name":       "team.json",
			"auth_index": "auth-1",
			"provider":   "codex",
			"account_id": "workspace-1",
			"account":    "bob@example.com",
		}),
	}

	if _, err := VerifyIdentity(files, Identity{
		AuthFileName:      "team.json",
		AuthIndex:         "auth-1",
		Provider:          "codex",
		AccountIDSnapshot: "workspace-1",
		AccountSnapshot:   "alice@example.com",
	}); !errors.Is(err, ErrAuthFileAmbiguous) {
		t.Fatalf("duplicate selector verification error = %v, want ErrAuthFileAmbiguous", err)
	}
	if found, ok := Find(files, "team.json", "auth-1"); !ok || found.ID != "runtime-alice" {
		t.Fatalf("legacy Find semantics = %#v, %t; want first match", found, ok)
	}
}

func TestVerifyIdentityKeepsDuplicateSelectorAmbiguousWithoutCompleteCodexEvidence(t *testing.T) {
	files := []File{
		FromMap(map[string]any{
			"id":         "runtime-alice",
			"name":       "team.json",
			"auth_index": "auth-1",
			"provider":   "codex",
			"account_id": "workspace-1",
			"account":    "alice@example.com",
		}),
		FromMap(map[string]any{
			"id":         "runtime-unknown",
			"name":       "team.json",
			"auth_index": "auth-1",
			"provider":   "codex",
			"account_id": "workspace-1",
		}),
	}

	if _, err := VerifyIdentity(files, Identity{
		AuthFileName:      "team.json",
		AuthIndex:         "auth-1",
		Provider:          "codex",
		AccountIDSnapshot: "workspace-1",
		AccountSnapshot:   "alice@example.com",
	}); !errors.Is(err, ErrAuthFileAmbiguous) {
		t.Fatalf("incomplete duplicate selector verification error = %v, want ErrAuthFileAmbiguous", err)
	}
}

func TestVerifyIdentityPreservesNonCodexFirstMatch(t *testing.T) {
	files := []File{
		FromMap(map[string]any{
			"id":         "runtime-first",
			"name":       "shared.json",
			"auth_index": "auth-1",
			"provider":   "gemini",
			"account":    "first@example.com",
		}),
		FromMap(map[string]any{
			"id":         "runtime-second",
			"name":       "shared.json",
			"auth_index": "auth-1",
			"provider":   "gemini",
			"account":    "second@example.com",
		}),
	}

	file, err := VerifyIdentity(files, Identity{
		AuthFileName: "shared.json",
		AuthIndex:    "auth-1",
		Provider:     "gemini",
	})
	if err != nil || file.ID != "runtime-first" {
		t.Fatalf("non-Codex duplicate selector verification = %#v, %v; want first match", file, err)
	}
}

func TestClientVerifyKeepsDuplicateCodexSelectorAmbiguousWithoutChangingLegacyFindSemantics(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("name"); got != "team.json" {
			t.Fatalf("name query = %q", got)
		}
		if got := r.URL.Query().Get("auth_index"); got != "auth-1" {
			t.Fatalf("auth_index query = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"files": []map[string]any{
				{"id": "runtime-alice", "name": "team.json", "auth_index": "auth-1", "provider": "codex", "account_id": "workspace-1", "account": "alice@example.com"},
				{"id": "runtime-bob", "name": "team.json", "auth_index": "auth-1", "provider": "codex", "account_id": "workspace-1", "account": "bob@example.com"},
			},
		})
	}))
	defer server.Close()

	client := New(server.Client())
	identity := Identity{
		AuthFileName:      "team.json",
		AuthIndex:         "auth-1",
		Provider:          "codex",
		AccountIDSnapshot: "workspace-1",
		AccountSnapshot:   "alice@example.com",
	}
	if found, ok, err := client.Find(context.Background(), server.URL, "mgmt", identity.AuthFileName, identity.AuthIndex); err != nil || !ok || found.ID != "runtime-alice" {
		t.Fatalf("Client.Find() = %#v, ok:%t err:%v; want legacy first match", found, ok, err)
	}
	file, err := client.Verify(context.Background(), server.URL, "mgmt", identity)
	if !errors.Is(err, ErrAuthFileAmbiguous) {
		t.Fatalf("Client.Verify() = %#v, %v; want ErrAuthFileAmbiguous", file, err)
	}
}

func TestClientDownloadReturnsExactAuthFileContent(t *testing.T) {
	want := []byte("  {\n  \"type\": \"codex\"\n}\n")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != authFilesDownloadPath {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("name"); got != "shared auth.json" {
			t.Fatalf("download name = %q", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer mgmt" {
			t.Fatalf("authorization = %q", got)
		}
		_, _ = w.Write(want)
	}))
	defer server.Close()

	got, err := New(server.Client()).Download(
		context.Background(),
		server.URL,
		"mgmt",
		" shared auth.json ",
	)
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("Download() = %q, want %q", got, want)
	}
}

func TestClientDownloadRejectsOversizedAuthFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", 129))
	}))
	defer server.Close()

	client := New(server.Client())
	client.maxResponseBytes = 128
	_, err := client.Download(context.Background(), server.URL, "mgmt", "auth.json")
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("Download() error = %v, want ErrResponseTooLarge", err)
	}
}

func TestFromMapDoesNotTreatDisplayLabelAsAccountIdentity(t *testing.T) {
	file := FromMap(map[string]any{
		"id":       "runtime-1",
		"name":     "shared.json",
		"provider": "codex",
		"label":    "Friendly account",
	})
	if file.AccountSnapshot != "" {
		t.Fatalf("account snapshot = %q, want empty for display-only label", file.AccountSnapshot)
	}
	if err := VerifyResolvedIdentity(file, Identity{
		AuthFileName:    "shared.json",
		Provider:        "codex",
		AccountSnapshot: "Friendly account",
	}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("label identity verification error = %v, want mismatch", err)
	}
}

func TestFromMapKeepsRuntimeIDSeparateFromAccountID(t *testing.T) {
	file := FromMap(map[string]any{
		"id":         "runtime-auth-id",
		"name":       "codex-auth.json",
		"auth_index": "auth-1",
	})
	if file.ID != "runtime-auth-id" || file.AccountID != "" {
		t.Fatalf("file = %#v", file)
	}
}

func TestVerifyIdentityNormalizesProviderAliases(t *testing.T) {
	file := FromMap(map[string]any{
		"id":         "runtime-xai",
		"name":       "xai-auth.json",
		"auth_index": "auth-xai-1",
		"provider":   "x_ai",
		"account":    "user@example.com",
	})
	if file.Provider != "xai" {
		t.Fatalf("provider = %q, want xai", file.Provider)
	}
	if err := verifyFileIdentity(file, Identity{
		Provider:        "grok",
		AccountSnapshot: "user@example.com",
	}); err != nil {
		t.Fatalf("verify provider alias: %v", err)
	}
}

func TestFromMapExtractsNestedAccountIDs(t *testing.T) {
	tests := []struct {
		name string
		file map[string]any
		want string
	}{
		{
			name: "top level account id",
			file: map[string]any{"id": "runtime-1", "account_id": "acct-top"},
			want: "acct-top",
		},
		{
			name: "decoded id token claims",
			file: map[string]any{
				"id":       "runtime-2",
				"id_token": map[string]any{"chatgpt_account_id": "acct-token"},
			},
			want: "acct-token",
		},
		{
			name: "metadata account id",
			file: map[string]any{
				"id":       "runtime-3",
				"metadata": map[string]any{"accountId": "acct-metadata"},
			},
			want: "acct-metadata",
		},
		{
			name: "attributes nested token subject",
			file: map[string]any{
				"id": "runtime-4",
				"attributes": map[string]any{
					"id_token": map[string]any{"sub": "acct-subject"},
				},
			},
			want: "acct-subject",
		},
		{
			name: "project id",
			file: map[string]any{"id": "runtime-5", "project_id": "project-1"},
			want: "project-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FromMap(tt.file).AccountID; got != tt.want {
				t.Fatalf("AccountID = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFromMapDoesNotDecodeJWTClaimsForNonCodexProviders(t *testing.T) {
	file := FromMap(map[string]any{
		"id":       "runtime-jwt-claude",
		"provider": "claude",
		"id_token": "header.eyJzdWIiOiJhY2N0LWp3dCJ9.signature",
	})
	if file.AccountID != "" {
		t.Fatalf("non-Codex JWT AccountID = %q, want empty", file.AccountID)
	}
}

func TestFromMapRejectsConflictingCodexAccountIDs(t *testing.T) {
	file := FromMap(map[string]any{
		"id":         "runtime-conflict",
		"provider":   "codex",
		"account_id": "workspace-a",
		"metadata": map[string]any{
			"id_token": map[string]any{"chatgpt_account_id": "workspace-b"},
		},
	})
	if file.AccountID != "" || !file.AccountIDInvalid {
		t.Fatalf("conflicting account identity = %#v, want empty invalid evidence", file)
	}
	if err := VerifyResolvedIdentity(file, Identity{
		Provider:          "codex",
		AccountIDSnapshot: "workspace-a",
	}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("conflicting account identity verification error = %v, want mismatch", err)
	}
}

func TestFromMapRejectsConflictingCodexMemberSnapshots(t *testing.T) {
	file := FromMap(map[string]any{
		"id":               "runtime-member-conflict",
		"provider":         "codex",
		"account_id":       "workspace-a",
		"account_snapshot": "bob@example.com",
		"accountSnapshot":  "alice@example.com",
		"account":          "stale@example.com",
	})
	if file.AccountSnapshot != "" || !file.AccountSnapshotInvalid {
		t.Fatalf("conflicting member identity = %#v, want empty invalid evidence", file)
	}
	if err := VerifyResolvedIdentity(file, Identity{
		Provider:          "codex",
		AccountIDSnapshot: "workspace-a",
		AccountSnapshot:   "alice@example.com",
	}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("conflicting member identity verification error = %v, want mismatch", err)
	}
}

func TestFromMapRejectsStrongCodexMemberSnapshotConflictingWithDisplayEmail(t *testing.T) {
	for _, field := range []string{"account", "email"} {
		t.Run(field, func(t *testing.T) {
			file := FromMap(map[string]any{
				"id":               "runtime-member-display-conflict",
				"provider":         "codex",
				"account_id":       "workspace-a",
				"account_snapshot": "alice@example.com",
				field:              "bob@example.com",
			})
			if file.AccountSnapshot != "" || !file.AccountSnapshotInvalid {
				t.Fatalf("conflicting member identity = %#v, want empty invalid evidence", file)
			}
		})
	}
}

func TestFromMapIgnoresWeakExplicitCodexMemberSnapshot(t *testing.T) {
	file := FromMap(map[string]any{
		"id":               "runtime-member-invalid",
		"provider":         "codex",
		"auth_index":       "auth-1",
		"account_id":       "workspace-a",
		"account_snapshot": "Alice",
		"email":            "alice@example.com",
	})
	if file.AccountSnapshot != "alice@example.com" || file.AccountSnapshotInvalid {
		t.Fatalf("weak member identity = %#v, want strong email without invalid evidence", file)
	}
	if err := VerifyResolvedIdentity(file, Identity{
		AuthIndex:         "auth-1",
		Provider:          "codex",
		AccountIDSnapshot: "workspace-a",
		AccountSnapshot:   "alice@example.com",
	}); err != nil {
		t.Fatalf("weak display value must not block credential verification: %v", err)
	}
}

func TestFromMapDoesNotTrimUnicodeWhitespaceBeforeCodexMemberNormalization(t *testing.T) {
	for _, field := range []string{"account_snapshot", "accountSnapshot", "account", "email"} {
		t.Run(field, func(t *testing.T) {
			file := FromMap(map[string]any{
				"id":       "runtime-unicode-member",
				"provider": "codex",
				field:      "\u2003alice@example.com",
			})
			if file.AccountSnapshot != "" || file.AccountSnapshotInvalid {
				t.Fatalf("Unicode-whitespace member field = %#v, want no strong member evidence", file)
			}
		})
	}
}

func TestVerifyResolvedIdentityIgnoresWeakCodexMemberWithCredentialLocator(t *testing.T) {
	file := FromMap(map[string]any{
		"id":         "runtime-member-locator",
		"name":       "codex-auth.json",
		"auth_index": "auth-1",
		"provider":   "codex",
		"account_id": "workspace-a",
		"account":    "alice@example.com",
	})

	for _, identity := range []Identity{
		{
			AuthFileName:      "codex-auth.json",
			AuthIndex:         "auth-1",
			Provider:          "codex",
			AccountIDSnapshot: "workspace-a",
			AccountSnapshot:   "Alice",
		},
		{
			AuthFileName:    "codex-auth.json",
			RuntimeID:       "runtime-member-locator",
			Provider:        "codex",
			AccountSnapshot: "Alice",
		},
	} {
		if err := VerifyResolvedIdentity(file, identity); err != nil {
			t.Fatalf("weak member with credential locator rejected: identity=%#v err=%v", identity, err)
		}
	}

	if err := VerifyResolvedIdentity(file, Identity{
		AuthFileName:      "codex-auth.json",
		Provider:          "codex",
		AccountIDSnapshot: "workspace-a",
		AccountSnapshot:   "Alice",
	}); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("weak member without credential locator error = %v, want ErrIdentityMismatch", err)
	}
}

func TestVerifyResolvedIdentityRejectsProviderMismatchBeforeWeakCodexMemberReturn(t *testing.T) {
	file := FromMap(map[string]any{
		"id":         "runtime-provider-mismatch",
		"name":       "codex-auth.json",
		"auth_index": "auth-1",
		"provider":   "codex",
		"account_id": "workspace-a",
		"account":    "Alice",
	})

	err := VerifyResolvedIdentity(file, Identity{
		AuthFileName:      "codex-auth.json",
		AuthIndex:         "auth-1",
		Provider:          "gemini",
		AccountIDSnapshot: "workspace-a",
		AccountSnapshot:   "Alice",
	})
	if !errors.Is(err, ErrIdentityMismatch) || !strings.Contains(err.Error(), "provider mismatch") {
		t.Fatalf("provider mismatch error = %v, want ErrIdentityMismatch/provider mismatch", err)
	}
}

func TestClientPatchDisabledAndDelete(t *testing.T) {
	var patched bool
	var deleted bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mgmt" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "runtime-codex", "name": "codex-auth.json", "auth_index": "7"}})
		case "PATCH /v0/management/auth-files/status":
			var payload struct {
				Name      string `json:"name"`
				AuthIndex string `json:"auth_index"`
				Disabled  bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if payload.Name != "runtime-codex" || payload.AuthIndex != "7" || !payload.Disabled {
				t.Fatalf("patch payload = %#v", payload)
			}
			patched = true
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "DELETE /v0/management/auth-files":
			if r.URL.Query().Get("name") != "codex-auth.json" {
				t.Fatalf("delete query = %s", r.URL.RawQuery)
			}
			deleted = true
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(server.Client())
	files, err := client.Fetch(context.Background(), server.URL, "mgmt")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if _, ok := Find(files, "codex-auth.json", "7"); !ok {
		t.Fatalf("find files = %#v", files)
	}
	if err := client.PatchDisabled(context.Background(), server.URL, "mgmt", "codex-auth.json", true, "7"); err != nil {
		t.Fatalf("patch disabled: %v", err)
	}
	if err := client.Delete(context.Background(), server.URL, "mgmt", "codex-auth.json"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if !patched || !deleted {
		t.Fatalf("patched=%t deleted=%t", patched, deleted)
	}
}

func TestClientPatchDisabledTargetsSameNameCredentialByRuntimeID(t *testing.T) {
	patchCalls := 0
	getCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			getCalls++
			if r.URL.Query().Get("name") != "shared.json" || r.URL.Query().Get("auth_index") != "" {
				t.Fatalf("status preflight query = %q, want selector-filtered lookup", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "runtime-1", "name": "shared.json", "auth_index": "auth-1"},
				{"id": "runtime-2", "name": "shared.json", "auth_index": "auth-2"},
				// Older CPA versions ignore the selector query. The local scan must
				// remain compatible without issuing another full-list request.
				{"id": "unrelated", "name": "unrelated.json", "auth_index": "auth-3"},
			})
		case "PATCH /v0/management/auth-files/status":
			patchCalls++
			var payload struct {
				Name      string `json:"name"`
				AuthIndex string `json:"auth_index"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			if payload.Name != "runtime-2" || payload.AuthIndex != "auth-2" {
				t.Fatalf("patch payload = %#v", payload)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if err := New(server.Client()).PatchDisabled(context.Background(), server.URL, "mgmt", "shared.json", true, "auth-2"); err != nil {
		t.Fatalf("PatchDisabled() error = %v", err)
	}
	if patchCalls != 1 {
		t.Fatalf("patch calls = %d, want 1", patchCalls)
	}
	if getCalls != 1 {
		t.Fatalf("GET calls = %d, want 1 for old-CPA full-list compatibility", getCalls)
	}
}

func TestClientPatchDisabledTargetKeepsVerifiedRuntimeIdentity(t *testing.T) {
	getCalls := 0
	patchedName := ""
	replaced := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			getCalls++
			runtimeID := "runtime-original"
			accountID := "account-original"
			if replaced {
				runtimeID = "runtime-replacement"
				accountID = "account-replacement"
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         runtimeID,
				"name":       "shared.json",
				"auth_index": "auth-1",
				"provider":   "codex",
				"account_id": accountID,
			}})
		case "PATCH /v0/management/auth-files/status":
			var payload struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			patchedName = payload.Name
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(server.Client())
	target, err := client.ResolveVerifiedStatusMutationTarget(context.Background(), server.URL, "mgmt", Identity{
		AuthFileName:      "shared.json",
		AuthIndex:         "auth-1",
		Provider:          "codex",
		AccountIDSnapshot: "account-original",
	})
	if err != nil {
		t.Fatalf("resolve verified target: %v", err)
	}
	replaced = true
	if err := client.PatchDisabledTarget(context.Background(), server.URL, "mgmt", target, true); err != nil {
		t.Fatalf("patch verified target: %v", err)
	}
	if getCalls != 1 || patchedName != "runtime-original" {
		t.Fatalf("getCalls=%d patchedName=%q, want one read and original runtime id", getCalls, patchedName)
	}
}

func TestClientResolveVerifiedStatusMutationTargetRejectsWorkspaceMemberWithoutCredentialLocator(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v0/management/auth-files" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":         "runtime-first",
				"name":       "shared.json",
				"account_id": "workspace-1",
				"provider":   "codex",
				"account":    "first@example.com",
			},
			{
				"id":         "runtime-second",
				"name":       "shared.json",
				"account_id": "workspace-1",
				"provider":   "codex",
				"account":    "second@example.com",
			},
		})
	}))
	defer server.Close()

	_, err := New(server.Client()).ResolveVerifiedStatusMutationTarget(
		context.Background(),
		server.URL,
		"mgmt",
		Identity{
			AuthFileName:      "shared.json",
			Provider:          "codex",
			AccountIDSnapshot: "workspace-1",
			AccountSnapshot:   "second@example.com",
		},
	)
	if !errors.Is(err, ErrStatusMutationScopeAmbiguous) {
		t.Fatalf("resolve verified target by workspace+member error = %v, want ErrStatusMutationScopeAmbiguous", err)
	}
}

func TestClientResolveVerifiedStatusMutationTargetAllowsUniqueCredentialWithoutMember(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v0/management/auth-files" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":         "runtime-alice",
			"name":       "team.json",
			"auth_index": "auth-1",
			"provider":   "codex",
			"account_id": "workspace-1",
		}})
	}))
	defer server.Close()

	target, err := New(server.Client()).ResolveVerifiedStatusMutationTarget(
		context.Background(),
		server.URL,
		"mgmt",
		Identity{
			AuthFileName:      "team.json",
			AuthIndex:         "auth-1",
			Provider:          "codex",
			AccountIDSnapshot: "workspace-1",
		},
	)
	if err != nil {
		t.Fatalf("resolve verified target without member: %v", err)
	}
	if target.Selector != "runtime-alice" || target.Scope != StatusMutationScopeCredential {
		t.Fatalf("target = %#v, want unique credential runtime target", target)
	}
}

func TestClientDeleteVerifiedSingleCredentialUsesRuntimeID(t *testing.T) {
	deletedSelector := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-original",
				"name":       "auth.json",
				"auth_index": "auth-1",
				"provider":   "codex",
				"account_id": "account-original",
			}})
		case "DELETE /v0/management/auth-files":
			deletedSelector = r.URL.Query().Get("name")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := New(server.Client()).DeleteVerifiedSingleCredential(context.Background(), server.URL, "mgmt", Identity{
		AuthFileName:      "auth.json",
		AuthIndex:         "auth-1",
		Provider:          "codex",
		AccountIDSnapshot: "account-original",
	})
	if err != nil {
		t.Fatalf("delete verified credential: %v", err)
	}
	if deletedSelector != "runtime-original" {
		t.Fatalf("delete selector = %q, want runtime-original", deletedSelector)
	}
}

func TestClientDeleteVerifiedSinglePluginCredentialFallsBackToPhysicalSource(t *testing.T) {
	deletedSelectors := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id":         "runtime-virtual",
				"name":       "source.json",
				"auth_index": "auth-1",
				"provider":   "gemini-cli",
				"account":    "project@example.com",
			}})
		case "DELETE /v0/management/auth-files":
			selector := r.URL.Query().Get("name")
			deletedSelectors = append(deletedSelectors, selector)
			if selector == "runtime-virtual" {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": cpaPluginVirtualMutationConflict})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := New(server.Client()).DeleteVerifiedSingleCredential(context.Background(), server.URL, "mgmt", Identity{
		AuthFileName:    "source.json",
		AuthIndex:       "auth-1",
		Provider:        "gemini-cli",
		AccountSnapshot: "project@example.com",
	})
	if err != nil {
		t.Fatalf("delete verified plugin credential: %v", err)
	}
	if !reflect.DeepEqual(deletedSelectors, []string{"runtime-virtual", "source.json"}) {
		t.Fatalf("delete selectors = %#v, want runtime then physical source", deletedSelectors)
	}
}

func TestClientDeleteVerifiedSinglePluginCredentialRejectsMembershipGrowthBeforeFallback(t *testing.T) {
	getCalls := 0
	deletedSelectors := make([]string, 0, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			getCalls++
			files := []map[string]any{{
				"id": "runtime-virtual", "name": "source.json", "auth_index": "auth-1", "provider": "gemini-cli", "account": "project@example.com",
			}}
			if getCalls > 1 {
				files = append(files, map[string]any{
					"id": "runtime-new", "name": "source.json", "auth_index": "auth-2", "provider": "gemini-cli", "account": "new@example.com",
				})
			}
			_ = json.NewEncoder(w).Encode(files)
		case "DELETE /v0/management/auth-files":
			selector := r.URL.Query().Get("name")
			deletedSelectors = append(deletedSelectors, selector)
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": cpaPluginVirtualMutationConflict})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := New(server.Client()).DeleteVerifiedSingleCredential(context.Background(), server.URL, "mgmt", Identity{
		AuthFileName:    "source.json",
		AuthIndex:       "auth-1",
		Provider:        "gemini-cli",
		AccountSnapshot: "project@example.com",
	})
	if !errors.Is(err, ErrDeleteMutationScopeAmbiguous) {
		t.Fatalf("delete error = %v, want ErrDeleteMutationScopeAmbiguous", err)
	}
	if !reflect.DeepEqual(deletedSelectors, []string{"runtime-virtual"}) {
		t.Fatalf("delete selectors = %#v, want no physical fallback", deletedSelectors)
	}
}

func TestClientDeleteVerifiedSingleCredentialRejectsSharedPhysicalFile(t *testing.T) {
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "source.json", "name": "source.json", "auth_index": "auth-1", "provider": "codex", "account_id": "account-1"},
				{"id": "runtime-child", "name": "source.json", "auth_index": "auth-2", "provider": "codex", "account_id": "account-2"},
			})
		case "DELETE /v0/management/auth-files":
			deleteCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := New(server.Client()).DeleteVerifiedSingleCredential(context.Background(), server.URL, "mgmt", Identity{
		AuthFileName:      "source.json",
		AuthIndex:         "auth-1",
		Provider:          "codex",
		AccountIDSnapshot: "account-1",
	})
	if !errors.Is(err, ErrDeleteMutationScopeAmbiguous) {
		t.Fatalf("delete shared credential error = %v, want ErrDeleteMutationScopeAmbiguous", err)
	}
	if deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want 0", deleteCalls)
	}
}

func TestClientDeleteVerifiedPhysicalFileChecksEveryCredential(t *testing.T) {
	deletedSelector := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "source.json", "name": "source.json", "auth_index": "auth-1", "provider": "codex", "account_id": "account-1"},
				{"id": "runtime-child", "name": "source.json", "auth_index": "auth-2", "provider": "codex", "account_id": "account-2"},
			})
		case "DELETE /v0/management/auth-files":
			deletedSelector = r.URL.Query().Get("name")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := New(server.Client()).DeleteVerifiedPhysicalFile(context.Background(), server.URL, "mgmt", []Identity{
		{AuthFileName: "source.json", AuthIndex: "auth-1", Provider: "codex", AccountIDSnapshot: "account-1"},
		{AuthFileName: "source.json", AuthIndex: "auth-2", Provider: "codex", AccountIDSnapshot: "account-2"},
	})
	if err != nil {
		t.Fatalf("delete verified physical file: %v", err)
	}
	if deletedSelector != "source.json" {
		t.Fatalf("delete selector = %q, want source.json runtime id", deletedSelector)
	}
}

func TestClientDeleteVerifiedPhysicalFileUsesSourceNameWithoutSourceRuntimeRow(t *testing.T) {
	deletedSelector := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "runtime-first", "name": "source.json", "auth_index": "auth-1", "provider": "codex", "account_id": "account-1"},
				{"id": "runtime-second", "name": "source.json", "auth_index": "auth-2", "provider": "codex", "account_id": "account-2"},
			})
		case "DELETE /v0/management/auth-files":
			deletedSelector = r.URL.Query().Get("name")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := New(server.Client()).DeleteVerifiedPhysicalFile(context.Background(), server.URL, "mgmt", []Identity{
		{AuthFileName: "source.json", AuthIndex: "auth-1", Provider: "codex", AccountIDSnapshot: "account-1"},
		{AuthFileName: "source.json", AuthIndex: "auth-2", Provider: "codex", AccountIDSnapshot: "account-2"},
	})
	if err != nil {
		t.Fatalf("delete verified physical file without source runtime row: %v", err)
	}
	if deletedSelector != "source.json" {
		t.Fatalf("delete selector = %q, want physical source name", deletedSelector)
	}
}

func TestClientDeleteVerifiedPhysicalFileRejectsPhysicalNameRuntimeCollision(t *testing.T) {
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "runtime-first", "name": "source.json", "auth_index": "auth-1", "provider": "codex", "account_id": "account-1"},
				{"id": "runtime-second", "name": "source.json", "auth_index": "auth-2", "provider": "codex", "account_id": "account-2"},
				{"id": "source.json", "name": "other.json", "auth_index": "auth-3", "provider": "codex", "account_id": "account-3"},
			})
		case "DELETE /v0/management/auth-files":
			deleteCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := New(server.Client()).DeleteVerifiedPhysicalFile(context.Background(), server.URL, "mgmt", []Identity{
		{AuthFileName: "source.json", AuthIndex: "auth-1", Provider: "codex", AccountIDSnapshot: "account-1"},
		{AuthFileName: "source.json", AuthIndex: "auth-2", Provider: "codex", AccountIDSnapshot: "account-2"},
	})
	if !errors.Is(err, ErrDeleteMutationScopeAmbiguous) {
		t.Fatalf("delete collision error = %v, want ErrDeleteMutationScopeAmbiguous", err)
	}
	if deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want 0", deleteCalls)
	}
}

func TestClientDeleteVerifiedSinglePhysicalFileRejectsPhysicalNameRuntimeCollision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v0/management/auth-files" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "runtime-target", "name": "source.json", "auth_index": "auth-1", "provider": "codex", "account_id": "account-1"},
			{"id": "source.json", "name": "other.json", "auth_index": "auth-2", "provider": "codex", "account_id": "account-2"},
		})
	}))
	defer server.Close()

	_, err := New(server.Client()).ResolveVerifiedPhysicalFileDeleteTarget(
		context.Background(),
		server.URL,
		"mgmt",
		[]Identity{{
			AuthFileName:      "source.json",
			RuntimeID:         "runtime-target",
			AuthIndex:         "auth-1",
			Provider:          "codex",
			AccountIDSnapshot: "account-1",
		}},
	)
	if !errors.Is(err, ErrDeleteMutationScopeAmbiguous) {
		t.Fatalf("single physical delete collision error = %v, want ErrDeleteMutationScopeAmbiguous", err)
	}
}

func TestClientResolveStatusMutationTargetDoesNotFallBackFromRuntimeIDOnAuthIndexMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v0/management/auth-files" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("name") != "source.json" {
			t.Fatalf("status preflight query = %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "source.json", "name": "other.json", "auth_index": "auth-other"},
			{"id": "runtime-target", "name": "source.json", "auth_index": "auth-target"},
		})
	}))
	defer server.Close()

	_, err := New(server.Client()).ResolveStatusMutationTarget(
		context.Background(),
		server.URL,
		"mgmt",
		"source.json",
		"auth-target",
	)
	if !errors.Is(err, ErrAuthFileNotFound) {
		t.Fatalf("ResolveStatusMutationTarget() error = %v, want ErrAuthFileNotFound", err)
	}
}

func TestClientResolveSourceFileStatusMutationTargetRejectsPhysicalNameRuntimeCollision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v0/management/auth-files" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("name") != "source.json" {
			t.Fatalf("status preflight query = %q", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"id": "source.json", "name": "other.json", "auth_index": "auth-other"},
			{"id": "runtime-target", "name": "source.json", "auth_index": "auth-target"},
		})
	}))
	defer server.Close()

	_, err := New(server.Client()).ResolveSourceFileStatusMutationTarget(
		context.Background(),
		server.URL,
		"mgmt",
		"source.json",
		"auth-target",
	)
	if !errors.Is(err, ErrStatusMutationScopeAmbiguous) {
		t.Fatalf("ResolveSourceFileStatusMutationTarget() error = %v, want ErrStatusMutationScopeAmbiguous", err)
	}
}

func TestClientResolveVerifiedSourceFileStatusMutationTargetRejectsAddedMember(t *testing.T) {
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

	_, err := New(server.Client()).ResolveVerifiedSourceFileStatusMutationTarget(
		context.Background(),
		server.URL,
		"mgmt",
		"source.json",
		"auth-b",
		[]Identity{
			{AuthFileName: "source.json", RuntimeID: "runtime-a", AuthIndex: "auth-a", Provider: "gemini-cli", AccountIDSnapshot: "account-a"},
			{AuthFileName: "source.json", RuntimeID: "runtime-b", AuthIndex: "auth-b", Provider: "gemini-cli", AccountIDSnapshot: "account-b"},
		},
	)
	if !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("ResolveVerifiedSourceFileStatusMutationTarget() error = %v, want ErrIdentityMismatch", err)
	}
}

func TestClientDeleteVerifiedPhysicalFileRejectsChangedMemberIdentity(t *testing.T) {
	deleteCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "source.json", "name": "source.json", "auth_index": "auth-1", "provider": "codex", "account_id": "account-1"},
				{"id": "runtime-replacement", "name": "source.json", "auth_index": "auth-2", "provider": "codex", "account_id": "replacement"},
			})
		case "DELETE /v0/management/auth-files":
			deleteCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := New(server.Client()).DeleteVerifiedPhysicalFile(context.Background(), server.URL, "mgmt", []Identity{
		{AuthFileName: "source.json", AuthIndex: "auth-1", Provider: "codex", AccountIDSnapshot: "account-1"},
		{AuthFileName: "source.json", AuthIndex: "auth-2", Provider: "codex", AccountIDSnapshot: "account-2"},
	})
	if !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("delete changed physical file error = %v, want ErrIdentityMismatch", err)
	}
	if deleteCalls != 0 {
		t.Fatalf("delete calls = %d, want 0", deleteCalls)
	}
}

func TestClientPatchDisabledRequiresExplicitSourceFileScope(t *testing.T) {
	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "source.json", "name": "source.json", "auth_index": "auth-source"},
				{"id": "virtual-child", "name": "source.json", "auth_index": "auth-child"},
			})
		case "PATCH /v0/management/auth-files/status":
			patchCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := New(server.Client()).PatchDisabled(context.Background(), server.URL, "mgmt", "source.json", true, "auth-source")
	if !errors.Is(err, ErrStatusMutationScopeAmbiguous) {
		t.Fatalf("PatchDisabled() error = %v, want ErrStatusMutationScopeAmbiguous", err)
	}
	if patchCalls != 0 {
		t.Fatalf("patch calls = %d, want 0", patchCalls)
	}
}

func TestClientPatchDisabledAllowsPluginSourceFileScope(t *testing.T) {
	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			if r.URL.Query().Get("name") != "source.json" {
				t.Fatalf("status preflight query = %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "source.json", "name": "source.json", "auth_index": "auth-source"},
				{"id": "virtual-child", "name": "source.json", "auth_index": "auth-child"},
			})
		case "PATCH /v0/management/auth-files/status":
			patchCalls++
			var payload struct {
				Name      string `json:"name"`
				AuthIndex string `json:"auth_index"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			if payload.Name != "source.json" || payload.AuthIndex != "auth-source" {
				t.Fatalf("patch payload = %#v", payload)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "disabled": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(server.Client())
	target, err := client.ResolveStatusMutationTarget(context.Background(), server.URL, "mgmt", "source.json", "auth-source")
	if err != nil {
		t.Fatalf("ResolveStatusMutationTarget() error = %v", err)
	}
	if target.Scope != StatusMutationScopeSourceFile || len(target.AffectedFiles) != 2 {
		t.Fatalf("target = %#v, want source-file scope with two affected files", target)
	}
	if err := client.PatchDisabledAllowSourceFile(context.Background(), server.URL, "mgmt", "source.json", true, "auth-source"); err != nil {
		t.Fatalf("PatchDisabledAllowSourceFile() error = %v", err)
	}
	if patchCalls != 1 {
		t.Fatalf("patch calls = %d, want 1", patchCalls)
	}
}

func TestClientPatchDisabledAllowsExplicitPluginSourceWithoutSourceRuntimeRow(t *testing.T) {
	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			if r.URL.Query().Get("name") != "source.json" {
				t.Fatalf("status preflight query = %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"id": "virtual-a", "name": "source.json", "auth_index": "auth-a"},
				{"id": "virtual-b", "name": "source.json", "auth_index": "auth-b"},
			})
		case "PATCH /v0/management/auth-files/status":
			patchCalls++
			var payload struct {
				Name      string `json:"name"`
				AuthIndex string `json:"auth_index"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			if payload.Name != "source.json" || payload.AuthIndex != "auth-b" {
				t.Fatalf("patch payload = %#v", payload)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "disabled": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(server.Client())
	target, err := client.ResolveSourceFileStatusMutationTarget(
		context.Background(),
		server.URL,
		"mgmt",
		"source.json",
		"auth-b",
	)
	if err != nil {
		t.Fatalf("ResolveSourceFileStatusMutationTarget() error = %v", err)
	}
	if target.Selector != "source.json" || target.File.ID != "virtual-b" ||
		target.Scope != StatusMutationScopeSourceFile || len(target.AffectedFiles) != 2 {
		t.Fatalf("target = %#v, want explicit source-file scope", target)
	}
	if err := client.PatchDisabledTargetAllowSourceFile(
		context.Background(),
		server.URL,
		"mgmt",
		target,
		true,
	); err != nil {
		t.Fatalf("PatchDisabledTargetAllowSourceFile() error = %v", err)
	}
	if patchCalls != 1 {
		t.Fatalf("patch calls = %d, want 1", patchCalls)
	}
}

func TestClientPatchDisabledAllowsExplicitSinglePluginSource(t *testing.T) {
	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			if r.URL.Query().Get("name") != "source.json" {
				t.Fatalf("status preflight query = %q", r.URL.RawQuery)
			}
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"id": "runtime-a", "name": "source.json", "auth_index": "auth-a",
			}})
		case "PATCH /v0/management/auth-files/status":
			patchCalls++
			var payload struct {
				Name      string `json:"name"`
				AuthIndex string `json:"auth_index"`
				Disabled  bool   `json:"disabled"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode patch payload: %v", err)
			}
			if payload.Name != "source.json" || payload.AuthIndex != "auth-a" || !payload.Disabled {
				t.Fatalf("patch payload = %#v", payload)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "disabled": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := New(server.Client())
	target, err := client.ResolveSourceFileStatusMutationTarget(
		context.Background(),
		server.URL,
		"mgmt",
		"source.json",
		"auth-a",
	)
	if err != nil {
		t.Fatalf("resolve error = %v", err)
	}
	if target.Selector != "source.json" || target.File.ID != "runtime-a" ||
		target.Scope != StatusMutationScopeSourceFile || len(target.AffectedFiles) != 1 {
		t.Fatalf("target = %#v, want explicit single-member source-file scope", target)
	}
	if err := client.PatchDisabledTargetAllowSourceFile(
		context.Background(),
		server.URL,
		"mgmt",
		target,
		true,
	); err != nil {
		t.Fatalf("PatchDisabledTargetAllowSourceFile() error = %v", err)
	}
	if patchCalls != 1 {
		t.Fatalf("patch calls = %d, want 1", patchCalls)
	}
}

func TestClientPatchDisabledRejectsPluginVirtualChild(t *testing.T) {
	patchCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v0/management/auth-files":
			switch r.URL.Query().Get("name") {
			case "virtual-child":
				_ = json.NewEncoder(w).Encode([]map[string]any{
					{"id": "virtual-child", "name": "source.json", "auth_index": "auth-child"},
				})
			case "source.json":
				_ = json.NewEncoder(w).Encode([]map[string]any{
					{"id": "source.json", "name": "source.json", "auth_index": "auth-source"},
					{"id": "virtual-child", "name": "source.json", "auth_index": "auth-child"},
				})
			default:
				t.Fatalf("unexpected status preflight query = %q", r.URL.RawQuery)
			}
		case "PATCH /v0/management/auth-files/status":
			patchCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	err := New(server.Client()).PatchDisabledAllowSourceFile(
		context.Background(),
		server.URL,
		"mgmt",
		"virtual-child",
		true,
		"auth-child",
	)
	if !errors.Is(err, ErrStatusMutationScopeAmbiguous) {
		t.Fatalf("PatchDisabledAllowSourceFile() error = %v, want ErrStatusMutationScopeAmbiguous", err)
	}
	if patchCalls != 0 {
		t.Fatalf("patch calls = %d, want 0", patchCalls)
	}
}

func TestValidateActionResponse(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "empty"},
		{name: "success", body: `{"ok":true}`},
		{name: "CPA status success", body: `{"status":"ok","disabled":true}`},
		{name: "empty object", body: `{}`, wantErr: true},
		{name: "unknown status", body: `{"status":"queued"}`, wantErr: true},
		{name: "unrelated object", body: `{"disabled":true}`, wantErr: true},
		{name: "failed list", body: `{"failed":["denied"]}`, wantErr: true},
		{name: "failed string", body: `{"failed":"denied"}`, wantErr: true},
		{name: "error field", body: `{"error":"denied"}`, wantErr: true},
		{name: "success false", body: `{"success":false}`, wantErr: true},
		{name: "ok false", body: `{"ok":false}`, wantErr: true},
		{name: "failed status", body: `{"status":"failed"}`, wantErr: true},
		{name: "partial status", body: `{"status":"partial"}`, wantErr: true},
		{name: "null response", body: `null`, wantErr: true},
		{name: "boolean response", body: `false`, wantErr: true},
		{name: "array response", body: `[]`, wantErr: true},
		{name: "invalid json", body: `{"ok":`, wantErr: true},
		{name: "multiple values", body: `{"ok":true}{"ok":true}`, wantErr: true},
		{
			name:    "failure beyond old read limit",
			body:    `{"padding":"` + strings.Repeat("x", 8192) + `","failed":["denied"]}`,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateActionResponse(strings.NewReader(tt.body))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateActionResponse() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestValidateActionResponseRejectsOversizedBody(t *testing.T) {
	body := `{"padding":"` + strings.Repeat("x", maxActionResponseSize) + `"}`
	err := ValidateActionResponse(strings.NewReader(body))
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("ValidateActionResponse() error = %v, want ErrResponseTooLarge", err)
	}
}

func TestClientActionsRejectBusinessFailureResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v0/management/auth-files" {
			_, _ = io.WriteString(w, `[{"id":"runtime-codex","name":"codex-auth.json","auth_index":"auth-1"}]`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":false}`)
	}))
	defer server.Close()

	client := New(server.Client())
	if err := client.PatchDisabled(context.Background(), server.URL, "mgmt", "codex-auth.json", true); err == nil {
		t.Fatal("PatchDisabled succeeded for ok=false response")
	}
	if err := client.Delete(context.Background(), server.URL, "mgmt", "codex-auth.json"); err == nil {
		t.Fatal("Delete succeeded for ok=false response")
	}
}

func TestClientFetchRejectsOversizedAuthFilesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"files":[{"name":"auth.json","padding":"`)
		_, _ = io.WriteString(w, strings.Repeat("x", 256))
		_, _ = io.WriteString(w, `"}]}`)
	}))
	defer server.Close()

	client := New(server.Client())
	client.maxResponseBytes = 128
	_, err := client.Fetch(context.Background(), server.URL, "mgmt")
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("Fetch() error = %v, want ErrResponseTooLarge", err)
	}
}

func TestClientFindStreamsLargeAuthFilesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v0/management/auth-files" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("name") != "target.json" || r.URL.Query().Get("auth_index") != "target-index" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer mgmt" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"files":[`)
		padding := strings.Repeat("x", 2048)
		for i := 0; i < 650; i++ {
			if i > 0 {
				_, _ = io.WriteString(w, ",")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"name":           "filler.json",
				"auth_index":     "filler",
				"status_message": padding,
			})
		}
		_, _ = io.WriteString(w, `,{"name":"target.json","auth_index":"target-index","provider":"codex","account":"user@example.com","account_id":"acct-123","disabled":false}]}`)
	}))
	defer server.Close()

	client := New(server.Client())
	file, ok, err := client.Find(context.Background(), server.URL, "mgmt", "target.json", "target-index")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !ok {
		t.Fatal("expected target auth file to be found")
	}
	if file.Name != "target.json" || file.AuthIndex != "target-index" || file.AccountID != "acct-123" {
		t.Fatalf("file = %#v", file)
	}
}

func TestClientFetchAndFindAcceptSingleAuthFileObject(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v0/management/auth-files" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":       "single.json",
			"auth_index": "single-index",
			"provider":   "codex",
			"account":    "user@example.com",
			"account_id": "acct-123",
			"disabled":   false,
		})
	}))
	defer server.Close()

	client := New(server.Client())
	files, err := client.Fetch(context.Background(), server.URL, "mgmt")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(files) != 1 || files[0].Name != "single.json" || files[0].AuthIndex != "single-index" {
		t.Fatalf("files = %#v", files)
	}
	file, ok, err := client.Find(context.Background(), server.URL, "mgmt", "single.json", "single-index")
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !ok || file.Name != "single.json" || file.AuthIndex != "single-index" {
		t.Fatalf("file=%#v ok=%t", file, ok)
	}
}
