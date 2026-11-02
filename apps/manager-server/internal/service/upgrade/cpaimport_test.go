package upgrade

// These tests pin the Managed CPA import step end to end against a stand-in CPA
// v8 destination and a stand-in Runtime: the version gate before any write, the
// copy of configuration and credentials, the plugin choice, the target proof and
// the stage the run records.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshadopt"
)

// importFixture is a stopped 1.x volume with a recorded copy-in and the managed
// CPA directory beside it.
type importFixture struct {
	legacyRoot  string
	targetRoot  string
	cpaDir      string
	credentials map[string]string
	// sourceLock is the 1.x ownership marker this fixture holds for the whole
	// test, exactly as the command does for a whole run.
	sourceLock *processlock.Lock
}

// newImportFixture performs the copy-in a real run performs and writes the CPA
// directory beside the source, so the import starts from the exact state the
// previous step leaves behind. The recorded operation is advanced to the
// reconciliation position the import is entered from, exactly as the reconcile
// step leaves it.
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
	writeOperationStage(t, targetRoot, model.UpgradeStageReconcile, []string{model.UpgradeDomainSourceCopy})
	return importFixture{
		legacyRoot:  volume.root,
		targetRoot:  targetRoot,
		cpaDir:      writeCPABeside(t, volume.root, config, credentials),
		credentials: credentials,
		sourceLock:  lock,
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

// fakeImportRuntime mirrors the Supervisor's destination admission rather than a
// map of canned answers.
//
// Its rules are the ones the real Executor applies: a submission whose expected
// Runtime identity or generation is not the current authority is refused; a
// journalled operation ID replays its recorded result and never applies a new
// request; and a replacement attempt is admitted only when the published
// destination request is identical after the operation ID is substituted, with a
// terminal stage and an absent or terminally failed start. A fake that keyed
// answers by operation ID alone could not observe a changed request, which is how
// a drifted retry stayed invisible to the lane's own test.
type fakeImportRuntime struct {
	mu         sync.Mutex
	state      model.RuntimeState
	generation model.RuntimeGeneration
	stageCall  *model.RuntimeDestinationRequest
	startCall  *model.RuntimeMutationRequest
	staged     *fakeDestinationCPA
	identity   model.RuntimeIdentity
	artifact   model.RuntimeArtifactID
	// failStage makes the stage answer a terminal failure.
	failStage bool
	// failStart makes the start answer a terminal failure and leaves the CPA
	// offline, which is the state the next run observes.
	failStart bool
	// journal holds the answer of every operation the Supervisor recorded.
	journal map[string]model.RuntimeOperationResult
	// submitted holds the request fingerprint of every journalled operation, so a
	// repeated ID with another source or artifact is refused as the journal's
	// Resolve refuses it.
	submitted map[string]model.RuntimeDestinationRequest
	// published is the destination request the Supervisor actually staged, the
	// object supersedeDestination compares against.
	published *model.RuntimeDestinationRequest
	// refusal names the Runtime rule that refused the last submission.
	refusal string
}

func newFakeImportRuntime(state model.RuntimeState) *fakeImportRuntime {
	return &fakeImportRuntime{
		state:      state,
		generation: 4,
		identity:   model.RuntimeIdentity("runtime-identity-1"),
		artifact:   model.RuntimeArtifactID("sha256:" + strings.Repeat("ab", 32)),
		journal:    map[string]model.RuntimeOperationResult{},
		submitted:  map[string]model.RuntimeDestinationRequest{},
	}
}

// restart makes the next observation report a new generation, which is what a
// Supervisor process restart looks like to the Manager.
func (f *fakeImportRuntime) restart() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.generation++
}

// lastRefusal reports the Runtime rule that refused the last submission, so a
// test can prove the rule applied instead of a fixture being silently ignored.
func (f *fakeImportRuntime) lastRefusal() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.refusal
}

