package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

const (
	CapabilityPrepareUpdate = "prepare_update"
	prepareUpdatePath       = "/v1/runtime/operations/prepare-update"
	// PrepareUpdateUploadPath identifies the typed streaming prepare route.
	PrepareUpdateUploadPath = prepareUpdatePath + "/upload"
	// PrepareUpdateResponseTimeout bounds the HTTP write budget for prepare-update
	// and authenticated prepare-update uploads beyond execution + terminal persistence.
	PrepareUpdateResponseTimeout = 12 * time.Minute
	// PrepareUpdateUploadReadTimeout bounds request-body streaming for authenticated
	// prepare-update uploads that pass synchronous admission checks.
	PrepareUpdateUploadReadTimeout = lifecycle.PrepareUpdateExecutionTimeout
)

type PrepareUpdateExecutor interface {
	PrepareUpdate(context.Context, lifecycle.PrepareUpdateRequest) (journal.Operation, error)
}

type PrepareUpdateUploadExecutor interface {
	PrepareUpdateUpload(context.Context, lifecycle.PrepareUpdateUploadRequest) (journal.Operation, error)
}

type prepareUpdateRequest struct {
	OperationID               string      `json:"operationId"`
	ExpectedRuntimeIdentity   string      `json:"expectedRuntimeIdentity"`
	ExpectedRuntimeGeneration uint64      `json:"expectedRuntimeGeneration"`
	ExpectedActiveArtifactID  artifact.ID `json:"expectedActiveArtifactId"`
	TargetVersion             string      `json:"targetVersion"`
	SourceMode                string      `json:"sourceMode,omitempty"`
	CDNPrefix                 string      `json:"cdnPrefix,omitempty"`
}

func (h *handler) submitPrepareUpdate(w http.ResponseWriter, r *http.Request) {
	request, err := decodePrepareUpdateRequest(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid prepare-update request")
		return
	}
	if h.prepareUpdate == nil {
		writeError(w, http.StatusBadRequest, "unsupported_operation", "prepare-update is not configured")
		return
	}
	controller := http.NewResponseController(w)
	if err := controller.SetWriteDeadline(time.Now().Add(PrepareUpdateResponseTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		writeError(w, http.StatusInternalServerError, "internal_error", "prepare-update response deadline unavailable")
		return
	}
	operation, err := h.prepareUpdate.PrepareUpdate(r.Context(), lifecycle.PrepareUpdateRequest{
		OperationID:               request.OperationID,
		ExpectedRuntimeIdentity:   request.ExpectedRuntimeIdentity,
		ExpectedRuntimeGeneration: request.ExpectedRuntimeGeneration,
		ExpectedActiveArtifactID:  request.ExpectedActiveArtifactID,
		TargetVersion:             request.TargetVersion,
		SourceMode:                request.SourceMode,
		CDNPrefix:                 request.CDNPrefix,
	})
	if err != nil {
		writeMutationError(w, err, "prepare-update")
		return
	}
	writeOperationResponse(w, operation, "prepare-update")
}

func (h *handler) submitPrepareUpdateUpload(w http.ResponseWriter, r *http.Request) {
	if !validPrepareUploadContentType(r.Header.Values("Content-Type")) {
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid prepare-update upload request")
		return
	}
	request, err := decodePrepareUpdateUploadRequest(r)
	if err != nil {
		if errors.Is(err, runtimeupdate.ErrArchiveTooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "archive_too_large", "uploaded archive exceeds size limit")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "invalid prepare-update upload request")
		return
	}
	if h.prepareUpdateUpload == nil {
		writeError(w, http.StatusBadRequest, "unsupported_operation", "prepare-update upload is not configured")
		return
	}
	controller := http.NewResponseController(w)
	now := time.Now()
	if err := controller.SetReadDeadline(now.Add(PrepareUpdateUploadReadTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		writeError(w, http.StatusInternalServerError, "internal_error", "prepare-update upload read deadline unavailable")
		return
	}
	if err := controller.SetWriteDeadline(now.Add(PrepareUpdateResponseTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		restoreUploadReadDeadline(controller, requestBodyConsumed(w))
		writeError(w, http.StatusInternalServerError, "internal_error", "prepare-update response deadline unavailable")
		return
	}
	defer func() {
		restoreUploadReadDeadline(controller, requestBodyConsumed(w))
	}()
	// MaxBytesReader probes one extra byte and signals overflow to staging.
	request.Archive = http.MaxBytesReader(unwrapResponseWriter(w), r.Body, runtimeupdate.MaxAcquisitionArchiveBytes)
	operation, err := h.prepareUpdateUpload.PrepareUpdateUpload(r.Context(), request)
	if err != nil {
		writeMutationError(w, err, "prepare-update")
		return
	}
	writeOperationResponse(w, operation, "prepare-update")
}

func validPrepareUploadContentType(values []string) bool {
	if len(values) == 0 {
		return true
	}
	if len(values) > 1 {
		return false
	}
	trimmed := strings.TrimSpace(values[0])
	if trimmed == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(trimmed)
	if err != nil {
		return false
	}
	switch strings.ToLower(mediaType) {
	case "application/gzip", "application/x-gzip", "application/octet-stream":
		return true
	default:
		return false
	}
}

func decodePrepareUpdateUploadRequest(r *http.Request) (lifecycle.PrepareUpdateUploadRequest, error) {
	if r == nil {
		return lifecycle.PrepareUpdateUploadRequest{}, lifecycle.ErrInvalidRequest
	}
	request := lifecycle.PrepareUpdateUploadRequest{Archive: r.Body, ContentLength: r.ContentLength}
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
	artifactStr, ok := value("expectedActiveArtifactId")
	if !ok {
		return request, lifecycle.ErrInvalidRequest
	}
	request.ExpectedActiveArtifactID = artifact.ID(artifactStr)
	if request.TargetVersion, ok = value("targetVersion"); !ok {
		return request, lifecycle.ErrInvalidRequest
	}
	if err := request.Validate(); err != nil {
		return request, err
	}
	return request, nil
}

func decodePrepareUpdateRequest(w http.ResponseWriter, r *http.Request) (prepareUpdateRequest, error) {
	var request prepareUpdateRequest
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
		case "sourceMode":
			err = decoder.Decode(&request.SourceMode)
		case "cdnPrefix":
			err = decoder.Decode(&request.CDNPrefix)
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
	typed := lifecycle.PrepareUpdateRequest{
		OperationID:               request.OperationID,
		ExpectedRuntimeIdentity:   request.ExpectedRuntimeIdentity,
		ExpectedRuntimeGeneration: request.ExpectedRuntimeGeneration,
		ExpectedActiveArtifactID:  request.ExpectedActiveArtifactID,
		TargetVersion:             request.TargetVersion,
		SourceMode:                request.SourceMode,
		CDNPrefix:                 request.CDNPrefix,
	}
	if err := typed.Validate(); err != nil {
		return request, err
	}
	return request, nil
}
