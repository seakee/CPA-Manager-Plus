package freshadopt

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
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// fakeCPA is one CPA v8 Management API stand-in. It records every request
// method so a test can prove the source was only ever read, and it keeps the
// credential bytes and configuration it holds so a test can grep its own state.
type fakeCPA struct {
	mu sync.Mutex
	// route counts method+path prefixes.
	methods []string
	// auth is the management key this CPA accepts.
	auth string
	// credentials maps a file name to the exact bytes held.
	credentials map[string][]byte
	// credentialTypes and disabled mirror the list response.
	credentialTypes map[string]string
	disabled        map[string]bool
	// runtimeOnly names the credentials the list reports as runtime-only.
	runtimeOnly map[string]bool
	// refuseUpload rejects these file names with CPA's own 500.
	refuseUpload map[string]string
	// config is the configuration document this CPA serves.
	config map[string]any
	// writeFailures makes one dotted path fail with the given status.
	writeFailures map[string]int
	// uploadStatus > 0 makes the upload answer that status but still store the
	// bytes, which is how an unknown result is reproduced.
	uploadStatus int
	// lostUploadBody true stores the bytes then answers 500.
	lostUploadBody bool
	// ignoreStatusPatch accepts the disable PATCH and changes nothing.
	ignoreStatusPatch bool
	// failListAfterPatch makes every credential listing fail once a disable PATCH
	// has been received, so exactly the read-back of that disable is unavailable.
	failListAfterPatch bool
	statusPatched      bool
	// failDownload makes these files fail their download, which writes nothing.
	failDownload map[string]bool
	// downloads_ records the file name of every credential download requested.
	downloaded []string
	// uploads records the file name of every POST /credentials received, so a
	// test can prove which items were written twice.
	uploads []string
	// listCalls counts credential list requests, and listFilters records the
	// `?name=` filter of each, so a test can prove the run reads one entry per
	// credential rather than the whole inventory per credential.
	listCalls   int
	listFilters []string
	server      *httptest.Server
}

func newFakeCPA(t *testing.T, auth string) *fakeCPA {
	t.Helper()
	cpa := &fakeCPA{
		auth:            auth,
		credentials:     map[string][]byte{},
		credentialTypes: map[string]string{},
		disabled:        map[string]bool{},
		runtimeOnly:     map[string]bool{},
		refuseUpload:    map[string]string{},
		config:          map[string]any{},
		writeFailures:   map[string]int{},
	}
	cpa.server = httptest.NewServer(http.HandlerFunc(cpa.serve))
	t.Cleanup(cpa.server.Close)
	return cpa
}

func (c *fakeCPA) endpoint() Endpoint {
	return Endpoint{BaseURL: c.server.URL, ManagementKey: c.auth}
}

func (c *fakeCPA) requestMethods() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.methods...)
}

func (c *fakeCPA) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	c.methods = append(c.methods, r.Method+" "+r.URL.Path)
	c.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+c.auth {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	path := r.URL.Path
	switch {
	case path == "/v8/management/credentials" && r.Method == http.MethodGet:
		c.listCredentials(w, r)
	case path == "/v8/management/credentials" && r.Method == http.MethodPost:
		c.uploadCredential(w, r)
	case path == "/v8/management/credentials/download" && r.Method == http.MethodGet:
		c.downloadCredential(w, r)
	case path == "/v8/management/credentials/status" && r.Method == http.MethodPatch:
		c.patchStatus(w, r)
	case path == "/v8/management/config" && r.Method == http.MethodGet:
		c.mu.Lock()
		body, err := json.Marshal(c.config)
		c.mu.Unlock()
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	case strings.HasPrefix(path, "/v8/management/config/") && (r.Method == http.MethodPatch || r.Method == http.MethodPut):
		c.writeConfig(w, r, strings.TrimPrefix(path, "/v8/management/config/"))
	case strings.HasPrefix(path, "/v8/management/config/") && r.Method == http.MethodGet:
		c.readConfig(w, strings.TrimPrefix(path, "/v8/management/config/"))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// listCredentials reproduces CPA's own list route: `?name=` narrows the answer to
// the one credential with that name, on both of ListAuthFiles' paths.
func (c *fakeCPA) listCredentials(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	if c.failListAfterPatch && c.statusPatched {
		c.mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"failed to read auth dir"}`))
		return
	}
	nameFilter := r.URL.Query().Get("name")
	c.listCalls++
	c.listFilters = append(c.listFilters, nameFilter)
	files := make([]map[string]any, 0, len(c.credentials))
	for name := range c.credentials {
		if nameFilter != "" && name != nameFilter {
			continue
		}
		files = append(files, map[string]any{
			"name": name, "type": c.credentialTypes[name],
			"disabled": c.disabled[name], "runtime_only": c.runtimeOnly[name],
		})
	}
	c.mu.Unlock()
	sort.Slice(files, func(i, j int) bool {
		return files[i]["name"].(string) < files[j]["name"].(string)
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"observed_at": "2026-10-28T00:00:00Z", "files": files})
}

func (c *fakeCPA) downloadCredential(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	c.mu.Lock()
	c.downloaded = append(c.downloaded, name)
	data, ok := c.credentials[name]
	broken := c.failDownload[name]
	c.mu.Unlock()
	if broken {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"failed to read file"}`))
		return
	}
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"file not found"}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (c *fakeCPA) uploadCredential(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	c.uploads = append(c.uploads, name)
	refusal, refused := c.refuseUpload[name]
	lost := c.lostUploadBody
	c.mu.Unlock()
	if refused {
		// CPA answers 500 for a file it cannot parse: `writeAuthFile` returns the
		// `buildAuthFromFileData` failure and the handler reports it as 500
		// (auth_files_crud.go:125-128,482-508). 400 is only a bad name or an
		// unreadable body, which this client never sends.
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": refusal})
		return
	}
	// CPA derives the listed type from the stored file, so the fake does too.
	var stored struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(body, &stored)
	c.mu.Lock()
	c.credentials[name] = body
	if c.credentialTypes[name] == "" {
		c.credentialTypes[name] = stored.Type
	}
	c.mu.Unlock()
	if lost {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func (c *fakeCPA) patchStatus(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name     string `json:"name"`
		Disabled bool   `json:"disabled"`
	}
	if json.NewDecoder(r.Body).Decode(&request) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	c.statusPatched = true
	if !c.ignoreStatusPatch {
		c.disabled[request.Name] = request.Disabled
	}
	c.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func (c *fakeCPA) writeConfig(w http.ResponseWriter, r *http.Request, slashPath string) {
	dotted := strings.ReplaceAll(strings.Trim(slashPath, "/"), "/", ".")
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	// CPA refuses the WHOLE write when one value in it is unacceptable, so a
	// failing leaf is matched against the leaves the body would actually set.
	c.mu.Lock()
	failing := ""
	for _, leaf := range append(leafPaths(dotted, body), dotted) {
		if c.writeFailures[leaf] != 0 {
			failing = leaf
			break
		}
	}
	if failing != "" {
		status := c.writeFailures[failing]
		c.mu.Unlock()
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_config", "message": `cannot set "` + failing + `": secret value quoted`})
		return
	}
	c.mu.Unlock()
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	c.mu.Lock()
	if r.Method == http.MethodPatch {
		mergeInto(c.config, strings.Split(dotted, "."), value)
	} else {
		mergeInto(c.config, strings.Split(dotted, "."), value)
	}
	c.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "config-version": 8})
}

func (c *fakeCPA) readConfig(w http.ResponseWriter, slashPath string) {
	dotted := strings.ReplaceAll(strings.Trim(slashPath, "/"), "/", ".")
	c.mu.Lock()
	value := lookup(c.config, strings.Split(dotted, "."))
	c.mu.Unlock()
	if value == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "not_found"})
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}

// leafPaths lists the dotted paths a PATCH body below one prefix would set.
func leafPaths(prefix string, body []byte) []string {
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil
	}
	var out []string
	var walk func(string, any)
	walk = func(path string, current any) {
		mapping, ok := current.(map[string]any)
		if !ok {
			out = append(out, path)
			return
		}
		for key, item := range mapping {
			walk(path+"."+key, item)
		}
	}
	walk(prefix, value)
	return out
}

// mergeInto applies one write to a document the way CPA does: PATCH and PUT
// both merge mappings recursively at every level, so setting one leaf below a
// group prefix never replaces its siblings.
func mergeInto(document map[string]any, parts []string, value any) {
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
				mergeInto(existing, []string{nested}, item)
			}
			return
		}
	}
	cursor[key] = value
}

func lookup(document map[string]any, parts []string) any {
	var cursor any = document
	for _, part := range parts {
		mapping, ok := cursor.(map[string]any)
		if !ok {
			return nil
		}
		cursor, ok = mapping[part]
		if !ok {
			return nil
		}
	}
	return cursor
}

// seedAuthFile stores one credential file and its list metadata.
func (c *fakeCPA) seedAuthFile(name, provider string, body []byte, disabled bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.credentials[name] = body
	c.credentialTypes[name] = provider
	c.disabled[name] = disabled
}

func (c *fakeCPA) seedRuntimeOnly(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.credentials[name] = []byte(`{"runtime":true}`)
	c.credentialTypes[name] = "plugin"
	c.runtimeOnly[name] = true
}

