package cpaupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	SourceMode               string                  `json:"source_mode,omitempty"`
	CDNPrefix                string                  `json:"cdn_prefix,omitempty"`
}

func (r MutationRequest) Validate() error {
	if _, err := parseSupportedUpdateVersion(r.TargetVersion); errors.Is(err, errUnsupportedCPAMajor) {
		return newMutationError(MutationErrorInvalid, "unsupported_cpa_major")
	}
	if err := validateOperationIdentity(r.RequestID, r.TargetVersion); err != nil {
		return newMutationError(MutationErrorInvalid, "invalid_request")
	}
	if !r.ExpectedActiveArtifactID.IsValid() {
		return newMutationError(MutationErrorInvalid, "invalid_request")
	}
	switch r.SourceMode {
	case "", "github":
		if strings.TrimSpace(r.CDNPrefix) != "" {
			return newMutationError(MutationErrorInvalid, "invalid_request")
		}
	case "cdn":
		if !validMutationCDNPrefix(r.CDNPrefix) {
			return newMutationError(MutationErrorInvalid, "invalid_request")
		}
	default:
		return newMutationError(MutationErrorInvalid, "invalid_request")
	}
	return nil
}

func validateOperationIdentity(requestID, targetVersion string) error {
	if len(requestID) == 0 || len([]byte(requestID)) > maxMutationRequestIDBytes ||
		!mutationRequestIDPattern.MatchString(requestID) {
		return errors.New("invalid request ID")
	}
	if _, err := parseStableVersion(targetVersion); err != nil {
		return errors.New("invalid target version")
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
	MutationErrorTooLarge    MutationErrorKind = "too_large"
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
	case MutationErrorTooLarge:
		return "uploaded archive exceeds size limit"
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
	return s.mutate(ctx, MutationPhasePrepare, request, nil, 0)
}

func (s *Service) PrepareUpload(ctx context.Context, request MutationRequest, archive io.Reader, contentLength int64) (MutationResult, error) {
	if archive == nil || strings.TrimSpace(request.SourceMode) != "" || strings.TrimSpace(request.CDNPrefix) != "" {
		return MutationResult{}, newMutationError(MutationErrorInvalid, "invalid_request")
	}
	return s.mutate(ctx, MutationPhasePrepare, request, archive, contentLength)
}

func (s *Service) Activate(ctx context.Context, request MutationRequest) (MutationResult, error) {
	if strings.TrimSpace(request.SourceMode) != "" || strings.TrimSpace(request.CDNPrefix) != "" {
		return MutationResult{}, newMutationError(MutationErrorInvalid, "invalid_request")
	}
	if err := request.Validate(); err != nil {
		return MutationResult{}, err
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	return s.mutate(ctx, MutationPhaseActivate, request, nil, 0)
}

func (s *Service) mutate(ctx context.Context, phase MutationPhase, request MutationRequest, uploadArchive io.Reader, uploadLength int64) (MutationResult, error) {
	if err := request.Validate(); err != nil {
		return MutationResult{}, err
	}
	if phase != MutationPhasePrepare && phase != MutationPhaseActivate {
		return MutationResult{}, newMutationError(MutationErrorInvalid, "invalid_phase")
	}
	result := newMutationResult(phase, request)

	mode := s.currentMode()
	if !mode.IsValid() {
		return MutationResult{}, newMutationError(MutationErrorUnavailable, "runtime_mode_invalid")
	}
	if mode != model.RuntimeModeEmbedded {
		return MutationResult{}, newMutationError(MutationErrorConflict, "managed_externally")
	}
	if s.isProductComplete == nil {
		return MutationResult{}, newMutationError(MutationErrorUnavailable, "product_completion_unavailable")
	}
	complete, err := s.isProductComplete(ctx)
	if err != nil {
		return MutationResult{}, newMutationError(MutationErrorUnavailable, "product_completion_unavailable")
	}
	if !complete {
		return MutationResult{}, newMutationError(MutationErrorConflict, "product_incomplete")
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
	targetVersion, err := parseSupportedUpdateVersion(state.TargetVersion)
	if err != nil {
		return MutationResult{}, newMutationError(MutationErrorUnavailable, "recommendation_invalid")
	}

	// A retry is sent with the original active-artifact fence. Fast-path an
	// activation already confirmed by this Service instance without taking
	// another write RPC; after a Manager restart, fall back to Supervisor's
	// persistent activation-intent verification once and cache the receipt.
	replay := phase == MutationPhaseActivate && artifact.Version == request.TargetVersion &&
		artifact.ArtifactID != request.ExpectedActiveArtifactID
	if replay {
		if receipt, ok := s.matchAppliedActivation(
			result.RuntimeOperationID,
			observed.Identity,
			observed.Generation,
			request.ExpectedActiveArtifactID,
			state.TargetVersion,
			artifact.ArtifactID,
		); ok {
			if err := s.verifyObservedActivationOperation(
				ctx,
				result.RuntimeOperationID,
				observed.Identity,
				observed.Generation,
				receipt.operationGeneration,
				state.TargetVersion,
			); err != nil {
				s.deleteAppliedActivation(result.RuntimeOperationID)
				var mutErr *MutationError
				if errors.As(err, &mutErr) {
					return MutationResult{}, err
				}
				return mutationRuntimeError(err)
			}
			result.State = string(model.RuntimeOperationSucceeded)
			result.AlreadyApplied = true
			return result, nil
		}
	}
	if !replay && artifact.ArtifactID != request.ExpectedActiveArtifactID {
		return MutationResult{}, newMutationError(MutationErrorConflict, "active_artifact_mismatch")
	}
	if !replay && compareStableVersions(targetVersion, currentVersion) != 1 {
		return MutationResult{}, newMutationError(MutationErrorConflict, "update_not_actionable")
	}

	prepareSupported := observed.Capabilities.Supports(model.RuntimeCapabilityPrepareUpdate)
	activateSupported := observed.Capabilities.Supports(model.RuntimeCapabilityActivateUpdate)
	if !replay && (!prepareSupported || !activateSupported) {
		return MutationResult{}, newMutationError(MutationErrorConflict, "runtime_capability_unavailable")
	}

	expectedType := model.RuntimeOperationPrepareUpdate
	if phase == MutationPhaseActivate {
		expectedType = model.RuntimeOperationActivateUpdate
	}

	mutationRequest := model.RuntimeMutationRequest{
		OperationID:               result.RuntimeOperationID,
		ExpectedRuntimeIdentity:   observed.Identity,
		ExpectedRuntimeGeneration: observed.Generation,
	}
	var operation model.RuntimeOperationResult
	if phase == MutationPhasePrepare {
		prepareReq := model.RuntimePrepareUpdateRequest{
			RuntimeMutationRequest:   mutationRequest,
			ExpectedActiveArtifactID: artifact.ArtifactID,
			TargetVersion:            state.TargetVersion,
			SourceMode:               request.SourceMode,
			CDNPrefix:                strings.TrimSpace(request.CDNPrefix),
		}
		if uploadArchive != nil {
			uploader, ok := s.runtime.(runtimeservice.PrepareUpdateUploader)
			if !ok {
				return MutationResult{}, newMutationError(MutationErrorConflict, "runtime_capability_unavailable")
			}
			operation, err = uploader.PrepareUpdateUpload(ctx, prepareReq, uploadArchive, uploadLength)
		} else {
			operation, err = s.runtime.PrepareUpdate(ctx, prepareReq)
		}
	} else {
		operation, err = s.runtime.ActivateUpdate(ctx, model.RuntimeActivateUpdateRequest{
			RuntimeMutationRequest:   mutationRequest,
			ExpectedActiveArtifactID: request.ExpectedActiveArtifactID,
			TargetVersion:            state.TargetVersion,
		})
	}
	if err != nil {
		return mutationRuntimeError(err)
	}
	if err := validateMutationOperation(operation, expectedType, mutationRequest); err != nil {
		return MutationResult{}, newMutationError(MutationErrorUnavailable, "runtime_result_unreliable")
	}

	switch operation.State {
	case model.RuntimeOperationSucceeded:
		if operation.Failure != nil {
			return MutationResult{}, newMutationError(MutationErrorUnavailable, "runtime_result_unreliable")
		}
		if phase == MutationPhaseActivate {
			appliedID := model.RuntimeArtifactID("")
			operationGen := model.RuntimeGeneration(0)
			if replay {
				if artifact.ArtifactID.IsValid() &&
					s.verifyObservedActivationOperation(
						ctx,
						result.RuntimeOperationID,
						observed.Identity,
						observed.Generation,
						operation.RuntimeGeneration,
						state.TargetVersion,
					) == nil {
					appliedID = artifact.ArtifactID
					operationGen = operation.RuntimeGeneration
				}
			} else if operation.RuntimeGeneration == observed.Generation {
				if postStatus, postErr := s.runtime.Status(ctx); postErr == nil &&
					postStatus.Validate() == nil &&
					postStatus.Identity == observed.Identity &&
					postStatus.Generation == observed.Generation &&
					postStatus.State == model.RuntimeStateReady &&
					postStatus.ActiveGatewayArtifact != nil &&
					postStatus.ActiveGatewayArtifact.Version == state.TargetVersion &&
					postStatus.ActiveGatewayArtifact.ArtifactID.IsValid() &&
					postStatus.ActiveGatewayArtifact.ArtifactID != request.ExpectedActiveArtifactID &&
					s.verifyObservedActivationOperation(
						ctx,
						result.RuntimeOperationID,
						observed.Identity,
						observed.Generation,
						observed.Generation,
						state.TargetVersion,
					) == nil {
					appliedID = postStatus.ActiveGatewayArtifact.ArtifactID
					operationGen = observed.Generation
				}
			}
			if appliedID != "" {
				s.recordAppliedActivation(appliedActivationReceipt{
					operationID:         result.RuntimeOperationID,
					runtimeIdentity:     observed.Identity,
					runtimeGeneration:   observed.Generation,
					operationGeneration: operationGen,
					previousArtifactID:  request.ExpectedActiveArtifactID,
					targetVersion:       state.TargetVersion,
					appliedArtifactID:   appliedID,
				})
			}
		}
		result.State = string(model.RuntimeOperationSucceeded)
		result.AlreadyApplied = replay
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

func (s *Service) recordAppliedActivation(receipt appliedActivationReceipt) {
	if receipt.appliedArtifactID == "" || !receipt.appliedArtifactID.IsValid() ||
		receipt.runtimeGeneration == 0 || receipt.operationGeneration == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appliedActivations == nil {
		s.appliedActivations = make(map[string]appliedActivationReceipt)
	}
	filtered := s.appliedActivationOrder[:0]
	for _, opID := range s.appliedActivationOrder {
		existing, ok := s.appliedActivations[opID]
		if !ok {
			continue
		}
		if existing.runtimeIdentity != receipt.runtimeIdentity || existing.runtimeGeneration != receipt.runtimeGeneration {
			delete(s.appliedActivations, opID)
			continue
		}
		if opID == receipt.operationID {
			continue
		}
		filtered = append(filtered, opID)
	}
	for i := len(filtered); i < len(s.appliedActivationOrder); i++ {
		s.appliedActivationOrder[i] = ""
	}
	s.appliedActivationOrder = filtered
	for opID, existing := range s.appliedActivations {
		if existing.runtimeIdentity != receipt.runtimeIdentity || existing.runtimeGeneration != receipt.runtimeGeneration {
			delete(s.appliedActivations, opID)
		}
	}
	for len(s.appliedActivationOrder) >= maxAppliedActivationReceipts {
		oldest := s.appliedActivationOrder[0]
		delete(s.appliedActivations, oldest)
		copy(s.appliedActivationOrder, s.appliedActivationOrder[1:])
		s.appliedActivationOrder[len(s.appliedActivationOrder)-1] = ""
		s.appliedActivationOrder = s.appliedActivationOrder[:len(s.appliedActivationOrder)-1]
	}
	s.appliedActivations[receipt.operationID] = receipt
	s.appliedActivationOrder = append(s.appliedActivationOrder, receipt.operationID)
}

func (s *Service) matchAppliedActivation(
	operationID string,
	identity model.RuntimeIdentity,
	generation model.RuntimeGeneration,
	previousArtifactID model.RuntimeArtifactID,
	targetVersion string,
	currentArtifactID model.RuntimeArtifactID,
) (appliedActivationReceipt, bool) {
	if currentArtifactID == "" || !currentArtifactID.IsValid() || generation == 0 {
		return appliedActivationReceipt{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	receipt, ok := s.appliedActivations[operationID]
	if !ok {
		return appliedActivationReceipt{}, false
	}
	if receipt.runtimeIdentity != identity ||
		receipt.runtimeGeneration != generation ||
		receipt.operationGeneration == 0 ||
		receipt.previousArtifactID != previousArtifactID ||
		receipt.targetVersion != targetVersion ||
		receipt.appliedArtifactID == "" ||
		receipt.appliedArtifactID != currentArtifactID {
		return appliedActivationReceipt{}, false
	}
	return receipt, true
}

func (s *Service) verifyObservedActivationOperation(
	ctx context.Context,
	operationID string,
	identity model.RuntimeIdentity,
	currentGeneration model.RuntimeGeneration,
	wantOperationGeneration model.RuntimeGeneration,
	targetVersion string,
) error {
	obs, err := s.runtime.ObserveUpdateOperation(ctx, model.RuntimeObserveUpdateOperationRequest{
		RuntimeMutationRequest: model.RuntimeMutationRequest{
			OperationID:               operationID,
			ExpectedRuntimeIdentity:   identity,
			ExpectedRuntimeGeneration: currentGeneration,
		},
		OperationType: model.RuntimeOperationActivateUpdate,
		TargetVersion: targetVersion,
	})
	if err != nil {
		return err
	}
	if err := obs.Validate(); err != nil ||
		obs.OperationID != operationID ||
		obs.OperationType != model.RuntimeOperationActivateUpdate ||
		obs.RuntimeIdentity != identity ||
		obs.RuntimeGeneration != wantOperationGeneration ||
		obs.State != model.RuntimeOperationSucceeded ||
		obs.FailureCode != "" {
		return newMutationError(MutationErrorUnavailable, "runtime_result_unreliable")
	}
	return nil
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

func mutationRuntimeError(err error) (MutationResult, error) {
	code, ok := runtimeservice.ErrorCode(err)
	if !ok {
		return MutationResult{}, newMutationError(MutationErrorUnavailable, "runtime_transport_unavailable")
	}
	switch code {
	case runtimeservice.ProtocolErrorInvalidRequest:
		return MutationResult{}, newMutationError(MutationErrorInvalid, string(code))
	case runtimeservice.ProtocolErrorArchiveTooLarge:
		return MutationResult{}, newMutationError(MutationErrorTooLarge, string(code))
	case runtimeservice.ProtocolErrorRuntimeIdentityMismatch,
		runtimeservice.ProtocolErrorStaleRuntimeGeneration,
		runtimeservice.ProtocolErrorOperationIDConflict,
		runtimeservice.ProtocolErrorOperationStateConflict,
		runtimeservice.ProtocolErrorActiveArtifactUnavailable,
		runtimeservice.ProtocolErrorActiveArtifactMismatch,
		runtimeservice.ProtocolErrorTargetStageUnavailable,
		runtimeservice.ProtocolErrorUnsupportedOperation,
		runtimeservice.ProtocolErrorUnsupportedStagingPlatform,
		runtimeservice.ProtocolErrorTargetStageCorrupt:
		return MutationResult{}, newMutationError(MutationErrorConflict, string(code))
	case runtimeservice.ProtocolErrorReleaseMetadataInvalid:
		return MutationResult{}, newMutationError(MutationErrorExecution, string(code))
	default:
		return MutationResult{}, newMutationError(MutationErrorUnavailable, "runtime_operation_unavailable")
	}
}

func validFailureCode(code string) bool {
	return len(code) > 0 && len(code) <= 64 && failureCodePattern.MatchString(code)
}
