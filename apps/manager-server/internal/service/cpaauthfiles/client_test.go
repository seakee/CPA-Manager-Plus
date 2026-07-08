package cpaauthfiles

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseAndVerifyIdentity(t *testing.T) {
	files, err := Parse([]byte(`{"auth_files":[{"name":"codex-auth.json","auth_index":"7","provider":"codex","account":"user@example.com","account_id":"acct-123","disabled":"true"}]}`))
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
	if !file.Disabled || file.AuthIndex != "7" || file.Provider != "codex" {
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
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "codex-auth.json", "auth_index": "7"}})
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
			if payload.Name != "codex-auth.json" || payload.AuthIndex != "7" || !payload.Disabled {
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
