package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshadopt"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

// The fixture's own secrets. Every leak check below greps for these exact
// values, because a leak would be one of them reaching durable state.
const (
	migrationSourceKey         = "adopt-migration-source-management-key"
	migrationCredentialToken   = "adopt-migration-credential-token"
	migrationProviderKeyValue  = "adopt-migration-provider-key"
	migrationSourceClientValue = "adopt-migration-source-client-key"
)

// migrationFakeCPA is the smallest CPA v8 that can serve both ends of the
// migration: the read face the executor lists, downloads and reads, and the
// write face it patches. It records every write so a test can prove what the
// executor did and did not touch.
type migrationFakeCPA struct {
	mu          sync.Mutex
	auth        string
	authFn      func() string
	server      *httptest.Server
	credentials map[string][]byte
	types       map[string]string
	disabled    map[string]bool
	runtimeOnly map[string]bool
	refuse      map[string]bool
	// writeFailures refuses these dotted configuration paths with the given
	// status, which is how a definite CPA refusal is reproduced.
	writeFailures map[string]int
	config        map[string]any
	writes        []string
	uploadsLog    []string
	// beforeDownload blocks every credential download until it returns. It is
	// how a test holds the copy inside a source read.
	beforeDownload func()
}

func newMigrationFakeCPA(t *testing.T) *migrationFakeCPA {
	t.Helper()
	fake := &migrationFakeCPA{
		credentials: map[string][]byte{}, types: map[string]string{},
		disabled: map[string]bool{}, runtimeOnly: map[string]bool{}, refuse: map[string]bool{},
		writeFailures: map[string]int{},
		config:        map[string]any{"config-version": json.Number("8")},
	}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *migrationFakeCPA) URL() string { return f.server.URL }

func (f *migrationFakeCPA) key() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.authFn != nil {
		return f.authFn()
	}
	return f.auth
}

func (f *migrationFakeCPA) serve(w http.ResponseWriter, r *http.Request) {
	// CPA answers every management read as JSON; the source safe-read refuses any
	// other media type, so the fake has to be honest about it.
	w.Header().Set("Content-Type", "application/json")
	path := r.URL.Path
	if path == "/v1/models" {
		// The client face, not the management face: only an installed client Key
		// answers, and the management key and an anonymous call are both refused.
		for _, key := range f.clientKeys() {
			if r.Header.Get("Authorization") == "Bearer "+key {
				_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
				return
			}
		}
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+f.key() {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch {
	case path == "/v8/management/credentials" && r.Method == http.MethodGet:
		f.listCredentials(w)
	case path == "/v8/management/credentials" && r.Method == http.MethodPost:
		f.uploadCredential(w, r)
	case path == "/v8/management/credentials/download" && r.Method == http.MethodGet:
		f.downloadCredential(w, r)
	case path == "/v8/management/credentials/status" && r.Method == http.MethodPatch:
		f.patchStatus(w, r)
	case path == "/v8/management/config" && r.Method == http.MethodGet:
		f.mu.Lock()
		body, _ := json.Marshal(f.config)
		f.mu.Unlock()
		_, _ = w.Write(body)
	case strings.HasPrefix(path, "/v8/management/config/") && r.Method != http.MethodGet:
		f.writeConfig(w, r, strings.TrimPrefix(path, "/v8/management/config/"))
	case strings.HasPrefix(path, "/v8/management/config/") && r.Method == http.MethodGet:
		dotted := strings.Split(strings.ReplaceAll(strings.TrimPrefix(path, "/v8/management/config/"), "/", "."), ".")
		f.mu.Lock()
		value, ok := migrationLookup(f.config, dotted)
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "not_found"})
			return
		}
		_ = json.NewEncoder(w).Encode(value)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// clientKeys reports the destination's installed client key list.
func (f *migrationFakeCPA) clientKeys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	access, _ := f.config["access"].(map[string]any)
	if access == nil {
		return nil
	}
	raw, _ := access["api-keys"].([]any)
	keys := make([]string, 0, len(raw))
	for _, entry := range raw {
		if text, ok := entry.(string); ok {
			keys = append(keys, text)
		}
	}
	return keys
}