func (c *fakeCPA) usesOnlyReads(t *testing.T) {
	t.Helper()
	for _, entry := range c.requestMethods() {
		if !strings.HasPrefix(entry, http.MethodGet+" ") {
			t.Fatalf("source received a non-GET request: %s", entry)
		}
	}
}

// listNames reports the `?name=` filter of every credential list this CPA
// received, empty when the request listed the whole inventory.
func (c *fakeCPA) listNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.listFilters...)
}

// heldNames reports the credentials this CPA holds, sorted.
func (c *fakeCPA) heldNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	names := make([]string, 0, len(c.credentials))
	for name := range c.credentials {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// uploadNames reports the file name of every credential upload this CPA received.
func (c *fakeCPA) uploadNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.uploads...)
}

// downloads reports how many times one credential was downloaded. The query
// string is kept for this, because the file name travels in it.
func (c *fakeCPA) downloads(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, entry := range c.downloaded {
		if entry == name {
			count++
		}
	}
	return count
}

// writeCount reports how many non-GET requests this CPA received.
func (c *fakeCPA) writeCount() int {
	count := 0
	for _, entry := range c.requestMethods() {
		if !strings.HasPrefix(entry, http.MethodGet+" ") {
			count++
		}
	}
	return count
}

func (c *fakeCPA) credential(name string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	data, ok := c.credentials[name]
	return data, ok
}

// sourceFixture is the source CPA of the happy path: credentials of several
// providers, one disabled file, one runtime-only file and one v8 refuses, plus a
// non-default configuration across all three migration kinds.
func sourceFixture(t *testing.T) *fakeCPA {
	t.Helper()
	source := newFakeCPA(t, "source-management-key")
	source.seedAuthFile("claude-a.json", "claude", []byte(`{"type":"claude","access_token":"src-claude-token"}`), false)
	source.seedAuthFile("codex-b.json", "codex", []byte(`{"type":"codex","access_token":"src-codex-token"}`), true)
	source.seedAuthFile("gemini-c.json", "gemini", []byte(`{"type":"gemini","access_token":"src-gemini-token"}`), false)
	source.seedRuntimeOnly("plugin-virtual.json")
	source.seedAuthFile("broken.json", "unknown", []byte("not-json"), false)
	source.config = map[string]any{
		"server": map[string]any{"host": "203.0.113.9", "port": json.Number("9999"), "trusted-proxies": []any{"10.0.0.0/8"}},
		"management": map[string]any{
			"secret-key":            "source-management-key",
			"allow-remote":          true,
			"disable-control-panel": false,
		},
		"access": map[string]any{"api-keys": []any{"src-client-key"}},
		"oauth": map[string]any{
			"auth-dir": "/source/auth",
			"model-alias": map[string]any{
				"claude": []any{map[string]any{"name": "claude-3", "alias": "claude-alias"}},
			},
			"excluded-models": map[string]any{"gemini": []any{"gemini-hidden"}},
			"settings":        map[string]any{"claude": []any{map[string]any{"name": "claude-3", "max-context-length": json.Number("200000")}}},
		},
		"plugins": map[string]any{"enabled": true, "dir": "/source/plugins"},
		"routing": map[string]any{
			"strategy":           "round-robin",
			"force-model-prefix": true,
			"retry":              map[string]any{"request-retry": json.Number("4"), "max-retry-credentials": json.Number("3")},
			"cooldown":           map[string]any{"disable-cooling": true},
		},
		"requests": map[string]any{
			"proxy-url": "http://proxy.internal:8080",
			"streaming": map[string]any{"keepalive-seconds": json.Number("120")},
			"payload": map[string]any{
				"default": []any{map[string]any{"models": []any{map[string]any{"name": "gpt-4o"}}, "params": map[string]any{"temperature": json.Number("1")}}},
			},
		},
		"multimedia": map[string]any{"disable-image-generation": "chat"},
		"upstream": map[string]any{
			"codex":  map[string]any{"response-steering": true},
			"claude": map[string]any{"model-level-cooling": true},
		},
		"client": map[string]any{"codex": map[string]any{"enable-apply-patch": true}},
		"api-keys": map[string]any{
			"claude": []any{map[string]any{"name": "claude-group", "keys": []any{map[string]any{"api-key": "src-provider-claude-key"}}}},
			"openai-compatibility": []any{map[string]any{
				"name": "compat-group", "base-url": "http://compat.internal/v1",
				"keys":   []any{map[string]any{"api-key": "src-provider-compat-key"}},
				"models": []any{map[string]any{"name": "compat-model"}},
			}},
		},
	}
	return source
}

// destinationFixture is the destination CPA: an empty, just-committed install.
func destinationFixture(t *testing.T) *fakeCPA {
	t.Helper()
	destination := newFakeCPA(t, "destination-management-key")
	// CPA refuses a file it cannot parse; the destination is where that verdict
	// lands, so the refusal is configured there.
	destination.refuseUpload["broken.json"] = "error converting YAML to JSON"
	destination.config = map[string]any{
		"server":     map[string]any{"host": "0.0.0.0", "port": json.Number("8317")},
		"management": map[string]any{"secret-key": "destination-own-secret", "allow-remote": true},
		"access":     map[string]any{"api-keys": []any{"destination-installed-key"}},
		"plugins":    map[string]any{"enabled": false, "dir": "/supervisor/plugins"},
		"oauth":      map[string]any{"auth-dir": "/destination/auth"},
	}
	return destination
}

func runMigration(t *testing.T, source, destination *fakeCPA, previous freshstore.AdoptionSummary) freshstore.AdoptionSummary {
	t.Helper()
	return runMigrationWithCheckpoint(t, source, destination, previous, nil)
}

func runMigrationWithCheckpoint(t *testing.T, source, destination *fakeCPA, previous freshstore.AdoptionSummary, checkpoint Checkpoint) freshstore.AdoptionSummary {
	t.Helper()
	sourceWire, err := NewSource(t.Context(), source.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	destinationWire, err := NewDestination(t.Context(), destination.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(sourceWire, destinationWire, migratingDecisions()).Run(context.Background(), previous, checkpoint)
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	return result.Summary
}

// migratingDecisions is the record's decision set a fully qualified real-CPA
// adoption carries: exactly the references the rules permit to migrate, each
// backed by the read/replay/verify refs of its own kind. A test that drives the
// executor directly has to hand it this record, because the executor copies only
// what the record marks migrate.
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

func itemByName(t *testing.T, summary freshstore.AdoptionSummary, name string) freshstore.AdoptionItem {
	t.Helper()
	item, ok := summary.Item(name)
	if !ok {
		t.Fatalf("missing migration item %q in %d recorded items", name, len(summary.Items))
	}
	return item
}

func TestMigrationCopiesEveryKindFromTheSourceToTheDestination(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})

	// Credential bytes and enabled state.
	for name, expected := range map[string]bool{"claude-a.json": true, "codex-b.json": true, "gemini-c.json": true} {
		item := itemByName(t, summary, credentialItemName(name))
		if item.Outcome != freshstore.AdoptionMigrated {
			t.Fatalf("credential %s: outcome=%s reason=%s", name, item.Outcome, item.Reason)
		}
		wantBytes, _ := source.credential(name)
		gotBytes, ok := destination.credential(name)
		if !ok || string(gotBytes) != string(wantBytes) {
			t.Fatalf("credential %s bytes were not copied byte for byte", name)
		}
		if !expected {
			continue
		}
	}
	if !destination.disabled["codex-b.json"] {
		t.Fatal("a disabled source credential must stay disabled on the destination")
	}
	if destination.disabled["claude-a.json"] {
		t.Fatal("an enabled source credential must stay enabled on the destination")
	}

	// Gateway leaves and rule families.
	for _, path := range []string{"routing.strategy", "routing.force-model-prefix", "requests.proxy-url", "multimedia.disable-image-generation"} {
		item := itemByName(t, summary, configLeafItemName(path))
		if item.Outcome != freshstore.AdoptionMigrated {
			t.Fatalf("leaf %s: outcome=%s reason=%s", path, item.Outcome, item.Reason)
		}
	}
	if got := lookup(destination.config, strings.Split("routing.strategy", ".")); got != "round-robin" {
		t.Fatalf("destination routing.strategy=%v", got)
	}
	if got := lookup(destination.config, []string{"multimedia", "disable-image-generation"}); got != "chat" {
		t.Fatalf("destination multimedia mode=%v", got)
	}
	family := itemByName(t, summary, configFamilyItemName("default"))
	if family.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("rule family default: %s", family.Outcome)
	}
	if lookup(destination.config, []string{"requests", "payload", "default"}) == nil {
		t.Fatal("the payload rule family was not copied")
	}

	// Provider sections.
	for _, section := range []string{"api-keys.claude", "api-keys.openai-compatibility", "oauth.model-alias", "oauth.excluded-models", "oauth.settings"} {
		item := itemByName(t, summary, providerSectionItemName(section))
		if item.Outcome != freshstore.AdoptionMigrated {
			t.Fatalf("section %s: outcome=%s reason=%s", section, item.Outcome, item.Reason)
		}
	}
	if lookup(destination.config, []string{"api-keys", "openai-compatibility"}) == nil {
		t.Fatal("the openai-compatibility section was not copied")
	}

	source.usesOnlyReads(t)
}

