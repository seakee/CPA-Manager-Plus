package freshadopt

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"gopkg.in/yaml.v3"
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
	// customWriteErrors makes one dotted path fail with a custom status + upstream
	// JSON error string so tests can verify the closed reason allowlist.
	customWriteErrors map[string]struct {
		status int
		code   string
	}
	// validateLiveMediaRelay enforces upstream CPA's codex_live.go Validate()
	// cross-field rule on every PATCH/PUT before committing.
	validateLiveMediaRelay bool
	// letting a test simulate a divergent or malformed read-back after PUT.
	readOverrides map[string]any
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
	listCalls              int
	listFilters            []string
	transformOnUpload      func(name string, raw []byte) []byte
	transformOnStatusPatch func(name string, current []byte, disabled bool) []byte
	statusPatchCount       int
	failConfigYAML         bool
	hangConfigYAML         bool
	configYAMLOverride     string
	server                 *httptest.Server
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
		writeFailures:     map[string]int{},
		customWriteErrors: map[string]struct{ status int; code string }{},
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
	case path == "/v8/management/config.yaml" && r.Method == http.MethodGet:
		c.mu.Lock()
		failYAML := c.failConfigYAML
		hangYAML := c.hangConfigYAML
		override := c.configYAMLOverride
		cfgCopy := cloneFakeConfig(c.config)
		c.mu.Unlock()
		if hangYAML {
			<-r.Context().Done()
			return
		}
		if failYAML {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"yaml_unavailable"}`))
			return
		}
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		if override != "" {
			_, _ = w.Write([]byte(override))
			return
		}
		body, err := yaml.Marshal(cfgCopy)
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(body)
	case path == "/v8/management/config" && r.Method == http.MethodGet:
		c.mu.Lock()
		redactedCfg := redactFakeTURNSecrets(cloneFakeConfig(c.config))
		body, err := json.Marshal(redactedCfg)
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
	c.mu.Lock()
	storedBytes := body
	if c.transformOnUpload != nil {
		storedBytes = c.transformOnUpload(name, body)
	}
	var stored struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(storedBytes, &stored)
	c.credentials[name] = storedBytes
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
	c.statusPatchCount++
	if !c.ignoreStatusPatch {
		c.disabled[request.Name] = request.Disabled
		if c.transformOnStatusPatch != nil {
			c.credentials[request.Name] = c.transformOnStatusPatch(request.Name, c.credentials[request.Name], request.Disabled)
		}
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
	for _, leaf := range append(leafPaths(dotted, body), dotted) {
		if custom, ok := c.customWriteErrors[leaf]; ok {
			c.mu.Unlock()
			w.WriteHeader(custom.status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": custom.code, "message": "upstream details"})
			return
		}
	}
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
	if strings.HasPrefix(dotted, "api-keys.") {
		value = stripFakeAPIKeyAuthIndexes(value)
	}
	value = cloneFakeValue(value)
	c.mu.Lock()
	if dotted == "credentials.concurrency" {
		incomingMap, _ := value.(map[string]any)
		existingMap, _ := lookup(c.config, []string{"credentials", "concurrency"}).(map[string]any)
		for _, revKey := range []string{"lifecycle-config-revision", "observation-barrier-revision"} {
			var oldRev, newRev any
			if existingMap != nil {
				oldRev = existingMap[revKey]
			}
			if incomingMap != nil {
				newRev = incomingMap[revKey]
			}
			if !reflect.DeepEqual(oldRev, newRev) {
				c.mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": "read_only_field", "field": "credentials/concurrency/" + revKey})
				return
			}
		}
	}
	candidate := cloneFakeValue(c.config).(map[string]any)
	if r.Method == http.MethodPut {
		replaceAtPath(candidate, strings.Split(dotted, "."), value)
	} else {
		mergeInto(candidate, strings.Split(dotted, "."), value)
	}
	if c.validateLiveMediaRelay {
		if err := validateFakeLiveMediaRelay(candidate); err != nil {
			c.mu.Unlock()
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_config", "message": err.Error()})
			return
		}
	}
	c.config = candidate
	c.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "config-version": 8})
}

func (c *fakeCPA) readConfig(w http.ResponseWriter, slashPath string) {
	dotted := strings.ReplaceAll(strings.Trim(slashPath, "/"), "/", ".")
	value := c.projectedValue(dotted)
	if value == nil {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": "not_found"})
		return
	}
	_ = json.NewEncoder(w).Encode(value)
}

func (c *fakeCPA) projectedValue(dotted string) any {
	c.mu.Lock()
	if c.readOverrides != nil {
		if override, exists := c.readOverrides[dotted]; exists {
			c.mu.Unlock()
			return override
		}
	}
	value := lookup(c.config, strings.Split(dotted, "."))
	if value != nil {
		value = cloneFakeValue(value)
	}
	if dotted == iceServersSectionPath && value != nil {
		value = redactFakeICEServersSlice(value)
	}
	if strings.HasPrefix(dotted, "api-keys.") && value != nil {
		value = injectFakeAPIKeyAuthIndexes(dotted, value)
	}
	c.mu.Unlock()
	return value
}

func fakeAuthIndex(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:8])
}

// stripFakeAPIKeyAuthIndexes mirrors CPA's stripAPIKeysAuthIndexesFromUpdate:
// PUT /v8/management/config/api-keys/* never persists auth_index/auth-index.
func stripFakeAPIKeyAuthIndexes(value any) any {
	groups, ok := value.([]any)
	if !ok {
		return value
	}
	out := make([]any, 0, len(groups))
	for _, rawGroup := range groups {
		group, ok := rawGroup.(map[string]any)
		if !ok {
			out = append(out, rawGroup)
			continue
		}
		cleanGroup := make(map[string]any, len(group))
		for k, v := range group {
			if k == "auth_index" || k == "auth-index" {
				continue
			}
			cleanGroup[k] = v
		}
		if rawKeys, ok := cleanGroup["keys"].([]any); ok {
			cleanKeys := make([]any, 0, len(rawKeys))
			for _, rawEntry := range rawKeys {
				entry, ok := rawEntry.(map[string]any)
				if !ok {
					cleanKeys = append(cleanKeys, rawEntry)
					continue
				}
				cleanEntry := make(map[string]any, len(entry))
				for k, v := range entry {
					if k == "auth_index" || k == "auth-index" {
						continue
					}
					cleanEntry[k] = v
				}
				cleanKeys = append(cleanKeys, cleanEntry)
			}
			cleanGroup["keys"] = cleanKeys
		}
		out = append(out, cleanGroup)
	}
	return out
}

// injectFakeAPIKeyAuthIndexes mirrors CPA v8.0.55's
// injectV8APIKeyAuthIndexesLocked: GET /v8/management/config/api-keys/* adds a
// 16-hex auth_index to each key entry, or to the group itself for a keyless
// openai-compatibility group with a base-url.
func injectFakeAPIKeyAuthIndexes(dotted string, value any) any {
	groups, ok := value.([]any)
	if !ok {
		return value
	}
	out := make([]any, 0, len(groups))
	for groupIdx, rawGroup := range groups {
		group, ok := rawGroup.(map[string]any)
		if !ok {
			out = append(out, rawGroup)
			continue
		}
		projectedGroup := make(map[string]any, len(group)+1)
		for k, v := range group {
			projectedGroup[k] = v
		}
		rawKeys, hasKeys := group["keys"]
		keys, keysSlice := rawKeys.([]any)
		if dotted == "api-keys.openai-compatibility" && (!hasKeys || (keysSlice && len(keys) == 0)) {
			if baseURL, _ := group["base-url"].(string); strings.TrimSpace(baseURL) != "" {
				projectedGroup["auth_index"] = fakeAuthIndex(fmt.Sprintf("%s#%d#%s", dotted, groupIdx, baseURL))
			}
			out = append(out, projectedGroup)
			continue
		}
		if keysSlice {
			projectedKeys := make([]any, 0, len(keys))
			for keyIdx, rawEntry := range keys {
				entry, ok := rawEntry.(map[string]any)
				if !ok {
					projectedKeys = append(projectedKeys, rawEntry)
					continue
				}
				projectedEntry := make(map[string]any, len(entry)+1)
				for k, v := range entry {
					projectedEntry[k] = v
				}
				apiKey, _ := entry["api-key"].(string)
				projectedEntry["auth_index"] = fakeAuthIndex(fmt.Sprintf("%s#%d#%d#%s", dotted, groupIdx, keyIdx, apiKey))
				projectedKeys = append(projectedKeys, projectedEntry)
			}
			projectedGroup["keys"] = projectedKeys
		}
		out = append(out, projectedGroup)
	}
	return out
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

func replaceAtPath(document map[string]any, parts []string, value any) {
	cursor := document
	for _, part := range parts[:len(parts)-1] {
		next, ok := cursor[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			cursor[part] = next
		}
		cursor = next
	}
	cursor[parts[len(parts)-1]] = value
}

func cloneFakeConfig(cfg map[string]any) map[string]any {
	if cfg == nil {
		return nil
	}
	out, _ := cloneFakeValue(cfg).(map[string]any)
	return out
}

func cloneFakeValue(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for k, item := range typed {
			out[k] = cloneFakeValue(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneFakeValue(item)
		}
		return out
	case json.Number:
		if n, err := typed.Int64(); err == nil {
			return int(n)
		}
		return typed
	default:
		return typed
	}
}

func redactFakeTURNSecrets(cfg map[string]any) map[string]any {
	if cfg == nil {
		return nil
	}
	if oauth, ok := cfg["oauth"].(map[string]any); ok {
		if providers, ok := oauth["providers"].(map[string]any); ok {
			if codex, ok := providers["codex"].(map[string]any); ok {
				if relay, ok := codex["live-media-relay"].(map[string]any); ok {
					if servers, ok := relay["ice-servers"]; ok {
						relay["ice-servers"] = redactFakeICEServersSlice(servers)
					}
				}
			}
		}
	}
	return cfg
}

func redactFakeICEServersSlice(v any) any {
	slice, ok := v.([]any)
	if !ok {
		return v
	}
	out := make([]any, len(slice))
	for i, raw := range slice {
		entry, ok := raw.(map[string]any)
		if !ok {
			out[i] = raw
			continue
		}
		clean := make(map[string]any, len(entry))
		for k, val := range entry {
			if k == "username" || k == "credential" {
				continue
			}
			clean[k] = val
		}
		out[i] = clean
	}
	return out
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

// `Planned` is the plan, not the prefix a run reached. A run that stops at a
// refused checkpoint has settled fewer items than it planned and has not reached
// the end of its list, so it leaves the run-finished marker unset — otherwise
// completion would accept a migration that copied half the inventory.
func TestStoppedRunLeavesThePlanUnfinished(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	sourceWire, err := NewSource(t.Context(), source.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	destinationWire, err := NewDestination(t.Context(), destination.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	errCheckpoint := errors.New("checkpoint refused")
	calls, planned := 0, 0
	result, runErr := New(sourceWire, destinationWire, migratingDecisions()).Run(context.Background(), freshstore.AdoptionSummary{}, func(current freshstore.AdoptionSummary) error {
		calls++
		if calls > 1 && current.Planned != planned {
			t.Fatalf("the plan changed during the run: %d → %d", planned, current.Planned)
		}
		planned = current.Planned
		if calls == 3 {
			return errCheckpoint
		}
		return nil
	})
	if !errors.Is(runErr, errCheckpoint) {
		t.Fatalf("a failed checkpoint must stop the run: %v", runErr)
	}
	stopped := result.Summary
	if stopped.Finished {
		t.Fatal("a run that stopped mid-plan recorded the run-finished marker")
	}
	if stopped.Planned <= len(stopped.Items) {
		t.Fatalf("planned %d items but the stop left the list at %d", stopped.Planned, len(stopped.Items))
	}
	// The plan is the whole list: the run that finishes it records exactly the
	// items the stopped run planned, and marks itself finished.
	completed := runMigration(t, source, destination, stopped)
	if !completed.Finished || completed.Planned != len(completed.Items) || completed.Planned != stopped.Planned {
		t.Fatalf("the finished run does not match its plan: finished=%v planned=%d items=%d stoppedPlan=%d",
			completed.Finished, completed.Planned, len(completed.Items), stopped.Planned)
	}
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
	// The marker carries how many files it stands for, so the operator is told
	// how much work is left instead of "some files".
	omitted := 0
	for name := range source.credentials {
		if summary.Index("credential:"+name) < 0 {
			omitted++
		}
	}
	if omitted == 0 || excess.Left != omitted {
		t.Fatalf("the excess marker reports %d left behind, the record omits %d", excess.Left, omitted)
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

// capacitySource is a source whose whole inventory is exactly the names the
// caller gives it, on an empty configuration. A test for the record's capacity
// has to know the full inventory, which the happy-path fixture's handful of
// credentials would hide.
func capacitySource(t *testing.T, names []string) *fakeCPA {
	t.Helper()
	source := newFakeCPA(t, "source-management-key")
	for index, name := range names {
		source.seedAuthFile(name, "claude", []byte(fmt.Sprintf(`{"type":"claude","access_token":"capacity-token-%d"}`, index)), false)
	}
	return source
}

// ordinaryCredentialName is one name the record can hold directly, sorted before
// every overlong name this file builds.
func ordinaryCredentialName(index int) string { return fmt.Sprintf("bulk-%04d.json", index) }

// overlongCredentialName is one name the store cannot hold as an item identity.
// The index keeps both the kept prefix and the whole-name digest distinct.
func overlongCredentialName(index int) string {
	return fmt.Sprintf("zzz-%04d-%s.json", index, strings.Repeat("x", freshstore.MaxAdoptionItemName()))
}

func ordinaryCredentialNames(count int) []string {
	names := make([]string, 0, count)
	for index := range count {
		names = append(names, ordinaryCredentialName(index))
	}
	return names
}

func overlongCredentialNames(count int) []string {
	names := make([]string, 0, count)
	for index := range count {
		names = append(names, overlongCredentialName(index))
	}
	return names
}

// assertCapacityRecord proves the invariants one over-capacity preflight must
// keep: a record the store accepts and keeps within its item bound, every fixed
// configuration item present, and exactly one aggregate marker whose count is
// the true number of source credentials the record does not hold.
func assertCapacityRecord(t *testing.T, source, destination *fakeCPA, summary freshstore.AdoptionSummary) freshstore.AdoptionItem {
	t.Helper()
	if err := freshstore.ValidateAdoptionSummary(summary); err != nil {
		t.Fatalf("the preflight produced a record the store refuses: %v", err)
	}
	if len(summary.Items) > freshstore.MaxAdoptionItems() {
		t.Fatalf("recorded %d items over the store's bound", len(summary.Items))
	}
	// The fixed configuration part of the plan keeps its room: one item per
	// gateway leaf, rule family and provider section, whatever the credential
	// inventory looks like.
	for _, path := range cpa.ConfigLeafPaths() {
		itemByName(t, summary, configLeafItemName(path))
	}
	for _, family := range cpa.ConfigRuleFamilyIDs() {
		itemByName(t, summary, configFamilyItemName(family))
	}
	for _, section := range ProviderSections() {
		itemByName(t, summary, providerSectionItemName(section))
	}
	marker := itemByName(t, summary, excessCredentialItem)
	if marker.Outcome != freshstore.AdoptionManual || marker.Reason != "adoption_inventory_too_large" || marker.Instruction == "" {
		t.Fatalf("the excess must be one manual item with an instruction: %+v", marker)
	}
	omitted := 0
	for name := range source.credentials {
		if summary.Index(credentialItemName(name)) >= 0 ||
			summary.Index(overlongCredentialItem(name)) >= 0 ||
			summary.Index(legacyOverlongCredentialItem(name)) >= 0 {
			continue
		}
		omitted++
	}
	if omitted == 0 || marker.Left != omitted {
		t.Fatalf("the excess marker reports %d left behind, the record omits %d", marker.Left, omitted)
	}
	// The budget is actually spent: the record sits at its item bound, so no
	// capacity was reserved for a kind of item that never needed it.
	if len(summary.Items) != freshstore.MaxAdoptionItems() {
		t.Fatalf("the record settled at %d of its %d item bound", len(summary.Items), freshstore.MaxAdoptionItems())
	}
	// Every uploaded credential is a recorded copy and nothing else: a bounded
	// record never claims a copy it did not make, and never makes one it did not
	// record.
	migratedCredentials := 0
	for _, item := range summary.Items {
		if item.Kind == freshstore.AdoptionKindCredential && item.Outcome == freshstore.AdoptionMigrated {
			migratedCredentials++
		}
	}
	if uploads := len(destination.uploadNames()); uploads != migratedCredentials {
		t.Fatalf("the destination received %d uploads for %d recorded copies", uploads, migratedCredentials)
	}
	return marker
}

// assertCapacityReplay proves the over-capacity record is a settle, not a stop:
// a replay of the same source appends nothing, leaves the marker alone and
// uploads nothing new.
func assertCapacityReplay(t *testing.T, source, destination *fakeCPA, durable freshstore.AdoptionSummary) {
	t.Helper()
	uploads := len(destination.uploadNames())
	replay := runMigration(t, source, destination, durable)
	if err := freshstore.ValidateAdoptionSummary(replay); err != nil {
		t.Fatalf("a replay produced a record the store refuses: %v", err)
	}
	if len(replay.Items) != len(durable.Items) {
		t.Fatalf("a replay appended %d items", len(replay.Items)-len(durable.Items))
	}
	if !reflect.DeepEqual(replay.Items, durable.Items) {
		t.Fatal("a replay rewrote a settled item")
	}
	if !replay.Finished || replay.Planned != len(replay.Items) {
		t.Fatalf("a replay did not settle its own plan: finished=%v planned=%d items=%d",
			replay.Finished, replay.Planned, len(replay.Items))
	}
	if len(destination.uploadNames()) != uploads {
		t.Fatalf("a replay uploaded %d credentials again", len(destination.uploadNames())-uploads)
	}
}

// Many names the record cannot hold must settle with one aggregate marker
// instead of stopping at the item bound. Every overlong name costs one slot of
// the same budget an ordinary credential costs, and the names past the budget
// are counted by the marker rather than written one by one.
func TestManyOverlongCredentialNamesSettleAsOneAggregate(t *testing.T) {
	const total = 1100
	names := overlongCredentialNames(total)
	source := capacitySource(t, names)
	destination := destinationFixture(t)
	var durable freshstore.AdoptionSummary
	checkpoint := func(current freshstore.AdoptionSummary) error {
		if err := freshstore.ValidateAdoptionSummary(current); err != nil {
			return err
		}
		durable = current
		return nil
	}
	summary := runMigrationWithCheckpoint(t, source, destination, freshstore.AdoptionSummary{}, checkpoint)
	marker := assertCapacityRecord(t, source, destination, summary)
	if marker.Left >= total {
		t.Fatalf("the marker counted every file as left behind: %d", marker.Left)
	}
	recorded := 0
	for _, item := range summary.Items {
		if item.Reason != "adoption_item_name_too_long" {
			continue
		}
		recorded++
		if item.Outcome != freshstore.AdoptionManual {
			t.Fatalf("an over-long name was not manual work: %+v", item)
		}
	}
	if recorded != total-marker.Left {
		t.Fatalf("the record holds %d over-long items and reports %d left behind", recorded, marker.Left)
	}
	// An overlong name can never be uploaded: it is manual work by definition.
	if uploads := destination.uploadNames(); len(uploads) != 0 {
		t.Fatalf("a run with no ordinary inventory uploaded %d credentials", len(uploads))
	}
	assertCapacityReplay(t, source, destination, durable)
	source.usesOnlyReads(t)
}

// A mixed inventory shares one budget: the ordinary credentials that fit are
// copied, the overlong names that fit are reported individually, and the rest of
// both kinds are counted by the same aggregate. Charging one kind and not the
// other used to overflow the item bound.
func TestMixedOrdinaryAndOverlongInventorySharesOneBudget(t *testing.T) {
	const ordinary, overlong = 850, 300
	names := append(ordinaryCredentialNames(ordinary), overlongCredentialNames(overlong)...)
	source := capacitySource(t, names)
	destination := destinationFixture(t)
	var durable freshstore.AdoptionSummary
	checkpoint := func(current freshstore.AdoptionSummary) error {
		if err := freshstore.ValidateAdoptionSummary(current); err != nil {
			return err
		}
		durable = current
		return nil
	}
	summary := runMigrationWithCheckpoint(t, source, destination, freshstore.AdoptionSummary{}, checkpoint)
	marker := assertCapacityRecord(t, source, destination, summary)
	// The ordinary names sorted first filled their part of the budget, so all of
	// them are recorded and copied; the overlong tail is what the marker counts.
	for index := range ordinary {
		item := itemByName(t, summary, credentialItemName(ordinaryCredentialName(index)))
		if item.Outcome != freshstore.AdoptionMigrated {
			t.Fatalf("an ordinary credential inside the budget was not copied: %+v", item)
		}
	}
	if marker.Left <= 0 || marker.Left >= overlong {
		t.Fatalf("the marker reports %d of %d overlong files left behind", marker.Left, overlong)
	}
	if uploads := destination.uploadNames(); len(uploads) != ordinary {
		t.Fatalf("the run uploaded %d credentials, %d ordinary names fit", len(uploads), ordinary)
	}
	if _, stored := destination.credential(overlongCredentialName(overlong - 1)); stored {
		t.Fatal("an overlong credential was uploaded anyway")
	}
	assertCapacityReplay(t, source, destination, durable)
	source.usesOnlyReads(t)
}

// An ordinary inventory far over the record's capacity settles the same way: the
// aggregate marker reports the true remainder, which is larger than the item
// bound the record itself keeps. A count is not a stored item, so it must not be
// the thing that stops the migration.
func TestOversizedOrdinaryInventoryStillFormsTheAggregateMarker(t *testing.T) {
	const total = 3000
	source := capacitySource(t, ordinaryCredentialNames(total))
	destination := destinationFixture(t)
	var durable freshstore.AdoptionSummary
	checkpoint := func(current freshstore.AdoptionSummary) error {
		if err := freshstore.ValidateAdoptionSummary(current); err != nil {
			return err
		}
		durable = current
		return nil
	}
	summary := runMigrationWithCheckpoint(t, source, destination, freshstore.AdoptionSummary{}, checkpoint)
	marker := assertCapacityRecord(t, source, destination, summary)
	if marker.Left <= freshstore.MaxAdoptionItems() {
		t.Fatalf("the marker reports %d left behind, which the item bound could already hold", marker.Left)
	}
	if marker.Left >= total {
		t.Fatalf("the marker counted every file as left behind: %d", marker.Left)
	}
	// The credential past the budget was never uploaded: a bounded record never
	// claims a copy it did not make.
	if _, stored := destination.credential(ordinaryCredentialName(total - 1)); stored {
		t.Fatal("a credential past the bound was uploaded anyway")
	}
	assertCapacityReplay(t, source, destination, durable)
	source.usesOnlyReads(t)
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
	observed := destination.projectedValue("api-keys.claude")
	if section.DestinationSHA256 != digestOfValue(observed) {
		t.Fatalf("the recorded section digest is not the observed value: %s vs %s", section.DestinationSHA256, digestOfValue(observed))
	}
	if section.DestinationSHA256 == section.SourceSHA256 {
		t.Fatalf("section destination digest %s equalled source digest despite injected auth_index", section.DestinationSHA256)
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
	// The 12 provider sections classify as provider state and the 6 structured
	// configuration sections classify as routing configuration; a bare root name,
	// or a section outside the plan, classifies as unknown configuration and
	// keeps its own manual action.
	if got, want := len(ProviderSections()), 18; got != want {
		t.Fatalf("len(ProviderSections()) = %d, want %d", got, want)
	}
	for _, section := range cpa.ProviderAdoptionSections() {
		if ref := ItemRef(providerSection(section)); ref != bootstrap.AdoptionRefProviderStateAPIKeys {
			t.Fatalf("provider section %s classified as %s", section, ref)
		}
	}
	for _, section := range cpa.StructuredConfigAdoptionSections() {
		if ref := ItemRef(providerSection(section)); ref != bootstrap.AdoptionRefConfigRouting {
			t.Fatalf("structured section %s classified as %s", section, ref)
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
	for _, path := range []string{"oauth.auth-dir", "not.a.path"} {
		if ref := ItemRef(freshstore.AdoptionItem{Item: configLeafItemName(path), Kind: freshstore.AdoptionKindConfigLeaf}); ref != bootstrap.AdoptionRefConfigUnknown {
			t.Fatalf("non-gateway leaf %s classified as %s", path, ref)
		}
	}
	for path, want := range map[string]bootstrap.ResourceRef{
		"server.host":                 bootstrap.AdoptionRefConfigServer,
		"plugins.dir":                 bootstrap.AdoptionRefConfigPlugins,
		"observability.logs.debug":    bootstrap.AdoptionRefConfigRouting,
		"models.catalog":              bootstrap.AdoptionRefConfigRouting,
		"server.kv-store.disabled":    bootstrap.AdoptionRefConfigRouting,
		"server.trusted-proxies":      bootstrap.AdoptionRefConfigRouting,
		"observability.logs.request-log": bootstrap.AdoptionRefConfigRouting,
	} {
		if ref := ItemRef(freshstore.AdoptionItem{Item: configLeafItemName(path), Kind: freshstore.AdoptionKindConfigLeaf}); ref != want {
			t.Fatalf("leaf %s classified as %s, want %s", path, ref, want)
		}
	}
	if ref := ItemRef(freshstore.AdoptionItem{Item: configFamilyItemName("not-a-family"), Kind: freshstore.AdoptionKindConfigFamily}); ref != bootstrap.AdoptionRefConfigUnknown {
		t.Fatalf("non-gateway family classified as %s", ref)
	}
	if ref := ItemRef(freshstore.AdoptionItem{Item: credentialItemName("claude-a.json"), Kind: freshstore.AdoptionKindCredential}); ref != bootstrap.AdoptionRefCPAAuthFiles {
		t.Fatalf("credential classified as %s", ref)
	}
	for section, want := range map[string]bootstrap.ResourceRef{
		"api-keys.claude":        bootstrap.AdoptionRefProviderStateAPIKeys,
		"api-keys.vertex":        bootstrap.AdoptionRefProviderStateAPIKeys,
		"oauth.model-alias":      bootstrap.AdoptionRefProviderStateAPIKeys,
		"oauth.settings":         bootstrap.AdoptionRefProviderStateAPIKeys,
		"credentials.concurrency": bootstrap.AdoptionRefConfigRouting,
		"credentials.in-flight":  bootstrap.AdoptionRefConfigRouting,
		"plugins.store-sources":  bootstrap.AdoptionRefConfigRouting,
		"plugins.store-auth":     bootstrap.AdoptionRefConfigRouting,
		"plugins.configs":        bootstrap.AdoptionRefConfigRouting,
		"oauth.providers.codex.live-media-relay.ice-servers": bootstrap.AdoptionRefConfigRouting,
		"providers":       bootstrap.AdoptionRefConfigUnknown,
		"client":          bootstrap.AdoptionRefConfigUnknown,
		"oauth":           bootstrap.AdoptionRefConfigUnknown,
		"plugins.enabled": bootstrap.AdoptionRefConfigPlugins,
		"made-up":         bootstrap.AdoptionRefConfigUnknown,
		"made-up.child":   bootstrap.AdoptionRefConfigUnknown,
	} {
		ref := ItemRef(providerSection(section))
		if ref != want {
			t.Fatalf("provider section %s classified as %s, want %s", section, ref, want)
		}
	}
	for _, section := range ProviderSections() {
		ref := ItemRef(providerSection(section))
		want := cpa.AdoptionItemRef(section)
		if ref != want || ref == bootstrap.AdoptionRefConfigUnknown {
			t.Fatalf("copyable section %s classified as %s (want %s)", section, ref, want)
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

// The executor's clones must carry the run generation: the checkpoint a run
// writes is the record's own generation, and a clone that dropped it would make
// every checkpoint of that run look like a different run's.
func TestExecutorCarriesTheRunGenerationThroughItsClones(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	runID := strings.Repeat("a", 32)
	previous := freshstore.AdoptionSummary{RunID: runID}
	checkpoints := 0
	summary := runMigrationWithCheckpoint(t, source, destination, previous, func(current freshstore.AdoptionSummary) error {
		checkpoints++
		if current.RunID != runID {
			t.Fatalf("a checkpoint carried the wrong generation: %q", current.RunID)
		}
		return nil
	})
	if checkpoints == 0 {
		t.Fatal("the run checkpointed nothing")
	}
	if summary.RunID != runID {
		t.Fatalf("the result dropped the generation: %q", summary.RunID)
	}
}

// An earlier executor recorded an over-long credential as the digest of its
// whole name. A resume recognises that identity from the current preflight name
// and records nothing for the file: the durable item is the outcome the operator
// was already told about, and appending the current identity would duplicate it.
func TestLegacyOverlongIdentityIsNotRecordedAgainOnResume(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	long := strings.Repeat("a", freshstore.MaxAdoptionItemName()) + ".json"
	source.seedAuthFile(long, "claude", []byte(`{"type":"claude","access_token":"legacy-token"}`), false)

	legacy := freshstore.AdoptionItem{
		Item: legacyOverlongCredentialItem(long), Kind: freshstore.AdoptionKindCredential,
		Outcome: freshstore.AdoptionManual, Reason: "adoption_item_name_too_long",
		Instruction: "Copy the credential file by hand.",
	}
	previous := freshstore.AdoptionSummary{Items: []freshstore.AdoptionItem{legacy}}

	summary := runMigration(t, source, destination, previous)
	if item, ok := summary.Item(legacyOverlongCredentialItem(long)); !ok || item != legacy {
		t.Fatalf("a resume rewrote the legacy item: %+v", item)
	}
	if _, ok := summary.Item(overlongCredentialItem(long)); ok {
		t.Fatal("a resume appended a second record for a file an earlier run settled")
	}
	tooLong := 0
	for _, item := range summary.Items {
		if item.Reason == "adoption_item_name_too_long" {
			tooLong++
		}
	}
	if tooLong != 1 {
		t.Fatalf("the resume recorded %d over-long items for one file", tooLong)
	}
	// The file is still not copied: the durable record says the operator must do
	// it by hand, and the identity being recognised does not change that.
	if _, stored := destination.credential(long); stored {
		t.Fatal("a credential whose earlier record is manual was uploaded anyway")
	}
	source.usesOnlyReads(t)
}

// TestProviderSectionReadBackAcceptsCPADerivedAuthIndexAcrossFamilies proves
// every api-keys.* family migrates and skips redundant writes on replay when
// the destination injects CPA's 16-hex auth_index (both on key entries and on a
// keyless openai-compatibility group), and when an Adopted v8 source also
// projects auth_index from GET /v8/management/config.
func TestProviderSectionReadBackAcceptsCPADerivedAuthIndexAcrossFamilies(t *testing.T) {
	source := newFakeCPA(t, "source-management-key")
	source.config = map[string]any{
		"api-keys": map[string]any{
			"gemini":       []any{map[string]any{"name": "gemini-1", "keys": []any{map[string]any{"api-key": "gem-key"}}}},
			"interactions": []any{map[string]any{"name": "interactions-1", "keys": []any{map[string]any{"api-key": "int-key"}}}},
			"vertex":       []any{map[string]any{"name": "vertex-1", "base-url": "https://vertex.example", "keys": []any{map[string]any{"api-key": "vtx-key"}}}},
			"codex":        []any{map[string]any{"name": "codex-1", "base-url": "https://codex.example/v1", "keys": []any{map[string]any{"api-key": "cdx-key"}}}},
			"claude": []any{map[string]any{
				"name": "claude-1", "base-url": "https://claude.example",
				"keys": []any{map[string]any{"api-key": "cld-key", "auth_index": fakeAuthIndex("adopted-source-v8-index")}},
			}},
			"xai":  []any{map[string]any{"name": "xai-1", "base-url": "https://xai.example/v1", "keys": []any{map[string]any{"api-key": "xai-key"}}}},
			"meta": []any{map[string]any{"name": "meta-1", "keys": []any{map[string]any{"api-key": "meta-key"}}}},
			"openai-compatibility": []any{
				map[string]any{
					"name": "compat-keyed", "base-url": "https://compat.example/v1",
					"keys":   []any{map[string]any{"api-key": "compat-key"}},
					"models": []any{map[string]any{"name": "compat-m", "alias": "compat-m"}},
				},
				map[string]any{
					"name": "compat-keyless", "base-url": "http://127.0.0.1:11434/v1",
					"keys":   []any{},
					"models": []any{map[string]any{"name": "local-m"}},
				},
			},
		},
	}
	destination := destinationFixture(t)
	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	families := []string{
		"api-keys.gemini", "api-keys.interactions", "api-keys.vertex", "api-keys.codex",
		"api-keys.claude", "api-keys.xai", "api-keys.meta", "api-keys.openai-compatibility",
	}
	for _, section := range families {
		item := itemByName(t, summary, providerSectionItemName(section))
		if item.Outcome != freshstore.AdoptionMigrated {
			t.Fatalf("section %s: outcome=%s reason=%s", section, item.Outcome, item.Reason)
		}
	}
	// A fresh run against the already-populated destination must skip every PUT via pre-read equality.
	before := destination.writeCount()
	second := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	if after := destination.writeCount(); after != before {
		t.Fatalf("pre-read equality failed to skip already-migrated provider sections: %d extra writes", after-before)
	}
	for _, section := range families {
		if item := itemByName(t, second, providerSectionItemName(section)); item.Outcome != freshstore.AdoptionMigrated {
			t.Fatalf("pre-read section %s: %+v", section, item)
		}
	}
}

// TestProviderSectionReadBackRejectsDivergentMalformedOrExtraFields ensures
// provider-section comparison stays fail-closed on altered values, malformed
// auth_index formats, misplaced group-level auth_index, and unexpected extra
// fields.
func TestProviderSectionReadBackRejectsDivergentMalformedOrExtraFields(t *testing.T) {
	validIdx := fakeAuthIndex("valid")
	cases := []struct {
		name     string
		section  string
		override any
	}{
		{
			name:    "changed api-key",
			section: "api-keys.openai-compatibility",
			override: []any{map[string]any{
				"name": "compat-group", "base-url": "http://compat.internal/v1",
				"keys":   []any{map[string]any{"api-key": "different-key", "auth_index": validIdx}},
				"models": []any{map[string]any{"name": "compat-model"}},
			}},
		},
		{
			name:    "changed base-url",
			section: "api-keys.openai-compatibility",
			override: []any{map[string]any{
				"name": "compat-group", "base-url": "http://wrong.internal/v1",
				"keys":   []any{map[string]any{"api-key": "src-provider-compat-key", "auth_index": validIdx}},
				"models": []any{map[string]any{"name": "compat-model"}},
			}},
		},
		{
			name:    "uppercase auth_index",
			section: "api-keys.openai-compatibility",
			override: []any{map[string]any{
				"name": "compat-group", "base-url": "http://compat.internal/v1",
				"keys":   []any{map[string]any{"api-key": "src-provider-compat-key", "auth_index": strings.ToUpper(validIdx)}},
				"models": []any{map[string]any{"name": "compat-model"}},
			}},
		},
		{
			name:    "short auth_index",
			section: "api-keys.claude",
			override: []any{map[string]any{
				"name": "claude-group",
				"keys": []any{map[string]any{"api-key": "src-provider-claude-key", "auth_index": "557ea525"}},
			}},
		},
		{
			name:    "non-hex auth_index",
			section: "api-keys.claude",
			override: []any{map[string]any{
				"name": "claude-group",
				"keys": []any{map[string]any{"api-key": "src-provider-claude-key", "auth_index": "zzzzzzzzzzzzzzzz"}},
			}},
		},
		{
			name:    "non-string auth_index",
			section: "api-keys.claude",
			override: []any{map[string]any{
				"name": "claude-group",
				"keys": []any{map[string]any{"api-key": "src-provider-claude-key", "auth_index": json.Number("1234567890123456")}},
			}},
		},
		{
			name:    "group-level auth_index on keyed group",
			section: "api-keys.openai-compatibility",
			override: []any{map[string]any{
				"name": "compat-group", "base-url": "http://compat.internal/v1", "auth_index": validIdx,
				"keys":   []any{map[string]any{"api-key": "src-provider-compat-key", "auth_index": validIdx}},
				"models": []any{map[string]any{"name": "compat-model"}},
			}},
		},
		{
			name:    "unexpected extra field on key entry",
			section: "api-keys.openai-compatibility",
			override: []any{map[string]any{
				"name": "compat-group", "base-url": "http://compat.internal/v1",
				"keys":   []any{map[string]any{"api-key": "src-provider-compat-key", "auth_index": validIdx, "unexpected": "extra"}},
				"models": []any{map[string]any{"name": "compat-model"}},
			}},
		},
		{
			name:    "hyphenated auth-index extra field",
			section: "api-keys.claude",
			override: []any{map[string]any{
				"name": "claude-group",
				"keys": []any{map[string]any{"api-key": "src-provider-claude-key", "auth-index": validIdx}},
			}},
		},
		{
			name:    "unexpected auth_index on oauth section",
			section: "oauth.model-alias",
			override: map[string]any{
				"claude":     []any{map[string]any{"name": "claude-3", "alias": "claude-alias"}},
				"auth_index": validIdx,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, destination := sourceFixture(t), destinationFixture(t)
			destination.readOverrides = map[string]any{tc.section: tc.override}
			summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
			item := itemByName(t, summary, providerSectionItemName(tc.section))
			if item.Outcome != freshstore.AdoptionFailed || item.Reason != "verify_mismatch" {
				t.Fatalf("case %s (%s): got outcome=%s reason=%s, want failed/verify_mismatch", tc.name, tc.section, item.Outcome, item.Reason)
			}
		})
	}
}

// TestProviderSectionReadBackRejectsGroupAuthIndexWithoutCPAAtPrecondition proves
// a group-level auth_index is treated as CPA-derived only where CPA actually
// projects one: a keyless openai-compatibility group whose base-url is non-empty
// (config_auth_index.go:460-488). With no base-url, an empty one, or a
// whitespace-only one, the field is not CPA's, so the pre-read must not skip the
// write and the post-write read-back must not report the section migrated.
func TestProviderSectionReadBackRejectsGroupAuthIndexWithoutCPAAtPrecondition(t *testing.T) {
	validIdx := fakeAuthIndex("group-precondition")
	cases := []struct {
		name  string
		group func() map[string]any
	}{
		{
			name: "base-url missing",
			group: func() map[string]any {
				return map[string]any{
					"name":   "compat-keyless",
					"models": []any{map[string]any{"name": "local-model"}},
				}
			},
		},
		{
			name: "base-url empty",
			group: func() map[string]any {
				return map[string]any{
					"name":     "compat-keyless",
					"base-url": "",
					"models":   []any{map[string]any{"name": "local-model"}},
				}
			},
		},
		{
			name: "base-url whitespace",
			group: func() map[string]any {
				return map[string]any{
					"name":     "compat-keyless",
					"base-url": "   ",
					"models":   []any{map[string]any{"name": "local-model"}},
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := sourceFixture(t)
			source.config["api-keys"] = map[string]any{"openai-compatibility": []any{tc.group()}}
			destination := destinationFixture(t)
			projected := tc.group()
			projected["auth_index"] = validIdx
			destination.readOverrides = map[string]any{"api-keys.openai-compatibility": []any{projected}}

			before := destination.writeCount()
			summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
			if destination.writeCount() == before {
				t.Fatalf("pre-read treated a non-derivable group auth_index as equal and skipped the write")
			}
			item := itemByName(t, summary, providerSectionItemName("api-keys.openai-compatibility"))
			if item.Outcome != freshstore.AdoptionFailed || item.Reason != "verify_mismatch" {
				t.Fatalf("post-write read-back: got outcome=%s reason=%s, want failed/verify_mismatch", item.Outcome, item.Reason)
			}
		})
	}
}

// simulateCPAV8CredentialPersist reproduces CPA 8.0.55's on-disk credential
// normalization (`sdk/auth/filestore.go:100-119`, `metadata_keys.go:10-49`,
// `synthesizer/file.go:148-193`, provider `SaveTokenToFile`): pretty-printed
// sorted JSON, explicit `"disabled"` boolean, canonical snake_case metadata
// aliases, and provider empty-string / fixed defaults when absent.
func simulateCPAV8CredentialPersist(raw []byte, forceDisabled *bool) []byte {
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return raw
	}
	for _, pair := range cpaV8CredentialMetadataKeyAliases {
		if val, ok := obj[pair[0]]; ok {
			if _, hasCanon := obj[pair[1]]; !hasCanon {
				obj[pair[1]] = val
			}
			delete(obj, pair[0])
		}
	}
	disabled := false
	if b, ok := obj["disabled"].(bool); ok {
		disabled = b
	}
	if forceDisabled != nil {
		disabled = *forceDisabled
	}
	obj["disabled"] = disabled

	if rawType, ok := obj["type"].(string); ok {
		canon := strings.ToLower(strings.TrimSpace(rawType))
		obj["type"] = canon
		ensureEmpty := func(keys ...string) {
			for _, k := range keys {
				if _, exists := obj[k]; !exists {
					obj[k] = ""
				}
			}
		}
		switch canon {
		case "claude":
			ensureEmpty("id_token", "access_token", "refresh_token", "last_refresh", "email", "expired")
		case "codex":
			ensureEmpty("id_token", "access_token", "refresh_token", "account_id", "last_refresh", "email", "expired")
		case "xai":
			ensureEmpty("access_token", "refresh_token")
			if _, exists := obj["auth_kind"]; !exists {
				obj["auth_kind"] = "oauth"
			}
		case "meta":
			ensureEmpty("access_token")
			if _, exists := obj["auth_kind"]; !exists {
				obj["auth_kind"] = "oauth"
			}
		case "vertex":
			sa, _ := obj["service_account"].(map[string]any)
			if _, exists := obj["project_id"]; !exists {
				p, _ := sa["project_id"].(string)
				obj["project_id"] = p
			}
			if _, exists := obj["email"]; !exists {
				e, _ := sa["client_email"].(string)
				obj["email"] = e
			}
		case "kimi", "kimi-ai":
			ensureEmpty("access_token", "refresh_token", "token_type")
			if _, exists := obj["domain"]; !exists {
				if canon == "kimi-ai" {
					obj["domain"] = "kimi.ai"
				} else {
					obj["domain"] = "kimi.com"
				}
			}
			if _, exists := obj["base_url"]; !exists {
				if obj["domain"] == "kimi.ai" {
					obj["base_url"] = "https://api.kimi.ai/coding"
				} else {
					obj["base_url"] = "https://api.kimi.com/coding"
				}
			}
		}
	}
	if _, exists := obj["oauth_provider"]; !exists {
		if portal, ok := obj["portal"].(string); ok && strings.TrimSpace(portal) != "" {
			obj["oauth_provider"] = strings.ToLower(strings.TrimSpace(portal))
		}
	}
	formatted, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return raw
	}
	return append(formatted, '\n')
}

func TestCredentialMigrationAcceptsNarrowCPAV8LosslessNormalizationAndPreservesUnknownFields(t *testing.T) {
	source := newFakeCPA(t, "src-key")
	source.config = map[string]any{"config-version": 8}
	sourceFiles := map[string]struct {
		typ      string
		disabled bool
		raw      string
	}{
		"claude-norm.json": {
			typ: "claude",
			raw: `{"type":"Claude","access_token":"sk-ant-oat-1","refresh_token":"rt-1","request-retry":3,"proxy-url":"http://proxy.internal:8080","provider-specific-key":"keep-verbatim","custom_nested":{"tier":"max","quota":100}}`,
		},
		"codex-disabled.json": {
			typ:      "codex",
			disabled: true,
			raw:      `{"type":"codex","access_token":"codex-access-1","account_id":"acct-42","disable-cooling":true,"excluded-models":["o1-preview"],"x_unknown_token":"opaque-secret"}`,
		},
		"kimi-norm.json": {
			typ: "kimi-ai",
			raw: `{"type":"kimi-ai","access_token":"kimi-tok","portal":"Kimi-Portal","tool-prefix-disabled":true}`,
		},
		"vertex-norm.json": {
			typ: "vertex",
			raw: `{"type":"vertex","service_account":{"project_id":"gcp-proj-1","client_email":"sa@gcp-proj-1.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\nabc\n-----END PRIVATE KEY-----\n"},"model-aliases":[{"from":"g1","to":"g2"}]}`,
		},
		"xai-meta-norm.json": {
			typ: "xai",
			raw: `{"type":"xai","access_token":"xai-tok","api-key":"xai-k","base-url":"https://api.x.ai/v1","fingerprint-profile":"chrome","request-scoped-errors":true}`,
		},
		"claude-redundant-alias.json": {
			typ: "claude",
			raw: `{"type":"claude","access_token":"sk-ant-oat-2","proxy-url":"http://proxy.internal:8080","proxy_url":"http://proxy.internal:8080"}`,
		},
	}
	for name, entry := range sourceFiles {
		source.credentials[name] = []byte(entry.raw)
		source.credentialTypes[name] = entry.typ
		source.disabled[name] = entry.disabled
	}

	destination := destinationFixture(t)
	destination.transformOnUpload = func(_ string, raw []byte) []byte {
		return simulateCPAV8CredentialPersist(raw, nil)
	}
	destination.transformOnStatusPatch = func(_ string, current []byte, disabled bool) []byte {
		return simulateCPAV8CredentialPersist(current, &disabled)
	}

	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	for name, entry := range sourceFiles {
		item := itemByName(t, summary, credentialItemName(name))
		if item.Outcome != freshstore.AdoptionMigrated {
			t.Fatalf("%s outcome = %s (reason=%q), want migrated", name, item.Outcome, item.Reason)
		}
		observed, ok := destination.credential(name)
		if !ok {
			t.Fatalf("destination missing %s", name)
		}
		if item.SourceSHA256 != digestSHA256([]byte(entry.raw)) {
			t.Fatalf("%s SourceSHA256 = %s, want %s", name, item.SourceSHA256, digestSHA256([]byte(entry.raw)))
		}
		if item.DestinationSHA256 != digestSHA256(observed) {
			t.Fatalf("%s DestinationSHA256 = %s, want observed %s", name, item.DestinationSHA256, digestSHA256(observed))
		}
		if item.SourceSHA256 == item.DestinationSHA256 {
			t.Fatalf("%s digests matched despite CPA v8 normalization", name)
		}
	}

	// Crash-retry before checkpoint settlement (empty summary, populated
	// destination) recognizes the normalized destination documents and neither
	// re-uploads nor re-patches disabled state.
	uploadsBefore := len(destination.uploads)
	patchesBefore := destination.statusPatchCount
	rerunSummary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	if len(destination.uploads) != uploadsBefore || destination.statusPatchCount != patchesBefore {
		t.Fatalf("idempotent rerun wrote again: uploads %d->%d, statusPatches %d->%d",
			uploadsBefore, len(destination.uploads), patchesBefore, destination.statusPatchCount)
	}
	for name := range sourceFiles {
		if got := itemByName(t, rerunSummary, credentialItemName(name)); got.Outcome != freshstore.AdoptionMigrated {
			t.Fatalf("rerun %s = %+v, want migrated", name, got)
		}
	}
}

func TestCredentialMigrationRefusesSemanticChangesAndFieldLoss(t *testing.T) {
	cases := []struct {
		name           string
		sourceType     string
		sourceDisabled bool
		sourceJSON     string
		destJSON       string
	}{
		{
			name:       "altered access_token",
			sourceType: "codex",
			sourceJSON: `{"type":"codex","access_token":"real-token-1"}`,
			destJSON:   `{"type":"codex","access_token":"different-token","disabled":false}`,
		},
		{
			name:       "non-empty source token blanked to empty string",
			sourceType: "claude",
			sourceJSON: `{"type":"claude","access_token":"sk-ant-1","refresh_token":"rt-keep"}`,
			destJSON:   `{"type":"claude","access_token":"sk-ant-1","refresh_token":"","disabled":false}`,
		},
		{
			name:       "non-empty source email blanked to empty string",
			sourceType: "codex",
			sourceJSON: `{"type":"codex","access_token":"tok","email":"ops@example.com"}`,
			destJSON:   `{"type":"codex","access_token":"tok","email":"","disabled":false}`,
		},
		{
			name:       "unknown hyphenated field dropped",
			sourceType: "codex",
			sourceJSON: `{"type":"codex","access_token":"tok","provider-specific-key":"must-keep"}`,
			destJSON:   `{"type":"codex","access_token":"tok","disabled":false}`,
		},
		{
			name:       "unknown hyphenated field renamed to snake_case",
			sourceType: "codex",
			sourceJSON: `{"type":"codex","access_token":"tok","provider-specific-key":"must-keep"}`,
			destJSON:   `{"type":"codex","access_token":"tok","provider_specific_key":"must-keep","disabled":false}`,
		},
		{
			name:       "unknown nested field altered",
			sourceType: "claude",
			sourceJSON: `{"type":"claude","access_token":"tok","custom_meta":{"tier":"pro"}}`,
			destJSON:   `{"type":"claude","access_token":"tok","custom_meta":{"tier":"free"},"disabled":false}`,
		},
		{
			name:       "unexpected extra field added on destination",
			sourceType: "codex",
			sourceJSON: `{"type":"codex","access_token":"tok"}`,
			destJSON:   `{"type":"codex","access_token":"tok","unexpected_extra":"added","disabled":false}`,
		},
		{
			name:       "non-empty provider field forged when absent at source",
			sourceType: "codex",
			sourceJSON: `{"type":"codex","access_token":"tok"}`,
			destJSON:   `{"type":"codex","access_token":"tok","account_id":"forged-acct","disabled":false}`,
		},
		{
			name:       "enabled credential returned with disabled true in JSON",
			sourceType: "codex",
			sourceJSON: `{"type":"codex","access_token":"tok"}`,
			destJSON:   `{"type":"codex","access_token":"tok","disabled":true}`,
		},
		{
			name:           "disabled credential rewritten with disabled false after status patch",
			sourceType:     "codex",
			sourceDisabled: true,
			sourceJSON:     `{"type":"codex","access_token":"tok","disabled":true}`,
			destJSON:       `{"type":"codex","access_token":"tok","disabled":false}`,
		},
		{
			name:       "provider type changed",
			sourceType: "codex",
			sourceJSON: `{"type":"codex","access_token":"tok"}`,
			destJSON:   `{"type":"claude","access_token":"tok","disabled":false}`,
		},
		{
			name:       "conflicting legacy and canonical api-key metadata aliases fail closed",
			sourceType: "xai",
			sourceJSON: `{"type":"xai","access_token":"tok","api-key":"legacy-secret","api_key":"canonical-secret"}`,
			destJSON:   `{"type":"xai","access_token":"tok","api_key":"canonical-secret","auth_kind":"oauth","refresh_token":"","disabled":false}`,
		},
		{
			name:       "conflicting legacy and canonical proxy-url metadata aliases fail closed",
			sourceType: "claude",
			sourceJSON: `{"type":"claude","access_token":"tok","proxy-url":"http://proxy-legacy:8080","proxy_url":"http://proxy-canonical:8080"}`,
			destJSON:   `{"type":"claude","access_token":"tok","proxy_url":"http://proxy-canonical:8080","id_token":"","refresh_token":"","last_refresh":"","email":"","expired":"","disabled":false}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := newFakeCPA(t, "src-key")
			source.config = map[string]any{"config-version": 8}
			source.credentials["cred.json"] = []byte(tc.sourceJSON)
			source.credentialTypes["cred.json"] = tc.sourceType
			source.disabled["cred.json"] = tc.sourceDisabled

			destination := destinationFixture(t)
			destination.transformOnUpload = func(_ string, _ []byte) []byte {
				return []byte(tc.destJSON)
			}
			destination.transformOnStatusPatch = func(_ string, _ []byte, _ bool) []byte {
				return []byte(tc.destJSON)
			}

			summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
			item := itemByName(t, summary, credentialItemName("cred.json"))
			if item.Outcome != freshstore.AdoptionFailed || item.Reason != "verify_mismatch" {
				t.Fatalf("case %q: got outcome=%s reason=%q, want failed/verify_mismatch",
					tc.name, item.Outcome, item.Reason)
			}
		})
	}
}

