package artifactproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

type fakeArtifactRuntime struct {
	status  runtimeservice.ArtifactStatus
	probe   runtimeservice.ArtifactProbeResult
	acquire runtimeservice.ArtifactOperationEnvelope
	upload  runtimeservice.ArtifactOperationEnvelope
	err     error
}

func (f *fakeArtifactRuntime) Status(context.Context) (runtimeservice.ArtifactStatus, error) {
	return f.status, f.err
}

func (f *fakeArtifactRuntime) Probe(context.Context) (runtimeservice.ArtifactProbeResult, error) {
	return f.probe, f.err
}

func (f *fakeArtifactRuntime) Acquire(context.Context, string, string) (runtimeservice.ArtifactOperationEnvelope, error) {
	return f.acquire, f.err
}

func (f *fakeArtifactRuntime) Upload(_ context.Context, body io.Reader, _ int64) (runtimeservice.ArtifactOperationEnvelope, error) {
	if _, err := io.Copy(io.Discard, body); err != nil {
		return runtimeservice.ArtifactOperationEnvelope{}, err
	}
	return f.upload, f.err
}

func TestServeResourceValidationAndNilRuntime(t *testing.T) {
	const pinnedID = "sha256:4444444444444444444444444444444444444444444444444444444444444444"
	runtime := &fakeArtifactRuntime{
		status: runtimeservice.ArtifactStatus{
			SchemaVersion: 1,
			Pinned:        runtimeservice.ArtifactPinned{Version: "8.0.55", ArtifactID: pinnedID},
			Required:      true,
		},
		probe: runtimeservice.ArtifactProbeResult{Reachable: true},
		acquire: runtimeservice.ArtifactOperationEnvelope{
			Operation:  runtimeservice.ArtifactOperation{ID: "op-1", Source: "github", Phase: "downloading"},
			HTTPStatus: http.StatusAccepted,
		},
		upload: runtimeservice.ArtifactOperationEnvelope{
			Operation:  runtimeservice.ArtifactOperation{ID: "op-2", Source: "upload", Phase: "succeeded"},
			HTTPStatus: http.StatusOK,
		},
	}

	if !IsResource(ResourceStatus) || !IsResource(ResourceProbe) || !IsResource(ResourceAcquire) || !IsResource(ResourceUpload) || IsResource("runtime/artifact/other") {
		t.Fatal("unexpected IsResource result")
	}

	// Nil runtime returns 503 runtime_unavailable.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/runtime/artifact", nil)
	ServeResource(rec, req, nil, ResourceStatus)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil runtime status = %d, want 503", rec.Code)
	}

	// Query string rejected with 400 invalid_request.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/runtime/artifact?x=1", nil)
	ServeResource(rec, req, runtime, ResourceStatus)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("query string status = %d, want 400", rec.Code)
	}

	// Valid status includes cdnPresets array.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/runtime/artifact", nil)
	ServeResource(rec, req, runtime, ResourceStatus)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got StatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.CDNPresets == nil || !got.Required || got.Pinned.ArtifactID != pinnedID {
		t.Fatalf("unexpected status response: %+v", got)
	}

	// Acquire over 4 KiB rejected.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/runtime/artifact/acquire", strings.NewReader(`{"source":"cdn","cdnPrefix":"https://mirror.example/`+strings.Repeat("a", MaxJSONBody)+`"}`))
	ServeResource(rec, req, runtime, ResourceAcquire)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("oversized acquire = %d, want 400", rec.Code)
	}

	// Upload with application/gzip succeeds.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/runtime/artifact/upload", bytes.NewReader([]byte("gzip")))
	req.Header.Set("Content-Type", "application/gzip")
	ServeResource(rec, req, runtime, ResourceUpload)
	if rec.Code != http.StatusOK {
		t.Fatalf("upload = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}