func TestUnsupportedCredentialBecomesAManualItemWithAnInstruction(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})

	// CPA refused the upload with its own 400: no resend can succeed.
	refused := itemByName(t, summary, credentialItemName("broken.json"))
	if refused.Outcome != freshstore.AdoptionManual || refused.Reason != "not_uploadable" || refused.Instruction == "" {
		t.Fatalf("refused credential: %+v", refused)
	}
	if _, stored := destination.credential("broken.json"); stored {
		t.Fatal("a refused credential must not be stored on the destination")
	}
	// A runtime-only credential has no file to copy at all.
	virtual := itemByName(t, summary, credentialItemName("plugin-virtual.json"))
	if virtual.Outcome != freshstore.AdoptionManual || virtual.Reason != "runtime_only_credential" || virtual.Instruction == "" {
		t.Fatalf("runtime-only credential: %+v", virtual)
	}
	if _, requested := destination.credential("plugin-virtual.json"); requested {
		t.Fatal("a runtime-only credential must never be uploaded")
	}
	// Neither refusal failed the run: the other items still migrated.
	if item := itemByName(t, summary, credentialItemName("claude-a.json")); item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("a manual item must not stop the other items: %+v", item)
	}
}

func TestOneFailedItemIsReportedWhileTheOthersContinue(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	const refusedPath = "routing.retry.request-retry"
	destination.writeFailures[refusedPath] = http.StatusUnprocessableEntity
	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})

	failed := itemByName(t, summary, configLeafItemName(refusedPath))
	if failed.Outcome != freshstore.AdoptionManual || failed.Reason == "" || failed.Instruction == "" {
		t.Fatalf("definite refusal must be a manual item: %+v", failed)
	}
	if strings.Contains(failed.Reason, "secret value quoted") || strings.Contains(failed.Instruction, "secret value quoted") {
		t.Fatal("CPA's quoted YAML value must never reach the recorded reason")
	}
	if item := itemByName(t, summary, configLeafItemName("routing.strategy")); item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("the next leaf must still migrate: %+v", item)
	}
	if item := itemByName(t, summary, credentialItemName("claude-a.json")); item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("credentials must still migrate: %+v", item)
	}
}

func TestUnknownWriteResultIsReconciledByReadBackWithoutResending(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	// The destination stores the bytes and then loses its reply. The executor
	// must not send the upload again; the destination still holds one copy.
	const lost = "gemini-c.json"
	destination.lostUploadBody = true
	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})

	uploads := 0
	for _, entry := range destination.requestMethods() {
		if entry == http.MethodPost+" /v8/management/credentials" {
			uploads++
		}
	}
	// Three plain credentials are uploaded once each; the unparseable one is
	// refused locally and the runtime-only one has no file, so neither is ever
	// sent, and no item is ever sent twice.
	if uploads != 3 {
		t.Fatalf("expected exactly one upload per uploadable credential, got %d", uploads)
	}
	item := itemByName(t, summary, credentialItemName(lost))
	if item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("a lost reply whose bytes landed must be reconciled as migrated: %+v", item)
	}
	if item.SourceSHA256 != item.DestinationSHA256 || item.SourceSHA256 == "" {
		t.Fatalf("a reconciled item must record the digest it proved: %+v", item)
	}
}

func TestRerunDoesNotRewriteMigratedItems(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	first := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	if item := itemByName(t, first, credentialItemName("claude-a.json")); item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("first run: %+v", item)
	}
	before := destination.writeCount()
	second := runMigration(t, source, destination, first)
	if after := destination.writeCount(); after != before {
		t.Fatalf("a rerun wrote %d times over already-migrated items", after-before)
	}
	if len(second.Items) != len(first.Items) {
		t.Fatalf("a rerun appended %d extra items", len(second.Items)-len(first.Items))
	}
	for index := range first.Items {
		if second.Items[index] != first.Items[index] {
			t.Fatalf("a rerun rewrote recorded item %d", index)
		}
	}
	source.usesOnlyReads(t)
}

func TestManagerOwnedFieldsAreNeverSentToTheDestination(t *testing.T) {
	// Every Manager-owned path is excluded from both tables, and no table path
	// may match the predicate.
	for _, path := range append(append(cpa.ConfigLeafPaths(), cpa.ConfigRuleFamilyPaths()...), ProviderSections()...) {
		if ManagerOwned(path) {
			t.Fatalf("table path %q is Manager-owned and must not be migratable", path)
		}
	}
	for _, path := range []string{
		"management.secret-key", "server.host", "server.port",
		"remote-management.allow-remote", "disable-control-panel",
		"plugins.enabled", "plugins.dir", "oauth.auth-dir", "access.api-keys",
	} {
		if !ManagerOwned(path) {
			t.Fatalf("Manager-owned path %q must be excluded", path)
		}
	}

	// And the executor never writes one of them, even though the source holds
	// every one of them with a different value from the destination.
	source, destination := sourceFixture(t), destinationFixture(t)
	runMigration(t, source, destination, freshstore.AdoptionSummary{})
	for _, entry := range destination.requestMethods() {
		if strings.HasPrefix(entry, http.MethodGet+" ") {
			continue
		}
		for _, owned := range []string{
			"/config/management", "/config/server/host", "/config/server/port",
			"/config/remote-management", "/config/disable-control-panel",
			"/config/plugins", "/config/oauth/auth-dir", "/config/access/api-keys",
		} {
			if strings.Contains(entry, owned) {
				t.Fatalf("executor wrote a Manager-owned path: %s", entry)
			}
		}
	}
	if got := lookup(destination.config, []string{"management", "secret-key"}); got != "destination-own-secret" {
		t.Fatalf("the destination management secret was overwritten: %v", got)
	}
	if got := lookup(destination.config, []string{"access", "api-keys"}); got == nil {
		t.Fatal("the destination client key list was removed")
	}
	if got := lookup(destination.config, []string{"server", "host"}); got != "0.0.0.0" {
		t.Fatalf("the destination listener was overwritten: %v", got)
	}
	if got := lookup(destination.config, []string{"plugins", "dir"}); got != "/supervisor/plugins" {
		t.Fatalf("the Supervisor plugin root was overwritten: %v", got)
	}
}

func TestSourceIsNeverWritten(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	runMigration(t, source, destination, freshstore.AdoptionSummary{})
	if writes := source.writeCount(); writes != 0 {
		t.Fatalf("the source received %d non-GET requests", writes)
	}
	source.usesOnlyReads(t)
	// The source's own bytes are unchanged too.
	for _, name := range []string{"claude-a.json", "codex-b.json", "gemini-c.json"} {
		if _, ok := source.credential(name); !ok {
			t.Fatalf("source credential %s disappeared", name)
		}
	}
	if got := lookup(source.config, []string{"management", "secret-key"}); got != "source-management-key" {
		t.Fatalf("the source management secret changed: %v", got)
	}
}

func TestNoSecretReachesTheSummary(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{
		"src-claude-token", "src-codex-token", "src-gemini-token",
		"src-provider-claude-key", "src-provider-compat-key", "src-client-key",
		"source-management-key", "destination-management-key",
		"source-own", "proxy.internal",
	}
	for _, secret := range secrets {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("the persisted summary contains a secret %q", secret)
		}
	}
	// Only digests, outcomes and stable identities are recorded.
	for _, item := range summary.Items {
		if item.Item == "" || item.Kind == "" || item.Outcome == "" {
			t.Fatalf("incomplete recorded item: %+v", item)
		}
		if len(item.SourceSHA256) != 0 && len(item.SourceSHA256) != 64 {
			t.Fatalf("non-digest value recorded: %q", item.SourceSHA256)
		}
	}
}

func TestDigestIsTheOnlyByteEvidenceRecorded(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	item := itemByName(t, summary, credentialItemName("claude-a.json"))
	body, _ := source.credential("claude-a.json")
	sum := sha256.Sum256(body)
	if item.SourceSHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("recorded digest %q does not match the copied bytes", item.SourceSHA256)
	}
}

func TestCredentialTypeDivergenceIsManual(t *testing.T) {
	// A destination that already classifies this file under another provider
	// keeps that classification, so the copied bytes are present but the type is
	// not: manual work, never a claimed success.
	destination := destinationFixture(t)
	destination.mu.Lock()
	destination.credentialTypes["gemini-c.json"] = "vertex"
	destination.mu.Unlock()
	source := sourceFixture(t)
	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	item := itemByName(t, summary, credentialItemName("gemini-c.json"))
	if item.Outcome != freshstore.AdoptionManual || item.Reason != "type_diverged" || item.Instruction == "" {
		t.Fatalf("type divergence must be reported as manual: %+v", item)
	}
	if strings.Contains(item.Instruction, "src-gemini-token") {
		t.Fatal("an instruction must never carry a credential byte")
	}
}

func TestManualItemNeverFailsTheAdoption(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	migrated, manual, failed := summary.Counts()
	if migrated == 0 || manual == 0 {
		t.Fatalf("expected both migrated and manual items: %d/%d/%d", migrated, manual, failed)
	}
	if failed != 0 {
		t.Fatalf("no item should have failed in the happy path: %d", failed)
	}
	if got := fmt.Sprintf("%d", len(summary.Items)); got == "0" {
		t.Fatal("no items recorded")
	}
}