func TestFullCPAConfigBusinessSettingsAndKVMapMigration(t *testing.T) {
	source := newFakeCPA(t, "src-key")
	source.seedAuthFile("claude-a.json", "claude", []byte(`{"type":"claude","access_token":"tok-1"}`), false)
	source.config = map[string]any{
		"config-version": 8,
		"server": map[string]any{
			"server-id":        "src-srv",
			"host":             "127.0.0.1",
			"port":             8317,
			"commercial-mode":  true,
			"trusted-proxies":  []any{"10.0.0.0/8", "192.168.1.0/24"},
			"github-token":     "ghp_source_secret_token",
			"tls":              map[string]any{"enable": true},
			"discovery":        map[string]any{"enabled": true},
			"kv-store":         map[string]any{"path": "/src/kv.db", "disabled": true},
		},
		"models": map[string]any{
			"catalog":       "custom-catalog",
			"codex-catalog": "embedded",
			"devin-catalog": "remote",
		},
		"oauth": map[string]any{
			"auth-dir": "/src/auth",
			"providers": map[string]any{
				"claude": map[string]any{
					"oauth-include-create-api-key": true,
				},
				"antigravity": map[string]any{
					"sensitive-words-replace": map[string]any{
						"badword": "goodword",
					},
					"sensitive-patterns-replace": []any{
						map[string]any{"pattern": `foo\d+`, "replacement": "bar"},
					},
				},
			},
		},
		"requests": map[string]any{
			"payload": map[string]any{
				"replace": []any{
					map[string]any{
						"models": []any{map[string]any{"name": "claude-*", "protocol": "claude"}},
						"params": map[string]any{
							"target": map[string]any{"paths": []any{"messages[0].content"}},
							"replace": []any{
								map[string]any{"pattern": "secret", "replacement": "redacted"},
							},
						},
					},
				},
			},
		},
		"credentials": map[string]any{
			"in-flight": map[string]any{
				"snapshot-interval": "30s",
				"stale-after":       "2m",
				"max-part-bytes":    65536,
				"max-part-count":    4,
				"max-revision-bytes": 131072,
			},
		},
		"plugins": map[string]any{
			"enabled":       false,
			"dir":           "/src/plugins",
			"auth-revision": 7,
			"store-sources": []any{map[string]any{"name": "default", "url": "https://plugins.example.com"}},
			"store-auth":    map[string]any{"default": map[string]any{"token": "store-tok"}},
			"configs":       map[string]any{"my-plugin": map[string]any{"threshold": 42}},
		},
		"observability": map[string]any{
			"usage": map[string]any{"statistics-enabled": false},
			"pprof": map[string]any{"enabled": true},
			"logs": map[string]any{
				"logging-to-file":           false,
				"logs-max-total-size-mb":    500,
				"error-logs-max-files":      20,
				"debug":                     true,
				"request-log":               true,
			},
		},
	}

	destination := destinationFixture(t)
	// Seed stale destination KVMap entry and destination host-exclusive fields
	// to verify whole-map PUT replacement and strict host preservation.
	mergeInto(destination.config, []string{"oauth", "providers", "antigravity", "sensitive-words-replace"}, map[string]any{
		"stalekey": "staleval",
	})
	mergeInto(destination.config, []string{"server"}, map[string]any{
		"server-id": "dst-srv",
		"host":      "0.0.0.0",
		"port":      8317,
		"kv-store":  map[string]any{"path": "/supervisor/kv.db"},
	})
	mergeInto(destination.config, []string{"plugins"}, map[string]any{
		"enabled":       true,
		"dir":           "/supervisor/plugins",
		"auth-revision": 1,
	})
	mergeInto(destination.config, []string{"observability"}, map[string]any{
		"usage": map[string]any{"statistics-enabled": true},
		"logs":  map[string]any{"logging-to-file": true},
	})

	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	for _, leaf := range []string{
		"server.commercial-mode",
		"server.trusted-proxies",
		"server.github-token",
		"server.kv-store.disabled",
		"models.catalog",
		"models.codex-catalog",
		"models.devin-catalog",
		"oauth.providers.claude.oauth-include-create-api-key",
		"oauth.providers.antigravity.sensitive-words-replace",
		"oauth.providers.antigravity.sensitive-patterns-replace",
		"observability.logs.debug",
		"observability.logs.request-log",
	} {
		if item := itemByName(t, summary, configLeafItemName(leaf)); item.Outcome != freshstore.AdoptionMigrated {
			t.Fatalf("leaf %s did not migrate: %+v", leaf, item)
		}
	}
	if item := itemByName(t, summary, configFamilyItemName("replace")); item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("replace rule family did not migrate: %+v", item)
	}
	for _, section := range []string{"credentials.in-flight", "plugins.store-sources", "plugins.store-auth", "plugins.configs"} {
		if item := itemByName(t, summary, providerSectionItemName(section)); item.Outcome != freshstore.AdoptionMigrated {
			t.Fatalf("section %s did not migrate: %+v", section, item)
		}
	}
	// KVMap PUT replaced the stale destination entry instead of merging.
	kvGot, _ := lookup(destination.config, []string{"oauth", "providers", "antigravity", "sensitive-words-replace"}).(map[string]any)
	if _, hasStale := kvGot["stalekey"]; hasStale || kvGot["badword"] != "goodword" {
		t.Fatalf("KVMap leaf was merged instead of replaced: %+v", kvGot)
	}
	// Host-exclusive fields stayed untouched on destination.
	if got := lookup(destination.config, []string{"server", "server-id"}); got != "dst-srv" {
		t.Fatalf("destination server-id changed: %v", got)
	}
	if got := lookup(destination.config, []string{"server", "kv-store", "path"}); got != "/supervisor/kv.db" {
		t.Fatalf("destination kv-store.path changed: %v", got)
	}
	if got := lookup(destination.config, []string{"plugins", "enabled"}); got != true {
		t.Fatalf("destination plugins.enabled changed: %v", got)
	}
	if got := lookup(destination.config, []string{"plugins", "auth-revision"}); got != 1 {
		t.Fatalf("destination plugins.auth-revision changed: %v", got)
	}
	if got := lookup(destination.config, []string{"observability", "logs", "logging-to-file"}); got != true {
		t.Fatalf("destination logging-to-file changed: %v", got)
	}
}

