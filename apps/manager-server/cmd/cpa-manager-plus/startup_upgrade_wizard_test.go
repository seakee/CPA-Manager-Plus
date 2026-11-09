package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
)

// TestStartupServesTheUpgradeWizardFromTheCopiedOperation is the post-copy-in
// continuation at the process boundary. A copied v2 root carries the operation
// row the copy-in recorded but no Fresh record, so the canonical continuation
// has nothing to authorize: the reads must be reachable on the fixed Bootstrap
// surface, beside the Upgrade entry leaf, with the source administrator key the
// entry verified. The source credential stays the only accepted one, a write is
// a method verdict that only a proved credential sees, and completion ends both
// reads on every surface.
func TestStartupServesTheUpgradeWizardFromTheCopiedOperation(t *testing.T) {
	for _, stage := range []model.UpgradeStage{model.UpgradeStageCopyIn, model.UpgradeStageReconcile} {
		t.Run(string(stage), func(t *testing.T) {
			source := upgradeWizardLegacyRoot(t)
			t.Setenv(legacyRootEnvironment, source)
			database := filepath.Join(t.TempDir(), "usage.sqlite")
			db, err := sqliterepo.Open(database)
			if err != nil {
				t.Fatal(err)
			}
			if err := upgradeoperation.New(db).Put(t.Context(), model.UpgradeOperation{
				Stage:                   stage,
				DomainsDone:             []string{model.UpgradeDomainSourceCopy},
				SourceKind:              "upgraded-legacy",
				SourceVersion:           "1.14.4",
				SourceFingerprintBefore: "fingerprint-before-secret-value",
				SourceFingerprintAfter:  "fingerprint-after-secret-value",
				BackupManifestSHA256:    "backup-manifest-secret-value",
				ManualItems:             []string{},
			}); err != nil {
				_ = db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			// The real startup handler, started the way the read-only startup
			// serves an unfinished operation.
			h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
			h.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(database), legacypreflight.Options{}, 5*time.Second)
			server := httptest.NewServer(h)
			defer server.Close()

			request := func(method, path, authorization string) (int, http.Header, string) {
				t.Helper()
				req, err := http.NewRequest(method, server.URL+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				if authorization != "" {
					req.Header.Set("Authorization", authorization)
				}
				response, err := server.Client().Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				return response.StatusCode, response.Header, string(body)
			}

			base := bootstrap.FixedBootstrapAPIPath + "installation/upgrade"
			nextActions := map[string]string{"preflight": "preflight", "status": "copy_in"}
			if stage == model.UpgradeStageReconcile {
				// A stage is a checkpoint, not a pending step: a recorded reconcile
				// means the comparison finished, so the decision it leaves is the
				// CPA import that follows it in model/upgrade_operation.go.
				nextActions["status"] = "cpa_import"
			}
			for _, resource := range []string{"preflight", "status"} {
				target := base + "/" + resource
				// The source credential comes first: no path, method or surface
				// verdict is answered to a caller without it.
				for name, authorization := range map[string]string{
					"anonymous":       "",
					"foreign key":     "Bearer another-Manager-key",
					"installation":    "Fresh fresh-session-token",
					"fresh admin key": "Bearer " + strategyTestAdmin,
				} {
					if status, _, body := request(http.MethodGet, target, authorization); status != http.StatusUnauthorized {
						t.Fatalf("%s with the %s = %d: %s", target, name, status, body)
					}
					if status, _, body := request(http.MethodPost, target, authorization); status != http.StatusUnauthorized {
						t.Fatalf("POST %s with the %s = %d: %s", target, name, status, body)
					}
				}
				// Only a proved credential learns the verb.
				status, headers, body := request(http.MethodPost, target, "Bearer "+upgradeWizardAdminKey)
				if status != http.StatusMethodNotAllowed || headers.Get("Allow") != http.MethodGet {
					t.Fatalf("POST %s = %d allow=%q: %s", target, status, headers.Get("Allow"), body)
				}
				status, headers, body = request(http.MethodGet, target, "Bearer "+upgradeWizardAdminKey)
				if status != http.StatusOK {
					t.Fatalf("GET %s = %d: %s", target, status, body)
				}
				if cache := headers.Get("Cache-Control"); cache != "no-store" {
					t.Fatalf("GET %s cache = %q", target, cache)
				}
				if !strings.Contains(body, `"contractVersion":"cpamp.upgrade-wizard.v1"`) {
					t.Fatalf("GET %s body = %s", target, body)
				}
				for _, secret := range []string{
					upgradeWizardAdminKey, "legacy-data-key-material", source,
					filepath.Dir(database), database, "fingerprint-before-secret-value",
					"fingerprint-after-secret-value", "backup-manifest-secret-value",
				} {
					if strings.Contains(body, secret) {
						t.Fatalf("GET %s leaked %q: %s", target, secret, body)
					}
				}
				if resource == "status" {
					if !strings.Contains(body, `"recorded":true`) ||
						!strings.Contains(body, `"stage":"`+string(stage)+`"`) ||
						!strings.Contains(body, `"nextAction":"`+nextActions[resource]+`"`) {
						t.Fatalf("recorded status = %s", body)
					}
				}
			}

			// Completion ends both reads: the fixed surface no longer mounts the
			// Upgrade leaves, and a Fresh session governs the whole handler.
			h.SetCompleted()
			for _, resource := range []string{"preflight", "status"} {
				target := base + "/" + resource
				for _, authorization := range []string{"Bearer " + upgradeWizardAdminKey, ""} {
					if status, _, body := request(http.MethodGet, target, authorization); status != http.StatusNotFound {
						t.Fatalf("GET %s after ProductComplete = %d: %s", target, status, body)
					}
				}
			}
			h.SetFreshHandler(http.NotFoundHandler())
			for _, resource := range []string{"preflight", "status"} {
				target := base + "/" + resource
				if status, _, body := request(http.MethodGet, target, "Bearer "+upgradeWizardAdminKey); status != http.StatusNotFound {
					t.Fatalf("GET %s under a Fresh session = %d: %s", target, status, body)
				}
			}
		})
	}
}

// TestStartupUpgradeWizardStaysClosedOutsideTheUpgradeGate keeps the new reads
// bound to the same gate as the entry leaf: writable startup and readiness both
// close them, and no other fixed-carrier resource becomes readable with a
// source credential.
func TestStartupUpgradeWizardStaysClosedOutsideTheUpgradeGate(t *testing.T) {
	source := upgradeWizardLegacyRoot(t)
	t.Setenv(legacyRootEnvironment, source)
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := upgradeoperation.New(db).Put(t.Context(), model.UpgradeOperation{
		Stage:       model.UpgradeStageCopyIn,
		DomainsDone: []string{model.UpgradeDomainSourceCopy},
		SourceKind:  "upgraded-legacy",
		ManualItems: []string{},
	}); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	newHandler := func() *startupHTTPHandler {
		h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
		h.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(database), legacypreflight.Options{}, 5*time.Second)
		return h
	}
	request := func(handler http.Handler, method, path, authorization string, want int) {
		t.Helper()
		r := httptest.NewRequest(method, path, nil)
		if authorization != "" {
			r.Header.Set("Authorization", authorization)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s %s = %d want %d: %s", method, path, w.Code, want, w.Body.String())
		}
	}
	base := bootstrap.FixedBootstrapAPIPath + "installation/upgrade"
	// A resource the wizard does not serve stays closed even for a proved
	// credential: the mount is two exact paths, not the whole fixed API.
	for _, path := range []string{
		bootstrap.FixedBootstrapAPIPath + "operation",
		base + "/preflight/extra",
		base + "/status/",
		base + "/runtime/artifact/",
		base + "/runtime/artifact/extra",
		strings.Replace(base, "/api/v2/", "/api/v2//", 1) + "/status",
	} {
		request(newHandler(), http.MethodGet, path, "Bearer "+upgradeWizardAdminKey, http.StatusNotFound)
	}
	artifactRoutes := []struct {
		method   string
		resource string
	}{
		{http.MethodGet, "preflight"},
		{http.MethodGet, "status"},
		{http.MethodGet, "runtime/artifact"},
		{http.MethodPost, "runtime/artifact/probe"},
		{http.MethodPost, "runtime/artifact/acquire"},
		{http.MethodPut, "runtime/artifact/upload"},
	}
	h := newHandler()
	h.BeginWritableStartup()
	for _, route := range artifactRoutes {
		request(h, route.method, base+"/"+route.resource, "Bearer "+upgradeWizardAdminKey, http.StatusNotFound)
	}
	h = newHandler()
	h.SetReady(http.NotFoundHandler())
	for _, route := range artifactRoutes {
		request(h, route.method, base+"/"+route.resource, "Bearer "+upgradeWizardAdminKey, http.StatusNotFound)
	}
	h = newHandler()
	h.SetCompleted()
	for _, route := range artifactRoutes {
		request(h, route.method, base+"/"+route.resource, "Bearer "+upgradeWizardAdminKey, http.StatusNotFound)
	}
}

