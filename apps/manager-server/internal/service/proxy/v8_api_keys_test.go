package proxy

import (
	"io"
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
		{target: "/v8/management/config/api-keys/", want: true},
		{target: "/v8/management/config/api-keys/claude?format=json", want: true},
		{target: "/v8/management/config/api-keys/../credentials", want: false},
		{target: "/v8/management/config/api-keys/./claude", want: false},
		{target: "/v8/management/config/api-keys//claude", want: false},
		{target: "/v8//management/config/api-keys", want: false},
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

// TestV8APIKeysConfigRequestPredicates pins the pair the router and the External
// cutover share: which requests are the one forwarded shape, and which dirty
// shapes must be refused before ServeMux can redirect them onto it.
func TestV8APIKeysConfigRequestPredicates(t *testing.T) {
	tests := []struct {
		target    string
		canonical bool
		refused   bool
	}{
		{target: "/v8/management/config/api-keys", canonical: true},
		{target: "/v8/management/config/api-keys/", canonical: true},
		{target: "/v8/management/config/api-keys/claude?format=json", canonical: true},
		// Canonical paths outside the subtree are not dirty: the router's ordinary
		// 404 answers them, not the pre-mux guard.
		{target: "/v8/management/config/api-keys-x"},
		{target: "/v8/management/config/access/api-keys"},
		{target: "/v8/management/config/api-keys//claude", refused: true},
		{target: "/v8/management/config/api-keys/./claude", refused: true},
		{target: "/v8/management/config/api-keys/../credentials", refused: true},
		{target: "/v8//management/config/api-keys", refused: true},
		{target: "//v8/management/config/api-keys", refused: true},
		{target: "/v8/management/config/api-keys/%2e%2e/credentials", refused: true},
		{target: "/v8/management/config/api-keys%2F..%2Fcredentials", refused: true},
		// Outside the subtree: the guard stays silent and the router's ordinary
		// ServeMux behaviour is untouched.
		{target: "/v1/chat/completions"},
		{target: "/v8/management//credentials"},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodGet, test.target, nil)
		if got := IsCanonicalV8APIKeysConfigRequest(request); got != test.canonical {
			t.Fatalf("IsCanonicalV8APIKeysConfigRequest(%q) = %v, want %v", test.target, got, test.canonical)
		}
		if got := IsNonCanonicalV8APIKeysConfigRequest(request); got != test.refused {
			t.Fatalf("IsNonCanonicalV8APIKeysConfigRequest(%q) = %v, want %v", test.target, got, test.refused)
		}
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
		{name: "options", method: http.MethodOptions, target: "/v8/management/config/api-keys", want: http.StatusMethodNotAllowed},
		{name: "config root", method: http.MethodGet, target: "/v8/management/config", want: http.StatusNotFound},
		{name: "client keys", method: http.MethodGet, target: "/v8/management/config/access/api-keys", want: http.StatusNotFound},
		{name: "sibling", method: http.MethodGet, target: "/v8/management/config/api-keys-x", want: http.StatusNotFound},
		{name: "duplicate slash", method: http.MethodGet, target: "/v8/management/config/api-keys//claude", want: http.StatusNotFound},
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

func TestV8ModelsRefreshPathAndPredicates(t *testing.T) {
	tests := []struct {
		target    string
		canonical bool
		refused   bool
	}{
		{target: "/v8/management/models/refresh", canonical: true},
		{target: "/v8/management/models/refresh/extra"},
		{target: "/v8/management/models/refresh-x"},
		{target: "/v8/management/models"},
		{target: "/v8//management/models/refresh", refused: true},
		{target: "//v8/management/models/refresh", refused: true},
		{target: "/v8/management/models/./refresh", refused: true},
		{target: "/v8/management/models/refresh/../config"},
		{target: "/v8/management/models/%2e%2e/models/refresh", refused: true},
	}
	for _, test := range tests {
		req := httptest.NewRequest(http.MethodPost, test.target, nil)
		if got := IsCanonicalV8ModelsRefreshRequest(req); got != test.canonical {
			t.Fatalf("IsCanonicalV8ModelsRefreshRequest(%q) = %v, want %v", test.target, got, test.canonical)
		}
		if got := IsNonCanonicalV8ModelsRefreshRequest(req); got != test.refused {
			t.Fatalf("IsNonCanonicalV8ModelsRefreshRequest(%q) = %v, want %v", test.target, got, test.refused)
		}
	}
}

func TestProxyV8ModelsRefreshForwardsOnlyAdminPosts(t *testing.T) {
	const savedKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	fixture := newPluginProxyFixture(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	methodNotAllowed := func(w http.ResponseWriter) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}

	recorder := httptest.NewRecorder()
	fixture.service.ProxyV8ModelsRefresh(
		recorder,
		httptest.NewRequest(http.MethodPost, "/v8/management/models/refresh", nil),
		writeProxyError,
		methodNotAllowed,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body=%q)", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if len(*fixture.upstreamPaths) != 1 || (*fixture.upstreamPaths)[0] != "/v8/management/models/refresh" {
		t.Fatalf("upstream paths = %#v, want [/v8/management/models/refresh]", *fixture.upstreamPaths)
	}
	if len(*fixture.upstreamAuthorizations) != 1 || (*fixture.upstreamAuthorizations)[0] != "Bearer "+savedKey {
		t.Fatalf("upstream auth = %#v, want saved management key", *fixture.upstreamAuthorizations)
	}

	for _, test := range []struct {
		name   string
		method string
		target string
		want   int
	}{
		{name: "get", method: http.MethodGet, target: "/v8/management/models/refresh", want: http.StatusMethodNotAllowed},
		{name: "put", method: http.MethodPut, target: "/v8/management/models/refresh", want: http.StatusMethodNotAllowed},
		{name: "delete", method: http.MethodDelete, target: "/v8/management/models/refresh", want: http.StatusMethodNotAllowed},
		{name: "trailing slash", method: http.MethodPost, target: "/v8/management/models/refresh/", want: http.StatusNotFound},
		{name: "nested path", method: http.MethodPost, target: "/v8/management/models/refresh/extra", want: http.StatusNotFound},
		{name: "duplicate slash", method: http.MethodPost, target: "/v8//management/models/refresh", want: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := len(*fixture.upstreamPaths)
			rr := httptest.NewRecorder()
			fixture.service.ProxyV8ModelsRefresh(
				rr,
				httptest.NewRequest(test.method, test.target, nil),
				writeProxyError,
				methodNotAllowed,
			)
			if rr.Code != test.want {
				t.Fatalf("status = %d, want %d (body=%q)", rr.Code, test.want, rr.Body.String())
			}
			if len(*fixture.upstreamPaths) != before {
				t.Fatalf("%s %s reached upstream unexpectedly", test.method, test.target)
			}
		})
	}
}