func TestConcurrencySectionPreservesDestinationRevisions(t *testing.T) {
	source := newFakeCPA(t, "src-key")
	source.seedAuthFile("claude-a.json", "claude", []byte(`{"type":"claude","access_token":"tok"}`), false)
	source.config = map[string]any{
		"config-version": 8,
		"credentials": map[string]any{
			"concurrency": map[string]any{
				"default":                       4,
				"lifecycle-config-revision":     "src-rev-99",
				"observation-barrier-revision":  12,
			},
		},
	}
	destination := destinationFixture(t)
	mergeInto(destination.config, []string{"credentials", "concurrency"}, map[string]any{
		"lifecycle-config-revision":    "dst-rev-1",
		"observation-barrier-revision": 3,
	})

	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	item := itemByName(t, summary, providerSectionItemName("credentials.concurrency"))
	if item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("credentials.concurrency outcome = %+v, want migrated", item)
	}
	got, _ := lookup(destination.config, []string{"credentials", "concurrency"}).(map[string]any)
	if got["lifecycle-config-revision"] != "dst-rev-1" || got["observation-barrier-revision"] != 3 {
		t.Fatalf("destination revisions overwritten: %+v", got)
	}
	if got["default"] != 4 {
		t.Fatalf("destination concurrency settings missing: %+v", got)
	}
	// Replay must be idempotent with zero additional writes to credentials.concurrency.
	replay := runMigration(t, source, destination, summary)
	if replayItem := itemByName(t, replay, providerSectionItemName("credentials.concurrency")); replayItem != item {
		t.Fatalf("replay rewrote concurrency item: %+v vs %+v", replayItem, item)
	}

	// Source with ONLY revision keys becomes absent_at_source.
	revOnlySource := newFakeCPA(t, "src-key")
	revOnlySource.seedAuthFile("claude-a.json", "claude", []byte(`{"type":"claude","access_token":"tok"}`), false)
	revOnlySource.config = map[string]any{
		"config-version": 8,
		"credentials": map[string]any{
			"concurrency": map[string]any{
				"lifecycle-config-revision": "src-only",
			},
		},
	}
	revOnlyDest := destinationFixture(t)
	revOnlySummary := runMigration(t, revOnlySource, revOnlyDest, freshstore.AdoptionSummary{})
	if got := itemByName(t, revOnlySummary, providerSectionItemName("credentials.concurrency")); got.Outcome != freshstore.AdoptionAbsentAtSource {
		t.Fatalf("revision-only concurrency outcome = %+v, want absent_at_source", got)
	}
}