func (f *fakeImportRuntime) Status(context.Context) (model.RuntimeObservedStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return model.RuntimeObservedStatus{
		Identity: f.identity, Generation: f.generation, ProtocolVersion: "v1", State: f.state,
		CPAObservedVersion:    "8.0.13",
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: f.artifact, Version: "8.0.13"},
		Capabilities:          model.RuntimeCapabilities{model.RuntimeCapabilityStageDestination, model.RuntimeCapabilityStart},
	}, nil
}

// validateSubmission mirrors journal.validateSubmission: the expected authority
// must be the current one before an operation is even resolved.
func (f *fakeImportRuntime) validateSubmissionLocked(identity model.RuntimeIdentity, generation model.RuntimeGeneration) error {
	switch {
	case identity != f.identity:
		f.refusal = "runtime identity mismatch"
	case generation != f.generation:
		f.refusal = "stale runtime generation"
	default:
		return nil
	}
	return errors.New(f.refusal)
}

func (f *fakeImportRuntime) StageDestination(_ context.Context, request model.RuntimeDestinationRequest) (model.RuntimeOperationResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stageCall = &request
	if err := f.validateSubmissionLocked(request.ExpectedRuntimeIdentity, request.ExpectedRuntimeGeneration); err != nil {
		return model.RuntimeOperationResult{}, err
	}
	// Resolve first, exactly as the journal does: an existing ID answers with its
	// journalled operation, but only when the request fingerprint is the same one.
	journalled, found := f.journal[request.OperationID]
	if found {
		submitted := f.submitted[request.OperationID]
		if submitted.ExpectedActiveArtifactID != request.ExpectedActiveArtifactID ||
			submitted.SourceIdentity != request.SourceIdentity || submitted.SourceClass != request.SourceClass {
			f.refusal = "operation ID conflict"
			return model.RuntimeOperationResult{}, errors.New(f.refusal)
		}
	}
	// A stored destination is compared before the replay, and a replacement is
	// admitted only under supersedeDestination's rule.
	supersedes := ""
	if f.published != nil && !reflect.DeepEqual(*f.published, request) {
		previous := *f.published
		previous.OperationID = request.OperationID
		if f.published.OperationID == request.OperationID || !reflect.DeepEqual(previous, request) {
			f.refusal = "operation ID conflict (request changed)"
			return model.RuntimeOperationResult{}, errors.New(f.refusal)
		}
		if err := f.admitSupersedeLocked(*f.published); err != nil {
			return model.RuntimeOperationResult{}, err
		}
		supersedes = f.published.OperationID
	}
	if found {
		return journalled, nil
	}
	if f.published != nil && supersedes == "" {
		f.refusal = "operation state conflict"
		return model.RuntimeOperationResult{}, errors.New(f.refusal)
	}
	result := model.RuntimeOperationResult{
		OperationID: request.OperationID, OperationType: model.RuntimeOperationStageDestination,
		RuntimeIdentity: request.ExpectedRuntimeIdentity, RuntimeGeneration: request.ExpectedRuntimeGeneration,
		State: model.RuntimeOperationSucceeded,
	}
	if f.failStage {
		result.State = model.RuntimeOperationFailed
		result.Failure = &model.RuntimeOperationFailure{Code: "stage_failed"}
	} else {
		// The destination CPA the Supervisor publishes carries the request's keys
		// and management secret, which is what the binding reads.
		f.staged.mu.Lock()
		f.staged.management = request.ManagementKey
		f.staged.clientKeys = append([]string(nil), request.APIKeys...)
		f.staged.mu.Unlock()
		published := request
		f.published = &published
	}
	f.journal[request.OperationID] = result
	f.submitted[request.OperationID] = request
	return result, nil
}

