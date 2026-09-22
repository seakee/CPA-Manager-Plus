package cpaidentityinventory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaauthfiles"
)

func TestFetchAPIKeys_Valid(t *testing.T) {
	rawSecret1 := "secret-token-123"
	rawSecret2 := "  secret-token-456  "     // should be trimmed
	rawSecretDuplicate := "secret-token-123" // duplicate after trim

	expectedHash1 := sha256Hex("secret-token-123")
	expectedHash2 := sha256Hex("secret-token-456")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/api-keys" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-mgmt-key" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"api-keys": [
				"secret-token-123",
				"  secret-token-456  ",
				"   ",
				"secret-token-123"
			]
		}`))
	}))
	defer server.Close()

	c := New(server.Client(), nil)
	keys, err := c.FetchAPIKeys(context.Background(), server.URL, "test-mgmt-key")
	if err != nil {
		t.Fatalf("FetchAPIKeys failed: %v", err)
	}

	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}

	if keys[0].KeyHash != expectedHash1 {
		t.Errorf("keys[0].KeyHash = %q, want %q", keys[0].KeyHash, expectedHash1)
	}
	if keys[1].KeyHash != expectedHash2 {
		t.Errorf("keys[1].KeyHash = %q, want %q", keys[1].KeyHash, expectedHash2)
	}

	// Verify containment: raw secret must not be present in keys
	for _, k := range keys {
		if strings.Contains(k.KeyHash, rawSecret1) || strings.Contains(k.KeyHash, rawSecret2) || strings.Contains(k.KeyHash, rawSecretDuplicate) {
			t.Errorf("raw secret leaked in returned port model: %v", k.KeyHash)
		}
	}
}

func TestFetchAPIKeys_ValidEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"api-keys": []}`))
	}))
	defer server.Close()

	c := New(server.Client(), nil)
	keys, err := c.FetchAPIKeys(context.Background(), server.URL, "key")
	if err != nil {
		t.Fatalf("expected nil error for valid empty inventory, got %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected 0 keys for empty inventory, got %d", len(keys))
	}
}

func TestFetchAPIKeys_MalformedShapes(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		errContain string
	}{
		{
			name:       "not a json object",
			body:       `["key1", "key2"]`,
			errContain: "invalid JSON object",
		},
		{
			name:       "missing api-keys field",
			body:       `{"keys": ["key1"]}`,
			errContain: "missing 'api-keys' field",
		},
		{
			name:       "empty root object",
			body:       `{}`,
			errContain: "missing 'api-keys' field",
		},
		{
			name:       "api-keys is null",
			body:       `{"api-keys": null}`,
			errContain: "'api-keys' is null",
		},
		{
			name:       "api-keys is not an array",
			body:       `{"api-keys": "not-an-array"}`,
			errContain: "'api-keys' must be a JSON array",
		},
		{
			name:       "api-keys is an object",
			body:       `{"api-keys": {}}`,
			errContain: "'api-keys' must be a JSON array",
		},
		{
			name:       "duplicate authority field",
			body:       `{"api-keys": ["a"], "api-keys": []}`,
			errContain: "duplicate 'api-keys' field",
		},
		{
			name:       "duplicate authority field with other field",
			body:       `{"api-keys": ["a"], "meta": 1, "api-keys": []}`,
			errContain: "duplicate 'api-keys' field",
		},
		{
			name:       "non-string item int in array",
			body:       `{"api-keys": ["valid-key", 12345]}`,
			errContain: "item at index 1 is not a string",
		},
		{
			name:       "non-string item null in array",
			body:       `{"api-keys": ["valid-key", null]}`,
			errContain: "item at index 1 is not a string",
		},
		{
			name:       "object item in array",
			body:       `{"api-keys": [{"nested": "value"}]}`,
			errContain: "item at index 0 is not a string",
		},
		{
			name:       "array item in array",
			body:       `{"api-keys": [["nested"]]}`,
			errContain: "item at index 0 is not a string",
		},
		{
			name:       "trailing JSON object",
			body:       `{"api-keys": []}{"x": 1}`,
			errContain: "unexpected trailing data",
		},
		{
			name:       "trailing garbage",
			body:       `{"api-keys": []}oops`,
			errContain: "unexpected trailing data",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			c := New(server.Client(), nil)
			_, err := c.FetchAPIKeys(context.Background(), server.URL, "key")
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.errContain)
			}
			if !errors.Is(err, ErrMalformedAPIKeysResponse) {
				t.Errorf("expected ErrMalformedAPIKeysResponse, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.errContain) {
				t.Errorf("expected error to contain %q, got %q", tc.errContain, err.Error())
			}
		})
	}
}

