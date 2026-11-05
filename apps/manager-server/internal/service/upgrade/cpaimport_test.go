package upgrade

// These tests pin the Managed CPA import step end to end against a stand-in CPA
// v8 destination and a stand-in Runtime: the version gate before any write, the
// copy of configuration and credentials, the plugin choice, the target proof and
// the stage the run records.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/processlock"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	managersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
)

// cleanSourceConfig is a 1.x CPA configuration whose every key this import
// copies, so the step's own manual-item report is empty. It is the shape a test
// needs to tell "this step reported none" apart from "the row lists another
// step's work".
const cleanSourceConfig = `host: "127.0.0.1"
port: 8317
api-keys:
  - "legacy-client-key-1"
request-retry: 3
`

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
	// failCredentialUpload makes the credentials endpoint answer as a destination
	// that cannot take the file, so a test can stop a run after the attempt was
	// recorded and before cpa_config is.
	failCredentialUpload bool
	server               *httptest.Server
}

func newFakeDestinationCPA(t *testing.T, management string) *fakeDestinationCPA {
	t.Helper()
	fake := &fakeDestinationCPA{
		management:  management,
		credentials: map[string][]byte{},
		types:       map[string]string{},
		config:      map[string]any{"config-version": 8, "server": map[string]any{"port": 8317}, "oauth": map[string]any{"auth-dir": "/root/.cli-proxy-api"}},
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
	case r.URL.Path == "/v0/management/config" && r.Method == http.MethodGet:
		// The version probe reads the advertised build from the response header.
		w.Header().Set("X-CPA-Version", "v8.0.13")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"version":"v8.0.13"}`))
	case r.URL.Path == "/v8/management/config.yaml" && r.Method == http.MethodGet:
		// The server fingerprint is the digest of this body, and a real CPA serves
		// the configuration it currently holds: the executor writes routing leaves,
		// payload rule families and provider sections into c.config, so a constant
		// body would keep the pre-write fingerprint valid and hide exactly the
		// staleness the import has to avoid.
		c.mu.Lock()
		document := c.renderConfigDocument()
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write([]byte(document))
	case r.URL.Path == "/v8/management/config/access/api-keys" && r.Method == http.MethodGet:
		// The server-fingerprint probe reads the destination's key list here.
		c.mu.Lock()
		keys := append([]string(nil), c.clientKeys...)
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(keys)
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

// renderConfigDocument renders the configuration this stand-in currently holds.
// The caller holds the lock. json.Marshal sorts map keys, so two reads of an
// unchanged configuration produce one body and one digest, while any PATCH or PUT
// the executor makes changes the body exactly as it changes a real config.yaml.
func (c *fakeDestinationCPA) renderConfigDocument() string {
	document := map[string]any{}
	for key, value := range c.config {
		document[key] = value
	}
	document["config-version"] = 8
	encoded, err := json.Marshal(document)
	if err != nil {
		return ""
	}
	return string(encoded)
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
	c.mu.Lock()
	fail := c.failCredentialUpload
	c.mu.Unlock()
	if fail {
		// 503 is neither CPA's "the file is bad" (500) nor its "the name is bad"
		// (400): the transport reports it as an unknown apply, which the executor
		// settles as a failed copy. That is the state a retry has to resume from.
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"temporarily unavailable"}`))
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
	c.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_, _ = w.Write(data)
}