// The checkpoint is the only durable record of the run, so it has to be
// written once per item, in migration order, before the next item is attempted.
// A run that batched at the end would lose every proven item to a crash. One
// final checkpoint carries the run-finished marker: completion reads it, and a
// run stopped at an item leaves a list that looks exactly like a complete one.
func TestEveryItemIsCheckpointedInOrder(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	var snapshots []freshstore.AdoptionSummary
	summary := runMigrationWithCheckpoint(t, source, destination, freshstore.AdoptionSummary{}, func(current freshstore.AdoptionSummary) error {
		snapshots = append(snapshots, current)
		return nil
	})
	if len(summary.Items) == 0 {
		t.Fatal("no items recorded")
	}
	if !summary.Finished || summary.Planned != len(summary.Items) {
		t.Fatalf("finished run did not record its marker: %+v", summary)
	}
	if len(snapshots) != len(summary.Items)+1 {
		t.Fatalf("checkpointed %d times for %d items and one marker", len(snapshots), len(summary.Items))
	}
	for _, snapshot := range snapshots[:len(summary.Items)] {
		if snapshot.Finished {
			t.Fatal("an item checkpoint claimed the run finished")
		}
	}
	if last := snapshots[len(snapshots)-1]; !last.Finished || last.Planned != len(summary.Items) || len(last.Items) != len(summary.Items) {
		t.Fatalf("final checkpoint is not the run-finished marker: %+v", last)
	}
	for index, snapshot := range snapshots[:len(summary.Items)] {
		// Each checkpoint holds exactly the items resolved so far, in order, and
		// is a strict prefix of the final summary.
		if len(snapshot.Items) != index+1 || !reflect.DeepEqual(snapshot.Items[index], summary.Items[index]) {
			t.Fatalf("checkpoint %d does not append exactly item %d", index, index)
		}
		if index > 0 && !reflect.DeepEqual(snapshot.Items[:index], summary.Items[:index]) {
			t.Fatalf("checkpoint %d rewrote an earlier item", index)
		}
	}
}

// A checkpoint that fails stops the run at that item. The item it describes has
// already been written and resolved, so the run must not continue past it: the
// caller resumes from the durable prefix, and the item that was in flight is the
// only one a resume may repeat (it is the same bytes under the same name, which
// CPA accepts idempotently).
func TestAFailedCheckpointStopsTheRunWithoutWritingLaterItems(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	errCheckpoint := errors.New("checkpoint refused")
	var durable freshstore.AdoptionSummary
	calls := 0
	sourceWire, err := NewSource(t.Context(), source.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	destinationWire, err := NewDestination(t.Context(), destination.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	// The credentials are listed in name order, so item three is codex-b.json.
	_, runErr := New(sourceWire, destinationWire, migratingDecisions()).Run(context.Background(), freshstore.AdoptionSummary{}, func(current freshstore.AdoptionSummary) error {
		calls++
		if calls == 3 {
			// The store refused this write, so this item is NOT durable: only the
			// two acknowledged checkpoints describe what a restart would read.
			return errCheckpoint
		}
		durable = current
		return nil
	})
	if !errors.Is(runErr, errCheckpoint) {
		t.Fatalf("a failed checkpoint must stop the run: %v", runErr)
	}
	if calls != 3 {
		t.Fatalf("checkpoint was called %d times after failure", calls)
	}
	if len(durable.Items) != 2 {
		t.Fatalf("durable prefix holds %d items", len(durable.Items))
	}
	// Nothing past the item in flight was written: gemini-c.json is item four
	// and must still be absent from the destination. codex-b.json is item three,
	// the one whose checkpoint failed, so its write did happen.
	if _, stored := destination.credential("gemini-c.json"); stored {
		t.Fatal("run continued past a failed checkpoint and wrote a later item")
	}
	if _, stored := destination.credential("codex-b.json"); !stored {
		t.Fatal("the item in flight was resolved before its checkpoint was refused")
	}
	// The durable prefix is exactly what a resume skips: it keeps those records
	// byte for byte and appends only the items the stop left unresolved.
	resumed := runMigration(t, source, destination, durable)
	if len(resumed.Items) <= len(durable.Items) {
		t.Fatalf("a resume did not resolve the remaining items: %d vs %d", len(resumed.Items), len(durable.Items))
	}
	for index := range durable.Items {
		if resumed.Items[index] != durable.Items[index] {
			t.Fatalf("a resume rewrote durable item %d", index)
		}
	}
	// Only the item that was in flight when the checkpoint failed may be written
	// twice. Every durable item is uploaded exactly once; broken.json is refused
	// locally and never uploaded at all.
	counts := map[string]int{}
	for _, name := range destination.uploadNames() {
		counts[name]++
	}
	if counts["claude-a.json"] != 1 || counts["gemini-c.json"] != 1 || counts["broken.json"] != 0 {
		t.Fatalf("a durable item was uploaded again: %v", counts)
	}
	// codex-b.json is the third item, so it is the one the failed checkpoint left
	// in flight. The resume reads the destination before writing, finds the bytes
	// already equal and records the item without a second upload.
	if counts["codex-b.json"] != 1 {
		t.Fatalf("the in-flight item was uploaded again: %v", counts)
	}
	if item, _ := resumed.Item(credentialItemName("codex-b.json")); item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("the in-flight item was not resolved by read-back: %+v", item)
	}
	if body, ok := destination.credential("codex-b.json"); !ok || !strings.Contains(string(body), "src-codex-token") {
		t.Fatal("the retried item left the wrong bytes on the destination")
	}
	source.usesOnlyReads(t)
}

// A nil checkpoint is a dry run: nothing is persisted and the summary is still
// complete in memory.
func TestNoCheckpointStillResolvesEveryItem(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	if len(summary.Items) == 0 {
		t.Fatal("a dry run resolved no items")
	}
}

// oversizedInventory seeds a source with more credential files than the summary
// can hold. Names are short and distinct so the count, not the name bound, is
// what the preflight has to catch.
func oversizedInventory(t *testing.T, count int) *fakeCPA {
	t.Helper()
	source := sourceFixture(t)
	for index := 0; index < count; index++ {
		name := fmt.Sprintf("bulk-%04d.json", index)
		source.seedAuthFile(name, "claude", []byte(`{"type":"claude","access_token":"bulk"}`), false)
	}
	return source
}

// A source whose inventory cannot fit the record is reported as manual work
// instead of being attempted: the record stays valid, the run completes, and a
// replay neither stops nor retries the items it already reported.
func TestOversizedInventoryIsRecordedAsManualAndTheRunCompletes(t *testing.T) {
	source := oversizedInventory(t, freshstore.MaxAdoptionItems())
	destination := destinationFixture(t)

	// The checkpoint is the store's own validator, so a record the store would
	// refuse fails here rather than in production.
	var durable freshstore.AdoptionSummary
	checkpoint := func(current freshstore.AdoptionSummary) error {
		if err := freshstore.ValidateAdoptionSummary(current); err != nil {
			return err
		}
		durable = current
		return nil
	}
	summary := runMigrationWithCheckpoint(t, source, destination, freshstore.AdoptionSummary{}, checkpoint)
	if err := freshstore.ValidateAdoptionSummary(summary); err != nil {
		t.Fatalf("the preflight produced a record the store refuses: %v", err)
	}
	if len(summary.Items) > freshstore.MaxAdoptionItems() {
		t.Fatalf("recorded %d items over the store's bound", len(summary.Items))
	}
	excess := itemByName(t, summary, "credential-inventory-excess")
	if excess.Outcome != freshstore.AdoptionManual || excess.Reason != "adoption_inventory_too_large" || excess.Instruction == "" {
		t.Fatalf("the excess must be one manual item with an instruction: %+v", excess)
	}
	// The credentials that did fit were still copied, so the excess is a bound on
	// the record and not on the migration.
	migrated, manual, failed := summary.Counts()
	if migrated == 0 || manual == 0 || failed != 0 {
		t.Fatalf("unexpected outcomes: %d migrated, %d manual, %d failed", migrated, manual, failed)
	}
	// A replay settles the excess marker instead of reporting it again, so the
	// bound cannot turn into a stop-and-resend loop.
	replay := runMigrationWithCheckpoint(t, source, destination, durable, checkpoint)
	if len(replay.Items) != len(summary.Items) {
		t.Fatalf("a replay of the bounded inventory appended %d items", len(replay.Items)-len(summary.Items))
	}
	if item := itemByName(t, replay, "credential-inventory-excess"); item.Outcome != freshstore.AdoptionManual {
		t.Fatalf("a replay rewrote the excess marker: %+v", item)
	}
	// The credential the preflight left out was never uploaded: a bounded record
	// never claims a copy it did not make.
	if _, stored := destination.credential(fmt.Sprintf("bulk-%04d.json", freshstore.MaxAdoptionItems()-1)); stored {
		t.Fatal("a credential past the bound was uploaded anyway")
	}
	source.usesOnlyReads(t)
}

// A credential name longer than an item identity is reported with a stable,
// bounded identity instead of stopping the run at a name the store refuses.
func TestCredentialNameLongerThanTheItemBoundIsManual(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	long := strings.Repeat("a", freshstore.MaxAdoptionItemName()) + ".json"
	source.seedAuthFile(long, "claude", []byte(`{"type":"claude","access_token":"long-name-token"}`), false)

	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	if err := freshstore.ValidateAdoptionSummary(summary); err != nil {
		t.Fatalf("an over-long credential name broke the record: %v", err)
	}
	item := itemByName(t, summary, overlongCredentialItem(long))
	if item.Outcome != freshstore.AdoptionManual || item.Reason != "adoption_item_name_too_long" || item.Instruction == "" {
		t.Fatalf("an over-long name must be a manual item: %+v", item)
	}
	if _, stored := destination.credential(long); stored {
		t.Fatal("a credential whose name the record cannot hold was uploaded anyway")
	}
	// The rest of the inventory still migrated: one unusable name is not a run
	// failure.
	if item := itemByName(t, summary, credentialItemName("claude-a.json")); item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("the run stopped at the over-long name: %+v", item)
	}
	// The stable identity is a digest of the name, so a replay recognizes it.
	recorded, _ := summary.Item(overlongCredentialItem(long))
	replay := runMigration(t, source, destination, summary)
	if item, ok := replay.Item(overlongCredentialItem(long)); !ok || item != recorded {
		t.Fatalf("a replay did not recognize the over-long item: %+v", item)
	}
}

