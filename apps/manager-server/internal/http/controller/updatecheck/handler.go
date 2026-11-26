package updatecheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/app"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/middleware"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/requestbody"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/response"
	updatechecksvc "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/updatecheck"
)

type Handler struct{ App *app.Context }

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	if !middleware.AuthorizePanel(&unreadBodyClosingResponseWriter{ResponseWriter: w, r: r}, r, h.App.AdminAuthService) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	suffix := strings.TrimPrefix(r.URL.Path, "/usage-service/updates")
	isSlotRoute := strings.HasPrefix(suffix, "/slot/")
	svc := h.App.UpdateCheckService
	if svc == nil {
		if isSlotRoute {
			writeSelfUpdateError(w, &updatechecksvc.SelfUpdateError{
				Code:       "update_unavailable",
				Message:    "update check unavailable",
				HTTPStatus: http.StatusServiceUnavailable,
			})
			return
		}
		http.Error(w, "update check unavailable", http.StatusServiceUnavailable)
		return
	}
	if isSlotRoute {
		h.handleSlot(w, r, svc, suffix)
		return
	}
	var payload any
	var err error
	switch {
	case suffix == "" && r.Method == http.MethodGet:
		payload, err = svc.Status(r.Context())
	case suffix == "/check" && r.Method == http.MethodPost:
		payload, err = svc.Check(r.Context())
	case suffix == "/notification" && r.Method == http.MethodPost:
		payload, err = svc.Claim(r.Context())
	case suffix == "/channel" && r.Method == http.MethodPut:
		var input struct {
			Preference string `json:"channel_preference"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if input.Preference != "auto" && input.Preference != "stable" && input.Preference != "rc" && input.Preference != "beta" {
			http.Error(w, "invalid channel", 400)
			return
		}
		payload, err = svc.SetChannel(r.Context(), input.Preference)
	case suffix == "/dismiss" && r.Method == http.MethodPost:
		var input struct {
			Version string `json:"version"`
		}
		if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		err = svc.Dismiss(r.Context(), input.Version)
		payload = map[string]bool{"ok": err == nil}
	default:
		response.MethodNotAllowed(w)
		return
	}
	if err != nil {
		var suErr *updatechecksvc.SelfUpdateError
		if errors.As(err, &suErr) && suErr != nil && suErr.Code == middleware.TrialHoldErrorCode {
			writeSelfUpdateError(w, suErr)
			return
		}
		http.Error(w, "update state unavailable", http.StatusServiceUnavailable)
		return
	}
	response.JSON(w, http.StatusOK, payload)
}

const (
	maxSlotJSONBodyBytes = 4096
	slotUploadDeadline   = 16 * time.Minute
)

func (h *Handler) handleSlot(w http.ResponseWriter, r *http.Request, svc *updatechecksvc.Service, suffix string) {
	switch {
	case suffix == "/slot/prepare" && r.Method == http.MethodPost:
		var input updatechecksvc.PrepareSlotInput
		if suErr := decodeStrictSlotJSON(w, r, maxSlotJSONBodyBytes, &input); suErr != nil {
			writeSelfUpdateError(w, suErr)
			return
		}
		result, err := svc.PrepareSlot(r.Context(), input)
		if err != nil {
			writeSelfUpdateError(w, err)
			return
		}
		response.JSON(w, http.StatusOK, result)
	case suffix == "/slot/prepare-upload" && r.Method == http.MethodPut:
		rc := http.NewResponseController(w)
		deadline := time.Now().Add(slotUploadDeadline)
		_ = rc.SetReadDeadline(deadline)
		_ = rc.SetWriteDeadline(deadline)

		if !hasValidSlotUploadContentType(r) {
			closeUnreadRequestBody(w, r)
			writeSelfUpdateError(w, &updatechecksvc.SelfUpdateError{
				Code:       "invalid_request",
				Message:    "Content-Type must be application/gzip or application/octet-stream",
				HTTPStatus: http.StatusBadRequest,
			})
			return
		}
		if r.ContentLength > updatechecksvc.MaxSlotArchiveBytes {
			closeUnreadRequestBody(w, r)
			writeSelfUpdateError(w, &updatechecksvc.SelfUpdateError{
				Code:       "archive_too_large",
				Message:    "slot archive exceeds 192 MiB limit",
				HTTPStatus: http.StatusRequestEntityTooLarge,
			})
			return
		}
		version, suErr := parseSlotUploadVersion(r)
		if suErr != nil {
			closeUnreadRequestBody(w, r)
			writeSelfUpdateError(w, suErr)
			return
		}
		if _, err := updatechecksvc.ParseVersion(version); err != nil || !strings.HasPrefix(version, "v") || strings.Contains(version, "+") {
			closeUnreadRequestBody(w, r)
			writeSelfUpdateError(w, &updatechecksvc.SelfUpdateError{
				Code:       "invalid_request",
				Message:    "version must be a valid canonical tag starting with v",
				HTTPStatus: http.StatusBadRequest,
			})
			return
		}
		if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
			closeUnreadRequestBody(w, r)
			writeSelfUpdateError(w, &updatechecksvc.SelfUpdateError{
				Code:       "invalid_request",
				Message:    "slot archive body is required",
				HTTPStatus: http.StatusBadRequest,
			})
			return
		}
		limited := newMaxSlotUploadReader(w, r.Body, updatechecksvc.MaxSlotArchiveBytes+1)
		defer limited.Close()
		result, err := svc.UploadSlot(r.Context(), version, limited, r.ContentLength)
		_ = limited.Close()
		if !limited.eofReached.Load() {
			w.Header().Set("Connection", "close")
			_ = rc.SetReadDeadline(time.Unix(1, 0))
		}
		if limited.exceeded.Load() {
			var suErr *updatechecksvc.SelfUpdateError
			if !errors.As(err, &suErr) || suErr.Code != "archive_too_large" {
				writeSelfUpdateError(w, &updatechecksvc.SelfUpdateError{
					Code:       "archive_too_large",
					Message:    "slot archive exceeds 192 MiB limit",
					HTTPStatus: http.StatusRequestEntityTooLarge,
				})
				return
			}
		}
		if err != nil {
			writeSelfUpdateError(w, err)
			return
		}
		response.JSON(w, http.StatusOK, result)
	case suffix == "/slot/activate" && r.Method == http.MethodPost:
		var input struct {
			Version string `json:"version"`
		}
		if suErr := decodeStrictSlotJSON(w, r, maxSlotJSONBodyBytes, &input); suErr != nil {
			writeSelfUpdateError(w, suErr)
			return
		}
		result, err := svc.ActivateSlot(r.Context(), input.Version)
		if err != nil {
			writeSelfUpdateError(w, err)
			return
		}
		response.JSON(w, http.StatusAccepted, result)
	case suffix == "/slot/rollback" && r.Method == http.MethodPost:
		var input struct {
			ExpectedActiveSlot string `json:"expected_active_slot"`
		}
		if suErr := decodeStrictSlotJSON(w, r, maxSlotJSONBodyBytes, &input); suErr != nil {
			writeSelfUpdateError(w, suErr)
			return
		}
		result, err := svc.RollbackSlot(r.Context(), input.ExpectedActiveSlot)
		if err != nil {
			writeSelfUpdateError(w, err)
			return
		}
		response.JSON(w, http.StatusAccepted, result)
	default:
		response.MethodNotAllowed(w)
	}
}

func decodeStrictSlotJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, dst any) *updatechecksvc.SelfUpdateError {
	body, err := io.ReadAll(http.MaxBytesReader(requestbody.UnwrapRootResponseWriter(w), r.Body, maxBytes))
	if err != nil {
		closeUnreadRequestBody(w, r)
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return &updatechecksvc.SelfUpdateError{
				Code:       "invalid_request",
				Message:    "request body exceeds maximum size",
				HTTPStatus: http.StatusRequestEntityTooLarge,
			}
		}
		return &updatechecksvc.SelfUpdateError{
			Code:       "invalid_request",
			Message:    "failed to read request body",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	if !utf8.Valid(body) {
		return &updatechecksvc.SelfUpdateError{
			Code:       "invalid_request",
			Message:    "request body must be valid UTF-8",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return &updatechecksvc.SelfUpdateError{
			Code:       "invalid_request",
			Message:    "request body must be a JSON object",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return &updatechecksvc.SelfUpdateError{
			Code:       "invalid_request",
			Message:    "invalid JSON request body",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return &updatechecksvc.SelfUpdateError{
			Code:       "invalid_request",
			Message:    "unexpected trailing data in JSON request body",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	return nil
}

func parseSlotUploadVersion(r *http.Request) (string, *updatechecksvc.SelfUpdateError) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 1 {
		return "", &updatechecksvc.SelfUpdateError{
			Code:       "invalid_request",
			Message:    "version query parameter is required",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	vals, ok := query["version"]
	if !ok || len(vals) != 1 || strings.TrimSpace(vals[0]) == "" {
		return "", &updatechecksvc.SelfUpdateError{
			Code:       "invalid_request",
			Message:    "version query parameter is required",
			HTTPStatus: http.StatusBadRequest,
		}
	}
	return strings.TrimSpace(vals[0]), nil
}

func hasValidSlotUploadContentType(r *http.Request) bool {
	values := r.Header.Values("Content-Type")
	if len(values) == 0 {
		return true
	}
	if len(values) > 1 {
		return false
	}
	raw := strings.TrimSpace(values[0])
	if raw == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(raw)
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

func closeUnreadRequestBody(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return
	}
	w.Header().Set("Connection", "close")
	_ = http.NewResponseController(w).SetReadDeadline(time.Unix(1, 0))
}

type unreadBodyClosingResponseWriter struct {
	http.ResponseWriter
	r           *http.Request
	wroteHeader bool
}

func (w *unreadBodyClosingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *unreadBodyClosingResponseWriter) WriteHeader(statusCode int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		closeUnreadRequestBody(w.ResponseWriter, w.r)
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *unreadBodyClosingResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func writeSelfUpdateError(w http.ResponseWriter, err error) {
	var suErr *updatechecksvc.SelfUpdateError
	if errors.As(err, &suErr) && suErr != nil {
		status := suErr.HTTPStatus
		if status < 400 || status > 599 {
			status = http.StatusServiceUnavailable
		}
		code := strings.TrimSpace(suErr.Code)
		if code == "" {
			code = "internal_error"
		}
		if code == middleware.TrialHoldErrorCode {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Retry-After", "5")
		}
		response.JSON(w, status, map[string]string{
			"code":    code,
			"message": suErr.Message,
		})
		return
	}
	response.JSON(w, http.StatusServiceUnavailable, map[string]string{
		"code":    "update_unavailable",
		"message": err.Error(),
	})
}

type maxSlotUploadReader struct {
	body       io.Reader
	rc         *http.ResponseController
	remaining  atomic.Int64
	exceeded   atomic.Bool
	eofReached atomic.Bool
	mu         sync.Mutex
	closed     bool
	readDone   chan struct{}
	closeOnce  sync.Once
}

func newMaxSlotUploadReader(w http.ResponseWriter, body io.Reader, limit int64) *maxSlotUploadReader {
	done := make(chan struct{})
	close(done)
	r := &maxSlotUploadReader{
		body:     body,
		rc:       http.NewResponseController(w),
		readDone: done,
	}
	r.remaining.Store(limit)
	return r
}

func (r *maxSlotUploadReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return 0, io.EOF
	}
	rem := r.remaining.Load()
	if rem <= 0 {
		r.exceeded.Store(true)
		r.mu.Unlock()
		return 0, io.EOF
	}
	if int64(len(p)) > rem {
		p = p[:rem]
	}
	done := make(chan struct{})
	r.readDone = done
	r.mu.Unlock()

	n, err := r.body.Read(p)
	if errors.Is(err, io.EOF) {
		r.eofReached.Store(true)
	}
	if n > 0 && r.remaining.Add(-int64(n)) <= 0 {
		r.exceeded.Store(true)
	}
	r.mu.Lock()
	closed := r.closed
	close(done)
	r.mu.Unlock()
	if closed && err == nil {
		return n, io.EOF
	}
	return n, err
}

func (r *maxSlotUploadReader) Close() error {
	r.mu.Lock()
	r.closed = true
	done := r.readDone
	r.mu.Unlock()
	r.closeOnce.Do(func() {
		if !r.eofReached.Load() && r.rc != nil {
			_ = r.rc.SetReadDeadline(time.Unix(1, 0))
		}
	})
	<-done
	return nil
}
