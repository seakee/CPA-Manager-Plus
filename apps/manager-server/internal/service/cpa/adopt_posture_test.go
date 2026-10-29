package cpa

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// adoptDestinationConfig is a destination staged by an adoption: the Supervisor
// template with the install-derived management credential, carrying the source's
// client keys and two root sections an adoption legitimately brings along.
const adoptDestinationConfig = `{"config-version":8,"server":{"host":"0.0.0.0","port":8317},"management":{"allow-remote":true,"secret-key":"$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12","disable-control-panel":true,"disable-auto-update-panel":true},"access":{"api-keys":[]},"routing":{"strategy":"round-robin"},"client":{"codex":{"enable-apply-patch":true}},"oauth":{"auth-dir":"/runtime/gateway/destination/auth"},"plugins":{"enabled":false,"dir":"/runtime/supervisor/plugins"},"observability":{"logs":{"logging-to-file":false},"usage":{"usage-statistics-enabled":true}}}`

func TestAdoptPostureAcceptsTheAdoptedDestination(t *testing.T) {
	adopt := adoptProofPosture(false)
	if !adopt.matches([]byte(adoptDestinationConfig)) {
		t.Fatal("adopted destination rejected")
	}
	// The same document is refused by the New Managed template: that posture
	// pins an exact root count, so it can never stand in for an adoption.
	if managedProofPosture.matches([]byte(adoptDestinationConfig)) {
		t.Fatal("managed template accepted an adopted destination")
	}
	// The recorded opt-in is pinned with the directory: a destination whose
	// switch disagrees with the record is not the one this install staged.
	if adoptProofPosture(true).matches([]byte(adoptDestinationConfig)) {
		t.Fatal("adopted destination accepted the wrong plugin opt-in")
	}
	enabled := strings.Replace(adoptDestinationConfig, `"enabled":false`, `"enabled":true`, 1)
	if !adoptProofPosture(true).matches([]byte(enabled)) || adoptProofPosture(false).matches([]byte(enabled)) {
		t.Fatal("plugin opt-in not pinned to the record")
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
	} {
		if adoptProofPosture(false).matches([]byte(body)) {
			t.Fatalf("%s permitted", name)
		}
	}
}

func TestAdoptPostureCarriesTheSourceKeyList(t *testing.T) {
	keys := []string{"source-key-one", "source-key-two", "source-key-three"}
	list, _ := json.Marshal(keys)
	root := strings.Replace(adoptDestinationConfig, `"api-keys":[]`, `"api-keys":`+string(list), 1)
	if !adoptProofPosture(false).matches([]byte(root)) {
		t.Fatal("adopted key list rejected")
	}
	// The same list is refused by the New Managed posture, which mints its own
	// canonical shapes and never carries three of them.
	if !adoptProofPosture(false).validKeys(keys) {
		t.Fatal("adopted key policy refused a source list")
	}
	if managedProofPosture.validKeys(keys) {
		t.Fatal("managed key policy accepted a source list")
	}
	for name, list := range map[string][]string{
		"empty":     nil,
		"duplicate": {"a", "a"},
		"too many":  make([]string, adoptMaxClientKeys+1),
		"not printable": {"a b"},
	} {
		if adoptProofPosture(false).validKeys(list) {
			t.Fatalf("%s list permitted", name)
		}
	}
}

// TestPostureFollowsTheCommittedStrategy proves the selector is the only source
// of the posture: the transport never assumes the New Managed template, and a
// strategy it does not own is refused rather than proven.
func TestPostureFollowsTheCommittedStrategy(t *testing.T) {
	if _, ok := PostureFor("connect_external", false); ok {
		t.Fatal("external answered by the Manager-owned posture")
	}
	if _, ok := PostureFor("", false); ok {
		t.Fatal("unselected strategy answered")
	}
	if v, ok := PostureFor("new_managed", true); !ok || v.Adopt {
		t.Fatal("new managed posture not resolved")
	}
	if v, ok := PostureFor("adopt_existing", true); !ok || !v.Adopt || !v.PluginsEnabled {
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
	transport.SetPostureSelector(func() (DestinationPosture, bool) { return PostureFor("adopt_existing", false) })
	got, err := transport.KeySnapshot(t.Context(), testManagement())
	if err != nil || len(got.SourceSHA256) != 64 || len(got.Keys) != 3 {
		t.Fatalf("adopted destination not proven: %v %+v", err, got)
	}
	// New Managed with three keys still fails: the posture is selected by the
	// strategy, not by the document's shape.
	transport.SetPostureSelector(func() (DestinationPosture, bool) { return PostureFor("new_managed", false) })
	if _, err = transport.KeySnapshot(t.Context(), testManagement()); err == nil {
		t.Fatal("new managed posture accepted an adopted destination")
	}
}
