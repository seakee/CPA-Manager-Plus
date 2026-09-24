package bootstrap

import (
	"errors"
	"fmt"
	"strings"
)

// AdoptionResourceKind is the first supported CPA migration taxonomy. Each
// discovered item within these categories still needs its own capability
// evidence; a category does not authorize copying all of its fields.
type AdoptionResourceKind string

const (
	APIKeys              AdoptionResourceKind = "api_keys"
	Credentials          AdoptionResourceKind = "credentials"
	CPAConfiguration     AdoptionResourceKind = "cpa_configuration"
	ProviderRuntimeState AdoptionResourceKind = "provider_runtime_state"
)

type AdoptionScope string

const (
	ClientAPIKeys        AdoptionScope = "client_api_keys"
	PortableAuthFiles    AdoptionScope = "portable_auth_files"
	ManagementAPIFields  AdoptionScope = "management_api_allowlisted_fields"
	DurableProviderState AdoptionScope = "capability_proven_durable_state"
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

// AdoptionResource is a resource-level plan item, never an implicit whole-CPA
// migration. Phase4-01 must supply evidence from the actual source and staged
// destination; endpoint names alone are not proof of instance capability.
type AdoptionResource struct {
	Kind           AdoptionResourceKind
	Scope          AdoptionScope
	ReadEvidence   string
	ReplayEvidence string
	VerifyEvidence string
	ManualAction   AdoptionManualAction
	Disposition    AdoptionDisposition
}

func adoptionScope(kind AdoptionResourceKind) (AdoptionScope, bool) {
	switch kind {
	case APIKeys:
		return ClientAPIKeys, true
	case Credentials:
		return PortableAuthFiles, true
	case CPAConfiguration:
		return ManagementAPIFields, true
	case ProviderRuntimeState:
		return DurableProviderState, true
	default:
		return "", false
	}
}

func validEvidence(value string) bool {
	return value != "" && value == strings.TrimSpace(value)
}

// DecideAdoptionResource requires read, replay/import, and verification
// evidence before an item may enter mutations. Missing proof is explicit
// unsupported or a named manual action; it is never silently migrated.
func DecideAdoptionResource(item AdoptionResource) (AdoptionDisposition, error) {
	scope, ok := adoptionScope(item.Kind)
	if !ok || item.Scope != scope {
		return "", fmt.Errorf("unknown or mismatched adoption resource %q/%q", item.Kind, item.Scope)
	}
	complete := validEvidence(item.ReadEvidence) && validEvidence(item.ReplayEvidence) && validEvidence(item.VerifyEvidence)
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

func adoptionEffect(disposition AdoptionDisposition, kind AdoptionResourceKind) string {
	return string(disposition) + "_" + string(kind)
}

func validateAdoptionResources(items []AdoptionResource, mutations, unsupported []string) error {
	// A missing category must be recorded as unsupported or manual action,
	// rather than silently omitted from the adoption plan.
	if len(items) != 4 {
		return errors.New("adoption requires a decision for each of the four resource categories")
	}
	seen := map[AdoptionResourceKind]bool{}
	expected := map[string]bool{}
	for _, item := range items {
		if seen[item.Kind] {
			return fmt.Errorf("duplicate adoption resource %q", item.Kind)
		}
		seen[item.Kind] = true
		decision, err := DecideAdoptionResource(item)
		if err != nil || item.Disposition != decision {
			return fmt.Errorf("adoption resource %q disposition disagrees with capability evidence: %v", item.Kind, err)
		}
		effect := adoptionEffect(decision, item.Kind)
		expected[effect] = true
		if decision == Migrate {
			if !has(mutations, effect) || has(unsupported, effect) {
				return fmt.Errorf("migratable resource %q missing from mutations", item.Kind)
			}
		} else if !has(unsupported, effect) || has(mutations, effect) {
			return fmt.Errorf("non-migratable resource %q missing from unsupported/manual", item.Kind)
		}
	}
	for _, group := range [][]string{mutations, unsupported} {
		for _, effect := range group {
			if strings.HasPrefix(effect, "migrate_") || strings.HasPrefix(effect, "manual_action_") || strings.HasPrefix(effect, "unsupported_") {
				if !expected[effect] {
					return fmt.Errorf("adoption effect %q lacks a resource decision", effect)
				}
			}
		}
	}
	return nil
}