// writeConfig merges one PATCH body or replaces one PUT value, exactly as CPA
// does, so a test can observe the destination's own state.
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
	// The Supervisor reports the artifact it is actually running, and the import
	// only stages onto the artifact the compiled record was accepted for. A
	// stand-in that reported a synthetic artifact would make every test in this
	// file prove a refusal instead of the step, so it reports the qualified one.
	artifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
	if err != nil {
		panic("the compiled CPA adoption record names no artifact: " + err.Error())
	}
	return &fakeImportRuntime{
		state:      state,
		generation: 4,
		identity:   model.RuntimeIdentity("runtime-identity-1"),
		artifact:   artifact,
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
	// Mirror runtime-supervisor lifecycle.DestinationRequest.Validate(): a
	// legacy_managed request must carry an external:sha256:<64 lowercase hex>
	// SourceIdentity, or the real Supervisor rejects StageDestination with 400.
	if digest, ok := strings.CutPrefix(request.SourceIdentity, "external:sha256:"); !ok || !identity.IsValidSHA256Hex(digest) {
		f.refusal = "invalid_request: sourceIdentity must have prefix external:sha256: and 64 lowercase hex characters"
		return model.RuntimeOperationResult{}, errors.New(f.refusal)
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

// TestImportCPACopiesTheManagedSourceAndRecordsCPAConfig is the acceptance case
// for this build: the destination is staged with the plugin choice and the source
// client keys, the compiled adoption authority copies the credential files, the
// gateway routing configuration and the provider runtime state into it, the
// remaining items are recorded with their rule's own manual action, and the row
// advances to cpa_import with the cpa_config domain.
func TestImportCPACopiesTheManagedSourceAndRecordsCPAConfig(t *testing.T) {
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
		t.Fatalf("domains = %v, want the recorded cpa_config", result.DomainsDone)
	}
	if result.MigratedCount == 0 {
		t.Fatalf("the run copied nothing: %+v", result)
	}
	if result.ServerFingerprint == "" {
		t.Fatal("the run recorded no destination server fingerprint")
	}
	// The plugin choice reached the destination request and the result.
	if runtime.stageCall == nil || !runtime.stageCall.PluginsEnabled || !result.PluginsEnabled {
		t.Fatalf("plugin choice = %+v / %t, want true", runtime.stageCall, result.PluginsEnabled)
	}
	if runtime.stageCall.SourceClass != "legacy_managed" {
		t.Fatalf("source class = %q, want legacy_managed", runtime.stageCall.SourceClass)
	}
	// The credential files, the routing leaves and the provider sections the
	// authority marks migrate are on the destination, read back by the executor.
	destination.mu.Lock()
	credentialNames := append([]string(nil), mapKeys(destination.credentials)...)
	retry, retryFound := configValueAt(destination.config, []string{"routing", "retry", "request-retry"})
	_, geminiFound := configValueAt(destination.config, []string{"api-keys", "gemini"})
	destination.mu.Unlock()
	if !reflect.DeepEqual(credentialNames, []string{"codex-user.json"}) {
		t.Fatalf("destination credentials = %v, want the credential v8 can list", credentialNames)
	}
	if !retryFound || fmt.Sprint(retry) != "3" {
		t.Fatalf("routing.retry.request-retry = %#v/%t, want the copied value 3", retry, retryFound)
	}
	if !geminiFound {
		t.Fatal("the destination received no copied provider state")
	}
	// The recorded row carries cpa_config, and the items the rules keep manual
	// carry the rule's own action rather than a blanket deferral.
	recorded := readOperation(t, fixture.targetRoot)
	if recorded.Stage != model.UpgradeStageCPAImport {
		t.Fatalf("recorded stage = %q, want %q", recorded.Stage, model.UpgradeStageCPAImport)
	}
	if !recorded.HasDomain(model.UpgradeDomainCPAConfig) {
		t.Fatalf("recorded domains = %v, want cpa_config", recorded.DomainsDone)
	}
	joined := strings.Join(recorded.ManualItems, "\n")
	for _, want := range []string{
		"config:codex.identity-confuse", "(removed_upstream)",
		"section:something-new", "(not_in_import_scope)",
		"section:ampcode", "credential:gemini-user.json", "(gemini_credential_unsupported)",
	} {
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
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	runtime.failStart = true
	if _, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true))); err == nil {
		t.Fatal("a failed destination start was recorded as success")
	}
	stagedCall := runtime.stageCall
	if stagedCall == nil {
		t.Fatal("the first run did not stage the destination")
	}

	// The other plugin choice is a different request, not a retry.
	_, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(false)))
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationRequestChanged {
		t.Fatalf("a changed plugin choice produced %v (code %q), want %s", err, code, UpgradeErrorCPADestinationRequestChanged)
	}
	if runtime.stageCall != stagedCall {
		t.Fatal("a changed plugin choice submitted a second destination request")
	}

	// Another destination URL is not this attempt's to reuse while nothing proved
	// the recorded URL — a URL no run proved stays replaceable — but the retry
	// then supersedes the terminal attempt, starts the destination and proves the
	// URL it was given, and that CPA is not the staged one.
	runtime.mu.Lock()
	runtime.failStart = false
	runtime.mu.Unlock()
	other := newFakeDestinationCPA(t, "")
	_, err = ImportCPA(context.Background(), importOptions(fixture, runtime, other.url(), boolPointer(true)))
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAImportTargetForeign {
		t.Fatalf("a changed destination URL produced %v (code %q), want %s", err, code, UpgradeErrorCPAImportTargetForeign)
	}
	for _, request := range other.requests() {
		if strings.HasPrefix(request, http.MethodPatch+" ") || strings.HasPrefix(request, http.MethodPut+" ") || strings.HasPrefix(request, http.MethodPost+" ") {
			t.Fatalf("the other destination received a write: %s", request)
		}
	}
	if recorded := readOperation(t, fixture.targetRoot); recorded.HasDomain(model.UpgradeDomainCPAConfig) {
		t.Fatalf("recorded domains = %v, want no cpa_config", recorded.DomainsDone)
	}
}

