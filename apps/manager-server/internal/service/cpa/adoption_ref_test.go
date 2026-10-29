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
	"server":        {"commercial-mode", "discovery", "host", "port", "tls"},
	"management":    {"allow-remote", "base-url", "disable-auto-update-panel", "disable-control-panel", "panel-github-repository", "secret-key"},
	"access":        {"api-keys"},
	"oauth":         {"auth-auto-refresh-workers", "auth-dir", "excluded-models", "model-alias", "providers", "request-scoped-errors", "settings"},
	"api-keys":      {"claude", "codex", "gemini", "interactions", "meta", "openai-compatibility", "vertex", "xai"},
	"routing":       {"cooldown", "force-model-prefix", "retry", "session-affinity", "session-affinity-subagents", "session-affinity-ttl", "strategy"},
	"client":        {"codex"},
	"plugins":       {"auth-revision", "dir", "enabled"},
	"observability": {"logs", "pprof", "usage"},
}

// gatewaySectionKeys are the source sections the v8 reader does not name, so its
// report puts them under the unknown root. Their keys are gateway configuration:
// each is either a leaf or a rule family of the gateway table, or it is uncopied
// configuration the record has to state.
var gatewaySectionKeys = map[string][]string{
	"requests":   {"proxy-url", "passthrough-headers", "nonstream-keepalive-interval", "streaming", "payload"},
	"multimedia": {"disable-image-generation", "gpt-image-2-base-model", "video-result-auth-cache-ttl"},
	"upstream":   {"codex", "claude", "xai"},
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
	uncopied := map[string]bool{}
	for _, key := range adoptionUncopiedSourceKeys {
		uncopied[key] = true
	}
	coveredBy := func(root, path string) bool {
		carrier := sourceKeyCarriers[path]
		for _, item := range AdoptionItemUniverse() {
			if AdoptionRootOf(item) != root || uncopied[item] {
				// A key that is in the universe only because it is declared uncopied
				// does not carry itself: that would make the declaration the answer.
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
			if !covered && !uncopied[path] && !wholeRoot {
				t.Fatalf("source key %s is carried by no item and is not reported as uncopied", path)
			}
			if uncopied[path] && covered {
				t.Fatalf("source key %s is reported as uncopied but an item carries it", path)
			}
			if uncopied[path] && wholeRoot {
				t.Fatalf("source key %s is redundant: its root %q already falls back whole", path, root)
			}
		}
	}
	// The sections the reader does not name reach discovery under the unknown
	// root, and each of their keys is gateway configuration the record must state.
	for section, children := range gatewaySectionKeys {
		if root := AdoptionRootOf(section); root != unknownSection {
			t.Fatalf("source section %q discovered as %q, not the unknown root", section, root)
		}
		for _, child := range children {
			path := section + "." + child
			covered := coveredBy(unknownSection, path)
			if !covered && !uncopied[path] {
				t.Fatalf("gateway configuration %s is carried by no item and is not reported as uncopied", path)
			}
		}
	}
	// The declared uncopied keys are real source keys of a named root: a typo or a
	// key that moved would silently cover nothing.
	for key := range uncopied {
		root, child, nested := strings.Cut(key, ".")
		if !nested || !slices.Contains(sourceSectionKeys[root], child) {
			t.Fatalf("declared uncopied key %q is not a source key of a named root", key)
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
		// `requests`, `multimedia` and `upstream` are not named sections: the
		// reader reports that gateway configuration under its catch-all, which is
		// why it carries both the routing decision and the unknown one.
		"unknown_cpa_native_resource": {bootstrap.AdoptionRefConfigRouting, bootstrap.AdoptionRefConfigUnknown},
		// `client` is exactly two gateway leaves. It has no provider section in
		// the copy plan, so it contributes routing configuration and no provider
		// verdict: `provider-state-client` covers configuration no item carries.
		"client":    {bootstrap.AdoptionRefConfigRouting},
		// `oauth` carries the auth directory (and so the credential files), its
		// copyable provider maps, and the source keys nothing copies — its refresh
		// worker count and its provider runtime block.
		"oauth":     {bootstrap.AdoptionRefCPAAuthFiles, bootstrap.AdoptionRefConfigUnknown, bootstrap.AdoptionRefProviderStateAPIKeys},
		"api-keys":  {bootstrap.AdoptionRefProviderStateAPIKeys},
		"routing":   {bootstrap.AdoptionRefConfigRouting},
		"providers": {bootstrap.AdoptionRefProviderStateProviders},
		"server":    {bootstrap.AdoptionRefConfigServer},
		"plugins":   {bootstrap.AdoptionRefConfigPlugins},
		"access":    {bootstrap.AdoptionRefClientAPIKeys},
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
