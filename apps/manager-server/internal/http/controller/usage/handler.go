package usage

import (
	"encoding/json"
	"errors"
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
const usageImportSessionsPath = "/v0/management/usage/import-sessions"

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizePanel(w, r, h.App.AdminAuthService) {
		return
	}
	if id, action, ok := parseImportSessionPath(r.URL.Path); ok {
		h.handleImportSession(w, r, id, action)
		return
	}
	cleanPath := strings.TrimRight(r.URL.Path, "/")
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

type createImportSessionRequest struct {
	Filename  string `json:"filename"`
	SizeBytes int64  `json:"size_bytes"`
	ResumeKey string `json:"resume_key,omitempty"`
}

func (h *Handler) handleImportSession(w http.ResponseWriter, r *http.Request, id string, action string) {
	switch {
	case id == "" && action == "" && r.Method == http.MethodPost:
		h.createImportSession(w, r)
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
	response.JSON(w, http.StatusCreated, session)
}

func (h *Handler) getImportSession(w http.ResponseWriter, r *http.Request, id string) {
	session, err := h.App.UsageService.GetImportSession(r.Context(), id)
	if err != nil {
		writeImportSessionError(w, err)
		return
	}
	response.JSON(w, http.StatusOK, session)
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
	response.JSON(w, http.StatusOK, session)
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
	response.JSON(w, status, session)
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
	response.JSON(w, status, session)
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
	}
	response.JSON(w, status, map[string]any{
		"error": err.Error(),
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
