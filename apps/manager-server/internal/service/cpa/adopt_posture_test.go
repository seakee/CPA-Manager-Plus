package cpa

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// adoptDestinationConfig is a destination staged by an adoption: the Supervisor
// template with the install-derived management credential, carrying the source's
// client keys and two root sections an adoption legitimately brings along.
const adoptDestinationConfig = `{"config-version":8,"server":{"host":"0.0.0.0","port":8317},"management":{"allow-remote":true,"secret-key":"$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12","disable-control-panel":true,"disable-auto-update-panel":true},"access":{"api-keys":[]},"routing":{"strategy":"round-robin"},"client":{"codex":{"enable-apply-patch":true}},"oauth":{"auth-dir":"/runtime/supervisor/destination/auth"},"plugins":{"enabled":false,"dir":"/runtime/supervisor/plugins"},"observability":{"logs":{"logging-to-file":false},"usage":{"usage-statistics-enabled":true}}}`

func TestAdoptPostureAcceptsTheAdoptedDestination(t *testing.T) {
	adopt := adoptProofPosture()
	if !adopt.matches([]byte(adoptDestinationConfig)) {
		t.Fatal("adopted destination rejected")
	}
	// An adoption may also carry source server settings (such as commercial-mode)
	// that Supervisor never stages on a New Managed listener.
	adoptedServer := strings.Replace(adoptDestinationConfig, `"port":8317`, `"port":8317,"commercial-mode":true`, 1)
	if !adopt.matches([]byte(adoptedServer)) {
		t.Fatal("adopted destination with commercial-mode rejected")
	}
	if managedProofPosture.matches([]byte(adoptedServer)) {
		t.Fatal("managed posture accepted a non-staged server setting")
	}
	// The plugin switch is the operator's: the plugin page opens for an adopted
	// installation, so saving either value must leave the posture proven. What
	// stays pinned is the Supervisor directory and the closed key set.
	for _, enabled := range []string{"true", "false"} {
		document := strings.Replace(adoptDestinationConfig, `"enabled":false`, `"enabled":`+enabled, 1)
		if !adoptProofPosture().matches([]byte(document)) {
			t.Fatalf("adopted destination rejected the plugin switch %s", enabled)
		}
	}
	for _, enabled := range []string{"true", "false"} {
		document := strings.Replace(adoptDestinationConfig, `"logging-to-file":false`, `"logging-to-file":`+enabled, 1)
		if !adoptProofPosture().matches([]byte(document)) {
			t.Fatalf("adopted destination rejected logging-to-file %s", enabled)
		}
	}
	for name, host := range map[string]string{"ipv4": "127.0.0.1", "ipv6": "::1"} {
		native := strings.NewReplacer(
			`"host":"0.0.0.0"`, `"host":"`+host+`"`,
			`"allow-remote":true`, `"allow-remote":false`,
		).Replace(adoptDestinationConfig)
		if !adoptProofPosture().matches([]byte(native)) {
			t.Fatalf("adopted destination rejected native loopback (%s)", name)
		}
	}
	for name, body := range map[string]string{
		"plugins enabled with installed plugin config and store sources": strings.Replace(
			adoptDestinationConfig,
			`"enabled":false,"dir":"/runtime/supervisor/plugins"`,
			`"enabled":true,"dir":"/runtime/supervisor/plugins","configs":{"sample":{"enabled":true}},"store-sources":["https://plugins.example/index.json"]`,
			1,
		),
		"models root override": strings.Replace(
			adoptDestinationConfig,
			`"routing":`,
			`"models":{"catalog":"builtin","codex-catalog":"remote"},"routing":`,
			1,
		),
		"retained safe server settings and disabled discovery/tls/kv-store": strings.Replace(
			adoptDestinationConfig,
			`"port":8317`,
			`"port":8317,"commercial-mode":true,"max-request-body-bytes":10485760,"trusted-proxies":["10.0.0.0/8"],"response-compression":{"enabled":true,"min-bytes":512},"github-token":"ghp_example","discovery":{"service-type":"_ai-gateway._tcp","subtypes":["_chat-completions"]},"tls":{"enable":false},"kv-store":{"disabled":false}`,
			1,
		),
	} {
		if !adoptProofPosture().matches([]byte(body)) {
			t.Fatalf("allowed adopted configuration %q rejected", name)
		}
	}
}

