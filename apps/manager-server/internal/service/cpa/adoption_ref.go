package cpa

import (
	"slices"
	"strings"
	"sync"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

// This file is the single adoption classification of the product: the reference
// one adoption item is gated under, the set of references a discovered root
// contributes, and the item universe that binds them. It is one file on purpose,
// and the consumer implementation digest pins it
// (`freshstrategy/production.go`), because a change here changes what an
// adoption copies.
//
// Two questions, two functions, and the second is derived from the first so they
// cannot disagree:
//
//   - AdoptionItemRef(path) is the reference of one copyable item. The executor
//     gates every write on it.
//   - AdoptionRootRefs(root) is the set of references any item under one
//     discovered root can carry. Discovery records a decision for every
//     reference in that set, so the record always covers everything the executor
//     can copy out of that root.
//
// A second hand-written table is exactly what let the two drift apart: a root
// recorded as manual while its items were copied. Here the only hand-written
// parts are the root names the source reader reports and the one reference each
// root contributes as a root.

// unknownSection is where the v8 reader puts every source key it does not name.
// It is a discovery identity, never an upstream key. `requests`, `multimedia`
// and `upstream` are not named sections, so the gateway configuration under them
// arrives under this root.
const unknownSection = "unknown_cpa_native_resource"

// adoptionSectionNames are the stable category names a source read reports, in
// reader order. `AdoptionSectionNames` and `AdoptionRootOf` are the single copy
// of that list for the rest of the product.
var adoptionSectionNames = []string{
	"server", "management", "access", "oauth", "providers", "api-keys",
	"routing", "client", "logging", "usage", "plugins", "observability",
	unknownSection,
}

// AdoptionSectionNames returns the discovery inventory in reader order.
func AdoptionSectionNames() []string { return slices.Clone(adoptionSectionNames) }

// AdoptionRootOf is the discovery root of one item: the named section the source
// document's key belongs to, or unknownSection when the reader does not name it.
// It is the rule the source readers apply, expressed once, so the executor's
// item universe and the record cannot be talking about different documents.
func AdoptionRootOf(item string) string {
	root, _, nested := strings.Cut(item, ".")
	if !nested {
		if slices.Contains(adoptionSectionNames, item) {
			return item
		}
		return unknownSection
	}
	if slices.Contains(adoptionSectionNames, root) {
		return root
	}
	return unknownSection
}

// adoptionRootSignals is the one reference each root contributes as a root, on
// top of the references its copyable items carry. A root is itself a thing the
// operator may have to redo by hand, so every root of the reader needs a
// decision of its own.
//
// The `oauth` root is the one place this differs from its own children, and
// deliberately: the root is where the source keeps its auth directory, so its
// presence is the source's credential surface (`cpa-auth-files`), while its
// copyable children (`model-alias`, `excluded-models`, `settings`,
// `request-scoped-errors`) are provider configuration and resolve below to
// `provider-state-api-keys`. Both references are in the root's set.
var adoptionRootSignals = map[string]bootstrap.ResourceRef{
	"access":        bootstrap.AdoptionRefClientAPIKeys,
	"oauth":         bootstrap.AdoptionRefCPAAuthFiles,
	"server":        bootstrap.AdoptionRefConfigServer,
	"management":    bootstrap.AdoptionRefConfigManagement,
	"routing":       bootstrap.AdoptionRefConfigRouting,
	"logging":       bootstrap.AdoptionRefConfigLogging,
	"usage":         bootstrap.AdoptionRefConfigUsage,
	"plugins":       bootstrap.AdoptionRefConfigPlugins,
	"observability": bootstrap.AdoptionRefConfigObservability,
	unknownSection:  bootstrap.AdoptionRefConfigUnknown,
	"providers":     bootstrap.AdoptionRefProviderStateProviders,
	"api-keys":      bootstrap.AdoptionRefProviderStateAPIKeys,
	"client":        bootstrap.AdoptionRefProviderStateClient,
}

// adoptionProviderSections is every whole section the adoption copies, in page
// order: the eight `api-keys.*` provider groups and the four `oauth.*` maps.
var adoptionProviderSections = []string{
	"api-keys.gemini",
	"api-keys.interactions",
	"api-keys.vertex",
	"api-keys.codex",
	"api-keys.claude",
	"api-keys.xai",
	"api-keys.meta",
	"api-keys.openai-compatibility",
	"oauth.model-alias",
	"oauth.excluded-models",
	"oauth.settings",
	"oauth.request-scoped-errors",
}

// adoptionProviderCopyRoots is the reference each copyable section root
// contributes. It is a separate statement from the root signals because copying
// `oauth.*` is not copying the `oauth` root: the root is the source's auth
// directory (`cpa-auth-files`), while its children are provider configuration
// and belong to `provider-state-api-keys`, the same decision as the per-provider
// key groups.
var adoptionProviderCopyRoots = map[string]bootstrap.ResourceRef{
	"api-keys": bootstrap.AdoptionRefProviderStateAPIKeys,
	"oauth":    bootstrap.AdoptionRefProviderStateAPIKeys,
}

// ProviderAdoptionSections returns the copyable provider sections in page order.
func ProviderAdoptionSections() []string { return slices.Clone(adoptionProviderSections) }

// adoptionProviderSectionRefs resolves each copyable section to the reference it
// is gated under: the section's own root's copy reference, never another root's.
// It is computed from the list above rather than written twice.
var adoptionProviderSectionRefs = func() map[string]bootstrap.ResourceRef {
	refs := make(map[string]bootstrap.ResourceRef, len(adoptionProviderSections))
	for _, section := range adoptionProviderSections {
		ref, ok := adoptionProviderCopyRoots[AdoptionRootOf(section)]
		if !ok {
			panic("provider section without a copy reference: " + section)
		}
		refs[section] = ref
	}
	return refs
}()

// adoptionFamilyPaths is the set of payload rule family paths. A family is a
// configuration path like any other (`requests.payload.default`), so the same
// path membership test classifies it.
var adoptionFamilyPaths = func() map[string]bool {
	paths := make(map[string]bool, len(ConfigRuleFamilyPaths()))
	for _, path := range ConfigRuleFamilyPaths() {
		paths[path] = true
	}
	return paths
}()

// AdoptionItemRef is the stable adoption reference of one copyable item: a
// gateway configuration path, a payload rule family (by path or by identifier),
// a whole provider section, or one discovered root named whole. It is the single
// classification of the product and the reference the operation record's
// decision is looked up under.
//
// A leaf or rule family the accepted gateway table names is `cpa-config-routing`
// — that table is the exact scope the gateway configuration page reads and
// writes against CPA v8.0.13. Everything else keeps `cpa-config-unknown` and
// therefore stays manual: the executor never widens a scope by classifying an
// item into a migrate verdict the record was not given.
func AdoptionItemRef(item string) bootstrap.ResourceRef {
	if ref, ok := adoptionProviderSectionRefs[item]; ok {
		return ref
	}
	if _, ok := LookupConfigLeaf(item); ok {
		return bootstrap.AdoptionRefConfigRouting
	}
	if adoptionFamilyPaths[item] {
		return bootstrap.AdoptionRefConfigRouting
	}
	if _, ok := LookupConfigRuleFamily(item); ok {
		// The executor records a family item by its identifier as well as by its
		// path, so both spellings classify.
		return bootstrap.AdoptionRefConfigRouting
	}
	// A discovery root named whole keeps its root reference. A nested path the
	// gateway table does not name is unknown configuration and stays manual.
	if !strings.Contains(item, ".") {
		if ref, ok := adoptionRootSignals[item]; ok {
			return ref
		}
	}
	return bootstrap.AdoptionRefConfigUnknown
}

// AdoptionItemUniverse returns every configuration item the adoption can
// enumerate: each gateway leaf, each payload rule family path, and each copyable
// provider section. The executor walks the first two lists and the section list;
// the invariant tests walk this, so a widened table is covered without a second
// inventory.
func AdoptionItemUniverse() []string {
	items := make([]string, 0, len(ConfigLeafPaths())+len(ConfigRuleFamilyPaths())+len(adoptionProviderSections))
	items = append(items, ConfigLeafPaths()...)
	items = append(items, ConfigRuleFamilyPaths()...)
	items = append(items, adoptionProviderSections...)
	return items
}

// AdoptionRootRefs is the set of references any item under one discovered root
// can carry, in a stable order. Discovery records one decision per reference in
// this set, which is what makes the record cover every item the executor can
// copy from that root. The set is computed from the executor's own classifier
// over the enumerable item universe, never restated.
func AdoptionRootRefs(root string) []bootstrap.ResourceRef {
	return slices.Clone(rootRefIndex()[root])
}

// rootRefIndex derives every root's set once and reuses it: the tables it reads
// are immutable at run time.
var rootRefIndex = sync.OnceValue(deriveRootRefIndex)

// deriveRootRefIndex builds each root's set: its own signal, the classifier's
// answer for every enumerable item that lands in the root, and — for every named
// root as well — cpa-config-unknown, because a path under it that the gateway
// table does not name is unknown configuration and stays manual.
func deriveRootRefIndex() map[string][]bootstrap.ResourceRef {
	index := make(map[string]map[bootstrap.ResourceRef]bool, len(adoptionRootSignals))
	add := func(root string, refs ...bootstrap.ResourceRef) {
		if index[root] == nil {
			index[root] = map[bootstrap.ResourceRef]bool{}
		}
		for _, ref := range refs {
			index[root][ref] = true
		}
	}
	for root, ref := range adoptionRootSignals {
		add(root, ref)
		// A named root can also carry a path the gateway table does not name —
		// `plugins.dir`, `server.host`, `oauth.auth-dir` — and those are unknown
		// configuration, which stays manual. Every root therefore contributes
		// that decision too, so the set is never narrower than what a path under
		// the root can classify as.
		add(root, bootstrap.AdoptionRefConfigUnknown)
	}
	for _, item := range AdoptionItemUniverse() {
		add(AdoptionRootOf(item), AdoptionItemRef(item))
	}
	ordered := make(map[string][]bootstrap.ResourceRef, len(index))
	for root, refs := range index {
		list := make([]bootstrap.ResourceRef, 0, len(refs))
		for ref := range refs {
			list = append(list, ref)
		}
		slices.Sort(list)
		ordered[root] = list
	}
	return ordered
}
