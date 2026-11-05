package freshstrategy

import (
	"errors"
	"slices"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
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
