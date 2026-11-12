package ingress

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestFreshPrivateProjectionRequiresCurrentManagerAuthorityAndRetainsFence(t *testing.T) {
	f := &FreshRoutes{}
	var authorized FreshRouteState
	h := f.ControlHandler(func(context.Context) (FreshRouteState, error) { return authorized, nil })
	s := FreshRouteState{InstallationID: "install", Generation: "gen", OperationID: "fresh-op", Revision: 1, Target: "/private-control"}
	put := func(state FreshRouteState) int {
		data, _ := json.Marshal(state)
		r := httptest.NewRequest("PUT", FreshRoutingControlPath, bytes.NewReader(data))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	if put(s) != http.StatusConflict || f.Active() {
		t.Fatal("projection without Manager authority was accepted")
	}
	authorized = s
	if put(s) != http.StatusOK || !f.Matches(s.Target+"/api/v2/operation") || f.Matches(s.Target+"-alias") {
		t.Fatal("matching Manager checkpoint failed")
	}
	foreign := s
	foreign.OperationID = "new-prefix-operation"
	authorized = foreign
	if put(foreign) != http.StatusConflict {
		t.Fatal("foreign owner accepted")
	}
	foreign = s
	foreign.Generation = "new-gen"
	authorized = foreign
	if put(foreign) != http.StatusConflict {
		t.Fatal("foreign generation accepted")
	}
	foreign = s
	foreign.Target = "/other"
	authorized = foreign
	if put(foreign) != http.StatusConflict {
		t.Fatal("foreign target accepted")
	}
	s.Committed = true
	authorized = s
	if put(s) != http.StatusOK {
		t.Fatal("commit acknowledgement rejected")
	}
	s.Committed = false
	authorized = s
	if put(s) != http.StatusConflict {
		t.Fatal("retired state reopened")
	}
	for _, target := range []string{"/", "/v1", "/.well-known/cpamp-bootstrap/nested", "/health", "/internal", "/internal/routing", FreshRoutingControlPath, FreshRoutingAuthorityPath, "/other/../control", "/control%2falias", "/v8/management", "/v8/management/control"} {
		empty := &FreshRoutes{}
		bad := s
		bad.Target = target
		data, _ := json.Marshal(bad)
		r := httptest.NewRequest("PUT", FreshRoutingControlPath, bytes.NewReader(data))
		w := httptest.NewRecorder()
		empty.ControlHandler(func(context.Context) (FreshRouteState, error) { return bad, nil }).ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatal(target, w.Code)
		}
	}
}

func TestFreshPublicGatewayClosedBeforeProjectionAndFixedNamespaceCaptured(t *testing.T) {
	var managerHits, gatewayHits int
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { managerHits++; w.WriteHeader(404) }))
	defer manager.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { gatewayHits++; w.WriteHeader(200) }))
	defer gateway.Close()
	m, _ := url.Parse(manager.URL)
	g, _ := url.Parse(gateway.URL)
	routes := &FreshRoutes{}
	h := NewFreshHandler(routes, m, g, log.New(io.Discard, "", 0), nil)
	for _, p := range []string{FixedBootstrapDiscoveryPath, FixedBootstrapDiscoveryPath + "/ui", FixedBootstrapDiscoveryPath + "/assets/a-AbCd1234.js", FixedBootstrapDiscoveryPath + "/api/v2/admin/commit", FixedBootstrapDiscoveryPath + "/unknown", FixedBootstrapDiscoveryPath + "%2funknown", FixedBootstrapDiscoveryPath + "%252funknown"} {
		r := httptest.NewRequest("POST", p, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 404 || w.Header().Get("X-CPAMP-Route-Owner") != "manager" {
			t.Fatal(p, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "/v1/models", nil)
	r.Header.Set("Authorization", "Bearer admin-or-management-secret")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 || w.Header().Get("X-CPAMP-Route-Owner") != "gateway" || gatewayHits != 0 {
		t.Fatal("Gateway forwarded before the first Fresh projection")
	}
	// Once the Fresh operation publishes its routing projection, product
	// initialization and a running CPA still do not prove data-plane client
	// authentication. Keep Gateway closed until a later qualified consumer.
	routes.state = FreshRouteState{InstallationID: "i", Generation: "g", OperationID: "op", Revision: 1, Target: "/private", Committed: true}
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 503 || gatewayHits != 0 {
		t.Fatal("Fresh routing ownership activated Gateway")
	}
	if managerHits != 7 {
		t.Fatal(managerHits)
	}
}

func TestOnlyExplicitLegacyManagerAllowsGatewayWithoutFreshProjection(t *testing.T) {
	manager := httptest.NewServer(http.NotFoundHandler())
	defer manager.Close()
	hits := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits++; w.WriteHeader(http.StatusOK) }))
	defer gateway.Close()
	m, _ := url.Parse(manager.URL)
	g, _ := url.Parse(gateway.URL)
	routes := &FreshRoutes{}
	legacy := false
	h := NewFreshHandler(routes, m, g, log.New(io.Discard, "", 0), func(context.Context) bool { return legacy })
	check := func(want, wantHits int) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
		if w.Code != want || hits != wantHits {
			t.Fatal(w.Code, hits)
		}
	}
	check(503, 0)
	legacy = true
	check(200, 1)
	routes.state = FreshRouteState{OperationID: "fresh-operation"}
	check(503, 1)
}

