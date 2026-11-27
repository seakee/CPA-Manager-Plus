package cpa

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
)

// ErrInvalidConfigPath rejects anything outside the gateway configuration path
// table: unknown paths, wrong value shapes and rule families that are not part
// of the accepted scope. Value bounds are never copied here: CPA validates its
// own schema and answers 400/422 for a value it will not persist
// (internal/api/handlers/management/config_v8.go:150-197, v8.0.55).
var ErrInvalidConfigPath = errors.New("invalid config path")

// ConfigLeafType is the JSON shape CPA accepts for one leaf. It is a shape, not
// a range: an integer stays an integer, a switch stays a boolean and a text
// leaf stays a string.
type ConfigLeafType string

const (
	ConfigLeafInt         ConfigLeafType = "int"
	ConfigLeafBool        ConfigLeafType = "bool"
	ConfigLeafString      ConfigLeafType = "string"
	ConfigLeafImageMode   ConfigLeafType = "image-mode"
	ConfigLeafStringList  ConfigLeafType = "string-list"
	ConfigLeafKVMap       ConfigLeafType = "kv-map"
	ConfigLeafPatternList ConfigLeafType = "pattern-list"
)

// ConfigLeafKind is presentation metadata only: the page renders a control for
// the kind and never derives a bound from it.
type ConfigLeafKind string

const (
	ConfigKindNumber      ConfigLeafKind = "number"
	ConfigKindBool        ConfigLeafKind = "bool"
	ConfigKindText        ConfigLeafKind = "text"
	ConfigKindDuration    ConfigLeafKind = "duration"
	ConfigKindURL         ConfigLeafKind = "url"
	ConfigKindSelect      ConfigLeafKind = "select"
	ConfigKindStringList  ConfigLeafKind = "string-list"
	ConfigKindKVMap       ConfigLeafKind = "kv-map"
	ConfigKindPatternList ConfigLeafKind = "pattern-list"
)

// ConfigLeaf is one writable CPA YAML leaf of the gateway page.
//
// Every path below exists in CPA v8.0.55 (tag e6e5708e) and is addressed through
// its canonical v8 name: internal/config/config_v8.go builds the same alias
// projection the management GET/PATCH endpoints apply before serving a path.
type ConfigLeaf struct {
	Path string
	Type ConfigLeafType
	Kind ConfigLeafKind
	// Group is the page group this leaf belongs to; one save writes exactly one
	// group.
	Group string
	// Private marks the frozen private H1 profile subset. That profile keeps its
	// existing six leaves and the Host reservation protocol; it is never widened
	// to the full table.
	Private bool
	// Inherit is the value CPA resolves for a *bool leaf whose key is absent
	// (internal/config/config_types.go:373, sdk/cliproxy/service_config.go:37).
	// A missing key and an explicit null both mean "unset", which is a third
	// state and NOT the same as an explicit false, so the page offers
	// inherit / on / off and labels inherit with this value. It is nil for every
	// leaf where absent already means false.
	Inherit *bool
	// Bounds is the range the private Host enforces on a private leaf
	// (apps/runtime-supervisor/internal/privateconfig/fields.go:18-25). It exists
	// only for the frozen private subset: the page never restates a CPA bound.
	Bounds *ConfigLeafBounds
}

// ConfigLeafBounds is the inclusive integer range of one private H1 leaf.
type ConfigLeafBounds struct {
	Min int
	Max int
}

// ConfigGroup is one page group: a single PATCH prefix plus its ordered leaves.
// Saving a group sends one PATCH below Prefix with only the edited leaves, and
// one DELETE per leaf restored to its default.
type ConfigGroup struct {
	ID     string
	Prefix string
	Leaves []string
}

// gatewayLeaves is the single writable scope of the gateway configuration page.
// It is generated from the accepted r3 field inventory and verified leaf by leaf
// against the CPA v8.0.55 schema; the page never edits a path outside it.
//
//	internal/config/config_types.go:352-374   routing strategy/affinity/force-model-prefix
//	internal/config/config.go:86-103          routing retry and cooldown
//	internal/config/sdk_config.go:35-109      requests, streaming, multimedia
//	internal/config/config_types.go:126-155   upstream claude header defaults, xai
//	internal/config/config.go:234             client codex compatibility
//	internal/config/config_types.go:60-100    upstream codex compatibility
var gatewayCoreLeaves = []ConfigLeaf{
	{Path: "routing.strategy", Type: ConfigLeafString, Kind: ConfigKindSelect, Group: "routing"},
	{Path: "routing.session-affinity", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "routing"},
	{Path: "routing.session-affinity-ttl", Type: ConfigLeafString, Kind: ConfigKindDuration, Group: "routing"},
	{Path: "routing.session-affinity-subagents", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "routing", Inherit: boolValue(true)},
	{Path: "routing.force-model-prefix", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "routing"},

	{Path: "routing.retry.request-retry", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "retry", Private: true, Bounds: &ConfigLeafBounds{Min: 0, Max: 10}},
	{Path: "routing.retry.max-retry-credentials", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "retry"},
	{Path: "routing.retry.max-retry-interval", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "retry", Private: true, Bounds: &ConfigLeafBounds{Min: 0, Max: 300}},

	{Path: "routing.cooldown.disable-cooling", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "cooldown"},
	{Path: "routing.cooldown.save-cooldown-status", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "cooldown"},
	{Path: "routing.cooldown.transient-error-cooldown-seconds", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "cooldown"},

	{Path: "requests.proxy-url", Type: ConfigLeafString, Kind: ConfigKindURL, Group: "request"},
	{Path: "requests.passthrough-headers", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "request", Private: true},
	{Path: "requests.nonstream-keepalive-interval", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "request", Private: true, Bounds: &ConfigLeafBounds{Min: 0, Max: 300}},

	{Path: "requests.streaming.keepalive-seconds", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "streaming", Private: true, Bounds: &ConfigLeafBounds{Min: 0, Max: 300}},
	{Path: "requests.streaming.bootstrap-retries", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "streaming", Private: true, Bounds: &ConfigLeafBounds{Min: 0, Max: 10}},

	{Path: "multimedia.disable-image-generation", Type: ConfigLeafImageMode, Kind: ConfigKindSelect, Group: "media"},
	{Path: "multimedia.gpt-image-2-base-model", Type: ConfigLeafString, Kind: ConfigKindText, Group: "media"},
	{Path: "multimedia.video-result-auth-cache-ttl", Type: ConfigLeafString, Kind: ConfigKindDuration, Group: "media"},

	{Path: "client.codex.enable-apply-patch", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "client"},
	{Path: "client.codex.optimize-multi-agent-v2", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "client"},

	{Path: "upstream.codex.response-steering", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "codex"},
	{Path: "upstream.codex.disable-codex-cloaking", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "codex"},
	{Path: "upstream.codex.stream-bootstrap-buffering", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "codex"},
	{Path: "upstream.codex.stream-bootstrap-timeout", Type: ConfigLeafString, Kind: ConfigKindDuration, Group: "codex"},
	{Path: "upstream.codex.orphan-delegation-compatibility", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "codex"},
	{Path: "upstream.codex.model-level-cooling", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "codex"},

	{Path: "upstream.claude.model-level-cooling", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "claude"},
	{Path: "upstream.claude.disable-claude-cloak-mode", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "claude"},
	{Path: "upstream.claude.disable-cloaking-model-list", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "claude"},

	{Path: "upstream.claude.header-defaults.user-agent", Type: ConfigLeafString, Kind: ConfigKindText, Group: "headers"},
	{Path: "upstream.claude.header-defaults.package-version", Type: ConfigLeafString, Kind: ConfigKindText, Group: "headers"},
	{Path: "upstream.claude.header-defaults.runtime-version", Type: ConfigLeafString, Kind: ConfigKindText, Group: "headers"},
	{Path: "upstream.claude.header-defaults.os", Type: ConfigLeafString, Kind: ConfigKindText, Group: "headers"},
	{Path: "upstream.claude.header-defaults.arch", Type: ConfigLeafString, Kind: ConfigKindText, Group: "headers"},
	{Path: "upstream.claude.header-defaults.timeout", Type: ConfigLeafString, Kind: ConfigKindText, Group: "headers"},
	{Path: "upstream.claude.header-defaults.timezone", Type: ConfigLeafString, Kind: ConfigKindText, Group: "headers"},
	{Path: "upstream.claude.header-defaults.stabilize-device-profile", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "headers"},

	{Path: "upstream.xai.inject-x-search", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "xai"},
}

