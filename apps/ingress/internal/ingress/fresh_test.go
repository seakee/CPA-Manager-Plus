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
	for _, target := range []string{"/", "/v1", "/.well-known/cpamp-bootstrap/nested", "/health", "/internal", "/internal/routing", FreshRoutingControlPath, FreshRoutingAuthorityPath, "/other/../control", "/control%2falias"} {
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