func TestFetchAPIKeys_UnknownFieldsIgnored(t *testing.T) {
	cases := []struct {
		name      string
		body      string
		wantCount int
	}{
		{
			name:      "unknown meta field before authority field",
			body:      `{"observed_at": "2026-09-22T00:00:00Z", "api-keys": ["a"]}`,
			wantCount: 1,
		},
		{
			name:      "unknown complex object before and scalar after",
			body:      `{"meta": {"version": "v1", "nested": [1, 2]}, "api-keys": ["a", "b"], "count": 2}`,
			wantCount: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			c := New(server.Client(), nil)
			keys, err := c.FetchAPIKeys(context.Background(), server.URL, "key")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(keys) != tc.wantCount {
				t.Fatalf("expected %d keys, got %d", tc.wantCount, len(keys))
			}
		})
	}
}

func TestFetchAPIKeys_Oversized(t *testing.T) {
	t.Run("custom small limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"api-keys": ["a-secret-key-that-causes-the-response-to-exceed-the-small-limit-1234567890"]}`))
		}))
		defer server.Close()

		c := New(server.Client(), nil)
		c.(*client).maxResponseBytes = 32

		_, err := c.FetchAPIKeys(context.Background(), server.URL, "key")
		if err == nil {
			t.Fatal("expected error for oversized response, got nil")
		}
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Errorf("expected ErrResponseTooLarge, got %v", err)
		}
		if strings.Contains(err.Error(), "a-secret-key") {
			t.Errorf("secret leaked in error: %v", err)
		}
	})

	t.Run("default limit exceeded", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			// Stream slightly more than maxAPIKeysBytes (4MB)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"api-keys": [`))
			chunk := `"` + strings.Repeat("a", 1024) + `",`
			totalWritten := 14
			for totalWritten < maxAPIKeysBytes+10 {
				n, _ := w.Write([]byte(chunk))
				totalWritten += n
			}
			_, _ = w.Write([]byte(`"end"]}`))
		}))
		defer server.Close()

		c := New(server.Client(), nil)
		_, err := c.FetchAPIKeys(context.Background(), server.URL, "key")
		if err == nil {
			t.Fatal("expected error for oversized response, got nil")
		}
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Errorf("expected ErrResponseTooLarge, got %v", err)
		}
		if !strings.Contains(err.Error(), fmt.Sprintf("exceeds %d bytes", maxAPIKeysBytes)) {
			t.Errorf("expected error to mention limit %d, got: %v", maxAPIKeysBytes, err)
		}
		if strings.Contains(err.Error(), strings.Repeat("a", 64)) {
			t.Errorf("raw data leaked in oversized error message: %v", err)
		}
	})
}

func TestFetchAPIKeys_RawKeyContainmentInError(t *testing.T) {
	rawSecret := "super-sensitive-raw-api-key-9999"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("internal error dump with sensitive key: " + rawSecret))
	}))
	defer server.Close()

	c := New(server.Client(), nil)
	_, err := c.FetchAPIKeys(context.Background(), server.URL, "key")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if strings.Contains(err.Error(), rawSecret) {
		t.Errorf("raw key %q leaked in error message: %v", rawSecret, err)
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("expected error to contain 'HTTP 500', got: %v", err)
	}
}

