package cpa

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestGatewayPathTableCoversAcceptedInventory pins both the adoption-pinned
// table (39 leaves, 11 groups, 5 families) and the expanded runtime gateway page
// table (88 leaves, 21 groups, 6 families including payload.replace).
func TestGatewayPathTableCoversAcceptedInventory(t *testing.T) {
	if len(gatewayLeaves) != 39 {
		t.Fatalf("leaf count %d", len(gatewayLeaves))
	}
	if len(ConfigLeafPaths()) != len(gatewayLeaves) {
		t.Fatal("leaf paths drifted")
	}
	if len(ConfigGroupIDs()) != 11 || len(ConfigRuleFamilyPaths()) != 5 {
		t.Fatalf("adoption pinned counts drifted: %d groups, %d families", len(ConfigGroupIDs()), len(ConfigRuleFamilyPaths()))
	}
	if len(gatewayPageLeaves) != 88 || len(GatewayPageLeafPaths()) != 88 {
		t.Fatalf("page leaf count %d", len(gatewayPageLeaves))
	}
	seen := map[string]string{}
	for _, group := range gatewayPageGroups {
		for _, path := range group.Leaves {
			leaf, ok := LookupGatewayPageLeaf(path)
			if !ok {
				t.Fatalf("%s: group %s names an unknown leaf", path, group.ID)
			}
			if leaf.Group != group.ID {
				t.Fatalf("%s: leaf group %s, group table %s", path, leaf.Group, group.ID)
			}
			if !strings.HasPrefix(path, group.Prefix+".") {
				t.Fatalf("%s: outside group prefix %s", path, group.Prefix)
			}
			if other, dup := seen[path]; dup {
				t.Fatalf("%s: in %s and %s", path, other, group.ID)
			}
			seen[path] = group.ID
		}
	}
	if len(seen) != len(gatewayPageLeaves) {
		t.Fatalf("groups cover %d of %d leaves", len(seen), len(gatewayPageLeaves))
	}
	if got := len(GatewayPageGroupIDs()); got != 21 {
		t.Fatalf("group count %d", got)
	}
	if got := len(GatewayPageRuleFamilyIDs()); got != 6 {
		t.Fatalf("rule family count %d", got)
	}
	for _, family := range []struct {
		name    string
		raw     bool
		filter  bool
		replace bool
	}{
		{"default", false, false, false}, {"default-raw", true, false, false},
		{"override", false, false, false}, {"override-raw", true, false, false},
		{"replace", false, false, true}, {"filter", false, true, false},
	} {
		entry, ok := LookupGatewayPageRuleFamily(family.name)
		if !ok || entry.Raw != family.raw || entry.Filter != family.filter || entry.Replace != family.replace ||
			entry.Path != "requests.payload."+family.name {
			t.Fatalf("family %s: %+v", family.name, entry)
		}
	}
}