// A source credential whose bytes CPA could not parse is never uploaded at all.
// CPA's answer for such a file is a 500 from `buildAuthFromFileData`, so sending
// it would only produce a refusal the executor can already predict.
func TestUnparseableCredentialIsNeverUploaded(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	// The destination would accept it (no refusal is configured); only the local
	// parse expectation can stop the upload.
	destination.mu.Lock()
	delete(destination.refuseUpload, "broken.json")
	destination.mu.Unlock()

	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	item := itemByName(t, summary, credentialItemName("broken.json"))
	if item.Outcome != freshstore.AdoptionManual || item.Reason != "not_uploadable" || item.Instruction == "" {
		t.Fatalf("an unparseable credential must be manual work: %+v", item)
	}
	for _, name := range destination.uploadNames() {
		if name == "broken.json" {
			t.Fatal("an unparseable credential was uploaded anyway")
		}
	}
	if _, stored := destination.credential("broken.json"); stored {
		t.Fatal("an unparseable credential reached the destination")
	}
}

// CPA's real refusal for an unparseable body is a 500, and the file is absent
// afterwards. That is a verdict on the bytes, not an unknown write: it becomes a
// manual item and is never resent.
func TestServerRefusedUploadThatDidNotLandIsManual(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	// A file that parses locally but CPA still refuses: the fake answers 500 and
	// stores nothing, exactly like `writeAuthFile` failing on synthesis.
	destination.mu.Lock()
	destination.refuseUpload["gemini-c.json"] = "invalid auth file: unsupported provider"
	destination.mu.Unlock()

	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	item := itemByName(t, summary, credentialItemName("gemini-c.json"))
	if item.Outcome != freshstore.AdoptionManual || item.Reason != "not_uploadable" {
		t.Fatalf("a 500 with an absent file must be manual work: %+v", item)
	}
	if uploads := destination.uploadNames(); len(uploads) != 0 && uploads[len(uploads)-1] != "gemini-c.json" {
		t.Fatalf("unexpected upload order: %v", uploads)
	}
	// A replay does not resend it: the item is settled.
	before := len(destination.uploadNames())
	replay := runMigration(t, source, destination, summary)
	if after := len(destination.uploadNames()); after != before {
		t.Fatalf("a replay resent the refused credential %d times", after-before)
	}
	if item, _ := replay.Item(credentialItemName("gemini-c.json")); item.Outcome != freshstore.AdoptionManual {
		t.Fatalf("a replay rewrote the refusal: %+v", item)
	}
}

// A crash before the checkpoint leaves the destination already holding the
// bytes. The replay must recognize them instead of writing again, and it must
// never overwrite a value the operator changed on the destination in between.
func TestReplayDoesNotRewriteWhatTheDestinationAlreadyHolds(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	// A checkpoint that refuses the very first item is a crash before the first
	// durable record, with that item's write already landed on the destination.
	sourceWire, err := NewSource(t.Context(), source.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	destinationWire, err := NewDestination(t.Context(), destination.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	errCheckpoint := errors.New("checkpoint refused")
	// The first two items are broken.json (a local manual verdict) and
	// claude-a.json: refusing the second checkpoint is a crash after claude-a.json
	// was written and before anything about it became durable.
	calls := 0
	_, runErr := New(sourceWire, destinationWire, migratingDecisions()).Run(context.Background(), freshstore.AdoptionSummary{}, func(freshstore.AdoptionSummary) error {
		calls++
		if calls == 2 {
			return errCheckpoint
		}
		return nil
	})
	if !errors.Is(runErr, errCheckpoint) {
		t.Fatalf("a refused checkpoint must stop the run: %v", runErr)
	}
	// The crash left claude-a.json on the destination with nothing recorded.
	if body, ok := destination.credential("claude-a.json"); !ok || !strings.Contains(string(body), "src-claude-token") {
		t.Fatal("the crash did not leave the first credential on the destination")
	}

	// The operator also changed one destination credential by hand before the
	// replay ran.
	destination.mu.Lock()
	destination.credentials["gemini-c.json"] = []byte(`{"type":"gemini","access_token":"operator-changed"}`)
	destination.mu.Unlock()

	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	counts := map[string]int{}
	for _, name := range destination.uploadNames() {
		counts[name]++
	}
	if counts["claude-a.json"] != 1 {
		t.Fatalf("the replay uploaded a credential the destination already held: %v", counts)
	}
	if item := itemByName(t, summary, credentialItemName("claude-a.json")); item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("an equal destination value must be recorded as migrated: %+v", item)
	}
	if item := itemByName(t, summary, credentialItemName("gemini-c.json")); item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("a divergent destination value must be copied: %+v", item)
	}
	if counts["gemini-c.json"] != 1 {
		t.Fatalf("the divergent credential was uploaded %d times", counts["gemini-c.json"])
	}
	wantBytes, _ := source.credential("gemini-c.json")
	gotBytes, _ := destination.credential("gemini-c.json")
	if string(gotBytes) != string(wantBytes) {
		t.Fatal("the source bytes did not replace the operator's divergent value")
	}
}

// configSectionValues proves a leaf and a whole section are read before they are
// written: an equal destination value costs no request.
func TestReplayDoesNotRewriteEqualConfigurationValues(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	// The destination already holds the whole source routing group and its
	// api-keys.claude section, so every leaf of that group and the section itself
	// cost no request.
	destination.mu.Lock()
	sourceRouting := lookup(source.config, []string{"routing"})
	destination.config["routing"] = map[string]any{}
	mergeInto(destination.config, []string{"routing"}, sourceRouting)
	sourceSection := lookup(source.config, []string{"api-keys", "claude"})
	mergeInto(destination.config, []string{"api-keys", "claude"}, sourceSection)
	destination.mu.Unlock()

	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	if item := itemByName(t, summary, configLeafItemName("routing.strategy")); item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("an equal leaf must be migrated: %+v", item)
	}
	for _, entry := range destination.requestMethods() {
		if !strings.HasPrefix(entry, http.MethodGet+" ") && strings.Contains(entry, "/config/routing") {
			t.Fatalf("an equal leaf was written again: %s", entry)
		}
	}
	if item := itemByName(t, summary, providerSectionItemName("api-keys.claude")); item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("an equal section must be migrated: %+v", item)
	}
	for _, entry := range destination.requestMethods() {
		if !strings.HasPrefix(entry, http.MethodGet+" ") && strings.Contains(entry, "/config/api-keys/claude") {
			t.Fatalf("an equal section was written again: %s", entry)
		}
	}
}

// An item the source does not set is recorded, so a replay cannot mistake the
// operator's later destination value for a stale copy.
func TestAbsentAtSourceIsRecordedSoAReplayLeavesTheDestinationAlone(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	// A leaf that is in the gateway table but absent from the source document.
	absentLeaf := ""
	for _, path := range cpa.ConfigLeafPaths() {
		if _, presence := cpa.LookupConfigValue(source.config, path); presence != cpa.ConfigPresent {
			absentLeaf = path
			break
		}
	}
	if absentLeaf == "" {
		t.Fatal("the fixture source sets every gateway leaf")
	}
	first := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	item := itemByName(t, first, configLeafItemName(absentLeaf))
	if item.Outcome != freshstore.AdoptionAbsentAtSource {
		t.Fatalf("an absent leaf must be recorded as absent_at_source: %+v", item)
	}

	// The operator sets it on the destination after the migration.
	parts := strings.Split(absentLeaf, ".")
	destination.mu.Lock()
	mergeInto(destination.config, parts, json.Number("300"))
	destination.mu.Unlock()
	before := destination.writeCount()

	replay := runMigration(t, source, destination, first)
	if destination.writeCount() != before {
		t.Fatalf("a replay wrote %d times over an absent-at-source item", destination.writeCount()-before)
	}
	if got := lookup(destination.config, parts); got != json.Number("300") {
		t.Fatalf("a replay overwrote the operator's value: %v", got)
	}
	if item, _ := replay.Item(configLeafItemName(absentLeaf)); item.Outcome != freshstore.AdoptionAbsentAtSource {
		t.Fatalf("a replay rewrote the absent record: %+v", item)
	}
}