func (f *migrationFakeCPA) listCredentials(w http.ResponseWriter) {
	f.mu.Lock()
	files := make([]map[string]any, 0, len(f.credentials))
	for name := range f.credentials {
		files = append(files, map[string]any{
			"name": name, "type": f.types[name],
			"disabled": f.disabled[name], "runtime_only": f.runtimeOnly[name],
		})
	}
	f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"files": files})
}

func (f *migrationFakeCPA) downloadCredential(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	f.mu.Lock()
	gate := f.beforeDownload
	data, ok := f.credentials[name]
	f.mu.Unlock()
	if gate != nil {
		gate()
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	_, _ = w.Write(data)
}

func (f *migrationFakeCPA) uploadCredential(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.writes = append(f.writes, "POST "+r.URL.Path+" "+name)
	f.uploadsLog = append(f.uploadsLog, name)
	refused := f.refuse[name]
	if !refused {
		f.credentials[name] = body
		var parsed struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(body, &parsed)
		if f.types[name] == "" {
			f.types[name] = parsed.Type
		}
	}
	f.mu.Unlock()
	if refused {
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid auth file"})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func (f *migrationFakeCPA) patchStatus(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name     string `json:"name"`
		Disabled bool   `json:"disabled"`
	}
	_ = json.NewDecoder(r.Body).Decode(&request)
	f.mu.Lock()
	f.disabled[request.Name] = request.Disabled
	f.writes = append(f.writes, "PATCH "+r.URL.Path+" "+request.Name)
	f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func (f *migrationFakeCPA) writeConfig(w http.ResponseWriter, r *http.Request, slashPath string) {
	dotted := strings.ReplaceAll(strings.Trim(slashPath, "/"), "/", ".")
	parts := strings.Split(dotted, ".")
	body, _ := io.ReadAll(r.Body)
	// A refused write changes nothing, exactly like CPA's own 4xx: the refusal is
	// matched against the path the write names.
	f.mu.Lock()
	failing := 0
	for path, status := range f.writeFailures {
		if path == dotted || strings.HasPrefix(path, dotted+".") || strings.HasPrefix(dotted, path+".") {
			failing = status
			break
		}
	}
	f.mu.Unlock()
	if failing != 0 {
		w.WriteHeader(failing)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_config", "message": `cannot set "` + dotted + `": secret value quoted`})
		return
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.writes = append(f.writes, r.Method+" "+r.URL.Path)
	migrationMerge(f.config, parts, value)
	f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func migrationLookup(document map[string]any, parts []string) (any, bool) {
	var cursor any = document
	for _, part := range parts {
		mapping, ok := cursor.(map[string]any)
		if !ok {
			return nil, false
		}
		if cursor, ok = mapping[part]; !ok {
			return nil, false
		}
	}
	return cursor, true
}

func migrationMerge(document map[string]any, parts []string, value any) {
	cursor := document
	for _, part := range parts[:len(parts)-1] {
		next, ok := cursor[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			cursor[part] = next
		}
		cursor = next
	}
	key := parts[len(parts)-1]
	if incoming, ok := value.(map[string]any); ok {
		if existing, ok := cursor[key].(map[string]any); ok {
			for nested, item := range incoming {
				migrationMerge(existing, []string{nested}, item)
			}
			return
		}
	}
	cursor[key] = value
}

// uploads reports the file name of every credential upload this CPA received.
func (f *migrationFakeCPA) uploads() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.uploadsLog...)
}

func (f *migrationFakeCPA) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

func (f *migrationFakeCPA) writesContaining(fragment string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, entry := range f.writes {
		if strings.Contains(entry, fragment) {
			return true
		}
	}
	return false
}

func (f *migrationFakeCPA) value(path ...string) any {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, _ := migrationLookup(f.config, path)
	return value
}

// migratingDecisions is the fully qualified record decision set of a real-CPA
// adoption: exactly the references the rules permit to migrate, each with its
// own read/replay/verify refs. The executor copies only what a record marks
// migrate, so a fixture that expects a copy has to hand the executor this.
func migratingDecisions() []bootstrap.AdoptionResourceDecision {
	refs := bootstrap.MigratedAdoptionResourceRefs()
	decisions := make([]bootstrap.AdoptionResourceDecision, 0, len(refs))
	for _, ref := range refs {
		rule, ok := bootstrap.FreshAdoptionRule(ref)
		if !ok {
			panic("migrated ref without a rule: " + string(ref))
		}
		decisions = append(decisions, bootstrap.AdoptionResourceDecision{
			ResourceRef: ref, Kind: rule.Kind, Scope: rule.Scope,
			ReadEvidenceRef:   bootstrap.CapabilityEvidenceRef("preflight:" + string(ref) + "-read"),
			ReplayEvidenceRef: bootstrap.CapabilityEvidenceRef("preflight:" + string(ref) + "-replay"),
			VerifyEvidenceRef: bootstrap.CapabilityEvidenceRef("preflight:" + string(ref) + "-verify"),
			Qualification:     bootstrap.CapabilityQualified, Disposition: bootstrap.Migrate,
		})
	}
	return decisions
}

// adoptMigrationFixture is a staged adoption whose source CPA carries every
// migration kind and whose destination is the Manager-owned CPA the Supervisor
// just staged. It is exactly the state the strategy handler is in when the
// committed checkpoint has been persisted.
type adoptMigrationFixture struct {
	app         *freshApplication
	runtime     *migrationRuntime
	source      *migrationFakeCPA
	destination *migrationFakeCPA
	record      freshstore.Record
	request     freshstrategy.Request
}

func newAdoptMigrationFixture(t *testing.T) *adoptMigrationFixture {
	t.Helper()
	ctx := t.Context()
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	opened, err := fresh.Open(ctx, paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	artifact := model.RuntimeArtifactID("sha256:" + strings.Repeat("a", 64))
	runtime := &migrationRuntime{status: model.RuntimeObservedStatus{
		Identity: "runtime", Generation: 3, ProtocolVersion: "v1", State: model.RuntimeStateReady,
		Capabilities:          model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStageDestination},
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact},
	}}
	fixture := &adoptMigrationFixture{runtime: runtime}

	// Source: two copyable credentials (one disabled), one runtime-only
	// credential, and configuration of all three migration kinds. The
	// Manager-owned values differ from the destination's, so an accidental copy
	// is visible rather than plausible.
	source := newMigrationFakeCPA(t)
	source.auth = migrationSourceKey
	source.credentials["claude-source.json"] = []byte(`{"type":"claude","access_token":"` + migrationCredentialToken + `"}`)
	source.types["claude-source.json"] = "claude"
	source.credentials["codex-source.json"] = []byte(`{"type":"codex","access_token":"` + migrationCredentialToken + `-codex"}`)
	source.types["codex-source.json"], source.disabled["codex-source.json"] = "codex", true
	source.credentials["plugin-source.json"] = []byte(`{"runtime":true}`)
	source.types["plugin-source.json"], source.runtimeOnly["plugin-source.json"] = "plugin", true
	source.config["routing"] = map[string]any{"strategy": "round-robin", "force-model-prefix": true}
	source.config["multimedia"] = map[string]any{"disable-image-generation": "chat"}
	source.config["api-keys"] = map[string]any{"claude": []any{map[string]any{
		"name": "claude-group", "keys": []any{map[string]any{"api-key": migrationProviderKeyValue}},
	}}}
	source.config["management"] = map[string]any{"secret-key": migrationSourceKey}
	source.config["server"] = map[string]any{"host": "203.0.113.9", "port": json.Number("9999")}
	source.config["access"] = map[string]any{"api-keys": []any{migrationSourceClientValue}}
	source.config["plugins"] = map[string]any{"enabled": true, "dir": "/source/plugins"}
	source.config["oauth"] = map[string]any{"auth-dir": "/source/auth"}
	fixture.source = source

	// Destination: the freshly staged CPA. The Supervisor generates its own
	// management secret, so the fake authenticates whatever was staged last.
	destination := newMigrationFakeCPA(t)
	destination.refuse["unsupported.json"] = true
	destination.config["server"] = map[string]any{"host": "0.0.0.0", "port": json.Number("8317")}
	destination.config["plugins"] = map[string]any{"enabled": false, "dir": "/supervisor/plugins"}
	destination.config["oauth"] = map[string]any{"auth-dir": "/destination/auth"}
	// StageDestination installs the source's client keys on the destination, so
	// the committed destination must already answer with them for
	// cpa.VerifyV8Adoption to accept the takeover.
	destination.config["access"] = map[string]any{"api-keys": []any{migrationSourceClientValue}}
	destination.authFn = func() string { return runtime.stagedKey }
	fixture.destination = destination

	app := newFreshApplication(opened, config.Config{}, nil)
	app.strategy = freshstrategy.New(freshstrategy.Options{
		Runtime: runtime, Protector: opened.Protector, DestinationBaseURL: destination.URL(),
		Qualification:       bootstrap.CPAQualification{ManagementGeneration: "v8", ArtifactQualified: true, RequiredProfileQualified: true},
		QualifiedArtifactID: artifact, AdoptionResources: migratingDecisions(),
	})
	fixture.request = freshstrategy.Request{Strategy: bootstrap.AdoptExisting, AdoptSourceClass: bootstrap.AdoptNetworkExisting,
		BaseURL: source.URL(), ManagementKey: migrationSourceKey,
		ExpectedRuntimeIdentity: "runtime", ExpectedRuntimeGeneration: 3, ExpectedArtifactID: artifact}
	record, err := app.store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	staged, err := app.strategy.Execute(ctx, record.Operation, fixture.request)
	if err != nil || staged.Checkpoint.Stage != "staged" {
		t.Fatalf("stage: %+v %v", staged, err)
	}
	record, err = app.store.Update(ctx, record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Strategy = staged.Checkpoint
		r.AdoptionCiphertext = staged.AdoptionCiphertext
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
			OperationID: r.Operation.Identity.OperationID, Revision: 1,
			Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.app, fixture.record = app, record
	return fixture
}

func (f *adoptMigrationFixture) reload(t *testing.T) freshstore.Record {
	t.Helper()
	record, err := f.app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return record
}

// blockCredentialDownloads holds every source credential download until the
// returned function is called, and reports the first time one was entered. The
// release is idempotent and always runs at cleanup, so a failed assertion can
// never leave the background run holding an HTTP connection.
func blockCredentialDownloads(t *testing.T, fixture *adoptMigrationFixture) (func(), <-chan struct{}) {
	t.Helper()
	var once sync.Once
	gate := make(chan struct{})
	entered := make(chan struct{}, 1)
	fixture.source.mu.Lock()
	fixture.source.beforeDownload = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-gate
	}
	fixture.source.mu.Unlock()
	release := func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	return release, entered
}

// wait blocks until the background Adopt migration of this fixture has
// finished. The commit reply no longer waits for it, so every assertion on the
// per-item summary reads the record only after the run has settled.
func (f *adoptMigrationFixture) wait() { f.app.waitAdoptionMigration() }

func (f *adoptMigrationFixture) strategy(t *testing.T, record freshstore.Record) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(struct {
		Identity         bootstrap.FreshIdentity `json:"identity"`
		ExpectedRevision uint64                  `json:"expectedRevision"`
		freshstrategy.Request
	}{record.Operation.Identity, record.Operation.Revision, f.request})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/control/api/v2/strategy", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	response := httptest.NewRecorder()
	f.app.canonicalAPI(response, request)
	return response
}

