package upgrade

// These tests pin the Managed CPA import step end to end against a stand-in CPA
// v8 destination and a stand-in Runtime: the version gate before any write, the
// copy of configuration and credentials, the plugin choice, the target proof and
// the stage the run records.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
)

// importFixture is a stopped 1.x volume with a recorded copy-in and the managed
// CPA directory beside it.
type importFixture struct {
	legacyRoot  string
	targetRoot  string
	cpaDir      string
	credentials map[string]string
}

// newImportFixture performs the copy-in a real run performs and writes the CPA
// directory beside the source, so the import starts from the exact state the
// previous step leaves behind.
func newImportFixture(t *testing.T, config string, credentials map[string]string) importFixture {
	t.Helper()
	volume := newLegacyVolume(t)
	targetRoot := canonicalTempDir(t)
	lock := volume.lock(t)
	if _, err := CopyIn(context.Background(), Options{
		LegacyRoot: volume.root,
		TargetRoot: targetRoot,
		BundleDir:  DefaultBundleDirectory(targetRoot),
		Lock:       lock,
	}); err != nil {
		t.Fatalf("copy the fixture 1.x volume in: %v", err)
	}
	return importFixture{
		legacyRoot:  volume.root,
		targetRoot:  targetRoot,
		cpaDir:      writeCPABeside(t, volume.root, config, credentials),
		credentials: credentials,
	}
}

// fakeDestinationCPA is one CPA v8 Management API stand-in. It records every
// method+path it received, so a test can prove what the import wrote, and it
// keeps the credential bytes and configuration it holds.
type fakeDestinationCPA struct {
	mu          sync.Mutex
	methods     []string
	management  string
	clientKeys  []string
	credentials map[string][]byte
	types       map[string]string
	config      map[string]any
	// silentDropPath accepts one configuration write and does not keep it, which
	// is the destination state the readback proof exists to catch.
	silentDropPath string
	// dropCredentials makes the download of these files answer 404 after the
	// upload, which is the readback mismatch a test needs.
	dropCredentials map[string]bool
	server          *httptest.Server
}

func newFakeDestinationCPA(t *testing.T, management string) *fakeDestinationCPA {
	t.Helper()
	fake := &fakeDestinationCPA{
		management:      management,
		credentials:     map[string][]byte{},
		types:           map[string]string{},
		config:          map[string]any{"config-version": 8, "server": map[string]any{"port": 8317}, "oauth": map[string]any{"auth-dir": "/root/.cli-proxy-api"}},
		dropCredentials: map[string]bool{},
	}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	return fake
}

func (c *fakeDestinationCPA) url() string { return c.server.URL }

func (c *fakeDestinationCPA) requests() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.methods...)
}