// A failure that wrote nothing is retryable: the next run attempts it once and
// records the real outcome. A failure after a write stays settled.
func TestRetryableFailureIsRetriedOnceAndUnknownWriteIsNot(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)

	// Make one source download fail, which writes nothing.
	source.mu.Lock()
	source.failDownload = map[string]bool{"claude-a.json": true}
	source.mu.Unlock()

	first := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	failed := itemByName(t, first, credentialItemName("claude-a.json"))
	if failed.Outcome != freshstore.AdoptionFailed || failed.Reason != "download_failed" {
		t.Fatalf("a failed download must be a retryable failure: %+v", failed)
	}
	if first.Settled(credentialItemName("claude-a.json")) {
		t.Fatal("a failure that wrote nothing must not be settled")
	}
	if _, stored := destination.credential("claude-a.json"); stored {
		t.Fatal("a failed download must not write anything")
	}

	// The source answers again: the replay copies it and replaces the provisional
	// record in place.
	source.mu.Lock()
	source.failDownload = nil
	source.mu.Unlock()
	second := runMigration(t, source, destination, first)
	retried := itemByName(t, second, credentialItemName("claude-a.json"))
	if retried.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("a retryable failure was not retried: %+v", retried)
	}
	if len(second.Items) != len(first.Items) {
		t.Fatalf("the retry appended a new record instead of replacing one: %d vs %d", len(second.Items), len(first.Items))
	}
	if index := second.Index(credentialItemName("claude-a.json")); index != first.Index(credentialItemName("claude-a.json")) {
		t.Fatal("the retried item moved in the record")
	}

	// A failure after a write attempt is an unknown effect: it stays settled and
	// a replay neither retries nor rewrites it.
	unknown := freshstore.AdoptionSummary{Items: []freshstore.AdoptionItem{{
		Item: credentialItemName("codex-b.json"), Kind: freshstore.AdoptionKindCredential,
		Outcome: freshstore.AdoptionFailed, Reason: "apply_unknown",
	}}}
	if unknown.Settled(credentialItemName("codex-b.json")) != true {
		t.Fatal("a failure after a write must stay settled")
	}
	before := len(destination.uploadNames())
	replay := runMigration(t, source, destination, unknown)
	if after := len(destination.uploadNames()); after != before {
		t.Fatalf("a settled unknown write was resent %d times", after-before)
	}
	if item, _ := replay.Item(credentialItemName("codex-b.json")); item != unknown.Items[0] {
		t.Fatalf("a settled unknown write was rewritten: %+v", item)
	}
}

// A retryable failure is attempted once per run, never in a loop inside one run,
// and it keeps failing observably until the source answers.
func TestRetryableFailureIsAttemptedOncePerRun(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	source.mu.Lock()
	source.failDownload = map[string]bool{"claude-a.json": true}
	source.mu.Unlock()

	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	// The failing item was attempted exactly once in this run, not retried in
	// place, and the other credentials still migrated.
	if item := itemByName(t, summary, credentialItemName("claude-a.json")); item.Outcome != freshstore.AdoptionFailed || item.Reason != "download_failed" {
		t.Fatalf("the failing item: %+v", item)
	}
	if item := itemByName(t, summary, credentialItemName("codex-b.json")); item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("a retryable failure stopped the run: %+v", item)
	}
	if got := source.downloads("claude-a.json"); got != 1 {
		t.Fatalf("the failing item was downloaded %d times in one run", got)
	}
	// A second run attempts it once more, so a permanently failing item is
	// retried exactly once per run rather than never.
	second := runMigration(t, source, destination, summary)
	if got := source.downloads("claude-a.json"); got != 2 {
		t.Fatalf("a replay attempted the retryable item %d times in total", got)
	}
	if item, _ := second.Item(credentialItemName("claude-a.json")); item.Outcome != freshstore.AdoptionFailed {
		t.Fatalf("the retry did not record its own failure: %+v", item)
	}
}

// A failure after a write attempt is never retried by a later run: its only
// resolution is reading the destination back.
func TestUnknownWriteIsReconciledOnlyByReadBack(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	// An upload that stores the bytes and answers 500 leaves the effect unknown.
	destination.lostUploadBody = true
	first := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	// The read-back decides it as a successful copy, and it is recorded as such
	// rather than as an unknown write.
	if item := itemByName(t, first, credentialItemName("claude-a.json")); item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("a landed upload must be reconciled by read-back: %+v", item)
	}

	// A settled unknown write is never re-attempted, whatever the destination
	// holds later.
	unknown := freshstore.AdoptionSummary{Items: []freshstore.AdoptionItem{{
		Item: credentialItemName("claude-a.json"), Kind: freshstore.AdoptionKindCredential,
		Outcome: freshstore.AdoptionFailed, Reason: "apply_unknown",
	}}}
	before := len(destination.uploadNames())
	replay := runMigration(t, source, destination, unknown)
	if after := len(destination.uploadNames()); after != before {
		t.Fatalf("an unknown write was resent %d times", after-before)
	}
	if item, _ := replay.Item(credentialItemName("claude-a.json")); item != unknown.Items[0] {
		t.Fatalf("an unknown write was rewritten: %+v", item)
	}
}

// ManagerOwned also matches a parent section named as a whole: copying one whole
// section would carry the destination's own identity, listener, plugin root,
// auth directory or client key list with it.
func TestManagerOwnedMatchesParentSectionsNamedWhole(t *testing.T) {
	for _, parent := range []string{"management", "plugins", "oauth", "access", "server"} {
		if !ManagerOwned(parent) {
			t.Fatalf("parent section %q must be Manager-owned", parent)
		}
	}
	// A subtree that is wholly owned stays excluded under any child name, because
	// every leaf of it belongs to the destination.
	for _, path := range []string{"management.secret-key", "management.anything", "plugins.anything"} {
		if !ManagerOwned(path) {
			t.Fatalf("%q must be Manager-owned", path)
		}
	}
	// The destination's client key list stays owned under any spelling.
	if !ManagerOwned("access.api-keys.vendor") {
		t.Fatal("a path below access.api-keys must stay owned")
	}
	// The provider sections this run copies live in the same parents, and naming
	// the parent must not swallow them: `oauth` as a whole is owned, but its
	// copyable children are named explicitly and are not.
	for _, path := range []string{"oauth.model-alias", "oauth.settings", "access.other"} {
		if ManagerOwned(path) {
			t.Fatalf("%q is copyable and must not be owned", path)
		}
	}
	// A section that only looks similar is not Manager-owned.
	for _, path := range []string{"routing", "requests", "multimedia", "upstream", "client", "api-keys.claude", "oauth.model-alias"} {
		if ManagerOwned(path) {
			t.Fatalf("%q is copyable and must not be Manager-owned", path)
		}
	}
}

// The disable PATCH is read back like every other write: a destination that does
// not report the credential disabled is manual work, and one whose read-back
// fails is reported as unverified rather than as "stays enabled".
func TestDisableStateIsReadBack(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	// This destination accepts the PATCH and ignores it.
	destination.mu.Lock()
	destination.ignoreStatusPatch = true
	destination.mu.Unlock()

	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	item := itemByName(t, summary, credentialItemName("codex-b.json"))
	if item.Outcome != freshstore.AdoptionManual || item.Reason != "status_write_failed" || item.Instruction == "" {
		t.Fatalf("a disable the destination did not apply must be manual work: %+v", item)
	}
	// The copy itself is still recorded as the successful copy it was, with both
	// observed digests.
	if item.SourceSHA256 == "" || item.DestinationSHA256 != item.SourceSHA256 {
		t.Fatalf("the copied bytes must still be recorded: %+v", item)
	}
}

// A destination whose status read-back fails reports the unknown outcome
// honestly instead of claiming the credential stays enabled.
func TestUnreadableDisableStateIsReportedAsUnverified(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	// The listings that decide the copy succeed; the one that would confirm the
	// disable does not.
	destination.mu.Lock()
	destination.failListAfterPatch = true
	destination.mu.Unlock()

	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	item := itemByName(t, summary, credentialItemName("codex-b.json"))
	if item.Outcome != freshstore.AdoptionManual || item.Reason != "status_unverified" {
		t.Fatalf("an unreadable disable state must be recorded as unverified: %+v", item)
	}
}

// The recorded destination digest is what the destination actually holds, not a
// copy of the source digest: a destination that stores different bytes is a
// mismatch, never a claimed success.
func TestDestinationDigestIsObservedFromTheReadBack(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	for _, name := range []string{"claude-a.json", "codex-b.json", "gemini-c.json"} {
		item := itemByName(t, summary, credentialItemName(name))
		readBack, ok := destination.credential(name)
		if !ok {
			t.Fatalf("the destination does not hold %s", name)
		}
		if item.DestinationSHA256 != digestSHA256(readBack) {
			t.Fatalf("%s: recorded destination digest is not the observed one", name)
		}
	}
	// A whole section records the digest of the value read back, not of the body
	// it sent.
	section := itemByName(t, summary, providerSectionItemName("api-keys.claude"))
	observed := lookup(destination.config, []string{"api-keys", "claude"})
	if section.DestinationSHA256 != digestOfValue(observed) {
		t.Fatalf("the recorded section digest is not the observed value: %s vs %s", section.DestinationSHA256, digestOfValue(observed))
	}
}