// gatewayExtraLeaves extends the runtime gateway configuration page with CPA v8
// controls without altering the pinned bootstrap/adoption item universe.
var gatewayExtraLeaves = []ConfigLeaf{
	{Path: "routing.session-affinity-rebind-on", Type: ConfigLeafString, Kind: ConfigKindSelect, Group: "routing"},

	{Path: "quota-exceeded.switch-project", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "quota"},
	{Path: "quota-exceeded.switch-preview-model", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "quota"},

	{Path: "oauth.auth-auto-refresh-workers", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "oauth"},

	{Path: "requests.tls-handshake-timeout-seconds", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "request"},
	{Path: "requests.streaming.send-keepalive-before-ft", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "streaming"},

	{Path: "server.max-request-body-bytes", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "server"},
	{Path: "server.shutdown-timeout", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "server"},
	{Path: "server.commercial-mode", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "server"},

	{Path: "server.response-compression.enabled", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "compression", Inherit: boolValue(true)},
	{Path: "server.response-compression.min-bytes", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "compression"},

	{Path: "oauth.providers.aistudio.ws-auth", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "aistudio", Inherit: boolValue(true)},

	{Path: "oauth.providers.codex.live-media-relay.enabled", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "live_relay"},
	{Path: "oauth.providers.codex.live-media-relay.max-sessions", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "live_relay"},
	{Path: "oauth.providers.codex.live-media-relay.disable-private-remote-ips", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "live_relay"},
	{Path: "oauth.providers.codex.live-media-relay.public-ip", Type: ConfigLeafString, Kind: ConfigKindText, Group: "live_relay"},
	{Path: "oauth.providers.codex.live-media-relay.udp-port-min", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "live_relay"},
	{Path: "oauth.providers.codex.live-media-relay.udp-port-max", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "live_relay"},

	{Path: "client.codex.web-search-result-cache.enabled", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "client"},
	{Path: "client.codex.web-search-result-cache.ttl", Type: ConfigLeafString, Kind: ConfigKindDuration, Group: "client"},
	{Path: "client.codex.web-search-result-cache.limit", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "client"},

	{Path: "client.claude.show-codex-commentary", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "client_claude"},

	{Path: "upstream.codex.upstream-request-compression", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "codex"},
	{Path: "upstream.codex.upstream-request-compression-min-bytes", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "codex"},
	{Path: "upstream.codex.websocket-compression", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "codex", Inherit: boolValue(true)},

	{Path: "oauth.providers.codex.signin-with-chatgpt-identifier", Type: ConfigLeafString, Kind: ConfigKindText, Group: "codex_oauth"},
	{Path: "oauth.providers.codex.header-defaults.user-agent", Type: ConfigLeafString, Kind: ConfigKindText, Group: "codex_oauth"},
	{Path: "oauth.providers.codex.header-defaults.beta-features", Type: ConfigLeafString, Kind: ConfigKindText, Group: "codex_oauth"},

	{Path: "upstream.claude.tool-search", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "claude"},
	{Path: "upstream.claude.server-side-fallback", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "claude"},
	{Path: "upstream.claude.session-date-pin-days", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "claude"},

	{Path: "upstream.claude.header-defaults.inject-diagnostics", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "headers", Inherit: boolValue(true)},
	{Path: "upstream.claude.header-defaults.client-request-id", Type: ConfigLeafString, Kind: ConfigKindSelect, Group: "headers"},
	{Path: "upstream.claude.header-defaults.native-entrypoints", Type: ConfigLeafStringList, Kind: ConfigKindStringList, Group: "headers"},
	{Path: "upstream.claude.header-defaults.anthropic-beta-strip", Type: ConfigLeafStringList, Kind: ConfigKindStringList, Group: "headers"},
	{Path: "upstream.claude.header-defaults.anthropic-beta-append", Type: ConfigLeafStringList, Kind: ConfigKindStringList, Group: "headers"},

	{Path: "oauth.providers.antigravity.antigravity-credits", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "antigravity"},
	{Path: "oauth.providers.antigravity.signature-cache-enabled", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "antigravity", Inherit: boolValue(true)},
	{Path: "oauth.providers.antigravity.signature-bypass-strict", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "antigravity"},
	{Path: "oauth.providers.antigravity.tos-pause-window", Type: ConfigLeafString, Kind: ConfigKindDuration, Group: "antigravity"},
	{Path: "oauth.providers.antigravity.connection-pool.enabled", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "antigravity"},
	{Path: "oauth.providers.antigravity.connection-pool.idle-conn-timeout", Type: ConfigLeafString, Kind: ConfigKindDuration, Group: "antigravity"},
	{Path: "oauth.providers.antigravity.connection-pool.max-idle-conns-per-host", Type: ConfigLeafInt, Kind: ConfigKindNumber, Group: "antigravity"},
	{Path: "oauth.providers.antigravity.sensitive-words", Type: ConfigLeafStringList, Kind: ConfigKindStringList, Group: "antigravity"},
	{Path: "oauth.providers.antigravity.sensitive-words-replace", Type: ConfigLeafKVMap, Kind: ConfigKindKVMap, Group: "antigravity"},
	{Path: "oauth.providers.antigravity.sensitive-patterns-replace", Type: ConfigLeafPatternList, Kind: ConfigKindPatternList, Group: "antigravity"},

	{Path: "oauth.providers.devin.sensitive-words", Type: ConfigLeafStringList, Kind: ConfigKindStringList, Group: "devin"},
	{Path: "oauth.providers.devin.sensitive-words-replace", Type: ConfigLeafKVMap, Kind: ConfigKindKVMap, Group: "devin"},
	{Path: "oauth.providers.devin.sensitive-patterns-replace", Type: ConfigLeafPatternList, Kind: ConfigKindPatternList, Group: "devin"},
}
var gatewayNonPageLeaves = []ConfigLeaf{
	{Path: "models.catalog", Type: ConfigLeafString, Kind: ConfigKindURL, Group: "models"},
	{Path: "models.codex-catalog", Type: ConfigLeafString, Kind: ConfigKindURL, Group: "models"},
	{Path: "models.devin-catalog", Type: ConfigLeafString, Kind: ConfigKindURL, Group: "models"},

	{Path: "server.trusted-proxies", Type: ConfigLeafStringList, Kind: ConfigKindStringList, Group: "server"},
	{Path: "server.github-token", Type: ConfigLeafString, Kind: ConfigKindText, Group: "server"},
	{Path: "server.kv-store.disabled", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "server"},

	{Path: "oauth.providers.claude.oauth-include-create-api-key", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "claude_oauth", Inherit: boolValue(true)},

	{Path: "observability.logs.debug", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "observability_logs"},
	{Path: "observability.logs.request-log", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: "observability_logs"},
}

