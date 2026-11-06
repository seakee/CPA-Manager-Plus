package cpa

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProviderNameETagContract(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusPreconditionFailed, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer management-test-only" {
					t.Error("missing management auth")
				}
				if r.Method == http.MethodGet {
					w.Header().Set("ETag", `"revision-1"`)
					_, _ = w.Write([]byte(`{"api-keys":{"codex":[{"name":"account@example.com","keys":[{"api-key":"synthetic-secret"}]}]}}`))
					return
				}
				if r.URL.Path != "/v8/management/config/api-keys/codex" || r.Header.Get("If-Match") != `"revision-1"` {
					t.Error("unguarded or wrong scope write")
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			transport := NewTransportForEndpoint(server.URL)
			document, etag, err := transport.ReadProviderNameDocument(context.Background(), "management-test-only")
			if err != nil || etag != `"revision-1"` || document["api-keys"] == nil {
				t.Fatalf("read failed: %v", err)
			}
			err = transport.PutProviderNameGroups(context.Background(), "management-test-only", "codex", etag, []byte(`[]`))
			if status == http.StatusOK && err != nil || status == http.StatusPreconditionFailed && !errors.Is(err, ErrConfigChanged) || status == http.StatusInternalServerError && !errors.Is(err, ErrApplyUnknown) {
				t.Fatalf("wrong result: %v", err)
			}
			if calls != 2 {
				t.Fatalf("request replayed: %d", calls)
			}
		})
	}
}

func TestProviderNamesRequireRevisionAndClosedPath(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; _, _ = w.Write([]byte(`{}`)) }))
	defer server.Close()
	transport := NewTransportForEndpoint(server.URL)
	if _, _, err := transport.ReadProviderNameDocument(context.Background(), "management-test-only"); err == nil {
		t.Fatal("accepted missing ETag")
	}
	for _, provider := range []string{"../../client-keys", "", "codex/name", "openai-compatibility", "meta"} {
		if err := transport.PutProviderNameGroups(context.Background(), "management-test-only", provider, `"a"`, []byte(`[]`)); err == nil {
			t.Fatal("accepted path")
		}
	}
	if calls != 1 {
		t.Fatal("invalid path reached CPA")
	}
}

func TestProviderNamesMapManagementRefusalsWithoutReplay(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		for _, method := range []string{http.MethodGet, http.MethodPut} {
			t.Run(method+http.StatusText(status), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls++
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":"synthetic-secret-upstream-body"}`))
				}))
				defer server.Close()
				transport := NewTransportForEndpoint(server.URL)
				var err error
				if method == http.MethodGet {
					_, _, err = transport.ReadProviderNameDocument(t.Context(), "management-test-only")
				} else {
					err = transport.PutProviderNameGroups(t.Context(), "management-test-only", "codex", `"r1"`, []byte(`[]`))
				}
				if !errors.Is(err, ErrManagementRejected) || (status == http.StatusForbidden && !errors.Is(err, ErrManagementClientForbidden)) || calls != 1 {
					t.Fatalf("status %d: %v, calls %d", status, err, calls)
				}
				if err == nil || strings.Contains(err.Error(), "synthetic-secret") {
					t.Fatal("upstream body escaped rejection mapping")
				}
			})
		}
	}
}

// Version strings are advisory; the actual revision capability is mandatory.
func TestProviderNamesETagCapabilityBoundary(t *testing.T) {
	for _, fixture := range []struct{ version, etag string }{
		{"v8.0.30", ""}, {"v8.0.31", `"r1"`},
	} {
		t.Run(fixture.version, func(t *testing.T) {
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-CPA-Version", fixture.version)
				if fixture.etag != "" {
					w.Header().Set("ETag", fixture.etag)
				}
				if r.Method == http.MethodPut {
					writes++
					w.WriteHeader(http.StatusPreconditionFailed)
					return
				}
				_, _ = w.Write([]byte(`{"api-keys":{"codex":[]}}`))
			}))
			defer server.Close()
			transport := NewTransportForEndpoint(server.URL)
			_, etag, err := transport.ReadProviderNameDocument(t.Context(), "management-test-only")
			if fixture.etag == "" {
				if !errors.Is(err, ErrProofUnavailable) {
					t.Fatalf("missing capability accepted: %v", err)
				}
				if err = transport.PutProviderNameGroups(t.Context(), "management-test-only", "codex", etag, []byte(`[]`)); err == nil || writes != 0 {
					t.Fatal("unguarded fallback write")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				err = transport.PutProviderNameGroups(t.Context(), "management-test-only", "codex", etag, []byte(`[]`))
				if !errors.Is(err, ErrConfigChanged) || writes != 1 {
					t.Fatal("412 did not stop guarded write")
				}
			}
		})
	}
}
