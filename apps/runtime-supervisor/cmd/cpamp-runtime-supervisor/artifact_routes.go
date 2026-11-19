package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

const (
	artifactStatusPath               = "/v1/runtime/artifact"
	artifactProbePath                = "/v1/runtime/artifact/probe"
	artifactAcquirePath              = "/v1/runtime/artifact/acquire"
	artifactUploadPath               = "/v1/runtime/artifact/upload"
	uploadArtifactBudgetTimeout      = lifecycle.AcquireArtifactExecutionTimeout
	uploadArtifactWriteBudgetTimeout = 12 * time.Minute
	maxArtifactJSONRequestBytes      = 4 << 10
)

type artifactErrorResponse struct {
	Code string `json:"code"`
}

type artifactRoutesHandler struct {
	tokenDigest [sha256.Size]byte
	executor    *lifecycle.Executor
	next        http.Handler
}

// enableArtifactRoutes mounts the /v1/runtime/artifact* surface alongside the
// Runtime Protocol handler. When CPAMP_CPA_ARTIFACT_MANIFEST is unset, when the
// Supervisor runs in private-configuration or read-only mode, or when the
// platform/stores are unsupported, acquisition stays unconfigured and the
// artifact routes return 409 unsupported_platform; when a manifest path is
// configured, acquisition is enabled and any unreadable or invalid manifest
// file surfaces lazily as 500 install_failed without blocking Supervisor
// startup or /v1/runtime/status.
func enableArtifactRoutes(cfg config, runtime *runtimeHandler, opts ...lifecycle.AcquisitionOption) error {
	if runtime == nil {
		return errors.New("runtime handler is required")
	}
	if !cfg.privateConfiguration && runtime.executor != nil && strings.TrimSpace(cfg.cpaArtifactManifest) != "" {
		_ = runtime.executor.EnableArtifactAcquisition(cfg.cpaArtifactManifest, opts...)
	}
	runtime.Handler = newArtifactRoutesHandler(cfg.token, runtime.executor, runtime.Handler)
	return nil
}

func newArtifactRoutesHandler(token string, executor *lifecycle.Executor, next http.Handler) http.Handler {
	return &artifactRoutesHandler{
		tokenDigest: sha256.Sum256([]byte(token)),
		executor:    executor,
		next:        next,
	}
}

func (h *artifactRoutesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var expectedMethod string
	switch r.URL.Path {
	case artifactStatusPath:
		expectedMethod = http.MethodGet
	case artifactProbePath, artifactAcquirePath:
		expectedMethod = http.MethodPost
	case artifactUploadPath:
		expectedMethod = http.MethodPut
	default:
		if h.next != nil {
			h.next.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
		return
	}
	w = &artifactResponseWriter{
		ResponseWriter: w,
		body:           trackArtifactRequestBody(r),
	}
	if !h.authorized(r.Header.Get("Authorization")) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeArtifactError(w, http.StatusUnauthorized, lifecycle.ArtifactCodeUnauthorized)
		return
	}
	if r.Method != expectedMethod {
		w.Header().Set("Allow", expectedMethod)
		writeArtifactError(w, http.StatusMethodNotAllowed, lifecycle.ArtifactCodeInvalidRequest)
		return
	}
	if h.executor == nil {
		writeArtifactError(w, http.StatusConflict, lifecycle.ArtifactCodeUnsupportedPlatform)
		return
	}

	switch r.URL.Path {
	case artifactStatusPath:
		h.serveStatus(w, r)
	case artifactProbePath:
		h.serveProbe(w, r)
	case artifactAcquirePath:
		h.serveAcquire(w, r)
	case artifactUploadPath:
		h.serveUpload(w, r)
	}
}

func (h *artifactRoutesHandler) authorized(authorization string) bool {
	fields := strings.Fields(authorization)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return false
	}
	providedDigest := sha256.Sum256([]byte(fields[1]))
	return subtle.ConstantTimeCompare(providedDigest[:], h.tokenDigest[:]) == 1
}

