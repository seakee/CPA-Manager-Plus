package cpaupdate

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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	cpaupdateservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpaupdate"
)

const routePrefix = "/usage-service/runtime/updates"

const (
	maxMutationBodyBytes  = 2048
	maxUploadArchiveBytes = 64 << 20
)

type Handler struct {
	App *app.Context
}

func (h *Handler) Handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !middleware.AuthorizePanel(&unreadBodyClosingResponseWriter{ResponseWriter: w, r: r}, r, h.App.AdminAuthService) {
		return
	}

	suffix := strings.TrimPrefix(r.URL.Path, routePrefix)
	isStatus := suffix == "" && r.Method == http.MethodGet
	isCheck := suffix == "/check" && r.Method == http.MethodPost
	isPrepare := suffix == "/prepare" && r.Method == http.MethodPost
	isPrepareUpload := suffix == "/prepare-upload" && r.Method == http.MethodPost
	isActivate := suffix == "/activate" && r.Method == http.MethodPost
	isObservePrepare := suffix == "/operations/prepare" && r.Method == http.MethodGet
	isObserveActivate := suffix == "/operations/activate" && r.Method == http.MethodGet
	if !isStatus && !isCheck && !isPrepare && !isPrepareUpload && !isActivate && !isObservePrepare && !isObserveActivate {
		closeUnreadRequestBody(w, r)
		response.MethodNotAllowed(w)
		return
	}
	if (isStatus || isCheck) && !emptyRequestBody(w, r) {
		closeUnreadRequestBody(w, r)
		response.Error(w, http.StatusBadRequest, errors.New("request body must be empty"))
		return
	}
	service := h.App.CPAUpdateService
	if service == nil {
		if !(isStatus || isCheck) {
			closeUnreadRequestBody(w, r)
		}
		response.Error(w, http.StatusServiceUnavailable, errors.New("CPA update state unavailable"))
		return
	}
	if isObservePrepare || isObserveActivate {
		request, err := decodeObservationRequest(w, r)
		if err != nil {
			writeObservationError(w, err)
			return
		}
		var result cpaupdateservice.ObservationResult
		if isObservePrepare {
			result, err = service.ObservePrepare(r.Context(), request)
		} else {
			result, err = service.ObserveActivate(r.Context(), request)
		}
		if err != nil {
			writeObservationError(w, err)
			return
		}
		response.JSON(w, http.StatusOK, result)
		return
	}
	if isPrepareUpload {
		if !hasValidUploadContentType(r) || r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 || r.ContentLength < -1 {
			closeUnreadRequestBody(w, r)
			writeMutationError(w, cpaupdateservice.MutationResult{}, &cpaupdateservice.MutationError{Kind: cpaupdateservice.MutationErrorInvalid, Code: "invalid_request"})
			return
		}
		if r.ContentLength > maxUploadArchiveBytes {
			closeUnreadRequestBody(w, r)
			writeUploadTooLarge(w)
			return
		}
		request, err := decodePrepareUploadQuery(r)
		if err != nil {
			closeUnreadRequestBody(w, r)
			writeMutationError(w, cpaupdateservice.MutationResult{}, err)
			return
		}
		// Forward one sentinel byte on an unknown-length upload so Supervisor
		// can persist archive_too_large. End the upstream stream cleanly there
		// rather than aborting the transport before it can return the result.
		const limit = maxUploadArchiveBytes + 1
		limited := newMaxUploadTrackingReader(w, r.Body, limit)
		defer limited.Close()
		result, err := service.PrepareUpload(r.Context(), request, limited, r.ContentLength)
		_ = limited.Close()
		if !limited.eofReached.Load() {
			w.Header().Set("Connection", "close")
			_ = http.NewResponseController(w).SetReadDeadline(time.Unix(1, 0))
		}
		if limited.exceeded.Load() && result.FailureCode != "archive_too_large" {
			writeUploadTooLarge(w)
			return
		}
		if err != nil {
			writeMutationError(w, result, err)
			return
		}
		response.JSON(w, http.StatusOK, result)
		return
	}
	if isPrepare || isActivate {
		request, err := decodeMutationRequest(w, r, isPrepare)
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

func decodeObservationRequest(w http.ResponseWriter, r *http.Request) (cpaupdateservice.ObservationRequest, error) {
	var request cpaupdateservice.ObservationRequest
	if !emptyRequestBody(w, r) {
		closeUnreadRequestBody(w, r)
		return request, &cpaupdateservice.ObservationError{Kind: cpaupdateservice.ObservationErrorInvalid, Code: "invalid_request"}
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 2 {
		return request, &cpaupdateservice.ObservationError{Kind: cpaupdateservice.ObservationErrorInvalid, Code: "invalid_request"}
	}
	requestIDs, requestOK := query["request_id"]
	targetVersions, targetOK := query["target_version"]
	if !requestOK || len(requestIDs) != 1 || requestIDs[0] == "" ||
		!targetOK || len(targetVersions) != 1 || targetVersions[0] == "" {
		return request, &cpaupdateservice.ObservationError{Kind: cpaupdateservice.ObservationErrorInvalid, Code: "invalid_request"}
	}
	request.RequestID = requestIDs[0]
	request.TargetVersion = targetVersions[0]
	if err := request.Validate(); err != nil {
		return request, err
	}
	return request, nil
}

func writeObservationError(w http.ResponseWriter, err error) {
	kind, code, ok := cpaupdateservice.ObservationErrorDetails(err)
	if !ok {
		kind = cpaupdateservice.ObservationErrorUnavailable
		code = "observation_unavailable"
	}
	status := http.StatusServiceUnavailable
	switch kind {
	case cpaupdateservice.ObservationErrorInvalid:
		status = http.StatusBadRequest
	case cpaupdateservice.ObservationErrorConflict:
		status = http.StatusConflict
	}
	response.JSON(w, status, map[string]string{
		"error": (&cpaupdateservice.ObservationError{Kind: kind, Code: code}).Error(),
		"code":  code,
	})
}

func emptyRequestBody(w http.ResponseWriter, r *http.Request) bool {
	data, err := io.ReadAll(http.MaxBytesReader(requestbody.UnwrapRootResponseWriter(w), r.Body, 0))
	return err == nil && len(data) == 0
}

func decodeMutationRequest(w http.ResponseWriter, r *http.Request, allowSourceFields bool) (cpaupdateservice.MutationRequest, error) {
	var request cpaupdateservice.MutationRequest
	body, err := io.ReadAll(http.MaxBytesReader(requestbody.UnwrapRootResponseWriter(w), r.Body, maxMutationBodyBytes))
	if err != nil || !utf8.Valid(body) {
		if err != nil {
			closeUnreadRequestBody(w, r)
		}
		return request, &cpaupdateservice.MutationError{Kind: cpaupdateservice.MutationErrorInvalid, Code: "invalid_request"}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return request, &cpaupdateservice.MutationError{Kind: cpaupdateservice.MutationErrorInvalid, Code: "invalid_request"}
	}
	seen := make(map[string]bool, 5)
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
		case "source_mode":
			if !allowSourceFields {
				return request, &cpaupdateservice.MutationError{Kind: cpaupdateservice.MutationErrorInvalid, Code: "invalid_request"}
			}
			err = decoder.Decode(&request.SourceMode)
		case "cdn_prefix":
			if !allowSourceFields {
				return request, &cpaupdateservice.MutationError{Kind: cpaupdateservice.MutationErrorInvalid, Code: "invalid_request"}
			}
			err = decoder.Decode(&request.CDNPrefix)
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

func decodePrepareUploadQuery(r *http.Request) (cpaupdateservice.MutationRequest, error) {
	var request cpaupdateservice.MutationRequest
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) != 3 {
		return request, &cpaupdateservice.MutationError{Kind: cpaupdateservice.MutationErrorInvalid, Code: "invalid_request"}
	}
	get := func(name string) (string, bool) {
		values, ok := query[name]
		if !ok || len(values) != 1 || values[0] == "" {
			return "", false
		}
		return values[0], true
	}
	var ok bool
	if request.RequestID, ok = get("request_id"); !ok {
		return request, &cpaupdateservice.MutationError{Kind: cpaupdateservice.MutationErrorInvalid, Code: "invalid_request"}
	}
	if request.TargetVersion, ok = get("target_version"); !ok {
		return request, &cpaupdateservice.MutationError{Kind: cpaupdateservice.MutationErrorInvalid, Code: "invalid_request"}
	}
	artifactID, ok := get("expected_active_artifact_id")
	if !ok {
		return request, &cpaupdateservice.MutationError{Kind: cpaupdateservice.MutationErrorInvalid, Code: "invalid_request"}
	}
	request.ExpectedActiveArtifactID = model.RuntimeArtifactID(artifactID)
	if err := request.Validate(); err != nil {
		return request, err
	}
	return request, nil
}

func hasValidUploadContentType(r *http.Request) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	raw := strings.TrimSpace(values[0])
	if raw == "" {
		return false
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

func hasUnreadRequestBody(r *http.Request) bool {
	return r != nil && r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0
}

func closeUnreadRequestBody(w http.ResponseWriter, r *http.Request) {
	if !hasUnreadRequestBody(r) {
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

type maxUploadTrackingReader struct {
	body          io.Reader
	rc            *http.ResponseController
	remaining     atomic.Int64
	exceeded      atomic.Bool
	eofReached    atomic.Bool
	mu            sync.Mutex
	closed        bool
	interruptible bool
	readDone      chan struct{}
	closeOnce     sync.Once
}

func newMaxUploadTrackingReader(w http.ResponseWriter, body io.Reader, limit int64) *maxUploadTrackingReader {
	done := make(chan struct{})
	close(done)
	r := &maxUploadTrackingReader{
		body:     body,
		rc:       http.NewResponseController(w),
		readDone: done,
	}
	r.remaining.Store(limit)
	return r
}

func (r *maxUploadTrackingReader) Read(p []byte) (int, error) {
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

	if n > 0 {
		if r.remaining.Add(-int64(n)) <= 0 {
			r.exceeded.Store(true)
		}
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

func (r *maxUploadTrackingReader) Close() error {
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()

	r.closeOnce.Do(func() {
		if !r.eofReached.Load() && r.rc != nil && r.rc.SetReadDeadline(time.Unix(1, 0)) == nil {
			r.mu.Lock()
			r.interruptible = true
			r.mu.Unlock()
		}
	})
	r.mu.Lock()
	wait := r.interruptible
	done := r.readDone
	r.mu.Unlock()
	if wait {
		<-done
	}
	return nil
}

func (r *maxUploadTrackingReader) Interruptible() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.interruptible
}

func writeUploadTooLarge(w http.ResponseWriter) {
	writeMutationError(w, cpaupdateservice.MutationResult{}, &cpaupdateservice.MutationError{
		Kind: cpaupdateservice.MutationErrorTooLarge,
		Code: "archive_too_large",
	})
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
	case cpaupdateservice.MutationErrorTooLarge:
		status = http.StatusRequestEntityTooLarge
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
