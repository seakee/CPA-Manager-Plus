package protocol

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
)

const (
	CapabilityObserveUpdateOperation = "observe_update_operation"
	observeUpdateOperationPath       = "/v1/runtime/operations/update"
)

type UpdateOperationObserver interface {
	ObserveUpdateOperation(context.Context, lifecycle.ObserveUpdateOperationRequest) (journal.Operation, error)
}

type observedOperationResponse struct {
	OperationID       string         `json:"operationId"`
	OperationType     string         `json:"operationType"`
	RuntimeIdentity   string         `json:"runtimeIdentity"`
	RuntimeGeneration uint64         `json:"runtimeGeneration"`
	State             journal.State  `json:"state"`
	Error             *protocolError `json:"error,omitempty"`
	CreatedAt         time.Time      `json:"createdAt"`
	UpdatedAt         time.Time      `json:"updatedAt"`
	CompletedAt       *time.Time     `json:"completedAt,omitempty"`
}

func (h *handler) serveObserveUpdateOperation(w http.ResponseWriter, r *http.Request) {
	request, err := decodeObserveUpdateOperationRequest(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid update operation observation request")
		return
	}
	if h.updateOperationObserver == nil {
		writeError(w, http.StatusBadRequest, "unsupported_operation", "update operation observation is not configured")
		return
	}
	operation, err := h.updateOperationObserver.ObserveUpdateOperation(r.Context(), request)
	if err != nil {
		writeObservationError(w, err)
		return
	}
	response := observedOperationResponse{
		OperationID:       operation.OperationID,
		OperationType:     operation.OperationType,
		RuntimeIdentity:   operation.RuntimeIdentity,
		RuntimeGeneration: operation.RuntimeGeneration,
		State:             operation.State,
		CreatedAt:         operation.CreatedAt,
		UpdatedAt:         operation.UpdatedAt,
		CompletedAt:       operation.CompletedAt,
	}
	if operation.FailureCode != "" {
		response.Error = &protocolError{Code: operation.FailureCode, Message: "update operation failed"}
	}
	writeJSON(w, http.StatusOK, response)
}

func decodeObserveUpdateOperationRequest(w http.ResponseWriter, r *http.Request) (lifecycle.ObserveUpdateOperationRequest, error) {
	var request lifecycle.ObserveUpdateOperationRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 0))
	if err != nil || len(body) != 0 {
		return request, lifecycle.ErrInvalidRequest
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 5 {
		return request, lifecycle.ErrInvalidRequest
	}
	value := func(name string) (string, bool) {
		values, ok := query[name]
		return firstExactValue(values, ok)
	}
	var ok bool
	if request.OperationID, ok = value("operationId"); !ok {
		return request, lifecycle.ErrInvalidRequest
	}
	if request.ExpectedRuntimeIdentity, ok = value("expectedRuntimeIdentity"); !ok {
		return request, lifecycle.ErrInvalidRequest
	}
	generation, ok := value("expectedRuntimeGeneration")
	if !ok {
		return request, lifecycle.ErrInvalidRequest
	}
	parsedGeneration, err := strconv.ParseUint(generation, 10, 64)
	if err != nil {
		return request, lifecycle.ErrInvalidRequest
	}
	request.ExpectedRuntimeGeneration = parsedGeneration
	if request.OperationType, ok = value("operationType"); !ok {
		return request, lifecycle.ErrInvalidRequest
	}
	if request.TargetVersion, ok = value("targetVersion"); !ok {
		return request, lifecycle.ErrInvalidRequest
	}
	return request, request.Validate()
}

func firstExactValue(values []string, exists bool) (string, bool) {
	if !exists || len(values) != 1 || values[0] == "" {
		return "", false
	}
	return values[0], true
}

func writeObservationError(w http.ResponseWriter, err error) {
	code, message, status := "internal_error", "update operation observation failed", http.StatusInternalServerError
	switch {
	case errors.Is(err, lifecycle.ErrPersistenceUnavailable):
		code, message, status = "operation_persistence_unavailable", "operation persistence is unavailable", http.StatusServiceUnavailable
	case errors.Is(err, lifecycle.ErrInvalidRequest):
		code, message, status = "invalid_request", "invalid update operation observation request", http.StatusBadRequest
	case errors.Is(err, journal.ErrOperationNotFound):
		code, message, status = "operation_not_found", "operation was not found", http.StatusNotFound
	case errors.Is(err, journal.ErrRuntimeIdentityMismatch):
		code, message, status = "runtime_identity_mismatch", "runtime identity does not match", http.StatusConflict
	case errors.Is(err, journal.ErrStaleRuntimeGeneration):
		code, message, status = "stale_runtime_generation", "runtime generation is stale", http.StatusConflict
	case errors.Is(err, journal.ErrOperationIDConflict):
		code, message, status = "operation_id_conflict", "operation ID names a different request", http.StatusConflict
	}
	writeError(w, status, code, message)
}
