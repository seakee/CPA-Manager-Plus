package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
)

const (
	consoleModelsManagementKey = "Cpamp_Console_Models_Management_Key_417283"
	consoleModelsClientKey     = "Cpamp_Console_Models_Client_Key_918273"
)

// consoleModelsCPA is the CPA of one installation: it owns a management
// credential, the client keys it accepts, and it records the authorization of
// every request it is asked to make a decision about.
type consoleModelsCPA struct {
	server *httptest.Server
	// listStatus is the answer the gateway gives the model list. A CPA that
	// refuses the client key answers 401 here.
	listStatus   int
	configStatus int
	clientKeys   []string

	mu    sync.Mutex
	calls []consoleModelsCPACall
}

type consoleModelsCPACall struct {
	path          string
	authorization string
}

func newConsoleModelsCPA(t *testing.T, clientKeys ...string) *consoleModelsCPA {
	t.Helper()
	fake := &consoleModelsCPA{listStatus: http.StatusOK, clientKeys: clientKeys}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.handle))
	t.Cleanup(fake.server.Close)
	return fake
}

func (c *consoleModelsCPA) handle(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.calls = append(c.calls, consoleModelsCPACall{path: r.URL.Path, authorization: r.Header.Get("Authorization")})
	cfgStatus := c.configStatus
	c.mu.Unlock()
	switch r.URL.Path {
	case "/v8/management/config":
		if cfgStatus != 0 && cfgStatus != http.StatusOK {
			w.WriteHeader(cfgStatus)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+consoleModelsManagementKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		keys, err := json.Marshal(c.clientKeys)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"config-version":8,"access":{"api-keys":` + string(keys) + `},"providers":{}}`))
	case "/v1/models":
		if c.listStatus != http.StatusOK {
			w.WriteHeader(c.listStatus)
			_, _ = w.Write([]byte(`{"error":"Invalid API key"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5"},{"id":"claude-sonnet"}]}`))
	default:
		http.NotFound(w, r)
	}
}

func (c *consoleModelsCPA) recorded() []consoleModelsCPACall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]consoleModelsCPACall(nil), c.calls...)
}

// consoleModelsApplication is the completed installation whose committed
// destination is fake.
func consoleModelsApplication(t *testing.T, fake *consoleModelsCPA) *freshApplication {
	t.Helper()
	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)
	app.managedSource = func(context.Context) (freshkeyapply.ManagedInstall, error) {
		return freshkeyapply.ManagedInstall{
			BaseURL:       fake.server.URL,
			ManagementKey: consoleModelsManagementKey,
		}, nil
	}
	return app
}

// The console's model list is read with a client key the CPA already holds: the
// Admin credential the console authenticated with never reaches the CPA, and the
// key that does reach it is never returned to the browser.
func TestFreshConsoleModelListUsesCPAHeldClientKey(t *testing.T) {
	fake := newConsoleModelsCPA(t, consoleModelsClientKey)
	app := consoleModelsApplication(t, fake)
	for _, resource := range []string{"cpamp/v1/models", "cpamp/models"} {
		t.Run(resource, func(t *testing.T) {
			response := installationAPI(t, app, http.MethodGet, resource, strategyTestAdmin)
			if response.Code != http.StatusOK {
				t.Fatalf("model list returned %d: %s", response.Code, response.Body.String())
			}
			var payload struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatalf("model list is not JSON: %v", err)
			}
			if len(payload.Data) != 2 || payload.Data[0].ID != "gpt-5" {
				t.Fatalf("model list = %s, want the CPA's own list", response.Body.String())
			}
			if strings.Contains(response.Body.String(), consoleModelsClientKey) {
				t.Fatal("model list returned the client key it was read with")
			}
		})
	}

	listAuthorization := ""
	for _, call := range fake.recorded() {
		if call.authorization == "Bearer "+strategyTestAdmin {
			t.Fatalf("CPA received the console's Admin credential on %s", call.path)
		}
		if call.path == "/v1/models" {
			listAuthorization = call.authorization
		}
	}
	if listAuthorization != "Bearer "+consoleModelsClientKey {
		t.Fatalf("model list read authorization = %q, want the CPA-held client key", listAuthorization)
	}
}

