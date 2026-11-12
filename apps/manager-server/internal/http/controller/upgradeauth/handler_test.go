package upgradeauth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
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

func TestBlockingOwnershipLookupHonorsCancellationDeadlineAndSingleFlight(t *testing.T) {
	managerDir := t.TempDir()
	legacyRoot := filepath.Join(t.TempDir(), "legacy-never-touched")
	paths := legacypreflight.DefaultPaths(filepath.Join(managerDir, "usage.sqlite"))

	t.Run("caller cancellation and single-flight coalescing", func(t *testing.T) {
		h, err := New(paths, "https://public-manager.test", false, legacyRoot)
		if err != nil {
			t.Fatal(err)
		}
		var (
			calls   int32
			active  int32
			peak    int32
			probed  atomic.Value
			entered = make(chan struct{})
			release = make(chan struct{})
			exited  = make(chan struct{})
		)
		h.probeOwnership = func(got sqlite.InspectionPaths, checkDataKey bool) bool {
			probed.Store(got.Database)
			if checkDataKey {
				t.Errorf("unpreserved manager store unexpectedly requested data.key check")
			}
			n := atomic.AddInt32(&active, 1)
			for {
				cur := atomic.LoadInt32(&peak)
				if n <= cur || atomic.CompareAndSwapInt32(&peak, cur, n) {
					break
				}
			}
			if atomic.AddInt32(&calls, 1) == 1 {
				close(entered)
			}
			<-release
			atomic.AddInt32(&active, -1)
			close(exited)
			return true
		}

		ctx1, cancel1 := context.WithCancel(context.Background())
		defer cancel1()

		newReq := func(ctx context.Context) *http.Request {
			r := httptest.NewRequest(http.MethodPost, Path, nil).WithContext(ctx)
			r.Host = "public-manager.test"
			r.Header.Set("Authorization", "Bearer test-key")
			r.Header.Set("Origin", "https://public-manager.test")
			return r
		}

		rec1 := httptest.NewRecorder()
		done1 := make(chan struct{})
		go func() {
			defer close(done1)
			h.ServeHTTP(rec1, newReq(ctx1))
		}()
		<-entered

		// Second caller arrives while flight #1 is blocked, joins the existing
		// flight without starting a second probe, and exits on its own context
		// deadline while flight #1 remains blocked.
		ctx2, cancel2 := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel2()
		rec2 := httptest.NewRecorder()
		h.ServeHTTP(rec2, newReq(ctx2))

		// First caller exits immediately on explicit caller cancellation while
		// release is still unclosed.
		cancel1()
		select {
		case <-done1:
		case <-time.After(2 * time.Second):
			close(release)
			t.Fatal("ServeHTTP held cancelled request while ownership lookup was blocked")
		}

		if got, _ := probed.Load().(string); got != paths.Database {
			close(release)
			t.Fatalf("ownership lookup probed %q, want bound source %q", got, paths.Database)
		}
		for i, rec := range []*httptest.ResponseRecorder{rec1, rec2} {
			if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"code":"source_unavailable"`) {
				close(release)
				t.Fatalf("request %d = %d %s, want 503 source_unavailable", i+1, rec.Code, rec.Body.String())
			}
		}
		if c, p := atomic.LoadInt32(&calls), atomic.LoadInt32(&peak); c != 1 || p != 1 {
			close(release)
			t.Fatalf("ownership flight calls=%d peak=%d, want 1 and 1", c, p)
		}

		close(release)
		<-exited
	})

	t.Run("inspection deadline bounds blocked lookup without misclassifying ownership", func(t *testing.T) {
		h, err := New(paths, "https://public-manager.test", true, managerDir)
		if err != nil {
			t.Fatal(err)
		}
		h.InspectionTimeout = 20 * time.Millisecond
		release := make(chan struct{})
		exited := make(chan struct{})
		h.probeOwnership = func(sqlite.InspectionPaths, bool) bool {
			<-release
			close(exited)
			return true
		}

		r := httptest.NewRequest(http.MethodPost, Path, nil)
		r.Host = "public-manager.test"
		r.Header.Set("Authorization", "Bearer test-key")
		r.Header.Set("Origin", "https://public-manager.test")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)

		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"source_unavailable"`) {
			close(release)
			t.Fatalf("timed-out ownership lookup = %d %s, want 503 source_unavailable", w.Code, w.Body.String())
		}
		close(release)
		<-exited
	})
}