// TestImportCPARefusesUnrecordedReadyDestinationWithIdenticalKeys is the High
// finding end to end. The Supervisor reports the artifact it runs as ready; that
// report says nothing about the process behind the caller's URL. Here a second v8
// CPA is configured with the exact same management key and the exact same ordered
// client key list as the Supervisor's own CPA, so the destination proof would
// accept it — and copying the 1.x source's configuration and credentials into it
// would hand another installation the whole managed state.
//
// Without a recorded staging attempt, this operation never staged that URL, so the
// run refuses before the URL is contacted at all: no read, no write, no attempt row
// and no stage movement. The static key the caller supplied is exactly the input
// that made the foreign CPA answerable, so it is refused with it.
func TestImportCPARefusesUnrecordedReadyDestinationWithIdenticalKeys(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{
		"codex-user.json": `{"type":"codex","access_token":"token"}`,
	})
	const sharedKey = "shared-destination-management-secret-0123456789abcdef"
	clientKeys := []string{"legacy-client-key-1", "legacy-client-key-2"}
	supervisorCPA := newFakeDestinationCPA(t, sharedKey)
	supervisorCPA.clientKeys = slices.Clone(clientKeys)
	foreignCPA := newFakeDestinationCPA(t, sharedKey)
	foreignCPA.clientKeys = slices.Clone(clientKeys)
	runtime := newFakeImportRuntime(model.RuntimeStateReady)
	runtime.staged = supervisorCPA
	options := importOptions(fixture, runtime, foreignCPA.url(), boolPointer(true))
	options.DestinationManagementKey = sharedKey

	_, err := ImportCPA(context.Background(), options)
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationUnready {
		t.Fatalf("an unrecorded ready destination with a shared key produced %v (code %q), want %s", err, code, UpgradeErrorCPADestinationUnready)
	}
	if requests := foreignCPA.requests(); len(requests) != 0 {
		t.Fatalf("the foreign CPA at the destination URL answered %v, want no request at all", requests)
	}
	if requests := supervisorCPA.requests(); len(requests) != 0 {
		t.Fatalf("the Supervisor's CPA answered %v, want no request at all", requests)
	}
	paths := newTargetPaths(fixture.targetRoot)
	if _, _, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey); err != nil || found {
		t.Fatalf("the refused run recorded a destination attempt (found %t): %v", found, err)
	}
	if recorded := readOperation(t, fixture.targetRoot); recorded.Stage != model.UpgradeStageReconcile {
		t.Fatalf("recorded stage = %q, want the untouched %q", recorded.Stage, model.UpgradeStageReconcile)
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
//
// The foreign CPA is configured with the same ordered client key list as the
// source. That is the point of the first stage minting its own secret: the keys a
// CPA shares say nothing about which process it is, and only the per-attempt
// 256-bit secret the stage writes into the Supervisor's CPA distinguishes them.
func TestImportCPARefusesAForeignDestinationBeforeAnyWrite(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{
		"codex-user.json": `{"type":"codex","access_token":"token"}`,
	})
	staged := newFakeDestinationCPA(t, "")
	foreign := newFakeDestinationCPA(t, "")
	foreign.clientKeys = []string{"legacy-client-key-1", "legacy-client-key-2"}
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = staged
	_, err := ImportCPA(context.Background(), importOptions(fixture, runtime, foreign.url(), boolPointer(true)))
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
// destination that already answers ready is proved, never staged again — but only
// because this operation recorded the attempt that staged it. The seeded row is
// the state a crash between the start and the executor leaves: the attempt exists
// (with the secret the destination was staged with) and the Supervisor reports the
// CPA ready, so the retry proves the destination and finishes without submitting a
// second stage or start.
func TestImportCPADoesNotRestageAnAlreadyRunningDestination(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "destination-management-secret-value-0123456789abcdef")
	destination.clientKeys = []string{"legacy-client-key-1", "legacy-client-key-2"}
	runtime := newFakeImportRuntime(model.RuntimeStateReady)
	runtime.staged = destination
	paths := newTargetPaths(fixture.targetRoot)
	if err := saveCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey, cpaImportDestinationRecord{
		OperationID: "upgrade-cpa-import:recorded-before-the-crash",
		Attempt:     1,
	}, destination.management); err != nil {
		t.Fatalf("seed the recorded destination attempt: %v", err)
	}
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

// TestImportCPAIsIdempotentOnceRecorded pins the resume rule for the whole step: a
// recorded import re-reads its destination evidence, re-proves the live
// destination and returns the row without writing anything. The reads are the
// point — the completion gate tells the operator to re-run the import after
// applying a manual item, so the replay has to observe what the destination serves
// now — and every one of them is a read.
func TestImportCPAIsIdempotentOnceRecorded(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	first, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true)))
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if !containsValue(first.DomainsDone, model.UpgradeDomainCPAConfig) {
		t.Fatalf("the first import recorded no cpa_config: %v", first.DomainsDone)
	}
	before := len(destination.requests())
	result, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true)))
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if !result.AlreadyComplete {
		t.Fatal("the second import did not report the recorded operation")
	}
	// The replay reports the destination it re-proved instead of leaving the
	// caller to reconstruct it from the row.
	if result.DestinationURL != destination.url() || result.ServerFingerprint != first.ServerFingerprint ||
		result.SourceIdentity != first.SourceIdentity {
		t.Fatalf("replay result = %+v, want the re-proved destination of %+v", result, first)
	}
	// It re-proves the destination, so it reads it — and only reads it.
	for _, request := range destination.requests()[before:] {
		for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodPost, http.MethodDelete} {
			if strings.HasPrefix(request, method+" ") {
				t.Fatalf("the replay wrote to the recorded destination: %s", request)
			}
		}
	}
}