func (h *artifactRoutesHandler) serveStatus(w http.ResponseWriter, _ *http.Request) {
	status, err := h.executor.ArtifactStatus()
	if err != nil {
		writeMappedArtifactError(w, err)
		return
	}
	writeArtifactJSON(w, http.StatusOK, status)
}

func (h *artifactRoutesHandler) serveProbe(w http.ResponseWriter, r *http.Request) {
	var empty struct{}
	if err := decodeStrictArtifactJSON(r.Body, &empty); err != nil {
		writeArtifactError(w, http.StatusBadRequest, lifecycle.ArtifactCodeInvalidRequest)
		return
	}
	result, err := h.executor.ProbeArtifactSource(r.Context())
	if err != nil {
		writeMappedArtifactError(w, err)
		return
	}
	writeArtifactJSON(w, http.StatusOK, result)
}

func (h *artifactRoutesHandler) serveAcquire(w http.ResponseWriter, r *http.Request) {
	var req lifecycle.ArtifactAcquireRequest
	if err := decodeStrictArtifactJSON(r.Body, &req); err != nil {
		writeArtifactError(w, http.StatusBadRequest, lifecycle.ArtifactCodeInvalidRequest)
		return
	}
	op, started, err := h.executor.AcquireArtifact(r.Context(), req)
	if err != nil {
		writeMappedArtifactError(w, err)
		return
	}
	status := http.StatusOK
	if started {
		status = http.StatusAccepted
	}
	writeArtifactJSON(w, status, lifecycle.ArtifactOperationResponse{Operation: op})
}

