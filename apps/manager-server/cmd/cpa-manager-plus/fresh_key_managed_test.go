package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

// The default install's destination CPA persists its management credential as a
// bcrypt digest of the secret the Supervisor staged.
const managedInstallBcrypt = "$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12"

// managedKeyDestination is the destination CPA of a default install: the managed
// template, the management credential gate and the client-key projection the
// initial install and every key operation read back.
type managedKeyDestination struct {
	mu         sync.Mutex
	management string
	keys       []string
	writes     int
	authDir    string
	pluginDir  string
	extraRoots map[string]any
}

func (d *managedKeyDestination) document() []byte {
	d.mu.Lock()
	defer d.mu.Unlock()
	keys := make([]any, 0, len(d.keys))
	for _, key := range d.keys {
		keys = append(keys, key)
	}
	authDir := d.authDir
	if authDir == "" {
		authDir = "/runtime/supervisor/destination/auth"
	}
	pluginDir := d.pluginDir
	if pluginDir == "" {
		pluginDir = "/runtime/supervisor/plugins"
	}
	doc := map[string]any{
		"config-version": 8,
		"server":         map[string]any{"host": "0.0.0.0", "port": 8317},
		"management": map[string]any{"allow-remote": true, "secret-key": managedInstallBcrypt,
			"disable-control-panel": true, "disable-auto-update-panel": true},
		"access":        map[string]any{"api-keys": keys},
		"oauth":         map[string]any{"auth-dir": authDir},
		"plugins":       map[string]any{"enabled": false, "dir": pluginDir},
		"observability": map[string]any{"logs": map[string]any{"logging-to-file": false}, "usage": map[string]any{"usage-statistics-enabled": true}},
	}
	for k, v := range d.extraRoots {
		doc[k] = v
	}
	body, _ := json.Marshal(doc)
	return body
}

// seed mirrors the destination CPA that was staged with exactly the Client Key
// the installation persisted before it started.
func (d *managedKeyDestination) seed(keys ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.keys = slices.Clone(keys)
}

func (d *managedKeyDestination) installed() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.keys)
}

func (d *managedKeyDestination) serve(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v8/") && r.Header.Get("Authorization") != "Bearer "+d.management {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch {
		case r.Method == http.MethodGet && (r.URL.Path == "/v8/management/config" || r.URL.Path == "/v8/management/config.yaml"):
			_, _ = w.Write(d.document())
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config/access/api-keys":
			keys := d.installed()
			if keys == nil {
				keys = []string{}
			}
			_ = json.NewEncoder(w).Encode(keys)
		case r.Method == http.MethodPut && r.URL.Path == "/v8/management/config/access/api-keys":
			var keys []string
			if json.NewDecoder(r.Body).Decode(&keys) != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			d.mu.Lock()
			d.writes++
			d.keys = slices.Clone(keys)
			d.mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			d.mu.Lock()
			accepted := auth != "" && slices.Contains(d.keys, auth)
			d.mu.Unlock()
			if accepted {
				_, _ = w.Write([]byte(`{"data":[]}`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// managedInstallRuntimeStatus is the Runtime this default install committed: the
// checkpoint pins its identity, generation and active artifact.
func managedInstallRuntimeStatus() model.RuntimeObservedStatus {
	return model.RuntimeObservedStatus{
		Identity: "embedded-default", Generation: 7, ProtocolVersion: "v1",
		State:                 model.RuntimeStateReady,
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))},
	}
}

// managedKeyRuntime serves the authenticated Supervisor status contract, so the
// application under test reaches the Runtime through its production client.
func managedKeyRuntime(t *testing.T, status model.RuntimeObservedStatus, token string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/runtime/status" || r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"protocolVersion":       string(status.ProtocolVersion),
			"runtimeIdentity":       string(status.Identity),
			"runtimeGeneration":     status.Generation,
			"state":                 string(status.State),
			"cpaObservedVersion":    string(status.CPAObservedVersion),
			"activeGatewayArtifact": map[string]any{"engine": status.ActiveGatewayArtifact.Engine, "artifactId": string(status.ActiveGatewayArtifact.ArtifactID), "version": status.ActiveGatewayArtifact.Version},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// managedInstallHarness is one completed default install: a protected Manager
// connection to a synthetic destination CPA, a committed managed checkpoint and
// the application the Compose installation actually builds.
type managedInstallHarness struct {
	app         *freshApplication
	destination *managedKeyDestination
	endpoint    string
	installKey  freshkeys.InitialKey
}

// managedInstallEnv is what a harness install state is built from: the Runtime
// the Supervisor publishes, the destination CPA address and the protected
// Manager connection to it.
type managedInstallEnv struct {
	status    model.RuntimeObservedStatus
	endpoint  string
	protected string
}

// managedInstallSetup writes one installation checkpoint into a pristine record.
type managedInstallSetup func(*freshstore.Record, managedInstallEnv)

// completedManagedInstall is the default Compose install: a committed managed
// destination bound to the protected Manager connection, product complete.
func completedManagedInstall(r *freshstore.Record, env managedInstallEnv) {
	r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: r.Operation.Identity.OperationID, Revision: 1, Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true}
	r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.NewManaged, Stage: "committed",
		TargetIdentity: string(env.status.Identity), TargetGeneration: uint64(env.status.Generation),
		TargetArtifactID:              string(env.status.ActiveGatewayArtifact.ArtifactID),
		DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity(env.endpoint),
	}
	r.Connection = &model.ManagerCPAConnectionConfig{CPABaseURL: env.endpoint, ManagementKey: env.protected}
	r.Operation.ProductComplete = true
}