func TestICEServersMigratesViaYAMLAndFailsClosedWhenYAMLUnavailable(t *testing.T) {
	source := newFakeCPA(t, "src-key")
	source.seedAuthFile("claude-a.json", "claude", []byte(`{"type":"claude","access_token":"tok"}`), false)
	source.config = map[string]any{
		"config-version": 8,
		"oauth": map[string]any{
			"providers": map[string]any{
				"codex": map[string]any{
					"live-media-relay": map[string]any{
						"enabled": true,
						"ice-servers": []any{
							map[string]any{
								"urls":       []any{"turn:turn.example.com:3478"},
								"username":   "turn-user",
								"credential": "turn-secret",
							},
						},
					},
				},
			},
		},
	}
	destination := destinationFixture(t)
	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	item := itemByName(t, summary, providerSectionItemName(iceServersSectionPath))
	if item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("ice-servers outcome = %+v, want migrated", item)
	}
	got, _ := lookup(destination.config, strings.Split(iceServersSectionPath, ".")).([]any)
	if len(got) != 1 {
		t.Fatalf("destination ice-servers len = %d, want 1", len(got))
	}
	first, _ := got[0].(map[string]any)
	if first["username"] != "turn-user" || first["credential"] != "turn-secret" {
		t.Fatalf("destination ice-servers missing hydrated TURN credentials: %+v", first)
	}

	// When source /v8/management/config.yaml fails, ice-servers must NOT migrate
	// redacted entries and must become manual reconfigure_after_adoption.
	source.failConfigYAML = true
	failDest := destinationFixture(t)
	failSummary := runMigration(t, source, failDest, freshstore.AdoptionSummary{})
	manual := itemByName(t, failSummary, providerSectionItemName(iceServersSectionPath))
	if manual.Outcome != freshstore.AdoptionManual || manual.Reason != "reconfigure_after_adoption" {
		t.Fatalf("redacted ice-servers outcome = %+v, want manual reconfigure_after_adoption", manual)
	}
	if got := lookup(failDest.config, strings.Split(iceServersSectionPath, ".")); got != nil {
		t.Fatalf("redacted ice-servers must not be written to destination: %+v", got)
	}
}