func TestV8OAuthPathAndPredicates(t *testing.T) {
	methodTests := []struct {
		path       string
		wantPath   bool
		wantMethod string
	}{
		{path: "/v8/management/oauth/siwc/bind", wantPath: true, wantMethod: http.MethodPost},
		{path: "/v8/management/oauth/callback", wantPath: true, wantMethod: http.MethodPost},
		{path: "/v8/management/oauth/status", wantPath: true, wantMethod: http.MethodGet},
		{path: "/v8/management/oauth/session", wantPath: true, wantMethod: http.MethodDelete},
		{path: "/v8/management/oauth", wantPath: false, wantMethod: ""},
		{path: "/v8/management/oauth/", wantPath: false, wantMethod: ""},
		{path: "/v8/management/oauth/siwc", wantPath: false, wantMethod: ""},
		{path: "/v8/management/oauth/siwc/bind/", wantPath: false, wantMethod: ""},
		{path: "/v8/management/oauth/siwc/bind/extra", wantPath: false, wantMethod: ""},
		{path: "/v8/management/oauth/status/", wantPath: false, wantMethod: ""},
		{path: "/v8/management/oauth/status/extra", wantPath: false, wantMethod: ""},
		{path: "/v8/management/oauth-x", wantPath: false, wantMethod: ""},
		{path: "", wantPath: false, wantMethod: ""},
	}
	for _, test := range methodTests {
		if got := IsCPAV8OAuthPath(test.path); got != test.wantPath {
			t.Fatalf("IsCPAV8OAuthPath(%q) = %v, want %v", test.path, got, test.wantPath)
		}
		if got := CPAV8OAuthAllowedMethod(test.path); got != test.wantMethod {
			t.Fatalf("CPAV8OAuthAllowedMethod(%q) = %q, want %q", test.path, got, test.wantMethod)
		}
	}

	predicateTests := []struct {
		target    string
		canonical bool
		refused   bool
	}{
		{target: "/v8/management/oauth/siwc/bind", canonical: true},
		{target: "/v8/management/oauth/callback", canonical: true},
		{target: "/v8/management/oauth/status?state=st_123", canonical: true},
		{target: "/v8/management/oauth/session?state=st_123", canonical: true},
		{target: "/v8/management/oauth/siwc/bind/", refused: true},
		{target: "/v8/management/oauth/callback/", refused: true},
		{target: "/v8/management/oauth/status/", refused: true},
		{target: "/v8/management/oauth/session/", refused: true},
		{target: "/v8//management/oauth/siwc/bind", refused: true},
		{target: "//v8/management/oauth/callback", refused: true},
		{target: "/v8/management/oauth//status", refused: true},
		{target: "/v8/management/oauth/./session", refused: true},
		{target: "/v8/management/oauth/siwc/bind/../callback", refused: true},
		{target: "/v8/management/oauth/siwc/bind/../../credentials", refused: true},
		{target: "/v8/management/oauth/%2e%2e/config", refused: true},
		{target: "/v8/management/oauth/siwc%2Fbind", refused: true},
		{target: "/v8/management/oauth/%2E%2E/config", refused: true},
		{target: "/v8/management/oauth/siwc%2fbind", refused: true},
		{target: "/v8/management/oauth/%73tatus", refused: true},
		{target: "/v8/management/oauth/siwc/%62ind", refused: true},
		{target: "/v8/management/%2e%2e/management/oauth/status", refused: true},
		// Clean syntax outside the 4 exact paths: answered by router's ordinary 404.
		{target: "/v8/management/oauth"},
		{target: "/v8/management/oauth/siwc"},
		{target: "/v8/management/oauth/siwc/bind/extra"},
		{target: "/v8/management/oauth/unknown"},
		{target: "/v1/chat/completions"},
		{target: "/v8/management//credentials"},
	}
	for _, test := range predicateTests {
		req := httptest.NewRequest(http.MethodGet, test.target, nil)
		if got := IsCanonicalV8OAuthRequest(req); got != test.canonical {
			t.Fatalf("IsCanonicalV8OAuthRequest(%q) = %v, want %v", test.target, got, test.canonical)
		}
		if got := IsNonCanonicalV8OAuthRequest(req); got != test.refused {
			t.Fatalf("IsNonCanonicalV8OAuthRequest(%q) = %v, want %v", test.target, got, test.refused)
		}
	}
	if IsCanonicalV8OAuthRequest(nil) || IsNonCanonicalV8OAuthRequest(nil) {
		t.Fatal("nil request must not match V8 OAuth predicates")
	}
}

