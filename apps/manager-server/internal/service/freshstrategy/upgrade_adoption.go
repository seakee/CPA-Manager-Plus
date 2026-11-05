package freshstrategy

// This file is the Upgrade surface's read of the one compiled qualification
// authority.
//
// A 1.x Managed CPA stack may only be copied into the v8 destination this
// Manager proved for it, and the copy may only happen under the per-reference
// decisions an accepted qualification record produced. The Upgrade surface holds
// no qualification record of its own, so it borrows exactly the two accepted
// consumers that together prove the whole chain:
//
//   - the legacy Managed consumer proves the compiled `CPA8-STAGE-r1`
//     legacy-to-v8 Supervisor staging transition was accepted and its artifact is
//     the one this binary carries;
//   - the network Existing consumer proves the compiled `CPA8-ADOPT-RESOURCE-r1`
//     v8 destination replay and verify chain for `client-api-keys`,
//     `cpa-auth-files`, `cpa-config-routing` and `provider-state-api-keys` was
//     accepted against that same v8 artifact.
//
// The history of the 1.x configuration is irrelevant here: the legacy source is
// read through the source adapter, and what must be v8-proven is the destination
// the executor writes into and reads back. That is why the decisions come from
// the network consumer rather than the legacy one, whose resource chain only
// ever exercised client keys.
//
// Nothing here is a second authority. Both consumers are the ones
// `loadProductionQualification` already validates against `consumer.json`,
// including the compiled implementation digest, so a semantic change to this
// package invalidates the record and these functions refuse. This file itself
// deliberately stays outside `consumerImplementation`: it grants no new copy
// capability, it only exposes a verdict the pinned files already reached.

import (
	"slices"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

// upgradeAdoptionRefs is the exact migrated resource set the Managed CPA import
// writes. A decision for anything less would make the compilation claim to have
// proven a chain the import cannot finish; a decision for anything more would
// authorize a copy this step does not perform.
var upgradeAdoptionRefs = []bootstrap.ResourceRef{
	bootstrap.AdoptionRefClientAPIKeys,
	bootstrap.AdoptionRefCPAAuthFiles,
	bootstrap.AdoptionRefConfigRouting,
	bootstrap.AdoptionRefProviderStateAPIKeys,
}

// VerifyCompiledUpgradeAdoption reports whether this binary carries the accepted
// staging transition and destination replay authority the Managed import runs
// under. It returns `errProductionQualification` for every way the compiled record
// fails to prove it, so a caller can only distinguish "proven" from "not proven".
func VerifyCompiledUpgradeAdoption() error {
	_, err := qualifiedUpgradeAdoption()
	return err
}

// QualifiedUpgradeAdoptionDecisions returns the per-reference decisions the
// Managed import is authorized to copy under: `Migrate` verdicts for client API
// keys, credential files, gateway routing configuration and provider runtime
// state. The slice is a defensive clone, so a caller cannot mutate the compiled
// record's decisions.
func QualifiedUpgradeAdoptionDecisions() ([]bootstrap.AdoptionResourceDecision, error) {
	_, decisions, err := qualifiedUpgradeAdoptionAuthority()
	if err != nil {
		return nil, err
	}
	return slices.Clone(decisions), nil
}

// QualifiedUpgradeAdoptionArtifactID returns the v8 artifact both accepted
// consumers were qualified against, which is the only artifact the Managed import
// may stage its destination on.
//
// The decisions alone say what may be copied; this says what it may be copied
// into. The Supervisor reports the artifact actually active, and a destination
// staged on another one is an artifact neither the staging transition nor the
// replay chain ever exercised, so a run that accepted it would copy under a
// verdict that does not describe the destination it writes.
func QualifiedUpgradeAdoptionArtifactID() (model.RuntimeArtifactID, error) {
	artifact, _, err := qualifiedUpgradeAdoptionAuthority()
	return artifact, err
}

// qualifiedUpgradeAdoption is the decisions half of the single derivation, for
// the callers that verify the authority without staging anything.
func qualifiedUpgradeAdoption() ([]bootstrap.AdoptionResourceDecision, error) {
	_, decisions, err := qualifiedUpgradeAdoptionAuthority()
	return decisions, err
}

// qualifiedUpgradeAdoptionAuthority is the single derivation every exported view
// shares. It never repairs or widens a decision: every resource it accepts was
// already validated by the record loader, and the set is collapsed onto the exact
// references the import copies.
func qualifiedUpgradeAdoptionAuthority() (model.RuntimeArtifactID, []bootstrap.AdoptionResourceDecision, error) {
	consumers, err := loadProductionQualification(productionEvidence)
	if err != nil {
		return "", nil, errProductionQualification
	}
	return qualifiedUpgradeAdoptionAuthorityFrom(consumers)
}

// qualifiedUpgradeAdoptionFrom is the derivation over an already-loaded record, so
// the refusal cases are testable without a second compiled evidence tree.
func qualifiedUpgradeAdoptionFrom(consumers map[consumerKey]qualifiedConsumer) ([]bootstrap.AdoptionResourceDecision, error) {
	_, decisions, err := qualifiedUpgradeAdoptionAuthorityFrom(consumers)
	return decisions, err
}

// qualifiedUpgradeAdoptionAuthorityFrom is the derivation itself.
func qualifiedUpgradeAdoptionAuthorityFrom(consumers map[consumerKey]qualifiedConsumer) (model.RuntimeArtifactID, []bootstrap.AdoptionResourceDecision, error) {
	legacy, ok := consumers[consumerKey{Strategy: bootstrap.AdoptExisting, Class: bootstrap.AdoptLegacyManaged}]
	if !ok || !legacy.Qualification.StagingV8TransitionQualified || legacy.ArtifactID == "" {
		// Without the staging transition the Supervisor may stage a destination
		// this qualification never accepted, and no copy may run under it.
		return "", nil, errProductionQualification
	}
	network, ok := consumers[consumerKey{Strategy: bootstrap.AdoptExisting, Class: bootstrap.AdoptNetworkExisting}]
	if !ok || network.ArtifactID != legacy.ArtifactID || len(network.AdoptionResources) == 0 {
		// The replay authority must be the exact artifact the staging transition
		// was accepted for, and it must carry the resource chain at all.
		return "", nil, errProductionQualification
	}
	authorized := make(map[bootstrap.ResourceRef]bootstrap.AdoptionResourceDecision, len(network.AdoptionResources))
	for _, decision := range network.AdoptionResources {
		if _, seen := authorized[decision.ResourceRef]; seen {
			return "", nil, errProductionQualification
		}
		disposition, err := bootstrap.DecideAdoptionResource(decision)
		if err != nil || disposition != bootstrap.Migrate ||
			decision.Qualification != bootstrap.CapabilityQualified ||
			decision.ManualAction != "" || decision.Disposition != disposition {
			return "", nil, errProductionQualification
		}
		authorized[decision.ResourceRef] = decision
	}
	// The returned set is exactly the references the import copies, in the table's
	// own order: a record carrying a further migrate verdict cannot widen what the
	// executor is handed, and a record missing one of these is refused rather than
	// returned as a partial authority.
	decisions := make([]bootstrap.AdoptionResourceDecision, 0, len(upgradeAdoptionRefs))
	for _, ref := range upgradeAdoptionRefs {
		decision, found := authorized[ref]
		if !found {
			return "", nil, errProductionQualification
		}
		decisions = append(decisions, decision)
	}
	return legacy.ArtifactID, decisions, nil
}