// A committed adoption copies the bounded inventory and persists one result per
// item, so the completion screen has something to show and a resume has
// something to skip.
func TestCommittedAdoptionMigratesTheBoundedInventory(t *testing.T) {
	fixture := newAdoptMigrationFixture(t)

	committed := fixture.strategy(t, fixture.record)
	if committed.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", committed.Code, committed.Body.String())
	}
	var reply struct {
		bootstrap.FreshOperation
		MigrationState string `json:"migrationState"`
	}
	if json.Unmarshal(committed.Body.Bytes(), &reply) != nil || reply.MigrationState != adoptMigrationStateRunning {
		t.Fatalf("the commit reply did not report the running migration: %s", committed.Body.String())
	}
	fixture.wait()
	record := fixture.reload(t)
	if record.Operation.Strategy.Stage != "committed" || record.Connection == nil {
		t.Fatalf("adoption did not commit: %+v", record.Operation.Strategy)
	}
	if record.AdoptionSummary == nil || len(record.AdoptionSummary.Items) == 0 {
		t.Fatal("the committed adoption recorded no migration items")
	}
	migrated, manual, failed := record.AdoptionSummary.Counts()
	if migrated == 0 || manual == 0 || failed != 0 {
		t.Fatalf("unexpected item outcomes: %d migrated, %d manual, %d failed", migrated, manual, failed)
	}

	// Credentials: bytes, enabled state and the runtime-only refusal.
	for _, name := range []string{"claude-source.json", "codex-source.json"} {
		item, ok := record.AdoptionSummary.Item("credential:" + name)
		if !ok || item.Outcome != freshstore.AdoptionMigrated {
			t.Fatalf("credential %s: %+v", name, item)
		}
	}
	if !fixture.destination.disabled["codex-source.json"] {
		t.Fatal("a disabled source credential must stay disabled on the destination")
	}
	if item, _ := record.AdoptionSummary.Item("credential:plugin-source.json"); item.Outcome != freshstore.AdoptionManual || item.Instruction == "" {
		t.Fatalf("a runtime-only credential must be a manual item: %+v", item)
	}

	// Configuration of both kinds.
	if item, ok := record.AdoptionSummary.Item("config:routing.strategy"); !ok || item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("config leaf: %+v", item)
	}
	if got := fixture.destination.value("routing", "strategy"); got != "round-robin" {
		t.Fatalf("destination routing.strategy=%v", got)
	}
	if item, ok := record.AdoptionSummary.Item("section:api-keys.claude"); !ok || item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("provider section: %+v", item)
	}
	if fixture.destination.value("api-keys", "claude") == nil {
		t.Fatal("the provider section was not copied")
	}

	// Manager-owned values survive untouched.
	if got := fixture.destination.value("server", "host"); got != "0.0.0.0" {
		t.Fatalf("the destination listener was overwritten: %v", got)
	}
	if got := fixture.destination.value("plugins", "dir"); got != "/supervisor/plugins" {
		t.Fatalf("the Supervisor plugin root was overwritten: %v", got)
	}
	if got := fixture.destination.value("oauth", "auth-dir"); got != "/destination/auth" {
		t.Fatalf("the destination auth directory was overwritten: %v", got)
	}
	for _, owned := range []string{"/config/management", "/config/server/host", "/config/server/port", "/config/plugins", "/config/oauth/auth-dir", "/config/access/api-keys"} {
		if fixture.destination.writesContaining(owned) {
			t.Fatalf("the executor wrote a Manager-owned path: %s", owned)
		}
	}
}