func newManagedInstallHarness(t *testing.T) *managedInstallHarness {
	t.Helper()
	return newManagedInstallHarnessWith(t, completedManagedInstall, true)
}

// newLegacyManagedInstallHarness is the pre-F5 default install: a committed
// managed destination whose CPA started with no client key at all.
func newLegacyManagedInstallHarness(t *testing.T) *managedInstallHarness {
	t.Helper()
	return newManagedInstallHarnessWith(t, completedManagedInstall, false)
}

// newManagedInstallHarnessWith builds one installation. withInstallKey mirrors
// the F5 flow: the installation mints its Client Key, claims the single
// initial-install journal and stages the destination with that key, so CPA never
// starts with client authentication disabled.
func newManagedInstallHarnessWith(t *testing.T, install managedInstallSetup, withInstallKey bool) *managedInstallHarness {
	t.Helper()
	destination := &managedKeyDestination{management: strings.Repeat("e", 64)}
	destinationServer := destination.serve(t)
	status := managedInstallRuntimeStatus()
	runtimeServer := managedKeyRuntime(t, status, "managed-key-runtime-token")
	tokenPath := filepath.Join(t.TempDir(), "runtime-token")
	if err := os.WriteFile(tokenPath, []byte("managed-key-runtime-token"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	app := newFreshApplication(opened, config.Config{
		RuntimeURL: runtimeServer.URL, RuntimeTokenFile: tokenPath, CPAUpstreamURL: destinationServer.URL,
	}, nil)
	protected, err := opened.Protector.ProtectString(destination.management)
	if err != nil {
		t.Fatal(err)
	}
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	env := managedInstallEnv{status: status, endpoint: destinationServer.URL, protected: protected}
	record, err = app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		install(r, env)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	harness := &managedInstallHarness{app: app, destination: destination, endpoint: destinationServer.URL}
	if withInstallKey {
		checkpoint := record.Operation.Strategy
		if checkpoint.Strategy == bootstrap.AdoptExisting {
			const adoptedSeed = "adopted-client-key-01"
			destination.seed(adoptedSeed)
			registered, err := app.keyApply.RegisterAdoption(t.Context(), []string{adoptedSeed})
			if err != nil {
				t.Fatal(err)
			}
			harness.installKey = freshkeys.InitialKey{ID: registered[0].ID, RequestID: registered[0].RequestID, Secret: adoptedSeed}
		} else {
			key, err := app.keyApply.EnsureInitialInstall(t.Context(), freshkeyapply.ManagedRunID(string(record.Operation.Identity.OperationID)), checkpoint.TargetGeneration)
			if err != nil {
				t.Fatal(err)
			}
			destination.seed(key.Secret)
			harness.installKey = key
		}
	}
	return harness
}

func (h *managedInstallHarness) call(t *testing.T, method, resource, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "/control/api/v2/"+resource, bytes.NewBufferString(body))
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	h.app.canonicalAPI(response, request)
	return response
}

func (h *managedInstallHarness) createKey(t *testing.T, requestID string) freshkeys.Result {
	t.Helper()
	response := h.call(t, http.MethodPost, "api-keys", `{"requestId":"`+requestID+`"}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("create key = %d %s", response.Code, response.Body.String())
	}
	var result freshkeys.Result
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.CreatedSecret == "" {
		t.Fatalf("create key result: %v %s", err, response.Body.String())
	}
	return result
}

// TestCompletedDefaultInstallOwnsKeyInstallAdmissionAndGatewayConfig is the F2/F4
// regression: a completed default install stages its authority from the
// committed checkpoint and the protected Manager connection, not from the
// private attested runtime constants. The private identity comparison this
// replaces answered 409 with an empty body for every one of these surfaces.
func TestCompletedDefaultInstallOwnsKeyInstallAdmissionAndGatewayConfig(t *testing.T) {
	harness := newManagedInstallHarness(t)
	op, err := harness.app.current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if op.Strategy.TargetIdentity == cpa.PrivateKeyRuntime {
		t.Fatal("test installation reused the private attested identity")
	}
	if harness.app.keyApply == nil || !harness.app.keyApply.Targets(op, freshkeyapply.SurfaceKeyInstall) {
		t.Fatal("completed default install is not the key authority's installation")
	}
	if harness.app.keyApply.ManagedEndpoint() != harness.endpoint {
		t.Fatalf("key authority endpoint = %q, want %q", harness.app.keyApply.ManagedEndpoint(), harness.endpoint)
	}
	// The install secret is never durable in plaintext: the canonical record
	// holds only the protected envelope, and no manager file or journal row
	// carries the key.
	record, err := harness.app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(record)
	if err != nil || bytes.Contains(public, []byte(harness.installKey.Secret)) {
		t.Fatal("install key leaked into the Fresh record")
	}
	stored, err := filepath.Glob(string(record.Operation.Identity.DatabasePath) + "*")
	if err != nil || len(stored) == 0 {
		t.Fatalf("manager store files: %v", err)
	}
	for _, name := range stored {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(harness.installKey.Secret)) {
			t.Fatalf("install key stored in plaintext in %s", filepath.Base(name))
		}
	}

	// F5: the destination was staged with the installation's Client Key before
	// CPA started, so anonymous Compose traffic is already refused and Gateway
	// admission is proven at completion.
	if !harness.app.gatewayAdmissionProven(t.Context()) {
		t.Fatal("admission not proven for an installation that started with its client key")
	}
	if got := harness.destination.installed(); len(got) != 1 || got[0] != harness.installKey.Secret {
		t.Fatalf("destination keys = %v", got)
	}
	listWhenAdmitted := harness.call(t, http.MethodGet, "api-keys", "")
	if listWhenAdmitted.Code != http.StatusOK || !strings.Contains(listWhenAdmitted.Body.String(), `"gatewayPending":false`) {
		t.Fatalf("admitted api-keys list = %d %s", listWhenAdmitted.Code, listWhenAdmitted.Body.String())
	}
	legacyHarness := newLegacyManagedInstallHarness(t)
	listWhenPending := legacyHarness.call(t, http.MethodGet, "api-keys", "")
	if listWhenPending.Code != http.StatusOK || !strings.Contains(listWhenPending.Body.String(), `"gatewayPending":true`) {
		t.Fatalf("unadmitted api-keys list = %d %s", listWhenPending.Code, listWhenPending.Body.String())
	}

	// The install route sees that key as already installed: it observes the live
	// destination instead of installing a second key, and writes nothing.
	install := harness.call(t, http.MethodPost, "api-keys/"+string(harness.installKey.ID)+"/initial-install", `{}`)
	if install.Code != http.StatusOK {
		t.Fatalf("initial install = %d %s", install.Code, install.Body.String())
	}
	var state freshkeyapply.State
	if err := json.Unmarshal(install.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if !state.ManagerStaged || !state.Attempted || !state.CPAConfigured || state.RuntimeState != "effective_observed" || state.InstallAvailable {
		t.Fatalf("install key state = %+v", state)
	}
	if harness.destination.writes != 0 {
		t.Fatalf("install key was installed a second time (%d writes)", harness.destination.writes)
	}
	// A key created later cannot consume the already-installed initial install.
	installed := harness.createKey(t, strings.Repeat("1", 32))
	blocked := harness.call(t, http.MethodPost, "api-keys/"+string(installed.Key.ID)+"/initial-install", `{}`)
	if blocked.Code != http.StatusConflict || harness.destination.writes != 0 {
		t.Fatalf("workspace install = %d %s", blocked.Code, blocked.Body.String())
	}

	// Rename is authorised by the same installation.
	detail := harness.call(t, http.MethodGet, "api-keys/"+string(harness.installKey.ID), "")
	if detail.Code != http.StatusOK {
		t.Fatalf("key detail = %d %s", detail.Code, detail.Body.String())
	}
	var key freshkeys.Key
	if err = json.Unmarshal(detail.Body.Bytes(), &key); err != nil {
		t.Fatal(err)
	}
	if key.Origin != freshkeys.OriginInstaller || key.Name != freshkeys.InstallerOriginName {
		t.Fatalf("install key projection = %+v", key)
	}
	renamed := harness.call(t, http.MethodPut, "api-keys/"+string(key.ID),
		`{"name":"Primary","nameRevision":`+strconv.FormatInt(key.NameRevision, 10)+`}`)
	if renamed.Code != http.StatusOK {
		t.Fatalf("rename = %d %s", renamed.Code, renamed.Body.String())
	}

	// Replace runs the whole add/revoke journal from the install key through the
	// install-derived per-step authority: no Host permit exists on a default
	// install.
	keeper := harness.createKey(t, strings.Repeat("2", 32))
	replaceID := strings.Repeat("3", 32)
	prepare := harness.call(t, http.MethodPost, "api-keys/"+string(harness.installKey.ID)+"/revocation",
		`{"requestId":"`+replaceID+`","keeperId":"`+string(keeper.Key.ID)+`","mode":"replace"}`)
	if prepare.Code != http.StatusOK {
		t.Fatalf("replace prepare = %d %s", prepare.Code, prepare.Body.String())
	}
	var result freshkeyapply.OperationResult
	for step := range 2 {
		cont := harness.call(t, http.MethodPost, "api-keys/operations/"+replaceID+"/continue", `{}`)
		if cont.Code != http.StatusOK {
			t.Fatalf("replace step %d = %d %s", step, cont.Code, cont.Body.String())
		}
		if err = json.Unmarshal(cont.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
	}
	if result.Operation == nil || result.Operation.Outcome != "committed" || result.RuntimeState != "effective_observed" {
		t.Fatalf("replace outcome = %+v", result)
	}
	if got := harness.destination.installed(); len(got) != 1 || got[0] != keeper.CreatedSecret {
		t.Fatalf("destination keys after replace = %v", got)
	}
	if harness.destination.writes != 2 {
		t.Fatalf("replace wrote %d times, want 2", harness.destination.writes)
	}

	// Revoking the only remaining key is refused by the authority itself, which
	// proves the gate let the request through instead of rejecting it blind.
	lone := harness.createKey(t, strings.Repeat("4", 32))
	revoke := harness.call(t, http.MethodPost, "api-keys/"+string(keeper.Key.ID)+"/revocation",
		`{"requestId":"`+strings.Repeat("5", 32)+`","keeperId":"`+string(lone.Key.ID)+`","mode":"revoke"}`)
	if revoke.Code != http.StatusConflict {
		t.Fatalf("lone revoke = %d %s", revoke.Code, revoke.Body.String())
	}
	var refused struct {
		Code string `json:"code"`
	}
	if err = json.Unmarshal(revoke.Body.Bytes(), &refused); err != nil || refused.Code != "last_effective_key" {
		t.Fatalf("lone revoke body = %s (%v)", revoke.Body.String(), err)
	}
	if harness.destination.writes != 2 {
		t.Fatalf("refused revocation wrote (%d writes)", harness.destination.writes)
	}

	// G: the Ingress admission signal is proven from the live destination.
	if !harness.app.gatewayAdmissionProven(t.Context()) {
		t.Fatal("Gateway admission not proven after installing a client key")
	}
	authority := authorityResponse(t, harness.app)
	if authority.Code != http.StatusOK {
		t.Fatalf("authority = %d %s", authority.Code, authority.Body.String())
	}
	var posture struct {
		GatewayAdmission struct {
			Admitted bool `json:"admitted"`
		} `json:"gatewayAdmission"`
	}
	if err = json.Unmarshal(authority.Body.Bytes(), &posture); err != nil || !posture.GatewayAdmission.Admitted {
		t.Fatalf("authority admission = %s (%v)", authority.Body.String(), err)
	}

	// F4: the gateway configuration page writes through the same authority.
	page := harness.call(t, http.MethodGet, "gateway/config", "")
	if page.Code != http.StatusOK {
		t.Fatalf("gateway config = %d %s", page.Code, page.Body.String())
	}
	var view struct {
		Capability freshconfig.Capability `json:"capability"`
		Generation string                 `json:"generation"`
	}
	if err = json.Unmarshal(page.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Capability.Mode != freshconfig.ModeManaged || !view.Capability.Readable || !view.Capability.Writable || !view.Capability.RulesWritable {
		t.Fatalf("gateway capability = %+v", view.Capability)
	}
	if view.Generation != strconv.FormatUint(op.Strategy.TargetGeneration, 10) {
		t.Fatalf("gateway generation = %q", view.Generation)
	}
}

// A committed install created before the install key existed started CPA with
// an empty client list. Admission therefore stays closed, and the workspace can
// still install its first key exactly as it did before F5.
func TestLegacyManagedInstallWithoutInstallKeyStaysClosedUntilAKeyIsInstalled(t *testing.T) {
	harness := newLegacyManagedInstallHarness(t)
	op, err := harness.app.current(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if harness.app.gatewayAdmissionProven(t.Context()) {
		t.Fatal("admission proven without an installed client key")
	}
	if got := harness.destination.installed(); len(got) != 0 {
		t.Fatalf("legacy destination keys = %v", got)
	}
	installed := harness.createKey(t, strings.Repeat("1", 32))
	install := harness.call(t, http.MethodPost, "api-keys/"+string(installed.Key.ID)+"/initial-install", `{}`)
	if install.Code != http.StatusOK {
		t.Fatalf("initial install = %d %s", install.Code, install.Body.String())
	}
	var state freshkeyapply.State
	if err := json.Unmarshal(install.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if !state.ManagerStaged || !state.Attempted || !state.CPAConfigured || state.RuntimeState != "effective_observed" {
		t.Fatalf("initial install state = %+v", state)
	}
	if got := harness.destination.installed(); len(got) != 1 || got[0] != installed.CreatedSecret {
		t.Fatalf("destination keys = %v", got)
	}
	if key, err := harness.app.keys.Detail(t.Context(), string(installed.Key.ID)); err != nil || key.Origin != freshkeys.OriginWorkspace {
		t.Fatalf("workspace projection = %+v %v", key, err)
	}
	if !harness.app.gatewayAdmissionProven(t.Context()) || op.Strategy.Stage != "committed" {
		t.Fatal("admission not proven after installing the first client key")
	}
}

// Every surface that accepted the private attested identity still refuses a
// checkpoint this installation does not own: incomplete and unowned.
func TestKeySurfaceRefusesCheckpointsTheAuthorityDoesNotOwn(t *testing.T) {
	committedRouting := func(r *freshstore.Record) {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{OperationID: r.Operation.Identity.OperationID,
			Revision: 1, Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true}
	}
	for _, scenario := range []struct {
		name    string
		install managedInstallSetup
	}{
		{"incomplete", func(r *freshstore.Record, env managedInstallEnv) {
			committedRouting(r)
			r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
				Strategy: bootstrap.NewManaged, Stage: "staged",
				TargetIdentity: string(env.status.Identity), TargetGeneration: uint64(env.status.Generation),
				TargetArtifactID: string(env.status.ActiveGatewayArtifact.ArtifactID),
			}
		}},
		{"unowned", func(r *freshstore.Record, _ managedInstallEnv) {
			committedRouting(r)
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			harness := newManagedInstallHarnessWith(t, scenario.install, false)
			op, err := harness.app.current(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if harness.app.keyApply.Targets(op, freshkeyapply.SurfaceKeyInstall) {
				t.Fatal("authority accepted a checkpoint it does not own")
			}
			if harness.app.keyOperationTarget(op) {
				t.Fatal("key operations authorised for a foreign checkpoint")
			}
			installed := harness.createKey(t, strings.Repeat("6", 32))
			install := harness.call(t, http.MethodPost, "api-keys/"+string(installed.Key.ID)+"/initial-install", `{}`)
			if install.Code != http.StatusConflict || install.Body.Len() != 0 {
				t.Fatalf("initial install = %d %s", install.Code, install.Body.String())
			}
			if got := harness.destination.installed(); len(got) != 0 {
				t.Fatalf("refused checkpoint wrote %v", got)
			}
			// The chat consumer answers the same installation question.
			t.Setenv("CPAMP_PRIVATE_S2A_CHAT", "1")
			chat := httptest.NewRequest(http.MethodPost, privateChatAdmissionPath,
				strings.NewReader(`{"requestBinding":"`+strings.Repeat("a", 64)+`"}`))
			chat.RemoteAddr = "127.0.0.1:30000"
			chat.Header.Set("Authorization", "Bearer cpamp_sk_"+strings.Repeat("c", 64))
			chat.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			harness.app.ServeHTTP(recorder, chat)
			if recorder.Code != http.StatusServiceUnavailable || recorder.Body.Len() != 0 {
				t.Fatalf("chat admission = %d %s", recorder.Code, recorder.Body.String())
			}
			// Admission and the gateway page fail closed as well.
			if harness.app.gatewayAdmissionProven(t.Context()) {
				t.Fatal("admission proven for a foreign checkpoint")
			}
			page := harness.call(t, http.MethodGet, "gateway/config", "")
			if page.Code != http.StatusOK {
				t.Fatalf("gateway config = %d %s", page.Code, page.Body.String())
			}
			var view struct {
				Capability freshconfig.Capability `json:"capability"`
			}
			if err = json.Unmarshal(page.Body.Bytes(), &view); err != nil {
				t.Fatal(err)
			}
			if view.Capability.Mode != freshconfig.ModeUnavailable || view.Capability.Writable {
				t.Fatalf("gateway capability = %+v", view.Capability)
			}
		})
	}
}

// The explicit private H1/direct profile keeps its own authority selection: it
// owns the attested private target and never the managed installation. The one
// discriminator still restores each surface's pre-cutover contract, so the key
// install, key operation and chat admission routes refuse a checkpoint that is
// merely adopted even when it names the attested private identity; only the
// private gateway page and field journal ever shipped without the managed
// strategy.
func TestPrivateProfileKeepsItsOwnAuthoritySelection(t *testing.T) {
	t.Setenv("CPAMP_PRIVATE_S1_PERMIT", freshkeyapply.PermitPath)
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	defer opened.Close()
	app := newFreshApplication(opened, config.Config{RuntimeURL: "http://127.0.0.1:9081", CPAUpstreamURL: "http://127.0.0.1:8317"}, nil)
	if app.keyApply == nil {
		t.Fatal("the private profile lost its key authority")
	}
	committedPrivate := bootstrap.FreshOperation{Strategy: bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.NewManaged, Stage: "committed",
		TargetIdentity: cpa.PrivateKeyRuntime, TargetArtifactID: cpa.PrivateKeyArtifact, TargetGeneration: 3,
	}}
	adoptedPrivate := bootstrap.FreshOperation{Strategy: bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.AdoptExisting, Stage: "committed",
		TargetIdentity: cpa.PrivateKeyRuntime, TargetArtifactID: cpa.PrivateKeyArtifact, TargetGeneration: 3,
	}}
	managed := bootstrap.FreshOperation{Strategy: bootstrap.FreshStrategyCheckpoint{
		Strategy: bootstrap.NewManaged, Stage: "committed",
		TargetIdentity: "embedded-default", TargetGeneration: 7, TargetArtifactID: "sha256:" + strings.Repeat("a", 64),
		DestinationConnectionIdentity: "managed:sha256:" + strings.Repeat("c", 64),
	}}
	// The committed private target is the private key authority on every surface.
	for _, surface := range []freshkeyapply.Surface{
		freshkeyapply.SurfaceKeyInstall, freshkeyapply.SurfaceKeyOperation,
		freshkeyapply.SurfaceChatAdmission, freshkeyapply.SurfaceGatewayConfig,
	} {
		if !app.keyApply.Targets(committedPrivate, surface) {
			t.Fatalf("the private profile no longer owns %s for the attested target", surface)
		}
		if app.keyApply.Targets(managed, surface) {
			t.Fatalf("the private profile accepted a managed installation on %s", surface)
		}
	}
	// An adopted checkpoint is not a key write path, exactly as before the
	// single discriminator existed.
	for _, surface := range []freshkeyapply.Surface{
		freshkeyapply.SurfaceKeyInstall, freshkeyapply.SurfaceKeyOperation, freshkeyapply.SurfaceChatAdmission,
	} {
		if app.keyApply.Targets(adoptedPrivate, surface) {
			t.Fatalf("%s accepted an adopted private checkpoint", surface)
		}
	}
	if !app.keyApply.Targets(adoptedPrivate, freshkeyapply.SurfaceGatewayConfig) {
		t.Fatal("the private page lost its adopted target scope")
	}
}

func TestGatewayAdmissionProvenBindsManagedAndAdoptedToConfiguredSupervisorRoot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		strategy bootstrap.CPAStrategy
	}{
		{name: "new_managed", strategy: bootstrap.NewManaged},
		{name: "adopt_existing", strategy: bootstrap.AdoptExisting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			customDataDir := t.TempDir()
			customSupRoot := filepath.ToSlash(filepath.Join(customDataDir, "runtime", "supervisor"))

			destination := &managedKeyDestination{
				management: strings.Repeat("e", 64),
				authDir:    customSupRoot + "/destination/auth",
				pluginDir:  customSupRoot + "/plugins",
			}
			if tc.strategy == bootstrap.AdoptExisting {
				destination.extraRoots = map[string]any{
					"routing": map[string]any{"strategy": "round-robin"},
				}
				destination.seed("source-key-one", "source-key-two")
			}
			destinationServer := destination.serve(t)
			status := managedInstallRuntimeStatus()
			runtimeServer := managedKeyRuntime(t, status, "managed-key-runtime-token")
			tokenPath := filepath.Join(t.TempDir(), "runtime-token")
			if err := os.WriteFile(tokenPath, []byte("managed-key-runtime-token"), 0o600); err != nil {
				t.Fatal(err)
			}

			storeDir := t.TempDir()
			paths := legacypreflight.DefaultPaths(filepath.Join(storeDir, "manager.sqlite"))
			paths.Config = filepath.Join(storeDir, "config.json")
			opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = opened.Close() })

			app := newFreshApplication(opened, config.Config{
				DataDir:          customDataDir,
				RuntimeURL:       runtimeServer.URL,
				RuntimeTokenFile: tokenPath,
				CPAUpstreamURL:   destinationServer.URL,
			}, nil)
			protected, err := opened.Protector.ProtectString(destination.management)
			if err != nil {
				t.Fatal(err)
			}
			record, err := app.store.Load(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			record, err = app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
				r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
					OperationID: r.Operation.Identity.OperationID,
					Revision:    1,
					Target:      "/control",
					Stage:       bootstrap.PrefixCommitted,
					Committed:   true,
				}
				checkpoint := bootstrap.FreshStrategyCheckpoint{
					Strategy:                      tc.strategy,
					SourceClass:                   bootstrap.AdoptSourceNone,
					Stage:                         "committed",
					TargetIdentity:                string(status.Identity),
					TargetGeneration:              uint64(status.Generation),
					TargetArtifactID:              string(status.ActiveGatewayArtifact.ArtifactID),
					DestinationConnectionIdentity: freshstrategy.ManagedDestinationIdentity(destinationServer.URL),
				}
				if tc.strategy == bootstrap.AdoptExisting {
					snapshotCipher, protectErr := opened.Protector.ProtectString("test-only encrypted adopt snapshot")
					if protectErr != nil {
						return protectErr
					}
					r.AdoptionCiphertext = snapshotCipher
					checkpoint.SourceClass = bootstrap.AdoptNetworkExisting
					checkpoint.SourceIdentity = "external:sha256:" + strings.Repeat("b", 64)
					checkpoint.DestinationConnectionIdentity = freshstrategy.AdoptedDestinationIdentity(destinationServer.URL)
					checkpoint.AdoptionResources = []bootstrap.ResourceRef{
						bootstrap.AdoptionRefClientAPIKeys,
						bootstrap.AdoptionRefCPAAuthFiles,
						bootstrap.AdoptionRefConfigServer,
						bootstrap.AdoptionRefProviderStateProviders,
					}
					checkpoint.AdoptionDecisions = []bootstrap.AdoptionResourceDecision{
						{ResourceRef: bootstrap.AdoptionRefClientAPIKeys, Kind: bootstrap.APIKeys, Scope: bootstrap.ClientAPIKeys, ReadEvidenceRef: "preflight:keys-read", ReplayEvidenceRef: "preflight:keys-replay", VerifyEvidenceRef: "preflight:keys-verify", Qualification: bootstrap.CapabilityQualified, Disposition: bootstrap.Migrate},
						{ResourceRef: bootstrap.AdoptionRefCPAAuthFiles, Kind: bootstrap.Credentials, Scope: bootstrap.CPAAuthFiles, Qualification: bootstrap.CapabilityPending, ManualAction: bootstrap.ReauthenticateAfterAdoption, Disposition: bootstrap.ManualAction},
						{ResourceRef: bootstrap.AdoptionRefConfigServer, Kind: bootstrap.CPAConfiguration, Scope: bootstrap.ManagementAPIFields, Qualification: bootstrap.CapabilityPending, ManualAction: bootstrap.ReconfigureAfterAdoption, Disposition: bootstrap.ManualAction},
						{ResourceRef: bootstrap.AdoptionRefProviderStateProviders, Kind: bootstrap.ProviderRuntimeState, Scope: bootstrap.ProviderState, Qualification: bootstrap.CapabilityPending, ManualAction: bootstrap.ReauthenticateAfterAdoption, Disposition: bootstrap.ManualAction},
					}
				}
				r.Operation.Strategy = checkpoint
				r.Connection = &model.ManagerCPAConnectionConfig{
					CPABaseURL:    destinationServer.URL,
					ManagementKey: protected,
				}
				r.Operation.ProductComplete = true
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if tc.strategy == bootstrap.NewManaged {
				key, installErr := app.installInitialClientKey(t.Context(), record.Operation, record.Operation.Strategy.TargetGeneration)
				if installErr != nil {
					t.Fatal(installErr)
				}
				destination.seed(key.Secret)
			} else {
				if _, regErr := app.keyApply.RegisterAdoption(t.Context(), []string{"source-key-one", "source-key-two"}); regErr != nil {
					t.Fatal(regErr)
				}
			}
			// 1. Custom <DataDir>/runtime/supervisor root is admitted.
			if !app.gatewayAdmissionProven(t.Context()) {
				t.Fatalf("configured custom Supervisor root (%s) was not admitted for %s", customSupRoot, tc.name)
			}

			// 2. Untrusted same-suffix prefixes, child-writable gateway/destination/auth,
			// and unconfigured container roots are all rejected.
			for driftName, mutate := range map[string]func(){
				"same-suffix untrusted auth prefix": func() {
					destination.authDir = "/tmp/foreign/runtime/supervisor/destination/auth"
					destination.pluginDir = customSupRoot + "/plugins"
				},
				"same-suffix untrusted plugin prefix": func() {
					destination.authDir = customSupRoot + "/destination/auth"
					destination.pluginDir = "/tmp/foreign/runtime/supervisor/plugins"
				},
				"child-writable gateway/destination/auth under DataDir": func() {
					destination.authDir = filepath.ToSlash(filepath.Join(customDataDir, "runtime", "gateway", "destination", "auth"))
					destination.pluginDir = customSupRoot + "/plugins"
				},
				"child-writable /runtime/gateway/destination/auth": func() {
					destination.authDir = "/runtime/gateway/destination/auth"
					destination.pluginDir = customSupRoot + "/plugins"
				},
				"unconfigured /runtime/supervisor container root": func() {
					destination.authDir = "/runtime/supervisor/destination/auth"
					destination.pluginDir = "/runtime/supervisor/plugins"
				},
			} {
				destination.mu.Lock()
				mutate()
				destination.mu.Unlock()
				if app.gatewayAdmissionProven(t.Context()) {
					t.Fatalf("%s unexpectedly admitted Gateway on %s", driftName, tc.name)
				}
			}

			// Restore canonical instance paths and re-verify admission recovers.
			destination.mu.Lock()
			destination.authDir = customSupRoot + "/destination/auth"
			destination.pluginDir = customSupRoot + "/plugins"
			destination.mu.Unlock()
			if !app.gatewayAdmissionProven(t.Context()) {
				t.Fatalf("restored custom Supervisor root failed admission on %s", tc.name)
			}
		})
	}
}