var gatewayPageLeaves = append(append([]ConfigLeaf(nil), gatewayCoreLeaves...), gatewayExtraLeaves...)

var gatewayLeaves = append(append([]ConfigLeaf(nil), gatewayPageLeaves...), gatewayNonPageLeaves...)

var imageModeValues = map[string]bool{"true": true, "false": true, "chat": true, "passthrough": true}

type ConfigRuleFamily struct {
	Family  string
	Path    string
	Raw     bool
	Filter  bool
	Replace bool
}

var gatewayRuleFamilies = []ConfigRuleFamily{
	{Family: "default", Path: "requests.payload.default"},
	{Family: "default-raw", Path: "requests.payload.default-raw", Raw: true},
	{Family: "override", Path: "requests.payload.override"},
	{Family: "override-raw", Path: "requests.payload.override-raw", Raw: true},
	{Family: "replace", Path: "requests.payload.replace", Replace: true},
	{Family: "filter", Path: "requests.payload.filter", Filter: true},
}

var gatewayPageRuleFamilies = []ConfigRuleFamily{
	{Family: "default", Path: "requests.payload.default"},
	{Family: "default-raw", Path: "requests.payload.default-raw", Raw: true},
	{Family: "override", Path: "requests.payload.override"},
	{Family: "override-raw", Path: "requests.payload.override-raw", Raw: true},
	{Family: "replace", Path: "requests.payload.replace", Replace: true},
	{Family: "filter", Path: "requests.payload.filter", Filter: true},
}

const ConfigRulesGroupID = "rules"