// admitSupersedeLocked applies supersedeDestination's terminal-evidence rule: the
// replaced stage must be terminal, and its start absent or terminally failed.
func (f *fakeImportRuntime) admitSupersedeLocked(stored model.RuntimeDestinationRequest) error {
	stage, ok := f.journal[stored.OperationID]
	if !ok || stage.OperationType != model.RuntimeOperationStageDestination ||
		(stage.State != model.RuntimeOperationFailed && stage.State != model.RuntimeOperationSucceeded) {
		f.refusal = "operation state conflict"
		return errors.New(f.refusal)
	}
	start, startFound := f.journal[stored.OperationID+":start"]
	if startFound && start.OperationType != model.RuntimeOperationStart {
		f.refusal = "operation state conflict"
		return errors.New(f.refusal)
	}
	startFailed := startFound && start.State == model.RuntimeOperationFailed
	if stage.State == model.RuntimeOperationSucceeded && !startFailed {
		f.refusal = "operation state conflict"
		return errors.New(f.refusal)
	}
	return nil
}

func (f *fakeImportRuntime) Start(_ context.Context, request model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.startCall = &request
	if err := f.validateSubmissionLocked(request.ExpectedRuntimeIdentity, request.ExpectedRuntimeGeneration); err != nil {
		return model.RuntimeOperationResult{}, err
	}
	if journalled, ok := f.journal[request.OperationID]; ok {
		return journalled, nil
	}
	result := model.RuntimeOperationResult{
		OperationID: request.OperationID, OperationType: model.RuntimeOperationStart,
		RuntimeIdentity: request.ExpectedRuntimeIdentity, RuntimeGeneration: request.ExpectedRuntimeGeneration,
		State: model.RuntimeOperationSucceeded,
	}
	if f.failStart {
		result.State = model.RuntimeOperationFailed
		result.Failure = &model.RuntimeOperationFailure{Code: "start_failed"}
		f.state = model.RuntimeStateOffline
	} else {
		f.state = model.RuntimeStateReady
	}
	f.journal[request.OperationID] = result
	return result, nil
}

// importOptions builds the step's input for one fixture and one destination.
func importOptions(fixture importFixture, runtime CPAImportRuntime, destinationURL string, plugins *bool) CPAImportOptions {
	return CPAImportOptions{
		LegacyRoot:     fixture.legacyRoot,
		SourceLock:     fixture.sourceLock,
		CPAVersion:     "7.3.4",
		TargetRoot:     fixture.targetRoot,
		Runtime:        runtime,
		DestinationURL: destinationURL,
		PluginsEnabled: plugins,
	}
}

func boolPointer(value bool) *bool { return &value }

// TestSuccessfulImportNeverReportsSecrets is the contract the CLI's output test
// claims to prove. It runs a real import and compares the reported result and the
// recorded operation row with the destination management secret and a credential
// token that crossed the run, so the claim is about bytes those structures hold,
// not about the absence of a field name.
func TestSuccessfulImportNeverReportsSecrets(t *testing.T) {
	const credentialToken = "credential-access-token-secret-value"
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{
		"codex-user.json": `{"type":"codex","access_token":"` + credentialToken + `"}`,
	})
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	result, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true)))
	if err != nil {
		t.Fatalf("import the fixture CPA: %v", err)
	}
	secret := runtime.stageCall.ManagementKey
	if strings.TrimSpace(secret) == "" {
		t.Fatal("the import staged no management secret, so the test proves nothing")
	}
	encodedResult, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedResult), secret) {
		t.Fatal("the import result carries the destination management secret")
	}
	if strings.Contains(string(encodedResult), credentialToken) {
		t.Fatal("the import result carries a credential token")
	}
	encodedRow, err := json.Marshal(readOperation(t, fixture.targetRoot))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedRow), secret) || strings.Contains(string(encodedRow), credentialToken) {
		t.Fatal("the recorded operation row carries a secret")
	}
}