// A CPA that holds no client key has nothing this console may present. The
// answer is the empty list the System page already renders, not a 401 the user
// could never fix from there.
func TestFreshConsoleModelListIsEmptyWithoutCPAClientKey(t *testing.T) {
	fake := newConsoleModelsCPA(t)
	app := consoleModelsApplication(t, fake)
	response := installationAPI(t, app, http.MethodGet, "cpamp/v1/models", strategyTestAdmin)
	if response.Code != http.StatusOK {
		t.Fatalf("model list returned %d: %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"data":[]`) {
		t.Fatalf("model list = %s, want an empty list", response.Body.String())
	}
	for _, call := range fake.recorded() {
		if call.path == "/v1/models" {
			t.Fatalf("empty installation still asked CPA for its model list with %q", call.authorization)
		}
	}
}

// A CPA that refuses the key this Manager holds is an upstream failure. Relaying
// its 401 would tell the browser its console session expired, which is false.
func TestFreshConsoleModelListDoesNotRelayCPAAuthenticationFailure(t *testing.T) {
	fake := newConsoleModelsCPA(t, consoleModelsClientKey)
	fake.listStatus = http.StatusUnauthorized
	app := consoleModelsApplication(t, fake)
	response := installationAPI(t, app, http.MethodGet, "cpamp/v1/models", strategyTestAdmin)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("model list returned %d: %s, want 502", response.Code, response.Body.String())
	}
}

// The console route is admin-protected, and the CPA is not consulted at all
// before that check passes.
func TestFreshConsoleModelListRejectsUnauthenticatedCaller(t *testing.T) {
	fake := newConsoleModelsCPA(t, consoleModelsClientKey)
	app := consoleModelsApplication(t, fake)
	for _, token := range []string{"", "not-the-admin-key"} {
		response := installationAPI(t, app, http.MethodGet, "cpamp/v1/models", token)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("token %q reached the model list: %d %s", token, response.Code, response.Body.String())
		}
	}
	if calls := fake.recorded(); len(calls) != 0 {
		t.Fatalf("unauthenticated model list reached the CPA: %#v", calls)
	}
}

// Without a committed installation authority there is no CPA this Manager may
// read, and the answer says so instead of presenting a credential anywhere.
func TestFreshConsoleModelListReportsUnavailableWithoutCPAAuthority(t *testing.T) {
	app := newInstallationTestApplication(t)
	markFreshProductComplete(t, app)
	response := installationAPI(t, app, http.MethodGet, "cpamp/v1/models", strategyTestAdmin)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("model list returned %d: %s, want 503", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), consoleModelListUnavailable) {
		t.Fatalf("model list = %s, want the %q code", response.Body.String(), consoleModelListUnavailable)
	}
}

func TestFirstCPAClientKeyProjectsOnlyGatewayClientList(t *testing.T) {
	for _, test := range []struct {
		name     string
		document map[string]any
		want     string
	}{
		{name: "absent access", document: map[string]any{}},
		{name: "absent list", document: map[string]any{"access": map[string]any{}}},
		{name: "not a list", document: map[string]any{"access": map[string]any{"api-keys": "key"}}},
		{name: "empty list", document: map[string]any{"access": map[string]any{"api-keys": []any{}}}},
		{name: "blank entry", document: map[string]any{"access": map[string]any{"api-keys": []any{"  "}}}},
		{
			name: "control character entry",
			document: map[string]any{"access": map[string]any{
				"api-keys": []any{"key\nInjected: 1"},
			}},
		},
		{
			name: "first usable entry",
			document: map[string]any{"access": map[string]any{
				"api-keys": []any{7, " ", "key-one", "key-two"},
			}},
			want: "key-one",
		},
		{
			name: "provider secrets stay behind",
			document: map[string]any{
				"access":    map[string]any{"api-keys": []any{"key-one"}},
				"providers": map[string]any{"openai": map[string]any{"api-key": "provider-secret"}},
			},
			want: "key-one",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := firstCPAClientKey(test.document); got != test.want {
				t.Fatalf("firstCPAClientKey() = %q, want %q", got, test.want)
			}
		})
	}
}

// setConsoleModelsStrategy rewrites the committed checkpoint the console reads.
func setConsoleModelsStrategy(t *testing.T, app *freshApplication, checkpoint bootstrap.FreshStrategyCheckpoint) {
	t.Helper()
	before, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.Stage == "" {
		checkpoint.Stage = before.Operation.Strategy.Stage
	}
	before.Operation.Strategy = checkpoint
	app.store = staticStore{before}
}