// TestStartupUpgradeSurfaceSlimArtifactAcquireLifecycle proves that when the
// slim image starts in upgrade-only mode with CPAMP_RUNTIME_URL and a runtime
// token file, the browser can observe required:true, trigger acquire on the
// pinned artifact, and observe required:false through the Upgrade surface,
// while SetReady closes the artifact endpoints afterward.
func TestStartupUpgradeSurfaceSlimArtifactAcquireLifecycle(t *testing.T) {
	const (
		runtimeToken = "slim-upgrade-runtime-token"
		pinnedID     = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	)
	var acquired atomic.Bool
	supervisor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+runtimeToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"unauthorized"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/runtime/artifact":
			if !acquired.Load() {
				_, _ = w.Write([]byte(`{"schemaVersion":1,"pinned":{"version":"8.0.55","artifactId":"` + pinnedID + `"},"active":null,"required":true,"operation":null}`))
				return
			}
			_, _ = w.Write([]byte(`{"schemaVersion":1,"pinned":{"version":"8.0.55","artifactId":"` + pinnedID + `"},"active":{"artifactId":"` + pinnedID + `","origin":"acquired"},"required":false,"operation":{"id":"op-slim-1","source":"github","phase":"succeeded","receivedBytes":1024,"totalBytes":1024,"code":""}}`))
		case "/v1/runtime/artifact/probe":
			_, _ = w.Write([]byte(`{"reachable":true,"code":""}`))
		case "/v1/runtime/artifact/acquire":
			acquired.Store(true)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"operation":{"id":"op-slim-1","source":"github","phase":"downloading","receivedBytes":0,"totalBytes":1024,"code":""}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer supervisor.Close()

	source := upgradeWizardLegacyRoot(t)
	t.Setenv(legacyRootEnvironment, source)
	tokenFile := filepath.Join(t.TempDir(), "runtime.token")
	if err := os.WriteFile(tokenFile, []byte(runtimeToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CPAMP_RUNTIME_URL", supervisor.URL)
	t.Setenv("CPAMP_RUNTIME_TOKEN_FILE", tokenFile)

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.EmbeddedRuntimeConfigured() {
		t.Fatal("expected EmbeddedRuntimeConfigured() to be true")
	}

	sourceDigest, err := upgradeservice.SourceFingerprint(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := upgradeoperation.New(db).Put(t.Context(), model.UpgradeOperation{
		Stage:                   model.UpgradeStageReconcile,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy},
		SourceKind:              "upgraded-legacy",
		SourceVersion:           "1.14.4",
		SourceRoot:              source,
		SourceFingerprintBefore: sourceDigest,
		SourceFingerprintAfter:  sourceDigest,
		BackupManifestSHA256:    strings.Repeat("b", 64),
		ManualItems:             []string{},
	}); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	tokenSource := runtimeservice.NewFileRuntimeTokenSource(cfg.RuntimeTokenFile)
	h.SetUpgradeArtifact(runtimeservice.NewArtifactClientWithTokenSource(cfg.RuntimeURL, tokenSource))
	h.EnableAdmittedDiscovery(legacypreflight.DefaultPaths(database), legacypreflight.Options{}, 5*time.Second, legacypreflight.Report{
		SourceKind: legacypreflight.SourceKindUpgradedLegacy,
	})

	base := bootstrap.FixedBootstrapAPIPath + "installation/upgrade/runtime/artifact"
	doReq := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Authorization", "Bearer "+upgradeWizardAdminKey)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// 1. Initial status reports slim required:true.
	statusRec := doReq(http.MethodGet, base, "")
	if statusRec.Code != http.StatusOK {
		t.Fatalf("initial GET %s = %d: %s", base, statusRec.Code, statusRec.Body.String())
	}
	var initialStatus struct {
		Required bool                           `json:"required"`
		Active   *runtimeservice.ArtifactActive `json:"active"`
	}
	if err := json.Unmarshal(statusRec.Body.Bytes(), &initialStatus); err != nil {
		t.Fatal(err)
	}
	if !initialStatus.Required || initialStatus.Active != nil {
		t.Fatalf("expected required:true and active:nil, got %+v", initialStatus)
	}

	// 2. Probe succeeds.
	probeRec := doReq(http.MethodPost, base+"/probe", "{}")
	if probeRec.Code != http.StatusOK || !strings.Contains(probeRec.Body.String(), `"reachable":true`) {
		t.Fatalf("POST %s/probe = %d: %s", base, probeRec.Code, probeRec.Body.String())
	}

	// 3. Acquire triggers Supervisor download.
	acqRec := doReq(http.MethodPost, base+"/acquire", `{"source":"github"}`)
	if acqRec.Code != http.StatusAccepted {
		t.Fatalf("POST %s/acquire = %d: %s", base, acqRec.Code, acqRec.Body.String())
	}

	// 4. Subsequent status poll reports required:false and active artifact.
	afterRec := doReq(http.MethodGet, base, "")
	if afterRec.Code != http.StatusOK {
		t.Fatalf("post-acquire GET %s = %d: %s", base, afterRec.Code, afterRec.Body.String())
	}
	var afterStatus struct {
		Required bool                           `json:"required"`
		Active   *runtimeservice.ArtifactActive `json:"active"`
	}
	if err := json.Unmarshal(afterRec.Body.Bytes(), &afterStatus); err != nil {
		t.Fatal(err)
	}
	if afterStatus.Required || afterStatus.Active == nil || afterStatus.Active.ArtifactID != pinnedID {
		t.Fatalf("expected required:false and active %s, got %+v", pinnedID, afterStatus)
	}

	// 5. After SetReady, the upgrade artifact surface is closed.
	h.SetReady(http.NotFoundHandler())
	if closedRec := doReq(http.MethodGet, base, ""); closedRec.Code != http.StatusNotFound {
		t.Fatalf("after SetReady GET %s = %d, want 404", base, closedRec.Code)
	}
}
