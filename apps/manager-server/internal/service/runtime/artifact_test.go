package runtime

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNewArtifactClientTransportAndTimeouts(t *testing.T) {
	client := NewArtifactClient("http://cpamp-runtime:18318/", testRuntimeToken)
	if client.baseURL != "http://cpamp-runtime:18318" {
		t.Fatalf("baseURL = %q, want trimmed base URL", client.baseURL)
	}
	if client.statusHTTPClient.Timeout != 5*time.Second {
		t.Fatalf("status timeout = %s, want 5s", client.statusHTTPClient.Timeout)
	}
	if client.probeHTTPClient.Timeout != 10*time.Second {
		t.Fatalf("probe timeout = %s, want 10s", client.probeHTTPClient.Timeout)
	}
	if client.acquireHTTPClient.Timeout != 15*time.Second {
		t.Fatalf("acquire timeout = %s, want 15s", client.acquireHTTPClient.Timeout)
	}
	if client.uploadHTTPClient.Timeout != 15*time.Minute {
		t.Fatalf("upload timeout = %s, want 15m", client.uploadHTTPClient.Timeout)
	}
	for _, hc := range []*http.Client{
		client.statusHTTPClient,
		client.probeHTTPClient,
		client.acquireHTTPClient,
		client.uploadHTTPClient,
	} {
		transport, ok := hc.Transport.(*http.Transport)
		if !ok {
			t.Fatalf("Transport = %T, want *http.Transport", hc.Transport)
		}
		if transport.Proxy != nil {
			t.Fatal("Transport.Proxy must be nil")
		}
		if transport == http.DefaultTransport {
			t.Fatal("Transport reused http.DefaultTransport instead of cloning it")
		}
	}
}

func TestArtifactClientRefusesRedirects(t *testing.T) {
	var targetHits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirector.Close()

	client := NewArtifactClient(redirector.URL, testRuntimeToken)
	if _, err := client.Status(t.Context()); err == nil {
		t.Fatal("Status() followed redirect or accepted 302")
	}
	if targetHits != 0 {
		t.Fatalf("redirect target hits = %d, want 0", targetHits)
	}
}

