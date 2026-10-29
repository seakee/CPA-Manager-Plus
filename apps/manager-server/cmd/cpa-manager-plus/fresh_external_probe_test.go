package main

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
)

// probeCPA is the user's own CPA behind the Manager-owned routes. It counts every
// request that reached it, so a refused route can prove it forwarded nothing.
type probeCPA struct {
	management atomic.Value
	requests   atomic.Int32
	server     *httptest.Server
}

func newProbeCPA(t *testing.T, management string) *probeCPA {
	t.Helper()
	f := &probeCPA{}
	f.management.Store(management)
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+f.management.Load().(string) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.13")
		switch r.URL.Path {
		case "/v0/management/config", "/v8/management/config":
			_, _ = w.Write([]byte(`{}`))
		case "/v8/management/config.yaml":
			_, _ = w.Write([]byte(`{"config-version":8}`))
		case "/v8/management/config/access/api-keys":
			_, _ = w.Write([]byte("[]"))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// newExternalProbeApp commits an External checkpoint bound to a fake CPA that
// counts every forwarded request.
func newExternalProbeApp(t *testing.T) (*freshApplication, *probeCPA) {
	t.Helper()
	fake := newProbeCPA(t, externalAuthorityManagement)
	return newExternalApp(t, fake.server.URL, externalAuthorityManagement), fake
}

// newExternalApp commits an External checkpoint bound to the given CPA address and
// management key, so a test can own what that CPA answers and when.
func newExternalApp(t *testing.T, cpaURL, management string) *freshApplication {
	t.Helper()
	app, closeApp := newA3TestApp(t)
	t.Cleanup(closeApp)
	baseURL, err := cpa.NormalizeV8BaseURL(cpaURL)
	if err != nil {
		t.Fatal(err)
	}
	protected, err := app.protector.ProtectString(management)
	if err != nil {
		t.Fatal(err)
	}
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.ConnectExternal, Stage: "committed",
			TargetIdentity: freshkeyapply.ExternalIdentity(baseURL), TargetGeneration: 1,
		}
		r.Operation.ProductComplete = true
		r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: protected}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	app.externalSource = externalInstallSource(app.store, app.protector)
	app.keyApply = freshkeyapply.NewExternal(app.keyRepo, app.keys, app.externalSource)
	return app
}
