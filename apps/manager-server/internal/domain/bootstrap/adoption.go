package bootstrap

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

type AdoptionResourceKind string

const (
	APIKeys              AdoptionResourceKind = "api_keys"
	Credentials          AdoptionResourceKind = "credentials"
	CPAConfiguration     AdoptionResourceKind = "cpa_configuration"
	ProviderRuntimeState AdoptionResourceKind = "provider_runtime_state"
)

type AdoptionScope string

const (
	ClientAPIKeys       AdoptionScope = "client_api_keys"
	CPAAuthFiles        AdoptionScope = "cpa_auth_files"
	ManagementAPIFields AdoptionScope = "management_api_fields"
	ProviderState       AdoptionScope = "provider_runtime_state"
)

type AdoptionDisposition string

const (
	Migrate      AdoptionDisposition = "migrate"
	ManualAction AdoptionDisposition = "manual_action"
	Unsupported  AdoptionDisposition = "unsupported"
)

type AdoptionManualAction string

const (
	ReauthenticateAfterAdoption AdoptionManualAction = "reauthenticate_after_adoption"
	ReconfigureAfterAdoption    AdoptionManualAction = "reconfigure_after_adoption"
)

type ResourceRef string
type CapabilityEvidenceRef string

type CapabilityQualification string

const (
	CapabilityPending   CapabilityQualification = "pending"
	CapabilityQualified CapabilityQualification = "qualified"
	CapabilityRejected  CapabilityQualification = "rejected"
)

var resourceRefPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func validResourceRef(ref ResourceRef) bool { return resourceRefPattern.MatchString(string(ref)) }
func validCapabilityEvidenceRef(ref CapabilityEvidenceRef) bool {
	if ref == "" {
		return true
	}
	id, ok := strings.CutPrefix(string(ref), "preflight:")
	return ok && validResourceRef(ResourceRef(id))
}
func validCapabilityQualification(v CapabilityQualification) bool {
	switch v {
	case CapabilityPending, CapabilityQualified, CapabilityRejected:
		return true
	default:
		return false
	}
}

type AdoptionResourceDecision struct {
	ResourceRef       ResourceRef
	Kind              AdoptionResourceKind
	Scope             AdoptionScope
	ReadEvidenceRef   CapabilityEvidenceRef
	ReplayEvidenceRef CapabilityEvidenceRef
	VerifyEvidenceRef CapabilityEvidenceRef
	Qualification     CapabilityQualification
	ManualAction      AdoptionManualAction
	Disposition       AdoptionDisposition
}

func adoptionScope(kind AdoptionResourceKind) (AdoptionScope, bool) {
	switch kind {
	case APIKeys:
		return ClientAPIKeys, true
	case Credentials:
		return CPAAuthFiles, true
	case CPAConfiguration:
		return ManagementAPIFields, true
	case ProviderRuntimeState:
		return ProviderState, true
	default:
		return "", false
	}
}

func DecideAdoptionResource(item AdoptionResourceDecision) (AdoptionDisposition, error) {
	if !validResourceRef(item.ResourceRef) {
		return "", fmt.Errorf("invalid adoption resource reference %q", item.ResourceRef)
	}
	scope, ok := adoptionScope(item.Kind)
	if !ok || item.Scope != scope {
		return "", fmt.Errorf("unknown or mismatched adoption resource %q/%q", item.Kind, item.Scope)
	}
	for _, ref := range []CapabilityEvidenceRef{item.ReadEvidenceRef, item.ReplayEvidenceRef, item.VerifyEvidenceRef} {
		if !validCapabilityEvidenceRef(ref) {
			return "", fmt.Errorf("invalid server preflight evidence reference %q", ref)
		}
	}
	if !validCapabilityQualification(item.Qualification) {
		return "", fmt.Errorf("unknown capability qualification %q", item.Qualification)
	}
	completeRefs := item.ReadEvidenceRef != "" && item.ReplayEvidenceRef != "" && item.VerifyEvidenceRef != ""
	if item.Qualification == CapabilityQualified && !completeRefs {
		return "", errors.New("qualified capability requires read/replay/verify evidence references")
	}
	if completeRefs && item.Qualification == CapabilityQualified {
		if item.ManualAction != "" {
			return "", errors.New("migratable resource cannot also require manual action")
		}
		return Migrate, nil
	}
	switch item.ManualAction {
	case "":
		return Unsupported, nil
	case ReauthenticateAfterAdoption, ReconfigureAfterAdoption:
		return ManualAction, nil
	default:
		return "", fmt.Errorf("unknown manual action %q", item.ManualAction)
	}
}

