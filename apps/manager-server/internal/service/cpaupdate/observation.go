package cpaupdate

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

type ObservationRequest struct {
	RequestID     string `json:"request_id"`
	TargetVersion string `json:"target_version"`
}

func (r ObservationRequest) Validate() error {
	if err := validateOperationIdentity(r.RequestID, r.TargetVersion); err != nil {
		return newObservationError(ObservationErrorInvalid, "invalid_request")
	}
	return nil
}

type ObservationState string

const (
	ObservationStateNotFound  ObservationState = "not_found"
	ObservationStateAccepted  ObservationState = "accepted"
	ObservationStateRunning   ObservationState = "running"
	ObservationStateSucceeded ObservationState = "succeeded"
	ObservationStateFailed    ObservationState = "failed"
)

type ObservationResult struct {
	Phase              MutationPhase    `json:"phase"`
	RequestID          string           `json:"request_id"`
	TargetVersion      string           `json:"target_version"`
	RuntimeOperationID string           `json:"runtime_operation_id"`
	State              ObservationState `json:"state"`
	FailureCode        string           `json:"failure_code,omitempty"`
	CreatedAt          *time.Time       `json:"created_at,omitempty"`
	UpdatedAt          *time.Time       `json:"updated_at,omitempty"`
	CompletedAt        *time.Time       `json:"completed_at,omitempty"`
}

type ObservationErrorKind string

const (
	ObservationErrorInvalid     ObservationErrorKind = "invalid"
	ObservationErrorConflict    ObservationErrorKind = "conflict"
	ObservationErrorUnavailable ObservationErrorKind = "unavailable"
)

type ObservationError struct {
	Kind ObservationErrorKind
	Code string
}

func (e *ObservationError) Error() string {
	switch e.Kind {
	case ObservationErrorInvalid:
		return "invalid CPA update observation request"
	case ObservationErrorConflict:
		return "CPA update observation conflicts with current Runtime authority"
	default:
		return "CPA update observation is unavailable"
	}
}

func ObservationErrorDetails(err error) (ObservationErrorKind, string, bool) {
	var observationErr *ObservationError
	if !errors.As(err, &observationErr) {
		return "", "", false
	}
	return observationErr.Kind, observationErr.Code, true
}

func newObservationError(kind ObservationErrorKind, code string) error {
	return &ObservationError{Kind: kind, Code: code}
}

func (s *Service) ObservePrepare(ctx context.Context, request ObservationRequest) (ObservationResult, error) {
	return s.observe(ctx, MutationPhasePrepare, request)
}

func (s *Service) ObserveActivate(ctx context.Context, request ObservationRequest) (ObservationResult, error) {
	return s.observe(ctx, MutationPhaseActivate, request)
}

func (s *Service) observe(ctx context.Context, phase MutationPhase, request ObservationRequest) (ObservationResult, error) {
	if err := request.Validate(); err != nil {
		return ObservationResult{}, err
	}
	if phase != MutationPhasePrepare && phase != MutationPhaseActivate {
		return ObservationResult{}, newObservationError(ObservationErrorInvalid, "invalid_phase")
	}
	result := ObservationResult{
		Phase:              phase,
		RequestID:          request.RequestID,
		TargetVersion:      request.TargetVersion,
		RuntimeOperationID: runtimeOperationID(phase, request.RequestID),
	}
	if !s.mode.IsValid() {
		return ObservationResult{}, newObservationError(ObservationErrorUnavailable, "runtime_mode_invalid")
	}
	if s.mode != model.RuntimeModeEmbedded {
		return ObservationResult{}, newObservationError(ObservationErrorConflict, "managed_externally")
	}
	if s.runtime == nil {
		return ObservationResult{}, newObservationError(ObservationErrorUnavailable, "runtime_unavailable")
	}

	observed, err := s.runtime.Status(ctx)
	if err != nil {
		return ObservationResult{}, newObservationError(ObservationErrorUnavailable, "runtime_observation_unavailable")
	}
	if err := observed.Validate(); err != nil || strings.TrimSpace(string(observed.Identity)) == "" || observed.Generation == 0 {
		return ObservationResult{}, newObservationError(ObservationErrorUnavailable, "runtime_observation_invalid")
	}
	if !observed.Capabilities.Supports(model.RuntimeCapabilityObserveUpdateOperation) {
		return ObservationResult{}, newObservationError(ObservationErrorConflict, "runtime_capability_unavailable")
	}

	operationType := model.RuntimeOperationPrepareUpdate
	if phase == MutationPhaseActivate {
		operationType = model.RuntimeOperationActivateUpdate
	}
	observationRequest := model.RuntimeObserveUpdateOperationRequest{
		RuntimeMutationRequest: model.RuntimeMutationRequest{
			OperationID:               result.RuntimeOperationID,
			ExpectedRuntimeIdentity:   observed.Identity,
			ExpectedRuntimeGeneration: observed.Generation,
		},
		OperationType: operationType,
		TargetVersion: request.TargetVersion,
	}
	operation, err := s.runtime.ObserveUpdateOperation(ctx, observationRequest)
	if err != nil {
		var protocolErr *runtimeservice.ProtocolError
		if errors.As(err, &protocolErr) && protocolErr.Code == runtimeservice.ProtocolErrorOperationNotFound &&
			protocolErr.HTTPStatus == http.StatusNotFound {
			result.State = ObservationStateNotFound
			return result, nil
		}
		return ObservationResult{}, observationRuntimeError(err)
	}
	if err := operation.Validate(); err != nil || operation.OperationID != observationRequest.OperationID ||
		operation.OperationType != observationRequest.OperationType || operation.RuntimeIdentity != observationRequest.ExpectedRuntimeIdentity {
		return ObservationResult{}, newObservationError(ObservationErrorUnavailable, "runtime_result_unreliable")
	}
	result.State = ObservationState(operation.State)
	createdAt, updatedAt := operation.CreatedAt, operation.UpdatedAt
	result.CreatedAt, result.UpdatedAt = &createdAt, &updatedAt
	if operation.CompletedAt != nil {
		completedAt := *operation.CompletedAt
		result.CompletedAt = &completedAt
	}
	if operation.State == model.RuntimeOperationFailed {
		result.FailureCode = operation.FailureCode
	}
	return result, nil
}

func observationRuntimeError(err error) error {
	if errors.Is(err, runtimeservice.ErrRuntimeObservationUnsupported) {
		return newObservationError(ObservationErrorConflict, "managed_externally")
	}
	code, ok := runtimeservice.ErrorCode(err)
	if !ok {
		return newObservationError(ObservationErrorUnavailable, "runtime_transport_unavailable")
	}
	switch code {
	case runtimeservice.ProtocolErrorRuntimeIdentityMismatch,
		runtimeservice.ProtocolErrorStaleRuntimeGeneration,
		runtimeservice.ProtocolErrorOperationIDConflict,
		runtimeservice.ProtocolErrorUnsupportedOperation:
		return newObservationError(ObservationErrorConflict, string(code))
	default:
		return newObservationError(ObservationErrorUnavailable, "runtime_operation_unavailable")
	}
}
