package usage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	usagesvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/usage"
)

const maxUsageImportBytes int64 = 64 * 1024 * 1024
const maxUsageImportSessionCreateBytes int64 = 64 * 1024
const maxUsageArchiveRequestBytes int64 = 64 * 1024
const usageImportSessionsPath = "/v0/management/usage/import-sessions"
const usageArchivesPath = "/v0/management/usage/archives"
const usageMaintenancePath = "/v0/management/usage/maintenance"

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	cleanPath := strings.TrimRight(r.URL.Path, "/")
	if strings.HasPrefix(cleanPath, usageMaintenancePath) {
		if !middleware.AuthorizeAdmin(w, r, h.App.AdminAuthService) {
			return
		}
		if cleanPath != usageMaintenancePath {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodHead:
			w.WriteHeader(http.StatusNoContent)
		case http.MethodGet:
			status, err := h.App.UsageService.MaintenanceStatus(r.Context())
			if err != nil {
				writeArchiveError(w, err)
				return
			}
			response.JSON(w, http.StatusOK, status)
		default:
			response.MethodNotAllowed(w)
		}
		return
	}
	if strings.HasPrefix(cleanPath, usageArchivesPath) {
		if !middleware.AuthorizeAdmin(w, r, h.App.AdminAuthService) {
			return
		}
		if _, _, ok := parseArchivePath(r.URL.Path); !ok {
			http.NotFound(w, r)
			return
		}
		h.handleArchive(w, r)
		return
	}
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	if id, action, ok := parseImportSessionPath(r.URL.Path); ok {
		h.handleImportSession(w, r, id, action)
		return
	}
	if strings.HasPrefix(cleanPath, usageImportSessionsPath+"/") {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		if strings.HasSuffix(r.URL.Path, "/export") {
			h.Export(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		writer := &countingWriter{writer: w}
		err := h.App.UsageService.WriteCompatibleUsage(r.Context(), writer, h.App.Config.QueryLimit)
		if err != nil {
			if writer.written == 0 {
				response.Error(w, http.StatusInternalServerError, err)
			} else {
				log.Printf("usage compatible stream failed after %d bytes: %v", writer.written, err)
			}
			return
		}
	case http.MethodPost:
		if strings.HasSuffix(r.URL.Path, "/import") {
			h.Import(w, r)
			return
		}
		response.MethodNotAllowed(w)
	default:
		response.MethodNotAllowed(w)
	}
}

type archiveCutoffRequest struct {
	CutoffTimestampMS int64 `json:"cutoff_timestamp_ms"`
}

func (h *Handler) handleArchive(w http.ResponseWriter, r *http.Request) {
	id, action, ok := parseArchivePath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if id == "" && action == "preview" && r.Method == http.MethodPost {
		var request archiveCutoffRequest
		if err := decodeSingleJSON(w, r, &request); err != nil {
			writeArchiveRequestError(w, err)
			return
		}
		preview, err := h.App.UsageService.PreviewArchive(r.Context(), request.CutoffTimestampMS)
		if err != nil {
			writeArchiveError(w, err)
			return
		}
		response.JSON(w, http.StatusOK, preview)
		return
	}
	if id == "" && action == "" && r.Method == http.MethodPost {
		var request archiveCutoffRequest
		if err := decodeSingleJSON(w, r, &request); err != nil {
			writeArchiveRequestError(w, err)
			return
		}
		status, err := h.App.UsageService.CreateArchive(r.Context(), request.CutoffTimestampMS)
		if err != nil {
			writeArchiveError(w, err)
			return
		}
		response.JSON(w, http.StatusCreated, usagesvc.NewArchiveStatusSummary(status))
		return
	}
	if id == "" && action == "" && r.Method == http.MethodGet {
		options, err := parseArchiveListOptions(r)
		if err != nil {
			writeArchiveError(w, err)
			return
		}
		list, err := h.App.UsageService.ListArchivePage(r.Context(), options)
		if err != nil {
			writeArchiveError(w, err)
			return
		}
		response.JSON(w, http.StatusOK, list)
		return
	}
	if id != "" && action == "" && r.Method == http.MethodGet {
		status, err := h.App.UsageService.ArchiveStatus(r.Context(), id)
		if err != nil {
			writeArchiveError(w, err)
			return
		}
		response.JSON(w, http.StatusOK, usagesvc.NewArchiveStatusSummary(status))
		return
	}
	if id != "" && action != "" && r.Method == http.MethodPost {
		if err := validateEmptyArchiveBody(w, r); err != nil {
			writeArchiveRequestError(w, err)
			return
		}
		wait, err := parseArchiveWait(r)
		if err != nil {
			writeArchiveRequestError(w, err)
			return
		}
		var (
			status usagesvc.ArchiveStatus
			queued bool
		)
		switch action {
		case "resume":
			expectedStage := strings.TrimSpace(r.URL.Query().Get("expected_stage"))
			status, queued, err = h.App.UsageService.SubmitArchiveResume(r.Context(), id, expectedStage, wait)
		case "verify":
			status, queued, err = h.App.UsageService.SubmitArchiveVerification(r.Context(), id, wait)
		case "delete":
			status, queued, err = h.App.UsageService.SubmitArchiveDeletion(r.Context(), id, wait)
		default:
			response.MethodNotAllowed(w)
			return
		}
		if err != nil {
			writeArchiveError(w, err)
			return
		}
		responseStatus := http.StatusOK
		if queued && !wait {
			responseStatus = http.StatusAccepted
			w.Header().Set("Location", usageArchivesPath+"/"+id)
			w.Header().Set("Retry-After", "2")
		}
		response.JSON(w, responseStatus, usagesvc.NewArchiveStatusSummary(status))
		return
	}
	response.MethodNotAllowed(w)
}

func parseArchiveWait(r *http.Request) (bool, error) {
	background := strings.TrimSpace(r.URL.Query().Get("background"))
	wait := strings.TrimSpace(r.URL.Query().Get("wait"))
	if background != "" && wait != "" {
		return false, errors.New("usage archive action must not set both background and wait")
	}
	if background != "" {
		value, err := strconv.ParseBool(background)
		if err != nil {
			return false, errors.New("usage archive background flag is invalid")
		}
		return !value, nil
	}
	if wait != "" {
		value, err := strconv.ParseBool(wait)
		if err != nil {
			return false, errors.New("usage archive wait flag is invalid")
		}
		return value, nil
	}
	return true, nil
}

func parseArchiveListOptions(r *http.Request) (usagesvc.ArchiveListOptions, error) {
	limit, err := parseArchiveLimit(r.URL.Query().Get("limit"))
	if err != nil {
		return usagesvc.ArchiveListOptions{}, err
	}
	return usagesvc.ArchiveListOptions{
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Mode:   strings.TrimSpace(r.URL.Query().Get("mode")),
		Limit:  limit,
		Cursor: strings.TrimSpace(r.URL.Query().Get("cursor")),
	}, nil
}

func parseArchiveLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 20, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 || limit > 100 {
		return 0, fmt.Errorf("%w: limit must be between 1 and 100", usagesvc.ErrArchiveInvalidRequest)
	}
	return limit, nil
}

