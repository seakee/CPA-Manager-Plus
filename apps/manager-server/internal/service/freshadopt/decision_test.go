package freshadopt

// This test pins the one authority the executor serves. A Fresh adoption copies
// only what its qualification evidence authorizes; a synthetic "qualified"
// decision that carries no read/replay/verify reference stays manual.

import (
	"context"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
)

// TestFreshDecisionWithoutEvidenceStillFailsClosed pins the boundary: the Fresh
// path keeps requiring read/replay/verify references, so a synthetic "qualified"
// shape cannot become a copy.
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
