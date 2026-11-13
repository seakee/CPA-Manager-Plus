package upgradeauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	_ "modernc.org/sqlite"
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

func createLegacyAuthFixture(t *testing.T, adminKey string, extraDDL string) (string, sqlite.InspectionPaths) {
	t.Helper()
	dir := t.TempDir()
	database := filepath.Join(dir, "usage.sqlite")
	credential, err := security.NewAdminCredential(adminKey, "test")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`pragma journal_mode=delete;
create table settings(key text primary key, value text);
create table usage_events(id integer primary key, fail_body blob);
insert into usage_events values(1,x'00ff');`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into settings values('admin_credential_v1',?)`, string(raw)); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if extraDDL != "" {
		if _, err := db.Exec(extraDDL); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	paths := legacypreflight.DefaultPaths(database)
	if err := os.WriteFile(paths.DataKey, []byte("legacy-data-key-material"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(database+".manager.lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, paths
}

func postAuthRequest(t *testing.T, h *Handler, key string) (int, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, Path, nil)
	r.Host = "public-manager.test"
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Origin", "https://public-manager.test")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func TestSourceOwnershipUnavailableEligibilityMatrix(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores 0000 file permissions")
	}
	const legacyKey = "legacy-matrix-admin-key-123456"

	t.Run("proven 1.x mount with unreadable lock or data.key reports ownership code", func(t *testing.T) {
		for _, memberName := range []string{"lock", "data.key"} {
			t.Run(memberName, func(t *testing.T) {
				dir, paths := createLegacyAuthFixture(t, legacyKey, "")
				target := paths.DataKey
				restoreMode := os.FileMode(0o600)
				if memberName == "lock" {
					target = paths.Database + ".manager.lock"
				}
				if err := os.Chmod(target, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(target, restoreMode) })

				h, err := New(paths, "https://public-manager.test", true, dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, presented := range []string{"wrong-key", legacyKey} {
					code, body := postAuthRequest(t, h, presented)
					if code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"source_ownership_unavailable"`) {
						t.Fatalf("key %q got %d %s, want 503 source_ownership_unavailable", presented, code, body)
					}
				}
			})
		}
	})

	t.Run("1.x mount with unreadable database or journal sidecar stays generic source_unavailable", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			mutate func(t *testing.T, paths sqlite.InspectionPaths)
		}{
			{
				name: "unreadable database",
				mutate: func(t *testing.T, paths sqlite.InspectionPaths) {
					if err := os.Chmod(paths.Database, 0o000); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(paths.Database, 0o644) })
				},
			},
			{
				name: "unreadable wal sidecar",
				mutate: func(t *testing.T, paths sqlite.InspectionPaths) {
					walPath := paths.Database + "-wal"
					if err := os.WriteFile(walPath, nil, 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(walPath, 0o000); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(walPath, 0o600) })
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				dir, paths := createLegacyAuthFixture(t, legacyKey, "")
				tc.mutate(t, paths)
				h, err := New(paths, "https://public-manager.test", true, dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, presented := range []string{"wrong-key", legacyKey} {
					code, body := postAuthRequest(t, h, presented)
					if code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"source_unavailable"`) {
						t.Fatalf("%s (%s) got %d %s, want 503 source_unavailable", tc.name, presented, code, body)
					}
				}
			})
		}
	})

	t.Run("non-sqlite stub with unreadable lock or unreadable database stays generic source_unavailable", func(t *testing.T) {
		for _, unreadableDB := range []bool{false, true} {
			name := "unreadable lock"
			if unreadableDB {
				name = "unreadable database"
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				paths := legacypreflight.DefaultPaths(filepath.Join(dir, "usage.sqlite"))
				if err := os.WriteFile(paths.Database, []byte(strings.Repeat("not-sqlite-", 20)), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(paths.DataKey, []byte("legacy-data-key"), 0o600); err != nil {
					t.Fatal(err)
				}
				lockPath := paths.Database + ".manager.lock"
				if err := os.WriteFile(lockPath, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				target := lockPath
				if unreadableDB {
					target = paths.Database
				}
				if err := os.Chmod(target, 0o000); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Chmod(target, 0o600) })

				h, err := New(paths, "https://public-manager.test", true, dir)
				if err != nil {
					t.Fatal(err)
				}
				code, body := postAuthRequest(t, h, "any-key")
				if code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"source_unavailable"`) {
					t.Fatalf("non-sqlite stub (%s) got %d %s, want 503 source_unavailable", name, code, body)
				}
			})
		}
	})

	t.Run("native v2 schema and fresh checkpoint DB stay generic source_unavailable", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			ddl  string
		}{
			{name: "native v2 table", ddl: `create table upgrade_operations(id integer primary key);`},
			{name: "fresh checkpoint setting", ddl: `insert into settings values('fresh_operation_v1','{"active":true}');`},
		} {
			for _, unreadableDB := range []bool{false, true} {
				modeName := tc.name + "/unreadable_lock"
				if unreadableDB {
					modeName = tc.name + "/unreadable_database"
				}
				t.Run(modeName, func(t *testing.T) {
					dir, paths := createLegacyAuthFixture(t, legacyKey, tc.ddl)
					target := paths.Database + ".manager.lock"
					if unreadableDB {
						target = paths.Database
					}
					if err := os.Chmod(target, 0o000); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = os.Chmod(target, 0o600) })

					h, err := New(paths, "https://public-manager.test", true, dir)
					if err != nil {
						t.Fatal(err)
					}
					for _, presented := range []string{"wrong-key", legacyKey} {
						code, body := postAuthRequest(t, h, presented)
						if code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"source_unavailable"`) {
							t.Fatalf("%s (%s) got %d %s, want 503 source_unavailable", modeName, presented, code, body)
						}
					}
				})
			}
		}
	})

	t.Run("admitted manager store and fallback manager store matrix", func(t *testing.T) {
		_, paths := createLegacyAuthFixture(t, legacyKey, "")
		if err := os.Chmod(paths.DataKey, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(paths.DataKey, 0o600) })

		// Admitted live Manager store (mountSource=false, preserve=false) never
		// emits source_ownership_unavailable, even if data.key is unreadable.
		admitted, err := New(paths, "https://public-manager.test", false, "")
		if err != nil {
			t.Fatal(err)
		}
		if code, body := postAuthRequest(t, admitted, "wrong-key"); code != http.StatusUnauthorized || !strings.Contains(body, `"code":"admin_unauthorized"`) {
			t.Fatalf("admitted store wrong key = %d %s, want 401 admin_unauthorized", code, body)
		}

		// Preserved fallback store (mountSource=false, preserve=true): proven 1.x
		// DB with unreadable data.key emits ownership code; unreadable DB itself
		// stays generic source_unavailable (cannot rule out root-owned v2 /data).
		preserved, err := New(paths, "https://public-manager.test", false, "")
		if err != nil {
			t.Fatal(err)
		}
		preserved.SetPreserveManagerStore(true)
		if code, body := postAuthRequest(t, preserved, legacyKey); code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"source_ownership_unavailable"`) {
			t.Fatalf("preserved 1.x fallback with unreadable data.key = %d %s, want 503 source_ownership_unavailable", code, body)
		}

		if err := os.Chmod(paths.Database, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(paths.Database, 0o644) })
		if code, body := postAuthRequest(t, preserved, legacyKey); code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"source_unavailable"`) {
			t.Fatalf("preserved fallback with unreadable database = %d %s, want 503 source_unavailable", code, body)
		}
	})
}

func TestMidRequestDataKeyChangeFailsClosedBeforeCredentialVerdict(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores 0000 file permissions")
	}
	const rightKey = "toctou-legacy-admin-key-123456"

	t.Run("data.key made unreadable after pre-probe answers 503 source_ownership_unavailable for wrong and right keys", func(t *testing.T) {
		for _, presented := range []string{"wrong-key", rightKey} {
			dir, paths := createLegacyAuthFixture(t, rightKey, "")
			h, err := New(paths, "https://public-manager.test", true, dir)
			if err != nil {
				t.Fatal(err)
			}
			h.afterOwnershipProbe = func() {
				if chmodErr := os.Chmod(paths.DataKey, 0o000); chmodErr != nil {
					t.Errorf("chmod data.key in hook: %v", chmodErr)
				}
			}
			t.Cleanup(func() { _ = os.Chmod(paths.DataKey, 0o600) })

			code, body := postAuthRequest(t, h, presented)
			if code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"source_ownership_unavailable"`) {
				t.Fatalf("presented=%q after mid-request chmod 0000 got %d %s, want 503 source_ownership_unavailable", presented, code, body)
			}
		}
	})

	t.Run("data.key replaced after pre-probe fails closed with 503 source_unavailable for wrong and right keys", func(t *testing.T) {
		for _, presented := range []string{"wrong-key", rightKey} {
			dir, paths := createLegacyAuthFixture(t, rightKey, "")
			replacement := filepath.Join(t.TempDir(), "replacement.key")
			if err := os.WriteFile(replacement, []byte("different-data-key-material"), 0o600); err != nil {
				t.Fatal(err)
			}
			h, err := New(paths, "https://public-manager.test", true, dir)
			if err != nil {
				t.Fatal(err)
			}
			h.afterOwnershipProbe = func() {
				if renameErr := os.Rename(replacement, paths.DataKey); renameErr != nil {
					t.Errorf("replace data.key in hook: %v", renameErr)
				}
			}

			code, body := postAuthRequest(t, h, presented)
			if code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"source_unavailable"`) {
				t.Fatalf("presented=%q after mid-request data.key swap got %d %s, want 503 source_unavailable", presented, code, body)
			}
		}
	})
}

func TestBlockingOwnershipLookupHonorsCancellationDeadlineAndSingleFlight(t *testing.T) {
	managerDir := t.TempDir()
	legacyRoot := filepath.Join(t.TempDir(), "legacy-never-touched")
	paths := legacypreflight.DefaultPaths(filepath.Join(managerDir, "usage.sqlite"))

	t.Run("caller cancellation and single-flight coalescing across subsequent callers", func(t *testing.T) {
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
		h.probeOwnership = func(_ context.Context, got sqlite.InspectionPaths, checkDataKey bool) ownershipAssessment {
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
			return ownershipAssessment{status: ownershipDenied}
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
		// flight without starting a second probe, and exits on its context deadline.
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

		// Third caller arriving after both earlier callers left still joins the
		// one occupied flight rather than spawning a second blocked worker.
		ctx3, cancel3 := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel3()
		rec3 := httptest.NewRecorder()
		h.ServeHTTP(rec3, newReq(ctx3))

		if got, _ := probed.Load().(string); got != paths.Database {
			close(release)
			t.Fatalf("ownership lookup probed %q, want bound source %q", got, paths.Database)
		}
		for i, rec := range []*httptest.ResponseRecorder{rec1, rec2, rec3} {
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
		h.probeOwnership = func(context.Context, sqlite.InspectionPaths, bool) ownershipAssessment {
			<-release
			close(exited)
			return ownershipAssessment{status: ownershipDenied}
		}

		code, body := postAuthRequest(t, h, "test-key")
		if code != http.StatusServiceUnavailable || !strings.Contains(body, `"code":"source_unavailable"`) {
			close(release)
			t.Fatalf("timed-out ownership lookup = %d %s, want 503 source_unavailable", code, body)
		}
		close(release)
		<-exited
	})

	t.Run("simultaneous completion and cancellation deterministically answers source_unavailable", func(t *testing.T) {
		for i := range 32 {
			h, err := New(paths, "https://public-manager.test", true, managerDir)
			if err != nil {
				t.Fatal(err)
			}
			release := make(chan struct{})
			h.probeOwnership = func(context.Context, sqlite.InspectionPaths, bool) ownershipAssessment {
				<-release
				return ownershipAssessment{status: ownershipDenied}
			}
			reqCtx, cancel := context.WithCancel(context.Background())
			h.onOwnershipFlightJoined = func(flightDone <-chan struct{}) {
				close(release)
				<-flightDone
				cancel()
			}

			r := httptest.NewRequest(http.MethodPost, Path, nil).WithContext(reqCtx)
			r.Host = "public-manager.test"
			r.Header.Set("Authorization", "Bearer test-key")
			r.Header.Set("Origin", "https://public-manager.test")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			cancel()

			if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), `"code":"source_unavailable"`) {
				t.Fatalf("iteration %d simultaneous done+cancel = %d %s, want 503 source_unavailable", i, w.Code, w.Body.String())
			}
		}
	})
}