func (c *fakeDestinationCPA) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.methods = append(c.methods, r.Method+" "+r.URL.Path)
	c.mu.Unlock()
	if r.URL.Path == "/v1/models" {
		c.serveModels(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+c.management {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/v8/management/credentials" && r.Method == http.MethodGet:
		c.listCredentials(w, r)
	case r.URL.Path == "/v8/management/credentials" && r.Method == http.MethodPost:
		c.uploadCredential(w, r)
	case r.URL.Path == "/v8/management/credentials/download" && r.Method == http.MethodGet:
		c.downloadCredential(w, r)
	case r.URL.Path == "/v8/management/credentials/status" && r.Method == http.MethodPatch:
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	case r.URL.Path == "/v8/management/config" && r.Method == http.MethodGet:
		c.writeDocument(w)
	case strings.HasPrefix(r.URL.Path, "/v8/management/config/") && r.Method == http.MethodGet:
		c.readConfig(w, strings.TrimPrefix(r.URL.Path, "/v8/management/config/"))
	case strings.HasPrefix(r.URL.Path, "/v8/management/config/") && (r.Method == http.MethodPatch || r.Method == http.MethodPut):
		c.writeConfig(w, r, strings.TrimPrefix(r.URL.Path, "/v8/management/config/"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// serveModels is CPA's client-authenticated model list: an absent or foreign key
// is rejected and an installed key is accepted, which is what
// cpa.VerifyV8Adoption reads.
func (c *fakeDestinationCPA) serveModels(w http.ResponseWriter, r *http.Request) {
	presented, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	c.mu.Lock()
	accepted := false
	for _, key := range c.clientKeys {
		if key != "" && key == presented {
			accepted = true
		}
	}
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if !accepted {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
		return
	}
	_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5","object":"model"}]}`))
}

// writeDocument answers the whole-document read: the shape CPA serves, including
// the client keys the destination was staged with.
func (c *fakeDestinationCPA) writeDocument(w http.ResponseWriter) {
	c.mu.Lock()
	document := map[string]any{}
	for key, value := range c.config {
		document[key] = value
	}
	access := map[string]any{"api-keys": append([]string(nil), c.clientKeys...)}
	if existing, ok := document["access"].(map[string]any); ok {
		for key, value := range existing {
			access[key] = value
		}
	}
	document["access"] = access
	document["config-version"] = 8
	c.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(document)
}

func (c *fakeDestinationCPA) listCredentials(w http.ResponseWriter, r *http.Request) {
	nameFilter := r.URL.Query().Get("name")
	c.mu.Lock()
	files := make([]map[string]any, 0, len(c.credentials))
	for name := range c.credentials {
		if nameFilter != "" && name != nameFilter {
			continue
		}
		files = append(files, map[string]any{"name": name, "type": c.types[name], "disabled": false, "runtime_only": false})
	}
	c.mu.Unlock()
	sort.Slice(files, func(i, j int) bool { return files[i]["name"].(string) < files[j]["name"].(string) })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
}

func (c *fakeDestinationCPA) uploadCredential(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var stored struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(body, &stored)
	c.mu.Lock()
	c.credentials[name] = body
	c.types[name] = stored.Type
	c.mu.Unlock()
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

func (c *fakeDestinationCPA) downloadCredential(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	c.mu.Lock()
	data, ok := c.credentials[name]
	drop := c.dropCredentials[name]
	c.mu.Unlock()
	if !ok || drop {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_, _ = w.Write(data)
}

// writeConfig merges one PATCH body or replaces one PUT value, exactly as CPA
// does, so the readback below is an observation of the destination's own state.
func (c *fakeDestinationCPA) writeConfig(w http.ResponseWriter, r *http.Request, slashPath string) {
	dotted := strings.ReplaceAll(strings.Trim(slashPath, "/"), "/", ".")
	var value any
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	if r.Method == http.MethodPatch {
		// CPA merges a PATCH mapping key by key, so a leaf below a page group
		// leaves the group's other leaves in place.
		patchConfig(c.config, strings.Split(dotted, "."), value)
	} else {
		mergeConfig(c.config, strings.Split(dotted, "."), value)
	}
	if c.silentDropPath != "" {
		dropConfigPath(c.config, strings.Split(c.silentDropPath, "."))
	}
	c.mu.Unlock()
	_, _ = w.Write([]byte(`{"status":"ok","config-version":8}`))
}

func (c *fakeDestinationCPA) readConfig(w http.ResponseWriter, slashPath string) {
	dotted := strings.ReplaceAll(strings.Trim(slashPath, "/"), "/", ".")
	c.mu.Lock()
	value, found := configValueAt(c.config, strings.Split(dotted, "."))
	c.mu.Unlock()
	if !found {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"not_found"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

// patchConfig deep-merges one PATCH body into the document below one path.
func patchConfig(document map[string]any, parts []string, value any) {
	current := document
	for _, part := range parts {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	incoming, ok := value.(map[string]any)
	if !ok {
		// A scalar body replaces the whole group, which is what CPA does.
		mergeConfig(document, parts, value)
		return
	}
	for key, item := range incoming {
		if nested, ok := item.(map[string]any); ok {
			if existing, ok := current[key].(map[string]any); ok {
				mergeMapping(existing, nested)
				continue
			}
		}
		current[key] = item
	}
}

// dropConfigPath removes one leaf, so the destination accepts a write and does
// not hold it.
func dropConfigPath(document map[string]any, parts []string) {
	current := document
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			return
		}
		current = next
	}
	delete(current, parts[len(parts)-1])
}

// mergeMapping deep-merges one mapping into another.
func mergeMapping(target, source map[string]any) {
	for key, item := range source {
		if nested, ok := item.(map[string]any); ok {
			if existing, ok := target[key].(map[string]any); ok {
				mergeMapping(existing, nested)
				continue
			}
		}
		target[key] = item
	}
}

func mergeConfig(document map[string]any, parts []string, value any) {
	current := document
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			current[part] = next
		}
		current = next
	}
	current[parts[len(parts)-1]] = value
}

func configValueAt(document map[string]any, parts []string) (any, bool) {
	var current any = document
	for _, part := range parts {
		mapping, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = mapping[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

// fakeImportRuntime is the Runtime the import stages the destination through. It
// records the two mutations, so a test can prove the plugin choice reached the
// destination request.
type fakeImportRuntime struct {
	mu        sync.Mutex
	state     model.RuntimeState
	stageCall *model.RuntimeDestinationRequest
	startCall *model.RuntimeMutationRequest
	staged    *fakeDestinationCPA
	identity  model.RuntimeIdentity
	artifact  model.RuntimeArtifactID
	// failStage makes the stage answer a terminal failure.
	failStage bool
}

func newFakeImportRuntime(state model.RuntimeState) *fakeImportRuntime {
	return &fakeImportRuntime{
		state:    state,
		identity: model.RuntimeIdentity("runtime-identity-1"),
		artifact: model.RuntimeArtifactID("sha256:" + strings.Repeat("ab", 32)),
	}
}

func (f *fakeImportRuntime) Status(context.Context) (model.RuntimeObservedStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return model.RuntimeObservedStatus{
		Identity: f.identity, Generation: 4, ProtocolVersion: "v1", State: f.state,
		CPAObservedVersion:    "8.0.13",
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: f.artifact, Version: "8.0.13"},
		Capabilities:          model.RuntimeCapabilities{model.RuntimeCapabilityStageDestination, model.RuntimeCapabilityStart},
	}, nil
}

func (f *fakeImportRuntime) StageDestination(_ context.Context, request model.RuntimeDestinationRequest) (model.RuntimeOperationResult, error) {
	f.mu.Lock()
	f.stageCall = &request
	fail := f.failStage
	f.mu.Unlock()
	if fail {
		return model.RuntimeOperationResult{
			OperationID: request.OperationID, OperationType: model.RuntimeOperationStageDestination,
			RuntimeIdentity: request.ExpectedRuntimeIdentity, RuntimeGeneration: request.ExpectedRuntimeGeneration,
			State: model.RuntimeOperationFailed, Failure: &model.RuntimeOperationFailure{Code: "stage_failed"},
		}, nil
	}
	// The destination CPA the Supervisor publishes carries the request's keys and
	// management secret, which is what the proof reads.
	f.staged.mu.Lock()
	f.staged.management = request.ManagementKey
	f.staged.clientKeys = append([]string(nil), request.APIKeys...)
	f.staged.mu.Unlock()
	f.mu.Lock()
	f.state = model.RuntimeStateReady
	f.mu.Unlock()
	return model.RuntimeOperationResult{
		OperationID: request.OperationID, OperationType: model.RuntimeOperationStageDestination,
		RuntimeIdentity: request.ExpectedRuntimeIdentity, RuntimeGeneration: request.ExpectedRuntimeGeneration,
		State: model.RuntimeOperationSucceeded,
	}, nil
}

func (f *fakeImportRuntime) Start(_ context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	f.mu.Lock()
	f.startCall = &request
	f.mu.Unlock()
	return model.RuntimeOperationResult{
		OperationID: request.OperationID, OperationType: model.RuntimeOperationStart,
		RuntimeIdentity: request.ExpectedRuntimeIdentity, RuntimeGeneration: request.ExpectedRuntimeGeneration,
		State: model.RuntimeOperationSucceeded,
	}, nil
}

// importOptions builds the step's input for one fixture and one destination.
func importOptions(fixture importFixture, runtime CPAImportRuntime, destinationURL string, plugins *bool) CPAImportOptions {
	return CPAImportOptions{
		LegacyRoot:     fixture.legacyRoot,
		CPAVersion:     "7.3.4",
		TargetRoot:     fixture.targetRoot,
		Runtime:        runtime,
		DestinationURL: destinationURL,
		PluginsEnabled: plugins,
	}
}

func boolPointer(value bool) *bool { return &value }

// TestImportCPAImportsAV734FixtureAndRecordsTheStage is the acceptance case: the
// configuration and the credentials land, the destination verifies, and the row
// advances to cpa_import carrying the cpa_config domain.
func TestImportCPAImportsAV734FixtureAndRecordsTheStage(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{
		"codex-user.json":  `{"type":"codex","access_token":"token"}`,
		"gemini-user.json": `{"type":"gemini","access_token":"token"}`,
	})
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	result, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true)))
	if err != nil {
		t.Fatalf("import the fixture CPA: %v", err)
	}
	if result.Stage != model.UpgradeStageCPAImport {
		t.Fatalf("stage = %q, want %q", result.Stage, model.UpgradeStageCPAImport)
	}
	if !containsValue(result.DomainsDone, model.UpgradeDomainCPAConfig) {
		t.Fatalf("domains = %v, want the cpa_config domain", result.DomainsDone)
	}
	// The plugin choice reached the destination request and the result.
	if runtime.stageCall == nil || !runtime.stageCall.PluginsEnabled || !result.PluginsEnabled {
		t.Fatalf("plugin choice = %+v / %t, want true", runtime.stageCall, result.PluginsEnabled)
	}
	if runtime.stageCall.SourceClass != "legacy_managed" {
		t.Fatalf("source class = %q, want legacy_managed", runtime.stageCall.SourceClass)
	}
	// The configuration landed.
	destination.mu.Lock()
	config, _ := configValueAt(destination.config, []string{"routing", "retry", "request-retry"})
	codexCloak, _ := configValueAt(destination.config, []string{"upstream", "codex", "disable-codex-cloaking"})
	geminiGroup, geminiFound := configValueAt(destination.config, []string{"api-keys", "gemini"})
	_, managementCopied := configValueAt(destination.config, []string{"management"})
	_, accessKeysCopied := configValueAt(destination.config, []string{"access", "api-keys"})
	credentials := append([]string(nil), mapKeys(destination.credentials)...)
	destination.mu.Unlock()
	if fmt.Sprint(config) != "3" {
		t.Fatalf("routing.retry.request-retry = %#v, want 3", config)
	}
	if fmt.Sprint(codexCloak) != "true" {
		t.Fatalf("upstream.codex.disable-codex-cloaking = %#v, want true", codexCloak)
	}
	if !geminiFound {
		t.Fatal("the gemini provider group did not land")
	}
	if groups, ok := geminiGroup.([]any); !ok || len(groups) != 1 {
		t.Fatalf("api-keys.gemini = %#v, want one group", geminiGroup)
	}
	if managementCopied {
		t.Fatal("the source management block was copied into the destination")
	}
	if accessKeysCopied {
		t.Fatal("the source client key list was written as configuration")
	}
	// Only the uploadable credential landed; the gemini one is a manual item.
	if len(credentials) != 1 || credentials[0] != "codex-user.json" {
		t.Fatalf("destination credentials = %v, want only the codex file", credentials)
	}
	// The recorded row carries the manual items, and the source secrets are gone.
	recorded := readOperation(t, fixture.targetRoot)
	if recorded.Stage != model.UpgradeStageCPAImport {
		t.Fatalf("recorded stage = %q, want %q", recorded.Stage, model.UpgradeStageCPAImport)
	}
	if !containsValue(recorded.DomainsDone, model.UpgradeDomainCPAConfig) {
		t.Fatalf("recorded domains = %v, want cpa_config", recorded.DomainsDone)
	}
	joined := strings.Join(recorded.ManualItems, "\n")
	for _, want := range []string{"gemini-user.json", "section:ampcode", "config:codex.identity-confuse", "section:something-new"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("recorded manual items do not name %q: %s", want, joined)
		}
	}
	for _, secret := range []string{"legacy-management-secret", "gemini-provider-key", "claude-provider-key", "legacy-client-key-1"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("the recorded manual items carry %q", secret)
		}
	}
}

// TestImportCPARefusesAV7012FixtureBeforeWriting is the preflight block: a source
// below the floor is refused with the typed code and the destination is never
// touched.
func TestImportCPARefusesAV7012FixtureBeforeWriting(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	options := importOptions(fixture, runtime, destination.url(), boolPointer(true))
	options.CPAVersion = "7.0.12"
	_, err := ImportCPA(context.Background(), options)
	code, ok := UpgradeErrorCodeOf(err)
	if !ok || code != UpgradeErrorCPAVersionUnsupported {
		t.Fatalf("a v7.0.12 source produced %v (code %q), want %s", err, code, UpgradeErrorCPAVersionUnsupported)
	}
	if runtime.stageCall != nil {
		t.Fatalf("the destination was staged for a refused source: %+v", runtime.stageCall)
	}
	if requests := destination.requests(); len(requests) != 0 {
		t.Fatalf("the destination answered %v for a refused source, want no request", requests)
	}
	recorded := readOperation(t, fixture.targetRoot)
	if recorded.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("recorded stage = %q, want the copy-in the refusal must not move", recorded.Stage)
	}
	if recorded.HasDomain(model.UpgradeDomainCPAConfig) {
		t.Fatalf("recorded domains = %v, want no cpa_config", recorded.DomainsDone)
	}
}

// TestImportCPARequiresThePluginChoice pins the missing choice: it is never
// defaulted, and nothing is read or written.
func TestImportCPARequiresThePluginChoice(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, nil)
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	_, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), nil))
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAPluginChoiceRequired {
		t.Fatalf("a run without a plugin choice produced %v (code %q), want %s", err, code, UpgradeErrorCPAPluginChoiceRequired)
	}
	if runtime.stageCall != nil || len(destination.requests()) != 0 {
		t.Fatal("a run without a plugin choice reached the destination")
	}
}

