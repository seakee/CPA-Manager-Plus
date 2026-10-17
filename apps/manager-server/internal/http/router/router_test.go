package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	routingdomain "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/routing"
)

func TestRouterCapturesFixedDiscoveryBeforeMuxNormalization(t *testing.T) {
	handler := New(&app.Context{})
	for _, target := range []string{
		routingdomain.BootstrapDiscoveryPath,
		routingdomain.BootstrapDiscoveryPath + "/",
		"/.well-known//cpamp-bootstrap",
		"/.well-known%2Fcpamp-bootstrap",
	} {
		req := httptest.NewRequest(http.MethodGet, "http://example.test"+target, nil)
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want generic 404", target, res.Code)
		}
		if res.Header().Get("Location") != "" {
			t.Fatalf("%s redirected", target)
		}
	}
}

func TestLegacyRouterExplicitModeAcknowledgement(t *testing.T) {
	h := New(&app.Context{})
	for _, tc := range []struct {
		method, target string
		code           int
	}{
		{"GET", "/internal/routing/fresh-authority", 204},
		{"PUT", "/internal/routing/fresh-authority", 405},
		{"GET", "/internal/routing/fresh-authority?legacy=true", 404},
		{"GET", "/internal/routing/fresh%2dauthority", 404},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(tc.method, tc.target, nil))
		if w.Code != tc.code || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(tc, w.Code, w.Header())
		}
	}
}
