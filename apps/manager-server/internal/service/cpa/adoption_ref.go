package cpa

import (
	"crypto/sha256"
	"encoding/hex"
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
//   - AdoptionItemRef(item) is the reference of one copyable item. The executor
//     gates every write on it.
//   - AdoptionRootRefs(root) is the set of references items under one discovered
//     root carry. Discovery records a decision for every reference in that set,
//     so the record always covers everything the executor can copy out of that
//     root.
//
// A second hand-written table is what let the two drift apart: a root recorded as
// manual while its items were copied. Here a root's set is computed from the
// item universe and nothing else. A reference only appears in a root's set
// because some item under that root classifies as it, or — for a root with no
// enumerable item at all — because it is that root's own declared fallback.

// unknownSection is where the v8 reader puts every source key it does not name.
// It is a discovery identity, never an upstream key. `requests`, `multimedia`
// and `upstream` are not named sections, so the gateway configuration under them
// arrives under this root.
const unknownSection = "unknown_cpa_native_resource"

// The two item classes the executor records that have no enumerable path. One
// representative of each is in the universe, so the references they produce are
// grounded in a real item class rather than in a root table.
const (
	// CredentialItemClass is one discovered credential file. The executor records
	// `credential:<name>` (or the digest form for an overlong name); discovery
	// finds those files in the source's auth directory, which CPA spells
	// `oauth.auth-dir`, so the class roots at `oauth`.
	CredentialItemClass = "credential:<name>"
	// OffTableConfigItemClass is one source configuration key the reader did not
	// name. Such a key is outside the accepted gateway table by construction, so
	// it classifies as unknown configuration. The keys themselves are not
	// enumerable, which is why the record carries one `cpa-config-unknown`
	// decision for the root and the Adopt screen must list the off-table paths.
	OffTableConfigItemClass = unknownSection + ".<key>"
)

// credentialItemPrefixes are the item identity prefixes the executor uses for a
// credential file.
var credentialItemPrefixes = []string{"credential:", "credential-name-sha256:"}

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
	for _, prefix := range credentialItemPrefixes {
		if strings.HasPrefix(item, prefix) {
			// Credentials are read from the source's auth directory.
			return "oauth"
		}
	}
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

// adoptionRootFallbacks is the single reference a root contributes when it has
// no enumerable item at all. Such a root is still something the operator may have
// to redo by hand — an adopted source's `server` block, its plugin set, its
// provider catalogue — so it needs one decision of its own.
//
// A root that does have items gets no fallback: its set is exactly what its items
// classify as. That is what keeps a root from claiming manual work nothing stands
// behind — `client` is two gateway leaves, so it contributes routing
// configuration and nothing else.
var adoptionRootFallbacks = map[string]bootstrap.ResourceRef{
	"access":        bootstrap.AdoptionRefClientAPIKeys,
	"server":        bootstrap.AdoptionRefConfigServer,
	"management":    bootstrap.AdoptionRefConfigManagement,
	"logging":       bootstrap.AdoptionRefConfigLogging,
	"usage":         bootstrap.AdoptionRefConfigUsage,
	"plugins":       bootstrap.AdoptionRefConfigPlugins,
	"observability": bootstrap.AdoptionRefConfigObservability,
	"providers":     bootstrap.AdoptionRefProviderStateProviders,
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
// contributes: api-keys and oauth sections are provider configuration and belong
// to `provider-state-api-keys`, the same decision as the per-provider key groups.
var adoptionProviderCopyRoots = map[string]bootstrap.ResourceRef{
	"api-keys": bootstrap.AdoptionRefProviderStateAPIKeys,
	"oauth":    bootstrap.AdoptionRefProviderStateAPIKeys,
}

// ProviderAdoptionSections returns the copyable provider sections in page order.
func ProviderAdoptionSections() []string { return slices.Clone(adoptionProviderSections) }

// adoptionProviderSectionRefs resolves each copyable section to the reference it
// is gated under: its own root's copy reference, never another root's. It is
// computed from the list above rather than written twice.
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
// gateway configuration path, a payload rule family (by path or by identifier), a
// whole provider section, a credential file, or a configuration key the reader
// did not name. It is the single classification of the product and the reference
// the operation record's decision is looked up under.
//
// A leaf or rule family the accepted gateway table names is `cpa-config-routing`
// — that table is the exact scope the gateway configuration page reads and
// writes against CPA v8.0.13. Everything else keeps `cpa-config-unknown` and
// therefore stays manual: the executor never widens a scope by classifying an
// item into a migrate verdict the record was not given.
func AdoptionItemRef(item string) bootstrap.ResourceRef {
	for _, prefix := range credentialItemPrefixes {
		if strings.HasPrefix(item, prefix) {
			return bootstrap.AdoptionRefCPAAuthFiles
		}
	}
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
	return bootstrap.AdoptionRefConfigUnknown
}

// AdoptionItemUniverse returns every item the adoption can enumerate: each
// gateway leaf, each payload rule family path, each copyable provider section,
// and one representative of each item class that has no enumerable path
// (credentials and keys the reader did not name).
//
// The executor walks the same three tables and records items of the same two
// classes; the invariant tests and the pin walk this, so a widened table is
// covered without a second inventory.
func AdoptionItemUniverse() []string {
	items := make([]string, 0, len(ConfigLeafPaths())+len(ConfigRuleFamilyPaths())+len(adoptionProviderSections)+2)
	items = append(items, ConfigLeafPaths()...)
	items = append(items, ConfigRuleFamilyPaths()...)
	items = append(items, adoptionProviderSections...)
	items = append(items, CredentialItemClass, OffTableConfigItemClass)
	return items
}

// AdoptionRootRefs is the set of references items under one discovered root
// carry, in a stable order. Discovery records one decision per reference in this
// set, which is what makes the record cover every item the executor can copy from
// that root.
//
// The set is derived: the classifier's answer for every universe item that lands
// in the root. A root with no universe item contributes only its declared
// fallback, so no root claims manual work that nothing stands behind.
func AdoptionRootRefs(root string) []bootstrap.ResourceRef {
	return slices.Clone(rootRefIndex()[root])
}

// rootRefIndex derives every root's set once and reuses it: the tables it reads
// are immutable at run time.
var rootRefIndex = sync.OnceValue(deriveRootRefIndex)

func deriveRootRefIndex() map[string][]bootstrap.ResourceRef {
	index := make(map[string]map[bootstrap.ResourceRef]bool, len(adoptionSectionNames))
	add := func(root string, refs ...bootstrap.ResourceRef) {
		if index[root] == nil {
			index[root] = map[bootstrap.ResourceRef]bool{}
		}
		for _, ref := range refs {
			index[root][ref] = true
		}
	}
	for _, item := range AdoptionItemUniverse() {
		add(AdoptionRootOf(item), AdoptionItemRef(item))
	}
	for root, fallback := range adoptionRootFallbacks {
		if len(index[root]) == 0 {
			add(root, fallback)
		}
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

// AdoptionUniverseDigest is the digest of the adoption item universe and the
// reference each item classifies as: the sorted (item, reference) pairs.
//
// It is folded into the safe-read implementation digest, because
// `AdoptionItemRef` decides its routing verdict from the gateway page table
// (`config_schema.go`), which is not itself pinned. Adding a gateway leaf or
// changing a classification therefore changes the consumer digest and
// invalidates an old acceptance, without coupling every gateway-page edit to the
// adoption pin through a whole-file hash.
func AdoptionUniverseDigest(items ...string) string {
	if len(items) == 0 {
		items = AdoptionItemUniverse()
	}
	pairs := make([]string, 0, len(items))
	for _, item := range items {
		pairs = append(pairs, item+"\x00"+string(AdoptionItemRef(item)))
	}
	slices.Sort(pairs)
	hash := sha256.New()
	for _, pair := range pairs {
		_, _ = hash.Write([]byte(pair))
		_, _ = hash.Write([]byte("\n"))
	}
	return hex.EncodeToString(hash.Sum(nil))
}
