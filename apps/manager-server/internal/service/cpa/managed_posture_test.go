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

// The default new_managed destination template: 0.0.0.0 with a child identity,
// remote management, usage statistics, the Supervisor-owned auth directory and
// a bcrypt management digest CPA wrote back at first startup.
const managedDestinationConfig = `{"config-version":8,"server":{"host":"0.0.0.0","port":8317},"management":{"allow-remote":true,"secret-key":"$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12","disable-control-panel":true,"disable-auto-update-panel":true},"access":{"api-keys":[]},"oauth":{"auth-dir":"/runtime/supervisor/destination/auth"},"plugins":{"enabled":false,"dir":"/runtime/supervisor/plugins"},"observability":{"logs":{"logging-to-file":false},"usage":{"usage-statistics-enabled":true}}}`

func TestManagedPostureAllowsOperatorEditsWhileEnforcingSecurityInvariants(t *testing.T) {
	if !managedProofPosture.matches([]byte(managedDestinationConfig)) {
		t.Fatal("managed destination template rejected")
	}
	if privateProofPosture.matches([]byte(managedDestinationConfig)) {
		t.Fatal("private seed posture accepted the managed destination")
	}

	allowedTests := []struct {
		name string
		body string
	}{
		{
			name: "plugins enabled",
			body: strings.Replace(managedDestinationConfig, `"enabled":false`, `"enabled":true`, 1),
		},
		{
			name: "plugins enabled with installed plugin config and store sources",
			body: strings.Replace(
				managedDestinationConfig,
				`"enabled":false,"dir":"/runtime/supervisor/plugins"`,
				`"enabled":true,"dir":"/runtime/supervisor/plugins","configs":{"sample":{"enabled":true}},"store-sources":["https://plugins.example.com/index.json"]`,
				1,
			),
		},
		{
			name: "native loopback ipv4",
			body: strings.NewReplacer(
				`"host":"0.0.0.0"`, `"host":"127.0.0.1"`,
				`"allow-remote":true`, `"allow-remote":false`,
			).Replace(managedDestinationConfig),
		},
		{
			name: "native loopback ipv6",
			body: strings.NewReplacer(
				`"host":"0.0.0.0"`, `"host":"::1"`,
				`"allow-remote":true`, `"allow-remote":false`,
			).Replace(managedDestinationConfig),
		},
		{
			name: "openai compatibility provider in api-keys",
			body: strings.Replace(
				managedDestinationConfig,
				`"access":{"api-keys":[]}`,
				`"access":{"api-keys":[]},"api-keys":{"openai-compatibility":[{"name":"mock-openai","base-url":"http://127.0.0.1:38339/v1","keys":[{"api-key":"sk-mock-upstream"}],"models":[{"name":"gpt-4o-mini"}]}]}`,
				1,
			),
		},
		{
			name: "multiple provider families in api-keys",
			body: strings.Replace(
				managedDestinationConfig,
				`"access":{"api-keys":[]}`,
				`"access":{"api-keys":[]},"api-keys":{"claude":[{"name":"claude-1","keys":[{"api-key":"sk-ant-test"}]}],"gemini":[{"name":"gem-1","keys":[{"api-key":"AIza-test"}]}]}`,
				1,
			),
		},
		{
			name: "gateway config groups routing requests upstream client multimedia",
			body: strings.Replace(
				managedDestinationConfig,
				`"access":{"api-keys":[]}`,
				`"access":{"api-keys":[]},"routing":{"strategy":"round-robin","retry":{"request-retry":2},"cooldown":{"disable-cooling":false}},"requests":{"proxy-url":"http://127.0.0.1:8080","passthrough-headers":true,"streaming":{"keepalive-seconds":15},"payload":{"filter":[{"models":[{"name":"*"}],"params":["metadata"]}]}},"upstream":{"codex":{"response-steering":true},"claude":{"disable-claude-cloak-mode":true},"xai":{"inject-x-search":false}},"client":{"codex":{"enable-apply-patch":true}},"multimedia":{"disable-image-generation":"false"}`,
				1,
			),
		},
		{
			name: "oauth model alias and excluded models keeping supervisor auth-dir",
			body: strings.Replace(
				managedDestinationConfig,
				`"oauth":{"auth-dir":"/runtime/supervisor/destination/auth"}`,
				`"oauth":{"auth-dir":"/data/runtime/supervisor/destination/auth","auth-auto-refresh-workers":2,"model-alias":{"codex":[{"name":"gpt-5","alias":"codex-main"}]},"excluded-models":{"codex":["legacy-model"]},"providers":{"aistudio":{"ws-auth":true}}}`,
				1,
			),
		},
		{
			name: "observability debug and request-log keeping stdout and usage queue",
			body: strings.Replace(
				managedDestinationConfig,
				`"observability":{"logs":{"logging-to-file":false},"usage":{"usage-statistics-enabled":true}}`,
				`"observability":{"logs":{"logging-to-file":false,"debug":true,"request-log":true},"usage":{"usage-statistics-enabled":true,"redis-usage-queue-retention-seconds":120},"pprof":{"enable":false}}`,
				1,
			),
		},
		{
			name: "optional models credentials and quota-exceeded sections",
			body: strings.Replace(
				managedDestinationConfig,
				`"access":{"api-keys":[]}`,
				`"access":{"api-keys":[]},"models":{"openai":{"source":"builtin"}},"credentials":{"in-flight":{"enabled":true}},"quota-exceeded":{"switch-project":true}`,
				1,
			),
		},
		{
			name: "cpa SaveConfigPreserveComments rewrite with disabled server discovery and defaults",
			body: `{"config-version":8,"plugins":{"dir":"/data/runtime/supervisor/plugins","enabled":false},"access":{"api-keys":[]},"server":{"host":"127.0.0.1","port":8317,"discovery":{"service-type":"_ai-gateway._tcp","subtypes":["_chat-completions","_responses","_messages","_generate-content","_interactions"]}},"management":{"allow-remote":false,"secret-key":"$2a$10$SO5RdGiOYIGOuA3sHAWDouh0VHJ7Lx6FqoP3XHRyNGLGlMEodr2ga","disable-control-panel":true,"disable-auto-update-panel":true},"oauth":{"auth-dir":"/data/runtime/supervisor/destination/auth","providers":{"aistudio":{"ws-auth":true}}},"observability":{"logs":{"logging-to-file":false},"usage":{"usage-statistics-enabled":true,"redis-usage-queue-retention-seconds":60}},"credentials":{"concurrency":{"max-limit":1000000},"in-flight":{"snapshot-interval":"2s"}},"routing":{"cooldown":{"disable-cooling":false},"retry":{"request-retry":0}},"api-keys":{"openai-compatibility":[{"name":"mock-fixa1","base-url":"http://host.docker.internal:38339/v1","models":[{"name":"gpt-4.1-mini"}],"keys":[{"api-key":"sk-mock-fixa1"}]}]}}`,
		},
	}
	for _, tc := range allowedTests {
		t.Run("allow/"+tc.name, func(t *testing.T) {
			if !managedProofPosture.matches([]byte(tc.body)) {
				t.Fatalf("allowed managed edit %q was rejected", tc.name)
			}
		})
	}

	rejectedTests := []struct {
		name string
		body string
	}{
		{
			name: "control panel enabled",
			body: strings.Replace(managedDestinationConfig, `"disable-control-panel":true`, `"disable-control-panel":false`, 1),
		},
		{
			name: "auto update panel enabled",
			body: strings.Replace(managedDestinationConfig, `"disable-auto-update-panel":true`, `"disable-auto-update-panel":false`, 1),
		},
		{
			name: "plaintext management secret",
			body: strings.Replace(managedDestinationConfig, `"$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12"`, `"plaintext-management-key"`, 1),
		},
		{
			name: "missing management secret",
			body: strings.Replace(managedDestinationConfig, `"secret-key":"$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12",`, ``, 1),
		},
		{
			name: "extra management field",
			body: strings.Replace(managedDestinationConfig, `"disable-auto-update-panel":true`, `"disable-auto-update-panel":true,"base-url":"https://evil.example"`, 1),
		},
		{
			name: "server port changed",
			body: strings.Replace(managedDestinationConfig, `"port":8317`, `"port":9000`, 1),
		},
		{
			name: "loopback host with remote management enabled",
			body: strings.Replace(managedDestinationConfig, `"host":"0.0.0.0"`, `"host":"127.0.0.1"`, 1),
		},
		{
			name: "wildcard host with remote management disabled",
			body: strings.Replace(managedDestinationConfig, `"allow-remote":true`, `"allow-remote":false`, 1),
		},
		{
			name: "non-canonical loopback 127.0.0.2",
			body: strings.NewReplacer(
				`"host":"0.0.0.0"`, `"host":"127.0.0.2"`,
				`"allow-remote":true`, `"allow-remote":false`,
			).Replace(managedDestinationConfig),
		},
		{
			name: "server tls added",
			body: strings.Replace(managedDestinationConfig, `"port":8317`, `"port":8317,"tls":{"enable":true}`, 1),
		},
		{
			name: "server discovery enabled",
			body: strings.Replace(managedDestinationConfig, `"port":8317`, `"port":8317,"discovery":{"enabled":true,"service-type":"_ai-gateway._tcp"}`, 1),
		},
		{
			name: "plugin dir outside supervisor root",
			body: strings.Replace(managedDestinationConfig, `"dir":"/runtime/supervisor/plugins"`, `"dir":"/tmp/plugins"`, 1),
		},
		{
			name: "plugin dir traversal",
			body: strings.Replace(managedDestinationConfig, `"dir":"/runtime/supervisor/plugins"`, `"dir":"/runtime/gateway/x/../supervisor/plugins"`, 1),
		},
		{
			name: "plugin dir not supervisor root",
			body: strings.Replace(managedDestinationConfig, `"dir":"/runtime/supervisor/plugins"`, `"dir":"/runtime/gateway/plugins/sub"`, 1),
		},
		{
			name: "plugin dir suffix under arbitrary temp prefix",
			body: strings.Replace(managedDestinationConfig, `"dir":"/runtime/supervisor/plugins"`, `"dir":"/tmp/x/supervisor/plugins"`, 1),
		},
		{
			name: "plugin dir unclean repeated slash",
			body: strings.Replace(managedDestinationConfig, `"dir":"/runtime/supervisor/plugins"`, `"dir":"/runtime//supervisor/plugins"`, 1),
		},
		{
			name: "plugin dir missing",
			body: strings.Replace(managedDestinationConfig, `,"dir":"/runtime/supervisor/plugins"`, ``, 1),
		},
		{
			name: "plugin auth revision",
			body: strings.Replace(managedDestinationConfig, `"dir":"/runtime/supervisor/plugins"`, `"dir":"/runtime/supervisor/plugins","auth-revision":2`, 1),
		},
		{
			name: "plugin unknown key",
			body: strings.Replace(managedDestinationConfig, `"dir":"/runtime/supervisor/plugins"`, `"dir":"/runtime/supervisor/plugins","auth":true`, 1),
		},
		{
			name: "plugin non-boolean switch",
			body: strings.Replace(managedDestinationConfig, `"enabled":false`, `"enabled":"false"`, 1),
		},
		{
			name: "foreign oauth auth dir",
			body: strings.Replace(managedDestinationConfig, `"auth-dir":"/runtime/supervisor/destination/auth"`, `"auth-dir":"/tmp/auth"`, 1),
		},
		{
			name: "bootstrap oauth auth dir",
			body: strings.Replace(managedDestinationConfig, `"auth-dir":"/runtime/supervisor/destination/auth"`, `"auth-dir":"/runtime/gateway/auth"`, 1),
		},
		{
			name: "gateway destination auth dir under /runtime",
			body: strings.Replace(managedDestinationConfig, `"auth-dir":"/runtime/supervisor/destination/auth"`, `"auth-dir":"/runtime/gateway/destination/auth"`, 1),
		},
		{
			name: "gateway destination auth dir under /data/runtime",
			body: strings.Replace(managedDestinationConfig, `"auth-dir":"/runtime/supervisor/destination/auth"`, `"auth-dir":"/data/runtime/gateway/destination/auth"`, 1),
		},
		{
			name: "oauth auth dir traversal",
			body: strings.Replace(managedDestinationConfig, `"auth-dir":"/runtime/supervisor/destination/auth"`, `"auth-dir":"/runtime/supervisor/x/../destination/auth"`, 1),
		},
		{
			name: "oauth auth dir suffix under arbitrary temp prefix",
			body: strings.Replace(managedDestinationConfig, `"auth-dir":"/runtime/supervisor/destination/auth"`, `"auth-dir":"/tmp/x/destination/auth"`, 1),
		},
		{
			name: "oauth auth dir supervisor suffix under arbitrary temp prefix",
			body: strings.Replace(managedDestinationConfig, `"auth-dir":"/runtime/supervisor/destination/auth"`, `"auth-dir":"/tmp/x/supervisor/destination/auth"`, 1),
		},
		{
			name: "oauth auth dir unclean repeated slash",
			body: strings.Replace(managedDestinationConfig, `"auth-dir":"/runtime/supervisor/destination/auth"`, `"auth-dir":"/runtime/supervisor//destination/auth"`, 1),
		},
		{
			name: "logging to file enabled",
			body: strings.Replace(managedDestinationConfig, `"logging-to-file":false`, `"logging-to-file":true`, 1),
		},
		{
			name: "usage statistics disabled",
			body: strings.Replace(managedDestinationConfig, `"usage-statistics-enabled":true`, `"usage-statistics-enabled":false`, 1),
		},
		{
			name: "pprof enabled",
			body: strings.Replace(managedDestinationConfig, `"usage":{"usage-statistics-enabled":true}`, `"usage":{"usage-statistics-enabled":true},"pprof":{"enable":true}`, 1),
		},
		{
			name: "unblanked client key in pre-install seed check",
			body: strings.Replace(managedDestinationConfig, `"api-keys":[]`, `"api-keys":["cpamp_sk_`+strings.Repeat("a", 64)+`"]`, 1),
		},
		{
			name: "extra access key",
			body: strings.Replace(managedDestinationConfig, `"api-keys":[]`, `"api-keys":[],"extra":true`, 1),
		},
		{
			name: "legacy list at top-level api-keys",
			body: strings.Replace(managedDestinationConfig, `"access":{"api-keys":[]}`, `"access":{"api-keys":[]},"api-keys":["sk-legacy"]`, 1),
		},
		{
			name: "unknown top-level root",
			body: strings.Replace(managedDestinationConfig, `"access":{"api-keys":[]}`, `"access":{"api-keys":[]},"foreign":{}`, 1),
		},
		{
			name: "unsupported config-version",
			body: strings.Replace(managedDestinationConfig, `"config-version":8`, `"config-version":7`, 1),
		},
	}
	for _, tc := range rejectedTests {
		t.Run("reject/"+tc.name, func(t *testing.T) {
			if managedProofPosture.matches([]byte(tc.body)) {
				t.Fatalf("unsafe managed edit %q was permitted", tc.name)
			}
		})
	}

	keyOne := "cpamp_sk_" + strings.Repeat("a", 64)
	keyTwo := "cpamp_sk_" + strings.Repeat("b", 64)
	for name, keys := range map[string][]string{
		"single canonical key":           {keyOne},
		"replacement pair canonical key": {keyOne, keyTwo},
	} {
		if !managedProofPosture.validKeys(keys) {
			t.Fatalf("valid managed client keys %q rejected", name)
		}
	}
	for name, keys := range map[string][]string{
		"empty client key list":   nil,
		"zero client keys":        {},
		"foreign client key":      {"sk-user-defined"},
		"duplicate canonical key": {keyOne, keyOne},
		"three canonical keys":    {keyOne, keyTwo, "cpamp_sk_" + strings.Repeat("c", 64)},
	} {
		if managedProofPosture.validKeys(keys) {
			t.Fatalf("unsafe managed client keys %q permitted", name)
		}
	}
}