// A manual or failed item is operator work, never an adoption failure: the
// commit reply stays 200 and the checkpoint stays committed.
func TestManualAndFailedItemsNeverFailTheAdoption(t *testing.T) {
	fixture := newAdoptMigrationFixture(t)
	// A source credential the destination refuses: the executor reports it as
	// manual. This is a manual item the fixture can really produce.
	fixture.source.mu.Lock()
	fixture.source.credentials["unsupported-source.json"] = []byte("not-json")
	fixture.source.types["unsupported-source.json"] = "unknown"
	fixture.source.mu.Unlock()
	// A destination leaf the fake refuses with CPA's own 4xx: a definite refusal,
	// recorded as manual work instead of failing the adoption.
	fixture.destination.mu.Lock()
	fixture.destination.writeFailures["routing.strategy"] = http.StatusUnprocessableEntity
	fixture.destination.mu.Unlock()

	committed := fixture.strategy(t, fixture.record)
	if committed.Code != http.StatusOK {
		t.Fatalf("a manual item failed the adoption: %d %s", committed.Code, committed.Body.String())
	}
	fixture.wait()
	record := fixture.reload(t)
	if record.Operation.Strategy.Stage != "committed" || record.AdoptionSummary == nil {
		t.Fatalf("adoption state after a manual item: %+v summary=%v", record.Operation.Strategy, record.AdoptionSummary)
	}
	_, manual, failed := record.AdoptionSummary.Counts()
	if manual == 0 {
		t.Fatal("no manual item was recorded")
	}
	if item, ok := record.AdoptionSummary.Item("credential:unsupported-source.json"); !ok || item.Outcome != freshstore.AdoptionManual {
		t.Fatalf("the refused credential must be manual work: %+v", item)
	}
	if item, ok := record.AdoptionSummary.Item("config:routing.strategy"); !ok || item.Outcome != freshstore.AdoptionManual {
		t.Fatalf("the refused leaf must be manual work: %+v", item)
	}
	if failed != 0 {
		t.Fatalf("%d items failed instead of being reported as manual work", failed)
	}
	// The reply carries the committed revision it committed, not the one the
	// background migration advanced to afterwards: the reply never waits for the
	// copy, so it cannot claim the copy's progress.
	var replied bootstrap.FreshOperation
	if json.Unmarshal(committed.Body.Bytes(), &replied) != nil || replied.Revision >= record.Operation.Revision {
		t.Fatalf("reply revision %d must precede the migration's revision %d", replied.Revision, record.Operation.Revision)
	}
	if replied.Strategy.Stage != "committed" {
		t.Fatalf("the reply must carry the committed checkpoint: %+v", replied.Strategy)
	}
}