// The console reads the CPA its committed strategy names. A process that holds
// both sources — the shape a later dual-wired build has — must answer from the
// strategy, not from whichever source happens to be non-nil.
func TestConsoleCPADestinationFollowsCommittedStrategy(t *testing.T) {
	managedFake := newConsoleModelsCPA(t, consoleModelsClientKey)
	externalFake := newConsoleModelsCPA(t, consoleModelsClientKey)
	app := consoleModelsApplication(t, managedFake)
	app.cfg.CPAUpstreamURL = "http://127.0.0.1:18317"
	app.cfg.ManagementKey = strings.Repeat("k", 32)
	app.externalSource = func(context.Context) (freshkeyapply.ExternalInstall, error) {
		return freshkeyapply.ExternalInstall{
			BaseURL:       externalFake.server.URL,
			ManagementKey: strings.Repeat("e", 64),
		}, nil
	}

	t.Run("new_managed reads the staged destination", func(t *testing.T) {
		baseURL, key, err := app.consoleCPADestination(t.Context())
		if err != nil {
			t.Fatalf("destination = %v", err)
		}
		if baseURL != managedFake.server.URL || key != consoleModelsManagementKey {
			t.Fatalf("destination = %s/%s, want the staged destination", baseURL, key)
		}
	})

	t.Run("adopt_existing network_existing reads the staged destination", func(t *testing.T) {
		setConsoleModelsStrategy(t, app, bootstrap.FreshStrategyCheckpoint{
			Strategy:    bootstrap.AdoptExisting,
			SourceClass: bootstrap.AdoptNetworkExisting,
			Stage:       "committed",
		})
		baseURL, key, err := app.consoleCPADestination(t.Context())
		if err != nil {
			t.Fatalf("destination = %v", err)
		}
		if baseURL != managedFake.server.URL || key != consoleModelsManagementKey {
			t.Fatalf("destination = %s/%s, want the staged destination", baseURL, key)
		}
		resp := installationAPI(t, app, http.MethodGet, "cpamp/v1/models", strategyTestAdmin)
		if resp.Code != http.StatusOK {
			t.Fatalf("adopt_existing network_existing model list = %d %s", resp.Code, resp.Body.String())
		}
	})

	t.Run("adopt_existing legacy_managed fails closed instead of falling through", func(t *testing.T) {
		setConsoleModelsStrategy(t, app, bootstrap.FreshStrategyCheckpoint{
			Strategy:    bootstrap.AdoptExisting,
			SourceClass: bootstrap.AdoptLegacyManaged,
			Stage:       "committed",
		})
		if _, _, err := app.consoleCPADestination(t.Context()); !errors.Is(err, errConsoleModelListUnavailable) {
			t.Fatalf("legacy_managed destination error = %v, want unavailable sentinel", err)
		}
		resp := installationAPI(t, app, http.MethodGet, "cpamp/v1/models", strategyTestAdmin)
		if resp.Code != http.StatusServiceUnavailable {
			t.Fatalf("legacy_managed model list = %d %s, want 503", resp.Code, resp.Body.String())
		}
	})

	t.Run("uncommitted strategy fails closed instead of falling through", func(t *testing.T) {
		setConsoleModelsStrategy(t, app, bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.ConnectExternal,
			Stage:    "staged",
		})
		if _, _, err := app.consoleCPADestination(t.Context()); !errors.Is(err, errConsoleModelListUnavailable) {
			t.Fatalf("uncommitted destination error = %v, want unavailable sentinel", err)
		}
		resp := installationAPI(t, app, http.MethodGet, "cpamp/v1/models", strategyTestAdmin)
		if resp.Code != http.StatusServiceUnavailable {
			t.Fatalf("uncommitted model list = %d %s, want 503", resp.Code, resp.Body.String())
		}
	})

	t.Run("no source falls back to the environment connection", func(t *testing.T) {
		envApp := &freshApplication{
			cfg: config.Config{CPAUpstreamURL: "http://127.0.0.1:18317", ManagementKey: strings.Repeat("k", 32)},
		}
		baseURL, key, err := envApp.consoleCPADestination(t.Context())
		if err != nil {
			t.Fatalf("destination = %v", err)
		}
		if baseURL != "http://127.0.0.1:18317" || key != strings.Repeat("k", 32) {
			t.Fatalf("destination = %s/%s, want the environment connection", baseURL, key)
		}
	})

	t.Run("unreadable checkpoint names no CPA", func(t *testing.T) {
		closed, err := sqlite.Open(filepath.Join(t.TempDir(), "closed.db"))
		if err != nil {
			t.Fatal(err)
		}
		if err := closed.Close(); err != nil {
			t.Fatal(err)
		}
		app.store = freshstore.New(closed)
		if _, _, err := app.consoleCPADestination(t.Context()); !errors.Is(err, errConsoleModelListUnavailable) {
			t.Fatalf("destination error = %v, want the unavailable sentinel", err)
		}
	})
}

