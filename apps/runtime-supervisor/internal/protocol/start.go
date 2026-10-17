package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
)

const (
	// CapabilityStart means typed Start submission is supported, not that the
	// current child state permits it or that the Runtime is ready.
	CapabilityStart = "start"
	startPath       = "/v1/runtime/operations/start"
	maxMutationBody = 16 << 10
)

type StartExecutor interface {
	Start(context.Context, lifecycle.StartRequest) (journal.Operation, error)
}

type operationResponse struct {
	OperationID       string         `json:"operationId"`
	OperationType     string         `json:"operationType"`
	RuntimeIdentity   string         `json:"runtimeIdentity"`
	RuntimeGeneration uint64         `json:"runtimeGeneration"`
	State             journal.State  `json:"state"`
	Error             *protocolError `json:"error,omitempty"`
}

func (h *handler) submitStart(w http.ResponseWriter, r *http.Request) {
	var expectedArtifact artifact.ID
	envelope, err := decodeMutationRequest(w, r, &expectedArtifact)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid Start request")
		return
	}
	if h.start == nil {
		writeError(w, http.StatusBadRequest, "unsupported_operation", "Start is not configured")
		return
	}
	operation, err := h.start.Start(r.Context(), lifecycle.StartRequest{
		OperationID: envelope.OperationID, ExpectedRuntimeIdentity: envelope.ExpectedRuntimeIdentity,
		ExpectedRuntimeGeneration: envelope.ExpectedRuntimeGeneration, ExpectedActiveArtifactID: expectedArtifact,
	})
	if err != nil {
		writeMutationError(w, err, "Start")
		return
	}
	writeOperationResponse(w, operation, "Start")
}

type mutationRequest struct {
	OperationID               string `json:"operationId"`
	ExpectedRuntimeIdentity   string `json:"expectedRuntimeIdentity"`
	ExpectedRuntimeGeneration uint64 `json:"expectedRuntimeGeneration"`
}

func (r mutationRequest) Validate() error {
	return lifecycle.StopRequest(r).Validate()
}

func writeOperationResponse(w http.ResponseWriter, operation journal.Operation, operationName string) {
	response := operationResponse{
		OperationID:       operation.OperationID,
		OperationType:     operation.OperationType,
		RuntimeIdentity:   operation.RuntimeIdentity,
		RuntimeGeneration: operation.RuntimeGeneration,
		State:             operation.State,
	}
	if operation.FailureCode != "" {
		response.Error = &protocolError{Code: operation.FailureCode, Message: operationName + " execution failed"}
	}
	writeJSON(w, http.StatusOK, response)
}

// Only Start supplies the optional exact-artifact sink. Stop/Restart retain
// their original empty payload and reject the field even when its value is empty.
func decodeMutationRequest(w http.ResponseWriter, r *http.Request, startArtifact ...*artifact.ID) (mutationRequest, error) {
	var request mutationRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMutationBody))
	if err != nil || !utf8.Valid(body) {
		return request, lifecycle.ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return request, lifecycle.ErrInvalidRequest
	}
	// Decode this one typed envelope explicitly so unknown, case-aliased and
	// duplicate fields cannot change the meaning of a privileged submission.
	seen := make(map[string]bool, 3)
	for decoder.More() {
		token, err := decoder.Token()
		field, ok := token.(string)
		if err != nil || !ok || seen[field] {
			return request, lifecycle.ErrInvalidRequest
		}
		seen[field] = true
		switch field {
		case "operationId":
			err = decoder.Decode(&request.OperationID)
		case "expectedRuntimeIdentity":
			err = decoder.Decode(&request.ExpectedRuntimeIdentity)
		case "expectedRuntimeGeneration":
			err = decoder.Decode(&request.ExpectedRuntimeGeneration)
		case "expectedActiveArtifactId":
			if len(startArtifact) != 1 {
				return request, lifecycle.ErrInvalidRequest
			}
			value, tokenErr := decoder.Token()
			text, ok := value.(string)
			if tokenErr != nil || !ok || text != "" && !artifact.ID(text).IsValid() {
				return request, lifecycle.ErrInvalidRequest
			}
			*startArtifact[0] = artifact.ID(text)
		default:
			return request, lifecycle.ErrInvalidRequest
		}
		if err != nil {
			return request, lifecycle.ErrInvalidRequest
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return request, lifecycle.ErrInvalidRequest
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return request, lifecycle.ErrInvalidRequest
	}
	return request, request.Validate()
}

func writeMutationError(w http.ResponseWriter, err error, operationName string) {
	code, message, status := "internal_error", operationName+" execution failed", http.StatusInternalServerError
	switch {
	case errors.Is(err, lifecycle.ErrExecutionFailed):
		// Post-spawn/result failures remain internal errors even when their
		// cause is a journal state conflict; retained evidence owns replay.
	case errors.Is(err, lifecycle.ErrPersistenceUnavailable):
		code, message, status = "operation_persistence_unavailable", "operation persistence is unavailable", http.StatusServiceUnavailable
	case errors.Is(err, lifecycle.ErrInvalidRequest):
		code, message, status = "invalid_request", "invalid "+operationName+" request", http.StatusBadRequest
	case errors.Is(err, journal.ErrRuntimeIdentityMismatch):
		code, message, status = "runtime_identity_mismatch", "runtime identity does not match", http.StatusConflict
	case errors.Is(err, journal.ErrStaleRuntimeGeneration):
		code, message, status = "stale_runtime_generation", "runtime generation is stale", http.StatusConflict
	case errors.Is(err, journal.ErrOperationIDConflict):
		code, message, status = "operation_id_conflict", "operation ID names a different request", http.StatusConflict
	case errors.Is(err, journal.ErrOperationStateConflict):
		code, message, status = "operation_state_conflict", "CPA process ownership does not permit "+operationName, http.StatusConflict
	case errors.Is(err, lifecycle.ErrActiveArtifactUnavailable):
		code, message, status = "active_artifact_unavailable", "active artifact identity is unavailable", http.StatusConflict
	case errors.Is(err, lifecycle.ErrActiveArtifactMismatch):
		code, message, status = "active_artifact_mismatch", "active artifact identity does not match", http.StatusConflict
	case errors.Is(err, lifecycle.ErrUnsupportedStaging):
		code, message, status = "unsupported_staging_platform", "update staging is unsupported", http.StatusBadRequest
	case errors.Is(err, lifecycle.ErrReleaseMetadataInvalid):
		code, message, status = "release_metadata_invalid", "official release metadata is unavailable or invalid", http.StatusBadGateway
	case errors.Is(err, lifecycle.ErrUnsupportedActivation):
		code, message, status = "unsupported_operation", "update activation is unsupported", http.StatusBadRequest
	case errors.Is(err, lifecycle.ErrTargetStageUnavailable):
		code, message, status = "target_stage_unavailable", "finalized target stage is unavailable", http.StatusConflict
	case errors.Is(err, lifecycle.ErrTargetStageCorrupt):
		code, message, status = "target_stage_corrupt", "finalized target stage is corrupt", http.StatusConflict
	}
	writeError(w, status, code, message)
}
