package cpa

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestObserveManagementAPI(t *testing.T) {
	tests := []struct {
		name        string
		headers     map[string]string
		wantVersion string
	}{
		{
			name:        "CPA version",
			headers:     map[string]string{"X-CPA-Version": " v7.2.130 "},
			wantVersion: "v7.2.130",
		},
		{
			name:        "legacy server version",
			headers:     map[string]string{"X-Server-Version": "v6.10.8"},
			wantVersion: "v6.10.8",
		},
		{
			name: "CPA version takes precedence",
			headers: map[string]string{
				"X-CPA-Version":    "v7.2.130",
				"X-Server-Version": "v6.10.8",
			},
			wantVersion: "v7.2.130",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var gotPath string
			var gotAuthorization string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotAuthorization = r.Header.Get("Authorization")
				for name, value := range test.headers {
					w.Header().Set(name, value)
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("not decoded"))
			}))
			defer server.Close()

			version, err := ObserveManagementAPI(t.Context(), server.URL, "management-key")
			if err != nil {
				t.Fatalf("ObserveManagementAPI() error = %v", err)
			}
			if version != test.wantVersion {
				t.Fatalf("ObserveManagementAPI() version = %q, want %q", version, test.wantVersion)
			}
			if gotPath != "/v0/management/config" {
				t.Fatalf("request path = %q", gotPath)
			}
			if gotAuthorization != "Bearer management-key" {
				t.Fatalf("Authorization = %q", gotAuthorization)
			}
		})
	}
}

func TestObserveManagementAPIPreservesValidationCompatibilityWithoutVersion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	version, err := ObserveManagementAPI(t.Context(), server.URL, "management-key")
	if err != nil {
		t.Fatalf("ObserveManagementAPI() error = %v", err)
	}
	if version != "" {
		t.Fatalf("ObserveManagementAPI() version = %q, want empty", version)
	}
	if err := ValidateManagementAPI(t.Context(), server.URL, "management-key"); err != nil {
		t.Fatalf("ValidateManagementAPI() error = %v", err)
	}
}

func TestObserveManagementAPIReturnsErrors(t *testing.T) {
	t.Run("non-success response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		}))
		defer server.Close()

		_, err := ObserveManagementAPI(t.Context(), server.URL, "wrong-key")
		if err == nil || !strings.Contains(err.Error(), "401 Unauthorized") {
			t.Fatalf("ObserveManagementAPI() error = %v", err)
		}
	})

	t.Run("caller context canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := ObserveManagementAPI(ctx, "http://127.0.0.1:1", "management-key")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ObserveManagementAPI() error = %v, want context canceled", err)
		}
	})

	t.Run("network unavailable", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		baseURL := server.URL
		server.Close()

		if _, err := ObserveManagementAPI(t.Context(), baseURL, "management-key"); err == nil {
			t.Fatal("ObserveManagementAPI() error = nil")
		}
	})
}

func TestNormalizeBaseURLCaseInsensitiveScheme(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{raw: "HTTP://127.0.0.1:8317", want: "http://127.0.0.1:8317"},
		{raw: "HTTPS://127.0.0.1:8317/v0/management/", want: "https://127.0.0.1:8317"},
		{raw: "Http://CPA.Example:8317/v0/", want: "http://CPA.Example:8317"},
		{raw: "127.0.0.1:8317/v0/management", want: "http://127.0.0.1:8317"},
		{raw: "   ", want: ""},
	}
	for _, tc := range cases {
		if got := NormalizeBaseURL(tc.raw); got != tc.want {
			t.Fatalf("NormalizeBaseURL(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}