// After an External installation the console reads the user's own CPA, and it
// does so even though this process also holds a Manager-owned source.
func TestConsoleCPADestinationReadsTheExternalConnection(t *testing.T) {
	hostCtx, stopHost := context.WithCancel(t.Context())
	defer stopHost()
	opened, app := newUsageCompletionApplication(t, hostCtx, config.Config{})
	managedFake := newConsoleModelsCPA(t, consoleModelsClientKey)
	externalFake := newConsoleModelsCPA(t, consoleModelsClientKey)
	app.managedSource = func(context.Context) (freshkeyapply.ManagedInstall, error) {
		return freshkeyapply.ManagedInstall{BaseURL: managedFake.server.URL, ManagementKey: consoleModelsManagementKey}, nil
	}
	// The fake CPA accepts one management credential; the address is what proves
	// which source the console read.
	externalKey := consoleModelsManagementKey
	normalized, err := cpa.NormalizeV8BaseURL(externalFake.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	protected, err := opened.Protector.ProtectString(externalKey)
	if err != nil {
		t.Fatal(err)
	}
	op, err := app.current(hostCtx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Update(hostCtx, op.Identity, op.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
			Strategy: bootstrap.ConnectExternal, Stage: "committed",
			TargetIdentity: freshkeyapply.ExternalIdentity(normalized), TargetGeneration: 1,
		}
		r.Operation.ProductComplete = true
		r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: normalized, ManagementKey: protected}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	app.externalSource = externalInstallSource(app.store, opened.Protector)
	app.keyApply = freshkeyapply.NewExternal(app.keyRepo, app.keys, app.externalSource)

	baseURL, key, err := app.consoleCPADestination(hostCtx)
	if err != nil {
		t.Fatalf("destination = %v", err)
	}
	if baseURL != normalized || key != externalKey {
		t.Fatalf("destination = %s/%s, want the user's own connection", baseURL, key)
	}

	response := installationAPI(t, app, http.MethodGet, "cpamp/v1/models", strategyTestAdmin)
	if response.Code != http.StatusOK {
		t.Fatalf("model list returned %d: %s", response.Code, response.Body.String())
	}
	if len(externalFake.recorded()) == 0 {
		t.Fatal("the external CPA was never asked for its models")
	}
	if len(managedFake.recorded()) != 0 {
		t.Fatalf("the Manager's own source answered an External installation: %v", managedFake.recorded())
	}
}

// On ConnectExternal, presenting a rotated management key on cpamp/v1/models
// records the durable External rejection and stops re-sending the refused key.
func TestConsoleModelListHonoursExternalCredentialRejectionGate(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			hostCtx, stopHost := context.WithCancel(t.Context())
			defer stopHost()
			opened, app := newUsageCompletionApplication(t, hostCtx, config.Config{})
			externalFake := newConsoleModelsCPA(t, consoleModelsClientKey)
			externalFake.mu.Lock()
			externalFake.configStatus = status
			externalFake.mu.Unlock()

			normalized, err := cpa.NormalizeV8BaseURL(externalFake.server.URL)
			if err != nil {
				t.Fatal(err)
			}
			protected, err := opened.Protector.ProtectString(consoleModelsManagementKey)
			if err != nil {
				t.Fatal(err)
			}
			op, err := app.current(hostCtx)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := app.store.Update(hostCtx, op.Identity, op.Revision, func(r *freshstore.Record) error {
				r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
					Strategy: bootstrap.ConnectExternal, Stage: "committed",
					TargetIdentity: freshkeyapply.ExternalIdentity(normalized), TargetGeneration: 1,
				}
				r.Operation.ProductComplete = true
				r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: normalized, ManagementKey: protected}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			app.externalSource = externalInstallSource(app.store, opened.Protector)
			app.keyApply = freshkeyapply.NewExternal(app.keyRepo, app.keys, app.externalSource)

			first := installationAPI(t, app, http.MethodGet, "cpamp/v1/models", strategyTestAdmin)
			if first.Code != http.StatusConflict {
				t.Fatalf("first model list = %d %s, want 409", first.Code, first.Body.String())
			}
			if code := errorCode(t, first); code != freshconfig.ReasonManagementRejected {
				t.Fatalf("first code = %q, want %q", code, freshconfig.ReasonManagementRejected)
			}
			if status == http.StatusForbidden && !strings.Contains(first.Body.String(), externalRebindForbiddenDetail) {
				t.Fatalf("403 response missing forbidden detail: %s", first.Body.String())
			}
			if !app.externalReadGrant(hostCtx).Refused {
				t.Fatal("External rejection was not recorded after model list auth failure")
			}

			before := len(externalFake.recorded())
			for range 3 {
				repeated := installationAPI(t, app, http.MethodGet, "cpamp/v1/models", strategyTestAdmin)
				if repeated.Code != http.StatusConflict || errorCode(t, repeated) != freshconfig.ReasonManagementRejected {
					t.Fatalf("repeated model list = %d %s", repeated.Code, repeated.Body.String())
				}
			}
			if after := len(externalFake.recorded()); after != before {
				t.Fatalf("short-circuited model list sent %d additional requests to External CPA", after-before)
			}
		})
	}
}
