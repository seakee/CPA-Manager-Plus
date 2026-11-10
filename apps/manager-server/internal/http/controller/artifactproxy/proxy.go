package artifactproxy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"reflect"
	"strings"

	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

const (
	MaxJSONBody    = 4 << 10  // 4 KiB
	MaxUploadBytes = 64 << 20 // 64 MiB
)

const (
	ResourceStatus  = "runtime/artifact"
	ResourceProbe   = "runtime/artifact/probe"
	ResourceAcquire = "runtime/artifact/acquire"
	ResourceUpload  = "runtime/artifact/upload"
)

// ArtifactRuntime is the Supervisor artifact boundary shared by the Fresh and
// Upgrade installation surfaces.
type ArtifactRuntime interface {
	Status(ctx context.Context) (runtimeservice.ArtifactStatus, error)
	Probe(ctx context.Context) (runtimeservice.ArtifactProbeResult, error)
	Acquire(ctx context.Context, source string, cdnPrefix string) (runtimeservice.ArtifactOperationEnvelope, error)
	Upload(ctx context.Context, body io.Reader, contentLength int64) (runtimeservice.ArtifactOperationEnvelope, error)
}

type CDNPreset struct {
	ID         string `json:"id"`
	Prefix     string `json:"prefix"`
	ThirdParty bool   `json:"thirdParty"`
}

// CDNPresets holds the release-selected third-party CDN prefixes.
// It is intentionally empty in S2a; presets are chosen at release time.
var CDNPresets = []CDNPreset{}

type StatusResponse struct {
	SchemaVersion int                               `json:"schemaVersion"`
	Pinned        runtimeservice.ArtifactPinned     `json:"pinned"`
	Active        *runtimeservice.ArtifactActive    `json:"active"`
	Required      bool                              `json:"required"`
	Operation     *runtimeservice.ArtifactOperation `json:"operation"`
	CDNPresets    []CDNPreset                       `json:"cdnPresets"`
}

type AcquireRequest struct {
	Source    string `json:"source"`
	CDNPrefix string `json:"cdnPrefix,omitempty"`
}

type ErrorResponse struct {
	Code string `json:"code"`
}

// IsResource reports whether resource is one of the four runtime artifact
// endpoints.
func IsResource(resource string) bool {
	switch resource {
	case ResourceStatus, ResourceProbe, ResourceAcquire, ResourceUpload:
		return true
	default:
		return false
	}
}

