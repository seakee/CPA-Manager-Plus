package upgradeauth

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

func TestExplicitPublicOriginSupportsTLSIngressWithoutTrustingForwardedHeaders(t *testing.T) {
	// The source is absent, so a request that clears the origin rule gets the
	// typed source answer. The origin verdict is what this table covers: a
	// same-origin browser reaches the credential path, a cross-site one never
	// does.
	paths := legacypreflight.DefaultPaths(filepath.Join(t.TempDir(), "missing.sqlite"))
	for _, tc := range []struct {
		configured, origin, forwarded, wantOrigin string
		want                                      int
	}{
		{"", "http://manager.test", "", "", 503},
		{"", "https://manager.test", "https", "", 403},
		{"https://public-manager.test/", "https://public-manager.test", "", "", 503},
		{"https://public-manager.test/", "http://127.0.0.1:38491", "", "https://public-manager.test", 403},
		{"https://public-manager.test", "http://manager.test", "", "https://public-manager.test", 403},
		{"https://public-manager.test", "https://attacker.test", "https", "https://public-manager.test", 403},
	} {
		h, err := New(paths, tc.configured, false, "")
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
		if tc.want == 403 {
			if !strings.Contains(w.Body.String(), `"code":"origin_rejected"`) {
				t.Fatalf("%+v missing origin_rejected code in %s", tc, w.Body.String())
			}
			if tc.wantOrigin != "" && !strings.Contains(w.Body.String(), `"expected_origin":"`+tc.wantOrigin+`"`) {
				t.Fatalf("%+v missing expected_origin in %s", tc, w.Body.String())
			}
			if tc.wantOrigin == "" && strings.Contains(w.Body.String(), `"expected_origin"`) {
				t.Fatalf("%+v unexpectedly emitted expected_origin in %s", tc, w.Body.String())
			}
		}
	}
	for _, invalid := range []string{"null", "file:///tmp/source", "https://user:pass@manager.test", "https://manager.test/path", "https://manager.test?key=test"} {
		if _, err := New(paths, invalid, false, ""); err == nil {
			t.Fatalf("accepted invalid origin %q", invalid)
		}
	}
}

func TestPermissionDeniedSourceReportsSourceOwnershipUnavailable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses 0000 file permissions")
	}
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "usage.sqlite"))
	if err := os.WriteFile(paths.Database, []byte("sqlite-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.DataKey, []byte("legacy-data-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	lockPath := paths.Database + ".manager.lock"
	if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(lockPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockPath, 0o600) })

	h, err := New(paths, "https://public-manager.test", true, dir)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, Path, nil)
	r.Host = "public-manager.test"
	r.Header.Set("Authorization", "Bearer test-key")
	r.Header.Set("Origin", "https://public-manager.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"source_ownership_unavailable"`) {
		t.Fatalf("permission-denied lock got %d %s, want 503 source_ownership_unavailable", w.Code, w.Body.String())
	}
}