// Bounded Fresh adoption resources. Only client API keys may migrate; every
// other discovered resource is an explicit manual action. These stable,
// non-secret references are the only identities Fresh may persist, so an
// arbitrary upstream key can never become durable state.
const (
	AdoptionRefClientAPIKeys          ResourceRef = "client-api-keys"
	AdoptionRefCPAAuthFiles           ResourceRef = "cpa-auth-files"
	AdoptionRefConfigServer           ResourceRef = "cpa-config-server"
	AdoptionRefConfigManagement       ResourceRef = "cpa-config-management"
	AdoptionRefConfigRouting          ResourceRef = "cpa-config-routing"
	AdoptionRefConfigLogging          ResourceRef = "cpa-config-logging"
	AdoptionRefConfigUsage            ResourceRef = "cpa-config-usage"
	AdoptionRefConfigPlugins          ResourceRef = "cpa-config-plugins"
	AdoptionRefConfigObservability    ResourceRef = "cpa-config-observability"
	AdoptionRefConfigUnknown          ResourceRef = "cpa-config-unknown"
	AdoptionRefProviderStateProviders ResourceRef = "provider-state-providers"
	AdoptionRefProviderStateAPIKeys   ResourceRef = "provider-state-api-keys"
	AdoptionRefProviderStateClient    ResourceRef = "provider-state-client"
)

type freshAdoptionRefRule struct {
	kind      AdoptionResourceKind
	scope     AdoptionScope
	manual    AdoptionManualAction
	qualified bool
}

var freshAdoptionRefRules = map[ResourceRef]freshAdoptionRefRule{
	AdoptionRefClientAPIKeys: {kind: APIKeys, scope: ClientAPIKeys, qualified: true},
	// Credential files, the gateway configuration and the provider sections are
	// copied by the adoption executor, so their decision says migrate. The
	// evidence for them is the same class-specific read/replay/verify chain the
	// client keys use, extended to name the resource kinds it actually exercised
	// against real CPA v8.
	//
	// Each of them also keeps its manual action as the fallback: a source class
	// whose evidence does not authorize the kind keeps exactly the manual action
	// it had before. A legacy_managed source is a pre-v8 process with no v8
	// configuration or credential surface, so nothing can be copied from it and
	// its consumers stay manual, without a second rule table.
	AdoptionRefCPAAuthFiles:         {kind: Credentials, scope: CPAAuthFiles, manual: ReauthenticateAfterAdoption, qualified: true},
	AdoptionRefConfigRouting:        {kind: CPAConfiguration, scope: ManagementAPIFields, manual: ReconfigureAfterAdoption, qualified: true},
	AdoptionRefProviderStateAPIKeys: {kind: ProviderRuntimeState, scope: ProviderState, manual: ReauthenticateAfterAdoption, qualified: true},
	// Not migrated: the Manager owns the destination's own identity and client
	// keys, the destination's plugin set is a deployment decision, and usage,
	// logs and observability history are the source's own history, which the
	// adoption explicitly excludes.
	AdoptionRefConfigServer:           {kind: CPAConfiguration, scope: ManagementAPIFields, manual: ReconfigureAfterAdoption},
	AdoptionRefConfigManagement:       {kind: CPAConfiguration, scope: ManagementAPIFields, manual: ReconfigureAfterAdoption},
	AdoptionRefConfigLogging:          {kind: CPAConfiguration, scope: ManagementAPIFields, manual: ReconfigureAfterAdoption},
	AdoptionRefConfigUsage:            {kind: CPAConfiguration, scope: ManagementAPIFields, manual: ReconfigureAfterAdoption},
	AdoptionRefConfigPlugins:          {kind: CPAConfiguration, scope: ManagementAPIFields, manual: ReconfigureAfterAdoption},
	AdoptionRefConfigObservability:    {kind: CPAConfiguration, scope: ManagementAPIFields, manual: ReconfigureAfterAdoption},
	AdoptionRefConfigUnknown:          {kind: CPAConfiguration, scope: ManagementAPIFields, manual: ReconfigureAfterAdoption},
	AdoptionRefProviderStateProviders: {kind: ProviderRuntimeState, scope: ProviderState, manual: ReauthenticateAfterAdoption},
	AdoptionRefProviderStateClient:    {kind: ProviderRuntimeState, scope: ProviderState, manual: ReauthenticateAfterAdoption},
}

