package cpaupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

type MutationPhase string

const (
	MutationPhasePrepare  MutationPhase = "prepare"
	MutationPhaseActivate MutationPhase = "activate"
)

const maxMutationRequestIDBytes = 64

var (
	mutationRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]+$`)
	failureCodePattern       = regexp.MustCompile(`^[a-z0-9_]+$`)
)

type MutationRequest struct {
	RequestID                string                  `json:"request_id"`
	TargetVersion            string                  `json:"target_version"`
	ExpectedActiveArtifactID model.RuntimeArtifactID `json:"expected_active_artifact_id"`
}

func (r MutationRequest) Validate() error {
	if len(r.RequestID) == 0 || len([]byte(r.RequestID)) > maxMutationRequestIDBytes ||
		!mutationRequestIDPattern.MatchString(r.RequestID) {
		return newMutationError(MutationErrorInvalid, "invalid_request")
	}
	if _, err := parseStableVersion(r.TargetVersion); err != nil {
		return newMutationError(MutationErrorInvalid, "invalid_request")
	}
	if !r.ExpectedActiveArtifactID.IsValid() {
		return newMutationError(MutationErrorInvalid, "invalid_request")
	}
	return nil
}

type MutationResult struct {
	Phase                    MutationPhase `json:"phase"`
	RequestID                string        `json:"request_id"`
	TargetVersion            string        `json:"target_version"`
	ExpectedActiveArtifactID string        `json:"expected_active_artifact_id"`
	RuntimeOperationID       string        `json:"runtime_operation_id"`
	State                    string        `json:"state"`
	FailureCode              string        `json:"failure_code,omitempty"`
	AlreadyApplied           bool          `json:"already_applied"`
}

type MutationErrorKind string

const (
	MutationErrorInvalid     MutationErrorKind = "invalid"
	MutationErrorConflict    MutationErrorKind = "conflict"
	MutationErrorExecution   MutationErrorKind = "execution"
	MutationErrorUnavailable MutationErrorKind = "unavailable"
)

type MutationError struct {
	Kind MutationErrorKind
	Code string
}

func (e *MutationError) Error() string {
	switch e.Kind {
	case MutationErrorInvalid:
		return "invalid CPA update mutation request"
	case MutationErrorConflict:
		return "CPA update mutation conflicts with current state"
	case MutationErrorExecution:
		return "CPA update mutation execution failed"
	default:
		return "CPA update mutation is unavailable"
	}
}

func MutationErrorDetails(err error) (MutationErrorKind, string, bool) {
	var mutationErr *MutationError
	if !errors.As(err, &mutationErr) {
		return "", "", false
	}
	return mutationErr.Kind, mutationErr.Code, true
}

func newMutationError(kind MutationErrorKind, code string) error {
	return &MutationError{Kind: kind, Code: code}
}

func (s *Service) Prepare(ctx context.Context, request MutationRequest) (MutationResult, error) {
	return s.mutate(ctx, MutationPhasePrepare, request)
}

func (s *Service) Activate(ctx context.Context, request MutationRequest) (MutationResult, error) {
	return s.mutate(ctx, MutationPhaseActivate, request)
}

func (s *Service) mutate(ctx context.Context, phase MutationPhase, request MutationRequest) (MutationResult, error) {
	if err := request.Validate(); err != nil {
		return MutationResult{}, err
	}
	if phase != MutationPhasePrepare && phase != MutationPhaseActivate {
		return MutationResult{}, newMutationError(MutationErrorInvalid, "invalid_phase")
	}
	result := newMutationResult(phase, request)

	if !s.mode.IsValid() {
		return MutationResult{}, newMutationError(MutationErrorUnavailable, "runtime_mode_invalid")
	}
	if s.mode != model.RuntimeModeEmbedded {
		return MutationResult{}, newMutationError(MutationErrorConflict, "managed_externally")
	}
	state, now, err := s.discoverySnapshot(ctx)
	if err != nil {
		return MutationResult{}, err
	}
	if state.TargetVersion == "" || state.LastSuccessAt.IsZero() || state.LastError != "" || discoveryIsStale(state, now) {
		return MutationResult{}, newMutationError(MutationErrorConflict, "recommendation_not_fresh")
	}
	if request.TargetVersion != state.TargetVersion {
		return MutationResult{}, newMutationError(MutationErrorConflict, "target_version_mismatch")
	}
	if s.runtime == nil {
		return MutationResult{}, newMutationError(MutationErrorUnavailable, "runtime_unavailable")
	}

	observed, err := s.runtime.Status(ctx)
	if err != nil {
		return MutationResult{}, newMutationError(MutationErrorUnavailable, "runtime_observation_unavailable")
	}
	if err := observed.Validate(); err != nil {
		return MutationResult{}, newMutationError(MutationErrorUnavailable, "runtime_observation_invalid")
	}
	if observed.State != model.RuntimeStateReady || strings.TrimSpace(string(observed.Identity)) == "" || observed.Generation == 0 {
		return MutationResult{}, newMutationError(MutationErrorConflict, "runtime_state_conflict")
	}
	artifact := observed.ActiveGatewayArtifact
	if artifact == nil {
		return MutationResult{}, newMutationError(MutationErrorConflict, "active_artifact_unavailable")
	}
	currentVersion, err := parseStableVersion(artifact.Version)
	if err != nil {
		return MutationResult{}, newMutationError(MutationErrorConflict, "current_version_invalid")
	}
	targetVersion, err := parseStableVersion(state.TargetVersion)
	if err != nil {
		return MutationResult{}, newMutationError(MutationErrorUnavailable, "recommendation_invalid")
	}

	if phase == MutationPhaseActivate && artifact.Version == request.TargetVersion &&
		artifact.ArtifactID != request.ExpectedActiveArtifactID {
		result.State = string(model.RuntimeOperationSucceeded)
		result.AlreadyApplied = true
		return result, nil
	}
	if artifact.ArtifactID != request.ExpectedActiveArtifactID {
		return MutationResult{}, newMutationError(MutationErrorConflict, "active_artifact_mismatch")
	}
	if compareStableVersions(targetVersion, currentVersion) != 1 {
		return MutationResult{}, newMutationError(MutationErrorConflict, "update_not_actionable")
	}

	expectedType := model.RuntimeOperationPrepareUpdate
	requiredCapability := model.RuntimeCapabilityPrepareUpdate
	if phase == MutationPhaseActivate {
		expectedType = model.RuntimeOperationActivateUpdate
		requiredCapability = model.RuntimeCapabilityActivateUpdate
	}
	if !observed.Capabilities.Supports(requiredCapability) {
		return MutationResult{}, newMutationError(MutationErrorConflict, "runtime_capability_unavailable")
	}

	mutationRequest := model.RuntimeMutationRequest{
		OperationID:               result.RuntimeOperationID,
		ExpectedRuntimeIdentity:   observed.Identity,
		ExpectedRuntimeGeneration: observed.Generation,
	}
	var operation model.RuntimeOperationResult
	if phase == MutationPhasePrepare {
		operation, err = s.runtime.PrepareUpdate(ctx, model.RuntimePrepareUpdateRequest{
			RuntimeMutationRequest:   mutationRequest,
			ExpectedActiveArtifactID: artifact.ArtifactID,
			TargetVersion:            state.TargetVersion,
		})
	} else {
		operation, err = s.runtime.ActivateUpdate(ctx, model.RuntimeActivateUpdateRequest{
			RuntimeMutationRequest:   mutationRequest,
			ExpectedActiveArtifactID: artifact.ArtifactID,
			TargetVersion:            state.TargetVersion,
		})
	}
	if err != nil {
		return mutationRuntimeError(result, err)
	}
	if err := validateMutationOperation(operation, expectedType, mutationRequest); err != nil {
		return MutationResult{}, newMutationError(MutationErrorUnavailable, "runtime_result_unreliable")
	}

	switch operation.State {
	case model.RuntimeOperationSucceeded:
		if operation.Failure != nil {
			return MutationResult{}, newMutationError(MutationErrorUnavailable, "runtime_result_unreliable")
		}
		result.State = string(model.RuntimeOperationSucceeded)
		return result, nil
	case model.RuntimeOperationFailed:
		if operation.Failure == nil || !validFailureCode(operation.Failure.Code) {
			return MutationResult{}, newMutationError(MutationErrorUnavailable, "runtime_result_unreliable")
		}
		result.State = string(model.RuntimeOperationFailed)
		result.FailureCode = operation.Failure.Code
		return result, newMutationError(MutationErrorExecution, operation.Failure.Code)
	default:
		return MutationResult{}, newMutationError(MutationErrorUnavailable, "runtime_result_incomplete")
	}
}

func (s *Service) discoverySnapshot(ctx context.Context) (DiscoveryState, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.load(ctx); err != nil {
		return DiscoveryState{}, time.Time{}, newMutationError(MutationErrorUnavailable, "recommendation_unavailable")
	}
	if s.persistenceFailed {
		return DiscoveryState{}, time.Time{}, newMutationError(MutationErrorUnavailable, "recommendation_unavailable")
	}
	return s.state, s.now(), nil
}

func newMutationResult(phase MutationPhase, request MutationRequest) MutationResult {
	return MutationResult{
		Phase:                    phase,
		RequestID:                request.RequestID,
		TargetVersion:            request.TargetVersion,
		ExpectedActiveArtifactID: string(request.ExpectedActiveArtifactID),
		RuntimeOperationID:       runtimeOperationID(phase, request.RequestID),
	}
}

func runtimeOperationID(phase MutationPhase, requestID string) string {
	digest := sha256.Sum256([]byte(requestID))
	return fmt.Sprintf("manager-cpa-update/v1:%s:%s", phase, hex.EncodeToString(digest[:]))
}

func validateMutationOperation(
	result model.RuntimeOperationResult,
	expectedType model.RuntimeOperationType,
	request model.RuntimeMutationRequest,
) error {
	if err := result.Validate(); err != nil {
		return err
	}
	if result.OperationID != request.OperationID || result.OperationType != expectedType ||
		result.RuntimeIdentity != request.ExpectedRuntimeIdentity {
		return errors.New("Runtime operation result does not match submitted request")
	}
	return nil
}

func mutationRuntimeError(result MutationResult, err error) (MutationResult, error) {
	code, ok := runtimeservice.ErrorCode(err)
	if !ok {
		return MutationResult{}, newMutationError(MutationErrorUnavailable, "runtime_transport_unavailable")
	}
	switch code {
	case runtimeservice.ProtocolErrorRuntimeIdentityMismatch,
		runtimeservice.ProtocolErrorStaleRuntimeGeneration,
		runtimeservice.ProtocolErrorOperationIDConflict,
		runtimeservice.ProtocolErrorOperationStateConflict,
		runtimeservice.ProtocolErrorActiveArtifactUnavailable,
		runtimeservice.ProtocolErrorActiveArtifactMismatch,
		runtimeservice.ProtocolErrorTargetStageUnavailable,
		runtimeservice.ProtocolErrorUnsupportedOperation:
		return MutationResult{}, newMutationError(MutationErrorConflict, string(code))
	case runtimeservice.ProtocolErrorUnsupportedStagingPlatform,
		runtimeservice.ProtocolErrorReleaseMetadataInvalid,
		runtimeservice.ProtocolErrorTargetStageCorrupt:
		result.State = string(model.RuntimeOperationFailed)
		result.FailureCode = string(code)
		return result, newMutationError(MutationErrorExecution, string(code))
	default:
		return MutationResult{}, newMutationError(MutationErrorUnavailable, "runtime_operation_unavailable")
	}
}

func validFailureCode(code string) bool {
	return len(code) > 0 && len(code) <= 64 && failureCodePattern.MatchString(code)
}