func parseArchivePath(path string) (id string, action string, ok bool) {
	clean := strings.TrimRight(path, "/")
	if clean == usageArchivesPath {
		return "", "", true
	}
	if clean == usageArchivesPath+"/preview" {
		return "", "preview", true
	}
	if !strings.HasPrefix(clean, usageArchivesPath+"/") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(clean, usageArchivesPath+"/"), "/")
	if len(parts) == 1 && parts[0] != "" {
		return parts[0], "", true
	}
	if len(parts) == 2 && parts[0] != "" && (parts[1] == "resume" || parts[1] == "verify" || parts[1] == "delete") {
		return parts[0], parts[1], true
	}
	return "", "", false
}

func decodeSingleJSON(w http.ResponseWriter, r *http.Request, target any) error {
	if r.ContentLength > maxUsageArchiveRequestBytes {
		return &http.MaxBytesError{Limit: maxUsageArchiveRequestBytes}
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUsageArchiveRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("request contains multiple JSON values")
		}
		return err
	}
	return nil
}

func validateEmptyArchiveBody(w http.ResponseWriter, r *http.Request) error {
	if r.ContentLength > maxUsageArchiveRequestBytes {
		return &http.MaxBytesError{Limit: maxUsageArchiveRequestBytes}
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxUsageArchiveRequestBytes))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(body)) != 0 {
		return errors.New("usage archive action request body must be empty")
	}
	return nil
}