// TestImportCPARequiresTheHeldSourceOwnershipLock is the Low finding: the step
// reads a source it assumes is stopped, so it must hold the 1.x ownership marker
// rather than only saying so in a comment. A caller without the marker is
// refused before the CPA directory is read, and a marker for another database
// does not authorize this source.
func TestImportCPARequiresTheHeldSourceOwnershipLock(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	options := importOptions(fixture, runtime, destination.url(), boolPointer(true))
	// A missing CPA directory would be reported as a missing source if the run
	// reached the read. The source-locked code proves the fence is asked for
	// first.
	if err := os.RemoveAll(fixture.cpaDir); err != nil {
		t.Fatalf("remove the fixture CPA directory: %v", err)
	}
	options.SourceLock = nil
	_, err := ImportCPA(context.Background(), options)
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPASourceLocked {
		t.Fatalf("a run without the source lock produced %v (code %q), want %s", err, code, UpgradeErrorCPASourceLocked)
	}
	if runtime.stageCall != nil || len(destination.requests()) != 0 {
		t.Fatal("a run without the source lock still wrote to the destination")
	}
	if recorded := readOperation(t, fixture.targetRoot); recorded.Stage != model.UpgradeStageReconcile {
		t.Fatalf("recorded stage = %q, want the untouched %q", recorded.Stage, model.UpgradeStageReconcile)
	}

	// The marker has to be the one for this source database.
	other := newLegacyVolume(t)
	options.SourceLock = other.lock(t)
	_, err = ImportCPA(context.Background(), options)
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPASourceLocked {
		t.Fatalf("a lock for another database produced %v (code %q), want %s", err, code, UpgradeErrorCPASourceLocked)
	}
}

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
	if recorded.Stage != model.UpgradeStageReconcile {
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

// TestImportCPARefusesToEnterBeforeReconciliation pins the position the import is
// entered from. The earlier gate only asked whether cpa_import was reachable, and
// copy_in can reach it: a single misplaced run then moved the row past reconcile,
// which refuses a stage it can no longer enter, so usage reconciliation was
// permanently skipped for that operation.
func TestImportCPARefusesToEnterBeforeReconciliation(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, nil)
	writeOperationStage(t, fixture.targetRoot, model.UpgradeStageCopyIn, []string{model.UpgradeDomainSourceCopy})
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	_, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true)))
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAStageConflict {
		t.Fatalf("a copy-in position produced %v (code %q), want %s", err, code, UpgradeErrorCPAStageConflict)
	}
	if runtime.stageCall != nil || len(destination.requests()) != 0 {
		t.Fatal("a copy-in position still reached the destination")
	}
	if recorded := readOperation(t, fixture.targetRoot); recorded.Stage != model.UpgradeStageCopyIn {
		t.Fatalf("recorded stage = %q, want the untouched %q", recorded.Stage, model.UpgradeStageCopyIn)
	}
}

// TestImportCPALeavesTheStageAloneWhenTheDestinationCannotBeUsed pins the other
// half of the ordering rule: a refusal after the version gate must not advance the
// row. A ready destination with no recorded secret and no supplied one used to
// leave the operation at cpa_import with nothing written, which reported a started
// import that never started.
func TestImportCPALeavesTheStageAloneWhenTheDestinationCannotBeUsed(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "destination-management-secret-value-0123456789abcdef")
	runtime := newFakeImportRuntime(model.RuntimeStateReady)
	runtime.staged = destination
	_, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true)))
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationKeyRequired {
		t.Fatalf("a ready destination without a secret produced %v (code %q), want %s", err, code, UpgradeErrorCPADestinationKeyRequired)
	}
	if requests := destination.requests(); len(requests) != 0 {
		t.Fatalf("the destination answered %v, want no request", requests)
	}
	if recorded := readOperation(t, fixture.targetRoot); recorded.Stage != model.UpgradeStageReconcile {
		t.Fatalf("recorded stage = %q, want the untouched %q", recorded.Stage, model.UpgradeStageReconcile)
	}

	// The same rule for an unusable destination URL, which is refused before the
	// Runtime is asked for its status.
	unusableFixture := newImportFixture(t, v7FixtureConfig, nil)
	unusableDestination := newFakeDestinationCPA(t, "")
	unusableRuntime := newFakeImportRuntime(model.RuntimeStateOffline)
	unusableRuntime.staged = unusableDestination
	options := importOptions(unusableFixture, unusableRuntime, "http://example.invalid/with space", boolPointer(true))
	_, err = ImportCPA(context.Background(), options)
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationUnready {
		t.Fatalf("an unusable destination URL produced %v (code %q), want %s", err, code, UpgradeErrorCPADestinationUnready)
	}
	if recorded := readOperation(t, unusableFixture.targetRoot); recorded.Stage != model.UpgradeStageReconcile {
		t.Fatalf("recorded stage = %q, want the untouched %q", recorded.Stage, model.UpgradeStageReconcile)
	}
}