// TestImportCPAReplayRefusesADestinationTheStoreNoLongerDescribes pins the replay
// guard: `cpa_config` is only reported as a finished import while the recorded
// destination evidence still matches the Manager's own connection. Removing the
// destination row is the shape of a store whose handoff did not survive.
func TestImportCPAReplayRefusesADestinationTheStoreNoLongerDescribes(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	if _, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true))); err != nil {
		t.Fatalf("first import: %v", err)
	}
	db, err := managersqlite.Open(newTargetPaths(fixture.targetRoot).Database)
	if err != nil {
		t.Fatalf("open the recorded store: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`delete from settings where key = ?`, cpaImportDestinationKeySetting); err != nil {
		_ = db.Close()
		t.Fatalf("remove the destination evidence: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close the recorded store: %v", err)
	}
	_, err = ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true)))
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAImportUnproven {
		t.Fatalf("a replay without its destination evidence produced %v (code %q), want %s", err, code, UpgradeErrorCPAImportUnproven)
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
	// The attempt row is kept: this build records no cpa_config, so a rerun must
	// replay the recorded request and the recorded secret.
	_, keptKey, found, err := loadCPAImportDestinationRecord(context.Background(), newTargetPaths(fixture.targetRoot).Database, newTargetPaths(fixture.targetRoot).DataKey)
	if err != nil || !found || keptKey != staged.ManagementKey {
		t.Fatalf("the recorded attempt (found %t) no longer holds the staged secret: %v", found, err)
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
		t.Fatalf("the retry recorded stage %q with domains %v, want cpa_import and cpa_config", result.Stage, result.DomainsDone)
	}
	repaired := managerConfig(t, fixture.targetRoot)
	if repaired.CPAConnection.CPABaseURL != destination.url() || repaired.CPAConnection.ManagementKey != recordedSecret {
		t.Fatalf("the rebuilt connection = %+v, want the staged destination", repaired.CPAConnection)
	}
	if _, keptKey, found, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey); err != nil || !found || keptKey != recordedSecret {
		t.Fatalf("the recorded attempt (found %t) no longer holds the staged secret: %v", found, err)
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
	// The mapped leaf is copied: the unknown section is the only item the rules
	// keep manual here.
	destination.mu.Lock()
	value, found := configValueAt(destination.config, []string{"routing", "retry", "request-retry"})
	destination.mu.Unlock()
	if !found || fmt.Sprint(value) != "4" {
		t.Fatalf("routing.retry.request-retry = %#v/%t, want the copied value 4", value, found)
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

// TestImportCPACopiesOnlyWhatTheAdoptionRulesAuthorize pins the authority the
// run copies under: the compiled record marks client keys, credential files,
// routing configuration and provider runtime state migrate, so those items are
// written and read back, while a reference the rules keep manual — an unknown
// or a credential v8 cannot list — is recorded with its own action and never
// written.
func TestImportCPACopiesOnlyWhatTheAdoptionRulesAuthorize(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{
		"codex-user.json": `{"type":"codex","access_token":"token"}`,
	})
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	result, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true)))
	if err != nil {
		t.Fatalf("import the fixture CPA: %v", err)
	}
	manual := map[string]string{}
	for _, item := range result.ManualItems {
		manual[item.Item] = item.Reason
	}
	if reason, ok := manual["credential:codex-user.json"]; ok {
		t.Fatalf("an authorized credential stayed manual with reason %q", reason)
	}
	if reason := manual["config:codex.identity-confuse"]; reason != "removed_upstream" {
		t.Fatalf("config:codex.identity-confuse reason = %q, want the rule's action", reason)
	}
	if reason := manual["section:something-new"]; reason != "not_in_import_scope" {
		t.Fatalf("section:something-new reason = %q, want the rule's action", reason)
	}
	// The migrated items were written; the manual ones were not invented onto the
	// destination.
	destination.mu.Lock()
	credentials := append([]string(nil), mapKeys(destination.credentials)...)
	_, unknownFound := configValueAt(destination.config, []string{"codex", "identity-confuse"})
	_, somethingNewFound := configValueAt(destination.config, []string{"something-new"})
	destination.mu.Unlock()
	if !reflect.DeepEqual(credentials, []string{"codex-user.json"}) {
		t.Fatalf("destination credentials = %v, want the copied file", credentials)
	}
	if unknownFound || somethingNewFound {
		t.Fatal("the destination received configuration the rules keep manual")
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
	// The requested stage is a fixture, not a transition: Put only ever moves a row
	// forward, so the pre-state is created from scratch with the provenance the
	// copy-in recorded. Revision 0 is the token an absent row carries.
	if _, err := db.ExecContext(context.Background(), `delete from upgrade_operations`); err != nil {
		t.Fatalf("clear the recorded operation: %v", err)
	}
	recorded.Revision = 0
	recorded.Stage = stage
	recorded.DomainsDone = domains
	if err := upgradeoperation.New(db).Put(context.Background(), recorded); err != nil {
		t.Fatalf("write the operation stage: %v", err)
	}
}

// TestImportCPARefusesADestinationStagedOnAnotherArtifact is the qualified-artifact
// gate: the Supervisor reports the artifact actually running, and a destination on
// an artifact the compiled record was never accepted for is not the target of this
// import. The refusal is raised before the attempt is read, so nothing is staged
// and no CPA receives a request.
func TestImportCPARefusesADestinationStagedOnAnotherArtifact(t *testing.T) {
	fixture := newImportFixture(t, v7FixtureConfig, map[string]string{"codex-user.json": `{"type":"codex"}`})
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	runtime.artifact = model.RuntimeArtifactID("sha256:" + strings.Repeat("cd", 32))
	_, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true)))
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAImportUnproven {
		t.Fatalf("a destination on an unqualified artifact produced %v (code %q), want %s", err, code, UpgradeErrorCPAImportUnproven)
	}
	if runtime.stageCall != nil {
		t.Fatalf("the import staged a destination on an artifact its record was never qualified for: %+v", runtime.stageCall)
	}
	if requests := destination.requests(); len(requests) != 0 {
		t.Fatalf("the destination answered %v, want no request", requests)
	}
	databasePath := newTargetPaths(fixture.targetRoot).Database
	if _, _, found, err := loadCPAImportDestinationRecord(context.Background(), databasePath, newTargetPaths(fixture.targetRoot).DataKey); err != nil || found {
		t.Fatalf("the refused run recorded a destination attempt (found %t): %v", found, err)
	}
	if recorded := readOperation(t, fixture.targetRoot); recorded.Stage != model.UpgradeStageReconcile {
		t.Fatalf("recorded stage = %q, want the untouched %q", recorded.Stage, model.UpgradeStageReconcile)
	}
}

// runManagedImport drives the real Managed import against a fixture that already
// ran the copy-in and the reconciliation, holding the 1.x ownership fence for the
// run and releasing it before the verification takes its own read-only one.
func runManagedImport(t *testing.T, fixture *verifyFixture, runtime CPAImportRuntime, destinationURL string, credentials map[string]string) (CPAImportResult, error) {
	t.Helper()
	return runManagedImportWithOptions(t, fixture, runtime, credentials, func(options *CPAImportOptions) {
		options.DestinationURL = destinationURL
	})
}