func writeArchiveRequestError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	code := "usage_archive_invalid_request"
	message := "invalid usage archive request"
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		status = http.StatusRequestEntityTooLarge
		code = "usage_archive_request_too_large"
		message = "usage archive request is too large"
	}
	log.Printf("usage archive request rejected: %v", err)
	response.JSON(w, status, map[string]any{
		"error": message,
		"code":  code,
	})
}

func writeArchiveError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, usagesvc.ErrArchiveInvalidID), errors.Is(err, usagesvc.ErrArchiveInvalidRequest):
		status = http.StatusBadRequest
	case errors.Is(err, usagesvc.ErrArchiveNoEvents):
		status = http.StatusUnprocessableEntity
	case errors.Is(err, usagesvc.ErrArchiveMaintenanceLocked),
		errors.Is(err, usagesvc.ErrArchiveInvalidState),
		errors.Is(err, usagesvc.ErrArchiveCoverageIncomplete),
		errors.Is(err, usagesvc.ErrArchiveDeleteUnavailable):
		status = http.StatusConflict
	case errors.Is(err, usagesvc.ErrArchiveNotFound):
		status = http.StatusNotFound
	case errors.Is(err, usagesvc.ErrArchiveUnavailable):
		status = http.StatusServiceUnavailable
	}
	log.Printf("usage archive API request failed: %v", err)
	response.JSON(w, status, map[string]any{
		"error": archiveErrorMessage(err),
		"code":  archiveErrorCode(err),
	})
}

func archiveErrorMessage(err error) string {
	switch {
	case errors.Is(err, usagesvc.ErrArchiveInvalidID), errors.Is(err, usagesvc.ErrArchiveInvalidRequest):
		return "invalid usage archive request"
	case errors.Is(err, usagesvc.ErrArchiveNoEvents):
		return "no usage events are eligible for archive"
	case errors.Is(err, usagesvc.ErrArchiveMaintenanceLocked):
		return "usage maintenance is already active"
	case errors.Is(err, usagesvc.ErrArchiveInvalidState):
		return "usage archive operation is not allowed in the current state"
	case errors.Is(err, usagesvc.ErrArchiveCoverageIncomplete):
		return "usage archive coverage is not ready"
	case errors.Is(err, usagesvc.ErrArchiveDeleteUnavailable):
		return "usage archive deletion is not enabled"
	case errors.Is(err, usagesvc.ErrArchiveNotFound):
		return "usage archive run was not found"
	case errors.Is(err, usagesvc.ErrArchiveUnavailable):
		return "usage archive is unavailable"
	default:
		return "usage archive request failed"
	}
}

func archiveErrorCode(err error) string {
	switch {
	case errors.Is(err, usagesvc.ErrArchiveInvalidID):
		return "usage_archive_invalid_id"
	case errors.Is(err, usagesvc.ErrArchiveInvalidRequest):
		return "usage_archive_invalid_request"
	case errors.Is(err, usagesvc.ErrArchiveNoEvents):
		return "usage_archive_no_events"
	case errors.Is(err, usagesvc.ErrArchiveMaintenanceLocked):
		return "usage_archive_maintenance_locked"
	case errors.Is(err, usagesvc.ErrArchiveInvalidState):
		return "usage_archive_invalid_state"
	case errors.Is(err, usagesvc.ErrArchiveCoverageIncomplete):
		return "usage_archive_coverage_incomplete"
	case errors.Is(err, usagesvc.ErrArchiveDeleteUnavailable):
		return "usage_archive_delete_unavailable"
	case errors.Is(err, usagesvc.ErrArchiveNotFound):
		return "usage_archive_not_found"
	case errors.Is(err, usagesvc.ErrArchiveUnavailable):
		return "usage_archive_unavailable"
	default:
		return "request_failed"
	}
}

