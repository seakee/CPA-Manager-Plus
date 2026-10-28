package freshadopt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

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
	// refuseUpload rejects these file names with CPA's own 400.
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
	server         *httptest.Server
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
		c.listCredentials(w)
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

func (c *fakeCPA) listCredentials(w http.ResponseWriter) {
	c.mu.Lock()
	files := make([]map[string]any, 0, len(c.credentials))
	for name := range c.credentials {
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
	data, ok := c.credentials[name]
	c.mu.Unlock()
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
	refusal, refused := c.refuseUpload[name]
	lost := c.lostUploadBody
	c.mu.Unlock()
	if refused {
		w.WriteHeader(http.StatusBadRequest)
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
	c.disabled[request.Name] = request.Disabled
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
			"strategy":        "round-robin",
			"force-model-prefix": true,
			"retry":           map[string]any{"request-retry": json.Number("4"), "max-retry-credentials": json.Number("3")},
			"cooldown":        map[string]any{"disable-cooling": true},
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
				"keys": []any{map[string]any{"api-key": "src-provider-compat-key"}},
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
	sourceWire, err := NewSource(source.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	destinationWire, err := NewDestination(destination.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(sourceWire, destinationWire).Run(context.Background(), previous)
	if err != nil {
		t.Fatalf("migration failed: %v", err)
	}
	return result.Summary
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
	// Three plain credentials are uploaded once each, and the refused and
	// runtime-only ones are not uploaded at all: no item is ever sent twice.
	if uploads != 4 {
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