// runManagedImportWithOptions is runManagedImport for a test that drives one caller
// input, so a replay can name a value the recorded attempt did not.
func runManagedImportWithOptions(t *testing.T, fixture *verifyFixture, runtime CPAImportRuntime, credentials map[string]string, mutate func(*CPAImportOptions)) (CPAImportResult, error) {
	t.Helper()
	lock, err := processlock.AcquireExisting(fixture.volume.databasePath())
	if err != nil {
		t.Fatalf("acquire the 1.x ownership fence: %v", err)
	}
	options := CPAImportOptions{
		LegacyRoot:     fixture.volume.root,
		SourceLock:     lock,
		CPADirectory:   writeCPABeside(t, fixture.volume.root, v7FixtureConfig, credentials),
		CPAVersion:     "7.3.4",
		TargetRoot:     fixture.target,
		Runtime:        runtime,
		PluginsEnabled: boolPointer(true),
	}
	if mutate != nil {
		mutate(&options)
	}
	result, importErr := ImportCPA(context.Background(), options)
	if closeErr := lock.Close(); closeErr != nil {
		t.Fatalf("release the 1.x ownership fence: %v", closeErr)
	}
	return result, importErr
}

// destinationFingerprint is the server fingerprint the stand-in serves right now.
// It is the same derivation the import and the gate apply, so a test can compare
// the recorded evidence with the configuration the destination actually holds.
func destinationFingerprint(t *testing.T, destination *fakeDestinationCPA) string {
	t.Helper()
	destination.mu.Lock()
	body := destination.renderConfigDocument()
	destination.mu.Unlock()
	digest := sha256.Sum256([]byte(body))
	return freshkeyapply.ExternalServerFingerprint("v8.0.13", hex.EncodeToString(digest[:]))
}

// TestImportCPAThenVerifyCompletesTheManagedOperation is the end-to-end contract
// between the two steps: the executor writes routing settings, payload rule
// families and provider sections into the destination, so the configuration the
// CPA serves after the import is not the one it served before it. The import
// records the fingerprint of the document it left behind, and the gate's live
// re-proof of that same destination then reaches `complete`.
func TestImportCPAThenVerifyCompletesTheManagedOperation(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.reconcile(t)
	destination := newFakeDestinationCPA(t, "")
	before := destinationFingerprint(t, destination)
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination

	result, err := runManagedImport(t, fixture, runtime, destination.url(), map[string]string{
		"codex-user.json": `{"type":"codex","access_token":"token"}`,
	})
	if err != nil {
		t.Fatalf("import the fixture CPA: %v", err)
	}
	if !containsValue(result.DomainsDone, model.UpgradeDomainCPAConfig) {
		t.Fatalf("the import recorded no cpa_config: %v", result.DomainsDone)
	}
	// The copy changed the configuration the CPA serves, so the recorded
	// fingerprint describes the document the executor left, not the one it found.
	after := destinationFingerprint(t, destination)
	if after == before {
		t.Fatal("the import left the destination's configuration unchanged, so the test proves nothing about the fingerprint")
	}
	if result.ServerFingerprint != after {
		t.Fatalf("recorded server fingerprint = %q, want the destination's own %q", result.ServerFingerprint, after)
	}

	verified, err := Verify(context.Background(), VerifyOptions{LegacyRoot: fixture.volume.root, TargetRoot: fixture.target, Runtime: runtime})
	if err != nil {
		t.Fatalf("verify the imported operation: %v", err)
	}
	if !verified.Complete || verified.Stage != model.UpgradeStageComplete {
		t.Fatalf("verification = %+v, want the completed operation", verified)
	}
	if verified.TargetMode != model.UpgradeTargetManaged {
		t.Fatalf("verification target = %q, want %q", verified.TargetMode, model.UpgradeTargetManaged)
	}
}

// TestImportCPAReplayRefreshesADestinationThatMoved is the replay contract: the
// gate tells the operator to re-run the import after applying a manual item to the
// Managed CPA, and applying one changes the document the destination serves. The
// gate refuses the stale evidence, the re-run refreshes it from the live
// destination and reports the destination it re-proved, and the gate then
// completes.
func TestImportCPAReplayRefreshesADestinationThatMoved(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.reconcile(t)
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	credentials := map[string]string{"codex-user.json": `{"type":"codex","access_token":"token"}`}

	first, err := runManagedImport(t, fixture, runtime, destination.url(), credentials)
	if err != nil {
		t.Fatalf("import the fixture CPA: %v", err)
	}

	// The operator applies one recorded manual item on the destination, which is
	// exactly the work the verification's own instruction asks them to do.
	destination.mu.Lock()
	destination.config["ampcode"] = map[string]any{"upstream-url": "https://amp.example"}
	destination.mu.Unlock()

	_, err = Verify(context.Background(), VerifyOptions{LegacyRoot: fixture.volume.root, TargetRoot: fixture.target, Runtime: runtime})
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorVerifyBindingDrifted {
		t.Fatalf("a moved destination produced %v (code %q), want %s", err, code, UpgradeErrorVerifyBindingDrifted)
	}

	replayed, err := runManagedImport(t, fixture, runtime, destination.url(), credentials)
	if err != nil {
		t.Fatalf("re-run the import after the destination moved: %v", err)
	}
	if !replayed.AlreadyComplete {
		t.Fatal("the re-run did not report the recorded import")
	}
	if replayed.ServerFingerprint != destinationFingerprint(t, destination) || replayed.ServerFingerprint == first.ServerFingerprint {
		t.Fatalf("replayed fingerprint = %q, want the destination's own %q (was %q)", replayed.ServerFingerprint, destinationFingerprint(t, destination), first.ServerFingerprint)
	}
	if replayed.DestinationURL != destination.url() || replayed.SourceIdentity != first.SourceIdentity {
		t.Fatalf("replay result = %+v, want the destination and source identity of the first run", replayed)
	}

	verified, err := Verify(context.Background(), VerifyOptions{LegacyRoot: fixture.volume.root, TargetRoot: fixture.target, Runtime: runtime})
	if err != nil {
		t.Fatalf("verify after the refresh: %v", err)
	}
	if !verified.Complete || verified.Stage != model.UpgradeStageComplete {
		t.Fatalf("verification = %+v, want the completed operation", verified)
	}
}