// TestAdoptPostureAcceptsThePluginGroupTheOperatorSaves is the P1 regression: the
// plugin page writes `plugins.enabled` on an adopted installation, so a proof that
// pinned the recorded opt-in failed for every request after one save. Admission
// and the Key snapshot read the same posture, so both are proven here after the
// switch moves in either direction.
func TestAdoptPostureAcceptsThePluginGroupTheOperatorSaves(t *testing.T) {
	keys := []string{"source-key-one", "source-key-two"}
	list, _ := json.Marshal(keys)
	root := strings.Replace(adoptDestinationConfig, `"api-keys":[]`, `"api-keys":`+string(list), 1)
	var served atomic.Value
	served.Store(root)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v8/management/config":
			w.Write([]byte(served.Load().(string)))
		case "/v8/management/config/access/api-keys":
			json.NewEncoder(w).Encode(keys)
		case "/v8/management/config.yaml":
			w.Write([]byte("adopted exact YAML bytes"))
		default:
			t.Error("unexpected request")
		}
	}))
	defer server.Close()
	// The posture is resolved from the committed record, exactly as the default
	// profile installs it.
	transport := NewTransportForEndpoint(server.URL)
	transport.SetPostureSelector(func() (DestinationPosture, bool) { return PostureFor("adopt_existing") })
	// The recorded opt-in was `false`; the operator saves the plugin group on.
	served.Store(strings.Replace(root, `"enabled":false`, `"enabled":true`, 1))
	if snap, err := transport.KeySnapshot(t.Context(), testManagement()); err != nil || len(snap.Keys) != 2 {
		t.Fatalf("a saved plugin switch broke the adopted proof: %v %+v", err, snap)
	}
	// Installing a plugin writes `plugins.configs` (and optional `store-sources`).
	served.Store(strings.Replace(
		root,
		`"enabled":false,"dir":"/runtime/supervisor/plugins"`,
		`"enabled":true,"dir":"/runtime/supervisor/plugins","configs":{"sample":{"enabled":true}},"store-sources":["https://plugins.example/index.json"]`,
		1,
	))
	if snap, err := transport.KeySnapshot(t.Context(), testManagement()); err != nil || len(snap.Keys) != 2 {
		t.Fatalf("an installed plugin config broke the adopted proof: %v %+v", err, snap)
	}
	// And back off again.
	served.Store(root)
	if snap, err := transport.KeySnapshot(t.Context(), testManagement()); err != nil || len(snap.Keys) != 2 {
		t.Fatalf("the recorded switch broke the adopted proof: %v %+v", err, snap)
	}
}

