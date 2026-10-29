package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

const externalAuthorityManagement = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// externalAuthorityCPA is a user-owned CPA answering the two management reads the
// External authority needs to derive its generation.
type externalAuthorityCPA struct {
	management string
	// version is the CPA version header this fake advertises. It defaults to the
	// v8 contract the routes below actually serve.
	version string
	// reject makes the CPA refuse every management read, as a rotated credential
	// does.
	reject atomic.Bool
	// forbid makes the CPA answer 403 instead of 401, which is what it does to a
	// banned address or one with remote management disabled.
	forbid atomic.Bool
	reads  atomic.Int32
	// writes counts PUT/PATCH/DELETE attempts, so a refused save can prove it
	// never reached the CPA while its reads still did.
	writes atomic.Int32
	// probes counts the requests that reached this CPA at all, refusals included.
	// A rebind only reads, so counting writes alone would assert something that
	// is always true; this is the counter that discriminates.
	probes atomic.Int32
	server *httptest.Server
}

func newExternalAuthorityCPA(t *testing.T) *externalAuthorityCPA {
	t.Helper()
	return newExternalAuthorityCPAWith(t, externalAuthorityManagement)
}

// newExternalAuthorityCPAWith is the same CPA holding another management
// credential, so a test can prove which key shapes the authority accepts.
func newExternalAuthorityCPAWith(t *testing.T, management string) *externalAuthorityCPA {
	t.Helper()
	f := &externalAuthorityCPA{management: management, version: "v8.0.13"}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.probes.Add(1)
		if f.forbid.Load() {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if f.reject.Load() || r.Header.Get("Authorization") != "Bearer "+f.management {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet {
			f.writes.Add(1)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/v0/management/config":
			// The version probe is the same authenticated read the setup path uses.
			if f.version != "" {
				w.Header().Set("X-CPA-Version", f.version)
			}
			_, _ = w.Write([]byte(`{}`))
		case "/v8/management/config":
			f.reads.Add(1)
			_, _ = w.Write([]byte(`{"routing":{"strategy":"round-robin"}}`))
		case "/v8/management/config/access/api-keys":
			f.reads.Add(1)
			_, _ = w.Write([]byte("[]"))
		case "/v8/management/config.yaml":
			f.reads.Add(1)
			_, _ = w.Write([]byte(`{"config-version":8}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.server.Close)
	return f
}

// externalAuthorityApp commits a Connect External checkpoint bound to the fake
// CPA and returns the application that administers it.
func externalAuthorityApp(t *testing.T, targetIdentity string) (*freshApplication, *externalAuthorityCPA) {
	t.Helper()
	return externalAuthorityAppWith(t, externalAuthorityManagement, targetIdentity, nil)
}

// externalAuthorityAppWith is the same commit with the management credential and
// the committed record under the caller's control, so the authority's refusals can
// be tested one at a time.
func externalAuthorityAppWith(t *testing.T, management, targetIdentity string, edit func(*freshstore.Record)) (*freshApplication, *externalAuthorityCPA) {
	t.Helper()
	app, closeApp := newA3TestApp(t)
	t.Cleanup(closeApp)
	cpaServer := newExternalAuthorityCPAWith(t, management)
	baseURL, err := cpa.NormalizeV8BaseURL(cpaServer.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	protected, err := app.protector.ProtectString(cpaServer.management)
	if err != nil {
		t.Fatal(err)
	}
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if targetIdentity == "" {
		targetIdentity = freshkeyapply.ExternalIdentity(baseURL)
	}
	if _, err = app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.ConnectExternal, Stage: "committed",
			TargetIdentity: targetIdentity, TargetGeneration: 1,
		}
		r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: baseURL, ManagementKey: protected}
		r.Operation.ProductComplete = true
		if edit != nil {
			edit(r)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The committed checkpoint and the protected connection are what the External
	// authority resolves, so the application is wired to them exactly as the
	// External profile wires itself.
	app.externalSource = externalInstallSource(app.store, app.protector)
	app.keyApply = freshkeyapply.NewExternal(app.keyRepo, app.keys, app.externalSource)
	return app, cpaServer
}

// TestExternalAuthorityResolvesTheCommittedConnection is the positive half: the
// committed External checkpoint plus the protected Manager connection resolve to
// the user's CPA. The authority is those two durable facts alone, so resolving it
// never contacts the CPA and an outage there cannot hide the target.
func TestExternalAuthorityResolvesTheCommittedConnection(t *testing.T) {
	app, cpaServer := externalAuthorityApp(t, "")
	endpoint, management, err := app.keyApply.ExternalTarget(t.Context(), 1)
	if err != nil {
		t.Fatalf("resolve committed External target: %v", err)
	}
	if endpoint != cpaServer.server.URL {
		t.Fatalf("endpoint = %q, want %q", endpoint, cpaServer.server.URL)
	}
	if management != externalAuthorityManagement {
		t.Fatal("the protected management credential was not decrypted")
	}
	if cpaServer.reads.Load() != 0 {
		t.Fatalf("resolving the authority contacted the user's CPA %d times", cpaServer.reads.Load())
	}
	// A checkpoint the page does not hold is a generation mismatch, not a target.
	if _, _, err = app.keyApply.ExternalTarget(t.Context(), 2); !errors.Is(err, freshkeyapply.ErrGeneration) {
		t.Fatalf("stale generation = %v", err)
	}
}

// TestExternalAuthorityReportsAMovedTarget proves the committed target and the
// live connection must be the same CPA. A connection the user edited to another
// CPA is never followed silently — a rebind this path does not perform — and it is
// reported as the typed blocker it is, not as a missing proof: the installation
// exists, so the page can offer the rebind the next slice ships.
func TestExternalAuthorityReportsAMovedTarget(t *testing.T) {
	app, cpaServer := externalAuthorityApp(t, "external:sha256:"+strings.Repeat("c", 64))
	if _, _, err := app.keyApply.ExternalTarget(t.Context(), 1); !errors.Is(err, cpa.ErrExternalTargetMoved) {
		t.Fatalf("moved target = %v", err)
	}
	if cpaServer.reads.Load() != 0 {
		t.Fatal("a connection the checkpoint does not name was contacted")
	}
}

// TestExternalKeyListWithoutACommittedCheckpointNeedsNoCPA proves the list read-back
// is gated on the committed External checkpoint. A Fresh application that has not
// committed one stages keys exactly as before: it answers 200 with the staged keys
// instead of failing because there is no CPA to read.
func TestExternalKeyListWithoutACommittedCheckpointNeedsNoCPA(t *testing.T) {
	app, closeApp := newA3TestApp(t)
	t.Cleanup(closeApp)
	created := app.keyApplyKeys(t, strings.Repeat("a", 32))
	request := httptest.NewRequest(http.MethodGet, "/control/api/v2/api-keys", nil)
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	app.canonicalAPI(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("list without a committed checkpoint = %d %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), string(created.Key.ID)) {
		t.Fatal("the staged key is missing from the list")
	}
}

// keyApplyKeys stages one key through the Manager-local record.
func (a *freshApplication) keyApplyKeys(t *testing.T, requestID string) freshkeys.Result {
	t.Helper()
	result, _, err := a.keys.Create(context.Background(), requestID)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// TestExternalAuthorityAcceptsTheConnectExternalKeyShape proves the production
// source accepts the key Connect External accepts — any printable key up to 4096
// characters (cpa/v8.go) — instead of an exact 64 characters. External management
// keys are chosen by the user, so a length rule would turn every other user's
// installation into an unavailable target.
func TestExternalAuthorityAcceptsTheConnectExternalKeyShape(t *testing.T) {
	const management = "short-key-2024"
	app, cpaServer := externalAuthorityAppWith(t, management, "", nil)
	endpoint, key, err := app.keyApply.ExternalTarget(t.Context(), 1)
	if err != nil {
		t.Fatalf("a %d-character management key was refused: %v", len(management), err)
	}
	if endpoint != cpaServer.server.URL || key != management {
		t.Fatalf("resolved %q %q", endpoint, key)
	}
	// The same CPA accepts that key on a real read, so the authority is not the
	// only place the shape is admitted.
	cpaServer.reads.Store(0)
	if _, err = cpa.NewTransportForEndpoint(cpaServer.server.URL).ExternalKeySnapshot(t.Context(), management); err != nil {
		t.Fatalf("the user's CPA refused the short management key: %v", err)
	}
	if cpaServer.reads.Load() == 0 {
		t.Fatal("the read-back never reached the user's CPA")
	}
}

// TestExternalSurfacesReportAMovedTarget proves a moved target is the typed
// blocker on every External surface — the key list, the key install and the key
// operation — instead of a missing host proof the page cannot act on. Nothing
// reaches a CPA the committed checkpoint no longer names.
func TestExternalSurfacesReportAMovedTarget(t *testing.T) {
	app, cpaServer := externalAuthorityApp(t, "external:sha256:"+strings.Repeat("c", 64))
	created := app.keyApplyKeys(t, strings.Repeat("a", 32))
	call := func(method, resource, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, "/control/api/v2/"+resource, bytes.NewBufferString(body))
		request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		app.canonicalAPI(response, request)
		return response
	}
	for _, scenario := range []struct{ method, resource, body string }{
		{http.MethodGet, "api-keys", ""},
		{http.MethodPost, "api-keys/" + string(created.Key.ID) + "/initial-install", `{}`},
		{http.MethodPost, "api-keys/" + string(created.Key.ID) + "/revocation",
			`{"requestId":"` + strings.Repeat("d", 32) + `","keeperId":"` + strings.Repeat("e", 32) + `","mode":"revoke"}`},
	} {
		response := call(scenario.method, scenario.resource, scenario.body)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), freshconfig.ReasonTargetMoved) {
			t.Fatalf("%s %s on a moved target = %d %s", scenario.method, scenario.resource, response.Code, response.Body.String())
		}
	}
	if cpaServer.reads.Load() != 0 {
		t.Fatalf("a moved target was contacted %d times", cpaServer.reads.Load())
	}
}

// TestExternalKeyListRouteAnswersWithoutACPA proves the list route never hides the
// Manager's own keys behind an outage, and never turns a blocker it cannot settle
// into a bare 503: the staged keys stay visible and unverified, while a refused
// credential is the typed code the page acts on.
func TestExternalKeyListRouteAnswersWithoutACPA(t *testing.T) {
	app, cpaServer := externalAuthorityApp(t, "")
	created := app.keyApplyKeys(t, strings.Repeat("a", 32))
	call := func() *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/control/api/v2/api-keys", nil)
		request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
		response := httptest.NewRecorder()
		app.canonicalAPI(response, request)
		return response
	}
	healthy := call()
	if healthy.Code != http.StatusOK || strings.Contains(healthy.Body.String(), `"presenceUnverified":true`) {
		t.Fatalf("list with a readable CPA = %d %s", healthy.Code, healthy.Body.String())
	}
	// The CPA is unreachable: the Manager's keys are the list, marked unverified.
	cpaServer.server.Close()
	outage := call()
	if outage.Code != http.StatusOK {
		t.Fatalf("list during a CPA outage = %d %s", outage.Code, outage.Body.String())
	}
	if !strings.Contains(outage.Body.String(), `"presenceUnverified":true`) ||
		!strings.Contains(outage.Body.String(), string(created.Key.ID)) {
		t.Fatalf("outage list hid the staged key or claimed verification: %s", outage.Body.String())
	}
	// A refused credential is a blocker with its own code.
	app2, cpa2 := externalAuthorityApp(t, "")
	app2.keyApplyKeys(t, strings.Repeat("b", 32))
	cpa2.reject.Store(true)
	request := httptest.NewRequest(http.MethodGet, "/control/api/v2/api-keys", nil)
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	app2.canonicalAPI(response, request)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), freshconfig.ReasonManagementRejected) {
		t.Fatalf("list on a refused credential = %d %s", response.Code, response.Body.String())
	}
}

// staticStore answers one record. The production source's refusals are about what
// the committed record says, so the store's own validation must not be the thing
// under test.
type staticStore struct{ record freshstore.Record }

func (s staticStore) Load(context.Context) (freshstore.Record, error) { return s.record, nil }
func (s staticStore) Create(context.Context, bootstrap.FreshIdentity, *model.AdminCredential) (freshstore.Record, error) {
	return freshstore.Record{}, errors.New("unsupported")
}
func (s staticStore) Update(context.Context, bootstrap.FreshIdentity, uint64, func(*freshstore.Record) error) (freshstore.Record, error) {
	return freshstore.Record{}, errors.New("unsupported")
}

// TestExternalInstallSourceFailsClosed proves the production source resolves a
// target only from a completed External installation with an intact protected
// credential: an incomplete product, another stage or strategy, a missing
// connection, a tampered envelope and a generation of zero are all refused, and a
// credential shape the Connect External rule rejects is refused too.
func TestExternalInstallSourceFailsClosed(t *testing.T) {
	protector, err := security.NewProtector([]byte("external-source-test-key"))
	if err != nil {
		t.Fatal(err)
	}
	const management = "short-key-2024"
	protected, err := protector.ProtectString(management)
	if err != nil {
		t.Fatal(err)
	}
	// A well-formed envelope this Manager's data key cannot open: the exact state a
	// tampered ciphertext leaves behind.
	foreign, err := security.NewProtector([]byte("another-data-key"))
	if err != nil {
		t.Fatal(err)
	}
	tampered, err := foreign.ProtectString(management)
	if err != nil {
		t.Fatal(err)
	}
	record := func() freshstore.Record {
		return freshstore.Record{
			Operation: bootstrap.FreshOperation{
				Strategy: bootstrap.FreshStrategyCheckpoint{
					Strategy: bootstrap.ConnectExternal, Stage: "committed",
					TargetIdentity: freshkeyapply.ExternalIdentity("https://cpa.test"), TargetGeneration: 1,
				},
				ProductComplete: true,
			},
			Connection: &model.ManagerCPAConnectionConfig{CPABaseURL: "https://cpa.test", ManagementKey: protected},
		}
	}
	install, err := externalInstallSource(staticStore{record()}, protector)(t.Context())
	if err != nil {
		t.Fatalf("a completed installation was refused: %v", err)
	}
	if install.BaseURL != "https://cpa.test" || install.ManagementKey != management || install.Checkpoint != 1 {
		t.Fatalf("resolved %+v", install)
	}
	for _, scenario := range []struct {
		name  string
		edit  func(*freshstore.Record)
		cause error
	}{
		{"product incomplete", func(r *freshstore.Record) { r.Operation.ProductComplete = false }, freshkeyapply.ErrUnavailable},
		{"stage not committed", func(r *freshstore.Record) { r.Operation.Strategy.Stage = "validated" }, freshkeyapply.ErrUnavailable},
		{"another strategy", func(r *freshstore.Record) { r.Operation.Strategy.Strategy = bootstrap.NewManaged }, freshkeyapply.ErrUnavailable},
		{"no connection", func(r *freshstore.Record) { r.Connection = nil }, freshkeyapply.ErrUnavailable},
		{"tampered credential", func(r *freshstore.Record) { r.Connection.ManagementKey = tampered }, freshkeyapply.ErrUnavailable},
		{"no generation", func(r *freshstore.Record) { r.Operation.Strategy.TargetGeneration = 0 }, freshkeyapply.ErrUnavailable},
		{"no address", func(r *freshstore.Record) { r.Connection.CPABaseURL = "" }, freshkeyapply.ErrUnavailable},
		{"moved target", func(r *freshstore.Record) {
			r.Operation.Strategy.TargetIdentity = freshkeyapply.ExternalIdentity("https://other.test")
		}, cpa.ErrExternalTargetMoved},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			subject := record()
			scenario.edit(&subject)
			if _, err := externalInstallSource(staticStore{subject}, protector)(t.Context()); !errors.Is(err, scenario.cause) {
				t.Fatalf("%s = %v, want %v", scenario.name, err, scenario.cause)
			}
		})
	}
}

// TestExternalSurfacesReportAnUnsupportedVersion proves the version refusal is the
// typed code on every External write surface — the key install, the key operation
// and the gateway save — while the key list still reads the CPA. Reads stay open:
// the version bounds what may be written, never what may be shown.
func TestExternalSurfacesReportAnUnsupportedVersion(t *testing.T) {
	for _, version := range []string{"v7.2.130", ""} {
		t.Run("version "+version, func(t *testing.T) {
			app, cpaServer := externalAuthorityApp(t, "")
			cpaServer.version = version
			// The page's own service, bound to the same External authority the key
			// surfaces use.
			app.gatewayConfig = freshconfig.New(app.store, nil, app.keyApply.TargetsFunc(freshkeyapply.SurfaceGatewayConfig))
			app.gatewayConfig.EnableExternal(app.keyApply.ExternalTarget)
			created := app.keyApplyKeys(t, strings.Repeat("a", 32))
			keeper := app.keyApplyKeys(t, strings.Repeat("b", 32))
			call := func(method, resource, body string) *httptest.ResponseRecorder {
				t.Helper()
				request := httptest.NewRequest(method, "/control/api/v2/"+resource, bytes.NewBufferString(body))
				request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
				if body != "" {
					request.Header.Set("Content-Type", "application/json")
				}
				response := httptest.NewRecorder()
				app.canonicalAPI(response, request)
				return response
			}
			// The list is a read: it answers the keys the Manager staged.
			list := call(http.MethodGet, "api-keys", "")
			if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), string(created.Key.ID)) {
				t.Fatalf("list on version %q = %d %s", version, list.Code, list.Body.String())
			}
			for _, scenario := range []struct{ method, resource, body string }{
				{http.MethodPost, "api-keys/" + string(created.Key.ID) + "/initial-install", `{}`},
				{http.MethodPost, "api-keys/" + string(created.Key.ID) + "/revocation",
					`{"requestId":"` + strings.Repeat("d", 32) + `","keeperId":"` + string(keeper.Key.ID) + `","mode":"revoke"}`},
				{http.MethodPost, "gateway/config/groups/routing",
					`{"requestId":"` + strings.Repeat("e", 32) + `",` +
						`"expected":{"generation":"1","baseline":"` + strings.Repeat("f", 64) + `"},` +
						`"edits":[{"path":"routing.strategy","action":"set","value":"\"fill-first\""}]}`},
			} {
				response := call(scenario.method, scenario.resource, scenario.body)
				if response.Code != http.StatusConflict ||
					!strings.Contains(response.Body.String(), freshconfig.ReasonVersionUnsupported) {
					t.Fatalf("%s %s on version %q = %d %s", scenario.method, scenario.resource, version, response.Code, response.Body.String())
				}
			}
			if cpaServer.writes.Load() != 0 {
				t.Fatalf("a refused write reached the CPA %d times", cpaServer.writes.Load())
			}
		})
	}
}

// TestExternalUsageExposesTheObservedVersion proves the usage workspace reports the
// version it observed and what that lets this Manager do, and that collection is
// refused on a CPA whose version does not prove the v8 contract.
func TestExternalUsageExposesTheObservedVersion(t *testing.T) {
	h := newExternalUsageHarness(t)
	h.cpa.enabled.Store(true)
	response := h.call(t, http.MethodGet, "external/usage", "")
	if response.Code != http.StatusOK {
		t.Fatalf("usage state = %d %s", response.Code, response.Body.String())
	}
	state := decodeExternalUsageState(t, response.Body.Bytes())
	if state.CPA.Version != "v8.0.13" || state.CPA.Support != cpa.VersionSupportSupported || state.CPA.WriteBlocked {
		t.Fatalf("version report = %+v", state.CPA)
	}

	h.cpa.version.Store("v7.2.130")
	// A later attempt re-runs the start, as a completion replay or a trigger does.
	h.app.resetUsageStart()
	h.app.startUsageCollection(h.operation(t))
	if reason := h.app.usageStartBlock(); reason != freshconfig.ReasonVersionUnsupported {
		t.Fatalf("blocked reason = %q, want the version refusal", reason)
	}
	if h.cpa.statWrites.Load() != 0 {
		t.Fatal("usage statistics were enabled on a CPA this Manager cannot write")
	}
	state = decodeExternalUsageState(t, h.call(t, http.MethodGet, "external/usage", "").Body.Bytes())
	if state.CPA.Support != cpa.VersionSupportUnsupported || !state.CPA.WriteBlocked {
		t.Fatalf("version report = %+v, want an unsupported class", state.CPA)
	}
	if state.BlockedReason != freshconfig.ReasonVersionUnsupported {
		t.Fatalf("blocked reason = %q", state.BlockedReason)
	}
}

func gatewayFor(app *freshApplication) *freshconfig.Service {
	svc := freshconfig.New(app.store, nil, app.keyApply.TargetsFunc(freshkeyapply.SurfaceGatewayConfig))
	svc.EnableExternal(app.keyApply.ExternalTarget)
	return svc
}

// TestExternalRuntimeStatusReportsTheVersionClass proves the Runtime status page
// publishes what this Manager proved about the CPA's version. The page can then
// say why the write surfaces are closed instead of offering a save that will be
// refused.
func TestExternalRuntimeStatusReportsTheVersionClass(t *testing.T) {
	app, cpaServer := externalAuthorityApp(t, "")
	app.runtime = nil
	app.runtimeExternal = runtimeservice.NewExternalClientWithConnectionSource(
		func(ctx context.Context) (string, string, error) {
			install, err := app.externalSource(ctx)
			if err != nil {
				return "", "", err
			}
			return install.BaseURL, install.ManagementKey, nil
		})
	op := freshRuntimeProductCompleteOperation(t, app)

	for _, scenario := range []struct {
		version     string
		wantSupport string
		wantBlocked bool
	}{
		{"v8.0.13", cpa.VersionSupportSupported, false},
		{"v7.2.130", cpa.VersionSupportUnsupported, true},
	} {
		cpaServer.version = scenario.version
		recorder := httptest.NewRecorder()
		app.runtimeStatus(recorder, httptest.NewRequest(http.MethodGet, "/runtime/status", nil), op)
		var body freshRuntimeStatusResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode status for %q: %v (%s)", scenario.version, err, recorder.Body.String())
		}
		if body.Gateway == nil || body.Gateway.Version != scenario.version {
			t.Fatalf("status for %q = %+v", scenario.version, body.Gateway)
		}
		if body.Gateway.VersionSupport != scenario.wantSupport || body.Gateway.WriteBlocked != scenario.wantBlocked {
			t.Fatalf("class for %q = %q blocked=%v", scenario.version, body.Gateway.VersionSupport, body.Gateway.WriteBlocked)
		}
	}
}
