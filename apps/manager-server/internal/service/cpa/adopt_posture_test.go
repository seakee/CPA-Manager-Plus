package cpa

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// adoptDestinationConfig is a destination staged by an adoption: the Supervisor
// template with the install-derived management credential, carrying the source's
// client keys and two root sections an adoption legitimately brings along.
const adoptDestinationConfig = `{"config-version":8,"server":{"host":"0.0.0.0","port":8317},"management":{"allow-remote":true,"secret-key":"$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12","disable-control-panel":true,"disable-auto-update-panel":true},"access":{"api-keys":[]},"routing":{"strategy":"round-robin"},"client":{"codex":{"enable-apply-patch":true}},"oauth":{"auth-dir":"/runtime/gateway/destination/auth"},"plugins":{"enabled":false,"dir":"/runtime/supervisor/plugins"},"observability":{"logs":{"logging-to-file":false},"usage":{"usage-statistics-enabled":true}}}`

func TestAdoptPostureAcceptsTheAdoptedDestination(t *testing.T) {
	adopt := adoptProofPosture()
	if !adopt.matches([]byte(adoptDestinationConfig)) {
		t.Fatal("adopted destination rejected")
	}
	// The same document is refused by the New Managed template: that posture
	// pins an exact root count, so it can never stand in for an adoption.
	if managedProofPosture.matches([]byte(adoptDestinationConfig)) {
		t.Fatal("managed template accepted an adopted destination")
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
	// And back off again.
	served.Store(root)
	if snap, err := transport.KeySnapshot(t.Context(), testManagement()); err != nil || len(snap.Keys) != 2 {
		t.Fatalf("the recorded switch broke the adopted proof: %v %+v", err, snap)
	}
}

func TestAdoptPostureRefusesWhatTheManagerNeverStaged(t *testing.T) {
	for name, body := range map[string]string{
		// A root CPA itself does not read is a document this Manager never proved.
		"unknown root":        strings.Replace(adoptDestinationConfig, `"routing":`, `"foreign":{},"routing":`, 1),
		"missing management":  strings.Replace(adoptDestinationConfig, `"management":`, `"unused":`, 1),
		"missing usage":       strings.Replace(adoptDestinationConfig, `"observability":`, `"unused":`, 1),
		"usage off":           strings.Replace(adoptDestinationConfig, `"usage-statistics-enabled":true`, `"usage-statistics-enabled":false`, 1),
		"plaintext secret":    strings.Replace(adoptDestinationConfig, `"$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12"`, `"plaintext-management-key"`, 1),
		"remote disabled":     strings.Replace(adoptDestinationConfig, `"allow-remote":true`, `"allow-remote":false`, 1),
		"loopback host":       strings.Replace(adoptDestinationConfig, `"host":"0.0.0.0"`, `"host":"127.0.0.1"`, 1),
		"foreign plugin dir":  strings.Replace(adoptDestinationConfig, `"/runtime/supervisor/plugins"`, `"/tmp/plugins"`, 1),
		"foreign auth dir":    strings.Replace(adoptDestinationConfig, `"/runtime/gateway/destination/auth"`, `"/tmp/auth"`, 1),
		"non-boolean switch":  strings.Replace(adoptDestinationConfig, `"enabled":false`, `"enabled":"false"`, 1),
		"logging to file":     strings.Replace(adoptDestinationConfig, `"logging-to-file":false`, `"logging-to-file":true`, 1),
		"foreign version":     strings.Replace(adoptDestinationConfig, `"config-version":8`, `"config-version":7`, 1),
		"non-object document": `[]`,
		// The plugin root is the Supervisor's: a store source, a per-plugin
		// config or an auth revision is a plugin configuration this Manager never
		// wrote, so it is refused even though the switch itself is free.
		"foreign plugin key": strings.Replace(adoptDestinationConfig, `"enabled":false`, `"enabled":false,"store":{"repository":"https://plugins.example"`, 1),
		"plugin auth":        strings.Replace(adoptDestinationConfig, `"enabled":false`, `"enabled":false,"auth":true`, 1),
		// `KeySnapshot` proves by blanking the client list, so an extra access
		// leaf must be refused rather than hidden by that rewrite.
		"extra access key": strings.Replace(adoptDestinationConfig, `"api-keys":[]`, `"api-keys":[],"extra":true`, 1),
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
	for name, document := range map[string]string{
		// Every one of these passes the record-independent subset the harness used
		// to apply; each is a destination the product refuses.
		"loopback host":   strings.Replace(root, `"host":"0.0.0.0"`, `"host":"127.0.0.1"`, 1),
		"panel enabled":   strings.Replace(root, `"disable-control-panel":true`, `"disable-control-panel":false`, 1),
		"foreign auth":    strings.Replace(root, `"/runtime/gateway/destination/auth"`, `"/runtime/gateway/auth"`, 1),
		"foreign plugins": strings.Replace(root, `"/runtime/supervisor/plugins"`, `"/runtime/gateway/plugins"`, 1),
		"logging to file": strings.Replace(root, `"logging-to-file":false`, `"logging-to-file":true`, 1),
		"port moved":      strings.Replace(root, `"port":8317`, `"port":8318`, 1),
		"plugin store":    strings.Replace(root, `"enabled":false`, `"enabled":false,"store":{}`, 1),
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
