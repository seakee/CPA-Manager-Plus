package cpa

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// managedRequiredRoots are the Supervisor-staged sections that every New
// Managed embedded CPA configuration must retain across runtime edits.
var managedRequiredRoots = []string{
	"config-version", // Enforced (= 8): pins the CPA v8 configuration layout contract.
	"server",         // Enforced (host + port 8317): pins the Supervisor-staged listener bind and port.
	"management",     // Enforced (allow-remote, bcrypt secret-key, panels disabled): preserves Manager's exclusive control authority.
	"access",         // Enforced (api-keys list; >=1 canonical client key required by validKeys): prevents unauthenticated /v1 model access.
	"oauth",          // Enforced (auth-dir under destination/auth; OAuth/model settings allowed): confines OAuth token files to Supervisor storage.
	"plugins",        // Enforced (dir under supervisor/plugins; enabled/configs/store-sources allowed): confines native plugin loading to Supervisor storage.
	"observability",  // Enforced (logging-to-file bool, usage-statistics-enabled=true, pprof off; closed log/usage keys with no path override): allows Console file logging while keeping usage collection intact.
}

// managedOptionalObjectRoots are the CPA v8 top-level sections that operator
// actions in the CPAMP console may add or remove on a New Managed destination.
var managedOptionalObjectRoots = map[string]bool{
	"api-keys":       true, // Allowed: upstream AI provider credentials (openai-compatibility, gemini, claude, codex, vertex, xai, meta, interactions).
	"routing":        true, // Allowed: credential selection strategy, session affinity, retry, and cooldown groups from /console/config.
	"requests":       true, // Allowed: upstream proxy URL, header passthrough, keepalive, streaming, and payload rules from /console/config.
	"upstream":       true, // Allowed: provider-specific upstream behavior (codex, claude, header-defaults, xai) from /console/config.
	"client":         true, // Allowed: downstream client compatibility switches (client.codex.*) from /console/config.
	"multimedia":     true, // Allowed: image generation mode, base model, and video auth cache TTL from /console/config.
	"models":         true, // Allowed: optional CPA v8 model catalog source overrides (catalog, codex-catalog, devin-catalog).
	"credentials":    true, // Allowed: CPA v8 credential concurrency and in-flight observation settings.
	"quota-exceeded": true, // Allowed: CPA v8 quota-exceeded fallback switches.
}

// disallowedLegacyRootKeys are pre-v8 or misplaced root keys that must never
// appear at the top level of a canonical CPA v8 document, even under forward-
// compatible v8.x minor-release validation.
var disallowedLegacyRootKeys = map[string]bool{
	"host":                      true,
	"port":                      true,
	"tls":                       true,
	"unix-socket":               true,
	"discovery":                 true,
	"kv-store":                  true,
	"shutdown-timeout":          true,
	"commercial-mode":           true,
	"remote-management":         true,
	"disable-control-panel":     true,
	"secret-key":                true,
	"auth-dir":                  true,
	"pprof":                     true,
	"logging":                   true,
	"logging-to-file":           true,
	"debug":                     true,
	"request-log":               true,
	"usage-statistics-enabled":  true,
	"gemini-api-key":            true,
	"interactions-api-key":      true,
	"vertex-api-key":            true,
	"codex-api-key":             true,
	"claude-api-key":            true,
	"xai-api-key":               true,
	"meta-api-key":              true,
	"openai-compatibility":      true,
	"credential-concurrency":    true,
	"credential-in-flight":      true,
	"ampcode":                   true,
	"oauth-model-alias":         true,
	"oauth-excluded-models":     true,
}

var managedAllowedOAuthKeys = map[string]bool{
	"auth-dir":                  true,
	"auth-auto-refresh-workers": true,
	"model-alias":               true,
	"excluded-models":           true,
	"request-scoped-errors":     true,
	"settings":                  true,
	"providers":                 true,
}

var managedAllowedObservabilityLogKeys = map[string]bool{
	"logging-to-file":        true,
	"debug":                  true,
	"request-log":            true,
	"logs-max-total-size-mb": true,
	"error-logs-max-files":   true,
}

