package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/freshbootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

// Opt-in browser fixture: real Manager store/auth/HTTP and packaged Full Web,
// with an isolated CPA safe-read source and a test Ingress projection. This is
// UI regression evidence, not deployment or legacy-migration qualification.
func TestFreshSetupBrowserFixture(t *testing.T) {
	if os.Getenv("CPAMP_SETUP_BROWSER") != "1" {
		t.Skip("opt-in isolated browser fixture")
	}
	paths := legacypreflight.DefaultPaths(filepath.Join(t.TempDir(), "manager.sqlite"))
	paths.Config = filepath.Join(filepath.Dir(paths.Database), "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	app := newFreshApplication(opened, config.Config{}, nil)
	app.routing = freshrouting.New(app.store, &setupBrowserIngress{})
	app.fixed = freshbootstrap.Handler{Authority: app.authority, Routing: app.routing, Current: app.current,
		Shell: httpapi.BootstrapWebHandler(), Strategy: http.HandlerFunc(app.fixedStrategy), Runtime: http.HandlerFunc(app.fixedRuntime),
		// The wizard's own progress read: an adoption that this claim committed
		// reads its recorded summary through the fixed carrier until the public
		// route is committed.
		Adoption: http.HandlerFunc(app.fixedAdoption)}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer browser-fixture-cpa-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Safe fixture reads for the original Console. Unsupported endpoints
		// return 404, not a false Admin authentication error that causes logout.
		bodies := map[string]string{
			"/v8/management/config":               `{"config-version":8,"server":{"port":8317},"oauth":{"auth-dir":"auth"},"access":{"api-keys":[]}}`,
			"/v0/management/config":               `{"port":8317,"api-keys":[]}`,
			"/v0/management/latest-version":       `{"latest-version":"8.0.0"}`,
			"/v0/management/api-keys":             `{"api-keys":[]}`,
			"/v0/management/auth-files":           `{"files":[]}`,
			"/v0/management/gemini-api-key":       `{"gemini-api-key":[]}`,
			"/v0/management/codex-api-key":        `{"codex-api-key":[]}`,
			"/v0/management/claude-api-key":       `{"claude-api-key":[]}`,
			"/v0/management/xai-api-key":          `{"xai-api-key":[]}`,
			"/v0/management/openai-compatibility": `{"openai-compatibility":[]}`,
			"/v1/models":                          `{"object":"list","data":[]}`,
		}
		body, ok := bodies[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer source.Close()
	// The browser reaches Manager through Ingress in a real installation.
	// This fixture models only its route-owner response header, not deployment
	// readiness. Do not weaken public identity verification for a direct port.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-CPAMP-Route-Owner", "manager")
		app.ServeHTTP(w, r)
	}))
	defer server.Close()
	handoff, err := app.authority.Issue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("BROWSER_FIXTURE=%s/.well-known/cpamp-bootstrap/ui%s\nCPA_FIXTURE=%s\n", server.URL, handoff.Fragment, source.URL)
	<-t.Context().Done()
}

type setupBrowserIngress struct{ state freshrouting.State }

func (d *setupBrowserIngress) Inspect(context.Context) (freshrouting.State, error) {
	return d.state, nil
}
func (d *setupBrowserIngress) Apply(_ context.Context, s freshrouting.State) error {
	d.state = s
	return nil
}
func (d *setupBrowserIngress) Verify(context.Context, freshrouting.State) error { return nil }
