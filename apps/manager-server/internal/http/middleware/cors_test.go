package middleware

import (
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