// AdoptionResourceRule is the exported shape of one rule, so a consumer can name
// the kind and scope a reference decides without restating them.
type AdoptionResourceRule struct {
	Kind  AdoptionResourceKind
	Scope AdoptionScope
}

// FreshAdoptionRule returns the rule for one reference.
func FreshAdoptionRule(ref ResourceRef) (AdoptionResourceRule, bool) {
	rule, ok := freshAdoptionRefRules[ref]
	if !ok {
		return AdoptionResourceRule{}, false
	}
	return AdoptionResourceRule{Kind: rule.kind, Scope: rule.scope}, true
}

// MigratedAdoptionResourceRefs is the closed set of resources an adoption
// decision may mark migrate. It is the single list the evidence chain must
// cover and the executor must copy: a kind cannot be marked migrate without
// evidence for it, and the executor copies nothing outside it.
func MigratedAdoptionResourceRefs() []ResourceRef {
	refs := make([]ResourceRef, 0, len(freshAdoptionRefRules))
	for ref, rule := range freshAdoptionRefRules {
		if rule.qualified {
			refs = append(refs, ref)
		}
	}
	slices.Sort(refs)
	return refs
}

// FreshAdoptionDecision returns the only decision shape permitted for a bounded
// Fresh adoption resource. Client API keys must come from accepted
// qualification; every other resource is an explicit manual action. An unknown
// reference yields no decision.
func FreshAdoptionDecision(ref ResourceRef, candidate AdoptionResourceDecision) (AdoptionResourceDecision, bool) {
	rule, ok := freshAdoptionRefRules[ref]
	if !ok {
		return AdoptionResourceDecision{}, false
	}
	// The caller passes the decision it derived for this ref from the evidence
	// chain, or no decision at all when the chain did not exercise this kind.
	// "Not authorized" is therefore the zero decision and the explicit pending
	// one; anything else is a claim this function then has to accept or refuse.
	if rule.qualified && candidate != (AdoptionResourceDecision{}) && candidate.Qualification != CapabilityPending {
		// A migrate verdict is only ever this ref's own shape, fully qualified,
		// and derived from evidence: never inferred from another kind.
		disposition, err := DecideAdoptionResource(candidate)
		if err != nil || candidate.ResourceRef != ref || candidate.Kind != rule.kind || candidate.Scope != rule.scope ||
			candidate.Qualification != CapabilityQualified || candidate.ManualAction != "" || candidate.Disposition != disposition || disposition != Migrate {
			return AdoptionResourceDecision{}, false
		}
		return candidate, true
	}
	// The kind was not authorized by the evidence chain. A resource with a
	// declared manual action falls back to exactly that action; a kind with none
	// is refused, so nothing unqualified can be claimed migratable.
	if rule.manual == "" {
		return AdoptionResourceDecision{}, false
	}
	decision := AdoptionResourceDecision{
		ResourceRef: ref, Kind: rule.kind, Scope: rule.scope,
		Qualification: CapabilityPending, ManualAction: rule.manual, Disposition: ManualAction,
	}
	if disposition, err := DecideAdoptionResource(decision); err != nil || disposition != ManualAction {
		return AdoptionResourceDecision{}, false
	}
	return decision, true
}

