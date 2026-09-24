// Package bootstrap defines Phase 4 product decisions without detecting or
// executing them. The 1.x persisted BootstrapState is a separate contract.
package bootstrap

import (
	"errors"
	"fmt"
	"strings"
)

type Mode string

const (
	Fresh              Mode = "fresh"
	Upgrade            Mode = "upgrade"
	UnsupportedUpgrade Mode = "unsupported_upgrade"
	Recovery           Mode = "recovery"
	Completed          Mode = "completed"
)

type CPAStrategy string

const (
	NewManaged      CPAStrategy = "new_managed"
	AdoptExisting   CPAStrategy = "adopt_existing"
	ConnectExternal CPAStrategy = "connect_external"
	MigrateLegacy   CPAStrategy = "migrate_legacy"
)

type CPAMPAction string

const (
	Initialize CPAMPAction = "initialize"
	MigrateV1  CPAMPAction = "migrate_v1"
)

type UsageAction string

const (
	NoUsageAction UsageAction = "none"
	PreserveUsage UsageAction = "preserve"
)

type RuntimeMode string

const (
	Embedded RuntimeMode = "embedded"
	External RuntimeMode = "external"
)

type RecoveryReason string

const (
	OperationIncomplete    RecoveryReason = "operation_incomplete"
	RollbackRequired       RecoveryReason = "rollback_required"
	DamagedState           RecoveryReason = "damaged_state"
	ManualRecoveryRequired RecoveryReason = "manual_recovery_required"
)

// DirectUpgradeSource is an exact release-transition entry. The supported
// entries are supplied by a release; this domain does not choose release tags.
type DirectUpgradeSource struct {
	VersionTag     string
	SchemaIdentity string
	SchemaVersion  int
}

func (s DirectUpgradeSource) valid() bool {
	return strings.TrimSpace(s.VersionTag) != "" &&
		strings.TrimSpace(s.SchemaIdentity) != "" && s.SchemaVersion > 0 &&
		s.VersionTag == strings.TrimSpace(s.VersionTag) &&
		s.SchemaIdentity == strings.TrimSpace(s.SchemaIdentity)
}

// ClassificationEvidence is server-owned evidence supplied by later detection.
// RecoveryReason represents an operation/checkpoint finding, not a user choice.
type ClassificationEvidence struct {
	HasV1ProductState bool
	Source            *DirectUpgradeSource
	RecoveryReason    RecoveryReason
	BootstrapComplete bool
}

func Classify(e ClassificationEvidence, allowlist []DirectUpgradeSource) (Mode, error) {
	if e.RecoveryReason != "" {
		if !validRecoveryReason(e.RecoveryReason) {
			return "", fmt.Errorf("unknown recovery reason %q", e.RecoveryReason)
		}
		return Recovery, nil
	}
	if e.BootstrapComplete {
		return Completed, nil
	}
	if !e.HasV1ProductState {
		if e.Source != nil {
			return "", errors.New("source supplied without v1 product state")
		}
		return Fresh, nil
	}
	// Release metadata only gates direct upgrade. Recovery, completed, and
	// fresh classification must remain available when that metadata is broken.
	seen := make(map[DirectUpgradeSource]bool, len(allowlist))
	for _, source := range allowlist {
		if !source.valid() || seen[source] {
			return "", errors.New("direct-upgrade allowlist has invalid or duplicate source")
		}
		seen[source] = true
	}
	if e.Source != nil && seen[*e.Source] {
		return Upgrade, nil
	}
	return UnsupportedUpgrade, nil
}

func validRecoveryReason(reason RecoveryReason) bool {
	switch reason {
	case OperationIncomplete, RollbackRequired, DamagedState, ManualRecoveryRequired:
		return true
	default:
		return false
	}
}

// ValidateTransition prevents treating an unresolved recovery checkpoint as a
// completed bootstrap. Later orchestration owns all other transitions.
func ValidateTransition(from, to Mode) error {
	if from == Recovery && to == Completed {
		return errors.New("recovery cannot transition directly to completed")
	}
	return nil
}

type SourceCPA string

const (
	ExistingCPA SourceCPA = "existing"
	LegacyCPA   SourceCPA = "legacy"
	ExternalCPA SourceCPA = "external"
)

// Plan is a domain decision, not a Public API DTO or executable operation.
// ControlBasePath denotes the desired canonical value; normalization, collision
// checks and apply/rollback are deferred to later work.
type Plan struct {
	Mode                   Mode
	CPAMPAction            CPAMPAction
	CPAStrategy            CPAStrategy
	SourceCPAMP            *DirectUpgradeSource
	SourceCPA              SourceCPA
	UsageAction            UsageAction
	BackupRequired         bool
	RuntimeMode            RuntimeMode
	Mutations              []string
	Preserved              []string
	Unsupported            []string
	DiscoveredResourceRefs []ResourceRef
	AdoptionDecisions      []AdoptionResourceDecision
	ControlBasePath        string
	EstimatedSteps         []string
}

func RuntimeForStrategy(strategy CPAStrategy) (RuntimeMode, error) {
	switch strategy {
	case NewManaged, AdoptExisting, MigrateLegacy:
		return Embedded, nil
	case ConnectExternal:
		return External, nil
	default:
		return "", fmt.Errorf("unknown CPA strategy %q", strategy)
	}
}