// TestImportCPAFailsTheStepWhenACopiedLeafDoesNotReadBack is the target proof's
// negative case: the destination accepts the write and does not hold it, so the
// step fails and the operation stays at cpa_import.
func TestImportCPAFailsTheStepWhenACopiedLeafDoesNotReadBack(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "")
	destination.silentDropPath = "routing.retry.request-retry"
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	_, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(false)))
	code, ok := UpgradeErrorCodeOf(err)
	if !ok || code != UpgradeErrorCPAImportUnproven {
		t.Fatalf("an unproven copy produced %v (code %q), want %s", err, code, UpgradeErrorCPAImportUnproven)
	}
	// The step entered its stage before it wrote, so the retry point is the
	// import; the domain is what the proof alone may claim.
	recorded := readOperation(t, fixture.targetRoot)
	if recorded.Stage != model.UpgradeStageCPAImport {
		t.Fatalf("recorded stage = %q, want %q", recorded.Stage, model.UpgradeStageCPAImport)
	}
	if recorded.HasDomain(model.UpgradeDomainCPAConfig) {
		t.Fatalf("recorded domains = %v, want no cpa_config", recorded.DomainsDone)
	}
}

// TestImportCPAFailsTheStepWhenACredentialDoesNotReadBack covers the credential
// half of the proof.
func TestImportCPAFailsTheStepWhenACredentialDoesNotReadBack(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "")
	destination.dropCredentials["codex-user.json"] = true
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	_, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(false)))
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAImportUnproven {
		t.Fatalf("an unproven credential produced %v (code %q), want %s", err, code, UpgradeErrorCPAImportUnproven)
	}
	if recorded := readOperation(t, fixture.targetRoot); recorded.HasDomain(model.UpgradeDomainCPAConfig) {
		t.Fatalf("recorded domains = %v, want no cpa_config", recorded.DomainsDone)
	}
}