// A committed replay resumes the migration from the durable prefix: settled
// items are never written again, and the destination therefore sees no second
// upload.
func TestCommittedReplayResumesWithoutRewritingItems(t *testing.T) {
	fixture := newAdoptMigrationFixture(t)
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()
	committed := fixture.reload(t)
	before, beforeItems := fixture.destination.writeCount(), len(committed.AdoptionSummary.Items)

	replay := fixture.strategy(t, committed)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	fixture.wait()
	again := fixture.reload(t)
	if fixture.destination.writeCount() != before {
		t.Fatalf("a replay wrote %d times over settled items", fixture.destination.writeCount()-before)
	}
	if len(again.AdoptionSummary.Items) != beforeItems {
		t.Fatalf("a replay appended %d items", len(again.AdoptionSummary.Items)-beforeItems)
	}
	for index, item := range committed.AdoptionSummary.Items {
		if again.AdoptionSummary.Items[index] != item {
			t.Fatalf("a replay rewrote item %d", index)
		}
	}
}

// The source CPA is only ever read: the migration cannot write to it, and the
// test proves the fake observed no write.
func TestAdoptionMigrationNeverWritesTheSource(t *testing.T) {
	fixture := newAdoptMigrationFixture(t)
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()
	if writes := fixture.source.writeCount(); writes != 0 {
		t.Fatalf("the source received %d write requests", writes)
	}
	if got := fixture.source.value("management", "secret-key"); got != migrationSourceKey {
		t.Fatalf("the source management secret changed: %v", got)
	}
}

