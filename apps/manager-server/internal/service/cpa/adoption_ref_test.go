package cpa

import (
	"slices"
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
		"oauth":     {bootstrap.AdoptionRefCPAAuthFiles, bootstrap.AdoptionRefProviderStateAPIKeys},
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