func TestArtifactClientStatusProbeAcquireAndUpload(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "runtime.token")
	if err := os.WriteFile(tokenFile, []byte(testRuntimeToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	archiveBytes := bytes.Repeat([]byte("cpa-archive-segment-"), 2048)
	archiveDigest := sha256.Sum256(archiveBytes)

	var gotUploadDigest [32]byte
	var gotUploadContentLength int64
	var gotUploadContentType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+testRuntimeToken {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runtime/artifact":
			_, _ = w.Write([]byte(`{"schemaVersion":1,"pinned":{"version":"8.0.55","artifactId":"` + testArtifactID + `"},"active":null,"required":true,"operation":null}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/artifact/probe":
			body, err := io.ReadAll(r.Body)
			if err != nil || string(body) != "{}" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":"invalid_request"}`))
				return
			}
			_, _ = w.Write([]byte(`{"reachable":true,"code":""}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v1/runtime/artifact/acquire":
			var req map[string]string
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":"invalid_request"}`))
				return
			}
			if req["source"] == "cdn" && req["cdnPrefix"] == "https://mirror.example.com/gh" {
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(`{"operation":{"id":"op-cdn-1","source":"cdn","phase":"downloading","receivedBytes":1024,"totalBytes":4096,"code":""}}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"operation":{"id":"op-gh-1","source":"github","phase":"succeeded","receivedBytes":4096,"totalBytes":4096,"code":""}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/v1/runtime/artifact/upload":
			gotUploadContentType = r.Header.Get("Content-Type")
			gotUploadContentLength = r.ContentLength
			hasher := sha256.New()
			if _, err := io.Copy(hasher, r.Body); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":"invalid_request"}`))
				return
			}
			copy(gotUploadDigest[:], hasher.Sum(nil))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"operation":{"id":"op-up-1","source":"upload","phase":"succeeded","receivedBytes":40960,"totalBytes":40960,"code":""}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := NewArtifactClientWithTokenSource(server.URL, NewFileRuntimeTokenSource(tokenFile))

	status, err := client.Status(t.Context())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	wantStatus := ArtifactStatus{
		SchemaVersion: 1,
		Pinned:        ArtifactPinned{Version: "8.0.55", ArtifactID: testArtifactID},
		Active:        nil,
		Required:      true,
		Operation:     nil,
	}
	if !reflect.DeepEqual(status, wantStatus) {
		t.Fatalf("Status() = %#v, want %#v", status, wantStatus)
	}

	probe, err := client.Probe(t.Context())
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if !probe.Reachable || probe.Code != "" {
		t.Fatalf("Probe() = %#v", probe)
	}

	acquiredCDN, err := client.Acquire(t.Context(), "cdn", "https://mirror.example.com/gh")
	if err != nil {
		t.Fatalf("Acquire(cdn) error = %v", err)
	}
	if acquiredCDN.HTTPStatus != http.StatusAccepted || acquiredCDN.Operation.ID != "op-cdn-1" || acquiredCDN.Operation.Phase != "downloading" {
		t.Fatalf("Acquire(cdn) = %#v", acquiredCDN)
	}

	acquiredGitHub, err := client.Acquire(t.Context(), "github", "")
	if err != nil {
		t.Fatalf("Acquire(github) error = %v", err)
	}
	if acquiredGitHub.HTTPStatus != http.StatusOK || acquiredGitHub.Operation.Phase != "succeeded" {
		t.Fatalf("Acquire(github) = %#v", acquiredGitHub)
	}

	uploaded, err := client.Upload(t.Context(), bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if uploaded.HTTPStatus != http.StatusOK || uploaded.Operation.Source != "upload" || uploaded.Operation.Phase != "succeeded" {
		t.Fatalf("Upload() = %#v", uploaded)
	}
	if gotUploadContentType != "application/gzip" {
		t.Fatalf("Upload Content-Type = %q, want application/gzip", gotUploadContentType)
	}
	if gotUploadContentLength != int64(len(archiveBytes)) {
		t.Fatalf("Upload ContentLength = %d, want %d", gotUploadContentLength, len(archiveBytes))
	}
	if gotUploadDigest != archiveDigest {
		t.Fatalf("Upload digest = %s, want %s", hex.EncodeToString(gotUploadDigest[:]), hex.EncodeToString(archiveDigest[:]))
	}
}

func TestArtifactClientMapsSupervisorErrorCodesAndRejectsUnknownFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/runtime/artifact":
			_, _ = w.Write([]byte(`{"schemaVersion":1,"pinned":{"version":"8.0.55","artifactId":"` + testArtifactID + `"},"active":null,"required":true,"operation":null,"unexpected":true}`))
		case "/v1/runtime/artifact/acquire":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"code":"acquisition_in_progress"}`))
		case "/v1/runtime/artifact/upload":
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"code":"artifact_mismatch"}`))
		}
	}))
	defer server.Close()

	client := NewArtifactClient(server.URL, testRuntimeToken)
	if _, err := client.Status(t.Context()); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Status() accepted unknown JSON field: %v", err)
	}

	_, err := client.Acquire(t.Context(), "github", "")
	code, status, ok := ArtifactErrorCode(err)
	if !ok || status != http.StatusConflict || code != "acquisition_in_progress" {
		t.Fatalf("Acquire() error = %v (code=%q status=%d ok=%v)", err, code, status, ok)
	}

	_, err = client.Upload(t.Context(), strings.NewReader("archive"), 7)
	code, status, ok = ArtifactErrorCode(err)
	if !ok || status != http.StatusUnprocessableEntity || code != "artifact_mismatch" {
		t.Fatalf("Upload() error = %v (code=%q status=%d ok=%v)", err, code, status, ok)
	}
}