// validFreshAdoptionInventory requires exactly one permitted decision per
// discovered resource and the qualified client-key migration. The other
// migrated kinds are optional: a source that carries no credential file has
// nothing to authorize, and requiring them would refuse such a source instead of
// adopting it with an empty set.
func validFreshAdoptionInventory(discovered []ResourceRef, decisions []AdoptionResourceDecision) bool {
	if validateAdoptionInventory(discovered, decisions) != nil {
		return false
	}
	hasKeys := false
	for _, decision := range decisions {
		expected, ok := FreshAdoptionDecision(decision.ResourceRef, decision)
		if !ok || expected != decision {
			return false
		}
		if decision.ResourceRef == AdoptionRefClientAPIKeys {
			hasKeys = true
		}
	}
	return hasKeys
}

func adoptionEffect(d AdoptionDisposition, ref ResourceRef) string {
	return "adoption:" + string(d) + ":" + string(ref)
}

// validateAdoptionInventory is the minimal one-to-one discovered-resource
// contract shared by the Bootstrap plan and the Fresh durable checkpoint.
func validateAdoptionInventory(discovered []ResourceRef, decisions []AdoptionResourceDecision) error {
	if discovered == nil || decisions == nil {
		return errors.New("adoption inventory and decisions must be explicit")
	}
	if len(discovered) != len(decisions) {
		return errors.New("each discovered adoption resource needs exactly one decision")
	}
	seenDiscovered := map[ResourceRef]bool{}
	for _, ref := range discovered {
		if !validResourceRef(ref) || seenDiscovered[ref] {
			return fmt.Errorf("invalid or duplicate discovered resource %q", ref)
		}
		seenDiscovered[ref] = true
	}
	seenDecisions := map[ResourceRef]bool{}
	for _, item := range decisions {
		if !seenDiscovered[item.ResourceRef] || seenDecisions[item.ResourceRef] {
			return fmt.Errorf("missing or duplicate discovered resource decision %q", item.ResourceRef)
		}
		seenDecisions[item.ResourceRef] = true
		decision, err := DecideAdoptionResource(item)
		if err != nil || item.Disposition != decision {
			return fmt.Errorf("adoption resource %q disposition conflicts with qualification: %v", item.ResourceRef, err)
		}
	}
	return nil
}

func validateAdoptionDecisions(discovered []ResourceRef, decisions []AdoptionResourceDecision, mutations, unsupported []string) error {
	if err := validateAdoptionInventory(discovered, decisions); err != nil {
		return err
	}
	expected := map[string]bool{}
	for _, item := range decisions {
		decision, _ := DecideAdoptionResource(item)
		effect := adoptionEffect(decision, item.ResourceRef)
		expected[effect] = true
		if decision == Migrate {
			if !has(mutations, effect) || has(unsupported, effect) {
				return fmt.Errorf("migratable resource %q missing from mutations", item.ResourceRef)
			}
		} else if !has(unsupported, effect) || has(mutations, effect) {
			return fmt.Errorf("resource %q missing from unsupported/manual actions", item.ResourceRef)
		}
	}
	for _, group := range [][]string{mutations, unsupported} {
		for _, effect := range group {
			if strings.HasPrefix(effect, "adoption:") && !expected[effect] {
				return fmt.Errorf("adoption effect %q lacks a resource decision", effect)
			}
		}
	}
	return nil
}