func fakeInt64(raw any) (int64, bool) {
	switch v := raw.(type) {
	case int:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		return n, err == nil
	default:
		return 0, false
	}
}

func validateFakeLiveMediaRelay(doc map[string]any) error {
	raw := lookup(doc, []string{"oauth", "providers", "codex", "live-media-relay"})
	relay, ok := raw.(map[string]any)
	if !ok || relay == nil {
		return nil
	}
	enabled, _ := relay["enabled"].(bool)
	if !enabled {
		return nil
	}
	maxSessions := int64(0)
	if rawMax, ok := relay["max-sessions"]; ok && rawMax != nil {
		parsed, ok := fakeInt64(rawMax)
		if !ok || parsed < 0 {
			return fmt.Errorf("max-sessions must be non-negative numeric")
		}
		maxSessions = parsed
	}
	effectiveMax := maxSessions
	if effectiveMax == 0 {
		effectiveMax = 32
	}
	portMin := int64(0)
	if rawMin, ok := relay["udp-port-min"]; ok && rawMin != nil {
		parsed, ok := fakeInt64(rawMin)
		if !ok {
			return fmt.Errorf("udp-port-min invalid")
		}
		portMin = parsed
	}
	portMax := int64(0)
	if rawMax, ok := relay["udp-port-max"]; ok && rawMax != nil {
		parsed, ok := fakeInt64(rawMax)
		if !ok {
			return fmt.Errorf("udp-port-max invalid")
		}
		portMax = parsed
	}
	if portMin <= 0 || portMax <= 0 || portMin > portMax {
		return fmt.Errorf("oauth.providers.codex.live-media-relay udp-port-min and udp-port-max must form a valid port range when enabled")
	}
	if portMax-portMin+1 < effectiveMax*2 {
		return fmt.Errorf("oauth.providers.codex.live-media-relay UDP range requires at least %d ports for %d sessions", effectiveMax*2, effectiveMax)
	}
	return nil
}

