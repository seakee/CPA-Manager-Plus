package cpaupdate

import (
	"context"
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

type BaselineStatus struct {
	PinnedVersion            string `json:"pinned_version"`
	PinnedArtifactID         string `json:"pinned_artifact_id"`
	BundledAvailable         bool   `json:"bundled_available"`
	AdoptBaselineAvailable   bool   `json:"adopt_baseline_available"`
	BaselineUpgradeAvailable bool   `json:"baseline_upgrade_available"`
}

type AdoptBaselineResult struct {
	ActiveArtifactID string         `json:"active_artifact_id"`
	Baseline         BaselineStatus `json:"baseline"`
}

// BaselineArtifactClient supplies Supervisor baseline inspection and adoption
// for Embedded installations, allowing callers to wrap the raw client with
// installation authority, lifecycle locking, and post-adoption reconciliation.
type BaselineArtifactClient interface {
	Status(context.Context) (runtimeservice.ArtifactStatus, error)
	AdoptBundledBaseline(context.Context, string) (runtimeservice.ArtifactStatus, error)
}

func projectBaseline(status runtimeservice.ArtifactStatus) BaselineStatus {
	return BaselineStatus{
		PinnedVersion: status.Pinned.Version, PinnedArtifactID: status.Pinned.ArtifactID,
		BundledAvailable:       status.BundledAvailable,
		AdoptBaselineAvailable: status.AdoptBaselineAvailable, BaselineUpgradeAvailable: status.BaselineUpgradeAvailable,
	}
}

func (s *Service) AdoptBaseline(ctx context.Context, expected model.RuntimeArtifactID) (AdoptBaselineResult, error) {
	if !expected.IsValid() {
		return AdoptBaselineResult{}, newMutationError(MutationErrorInvalid, "invalid_request")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	if err := s.requireEmbeddedProductComplete(ctx); err != nil {
		return AdoptBaselineResult{}, err
	}
	if s.runtime == nil || s.artifact == nil {
		return AdoptBaselineResult{}, newMutationError(MutationErrorUnavailable, "runtime_unavailable")
	}
	observed, err := s.runtime.Status(ctx)
	if err != nil {
		return AdoptBaselineResult{}, newMutationError(MutationErrorUnavailable, "runtime_observation_unavailable")
	}
	if err := observed.Validate(); err != nil {
		return AdoptBaselineResult{}, newMutationError(MutationErrorUnavailable, "runtime_observation_invalid")
	}
	if observed.ActiveGatewayArtifact == nil {
		return AdoptBaselineResult{}, newMutationError(MutationErrorConflict, "active_artifact_unavailable")
	}
	active := observed.ActiveGatewayArtifact
	if active.ArtifactID != expected {
		return AdoptBaselineResult{}, newMutationError(MutationErrorConflict, "active_artifact_mismatch")
	}
	if _, err := parseSupportedUpdateVersion(active.Version); err != nil {
		return AdoptBaselineResult{}, newMutationError(MutationErrorConflict, "current_version_unsupported")
	}

	status, err := s.artifact.AdoptBundledBaseline(ctx, string(expected))
	if err != nil {
		if code, status, ok := runtimeservice.ArtifactErrorCode(err); ok {
			switch status {
			case http.StatusConflict:
				return AdoptBaselineResult{}, newMutationError(MutationErrorConflict, code)
			case http.StatusInternalServerError:
				return AdoptBaselineResult{}, newMutationError(MutationErrorExecution, code)
			}
		}
		return AdoptBaselineResult{}, newMutationError(MutationErrorUnavailable, "runtime_result_unreliable")
	}
	if status.Active == nil {
		return AdoptBaselineResult{}, newMutationError(MutationErrorUnavailable, "runtime_result_unreliable")
	}
	return AdoptBaselineResult{ActiveArtifactID: status.Active.ArtifactID, Baseline: projectBaseline(status)}, nil
}
