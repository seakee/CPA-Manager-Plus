package main

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/freshbootstrap"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

const (
	freshArtifactMaxJSONBody    = 4 << 10  // 4 KiB
	freshArtifactMaxUploadBytes = 64 << 20 // 64 MiB
)

type freshArtifactCDNPreset struct {
	ID         string `json:"id"`
	Prefix     string `json:"prefix"`
	ThirdParty bool   `json:"thirdParty"`
}

// freshArtifactCDNPresets holds the release-selected third-party CDN prefixes.
// It is intentionally empty in S2a; presets are chosen at release time.
var freshArtifactCDNPresets = []freshArtifactCDNPreset{}

type freshArtifactStatusResponse struct {
	SchemaVersion int                               `json:"schemaVersion"`
	Pinned        runtimeservice.ArtifactPinned     `json:"pinned"`
	Active        *runtimeservice.ArtifactActive    `json:"active"`
	Required      bool                              `json:"required"`
	Operation     *runtimeservice.ArtifactOperation `json:"operation"`
	CDNPresets    []freshArtifactCDNPreset          `json:"cdnPresets"`
}

type freshArtifactAcquireRequest struct {
	Source    string `json:"source"`
	CDNPrefix string `json:"cdnPrefix,omitempty"`
}

type freshArtifactErrorResponse struct {
	Code string `json:"code"`
}

func runtimeArtifactResource(resource string) bool {
	switch resource {
	case "runtime/artifact", "runtime/artifact/probe", "runtime/artifact/acquire", "runtime/artifact/upload":
		return true
	default:
		return false
	}
}

func (a *freshApplication) fixedArtifact(w http.ResponseWriter, r *http.Request) {
	token, ok := freshbootstrap.SessionToken(r)
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	op, err := a.authority.Authorize(r.Context(), token)
	if err != nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	resource := strings.TrimPrefix(r.URL.Path, bootstrap.FixedBootstrapAPIPath)
	a.runtimeArtifact(w, r, op, resource)
}

func (a *freshApplication) runtimeArtifact(w http.ResponseWriter, r *http.Request, _ bootstrap.FreshOperation, resource string) {
	if r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		writeFreshArtifactError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	switch resource {
	case "runtime/artifact":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		a.runtimeArtifactStatus(w, r)
	case "runtime/artifact/probe":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		a.runtimeArtifactProbe(w, r)
	case "runtime/artifact/acquire":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		a.runtimeArtifactAcquire(w, r)
	case "runtime/artifact/upload":
		if r.Method != http.MethodPut {
			w.Header().Set("Allow", http.MethodPut)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		a.runtimeArtifactUpload(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *freshApplication) runtimeArtifactStatus(w http.ResponseWriter, r *http.Request) {
	if a.artifact == nil {
		writeFreshArtifactError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	status, err := a.artifact.Status(r.Context())
	if err != nil {
		writeFreshArtifactUpstreamError(w, err)
		return
	}
	presets := freshArtifactCDNPresets
	if presets == nil {
		presets = []freshArtifactCDNPreset{}
	}
	writeFreshArtifactStatusJSON(w, http.StatusOK, freshArtifactStatusResponse{
		SchemaVersion: status.SchemaVersion,
		Pinned:        status.Pinned,
		Active:        status.Active,
		Required:      status.Required,
		Operation:     status.Operation,
		CDNPresets:    presets,
	})
}

func (a *freshApplication) runtimeArtifactProbe(w http.ResponseWriter, r *http.Request) {
	if !decodeEmptyArtifactObject(w, r) {
		writeFreshArtifactError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if a.artifact == nil {
		writeFreshArtifactError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	result, err := a.artifact.Probe(r.Context())
	if err != nil {
		writeFreshArtifactUpstreamError(w, err)
		return
	}
	writeFreshArtifactStatusJSON(w, http.StatusOK, result)
}

func (a *freshApplication) runtimeArtifactAcquire(w http.ResponseWriter, r *http.Request) {
	if r.Body == nil {
		writeFreshArtifactError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	var req freshArtifactAcquireRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, freshArtifactMaxJSONBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeFreshArtifactError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		writeFreshArtifactError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	switch req.Source {
	case "github":
		if req.CDNPrefix != "" {
			writeFreshArtifactError(w, http.StatusBadRequest, "invalid_request")
			return
		}
	case "cdn":
		if strings.TrimSpace(req.CDNPrefix) == "" {
			writeFreshArtifactError(w, http.StatusBadRequest, "invalid_request")
			return
		}
	default:
		writeFreshArtifactError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if a.artifact == nil {
		writeFreshArtifactError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	result, err := a.artifact.Acquire(r.Context(), req.Source, req.CDNPrefix)
	if err != nil {
		writeFreshArtifactUpstreamError(w, err)
		return
	}
	status := result.HTTPStatus
	if status != http.StatusOK && status != http.StatusAccepted {
		status = http.StatusAccepted
	}
	writeFreshArtifactStatusJSON(w, status, result)
}

func (a *freshApplication) runtimeArtifactUpload(w http.ResponseWriter, r *http.Request) {
	if !hasGzipContentType(r) || r.Body == nil {
		writeFreshArtifactError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	limited := &maxBytesTrackingReader{
		reader: http.MaxBytesReader(w, r.Body, freshArtifactMaxUploadBytes),
	}
	defer limited.Close()
	if r.ContentLength > freshArtifactMaxUploadBytes {
		writeFreshArtifactError(w, http.StatusRequestEntityTooLarge, "archive_too_large")
		return
	}
	if a.artifact == nil {
		writeFreshArtifactError(w, http.StatusServiceUnavailable, "runtime_unavailable")
		return
	}
	result, err := a.artifact.Upload(r.Context(), limited, r.ContentLength)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if limited.exceeded || errors.As(err, &maxBytesErr) {
			writeFreshArtifactError(w, http.StatusRequestEntityTooLarge, "archive_too_large")
			return
		}
		writeFreshArtifactUpstreamError(w, err)
		return
	}
	if limited.exceeded {
		writeFreshArtifactError(w, http.StatusRequestEntityTooLarge, "archive_too_large")
		return
	}
	writeFreshArtifactStatusJSON(w, http.StatusOK, result)
}

func decodeEmptyArtifactObject(w http.ResponseWriter, r *http.Request) bool {
	if r.Body == nil {
		return true
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, freshArtifactMaxJSONBody))
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

func writeFreshArtifactUpstreamError(w http.ResponseWriter, err error) {
	if code, status, ok := runtimeservice.ArtifactErrorCode(err); ok && status >= 400 && status <= 599 && code != "" {
		writeFreshArtifactError(w, status, code)
		return
	}
	writeFreshArtifactError(w, http.StatusServiceUnavailable, "runtime_unavailable")
}

func writeFreshArtifactError(w http.ResponseWriter, status int, code string) {
	writeFreshArtifactStatusJSON(w, status, freshArtifactErrorResponse{Code: code})
}

func writeFreshArtifactStatusJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	writeFreshJSON(w, v)
}