func TestAdoptPostureRefusesWhatTheManagerNeverStaged(t *testing.T) {
	for name, body := range map[string]string{
		// A root CPA itself does not read is a document this Manager never proved.
		"unknown root":       strings.Replace(adoptDestinationConfig, `"routing":`, `"foreign":{},"routing":`, 1),
		"missing management": strings.Replace(adoptDestinationConfig, `"management":`, `"unused":`, 1),
		"missing usage":      strings.Replace(adoptDestinationConfig, `"observability":`, `"unused":`, 1),
		"usage off":          strings.Replace(adoptDestinationConfig, `"usage-statistics-enabled":true`, `"usage-statistics-enabled":false`, 1),
		"plaintext secret":   strings.Replace(adoptDestinationConfig, `"$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12"`, `"plaintext-management-key"`, 1),
		"remote disabled":    strings.Replace(adoptDestinationConfig, `"allow-remote":true`, `"allow-remote":false`, 1),
		"loopback host":      strings.Replace(adoptDestinationConfig, `"host":"0.0.0.0"`, `"host":"127.0.0.1"`, 1),
		"non-canonical loopback 127.0.0.2": strings.NewReplacer(
			`"host":"0.0.0.0"`, `"host":"127.0.0.2"`,
			`"allow-remote":true`, `"allow-remote":false`,
		).Replace(adoptDestinationConfig),
		"foreign plugin dir":            strings.Replace(adoptDestinationConfig, `"/runtime/supervisor/plugins"`, `"/tmp/plugins"`, 1),
		"foreign auth dir":              strings.Replace(adoptDestinationConfig, `"/runtime/supervisor/destination/auth"`, `"/tmp/auth"`, 1),
		"non-boolean switch":            strings.Replace(adoptDestinationConfig, `"enabled":false`, `"enabled":"false"`, 1),
		"non-boolean logging-to-file":   strings.Replace(adoptDestinationConfig, `"logging-to-file":false`, `"logging-to-file":"true"`, 1),
		"missing logging-to-file":       strings.Replace(adoptDestinationConfig, `"logging-to-file":false`, `"debug":true`, 1),
		"unknown observability log key": strings.Replace(adoptDestinationConfig, `"logging-to-file":false`, `"logging-to-file":true,"log-dir":"/tmp/logs"`, 1),
		"foreign version":               strings.Replace(adoptDestinationConfig, `"config-version":8`, `"config-version":7`, 1),
		"non-object document":           `[]`,
		// The plugin root is the Supervisor's: a store source, a per-plugin
		// config or an auth revision is a plugin configuration this Manager never
		// wrote, so it is refused even though the switch itself is free.
		"foreign plugin key": strings.Replace(adoptDestinationConfig, `"enabled":false`, `"enabled":false,"store":{"repository":"https://plugins.example"`, 1),
		"plugin auth":        strings.Replace(adoptDestinationConfig, `"enabled":false`, `"enabled":false,"auth":true`, 1),
		// `KeySnapshot` proves by blanking the client list, so an extra access
		// leaf must be refused rather than hidden by that rewrite.
		"extra access key":                                   strings.Replace(adoptDestinationConfig, `"api-keys":[]`, `"api-keys":[],"extra":true`, 1),
		"server tls enabled":                                 strings.Replace(adoptDestinationConfig, `"port":8317`, `"port":8317,"tls":{"enable":true}`, 1),
		"server discovery enabled":                           strings.Replace(adoptDestinationConfig, `"port":8317`, `"port":8317,"discovery":{"enabled":true}`, 1),
		"server kv-store custom path":                        strings.Replace(adoptDestinationConfig, `"port":8317`, `"port":8317,"kv-store":{"path":"/tmp/kv.db"}`, 1),
		"server unknown key":                                 strings.Replace(adoptDestinationConfig, `"port":8317`, `"port":8317,"unknown-server-key":true`, 1),
		"observability pprof enabled":                        strings.Replace(adoptDestinationConfig, `"usage":{"usage-statistics-enabled":true}`, `"usage":{"usage-statistics-enabled":true},"pprof":{"enable":true}`, 1),
		"management unknown field":                           strings.Replace(adoptDestinationConfig, `"disable-auto-update-panel":true`, `"disable-auto-update-panel":true,"base-url":"https://evil.example"`, 1),
		"management non-bool allow-remote":                   strings.Replace(adoptDestinationConfig, `"allow-remote":true`, `"allow-remote":"true"`, 1),
		"plugin auth revision":                               strings.Replace(adoptDestinationConfig, `"enabled":false`, `"enabled":false,"auth-revision":2`, 1),
		"plugin dir suffix under temp prefix":                strings.Replace(adoptDestinationConfig, `"dir":"/runtime/supervisor/plugins"`, `"dir":"/tmp/x/supervisor/plugins"`, 1),
		"oauth auth dir suffix under temp prefix":            strings.Replace(adoptDestinationConfig, `"auth-dir":"/runtime/supervisor/destination/auth"`, `"auth-dir":"/tmp/x/destination/auth"`, 1),
		"oauth auth dir supervisor suffix under temp prefix": strings.Replace(adoptDestinationConfig, `"auth-dir":"/runtime/supervisor/destination/auth"`, `"auth-dir":"/tmp/x/supervisor/destination/auth"`, 1),
		"gateway destination auth under /runtime":            strings.Replace(adoptDestinationConfig, `"auth-dir":"/runtime/supervisor/destination/auth"`, `"auth-dir":"/runtime/gateway/destination/auth"`, 1),
		"gateway destination auth under /data/runtime":       strings.Replace(adoptDestinationConfig, `"auth-dir":"/runtime/supervisor/destination/auth"`, `"auth-dir":"/data/runtime/gateway/destination/auth"`, 1),
	} {
		if adoptProofPosture().matches([]byte(body)) {
			t.Fatalf("%s permitted", name)
		}
	}
}

func TestAdoptPostureCarriesTheSourceKeyList(t *testing.T) {
	keys := []string{"source-key-one", "source-key-two", "source-key-three"}
	list, _ := json.Marshal(keys)
	root := strings.Replace(adoptDestinationConfig, `"api-keys":[]`, `"api-keys":`+string(list), 1)
	if !adoptProofPosture().matches([]byte(root)) {
		t.Fatal("adopted key list rejected")
	}
	// The same list is refused by the New Managed posture, which mints its own
	// canonical shapes and never carries three of them.
	if !adoptProofPosture().validKeys(keys) {
		t.Fatal("adopted key policy refused a source list")
	}
	if managedProofPosture.validKeys(keys) {
		t.Fatal("managed key policy accepted a source list")
	}
	for name, list := range map[string][]string{
		"empty":         nil,
		"duplicate":     {"a", "a"},
		"too many":      make([]string, adoptMaxClientKeys+1),
		"not printable": {"a b"},
	} {
		if adoptProofPosture().validKeys(list) {
			t.Fatalf("%s list permitted", name)
		}
	}
}