func TestPrivatePostureRejectsUsageStatisticsAndRemoteManagement(t *testing.T) {
	for name, body := range map[string]string{
		"usage statistics":  strings.Replace(proofConfig, `"usage-statistics-enabled":false`, `"usage-statistics-enabled":true`, 1),
		"remote management": strings.Replace(proofConfig, `"allow-remote":false`, `"allow-remote":true`, 1),
		"foreign auth dir":  strings.Replace(proofConfig, `"/runtime/gateway/auth"`, `"/runtime/gateway/destination/auth"`, 1),
	} {
		if emptyProofConfig([]byte(body)) {
			t.Fatalf("%s permitted", name)
		}
	}
	if !emptyProofConfig([]byte(proofConfig)) {
		t.Fatal("private seed rejected")
	}
}

func TestLooksLikeBcrypt(t *testing.T) {
	for value, want := range map[string]bool{
		"$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12": true,
		"$2y$04$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12": true,
		"$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY":   false,
		"$3a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12": false,
		"plaintext-management-key":                                     false,
		strings.Repeat("m", 60):                                        false,
	} {
		if looksLikeBcrypt(value) != want {
			t.Fatalf("looksLikeBcrypt(%q) != %v", value, want)
		}
	}
}

func TestKeySnapshotBindsManagedAndAdoptedPosturesToConfiguredSupervisorRoot(t *testing.T) {
	canonicalManagedKey := "cpamp_sk_" + strings.Repeat("a", 64)
	customDataDir := filepath.ToSlash(t.TempDir())
	customSupervisorRoot := customDataDir + "/runtime/supervisor"

	for _, tc := range []struct {
		strategy string
		baseDoc  string
		keys     []string
	}{
		{
			strategy: "new_managed",
			baseDoc:  managedDestinationConfig,
			keys:     []string{canonicalManagedKey},
		},
		{
			strategy: "adopt_existing",
			baseDoc:  adoptDestinationConfig,
			keys:     []string{"source-key-one", "source-key-two"},
		},
	} {
		t.Run(tc.strategy, func(t *testing.T) {
			keyListJSON, err := json.Marshal(tc.keys)
			if err != nil {
				t.Fatal(err)
			}
			buildDoc := func(authDir, pluginDir string) string {
				return strings.NewReplacer(
					`"api-keys":[]`, `"api-keys":`+string(keyListJSON),
					`"/runtime/supervisor/destination/auth"`, `"`+authDir+`"`,
					`"/runtime/supervisor/plugins"`, `"`+pluginDir+`"`,
				).Replace(tc.baseDoc)
			}

			var served atomic.Value
			served.Store(buildDoc(customSupervisorRoot+"/destination/auth", customSupervisorRoot+"/plugins"))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+testManagement() {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				switch r.URL.Path {
				case "/v8/management/config":
					_, _ = w.Write([]byte(served.Load().(string)))
				case "/v8/management/config/access/api-keys":
					_ = json.NewEncoder(w).Encode(tc.keys)
				case "/v8/management/config.yaml":
					_, _ = w.Write([]byte("config-version: 8\n"))
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()

			transport := NewTransportForEndpoint(server.URL)
			transport.SetTrustedSupervisorRoots([]string{customSupervisorRoot})
			transport.SetPostureSelector(func() (DestinationPosture, bool) {
				return PostureForWithSupervisorRoots(tc.strategy, []string{customSupervisorRoot})
			})

			// Admits the instance's own configured Supervisor root.
			snap, err := transport.KeySnapshot(t.Context(), testManagement())
			if err != nil || len(snap.Keys) != len(tc.keys) {
				t.Fatalf("configured custom Supervisor root rejected on %s: err=%v snap=%+v", tc.strategy, err, snap)
			}

			// Rejects same-suffix untrusted prefixes, child-writable gateway/destination/auth,
			// and container roots not configured for this instance.
			for name, badDoc := range map[string]string{
				"same suffix untrusted auth prefix": buildDoc(
					"/tmp/foreign/runtime/supervisor/destination/auth",
					customSupervisorRoot+"/plugins",
				),
				"same suffix untrusted plugin prefix": buildDoc(
					customSupervisorRoot+"/destination/auth",
					"/tmp/foreign/runtime/supervisor/plugins",
				),
				"gateway destination auth under instance runtime root": buildDoc(
					customDataDir+"/runtime/gateway/destination/auth",
					customSupervisorRoot+"/plugins",
				),
				"gateway destination auth under /runtime": buildDoc(
					"/runtime/gateway/destination/auth",
					customSupervisorRoot+"/plugins",
				),
				"unconfigured /runtime/supervisor root": buildDoc(
					"/runtime/supervisor/destination/auth",
					"/runtime/supervisor/plugins",
				),
			} {
				served.Store(badDoc)
				if _, err := transport.KeySnapshot(t.Context(), testManagement()); err == nil {
					t.Fatalf("%s unexpectedly passed KeySnapshot on %s", name, tc.strategy)
				}
			}
		})
	}
}
