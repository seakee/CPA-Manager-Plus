package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/artifact"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/cpaprocess"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/journal"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/lifecycle"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/protocol"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/readiness"
	"github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/selection"
	runtimeupdate "github.com/seakee/cpa-manager-plus/apps/runtime-supervisor/internal/update"
)

type routeTestHarness struct {
	handler       http.Handler
	stageRoot     string
	selectionRoot string
	pinnedID      string
	archive       []byte
}

func newRouteTestHarness(t *testing.T, opts ...lifecycle.AcquisitionOption) *routeTestHarness {
	t.Helper()
	root := t.TempDir()
	supervisorRoot := filepath.Join(root, "supervisor")
	stageRoot := filepath.Join(supervisorRoot, "artifacts", "cpa")
	selectionRoot := filepath.Join(supervisorRoot, "active", "cpa")
	missingBundled := filepath.Join(root, "missing-cpa")
	manifestPath := filepath.Join(root, "cpa-artifact.json")

	binary := []byte("#!/bin/sh\necho s2a-route-cpa\n")
	sum := sha256.Sum256(binary)
	pinnedID := "sha256:" + hex.EncodeToString(sum[:])
	archive := buildRouteTarGzip(t, binary, []byte("MIT License\n"))

	manifestJSON := fmt.Sprintf(
		"{\"schemaVersion\":1,\"engine\":\"cpa\",\"version\":\"8.0.55\",\"artifactId\":%q}\n",
		pinnedID,
	)
	if err := os.WriteFile(manifestPath, []byte(manifestJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	stageStore, err := runtimeupdate.NewStore(stageRoot)
	if err != nil {
		t.Fatal(err)
	}
	selectionStore, err := selection.NewStore(selectionRoot, stageStore)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := selectionStore.Load(selection.Bundled(missingBundled, manifestPath))
	if err != nil {
		t.Fatal(err)
	}
	artifactObserver := artifact.NewObserver(selected.ExecutablePath, selected.MetadataPath)
	_ = artifactObserver.RefreshFrom(selected.ExecutablePath, selected.MetadataPath)

	child := cpaprocess.NewManager(func(spec cpaprocess.StartSpec) error {
		meta := manifestPath
		if spec.Executable != missingBundled {
			meta = filepath.Join(filepath.Dir(spec.Executable), "artifact.json")
		}
		return artifactObserver.RefreshFrom(spec.Executable, meta)
	})
	observer, err := readiness.New(child, "127.0.0.1:18399")
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(t.Context(), filepath.Join(supervisorRoot, "operations.sqlite"), journal.Options{})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := lifecycle.NewExecutor(
		journal.Authority{RuntimeIdentity: "runtime-01", RuntimeGeneration: 41},
		store,
		child,
		selected.ExecutablePath,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = executor.Close() })

	if err := executor.EnableActivateUpdate(selected, artifactObserver, selectionStore, observer); err != nil {
		t.Fatal(err)
	}
	protocolHandler, err := protocol.NewHandler(protocol.Config{
		RuntimeIdentity:   "runtime-01",
		RuntimeGeneration: 41,
		Token:             "runtime-token",
		Start:             executor,
		Stop:              executor,
		Restart:           executor,
		Status:            observer,
		Recovery:          executor,
		Artifact:          artifactObserver,
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg := config{
		runtimeIdentity:     "runtime-01",
		runtimeGeneration:   41,
		token:               "runtime-token",
		journalPath:         filepath.Join(supervisorRoot, "operations.sqlite"),
		cpaExecutable:       missingBundled,
		cpaArtifactManifest: manifestPath,
	}
	rh := &runtimeHandler{Handler: protocolHandler, executor: executor}
	allOpts := append([]lifecycle.AcquisitionOption{lifecycle.WithAcquisitionPlatform("linux", "amd64")}, opts...)
	if err := enableArtifactRoutes(cfg, rh, allOpts...); err != nil {
		t.Fatal(err)
	}

	return &routeTestHarness{
		handler:       rh,
		stageRoot:     stageRoot,
		selectionRoot: selectionRoot,
		pinnedID:      pinnedID,
		archive:       archive,
	}
}

func TestArtifactRoutesRequireConstantTimeBearerAuthorization(t *testing.T) {
	h := newRouteTestHarness(t)
	routes := []struct {
		method string
		path   string
	}{
		{method: http.MethodGet, path: artifactStatusPath},
		{method: http.MethodPost, path: artifactProbePath},
		{method: http.MethodPost, path: artifactAcquirePath},
		{method: http.MethodPut, path: artifactUploadPath},
	}
	authHeaders := []string{
		"",
		"Basic dXNlcjpwYXNz",
		"Bearer",
		"Bearer wrong-token",
		"Bearer runtime-token extra",
	}

	for _, route := range routes {
		for _, auth := range authHeaders {
			req := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			rec := httptest.NewRecorder()
			h.handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s with auth %q status = %d, want 401", route.method, route.path, auth, rec.Code)
			}
			if rec.Header().Get("WWW-Authenticate") != "Bearer" {
				t.Fatalf("%s %s WWW-Authenticate = %q, want Bearer", route.method, route.path, rec.Header().Get("WWW-Authenticate"))
			}
			var errResp artifactErrorResponse
			if err := json.NewDecoder(rec.Body).Decode(&errResp); err != nil || errResp.Code != lifecycle.ArtifactCodeUnauthorized {
				t.Fatalf("%s %s error body = %+v, err=%v", route.method, route.path, errResp, err)
			}
		}
	}
}

func TestArtifactRoutesRejectUnknownFieldsAndInvalidRequests(t *testing.T) {
	h := newRouteTestHarness(t)

	invalidProbes := []string{
		"",
		`{"unexpected":true}`,
		`{}{}`,
		strings.Repeat(" ", maxArtifactJSONRequestBytes+1),
	}
	for _, body := range invalidProbes {
		req := httptest.NewRequest(http.MethodPost, artifactProbePath, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer runtime-token")
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST probe(%q) status = %d, want 400", body, rec.Code)
		}
		assertArtifactErrorCode(t, rec, lifecycle.ArtifactCodeInvalidRequest)
	}

	invalidAcquires := []string{
		"",
		`{}`,
		`{"source":"unknown"}`,
		`{"source":"github","cdnPrefix":"https://cdn.example.com"}`,
		`{"source":"cdn"}`,
		`{"source":"cdn","cdnPrefix":"http://cdn.example.com"}`,
		`{"source":"cdn","cdnPrefix":"https://user@cdn.example.com"}`,
		`{"source":"github","unknownField":1}`,
		`{"source":"github"}` + strings.Repeat(" ", maxArtifactJSONRequestBytes),
	}
	for _, body := range invalidAcquires {
		req := httptest.NewRequest(http.MethodPost, artifactAcquirePath, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer runtime-token")
		rec := httptest.NewRecorder()
		h.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("POST acquire(%d bytes) status = %d, want 400", len(body), rec.Code)
		}
		assertArtifactErrorCode(t, rec, lifecycle.ArtifactCodeInvalidRequest)
	}

	// Wrong HTTP method returns 405 with invalid_request.
	req := httptest.NewRequest(http.MethodPost, artifactStatusPath, nil)
	req.Header.Set("Authorization", "Bearer runtime-token")
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST %s status = %d, want 405", artifactStatusPath, rec.Code)
	}
	assertArtifactErrorCode(t, rec, lifecycle.ArtifactCodeInvalidRequest)
}

func TestArtifactUploadOver64MiBReturns413AndSetsDeadlines(t *testing.T) {
	h := newRouteTestHarness(t)

	// Streaming reader exceeding 64 MiB without Content-Length header.
	oversizeStream := io.LimitReader(zeroReader{}, runtimeupdate.MaxAcquisitionArchiveBytes+1024)
	req := httptest.NewRequest(http.MethodPut, artifactUploadPath, oversizeStream)
	req.ContentLength = -1
	req.Header.Set("Authorization", "Bearer runtime-token")
	req.Header.Set("Content-Type", "application/gzip")
	writer := &readWriteDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	h.handler.ServeHTTP(writer, req)
	if writer.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("PUT oversize upload status = %d, want 413 (body=%s)", writer.Code, writer.Body.String())
	}
	assertArtifactErrorCode(t, writer.ResponseRecorder, lifecycle.ArtifactCodeArchiveTooLarge)
	if len(writer.readDeadlines) != 2 || len(writer.writeDeadlines) != 2 {
		t.Fatalf("upload deadlines read=%d write=%d, want 2 each", len(writer.readDeadlines), len(writer.writeDeadlines))
	}
	if !writer.readDeadlines[0].After(time.Now().Add(9*time.Minute)) ||
		!writer.writeDeadlines[0].After(time.Now().Add(9*time.Minute)) {
		t.Fatalf("upload deadlines = read %v, write %v; want ~10m budget", writer.readDeadlines[0], writer.writeDeadlines[0])
	}

	entries, err := os.ReadDir(h.stageRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("oversize upload left stage state: %v, %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(h.selectionRoot, "selection.json")); !os.IsNotExist(err) {
		t.Fatalf("oversize upload created selection.json: %v", err)
	}
}

func TestArtifactRoutesUploadAndIdempotentAcquireAndProtocolCoexistence(t *testing.T) {
	h := newRouteTestHarness(t)

	// Initial GET /v1/runtime/artifact shows active=null, required=true.
	statusRec := httptest.NewRecorder()
	statusReq := httptest.NewRequest(http.MethodGet, artifactStatusPath, nil)
	statusReq.Header.Set("Authorization", "Bearer runtime-token")
	h.handler.ServeHTTP(statusRec, statusReq)
	if statusRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, %s", statusRec.Code, statusRec.Body.String())
	}
	var before lifecycle.ArtifactStatusResponse
	if err := json.NewDecoder(statusRec.Body).Decode(&before); err != nil {
		t.Fatal(err)
	}
	if before.Active != nil || !before.Required || before.Pinned.Version != "8.0.55" || string(before.Pinned.ArtifactID) != h.pinnedID {
		t.Fatalf("initial status = %+v", before)
	}

	// Synchronous PUT /v1/runtime/artifact/upload succeeds with 200.
	uploadReq := httptest.NewRequest(http.MethodPut, artifactUploadPath, bytes.NewReader(h.archive))
	uploadReq.Header.Set("Authorization", "Bearer runtime-token")
	uploadReq.Header.Set("Content-Type", "application/gzip")
	uploadRec := httptest.NewRecorder()
	h.handler.ServeHTTP(uploadRec, uploadReq)
	if uploadRec.Code != http.StatusOK {
		t.Fatalf("PUT upload = %d, %s", uploadRec.Code, uploadRec.Body.String())
	}
	var uploadResp lifecycle.ArtifactOperationResponse
	if err := json.NewDecoder(uploadRec.Body).Decode(&uploadResp); err != nil {
		t.Fatal(err)
	}
	if uploadResp.Operation.Source != lifecycle.ArtifactSourceUpload ||
		uploadResp.Operation.Phase != lifecycle.ArtifactPhaseSucceeded ||
		uploadResp.Operation.Code != "" ||
		uploadResp.Operation.ReceivedBytes != int64(len(h.archive)) {
		t.Fatalf("upload operation = %+v", uploadResp.Operation)
	}

	// Subsequent POST /v1/runtime/artifact/acquire returns 200 (idempotent).
	acqReq := httptest.NewRequest(http.MethodPost, artifactAcquirePath, strings.NewReader(`{"source":"github"}`))
	acqReq.Header.Set("Authorization", "Bearer runtime-token")
	acqRec := httptest.NewRecorder()
	h.handler.ServeHTTP(acqRec, acqReq)
	if acqRec.Code != http.StatusOK {
		t.Fatalf("idempotent POST acquire = %d, %s", acqRec.Code, acqRec.Body.String())
	}

	// Second PUT /v1/runtime/artifact/upload returns 409 artifact_already_active.
	secondUploadReq := httptest.NewRequest(http.MethodPut, artifactUploadPath, bytes.NewReader(h.archive))
	secondUploadReq.Header.Set("Authorization", "Bearer runtime-token")
	secondUploadReq.Header.Set("Content-Type", "application/gzip")
	secondUploadRec := httptest.NewRecorder()
	h.handler.ServeHTTP(secondUploadRec, secondUploadReq)
	if secondUploadRec.Code != http.StatusConflict {
		t.Fatalf("second PUT upload = %d, want 409 (%s)", secondUploadRec.Code, secondUploadRec.Body.String())
	}
	assertArtifactErrorCode(t, secondUploadRec, lifecycle.ArtifactCodeArtifactAlreadyActive)

	// Post-commit failure maps to 500 recovery_required.
	recoveryHarness := newRouteTestHarness(t, lifecycle.WithAcquisitionPostCommitHooks(
		func() error { return fmt.Errorf("post-commit refresh failure") },
		nil,
	))
	recReq := httptest.NewRequest(http.MethodPut, artifactUploadPath, bytes.NewReader(recoveryHarness.archive))
	recReq.Header.Set("Authorization", "Bearer runtime-token")
	recReq.Header.Set("Content-Type", "application/gzip")
	recResp := httptest.NewRecorder()
	recoveryHarness.handler.ServeHTTP(recResp, recReq)
	if recResp.Code != http.StatusInternalServerError {
		t.Fatalf("post-commit failure PUT upload = %d, want 500 (%s)", recResp.Code, recResp.Body.String())
	}
	assertArtifactErrorCode(t, recResp, lifecycle.ArtifactCodeRecoveryRequired)

	// Runtime Protocol /v1/runtime/status now reports the acquired ActiveGatewayArtifact.
	rtStatus := requestRuntime(t, h.handler, "/v1/runtime/status")
	if rtStatus.ActiveGatewayArtifact == nil || rtStatus.ActiveGatewayArtifact.ArtifactID != h.pinnedID ||
		rtStatus.ActiveGatewayArtifact.Version != "8.0.55" {
		t.Fatalf("runtime status after upload = %+v", rtStatus)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

type readWriteDeadlineRecorder struct {
	*httptest.ResponseRecorder
	readDeadlines  []time.Time
	writeDeadlines []time.Time
}

func (r *readWriteDeadlineRecorder) SetReadDeadline(d time.Time) error {
	r.readDeadlines = append(r.readDeadlines, d)
	return nil
}

func (r *readWriteDeadlineRecorder) SetWriteDeadline(d time.Time) error {
	r.writeDeadlines = append(r.writeDeadlines, d)
	return nil
}

func assertArtifactErrorCode(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var got artifactErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode error response %q: %v", rec.Body.String(), err)
	}
	if got.Code != want {
		t.Fatalf("error code = %q, want %q", got.Code, want)
	}
}

func buildRouteTarGzip(t *testing.T, binary, license []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range []struct {
		name string
		data []byte
	}{
		{name: "cli-proxy-api", data: binary},
		{name: "LICENSE", data: license},
	} {
		if err := tw.WriteHeader(&tar.Header{
			Name:     entry.name,
			Typeflag: tar.TypeReg,
			Mode:     0o755,
			Size:     int64(len(entry.data)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestEnableArtifactRoutesNoOpWhenExecutorNil(t *testing.T) {
	cfg := loadTestConfig(t, fixedGeneration(41))
	rh, err := newRuntimeHandler(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := enableArtifactRoutes(cfg, rh); err != nil {
		t.Fatalf("enableArtifactRoutes(read-only) error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, artifactStatusPath, nil)
	req.Header.Set("Authorization", "Bearer runtime-token")
	rec := httptest.NewRecorder()
	rh.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("read-only GET artifact status = %d, want 409", rec.Code)
	}
	assertArtifactErrorCode(t, rec, lifecycle.ArtifactCodeUnsupportedPlatform)
}

func TestRunStartsAndAnswersStatusWithMinimalAdoptionEnvAndPrefixedManifestVersion(t *testing.T) {
	for _, tc := range []struct {
		name             string
		manifestMode     string
		goos             string
		goarch           string
		wantArtifactCode string
		wantHTTPStatus   int
	}{
		{
			name:             "v-prefixed version manifest from adoption harness returns 500 install_failed on artifact routes",
			manifestMode:     "v-prefixed",
			goos:             "linux",
			goarch:           "amd64",
			wantArtifactCode: lifecycle.ArtifactCodeInstallFailed,
			wantHTTPStatus:   http.StatusInternalServerError,
		},
		{
			name:             "configured missing manifest file returns 500 install_failed on artifact routes",
			manifestMode:     "missing-file",
			goos:             "linux",
			goarch:           "amd64",
			wantArtifactCode: lifecycle.ArtifactCodeInstallFailed,
			wantHTTPStatus:   http.StatusInternalServerError,
		},
		{
			name:             "unset manifest path returns 409 unsupported_platform on artifact routes",
			manifestMode:     "unset",
			goos:             "linux",
			goarch:           "amd64",
			wantArtifactCode: lifecycle.ArtifactCodeUnsupportedPlatform,
			wantHTTPStatus:   http.StatusConflict,
		},
		{
			name:             "unsupported platform darwin/amd64 returns 409 unsupported_platform on GET, probe, acquire, and upload",
			manifestMode:     "canonical",
			goos:             "darwin",
			goarch:           "amd64",
			wantArtifactCode: lifecycle.ArtifactCodeUnsupportedPlatform,
			wantHTTPStatus:   http.StatusConflict,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			targetRoot := t.TempDir()
			cpaPath := filepath.Join(targetRoot, "cli-proxy-api")
			binary := []byte("#!/bin/sh\necho adoption-cpa\n")
			if err := os.WriteFile(cpaPath, binary, 0o755); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(binary)
			artifactID := "sha256:" + hex.EncodeToString(sum[:])
			archive := buildRouteTarGzip(t, binary, []byte("MIT License\n"))

			var manifestPath string
			switch tc.manifestMode {
			case "canonical":
				manifestPath = filepath.Join(targetRoot, "artifact.json")
				content := fmt.Sprintf(`{"schemaVersion":1,"engine":"cpa","version":"8.0.55","artifactId":%q}`, artifactID)
				if err := os.WriteFile(manifestPath, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			case "v-prefixed":
				manifestPath = filepath.Join(targetRoot, "artifact.json")
				content := fmt.Sprintf(`{"schemaVersion":1,"engine":"cpa","version":"v8.0.55","artifactId":%q}`, artifactID)
				if err := os.WriteFile(manifestPath, []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing-file":
				manifestPath = filepath.Join(targetRoot, "missing-artifact.json")
			case "unset":
				manifestPath = ""
			}
			env := map[string]string{
				"CPAMP_RUNTIME_ADDR":          "127.0.0.1:0",
				"CPAMP_RUNTIME_IDENTITY":      "isolated-adopt-runtime",
				"CPAMP_RUNTIME_TOKEN":         "isolated-runtime-transport-token",
				"CPAMP_RUNTIME_JOURNAL_PATH":  filepath.Join(targetRoot, "operations.sqlite"),
				"CPAMP_CPA_EXECUTABLE":        cpaPath,
				"CPAMP_CPA_ARTIFACT_MANIFEST": manifestPath,
				"CPAMP_RUNTIME_CPA_ADDR":      "127.0.0.1:18399",
			}
			cfg, err := loadConfig(func(key string) string { return env[key] }, fixedGeneration(77))
			if err != nil {
				t.Fatalf("loadConfig() error = %v", err)
			}

			rh, err := newRuntimeHandler(t.Context(), cfg)
			if err != nil {
				t.Fatalf("newRuntimeHandler() error = %v", err)
			}
			t.Cleanup(func() { _ = rh.Close() })

			if err := enableArtifactRoutes(cfg, rh, lifecycle.WithAcquisitionPlatform(tc.goos, tc.goarch)); err != nil {
				t.Fatalf("enableArtifactRoutes() error = %v", err)
			}

			// /v1/runtime/status stays 200 in every configuration.
			req := httptest.NewRequest(http.MethodGet, "/v1/runtime/status", nil)
			req.Header.Set("Authorization", "Bearer isolated-runtime-transport-token")
			req.Header.Set(protocol.ArtifactObservationHeader, protocol.ArtifactObservationFeature)
			rec := httptest.NewRecorder()
			rh.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /v1/runtime/status = %d (%s), want 200", rec.Code, rec.Body.String())
			}
			var status runtimeResponse
			if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
				t.Fatalf("decode /v1/runtime/status: %v", err)
			}
			if status.RuntimeIdentity != "isolated-adopt-runtime" || status.ActiveGatewayArtifact == nil || status.ActiveGatewayArtifact.ArtifactID != artifactID {
				t.Fatalf("/v1/runtime/status = %+v, want identity isolated-adopt-runtime and artifact %s", status, artifactID)
			}

			// All four artifact routes return the expected error class (409 unsupported_platform vs 500 install_failed).
			for _, route := range []struct {
				method      string
				path        string
				contentType string
				body        []byte
			}{
				{method: http.MethodGet, path: artifactStatusPath},
				{method: http.MethodPost, path: artifactProbePath, contentType: "application/json", body: []byte(`{}`)},
				{method: http.MethodPost, path: artifactAcquirePath, contentType: "application/json", body: []byte(`{"source":"github"}`)},
				{method: http.MethodPut, path: artifactUploadPath, contentType: "application/gzip", body: archive},
			} {
				artReq := httptest.NewRequest(route.method, route.path, bytes.NewReader(route.body))
				artReq.Header.Set("Authorization", "Bearer isolated-runtime-transport-token")
				if route.contentType != "" {
					artReq.Header.Set("Content-Type", route.contentType)
				}
				artRec := httptest.NewRecorder()
				rh.ServeHTTP(artRec, artReq)
				if artRec.Code != tc.wantHTTPStatus {
					t.Fatalf("%s %s status = %d (%s), want %d", route.method, route.path, artRec.Code, artRec.Body.String(), tc.wantHTTPStatus)
				}
				assertArtifactErrorCode(t, artRec, tc.wantArtifactCode)
			}
		})
	}
}
