package bootstrap

import (
	"errors"
	"fmt"
	"regexp"
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
	AdoptionRefClientAPIKeys:          {kind: APIKeys, scope: ClientAPIKeys, qualified: true},
	AdoptionRefCPAAuthFiles:           {kind: Credentials, scope: CPAAuthFiles, manual: ReauthenticateAfterAdoption},
	AdoptionRefConfigServer:           {kind: CPAConfiguration, scope: ManagementAPIFields, manual: ReconfigureAfterAdoption},
	AdoptionRefConfigManagement:       {kind: CPAConfiguration, scope: ManagementAPIFields, manual: ReconfigureAfterAdoption},
	AdoptionRefConfigRouting:          {kind: CPAConfiguration, scope: ManagementAPIFields, manual: ReconfigureAfterAdoption},
	AdoptionRefConfigLogging:          {kind: CPAConfiguration, scope: ManagementAPIFields, manual: ReconfigureAfterAdoption},
	AdoptionRefConfigUsage:            {kind: CPAConfiguration, scope: ManagementAPIFields, manual: ReconfigureAfterAdoption},
	AdoptionRefConfigPlugins:          {kind: CPAConfiguration, scope: ManagementAPIFields, manual: ReconfigureAfterAdoption},
	AdoptionRefConfigObservability:    {kind: CPAConfiguration, scope: ManagementAPIFields, manual: ReconfigureAfterAdoption},
	AdoptionRefConfigUnknown:          {kind: CPAConfiguration, scope: ManagementAPIFields, manual: ReconfigureAfterAdoption},
	AdoptionRefProviderStateProviders: {kind: ProviderRuntimeState, scope: ProviderState, manual: ReauthenticateAfterAdoption},
	AdoptionRefProviderStateAPIKeys:   {kind: ProviderRuntimeState, scope: ProviderState, manual: ReauthenticateAfterAdoption},
	AdoptionRefProviderStateClient:    {kind: ProviderRuntimeState, scope: ProviderState, manual: ReauthenticateAfterAdoption},
}

// FreshAdoptionDecision returns the only decision shape permitted for a bounded
// Fresh adoption resource. Client API keys must come from accepted
// qualification; every other resource is an explicit manual action. An unknown
// reference yields no decision.
func FreshAdoptionDecision(ref ResourceRef, clientAPIKeys AdoptionResourceDecision) (AdoptionResourceDecision, bool) {
	rule, ok := freshAdoptionRefRules[ref]
	if !ok {
		return AdoptionResourceDecision{}, false
	}
	if rule.qualified {
		disposition, err := DecideAdoptionResource(clientAPIKeys)
		if err != nil || clientAPIKeys.ResourceRef != ref || clientAPIKeys.Kind != rule.kind || clientAPIKeys.Scope != rule.scope ||
			clientAPIKeys.Qualification != CapabilityQualified || clientAPIKeys.ManualAction != "" || clientAPIKeys.Disposition != disposition || disposition != Migrate {
			return AdoptionResourceDecision{}, false
		}
		return clientAPIKeys, true
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
// discovered resource, the qualified client-key migration, and the auth-file
// manual action that cannot be replayed safely.
func validFreshAdoptionInventory(discovered []ResourceRef, decisions []AdoptionResourceDecision) bool {
	if validateAdoptionInventory(discovered, decisions) != nil {
		return false
	}
	hasKeys, hasAuth := false, false
	for _, decision := range decisions {
		expected, ok := FreshAdoptionDecision(decision.ResourceRef, decision)
		if !ok || expected != decision {
			return false
		}
		switch decision.ResourceRef {
		case AdoptionRefClientAPIKeys:
			hasKeys = true
		case AdoptionRefCPAAuthFiles:
			hasAuth = true
		}
	}
	return hasKeys && hasAuth
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
