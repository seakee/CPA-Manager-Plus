package cpa

import (
	"slices"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

// The record and the copy gate must be talking about the same thing. Discovery
// records one decision per reference a root contributes; the executor gates each
// item on its own reference. If an item could classify under a reference its root
// does not contribute, the record would tell the operator to redo work the
// executor copied — or authorize a copy the record never mentioned.
//
// This is that invariant, over the whole enumerable universe and over both the
// item's own path and its family identifier (the two spellings the executor
// records).
func TestAdoptionRootSetsCoverEveryItemUnderTheRoot(t *testing.T) {
	for _, item := range AdoptionItemUniverse() {
		root := AdoptionRootOf(item)
		if root == "" {
			t.Fatalf("%q has no discovery root", item)
		}
		ref := AdoptionItemRef(item)
		if !slices.Contains(AdoptionRootRefs(root), ref) {
			t.Fatalf("item %q is gated under %s but root %q contributes %v", item, ref, root, AdoptionRootRefs(root))
		}
	}
	// Every family identifier the executor records classifies exactly as the
	// family's path does, so the two spellings cannot diverge.
	for _, family := range ConfigRuleFamilyIDs() {
		entry, ok := LookupConfigRuleFamily(family)
		if !ok {
			t.Fatalf("family %q has no table entry", family)
		}
		if byID, byPath := AdoptionItemRef(family), AdoptionItemRef(entry.Path); byID != byPath {
			t.Fatalf("family %q classifies as %s but its path as %s", family, byID, byPath)
		}
		if !slices.Contains(AdoptionRootRefs(AdoptionRootOf(family)), AdoptionItemRef(family)) {
			t.Fatalf("family %q is not covered by its root's set", family)
		}
	}
	// Every root the reader can report contributes at least one reference: a root
	// with an empty set would be a root whose discovery yields no decision at all.
	for _, root := range AdoptionSectionNames() {
		refs := AdoptionRootRefs(root)
		if len(refs) == 0 {
			t.Fatalf("root %q contributes no reference", root)
		}
		for _, ref := range refs {
			if _, known := bootstrap.FreshAdoptionRule(ref); !known {
				t.Fatalf("root %q contributes %s, which has no adoption rule", root, ref)
			}
			// Nothing may be in a root's set unless some item under that root
			// classifies as it, or the root has no item at all and declares the
			// reference as its fallback.
			grounded := false
			for _, item := range AdoptionItemUniverse() {
				if AdoptionRootOf(item) == root && AdoptionItemRef(item) == ref {
					grounded = true
					break
				}
			}
			fallback, declared := adoptionRootFallbacks[root]
			if !grounded && !(declared && fallback == ref && !hasUniverseItem(root)) {
				t.Fatalf("root %q contributes %s, but no item under it classifies as that", root, ref)
			}
		}
	}
}

// hasUniverseItem reports whether any enumerable item lands in the root.
func hasUniverseItem(root string) bool {
	for _, item := range AdoptionItemUniverse() {
		if AdoptionRootOf(item) == root {
			return true
		}
	}
	return false
}

// sourceSectionKeys is the closed inventory of every direct child key of a named
// section of the accepted CPA v8.0.13 configuration document, derived from the
// type definitions rather than from any table here:
//
//	internal/config/config.go:10-60      the Config root's own sections
//	internal/config/sdk_config.go:8-15   `client`
//	internal/config/config_v8.go:75-120  the v8→canonical path translation
//
// A reader root is a section the v8 reader names; the gateway table is the only
// source of copyable leaves. A child key that is neither in the gateway table nor
// a copyable provider section is configuration nothing copies, so it must be
// visible to the record — otherwise discovery reports the root as fully
// accounted for while that key vanished from the adoption.
var sourceSectionKeys = map[string][]string{
	"server":        {"commercial-mode", "discovery", "github-token", "host", "kv-store", "max-request-body-bytes", "port", "response-compression", "server-id", "shutdown-timeout", "tls", "trusted-proxies"},
	"management":    {"allow-remote", "base-url", "disable-auto-update-panel", "disable-control-panel", "panel-github-repository", "secret-key"},
	"access":        {"api-keys"},
	"oauth":         {"auth-auto-refresh-workers", "auth-dir", "excluded-models", "model-alias", "providers", "request-scoped-errors", "settings"},
	"api-keys":      {"claude", "codex", "gemini", "interactions", "meta", "openai-compatibility", "vertex", "xai"},
	"routing":       {"cooldown", "force-model-prefix", "retry", "session-affinity", "session-affinity-rebind-on", "session-affinity-subagents", "session-affinity-ttl", "strategy"},
	"client":        {"claude", "codex"},
	"plugins":       {"auth-revision", "configs", "dir", "enabled", "store-auth", "store-sources"},
	"observability": {"logs", "pprof", "usage"},
}

// gatewaySectionKeys are the source sections the v8 reader does not name, so its
// report puts them under the unknown root. Their keys are gateway/business
// configuration: each is carried by a leaf, rule family, or structured section.
var gatewaySectionKeys = map[string][]string{
	"requests":      {"proxy-url", "tls-handshake-timeout-seconds", "passthrough-headers", "nonstream-keepalive-interval", "streaming", "payload"},
	"multimedia":    {"disable-image-generation", "gpt-image-2-base-model", "video-result-auth-cache-ttl"},
	"upstream":      {"codex", "claude", "xai"},
	"models":        {"catalog", "codex-catalog", "devin-catalog"},
	"quota-exceeded": {"switch-project", "switch-preview-model"},
	"credentials":   {"concurrency", "in-flight"},
}
// sourceKeyCarriers names the item that carries a source key whose own path is
// not an item: the source's auth directory is exactly where the credential files
// are discovered, so `credential:<name>` is what carries it.
var sourceKeyCarriers = map[string]string{
	"oauth.auth-dir": CredentialItemClass,
}

// TestEverySourceKeyIsCoveredByItsRoot is the invariant that a source key cannot
// vanish. Every direct child of a named root is either carried by a copyable item
// or listed as an uncopied key, and every uncopied key it lists is really a
// source key of that root, so the table cannot drift into fiction.
func TestEverySourceKeyIsCoveredByItsRoot(t *testing.T) {
	hostManaged := map[string]bool{}
	for key := range adoptionHostManagedSourceKeys {
		hostManaged[key] = true
	}
	coveredBy := func(root, path string) bool {
		carrier := sourceKeyCarriers[path]
		for _, item := range AdoptionItemUniverse() {
			if AdoptionRootOf(item) != root {
				continue
			}
			if item == carrier || item == path || strings.HasPrefix(item, path+".") {
				return true
			}
		}
		return false
	}
	for root, children := range sourceSectionKeys {
		// A root with no enumerable item contributes its whole-root fallback, which
		// already tells the operator to redo the block: its children are covered by
		// that one decision.
		wholeRoot := !hasUniverseItem(root)
		for _, child := range children {
			path := root + "." + child
			covered := coveredBy(root, path)
			if !covered && !hostManaged[path] && !wholeRoot {
				t.Fatalf("source key %s is carried by no item and is not reported as host-managed", path)
			}
			if hostManaged[path] && wholeRoot {
				t.Fatalf("source key %s is redundant: its root %q already falls back whole", path, root)
			}
		}
	}
	// The sections the reader does not name reach discovery under the unknown
	// root, and each of their keys is configuration the record must state.
	for section, children := range gatewaySectionKeys {
		if root := AdoptionRootOf(section); root != unknownSection {
			t.Fatalf("source section %q discovered as %q, not the unknown root", section, root)
		}
		for _, child := range children {
			path := section + "." + child
			if !coveredBy(unknownSection, path) {
				t.Fatalf("gateway configuration %s is carried by no item", path)
			}
		}
	}
	// The declared host-managed keys are real source keys of a named root.
	for key := range hostManaged {
		root, rest, nested := strings.Cut(key, ".")
		topChild, _, _ := strings.Cut(rest, ".")
		if !nested || !slices.Contains(sourceSectionKeys[root], topChild) {
			t.Fatalf("declared host-managed key %q is not a source key of a named root", key)
		}
	}
	// A carried key names an item that really exists.
	for key, carrier := range sourceKeyCarriers {
		if !slices.Contains(AdoptionItemUniverse(), carrier) {
			t.Fatalf("carrier %q of source key %s is not an item", carrier, key)
		}
	}
}

// The sets are what makes a root's decision list complete: the gateway
// configuration a v8 reader reports under its unknown-root catch-all must record
// the routing decision *and* the unknown one, because a leaf outside the gateway
// table is still the operator's manual work.
func TestAdoptionRootSetsFollowTheItems(t *testing.T) {
	// Every set is exactly what the universe's items under that root classify as,
	// plus the declared fallback for a root with no item. Nothing else may appear:
	// a reference in a root's set that no item produces would tell the operator to
	// redo work the executor never does.
	want := map[string][]bootstrap.ResourceRef{
		"unknown_cpa_native_resource": {bootstrap.AdoptionRefConfigRouting, bootstrap.AdoptionRefConfigUnknown},
		"client":                      {bootstrap.AdoptionRefConfigRouting},
		"oauth":                       {bootstrap.AdoptionRefCPAAuthFiles, bootstrap.AdoptionRefConfigRouting, bootstrap.AdoptionRefProviderStateAPIKeys},
		"api-keys":                    {bootstrap.AdoptionRefProviderStateAPIKeys},
		"routing":                     {bootstrap.AdoptionRefConfigRouting},
		"providers":                   {bootstrap.AdoptionRefProviderStateProviders},
		"server":                      {bootstrap.AdoptionRefConfigRouting, bootstrap.AdoptionRefConfigServer},
		"plugins":                     {bootstrap.AdoptionRefConfigPlugins, bootstrap.AdoptionRefConfigRouting},
		"observability":               {bootstrap.AdoptionRefConfigObservability, bootstrap.AdoptionRefConfigRouting},
		"access":                      {bootstrap.AdoptionRefClientAPIKeys},
	}
	for root, refs := range want {
		if got := AdoptionRootRefs(root); !slices.Equal(got, refs) {
			t.Fatalf("root %q contributes %v, want %v", root, got, refs)
		}
	}
	// A root the reader never reports has no set, so it can authorize nothing.
	for _, root := range []string{"requests", "multimedia", "upstream"} {
		if refs := AdoptionRootRefs(root); len(refs) != 0 {
			t.Fatalf("root %q is not a reader root but contributes %v", root, refs)
		}
	}
	if root := AdoptionRootOf("requests.proxy-url"); root != "unknown_cpa_native_resource" {
		t.Fatalf("gateway configuration under requests discovered as %q", root)
	}
}

// The digest is what the consumer pin folds in, so it must move when a
// classification moves and stay still when nothing does.
func TestAdoptionUniverseDigestTracksClassification(t *testing.T) {
	universe := AdoptionItemUniverse()
	digest := AdoptionUniverseDigest()
	if digest != AdoptionUniverseDigest(universe...) {
		t.Fatal("the digest depends on more than the items it is given")
	}
	if digest == AdoptionUniverseDigest(AdoptionItemUniverse()[:len(universe)-1]...) {
		t.Fatal("dropping an item did not change the digest")
	}
	// A leaf the gateway table names and one it does not classify differently, and
	// the digest reflects that.
	if AdoptionUniverseDigest("requests.proxy-url") == AdoptionUniverseDigest("requests.made-up-leaf") {
		t.Fatal("two differently classified items produced the same digest")
	}
}

func TestUnknownV8ConfigPathsDetectsNestedOffTableKeysUnderNamedRoots(t *testing.T) {
	clean := map[string]any{
		"config-version": 8,
		"server": map[string]any{
			"port":            8317,
			"commercial-mode": true,
			"kv-store":        map[string]any{"path": "/var/lib/kv.db", "disabled": false},
		},
		"access": map[string]any{"api-keys": []any{"sk-1"}},
		"plugins": map[string]any{
			"enabled": true,
			"configs": map[string]any{"custom-plugin": map[string]any{"flag": true}},
		},
		"oauth": map[string]any{
			"auth-dir": "/var/lib/auth",
			"providers": map[string]any{
				"antigravity": map[string]any{
					"sensitive-words-replace": map[string]any{"user-key": "mask"},
				},
			},
		},
		"requests": map[string]any{
			"proxy-url": "http://127.0.0.1:8080",
		},
		"credentials": map[string]any{
			"concurrency": map[string]any{"max-limit": 4},
		},
		"providers": map[string]any{},
	}
	if got := UnknownV8ConfigPaths(clean); len(got) != 0 {
		t.Fatalf("clean v8 config reported unknown paths: %v", got)
	}

	withNestedUnknown := map[string]any{
		"config-version": 8,
		"server": map[string]any{
			"port":    8317,
			"mystery": 1,
		},
		"access": map[string]any{"api-keys": []any{"sk-1"}},
		"plugins": map[string]any{
			"enabled":      false,
			"mystery_flag": true,
		},
		"oauth": map[string]any{
			"auth-dir": "/var/lib/auth",
			"providers": map[string]any{
				"codex": map[string]any{
					"live-media-relay": map[string]any{
						"enabled": false,
						"unknown": "x",
					},
				},
			},
		},
		"credentials": map[string]any{
			"extra-flag": true,
		},
	}
	got := UnknownV8ConfigPaths(withNestedUnknown)
	want := []string{
		"credentials.extra-flag",
		"oauth.providers.codex.live-media-relay.unknown",
		"plugins.mystery_flag",
		"server.mystery",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("UnknownV8ConfigPaths = %v, want %v", got, want)
	}
}

func TestScanV8YAMLArchiveCandidatesCoversRealCPAFootCommentShapes(t *testing.T) {
	cases := []struct {
		name         string
		yaml         string
		want         []string
		wantComplete bool
	}{
		{
			name: "flow-style map and nested multiline unknown blocks",
			yaml: `# operator header comment
config-version: 8
server:
    host: 127.0.0.1
    port: 8317
oauth: {auth-dir: "/tmp/auth", providers: {codex: {}}}
plugins: {}
upstream:
    codex:
        disable-codex-cloaking: true

# custom_root:
#     alpha: 1
#     beta: [x, y]
# oauth.providers.codex.retired-setting: false
# plugins.mystery_obj:
#     nested: true
#     items:
#         - one
#         - two
`,
			want: []string{
				"custom_root",
				"oauth.providers.codex.retired-setting",
				"plugins.mystery_obj",
			},
			wantComplete: true,
		},
		{
			name: "flow-style only document with single archived leaf",
			yaml: `config-version: 8
server: {host: "127.0.0.1", port: 8317}
access: {api-keys: ["sk-client-1"]}
oauth: {auth-dir: "/tmp/auth", providers: {codex: {}}}

# oauth.providers.codex.retired-setting: false
`,
			want:         []string{"oauth.providers.codex.retired-setting"},
			wantComplete: true,
		},
		{
			name: "block scalar with blank archive comment line and zero-indent list",
			yaml: `config-version: 8
server:
    port: 8317

# custom_script: |
#     echo hello
#
#     echo world
# custom_list:
# - one
# - two
`,
			want:         []string{"custom_list", "custom_script"},
			wantComplete: true,
		},
		{
			name: "literal dotted keys under known roots and retired unknown dotted roots",
			yaml: `config-version: 8
server:
    port: 8317
plugins: {}

# plugins.retired.option: true
# plugins.retiredOption: true
# management.retired_flag: true
# oauth.providers.codex.a.b: 1
# retired.root.option: true
# custom-operator-note: true
`,
			want: []string{
				"custom-operator-note",
				"management.retired_flag",
				"oauth.providers.codex.a.b",
				"plugins.retired.option",
				"plugins.retiredOption",
				"retired.root.option",
			},
			wantComplete: true,
		},
		{
			name: "colliding literal dotted keys matching known nested schema paths are flagged as candidates",
			yaml: `config-version: 8
server:
    host: 127.0.0.1
    port: 8317
routing:
    strategy: round-robin

# server.kv-store.path: /tmp/colliding-sibling
# routing.strategy: round-robin
# remote-management.allow-remote: false
# oauth.providers.codex.live-media-relay.allow-private-remote-ips: true
# codex.live-media-relay.allow-private-remote-ips: true
`,
			want: []string{
				"codex.live-media-relay.allow-private-remote-ips",
				"oauth.providers.codex.live-media-relay.allow-private-remote-ips",
				"remote-management.allow-remote",
				"routing.strategy",
				"server.kv-store.path",
			},
			wantComplete: true,
		},
		{
			name: "pre-existing top and middle comments plus trailing plain prose without mapping syntax are ignored",
			yaml: `# custom_top_commented: true
# Note: operator header
# debug: false
config-version: 8
# plugins.commented_in_middle: true
server:
    host: 127.0.0.1
    port: 8317
oauth:
    auth-dir: /tmp/auth
    providers:
        codex:
            live-media-relay: {}
# Legacy compatibility reference (comments only; not part of this v8 template).
#   host: ""
#   port: 8317
# operator comment without colon
# just plain prose at end of file
# : bare null key ignored
`,
			want:         nil,
			wantComplete: true,
		},
		{
			name: "non-identifier, spaced, punctuated, complex, multiline flow, numeric, empty, and v7-named trailing mappings are all captured",
			yaml: `config-version: 8
server:
    port: 8317

# plugins.future flag: true
# "  spaced key  ": 1
# "plugins.@weird/key..1": [
#   a,
#   b
# ]
# ? "plugins.
# multiline"
# : true
# 123: numeric-key
# "": empty-string-key
# Note: trailing mapping
# debug: false
# codex:
#     retired_sub: 1
`,
			want: []string{
				"  spaced key  ",
				"123",
				"<empty>",
				"Note",
				"codex",
				"debug",
				"plugins. multiline",
				"plugins.@weird/key..1",
				"plugins.future flag",
			},
			wantComplete: true,
		},
		{
			name: "multi-document stream only inspects trailing tail of first document",
			yaml: `---
# header_before_doc1: true
config-version: 8
server:
    port: 8317

# plugins.mystery_flag: true
---
# second_doc_unknown: true
second_doc_key: 123
`,
			want:         []string{"plugins.mystery_flag"},
			wantComplete: true,
		},
		{
			name: "explicit document end marker stops before subsequent comments",
			yaml: `config-version: 8
server:
    port: 8317

# custom_before_end: true
...
# custom_after_end: true
`,
			want:         []string{"custom_before_end"},
			wantComplete: true,
		},
		{
			name: "block scalar containing indented hash before active closing line",
			yaml: `config-version: 8
server:
    github-token: |
        line-1
        # fake_inside_scalar: true
    port: 8317
`,
			want:         nil,
			wantComplete: true,
		},
		{
			name:         "json payload has no archived yaml comments",
			yaml:         `{"config-version":8,"server":{"port":8317},"access":{"api-keys":["sk-1"]}}`,
			want:         nil,
			wantComplete: true,
		},
		{
			name:         "empty yaml marks scan incomplete",
			yaml:         "   \n",
			want:         nil,
			wantComplete: false,
		},
		{
			name:         "comment-only yaml marks scan incomplete",
			yaml:         "# just a comment\n",
			want:         nil,
			wantComplete: false,
		},
		{
			name:         "non-mapping sequence yaml marks scan incomplete",
			yaml:         "- one\n- two\n",
			want:         nil,
			wantComplete: false,
		},
		{
			name:         "invalid yaml marks scan incomplete",
			yaml:         "config-version: [8\n",
			want:         nil,
			wantComplete: false,
		},
		{
			name:         "invalid utf8 marks scan incomplete",
			yaml:         string([]byte{0xff, 0xfe, 'a'}),
			want:         nil,
			wantComplete: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, complete := ScanV8YAMLArchiveCandidates([]byte(tc.yaml))
			if complete != tc.wantComplete || !slices.Equal(got, tc.want) {
				t.Fatalf("ScanV8YAMLArchiveCandidates = (%v, %v), want (%v, %v)", got, complete, tc.want, tc.wantComplete)
			}
		})
	}
}

