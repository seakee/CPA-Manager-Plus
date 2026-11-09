package main

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
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

const (
	artifactTestRuntimeToken = "s2a-runtime-token"
	artifactTestPinnedID     = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
)

func newArtifactTestSetup(t *testing.T, supervisorURL string) (*freshApplication, string) {
	t.Helper()
	dir := t.TempDir()
	paths := legacypreflight.DefaultPaths(filepath.Join(dir, "manager.sqlite"))
	paths.Config = filepath.Join(dir, "config.json")
	tokenFile := filepath.Join(dir, "runtime.token")
	if err := os.WriteFile(tokenFile, []byte(artifactTestRuntimeToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	cfg := config.Config{
		RuntimeURL:       supervisorURL,
		RuntimeTokenFile: tokenFile,
	}
	app := newFreshApplication(opened, cfg, nil)
	session := installationSession(t, app)
	return app, session
}

func commitArtifactTestRouting(t *testing.T, app *freshApplication) {
	t.Helper()
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
			OperationID: r.Operation.Identity.OperationID,
			Revision:    1,
			Target:      "/control",
			Stage:       bootstrap.PrefixCommitted,
			Committed:   true,
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func doArtifactRequest(t *testing.T, handler http.Handler, method, path, auth, contentType string, body io.Reader, contentLength int64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	if contentLength >= -1 && body != nil {
		req.ContentLength = contentLength
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestFreshArtifactAuthorizationOnBothCarriersAndBeforeCPAExists(t *testing.T) {
	var supervisorCalls atomic.Int32
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		supervisorCalls.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+artifactTestRuntimeToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/runtime/artifact":
			_, _ = w.Write([]byte(`{"schemaVersion":1,"pinned":{"version":"8.0.55","artifactId":"` + artifactTestPinnedID + `"},"active":null,"required":true,"operation":null}`))
		case "/v1/runtime/artifact/probe":
			_, _ = w.Write([]byte(`{"reachable":true,"code":""}`))
		case "/v1/runtime/artifact/acquire":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"operation":{"id":"op-1","source":"github","phase":"downloading","receivedBytes":0,"totalBytes":0,"code":""}}`))
		case "/v1/runtime/artifact/upload":
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"operation":{"id":"op-2","source":"upload","phase":"succeeded","receivedBytes":4,"totalBytes":4,"code":""}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer supervisor.Close()

	app, session := newArtifactTestSetup(t, supervisor.URL)
	// Simulate slim image before CPA artifact exists: ActiveGatewayArtifact is nil,
	// so installation/runtime fails closed with 503 while runtime/artifact works.
	app.runtime = &installationTestRuntime{
		status: model.RuntimeObservedStatus{
			Identity:        "slim-runtime",
			Generation:      1,
			ProtocolVersion: "v1",
			State:           model.RuntimeStateOffline,
		},
	}

	routes := []struct {
		method      string
		resource    string
		contentType string
		body        string
		wantOK      int
	}{
		{http.MethodGet, "runtime/artifact", "", "", http.StatusOK},
		{http.MethodPost, "runtime/artifact/probe", "application/json", "{}", http.StatusOK},
		{http.MethodPost, "runtime/artifact/acquire", "application/json", `{"source":"github"}`, http.StatusAccepted},
		{http.MethodPut, "runtime/artifact/upload", "application/gzip", "gzip", http.StatusOK},
	}

	// 1. Pre-cutover fixed bootstrap carrier: unauthenticated and Bearer Admin are 401; Fresh session succeeds.
	for _, route := range routes {
		path := bootstrap.FixedBootstrapAPIPath + route.resource
		for _, badAuth := range []string{"", "Fresh invalid-session", "Bearer " + strategyTestAdmin, "Bearer " + session} {
			before := supervisorCalls.Load()
			rec := doArtifactRequest(t, app, route.method, path, badAuth, route.contentType, strings.NewReader(route.body), int64(len(route.body)))
			if rec.Code != http.StatusUnauthorized || supervisorCalls.Load() != before {
				t.Fatalf("fixed carrier %s %s with %q = %d, want 401 without contacting Supervisor", route.method, route.resource, badAuth, rec.Code)
			}
		}
		rec := doArtifactRequest(t, app, route.method, path, "Fresh "+session, route.contentType, strings.NewReader(route.body), int64(len(route.body)))
		if rec.Code != route.wantOK {
			t.Fatalf("fixed carrier %s %s with Fresh session = %d: %s", route.method, route.resource, rec.Code, rec.Body.String())
		}
	}

	// Verify installation/runtime still returns 503 when ActiveGatewayArtifact is nil.
	runtimeRec := doArtifactRequest(t, app, http.MethodGet, bootstrap.FixedBootstrapAPIPath+"installation/runtime", "Fresh "+session, "", nil, 0)
	if runtimeRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("installation/runtime without active artifact = %d, want 503", runtimeRec.Code)
	}

	// 2. Post-cutover canonical carrier: fixed carrier is now 404; canonical carrier rejects unauthenticated,
	// accepts both Fresh continuation session and Admin Bearer while installation is in progress.
	commitArtifactTestRouting(t, app)

	for _, route := range routes {
		fixedPath := bootstrap.FixedBootstrapAPIPath + route.resource
		if rec := doArtifactRequest(t, app, route.method, fixedPath, "Fresh "+session, route.contentType, strings.NewReader(route.body), int64(len(route.body))); rec.Code != http.StatusNotFound {
			t.Fatalf("committed fixed carrier %s %s = %d, want 404", route.method, route.resource, rec.Code)
		}

		canonicalPath := "/control/api/v2/" + route.resource
		for _, badAuth := range []string{"", "Fresh invalid-session", "Bearer invalid-admin", "Bearer " + session, "Fresh " + strategyTestAdmin} {
			before := supervisorCalls.Load()
			rec := doArtifactRequest(t, http.HandlerFunc(app.canonicalAPI), route.method, canonicalPath, badAuth, route.contentType, strings.NewReader(route.body), int64(len(route.body)))
			if rec.Code != http.StatusUnauthorized || supervisorCalls.Load() != before {
				t.Fatalf("canonical carrier %s %s with %q = %d, want 401", route.method, route.resource, badAuth, rec.Code)
			}
		}

		for _, validAuth := range []string{"Fresh " + session, "Bearer " + strategyTestAdmin} {
			rec := doArtifactRequest(t, http.HandlerFunc(app.canonicalAPI), route.method, canonicalPath, validAuth, route.contentType, strings.NewReader(route.body), int64(len(route.body)))
			if rec.Code != route.wantOK {
				t.Fatalf("canonical carrier %s %s with %q = %d: %s", route.method, route.resource, validAuth, rec.Code, rec.Body.String())
			}
		}
	}

	// 3. After ProductComplete, Fresh session is no longer accepted for artifact routes, while Admin Bearer remains accepted.
	installManagedCheckpoint(t, app, "committed")
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.ProductComplete = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, route := range routes {
		canonicalPath := "/control/api/v2/" + route.resource
		if rec := doArtifactRequest(t, http.HandlerFunc(app.canonicalAPI), route.method, canonicalPath, "Fresh "+session, route.contentType, strings.NewReader(route.body), int64(len(route.body))); rec.Code != http.StatusUnauthorized {
			t.Fatalf("completed canonical carrier %s %s with Fresh session = %d, want 401", route.method, route.resource, rec.Code)
		}
		if rec := doArtifactRequest(t, http.HandlerFunc(app.canonicalAPI), route.method, canonicalPath, "Bearer "+strategyTestAdmin, route.contentType, strings.NewReader(route.body), int64(len(route.body))); rec.Code != route.wantOK {
			t.Fatalf("completed canonical carrier %s %s with Admin Bearer = %d, want %d", route.method, route.resource, rec.Code, route.wantOK)
		}
	}
}

func TestFreshArtifactStatusPassthroughAndCDNPresets(t *testing.T) {
	var statusJSON atomic.Value
	statusJSON.Store(`{"schemaVersion":1,"pinned":{"version":"8.0.55","artifactId":"` + artifactTestPinnedID + `"},"active":null,"required":true,"operation":null}`)

	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(statusJSON.Load().(string)))
	}))
	defer supervisor.Close()

	app, _ := newArtifactTestSetup(t, supervisor.URL)
	commitArtifactTestRouting(t, app)

	rec := doArtifactRequest(t, http.HandlerFunc(app.canonicalAPI), http.MethodGet, "/control/api/v2/runtime/artifact", "Bearer "+strategyTestAdmin, "", nil, 0)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET runtime/artifact = %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"schemaVersion": float64(1),
		"pinned": map[string]any{
			"version":    "8.0.55",
			"artifactId": artifactTestPinnedID,
		},
		"active":     nil,
		"required":   true,
		"operation":  nil,
		"cdnPresets": []any{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GET runtime/artifact = %#v, want %#v", got, want)
	}

	statusJSON.Store(`{"schemaVersion":1,"pinned":{"version":"8.0.55","artifactId":"` + artifactTestPinnedID + `"},"active":{"artifactId":"` + artifactTestPinnedID + `","origin":"acquired"},"required":false,"operation":{"id":"op-9","source":"cdn","phase":"succeeded","receivedBytes":12345,"totalBytes":12345,"code":""}}`)
	rec = doArtifactRequest(t, http.HandlerFunc(app.canonicalAPI), http.MethodGet, "/control/api/v2/runtime/artifact", "Bearer "+strategyTestAdmin, "", nil, 0)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET runtime/artifact acquired = %d: %s", rec.Code, rec.Body.String())
	}
	got = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	wantActive := map[string]any{
		"schemaVersion": float64(1),
		"pinned": map[string]any{
			"version":    "8.0.55",
			"artifactId": artifactTestPinnedID,
		},
		"active": map[string]any{
			"artifactId": artifactTestPinnedID,
			"origin":     "acquired",
		},
		"required": false,
		"operation": map[string]any{
			"id":            "op-9",
			"source":        "cdn",
			"phase":         "succeeded",
			"receivedBytes": float64(12345),
			"totalBytes":    float64(12345),
			"code":          "",
		},
		"cdnPresets": []any{},
	}
	if !reflect.DeepEqual(got, wantActive) {
		t.Fatalf("GET runtime/artifact acquired = %#v, want %#v", got, wantActive)
	}
}

func TestFreshArtifactAcquireValidationTable(t *testing.T) {
	var supervisorCalls int
	var lastSource, lastPrefix string
	supervisorStatus := http.StatusAccepted
	supervisorPhase := "downloading"

	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		supervisorCalls++
		var body struct {
			Source    string `json:"source"`
			CDNPrefix string `json:"cdnPrefix"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		lastSource = body.Source
		lastPrefix = body.CDNPrefix
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(supervisorStatus)
		_, _ = w.Write([]byte(`{"operation":{"id":"op-acq","source":"` + body.Source + `","phase":"` + supervisorPhase + `","receivedBytes":0,"totalBytes":0,"code":""}}`))
	}))
	defer supervisor.Close()

	app, _ := newArtifactTestSetup(t, supervisor.URL)
	commitArtifactTestRouting(t, app)

	tests := []struct {
		name               string
		body               string
		supervisorStatus   int
		supervisorPhase    string
		wantStatus         int
		wantCode           string
		wantSupervisorCall bool
		wantSource         string
		wantPrefix         string
	}{
		{
			name:               "github async 202",
			body:               `{"source":"github"}`,
			supervisorStatus:   http.StatusAccepted,
			supervisorPhase:    "downloading",
			wantStatus:         http.StatusAccepted,
			wantSupervisorCall: true,
			wantSource:         "github",
			wantPrefix:         "",
		},
		{
			name:               "github idempotent 200",
			body:               `{"source":"github"}`,
			supervisorStatus:   http.StatusOK,
			supervisorPhase:    "succeeded",
			wantStatus:         http.StatusOK,
			wantSupervisorCall: true,
			wantSource:         "github",
			wantPrefix:         "",
		},
		{
			name:               "cdn with prefix forwards to Supervisor",
			body:               `{"source":"cdn","cdnPrefix":"https://mirror.example.com/prefix"}`,
			supervisorStatus:   http.StatusAccepted,
			supervisorPhase:    "downloading",
			wantStatus:         http.StatusAccepted,
			wantSupervisorCall: true,
			wantSource:         "cdn",
			wantPrefix:         "https://mirror.example.com/prefix",
		},
		{
			name:       "github with cdnPrefix rejected",
			body:       `{"source":"github","cdnPrefix":"https://mirror.example.com"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "cdn without cdnPrefix rejected",
			body:       `{"source":"cdn"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "cdn with whitespace cdnPrefix rejected",
			body:       `{"source":"cdn","cdnPrefix":"   "}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "unsupported source upload rejected",
			body:       `{"source":"upload"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "empty object rejected",
			body:       `{}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "unknown field rejected",
			body:       `{"source":"github","artifactId":"sha256:00"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "trailing json rejected",
			body:       `{"source":"github"}{"source":"github"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name:       "body over 4 KiB rejected",
			body:       `{"source":"cdn","cdnPrefix":"https://mirror.example.com/` + strings.Repeat("a", 4096) + `"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			beforeCalls := supervisorCalls
			if tc.supervisorStatus != 0 {
				supervisorStatus = tc.supervisorStatus
				supervisorPhase = tc.supervisorPhase
			}
			rec := doArtifactRequest(
				t,
				http.HandlerFunc(app.canonicalAPI),
				http.MethodPost,
				"/control/api/v2/runtime/artifact/acquire",
				"Bearer "+strategyTestAdmin,
				"application/json",
				strings.NewReader(tc.body),
				int64(len(tc.body)),
			)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if tc.wantSupervisorCall {
				if supervisorCalls != beforeCalls+1 {
					t.Fatalf("supervisorCalls = %d, want %d", supervisorCalls, beforeCalls+1)
				}
				if lastSource != tc.wantSource || lastPrefix != tc.wantPrefix {
					t.Fatalf("forwarded (%q, %q), want (%q, %q)", lastSource, lastPrefix, tc.wantSource, tc.wantPrefix)
				}
			} else {
				if supervisorCalls != beforeCalls {
					t.Fatalf("invalid acquire request contacted Supervisor")
				}
				var errResp freshArtifactErrorResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil || errResp.Code != tc.wantCode {
					t.Fatalf("error response = %s, want code %q", rec.Body.String(), tc.wantCode)
				}
			}
		})
	}
}

func TestFreshArtifactUploadStreamsExactBytesAndEnforces64MiBCap(t *testing.T) {
	const maxUpload = 64 << 20

	var supervisorUploadCalls atomic.Int32
	var receivedContentType string
	var receivedContentLength int64
	var receivedBytes atomic.Int64
	var receivedDigest [32]byte
	var completedFullRead atomic.Bool

	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		supervisorUploadCalls.Add(1)
		receivedContentType = r.Header.Get("Content-Type")
		receivedContentLength = r.ContentLength
		hasher := sha256.New()
		n, err := io.Copy(hasher, r.Body)
		receivedBytes.Store(n)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":"archive_invalid"}`))
			return
		}
		completedFullRead.Store(true)
		copy(receivedDigest[:], hasher.Sum(nil))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"operation":{"id":"op-upload","source":"upload","phase":"succeeded","receivedBytes":131072,"totalBytes":131072,"code":""}}`))
	}))
	defer supervisor.Close()

	app, _ := newArtifactTestSetup(t, supervisor.URL)
	commitArtifactTestRouting(t, app)

	// 1. Wrong or missing Content-Type is rejected with 400 invalid_request without contacting Supervisor.
	for _, ct := range []string{"", "application/octet-stream", "application/json"} {
		rec := doArtifactRequest(t, http.HandlerFunc(app.canonicalAPI), http.MethodPut, "/control/api/v2/runtime/artifact/upload", "Bearer "+strategyTestAdmin, ct, strings.NewReader("data"), 4)
		if rec.Code != http.StatusBadRequest || supervisorUploadCalls.Load() != 0 {
			t.Fatalf("upload with Content-Type %q = %d (calls=%d), want 400", ct, rec.Code, supervisorUploadCalls.Load())
		}
	}

	// 2. Valid archive streams exact bytes with Content-Type: application/gzip and Content-Length preserved.
	archive := bytes.Repeat([]byte{0x1f, 0x8b, 0x08, 0x00, 0xde, 0xad, 0xbe, 0xef}, 16384)
	expectedDigest := sha256.Sum256(archive)
	rec := doArtifactRequest(
		t,
		http.HandlerFunc(app.canonicalAPI),
		http.MethodPut,
		"/control/api/v2/runtime/artifact/upload",
		"Bearer "+strategyTestAdmin,
		"application/gzip",
		bytes.NewReader(archive),
		int64(len(archive)),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid upload = %d: %s", rec.Code, rec.Body.String())
	}
	if receivedContentType != "application/gzip" {
		t.Fatalf("Supervisor received Content-Type = %q, want application/gzip", receivedContentType)
	}
	if receivedContentLength != int64(len(archive)) {
		t.Fatalf("Supervisor received ContentLength = %d, want %d", receivedContentLength, len(archive))
	}
	if receivedDigest != expectedDigest {
		t.Fatalf("Supervisor received digest = %s, want %s", hex.EncodeToString(receivedDigest[:]), hex.EncodeToString(expectedDigest[:]))
	}

	// 3. Upload over 64 MiB with known Content-Length returns 413 archive_too_large and contacts Supervisor 0 times.
	beforeCalls := supervisorUploadCalls.Load()
	oversizedLength := int64(maxUpload + 1024)
	rec = doArtifactRequest(
		t,
		http.HandlerFunc(app.canonicalAPI),
		http.MethodPut,
		"/control/api/v2/runtime/artifact/upload",
		"Bearer "+strategyTestAdmin,
		"application/gzip",
		io.LimitReader(zeroReader{}, oversizedLength),
		oversizedLength,
	)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized known-length upload = %d, want 413: %s", rec.Code, rec.Body.String())
	}
	var errResp freshArtifactErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil || errResp.Code != "archive_too_large" {
		t.Fatalf("oversized known-length body = %s, want archive_too_large", rec.Body.String())
	}
	if supervisorUploadCalls.Load() != beforeCalls {
		t.Fatalf("oversized known-length upload contacted Supervisor")
	}

	// 4. Upload over 64 MiB with unknown Content-Length (-1) is cut off by MaxBytesReader -> 413 archive_too_large
	// and the fake Supervisor never receives the full body.
	completedFullRead.Store(false)
	receivedBytes.Store(0)
	rec = doArtifactRequest(
		t,
		http.HandlerFunc(app.canonicalAPI),
		http.MethodPut,
		"/control/api/v2/runtime/artifact/upload",
		"Bearer "+strategyTestAdmin,
		"application/gzip",
		io.LimitReader(zeroReader{}, oversizedLength),
		-1,
	)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized streaming upload = %d, want 413: %s", rec.Code, rec.Body.String())
	}
	errResp = freshArtifactErrorResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil || errResp.Code != "archive_too_large" {
		t.Fatalf("oversized streaming body = %s, want archive_too_large", rec.Body.String())
	}
	if completedFullRead.Load() || receivedBytes.Load() >= oversizedLength {
		t.Fatalf("Supervisor received full oversized body (completed=%v, bytes=%d, total=%d)", completedFullRead.Load(), receivedBytes.Load(), oversizedLength)
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func TestFreshArtifactSupervisorErrorPassthroughAndRuntimeDown(t *testing.T) {
	var upstreamStatus int
	var upstreamCode string

	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(upstreamStatus)
		_, _ = w.Write([]byte(`{"code":"` + upstreamCode + `"}`))
	}))
	defer supervisor.Close()

	app, _ := newArtifactTestSetup(t, supervisor.URL)
	commitArtifactTestRouting(t, app)

	codes := []struct {
		status int
		code   string
	}{
		{http.StatusBadRequest, "invalid_request"},
		{http.StatusConflict, "unsupported_platform"},
		{http.StatusConflict, "acquisition_in_progress"},
		{http.StatusConflict, "artifact_conflict"},
		{http.StatusConflict, "artifact_already_active"},
		{http.StatusRequestEntityTooLarge, "archive_too_large"},
		{http.StatusUnprocessableEntity, "archive_invalid"},
		{http.StatusUnprocessableEntity, "artifact_mismatch"},
		{http.StatusBadGateway, "source_unreachable"},
		{http.StatusBadGateway, "download_failed"},
		{http.StatusInternalServerError, "install_failed"},
		{http.StatusInternalServerError, "recovery_required"},
	}

	for _, tc := range codes {
		upstreamStatus = tc.status
		upstreamCode = tc.code
		rec := doArtifactRequest(
			t,
			http.HandlerFunc(app.canonicalAPI),
			http.MethodPost,
			"/control/api/v2/runtime/artifact/acquire",
			"Bearer "+strategyTestAdmin,
			"application/json",
			strings.NewReader(`{"source":"github"}`),
			19,
		)
		if rec.Code != tc.status {
			t.Fatalf("passthrough status for %s = %d, want %d", tc.code, rec.Code, tc.status)
		}
		var got freshArtifactErrorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Code != tc.code {
			t.Fatalf("passthrough body for %s = %s, want code %q", tc.code, rec.Body.String(), tc.code)
		}
	}

	// Shut down the fake Supervisor: all 4 routes must return 503 runtime_unavailable.
	supervisor.Close()
	for _, route := range []struct {
		method      string
		resource    string
		contentType string
		body        string
	}{
		{http.MethodGet, "runtime/artifact", "", ""},
		{http.MethodPost, "runtime/artifact/probe", "application/json", "{}"},
		{http.MethodPost, "runtime/artifact/acquire", "application/json", `{"source":"github"}`},
		{http.MethodPut, "runtime/artifact/upload", "application/gzip", "gzip"},
	} {
		rec := doArtifactRequest(
			t,
			http.HandlerFunc(app.canonicalAPI),
			route.method,
			"/control/api/v2/"+route.resource,
			"Bearer "+strategyTestAdmin,
			route.contentType,
			strings.NewReader(route.body),
			int64(len(route.body)),
		)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("runtime down %s %s = %d, want 503", route.method, route.resource, rec.Code)
		}
		var got freshArtifactErrorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Code != "runtime_unavailable" {
			t.Fatalf("runtime down %s %s body = %s, want runtime_unavailable", route.method, route.resource, rec.Body.String())
		}
	}

	// Unconfigured artifact client (nil) also returns 503 runtime_unavailable.
	app.artifact = nil
	rec := doArtifactRequest(t, http.HandlerFunc(app.canonicalAPI), http.MethodGet, "/control/api/v2/runtime/artifact", "Bearer "+strategyTestAdmin, "", nil, 0)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"runtime_unavailable"`) {
		t.Fatalf("nil artifact client = %d %s, want 503 runtime_unavailable", rec.Code, rec.Body.String())
	}
	_ = runtimeservice.ArtifactStatus{}
}