// TestImportCPAReplayRefusesWhenRuntimeArtifactChanges is the High finding: the
// recorded import's authority was qualified against one v8 artifact, and the
// Supervisor reports the artifact it runs now. A Supervisor that switched to an
// unqualified artifact B while keeping the URL, the management key and the client
// key list answers the destination proof exactly as A did, so a replay that only
// re-proved the destination would refresh the stored fingerprint to B's digest and
// report a finished import for a server no accepted chain describes.
func TestImportCPAReplayRefusesWhenRuntimeArtifactChanges(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.reconcile(t)
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	credentials := map[string]string{"codex-user.json": `{"type":"codex","access_token":"token"}`}

	if _, err := runManagedImport(t, fixture, runtime, destination.url(), credentials); err != nil {
		t.Fatalf("import the fixture CPA: %v", err)
	}
	before := recordedDestinationRecord(t, fixture)

	// The Supervisor now runs an artifact no compiled record accepted, and the
	// destination's configuration moved as it would after a manual item was applied.
	// URL, management secret and client keys are unchanged, so only the artifact
	// distinguishes this server from the one the import staged.
	runtime.mu.Lock()
	runtime.artifact = model.RuntimeArtifactID("sha256:" + strings.Repeat("b", 64))
	runtime.mu.Unlock()
	destination.mu.Lock()
	destination.config["ampcode"] = map[string]any{"upstream-url": "https://amp.example"}
	destination.mu.Unlock()
	beforeRequests := len(destination.requests())

	_, err := runManagedImport(t, fixture, runtime, destination.url(), credentials)
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAImportUnproven {
		t.Fatalf("a replay on an unqualified artifact produced %v (code %q), want %s", err, code, UpgradeErrorCPAImportUnproven)
	}
	if requests := destination.requests()[beforeRequests:]; len(requests) != 0 {
		t.Fatalf("the refused replay contacted the destination: %v", requests)
	}
	after := recordedDestinationRecord(t, fixture)
	if after.ServerFingerprint != before.ServerFingerprint {
		t.Fatalf("the refused replay refreshed the stored fingerprint: %q -> %q", before.ServerFingerprint, after.ServerFingerprint)
	}
}

// TestImportCPAReplayRefusesAChangedCallerRequest pins the Medium finding: the
// plugin choice, the destination URL and the supplied management secret are the
// inputs the recorded request was staged with. A replay that named a different one
// would report the recorded verdict for a run the operator did not ask for, so each
// is refused as a changed request before the destination is contacted.
func TestImportCPAReplayRefusesAChangedCallerRequest(t *testing.T) {
	testCases := []struct {
		name   string
		mutate func(*testing.T, *CPAImportOptions)
	}{
		{name: "a different plugin choice", mutate: func(_ *testing.T, options *CPAImportOptions) {
			options.PluginsEnabled = boolPointer(false)
		}},
		{name: "a different destination URL", mutate: func(t *testing.T, options *CPAImportOptions) {
			options.DestinationURL = newFakeDestinationCPA(t, "").url()
		}},
		{name: "a different management secret", mutate: func(_ *testing.T, options *CPAImportOptions) {
			options.DestinationManagementKey = "another-management-secret-0123456789abcdef"
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newVerifyFixture(t)
			fixture.reconcile(t)
			destination := newFakeDestinationCPA(t, "")
			runtime := newFakeImportRuntime(model.RuntimeStateOffline)
			runtime.staged = destination
			credentials := map[string]string{"codex-user.json": `{"type":"codex","access_token":"token"}`}
			if _, err := runManagedImport(t, fixture, runtime, destination.url(), credentials); err != nil {
				t.Fatalf("import the fixture CPA: %v", err)
			}
			before := len(destination.requests())

			_, err := runManagedImportWithOptions(t, fixture, runtime, credentials, func(options *CPAImportOptions) {
				options.DestinationURL = destination.url()
				testCase.mutate(t, options)
			})
			if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationRequestChanged {
				t.Fatalf("a replay with %s produced %v (code %q), want %s", testCase.name, err, code, UpgradeErrorCPADestinationRequestChanged)
			}
			if requests := destination.requests()[before:]; len(requests) != 0 {
				t.Fatalf("the refused replay contacted the destination: %v", requests)
			}
		})
	}
}