// TestProveAdoptedDestinationRunsTheProductionRules is the step-3 regression: the
// exported proof the real-CPA harness runs is `KeySnapshot`, so it refuses every
// document the product refuses — not just the record-independent subset a
// harness-local matcher once applied.
func TestProveAdoptedDestinationRunsTheProductionRules(t *testing.T) {
	keys := []string{"source-key-one", "source-key-two"}
	list, _ := json.Marshal(keys)
	root := strings.Replace(adoptDestinationConfig, `"api-keys":[]`, `"api-keys":`+string(list), 1)
	var served atomic.Value
	served.Store(root)
	yaml := "adopted exact YAML bytes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testManagement() {
			t.Error("wrong authority")
		}
		switch r.URL.Path {
		case "/v8/management/config":
			w.Write([]byte(served.Load().(string)))
		case "/v8/management/config/access/api-keys":
			json.NewEncoder(w).Encode(keys)
		case "/v8/management/config.yaml":
			w.Write([]byte(yaml))
		default:
			t.Error("unexpected request")
		}
	}))
	defer server.Close()
	if got, err := ProveAdoptedDestination(t.Context(), server.URL, testManagement()); err != nil || len(got) != 2 {
		t.Fatalf("the adopted destination did not prove: %v %v", got, err)
	}
	served.Store(strings.Replace(root, `"logging-to-file":false`, `"logging-to-file":true`, 1))
	if got, err := ProveAdoptedDestination(t.Context(), server.URL, testManagement()); err != nil || len(got) != 2 {
		t.Fatalf("the adopted destination with file logging enabled did not prove: %v %v", got, err)
	}
	served.Store(root)
	for name, document := range map[string]string{
		// Every one of these passes the record-independent subset the harness used
		// to apply; each is a destination the product refuses.
		"loopback host":               strings.Replace(root, `"host":"0.0.0.0"`, `"host":"127.0.0.1"`, 1),
		"panel enabled":               strings.Replace(root, `"disable-control-panel":true`, `"disable-control-panel":false`, 1),
		"foreign auth":                strings.Replace(root, `"/runtime/supervisor/destination/auth"`, `"/runtime/gateway/auth"`, 1),
		"gateway destination auth":    strings.Replace(root, `"/runtime/supervisor/destination/auth"`, `"/runtime/gateway/destination/auth"`, 1),
		"same suffix temp auth":       strings.Replace(root, `"/runtime/supervisor/destination/auth"`, `"/tmp/x/supervisor/destination/auth"`, 1),
		"foreign plugins":             strings.Replace(root, `"/runtime/supervisor/plugins"`, `"/runtime/gateway/plugins"`, 1),
		"same suffix temp plugin":     strings.Replace(root, `"/runtime/supervisor/plugins"`, `"/tmp/x/supervisor/plugins"`, 1),
		"non-boolean logging-to-file": strings.Replace(root, `"logging-to-file":false`, `"logging-to-file":"true"`, 1),
		"unknown log key":             strings.Replace(root, `"logging-to-file":false`, `"logging-to-file":true,"log-dir":"/tmp/logs"`, 1),
		"port moved":                  strings.Replace(root, `"port":8317`, `"port":8318`, 1),
		"plugin store":                strings.Replace(root, `"enabled":false`, `"enabled":false,"store":{}`, 1),
	} {
		served.Store(document)
		if _, err := ProveAdoptedDestination(t.Context(), server.URL, testManagement()); err == nil {
			t.Fatalf("%s proved the adopted destination", name)
		}
	}
	// The advertised key list is the same one the document carries, because the
	// production proof compares them.
	served.Store(root)
	if _, err := ProveAdoptedDestination(t.Context(), server.URL, testManagement()); err != nil {
		t.Fatal(err)
	}
	customSupRoot := filepath.ToSlash(filepath.Join(t.TempDir(), "supervisor"))
	served.Store(strings.NewReplacer(
		`"/runtime/supervisor/destination/auth"`, `"`+customSupRoot+`/destination/auth"`,
		`"/runtime/supervisor/plugins"`, `"`+customSupRoot+`/plugins"`,
	).Replace(root))
	if _, err := ProveAdoptedDestination(t.Context(), server.URL, testManagement()); err == nil {
		t.Fatal("default Supervisor roots proved an unconfigured temporary Supervisor root")
	}
	if got, err := ProveAdoptedDestination(t.Context(), server.URL, testManagement(), customSupRoot); err != nil || len(got) != 2 {
		t.Fatalf("configured isolated Supervisor root did not prove adopted destination: %v %v", got, err)
	}
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v8/management/config":
			w.Write([]byte(root))
		case "/v8/management/config/access/api-keys":
			json.NewEncoder(w).Encode([]string{"source-key-one", "source-key-three"})
		case "/v8/management/config.yaml":
			w.Write([]byte(yaml))
		}
	}))
	defer other.Close()
	if _, err := ProveAdoptedDestination(t.Context(), other.URL, testManagement()); err == nil {
		t.Fatal("a destination whose advertised keys differ from its document proved")
	}
}