func TestPreCommitBrowserNavigationRedirectsToBootstrapUI(t *testing.T) {
	var managerHits, gatewayHits int
	manager := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		managerHits++
		w.WriteHeader(http.StatusNotFound)
	}))
	defer manager.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gatewayHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()
	m, _ := url.Parse(manager.URL)
	g, _ := url.Parse(gateway.URL)
	routes := &FreshRoutes{}
	legacy := false
	h := NewFreshHandler(routes, m, g, log.New(io.Discard, "", 0), func(context.Context) bool { return legacy })
	bootstrapUI := FixedBootstrapDiscoveryPath + "/ui"

	do := func(method, target string, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if host, ok := headers["Host"]; ok {
			req.Host = host
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	browserAccept := "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"
	for _, tc := range []struct {
		name    string
		method  string
		target  string
		headers map[string]string
	}{
		{
			name:    "browser-get-console",
			method:  http.MethodGet,
			target:  "/console",
			headers: map[string]string{"Accept": browserAccept},
		},
		{
			name:    "browser-head-console",
			method:  http.MethodHead,
			target:  "/console",
			headers: map[string]string{"Accept": "text/html"},
		},
		{
			name:    "browser-get-console-subpath",
			method:  http.MethodGet,
			target:  "/console/login",
			headers: map[string]string{"Accept": browserAccept},
		},
		{
			name:   "xhtml-first-navigate",
			method: http.MethodGet,
			target: "/console",
			headers: map[string]string{
				"Accept":         "application/xhtml+xml, text/html;q=0.9, */*;q=0.8",
				"Sec-Fetch-Mode": "navigate",
				"Sec-Fetch-Dest": "document",
			},
		},
		{
			name:   "untrusted-host-and-forwarding-headers",
			method: http.MethodGet,
			target: "/console",
			headers: map[string]string{
				"Accept":           "text/html",
				"Host":             "untrusted.example:9999",
				"X-Forwarded-Host": "other.example",
				"Forwarded":        "host=other.example;proto=https",
			},
		},
	} {
		t.Run("pre-projection/"+tc.name, func(t *testing.T) {
			rec := do(tc.method, tc.target, tc.headers)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
			}
			if got := rec.Header().Get("Location"); got != bootstrapUI {
				t.Fatalf("Location = %q, want %q", got, bootstrapUI)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", got)
			}
			if got := rec.Header().Get("X-CPAMP-Route-Owner"); got != "gateway" {
				t.Fatalf("X-CPAMP-Route-Owner = %q, want gateway", got)
			}
			if tc.method == http.MethodHead && rec.Body.Len() != 0 {
				t.Fatalf("HEAD body len = %d, want 0", rec.Body.Len())
			}
		})
	}

	for _, tc := range []struct {
		name    string
		method  string
		target  string
		headers map[string]string
	}{
		{name: "no-accept", method: http.MethodGet, target: "/console"},
		{name: "curl-wildcard-accept", method: http.MethodGet, target: "/console", headers: map[string]string{"Accept": "*/*"}},
		{name: "json-accept", method: http.MethodGet, target: "/console", headers: map[string]string{"Accept": "application/json"}},
		{name: "json-preferred-over-html", method: http.MethodGet, target: "/console", headers: map[string]string{"Accept": "application/json, text/html;q=0.9"}},
		{name: "plain-tied-with-html", method: http.MethodGet, target: "/console", headers: map[string]string{"Accept": "text/plain, text/html"}},
		{name: "html-q-zero", method: http.MethodGet, target: "/console", headers: map[string]string{"Accept": "text/html;q=0"}},
		{name: "wildcard-preferred-over-html", method: http.MethodGet, target: "/console", headers: map[string]string{"Accept": "*/*, text/html;q=0.8"}},
		{name: "post-console-with-html-accept", method: http.MethodPost, target: "/console", headers: map[string]string{"Accept": browserAccept}},
		{name: "put-console-with-html-accept", method: http.MethodPut, target: "/console", headers: map[string]string{"Accept": browserAccept}},
		{name: "cors-fetch-mode", method: http.MethodGet, target: "/console", headers: map[string]string{"Accept": browserAccept, "Sec-Fetch-Mode": "cors", "Sec-Fetch-Dest": "empty"}},
		{name: "websocket-upgrade", method: http.MethodGet, target: "/console", headers: map[string]string{"Accept": browserAccept, "Connection": "Upgrade", "Upgrade": "websocket"}},
		{name: "model-v1-models-with-html-accept", method: http.MethodGet, target: "/v1/models", headers: map[string]string{"Accept": browserAccept}},
		{name: "model-v1beta-models-with-html-accept", method: http.MethodGet, target: "/v1beta/models", headers: map[string]string{"Accept": browserAccept}},
		{name: "model-root-models-with-html-accept", method: http.MethodGet, target: "/models", headers: map[string]string{"Accept": browserAccept}},
		{name: "encoded-model-v1-models", method: http.MethodGet, target: "/v1%2fmodels", headers: map[string]string{"Accept": browserAccept}},
	} {
		t.Run("unchanged-503/"+tc.name, func(t *testing.T) {
			rec := do(tc.method, tc.target, tc.headers)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
			}
			if got := rec.Header().Get("Location"); got != "" {
				t.Fatalf("unexpected Location = %q", got)
			}
			if got := rec.Header().Get("X-CPAMP-Route-Owner"); got != "gateway" {
				t.Fatalf("X-CPAMP-Route-Owner = %q, want gateway", got)
			}
		})
	}

	if managerHits != 0 || gatewayHits != 0 {
		t.Fatalf("unexpected upstream traffic before projection: manager=%d gateway=%d", managerHits, gatewayHits)
	}

	// Uncommitted Fresh projection on another candidate path still redirects /console browser navigations.
	routes.state = FreshRouteState{InstallationID: "i", Generation: "g", OperationID: "op", Revision: 1, Target: "/private", Committed: false}
	if rec := do(http.MethodGet, "/console", map[string]string{"Accept": browserAccept}); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != bootstrapUI {
		t.Fatalf("uncommitted projection navigation = %d Location=%q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := do(http.MethodGet, "/console", map[string]string{"Accept": "*/*"}); rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Location") != "" {
		t.Fatalf("uncommitted projection non-HTML = %d Location=%q", rec.Code, rec.Header().Get("Location"))
	}

	// Once Fresh routing is committed, unadmitted non-Control browser navigations keep 503 unchanged.
	routes.state = FreshRouteState{InstallationID: "i", Generation: "g", OperationID: "op", Revision: 1, Target: "/private", Committed: true}
	if rec := do(http.MethodGet, "/console", map[string]string{"Accept": browserAccept}); rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Location") != "" {
		t.Fatalf("committed projection navigation = %d Location=%q, want 503 without redirect", rec.Code, rec.Header().Get("Location"))
	}
	if managerHits != 0 || gatewayHits != 0 {
		t.Fatalf("unexpected upstream traffic on unadmitted routes: manager=%d gateway=%d", managerHits, gatewayHits)
	}

	// Explicit legacy Manager mode still forwards to Gateway unchanged.
	routes.state = FreshRouteState{}
	legacy = true
	if rec := do(http.MethodGet, "/console", map[string]string{"Accept": browserAccept}); rec.Code != http.StatusOK || gatewayHits != 1 {
		t.Fatalf("legacy passthrough = %d gatewayHits=%d", rec.Code, gatewayHits)
	}
}
