package upgradewizard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/artifactproxy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradesurface"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade/copyinmarker"
)

const (
	upgradeArtifactTestToken    = "upgrade-artifact-runtime-token"
	upgradeArtifactTestPinnedID = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	v2StoreAdminKey             = "v2-manager-admin-credential"
)

type artifactRouteSpec struct {
	method      string
	suffix      string
	contentType string
	body        string
	wantStatus  int
}

func upgradeArtifactRouteSpecs() []artifactRouteSpec {
	return []artifactRouteSpec{
		{http.MethodGet, ArtifactStatusSuffix, "", "", http.StatusOK},
		{http.MethodPost, ArtifactProbeSuffix, "application/json", "{}", http.StatusOK},
		{http.MethodPost, ArtifactAcquireSuffix, "application/json", `{"source":"github"}`, http.StatusAccepted},
		{http.MethodPut, ArtifactUploadSuffix, "application/gzip", "gzip", http.StatusOK},
	}
}

func recordedArtifactStore(t *testing.T, adminKey, sourceRoot string) string {
	t.Helper()
	digest, err := upgradeservice.SourceFingerprint(t.Context(), sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	return managerStoreWithOperation(t, adminKey, model.UpgradeOperation{
		Stage:                   model.UpgradeStageReconcile,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy},
		SourceKind:              "upgraded-legacy",
		SourceRoot:              sourceRoot,
		SourceFingerprintBefore: digest,
		SourceFingerprintAfter:  digest,
		BackupManifestSHA256:    strings.Repeat("b", 64),
	})
}

func doUpgradeArtifactRequest(
	t *testing.T,
	handler http.Handler,
	method, path, auth, contentType string,
	body io.Reader,
	contentLength int64,
) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, body)
	req.Host = "manager.test"
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

func newFakeArtifactSupervisor(t *testing.T, calls *atomic.Int32) (*httptest.Server, *runtimeservice.ArtifactClient) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls != nil {
			calls.Add(1)
		}
		if r.Header.Get("Authorization") != "Bearer "+upgradeArtifactTestToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/runtime/artifact":
			_, _ = w.Write([]byte(`{"schemaVersion":1,"pinned":{"version":"8.0.55","artifactId":"` + upgradeArtifactTestPinnedID + `"},"active":null,"required":true,"operation":null}`))
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
	t.Cleanup(server.Close)
	return server, runtimeservice.NewArtifactClient(server.URL, upgradeArtifactTestToken)
}

func TestArtifactAuthMatrixAndZeroSupervisorContact(t *testing.T) {
	var supervisorCalls atomic.Int32
	_, client := newFakeArtifactSupervisor(t, &supervisorCalls)

	root := legacySourceDir(t)
	database := recordedArtifactStore(t, v2StoreAdminKey, root)
	handler := newHandler(root, database)
	handler.Artifact = client

	rejectedAuth := map[string]string{
		"none":              "",
		"wrong-key":         bearer(foreignKey),
		"fresh-session":     "Fresh installation-session-token",
		"v2-admin-key":      bearer(v2StoreAdminKey),
		"other-scheme":      "Basic b3BlcmF0b3I6cGFzcw==",
		"empty-bearer":      "Bearer ",
		"bare-bearer":       "Bearer",
		"padded-bearer":     bearer(" " + testAdminKey),
		"bare-key":          testAdminKey,
		"trailing-material": bearer(testAdminKey) + " extra",
	}

	for name, auth := range rejectedAuth {
		t.Run(name, func(t *testing.T) {
			for _, prefix := range []string{testPrefix, canonical} {
				for _, route := range upgradeArtifactRouteSpecs() {
					before := supervisorCalls.Load()
					rec := doUpgradeArtifactRequest(
						t,
						handler,
						route.method,
						prefix+route.suffix,
						auth,
						route.contentType,
						strings.NewReader(route.body),
						int64(len(route.body)),
					)
					if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"`+CodeAdminUnauthorized+`"`) {
						t.Fatalf("%s %s%s with %s = %d: %s", route.method, prefix, route.suffix, name, rec.Code, rec.Body.String())
					}
					if supervisorCalls.Load() != before {
						t.Fatalf("%s %s%s with %s contacted Supervisor", route.method, prefix, route.suffix, name)
					}
				}
			}
		})
	}

	// Duplicate Authorization headers must also fail closed without contacting Supervisor.
	for _, route := range upgradeArtifactRouteSpecs() {
		before := supervisorCalls.Load()
		req := httptest.NewRequest(route.method, testPrefix+route.suffix, strings.NewReader(route.body))
		req.Host = "manager.test"
		req.Header.Add("Authorization", bearer(testAdminKey))
		req.Header.Add("Authorization", bearer(testAdminKey))
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized || supervisorCalls.Load() != before {
			t.Fatalf("duplicate Authorization on %s = %d (calls=%d)", route.suffix, rec.Code, supervisorCalls.Load()-before)
		}
	}
}

func TestArtifactCrossSiteOriginRejection(t *testing.T) {
	var supervisorCalls atomic.Int32
	_, client := newFakeArtifactSupervisor(t, &supervisorCalls)

	root := legacySourceDir(t)
	database := recordedArtifactStore(t, v2StoreAdminKey, root)
	handler := newHandler(root, database)
	handler.PublicOrigin = "https://manager.test"
	handler.Artifact = client

	crossSiteCases := []struct {
		name   string
		origin string
		site   string
	}{
		{name: "cross-origin-header", origin: "https://other.example", site: ""},
		{name: "null-origin", origin: "null", site: ""},
		{name: "sec-fetch-cross-site", origin: "", site: "cross-site"},
		{name: "sec-fetch-same-site-without-matching-origin", origin: "https://sub.manager.test", site: "same-site"},
	}

	for _, tc := range crossSiteCases {
		t.Run(tc.name, func(t *testing.T) {
			for _, route := range upgradeArtifactRouteSpecs() {
				before := supervisorCalls.Load()
				req := httptest.NewRequest(route.method, testPrefix+route.suffix, strings.NewReader(route.body))
				req.Host = "manager.test"
				req.Header.Set("Authorization", bearer(testAdminKey))
				if route.contentType != "" {
					req.Header.Set("Content-Type", route.contentType)
				}
				if tc.origin != "" {
					req.Header.Set("Origin", tc.origin)
				}
				if tc.site != "" {
					req.Header.Set("Sec-Fetch-Site", tc.site)
				}
				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, req)
				if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"`+upgradesurface.CodeOriginRejected+`"`) {
					t.Fatalf("%s %s (%s) = %d: %s", route.method, route.suffix, tc.name, rec.Code, rec.Body.String())
				}
				if supervisorCalls.Load() != before {
					t.Fatalf("cross-site request %s contacted Supervisor", tc.name)
				}
			}
		})
	}
}