// TestImportCPARefusesARetryThatChangesTheStagedRequest pins the immutability of
// the recorded attempt. The Supervisor replays a journalled stage by operation ID
// and never applies a different request under the same one, so a retry with the
// other plugin choice, a different destination URL or another client key set would
// keep the destination it staged while reporting the new choice.
func TestImportCPARefusesARetryThatChangesTheStagedRequest(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "")
	destination.silentDropPath = "routing.retry.request-retry"
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	if _, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true))); err == nil {
		t.Fatal("a dropped configuration write was proved")
	}
	stagedCall := runtime.stageCall
	if stagedCall == nil {
		t.Fatal("the first run did not stage the destination")
	}
	destination.mu.Lock()
	destination.silentDropPath = ""
	destination.mu.Unlock()

	// The other plugin choice is a different request, not a retry.
	_, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(false)))
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationRequestChanged {
		t.Fatalf("a changed plugin choice produced %v (code %q), want %s", err, code, UpgradeErrorCPADestinationRequestChanged)
	}
	if runtime.stageCall != stagedCall {
		t.Fatal("a changed plugin choice submitted a second destination request")
	}

	// Another destination is not this attempt's to reuse either.
	other := newFakeDestinationCPA(t, "")
	_, err = ImportCPA(context.Background(), importOptions(fixture, runtime, other.url(), boolPointer(true)))
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationRequestChanged {
		t.Fatalf("a changed destination URL produced %v (code %q), want %s", err, code, UpgradeErrorCPADestinationRequestChanged)
	}
	if requests := other.requests(); len(requests) != 0 {
		t.Fatalf("the other destination answered %v, want no request", requests)
	}
	if recorded := readOperation(t, fixture.targetRoot); recorded.HasDomain(model.UpgradeDomainCPAConfig) {
		t.Fatalf("recorded domains = %v, want no cpa_config", recorded.DomainsDone)
	}
}

// TestImportCPARefusesAReadyDestinationThatIsNotThisImportsTarget pins the
// binding of a destination this run did not stage. A ready CPA carrying another
// installation's client keys is not the target of this import; it used to receive
// every configuration mutation and credential upload and only then fail the proof,
// leaving another CPA's configuration rewritten by an operation that never
// recorded the work.
func TestImportCPARefusesAReadyDestinationThatIsNotThisImportsTarget(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "foreign-management-secret-value-0123456789")
	destination.clientKeys = []string{"another-installation-client-key"}
	runtime := newFakeImportRuntime(model.RuntimeStateReady)
	runtime.staged = destination
	options := importOptions(fixture, runtime, destination.url(), boolPointer(true))
	options.DestinationManagementKey = destination.management
	_, err := ImportCPA(context.Background(), options)
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAImportTargetForeign {
		t.Fatalf("a foreign ready destination produced %v (code %q), want %s", err, code, UpgradeErrorCPAImportTargetForeign)
	}
	for _, request := range destination.requests() {
		if strings.HasPrefix(request, http.MethodPatch+" ") || strings.HasPrefix(request, http.MethodPut+" ") || strings.HasPrefix(request, http.MethodPost+" ") {
			t.Fatalf("a foreign target received a write: %s", request)
		}
	}
	if recorded := readOperation(t, fixture.targetRoot); recorded.HasDomain(model.UpgradeDomainCPAConfig) {
		t.Fatalf("recorded domains = %v, want no cpa_config", recorded.DomainsDone)
	}
}