// The executor copies what the record authorized and nothing else. The record
// here marks credentials and gateway configuration migrate and leaves the
// provider sections at their manual action, so the provider sections that are
// present at the source must not be written and must be recorded as the manual
// work the decision names.
func TestExecutorCopiesOnlyWhatTheRecordMarksMigrate(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	decisions := make([]bootstrap.AdoptionResourceDecision, 0, len(migratingDecisions()))
	for _, decision := range migratingDecisions() {
		if decision.ResourceRef == bootstrap.AdoptionRefProviderStateAPIKeys {
			continue
		}
		decisions = append(decisions, decision)
	}

	sourceWire, err := NewSource(t.Context(), source.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	destinationWire, err := NewDestination(t.Context(), destination.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(sourceWire, destinationWire, decisions).Run(context.Background(), freshstore.AdoptionSummary{}, nil)
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	summary := result.Summary

	// Every item of a migrate reference is written under its reference, and
	// every item of a manual reference is recorded as that manual action.
	for _, item := range summary.Items {
		ref := ItemRef(item)
		migrate := false
		for _, decision := range decisions {
			migrate = migrate || decision.ResourceRef == ref && decision.Disposition == bootstrap.Migrate
		}
		if migrate {
			continue
		}
		if item.Outcome != freshstore.AdoptionManual || item.Reason != string(bootstrap.ReauthenticateAfterAdoption) || item.Instruction == "" {
			t.Fatalf("%s (%s) is not authorized by the record but was not recorded as manual work: %+v", item.Item, ref, item)
		}
	}

	// The provider sections really are present at the source, so the read above
	// is a decision not to write them rather than an absence.
	for _, section := range []string{"api-keys.claude", "oauth.model-alias"} {
		if _, presence := cpa.LookupConfigValue(source.config, section); presence != cpa.ConfigPresent {
			t.Fatalf("the fixture source does not carry %s", section)
		}
		item := itemByName(t, summary, providerSectionItemName(section))
		if item.Outcome != freshstore.AdoptionManual {
			t.Fatalf("section %s: %+v", section, item)
		}
	}
	for _, entry := range destination.requestMethods() {
		if strings.HasPrefix(entry, http.MethodGet+" ") {
			continue
		}
		if strings.Contains(entry, "/config/api-keys") || strings.Contains(entry, "/config/oauth/") {
			t.Fatalf("the executor wrote a manual provider section: %s", entry)
		}
	}
	if lookup(destination.config, []string{"oauth", "model-alias"}) != nil {
		t.Fatal("a manual provider section reached the destination")
	}
	// The authorized kinds were still copied: the gate is per reference, not a
	// whole-run switch.
	if item := itemByName(t, summary, credentialItemName("claude-a.json")); item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("an authorized credential did not migrate: %+v", item)
	}
	if item := itemByName(t, summary, configLeafItemName("routing.strategy")); item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("an authorized leaf did not migrate: %+v", item)
	}
	source.usesOnlyReads(t)
}