var managedAllowedObservabilityUsageKeys = map[string]bool{
	"usage-statistics-enabled":            true,
	"redis-usage-queue-retention-seconds": true,
}

// matchesManaged validates a New Managed embedded CPA document (with
// access.api-keys blanked by KeySnapshot, or prior to initial key install).
// Security- and Supervisor-owned invariants remain pinned to the destination
// seed template while operator-editable v8 sections and benign CPA v8.x
// minor-release additions are permitted.
func (p proofPosture) matchesManaged(data []byte) bool {
	var root map[string]json.RawMessage
	if json.Unmarshal(data, &root) != nil || root == nil {
		return false
	}
	requiredSet := make(map[string]bool, len(managedRequiredRoots))
	for _, name := range managedRequiredRoots {
		if _, ok := root[name]; !ok {
			return false
		}
		requiredSet[name] = true
	}
	for name, raw := range root {
		if requiredSet[name] {
			continue
		}
		if disallowedLegacyRootKeys[strings.ToLower(name)] || !isJSONObject(raw) {
			return false
		}
		if !managedOptionalObjectRoots[name] && (isUnsafeExtensionKey(name) || !safeExtensionObject(raw)) {
			return false
		}
	}

	if !exactJSONInt(root["config-version"], 8) {
		return false
	}

	boundHost, ok := matchesManagedServer(root["server"])
	if !ok {
		return false
	}

	if !p.matchesManagedManagement(root["management"], boundHost) {
		return false
	}

	var access map[string]json.RawMessage
	if json.Unmarshal(root["access"], &access) != nil || len(access) != 1 || !emptyStringList(access["api-keys"]) {
		return false
	}

	if !p.matchesManagedOAuth(root["oauth"]) ||
		!p.matchesManagedPlugins(root["plugins"]) ||
		!p.matchesManagedObservability(root["observability"]) {
		return false
	}
	return true
}

func (p proofPosture) matchesManagedManagement(raw json.RawMessage, boundHost string) bool {
	var management map[string]json.RawMessage
	if json.Unmarshal(raw, &management) != nil || len(management) < 4 {
		return false
	}
	allowRemote, ok := parseExactJSONBool(management["allow-remote"])
	if !ok || !p.validDestinationBind(boundHost, allowRemote) {
		return false
	}
	if !exactJSONBool(management["disable-control-panel"], true) ||
		!exactJSONBool(management["disable-auto-update-panel"], true) {
		return false
	}
	var secret string
	if json.Unmarshal(management["secret-key"], &secret) != nil || !looksLikeBcrypt(secret) {
		return false
	}
	for key, val := range management {
		switch key {
		case "allow-remote", "secret-key", "disable-control-panel", "disable-auto-update-panel":
		default:
			if isUnsafeExtensionKey(key) || !safeExtensionValue(val, 0) {
				return false
			}
		}
	}
	return true
}

func (p proofPosture) matchesManagedOAuth(raw json.RawMessage) bool {
	var oauth map[string]json.RawMessage
	if json.Unmarshal(raw, &oauth) != nil || oauth == nil {
		return false
	}
	for key, val := range oauth {
		if managedAllowedOAuthKeys[key] {
			continue
		}
		if isUnsafeExtensionKey(key) || !safeExtensionValue(val, 0) {
			return false
		}
	}
	var authDir string
	return json.Unmarshal(oauth["auth-dir"], &authDir) == nil && p.validAuthDir(authDir)
}