func (h *artifactRoutesHandler) serveUpload(w http.ResponseWriter, r *http.Request) {
	if len(r.Header.Values("Content-Type")) > 1 || !validUploadContentType(r.Header.Get("Content-Type")) {
		writeArtifactError(w, http.StatusBadRequest, lifecycle.ArtifactCodeInvalidRequest)
		return
	}
	if r.ContentLength > runtimeupdate.MaxAcquisitionArchiveBytes {
		writeArtifactError(w, http.StatusRequestEntityTooLarge, lifecycle.ArtifactCodeArchiveTooLarge)
		return
	}
	if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 || r.ContentLength < -1 {
		writeArtifactError(w, http.StatusBadRequest, lifecycle.ArtifactCodeInvalidRequest)
		return
	}
	controller := http.NewResponseController(w)
	now := time.Now()
	if err := controller.SetReadDeadline(now.Add(uploadArtifactBudgetTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		writeArtifactError(w, http.StatusInternalServerError, lifecycle.ArtifactCodeInstallFailed)
		return
	}
	if err := controller.SetWriteDeadline(now.Add(uploadArtifactWriteBudgetTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		restoreArtifactUploadReadDeadline(controller, artifactRequestBodyConsumed(w))
		writeArtifactError(w, http.StatusInternalServerError, lifecycle.ArtifactCodeInstallFailed)
		return
	}
	defer func() {
		restoreArtifactUploadReadDeadline(controller, artifactRequestBodyConsumed(w))
	}()

	body := http.MaxBytesReader(unwrapArtifactResponseWriter(w), r.Body, runtimeupdate.MaxAcquisitionArchiveBytes)
	op, err := h.executor.UploadArtifact(r.Context(), body, r.ContentLength)
	if err != nil {
		writeMappedArtifactError(w, err)
		return
	}
	writeArtifactJSON(w, http.StatusOK, lifecycle.ArtifactOperationResponse{Operation: op})
}

func validUploadContentType(raw string) bool {
	trimmed := strings.TrimSpace(raw)
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

func decodeStrictArtifactJSON(body io.Reader, target any) error {
	if body == nil {
		return errors.New("request body is required")
	}
	encoded, err := io.ReadAll(io.LimitReader(body, maxArtifactJSONRequestBytes+1))
	if err != nil || len(encoded) == 0 || len(encoded) > maxArtifactJSONRequestBytes {
		return errors.New("invalid request body size")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func writeMappedArtifactError(w http.ResponseWriter, err error) {
	code := lifecycle.ArtifactErrorCode(err)
	writeArtifactError(w, artifactHTTPStatus(code), code)
}

func artifactHTTPStatus(code string) int {
	switch code {
	case lifecycle.ArtifactCodeInvalidRequest:
		return http.StatusBadRequest
	case lifecycle.ArtifactCodeUnauthorized:
		return http.StatusUnauthorized
	case lifecycle.ArtifactCodeUnsupportedPlatform,
		lifecycle.ArtifactCodeAcquisitionInProgress,
		lifecycle.ArtifactCodeArtifactConflict,
		lifecycle.ArtifactCodeArtifactAlreadyActive:
		return http.StatusConflict
	case lifecycle.ArtifactCodeArchiveTooLarge:
		return http.StatusRequestEntityTooLarge
	case lifecycle.ArtifactCodeArchiveInvalid,
		lifecycle.ArtifactCodeArtifactMismatch:
		return http.StatusUnprocessableEntity
	case lifecycle.ArtifactCodeSourceUnreachable,
		lifecycle.ArtifactCodeDownloadFailed:
		return http.StatusBadGateway
	case lifecycle.ArtifactCodeRecoveryRequired:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

func writeArtifactError(w http.ResponseWriter, status int, code string) {
	writeArtifactJSON(w, status, artifactErrorResponse{Code: code})
}

func writeArtifactJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

type artifactBodyTracker struct {
	rc       io.ReadCloser
	consumed atomic.Bool
}

func trackArtifactRequestBody(r *http.Request) *artifactBodyTracker {
	tracker := &artifactBodyTracker{}
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		tracker.consumed.Store(true)
		return tracker
	}
	tracker.rc = r.Body
	r.Body = tracker
	return tracker
}

func (b *artifactBodyTracker) Read(p []byte) (int, error) {
	n, err := b.rc.Read(p)
	if errors.Is(err, io.EOF) {
		b.consumed.Store(true)
	}
	return n, err
}

func (b *artifactBodyTracker) Close() error {
	return b.rc.Close()
}

type artifactResponseWriter struct {
	http.ResponseWriter
	body                *artifactBodyTracker
	wroteHeader         bool
	expiredReadDeadline bool
}

func (w *artifactResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *artifactResponseWriter) SetReadDeadline(deadline time.Time) error {
	if !deadline.IsZero() && !deadline.After(time.Now().Add(time.Second)) {
		if w.expiredReadDeadline {
			return nil
		}
		w.expiredReadDeadline = true
	} else {
		w.expiredReadDeadline = false
	}
	return http.NewResponseController(w.ResponseWriter).SetReadDeadline(deadline)
}

func (w *artifactResponseWriter) WriteHeader(statusCode int) {
	if !w.wroteHeader {
		w.wroteHeader = true
		if w.body != nil && !w.body.consumed.Load() {
			w.Header().Set("Connection", "close")
			if !w.expiredReadDeadline {
				w.expiredReadDeadline = true
				_ = http.NewResponseController(w.ResponseWriter).SetReadDeadline(time.Now())
			}
		}
	}
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *artifactResponseWriter) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(p)
}

func unwrapArtifactResponseWriter(w http.ResponseWriter) http.ResponseWriter {
	if wrapped, ok := w.(*artifactResponseWriter); ok {
		return wrapped.ResponseWriter
	}
	return w
}

func artifactRequestBodyConsumed(w http.ResponseWriter) bool {
	if wrapped, ok := w.(*artifactResponseWriter); ok && wrapped.body != nil {
		return wrapped.body.consumed.Load()
	}
	return false
}

func restoreArtifactUploadReadDeadline(controller *http.ResponseController, consumed bool) {
	if consumed {
		_ = controller.SetReadDeadline(time.Time{})
		return
	}
	_ = controller.SetReadDeadline(time.Now())
}