// TestImportCPARefusesAStageThatCannotEnterTheImport pins the monotonicity rule.
func TestImportCPARefusesAStageThatCannotEnterTheImport(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, nil)
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	// A stage past the import can never take it again.
	writeOperationStage(t, fixture.targetRoot, model.UpgradeStageVerify, []string{model.UpgradeDomainSourceCopy})
	_, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true)))
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAStageConflict {
		t.Fatalf("a stage past the import produced %v (code %q), want %s", err, code, UpgradeErrorCPAStageConflict)
	}
	if runtime.stageCall != nil {
		t.Fatal("a stage that cannot enter the import still reached the destination")
	}
}

// TestImportCPADoesNotRestageAnAlreadyRunningDestination pins the resume rule: a
// destination that already answers ready is proved, never staged again.
func TestImportCPADoesNotRestageAnAlreadyRunningDestination(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "destination-management-secret-value-0123456789abcdef")
	destination.clientKeys = []string{"legacy-client-key-1", "legacy-client-key-2"}
	runtime := newFakeImportRuntime(model.RuntimeStateReady)
	runtime.staged = destination
	options := importOptions(fixture, runtime, destination.url(), boolPointer(true))
	options.DestinationManagementKey = destination.management
	result, err := ImportCPA(context.Background(), options)
	if err != nil {
		t.Fatalf("import into a ready destination: %v", err)
	}
	if runtime.stageCall != nil || runtime.startCall != nil {
		t.Fatal("the import staged a destination that was already ready")
	}
	if result.Stage != model.UpgradeStageCPAImport {
		t.Fatalf("stage = %q, want %q", result.Stage, model.UpgradeStageCPAImport)
	}
}

