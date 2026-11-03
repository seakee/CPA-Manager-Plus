package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

func setupContinuationApp(t *testing.T) (*freshApplication, string) {
	t.Helper()
	paths := legacypreflight.DefaultPaths(filepath.Join(t.TempDir(), "manager.sqlite"))
	paths.Config = filepath.Join(filepath.Dir(paths.Database), "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	app := newFreshApplication(opened, config.Config{}, nil)
	handoff, err := app.authority.Issue(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(handoff.Fragment, "#handoff="))
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	return app, payload.Token
}

func TestFreshSetupContinuationCannotBecomeConsoleAuthentication(t *testing.T) {
	app, token := setupContinuationApp(t)
	request := func(method, path, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set("Authorization", auth)
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	app.runtime = &installationTestRuntime{status: validInstallationRuntime()}
	// Configuration can read only the deployment-owned target before route cutover.
	if w := request(http.MethodGet, bootstrap.FixedBootstrapAPIPath+"installation/runtime", "Fresh "+token); w.Code != 200 {
		t.Fatal("authorized target read", w.Code)
	}
	if w := request(http.MethodGet, bootstrap.FixedBootstrapAPIPath+"installation/runtime", "Bearer "+strategyTestAdmin); w.Code != 401 {
		t.Fatal("Admin accepted by fixed transport", w.Code)
	}
	op, _ := app.current(t.Context())
	_, err := app.store.Update(t.Context(), op.Identity, op.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: op.Identity.OperationID, Revision: 1, Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true}
		s := validInstallationRuntime()
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{Strategy: bootstrap.NewManaged, Stage: "committed", TargetIdentity: string(s.Identity), TargetGeneration: uint64(s.Generation), TargetArtifactID: string(s.ActiveGatewayArtifact.ArtifactID)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range []string{"operation", "installation/runtime"} {
		if w := request(http.MethodGet, bootstrap.FixedBootstrapAPIPath+resource, "Fresh "+token); w.Code != 404 {
			t.Fatal("fixed transport reopened", resource, w.Code)
		}
		if w := request(http.MethodGet, "/control/api/v2/"+resource, "Fresh "+token); w.Code != 200 {
			t.Fatal("original installation cannot continue", resource, w.Code)
		}
	}
	for _, resource := range []string{"cpamp/usage-service/info", "cpamp/v0/management/config", "api-keys", "settings/update-channel", "admin/commit", "claim/exchange", "routing/prepare"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			if w := request(method, "/control/api/v2/"+resource, "Fresh "+token); w.Code != 401 {
				t.Fatal("installation credential escaped its scope", resource, method, w.Code)
			}
		}
	}
	if w := request(http.MethodPost, "/control/api/v2/complete", "Fresh "+token); w.Code != 200 {
		t.Fatal("setup needed a premature Admin login", w.Code)
	}
	// A lost completion reply has a bounded read receipt, not a normal session.
	if w := request(http.MethodGet, "/control/api/v2/operation", "Fresh "+token); w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte(`"productComplete":true`)) {
		t.Fatal("completion receipt unavailable", w.Code)
	}
	if w := request(http.MethodPost, "/control/api/v2/strategy", "Fresh "+token); w.Code != 401 {
		t.Fatal("completed installation allowed mutation", w.Code)
	}
	// Completion ends the installation credential's reach over its own resources
	// too. The adoption summary is the one installation resource the session read
	// while the installation was pending (see fresh_adoption_summary_test.go);
	// afterwards it is a Console resource and the session is only a receipt
	// reader.
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if w := request(method, "/control/api/v2/installation/adoption", "Fresh "+token); w.Code != 401 {
			t.Fatal("completed installation still reached its adoption summary", method, w.Code)
		}
	}
	if w := request(http.MethodGet, "/control/api/v2/operation", "Bearer "+strategyTestAdmin); w.Code != 200 {
		t.Fatal("normal Admin login stopped working", w.Code)
	}
	for _, auth := range []string{"Fresh wrong", "Bearer " + token, "Fresh " + strategyTestAdmin} {
		if w := request(http.MethodGet, "/control/api/v2/operation", auth); w.Code != 401 {
			t.Fatal("wrong credential accepted", w.Code)
		}
	}
}