// TestPrivateProfileStaysFrozen proves the page table cannot widen the private
// H1 profile: the Host keeps exactly its six leaves.
func TestPrivateProfileStaysFrozen(t *testing.T) {
	want := []string{
		"requests.nonstream-keepalive-interval",
		"requests.passthrough-headers",
		"requests.streaming.bootstrap-retries",
		"requests.streaming.keepalive-seconds",
		"routing.retry.max-retry-interval",
		"routing.retry.request-retry",
	}
	got := PrivateConfigLeafPaths()
	if len(got) != len(want) {
		t.Fatalf("private profile %v", got)
	}
	for index, path := range want {
		if got[index] != path {
			t.Fatalf("private profile %v", got)
		}
		if err := ValidatePrivateConfigLeafValue(path, sampleValue(path)); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	if err := ValidatePrivateConfigLeafValue("requests.proxy-url", "http://proxy.test:8080"); err == nil {
		t.Fatal("private profile widened to a page leaf")
	}
	if err := ValidatePrivateConfigLeafValue("routing.strategy", "round-robin"); err == nil {
		t.Fatal("private profile widened to a string leaf")
	}
}

func sampleValue(path string) any {
	leaf, _ := LookupConfigLeaf(path)
	switch leaf.Type {
	case ConfigLeafInt:
		return 1
	case ConfigLeafBool:
		return true
	default:
		return "value"
	}
}

// TestLeafValuesAreShapesOnly proves CPAMP never restates a CPA bound: a value
// CPA accepts is a shape, and CPA owns the range.
func TestLeafValuesAreShapesOnly(t *testing.T) {
	for _, tc := range []struct {
		path  string
		value any
		ok    bool
	}{
		{"routing.retry.request-retry", 11, true},
		{"routing.retry.request-retry", 0, true},
		{"routing.retry.request-retry", -1, true},
		{"requests.streaming.keepalive-seconds", 301, true},
		{"routing.retry.request-retry", "0", false},
		{"routing.retry.request-retry", 1.5, false},
		{"routing.retry.request-retry", nil, false},
		{"requests.passthrough-headers", true, true},
		{"requests.passthrough-headers", 1, false},
		{"routing.strategy", "weighted-round-robin", true},
		{"routing.strategy", 1, false},
		{"multimedia.disable-image-generation", true, true},
		{"multimedia.disable-image-generation", false, true},
		{"multimedia.disable-image-generation", "chat", true},
		{"multimedia.disable-image-generation", "passthrough", true},
		{"multimedia.disable-image-generation", "disabled", false},
		{"multimedia.gpt-image-2-base-model", "gpt-5.4-mini", true},
		{"oauth.providers.antigravity.sensitive-words", []string{"API", "proxy"}, true},
		{"oauth.providers.antigravity.sensitive-words", []any{1}, false},
		{"oauth.providers.antigravity.sensitive-words-replace", map[string]string{"old": "new"}, true},
		{"oauth.providers.antigravity.sensitive-words-replace", map[string]string{"": "new"}, false},
		{"oauth.providers.antigravity.sensitive-patterns-replace", []map[string]string{{"pattern": "^foo", "replacement": ""}}, true},
		{"oauth.providers.antigravity.sensitive-patterns-replace", []map[string]string{{"pattern": " ", "replacement": ""}}, false},
	} {
		err := ValidateConfigLeafValue(tc.path, tc.value)
		if (err == nil) != tc.ok {
			t.Fatalf("%s=%v ok=%v err=%v", tc.path, tc.value, tc.ok, err)
		}
	}
	raw, err := ParseConfigLeafValue("multimedia.disable-image-generation", json.RawMessage(`"chat"`))
	if err != nil || raw != "chat" {
		t.Fatal(raw, err)
	}
	if listVal, err := ParseConfigLeafValue("oauth.providers.antigravity.sensitive-words", json.RawMessage(`["a","b"]`)); err != nil || len(listVal.([]string)) != 2 {
		t.Fatalf("string list parse failed: %v %v", listVal, err)
	}
	if _, err = ParseConfigLeafValue("multimedia.disable-image-generation", json.RawMessage(`null`)); err == nil {
		t.Fatal("null accepted as a leaf value")
	}
	if _, err = ParseConfigLeafValue("routing.retry.request-retry", json.RawMessage(`{}`)); err == nil {
		t.Fatal("object accepted as a scalar")
	}
	if _, err = ParseConfigLeafValue("unknown.path", json.RawMessage(`1`)); err == nil {
		t.Fatal("unknown path accepted")
	}
}

// TestProxyURLCredentialsAreRejected is one of the two product rules CPAMP owns.
// The scheme list is CPA's business, so every scheme and every "no proxy" spelling
// CPA accepts must pass here; only a credential the browser would echo back into a
// projected page is refused, and that refusal is its own error so the page can
// show it on the field.
func TestProxyURLCredentialsAreRejected(t *testing.T) {
	for _, tc := range []struct {
		value string
		ok    bool
	}{
		{"http://proxy.test:8080", true},
		{"https://user:pass@proxy.test:8080", false},
		{"socks5://user@proxy.test:1080", false},
		{"socks5h://proxy.test:1080", true},
		{"ftp://proxy.test", true},
		{"proxy.test:8080", true},
		{"", true},
		{"direct", true},
		{"none", true},
		{"DIRECT", true},
	} {
		err := ValidateProxyURL(tc.value)
		if (err == nil) != tc.ok {
			t.Fatalf("%q ok=%v err=%v", tc.value, tc.ok, err)
		}
		if !tc.ok && !errors.Is(err, ErrProxyURLCredentials) {
			t.Fatalf("%q refused for the wrong reason: %v", tc.value, err)
		}
	}
	if err := ValidateProxyURL(1); !errors.Is(err, ErrInvalidConfigPath) {
		t.Fatalf("non-string proxy = %v", err)
	}
}

// TestRuleFamiliesRejectDuplicatePathsAndBadShapes is the other product rule.
func TestRuleFamiliesRejectDuplicatePathsAndBadShapes(t *testing.T) {
	valid := map[string]string{
		"default":      `[{"models":[{"name":"gpt-*","protocol":"codex","headers":{"X-Tier":"gold"},"exist":["metadata"]}],"params":{"reasoning.effort":"high","temperature":0.2}}]`,
		"default-raw":  `[{"models":[{"name":"gpt-*"}],"params":{"response_format":"{\"type\":\"json_object\"}"}}]`,
		"override":     `[{"models":[{"name":"*-5","from-protocol":"responses","match":[{"metadata.client":"x"}],"not-exist":["tools"]}],"params":{"top_p":1}}]`,
		"override-raw": `[{"models":[{"name":"*"}],"params":{}}]`,
		"replace":      `[{"models":[{"name":"*","protocol":"claude"}],"target":{"role":"user","positions":[1,2]},"replacements":[{"old":"new"}]}]`,
		"filter":       `[{"models":[{"name":"gpt-*"}],"params":["metadata.client","metadata.tags"]}]`,
	}
	for family, body := range valid {
		if _, err := ParseConfigRules(family, json.RawMessage(body)); err != nil {
			t.Fatalf("%s: %v", family, err)
		}
	}
	invalid := map[string]struct {
		family string
		body   string
	}{
		"duplicate filter path":          {"filter", `[{"models":[{"name":"*"}],"params":["a","a"]}]`},
		"duplicate exist path":           {"default", `[{"models":[{"name":"*","exist":["a","a"]}],"params":{}}]`},
		"duplicate match path":           {"default", `[{"models":[{"name":"*","match":[{"a":1},{"a":2}]}],"params":{}}]`},
		"folded duplicate header":        {"default", `[{"models":[{"name":"*","headers":{"X-A":"1","x-a":"2"}}],"params":{}}]`},
		"raw value not json":             {"default-raw", `[{"models":[{"name":"*"}],"params":{"a":"{"}}]`},
		"raw value not string":           {"default-raw", `[{"models":[{"name":"*"}],"params":{"a":{}}}]`},
		"filter params not array":        {"filter", `[{"models":[{"name":"*"}],"params":{"a":"b"}}]`},
		"params not object":              {"default", `[{"models":[{"name":"*"}],"params":"[]"}]`},
		"unknown condition":              {"default", `[{"models":[{"name":"*","when":[]}],"params":{}}]`},
		"blank model name":               {"default", `[{"models":[{"name":" "}],"params":{}}]`},
		"unknown rule key":               {"default", `[{"models":[{"name":"*"}],"params":{},"note":"x"}]`},
		"replace missing target":         {"replace", `[{"models":[{"name":"*"}],"replacements":[{"a":"b"}]}]`},
		"replace all and positions both": {"replace", `[{"models":[{"name":"*"}],"target":{"role":"user","all":true,"positions":[1]},"replacements":[{"a":"b"}]}]`},
		"replace empty replacements":     {"replace", `[{"models":[{"name":"*"}],"target":{"role":"user","all":true},"replacements":[]}]`},
		"empty body":                     {"default", ``},
	}
	for name, tc := range invalid {
		if _, err := ParseConfigRules(tc.family, json.RawMessage(tc.body)); err == nil {
			t.Fatalf("%s (%s): invalid rules accepted", name, tc.family)
		}
	}
	if _, err := ParseConfigRules("filter", json.RawMessage(`[]`)); err != nil {
		t.Fatalf("empty family rejected: %v", err)
	}
	if _, err := ParseConfigRules("unknown", json.RawMessage(`[]`)); !errors.Is(err, ErrInvalidConfigPath) {
		t.Fatal("unknown family accepted")
	}
}

// TestPluginScopeIsSeparateFromTheGatewayPage pins the plugin runtime switch as
// its own scope: the accepted gateway page stays at 39 leaves / 11 groups and the
// plugin group is not a gateway group, so neither route can reach the other's
// leaves.
func TestPluginScopeIsSeparateFromTheGatewayPage(t *testing.T) {
	if got := PluginConfigLeafPaths(); len(got) != 1 || got[0] != "plugins.enabled" {
		t.Fatalf("plugin leaves %v", got)
	}
	if got := PluginConfigGroupIDs(); len(got) != 1 || got[0] != PluginConfigGroupID {
		t.Fatalf("plugin groups %v", got)
	}
	group, ok := LookupPluginConfigGroup(PluginConfigGroupID)
	if !ok || group.Prefix != "plugins" || len(group.Leaves) != 1 {
		t.Fatalf("plugin group %+v", group)
	}
	// The gateway page never projects or accepts a plugin path.
	if _, ok := LookupConfigLeaf("plugins.enabled"); ok {
		t.Fatal("the plugin leaf is in the gateway leaf table")
	}
	if _, ok := LookupConfigGroup(PluginConfigGroupID); ok {
		t.Fatal("the plugin group is in the gateway group table")
	}
	if _, ok := LookupPluginConfigLeaf("routing.strategy"); ok {
		t.Fatal("the plugin scope accepts a gateway leaf")
	}
	// plugins.dir stays out of both tables: the Supervisor owns the plugin root.
	if _, ok := LookupPluginConfigLeaf("plugins.dir"); ok {
		t.Fatal("the plugin root is a writable page leaf")
	}
	for _, path := range PluginConfigLeafPaths() {
		if path == "" || strings.HasPrefix(path, "plugins.dir") {
			t.Fatalf("plugin leaf %q", path)
		}
	}
	// The switch is a boolean shape and nothing else.
	for _, raw := range []string{`true`, `false`} {
		value, err := ParsePluginConfigLeafValue("plugins.enabled", json.RawMessage(raw))
		if err != nil || value.(bool) != (raw == "true") {
			t.Fatalf("%s parsed to %v err=%v", raw, value, err)
		}
	}
	for _, raw := range []string{`"true"`, `1`, `null`, `{}`} {
		if _, err := ParsePluginConfigLeafValue("plugins.enabled", json.RawMessage(raw)); err == nil {
			t.Fatalf("%s accepted as the switch", raw)
		}
	}
	if _, err := ParsePluginConfigLeafValue("routing.strategy", json.RawMessage(`"round-robin"`)); err == nil {
		t.Fatal("the plugin parser accepted a gateway leaf")
	}
	if _, err := ParseConfigLeafValue("plugins.enabled", json.RawMessage(`true`)); err == nil {
		t.Fatal("the gateway parser accepted the plugin leaf")
	}
}

func TestIsConfigKVMapLeafAndReplaceRuleEdgeCases(t *testing.T) {
	for _, path := range []string{
		"oauth.providers.antigravity.sensitive-words-replace",
		"oauth.providers.devin.sensitive-words-replace",
		"OAuth.Providers.Antigravity.Sensitive-Words-Replace",
	} {
		if !IsConfigKVMapLeaf(path) {
			t.Fatalf("expected %q to be recognized as ConfigLeafKVMap", path)
		}
	}
	for _, path := range []string{
		"oauth.providers.antigravity.sensitive-patterns-replace",
		"routing.strategy",
		"requests.payload.replace",
		"unknown.leaf",
	} {
		if IsConfigKVMapLeaf(path) {
			t.Fatalf("expected %q not to be ConfigLeafKVMap", path)
		}
	}

	validReplace := map[string]string{
		"all false with positions":      `[{"models":[{"name":"*"}],"target":{"role":"user","all":false,"positions":[1,2]},"replacements":[{"old.phrase":"new"}]}]`,
		"all true with empty positions": `[{"models":[{"name":"*"}],"target":{"role":"user","all":true,"positions":[]},"replacements":[{"a":"b"}]}]`,
		"whitespace-only source key":    `[{"models":[{"name":"*"}],"target":{"role":"user","all":true},"replacements":[{" ":""},{"\t\n":" "}]}]`,
	}
	for name, body := range validReplace {
		if _, err := ParseConfigRules("replace", json.RawMessage(body)); err != nil {
			t.Fatalf("%s rejected: %v", name, err)
		}
	}

	invalidReplace := map[string]string{
		"empty string source key":        `[{"models":[{"name":"*"}],"target":{"role":"user","all":true},"replacements":[{"":"b"}]}]`,
		"all false without positions":    `[{"models":[{"name":"*"}],"target":{"role":"user","all":false},"replacements":[{"a":"b"}]}]`,
		"all false with empty positions": `[{"models":[{"name":"*"}],"target":{"role":"user","all":false,"positions":[]},"replacements":[{"a":"b"}]}]`,
		"all non-bool":                   `[{"models":[{"name":"*"}],"target":{"role":"user","all":"false","positions":[1]},"replacements":[{"a":"b"}]}]`,
		"fractional position":            `[{"models":[{"name":"*"}],"target":{"role":"user","positions":[1.5]},"replacements":[{"a":"b"}]}]`,
		"zero position":                  `[{"models":[{"name":"*"}],"target":{"role":"user","positions":[0]},"replacements":[{"a":"b"}]}]`,
	}
	for name, body := range invalidReplace {
		if _, err := ParseConfigRules("replace", json.RawMessage(body)); err == nil {
			t.Fatalf("%s accepted unexpectedly", name)
		}
	}
}
