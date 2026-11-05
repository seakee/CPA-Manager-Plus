package freshadopt

import (
	"errors"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

// qualifiedDecision builds the decision shape a qualification produces for one
// reference: the rule's own kind and scope, a qualified verdict, and evidence
// references naming the chain that proved it.
func qualifiedDecision(ref bootstrap.ResourceRef) bootstrap.AdoptionResourceDecision {
	rule, ok := bootstrap.FreshAdoptionRule(ref)
	if !ok {
		panic("unknown adoption reference " + string(ref))
	}
	decision := bootstrap.AdoptionResourceDecision{
		ResourceRef:       ref,
		Kind:              rule.Kind,
		Scope:             rule.Scope,
		Qualification:     bootstrap.CapabilityQualified,
		ReadEvidenceRef:   bootstrap.CapabilityEvidenceRef("preflight:" + string(ref)),
		ReplayEvidenceRef: bootstrap.CapabilityEvidenceRef("preflight:" + string(ref)),
		VerifyEvidenceRef: bootstrap.CapabilityEvidenceRef("preflight:" + string(ref)),
	}
	disposition, err := bootstrap.DecideAdoptionResource(decision)
	if err != nil {
		panic(err)
	}
	decision.Disposition = disposition
	return decision
}

// authorizedSet is the set the Managed import copies under.
func authorizedSet() []bootstrap.AdoptionResourceDecision {
	return []bootstrap.AdoptionResourceDecision{
		qualifiedDecision(bootstrap.AdoptionRefClientAPIKeys),
		qualifiedDecision(bootstrap.AdoptionRefCPAAuthFiles),
		qualifiedDecision(bootstrap.AdoptionRefConfigRouting),
		qualifiedDecision(bootstrap.AdoptionRefProviderStateAPIKeys),
	}
}

func TestNewAuthorizationRequiresAProducingAuthority(t *testing.T) {
	if _, err := NewAuthorization(authorizedSet(), nil); !errors.Is(err, ErrAuthorizationUnproven) {
		t.Fatalf("a nil authority produced %v, want ErrAuthorizationUnproven", err)
	}
	refused := errors.New("the compiled record is not accepted")
	_, err := NewAuthorization(authorizedSet(), func() error { return refused })
	if !errors.Is(err, ErrAuthorizationUnproven) || !errors.Is(err, refused) {
		t.Fatalf("a refused authority produced %v, want the refusal wrapped", err)
	}
}

func TestNewAuthorizationAcceptsTheQualifiedSet(t *testing.T) {
	decisions := authorizedSet()
	auth, err := NewAuthorization(decisions, func() error { return nil })
	if err != nil {
		t.Fatalf("authorize the qualified set: %v", err)
	}
	if len(auth.decisionsOf()) != len(decisions) {
		t.Fatalf("authorization carries %d decisions, want %d", len(auth.decisionsOf()), len(decisions))
	}
	// The authorization clones its input, so a caller cannot widen it afterwards.
	decisions[0].ManualAction = bootstrap.ReconfigureAfterAdoption
	if auth.decisionsOf()[0].ManualAction != "" {
		t.Fatal("a caller's mutation reached the authorization")
	}
}

// TestAuthorizationVerifiedDistinguishesProofFromADecisionList pins the Medium
// finding: a decision list is not the proof. Only NewAuthorization's value reports
// itself verified, so a step that must copy under a proven authority can tell the
// two apart instead of inferring proof from a non-empty slice.
func TestAuthorizationVerifiedDistinguishesProofFromADecisionList(t *testing.T) {
	auth, err := NewAuthorization(authorizedSet(), func() error { return nil })
	if err != nil {
		t.Fatalf("authorize the qualified set: %v", err)
	}
	if !auth.Verified() {
		t.Fatal("a verified authorization does not report itself verified")
	}
	// New — the Fresh adoption's constructor — builds exactly this value from a
	// bare decision list: the decisions are there, the producing authority's proof
	// is not.
	if (Authorization{decisions: authorizedSet()}).Verified() {
		t.Fatal("a bare decision list reported a verified authority")
	}
	if (Authorization{}).Verified() {
		t.Fatal("the zero authorization reported itself verified")
	}
}

func TestNewAuthorizationRefusesAnIncompleteCopySet(t *testing.T) {
	verify := func() error { return nil }
	for _, test := range []struct {
		name      string
		decisions []bootstrap.AdoptionResourceDecision
	}{
		{name: "no decisions", decisions: nil},
		{name: "no client keys", decisions: []bootstrap.AdoptionResourceDecision{
			qualifiedDecision(bootstrap.AdoptionRefCPAAuthFiles),
		}},
		{name: "a duplicate reference", decisions: []bootstrap.AdoptionResourceDecision{
			qualifiedDecision(bootstrap.AdoptionRefClientAPIKeys),
			qualifiedDecision(bootstrap.AdoptionRefClientAPIKeys),
		}},
		{name: "an unqualified verdict", decisions: []bootstrap.AdoptionResourceDecision{{
			ResourceRef:   bootstrap.AdoptionRefClientAPIKeys,
			Kind:          bootstrap.APIKeys,
			Scope:         bootstrap.ClientAPIKeys,
			Qualification: bootstrap.CapabilityPending,
		}}},
		{name: "a migrate verdict with a manual action", decisions: []bootstrap.AdoptionResourceDecision{
			func() bootstrap.AdoptionResourceDecision {
				decision := qualifiedDecision(bootstrap.AdoptionRefClientAPIKeys)
				decision.ManualAction = bootstrap.ReconfigureAfterAdoption
				return decision
			}(),
		}},
		{name: "a decision under another kind's shape", decisions: []bootstrap.AdoptionResourceDecision{
			func() bootstrap.AdoptionResourceDecision {
				decision := qualifiedDecision(bootstrap.AdoptionRefClientAPIKeys)
				decision.Kind = bootstrap.Credentials
				return decision
			}(),
		}},
		{name: "a disposition that disagrees with its evidence", decisions: []bootstrap.AdoptionResourceDecision{
			func() bootstrap.AdoptionResourceDecision {
				decision := qualifiedDecision(bootstrap.AdoptionRefClientAPIKeys)
				decision.Disposition = bootstrap.ManualAction
				return decision
			}(),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewAuthorization(test.decisions, verify); !errors.Is(err, ErrAuthorizationInvalid) {
				t.Fatalf("%s produced %v, want ErrAuthorizationInvalid", test.name, err)
			}
		})
	}
}

// TestZeroAuthorizationAuthorizesNothing pins the fail-closed value: the zero
// Authorization carries no decisions, so the executor records every item as
// manual and writes nothing.
func TestZeroAuthorizationAuthorizesNothing(t *testing.T) {
	var auth Authorization
	if decisions := auth.decisionsOf(); len(decisions) != 0 {
		t.Fatalf("the zero authorization carries %d decisions", len(decisions))
	}
	if NewExecutor(nil, nil, auth).decisions == nil {
		t.Fatal("the executor did not index the empty authorization")
	}
	if len(NewExecutor(nil, nil, auth).decisions) != 0 {
		t.Fatal("the zero authorization authorized a resource")
	}
}