// Validate checks a decision against the release-supplied exact source list.
func (p Plan) Validate(allowlist []DirectUpgradeSource) error {
	var expectedAction CPAMPAction
	var expectedUsage UsageAction
	var expectedBackup bool
	switch p.Mode {
	case Fresh:
		expectedAction, expectedUsage = Initialize, NoUsageAction
		if p.CPAStrategy != NewManaged && p.CPAStrategy != AdoptExisting && p.CPAStrategy != ConnectExternal {
			return errors.New("fresh requires new_managed, adopt_existing or connect_external")
		}
		if p.SourceCPAMP != nil {
			return errors.New("fresh cannot have a CPAMP v1 source")
		}
	case Upgrade:
		expectedAction, expectedUsage, expectedBackup = MigrateV1, PreserveUsage, true
		if p.CPAStrategy != MigrateLegacy && p.CPAStrategy != ConnectExternal {
			return errors.New("upgrade requires migrate_legacy or connect_external")
		}
		if p.SourceCPAMP == nil || !p.SourceCPAMP.valid() {
			return errors.New("upgrade requires an exact CPAMP source")
		}
		mode, err := Classify(ClassificationEvidence{HasV1ProductState: true, Source: p.SourceCPAMP}, allowlist)
		if err != nil || mode != Upgrade {
			return errors.New("upgrade source is not in the direct-upgrade allowlist")
		}
	default:
		return fmt.Errorf("mode %q cannot produce a user path plan", p.Mode)
	}
	if p.CPAMPAction != expectedAction || p.UsageAction != expectedUsage || p.BackupRequired != expectedBackup {
		return errors.New("CPAMP action, usage action or backup requirement conflicts with mode")
	}
	runtime, err := RuntimeForStrategy(p.CPAStrategy)
	if err != nil || p.RuntimeMode != runtime {
		return errors.New("runtime mode must derive from CPA strategy")
	}
	var expectedSource SourceCPA
	switch p.CPAStrategy {
	case AdoptExisting:
		expectedSource = ExistingCPA
	case MigrateLegacy:
		expectedSource = LegacyCPA
	case ConnectExternal:
		expectedSource = ExternalCPA
	}
	if p.SourceCPA != expectedSource {
		return errors.New("source CPA conflicts with strategy")
	}
	if strings.TrimSpace(p.ControlBasePath) == "" || len(p.EstimatedSteps) == 0 || len(p.Mutations) == 0 {
		return errors.New("plan requires controlBasePath, mutations and estimated steps")
	}
	if err := validateLists(p.Mutations, p.Preserved, p.Unsupported, p.EstimatedSteps); err != nil {
		return err
	}
	if p.Mode == Upgrade {
		if !has(p.Preserved, "usage_events_authoritative") || !has(p.Unsupported, "usage_events_export_transform_reimport") {
			return errors.New("upgrade must preserve authoritative usage_events without re-import")
		}
	}
	if p.CPAStrategy == AdoptExisting {
		for _, v := range []string{"historical_usage_import", "request_history_import", "cpa_log_import", "historical_analytics_import", "plugin_runtime_implicit_import", "source_cpa_stop", "source_cpa_delete", "source_cpa_overwrite"} {
			if !has(p.Unsupported, v) {
				return fmt.Errorf("adoption must disallow %s", v)
			}
		}
		if !has(p.Preserved, "source_cpa_recoverable_until_success") {
			return errors.New("adoption must keep source CPA recoverable until success")
		}
		if !has(p.Preserved, "source_cpa_available_until_commit") {
			return errors.New("adoption must keep source CPA available until commit")
		}
		if err := validateAdoptionDecisions(p.DiscoveredResourceRefs, p.AdoptionDecisions, p.Mutations, p.Unsupported); err != nil {
			return err
		}
	} else {
		if len(p.DiscoveredResourceRefs) != 0 || len(p.AdoptionDecisions) != 0 {
			return errors.New("adoption resources require adopt_existing strategy")
		}
		for _, group := range [][]string{p.Mutations, p.Unsupported} {
			for _, effect := range group {
				if strings.HasPrefix(effect, "adoption:") {
					return errors.New("adoption effect requires adopt_existing strategy")
				}
			}
		}
	}
	if p.CPAStrategy == ConnectExternal {
		for _, v := range []string{"source_cpa_stop", "source_cpa_delete", "source_cpa_takeover"} {
			if !has(p.Unsupported, v) {
				return fmt.Errorf("external connection must disallow %s", v)
			}
		}
		if !has(p.Preserved, "source_cpa_user_owned") {
			return errors.New("external CPA must remain user-owned")
		}
	}
	if p.Mode == Fresh && p.CPAStrategy != NewManaged && !has(p.Unsupported, "historical_usage_import") {
		return errors.New("fresh source CPA cannot import historical usage")
	}
	return nil
}

func validateLists(groups ...[]string) error {
	for _, group := range groups {
		if group == nil {
			return errors.New("plan lists must be explicit")
		}
		seen := map[string]bool{}
		for _, value := range group {
			if value == "" || seen[value] {
				return errors.New("plan lists contain empty or duplicate value")
			}
			seen[value] = true
		}
	}
	return nil
}

func has(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