func TestCoupledLiveMediaRelayMigratesAtomicallyUnderStrictValidation(t *testing.T) {
	t.Run("valid coupled group applies in single atomic PATCH", func(t *testing.T) {
		source := newFakeCPA(t, "src-key")
		source.config = map[string]any{
			"oauth": map[string]any{
				"providers": map[string]any{
					"codex": map[string]any{
						"live-media-relay": map[string]any{
							"enabled":                    true,
							"disable-private-remote-ips": true,
							"udp-port-min":               json.Number("40000"),
							"udp-port-max":               json.Number("40100"),
						},
					},
				},
			},
		}
		destination := destinationFixture(t)
		destination.validateLiveMediaRelay = true
		summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})

		var patchCount int
		for _, m := range destination.requestMethods() {
			if m == "PATCH /v8/management/config/oauth/providers/codex/live-media-relay" {
				patchCount++
			}
		}
		if patchCount != 1 {
			t.Fatalf("live-media-relay PATCH count = %d, want 1 atomic PATCH (%v)", patchCount, destination.requestMethods())
		}
		for _, path := range []string{
			"oauth.providers.codex.live-media-relay.enabled",
			"oauth.providers.codex.live-media-relay.disable-private-remote-ips",
			"oauth.providers.codex.live-media-relay.udp-port-min",
			"oauth.providers.codex.live-media-relay.udp-port-max",
		} {
			got := itemByName(t, summary, configLeafItemName(path))
			if got.Outcome != freshstore.AdoptionMigrated {
				t.Fatalf("%s outcome = %+v, want migrated", path, got)
			}
		}
		for _, absent := range []string{
			"oauth.providers.codex.live-media-relay.max-sessions",
			"oauth.providers.codex.live-media-relay.public-ip",
		} {
			got := itemByName(t, summary, configLeafItemName(absent))
			if got.Outcome != freshstore.AdoptionAbsentAtSource {
				t.Fatalf("%s outcome = %+v, want absent_at_source", absent, got)
			}
		}

		// Replay idempotently: zero additional PATCH requests.
		before := len(destination.requestMethods())
		replayed := runMigration(t, source, destination, summary)
		if !replayed.Finished || len(destination.requestMethods()) != before {
			t.Fatalf("replay mutated destination or did not finish: finished=%v methods=%v", replayed.Finished, destination.requestMethods()[before:])
		}
	})

	t.Run("cross-field rejection leaves zero half-applied state and maps manual reason", func(t *testing.T) {
		source := newFakeCPA(t, "src-key")
		source.config = map[string]any{
			"oauth": map[string]any{
				"providers": map[string]any{
					"codex": map[string]any{
						"live-media-relay": map[string]any{
							"enabled":      true,
							"udp-port-min": json.Number("40000"),
							"udp-port-max": json.Number("40010"), // too narrow for default 32 sessions (needs 64)
						},
					},
				},
			},
			"routing": map[string]any{
				"strategy": "round-robin",
			},
		}
		destination := destinationFixture(t)
		destination.validateLiveMediaRelay = true
		summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
		for _, path := range []string{
			"oauth.providers.codex.live-media-relay.enabled",
			"oauth.providers.codex.live-media-relay.udp-port-min",
			"oauth.providers.codex.live-media-relay.udp-port-max",
		} {
			got := itemByName(t, summary, configLeafItemName(path))
			if got.Outcome != freshstore.AdoptionManual || got.Reason != "reconfigure_after_adoption" {
				t.Fatalf("%s outcome = %+v, want manual reconfigure_after_adoption", path, got)
			}
		}
		if got := lookup(destination.config, []string{"oauth", "providers", "codex", "live-media-relay"}); got != nil {
			t.Fatalf("destination left half-applied live-media-relay state: %#v", got)
		}
		if got := itemByName(t, summary, configLeafItemName("routing.strategy")); got.Outcome != freshstore.AdoptionMigrated {
			t.Fatalf("subsequent leaf routing.strategy = %+v, want migrated", got)
		}
	})

	t.Run("local validation failure aborts atomic batch without half-writing siblings", func(t *testing.T) {
		source := newFakeCPA(t, "src-key")
		source.config = map[string]any{
			"oauth": map[string]any{
				"providers": map[string]any{
					"codex": map[string]any{
						"live-media-relay": map[string]any{
							"enabled":      true,
							"udp-port-min": "not-an-int",
							"udp-port-max": json.Number("40100"),
						},
					},
				},
			},
		}
		destination := destinationFixture(t)
		destination.validateLiveMediaRelay = true
		summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
		for _, m := range destination.requestMethods() {
			if m == "PATCH /v8/management/config/oauth/providers/codex/live-media-relay" {
				t.Fatalf("local validation failure must not issue live-media-relay PATCH (%v)", destination.requestMethods())
			}
		}
		if got := itemByName(t, summary, configLeafItemName("oauth.providers.codex.live-media-relay.udp-port-min")); got.Outcome != freshstore.AdoptionManual || got.Reason != "value_shape_unsupported" {
			t.Fatalf("invalid leaf = %+v, want manual value_shape_unsupported", got)
		}
		for _, sibling := range []string{
			"oauth.providers.codex.live-media-relay.enabled",
			"oauth.providers.codex.live-media-relay.udp-port-max",
		} {
			got := itemByName(t, summary, configLeafItemName(sibling))
			if got.Outcome != freshstore.AdoptionManual || got.Reason != "reconfigure_after_adoption" {
				t.Fatalf("sibling %s = %+v, want manual reconfigure_after_adoption", sibling, got)
			}
		}
	})
}

func TestNamedRootsOnlyWithNestedUnknownKeyEmitsStep3DecisionInventoryCountAndStep4ManualItem(t *testing.T) {
	source := newFakeCPA(t, "src-key")
	source.config = map[string]any{
		"config-version": 8,
		"server": map[string]any{
			"port": 8317,
		},
		"oauth": map[string]any{
			"auth-dir": "/source/auth",
		},
		"access": map[string]any{
			"api-keys": []any{"sk-client-1"},
		},
		"plugins": map[string]any{
			"enabled":      false,
			"mystery_flag": true,
		},
	}

	// (a) ReadV8Config emits unknown_cpa_native_resource, producing cpa-config-unknown decision.
	obs, err := cpa.ReadV8Config(t.Context(), source.server.URL, source.auth)
	if err != nil {
		t.Fatalf("ReadV8Config: %v", err)
	}
	if !slices.Contains(obs.ResourceSections, "unknown_cpa_native_resource") {
		t.Fatalf("ReadV8Config ResourceSections = %v, want unknown_cpa_native_resource", obs.ResourceSections)
	}
	refs := cpa.AdoptionRootRefs("unknown_cpa_native_resource")
	if !slices.Contains(refs, bootstrap.AdoptionRefConfigUnknown) {
		t.Fatalf("AdoptionRootRefs(unknown_cpa_native_resource) = %v, want cpa-config-unknown", refs)
	}
	decision, ok := bootstrap.FreshAdoptionDecision(bootstrap.AdoptionRefConfigUnknown, bootstrap.AdoptionResourceDecision{})
	if !ok || decision.Disposition != bootstrap.ManualAction || decision.ManualAction != "reconfigure_after_adoption" {
		t.Fatalf("FreshAdoptionDecision(cpa-config-unknown) = (%+v, %v)", decision, ok)
	}

	// (b) CountSourceInventory reports cpa-config-unknown: 1.
	srcEndpoint, err := NewSource(t.Context(), source.endpoint())
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	defer srcEndpoint.Close()
	counts := CountSourceInventory(t.Context(), srcEndpoint)
	if counts[string(bootstrap.AdoptionRefConfigUnknown)] != 1 {
		t.Fatalf("CountSourceInventory[cpa-config-unknown] = %d, want 1 (%v)", counts[string(bootstrap.AdoptionRefConfigUnknown)], counts)
	}

	// (c) Executor records durable AdoptionManual reconfigure_after_adoption item.
	destination := destinationFixture(t)
	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	if !summary.Finished || len(summary.Items) != summary.Planned {
		t.Fatalf("summary did not complete cleanly: finished=%v items=%d planned=%d", summary.Finished, len(summary.Items), summary.Planned)
	}
	unknownItem := itemByName(t, summary, unknownConfigItemName)
	if unknownItem.Kind != freshstore.AdoptionKindConfigFamily || unknownItem.Outcome != freshstore.AdoptionManual || unknownItem.Reason != "reconfigure_after_adoption" {
		t.Fatalf("unknown-config item = %+v, want manual reconfigure_after_adoption", unknownItem)
	}
	if !CanonicalItem(unknownItem) {
		t.Fatalf("CanonicalItem(%+v) = false, want true", unknownItem)
	}
	if ref := ItemRef(unknownItem); ref != bootstrap.AdoptionRefConfigUnknown {
		t.Fatalf("ItemRef(%+v) = %q, want %q", unknownItem, ref, bootstrap.AdoptionRefConfigUnknown)
	}
}

