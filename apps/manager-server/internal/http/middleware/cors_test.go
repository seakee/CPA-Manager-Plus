package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
)

func TestWriteCORSAllowsSupportedMethods(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/usage-service/account-processing-policy", nil)
	WriteCORS(config.Config{CORSOrigins: []string{"*"}}, rr, req)

	methods := rr.Header().Get("Access-Control-Allow-Methods")
	for _, method := range []string{"HEAD", "PATCH"} {
		if !strings.Contains(methods, method) {
			t.Fatalf("Access-Control-Allow-Methods = %q, want %s", methods, method)
		}
	}
}

func TestWriteCORSAllowsResumableImportHeader(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/v0/management/usage/import-sessions/session-1/chunk?offset=1024", nil)
	req.Header.Set("Origin", "http://example.com")
	req.Header.Set("Access-Control-Request-Method", "PUT")
	req.Header.Set("Access-Control-Request-Headers", "authorization,content-type,x-usage-import-prefix-sha256")

	handler := WithCORS(config.Config{CORSOrigins: []string{"*"}}, func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("unexpected handler invocation on OPTIONS preflight")
	})
	handler(rr, req)

	if rr.Code != 204 {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if origin := rr.Header().Get("Access-Control-Allow-Origin"); origin != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", origin)
	}
	headers := rr.Header().Get("Access-Control-Allow-Headers")
	if !strings.Contains(strings.ToLower(headers), "x-usage-import-prefix-sha256") {
		t.Fatalf("Access-Control-Allow-Headers = %q, want X-Usage-Import-Prefix-SHA256", headers)
	}
}
