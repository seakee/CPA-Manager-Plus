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

// ApplyStagedRotate stages (or replays) and executes an Option B in-place
// secret rotation under s.mu so candidate staging and operation journal creation
// are atomic with unsettled-operation reconciliation and preflight checks.
func (s *Service) ApplyStagedRotate(ctx context.Context, id, target string, generation uint64) (freshkeys.StagedRotation, bool, OperationResult, error) {
	result := OperationResult{RuntimeState: "effect_unknown"}
	s = s.active()
	if s == nil || s.repo == nil || s.keys == nil {
		return freshkeys.StagedRotation{}, false, result, ErrUnavailable
	}
	if !freshkeys.ValidRequestID(id) || !freshkeys.ValidRequestID(target) {
		return freshkeys.StagedRotation{}, false, result, freshkeys.ErrRequest
	}
	if !s.KeyOperationsEnabled() {
		return freshkeys.StagedRotation{}, false, result, ErrUnavailable
	}

	var (
		staged   freshkeys.StagedRotation
		created  bool
		prepared OperationResult
		prepErr  error
	)
	s.mu.Lock()
	if !s.KeyOperationsEnabled() {
		s.mu.Unlock()
		return freshkeys.StagedRotation{}, false, result, ErrUnavailable
	}
	previous, opErr := s.repo.Operation(ctx, id)
	if opErr != nil {
		s.mu.Unlock()
		return freshkeys.StagedRotation{}, false, result, opErr
	}
	if previous != nil && (previous.TargetID != target || previous.KeeperID != "" || previous.Mode != "rotate") {
		s.mu.Unlock()
		return freshkeys.StagedRotation{}, false, result, repo.ErrConflict
	}
	if previous == nil {
		latest, recErr := s.reconcileUnsettledLatestLocked(ctx, generation)
		if recErr != nil {
			s.mu.Unlock()
			return freshkeys.StagedRotation{}, false, result, recErr
		}
		if latest != nil && !latest.IsTerminal() {
			s.mu.Unlock()
			return freshkeys.StagedRotation{}, false, OperationResult{Operation: latest, RuntimeState: "effect_unknown"}, repo.ErrConflict
		}
	}
	if previous == nil {
		if sweepErr := s.keys.DiscardOrphanedRotate(ctx, id, target); sweepErr != nil {
			s.mu.Unlock()
			return freshkeys.StagedRotation{}, false, result, sweepErr
		}
	}
	staged, created, prepErr = s.keys.StageRotate(ctx, id, target)
	if prepErr != nil {
		s.mu.Unlock()
		return freshkeys.StagedRotation{}, false, result, prepErr
	}
	prepared, prepErr = s.prepareLocked(ctx, id, target, "", "rotate", generation)
	if prepErr != nil {
		if op, checkErr := s.repo.Operation(context.WithoutCancel(ctx), id); checkErr == nil && op == nil {
			_ = s.keys.DiscardUnstartedRotate(context.WithoutCancel(ctx), id, target)
		}
		s.mu.Unlock()
		return staged, created, prepared, prepErr
	}
	s.mu.Unlock()

	if prepared.Operation != nil && prepared.Operation.Outcome == repo.OutcomeCommitted {
		key := staged.Key
		if refreshed, detailErr := s.keys.Detail(ctx, target); detailErr == nil {
			key = refreshed
		}
		if key.Lifecycle == identity.LifecycleSuperseded {
			prepared.RuntimeState = "revoked_observed"
			prepared.NextStep = ""
			return staged, created, prepared, nil
		}
		obs, obsErr := s.observeCommittedAdd(ctx, prepared, key, generation)
		return staged, created, obs, obsErr
	}
	if prepared.Operation != nil && prepared.Operation.Outcome == repo.OutcomeAborted {
		return staged, created, prepared, repo.ErrConflict
	}
	if prepared.NextStep == "rotate" {
		cont, contErr := s.ContinueOperation(ctx, id, generation)
		return staged, created, cont, contErr
	}
	return staged, created, prepared, cpa.ErrApplyUnknown
}

// ApplyRotate executes or reconciles an Option B in-place secret rotation for
// one canonical Client Key while preserving its canonical ID, name and origin.
func (s *Service) ApplyRotate(ctx context.Context, id, target string, generation uint64) (OperationResult, error) {
	_, _, res, err := s.ApplyStagedRotate(ctx, id, target, generation)
	return res, err
}

// ReconcileUnsettled performs a read-only check on an unsettled LatestOperation
// so a lost HTTP reply after CPA applied a rotation/add/revoke settles before
// staging a subsequent request.
func (s *Service) ReconcileUnsettled(ctx context.Context, generation uint64) (*repo.Operation, error) {
	s = s.active()
	if s == nil || s.repo == nil || !s.KeyOperationsEnabled() {
		return nil, ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.keys != nil {
		if sweepErr := s.keys.DiscardOrphanedRotate(ctx, "", ""); sweepErr != nil {
			return nil, sweepErr
		}
	}
	latest, err := s.repo.LatestOperation(ctx)
	if err != nil || latest == nil || latest.IsTerminal() {
		return latest, err
	}
	resolvedGen, genErr := s.readGeneration(ctx, generation)
	if genErr != nil {
		return latest, genErr
	}
	return s.reconcileUnsettledLatestLocked(ctx, resolvedGen)
}