// A provider section is gated by its own root's decision, never by another
// root's. The record here marks `provider-state-api-keys` migrate and leaves
// `provider-state-client` and `provider-state-providers` at their manual action,
// so the `api-keys.*` and `oauth.*` sections are copied while a `client` or
// `providers` section is refused as the manual work its own decision names.
func TestProviderSectionsAreGatedByTheirOwnRoot(t *testing.T) {
	apiKeysMigrate := replaceDecision(migratingDecisions(), bootstrap.AdoptionRefProviderStateClient, manualReauthentication)
	apiKeysMigrate = replaceDecision(apiKeysMigrate, bootstrap.AdoptionRefProviderStateProviders, manualReauthentication)

	source, destination := sourceFixture(t), destinationFixture(t)
	sourceWire, err := NewSource(t.Context(), source.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	destinationWire, err := NewDestination(t.Context(), destination.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(sourceWire, destinationWire, apiKeysMigrate).Run(context.Background(), freshstore.AdoptionSummary{}, nil)
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	summary := result.Summary
	// Every copyable section belongs to the api-keys decision: the ones the
	// source carries are copied, and the ones it does not carry are recorded as
	// absent rather than refused.
	for _, section := range ProviderSections() {
		want, presence := cpa.LookupConfigValue(source.config, section)
		item := itemByName(t, summary, providerSectionItemName(section))
		if presence != cpa.ConfigPresent {
			if item.Outcome != freshstore.AdoptionAbsentAtSource {
				t.Fatalf("absent section %s recorded as %+v", section, item)
			}
			continue
		}
		if item.Outcome != freshstore.AdoptionMigrated {
			t.Fatalf("an authorized provider section did not migrate: %+v", item)
		}
		if got, seen := cpa.LookupConfigValue(destination.config, section); seen != cpa.ConfigPresent || !equalConfigValue(want, got) {
			t.Fatalf("%s did not reach the destination intact: %+v %v", section, got, seen)
		}
	}
	source.usesOnlyReads(t)

	// The gate itself: the record authorizes the provider-copy reference and
	// nothing else, so a section the plan does not contain is refused rather than
	// borrowing that verdict.
	executor := New(nil, nil, apiKeysMigrate)
	for _, section := range []string{"providers", "made-up", "client"} {
		if _, gated := executor.gated(providerSection(section), section); !gated {
			t.Fatalf("section %q is not in the copy plan but was not gated", section)
		}
	}
	for _, section := range []string{"api-keys.claude", "oauth.model-alias"} {
		if _, gated := executor.gated(providerSection(section), section); gated {
			t.Fatalf("authorized section %s was gated", section)
		}
	}
	// Only the twelve planned sections classify as provider state; a bare root
	// name, and a section under a root the plan does not name, are configuration
	// the executor does not copy, so they classify as unknown configuration and
	// keep their own manual action.
	for _, section := range ProviderSections() {
		if ref := ItemRef(providerSection(section)); ref != bootstrap.AdoptionRefProviderStateAPIKeys {
			t.Fatalf("planned section %s classified as %s", section, ref)
		}
	}
	for _, section := range []string{"client", "providers", "oauth", "api-keys", "made-up", "made-up.child"} {
		if ref := ItemRef(providerSection(section)); ref != bootstrap.AdoptionRefConfigUnknown {
			t.Fatalf("unplanned section %q classified as %s", section, ref)
		}
	}
	// Even a forged migrate claim for the client reference cannot authorize a
	// section: the rules keep that reference manual, and no section carries it.
	forged := replaceDecision(apiKeysMigrate, bootstrap.AdoptionRefProviderStateClient, bootstrap.AdoptionResourceDecision{
		ResourceRef: bootstrap.AdoptionRefProviderStateClient, Qualification: bootstrap.CapabilityQualified,
		Disposition: bootstrap.Migrate,
	})
	if _, gated := New(nil, nil, forged).gated(providerSection("client"), "client"); !gated {
		t.Fatal("a forged migrate claim authorized a client section")
	}
	item, gated := New(nil, nil, apiKeysMigrate).gated(providerSection("providers"), "providers")
	if !gated || item.Outcome != freshstore.AdoptionManual || item.Reason == "" || item.Instruction == "" {
		t.Fatalf("an unplanned section was not recorded as manual work: %+v", item)
	}
}

// The record a real v8 adoption carries must cover every item the executor can
// copy out of the roots that source reports, and each item's outcome must follow
// its own reference's decision. This is the production shape: discovery records a
// decision per root contribution, the executor classifies each item on its own
// reference, and the two must agree item by item.
//
// The roots are the ones the controlled real-CPA source of
// `adoption_cpa_test.go` reports: `server`, `oauth`, `management`, `access`,
// `plugins`, `routing`, `client`, `api-keys`, and the reader's unknown catch-all
// for `requests`, `multimedia` and `upstream`.
func TestDiscoveredDecisionsCoverEveryItemTheExecutorClassifies(t *testing.T) {
	roots := []string{"server", "management", "access", "oauth", "api-keys", "routing", "client", "plugins", "unknown_cpa_native_resource"}
	refs := make([]bootstrap.ResourceRef, 0, len(roots))
	for _, root := range roots {
		for _, ref := range cpa.AdoptionRootRefs(root) {
			if !slices.Contains(refs, ref) {
				refs = append(refs, ref)
			}
		}
	}
	// The decisions are exactly what discovery writes: one per reference the
	// reported roots contribute, migrate only for a reference the rules permit.
	migrating := migratingDecisions()
	decisions := make([]bootstrap.AdoptionResourceDecision, 0, len(refs))
	for _, ref := range refs {
		candidate := bootstrap.AdoptionResourceDecision{}
		for _, decision := range migrating {
			if decision.ResourceRef == ref {
				candidate = decision
			}
		}
		decision, ok := bootstrap.FreshAdoptionDecision(ref, candidate)
		if !ok {
			t.Fatalf("no decision for discovered reference %s", ref)
		}
		decisions = append(decisions, decision)
	}

	source, destination := sourceFixture(t), destinationFixture(t)
	sourceWire, err := NewSource(t.Context(), source.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	destinationWire, err := NewDestination(t.Context(), destination.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(sourceWire, destinationWire, decisions).Run(context.Background(), freshstore.AdoptionSummary{}, nil)
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}

	authorized := map[bootstrap.ResourceRef]bootstrap.AdoptionResourceDecision{}
	for _, decision := range decisions {
		authorized[decision.ResourceRef] = decision
	}
	// Every item the executor recorded is gated under a reference discovery
	// recorded, and its outcome follows that reference's disposition.
	for _, item := range result.Summary.Items {
		ref := ItemRef(item)
		decision, recorded := authorized[ref]
		if !recorded {
			t.Fatalf("%s (%s) is gated under a reference discovery never recorded: %v", item.Item, ref, refs)
		}
		// For an item of the enumerable universe, the reference must be one the
		// item's OWN root contributes, not merely one some root happened to
		// contribute: a union check passes while a root's set is wrong.
		if path, enumerable := enumerableItemPath(item); enumerable {
			root := cpa.AdoptionRootOf(path)
			if slices.Contains(roots, root) && !slices.Contains(cpa.AdoptionRootRefs(root), ref) {
				t.Fatalf("%s is gated under %s but root %q contributes %v", item.Item, ref, root, cpa.AdoptionRootRefs(root))
			}
		}
		if decision.Disposition == bootstrap.Migrate {
			// Authorized does not promise copied: an item CPA itself would reject
			// (an unparseable credential file) or one the Manager owns is still
			// manual work. What it must never be is refused for lack of
			// authorization.
			if item.Outcome == freshstore.AdoptionManual && item.Reason == string(decision.ManualAction) {
				t.Fatalf("%s (%s) is authorized but was recorded as unauthorized manual work: %+v", item.Item, ref, item)
			}
			continue
		}
		if item.Outcome != freshstore.AdoptionManual || item.Reason != string(decision.ManualAction) || item.Instruction == "" {
			t.Fatalf("%s (%s) is not authorized but was not recorded as the decision's manual work: %+v", item.Item, ref, item)
		}
	}
	// Nothing the record calls manual was written: the destination holds none of
	// the source's values for those paths.
	for _, item := range result.Summary.Items {
		if item.Outcome != freshstore.AdoptionManual {
			continue
		}
		path := strings.TrimPrefix(item.Item, "config:")
		if path == item.Item {
			continue
		}
		if value, presence := cpa.LookupConfigValue(destination.config, path); presence == cpa.ConfigPresent &&
			equalConfigValue(lookup(source.config, strings.Split(path, ".")), value) {
			t.Fatalf("manual path %s reached the destination", path)
		}
	}
	source.usesOnlyReads(t)
}

// enumerableItemPath returns the adoption path one recorded item corresponds to,
// when that path is part of the enumerable universe (a gateway leaf, a rule
// family or a copyable section). Credential items and the excess marker are not
// configuration paths and have no root.
func enumerableItemPath(item freshstore.AdoptionItem) (string, bool) {
	var path string
	switch item.Kind {
	case freshstore.AdoptionKindConfigLeaf:
		path, _ = strings.CutPrefix(item.Item, "config:")
	case freshstore.AdoptionKindConfigFamily:
		family, ok := strings.CutPrefix(item.Item, "rules:")
		if !ok {
			return "", false
		}
		entry, ok := cpa.LookupConfigRuleFamily(family)
		if !ok {
			return "", false
		}
		path = entry.Path
	case freshstore.AdoptionKindProviderSection:
		path, _ = strings.CutPrefix(item.Item, "section:")
	default:
		return "", false
	}
	return path, slices.Contains(cpa.AdoptionItemUniverse(), path)
}

// providerSection is the executor's own item shape for one whole section.
func providerSection(section string) freshstore.AdoptionItem {
	return freshstore.AdoptionItem{Item: providerSectionItemName(section), Kind: freshstore.AdoptionKindProviderSection}
}

// manualReauthentication is the manual decision the adoption rules permit for a
// provider-state reference: built by the rules themselves, so it is exactly the
// shape `gated` accepts.
var manualReauthentication = func() bootstrap.AdoptionResourceDecision {
	ref := bootstrap.AdoptionRefProviderStateClient
	decision, ok := bootstrap.FreshAdoptionDecision(ref, bootstrap.AdoptionResourceDecision{
		ResourceRef: ref, Qualification: bootstrap.CapabilityPending, ManualAction: bootstrap.ReauthenticateAfterAdoption,
	})
	if !ok {
		panic("no manual decision for " + string(ref))
	}
	return decision
}()

// replaceDecision returns a copy of decisions with ref's decision replaced.
func replaceDecision(decisions []bootstrap.AdoptionResourceDecision, ref bootstrap.ResourceRef, replacement bootstrap.AdoptionResourceDecision) []bootstrap.AdoptionResourceDecision {
	replaced := slices.Clone(decisions)
	for i := range replaced {
		if replaced[i].ResourceRef == ref {
			replaced[i] = replacement
		}
	}
	return replaced
}

// Every writable gateway leaf, payload rule family and credential is routing or
// credentials; anything outside the accepted gateway table keeps the reference
// that describes it and therefore stays manual.
func TestItemRefClassifiesAgainstTheGatewayTable(t *testing.T) {
	for _, path := range []string{"routing.strategy", "requests.proxy-url", "requests.streaming.keepalive-seconds",
		"multimedia.disable-image-generation", "upstream.codex.response-steering", "upstream.claude.header-defaults.timezone",
		"upstream.xai.inject-x-search", "client.codex.enable-apply-patch", "client.codex.optimize-multi-agent-v2"} {
		if ref := ItemRef(freshstore.AdoptionItem{Item: configLeafItemName(path), Kind: freshstore.AdoptionKindConfigLeaf}); ref != bootstrap.AdoptionRefConfigRouting {
			t.Fatalf("gateway leaf %s classified as %s", path, ref)
		}
	}
	for _, family := range cpa.ConfigRuleFamilyIDs() {
		if ref := ItemRef(freshstore.AdoptionItem{Item: configFamilyItemName(family), Kind: freshstore.AdoptionKindConfigFamily}); ref != bootstrap.AdoptionRefConfigRouting {
			t.Fatalf("rule family %s classified as %s", family, ref)
		}
	}
	for _, path := range []string{"oauth.auth-dir", "server.host", "observability.logs.debug", "plugins.dir", "not.a.path"} {
		if ref := ItemRef(freshstore.AdoptionItem{Item: configLeafItemName(path), Kind: freshstore.AdoptionKindConfigLeaf}); ref != bootstrap.AdoptionRefConfigUnknown {
			t.Fatalf("non-gateway leaf %s classified as %s", path, ref)
		}
	}
	if ref := ItemRef(freshstore.AdoptionItem{Item: configFamilyItemName("not-a-family"), Kind: freshstore.AdoptionKindConfigFamily}); ref != bootstrap.AdoptionRefConfigUnknown {
		t.Fatalf("non-gateway family classified as %s", ref)
	}
	if ref := ItemRef(freshstore.AdoptionItem{Item: credentialItemName("claude-a.json"), Kind: freshstore.AdoptionKindCredential}); ref != bootstrap.AdoptionRefCPAAuthFiles {
		t.Fatalf("credential classified as %s", ref)
	}
	// A provider section is gated under its own root, never under another
	// A section the copy plan does not contain is configuration the executor
	// never writes, so it classifies as unknown configuration and stays manual.
	// It never borrows a root's verdict: `providers` is a section whose contents
	// the plan does not copy, and `client` is gateway leaves only.
	for section, want := range map[string]bootstrap.ResourceRef{
		"api-keys.claude":   bootstrap.AdoptionRefProviderStateAPIKeys,
		"api-keys.vertex":   bootstrap.AdoptionRefProviderStateAPIKeys,
		"oauth.model-alias": bootstrap.AdoptionRefProviderStateAPIKeys,
		"oauth.settings":    bootstrap.AdoptionRefProviderStateAPIKeys,
		"providers":         bootstrap.AdoptionRefConfigUnknown,
		"client":            bootstrap.AdoptionRefConfigUnknown,
		"oauth":             bootstrap.AdoptionRefConfigUnknown,
		"plugins.enabled":   bootstrap.AdoptionRefConfigUnknown,
		"made-up":           bootstrap.AdoptionRefConfigUnknown,
		"made-up.child":     bootstrap.AdoptionRefConfigUnknown,
	} {
		ref := ItemRef(freshstore.AdoptionItem{Item: providerSectionItemName(section), Kind: freshstore.AdoptionKindProviderSection})
		if ref != want {
			t.Fatalf("provider section %s classified as %s, want %s", section, ref, want)
		}
	}
	// The keys a section is copied under are the ones the executor actually
	// enumerates: every provider section of the copy plan resolves to a root
	// reference, and none of them lands on the fallback.
	for _, section := range ProviderSections() {
		ref := ItemRef(freshstore.AdoptionItem{Item: providerSectionItemName(section), Kind: freshstore.AdoptionKindProviderSection})
		if ref != bootstrap.AdoptionRefProviderStateAPIKeys {
			t.Fatalf("copyable section %s classified as %s", section, ref)
		}
	}
}

// Reading the destination's state of one credential costs one entry, not the
// whole inventory: the route is narrowed by `?name=`.
func TestCredentialStateReadsOnlyTheOneCredential(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	runMigration(t, source, destination, freshstore.AdoptionSummary{})

	names := destination.listNames()
	held := destination.heldNames()
	if len(held) == 0 {
		t.Fatal("the fixture destination holds no credential")
	}
	if len(names) == 0 {
		t.Fatal("the run read no credential state at all")
	}
	for _, name := range names {
		if name == "" {
			t.Fatalf("a credential state read listed the whole inventory: %v", names)
		}
	}
	for _, name := range held {
		if !slices.Contains(names, name) {
			t.Fatalf("no per-name read of %s: %v", name, names)
		}
	}
}