var gatewayGroups = []ConfigGroup{
	{ID: "routing", Prefix: "routing", Leaves: []string{
		"routing.strategy", "routing.session-affinity", "routing.session-affinity-ttl",
		"routing.session-affinity-subagents", "routing.session-affinity-rebind-on",
		"routing.force-model-prefix",
	}},
	{ID: "quota", Prefix: "quota-exceeded", Leaves: []string{
		"quota-exceeded.switch-project", "quota-exceeded.switch-preview-model",
	}},
	{ID: "models", Prefix: "models", Leaves: []string{
		"models.catalog", "models.codex-catalog", "models.devin-catalog",
	}},
	{ID: "oauth", Prefix: "oauth", Leaves: []string{
		"oauth.auth-auto-refresh-workers",
	}},
	{ID: "retry", Prefix: "routing.retry", Leaves: []string{
		"routing.retry.request-retry", "routing.retry.max-retry-credentials", "routing.retry.max-retry-interval",
	}},
	{ID: "cooldown", Prefix: "routing.cooldown", Leaves: []string{
		"routing.cooldown.disable-cooling", "routing.cooldown.save-cooldown-status",
		"routing.cooldown.transient-error-cooldown-seconds",
	}},
	{ID: "request", Prefix: "requests", Leaves: []string{
		"requests.proxy-url", "requests.tls-handshake-timeout-seconds",
		"requests.passthrough-headers", "requests.nonstream-keepalive-interval",
	}},
	{ID: "streaming", Prefix: "requests.streaming", Leaves: []string{
		"requests.streaming.keepalive-seconds", "requests.streaming.bootstrap-retries",
		"requests.streaming.send-keepalive-before-ft",
	}},
	{ID: "server", Prefix: "server", Leaves: []string{
		"server.max-request-body-bytes", "server.shutdown-timeout", "server.commercial-mode",
		"server.trusted-proxies", "server.github-token", "server.kv-store.disabled",
	}},
	{ID: "compression", Prefix: "server.response-compression", Leaves: []string{
		"server.response-compression.enabled", "server.response-compression.min-bytes",
	}},
	{ID: "aistudio", Prefix: "oauth.providers.aistudio", Leaves: []string{
		"oauth.providers.aistudio.ws-auth",
	}},
	{ID: "claude_oauth", Prefix: "oauth.providers.claude", Leaves: []string{
		"oauth.providers.claude.oauth-include-create-api-key",
	}},
	{ID: "media", Prefix: "multimedia", Leaves: []string{
		"multimedia.disable-image-generation", "multimedia.gpt-image-2-base-model",
		"multimedia.video-result-auth-cache-ttl",
	}},
	{ID: "live_relay", Prefix: "oauth.providers.codex.live-media-relay", Leaves: []string{
		"oauth.providers.codex.live-media-relay.enabled",
		"oauth.providers.codex.live-media-relay.max-sessions",
		"oauth.providers.codex.live-media-relay.disable-private-remote-ips",
		"oauth.providers.codex.live-media-relay.public-ip",
		"oauth.providers.codex.live-media-relay.udp-port-min",
		"oauth.providers.codex.live-media-relay.udp-port-max",
	}},
	{ID: "client", Prefix: "client.codex", Leaves: []string{
		"client.codex.enable-apply-patch", "client.codex.optimize-multi-agent-v2",
		"client.codex.web-search-result-cache.enabled",
		"client.codex.web-search-result-cache.ttl",
		"client.codex.web-search-result-cache.limit",
	}},
	{ID: "client_claude", Prefix: "client.claude", Leaves: []string{
		"client.claude.show-codex-commentary",
	}},
	{ID: "codex", Prefix: "upstream.codex", Leaves: []string{
		"upstream.codex.response-steering", "upstream.codex.disable-codex-cloaking",
		"upstream.codex.stream-bootstrap-buffering", "upstream.codex.stream-bootstrap-timeout",
		"upstream.codex.orphan-delegation-compatibility", "upstream.codex.model-level-cooling",
		"upstream.codex.upstream-request-compression",
		"upstream.codex.upstream-request-compression-min-bytes",
		"upstream.codex.websocket-compression",
	}},
	{ID: "codex_oauth", Prefix: "oauth.providers.codex", Leaves: []string{
		"oauth.providers.codex.signin-with-chatgpt-identifier",
		"oauth.providers.codex.header-defaults.user-agent",
		"oauth.providers.codex.header-defaults.beta-features",
	}},
	{ID: "claude", Prefix: "upstream.claude", Leaves: []string{
		"upstream.claude.model-level-cooling", "upstream.claude.disable-claude-cloak-mode",
		"upstream.claude.disable-cloaking-model-list",
		"upstream.claude.tool-search", "upstream.claude.server-side-fallback",
		"upstream.claude.session-date-pin-days",
	}},
	{ID: "headers", Prefix: "upstream.claude.header-defaults", Leaves: []string{
		"upstream.claude.header-defaults.user-agent", "upstream.claude.header-defaults.package-version",
		"upstream.claude.header-defaults.runtime-version", "upstream.claude.header-defaults.os",
		"upstream.claude.header-defaults.arch", "upstream.claude.header-defaults.timeout",
		"upstream.claude.header-defaults.timezone", "upstream.claude.header-defaults.stabilize-device-profile",
		"upstream.claude.header-defaults.inject-diagnostics",
		"upstream.claude.header-defaults.client-request-id",
		"upstream.claude.header-defaults.native-entrypoints",
		"upstream.claude.header-defaults.anthropic-beta-strip",
		"upstream.claude.header-defaults.anthropic-beta-append",
	}},
	{ID: "antigravity", Prefix: "oauth.providers.antigravity", Leaves: []string{
		"oauth.providers.antigravity.antigravity-credits",
		"oauth.providers.antigravity.signature-cache-enabled",
		"oauth.providers.antigravity.signature-bypass-strict",
		"oauth.providers.antigravity.tos-pause-window",
		"oauth.providers.antigravity.connection-pool.enabled",
		"oauth.providers.antigravity.connection-pool.idle-conn-timeout",
		"oauth.providers.antigravity.connection-pool.max-idle-conns-per-host",
		"oauth.providers.antigravity.sensitive-words",
		"oauth.providers.antigravity.sensitive-words-replace",
		"oauth.providers.antigravity.sensitive-patterns-replace",
	}},
	{ID: "devin", Prefix: "oauth.providers.devin", Leaves: []string{
		"oauth.providers.devin.sensitive-words",
		"oauth.providers.devin.sensitive-words-replace",
		"oauth.providers.devin.sensitive-patterns-replace",
	}},
	{ID: "xai", Prefix: "upstream.xai", Leaves: []string{"upstream.xai.inject-x-search"}},
	{ID: "observability_logs", Prefix: "observability.logs", Leaves: []string{
		"observability.logs.debug", "observability.logs.request-log",
	}},
}