func TestArtifactUnknownSubpathAndWrongVerbOrder(t *testing.T) {
	var supervisorCalls atomic.Int32
	_, client := newFakeArtifactSupervisor(t, &supervisorCalls)

	root := legacySourceDir(t)
	database := recordedArtifactStore(t, v2StoreAdminKey, root)
	handler := newHandler(root, database)
	handler.Artifact = client

	unknownSubpaths := []string{
		testPrefix + "/installation/upgrade/runtime",
		testPrefix + "/installation/upgrade/runtime/",
		testPrefix + ArtifactStatusSuffix + "/",
		testPrefix + ArtifactStatusSuffix + "/extra",
		testPrefix + ArtifactProbeSuffix + "/extra",
		testPrefix + ArtifactAcquireSuffix + "/extra",
		testPrefix + ArtifactUploadSuffix + "/extra",
		canonical + ArtifactStatusSuffix + "/unknown",
	}

	for _, path := range unknownSubpaths {
		for _, badAuth := range []string{"", bearer(foreignKey), "Fresh session-token", bearer(v2StoreAdminKey)} {
			rec := doUpgradeArtifactRequest(t, handler, http.MethodGet, path, badAuth, "", nil, 0)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("unknown subpath %s with %q = %d, want 401 before path resolution", path, badAuth, rec.Code)
			}
		}
		rec := doUpgradeArtifactRequest(t, handler, http.MethodGet, path, bearer(testAdminKey), "", nil, 0)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("unknown subpath %s with valid key = %d, want 404", path, rec.Code)
		}
	}

	wrongVerbCases := []struct {
		suffix    string
		wantAllow string
		wrong     []string
	}{
		{
			suffix:    ArtifactStatusSuffix,
			wantAllow: http.MethodGet,
			wrong:     []string{http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch},
		},
		{
			suffix:    ArtifactProbeSuffix,
			wantAllow: http.MethodPost,
			wrong:     []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodPatch},
		},
		{
			suffix:    ArtifactAcquireSuffix,
			wantAllow: http.MethodPost,
			wrong:     []string{http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodPatch},
		},
		{
			suffix:    ArtifactUploadSuffix,
			wantAllow: http.MethodPut,
			wrong:     []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodDelete, http.MethodPatch},
		},
	}

	for _, tc := range wrongVerbCases {
		for _, method := range tc.wrong {
			for _, badAuth := range []string{"", bearer(foreignKey), "Fresh session-token", bearer(v2StoreAdminKey)} {
				rec := doUpgradeArtifactRequest(t, handler, method, testPrefix+tc.suffix, badAuth, "", nil, 0)
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("%s %s with %q = %d, want 401 before method check", method, tc.suffix, badAuth, rec.Code)
				}
			}
			rec := doUpgradeArtifactRequest(t, handler, method, testPrefix+tc.suffix, bearer(testAdminKey), "", nil, 0)
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s %s with valid key = %d, want 405: %s", method, tc.suffix, rec.Code, rec.Body.String())
			}
			if allow := rec.Header().Get("Allow"); allow != tc.wantAllow {
				t.Fatalf("%s %s Allow = %q, want %q", method, tc.suffix, allow, tc.wantAllow)
			}
		}
	}

	if supervisorCalls.Load() != 0 {
		t.Fatalf("unknown subpaths or wrong verbs contacted Supervisor %d times", supervisorCalls.Load())
	}
}

