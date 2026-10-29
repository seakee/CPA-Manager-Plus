package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

func TestFreshClientKeysAdminPersistenceAndStops(t *testing.T) {
	ctx := t.Context()
	paths := legacypreflight.DefaultPaths(filepath.Join(t.TempDir(), "manager.sqlite"))
	paths.Config = filepath.Join(filepath.Dir(paths.Database), "config.json")
	opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if opened != nil {
			_ = opened.Close()
		}
	}()
	app := newFreshApplication(opened, config.Config{}, nil)
	_, err = app.store.Update(ctx, opened.Record.Operation.Identity, opened.Record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: r.Operation.Identity.OperationID, Revision: 1, Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, resource, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, "/control/api/v2/"+resource, bytes.NewBufferString(body))
		request.Header.Set("Content-Type", "application/json")
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		response := httptest.NewRecorder()
		// Exercise the real canonical Admin verifier. Ingress routing is covered
		// separately by the process smoke; this does not forge its verification.
		httpapi.InitialFullWebHandler("/control", http.HandlerFunc(app.canonicalAPI)).ServeHTTP(response, request)
		if response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("Full Web wrapper weakened API cache control")
		}
		if response.Body.String() != "" && strings.Contains(response.Body.String(), strategyTestAdmin) {
			t.Fatal("admin secret leaked")
		}
		return response
	}
	id := strings.Repeat("a", 32)
	body := `{"requestId":"` + id + `"}`
	for _, token := range []string{"", "wrong-admin"} {
		for _, method := range []string{"GET", "POST", "DELETE"} {
			if got := call(method, "api-keys", token, body); got.Code != 401 {
				t.Fatalf("unauthorized %s: %d", method, got.Code)
			}
		}
		if got := call("GET", "api-keys/requests/"+id, token, ""); got.Code != 401 {
			t.Fatal("unauthorized lookup")
		}
		for _, resource := range []string{"api-keys/" + id + "/revocation", "api-keys/operations/" + id + "/continue", "api-keys/operations/latest"} {
			for _, method := range []string{"GET", "POST"} {
				if got := call(method, resource, token, body); got.Code != 401 {
					t.Fatal("unauthorized key operation", method, resource, got.Code)
				}
			}
		}
	}
	for _, resource := range []string{"api-keys/" + id + "/revocation", "api-keys/operations/" + id + "/continue", "api-keys/operations/latest"} {
		if got := call("POST", resource, strategyTestAdmin, body); got.Code != 409 {
			t.Fatal("default profile must not enable KU", resource, got.Code)
		}
	}
	created := call("POST", "api-keys", strategyTestAdmin, body)
	if created.Code != 201 {
		t.Fatalf("create %d", created.Code)
	}
	var result freshkeys.Result
	if json.Unmarshal(created.Body.Bytes(), &result) != nil || result.CreatedSecret == "" {
		t.Fatal("missing creation result")
	}
	for _, resource := range []string{"api-keys", "api-keys/requests/" + id} {
		got := call("GET", resource, strategyTestAdmin, "")
		if got.Code != 200 || strings.Contains(got.Body.String(), result.CreatedSecret) || !strings.Contains(got.Body.String(), string(result.Key.ID)) {
			t.Fatal("invalid protected read")
		}
	}
	detailPath := "api-keys/" + string(result.Key.ID)
	installPath := detailPath + "/initial-install"
	for _, token := range []string{"", "wrong-admin"} {
		for _, method := range []string{"GET", "POST"} {
			if got := call(method, installPath, token, `{}`); got.Code != 401 {
				t.Fatal("unauthenticated install endpoint", got.Code)
			}
		}
	}
	for _, body := range []string{`null`, `{"sourceExclusive":true}`, `{"runtimeIdentity":"embedded-auth-proof"}`, `{"generation":1}`, `{"secret":"not-accepted"}`, `{} {}`} {
		if got := call("POST", installPath, strategyTestAdmin, body); got.Code != 400 {
			t.Fatal("accepted client proof/input", got.Code)
		}
	}
	if got := call("POST", installPath, strategyTestAdmin, `{}`); got.Code != 409 {
		t.Fatal("default environment allowed install", got.Code)
	}
	if got := call("GET", installPath, strategyTestAdmin, ""); got.Code != 200 || !strings.Contains(got.Body.String(), `"installAvailable":false`) || strings.Contains(got.Body.String(), result.CreatedSecret) {
		t.Fatal("unsafe default install status")
	}
	for _, token := range []string{"", "wrong-admin", "expired-session-token"} {
		for _, method := range []string{"GET", "PUT"} {
			if got := call(method, detailPath, token, `{"name":"blocked","nameRevision":1}`); got.Code != 401 {
				t.Fatal("unauthorized detail/rename", got.Code)
			}
		}
	}
	if got := call("PUT", detailPath, strategyTestAdmin, `{"name":"Production client","nameRevision":1}`); got.Code != 200 || strings.Contains(got.Body.String(), result.CreatedSecret) {
		t.Fatal("rename failed", got.Code)
	}
	if got := call("PUT", detailPath, strategyTestAdmin, `{"name":"duplicate","nameRevision":1}`); got.Code != 409 {
		t.Fatal("duplicate save accepted")
	}
	for _, invalid := range []string{`{}`, `{"name":null,"nameRevision":2}`, `{"name":"x"}`, `{"name":"x","nameRevision":0}`, `{"name":"x","nameRevision":2,"secret":"forbidden"}`, `{"name":"x","nameRevision":2} {}`} {
		if got := call("PUT", detailPath, strategyTestAdmin, invalid); got.Code != 400 {
			t.Fatal("invalid rename accepted", got.Code)
		}
	}
	for _, wrongID := range []string{id, strings.Repeat("d", 32)} {
		if got := call("PUT", "api-keys/"+wrongID, strategyTestAdmin, `{"name":"wrong","nameRevision":1}`); got.Code != 404 {
			t.Fatal("wrong canonical ID accepted", got.Code)
		}
	}
	if got := call("GET", "api-keys/bad", strategyTestAdmin, ""); got.Code != 400 {
		t.Fatal("invalid canonical ID accepted")
	}
	replay := call("POST", "api-keys", strategyTestAdmin, body)
	if replay.Code != 200 || strings.Contains(replay.Body.String(), "createdSecret") {
		t.Fatal("replay disclosed secret")
	}
	for _, resource := range []string{"api-keys", "api-keys/" + string(result.Key.ID), "api-keys/" + string(result.Key.ID) + "/replace", "api-keys/" + string(result.Key.ID) + "/reveal"} {
		if got := call("DELETE", resource, strategyTestAdmin, ""); got.Code != 405 {
			t.Fatal("dangerous delete enabled")
		}
		if resource != "api-keys" && call("POST", resource, strategyTestAdmin, body).Code != 405 {
			t.Fatal("unsupported mutation enabled")
		}
	}
	if got := call("GET", "api-keys/requests/"+strings.Repeat("b", 32), strategyTestAdmin, ""); got.Code != 404 {
		t.Fatal("unknown operation misreported")
	}
	for _, invalid := range []string{`{}`, `{"requestId":"bad"}`, body + ` {}`, `{"requestId":"` + id + `","secret":"forbidden"}`} {
		if call("POST", "api-keys", strategyTestAdmin, invalid).Code != 400 {
			t.Fatal("malformed create accepted")
		}
	}
	before, _ := app.store.Load(ctx)
	if err = opened.Close(); err != nil {
		t.Fatal(err)
	}
	opened = nil
	opened, err = fresh.Open(ctx, paths, fresh.OpenOptions{AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	app = newFreshApplication(opened, config.Config{}, nil)
	after, _ := app.store.Load(ctx)
	if after.Operation.Revision != before.Operation.Revision || !after.Operation.GatewayPending {
		t.Fatal("creation changed Gateway or bootstrap authority")
	}
	list := call("GET", "api-keys", strategyTestAdmin, "")
	if list.Code != 200 || !strings.Contains(list.Body.String(), string(result.Key.ID)) || strings.Contains(list.Body.String(), result.CreatedSecret) {
		t.Fatal("restart lost redacted result")
	}
	for _, resource := range []string{detailPath, "api-keys/requests/" + id} {
		got := call("GET", resource, strategyTestAdmin, "")
		if got.Code != 200 || !strings.Contains(got.Body.String(), `"name":"Production client"`) || !strings.Contains(got.Body.String(), `"nameRevision":2`) || strings.Contains(got.Body.String(), result.CreatedSecret) {
			t.Fatal("restart lost renamed metadata", got.Code)
		}
	}
	// Adopt is a Manager-owned installation, so its Key routes answer exactly as
	// New Managed's do: this Manager staged that destination and owns the
	// credential the Key workspace writes through. Connect External is not, and
	// this application has no External authority, so the route must refuse it
	// rather than serve an installation it does not own.
	adopted := after.Operation
	adopted.Strategy.Strategy = bootstrap.AdoptExisting
	adopted.Strategy.SourceClass = bootstrap.AdoptNetworkExisting
	adoptedRequest := httptest.NewRequest("POST", "/control/api/v2/api-keys", strings.NewReader(body))
	adoptedRequest.Header.Set("Content-Type", "application/json")
	served := httptest.NewRecorder()
	app.clientKeys(served, adoptedRequest, adopted, "api-keys")
	if served.Code != 200 || !strings.Contains(served.Body.String(), string(result.Key.ID)) {
		t.Fatalf("adopted scope did not serve its Key workspace: %d %s", served.Code, served.Body.String())
	}
	// A legacy_managed adoption is Upgrade's source class, not a destination this
	// lane staged, so the same route refuses it even though the strategy matches.
	legacy := after.Operation
	legacy.Strategy.Strategy = bootstrap.AdoptExisting
	legacy.Strategy.SourceClass = bootstrap.AdoptLegacyManaged
	legacyRecorder := httptest.NewRecorder()
	app.clientKeys(legacyRecorder, httptest.NewRequest("POST", "/control/api/v2/api-keys", strings.NewReader(body)), legacy, "api-keys")
	if legacyRecorder.Code != 409 {
		t.Fatalf("legacy_managed scope answered without its installation authority: %d", legacyRecorder.Code)
	}
	external := after.Operation
	external.Strategy.Strategy = bootstrap.ConnectExternal
	refused := httptest.NewRecorder()
	app.clientKeys(refused, httptest.NewRequest("POST", "/control/api/v2/api-keys", strings.NewReader(body)), external, "api-keys")
	if refused.Code != 409 {
		t.Fatalf("Connect External scope answered without its installation authority: %d", refused.Code)
	}
}

func TestPrivateStagedKeyProofIsOptInLoopbackOnlyAndNeverGatewayAuthorized(t *testing.T) {
	paths := legacypreflight.DefaultPaths(filepath.Join(t.TempDir(), "manager.sqlite"))
	paths.Config = filepath.Join(filepath.Dir(paths.Database), "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	app := newFreshApplication(opened, config.Config{}, nil)
	result, created, err := app.keys.Create(t.Context(), strings.Repeat("c", 32))
	if err != nil || !created {
		t.Fatal("create", err)
	}

	call := func(path, method, remoteAddr, authorization string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = remoteAddr
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if strings.Contains(rec.Body.String(), result.CreatedSecret) {
			t.Fatal("private proof leaked secret")
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("private proof must not cache")
		}
		return rec
	}
	url := privateStagedKeyProofPath
	valid := "Bearer " + result.CreatedSecret
	if got := call(url, "POST", "127.0.0.1:1442", valid); got.Code != 404 {
		t.Fatalf("proof enabled by default: %d", got.Code)
	}
	t.Setenv("CPAMP_PRIVATE_CLIENT_AUTH_PROOF", "1")
	for _, remote := range []string{"192.0.2.1:1442", "invalid", "cpamp-manager:1442"} {
		if got := call(url, "POST", remote, valid); got.Code != 404 {
			t.Fatalf("nonloopback proof leaked: %d", got.Code)
		}
	}
	for _, invalid := range []string{url + "?secret=1", "/internal/fresh-client-key-stage/%70robe"} {
		if got := call(invalid, "POST", "127.0.0.1:1442", valid); got.Code != 404 {
			t.Fatalf("noncanonical proof accepted %s: %d", invalid, got.Code)
		}
	}
	if got := call(url, "GET", "127.0.0.1:1442", valid); got.Code != 405 {
		t.Fatalf("method accepted: %d", got.Code)
	}
	for _, value := range []string{"", "Bearer wrong", "Bearer " + strings.Repeat("0", 73)} {
		if got := call(url, "POST", "127.0.0.1:1442", value); got.Code != 401 {
			t.Fatalf("bad credential accepted: %d", got.Code)
		}
	}
	for _, addr := range []string{"127.0.0.1:1442", "[::1]:1442"} {
		got := call(url, "POST", addr, valid)
		if got.Code != 200 || !strings.Contains(got.Body.String(), `"managerStagedKeyMatched":true`) ||
			!strings.Contains(got.Body.String(), `"runtimeEffective":false`) ||
			!strings.Contains(got.Body.String(), `"gatewayAuthorized":false`) {
			t.Fatalf("staged match misrepresented Runtime authority: %d %s", got.Code, got.Body.String())
		}
	}
	if _, err := opened.DB.Exec("update fresh_client_api_keys set secret_hash=?", strings.Repeat("0", 64)); err != nil {
		t.Fatal(err)
	}
	if got := call(url, "POST", "127.0.0.1:1442", valid); got.Code != 503 {
		t.Fatalf("corrupt authority did not fail closed: %d", got.Code)
	}
}
