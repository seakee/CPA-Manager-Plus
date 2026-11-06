package cpa

import (
	"strings"
	"testing"
)

// The default new_managed destination template: 0.0.0.0 with a child identity,
// remote management, usage statistics, the Supervisor-owned auth directory and
// a bcrypt management digest CPA wrote back at first startup.
const managedDestinationConfig = `{"config-version":8,"server":{"host":"0.0.0.0","port":8317},"management":{"allow-remote":true,"secret-key":"$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12","disable-control-panel":true,"disable-auto-update-panel":true},"access":{"api-keys":[]},"oauth":{"auth-dir":"/runtime/gateway/destination/auth"},"plugins":{"enabled":false,"dir":"/runtime/supervisor/plugins"},"observability":{"logs":{"logging-to-file":false},"usage":{"usage-statistics-enabled":true}}}`

func TestManagedPostureAcceptsOnlyTheDestinationTemplate(t *testing.T) {
	if !managedProofPosture.matches([]byte(managedDestinationConfig)) {
		t.Fatal("managed destination template rejected")
	}
	if privateProofPosture.matches([]byte(managedDestinationConfig)) {
		t.Fatal("private seed posture accepted the managed destination")
	}
	// The plugin switch is the operator's own decision: both values are the
	// template's, and only the Supervisor-owned directory plus the closed key set
	// are pinned. Every other plugin shape is a posture this Manager never wrote.
	for name, body := range map[string]string{
		"plugins enabled": strings.Replace(managedDestinationConfig, `"enabled":false`, `"enabled":true`, 1),
		"native loopback ipv4": strings.NewReplacer(
			`"host":"0.0.0.0"`, `"host":"127.0.0.1"`,
			`"allow-remote":true`, `"allow-remote":false`,
		).Replace(managedDestinationConfig),
		"native loopback ipv6": strings.NewReplacer(
			`"host":"0.0.0.0"`, `"host":"::1"`,
			`"allow-remote":true`, `"allow-remote":false`,
		).Replace(managedDestinationConfig),
	} {
		if !managedProofPosture.matches([]byte(body)) {
			t.Fatalf("%s refused", name)
		}
	}
	for name, body := range map[string]string{
		"plugin dir outside the Supervisor root":  strings.Replace(managedDestinationConfig, `"dir":"/runtime/supervisor/plugins"`, `"dir":"/tmp/plugins"`, 1),
		"plugin dir traversal":                    strings.Replace(managedDestinationConfig, `"dir":"/runtime/supervisor/plugins"`, `"dir":"/runtime/gateway/x/../plugins"`, 1),
		"plugin dir not a plugin root":            strings.Replace(managedDestinationConfig, `"dir":"/runtime/supervisor/plugins"`, `"dir":"/runtime/gateway/plugins/sub"`, 1),
		"plugin dir missing":                      strings.Replace(managedDestinationConfig, `,"dir":"/runtime/supervisor/plugins"`, ``, 1),
		"plugin store source":                     strings.Replace(managedDestinationConfig, `"dir":"/runtime/supervisor/plugins"`, `"dir":"/runtime/supervisor/plugins","store-sources":["https://example.invalid"]`, 1),
		"plugin instance config":                  strings.Replace(managedDestinationConfig, `"dir":"/runtime/supervisor/plugins"`, `"dir":"/runtime/supervisor/plugins","configs":{"acme":{"enabled":true}}`, 1),
		"plugin auth revision":                    strings.Replace(managedDestinationConfig, `"dir":"/runtime/supervisor/plugins"`, `"dir":"/runtime/supervisor/plugins","auth-revision":2`, 1),
		"plugin non-boolean switch":               strings.Replace(managedDestinationConfig, `"enabled":false`, `"enabled":"false"`, 1),
		"loopback host":    strings.Replace(managedDestinationConfig, `"host":"0.0.0.0"`, `"host":"127.0.0.1"`, 1),
		"remote disabled":  strings.Replace(managedDestinationConfig, `"allow-remote":true`, `"allow-remote":false`, 1),
		"non-canonical loopback 127.0.0.2": strings.NewReplacer(
			`"host":"0.0.0.0"`, `"host":"127.0.0.2"`,
			`"allow-remote":true`, `"allow-remote":false`,
		).Replace(managedDestinationConfig),
		"usage disabled":   strings.Replace(managedDestinationConfig, `"usage-statistics-enabled":true`, `"usage-statistics-enabled":false`, 1),
		"plaintext secret": strings.Replace(managedDestinationConfig, `"$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12"`, `"plaintext-management-key"`, 1),
		"installed key":    strings.Replace(managedDestinationConfig, `"api-keys":[]`, `"api-keys":["cpamp_sk_`+strings.Repeat("a", 64)+`"]`, 1),
		"foreign auth dir": strings.Replace(managedDestinationConfig, `"auth-dir":"/runtime/gateway/destination/auth"`, `"auth-dir":"/tmp/auth"`, 1),
		"bootstrap auth":   strings.Replace(managedDestinationConfig, `"auth-dir":"/runtime/gateway/destination/auth"`, `"auth-dir":"/runtime/gateway/auth"`, 1),
		"auth traversal":   strings.Replace(managedDestinationConfig, `"auth-dir":"/runtime/gateway/destination/auth"`, `"auth-dir":"/runtime/gateway/x/../destination/auth"`, 1),
		"remote panel":     strings.Replace(managedDestinationConfig, `"disable-control-panel":true`, `"disable-control-panel":false`, 1),
	} {
		if managedProofPosture.matches([]byte(body)) {
			t.Fatalf("%s permitted", name)
		}
	}
}

func TestPrivatePostureRejectsUsageStatisticsAndRemoteManagement(t *testing.T) {
	for name, body := range map[string]string{
		"usage statistics": strings.Replace(proofConfig, `"usage-statistics-enabled":false`, `"usage-statistics-enabled":true`, 1),
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
