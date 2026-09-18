package cpaupdate

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	cpaupdateservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaupdate"
)

const routePrefix = "/usage-service/runtime/updates"

const maxMutationBodyBytes = 512

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}

	suffix := strings.TrimPrefix(r.URL.Path, routePrefix)
	isStatus := suffix == "" && r.Method == http.MethodGet
	isCheck := suffix == "/check" && r.Method == http.MethodPost
	isPrepare := suffix == "/prepare" && r.Method == http.MethodPost
	isActivate := suffix == "/activate" && r.Method == http.MethodPost
	if !isStatus && !isCheck && !isPrepare && !isActivate {
		response.MethodNotAllowed(w)
		return
	}
	if (isStatus || isCheck) && !emptyRequestBody(w, r) {
		response.Error(w, http.StatusBadRequest, errors.New("request body must be empty"))
		return
	}
	service := h.App.CPAUpdateService
	if service == nil {
		response.Error(w, http.StatusServiceUnavailable, errors.New("CPA update state unavailable"))
		return
	}
	if isPrepare || isActivate {
		request, err := decodeMutationRequest(w, r)
		if err != nil {
			writeMutationError(w, cpaupdateservice.MutationResult{}, err)
			return
		}
		var result cpaupdateservice.MutationResult
		if isPrepare {
			result, err = service.Prepare(r.Context(), request)
		} else {
			result, err = service.Activate(r.Context(), request)
		}
		if err != nil {
			writeMutationError(w, result, err)
			return
		}
		response.JSON(w, http.StatusOK, result)
		return
	}

	var (
		payload any
		err     error
	)
	if suffix == "" {
		payload, err = service.Status(r.Context())
	} else {
		payload, err = service.Check(r.Context())
	}
	if err != nil {
		response.Error(w, http.StatusServiceUnavailable, errors.New("CPA update state unavailable"))
		return
	}
	response.JSON(w, http.StatusOK, payload)
}

func emptyRequestBody(w http.ResponseWriter, r *http.Request) bool {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 0))
	return err == nil && len(data) == 0
}

func decodeMutationRequest(w http.ResponseWriter, r *http.Request) (cpaupdateservice.MutationRequest, error) {
	var request cpaupdateservice.MutationRequest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMutationBodyBytes))
	if err != nil || !utf8.Valid(body) {
		return request, &cpaupdateservice.MutationError{Kind: cpaupdateservice.MutationErrorInvalid, Code: "invalid_request"}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return request, &cpaupdateservice.MutationError{Kind: cpaupdateservice.MutationErrorInvalid, Code: "invalid_request"}
	}
	seen := make(map[string]bool, 3)
	for decoder.More() {
		token, tokenErr := decoder.Token()
		field, ok := token.(string)
		if tokenErr != nil || !ok || seen[field] {
			return request, &cpaupdateservice.MutationError{Kind: cpaupdateservice.MutationErrorInvalid, Code: "invalid_request"}
		}
		seen[field] = true
		switch field {
		case "request_id":
			err = decoder.Decode(&request.RequestID)
		case "target_version":
			err = decoder.Decode(&request.TargetVersion)
		case "expected_active_artifact_id":
			var artifactID string
			err = decoder.Decode(&artifactID)
			request.ExpectedActiveArtifactID = model.RuntimeArtifactID(artifactID)
		default:
			return request, &cpaupdateservice.MutationError{Kind: cpaupdateservice.MutationErrorInvalid, Code: "invalid_request"}
		}
		if err != nil {
			return request, &cpaupdateservice.MutationError{Kind: cpaupdateservice.MutationErrorInvalid, Code: "invalid_request"}
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return request, &cpaupdateservice.MutationError{Kind: cpaupdateservice.MutationErrorInvalid, Code: "invalid_request"}
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return request, &cpaupdateservice.MutationError{Kind: cpaupdateservice.MutationErrorInvalid, Code: "invalid_request"}
	}
	if err := request.Validate(); err != nil {
		return request, err
	}
	return request, nil
}

func writeMutationError(w http.ResponseWriter, result cpaupdateservice.MutationResult, err error) {
	kind, code, ok := cpaupdateservice.MutationErrorDetails(err)
	if !ok {
		kind = cpaupdateservice.MutationErrorUnavailable
		code = "update_unavailable"
	}
	status := http.StatusServiceUnavailable
	switch kind {
	case cpaupdateservice.MutationErrorInvalid:
		status = http.StatusBadRequest
	case cpaupdateservice.MutationErrorConflict:
		status = http.StatusConflict
	case cpaupdateservice.MutationErrorExecution:
		status = http.StatusBadGateway
	}
	if kind == cpaupdateservice.MutationErrorExecution && result.Phase != "" {
		response.JSON(w, status, result)
		return
	}
	response.JSON(w, status, map[string]string{
		"error": (&cpaupdateservice.MutationError{Kind: kind, Code: code}).Error(),
		"code":  code,
	})
}