func TestUpstreamCustomErrorStringIsMappedToClosedAllowlistBeforeCheckpointing(t *testing.T) {
	source := newFakeCPA(t, "src-key")
	source.config = map[string]any{
		"routing": map[string]any{
			"strategy": "round-robin",
		},
	}
	destination := destinationFixture(t)
	destination.customWriteErrors["routing.strategy"] = struct {
		status int
		code   string
	}{
		status: http.StatusBadRequest,
		code:   "custom free-text code",
	}
	srcEndpoint, err := NewSource(t.Context(), source.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	defer srcEndpoint.Close()
	dstEndpoint, err := NewDestination(t.Context(), destination.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	defer dstEndpoint.Close()

	var checkpointed freshstore.AdoptionSummary
	result, err := New(srcEndpoint, dstEndpoint, migratingDecisions()).Run(t.Context(), freshstore.AdoptionSummary{}, func(current freshstore.AdoptionSummary) error {
		checkpointed = current
		return nil
	})
	if err != nil {
		t.Fatalf("Executor.Run: %v", err)
	}
	for _, summary := range []freshstore.AdoptionSummary{result.Summary, checkpointed} {
		item := itemByName(t, summary, configLeafItemName("routing.strategy"))
		if item.Outcome != freshstore.AdoptionManual || item.Reason != "reconfigure_after_adoption" {
			t.Fatalf("persisted item = %+v, want manual with Reason=reconfigure_after_adoption", item)
		}
	}
}

func TestArchivedUnknownConfigInYAMLSurfacesInInventoryCountAndExecutor(t *testing.T) {
	source := newFakeCPA(t, "src-key")
	source.config = map[string]any{
		"config-version": 8,
		"server":         map[string]any{"port": 8317},
		"oauth":          map[string]any{"auth-dir": "/source/auth"},
		"access":         map[string]any{"api-keys": []any{"sk-client-1"}},
		"upstream": map[string]any{
			"codex": map[string]any{
				"disable-codex-cloaking": true,
			},
		},
		"plugins": map[string]any{},
	}
	source.configYAMLOverride = `# operator comment
config-version: 8
server:
    port: 8317
oauth: {auth-dir: "/source/auth", providers: {codex: {}}}
plugins: {}
upstream:
    codex:
        disable-codex-cloaking: true

# oauth.providers.codex.retired-setting: false
# plugins.mystery_flag: true
`

	snapshot, err := cpa.ReadV8AdoptionSnapshot(t.Context(), source.server.URL, source.auth)
	if err != nil {
		t.Fatalf("ReadV8AdoptionSnapshot: %v", err)
	}
	if !slices.Contains(snapshot.ResourceSections, "unknown_cpa_native_resource") {
		t.Fatalf("ResourceSections = %v, want unknown_cpa_native_resource", snapshot.ResourceSections)
	}
	refs := cpa.AdoptionRootRefs("unknown_cpa_native_resource")
	if !slices.Contains(refs, bootstrap.AdoptionRefConfigUnknown) {
		t.Fatalf("AdoptionRootRefs(unknown_cpa_native_resource) = %v, want cpa-config-unknown", refs)
	}

	srcEndpoint, err := NewSource(t.Context(), source.endpoint())
	if err != nil {
		t.Fatalf("NewSource: %v", err)
	}
	defer srcEndpoint.Close()
	counts := CountSourceInventory(t.Context(), srcEndpoint)
	if val, ok := counts[string(bootstrap.AdoptionRefConfigUnknown)]; ok {
		t.Fatalf("CountSourceInventory[cpa-config-unknown] = (%d, true), want omitted when archive candidates exist (%v)", val, counts)
	}

	destination := destinationFixture(t)
	summary := runMigration(t, source, destination, freshstore.AdoptionSummary{})
	if !summary.Finished || len(summary.Items) != summary.Planned {
		t.Fatalf("summary did not complete cleanly: finished=%v items=%d planned=%d", summary.Finished, len(summary.Items), summary.Planned)
	}
	if item := itemByName(t, summary, configLeafItemName("upstream.codex.disable-codex-cloaking")); item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("disable-codex-cloaking = %+v, want migrated", item)
	}
	unknownItem := itemByName(t, summary, unknownConfigItemName)
	if unknownItem.Kind != freshstore.AdoptionKindConfigFamily || unknownItem.Outcome != freshstore.AdoptionManual || unknownItem.Reason != "reconfigure_after_adoption" {
		t.Fatalf("unknown-config item = %+v, want manual reconfigure_after_adoption", unknownItem)
	}
	if !strings.Contains(unknownItem.Instruction, "archived or commented-out settings") {
		t.Fatalf("unknown-config instruction = %q, want archive candidate wording", unknownItem.Instruction)
	}
}

func TestArchivedUnknownScanFailureAndSizeBoundaryFailClosedWithoutFalseZero(t *testing.T) {
	t.Run("failed or timed out config.yaml omits exact count and records manual incomplete-scan item", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			hang bool
		}{
			{name: "http 500"},
			{name: "timeout", hang: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				source := newFakeCPA(t, "src-key")
				source.config = map[string]any{
					"config-version": 8,
					"server":         map[string]any{"port": 8317},
					"oauth":          map[string]any{"auth-dir": "/source/auth"},
					"access":         map[string]any{"api-keys": []any{"sk-client-1"}},
					"routing":        map[string]any{"strategy": "round-robin"},
				}
				source.failConfigYAML = !tc.hang
				source.hangConfigYAML = tc.hang

				snapshotCtx, cancelSnapshot := context.WithTimeout(t.Context(), 150*time.Millisecond)
				defer cancelSnapshot()
				if !tc.hang {
					snapshotCtx = t.Context()
				}
				snapshot, err := cpa.ReadV8AdoptionSnapshot(snapshotCtx, source.server.URL, source.auth)
				if err != nil {
					t.Fatalf("ReadV8AdoptionSnapshot: %v", err)
				}
				if !slices.Contains(snapshot.ResourceSections, "unknown_cpa_native_resource") {
					t.Fatalf("ResourceSections = %v, want unknown_cpa_native_resource", snapshot.ResourceSections)
				}

				srcEndpoint, err := NewSource(t.Context(), source.endpoint())
				if err != nil {
					t.Fatalf("NewSource: %v", err)
				}
				defer srcEndpoint.Close()
				if tc.hang {
					srcEndpoint.w.client.Timeout = 80 * time.Millisecond
				}

				counts := CountSourceInventory(t.Context(), srcEndpoint)
				if val, ok := counts[string(bootstrap.AdoptionRefConfigUnknown)]; ok {
					t.Fatalf("CountSourceInventory[cpa-config-unknown] = (%d, true), want omitted on incomplete scan (%v)", val, counts)
				}

				dstEndpoint, err := NewDestination(t.Context(), destinationFixture(t).endpoint())
				if err != nil {
					t.Fatalf("NewDestination: %v", err)
				}
				defer dstEndpoint.Close()
				result, err := New(srcEndpoint, dstEndpoint, migratingDecisions()).Run(t.Context(), freshstore.AdoptionSummary{}, nil)
				if err != nil {
					t.Fatalf("Executor.Run: %v", err)
				}
				unknownItem := itemByName(t, result.Summary, unknownConfigItemName)
				if unknownItem.Outcome != freshstore.AdoptionManual || unknownItem.Reason != "reconfigure_after_adoption" {
					t.Fatalf("unknown-config item = %+v, want manual reconfigure_after_adoption", unknownItem)
				}
				if !strings.Contains(unknownItem.Instruction, "could not be completely scanned") {
					t.Fatalf("unknown-config instruction = %q, want incomplete-scan wording", unknownItem.Instruction)
				}
			})
		}
	})

	t.Run("1.2 MiB YAML between 1 MiB snapshot cap and 8 MiB transport cap distinguishes clean zero from archived unknown", func(t *testing.T) {
		headerPadding := strings.Repeat("# operator header padding line for bounded read boundary verification 0123456789\n", 16000)
		activeYAML := `config-version: 8
server:
    port: 8317
oauth:
    auth-dir: /source/auth
access:
    api-keys:
        - sk-client-1
plugins: {}
`
		if len(headerPadding) <= 1<<20 || len(headerPadding)+len(activeYAML) >= 8<<20 {
			t.Fatalf("unexpected YAML test size: %d", len(headerPadding)+len(activeYAML))
		}

		for _, withArchived := range []bool{false, true} {
			name := "clean 1.2 MiB YAML"
			yamlBody := headerPadding + activeYAML
			if withArchived {
				name = "dirty 1.2 MiB YAML"
				yamlBody += "\n# plugins.retired.option: true\n"
			}
			t.Run(name, func(t *testing.T) {
				source := newFakeCPA(t, "src-key")
				source.config = map[string]any{
					"config-version": 8,
					"server":         map[string]any{"port": 8317},
					"oauth":          map[string]any{"auth-dir": "/source/auth"},
					"access":         map[string]any{"api-keys": []any{"sk-client-1"}},
					"plugins":        map[string]any{},
				}
				source.configYAMLOverride = yamlBody

				// Step 1/3 snapshot probe reads up to 1 MiB -> fails closed on >1 MiB YAML.
				snapshot, err := cpa.ReadV8AdoptionSnapshot(t.Context(), source.server.URL, source.auth)
				if err != nil {
					t.Fatalf("ReadV8AdoptionSnapshot: %v", err)
				}
				if !slices.Contains(snapshot.ResourceSections, "unknown_cpa_native_resource") {
					t.Fatalf("ResourceSections = %v, want unknown_cpa_native_resource above 1 MiB cap", snapshot.ResourceSections)
				}

				// Inventory count & executor read up to 8 MiB -> exact 0 when clean, omitted/manual when archived.
				srcEndpoint, err := NewSource(t.Context(), source.endpoint())
				if err != nil {
					t.Fatalf("NewSource: %v", err)
				}
				defer srcEndpoint.Close()
				counts := CountSourceInventory(t.Context(), srcEndpoint)
				val, ok := counts[string(bootstrap.AdoptionRefConfigUnknown)]
				if withArchived {
					if ok {
						t.Fatalf("CountSourceInventory[cpa-config-unknown] = (%d, true), want omitted for archived 1.2 MiB YAML", val)
					}
				} else if !ok || val != 0 {
					t.Fatalf("CountSourceInventory[cpa-config-unknown] = (%d, %v), want (0, true) for clean 1.2 MiB YAML", val, ok)
				}

				summary := runMigration(t, source, destinationFixture(t), freshstore.AdoptionSummary{})
				item, exists := summary.Item(unknownConfigItemName)
				if withArchived {
					if !exists || item.Outcome != freshstore.AdoptionManual || item.Reason != "reconfigure_after_adoption" {
						t.Fatalf("dirty 1.2 MiB unknown-config item = (%+v, %v), want manual reconfigure_after_adoption", item, exists)
					}
				} else if exists {
					t.Fatalf("clean 1.2 MiB YAML unexpectedly recorded family:unknown-config: %+v", item)
				}
			})
		}
	})
}

