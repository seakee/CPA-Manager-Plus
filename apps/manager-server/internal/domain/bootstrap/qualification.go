package bootstrap

import "errors"

type AdoptSourceClass string

const (
	AdoptSourceNone          AdoptSourceClass = ""
	AdoptNetworkExisting     AdoptSourceClass = "network_existing"
	AdoptLegacyManaged       AdoptSourceClass = "legacy_managed"
)

func validAdoptSourceClass(value AdoptSourceClass) bool {
	switch value {
	case AdoptSourceNone, AdoptNetworkExisting, AdoptLegacyManaged:
		return true
	default:
		return false
	}
}

// CPAQualification freezes the evidence gates a concrete Bootstrap plan must
// satisfy before it may name a CPA strategy. It does not perform qualification.
type CPAQualification struct {
	ManagementGeneration         string
	ArtifactQualified            bool
	RequiredProfileQualified     bool
	StagingV8TransitionQualified bool
}

func (q CPAQualification) ValidateForStrategy(strategy CPAStrategy, adoptClass AdoptSourceClass) error {
	if q.ManagementGeneration != CPAManagementGenerationV8 {
		return errors.New("CPA strategy requires v8 management generation")
	}
	if !validAdoptSourceClass(adoptClass) {
		return errors.New("unknown adopt source class")
	}
	if strategy != AdoptExisting && adoptClass != AdoptSourceNone {
		return errors.New("adopt source class requires adopt_existing strategy")
	}
	switch strategy {
	case NewManaged:
		if !q.ArtifactQualified {
			return errors.New("new_managed requires a qualified exact v8 artifact")
		}
	case AdoptExisting:
		switch adoptClass {
		case AdoptNetworkExisting:
			if !q.RequiredProfileQualified {
				return errors.New("network_existing adopt source requires the required v8 management profile")
			}
		case AdoptLegacyManaged:
			if !q.RequiredProfileQualified {
				return errors.New("legacy_managed adopt source requires the required v8 management profile")
			}
			if !q.StagingV8TransitionQualified {
				return errors.New("legacy_managed adopt source requires a qualified staging v8 transition")
			}
		default:
			return errors.New("adopt_existing requires an explicit source class")
		}
	case ConnectExternal:
		if !q.RequiredProfileQualified {
			return errors.New("connect_external requires the required v8 management profile")
		}
	case MigrateLegacy:
		if !q.StagingV8TransitionQualified {
			return errors.New("migrate_legacy requires a qualified staging v8 transition")
		}
	default:
		return errors.New("unknown CPA strategy")
	}
	return nil
}
