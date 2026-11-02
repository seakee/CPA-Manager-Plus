package freshadopt

// These tests split the two authorities the executor serves. A Fresh adoption
// copies only what its qualification evidence authorizes; an Upgrade step has no
// qualification record, so it passes an explicit UpgradeAuthorization that names
// the references it copies. Neither path may borrow the other's shape: an
// unbacked "qualified" decision stays manual, and an Upgrade authorization cannot
// name a reference the product forbids.

import (
	"context"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
)

// runUpgradeMigration drives the executor the way the Upgrade step does: one
// explicit authorization, no per-reference evidence.
func runUpgradeMigration(t *testing.T, source, destination *fakeCPA, authorization UpgradeAuthorization) freshstore.AdoptionSummary {
	t.Helper()
	sourceWire, err := NewSource(t.Context(), source.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	destinationWire, err := NewDestination(t.Context(), destination.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewUpgrade(sourceWire, destinationWire, authorization)
	if err != nil {
		t.Fatalf("build the Upgrade executor: %v", err)
	}
	result, err := executor.Run(context.Background(), freshstore.AdoptionSummary{}, nil)
	if err != nil {
		t.Fatalf("Upgrade migration failed: %v", err)
	}
	return result.Summary
}

// TestUpgradeAuthorizationCopiesOnlyItsNamedReferences proves the Upgrade
// authorization is per reference, like the Fresh decision it replaces: the named
// kinds are written, and every other kind keeps the product's manual action.
func TestUpgradeAuthorizationCopiesOnlyItsNamedReferences(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	summary := runUpgradeMigration(t, source, destination, UpgradeAuthorization{Refs: []bootstrap.ResourceRef{
		bootstrap.AdoptionRefConfigRouting,
		bootstrap.AdoptionRefCPAAuthFiles,
	}})

	if item := itemByName(t, summary, credentialItemName("claude-a.json")); item.Outcome != freshstore.AdoptionMigrated {
		t.Fatalf("an authorized credential was not migrated: %+v", item)
	}
	// The provider sections are present at the source, so recording them as
	// manual is a decision not to write them rather than an absence.
	if _, presence := cpa.LookupConfigValue(source.config, "api-keys.claude"); presence != cpa.ConfigPresent {
		t.Fatal("the fixture source does not carry api-keys.claude")
	}
	item := itemByName(t, summary, providerSectionItemName("api-keys.claude"))
	if item.Outcome != freshstore.AdoptionManual || item.Reason != string(bootstrap.ReauthenticateAfterAdoption) {
		t.Fatalf("an unauthorized provider section was not recorded as manual work: %+v", item)
	}
	if lookup(destination.config, []string{"api-keys", "claude"}) != nil {
		t.Fatal("an unauthorized provider section reached the destination")
	}
}

// TestUpgradeAuthorizationRefusesReferencesTheProductDoesNotCopy proves the new
// value cannot widen the executor: an empty authorization, a Manager-owned
// section and an unknown reference are refused before a run starts.
func TestUpgradeAuthorizationRefusesReferencesTheProductDoesNotCopy(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	sourceWire, err := NewSource(t.Context(), source.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	destinationWire, err := NewDestination(t.Context(), destination.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	for name, authorization := range map[string]UpgradeAuthorization{
		"empty":         {},
		"manager-owned": {Refs: []bootstrap.ResourceRef{bootstrap.AdoptionRefConfigServer}},
		"unknown":       {Refs: []bootstrap.ResourceRef{"not-a-reference"}},
		"mixture":       {Refs: []bootstrap.ResourceRef{bootstrap.AdoptionRefCPAAuthFiles, bootstrap.AdoptionRefConfigManagement}},
	} {
		if _, err := NewUpgrade(sourceWire, destinationWire, authorization); err == nil {
			t.Fatalf("%s authorization was accepted", name)
		}
	}
}

// TestFreshDecisionWithoutEvidenceStillFailsClosed is the other half of the
// boundary: the Fresh path keeps requiring read/replay/verify references, so a
// synthetic "qualified" shape cannot become a copy.
func TestFreshDecisionWithoutEvidenceStillFailsClosed(t *testing.T) {
	source, destination := sourceFixture(t), destinationFixture(t)
	rule, ok := bootstrap.FreshAdoptionRule(bootstrap.AdoptionRefCPAAuthFiles)
	if !ok {
		t.Fatal("the credential reference has no rule")
	}
	decisions := []bootstrap.AdoptionResourceDecision{{
		ResourceRef:   bootstrap.AdoptionRefCPAAuthFiles,
		Kind:          rule.Kind,
		Scope:         rule.Scope,
		Qualification: bootstrap.CapabilityQualified,
		Disposition:   bootstrap.Migrate,
	}}
	sourceWire, err := NewSource(t.Context(), source.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	destinationWire, err := NewDestination(t.Context(), destination.endpoint())
	if err != nil {
		t.Fatal(err)
	}
	result, err := New(sourceWire, destinationWire, decisions).Run(context.Background(), freshstore.AdoptionSummary{}, nil)
	if err != nil {
		t.Fatalf("the Fresh run failed instead of recording the item as manual: %v", err)
	}
	item := itemByName(t, result.Summary, credentialItemName("claude-a.json"))
	if item.Outcome != freshstore.AdoptionManual {
		t.Fatalf("a decision with no evidence references copied a credential: %+v", item)
	}
	if _, uploaded := destination.credential("claude-a.json"); uploaded {
		t.Fatal("the destination received a credential the Fresh record did not qualify")
	}
}