// No credential byte, provider key or management secret may reach the durable
// record or the HTTP reply: the summary carries identities, outcomes and
// digests only.
func TestAdoptionMigrationLeaksNoSecret(t *testing.T) {
	fixture := newAdoptMigrationFixture(t)
	response := fixture.strategy(t, fixture.record)
	if response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()
	record := fixture.reload(t)
	summary, err := json.Marshal(record.AdoptionSummary)
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{
		migrationSourceKey, migrationCredentialToken, migrationProviderKeyValue,
		migrationSourceClientValue, fixture.runtime.stagedKey,
	}
	if fixture.runtime.stagedKey == "" {
		t.Fatal("the fixture staged no destination management key")
	}
	for _, secret := range secrets {
		if strings.Contains(string(summary), secret) {
			t.Fatalf("the durable summary carries a secret")
		}
		if strings.Contains(response.Body.String(), secret) {
			t.Fatalf("the commit reply carries a secret")
		}
	}
}

// A source that cannot be read leaves the adoption committed and the summary
// empty: the migration is resumable work, not a failure of the takeover.
func TestUnreachableSourceLeavesTheAdoptionCommitted(t *testing.T) {
	fixture := newAdoptMigrationFixture(t)
	// The fixture's staged snapshot already holds the source URL and secret, so
	// taking the source down is how an unreachable source is reproduced.
	fixture.source.server.Close()
	committed := fixture.strategy(t, fixture.record)
	if committed.Code != http.StatusOK {
		t.Fatalf("an unreachable source failed the adoption: %d %s", committed.Code, committed.Body.String())
	}
	fixture.wait()
	record := fixture.reload(t)
	if record.Operation.Strategy.Stage != "committed" || record.Connection == nil {
		t.Fatalf("the adoption did not commit: %+v", record.Operation.Strategy)
	}
	if record.AdoptionSummary != nil && len(record.AdoptionSummary.Items) != 0 {
		t.Fatalf("an unreadable source recorded %d items", len(record.AdoptionSummary.Items))
	}
}

