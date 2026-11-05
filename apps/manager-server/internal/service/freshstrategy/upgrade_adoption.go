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
	decisions, err := qualifiedUpgradeAdoption()
	if err != nil {
		return nil, err
	}
	return slices.Clone(decisions), nil
}

// qualifiedUpgradeAdoption is the single derivation both exported functions
// share. It never repairs or widens a decision: every resource it accepts was
// already validated by the record loader, and the set is checked against the
// exact references the import copies.
func qualifiedUpgradeAdoption() ([]bootstrap.AdoptionResourceDecision, error) {
	consumers, err := loadProductionQualification(productionEvidence)
	if err != nil {
		return nil, errProductionQualification
	}
	return qualifiedUpgradeAdoptionFrom(consumers)
}

// qualifiedUpgradeAdoptionFrom is the derivation over an already-loaded record, so
// the refusal cases are testable without a second compiled evidence tree.
func qualifiedUpgradeAdoptionFrom(consumers map[consumerKey]qualifiedConsumer) ([]bootstrap.AdoptionResourceDecision, error) {
	legacy, ok := consumers[consumerKey{Strategy: bootstrap.AdoptExisting, Class: bootstrap.AdoptLegacyManaged}]
	if !ok || !legacy.Qualification.StagingV8TransitionQualified || legacy.ArtifactID == "" {
		// Without the staging transition the Supervisor may stage a destination
		// this qualification never accepted, and no copy may run under it.
		return nil, errProductionQualification
	}
	network, ok := consumers[consumerKey{Strategy: bootstrap.AdoptExisting, Class: bootstrap.AdoptNetworkExisting}]
	if !ok || network.ArtifactID != legacy.ArtifactID || len(network.AdoptionResources) == 0 {
		// The replay authority must be the exact artifact the staging transition
		// was accepted for, and it must carry the resource chain at all.
		return nil, errProductionQualification
	}
	authorized := make(map[bootstrap.ResourceRef]bool, len(network.AdoptionResources))
	for _, decision := range network.AdoptionResources {
		if authorized[decision.ResourceRef] {
			return nil, errProductionQualification
		}
		disposition, err := bootstrap.DecideAdoptionResource(decision)
		if err != nil || disposition != bootstrap.Migrate ||
			decision.Qualification != bootstrap.CapabilityQualified ||
			decision.ManualAction != "" || decision.Disposition != disposition {
			return nil, errProductionQualification
		}
		authorized[decision.ResourceRef] = true
	}
	for _, ref := range upgradeAdoptionRefs {
		if !authorized[ref] {
			return nil, errProductionQualification
		}
	}
	return network.AdoptionResources, nil
}