func (p proofPosture) matchesManagedPlugins(raw json.RawMessage) bool {
	var plugins map[string]json.RawMessage
	if json.Unmarshal(raw, &plugins) != nil || plugins == nil {
		return false
	}
	if _, ok := parseExactJSONBool(plugins["enabled"]); !ok {
		return false
	}
	var pluginDir string
	if json.Unmarshal(plugins["dir"], &pluginDir) != nil || !p.validPluginDir(pluginDir) {
		return false
	}
	for key, val := range plugins {
		switch key {
		case "enabled", "dir":
		case "configs":
			if !isJSONObject(val) {
				return false
			}
		case "store-sources":
			if !isStringSlice(val) {
				return false
			}
		case "store-auth":
			if !isJSONObjectSlice(val) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func isJSONObjectSlice(raw json.RawMessage) bool {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || items == nil {
		return false
	}
	for _, item := range items {
		if !isJSONObject(item) {
			return false
		}
	}
	return true
}

func (p proofPosture) matchesManagedObservability(raw json.RawMessage) bool {
	var obs map[string]json.RawMessage
	if json.Unmarshal(raw, &obs) != nil || obs == nil {
		return false
	}
	logsRaw, hasLogs := obs["logs"]
	usageRaw, hasUsage := obs["usage"]
	if !hasLogs || !hasUsage {
		return false
	}
	for key, val := range obs {
		switch key {
		case "logs", "usage":
		case "pprof":
			if !matchesDisabledPprof(val) {
				return false
			}
		default:
			if isUnsafeExtensionKey(key) || !safeExtensionObject(val) {
				return false
			}
		}
	}

	var logs map[string]json.RawMessage
	if json.Unmarshal(logsRaw, &logs) != nil || logs == nil {
		return false
	}
	for key, val := range logs {
		if managedAllowedObservabilityLogKeys[key] {
			continue
		}
		if isUnsafeExtensionKey(key) || !safeExtensionValue(val, 0) {
			return false
		}
	}
	// CPA resolves file logs under the prepared Gateway logs directory (or
	// <oauth.auth-dir>/logs fallback) with no configurable path key; allow the
	// Console Logs toggle in either direction while requiring an exact JSON bool.
	if _, ok := parseExactJSONBool(logs["logging-to-file"]); !ok {
		return false
	}

	var usage map[string]json.RawMessage
	if json.Unmarshal(usageRaw, &usage) != nil || usage == nil {
		return false
	}
	for key, val := range usage {
		if managedAllowedObservabilityUsageKeys[key] {
			continue
		}
		if isUnsafeExtensionKey(key) || !safeExtensionValue(val, 0) {
			return false
		}
	}
	return exactJSONBool(usage["usage-statistics-enabled"], p.usageStatistics)
}

func matchesDisabledPprof(raw json.RawMessage) bool {
	var pprof map[string]json.RawMessage
	if json.Unmarshal(raw, &pprof) != nil || pprof == nil {
		return false
	}
	for key, val := range pprof {
		switch key {
		case "enable":
			if !exactJSONBool(val, false) {
				return false
			}
		case "addr":
			var addr string
			if json.Unmarshal(val, &addr) != nil {
				return false
			}
		default:
			if isUnsafeExtensionKey(key) || !safeExtensionValue(val, 0) {
				return false
			}
		}
	}
	return true
}

var managedAllowedDiscoveryKeys = map[string]bool{
	"enabled":              true,
	"service-name":         true,
	"service-type":         true,
	"subtypes":             true,
	"interfaces":           true,
	"auth-required":        true,
	"advertise-management": true,
}

// matchesManagedServer enforces Supervisor's bound host and port 8317 while
// permitting safe operator-editable `server.*` leaves, CPA 8.1+ deployment
// identity (`server-id`), disabled defaults (`discovery`, `tls`, confined
// `kv-store`), and benign future CPA v8.x non-path/non-listener server knobs.
func matchesManagedServer(raw json.RawMessage) (string, bool) {
	return matchesSupervisorServer(raw, false)
}

func matchesSupervisorServer(raw json.RawMessage, allowSourceGitHubToken bool) (string, bool) {
	var server map[string]json.RawMessage
	if json.Unmarshal(raw, &server) != nil || server == nil {
		return "", false
	}
	var boundHost string
	if json.Unmarshal(server["host"], &boundHost) != nil || !exactJSONInt(server["port"], 8317) {
		return "", false
	}
	for key, val := range server {
		switch key {
		case "host", "port":
		case "server-id":
			if !matchesValidServerID(val) {
				return "", false
			}
		case "shutdown-timeout", "max-request-body-bytes":
			if !exactJSONNonNegativeInt64(val) {
				return "", false
			}
		case "commercial-mode":
			if _, ok := parseExactJSONBool(val); !ok {
				return "", false
			}
		case "trusted-proxies":
			if !isStringSlice(val) {
				return "", false
			}
		case "response-compression":
			if !matchesResponseCompression(val) {
				return "", false
			}
		case "discovery":
			if !matchesDisabledDiscovery(val) {
				return "", false
			}
		case "tls":
			if !matchesDisabledTLS(val) {
				return "", false
			}
		case "kv-store":
			if !matchesAdoptKVStore(val) {
				return "", false
			}
		case "github-token":
			if !allowSourceGitHubToken {
				return "", false
			}
			var token string
			if json.Unmarshal(val, &token) != nil {
				return "", false
			}
		default:
			if isUnsafeExtensionKey(key) || !safeExtensionValue(val, 0) {
				return "", false
			}
		}
	}
	return boundHost, true
}

func matchesValidServerID(raw json.RawMessage) bool {
	var id string
	if json.Unmarshal(raw, &id) != nil || len(id) != 36 {
		return false
	}
	nonZero := false
	for i := range len(id) {
		ch := id[i]
		switch i {
		case 8, 13, 18, 23:
			if ch != '-' {
				return false
			}
		default:
			isDigit := ch >= '0' && ch <= '9'
			isLowerHex := ch >= 'a' && ch <= 'f'
			isUpperHex := ch >= 'A' && ch <= 'F'
			if !isDigit && !isLowerHex && !isUpperHex {
				return false
			}
			if ch != '0' {
				nonZero = true
			}
		}
	}
	return nonZero
}

func matchesDisabledDiscovery(raw json.RawMessage) bool {
	var discovery map[string]json.RawMessage
	if json.Unmarshal(raw, &discovery) != nil || discovery == nil {
		return false
	}
	for key, val := range discovery {
		if key == "enabled" {
			if !exactJSONBool(val, false) {
				return false
			}
			continue
		}
		if managedAllowedDiscoveryKeys[key] {
			continue
		}
		if isUnsafeExtensionKey(key) || !safeExtensionValue(val, 0) {
			return false
		}
	}
	return true
}

func matchesDisabledTLS(raw json.RawMessage) bool {
	var tlsCfg map[string]json.RawMessage
	if json.Unmarshal(raw, &tlsCfg) != nil || tlsCfg == nil {
		return false
	}
	for key, val := range tlsCfg {
		switch key {
		case "enable":
			if !exactJSONBool(val, false) {
				return false
			}
		case "cert", "key":
			var s string
			if json.Unmarshal(val, &s) != nil {
				return false
			}
		default:
			if isUnsafeExtensionKey(key) || !safeExtensionValue(val, 0) {
				return false
			}
		}
	}
	return true
}

var unsafeExtensionKeySegments = map[string]bool{
	// Filesystem / socket / mount confinement
	"dir": true, "dirs": true, "directory": true,
	"path": true, "paths": true, "file": true, "files": true,
	"root": true, "roots": true, "socket": true, "sock": true,
	"unix": true, "mount": true,
	// Listener / bind / endpoint / control-plane hijack
	"listen": true, "listener": true, "bind": true,
	"port": true, "ports": true, "host": true, "hosts": true,
	"addr": true, "address": true, "endpoint": true, "endpoints": true,
	"url": true, "urls": true, "uri": true, "tls": true, "cert": true,
	"panel": true, "remote": true, "pprof": true,
	// Auth / secret / bypass hijack (note: "token" alone is not blocked by
	// segment so LLM/OAuth knobs like "token-refresh-skew" or "max-tokens" stay
	// forward-compatible; credential token keys are caught by suffix/exact match).
	"secret": true, "secrets": true,
	"password": true, "passwd": true,
	"bypass": true, "anonymous": true, "insecure": true,
}

var unsafeExtensionExactKeys = map[string]bool{
	"key":            true,
	"keys":           true,
	"token":          true,
	"auth":           true,
	"credential":     true,
	"credentials":    true,
	"github-token":   true,
	"access-token":   true,
	"refresh-token":  true,
	"bearer-token":   true,
	"auth-token":     true,
	"id-token":       true,
	"api-token":      true,
}

var unsafeExtensionKeySuffixes = []string{
	"-key", "_key", "-keys", "_keys",
	"-auth", "_auth",
}

func isUnsafeExtensionKey(key string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(key))
	if trimmed == "" || unsafeExtensionExactKeys[trimmed] {
		return true
	}
	for _, suffix := range unsafeExtensionKeySuffixes {
		if strings.HasSuffix(trimmed, suffix) {
			return true
		}
	}
	for _, seg := range strings.FieldsFunc(trimmed, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == '/'
	}) {
		if unsafeExtensionKeySegments[seg] {
			return true
		}
	}
	return false
}

func safeExtensionObject(raw json.RawMessage) bool {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return false
	}
	for key, val := range obj {
		if isUnsafeExtensionKey(key) || !safeExtensionValue(val, 1) {
			return false
		}
	}
	return true
}