func TestRealCPAV8ArchivedUnknownConfigEndToEnd(t *testing.T) {
	cpaBin := strings.TrimSpace(os.Getenv("CPAMP_TEST_CPA_V8"))
	if cpaBin == "" {
		t.Skip("CPAMP_TEST_CPA_V8 is unset")
	}

	startRealCPA := func(t *testing.T, configTemplate string, mgmtKey string) (string, *http.Client) {
		t.Helper()
		dir := t.TempDir()
		authDir := filepath.Join(dir, "auth")
		if err := os.MkdirAll(authDir, 0o700); err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := l.Addr().String()
		_, portStr, _ := net.SplitHostPort(addr)
		_ = l.Close()

		cfgText := strings.NewReplacer("{PORT}", portStr, "{AUTH_DIR}", authDir).Replace(configTemplate)
		cfgPath := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(cfgPath, []byte(cfgText), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(cpaBin, "-config", cfgPath)
		cmd.Dir = dir
		if err := cmd.Start(); err != nil {
			t.Fatalf("start CPA binary %s: %v", cpaBin, err)
		}
		t.Cleanup(func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})

		baseURL := "http://" + addr
		client := &http.Client{Timeout: 2 * time.Second}
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			req, _ := http.NewRequest(http.MethodGet, baseURL+"/v8/management/config", nil)
			req.Header.Set("Authorization", "Bearer "+mgmtKey)
			resp, err := client.Do(req)
			if err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
				_ = resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					return baseURL, client
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("CPA at %s did not become ready in time", baseURL)
		return "", nil
	}

	t.Run("case A: strips flow and block unknown keys on disk and surfaces them in discovery, inventory count, and executor", func(t *testing.T) {
		const sourceMgmtKey = "real-cpa-source-mgmt"
		const destMgmtKey = "real-cpa-dest-mgmt"
		srcURL, _ := startRealCPA(t, `# operator comment
config-version: 8
server:
  host: 127.0.0.1
  port: {PORT}
management:
  secret-key: `+sourceMgmtKey+`
  allow-remote: true
  disable-control-panel: true
access:
  api-keys:
    - "sk-client-1"
oauth: {auth-dir: "{AUTH_DIR}", providers: {codex: {disable-codex-cloaking: true, retired-setting: false}}}
plugins:
  mystery_flag: true
`, sourceMgmtKey)

		dstURL, _ := startRealCPA(t, `config-version: 8
server:
  host: 127.0.0.1
  port: {PORT}
management:
  secret-key: `+destMgmtKey+`
  allow-remote: true
  disable-control-panel: true
access:
  api-keys:
    - "sk-client-1"
oauth:
  auth-dir: "{AUTH_DIR}"
`, destMgmtKey)

		// 1) ReadV8AdoptionSnapshot includes unknown_cpa_native_resource and Step 3 produces manual_action.
		snapshot, err := cpa.ReadV8AdoptionSnapshot(t.Context(), srcURL, sourceMgmtKey)
		if err != nil {
			t.Fatalf("ReadV8AdoptionSnapshot: %v", err)
		}
		if !slices.Contains(snapshot.ResourceSections, "unknown_cpa_native_resource") {
			t.Fatalf("ResourceSections = %v, want unknown_cpa_native_resource", snapshot.ResourceSections)
		}
		refs := cpa.AdoptionRootRefs("unknown_cpa_native_resource")
		if !slices.Contains(refs, bootstrap.AdoptionRefConfigUnknown) {
			t.Fatalf("AdoptionRootRefs(unknown_cpa_native_resource) = %v, want cpa-config-unknown", refs)
		}
		decision, ok := bootstrap.FreshAdoptionDecision(bootstrap.AdoptionRefConfigUnknown, bootstrap.AdoptionResourceDecision{})
		if !ok || decision.Disposition != bootstrap.ManualAction || decision.ManualAction != bootstrap.ReconfigureAfterAdoption {
			t.Fatalf("FreshAdoptionDecision(cpa-config-unknown) = (%+v, %v)", decision, ok)
		}

		// 2) CountSourceInventory reports cpa-config-unknown == 2.
		srcWire, err := NewSource(t.Context(), Endpoint{BaseURL: srcURL, ManagementKey: sourceMgmtKey})
		if err != nil {
			t.Fatalf("NewSource: %v", err)
		}
		defer srcWire.Close()
		counts := CountSourceInventory(t.Context(), srcWire)
		if val, ok := counts[string(bootstrap.AdoptionRefConfigUnknown)]; ok {
			t.Fatalf("CountSourceInventory[cpa-config-unknown] = (%d, true), want omitted when archive candidates exist (%v)", val, counts)
		}

		// 3) Executor against real CPA v8.0.55 destination migrates disable-codex-cloaking and records family:unknown-config manual.
		dstWire, err := NewDestination(t.Context(), Endpoint{BaseURL: dstURL, ManagementKey: destMgmtKey})
		if err != nil {
			t.Fatalf("NewDestination: %v", err)
		}
		defer dstWire.Close()
		result, err := New(srcWire, dstWire, migratingDecisions()).Run(t.Context(), freshstore.AdoptionSummary{}, nil)
		if err != nil {
			t.Fatalf("Executor.Run: %v", err)
		}
		if !result.Summary.Finished || len(result.Summary.Items) != result.Summary.Planned {
			t.Fatalf("summary incomplete: finished=%v items=%d planned=%d", result.Summary.Finished, len(result.Summary.Items), result.Summary.Planned)
		}
		if cloaking := itemByName(t, result.Summary, configLeafItemName("upstream.codex.disable-codex-cloaking")); cloaking.Outcome != freshstore.AdoptionMigrated {
			t.Fatalf("upstream.codex.disable-codex-cloaking = %+v, want migrated", cloaking)
		}
		if val, presence, readErr := dstWire.ReadConfigValue(t.Context(), "upstream.codex.disable-codex-cloaking"); readErr != nil || presence != cpa.ConfigPresent || val != true {
			t.Fatalf("destination readback upstream.codex.disable-codex-cloaking = (%v, %v, %v), want true", val, presence, readErr)
		}
		unknownItem := itemByName(t, result.Summary, unknownConfigItemName)
		if unknownItem.Kind != freshstore.AdoptionKindConfigFamily || unknownItem.Outcome != freshstore.AdoptionManual || unknownItem.Reason != "reconfigure_after_adoption" {
			t.Fatalf("unknown-config item = %+v, want manual reconfigure_after_adoption", unknownItem)
		}
	})

	t.Run("case B: clean v8 config with operator comment and allow-private-remote-ips alias has zero unknown keys", func(t *testing.T) {
		const sourceMgmtKey = "real-cpa-clean-source-mgmt"
		const destMgmtKey = "real-cpa-clean-dest-mgmt"
		srcURL, _ := startRealCPA(t, `# operator comment
config-version: 8
server:
  host: 127.0.0.1
  port: {PORT}
management:
  secret-key: `+sourceMgmtKey+`
  allow-remote: true
  disable-control-panel: true
access:
  api-keys:
    - "sk-client-1"
oauth:
  auth-dir: "{AUTH_DIR}"
  providers:
    codex:
      live-media-relay:
        allow-private-remote-ips: true
`, sourceMgmtKey)

		dstURL, _ := startRealCPA(t, `config-version: 8
server:
  host: 127.0.0.1
  port: {PORT}
management:
  secret-key: `+destMgmtKey+`
  allow-remote: true
  disable-control-panel: true
access:
  api-keys:
    - "sk-client-1"
oauth:
  auth-dir: "{AUTH_DIR}"
`, destMgmtKey)

		snapshot, err := cpa.ReadV8AdoptionSnapshot(t.Context(), srcURL, sourceMgmtKey)
		if err != nil {
			t.Fatalf("ReadV8AdoptionSnapshot: %v", err)
		}
		if slices.Contains(snapshot.ResourceSections, "unknown_cpa_native_resource") {
			t.Fatalf("clean source unexpectedly reported unknown_cpa_native_resource: %v", snapshot.ResourceSections)
		}
		for _, section := range snapshot.ResourceSections {
			if slices.Contains(cpa.AdoptionRootRefs(section), bootstrap.AdoptionRefConfigUnknown) {
				t.Fatalf("clean source section %q unexpectedly mapped to cpa-config-unknown (%v)", section, snapshot.ResourceSections)
			}
		}

		srcWire, err := NewSource(t.Context(), Endpoint{BaseURL: srcURL, ManagementKey: sourceMgmtKey})
		if err != nil {
			t.Fatalf("NewSource: %v", err)
		}
		defer srcWire.Close()
		counts := CountSourceInventory(t.Context(), srcWire)
		if val, ok := counts[string(bootstrap.AdoptionRefConfigUnknown)]; !ok || val != 0 {
			t.Fatalf("CountSourceInventory[cpa-config-unknown] = (%d, %v), want (0, true) (%v)", val, ok, counts)
		}

		dstWire, err := NewDestination(t.Context(), Endpoint{BaseURL: dstURL, ManagementKey: destMgmtKey})
		if err != nil {
			t.Fatalf("NewDestination: %v", err)
		}
		defer dstWire.Close()
		result, err := New(srcWire, dstWire, migratingDecisions()).Run(t.Context(), freshstore.AdoptionSummary{}, nil)
		if err != nil {
			t.Fatalf("Executor.Run: %v", err)
		}
		if item, exists := result.Summary.Item(unknownConfigItemName); exists {
			t.Fatalf("clean source unexpectedly recorded family:unknown-config item: %+v", item)
		}
	})

	t.Run("case C: real CPA source with only named roots in JSON still surfaces archived unknown YAML key in ReadV8AdoptionSnapshot", func(t *testing.T) {
		const sourceMgmtKey = "real-cpa-archived-only-mgmt"
		srcURL, _ := startRealCPA(t, `# operator comment
config-version: 8
server:
  host: 127.0.0.1
  port: {PORT}
management:
  secret-key: `+sourceMgmtKey+`
  allow-remote: true
  disable-control-panel: true
access:
  api-keys:
    - "sk-client-1"
oauth: {auth-dir: "{AUTH_DIR}", providers: {codex: {retired-setting: false}}}
plugins:
  mystery_flag: true
`, sourceMgmtKey)

		snapshot, err := cpa.ReadV8AdoptionSnapshot(t.Context(), srcURL, sourceMgmtKey)
		if err != nil {
			t.Fatalf("ReadV8AdoptionSnapshot: %v", err)
		}
		if !slices.Contains(snapshot.ResourceSections, "unknown_cpa_native_resource") {
			t.Fatalf("ResourceSections = %v, want unknown_cpa_native_resource", snapshot.ResourceSections)
		}
		srcWire, err := NewSource(t.Context(), Endpoint{BaseURL: srcURL, ManagementKey: sourceMgmtKey})
		if err != nil {
			t.Fatalf("NewSource: %v", err)
		}
		defer srcWire.Close()
		counts := CountSourceInventory(t.Context(), srcWire)
		if val, ok := counts[string(bootstrap.AdoptionRefConfigUnknown)]; ok {
			t.Fatalf("CountSourceInventory[cpa-config-unknown] = (%d, true), want omitted when archive candidates exist (%v)", val, counts)
		}
	})

	t.Run("case D: real CPA strips literal dotted key under plugins and archives it cleanly", func(t *testing.T) {
		const sourceMgmtKey = "real-cpa-dotted-plugins-mgmt"
		const destMgmtKey = "real-cpa-dotted-plugins-dest"
		srcURL, _ := startRealCPA(t, `config-version: 8
server:
  host: 127.0.0.1
  port: {PORT}
management:
  secret-key: `+sourceMgmtKey+`
  allow-remote: true
  disable-control-panel: true
access:
  api-keys:
    - "sk-client-1"
oauth:
  auth-dir: "{AUTH_DIR}"
plugins:
  retired.option: true
`, sourceMgmtKey)
		dstURL, _ := startRealCPA(t, `config-version: 8
server:
  host: 127.0.0.1
  port: {PORT}
management:
  secret-key: `+destMgmtKey+`
  allow-remote: true
  disable-control-panel: true
access:
  api-keys:
    - "sk-client-1"
oauth:
  auth-dir: "{AUTH_DIR}"
`, destMgmtKey)

		snapshot, err := cpa.ReadV8AdoptionSnapshot(t.Context(), srcURL, sourceMgmtKey)
		if err != nil {
			t.Fatalf("ReadV8AdoptionSnapshot: %v", err)
		}
		if !slices.Contains(snapshot.ResourceSections, "unknown_cpa_native_resource") {
			t.Fatalf("ResourceSections = %v, want unknown_cpa_native_resource", snapshot.ResourceSections)
		}
		srcWire, err := NewSource(t.Context(), Endpoint{BaseURL: srcURL, ManagementKey: sourceMgmtKey})
		if err != nil {
			t.Fatalf("NewSource: %v", err)
		}
		defer srcWire.Close()
		counts := CountSourceInventory(t.Context(), srcWire)
		if val, ok := counts[string(bootstrap.AdoptionRefConfigUnknown)]; ok {
			t.Fatalf("CountSourceInventory[cpa-config-unknown] = (%d, true), want omitted (%v)", val, counts)
		}

		dstWire, err := NewDestination(t.Context(), Endpoint{BaseURL: dstURL, ManagementKey: destMgmtKey})
		if err != nil {
			t.Fatalf("NewDestination: %v", err)
		}
		defer dstWire.Close()
		result, err := New(srcWire, dstWire, migratingDecisions()).Run(t.Context(), freshstore.AdoptionSummary{}, nil)
		if err != nil {
			t.Fatalf("Executor.Run: %v", err)
		}
		unknownItem := itemByName(t, result.Summary, unknownConfigItemName)
		if unknownItem.Kind != freshstore.AdoptionKindConfigFamily || unknownItem.Outcome != freshstore.AdoptionManual || unknownItem.Reason != "reconfigure_after_adoption" {
			t.Fatalf("unknown-config item = %+v, want manual reconfigure_after_adoption", unknownItem)
		}
	})

	t.Run("case E: real CPA with trailing operator # key: value comment omits exact count and records manual item", func(t *testing.T) {
		const sourceMgmtKey = "real-cpa-trailing-comment-mgmt"
		const destMgmtKey = "real-cpa-trailing-comment-dest"
		srcURL, _ := startRealCPA(t, `config-version: 8
server:
  host: 127.0.0.1
  port: {PORT}
management:
  secret-key: `+sourceMgmtKey+`
  allow-remote: true
  disable-control-panel: true
access:
  api-keys:
    - "sk-client-1"
oauth:
  auth-dir: "{AUTH_DIR}"
# custom-operator-note: true
`, sourceMgmtKey)
		dstURL, _ := startRealCPA(t, `config-version: 8
server:
  host: 127.0.0.1
  port: {PORT}
management:
  secret-key: `+destMgmtKey+`
  allow-remote: true
  disable-control-panel: true
access:
  api-keys:
    - "sk-client-1"
oauth:
  auth-dir: "{AUTH_DIR}"
`, destMgmtKey)

		snapshot, err := cpa.ReadV8AdoptionSnapshot(t.Context(), srcURL, sourceMgmtKey)
		if err != nil {
			t.Fatalf("ReadV8AdoptionSnapshot: %v", err)
		}
		if !slices.Contains(snapshot.ResourceSections, "unknown_cpa_native_resource") {
			t.Fatalf("ResourceSections = %v, want unknown_cpa_native_resource", snapshot.ResourceSections)
		}
		srcWire, err := NewSource(t.Context(), Endpoint{BaseURL: srcURL, ManagementKey: sourceMgmtKey})
		if err != nil {
			t.Fatalf("NewSource: %v", err)
		}
		defer srcWire.Close()
		counts := CountSourceInventory(t.Context(), srcWire)
		if val, ok := counts[string(bootstrap.AdoptionRefConfigUnknown)]; ok {
			t.Fatalf("CountSourceInventory[cpa-config-unknown] = (%d, true), want omitted (%v)", val, counts)
		}

		dstWire, err := NewDestination(t.Context(), Endpoint{BaseURL: dstURL, ManagementKey: destMgmtKey})
		if err != nil {
			t.Fatalf("NewDestination: %v", err)
		}
		defer dstWire.Close()
		result, err := New(srcWire, dstWire, migratingDecisions()).Run(t.Context(), freshstore.AdoptionSummary{}, nil)
		if err != nil {
			t.Fatalf("Executor.Run: %v", err)
		}
		unknownItem := itemByName(t, result.Summary, unknownConfigItemName)
		if unknownItem.Kind != freshstore.AdoptionKindConfigFamily || unknownItem.Outcome != freshstore.AdoptionManual || unknownItem.Reason != "reconfigure_after_adoption" {
			t.Fatalf("unknown-config item = %+v, want manual reconfigure_after_adoption", unknownItem)
		}
	})
}