// TestImportCPARefusesAForeignDestinationBeforeAnyWrite is the High finding: a
// run that stages its destination from a stopped state used to submit the stage
// and then address the URL the caller supplied, so an offline Runtime plus a URL
// naming another CPA handed that CPA the source's configuration and credentials
// and only then failed the proof. Every write branch now binds the URL to the
// staged destination first, so the foreign CPA receives the authenticated read
// that proves it is not this operation's target and no mutation at all.
func TestImportCPARefusesAForeignDestinationBeforeAnyWrite(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{
		"codex-user.json": `{"type":"codex","access_token":"token"}`,
	})
	const secret = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	staged := newFakeDestinationCPA(t, "")
	foreign := newFakeDestinationCPA(t, secret)
	foreign.clientKeys = []string{"another-installation-client-key"}
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = staged
	options := importOptions(fixture, runtime, foreign.url(), boolPointer(true))
	options.DestinationManagementKey = secret
	_, err := ImportCPA(context.Background(), options)
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAImportTargetForeign {
		t.Fatalf("an offline stage with a foreign URL produced %v (code %q), want %s", err, code, UpgradeErrorCPAImportTargetForeign)
	}
	if runtime.stageCall == nil {
		t.Fatal("the test did not stage a destination, so it proves nothing about the binding")
	}
	for _, request := range foreign.requests() {
		for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodPost, http.MethodDelete} {
			if strings.HasPrefix(request, method+" ") {
				t.Fatalf("the foreign CPA received a mutation: %s", request)
			}
		}
	}
	foreign.mu.Lock()
	uploads := len(foreign.credentials)
	foreign.mu.Unlock()
	if uploads != 0 {
		t.Fatalf("the foreign CPA received %d credential uploads", uploads)
	}
	// The staged destination is not written either: the executor never ran.
	for _, request := range staged.requests() {
		for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodPost, http.MethodDelete} {
			if strings.HasPrefix(request, method+" ") {
				t.Fatalf("the staged destination received a mutation before the URL was bound: %s", request)
			}
		}
	}
	recorded := readOperation(t, fixture.targetRoot)
	if recorded.Stage != model.UpgradeStageReconcile {
		t.Fatalf("recorded stage = %q, want the untouched %q", recorded.Stage, model.UpgradeStageReconcile)
	}
	if recorded.HasDomain(model.UpgradeDomainCPAConfig) {
		t.Fatalf("recorded domains = %v, want no cpa_config", recorded.DomainsDone)
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

// TestImportCPAHoldsTheDestinationConnectionInTheManagerConfig pins the High
// finding's success path: the secret this step minted is the only one the
// destination will ever accept, so the Manager's own connection has to keep it.
// The stored configuration reaches the destination after a restart, and the
// unrelated settings the import does not own survive unchanged.
func TestImportCPAHoldsTheDestinationConnectionInTheManagerConfig(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	seedManagerConfig(t, fixture.targetRoot, model.ManagerConfig{
		Collector: model.ManagerCollectorConfig{
			CollectorMode: "http", Queue: "custom-usage", PopSide: "left", BatchSize: 7,
			PollIntervalMS: 900, QueryLimit: 4321, TLSSkipVerify: true,
		},
		CodexInspection: model.ManagerCodexInspectionConfig{Enabled: boolPointer(true)},
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
	staged := runtime.stageCall
	if staged == nil || strings.TrimSpace(staged.ManagementKey) == "" {
		t.Fatalf("the run staged no destination secret: %+v", staged)
	}
	cfg := managerConfig(t, fixture.targetRoot)
	if cfg.CPAConnection.CPABaseURL != destination.url() {
		t.Fatalf("Manager CPA connection = %q, want the destination %q", cfg.CPAConnection.CPABaseURL, destination.url())
	}
	if cfg.CPAConnection.ManagementKey != staged.ManagementKey {
		t.Fatal("the Manager connection does not hold the secret the destination was staged with")
	}
	if cfg.Collector.Queue != "custom-usage" || cfg.Collector.BatchSize != 7 || !cfg.Collector.TLSSkipVerify {
		t.Fatalf("the import rewrote the collector settings: %+v", cfg.Collector)
	}
	if cfg.CodexInspection.Enabled == nil || !*cfg.CodexInspection.Enabled {
		t.Fatalf("the import rewrote the codex inspection settings: %+v", cfg.CodexInspection)
	}
	// The attempt row is released only because the connection now holds the same
	// secret, so nothing this step minted is left without an owner.
	if _, _, found, err := loadCPAImportDestinationRecord(context.Background(), newTargetPaths(fixture.targetRoot).Database, newTargetPaths(fixture.targetRoot).DataKey); err != nil || found {
		t.Fatalf("the destination attempt survived the successful import (found %t): %v", found, err)
	}
}

// TestImportCPARefusesToRepointAnExistingManagerConnection pins the other half:
// a stored connection to a different CPA is not overwritten. The refusal leaves
// the attempt recorded, so the secret is not lost and the same run completes once
// the operator rebinds the connection explicitly.
func TestImportCPARefusesToRepointAnExistingManagerConnection(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	const otherURL = "http://other-cpa.example:8317"
	const otherKey = "another-cpa-management-key"
	seedManagerConfig(t, fixture.targetRoot, model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: otherURL, ManagementKey: otherKey},
	})
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	options := importOptions(fixture, runtime, destination.url(), boolPointer(true))
	_, err := ImportCPA(context.Background(), options)
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAConnectionConflict {
		t.Fatalf("a connection to another CPA produced %v (code %q), want %s", err, code, UpgradeErrorCPAConnectionConflict)
	}
	unchanged := managerConfig(t, fixture.targetRoot)
	if unchanged.CPAConnection.CPABaseURL != otherURL || unchanged.CPAConnection.ManagementKey != otherKey {
		t.Fatalf("the refusal rewrote the stored connection: %+v", unchanged.CPAConnection)
	}
	if recorded := readOperation(t, fixture.targetRoot); recorded.HasDomain(model.UpgradeDomainCPAConfig) {
		t.Fatalf("recorded domains = %v, want no cpa_config", recorded.DomainsDone)
	}
	paths := newTargetPaths(fixture.targetRoot)
	_, recordedSecret, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
	if err != nil || !found {
		t.Fatalf("the refusal dropped the staged secret (found %t): %v", found, err)
	}
	if recordedSecret != runtime.stageCall.ManagementKey {
		t.Fatal("the recorded secret is not the one the destination was staged with")
	}

	// The operator rebinds the stored connection while Manager Server is stopped;
	// the same run then completes and both copies agree.
	seedManagerConfig(t, fixture.targetRoot, model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{CPABaseURL: destination.url(), ManagementKey: recordedSecret},
	})
	result, err := ImportCPA(context.Background(), options)
	if err != nil {
		t.Fatalf("the retry after rebinding the connection was refused: %v", err)
	}
	if result.Stage != model.UpgradeStageCPAImport || !containsValue(result.DomainsDone, model.UpgradeDomainCPAConfig) {
		t.Fatalf("the retry recorded stage %q with domains %v", result.Stage, result.DomainsDone)
	}
	repaired := managerConfig(t, fixture.targetRoot)
	if repaired.CPAConnection.CPABaseURL != destination.url() || repaired.CPAConnection.ManagementKey != recordedSecret {
		t.Fatalf("the rebuilt connection = %+v, want the staged destination", repaired.CPAConnection)
	}
	if _, _, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey); err != nil || found {
		t.Fatalf("the destination attempt survived the successful import (found %t): %v", found, err)
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

// TestCPAImportAuthorizationNamesOnlyTheCopiedReferences pins the authority the
// step runs under. It is an Upgrade authorization: a set of references, with no
// qualification and no evidence reference to fabricate. Every reference must also
// be one the product permits an adoption to copy, so the step cannot widen the
// executor's write surface by naming a section.
func TestCPAImportAuthorizationNamesOnlyTheCopiedReferences(t *testing.T) {
	authorization := cpaImportAuthorization()
	want := []string{"cpa-auth-files", "cpa-config-routing", "provider-state-api-keys"}
	migratable := map[bootstrap.ResourceRef]bool{}
	for _, ref := range bootstrap.MigratedAdoptionResourceRefs() {
		migratable[ref] = true
	}
	got := make([]string, 0, len(authorization.Refs))
	for _, ref := range authorization.Refs {
		if !migratable[ref] {
			t.Fatalf("the import authorizes %q, which the product does not permit an adoption to copy", ref)
		}
		got = append(got, string(ref))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("authorized references = %v, want %v", got, want)
	}
	// The constructor is the gate: a reference outside the set is refused before
	// any run, so the authorization cannot carry a Manager-owned section.
	for _, ref := range []bootstrap.ResourceRef{bootstrap.AdoptionRefConfigServer, bootstrap.AdoptionRefConfigPlugins, ""} {
		if _, err := freshadopt.NewUpgrade(nil, nil, freshadopt.UpgradeAuthorization{Refs: []bootstrap.ResourceRef{ref}}); err == nil {
			t.Fatalf("the executor accepted an authorization for %q", ref)
		}
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

// seedManagerConfig writes the Manager's own encrypted configuration, which is
// the row the import hands the destination connection to and whose unrelated
// settings it must preserve.
func seedManagerConfig(t *testing.T, targetRoot string, cfg model.ManagerConfig) {
	t.Helper()
	paths := newTargetPaths(targetRoot)
	key, _, err := security.LoadOrCreateDataKey("", paths.DataKey)
	if err != nil {
		t.Fatalf("load the fixture data key: %v", err)
	}
	protector, err := security.NewProtector(key)
	if err != nil {
		t.Fatalf("initialize the fixture protector: %v", err)
	}
	db, err := managersqlite.Open(paths.Database)
	if err != nil {
		t.Fatalf("open the v2 database: %v", err)
	}
	defer func() { _ = db.Close() }()
	if err := setting.New(db, protector).SaveManagerConfig(context.Background(), cfg); err != nil {
		t.Fatalf("write the fixture manager configuration: %v", err)
	}
}

// managerConfig reads the Manager's own configuration through the repository the
// running server resolves its CPA connection from.
func managerConfig(t *testing.T, targetRoot string) model.ManagerConfig {
	t.Helper()
	paths := newTargetPaths(targetRoot)
	key, _, err := security.LoadOrCreateDataKey("", paths.DataKey)
	if err != nil {
		t.Fatalf("load the fixture data key: %v", err)
	}
	protector, err := security.NewProtector(key)
	if err != nil {
		t.Fatalf("initialize the fixture protector: %v", err)
	}
	db, err := managersqlite.Open(paths.Database)
	if err != nil {
		t.Fatalf("open the v2 database: %v", err)
	}
	defer func() { _ = db.Close() }()
	cfg, found, err := setting.New(db, protector).LoadManagerConfig(context.Background())
	if err != nil || !found {
		t.Fatalf("read the manager configuration (found %t): %v", found, err)
	}
	return cfg
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
