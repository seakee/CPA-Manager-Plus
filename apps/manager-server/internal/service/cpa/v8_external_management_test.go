package cpa

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestExternalModelSurface proves the model-list probe reads exactly that list on
// the External transport: it answers only for a non-empty model set, refuses an
// empty one, and never moves a non-200 answer into a pass.
func TestExternalModelSurface(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		wantOK bool
	}{
		"a model list":         {status: http.StatusOK, body: `{"data":[{"id":"gpt-test"}]}`, wantOK: true},
		"an empty list":        {status: http.StatusOK, body: `{"data":[]}`},
		"an empty OpenAI list": {status: http.StatusOK, body: `{"object":"list","data":[]}`},
		"an id-less entry":     {status: http.StatusOK, body: `{"data":[{"id":"   "}]}`},
		"an unparsable body":   {status: http.StatusOK, body: `{"data":[`},
		"an error status":      {status: http.StatusBadGateway, body: `{"data":[{"id":"gpt-test"}]}`},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			var authorization string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/v1/models" {
					writer.WriteHeader(http.StatusNotFound)
					return
				}
				authorization = request.Header.Get("Authorization")
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(testCase.status)
				_, _ = writer.Write([]byte(testCase.body))
			}))
			defer server.Close()
			err := NewTransportForEndpoint(server.URL).ExternalModelSurface(context.Background(), "client-key-1")
			if testCase.wantOK && err != nil {
				t.Fatalf("a served model list was refused: %v", err)
			}
			if !testCase.wantOK && err == nil {
				t.Fatal("an unserved model list was accepted")
			}
			if authorization != "Bearer client-key-1" {
				t.Fatalf("authorization = %q", authorization)
			}
		})
	}
	// A nil transport has no direct, proxy-free client to speak on, so it never
	// reports the surface as served.
	var absent *Transport
	if err := absent.ExternalModelSurface(context.Background(), ""); err == nil {
		t.Fatal("a nil transport reported a served model surface")
	}
}

// TestManagedModelSurface proves the Managed destination model-list probe
// accepts an authenticated empty OpenAI model list like VerifyV8Adoption while
// refusing 401/403, non-200, malformed JSON and missing data without mentioning
// private keys.
func TestManagedModelSurface(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		clientKey  string
		wantOK     bool
		wantSubstr string
	}{
		{
			name:      "empty model list",
			status:    http.StatusOK,
			body:      `{"object":"list","data":[]}`,
			clientKey: "client-key-1",
			wantOK:    true,
		},
		{
			name:      "populated model list",
			status:    http.StatusOK,
			body:      `{"object":"list","data":[{"id":"gpt-test","object":"model"}]}`,
			clientKey: "client-key-1",
			wantOK:    true,
		},
		{
			name:       "unauthorized client key",
			status:     http.StatusUnauthorized,
			body:       `{"error":"unauthorized"}`,
			clientKey:  "client-key-1",
			wantSubstr: "rejected the client key",
		},
		{
			name:       "forbidden client key",
			status:     http.StatusForbidden,
			body:       `{"error":"forbidden"}`,
			clientKey:  "client-key-1",
			wantSubstr: "rejected the client key",
		},
		{
			name:       "upstream error status",
			status:     http.StatusBadGateway,
			body:       `{"object":"list","data":[]}`,
			clientKey:  "client-key-1",
			wantSubstr: "status 502",
		},
		{
			name:       "malformed JSON",
			status:     http.StatusOK,
			body:       `{"object":"list","data":[`,
			clientKey:  "client-key-1",
			wantSubstr: "not provable",
		},
		{
			name:       "missing data array",
			status:     http.StatusOK,
			body:       `{"object":"list"}`,
			clientKey:  "client-key-1",
			wantSubstr: "not provable",
		},
		{
			name:       "null data array",
			status:     http.StatusOK,
			body:       `{"object":"list","data":null}`,
			clientKey:  "client-key-1",
			wantSubstr: "not provable",
		},
		{
			name:       "wrong object type",
			status:     http.StatusOK,
			body:       `{"object":"model","data":[]}`,
			clientKey:  "client-key-1",
			wantSubstr: "not provable",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var authorization string
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/v1/models" {
					writer.WriteHeader(http.StatusNotFound)
					return
				}
				authorization = request.Header.Get("Authorization")
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(testCase.status)
				_, _ = writer.Write([]byte(testCase.body))
			}))
			defer server.Close()
			err := NewTransportForEndpoint(server.URL).ManagedModelSurface(context.Background(), testCase.clientKey)
			if testCase.wantOK {
				if err != nil {
					t.Fatalf("expected valid Managed model surface, got %v", err)
				}
			} else {
				if err == nil {
					t.Fatal("expected invalid Managed model surface to fail")
				}
				if strings.Contains(strings.ToLower(err.Error()), "private key") {
					t.Fatalf("error %q mentioned private key", err)
				}
				if testCase.wantSubstr != "" && !strings.Contains(err.Error(), testCase.wantSubstr) {
					t.Fatalf("error = %q, want substring %q", err, testCase.wantSubstr)
				}
			}
			if authorization != "Bearer "+testCase.clientKey {
				t.Fatalf("authorization = %q, want Bearer %s", authorization, testCase.clientKey)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer server.Close()
	if err := NewTransportForEndpoint(server.URL).ManagedModelSurface(context.Background(), ""); err == nil || strings.Contains(strings.ToLower(err.Error()), "private key") {
		t.Fatalf("empty client key must be refused without private-key wording, got %v", err)
	}
	var absent *Transport
	if err := absent.ManagedModelSurface(context.Background(), "client-key-1"); err == nil || strings.Contains(strings.ToLower(err.Error()), "private key") {
		t.Fatalf("nil transport must be refused without private-key wording, got %v", err)
	}
}

func TestExternalReadConfigDocument(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		body          string
		wantRejected  bool
		wantForbidden bool
		wantOK        bool
	}{
		{name: "200 document", status: http.StatusOK, body: `{"config-version":8,"access":{"api-keys":["k1"]}}`, wantOK: true},
		{name: "401 unauthorized", status: http.StatusUnauthorized, body: `{"error":"unauthorized"}`, wantRejected: true},
		{name: "403 forbidden", status: http.StatusForbidden, body: `{"error":"forbidden"}`, wantRejected: true, wantForbidden: true},
		{name: "502 bad gateway", status: http.StatusBadGateway, body: `bad gateway`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v8/management/config" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			doc, err := NewTransportForEndpoint(server.URL).ExternalReadConfigDocument(t.Context(), "management-key-1")
			if tc.wantOK {
				if err != nil || doc == nil {
					t.Fatalf("ExternalReadConfigDocument = %v, %v; want document", doc, err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if got := errors.Is(err, ErrManagementRejected); got != tc.wantRejected {
				t.Fatalf("errors.Is(ErrManagementRejected) = %v, want %v (%v)", got, tc.wantRejected, err)
			}
			if got := errors.Is(err, ErrManagementClientForbidden); got != tc.wantForbidden {
				t.Fatalf("errors.Is(ErrManagementClientForbidden) = %v, want %v (%v)", got, tc.wantForbidden, err)
			}
		})
	}
}
