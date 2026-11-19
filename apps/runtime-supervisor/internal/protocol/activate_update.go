package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
)

const (
	CapabilityActivateUpdate = "activate_update"
	activateUpdatePath       = "/v1/runtime/operations/activate-update"
	// ActivateUpdateResponseTimeout bounds the HTTP write budget for authenticated
	// activate-update requests beyond candidate readiness, rollback, and terminal persistence.
	ActivateUpdateResponseTimeout = 150 * time.Second
)

type ActivateUpdateExecutor interface {
	ActivateUpdate(context.Context, lifecycle.ActivateUpdateRequest) (journal.Operation, error)
}

type activateUpdateRequest struct {
	OperationID               string      `json:"operationId"`
	ExpectedRuntimeIdentity   string      `json:"expectedRuntimeIdentity"`
	ExpectedRuntimeGeneration uint64      `json:"expectedRuntimeGeneration"`
	ExpectedActiveArtifactID  artifact.ID `json:"expectedActiveArtifactId"`
	TargetVersion             string      `json:"targetVersion"`
}

func (h *handler) submitActivateUpdate(w http.ResponseWriter, r *http.Request) {
	request, err := decodeActivateUpdateRequest(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid activate-update request")
		return
	}
	if h.activateUpdate == nil {
		writeError(w, http.StatusBadRequest, "unsupported_operation", "activate-update is not configured")
		return
	}
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(ActivateUpdateResponseTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		writeError(w, http.StatusInternalServerError, "internal_error", "activate-update response deadline unavailable")
		return
	}
	operation, err := h.activateUpdate.ActivateUpdate(r.Context(), lifecycle.ActivateUpdateRequest{
		OperationID:               request.OperationID,
		ExpectedRuntimeIdentity:   request.ExpectedRuntimeIdentity,
		ExpectedRuntimeGeneration: request.ExpectedRuntimeGeneration,
		ExpectedActiveArtifactID:  request.ExpectedActiveArtifactID,
		TargetVersion:             request.TargetVersion,
	})
	if err != nil {
		writeMutationError(w, err, "activate-update")
		return
	}
	writeOperationResponse(w, operation, "activate-update")
}

func decodeActivateUpdateRequest(w http.ResponseWriter, r *http.Request) (activateUpdateRequest, error) {
	var request activateUpdateRequest
	body, err := io.ReadAll(http.MaxBytesReader(unwrapResponseWriter(w), r.Body, maxMutationBody))
	if err != nil || !utf8.Valid(body) {
		return request, lifecycle.ErrInvalidRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return request, lifecycle.ErrInvalidRequest
	}
	seen := make(map[string]bool, 5)
	for decoder.More() {
		token, tokenErr := decoder.Token()
		field, ok := token.(string)
		if tokenErr != nil || !ok || seen[field] {
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
			err = decoder.Decode(&request.ExpectedActiveArtifactID)
		case "targetVersion":
			err = decoder.Decode(&request.TargetVersion)
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
	typed := lifecycle.ActivateUpdateRequest{
		OperationID:               request.OperationID,
		ExpectedRuntimeIdentity:   request.ExpectedRuntimeIdentity,
		ExpectedRuntimeGeneration: request.ExpectedRuntimeGeneration,
		ExpectedActiveArtifactID:  request.ExpectedActiveArtifactID,
		TargetVersion:             request.TargetVersion,
	}
	if err := typed.Validate(); err != nil {
		return request, err
	}
	return request, nil
}