// TestImportCPAReplayReportsTheRecordedSourceFacts pins the other half of the
// Medium finding: a completed import used to be replayed with no source version, no
// source layout and an empty manual-item list, so the CLI printed "manual items: 0"
// for a store whose operation row lists them. The replay now answers with what the
// attempt recorded, and the row holds it because the completion wrote it.
func TestImportCPAReplayReportsTheRecordedSourceFacts(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.reconcile(t)
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	credentials := map[string]string{"codex-user.json": `{"type":"codex","access_token":"token"}`}

	first, err := runManagedImport(t, fixture, runtime, destination.url(), credentials)
	if err != nil {
		t.Fatalf("import the fixture CPA: %v", err)
	}
	if first.SourceVersion != "7.3.4" || first.SourceLayout != "v7" {
		t.Fatalf("first result reports %q/%q, want the fixture source", first.SourceVersion, first.SourceLayout)
	}
	if len(first.ManualItems) == 0 {
		t.Fatal("the fixture source recorded no manual items, so the replay proves nothing")
	}
	recorded := recordedDestinationRecord(t, fixture)
	if !slices.Equal(recorded.ManualItems, first.ManualItems) {
		t.Fatalf("the attempt recorded manual items %+v, want the run's %+v", recorded.ManualItems, first.ManualItems)
	}
	if recorded.SourceVersion != first.SourceVersion || recorded.SourceLayout != first.SourceLayout {
		t.Fatalf("the attempt recorded source %q/%q, want %q/%q", recorded.SourceVersion, recorded.SourceLayout, first.SourceVersion, first.SourceLayout)
	}

	replayed, err := runManagedImport(t, fixture, runtime, destination.url(), credentials)
	if err != nil {
		t.Fatalf("replay the completed import: %v", err)
	}
	if !replayed.AlreadyComplete {
		t.Fatal("the replay did not report the recorded import")
	}
	if len(replayed.ManualItems) != len(first.ManualItems) || !slices.Equal(replayed.ManualItems, first.ManualItems) {
		t.Fatalf("the replay reported %d manual item(s) %+v, want the recorded %d %+v",
			len(replayed.ManualItems), replayed.ManualItems, len(first.ManualItems), first.ManualItems)
	}
	if replayed.SourceVersion != first.SourceVersion || replayed.SourceLayout != first.SourceLayout {
		t.Fatalf("the replay reported source %q/%q, want %q/%q", replayed.SourceVersion, replayed.SourceLayout, first.SourceVersion, first.SourceLayout)
	}
	if replayed.PluginsEnabled != first.PluginsEnabled {
		t.Fatalf("the replay reported plugins %t, want the recorded %t", replayed.PluginsEnabled, first.PluginsEnabled)
	}
}

// TestImportCPARetryRefusesModifiedSourceCredentialsWithUnchangedConfig pins the
// Medium finding: the Supervisor's request carries config.yaml's digest and the
// client key list, never the credential files, so a retry could hand the
// destination different credential bytes under the request the recorded attempt
// already published. The attempt now records the credential set's digest, and a
// retry that reads another set is refused before the destination is contacted.
func TestImportCPARetryRefusesModifiedSourceCredentialsWithUnchangedConfig(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.reconcile(t)
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	credentials := map[string]string{"codex-user.json": `{"type":"codex","access_token":"token"}`}

	// The copy fails after the destination was staged and the attempt recorded:
	// cpa_config is never written, so the next run resumes this attempt.
	destination.mu.Lock()
	destination.failCredentialUpload = true
	destination.mu.Unlock()
	if _, err := runManagedImport(t, fixture, runtime, destination.url(), credentials); err == nil {
		t.Fatal("a destination that refused the credential upload was recorded as a successful import")
	}
	destination.mu.Lock()
	destination.failCredentialUpload = false
	destination.mu.Unlock()

	recorded := readOperation(t, fixture.target)
	if recorded.Stage != model.UpgradeStageCPAImport || recorded.HasDomain(model.UpgradeDomainCPAConfig) {
		t.Fatalf("the failed copy left stage %q with domains %v, want cpa_import without cpa_config", recorded.Stage, recorded.DomainsDone)
	}
	attempt := recordedDestinationRecord(t, fixture)
	if attempt.SourceCredentialsDigest == "" {
		t.Fatal("the recorded attempt carries no source credentials digest, so a retry could hand over another credential set")
	}
	before := len(destination.requests())

	// config.yaml and the client keys are byte-identical here; only the credential
	// file changed, which is what a re-login on the 1.x stack looks like.
	modified := map[string]string{"codex-user.json": `{"type":"codex","access_token":"rotated-token"}`}
	_, err := runManagedImport(t, fixture, runtime, destination.url(), modified)
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPADestinationRequestChanged {
		t.Fatalf("a retry with modified source credentials produced %v (code %q), want %s", err, code, UpgradeErrorCPADestinationRequestChanged)
	}
	if requests := destination.requests()[before:]; len(requests) != 0 {
		t.Fatalf("the refused retry contacted the destination: %v", requests)
	}
	if after := readOperation(t, fixture.target); after.Stage != recorded.Stage || after.HasDomain(model.UpgradeDomainCPAConfig) {
		t.Fatalf("the refused retry moved the row: %q/%v -> %q/%v", recorded.Stage, recorded.DomainsDone, after.Stage, after.DomainsDone)
	}

	// Restoring the exact bytes makes this the same request the attempt recorded,
	// so the retry resumes it and records cpa_config.
	result, err := runManagedImport(t, fixture, runtime, destination.url(), credentials)
	if err != nil {
		t.Fatalf("the retry with the original credentials failed: %v", err)
	}
	if !containsValue(result.DomainsDone, model.UpgradeDomainCPAConfig) {
		t.Fatalf("the retry recorded no cpa_config: %v", result.DomainsDone)
	}
}