// TestImportCPAIsIdempotentOnceRecorded pins the resume rule for the whole step:
// a recorded import returns the row without touching the destination.
func TestImportCPAIsIdempotentOnceRecorded(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	if _, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true))); err != nil {
		t.Fatalf("first import: %v", err)
	}
	before := len(destination.requests())
	result, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true)))
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if !result.AlreadyComplete {
		t.Fatal("the second import did not report the recorded operation")
	}
	if after := len(destination.requests()); after != before {
		t.Fatalf("the second import issued %d further requests", after-before)
	}
}

// TestImportCPAReportsAManualItemForAnUnknownSectionAndStillSucceeds pins the
// rule that an unknown key never fails the run and never disappears.
func TestImportCPAReportsAManualItemForAnUnknownSectionAndStillSucceeds(t *testing.T) {
	config := "api-keys:\n  - \"legacy-client-key-1\"\nrequest-retry: 4\nsome-future-section:\n  enabled: true\n"
	fixture := newImportFixture(t, config, nil)
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	result, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(false)))
	if err != nil {
		t.Fatalf("an unknown section failed the run: %v", err)
	}
	if result.Stage != model.UpgradeStageCPAImport {
		t.Fatalf("stage = %q, want %q", result.Stage, model.UpgradeStageCPAImport)
	}
	found := false
	for _, item := range result.ManualItems {
		if item.Item == "section:some-future-section" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the unknown section is not a manual item: %+v", result.ManualItems)
	}
	if recorded := readOperation(t, fixture.targetRoot); !containsValue(recorded.ManualItems, manualItemFor(t, result, "section:some-future-section")) {
		t.Fatalf("the recorded manual items do not carry the unknown section: %v", recorded.ManualItems)
	}
	// The mapped leaf still landed.
	destination.mu.Lock()
	value, found := configValueAt(destination.config, []string{"routing", "retry", "request-retry"})
	destination.mu.Unlock()
	if !found || fmt.Sprint(value) != "4" {
		t.Fatalf("routing.retry.request-retry = %#v, present = %t", value, found)
	}
}

