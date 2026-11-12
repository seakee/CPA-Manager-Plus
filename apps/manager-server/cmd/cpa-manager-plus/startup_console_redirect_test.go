package main

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/freshbootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/fullweb"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

func reserveLoopbackAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// TestRealIngressWithProductionStartupAndFreshHandlers verifies across the real
// cpamp-ingress subprocess and production Manager startup/Fresh handlers that
// /console redirects with 303 only when Manager's Bootstrap UI actually answers
// 200, and stays closed (503, no Location) during unready, unstaged-shell,
// BeginWritableStartup/store-failure, and corrupted-Fresh-store states.
func TestRealIngressWithProductionStartupAndFreshHandlers(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	ingressDir := filepath.Clean(filepath.Join(filepath.Dir(source), "../../..", "ingress"))
	work := t.TempDir()
	binary := filepath.Join(work, "cpamp-ingress")
	build := exec.Command("go", "build", "-o", binary, "./cmd/cpamp-ingress")
	build.Dir = ingressDir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build cpamp-ingress: %v\n%s", err, out)
	}

	startup := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	manager := httptest.NewServer(startup)
	defer manager.Close()

	var gatewayHits int
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		gatewayHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()

	ingressAddr := reserveLoopbackAddr(t)
	controlAddr := reserveLoopbackAddr(t)
	origin := "http://" + ingressAddr

	proc := exec.Command(binary)
	proc.Env = append(os.Environ(),
		"CPAMP_INGRESS_ADDR="+ingressAddr,
		"CPAMP_INGRESS_CONTROL_ADDR="+controlAddr,
		"CPAMP_MANAGER_URL="+manager.URL,
		"CPAMP_GATEWAY_URL="+gateway.URL,
	)
	proc.Stdout = io.Discard
	proc.Stderr = io.Discard
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if proc.Process != nil {
			_ = proc.Process.Kill()
			_ = proc.Wait()
		}
	}()

	started := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(origin + bootstrap.FixedLivenessPath)
		if err == nil {
			_ = resp.Body.Close()
			started = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !started {
		t.Fatal("cpamp-ingress did not start listening")
	}

	noRedirect := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	browserAccept := "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"

	doRaw := func(method, path, accept string) (int, http.Header, string) {
		t.Helper()
		req, err := http.NewRequest(method, origin+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		resp, err := noRedirect.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, resp.Header, string(body)
	}

	assertClosed503 := func(stage string) {
		t.Helper()
		code, hdr, _ := doRaw(http.MethodGet, "/console", browserAccept)
		if code != http.StatusServiceUnavailable || hdr.Get("Location") != "" {
			t.Fatalf("%s: GET /console = %d Location=%q, want 503 without redirect", stage, code, hdr.Get("Location"))
		}
	}

	assertRedirectReaches200UI := func(stage string) {
		t.Helper()
		code, hdr, _ := doRaw(http.MethodGet, "/console", browserAccept)
		if code != http.StatusSeeOther || hdr.Get("Location") != bootstrap.FixedBootstrapUIPath || hdr.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s: GET /console = %d Location=%q Cache-Control=%q, want 303 %q no-store",
				stage, code, hdr.Get("Location"), hdr.Get("Cache-Control"), bootstrap.FixedBootstrapUIPath)
		}
		uiCode, uiHdr, uiBody := doRaw(http.MethodGet, hdr.Get("Location"), browserAccept)
		if uiCode != http.StatusOK || uiHdr.Get("X-CPAMP-Route-Owner") != "manager" || !strings.Contains(uiBody, `{"state":"bootstrap","bootstrap":true}`) {
			t.Fatalf("%s: 303 target %q returned status=%d owner=%q body=%q",
				stage, hdr.Get("Location"), uiCode, uiHdr.Get("X-CPAMP-Route-Owner"), uiBody)
		}
		nonHTMLCode, nonHTMLHdr, _ := doRaw(http.MethodGet, "/console", "*/*")
		if nonHTMLCode != http.StatusServiceUnavailable || nonHTMLHdr.Get("Location") != "" {
			t.Fatalf("%s: non-HTML GET /console = %d Location=%q, want 503", stage, nonHTMLCode, nonHTMLHdr.Get("Location"))
		}
	}

	// 1. Initial unready startupHTTPHandler (readOnlyDiscovery off): /ui is 404, /console stays 503.
	if uiCode, _, _ := doRaw(http.MethodGet, bootstrap.FixedBootstrapUIPath, browserAccept); uiCode != http.StatusNotFound {
		t.Fatalf("unready startup UI = %d, want 404", uiCode)
	}
	assertClosed503("unready startup listener")

	// 2. EnableReadOnlyDiscovery with empty shell (simulating missing bundle where /ui is 404): stays 503.
	pristineDir := filepath.Join(work, "pristine")
	if err := os.MkdirAll(pristineDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pristinePaths := legacypreflight.DefaultPaths(filepath.Join(pristineDir, "manager.sqlite"))
	pristinePaths.Config = filepath.Join(pristineDir, "config.json")
	startup.projectionMu.Lock()
	startup.readOnlyShell = fullweb.BootstrapHandler{Dist: fstest.MapFS{}}
	startup.projectionMu.Unlock()
	startup.EnableReadOnlyDiscovery(pristinePaths, legacypreflight.Options{}, 5*time.Second)
	if uiCode, _, _ := doRaw(http.MethodGet, bootstrap.FixedBootstrapUIPath, browserAccept); uiCode != http.StatusNotFound {
		t.Fatalf("empty-bundle startup UI = %d, want 404", uiCode)
	}
	assertClosed503("readOnlyDiscovery enabled without usable shell bundle")

	// 3. Attach production fullweb.BootstrapHandler with packaged-shape HTML: pristine /console -> 303 -> 200 UI.
	realShell := fullweb.BootstrapHandler{
		Dist: fstest.MapFS{
			"index.html":               {Data: []byte(`<!doctype html><html><body><div id="root"></div><script type="module" src="./assets/index-AbCd1234.js"></script><script>window.__CPAMP__=__CPAMP_ROUTING_CONTEXT__;</script></body></html>`)},
			"assets/index-AbCd1234.js": {Data: []byte(`console.log("bootstrap");`)},
		},
	}
	startup.projectionMu.Lock()
	startup.readOnlyShell = realShell
	startup.projectionMu.Unlock()
	if discCode, _, discBody := doRaw(http.MethodGet, bootstrap.FixedBootstrapDiscoveryPath, "application/json"); discCode != http.StatusOK || !strings.Contains(discBody, `"reason":"fresh_claim_required"`) {
		t.Fatalf("pristine discovery = %d %s", discCode, discBody)
	}
	assertRedirectReaches200UI("pristine readOnlyDiscovery")

	// 4. BeginWritableStartup (including post-admission Store.Open failure diagnostic state): /ui is 404, /console returns to 503.
	startup.BeginWritableStartup()
	if uiCode, _, _ := doRaw(http.MethodGet, bootstrap.FixedBootstrapUIPath, browserAccept); uiCode != http.StatusNotFound {
		t.Fatalf("writable-transition UI = %d, want 404", uiCode)
	}
	assertClosed503("BeginWritableStartup / failed-store diagnostics")

	// 5. Recovery via EnableAdmittedDiscovery (SurfaceUpgradeOnly): /console recovers to 303 -> 200 UI.
	startup.EnableAdmittedDiscovery(pristinePaths, legacypreflight.Options{}, 5*time.Second, legacypreflight.Report{
		SourceKind: legacypreflight.SourceKindUpgradedLegacy,
	})
	assertRedirectReaches200UI("admitted Upgrade-only discovery recovery")

	// 6. Fresh store takeover before routing/prepare, store failure, and recovery.
	freshDir := filepath.Join(work, "fresh")
	if err := os.MkdirAll(freshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	freshPaths := legacypreflight.DefaultPaths(filepath.Join(freshDir, "manager.sqlite"))
	freshPaths.Config = filepath.Join(freshDir, "config.json")
	opened, err := fresh.Open(t.Context(), freshPaths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	app := newFreshApplication(opened, config.Config{}, nil)
	fixedHandler := app.fixed.(freshbootstrap.Handler)
	fixedHandler.Shell = realShell
	app.fixed = fixedHandler
	startup.SetFreshHandler(app)
	assertRedirectReaches200UI("Fresh store takeover before routing/prepare")

	var savedOperation string
	if err := opened.DB.QueryRow("SELECT value FROM settings WHERE key='fresh_operation_v1'").Scan(&savedOperation); err != nil {
		t.Fatal(err)
	}
	if _, err := opened.DB.Exec("UPDATE settings SET value='{}' WHERE key='fresh_operation_v1'"); err != nil {
		t.Fatal(err)
	}
	assertClosed503("damaged Fresh operation record")

	if _, err := opened.DB.Exec("UPDATE settings SET value=? WHERE key='fresh_operation_v1'", savedOperation); err != nil {
		t.Fatal(err)
	}
	assertRedirectReaches200UI("recovered Fresh operation record")

	if gatewayHits != 0 {
		t.Fatalf("unexpected Gateway traffic during pre-commit transitions: %d", gatewayHits)
	}
}