var gatewayPageGroups = []ConfigGroup{
	{ID: "routing", Prefix: "routing", Leaves: []string{
		"routing.strategy", "routing.session-affinity", "routing.session-affinity-ttl",
		"routing.session-affinity-subagents", "routing.session-affinity-rebind-on",
		"routing.force-model-prefix",
	}},
	{ID: "quota", Prefix: "quota-exceeded", Leaves: []string{
		"quota-exceeded.switch-project", "quota-exceeded.switch-preview-model",
	}},
	{ID: "oauth", Prefix: "oauth", Leaves: []string{
		"oauth.auth-auto-refresh-workers",
	}},
	{ID: "retry", Prefix: "routing.retry", Leaves: []string{
		"routing.retry.request-retry", "routing.retry.max-retry-credentials", "routing.retry.max-retry-interval",
	}},
	{ID: "cooldown", Prefix: "routing.cooldown", Leaves: []string{
		"routing.cooldown.disable-cooling", "routing.cooldown.save-cooldown-status",
		"routing.cooldown.transient-error-cooldown-seconds",
	}},
	{ID: "request", Prefix: "requests", Leaves: []string{
		"requests.proxy-url", "requests.tls-handshake-timeout-seconds",
		"requests.passthrough-headers", "requests.nonstream-keepalive-interval",
	}},
	{ID: "streaming", Prefix: "requests.streaming", Leaves: []string{
		"requests.streaming.keepalive-seconds", "requests.streaming.bootstrap-retries",
		"requests.streaming.send-keepalive-before-ft",
	}},
	{ID: "server", Prefix: "server", Leaves: []string{
		"server.max-request-body-bytes", "server.shutdown-timeout", "server.commercial-mode",
	}},
	{ID: "compression", Prefix: "server.response-compression", Leaves: []string{
		"server.response-compression.enabled", "server.response-compression.min-bytes",
	}},
	{ID: "aistudio", Prefix: "oauth.providers.aistudio", Leaves: []string{
		"oauth.providers.aistudio.ws-auth",
	}},
	{ID: "media", Prefix: "multimedia", Leaves: []string{
		"multimedia.disable-image-generation", "multimedia.gpt-image-2-base-model",
		"multimedia.video-result-auth-cache-ttl",
	}},
	{ID: "live_relay", Prefix: "oauth.providers.codex.live-media-relay", Leaves: []string{
		"oauth.providers.codex.live-media-relay.enabled",
		"oauth.providers.codex.live-media-relay.max-sessions",
		"oauth.providers.codex.live-media-relay.disable-private-remote-ips",
		"oauth.providers.codex.live-media-relay.public-ip",
		"oauth.providers.codex.live-media-relay.udp-port-min",
		"oauth.providers.codex.live-media-relay.udp-port-max",
	}},
	{ID: "client", Prefix: "client.codex", Leaves: []string{
		"client.codex.enable-apply-patch", "client.codex.optimize-multi-agent-v2",
		"client.codex.web-search-result-cache.enabled",
		"client.codex.web-search-result-cache.ttl",
		"client.codex.web-search-result-cache.limit",
	}},
	{ID: "client_claude", Prefix: "client.claude", Leaves: []string{
		"client.claude.show-codex-commentary",
	}},
	{ID: "codex", Prefix: "upstream.codex", Leaves: []string{
		"upstream.codex.response-steering", "upstream.codex.disable-codex-cloaking",
		"upstream.codex.stream-bootstrap-buffering", "upstream.codex.stream-bootstrap-timeout",
		"upstream.codex.orphan-delegation-compatibility", "upstream.codex.model-level-cooling",
		"upstream.codex.upstream-request-compression",
		"upstream.codex.upstream-request-compression-min-bytes",
		"upstream.codex.websocket-compression",
	}},
	{ID: "codex_oauth", Prefix: "oauth.providers.codex", Leaves: []string{
		"oauth.providers.codex.signin-with-chatgpt-identifier",
		"oauth.providers.codex.header-defaults.user-agent",
		"oauth.providers.codex.header-defaults.beta-features",
	}},
	{ID: "claude", Prefix: "upstream.claude", Leaves: []string{
		"upstream.claude.model-level-cooling", "upstream.claude.disable-claude-cloak-mode",
		"upstream.claude.disable-cloaking-model-list",
		"upstream.claude.tool-search", "upstream.claude.server-side-fallback",
		"upstream.claude.session-date-pin-days",
	}},
	{ID: "headers", Prefix: "upstream.claude.header-defaults", Leaves: []string{
		"upstream.claude.header-defaults.user-agent", "upstream.claude.header-defaults.package-version",
		"upstream.claude.header-defaults.runtime-version", "upstream.claude.header-defaults.os",
		"upstream.claude.header-defaults.arch", "upstream.claude.header-defaults.timeout",
		"upstream.claude.header-defaults.timezone", "upstream.claude.header-defaults.stabilize-device-profile",
		"upstream.claude.header-defaults.inject-diagnostics",
		"upstream.claude.header-defaults.client-request-id",
		"upstream.claude.header-defaults.native-entrypoints",
		"upstream.claude.header-defaults.anthropic-beta-strip",
		"upstream.claude.header-defaults.anthropic-beta-append",
	}},
	{ID: "antigravity", Prefix: "oauth.providers.antigravity", Leaves: []string{
		"oauth.providers.antigravity.antigravity-credits",
		"oauth.providers.antigravity.signature-cache-enabled",
		"oauth.providers.antigravity.signature-bypass-strict",
		"oauth.providers.antigravity.tos-pause-window",
		"oauth.providers.antigravity.connection-pool.enabled",
		"oauth.providers.antigravity.connection-pool.idle-conn-timeout",
		"oauth.providers.antigravity.connection-pool.max-idle-conns-per-host",
		"oauth.providers.antigravity.sensitive-words",
		"oauth.providers.antigravity.sensitive-words-replace",
		"oauth.providers.antigravity.sensitive-patterns-replace",
	}},
	{ID: "devin", Prefix: "oauth.providers.devin", Leaves: []string{
		"oauth.providers.devin.sensitive-words",
		"oauth.providers.devin.sensitive-words-replace",
		"oauth.providers.devin.sensitive-patterns-replace",
	}},
	{ID: "xai", Prefix: "upstream.xai", Leaves: []string{"upstream.xai.inject-x-search"}},
}

// PluginConfigGroupID is the page group of the plugin runtime switch. It is a
// scope of its own and NOT a gateway page group: the accepted gateway page is
// complete without it, and the plugin route projects no gateway leaf.
const PluginConfigGroupID = "plugins"

// pluginLeaves is the closed write scope of the plugin runtime switch. The key
// exists in CPA v8's own configuration document; `plugins.dir` is deliberately
// absent because the Supervisor owns the plugin root, and a page that could
// repoint plugin discovery would be a native-code path.
var pluginLeaves = []ConfigLeaf{
	{Path: "plugins.enabled", Type: ConfigLeafBool, Kind: ConfigKindBool, Group: PluginConfigGroupID},
}

// pluginGroups is the plugin scope's group table: one group, one leaf, and the
// switch's own parent mapping as its PATCH prefix.
var pluginGroups = []ConfigGroup{
	{ID: PluginConfigGroupID, Prefix: "plugins", Leaves: []string{"plugins.enabled"}},
}

var (
	gatewayLeafIndex       = buildLeafIndex(gatewayLeaves)
	gatewayPageLeafIndex   = buildLeafIndex(gatewayPageLeaves)
	gatewayGroupIndex      = buildGroupIndex(gatewayGroups)
	gatewayPageGroupIndex  = buildGroupIndex(gatewayPageGroups)
	gatewayFamilyIndex     = buildFamilyIndex(gatewayRuleFamilies)
	gatewayPageFamilyIndex = buildFamilyIndex(gatewayPageRuleFamilies)
	pluginLeafIndex        = buildLeafIndex(pluginLeaves)
	pluginGroupIndex       = buildGroupIndex(pluginGroups)
	privateConfigLeaves    = buildPrivateLeaves()
	privateConfigGroups    = buildPrivateGroups()
)

func buildLeafIndex(leaves []ConfigLeaf) map[string]ConfigLeaf {
	index := make(map[string]ConfigLeaf, len(leaves))
	for _, leaf := range leaves {
		index[leaf.Path] = leaf
	}
	return index
}

func buildGroupIndex(groups []ConfigGroup) map[string]ConfigGroup {
	index := make(map[string]ConfigGroup, len(groups))
	for _, group := range groups {
		index[group.ID] = group
	}
	return index
}

func buildFamilyIndex(families []ConfigRuleFamily) map[string]ConfigRuleFamily {
	index := make(map[string]ConfigRuleFamily, len(families))
	for _, family := range families {
		index[family.Family] = family
	}
	return index
}

// buildPrivateLeaves freezes the private H1 profile subset. The Host keeps the
// same six leaves, so widening the page table can never widen that profile.
func buildPrivateLeaves() []string {
	var paths []string
	for _, leaf := range gatewayLeaves {
		if leaf.Private {
			paths = append(paths, leaf.Path)
		}
	}
	sort.Strings(paths)
	return paths
}