func TestArtifactExactlyFourResourcesReachableAndNilClient(t *testing.T) {
	var supervisorCalls atomic.Int32
	_, client := newFakeArtifactSupervisor(t, &supervisorCalls)

	root := legacySourceDir(t)
	database := recordedArtifactStore(t, v2StoreAdminKey, root)
	handler := newHandler(root, database)
	handler.Artifact = client

	for _, prefix := range []string{testPrefix, canonical} {
		for _, route := range upgradeArtifactRouteSpecs() {
			rec := doUpgradeArtifactRequest(
				t,
				handler,
				route.method,
				prefix+route.suffix,
				bearer(testAdminKey),
				route.contentType,
				strings.NewReader(route.body),
				int64(len(route.body)),
			)
			if rec.Code != route.wantStatus {
				t.Fatalf("%s %s%s = %d, want %d: %s", route.method, prefix, route.suffix, rec.Code, route.wantStatus, rec.Body.String())
			}
			if route.suffix == ArtifactStatusSuffix {
				var got map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
					t.Fatal(err)
				}
				want := map[string]any{
					"schemaVersion": float64(1),
					"pinned": map[string]any{
						"version":    "8.0.55",
						"artifactId": upgradeArtifactTestPinnedID,
					},
					"active":     nil,
					"required":   true,
					"operation":  nil,
					"cdnPresets": []any{},
				}
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("status body = %#v, want %#v", got, want)
				}
			}
		}
	}

	// Nil client (both untyped nil and typed nil pointer) must return 503 runtime_unavailable.
	var typedNilClient *runtimeservice.ArtifactClient
	for _, nilArtifact := range []ArtifactRuntime{nil, typedNilClient} {
		handler.Artifact = nilArtifact
		for _, route := range upgradeArtifactRouteSpecs() {
			before := supervisorCalls.Load()
			rec := doUpgradeArtifactRequest(
				t,
				handler,
				route.method,
				testPrefix+route.suffix,
				bearer(testAdminKey),
				route.contentType,
				strings.NewReader(route.body),
				int64(len(route.body)),
			)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("nil artifact %s %s = %d, want 503: %s", route.method, route.suffix, rec.Code, rec.Body.String())
			}
			var errResp artifactproxy.ErrorResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil || errResp.Code != "runtime_unavailable" {
				t.Fatalf("nil artifact %s %s body = %s, want runtime_unavailable", route.method, route.suffix, rec.Body.String())
			}
			if supervisorCalls.Load() != before {
				t.Fatalf("nil artifact %s %s contacted Supervisor", route.method, route.suffix)
			}
		}
	}
}

