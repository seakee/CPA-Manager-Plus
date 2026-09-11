package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/testutil"
)

func TestUpdateEndpointsRequirePanelAuthentication(t *testing.T) {
	handler := newTestHandler(t, "", false)
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/usage-service/updates", ""},
		{"POST", "/usage-service/updates/check", ""},
		{"POST", "/usage-service/updates/notification", ""},
		{"POST", "/usage-service/updates/dismiss", `{"version":"v2.0.0"}`},
		{"PUT", "/usage-service/updates/channel", `{"channel_preference":"beta"}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusUnauthorized {
			t.Fatalf("%s: %d", tc.path, res.Code)
		}
		req = httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+testutil.AdminKey)
		res = httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != http.StatusOK || res.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: %d %s", tc.path, res.Code, res.Body.String())
		}
	}
}
