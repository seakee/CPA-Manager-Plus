package upgradeauth

import (
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

func TestExplicitPublicOriginSupportsTLSIngressWithoutTrustingForwardedHeaders(t *testing.T) {
	paths := legacypreflight.DefaultPaths(filepath.Join(t.TempDir(), "missing.sqlite"))
	for _, tc := range []struct {
		configured, origin, forwarded string
		want                          int
	}{
		{"", "http://manager.test", "", 401},
		{"", "https://manager.test", "https", 403},
		{"https://public-manager.test/", "https://public-manager.test", "", 401},
		{"https://public-manager.test", "http://manager.test", "", 403},
		{"https://public-manager.test", "https://attacker.test", "https", 403},
	} {
		h, err := New(paths, tc.configured)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", Path, nil)
		r.Host = "manager.test"
		r.Header.Set("Authorization", "Bearer test-key")
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-Forwarded-Proto", tc.forwarded)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want || w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatalf("%+v: %d", tc, w.Code)
		}
	}
	for _, invalid := range []string{"null", "file:///tmp/source", "https://user:pass@manager.test", "https://manager.test/path", "https://manager.test?key=test"} {
		if _, err := New(paths, invalid); err == nil {
			t.Fatalf("accepted invalid origin %q", invalid)
		}
	}
}