// TestImportCPARefusesAFailedDestinationStage pins the Runtime refusal: no
// configuration is written when the destination never came up.
func TestImportCPARefusesAFailedDestinationStage(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, nil)
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	runtime.failStage = true
	_, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true)))
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationUnready {
		t.Fatalf("a failed stage produced %v (code %q), want %s", err, code, UpgradeErrorCPADestinationUnready)
	}
	if requests := destination.requests(); len(requests) != 0 {
		t.Fatalf("the destination answered %v for a failed stage, want no request", requests)
	}
}

// TestCPAImportDecisionsAuthorizeOnlyTheCopiedReferences pins the authorization
// the step runs under: exactly the three references the executor copies.
func TestCPAImportDecisionsAuthorizeOnlyTheCopiedReferences(t *testing.T) {
	decisions := cpaImportDecisions()
	want := []string{"cpa-auth-files", "cpa-config-routing", "provider-state-api-keys"}
	got := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		if decision.Disposition != "migrate" {
			t.Fatalf("decision %q is %q, want migrate", decision.ResourceRef, decision.Disposition)
		}
		got = append(got, string(decision.ResourceRef))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("authorized references = %v, want %v", got, want)
	}
}

// manualItemFor renders the one recorded line for one manual item, so a test can
// compare the row with the result without restating the format.
func manualItemFor(t *testing.T, result CPAImportResult, item string) string {
	t.Helper()
	for _, candidate := range result.ManualItems {
		if candidate.Item == item {
			return boundedManualItem(candidate)
		}
	}
	t.Fatalf("the result holds no manual item %q", item)
	return ""
}

func mapKeys(values map[string][]byte) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func containsValue(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// readOperation reads the recorded operation from a v2 data root.
func readOperation(t *testing.T, targetRoot string) model.UpgradeOperation {
	t.Helper()
	recorded, found, err := ReadRecord(context.Background(), newTargetPaths(targetRoot).Database)
	if err != nil {
		t.Fatalf("read the recorded operation: %v", err)
	}
	if !found {
		t.Fatal("no operation was recorded")
	}
	return recorded
}

// writeOperationStage records one stage on an existing operation row, which is the
// state a later step leaves behind.
func writeOperationStage(t *testing.T, targetRoot string, stage model.UpgradeStage, domains []string) {
	t.Helper()
	db, err := managersqlite.Open(newTargetPaths(targetRoot).Database)
	if err != nil {
		t.Fatalf("open the v2 database: %v", err)
	}
	defer func() { _ = db.Close() }()
	recorded, found, err := ReadRecord(context.Background(), newTargetPaths(targetRoot).Database)
	if err != nil || !found {
		t.Fatalf("read the recorded operation: found=%t err=%v", found, err)
	}
	recorded.Stage = stage
	recorded.DomainsDone = domains
	if err := upgradeoperation.New(db).Put(context.Background(), recorded); err != nil {
		t.Fatalf("write the operation stage: %v", err)
	}
}