// buildPrivateGroups lists the page groups holding a private leaf, in page order.
func buildPrivateGroups() []string {
	var ids []string
	for _, group := range gatewayGroups {
		for _, path := range group.Leaves {
			if leaf, ok := gatewayLeafIndex[path]; ok && leaf.Private {
				ids = append(ids, group.ID)
				break
			}
		}
	}
	return ids
}

// LookupConfigLeaf is the single membership test for the gateway path table.
func LookupConfigLeaf(path string) (ConfigLeaf, bool) {
	leaf, ok := gatewayLeafIndex[path]
	return leaf, ok
}

// LookupPrivateConfigLeaf is the membership test of the frozen private H1
// profile. An intent outside it is never a private Host operation.
func LookupPrivateConfigLeaf(path string) (ConfigLeaf, bool) {
	leaf, ok := gatewayLeafIndex[path]
	return leaf, ok && leaf.Private
}

// ConfigLeafPaths returns every writable leaf in page order.
func ConfigLeafPaths() []string {
	paths := make([]string, 0, len(gatewayLeaves))
	for _, leaf := range gatewayLeaves {
		paths = append(paths, leaf.Path)
	}
	return paths
}

// PrivateConfigLeafPaths returns the frozen private H1 profile subset.
func PrivateConfigLeafPaths() []string {
	return append([]string(nil), privateConfigLeaves...)
}

// ConfigGroupIDs returns the page groups in order.
func ConfigGroupIDs() []string {
	ids := make([]string, 0, len(gatewayGroups))
	for _, group := range gatewayGroups {
		ids = append(ids, group.ID)
	}
	return ids
}

// LookupConfigGroup returns one page group and its leaves.
func LookupConfigGroup(id string) (ConfigGroup, bool) {
	group, ok := gatewayGroupIndex[id]
	return group, ok
}

// LookupConfigRuleFamily returns one payload rule family.
func LookupConfigRuleFamily(family string) (ConfigRuleFamily, bool) {
	entry, ok := gatewayFamilyIndex[family]
	return entry, ok
}

// PluginConfigGroupIDs returns the plugin scope's groups in order.
func PluginConfigGroupIDs() []string {
	ids := make([]string, 0, len(pluginGroups))
	for _, group := range pluginGroups {
		ids = append(ids, group.ID)
	}
	return ids
}

// LookupPluginConfigGroup is the group membership test of the plugin scope. It
// is a separate table from the gateway page: neither scope can name the other's
// group, so a plugin save is never a gateway group save.
func LookupPluginConfigGroup(id string) (ConfigGroup, bool) {
	group, ok := pluginGroupIndex[id]
	return group, ok
}

// PluginConfigLeafPaths returns the plugin scope's leaves in page order.
func PluginConfigLeafPaths() []string {
	paths := make([]string, 0, len(pluginLeaves))
	for _, leaf := range pluginLeaves {
		paths = append(paths, leaf.Path)
	}
	return paths
}

// LookupPluginConfigLeaf is the leaf membership test of the plugin scope.
func LookupPluginConfigLeaf(path string) (ConfigLeaf, bool) {
	leaf, ok := pluginLeafIndex[path]
	return leaf, ok
}

// ConfigRuleFamilyIDs returns every rule family identifier in page order.
func ConfigRuleFamilyIDs() []string {
	ids := make([]string, 0, len(gatewayRuleFamilies))
	for _, family := range gatewayRuleFamilies {
		ids = append(ids, family.Family)
	}
	return ids
}

// ConfigRuleFamilyPaths returns every rule family path in page order.
func ConfigRuleFamilyPaths() []string {
	paths := make([]string, 0, len(gatewayRuleFamilies))
	for _, family := range gatewayRuleFamilies {
		paths = append(paths, family.Path)
	}
	return paths
}

// ConfigGroupPrefix resolves the single PATCH prefix of a page group.
func ConfigGroupPrefix(id string) (string, error) {
	group, ok := gatewayGroupIndex[id]
	if !ok {
		return "", ErrInvalidConfigPath
	}
	return group.Prefix, nil
}

func lookupConfigOrPageLeaf(path string) (ConfigLeaf, bool) {
	if leaf, ok := gatewayLeafIndex[path]; ok {
		return leaf, true
	}
	leaf, ok := gatewayPageLeafIndex[path]
	return leaf, ok
}

func lookupConfigOrPageFamily(family string) (ConfigRuleFamily, bool) {
	if entry, ok := gatewayFamilyIndex[family]; ok {
		return entry, true
	}
	entry, ok := gatewayPageFamilyIndex[family]
	return entry, ok
}

// ConfigLeafEndpoint is the management endpoint of one leaf. CPA answers 404 for
// a missing key, 400 for an unknown path and 422 for a value it refuses.
func ConfigLeafEndpoint(path string) (string, error) {
	if _, ok := lookupConfigOrPageLeaf(path); !ok {
		return "", ErrInvalidConfigPath
	}
	return configPathEndpoint(path), nil
}

// ConfigValueEndpoint is the management endpoint of a whole payload rule family.
func ConfigValueEndpoint(family string) (string, error) {
	entry, ok := lookupConfigOrPageFamily(family)
	if !ok {
		return "", ErrInvalidConfigPath
	}
	return configPathEndpoint(entry.Path), nil
}

func configPathEndpoint(path string) string {
	return "/v8/management/config/" + strings.ReplaceAll(path, ".", "/")
}

// ConfigPathResource is the Public API resource suffix of one dotted CPA path.
func ConfigPathResource(path string) string {
	return strings.ReplaceAll(path, ".", "/")
}

// ValidateConfigLeafValue accepts only the leaf's JSON shape. Bounds belong to
// CPA; CPAMP never restates them.
func ValidateConfigLeafValue(path string, value any) error {
	leaf, ok := lookupConfigOrPageLeaf(path)
	if !ok {
		return ErrInvalidConfigPath
	}
	switch leaf.Type {
	case ConfigLeafInt:
		if _, ok := value.(int); !ok {
			return ErrInvalidConfigPath
		}
	case ConfigLeafBool:
		if _, ok := value.(bool); !ok {
			return ErrInvalidConfigPath
		}
	case ConfigLeafString:
		if _, ok := value.(string); !ok {
			return ErrInvalidConfigPath
		}
	case ConfigLeafImageMode:
		switch typed := value.(type) {
		case bool:
		case string:
			if !imageModeValues[typed] {
				return ErrInvalidConfigPath
			}
		default:
			return ErrInvalidConfigPath
		}
	case ConfigLeafStringList:
		if _, err := normalizeStringListValue(value); err != nil {
			return ErrInvalidConfigPath
		}
	case ConfigLeafKVMap:
		if _, err := normalizeKVMapValue(value); err != nil {
			return ErrInvalidConfigPath
		}
	case ConfigLeafPatternList:
		if _, err := normalizePatternListValue(value); err != nil {
			return ErrInvalidConfigPath
		}
	default:
		return ErrInvalidConfigPath
	}
	return nil
}

