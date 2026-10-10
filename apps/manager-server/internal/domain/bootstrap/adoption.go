package bootstrap

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// AdoptionResourceKind names the supported migration taxonomy, not a unit of
// migration. Distinct resources of the same kind may need distinct decisions.
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

// ResourceRef is an opaque identifier assigned to one discovered source CPA
// resource by server-owned preflight. It must not contain a secret or URL.
type ResourceRef string

// CapabilityEvidenceRef points to a server-owned detection/preflight record.
// The domain checks reference shape only. Phase4-01 must resolve each reference
// against authoritative server evidence before a migration can be executed;
// endpoint names, user input, or arbitrary text are not capability proof.
type CapabilityEvidenceRef string

var resourceRefPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

func validResourceRef(ref ResourceRef) bool {
	return resourceRefPattern.MatchString(string(ref))
}

func validEvidenceRef(ref CapabilityEvidenceRef) bool {
	if ref == "" {
		return true
	}
	id, ok := strings.CutPrefix(string(ref), "preflight:")
	return ok && validResourceRef(ResourceRef(id))
}

// AdoptionResourceDecision applies to one discovered resource. An empty
// evidence reference means the corresponding capability is not proven yet.
type AdoptionResourceDecision struct {
	ResourceRef       ResourceRef
	Kind              AdoptionResourceKind
	Scope             AdoptionScope
	ReadEvidenceRef   CapabilityEvidenceRef
	ReplayEvidenceRef CapabilityEvidenceRef
	VerifyEvidenceRef CapabilityEvidenceRef
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

// DecideAdoptionResource requires references for read, replay/import, and
// verification before a resource can be proposed for migration. The server
// must resolve those references before execution; this function does not
// turn their mere presence into proof of capability.
func DecideAdoptionResource(item AdoptionResourceDecision) (AdoptionDisposition, error) {
	if !validResourceRef(item.ResourceRef) {
		return "", fmt.Errorf("invalid adoption resource reference %q", item.ResourceRef)
	}
	scope, ok := adoptionScope(item.Kind)
	if !ok || item.Scope != scope {
		return "", fmt.Errorf("unknown or mismatched adoption resource %q/%q", item.Kind, item.Scope)
	}
	for _, ref := range []CapabilityEvidenceRef{item.ReadEvidenceRef, item.ReplayEvidenceRef, item.VerifyEvidenceRef} {
		if !validEvidenceRef(ref) {
			return "", fmt.Errorf("invalid server preflight evidence reference %q", ref)
		}
	}
	complete := item.ReadEvidenceRef != "" && item.ReplayEvidenceRef != "" && item.VerifyEvidenceRef != ""
	if complete {
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

func adoptionEffect(disposition AdoptionDisposition, ref ResourceRef) string {
	return "adoption:" + string(disposition) + ":" + string(ref)
}

func validateAdoptionDecisions(discovered []ResourceRef, decisions []AdoptionResourceDecision, mutations, unsupported []string) error {
	if discovered == nil || decisions == nil {
		return errors.New("adoption inventory and decisions must be explicit")
	}
	if len(discovered) != len(decisions) {
		return errors.New("each discovered adoption resource needs exactly one decision")
	}
	seenDiscovered := make(map[ResourceRef]bool, len(discovered))
	for _, ref := range discovered {
		if !validResourceRef(ref) || seenDiscovered[ref] {
			return fmt.Errorf("invalid or duplicate discovered resource %q", ref)
		}
		seenDiscovered[ref] = true
	}
	seenDecisions := make(map[ResourceRef]bool, len(decisions))
	expected := make(map[string]bool, len(decisions))
	for _, item := range decisions {
		if !seenDiscovered[item.ResourceRef] || seenDecisions[item.ResourceRef] {
			return fmt.Errorf("missing or duplicate discovered resource decision %q", item.ResourceRef)
		}
		seenDecisions[item.ResourceRef] = true
		decision, err := DecideAdoptionResource(item)
		if err != nil || item.Disposition != decision {
			return fmt.Errorf("adoption resource %q disposition conflicts with evidence references: %v", item.ResourceRef, err)
		}
		effect := adoptionEffect(decision, item.ResourceRef)
		expected[effect] = true
		if decision == Migrate {
			if !has(mutations, effect) || has(unsupported, effect) {
				return fmt.Errorf("migratable resource %q missing from mutations", item.ResourceRef)
			}
		} else if !has(unsupported, effect) || has(mutations, effect) {
			return fmt.Errorf("resource %q missing from unsupported or manual actions", item.ResourceRef)
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
