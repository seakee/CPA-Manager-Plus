package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/router"
)

func TestWriteCORSAllowsSupportedMethods(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/usage-service/account-processing-policy", nil)
	middleware.WriteCORS(config.Config{CORSOrigins: []string{"*"}}, rr, req)

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

	handler := middleware.WithCORS(config.Config{CORSOrigins: []string{"*"}}, func(w http.ResponseWriter, r *http.Request) {
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

func TestWriteCORSAllowsIfMatchAndExposesETag(t *testing.T) {
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v8/management/config/api-keys/claude", nil)
	req.Header.Set("Origin", "http://example.com")
	middleware.WriteCORS(config.Config{CORSOrigins: []string{"http://example.com"}}, rr, req)

	allowHeaders := strings.ToLower(rr.Header().Get("Access-Control-Allow-Headers"))
	if !strings.Contains(allowHeaders, "if-match") {
		t.Fatalf("Access-Control-Allow-Headers = %q, want If-Match", rr.Header().Get("Access-Control-Allow-Headers"))
	}
	exposeHeaders := strings.ToLower(rr.Header().Get("Access-Control-Expose-Headers"))
	for _, expected := range []string{
		"etag",
		"x-cpa-version",
		"x-cpa-commit",
		"x-cpa-build-date",
		"x-cpa-support-plugin",
		"x-server-version",
		"x-server-commit",
		"x-server-build-date",
	} {
		if !strings.Contains(exposeHeaders, expected) {
			t.Fatalf("Access-Control-Expose-Headers = %q, missing %s", rr.Header().Get("Access-Control-Expose-Headers"), expected)
		}
	}
}

func TestRouterV8PreflightAdmitsCanonicalEndpointsAndRejectsTrailingSlashOrUnexposedSubtreePaths(t *testing.T) {
	appCtx := &app.Context{
		Config: config.Config{CORSOrigins: []string{"http://localhost:5173"}},
	}
	handler := router.New(appCtx)

	for _, tc := range []struct {
		name          string
		target        string
		origin        string
		requestMethod string
		wantStatus    int
	}{
		{
			name:          "canonical api-keys root GET preflight",
			target:        "/v8/management/config/api-keys",
			origin:        "http://localhost:5173",
			requestMethod: http.MethodGet,
			wantStatus:    http.StatusNoContent,
		},
		{
			name:          "canonical api-keys family PUT preflight",
			target:        "/v8/management/config/api-keys/claude",
			origin:        "http://localhost:5173",
			requestMethod: http.MethodPut,
			wantStatus:    http.StatusNoContent,
		},
		{
			name:          "canonical credentials GET preflight",
			target:        "/v8/management/credentials",
			origin:        "http://localhost:5173",
			requestMethod: http.MethodGet,
			wantStatus:    http.StatusNoContent,
		},
		{
			name:          "canonical oauth status GET preflight",
			target:        "/v8/management/oauth/status",
			origin:        "http://localhost:5173",
			requestMethod: http.MethodGet,
			wantStatus:    http.StatusNoContent,
		},
		{
			name:          "canonical models refresh POST preflight",
			target:        "/v8/management/models/refresh",
			origin:        "http://localhost:5173",
			requestMethod: http.MethodPost,
			wantStatus:    http.StatusNoContent,
		},
		{
			name:          "trailing-slash api-keys root OPTIONS returns 404",
			target:        "/v8/management/config/api-keys/",
			origin:        "http://localhost:5173",
			requestMethod: http.MethodGet,
			wantStatus:    http.StatusNotFound,
		},
		{
			name:          "trailing-slash api-keys family OPTIONS returns 404",
			target:        "/v8/management/config/api-keys/claude/",
			origin:        "http://localhost:5173",
			requestMethod: http.MethodPut,
			wantStatus:    http.StatusNotFound,
		},
		{
			name:          "trailing-slash models refresh OPTIONS returns 404",
			target:        "/v8/management/models/refresh/",
			origin:        "http://localhost:5173",
			requestMethod: http.MethodPost,
			wantStatus:    http.StatusNotFound,
		},
		{
			name:          "unexposed api-keys access child OPTIONS returns 404",
			target:        "/v8/management/config/api-keys/access",
			origin:        "http://localhost:5173",
			requestMethod: http.MethodGet,
			wantStatus:    http.StatusNotFound,
		},
		{
			name:          "unexposed api-keys unknown family OPTIONS returns 404",
			target:        "/v8/management/config/api-keys/unknown",
			origin:        "http://localhost:5173",
			requestMethod: http.MethodPut,
			wantStatus:    http.StatusNotFound,
		},
		{
			name:          "unexposed api-keys nested member OPTIONS returns 404",
			target:        "/v8/management/config/api-keys/claude/0",
			origin:        "http://localhost:5173",
			requestMethod: http.MethodPut,
			wantStatus:    http.StatusNotFound,
		},
		{
			name:          "unexposed credentials verify OPTIONS returns 404",
			target:        "/v8/management/credentials/verify",
			origin:        "http://localhost:5173",
			requestMethod: http.MethodPost,
			wantStatus:    http.StatusNotFound,
		},
		{
			name:          "unexposed oauth unknown OPTIONS returns 404",
			target:        "/v8/management/oauth/unknown",
			origin:        "http://localhost:5173",
			requestMethod: http.MethodGet,
			wantStatus:    http.StatusNotFound,
		},
		{
			name:          "non-subtree route keeps generic 204 preflight",
			target:        "/v8/management/config/access/api-keys",
			origin:        "http://localhost:5173",
			requestMethod: http.MethodGet,
			wantStatus:    http.StatusNoContent,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodOptions, tc.target, nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.requestMethod != "" {
				req.Header.Set("Access-Control-Request-Method", tc.requestMethod)
			}
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("OPTIONS %s status = %d, want %d", tc.target, rr.Code, tc.wantStatus)
			}
			if loc := rr.Header().Get("Location"); loc != "" {
				t.Fatalf("OPTIONS %s redirected to %q", tc.target, loc)
			}
		})
	}
}