func safeExtensionValue(raw json.RawMessage, depth int) bool {
	if depth > 6 {
		return false
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	switch trimmed[0] {
	case 'n':
		return false
	case 't', 'f':
		_, ok := parseExactJSONBool(trimmed)
		return ok
	case '"':
		var s string
		if json.Unmarshal(trimmed, &s) != nil {
			return false
		}
		lower := strings.ToLower(strings.TrimSpace(s))
		if strings.HasPrefix(lower, "/") ||
			strings.HasPrefix(lower, "~/") ||
			strings.Contains(lower, "\\") ||
			(len(lower) >= 2 && lower[0] >= 'a' && lower[0] <= 'z' && lower[1] == ':') ||
			strings.Contains(lower, "..") ||
			strings.Contains(lower, "://") {
			return false
		}
		return true
	case '{':
		var obj map[string]json.RawMessage
		if json.Unmarshal(trimmed, &obj) != nil || obj == nil {
			return false
		}
		for key, val := range obj {
			if isUnsafeExtensionKey(key) || !safeExtensionValue(val, depth+1) {
				return false
			}
		}
		return true
	case '[':
		var arr []json.RawMessage
		if json.Unmarshal(trimmed, &arr) != nil || arr == nil {
			return false
		}
		for _, elem := range arr {
			if !safeExtensionValue(elem, depth+1) {
				return false
			}
		}
		return true
	default:
		if trimmed[0] != '-' && (trimmed[0] < '0' || trimmed[0] > '9') {
			return false
		}
		var num json.Number
		dec := json.NewDecoder(bytes.NewReader(trimmed))
		dec.UseNumber()
		if dec.Decode(&num) != nil || dec.More() {
			return false
		}
		f, err := strconv.ParseFloat(string(num), 64)
		return err == nil && !math.IsInf(f, 0) && !math.IsNaN(f)
	}
}
func isJSONObject(raw json.RawMessage) bool {
	var m map[string]json.RawMessage
	return json.Unmarshal(raw, &m) == nil && m != nil
}

func isStringSlice(raw json.RawMessage) bool {
	var s []string
	return json.Unmarshal(raw, &s) == nil && s != nil
}

func parseExactJSONBool(raw json.RawMessage) (bool, bool) {
	trimmed := bytes.TrimSpace(raw)
	if bytes.Equal(trimmed, []byte("true")) {
		return true, true
	}
	if bytes.Equal(trimmed, []byte("false")) {
		return false, true
	}
	return false, false
}

func exactJSONBool(raw json.RawMessage, want bool) bool {
	got, ok := parseExactJSONBool(raw)
	return ok && got == want
}

func exactJSONInt(raw json.RawMessage, want int) bool {
	var got int
	if json.Unmarshal(raw, &got) != nil || got != want {
		return false
	}
	var rawNum json.Number
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimSpace(raw)))
	dec.UseNumber()
	if dec.Decode(&rawNum) != nil {
		return false
	}
	i, err := rawNum.Int64()
	return err == nil && int(i) == want
}
