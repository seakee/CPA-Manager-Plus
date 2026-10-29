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
	// Every root the reader can report contributes at least one reference, and it
	// contributes its own signal: a root with an empty set would be a root whose
	// discovery yields no decision at all.
	for _, root := range AdoptionSectionNames() {
		refs := AdoptionRootRefs(root)
		if len(refs) == 0 {
			t.Fatalf("root %q contributes no reference", root)
		}
		signal, ok := adoptionRootSignals[root]
		if !ok || !slices.Contains(refs, signal) {
			t.Fatalf("root %q does not contribute its own signal %s: %v", root, signal, refs)
		}
		for _, ref := range refs {
			if _, known := bootstrap.FreshAdoptionRule(ref); !known {
				t.Fatalf("root %q contributes %s, which has no adoption rule", root, ref)
			}
		}
	}
}

// The sets are what makes a root's decision list complete: the gateway
// configuration a v8 reader reports under its unknown-root catch-all must record
// the routing decision *and* the unknown one, because a leaf outside the gateway
// table is still the operator's manual work.
func TestAdoptionRootSetsFollowTheReaderRoots(t *testing.T) {
	want := map[string][]bootstrap.ResourceRef{
		// `requests`, `multimedia` and `upstream` are not named sections: the
		// reader reports the gateway configuration under its catch-all.
		"unknown_cpa_native_resource": {bootstrap.AdoptionRefConfigRouting, bootstrap.AdoptionRefConfigUnknown},
		"client":                      {bootstrap.AdoptionRefConfigRouting, bootstrap.AdoptionRefConfigUnknown, bootstrap.AdoptionRefProviderStateClient},
		"oauth":                       {bootstrap.AdoptionRefCPAAuthFiles, bootstrap.AdoptionRefConfigUnknown, bootstrap.AdoptionRefProviderStateAPIKeys},
		"api-keys":                    {bootstrap.AdoptionRefConfigUnknown, bootstrap.AdoptionRefProviderStateAPIKeys},
		"providers":                   {bootstrap.AdoptionRefConfigUnknown, bootstrap.AdoptionRefProviderStateProviders},
		"routing":                     {bootstrap.AdoptionRefConfigRouting, bootstrap.AdoptionRefConfigUnknown},
		"server":                      {bootstrap.AdoptionRefConfigServer, bootstrap.AdoptionRefConfigUnknown},
		"plugins":                     {bootstrap.AdoptionRefConfigPlugins, bootstrap.AdoptionRefConfigUnknown},
		"access":                      {bootstrap.AdoptionRefClientAPIKeys, bootstrap.AdoptionRefConfigUnknown},
	}
	for root, refs := range want {
		if got := AdoptionRootRefs(root); !slices.Equal(got, refs) {
			t.Fatalf("root %q contributes %v, want %v", root, got, refs)
		}
	}
	// A root the reader never reports has no set, so it can authorize nothing.
	if refs := AdoptionRootRefs("requests"); len(refs) != 0 {
		t.Fatalf("a root the reader never reports contributes %v", refs)
	}
	if root := AdoptionRootOf("requests.proxy-url"); root != "unknown_cpa_native_resource" {
		t.Fatalf("gateway configuration under requests discovered as %q", root)
	}
}