// The commit reply must not wait for the copy: it reports the committed
// checkpoint and the state `running`, and the reply is written while the
// migration is still working.
func TestCommitReplyReturnsBeforeTheMigrationEnds(t *testing.T) {
	fixture := newAdoptMigrationFixture(t)
	// The source holds a referenced credential the fake answers slowly: every
	// download blocks until the test releases it, which is exactly the case a
	// synchronous commit reply would hang on.
	release, entered := blockCredentialDownloads(t, fixture)
	committed := fixture.strategy(t, fixture.record)
	if committed.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", committed.Code, committed.Body.String())
	}
	var reply struct {
		bootstrap.FreshOperation
		MigrationState string `json:"migrationState"`
	}
	if json.Unmarshal(committed.Body.Bytes(), &reply) != nil || reply.MigrationState != adoptMigrationStateRunning {
		t.Fatalf("the commit reply must report the running migration: %s", committed.Body.String())
	}
	if reply.Strategy.Stage != "committed" {
		t.Fatalf("the reply must carry the committed checkpoint: %+v", reply.Strategy)
	}
	// The reply arrived while the copy is still inside a source read, and the
	// record it committed has not copied a single credential yet.
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the reply returned before the migration ever started")
	}
	if record := fixture.reload(t); record.AdoptionSummary != nil {
		for _, item := range record.AdoptionSummary.Items {
			if item.Kind == freshstore.AdoptionKindCredential && item.Outcome == freshstore.AdoptionMigrated {
				t.Fatalf("the reply waited for the copy: %q was already migrated", item.Item)
			}
		}
	}
	release()
	fixture.wait()
	if record := fixture.reload(t); record.AdoptionSummary == nil || len(record.AdoptionSummary.Items) == 0 {
		t.Fatal("the background migration recorded nothing after the reply")
	}
}

// Two committed replays must not start two copies of the same migration: the
// second start finds the run in flight and only reports that it is running.
func TestConcurrentReplaysStartOneMigrationRun(t *testing.T) {
	fixture := newAdoptMigrationFixture(t)
	release, entered := blockCredentialDownloads(t, fixture)
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	<-entered
	committed := fixture.reload(t)

	// A second committed replay while the first run is still inside the copy.
	replay := fixture.strategy(t, committed)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	var reply struct {
		MigrationState string `json:"migrationState"`
	}
	if json.Unmarshal(replay.Body.Bytes(), &reply) != nil || reply.MigrationState != adoptMigrationStateRunning {
		t.Fatalf("a replay while a run is in flight must report running, got %s", replay.Body.String())
	}
	release()
	fixture.wait()

	// Exactly one copy happened: each uploadable credential was uploaded once,
	// and the record holds one item per identity.
	uploads := map[string]int{}
	for _, entry := range fixture.destination.uploads() {
		uploads[entry]++
	}
	for name, count := range uploads {
		if count != 1 {
			t.Fatalf("credential %s was uploaded %d times by concurrent runs", name, count)
		}
	}
	record := fixture.reload(t)
	seen := map[string]bool{}
	for _, item := range record.AdoptionSummary.Items {
		if seen[item.Item] {
			t.Fatalf("the concurrent runs recorded %q twice", item.Item)
		}
		seen[item.Item] = true
	}
}

