package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
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
				nextActions["status"] = "reconcile"
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
		strings.Replace(base, "/api/v2/", "/api/v2//", 1) + "/status",
	} {
		request(newHandler(), http.MethodGet, path, "Bearer "+upgradeWizardAdminKey, http.StatusNotFound)
	}
	h := newHandler()
	h.BeginWritableStartup()
	for _, resource := range []string{"preflight", "status"} {
		request(h, http.MethodGet, base+"/"+resource, "Bearer "+upgradeWizardAdminKey, http.StatusNotFound)
	}
	h = newHandler()
	h.SetReady(http.NotFoundHandler())
	for _, resource := range []string{"preflight", "status"} {
		request(h, http.MethodGet, base+"/"+resource, "Bearer "+upgradeWizardAdminKey, http.StatusNotFound)
	}
}