// ValidatePrivateConfigLeafValue accepts a value for the frozen private H1
// profile only. It exists so widening the page table cannot widen the private
// Host contract: the Runtime Supervisor enforces 0-10 / 0-300 on the same six
// fields (apps/runtime-supervisor/internal/privateconfig/fields.go:18-25), so the
// page refuses an out-of-range value on the field instead of surfacing an opaque
// Host refusal after the write is reserved. CPA-backed leaves never inherit these
// bounds.
func ValidatePrivateConfigLeafValue(path string, value any) error {
	leaf, ok := LookupPrivateConfigLeaf(path)
	if !ok {
		return ErrInvalidConfigPath
	}
	if err := ValidateConfigLeafValue(path, value); err != nil {
		return err
	}
	if leaf.Bounds != nil {
		number, ok := value.(int)
		if !ok || number < leaf.Bounds.Min || number > leaf.Bounds.Max {
			return ErrPrivateConfigOutOfRange
		}
	}
	return nil
}

// ErrPrivateConfigOutOfRange is a private-profile value the Host would refuse.
var ErrPrivateConfigOutOfRange = errors.New("private config value out of range")

// ConfigLeafInheritable returns the value CPA resolves for a leaf whose key is
// absent, and whether absence is a third state at all (see ConfigLeaf.Inherit).
func ConfigLeafInheritable(path string) (bool, bool) {
	leaf, ok := lookupConfigOrPageLeaf(path)
	if !ok || leaf.Inherit == nil {
		return false, false
	}
	return *leaf.Inherit, true
}

// GatewayPageLeafPaths returns all writable leaves of the runtime gateway page.
func GatewayPageLeafPaths() []string {
	paths := make([]string, 0, len(gatewayPageLeaves))
	for _, leaf := range gatewayPageLeaves {
		paths = append(paths, leaf.Path)
	}
	return paths
}

// LookupGatewayPageLeaf looks up any leaf on the runtime gateway page.
func LookupGatewayPageLeaf(path string) (ConfigLeaf, bool) {
	leaf, ok := gatewayPageLeafIndex[path]
	return leaf, ok
}

// IsConfigKVMapLeaf reports whether path names a user-data key-value mapping leaf
// on the runtime gateway page. Keys inside a KVMap leaf are arbitrary user strings
// rather than structural YAML path segments, so writes replace the whole mapping.
func IsConfigKVMapLeaf(path string) bool {
	leaf, ok := lookupConfigOrPageLeaf(strings.ToLower(path))
	return ok && leaf.Type == ConfigLeafKVMap
}

// GatewayPageGroupIDs returns all groups of the runtime gateway page in order.
func GatewayPageGroupIDs() []string {
	ids := make([]string, 0, len(gatewayPageGroups))
	for _, group := range gatewayPageGroups {
		ids = append(ids, group.ID)
	}
	return ids
}

// LookupGatewayPageGroup returns one runtime gateway page group and its leaves.
func LookupGatewayPageGroup(id string) (ConfigGroup, bool) {
	group, ok := gatewayPageGroupIndex[id]
	return group, ok
}

// GatewayPageRuleFamilyIDs returns every runtime gateway rule family identifier.
func GatewayPageRuleFamilyIDs() []string {
	ids := make([]string, 0, len(gatewayPageRuleFamilies))
	for _, family := range gatewayPageRuleFamilies {
		ids = append(ids, family.Family)
	}
	return ids
}

// LookupGatewayPageRuleFamily returns one runtime gateway rule family.
func LookupGatewayPageRuleFamily(family string) (ConfigRuleFamily, bool) {
	entry, ok := gatewayPageFamilyIndex[family]
	return entry, ok
}

// PrivateConfigPaths returns the frozen private profile subset: the six leaves
// the Host reservation protocol has always written. Every other page leaf stays
// read-only there.
func PrivateConfigPaths() []string { return PrivateConfigLeafPaths() }

// PrivateConfigGroups returns the page groups the private profile may save.
func PrivateConfigGroups() []string { return append([]string(nil), privateConfigGroups...) }

// LookupPrivateConfigGroup reports the page group of one private save, restricted
// to the groups that hold a private leaf.
func LookupPrivateConfigGroup(id string) (ConfigGroup, bool) {
	group, ok := gatewayGroupIndex[id]
	if !ok {
		return ConfigGroup{}, false
	}
	allowed := false
	for _, path := range group.Leaves {
		if leaf, ok := gatewayLeafIndex[path]; ok && leaf.Private {
			allowed = true
			break
		}
	}
	if !allowed {
		return ConfigGroup{}, false
	}
	// The page must never offer a leaf the Host would refuse, so a private group
	// carries only its private leaves.
	private := ConfigGroup{ID: group.ID, Prefix: group.Prefix}
	for _, path := range group.Leaves {
		if leaf, ok := gatewayLeafIndex[path]; ok && leaf.Private {
			private.Leaves = append(private.Leaves, path)
		}
	}
	return private, true
}

func boolValue(value bool) *bool { return &value }

// NormalizeConfigValue converts an integral JSON number back into the exact Go
// scalar the table uses, so a value that crossed a JSON boundary still compares
// and validates identically. Non-integral numbers are kept and then rejected.
func NormalizeConfigValue(value any) any {
	if number, ok := value.(float64); ok && number == float64(int(number)) {
		return int(number)
	}
	return value
}

// ParseConfigLeafValue decodes one exact JSON value of a gateway leaf. Null,
// composite values and trailing data are rejected; presence is reported
// separately.
func ParseConfigLeafValue(path string, raw json.RawMessage) (any, error) {
	leaf, ok := lookupConfigOrPageLeaf(path)
	if !ok {
		return nil, ErrInvalidConfigPath
	}
	return parseConfigLeafValue(leaf, raw)
}

// ParsePluginConfigLeafValue decodes one exact JSON value of a plugin-scope leaf.
// It never accepts a gateway leaf: one scope is not a superset of the other.
func ParsePluginConfigLeafValue(path string, raw json.RawMessage) (any, error) {
	leaf, ok := pluginLeafIndex[path]
	if !ok {
		return nil, ErrInvalidConfigPath
	}
	return parseConfigLeafValue(leaf, raw)
}

