package freshkeyapply

import (
	"context"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

// A committed add is immutable history, not proof that its key still works.
// Replaying a creation after later writes must observe the current canonical
// key without replaying the old mutation or treating its terminal ID as pending.
func (s *Service) observeCommittedAdd(ctx context.Context, result OperationResult, key freshkeys.Key, generation uint64) (OperationResult, error) {
	result.NextStep = ""
	if key.Lifecycle == identity.LifecycleSuperseded {
		return result, repo.ErrConflict
	}
	observed, err := s.ObserveKey(ctx, key, generation)
	result.RuntimeState = observed.RuntimeState
	if err != nil {
		return result, err
	}
	if observed.Lifecycle == identity.LifecycleSuperseded {
		return result, repo.ErrConflict
	}
	if result.RuntimeState != "effective_observed" {
		return result, cpa.ErrApplyUnknown
	}
	return result, nil
}
