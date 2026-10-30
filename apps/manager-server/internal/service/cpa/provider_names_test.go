package cpa

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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
	for _, provider := range []string{"../../client-keys", "", "codex/name"} {
		if err := transport.PutProviderNameGroups(context.Background(), "management-test-only", provider, `"a"`, []byte(`[]`)); err == nil {
			t.Fatal("accepted path")
		}
	}
	if calls != 1 {
		t.Fatal("invalid path reached CPA")
	}
}
