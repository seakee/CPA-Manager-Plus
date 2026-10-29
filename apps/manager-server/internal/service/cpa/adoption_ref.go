package cpa

import (
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

// adoptionSectionRefs maps one bounded safe-read section of an adoption source —
// a top-level root of the source document — to the stable resource reference a
// record decision is keyed by. Arbitrary upstream keys never become refs: only
// the names below do.
//
// This table is the discovery inventory: `freshstrategy.adoptionResourceRefs`
// resolves a source root through it and refuses anything it does not name.
var adoptionSectionRefs = map[string]bootstrap.ResourceRef{
	"access":                      bootstrap.AdoptionRefClientAPIKeys,
	"oauth":                       bootstrap.AdoptionRefCPAAuthFiles,
	"server":                      bootstrap.AdoptionRefConfigServer,
	"management":                  bootstrap.AdoptionRefConfigManagement,
	"routing":                     bootstrap.AdoptionRefConfigRouting,
	"logging":                     bootstrap.AdoptionRefConfigLogging,
	"usage":                       bootstrap.AdoptionRefConfigUsage,
	"plugins":                     bootstrap.AdoptionRefConfigPlugins,
	"observability":               bootstrap.AdoptionRefConfigObservability,
	"unknown_cpa_native_resource": bootstrap.AdoptionRefConfigUnknown,
	"providers":                   bootstrap.AdoptionRefProviderStateProviders,
	"api-keys":                    bootstrap.AdoptionRefProviderStateAPIKeys,
	"client":                      bootstrap.AdoptionRefProviderStateClient,
}

// providerCopyRoots maps a root whose children the adoption executor copies as
// whole sections to the reference that authorizes each copy.
//
// It differs from the discovery table in exactly one entry, and deliberately.
// The `oauth` root is where a v8 source keeps its auth directory, so the
// discovery read treats its presence as the source's credential surface
// (`cpa-auth-files`). Its copyable children (`model-alias`, `excluded-models`,
// `settings`, `request-scoped-errors`) are provider configuration, not
// credentials, and they are the rows the provider-state evidence chain counted:
// they belong to the same `provider-state-api-keys` decision as the
// per-provider API key groups. No other root may share a reference with another:
// `providers` and `client` stay separate, so one root's decision can never be
// borrowed by another root's sections.
var providerCopyRoots = map[string]bootstrap.ResourceRef{
	"api-keys":  bootstrap.AdoptionRefProviderStateAPIKeys,
	"oauth":     bootstrap.AdoptionRefProviderStateAPIKeys,
	"providers": bootstrap.AdoptionRefProviderStateProviders,
	"client":    bootstrap.AdoptionRefProviderStateClient,
}

// AdoptionSectionRef resolves one bounded source section (a top-level root of the
// source document) to its reference. A name the table does not carry has no
// reference, and the caller must treat it as unauthorized.
func AdoptionSectionRef(section string) (bootstrap.ResourceRef, bool) {
	ref, ok := adoptionSectionRefs[section]
	return ref, ok
}

// AdoptionConfigRef resolves one gateway configuration item of the accepted
// gateway table to the reference its decision is looked up under: a writable
// leaf (`LookupConfigLeaf`) or a whole payload rule family
// (`LookupConfigRuleFamily`) is routing configuration, because that table is the
// exact scope the gateway configuration page reads and writes against CPA
// v8.0.13. Anything outside the table keeps cpa-config-unknown, so a leaf the
// page cannot write stays manual and can never borrow another item's decision.
func AdoptionConfigRef(name string) bootstrap.ResourceRef {
	if _, ok := LookupConfigLeaf(name); ok {
		return bootstrap.AdoptionRefConfigRouting
	}
	if _, ok := LookupConfigRuleFamily(name); ok {
		return bootstrap.AdoptionRefConfigRouting
	}
	return bootstrap.AdoptionRefConfigUnknown
}

// AdoptionProviderSectionRef resolves one whole provider section (a path such as
// `api-keys.claude` or `oauth.model-alias`) through its root, so a decision for
// the root covers exactly that root's sections. A root named whole also
// resolves, through the discovery table, so a section item can never be gated
// under a reference the record did not discover.
func AdoptionProviderSectionRef(section string) (bootstrap.ResourceRef, bool) {
	root, _, _ := strings.Cut(section, ".")
	if root == section {
		return AdoptionSectionRef(section)
	}
	ref, ok := providerCopyRoots[root]
	return ref, ok
}