func TestProxyV8OAuthForwardsOnlyExactAllowedMethodsAndPreservesQuery(t *testing.T) {
	const savedKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	type observed struct {
		method string
		path   string
		query  string
		body   string
	}
	var seen []observed
	fixture := newPluginProxyFixture(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen = append(seen, observed{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
			body:   string(raw),
		})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	methodNotAllowed := func(w http.ResponseWriter) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}

	validCalls := []struct {
		method    string
		target    string
		body      string
		wantPath  string
		wantQuery string
	}{
		{
			method:   http.MethodPost,
			target:   "/v8/management/oauth/siwc/bind",
			body:     `{"name":"codex-acct.json"}`,
			wantPath: "/v8/management/oauth/siwc/bind",
		},
		{
			method:   http.MethodPost,
			target:   "/v8/management/oauth/callback",
			body:     `{"provider":"codex","redirect_url":"http://127.0.0.1:1455/auth/callback?state=st_1&code=cd_1"}`,
			wantPath: "/v8/management/oauth/callback",
		},
		{
			method:    http.MethodGet,
			target:    "/v8/management/oauth/status?state=st_1",
			wantPath:  "/v8/management/oauth/status",
			wantQuery: "state=st_1",
		},
		{
			method:    http.MethodDelete,
			target:    "/v8/management/oauth/session?state=st_1",
			wantPath:  "/v8/management/oauth/session",
			wantQuery: "state=st_1",
		},
	}
	for idx, call := range validCalls {
		req := httptest.NewRequest(call.method, call.target, strings.NewReader(call.body))
		req.Header.Set("Authorization", "Bearer caller-admin-token")
		rr := httptest.NewRecorder()
		fixture.service.ProxyV8OAuth(rr, req, writeProxyError, methodNotAllowed)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s %s status = %d, want 200 (body=%q)", call.method, call.target, rr.Code, rr.Body.String())
		}
		if len(seen) != idx+1 {
			t.Fatalf("seen count = %d, want %d", len(seen), idx+1)
		}
		got := seen[idx]
		if got.method != call.method || got.path != call.wantPath || got.query != call.wantQuery || got.body != call.body {
			t.Fatalf("upstream call %d = %#v, want method=%q path=%q query=%q body=%q", idx, got, call.method, call.wantPath, call.wantQuery, call.body)
		}
		if (*fixture.upstreamAuthorizations)[idx] != "Bearer "+savedKey {
			t.Fatalf("upstream auth %d = %q, want saved management key", idx, (*fixture.upstreamAuthorizations)[idx])
		}
	}

	for _, test := range []struct {
		name   string
		method string
		target string
		want   int
	}{
		{name: "bind get", method: http.MethodGet, target: "/v8/management/oauth/siwc/bind", want: http.StatusMethodNotAllowed},
		{name: "bind delete", method: http.MethodDelete, target: "/v8/management/oauth/siwc/bind", want: http.StatusMethodNotAllowed},
		{name: "callback get", method: http.MethodGet, target: "/v8/management/oauth/callback", want: http.StatusMethodNotAllowed},
		{name: "callback put", method: http.MethodPut, target: "/v8/management/oauth/callback", want: http.StatusMethodNotAllowed},
		{name: "status post", method: http.MethodPost, target: "/v8/management/oauth/status?state=st_1", want: http.StatusMethodNotAllowed},
		{name: "status delete", method: http.MethodDelete, target: "/v8/management/oauth/status?state=st_1", want: http.StatusMethodNotAllowed},
		{name: "session get", method: http.MethodGet, target: "/v8/management/oauth/session?state=st_1", want: http.StatusMethodNotAllowed},
		{name: "session post", method: http.MethodPost, target: "/v8/management/oauth/session?state=st_1", want: http.StatusMethodNotAllowed},
		{name: "options status", method: http.MethodOptions, target: "/v8/management/oauth/status", want: http.StatusMethodNotAllowed},
		{name: "trailing slash bind", method: http.MethodPost, target: "/v8/management/oauth/siwc/bind/", want: http.StatusNotFound},
		{name: "trailing slash status", method: http.MethodGet, target: "/v8/management/oauth/status/", want: http.StatusNotFound},
		{name: "nested subpath", method: http.MethodPost, target: "/v8/management/oauth/siwc/bind/extra", want: http.StatusNotFound},
		{name: "oauth root", method: http.MethodGet, target: "/v8/management/oauth", want: http.StatusNotFound},
		{name: "duplicate slash", method: http.MethodGet, target: "/v8//management/oauth/status", want: http.StatusNotFound},
		{name: "encoded dot segment", method: http.MethodGet, target: "/v8/management/oauth/%2e%2e/config", want: http.StatusNotFound},
		{name: "encoded slash", method: http.MethodPost, target: "/v8/management/oauth/siwc%2Fbind", want: http.StatusNotFound},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := len(*fixture.upstreamPaths)
			rr := httptest.NewRecorder()
			fixture.service.ProxyV8OAuth(
				rr,
				httptest.NewRequest(test.method, test.target, nil),
				writeProxyError,
				methodNotAllowed,
			)
			if rr.Code != test.want {
				t.Fatalf("status = %d, want %d (body=%q)", rr.Code, test.want, rr.Body.String())
			}
			if len(*fixture.upstreamPaths) != before {
				t.Fatalf("%s %s reached upstream unexpectedly", test.method, test.target)
			}
		})
	}

	ctx, mark := WithSavedKeyMark(t.Context())
	markedReq := httptest.NewRequest(http.MethodGet, "/v8/management/oauth/status?state=st_mark", nil).WithContext(ctx)
	fixture.service.ProxyV8OAuth(httptest.NewRecorder(), markedReq, writeProxyError, methodNotAllowed)
	if !mark.Attached() {
		t.Fatal("expected saved management key mark attached on forwarded V8 OAuth request")
	}

	unconfiguredStore, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = unconfiguredStore.Close() })
	unconfiguredSvc := New(managerconfig.New(config.Config{}, unconfiguredStore, nil), unconfiguredStore)
	unconfiguredRR := httptest.NewRecorder()
	unconfiguredSvc.ProxyV8OAuth(
		unconfiguredRR,
		httptest.NewRequest(http.MethodPost, "/v8/management/oauth/siwc/bind", strings.NewReader(`{"name":"a.json"}`)),
		writeProxyError,
		methodNotAllowed,
	)
	if unconfiguredRR.Code != http.StatusPreconditionRequired {
		t.Fatalf("unconfigured status = %d, want %d", unconfiguredRR.Code, http.StatusPreconditionRequired)
	}
}

