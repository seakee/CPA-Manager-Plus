package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

// dualPrefixRoutingDriver reports the committed dual-prefix route the Manager
// staged, exactly like the Ingress route owner does after a verified commit.
type dualPrefixRoutingDriver struct{ state freshrouting.State }

func (d *dualPrefixRoutingDriver) Inspect(context.Context) (freshrouting.State, error) {
	return d.state, nil
}

func (d *dualPrefixRoutingDriver) Apply(_ context.Context, state freshrouting.State) error {
	d.state = state
	return nil
}

func (d *dualPrefixRoutingDriver) Verify(_ context.Context, state freshrouting.State) error {
	d.state = state
	return nil
}

// newDualPrefixTestApplication builds a real Fresh store whose routing half is
// the current install contract: both prefixes committed and verified.
func newDualPrefixTestApplication(t *testing.T) (*freshApplication, dualPrefixRoutingDriver) {
	t.Helper()
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	app := newFreshApplication(opened, config.Config{}, nil)
	driver := dualPrefixRoutingDriver{}
	app.routing = freshrouting.New(app.store, &driver)
	control, inference := currentInstallControlPrefix, "/cpamp-api"
	if _, err := app.store.Update(t.Context(), opened.Record.Operation.Identity, opened.Record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing.OperationID = r.Operation.Identity.OperationID
		r.Operation.Routing.Revision = 1
		r.Operation.Routing.Target = control
		r.Operation.Routing.InferenceTarget = inference
		r.Operation.Routing.Stage = bootstrap.PrefixCommitted
		r.Operation.Routing.Committed = true
		r.Operation.Routing.InferenceCommitted = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	driver.state = freshrouting.StateFor(record.Operation)
	return app, driver
}

// A committed dual-prefix install answers the public API under the committed
// control prefix it publishes to clients and never under a legacy fixed mount;
// inference and Bootstrap paths stay outside the public API namespace.
func TestDualPrefixInstallServesOnlyItsCommittedControlPrefix(t *testing.T) {
	app, driver := newDualPrefixTestApplication(t)
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	control := record.Operation.Routing.Target
	if control == "" || control == "/control" {
		t.Fatalf("fixture did not commit its own control prefix: %q", control)
	}
	verified, ok := freshrouting.SnapshotForRecord(record).EffectiveControlBasePath()
	if !ok || verified != control {
		t.Fatalf("committed install has no verified control prefix: %q ok=%t", verified, ok)
	}
	read := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		response := httptest.NewRecorder()
		app.ServeHTTP(response, request)
		return response
	}
	for _, resource := range []string{"operation", "api-keys/access-info"} {
		committed := read(control + "/api/v2/" + resource)
		if committed.Code != http.StatusOK {
			t.Fatalf("GET %s under the committed prefix = %d %s", resource, committed.Code, committed.Body.String())
		}
		// The same public API is not additionally mounted under the legacy
		// fixed bootstrap control prefix.
		if legacy := read("/control/api/v2/" + resource); legacy.Code != http.StatusNotFound {
			t.Fatalf("GET %s kept a legacy /control mount: %d %s", resource, legacy.Code, legacy.Body.String())
		}
		unauthenticated := httptest.NewRecorder()
		app.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, control+"/api/v2/"+resource, nil))
		if unauthenticated.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s without a credential = %d", resource, unauthenticated.Code)
		}
	}
	for _, path := range []string{"/v1/models", "/v1beta/models", bootstrap.FixedBootstrapAPIPath + "operation"} {
		if response := read(path); response.Code == http.StatusOK {
			t.Fatalf("%s was answered by the Manager console", path)
		}
	}
	if driver.state.Target != control || driver.state.InferenceTarget != record.Operation.Routing.InferenceTarget || !driver.state.InferenceCommitted {
		t.Fatalf("routing driver state = %+v, want the committed checkpoint", driver.state)
	}
}