func parseConfigLeafValue(leaf ConfigLeaf, raw json.RawMessage) (any, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, ErrInvalidConfigPath
	}
	switch leaf.Type {
	case ConfigLeafInt:
		var number int
		if json.Unmarshal(raw, &number) != nil {
			return nil, ErrInvalidConfigPath
		}
		return number, nil
	case ConfigLeafBool:
		var flag bool
		if json.Unmarshal(raw, &flag) != nil {
			return nil, ErrInvalidConfigPath
		}
		return flag, nil
	case ConfigLeafString:
		var text string
		if json.Unmarshal(raw, &text) != nil {
			return nil, ErrInvalidConfigPath
		}
		return text, nil
	case ConfigLeafImageMode:
		var flag bool
		if json.Unmarshal(raw, &flag) == nil {
			return flag, nil
		}
		var text string
		if json.Unmarshal(raw, &text) != nil || !imageModeValues[text] {
			return nil, ErrInvalidConfigPath
		}
		return text, nil
	case ConfigLeafStringList:
		var items []string
		if json.Unmarshal(raw, &items) != nil || items == nil {
			return nil, ErrInvalidConfigPath
		}
		return normalizeStringListValue(items)
	case ConfigLeafKVMap:
		var entries map[string]string
		if json.Unmarshal(raw, &entries) != nil || entries == nil {
			return nil, ErrInvalidConfigPath
		}
		return normalizeKVMapValue(entries)
	case ConfigLeafPatternList:
		var rules []any
		if json.Unmarshal(raw, &rules) != nil || rules == nil {
			return nil, ErrInvalidConfigPath
		}
		return normalizePatternListValue(rules)
	default:
		return nil, ErrInvalidConfigPath
	}
}

// EncodeConfigLeafValue renders the exact JSON scalar or compound value CPA accepts.
func EncodeConfigLeafValue(path string, value any) ([]byte, error) {
	if err := ValidateConfigLeafValue(path, value); err != nil {
		return nil, err
	}
	switch typed := value.(type) {
	case int:
		return []byte(strconv.Itoa(typed)), nil
	case bool:
		if typed {
			return []byte("true"), nil
		}
		return []byte("false"), nil
	case string:
		return json.Marshal(typed)
	case []string, []any, map[string]string, map[string]any, []map[string]string:
		return json.Marshal(typed)
	default:
		return nil, ErrInvalidConfigPath
	}
}

func normalizeStringListValue(value any) ([]string, error) {
	switch typed := value.(type) {
	case []string:
		if typed == nil {
			return nil, ErrInvalidConfigPath
		}
		out := make([]string, len(typed))
		copy(out, typed)
		return out, nil
	case []any:
		if typed == nil {
			return nil, ErrInvalidConfigPath
		}
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, ErrInvalidConfigPath
			}
			out = append(out, text)
		}
		return out, nil
	default:
		return nil, ErrInvalidConfigPath
	}
}

func normalizeKVMapValue(value any) (map[string]string, error) {
	switch typed := value.(type) {
	case map[string]string:
		if typed == nil {
			return nil, ErrInvalidConfigPath
		}
		out := make(map[string]string, len(typed))
		for k, v := range typed {
			if strings.TrimSpace(k) == "" {
				return nil, ErrInvalidConfigPath
			}
			out[k] = v
		}
		return out, nil
	case map[string]any:
		if typed == nil {
			return nil, ErrInvalidConfigPath
		}
		out := make(map[string]string, len(typed))
		for k, rawVal := range typed {
			if strings.TrimSpace(k) == "" {
				return nil, ErrInvalidConfigPath
			}
			v, ok := rawVal.(string)
			if !ok {
				return nil, ErrInvalidConfigPath
			}
			out[k] = v
		}
		return out, nil
	default:
		return nil, ErrInvalidConfigPath
	}
}

func normalizePatternListValue(value any) ([]map[string]string, error) {
	switch typed := value.(type) {
	case []map[string]string:
		if typed == nil {
			return nil, ErrInvalidConfigPath
		}
		out := make([]map[string]string, 0, len(typed))
		for _, item := range typed {
			if item == nil || len(item) != 2 {
				return nil, ErrInvalidConfigPath
			}
			pattern, okP := item["pattern"]
			replacement, okR := item["replacement"]
			if !okP || !okR || strings.TrimSpace(pattern) == "" {
				return nil, ErrInvalidConfigPath
			}
			out = append(out, map[string]string{"pattern": pattern, "replacement": replacement})
		}
		return out, nil
	case []any:
		if typed == nil {
			return nil, ErrInvalidConfigPath
		}
		out := make([]map[string]string, 0, len(typed))
		for _, raw := range typed {
			obj, ok := raw.(map[string]any)
			if !ok {
				return nil, ErrInvalidConfigPath
			}
			for k := range obj {
				if k != "pattern" && k != "replacement" {
					return nil, ErrInvalidConfigPath
				}
			}
			pattern, okP := obj["pattern"].(string)
			replacement, okR := obj["replacement"].(string)
			if !okP || !okR || strings.TrimSpace(pattern) == "" {
				return nil, ErrInvalidConfigPath
			}
			out = append(out, map[string]string{"pattern": pattern, "replacement": replacement})
		}
		return out, nil
	default:
		return nil, ErrInvalidConfigPath
	}
}

// ErrProxyURLCredentials is the one proxy rule CPAMP owns (see ValidateProxyURL).
var ErrProxyURLCredentials = errors.New("proxy url carries credentials")

// ValidateProxyURL is the single product rule CPAMP owns for requests.proxy-url:
// the page refuses a value that embeds userinfo, because no safe reference design
// exists yet. Everything else is CPA's business (sdk/proxyutil/proxy.go:41-74),
// so "", "direct", "none", a bare host and any scheme CPA accepts all pass here
// and are judged by CPA on save.
func ValidateProxyURL(value any) error {
	text, ok := value.(string)
	if !ok {
		return ErrInvalidConfigPath
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || strings.EqualFold(trimmed, "direct") || strings.EqualFold(trimmed, "none") {
		return nil
	}
	// A value without a scheme cannot carry userinfo; CPA rejects such a host if
	// it is invalid for the configured mode.
	_, rest, found := strings.Cut(trimmed, "://")
	if !found {
		return nil
	}
	authority := rest
	if slash := strings.IndexByte(authority, '/'); slash >= 0 {
		authority = authority[:slash]
	}
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		if before := authority[:at]; before != "" {
			return ErrProxyURLCredentials
		}
	}
	return nil
}