// TestImportCPAReplayWithZeroImportManualItemsDoesNotEchoReconcileManualItems
// pins the Low finding: the replay's legacy fallback parsed every line of the
// shared operation row back into an import manual item, so a store whose
// reconciliation had written an acknowledgement reported it as this step's own
// finding on every replay — and did so with a count of one for an import that
// reported none. The attempt now says whether it recorded its items at all, and
// the fallback is filtered to the prefixes this step writes.
func TestImportCPAReplayWithZeroImportManualItemsDoesNotEchoReconcileManualItems(t *testing.T) {
	fixture := newImportFixture(t, cleanSourceConfig, map[string]string{
		"codex-user.json": `{"type":"codex","access_token":"token"}`,
	})
	const reconcileLine = "reconcile:2026-03-15: accepted permanent gap (reconcile_acknowledged)"
	seedManualItem(t, fixture.targetRoot, reconcileLine)

	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination

	first, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true)))
	if err != nil {
		t.Fatalf("import the clean fixture CPA: %v", err)
	}
	if len(first.ManualItems) != 0 {
		t.Fatalf("the clean source reported %d manual item(s) %+v, want none", len(first.ManualItems), first.ManualItems)
	}
	recorded := readOperation(t, fixture.targetRoot)
	if !slices.Contains(recorded.ManualItems, reconcileLine) {
		t.Fatalf("the import dropped the reconciliation's manual item: %v", recorded.ManualItems)
	}
	paths := newTargetPaths(fixture.targetRoot)
	attempt, _, attemptFound, err := loadCPAImportDestinationRecord(context.Background(), paths.Database, paths.DataKey)
	if err != nil || !attemptFound {
		t.Fatalf("read the recorded destination attempt (found %t): %v", attemptFound, err)
	}
	if !attempt.ManualItemsRecorded {
		t.Fatal("the completed attempt did not record that it reported its manual items")
	}

	replay, err := ImportCPA(context.Background(), importOptions(fixture, runtime, destination.url(), boolPointer(true)))
	if err != nil {
		t.Fatalf("replay the completed import: %v", err)
	}
	if !replay.AlreadyComplete {
		t.Fatal("the replay did not report the recorded import")
	}
	if len(replay.ManualItems) != 0 {
		t.Fatalf("the replay echoed %d manual item(s) %+v for an import that reported none", len(replay.ManualItems), replay.ManualItems)
	}
}

// seedManualItem appends one line to the operation row, exactly as the step that
// wrote it would.
func seedManualItem(t *testing.T, targetRoot, line string) {
	t.Helper()
	db, err := managersqlite.Open(newTargetPaths(targetRoot).Database)
	if err != nil {
		t.Fatalf("open the v2 database: %v", err)
	}
	defer func() { _ = db.Close() }()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin the manual-item transaction: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := upgradeoperation.AppendManualItemsTx(context.Background(), tx, []string{line}); err != nil {
		t.Fatalf("append the manual item: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit the manual item: %v", err)
	}
}

// TestRestoreManualItemsFallsBackToTheRenderedRow covers a row written before the
// attempt kept the items: the operation row's one-line render is parsed back, and
// only the lines this step wrote are reported — a line another step wrote is not
// this step's finding.
func TestRestoreManualItemsFallsBackToTheRenderedRow(t *testing.T) {
	item := CPAImportManualItem{Item: "section:ampcode", Reason: "reconfigure_after_adoption", Instruction: "re-create it"}
	excess := CPAImportManualItem{Item: cpaImportManualItemExcessCredential, Reason: "credential_inventory_excess", Instruction: "prune the source"}
	recorded := model.UpgradeOperation{ManualItems: []string{
		boundedManualItem(item),
		"reconcile:2026-03-15: accepted permanent gap (reconcile_acknowledged)",
		boundedManualItem(excess),
	}}
	restored := restoreManualItems(recorded, cpaImportDestinationRecord{})
	if len(restored) != 2 {
		t.Fatalf("restored %d item(s) %+v, want only this step's", len(restored), restored)
	}
	if restored[0] != item || restored[1] != excess {
		t.Fatalf("restored %+v, want %+v", restored, []CPAImportManualItem{item, excess})
	}
	// A row that recorded its own items — including zero — answers with them and
	// never falls back to the shared row.
	recordedOnly := restoreManualItems(recorded, cpaImportDestinationRecord{ManualItemsRecorded: true})
	if len(recordedOnly) != 0 {
		t.Fatalf("an attempt that recorded no manual items reported %+v", recordedOnly)
	}
}

// recordedDestinationRecord reads the destination attempt a completed import left.
func recordedDestinationRecord(t *testing.T, fixture *verifyFixture) cpaImportDestinationRecord {
	t.Helper()
	record, _, found, err := loadCPAImportDestinationRecord(context.Background(), fixture.databasePath(), fixture.paths.DataKey)
	if err != nil || !found {
		t.Fatalf("the recorded destination attempt could not be read (found %t): %v", found, err)
	}
	return record
}

// TestImportCPAReplayRefusesAnUnreachableDestination locks down R2-1: when the
// recorded destination CPA is down, re-running ImportCPA must fail closed with
// UpgradeErrorCPAImportTargetUnreachable instead of reporting AlreadyComplete
// with a stale ServerFingerprint.
func TestImportCPAReplayRefusesAnUnreachableDestination(t *testing.T) {
	fixture := newVerifyFixture(t)
	fixture.reconcile(t)
	destination := newFakeDestinationCPA(t, "")
	runtime := newFakeImportRuntime(model.RuntimeStateOffline)
	runtime.staged = destination
	credentials := map[string]string{"codex-user.json": `{"type":"codex","access_token":"token"}`}

	if _, err := runManagedImport(t, fixture, runtime, destination.url(), credentials); err != nil {
		t.Fatalf("import the fixture CPA: %v", err)
	}
	destination.server.Close()

	_, err := runManagedImport(t, fixture, runtime, destination.url(), credentials)
	if code, ok := UpgradeErrorCodeOf(err); !ok || code != UpgradeErrorCPAImportTargetUnreachable {
		t.Fatalf("replay against an unreachable destination = %v (code %q), want %s", err, code, UpgradeErrorCPAImportTargetUnreachable)
	}
}