func TestFetchCredentials_ValidAndExcluded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"files": [
				{
					"id": "runtime-auth-1",
					"name": "cred1.json",
					"auth_index": "10",
					"provider": "codex",
					"account": "user1@example.com",
					"account_id": "acct-1",
					"disabled": false,
					"runtime_only": false
				},
				{
					"id": "runtime-auth-virtual",
					"name": "virtual.json",
					"auth_index": "11",
					"provider": "plugin",
					"runtime_only": true
				},
				{
					"id": "runtime-auth-2",
					"name": "cred2.json",
					"auth_index": "20",
					"provider": "claude",
					"account": "user2@example.com",
					"account_id": "acct-2",
					"disabled": true
				}
			]
		}`))
	}))
	defer server.Close()

	authFilesClient := cpaauthfiles.New(server.Client())
	c := New(server.Client(), authFilesClient)

	creds, err := c.FetchCredentials(context.Background(), server.URL, "mgmt-key")
	if err != nil {
		t.Fatalf("FetchCredentials failed: %v", err)
	}

	// runtime_only should be excluded, leaving 2 items
	if len(creds) != 2 {
		t.Fatalf("expected 2 creds, got %d", len(creds))
	}

	// Verify metadata mapped correctly
	c1 := creds[0]
	if c1.SourceAuthID != "runtime-auth-1" {
		t.Errorf("SourceAuthID = %q, want runtime-auth-1", c1.SourceAuthID)
	}
	if c1.PhysicalName != "cred1.json" {
		t.Errorf("PhysicalName = %q, want cred1.json", c1.PhysicalName)
	}
	if c1.AuthIndex != "10" {
		t.Errorf("AuthIndex = %q, want 10", c1.AuthIndex)
	}
	if c1.Provider != "codex" {
		t.Errorf("Provider = %q, want codex", c1.Provider)
	}
	if c1.AccountSnapshot != "user1@example.com" {
		t.Errorf("AccountSnapshot = %q, want user1@example.com", c1.AccountSnapshot)
	}
	if c1.AccountIDSnapshot != "acct-1" {
		t.Errorf("AccountIDSnapshot = %q, want acct-1", c1.AccountIDSnapshot)
	}
	if c1.Disabled != false {
		t.Errorf("Disabled = %v, want false", c1.Disabled)
	}

	c2 := creds[1]
	if c2.SourceAuthID != "runtime-auth-2" {
		t.Errorf("SourceAuthID = %q, want runtime-auth-2", c2.SourceAuthID)
	}
	if c2.Disabled != true {
		t.Errorf("Disabled = %v, want true", c2.Disabled)
	}

	// Confirm virtual item was omitted
	for _, c := range creds {
		if c.SourceAuthID == "runtime-auth-virtual" {
			t.Errorf("runtime_only credential was not excluded: %v", c)
		}
	}
}

func TestFetchCredentials_EmptyAuthID_FailClosed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Credential without id (e.g. disk-only fallback)
		_, _ = w.Write([]byte(`{
			"files": [
				{
					"name": "disk-only.json",
					"auth_index": "1",
					"provider": "codex",
					"runtime_only": false
				}
			]
		}`))
	}))
	defer server.Close()

	authFilesClient := cpaauthfiles.New(server.Client())
	c := New(server.Client(), authFilesClient)

	_, err := c.FetchCredentials(context.Background(), server.URL, "mgmt-key")
	if err == nil {
		t.Fatal("expected error for empty Auth.ID, got nil")
	}
	if !errors.Is(err, ErrIncompleteCredentialInventory) {
		t.Errorf("expected ErrIncompleteCredentialInventory, got %v", err)
	}
}

func TestFetchCredentials_StrictInventoryContract(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantCount   int
		wantErr     bool
		errSentinel error
	}{
		{
			name:      "valid non-empty",
			body:      `{"files": [{"id": "auth-1", "name": "cred.json", "runtime_only": false}]}`,
			wantCount: 1,
			wantErr:   false,
		},
		{
			name:      "valid empty",
			body:      `{"files": []}`,
			wantCount: 0,
			wantErr:   false,
		},
		{
			name:        "malformed root empty object",
			body:        `{}`,
			wantErr:     true,
			errSentinel: cpaauthfiles.ErrMalformedAuthFilesResponse,
		},
		{
			name:        "null list",
			body:        `{"files": null}`,
			wantErr:     true,
			errSentinel: cpaauthfiles.ErrMalformedAuthFilesResponse,
		},
		{
			name:        "wrong list type string",
			body:        `{"files": "bad"}`,
			wantErr:     true,
			errSentinel: cpaauthfiles.ErrMalformedAuthFilesResponse,
		},
		{
			name:        "empty object item",
			body:        `{"files": [{}]}`,
			wantErr:     true,
			errSentinel: cpaauthfiles.ErrMalformedAuthFilesResponse,
		},
		{
			name:        "non-object item string",
			body:        `{"files": ["bad"]}`,
			wantErr:     true,
			errSentinel: cpaauthfiles.ErrMalformedAuthFilesResponse,
		},
		{
			name:        "non-object item null",
			body:        `{"files": [null]}`,
			wantErr:     true,
			errSentinel: cpaauthfiles.ErrMalformedAuthFilesResponse,
		},
		{
			name:      "runtime_only only",
			body:      `{"files": [{"id": "virtual-1", "runtime_only": true}]}`,
			wantCount: 0,
			wantErr:   false,
		},
		{
			name:        "missing Auth.ID non-runtime-only",
			body:        `{"files": [{"name": "disk-only.json", "runtime_only": false}]}`,
			wantErr:     true,
			errSentinel: ErrIncompleteCredentialInventory,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			authFilesClient := cpaauthfiles.New(server.Client())
			c := New(server.Client(), authFilesClient)

			creds, err := c.FetchCredentials(context.Background(), server.URL, "key")
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tt.errSentinel != nil && !errors.Is(err, tt.errSentinel) {
					t.Fatalf("expected error %v, got %v", tt.errSentinel, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(creds) != tt.wantCount {
				t.Fatalf("creds count = %d, want %d", len(creds), tt.wantCount)
			}
		})
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