// The overall deadline stops the run. What it recorded stays recorded, and the
// items it never reached stay unrecorded so a later replay can retry them.
func TestMigrationDeadlineStopsTheRunWithTheRemainderUnrecorded(t *testing.T) {
	fixture := newAdoptMigrationFixture(t)

	// A cancelled run context is what the deadline produces, and it is the only
	// difference between "stopped by the clock" and "ran to the end" that the
	// executor can see. Reproduce it directly so the test does not have to wait
	// ten minutes.
	source, err := freshadopt.NewSource(t.Context(), freshadopt.Endpoint{BaseURL: fixture.source.URL(), ManagementKey: migrationSourceKey})
	if err != nil {
		t.Fatal(err)
	}
	// This test drives the executor directly rather than through the commit, so
	// the destination fake is pinned to a known secret first.
	const deadlineDestinationKey = "deadline-destination-key"
	fixture.destination.mu.Lock()
	fixture.destination.auth, fixture.destination.authFn = deadlineDestinationKey, nil
	fixture.destination.mu.Unlock()
	destination, err := freshadopt.NewDestination(t.Context(), freshadopt.Endpoint{BaseURL: fixture.destination.URL(), ManagementKey: deadlineDestinationKey})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	recorded := 0
	result, runErr := freshadopt.New(source, destination, migratingDecisions()).Run(ctx, freshstore.AdoptionSummary{}, func(summary freshstore.AdoptionSummary) error {
		recorded = len(summary.Items)
		if recorded == 2 {
			cancel()
		}
		return nil
	})
	if runErr == nil {
		t.Fatal("a cancelled run reported success")
	}
	if recorded != 2 || len(result.Summary.Items) > 2 {
		t.Fatalf("a stopped run recorded %d durable and %d returned items", recorded, len(result.Summary.Items))
	}
	// The remainder is not in the summary, which is what makes it retryable.
	if _, ok := result.Summary.Item("credential:gemini-c.json"); ok {
		t.Fatal("a stopped run recorded an item it never reached")
	}
}

// The deadline is a real bound on one run, not just a comment.
func TestAdoptionMigrationDeadlineIsTenMinutes(t *testing.T) {
	if adoptMigrationDeadline != 10*time.Minute {
		t.Fatalf("migration deadline = %s", adoptMigrationDeadline)
	}
}

// Once the installation is complete the migration is over. A committed replay
// must not copy anything: the source is by definition another install, and its
// values would overwrite what the operator chose on the destination.
func TestCompletedInstallationIsNeverMigratedAgain(t *testing.T) {
	fixture := newAdoptMigrationFixture(t)
	if response := fixture.strategy(t, fixture.record); response.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", response.Code, response.Body.String())
	}
	fixture.wait()
	record := fixture.reload(t)
	if record.AdoptionSummary == nil || len(record.AdoptionSummary.Items) == 0 {
		t.Fatal("the committed adoption recorded no items")
	}

	// The installation completes, and the operator changes the destination.
	if _, err := fixture.app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.ProductComplete = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	fixture.destination.mu.Lock()
	fixture.destination.config["routing"] = map[string]any{"strategy": "operator-choice"}
	fixture.destination.mu.Unlock()
	// A credential the source gained after completion. Without the completion
	// check a replay would happily copy it into a finished installation.
	fixture.source.mu.Lock()
	fixture.source.credentials["late-source.json"] = []byte(`{"type":"claude","access_token":"late"}`)
	fixture.source.types["late-source.json"] = "claude"
	fixture.source.mu.Unlock()
	writes, uploads := fixture.destination.writeCount(), len(fixture.destination.uploads())

	completed := fixture.reload(t)
	replay := fixture.strategy(t, completed)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay: %d %s", replay.Code, replay.Body.String())
	}
	fixture.wait()
	if got := fixture.destination.writeCount(); got != writes {
		t.Fatalf("a completed installation was migrated again: %d writes", got-writes)
	}
	if got := len(fixture.destination.uploads()); got != uploads {
		t.Fatalf("a completed installation received %d more uploads", got-uploads)
	}
	if got := fixture.destination.value("routing", "strategy"); got != "operator-choice" {
		t.Fatalf("the migration overwrote a post-completion change: %v", got)
	}
	for _, name := range fixture.destination.uploads() {
		if name == "late-source.json" {
			t.Fatal("a completed installation was migrated: a late source credential was copied")
		}
	}
}