// ServeResource validates the request URL, method, and payload for one of the
// four runtime artifact resources (`runtime/artifact`, `.../probe`,
// `.../acquire`, `.../upload`) and forwards the operation to runtime.
func ServeResource(w http.ResponseWriter, r *http.Request, runtime ArtifactRuntime, resource string) {
	if r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		WriteError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	switch resource {
	case ResourceStatus:
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ServeStatus(w, r, runtime)
	case ResourceProbe:
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ServeProbe(w, r, runtime)
	case ResourceAcquire:
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ServeAcquire(w, r, runtime)
	case ResourceUpload:
		if r.Method != http.MethodPut {
			w.Header().Set("Allow", http.MethodPut)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		ServeUpload(w, r, runtime)
	default:
		http.NotFound(w, r)
	}
}

func ServeStatus(w http.ResponseWriter, r *http.Request, runtime ArtifactRuntime) {
	if isNilRuntime(runtime) {
		WriteError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	status, err := runtime.Status(r.Context())
	if err != nil {
		WriteUpstreamError(w, err)
		return
	}
	presets := CDNPresets
	if presets == nil {
		presets = []CDNPreset{}
	}
	WriteStatusJSON(w, http.StatusOK, StatusResponse{
		SchemaVersion: status.SchemaVersion,
		Pinned:        status.Pinned,
		Active:        status.Active,
		Required:      status.Required,
		Operation:     status.Operation,
		CDNPresets:    presets,
	})
}

func ServeProbe(w http.ResponseWriter, r *http.Request, runtime ArtifactRuntime) {
	if !decodeEmptyArtifactObject(w, r) {
		WriteError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if isNilRuntime(runtime) {
		WriteError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	result, err := runtime.Probe(r.Context())
	if err != nil {
		WriteUpstreamError(w, err)
		return
	}
	WriteStatusJSON(w, http.StatusOK, result)
}

func ServeAcquire(w http.ResponseWriter, r *http.Request, runtime ArtifactRuntime) {
	if r.Body == nil {
		WriteError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	var req AcquireRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxJSONBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		WriteError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	switch req.Source {
	case "github":
		if req.CDNPrefix != "" {
			WriteError(w, http.StatusBadRequest, "invalid_request")
			return
		}
	case "cdn":
		if strings.TrimSpace(req.CDNPrefix) == "" {
			WriteError(w, http.StatusBadRequest, "invalid_request")
			return
		}
	default:
		WriteError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if isNilRuntime(runtime) {
		WriteError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	result, err := runtime.Acquire(r.Context(), req.Source, req.CDNPrefix)
	if err != nil {
		WriteUpstreamError(w, err)
		return
	}
	status := result.HTTPStatus
	if status != http.StatusOK && status != http.StatusAccepted {
		status = http.StatusAccepted
	}
	WriteStatusJSON(w, status, result)
}

func ServeUpload(w http.ResponseWriter, r *http.Request, runtime ArtifactRuntime) {
	if !hasGzipContentType(r) || r.Body == nil {
		WriteError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	limited := &maxBytesTrackingReader{
		reader: http.MaxBytesReader(w, r.Body, MaxUploadBytes),
	}
	defer limited.Close()
	if r.ContentLength > MaxUploadBytes {
		WriteError(w, http.StatusRequestEntityTooLarge, "archive_too_large")
		return
	}
	if isNilRuntime(runtime) {
		WriteError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	result, err := runtime.Upload(r.Context(), limited, r.ContentLength)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if limited.exceeded || errors.As(err, &maxBytesErr) {
			WriteError(w, http.StatusRequestEntityTooLarge, "archive_too_large")
			return
		}
		WriteUpstreamError(w, err)
		return
	}
	if limited.exceeded {
		WriteError(w, http.StatusRequestEntityTooLarge, "archive_too_large")
		return
	}
	WriteStatusJSON(w, http.StatusOK, result)
}

func isNilRuntime(runtime ArtifactRuntime) bool {
	if runtime == nil {
		return true
	}
	rv := reflect.ValueOf(runtime)
	return rv.Kind() == reflect.Pointer && rv.IsNil()
}

func decodeEmptyArtifactObject(w http.ResponseWriter, r *http.Request) bool {
	if r.Body == nil {
		return true
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxJSONBody))
	decoder.DisallowUnknownFields()
	var empty struct{}
	if err := decoder.Decode(&empty); err != nil {
		return errors.Is(err, io.EOF)
	}
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

func hasGzipContentType(r *http.Request) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(values[0]))
	return err == nil && mediaType == "application/gzip"
}

type maxBytesTrackingReader struct {
	reader   io.ReadCloser
	exceeded bool
}

func (r *maxBytesTrackingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	var maxBytesErr *http.MaxBytesError
	if errors.As(err, &maxBytesErr) {
		r.exceeded = true
	}
	return n, err
}

func (r *maxBytesTrackingReader) Close() error {
	return r.reader.Close()
}

func WriteUpstreamError(w http.ResponseWriter, err error) {
	if code, status, ok := runtimeservice.ArtifactErrorCode(err); ok && code != "" {
		if knownStatus, known := runtimeservice.KnownArtifactErrorStatus(code); known {
			WriteError(w, knownStatus, code)
			return
		}
		if status >= 400 && status <= 599 {
			WriteError(w, status, code)
			return
		}
	}
	WriteError(w, http.StatusServiceUnavailable, "runtime_unavailable")
}

func WriteError(w http.ResponseWriter, status int, code string) {
	WriteStatusJSON(w, status, ErrorResponse{Code: code})
}

func WriteStatusJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