type createImportSessionRequest struct {
	Filename  string `json:"filename"`
	SizeBytes int64  `json:"size_bytes"`
	ResumeKey string `json:"resume_key,omitempty"`
}

func (h *Handler) handleImportSession(w http.ResponseWriter, r *http.Request, id string, action string) {
	switch {
	case id == "" && action == "" && r.Method == http.MethodPost:
		h.createImportSession(w, r)
	case id == "" && action == "" && r.Method == http.MethodGet:
		h.listImportSessions(w, r)
	case id != "" && action == "" && r.Method == http.MethodGet:
		h.getImportSession(w, r, id)
	case id != "" && action == "" && r.Method == http.MethodDelete:
		h.cancelImportSession(w, r, id)
	case id != "" && action == "chunk" && r.Method == http.MethodPut:
		h.writeImportSessionChunk(w, r, id)
	case id != "" && action == "complete" && r.Method == http.MethodPost:
		h.completeImportSession(w, r, id)
	default:
		response.MethodNotAllowed(w)
	}
}

func (h *Handler) listImportSessions(w http.ResponseWriter, r *http.Request) {
	limit, err := parseArchiveLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeImportSessionError(w, newImportSessionRequestError(err))
		return
	}
	list, err := h.App.UsageService.ListImportSessions(r.Context(), usagesvc.ImportSessionListOptions{
		Status: strings.TrimSpace(r.URL.Query().Get("status")),
		Limit:  limit,
		Cursor: strings.TrimSpace(r.URL.Query().Get("cursor")),
	})
	if err != nil {
		writeImportSessionError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, list)
}

func newImportSessionRequestError(_ error) error {
	return &usagesvc.ImportSessionError{
		Code:    usagesvc.ImportSessionErrorInvalidRequest,
		Message: "invalid usage import session request",
	}
}

func (h *Handler) createImportSession(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, maxUsageImportSessionCreateBytes)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	var request createImportSessionRequest
	if err := decoder.Decode(&request); err != nil {
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("usage import session request contains multiple JSON values")
		}
		response.Error(w, http.StatusBadRequest, err)
		return
	}
	session, err := h.App.UsageService.CreateImportSession(
		r.Context(),
		request.Filename,
		request.SizeBytes,
		request.ResumeKey,
	)
	if err != nil {
		writeImportSessionError(w, err)
		return
	}
	response.JSON(w, http.StatusCreated, usagesvc.NewImportSessionSummary(session))
}

func (h *Handler) getImportSession(w http.ResponseWriter, r *http.Request, id string) {
	session, err := h.App.UsageService.GetImportSession(r.Context(), id)
	if err != nil {
		writeImportSessionError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, usagesvc.NewImportSessionSummary(session))
}

func (h *Handler) writeImportSessionChunk(w http.ResponseWriter, r *http.Request, id string) {
	offsetText := strings.TrimSpace(r.URL.Query().Get("offset"))
	offset, err := strconv.ParseInt(offsetText, 10, 64)
	if err != nil || offset < 0 {
		response.Error(w, http.StatusBadRequest, errors.New("usage import chunk offset is invalid"))
		return
	}
	session, err := h.App.UsageService.WriteImportSessionChunk(
		r.Context(),
		id,
		offset,
		r.ContentLength,
		r.Body,
	)
	if err != nil {
		writeImportSessionError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, usagesvc.NewImportSessionSummary(session))
}

func (h *Handler) completeImportSession(w http.ResponseWriter, r *http.Request, id string) {
	session, err := h.App.UsageService.CompleteImportSession(r.Context(), id)
	if err != nil {
		writeImportSessionError(w, err)
		return
	}
	status := http.StatusOK
	if session.Status == usagesvc.ImportSessionStatusProcessing {
		status = http.StatusAccepted
	}
	response.JSON(w, status, usagesvc.NewImportSessionSummary(session))
}