func TestArtifactRequiresRecordedUnfinishedOperation(t *testing.T) {
	var supervisorCalls atomic.Int32
	_, client := newFakeArtifactSupervisor(t, &supervisorCalls)
	root := legacySourceDir(t)
	rootDigest, err := upgradeservice.SourceFingerprint(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}

	// 1. No database configured at all -> 409 upgrade_artifact_unavailable.
	noDBHandler := newHandler(root, "")
	noDBHandler.Artifact = client
	for _, route := range upgradeArtifactRouteSpecs() {
		rec := doUpgradeArtifactRequest(t, noDBHandler, route.method, testPrefix+route.suffix, bearer(testAdminKey), route.contentType, strings.NewReader(route.body), int64(len(route.body)))
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"`+CodeArtifactUnavailable+`"`) {
			t.Fatalf("unconfigured database %s %s = %d: %s", route.method, route.suffix, rec.Code, rec.Body.String())
		}
	}

	// 2. Empty Manager database with no recorded operation -> 409 upgrade_artifact_unavailable.
	emptyDB := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(emptyDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	emptyOpHandler := newHandler(root, emptyDB)
	emptyOpHandler.Artifact = client
	for _, route := range upgradeArtifactRouteSpecs() {
		rec := doUpgradeArtifactRequest(t, emptyOpHandler, route.method, testPrefix+route.suffix, bearer(testAdminKey), route.contentType, strings.NewReader(route.body), int64(len(route.body)))
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"`+CodeArtifactUnavailable+`"`) {
			t.Fatalf("unrecorded operation %s %s = %d: %s", route.method, route.suffix, rec.Code, rec.Body.String())
		}
	}

	// 3. Completed operation -> 409 upgrade_artifact_unavailable.
	completedDB := filepath.Join(t.TempDir(), "usage.sqlite")
	recordOperation(t, completedDB, model.UpgradeOperation{
		Stage:                   model.UpgradeStageCopyIn,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy},
		SourceKind:              "upgraded-legacy",
		SourceRoot:              root,
		SourceFingerprintBefore: rootDigest,
		SourceFingerprintAfter:  rootDigest,
		BackupManifestSHA256:    strings.Repeat("b", 64),
	})
	recordCompletedOperation(t, completedDB)
	completedHandler := newHandler(root, completedDB)
	completedHandler.Artifact = client
	for _, route := range upgradeArtifactRouteSpecs() {
		rec := doUpgradeArtifactRequest(t, completedHandler, route.method, testPrefix+route.suffix, bearer(testAdminKey), route.contentType, strings.NewReader(route.body), int64(len(route.body)))
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"`+CodeArtifactUnavailable+`"`) {
			t.Fatalf("completed operation %s %s = %d: %s", route.method, route.suffix, rec.Code, rec.Body.String())
		}
	}

	// 4. Rolled-back operation -> 409 upgrade_artifact_unavailable.
	rolledBackDB := filepath.Join(t.TempDir(), "usage.sqlite")
	rbDB, err := sqliterepo.Open(rolledBackDB)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rbDB.Exec(`insert into upgrade_operations (id, stage, created_at_ms, updated_at_ms) values (1, 'rolled_back', 1, 2)`); err != nil {
		_ = rbDB.Close()
		t.Fatal(err)
	}
	if err := rbDB.Close(); err != nil {
		t.Fatal(err)
	}
	rolledBackHandler := newHandler(root, rolledBackDB)
	rolledBackHandler.Artifact = client
	for _, route := range upgradeArtifactRouteSpecs() {
		rec := doUpgradeArtifactRequest(t, rolledBackHandler, route.method, testPrefix+route.suffix, bearer(testAdminKey), route.contentType, strings.NewReader(route.body), int64(len(route.body)))
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"`+CodeArtifactUnavailable+`"`) {
			t.Fatalf("rolled_back operation %s %s = %d: %s", route.method, route.suffix, rec.Code, rec.Body.String())
		}
	}

	// 5. In-flight copy-in marker with non-64-hex fingerprint -> 409 upgrade_artifact_unavailable.
	badMarkerDir := t.TempDir()
	badMarkerDB := filepath.Join(badMarkerDir, "usage.sqlite")
	if err := copyinmarker.Write(badMarkerDir, copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		Phase:                   copyinmarker.PhaseMaterializing,
		TargetDatabase:          badMarkerDB,
		SourceKind:              "upgraded-legacy",
		SourceVersion:           "1.14.4",
		SourceFingerprintBefore: "non-hex-fingerprint",
		SourceFingerprintAfter:  "",
		BackupManifestSHA256:    strings.Repeat("b", 64),
		WrittenAtMS:             time.Now().UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	badMarkerHandler := newHandler(root, badMarkerDB)
	badMarkerHandler.Artifact = client
	for _, route := range upgradeArtifactRouteSpecs() {
		rec := doUpgradeArtifactRequest(t, badMarkerHandler, route.method, testPrefix+route.suffix, bearer(testAdminKey), route.contentType, strings.NewReader(route.body), int64(len(route.body)))
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"`+CodeArtifactUnavailable+`"`) {
			t.Fatalf("non-hex copy-in marker %s %s = %d, want 409: %s", route.method, route.suffix, rec.Code, rec.Body.String())
		}
	}

	if supervisorCalls.Load() != 0 {
		t.Fatalf("unavailable operation states contacted Supervisor %d times", supervisorCalls.Load())
	}

	// 6. In-flight copy-in marker with matching 64-hex SourceFingerprintBefore is permitted.
	markerDir := t.TempDir()
	markerDB := filepath.Join(markerDir, "usage.sqlite")
	if err := copyinmarker.Write(markerDir, copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		Phase:                   copyinmarker.PhaseMaterializing,
		TargetDatabase:          markerDB,
		SourceKind:              "upgraded-legacy",
		SourceVersion:           "1.14.4",
		SourceFingerprintBefore: rootDigest,
		SourceFingerprintAfter:  rootDigest,
		BackupManifestSHA256:    strings.Repeat("b", 64),
		WrittenAtMS:             time.Now().UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	markerHandler := newHandler(root, markerDB)
	markerHandler.Artifact = client
	for _, route := range upgradeArtifactRouteSpecs() {
		rec := doUpgradeArtifactRequest(t, markerHandler, route.method, testPrefix+route.suffix, bearer(testAdminKey), route.contentType, strings.NewReader(route.body), int64(len(route.body)))
		if rec.Code != route.wantStatus {
			t.Fatalf("copy-in marker %s %s = %d, want %d: %s", route.method, route.suffix, rec.Code, route.wantStatus, rec.Body.String())
		}
	}
}

func TestArtifactRequiresTheRecordedSource(t *testing.T) {
	recordedSource := legacySourceDir(t)
	currentSource := legacySourceDir(t)
	if err := os.WriteFile(filepath.Join(currentSource, "current-only-marker"), []byte("different-source"), 0o600); err != nil {
		t.Fatal(err)
	}
	recordedDigest, err := upgradeservice.SourceFingerprint(t.Context(), recordedSource)
	if err != nil {
		t.Fatal(err)
	}
	currentDigest, err := upgradeservice.SourceFingerprint(t.Context(), currentSource)
	if err != nil {
		t.Fatal(err)
	}
	if recordedDigest == currentDigest {
		t.Fatal("expected distinct source digests for recordedSource and currentSource")
	}

	database := managerStoreWithOperation(t, foreignKey, model.UpgradeOperation{
		Stage:                   model.UpgradeStageReconcile,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy},
		SourceKind:              "upgraded-legacy",
		SourceRoot:              recordedSource,
		SourceFingerprintBefore: recordedDigest,
		SourceFingerprintAfter:  recordedDigest,
		BackupManifestSHA256:    strings.Repeat("b", 64),
	})
	var calls atomic.Int32
	_, client := newFakeArtifactSupervisor(t, &calls)

	// 1. Mismatch: operation records recordedSource, while mount and proven key belong to currentSource.
	mismatchHandler := newHandler(currentSource, database)
	mismatchHandler.Artifact = client
	for _, route := range upgradeArtifactRouteSpecs() {
		t.Run("mismatch"+route.suffix, func(t *testing.T) {
			before := calls.Load()
			result := doUpgradeArtifactRequest(
				t,
				mismatchHandler,
				route.method,
				testPrefix+route.suffix,
				bearer(testAdminKey),
				route.contentType,
				strings.NewReader(route.body),
				int64(len(route.body)),
			)
			if result.Code != http.StatusConflict || !strings.Contains(result.Body.String(), `"`+CodeArtifactUnavailable+`"`) || calls.Load() != before {
				t.Errorf("artifact authority must belong to the recorded source: status=%d body=%s supervisorCalls=%d; want 409 and 0", result.Code, result.Body.String(), calls.Load()-before)
			}
		})
	}

	// 2. Unbound or incomplete provenance rows on the mounted branch -> 409 upgrade_artifact_unavailable.
	incompleteCases := []struct {
		name string
		op   model.UpgradeOperation
	}{
		{
			name: "missing-source-root",
			op: model.UpgradeOperation{
				Stage:                   model.UpgradeStageReconcile,
				DomainsDone:             []string{model.UpgradeDomainSourceCopy},
				SourceKind:              "upgraded-legacy",
				SourceFingerprintBefore: currentDigest,
				SourceFingerprintAfter:  currentDigest,
				BackupManifestSHA256:    strings.Repeat("b", 64),
			},
		},
		{
			name: "non-hex-fingerprint-before",
			op: model.UpgradeOperation{
				Stage:                   model.UpgradeStageReconcile,
				DomainsDone:             []string{model.UpgradeDomainSourceCopy},
				SourceKind:              "upgraded-legacy",
				SourceRoot:              currentSource,
				SourceFingerprintBefore: "recorded-source-fingerprint",
				SourceFingerprintAfter:  currentDigest,
				BackupManifestSHA256:    strings.Repeat("b", 64),
			},
		},
		{
			name: "non-hex-fingerprint-after",
			op: model.UpgradeOperation{
				Stage:                   model.UpgradeStageReconcile,
				DomainsDone:             []string{model.UpgradeDomainSourceCopy},
				SourceKind:              "upgraded-legacy",
				SourceRoot:              currentSource,
				SourceFingerprintBefore: currentDigest,
				SourceFingerprintAfter:  "recorded-source-fingerprint",
				BackupManifestSHA256:    strings.Repeat("b", 64),
			},
		},
		{
			name: "missing-backup-manifest",
			op: model.UpgradeOperation{
				Stage:                   model.UpgradeStageReconcile,
				DomainsDone:             []string{model.UpgradeDomainSourceCopy},
				SourceKind:              "upgraded-legacy",
				SourceRoot:              currentSource,
				SourceFingerprintBefore: currentDigest,
				SourceFingerprintAfter:  currentDigest,
			},
		},
		{
			name: "same-root-path-but-different-64hex-digest",
			op: model.UpgradeOperation{
				Stage:                   model.UpgradeStageReconcile,
				DomainsDone:             []string{model.UpgradeDomainSourceCopy},
				SourceKind:              "upgraded-legacy",
				SourceRoot:              currentSource,
				SourceFingerprintBefore: recordedDigest,
				SourceFingerprintAfter:  recordedDigest,
				BackupManifestSHA256:    strings.Repeat("b", 64),
			},
		},
	}

	for _, tc := range incompleteCases {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := managerStoreWithOperation(t, foreignKey, tc.op)
			h := newHandler(currentSource, dbPath)
			h.Artifact = client
			for _, route := range upgradeArtifactRouteSpecs() {
				before := calls.Load()
				result := doUpgradeArtifactRequest(
					t,
					h,
					route.method,
					testPrefix+route.suffix,
					bearer(testAdminKey),
					route.contentType,
					strings.NewReader(route.body),
					int64(len(route.body)),
				)
				if result.Code != http.StatusConflict || calls.Load() != before {
					t.Fatalf("%s %s %s = %d (calls=%d), want 409 and 0", tc.name, route.method, route.suffix, result.Code, calls.Load()-before)
				}
			}
		})
	}

	// 3. Copy-in marker recording a different 64-hex source digest -> 409 upgrade_artifact_unavailable.
	foreignMarkerDir := t.TempDir()
	foreignMarkerDB := filepath.Join(foreignMarkerDir, "usage.sqlite")
	if err := copyinmarker.Write(foreignMarkerDir, copyinmarker.Marker{
		Version:                 copyinmarker.Version,
		Phase:                   copyinmarker.PhaseMaterializing,
		TargetDatabase:          foreignMarkerDB,
		SourceKind:              "upgraded-legacy",
		SourceVersion:           "1.14.4",
		SourceFingerprintBefore: recordedDigest,
		SourceFingerprintAfter:  recordedDigest,
		BackupManifestSHA256:    strings.Repeat("b", 64),
		WrittenAtMS:             time.Now().UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	foreignMarkerHandler := newHandler(currentSource, foreignMarkerDB)
	foreignMarkerHandler.Artifact = client
	for _, route := range upgradeArtifactRouteSpecs() {
		before := calls.Load()
		result := doUpgradeArtifactRequest(t, foreignMarkerHandler, route.method, testPrefix+route.suffix, bearer(testAdminKey), route.contentType, strings.NewReader(route.body), int64(len(route.body)))
		if result.Code != http.StatusConflict || calls.Load() != before {
			t.Fatalf("foreign copy-in marker %s %s = %d (calls=%d), want 409 and 0", route.method, route.suffix, result.Code, calls.Load()-before)
		}
	}

	// 4. Positive (same source via mount, including symlinked alias of recordedSource):
	aliasRoot := filepath.Join(t.TempDir(), "recorded-source-alias")
	if err := os.Symlink(recordedSource, aliasRoot); err != nil {
		t.Fatal(err)
	}
	for _, mountPath := range []string{recordedSource, aliasRoot} {
		sameSourceHandler := newHandler(mountPath, database)
		sameSourceHandler.Artifact = client
		for _, route := range upgradeArtifactRouteSpecs() {
			before := calls.Load()
			result := doUpgradeArtifactRequest(
				t,
				sameSourceHandler,
				route.method,
				testPrefix+route.suffix,
				bearer(testAdminKey),
				route.contentType,
				strings.NewReader(route.body),
				int64(len(route.body)),
			)
			if result.Code != route.wantStatus || calls.Load() != before+1 {
				t.Fatalf("same source via mount (%s) %s %s = %d (calls=%d), want %d and 1: %s", mountPath, route.method, route.suffix, result.Code, calls.Load()-before, route.wantStatus, result.Body.String())
			}
		}
	}

	// 5. Positive (same operation via Manager-store fallback): source is already
	// copied into the Manager store and the 1.x mount is no longer present.
	fallbackDB := managerStoreWithOperation(t, testAdminKey, model.UpgradeOperation{
		Stage:                   model.UpgradeStageReconcile,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy},
		SourceKind:              "upgraded-legacy",
		SourceRoot:              recordedSource,
		SourceFingerprintBefore: recordedDigest,
		SourceFingerprintAfter:  recordedDigest,
		BackupManifestSHA256:    strings.Repeat("b", 64),
	})
	fallbackHandler := newHandler(filepath.Join(t.TempDir(), "unmounted-source"), fallbackDB)
	fallbackHandler.Artifact = client
	for _, route := range upgradeArtifactRouteSpecs() {
		t.Run("manager-store-fallback"+route.suffix, func(t *testing.T) {
			before := calls.Load()
			result := doUpgradeArtifactRequest(
				t,
				fallbackHandler,
				route.method,
				testPrefix+route.suffix,
				bearer(testAdminKey),
				route.contentType,
				strings.NewReader(route.body),
				int64(len(route.body)),
			)
			if result.Code != route.wantStatus || calls.Load() != before+1 {
				t.Fatalf("manager store fallback %s %s = %d (calls=%d), want %d and 1: %s", route.method, route.suffix, result.Code, calls.Load()-before, route.wantStatus, result.Body.String())
			}
		})
	}
}

func TestArtifactProofCacheSingleFlightAndTimeout(t *testing.T) {
	origFingerprint := artifactSourceFingerprint
	origCache := defaultArtifactProofCache
	t.Cleanup(func() {
		artifactSourceFingerprint = origFingerprint
		defaultArtifactProofCache = origCache
	})

	root := legacySourceDir(t)
	database := recordedArtifactStore(t, v2StoreAdminKey, root)
	var supervisorCalls atomic.Int32
	_, client := newFakeArtifactSupervisor(t, &supervisorCalls)
	handler := newHandler(root, database)
	handler.Artifact = client

	// 1. Cold proof runs once; cached second and third polls do not re-read file contents.
	defaultArtifactProofCache = newArtifactProofCache()
	var fingerprintCalls atomic.Int32
	artifactSourceFingerprint = func(ctx context.Context, r string) (string, error) {
		fingerprintCalls.Add(1)
		return origFingerprint(ctx, r)
	}

	for poll := range 3 {
		rec := doUpgradeArtifactRequest(t, handler, http.MethodGet, testPrefix+ArtifactStatusSuffix, bearer(testAdminKey), "", nil, 0)
		if rec.Code != http.StatusOK {
			t.Fatalf("poll %d = %d: %s", poll, rec.Code, rec.Body.String())
		}
	}
	if got := fingerprintCalls.Load(); got != 1 {
		t.Fatalf("fingerprintCalls across 3 identical polls = %d, want 1", got)
	}

	// 2. Single-flight: concurrent cold requests for the same source state compute once.
	defaultArtifactProofCache = newArtifactProofCache()
	fingerprintCalls.Store(0)
	entered := make(chan struct{})
	release := make(chan struct{})
	var enterOnce sync.Once
	artifactSourceFingerprint = func(ctx context.Context, r string) (string, error) {
		fingerprintCalls.Add(1)
		enterOnce.Do(func() { close(entered) })
		<-release
		return origFingerprint(ctx, r)
	}

	const concurrentCallers = 6
	var wg sync.WaitGroup
	codes := make([]int, concurrentCallers)
	for idx := range concurrentCallers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := doUpgradeArtifactRequest(t, handler, http.MethodGet, testPrefix+ArtifactStatusSuffix, bearer(testAdminKey), "", nil, 0)
			codes[i] = rec.Code
		}(idx)
	}
	<-entered
	time.Sleep(15 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("single-flight caller %d = %d, want 200", i, code)
		}
	}
	if got := fingerprintCalls.Load(); got != 1 {
		t.Fatalf("single-flight fingerprintCalls = %d, want 1", got)
	}

	// 3. Cold timeout returns 503 with upgrade_credential_timeout, does not cache, and makes 0 Supervisor calls.
	defaultArtifactProofCache = newArtifactProofCache()
	artifactSourceFingerprint = func(ctx context.Context, _ string) (string, error) {
		return "", context.DeadlineExceeded
	}
	beforeSup := supervisorCalls.Load()
	timeoutRec := doUpgradeArtifactRequest(t, handler, http.MethodGet, testPrefix+ArtifactStatusSuffix, bearer(testAdminKey), "", nil, 0)
	if timeoutRec.Code != http.StatusServiceUnavailable || !strings.Contains(timeoutRec.Body.String(), `"`+CodeCredentialTimeout+`"`) {
		t.Fatalf("cold timeout = %d: %s, want 503 %s", timeoutRec.Code, timeoutRec.Body.String(), CodeCredentialTimeout)
	}
	if supervisorCalls.Load() != beforeSup {
		t.Fatalf("cold timeout contacted Supervisor")
	}

	// 4. Restoring real fingerprint function succeeds on retry, and modifying a file in root
	// invalidates the metadata cache and returns 409 upgrade_artifact_unavailable without caching.
	fingerprintCalls.Store(0)
	artifactSourceFingerprint = func(ctx context.Context, r string) (string, error) {
		fingerprintCalls.Add(1)
		return origFingerprint(ctx, r)
	}
	retryRec := doUpgradeArtifactRequest(t, handler, http.MethodGet, testPrefix+ArtifactStatusSuffix, bearer(testAdminKey), "", nil, 0)
	if retryRec.Code != http.StatusOK || fingerprintCalls.Load() != 1 {
		t.Fatalf("retry after timeout = %d (calls=%d), want 200 and 1", retryRec.Code, fingerprintCalls.Load())
	}

	if err := os.WriteFile(filepath.Join(root, "unexpected-change"), []byte("mutated"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeSup = supervisorCalls.Load()
	mutatedRec := doUpgradeArtifactRequest(t, handler, http.MethodGet, testPrefix+ArtifactStatusSuffix, bearer(testAdminKey), "", nil, 0)
	if mutatedRec.Code != http.StatusConflict || !strings.Contains(mutatedRec.Body.String(), `"`+CodeArtifactUnavailable+`"`) {
		t.Fatalf("mutated source = %d: %s, want 409", mutatedRec.Code, mutatedRec.Body.String())
	}
	if fingerprintCalls.Load() != 2 {
		t.Fatalf("fingerprintCalls after file mutation = %d, want 2", fingerprintCalls.Load())
	}
	if supervisorCalls.Load() != beforeSup {
		t.Fatalf("mutated source contacted Supervisor")
	}
}

func TestArtifactSourceProofPollCost(t *testing.T) {
	for _, layout := range []string{"database-16MiB", "wal-16MiB", "archive-64MiB"} {
		t.Run(layout, func(t *testing.T) {
			var root string
			switch layout {
			case "database-16MiB":
				root = largeSourceDir(t, 16)
			case "wal-16MiB":
				root = legacySourceDir(t)
				writer, err := sql.Open("sqlite", filepath.Join(root, "usage.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				writer.SetMaxOpenConns(1)
				t.Cleanup(func() { _ = writer.Close() })
				if _, err := writer.Exec(`pragma journal_mode=wal; pragma wal_autocheckpoint=0; create table poll_padding(value blob)`); err != nil {
					t.Fatal(err)
				}
				tx, err := writer.Begin()
				if err != nil {
					t.Fatal(err)
				}
				for range 16 {
					if _, err := tx.Exec(`insert into poll_padding values(zeroblob(1048576))`); err != nil {
						_ = tx.Rollback()
						t.Fatal(err)
					}
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			case "archive-64MiB":
				root = legacySourceDir(t)
				archiveDir := filepath.Join(root, "usage-archives")
				if err := os.MkdirAll(archiveDir, 0o700); err != nil {
					t.Fatal(err)
				}
				archive, err := os.Create(filepath.Join(archiveDir, "retained-history.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
				if err := archive.Truncate(64 << 20); err != nil {
					_ = archive.Close()
					t.Fatal(err)
				}
				if err := archive.Close(); err != nil {
					t.Fatal(err)
				}
			}

			origFingerprint := artifactSourceFingerprint
			var hashCalls atomic.Int32
			artifactSourceFingerprint = func(ctx context.Context, r string) (string, error) {
				hashCalls.Add(1)
				return origFingerprint(ctx, r)
			}
			t.Cleanup(func() {
				artifactSourceFingerprint = origFingerprint
			})

			database := recordedArtifactStore(t, foreignKey, root)
			var supCalls atomic.Int32
			_, client := newFakeArtifactSupervisor(t, &supCalls)
			handler := newHandler(root, database)
			handler.Artifact = client

			warm := doUpgradeArtifactRequest(t, handler, http.MethodGet, testPrefix+StatusSuffix, bearer(testAdminKey), "", nil, 0)
			if warm.Code != http.StatusOK {
				t.Fatalf("warm credential: status=%d body=%s", warm.Code, warm.Body.String())
			}

			for poll := range 3 {
				result := doUpgradeArtifactRequest(t, handler, http.MethodGet, testPrefix+ArtifactStatusSuffix, bearer(testAdminKey), "", nil, 0)
				if result.Code != http.StatusOK {
					t.Fatalf("%s poll %d = %d: %s", layout, poll, result.Code, result.Body.String())
				}
			}
			if got := hashCalls.Load(); got != 1 {
				t.Fatalf("%s full fingerprint calls across 3 polls = %d, want 1", layout, got)
			}
			if got := supCalls.Load(); got != 3 {
				t.Fatalf("%s Supervisor calls across 3 polls = %d, want 3", layout, got)
			}
		})
	}
}

func TestArtifactAcquireAndProbeValidationAndSupervisorErrorPassthrough(t *testing.T) {
	var supervisorCalls int
	var lastSource, lastPrefix string
	supervisorStatus := http.StatusAccepted
	supervisorPhase := "downloading"
	upstreamErrorStatus := 0
	upstreamErrorCode := ""

	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		supervisorCalls++
		w.Header().Set("Content-Type", "application/json")
		if upstreamErrorStatus != 0 {
			w.WriteHeader(upstreamErrorStatus)
			_, _ = w.Write([]byte(`{"code":"` + upstreamErrorCode + `"}`))
			return
		}
		if r.URL.Path == "/v1/runtime/artifact/probe" {
			_, _ = w.Write([]byte(`{"reachable":true,"code":""}`))
			return
		}
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
		w.WriteHeader(supervisorStatus)
		_, _ = w.Write([]byte(`{"operation":{"id":"op-acq","source":"` + body.Source + `","phase":"` + supervisorPhase + `","receivedBytes":0,"totalBytes":0,"code":""}}`))
	}))
	defer supervisor.Close()

	root := legacySourceDir(t)
	database := recordedArtifactStore(t, v2StoreAdminKey, root)
	handler := newHandler(root, database)
	handler.Artifact = runtimeservice.NewArtifactClient(supervisor.URL, upgradeArtifactTestToken)

	// Probe body validation: empty or `{}` allowed; unknown fields, trailing JSON, or > 4 KiB rejected.
	for _, invalidProbe := range []string{
		`{"extra":true}`,
		`{}{}`,
		`{"` + strings.Repeat("a", 4096) + `":1}`,
	} {
		before := supervisorCalls
		rec := doUpgradeArtifactRequest(t, handler, http.MethodPost, testPrefix+ArtifactProbeSuffix, bearer(testAdminKey), "application/json", strings.NewReader(invalidProbe), int64(len(invalidProbe)))
		if rec.Code != http.StatusBadRequest || supervisorCalls != before {
			t.Fatalf("invalid probe = %d (calls=%d), want 400", rec.Code, supervisorCalls-before)
		}
	}

	acquireTests := []struct {
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
		},
		{
			name:               "github idempotent 200",
			body:               `{"source":"github"}`,
			supervisorStatus:   http.StatusOK,
			supervisorPhase:    "succeeded",
			wantStatus:         http.StatusOK,
			wantSupervisorCall: true,
			wantSource:         "github",
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

	for _, tc := range acquireTests {
		t.Run(tc.name, func(t *testing.T) {
			beforeCalls := supervisorCalls
			if tc.supervisorStatus != 0 {
				supervisorStatus = tc.supervisorStatus
				supervisorPhase = tc.supervisorPhase
			}
			rec := doUpgradeArtifactRequest(
				t,
				handler,
				http.MethodPost,
				testPrefix+ArtifactAcquireSuffix,
				bearer(testAdminKey),
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
				var errResp artifactproxy.ErrorResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil || errResp.Code != tc.wantCode {
					t.Fatalf("error response = %s, want code %q", rec.Body.String(), tc.wantCode)
				}
			}
		})
	}

	// Supervisor error code -> HTTP status passthrough.
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
		upstreamErrorStatus = tc.status
		upstreamErrorCode = tc.code
		rec := doUpgradeArtifactRequest(
			t,
			handler,
			http.MethodPost,
			testPrefix+ArtifactAcquireSuffix,
			bearer(testAdminKey),
			"application/json",
			strings.NewReader(`{"source":"github"}`),
			19,
		)
		if rec.Code != tc.status {
			t.Fatalf("passthrough status for %s = %d, want %d", tc.code, rec.Code, tc.status)
		}
		var got artifactproxy.ErrorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Code != tc.code {
			t.Fatalf("passthrough body for %s = %s, want code %q", tc.code, rec.Body.String(), tc.code)
		}
	}

	// Shut down Supervisor: all 4 routes return 503 runtime_unavailable.
	supervisor.Close()
	for _, route := range upgradeArtifactRouteSpecs() {
		rec := doUpgradeArtifactRequest(
			t,
			handler,
			route.method,
			testPrefix+route.suffix,
			bearer(testAdminKey),
			route.contentType,
			strings.NewReader(route.body),
			int64(len(route.body)),
		)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("Supervisor down %s %s = %d, want 503", route.method, route.suffix, rec.Code)
		}
		var got artifactproxy.ErrorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Code != "runtime_unavailable" {
			t.Fatalf("Supervisor down %s %s body = %s, want runtime_unavailable", route.method, route.suffix, rec.Body.String())
		}
	}
}

func TestArtifactUploadValidationAndStreamingExactBytes(t *testing.T) {
	const maxUpload = artifactproxy.MaxUploadBytes

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

	root := legacySourceDir(t)
	database := recordedArtifactStore(t, v2StoreAdminKey, root)
	handler := newHandler(root, database)
	handler.Artifact = runtimeservice.NewArtifactClient(supervisor.URL, upgradeArtifactTestToken)

	// 1. Wrong or missing Content-Type is rejected with 400 invalid_request without contacting Supervisor.
	for _, ct := range []string{"", "application/octet-stream", "application/json", "text/plain"} {
		rec := doUpgradeArtifactRequest(t, handler, http.MethodPut, testPrefix+ArtifactUploadSuffix, bearer(testAdminKey), ct, strings.NewReader("data"), 4)
		if rec.Code != http.StatusBadRequest || supervisorUploadCalls.Load() != 0 {
			t.Fatalf("upload with Content-Type %q = %d (calls=%d), want 400", ct, rec.Code, supervisorUploadCalls.Load())
		}
	}

	// 2. Valid archive streams exact bytes with Content-Type: application/gzip and Content-Length preserved.
	archive := bytes.Repeat([]byte{0x1f, 0x8b, 0x08, 0x00, 0xca, 0xfe, 0xba, 0xbe}, 16384)
	expectedDigest := sha256.Sum256(archive)
	rec := doUpgradeArtifactRequest(
		t,
		handler,
		http.MethodPut,
		testPrefix+ArtifactUploadSuffix,
		bearer(testAdminKey),
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
	rec = doUpgradeArtifactRequest(
		t,
		handler,
		http.MethodPut,
		testPrefix+ArtifactUploadSuffix,
		bearer(testAdminKey),
		"application/gzip",
		io.LimitReader(upgradeZeroReader{}, oversizedLength),
		oversizedLength,
	)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized known-length upload = %d, want 413: %s", rec.Code, rec.Body.String())
	}
	var errResp artifactproxy.ErrorResponse
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
	rec = doUpgradeArtifactRequest(
		t,
		handler,
		http.MethodPut,
		testPrefix+ArtifactUploadSuffix,
		bearer(testAdminKey),
		"application/gzip",
		io.LimitReader(upgradeZeroReader{}, oversizedLength),
		-1,
	)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized streaming upload = %d, want 413: %s", rec.Code, rec.Body.String())
	}
	errResp = artifactproxy.ErrorResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &errResp); err != nil || errResp.Code != "archive_too_large" {
		t.Fatalf("oversized streaming body = %s, want archive_too_large", rec.Body.String())
	}
	if completedFullRead.Load() || receivedBytes.Load() >= oversizedLength {
		t.Fatalf("Supervisor received full oversized body (completed=%v, bytes=%d, total=%d)", completedFullRead.Load(), receivedBytes.Load(), oversizedLength)
	}
}

type upgradeZeroReader struct{}

func (upgradeZeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