func TestV8CredentialsPathAndPredicates(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{path: "/v8/management/credentials", want: true},
		{path: "/v8/management/credentials/", want: false},
		{path: "/v8/management/credentials/verify", want: false},
		{path: "/v8/management/credentials/status", want: false},
		{path: "/v8/management/credentials/fields", want: false},
		{path: "/v8/management/credentials-x", want: false},
		{path: "", want: false},
	} {
		if got := IsCPAV8CredentialsPath(tc.path); got != tc.want {
			t.Fatalf("IsCPAV8CredentialsPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}

	predicateTests := []struct {
		target    string
		canonical bool
		refused   bool
	}{
		{target: "/v8/management/credentials", canonical: true},
		{target: "/v8/management/credentials?name=codex.json", canonical: true},
		{target: "/v8/management/credentials/", refused: true},
		{target: "/v8//management/credentials", refused: true},
		{target: "//v8/management/credentials", refused: true},
		{target: "/v8/management/./credentials", refused: true},
		{target: "/v8/management/credentials/../config", refused: true},
		{target: "/v8/management/credentials/%2e%2e/config", refused: true},
		{target: "/v8/management/credentials/%2E%2E/config", refused: true},
		{target: "/v8/management/%63redentials", refused: true},
		{target: "/v8/management/credentials%2fverify", refused: true},
		{target: "/v8/management/credentials/verify"},
		{target: "/v8/management/credentials/status"},
		{target: "/v8/management/credentials/fields"},
		{target: "/v8/management/credentials-x"},
		{target: "/v8/management/oauth/status"},
	}
	for _, tc := range predicateTests {
		req := httptest.NewRequest(http.MethodGet, tc.target, nil)
		if got := IsCanonicalV8CredentialsRequest(req); got != tc.canonical {
			t.Fatalf("IsCanonicalV8CredentialsRequest(%q) = %v, want %v", tc.target, got, tc.canonical)
		}
		if got := IsNonCanonicalV8CredentialsRequest(req); got != tc.refused {
			t.Fatalf("IsNonCanonicalV8CredentialsRequest(%q) = %v, want %v", tc.target, got, tc.refused)
		}
	}
	if IsCanonicalV8CredentialsRequest(nil) || IsNonCanonicalV8CredentialsRequest(nil) {
		t.Fatal("nil request must not match V8 credentials predicates")
	}
}

func TestProxyV8CredentialsForwardsOnlyCanonicalAdminGets(t *testing.T) {
	const savedKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	type observed struct {
		method string
		path   string
		query  string
	}
	var seen []observed
	fixture := newPluginProxyFixture(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, observed{
			method: r.Method,
			path:   r.URL.Path,
			query:  r.URL.RawQuery,
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[{"name":"codex.json","siwc_bind_supported":true}]}`))
	})
	methodNotAllowed := func(w http.ResponseWriter) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}

	for idx, target := range []struct {
		rawURL    string
		wantQuery string
	}{
		{rawURL: "/v8/management/credentials"},
		{rawURL: "/v8/management/credentials?name=codex.json", wantQuery: "name=codex.json"},
	} {
		req := httptest.NewRequest(http.MethodGet, target.rawURL, nil)
		req.Header.Set("Authorization", "Bearer caller-admin-token")
		rr := httptest.NewRecorder()
		fixture.service.ProxyV8Credentials(rr, req, writeProxyError, methodNotAllowed)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200 (body=%q)", target.rawURL, rr.Code, rr.Body.String())
		}
		if len(seen) != idx+1 {
			t.Fatalf("seen count = %d, want %d", len(seen), idx+1)
		}
		if seen[idx].method != http.MethodGet || seen[idx].path != "/v8/management/credentials" || seen[idx].query != target.wantQuery {
			t.Fatalf("upstream call %d = %#v", idx, seen[idx])
		}
		if (*fixture.upstreamAuthorizations)[idx] != "Bearer "+savedKey {
			t.Fatalf("upstream auth %d = %q, want saved management key", idx, (*fixture.upstreamAuthorizations)[idx])
		}
	}

	for _, tc := range []struct {
		name   string
		method string
		target string
		want   int
	}{
		{name: "post", method: http.MethodPost, target: "/v8/management/credentials", want: http.StatusMethodNotAllowed},
		{name: "put", method: http.MethodPut, target: "/v8/management/credentials", want: http.StatusMethodNotAllowed},
		{name: "patch", method: http.MethodPatch, target: "/v8/management/credentials", want: http.StatusMethodNotAllowed},
		{name: "delete", method: http.MethodDelete, target: "/v8/management/credentials", want: http.StatusMethodNotAllowed},
		{name: "options", method: http.MethodOptions, target: "/v8/management/credentials", want: http.StatusMethodNotAllowed},
		{name: "trailing slash", method: http.MethodGet, target: "/v8/management/credentials/", want: http.StatusNotFound},
		{name: "verify subpath", method: http.MethodPost, target: "/v8/management/credentials/verify", want: http.StatusNotFound},
		{name: "status subpath", method: http.MethodPatch, target: "/v8/management/credentials/status", want: http.StatusNotFound},
		{name: "duplicate slash", method: http.MethodGet, target: "/v8//management/credentials", want: http.StatusNotFound},
		{name: "encoded unreserved", method: http.MethodGet, target: "/v8/management/%63redentials", want: http.StatusNotFound},
		{name: "encoded dot segment", method: http.MethodGet, target: "/v8/management/credentials/%2e%2e/config", want: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := len(*fixture.upstreamPaths)
			rr := httptest.NewRecorder()
			fixture.service.ProxyV8Credentials(
				rr,
				httptest.NewRequest(tc.method, tc.target, nil),
				writeProxyError,
				methodNotAllowed,
			)
			if rr.Code != tc.want {
				t.Fatalf("status = %d, want %d (body=%q)", rr.Code, tc.want, rr.Body.String())
			}
			if len(*fixture.upstreamPaths) != before {
				t.Fatalf("%s %s reached upstream unexpectedly", tc.method, tc.target)
			}
		})
	}

	ctx, mark := WithSavedKeyMark(t.Context())
	markedReq := httptest.NewRequest(http.MethodGet, "/v8/management/credentials", nil).WithContext(ctx)
	fixture.service.ProxyV8Credentials(httptest.NewRecorder(), markedReq, writeProxyError, methodNotAllowed)
	if !mark.Attached() {
		t.Fatal("expected saved management key mark attached on forwarded V8 credentials request")
	}

	unconfiguredStore, err := store.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = unconfiguredStore.Close() })
	unconfiguredSvc := New(managerconfig.New(config.Config{}, unconfiguredStore, nil), unconfiguredStore)
	unconfiguredRR := httptest.NewRecorder()
	unconfiguredSvc.ProxyV8Credentials(
		unconfiguredRR,
		httptest.NewRequest(http.MethodGet, "/v8/management/credentials", nil),
		writeProxyError,
		methodNotAllowed,
	)
	if unconfiguredRR.Code != http.StatusPreconditionRequired {
		t.Fatalf("unconfigured status = %d, want %d", unconfiguredRR.Code, http.StatusPreconditionRequired)
	}
}
