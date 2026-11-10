package cpa

import (
	"bytes"
	"encoding/json"
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
	"observability",  // Enforced (logging-to-file=false, usage-statistics-enabled=true, pprof off; log/usage tuning allowed): keeps stdout logging and usage collection intact.
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
	"models":         true, // Allowed: optional CPA v8 model catalog source selections.
	"credentials":    true, // Allowed: CPA v8 credential concurrency and in-flight observation settings.
	"quota-exceeded": true, // Allowed: CPA v8 quota-exceeded fallback switches.
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
	"usage-statistics-enabled":           true,
	"redis-usage-queue-retention-seconds": true,
}

// matchesManaged validates a New Managed embedded CPA document (with
// access.api-keys blanked by KeySnapshot, or prior to initial key install).
// Security- and Supervisor-owned invariants remain pinned to the destination
// seed template while operator-editable v8 sections are permitted.
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
		if !managedOptionalObjectRoots[name] || !isJSONObject(raw) {
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

	var management map[string]json.RawMessage
	if json.Unmarshal(root["management"], &management) != nil || len(management) != 4 {
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

func (p proofPosture) matchesManagedOAuth(raw json.RawMessage) bool {
	var oauth map[string]json.RawMessage
	if json.Unmarshal(raw, &oauth) != nil || oauth == nil {
		return false
	}
	for key := range oauth {
		if !managedAllowedOAuthKeys[key] {
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
		default:
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
			return false
		}
	}

	var logs map[string]json.RawMessage
	if json.Unmarshal(logsRaw, &logs) != nil || logs == nil {
		return false
	}
	for key := range logs {
		if !managedAllowedObservabilityLogKeys[key] {
			return false
		}
	}
	if !exactJSONBool(logs["logging-to-file"], false) {
		return false
	}

	var usage map[string]json.RawMessage
	if json.Unmarshal(usageRaw, &usage) != nil || usage == nil {
		return false
	}
	for key := range usage {
		if !managedAllowedObservabilityUsageKeys[key] {
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
			return false
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
// permitting disabled defaults (such as server.discovery metadata with mDNS
// advertising off) materialized when CPA's SaveConfigPreserveComments rewrites
// config.yaml.
func matchesManagedServer(raw json.RawMessage) (string, bool) {
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
		case "discovery":
			if !matchesDisabledDiscovery(val) {
				return "", false
			}
		case "tls":
			if !matchesDisabledTLS(val) {
				return "", false
			}
		default:
			return "", false
		}
	}
	return boundHost, true
}

func matchesDisabledDiscovery(raw json.RawMessage) bool {
	var discovery map[string]json.RawMessage
	if json.Unmarshal(raw, &discovery) != nil || discovery == nil {
		return false
	}
	for key, val := range discovery {
		if !managedAllowedDiscoveryKeys[key] {
			return false
		}
		if key == "enabled" && !exactJSONBool(val, false) {
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
			return false
		}
	}
	return true
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