// TestPostureFollowsTheCommittedStrategy proves the selector is the only source
// of the posture: the transport never assumes the New Managed template, and a
// strategy it does not own is refused rather than proven.
func TestPostureFollowsTheCommittedStrategy(t *testing.T) {
	if _, ok := PostureFor("connect_external"); ok {
		t.Fatal("external answered by the Manager-owned posture")
	}
	if _, ok := PostureFor(""); ok {
		t.Fatal("unselected strategy answered")
	}
	if v, ok := PostureFor("new_managed"); !ok || v.Adopt {
		t.Fatal("new managed posture not resolved")
	}
	if v, ok := PostureFor("adopt_existing"); !ok || !v.Adopt {
		t.Fatal("adopt posture not resolved")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer srv.Close()
	transport := &Transport{client: srv.Client(), endpoint: srv.URL, posture: managedProofPosture}
	// A selector that cannot answer refuses: it never degrades to the template.
	transport.SetPostureSelector(func() (DestinationPosture, bool) { return DestinationPosture{}, false })
	if _, err := transport.KeySnapshot(t.Context(), testManagement()); err == nil {
		t.Fatal("refusing selector proved a destination")
	}
}

// TestKeySnapshotProvesTheAdoptedDestination is the step's real proof: an
// authenticated read of a destination shaped like an adopted source — several
// keys, extra root sections — proves under the Adopt posture and fails under the
// New Managed one.
func TestKeySnapshotProvesTheAdoptedDestination(t *testing.T) {
	keys := []string{"source-key-one", "source-key-two", "source-key-three"}
	list, _ := json.Marshal(keys)
	root := strings.Replace(adoptDestinationConfig, `"api-keys":[]`, `"api-keys":`+string(list), 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testManagement() {
			t.Error("wrong authority")
		}
		switch r.URL.Path {
		case "/v8/management/config":
			w.Write([]byte(root))
		case "/v8/management/config/access/api-keys":
			json.NewEncoder(w).Encode(keys)
		case "/v8/management/config.yaml":
			w.Write([]byte("adopted exact YAML bytes"))
		default:
			t.Error("unexpected request")
		}
	}))
	defer server.Close()
	transport := NewTransportForEndpoint(server.URL)
	transport.SetPostureSelector(func() (DestinationPosture, bool) { return PostureFor("adopt_existing") })
	got, err := transport.KeySnapshot(t.Context(), testManagement())
	if err != nil || len(got.SourceSHA256) != 64 || len(got.Keys) != 3 {
		t.Fatalf("adopted destination not proven: %v %+v", err, got)
	}
	// New Managed with three keys still fails: the posture is selected by the
	// strategy, not by the document's shape.
	transport.SetPostureSelector(func() (DestinationPosture, bool) { return PostureFor("new_managed") })
	if _, err = transport.KeySnapshot(t.Context(), testManagement()); err == nil {
		t.Fatal("new managed posture accepted an adopted destination")
	}
}

func TestPostureAcceptsPluginStoreAuthAndRejectsMalformedStoreAuth(t *testing.T) {
	withStoreAuth := strings.Replace(
		adoptDestinationConfig,
		`"enabled":false,"dir":"/runtime/supervisor/plugins"`,
		`"enabled":true,"dir":"/runtime/supervisor/plugins","store-sources":["http://registry.internal/registry.json"],"store-auth":[{"match":"http://registry.internal/","allow-insecure":true}],"configs":{"sample-provider":{"enabled":true}}`,
		1,
	)
	if !adoptProofPosture().matches([]byte(withStoreAuth)) {
		t.Fatal("adopt posture rejected valid plugins.store-auth")
	}
	badStoreAuth := strings.Replace(withStoreAuth, `[{"match":"http://registry.internal/","allow-insecure":true}]`, `["not-an-object"]`, 1)
	if adoptProofPosture().matches([]byte(badStoreAuth)) {
		t.Fatal("adopt posture accepted non-object plugins.store-auth element")
	}
}
