package protocol

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
)

const maxMutationBodyBytes = 64 << 10

type startRequest struct {
	OperationID               string `json:"operationId"`
	ExpectedRuntimeIdentity   string `json:"expectedRuntimeIdentity"`
	ExpectedRuntimeGeneration uint64 `json:"expectedRuntimeGeneration"`
}

type operationResponse struct {
	OperationID       string         `json:"operationId"`
	OperationType     string         `json:"operationType"`
	RuntimeIdentity   string         `json:"runtimeIdentity"`
	RuntimeGeneration uint64         `json:"runtimeGeneration"`
	State             journal.State  `json:"state"`
	Error             *protocolError `json:"error,omitempty"`
}

func (h *handler) handleStart(w http.ResponseWriter, r *http.Request) {
	var request startRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMutationBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must be one valid JSON object")
		return
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must contain exactly one JSON object")
		return
	}
	start := lifecycle.StartRequest{
		OperationID:               request.OperationID,
		ExpectedRuntimeIdentity:   request.ExpectedRuntimeIdentity,
		ExpectedRuntimeGeneration: request.ExpectedRuntimeGeneration,
	}
	if err := lifecycle.ValidateStartRequest(start); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "start request is invalid")
		return
	}
	if h.start == nil {
		writeError(w, http.StatusBadRequest, "unsupported_operation", "start operation is not configured")
		return
	}

	operation, err := h.start(r.Context(), start)
	if err != nil {
		h.writeStartError(w, err)
		return
	}
	writeOperation(w, http.StatusOK, operation)
}

func (h *handler) writeStartError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, lifecycle.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, "invalid_request", "start request is invalid")
	case errors.Is(err, journal.ErrRuntimeIdentityMismatch):
		writeError(w, http.StatusConflict, "runtime_identity_mismatch", "request targets a different Runtime identity")
	case errors.Is(err, journal.ErrStaleRuntimeGeneration):
		writeError(w, http.StatusConflict, "stale_runtime_generation", "request targets a stale Runtime generation")
	case errors.Is(err, journal.ErrOperationIDConflict):
		writeError(w, http.StatusConflict, "operation_id_conflict", "operation ID is already bound to a different request")
	case errors.Is(err, lifecycle.ErrOperationStateConflict):
		writeError(w, http.StatusConflict, "operation_state_conflict", "CPA process state does not allow Start")
	case errors.Is(err, lifecycle.ErrPersistenceUnavailable):
		writeError(w, http.StatusServiceUnavailable, "operation_persistence_unavailable", "durable operation state is unavailable")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "start operation failed")
	}
}

func writeOperation(w http.ResponseWriter, status int, operation journal.Operation) {
	var resultError *protocolError
	if operation.FailureCode != "" {
		resultError = &protocolError{Code: operation.FailureCode, Message: "operation failed"}
	}
	writeJSON(w, status, operationResponse{
		OperationID:       operation.OperationID,
		OperationType:     operation.OperationType,
		RuntimeIdentity:   operation.RuntimeIdentity,
		RuntimeGeneration: operation.RuntimeGeneration,
		State:             operation.State,
		Error:             resultError,
	})
}