func TestAttachV8YAMLArchiveScanSeparatesJSONFromArchiveAndIncompleteState(t *testing.T) {
	doc := map[string]any{
		"config-version": 8,
		"server": map[string]any{
			"port":    8317,
			"mystery": 1,
		},
		"access": map[string]any{"api-keys": []any{"sk-1"}},
	}
	AttachV8YAMLArchiveScan(doc, []string{
		"plugins.mystery_flag",
		"oauth.providers.codex.retired-setting",
		"plugins.mystery_flag",
	}, true)

	// UnknownV8ConfigPaths must remain strictly definite active JSON paths and
	// must never expose archive candidates or NUL-prefixed carrier keys.
	gotJSON := UnknownV8ConfigPaths(doc)
	if !slices.Equal(gotJSON, []string{"server.mystery"}) {
		t.Fatalf("UnknownV8ConfigPaths = %v, want [server.mystery]", gotJSON)
	}
	if slices.Contains(gotJSON, archivedV8YAMLScanCandidatesKey) || slices.Contains(gotJSON, archivedV8YAMLScanIncompleteKey) {
		t.Fatalf("carrier key leaked into UnknownV8ConfigPaths: %v", gotJSON)
	}

	scan := InspectV8UnknownConfig(doc)
	wantCandidates := []string{"oauth.providers.codex.retired-setting", "plugins.mystery_flag"}
	if !slices.Equal(scan.JSONPaths, []string{"server.mystery"}) ||
		!slices.Equal(scan.ArchiveCandidates, wantCandidates) ||
		scan.ArchiveScanIncomplete || !scan.HasAny() {
		t.Fatalf("InspectV8UnknownConfig = %+v, want JSON=[server.mystery] Candidates=%v Incomplete=false", scan, wantCandidates)
	}

	// Clean doc with incomplete YAML scan fails closed in HasAny() while keeping
	// JSONPaths empty.
	incompleteClean := map[string]any{
		"config-version": 8,
		"server":         map[string]any{"port": 8317},
		"access":         map[string]any{"api-keys": []any{"sk-1"}},
	}
	AttachV8YAMLArchiveScan(incompleteClean, nil, false)
	if got := UnknownV8ConfigPaths(incompleteClean); len(got) != 0 {
		t.Fatalf("UnknownV8ConfigPaths(incompleteClean) = %v, want empty", got)
	}
	incompleteScan := InspectV8UnknownConfig(incompleteClean)
	if len(incompleteScan.JSONPaths) != 0 || len(incompleteScan.ArchiveCandidates) != 0 || !incompleteScan.ArchiveScanIncomplete || !incompleteScan.HasAny() {
		t.Fatalf("InspectV8UnknownConfig(incompleteClean) = %+v, want ArchiveScanIncomplete=true HasAny=true", incompleteScan)
	}

	// Re-attaching a clean complete scan clears earlier incomplete/candidate markers.
	AttachV8YAMLArchiveScan(incompleteClean, nil, true)
	cleared := InspectV8UnknownConfig(incompleteClean)
	if cleared.HasAny() {
		t.Fatalf("InspectV8UnknownConfig(cleared) = %+v, want HasAny=false", cleared)
	}
}
