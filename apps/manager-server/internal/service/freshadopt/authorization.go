package freshadopt

// The executor serves one authority: the per-reference decisions a qualification
// produced. Fresh adoption reads them from its own checkpoint, so it constructs
// an executor directly. The Upgrade surface holds no checkpoint of its own and
// borrows the compiled record instead, so it must prove that record before the
// executor is built rather than passing a bare reference set to it.
//
// Authorization is that proof: a decision set plus the verification that produced
// it. The zero value carries no decisions and therefore authorizes nothing, which
// is the same fail-closed shape the executor already has for a caller that holds
// no record.

import (
	"errors"
	"slices"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
)

var (
	// ErrAuthorizationUnproven reports a decision set whose producing authority
	// is missing or refused.
	ErrAuthorizationUnproven = errors.New("freshadopt: adoption authorization unproven")
	// ErrAuthorizationInvalid reports a decision set the adoption rules do not
	// accept as a migrate verdict, or one that cannot copy what it claims.
	ErrAuthorizationInvalid = errors.New("freshadopt: adoption authorization invalid")
)

// Authorization is the verified per-reference decision set one run may copy
// under. Its decisions are unexported on purpose: a caller either holds a
// verified Authorization or holds nothing, and the executor never accepts an
// arbitrary reference list as a substitute.
type Authorization struct {
	decisions []bootstrap.AdoptionResourceDecision
}

// NewAuthorization builds an authorization from decisions produced by verify.
//
// verify is required and runs before anything else: a decision set with no
// producing authority is exactly the bare reference set this type exists to
// refuse. Every decision must then be the fully qualified migrate verdict the
// adoption rules allow for its reference, its reference must appear once, and
// client API keys must be present: a run that copies configuration without the
// client keys the destination was staged with is a partial adoption, not an
// authorized one.
func NewAuthorization(decisions []bootstrap.AdoptionResourceDecision, verify func() error) (Authorization, error) {
	if verify == nil {
		return Authorization{}, ErrAuthorizationUnproven
	}
	if err := verify(); err != nil {
		return Authorization{}, errors.Join(ErrAuthorizationUnproven, err)
	}
	if len(decisions) == 0 {
		return Authorization{}, ErrAuthorizationInvalid
	}
	indexed := make(map[bootstrap.ResourceRef]bool, len(decisions))
	for _, decision := range decisions {
		if indexed[decision.ResourceRef] {
			return Authorization{}, ErrAuthorizationInvalid
		}
		disposition, err := bootstrap.DecideAdoptionResource(decision)
		if err != nil || disposition != bootstrap.Migrate ||
			decision.Qualification != bootstrap.CapabilityQualified ||
			decision.ManualAction != "" || decision.Disposition != disposition {
			return Authorization{}, ErrAuthorizationInvalid
		}
		// The rule for this reference decides the kind and scope: a decision
		// cannot claim a migrate verdict under another kind's shape.
		expected, ok := bootstrap.FreshAdoptionDecision(decision.ResourceRef, decision)
		if !ok || expected != decision {
			return Authorization{}, ErrAuthorizationInvalid
		}
		indexed[decision.ResourceRef] = true
	}
	if !indexed[bootstrap.AdoptionRefClientAPIKeys] {
		return Authorization{}, ErrAuthorizationInvalid
	}
	return Authorization{decisions: slices.Clone(decisions)}, nil
}

// decisionsOf returns the authorization's decisions for the executor's index. A
// zero Authorization yields none, and the executor then records every item as
// manual work.
func (a Authorization) decisionsOf() []bootstrap.AdoptionResourceDecision {
	return a.decisions
}