func (h *Handler) cancelImportSession(w http.ResponseWriter, r *http.Request, id string) {
	session, err := h.App.UsageService.CancelImportSession(r.Context(), id)
	if err != nil {
		writeImportSessionError(w, err)
		return
	}
	status := http.StatusOK
	if session.Status == usagesvc.ImportSessionStatusProcessing {
		status = http.StatusAccepted
	}
	response.JSON(w, status, usagesvc.NewImportSessionSummary(session))
}

func parseImportSessionPath(path string) (id string, action string, ok bool) {
	clean := strings.TrimRight(path, "/")
	if clean == usageImportSessionsPath {
		return "", "", true
	}
	if !strings.HasPrefix(clean, usageImportSessionsPath+"/") {
		return "", "", false
	}
	parts := strings.Split(strings.TrimPrefix(clean, usageImportSessionsPath+"/"), "/")
	switch len(parts) {
	case 1:
		return parts[0], "", parts[0] != ""
	case 2:
		if parts[0] != "" && (parts[1] == "chunk" || parts[1] == "complete") {
			return parts[0], parts[1], true
		}
	}
	return "", "", false
}

func writeImportSessionError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	code := usagesvc.ImportSessionErrorUnavailable
	message := "usage import session request failed"
	var sessionErr *usagesvc.ImportSessionError
	if errors.As(err, &sessionErr) {
		code = sessionErr.Code
		switch sessionErr.Code {
		case usagesvc.ImportSessionErrorInvalidRequest:
			status = http.StatusBadRequest
		case usagesvc.ImportSessionErrorNotFound:
			status = http.StatusNotFound
		case usagesvc.ImportSessionErrorConflict:
			status = http.StatusConflict
		case usagesvc.ImportSessionErrorTooLarge:
			status = http.StatusRequestEntityTooLarge
		case usagesvc.ImportSessionErrorQuotaExceeded:
			status = http.StatusInsufficientStorage
		case usagesvc.ImportSessionErrorLimitExceeded:
			status = http.StatusTooManyRequests
		}
		if status < http.StatusInternalServerError {
			message = sessionErr.Message
		}
	}
	if status >= http.StatusInternalServerError {
		log.Printf("usage import session API request failed: %v", err)
	}
	response.JSON(w, status, map[string]any{
		"error": message,
		"code":  code,
	})
}

func (h *Handler) Export(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="usage-events.jsonl"`)
	writer := &countingWriter{writer: w}
	if err := h.App.UsageService.WriteExport(r.Context(), writer, h.App.Config.QueryLimit); err != nil {
		if writer.written == 0 {
			w.Header().Del("Content-Disposition")
			response.Error(w, http.StatusInternalServerError, err)
		} else {
			log.Printf("usage export stream failed after %d bytes: %v", writer.written, err)
		}
	}
}

type countingWriter struct {
	writer  io.Writer
	written int64
}

func (w *countingWriter) Write(data []byte) (int, error) {
	written, err := w.writer.Write(data)
	w.written += int64(written)
	return written, err
}

func (h *Handler) Import(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > maxUsageImportBytes {
		response.Error(w, http.StatusRequestEntityTooLarge, errors.New("http: request body too large"))
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxUsageImportBytes)
	result, parsed, err := h.App.UsageService.Import(r.Context(), body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			response.Error(w, http.StatusRequestEntityTooLarge, err)
			return
		}
		var persistenceErr *usagesvc.ImportPersistenceError
		if errors.As(err, &persistenceErr) || result.Added+result.Skipped > 0 {
			response.Error(w, http.StatusInternalServerError, err)
			return
		}
		if parsed == nil {
			response.Error(w, http.StatusBadRequest, err)
			return
		}
		response.JSON(w, http.StatusBadRequest, map[string]any{
			"error":       err.Error(),
			"format":      parsed.Format,
			"failed":      parsed.Failed,
			"unsupported": parsed.Unsupported,
			"warnings":    parsed.Warnings,
		})
		return
	}
	response.JSON(w, http.StatusOK, result)
}
