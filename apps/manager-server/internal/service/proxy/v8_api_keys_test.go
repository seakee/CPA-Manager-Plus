package proxy

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/managerconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

// TestIsCPAV8APIKeysConfigPath pins the one v8 management subtree Manager
// forwards. The match is on a path segment boundary so a sibling such as
// /config/api-keys-x, the config root, or any other v8 route stays unmatched.
func TestIsCPAV8APIKeysConfigPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "/v8/management/config/api-keys", want: true},
		{path: "/v8/management/config/api-keys/", want: true},
		{path: "/v8/management/config/api-keys/claude", want: true},
		{path: "/v8/management/config/api-keys/openai-compatibility", want: true},
		{path: "/v8/management/config/api-keys-x", want: false},
		{path: "/v8/management/config/api-key", want: false},
		{path: "/v8/management/config", want: false},
		{path: "/v8/management/config/access/api-keys", want: false},
		{path: "/v8/management/config.yaml", want: false},
		{path: "/v8/management/credentials", want: false},
		{path: "/v8/management", want: false},
		{path: "/v0/management/api-keys", want: false},
		{path: "", want: false},
	}
	for _, test := range tests {
		if got := IsCPAV8APIKeysConfigPath(test.path); got != test.want {
			t.Fatalf("IsCPAV8APIKeysConfigPath(%q) = %v, want %v", test.path, got, test.want)
		}
	}
}

// TestIsCanonicalRequestPath pins the shape the prefix check alone cannot see:
// ServeMux leaves escaped segments untouched, so a request that would resolve to
// another upstream route must be refused before it is forwarded.
func TestIsCanonicalRequestPath(t *testing.T) {
	tests := []struct {
		target string
		want   bool
	}{
		{target: "/v8/management/config/api-keys", want: true},
		{target: "/v8/management/config/api-keys/claude?format=json", want: true},
		{target: "/v8/management/config/api-keys/../credentials", want: false},
		{target: "/v8/management/config/api-keys/./claude", want: false},
		{target: "/v8/management/config/api-keys/%2e%2e/credentials", want: false},
		{target: "/v8/management/config/api-keys%2F..%2Fcredentials", want: false},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodGet, test.target, nil)
		if got := isCanonicalRequestPath(request.URL); got != test.want {
			t.Fatalf("isCanonicalRequestPath(%q) = %v, want %v", test.target, got, test.want)
		}
	}
	var nilURL *url.URL
	if isCanonicalRequestPath(nilURL) {
		t.Fatal("a nil URL must not be treated as canonical")
	}
}

// TestProxyV8APIKeysConfigForwardsOnlyAdminReads pins both halves of the
// contract: one admitted GET reaches CPA with the saved management key and an
// untouched path/query, and every other request shape is refused locally.
func TestProxyV8APIKeysConfigForwardsOnlyAdminReads(t *testing.T) {
	const savedKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	fixture := newPluginProxyFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "..") {
			t.Errorf("upstream received a dot-segment path: %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[{"name":"claude-1"}]`))
	})
	methodNotAllowed := func(w http.ResponseWriter) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}

	recorder := httptest.NewRecorder()
	fixture.service.ProxyV8APIKeysConfig(
		recorder,
		httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/claude?format=json", nil),
		writeProxyError,
		methodNotAllowed,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%q", recorder.Code, recorder.Body.String())
	}
	if len(*fixture.upstreamPaths) != 1 || (*fixture.upstreamPaths)[0] != "/v8/management/config/api-keys/claude" {
		t.Fatalf("upstream paths = %#v", *fixture.upstreamPaths)
	}
	if got := (*fixture.upstreamAuthorizations)[0]; got != "Bearer "+savedKey {
		t.Fatalf("upstream authorization = %q, want the saved management key", got)
	}

	for _, test := range []struct {
		name   string
		method string
		target string
		want   int
	}{
		{name: "post", method: http.MethodPost, target: "/v8/management/config/api-keys", want: http.StatusMethodNotAllowed},
		{name: "put", method: http.MethodPut, target: "/v8/management/config/api-keys/claude", want: http.StatusMethodNotAllowed},
		{name: "patch", method: http.MethodPatch, target: "/v8/management/config/api-keys", want: http.StatusMethodNotAllowed},
		{name: "delete", method: http.MethodDelete, target: "/v8/management/config/api-keys", want: http.StatusMethodNotAllowed},
		{name: "head", method: http.MethodHead, target: "/v8/management/config/api-keys", want: http.StatusMethodNotAllowed},
		{name: "config root", method: http.MethodGet, target: "/v8/management/config", want: http.StatusNotFound},
		{name: "client keys", method: http.MethodGet, target: "/v8/management/config/access/api-keys", want: http.StatusNotFound},
		{name: "sibling", method: http.MethodGet, target: "/v8/management/config/api-keys-x", want: http.StatusNotFound},
		{name: "dot segments", method: http.MethodGet, target: "/v8/management/config/api-keys/../config", want: http.StatusNotFound},
		{name: "encoded dot segments", method: http.MethodGet, target: "/v8/management/config/api-keys/%2e%2e/config", want: http.StatusNotFound},
		{name: "encoded slash dot segments", method: http.MethodGet, target: "/v8/management/config/api-keys%2F..%2Fconfig", want: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := len(*fixture.upstreamPaths)
			recorder := httptest.NewRecorder()
			fixture.service.ProxyV8APIKeysConfig(
				recorder,
				httptest.NewRequest(test.method, test.target, nil),
				writeProxyError,
				methodNotAllowed,
			)
			if recorder.Code != test.want {
				t.Fatalf("status = %d, want %d (body=%q)", recorder.Code, test.want, recorder.Body.String())
			}
			if after := len(*fixture.upstreamPaths); after != before {
				t.Fatalf("%s %s reached upstream: %#v", test.method, test.target, (*fixture.upstreamPaths)[before:])
			}
		})
	}
}

// TestProxyV8APIKeysConfigRequiresASavedSetup pins the fail-closed answer for a
// Manager that has saved no CPA connection: the route has no upstream to name, so
// it answers the same precondition every other saved-key proxy does instead of
// inventing one.
func TestProxyV8APIKeysConfigRequiresASavedSetup(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	service := New(managerconfig.New(config.Config{}, st, nil), st)

	recorder := httptest.NewRecorder()
	service.ProxyV8APIKeysConfig(
		recorder,
		httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys", nil),
		writeProxyError,
		func(w http.ResponseWriter) { w.WriteHeader(http.StatusMethodNotAllowed) },
	)
	if recorder.Code != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, want %d (body=%q)", recorder.Code, http.StatusPreconditionRequired, recorder.Body.String())
	}
}
