package freshstrategy

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
)

// TestVerifyCompiledUpgradeAdoptionAcceptsTheCarriedRecord pins the positive
// control: this binary carries both consumers the Upgrade surface borrows, and
// the derivation returns them without repairing or widening anything.
func TestVerifyCompiledUpgradeAdoptionAcceptsTheCarriedRecord(t *testing.T) {
	if err := VerifyCompiledUpgradeAdoption(); err != nil {
		t.Fatalf("the carried qualification does not prove the Managed import: %v", err)
	}
}

// TestQualifiedUpgradeAdoptionDecisionsAuthorizesTheManagedCopySet pins the exact
// set the import copies under: the four references, each a qualified migrate
// verdict, and nothing the rules keep manual.
func TestQualifiedUpgradeAdoptionDecisionsAuthorizesTheManagedCopySet(t *testing.T) {
	decisions, err := QualifiedUpgradeAdoptionDecisions()
	if err != nil {
		t.Fatalf("read the decisions: %v", err)
	}
	want := []bootstrap.ResourceRef{
		bootstrap.AdoptionRefClientAPIKeys,
		bootstrap.AdoptionRefCPAAuthFiles,
		bootstrap.AdoptionRefConfigRouting,
		bootstrap.AdoptionRefProviderStateAPIKeys,
	}
	if len(decisions) != len(want) {
		t.Fatalf("decisions = %d, want the %d the import copies", len(decisions), len(want))
	}
	for _, ref := range want {
		index := slices.IndexFunc(decisions, func(decision bootstrap.AdoptionResourceDecision) bool {
			return decision.ResourceRef == ref
		})
		if index < 0 {
			t.Fatalf("decision set carries no %q", ref)
		}
		decision := decisions[index]
		disposition, err := bootstrap.DecideAdoptionResource(decision)
		if err != nil || disposition != bootstrap.Migrate {
			t.Fatalf("%q = %q/%v, want migrate", ref, disposition, err)
		}
		if decision.Qualification != bootstrap.CapabilityQualified || decision.ManualAction != "" {
			t.Fatalf("%q decision = %+v, want a qualified migrate verdict", ref, decision)
		}
	}
}

// TestQualifiedUpgradeAdoptionDecisionsClonesTheRecord pins the defensive copy:
// a caller that mutates the returned slice cannot change what a later read
// derives from the compiled record.
func TestQualifiedUpgradeAdoptionDecisionsClonesTheRecord(t *testing.T) {
	first, err := QualifiedUpgradeAdoptionDecisions()
	if err != nil {
		t.Fatalf("read the decisions: %v", err)
	}
	first[0].ResourceRef = "mutated"
	first[0].ManualAction = "delete"

	second, err := QualifiedUpgradeAdoptionDecisions()
	if err != nil {
		t.Fatalf("read the decisions again: %v", err)
	}
	if second[0].ResourceRef == "mutated" || second[0].ManualAction == "delete" {
		t.Fatalf("a caller's mutation reached the compiled record: %+v", second[0])
	}
	if err := VerifyCompiledUpgradeAdoption(); err != nil {
		t.Fatalf("a caller's mutation invalidated the record: %v", err)
	}
}

// TestUpgradeAdoptionRefusesARefusedRecord pins the derivation's own check: a
// record that does not carry both consumers cannot authorize the copy, and the
// refusal is the same opaque error the loader reports.
func TestUpgradeAdoptionRefusesARefusedRecord(t *testing.T) {
	consumers, err := loadProductionQualification(productionEvidence)
	if err != nil {
		t.Fatalf("load the carried qualification: %v", err)
	}
	if _, ok := consumers[consumerKey{Strategy: bootstrap.AdoptExisting, Class: bootstrap.AdoptNetworkExisting}]; !ok {
		t.Fatalf("the fixture assumption failed: no network Existing consumer")
	}
	// A consumer map without the network replay authority is exactly the record
	// shape that must not authorize the copy.
	delete(consumers, consumerKey{Strategy: bootstrap.AdoptExisting, Class: bootstrap.AdoptNetworkExisting})
	if _, err := qualifiedUpgradeAdoptionFrom(consumers); !errors.Is(err, errProductionQualification) {
		t.Fatalf("a record without the replay authority produced %v, want the opaque refusal", err)
	}
}

// TestQualifiedUpgradeAdoptionReturnsExactlyTheCopySet pins the returned set: the
// derivation hands the executor the four references the import copies, in the
// table's own order, and refuses a record that would authorize fewer — a partial
// set is a chain the import cannot finish, not a narrower copy.
func TestQualifiedUpgradeAdoptionReturnsExactlyTheCopySet(t *testing.T) {
	decisions, err := qualifiedUpgradeAdoption()
	if err != nil {
		t.Fatalf("read the decisions: %v", err)
	}
	got := make([]bootstrap.ResourceRef, 0, len(decisions))
	for _, decision := range decisions {
		got = append(got, decision.ResourceRef)
	}
	if !slices.Equal(got, upgradeAdoptionRefs) {
		t.Fatalf("decisions = %v, want exactly %v", got, upgradeAdoptionRefs)
	}

	consumers, err := loadProductionQualification(productionEvidence)
	if err != nil {
		t.Fatalf("load the carried qualification: %v", err)
	}
	key := consumerKey{Strategy: bootstrap.AdoptExisting, Class: bootstrap.AdoptNetworkExisting}
	network := consumers[key]
	network.AdoptionResources = network.AdoptionResources[:len(network.AdoptionResources)-1]
	consumers[key] = network
	if _, err := qualifiedUpgradeAdoptionFrom(consumers); !errors.Is(err, errProductionQualification) {
		t.Fatalf("a record missing one copied reference produced %v, want the opaque refusal", err)
	}
}

// TestQualifiedUpgradeAdoptionArtifactIDNamesTheQualifiedArtifact pins the other
// half of the authority: the artifact the import may stage its destination on is
// the one both consumers were accepted against, and a record whose consumers do
// not share it is refused.
func TestQualifiedUpgradeAdoptionArtifactIDNamesTheQualifiedArtifact(t *testing.T) {
	artifact, err := QualifiedUpgradeAdoptionArtifactID()
	if err != nil {
		t.Fatalf("read the qualified artifact: %v", err)
	}
	consumers, err := loadProductionQualification(productionEvidence)
	if err != nil {
		t.Fatalf("load the carried qualification: %v", err)
	}
	legacy := consumers[consumerKey{Strategy: bootstrap.AdoptExisting, Class: bootstrap.AdoptLegacyManaged}]
	network := consumers[consumerKey{Strategy: bootstrap.AdoptExisting, Class: bootstrap.AdoptNetworkExisting}]
	if artifact == "" || artifact != legacy.ArtifactID || artifact != network.ArtifactID {
		t.Fatalf("qualified artifact = %q, want the one both consumers name (%q / %q)", artifact, legacy.ArtifactID, network.ArtifactID)
	}

	// A record whose replay authority was accepted on another artifact cannot
	// name a destination this build may stage: the copy would land on an artifact
	// no chain was accepted for.
	network.ArtifactID = model.RuntimeArtifactID("sha256:" + strings.Repeat("cd", 32))
	consumers[consumerKey{Strategy: bootstrap.AdoptExisting, Class: bootstrap.AdoptNetworkExisting}] = network
	if _, _, err := qualifiedUpgradeAdoptionAuthorityFrom(consumers); !errors.Is(err, errProductionQualification) {
		t.Fatalf("a record qualified on another artifact produced %v, want the opaque refusal", err)
	}
}
