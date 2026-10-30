package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
)

// upgradeWizardLegacyRoot writes the smallest 1.x data directory a read-only
// preflight can inspect.
func upgradeWizardLegacyRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data.key"), []byte("legacy-data-key-material"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "usage.sqlite"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// commitInstallationRouting gives the installation its committed control
// prefix, which is what makes the continuation session usable. The session
// issued before the commit stays valid across it.
func commitInstallationRouting(t *testing.T, app *freshApplication) {
	t.Helper()
	record, err := app.store.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.Update(t.Context(), record.Operation.Identity, record.Operation.Revision, func(next *freshstore.Record) error {
		next.Operation.Routing = bootstrap.FreshRoutingCheckpoint{
			OperationID: next.Operation.Identity.OperationID, Revision: 1,
			Target: "/control", Stage: bootstrap.PrefixCommitted, Committed: true,
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeWizardReadRoutesFollowTheInstallationSession is the route contract
// at the process boundary. Before cutover the installation session reads both
// routes on the fixed Bootstrap API; after cutover but before completion the
// same session reads them on the canonical continuation prefix; after
// completion both surfaces refuse it. A wrong credential is 401 and a write is
// 405 on every surface.
func TestUpgradeWizardReadRoutesFollowTheInstallationSession(t *testing.T) {
	app, token := setupContinuationApp(t)
	legacy := upgradeWizardLegacyRoot(t)
	t.Setenv(legacyRootEnvironment, legacy)
	database := filepath.Join(t.TempDir(), "manager.sqlite")
	app.cfg.DBPath = database
	request := func(method, path, auth string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	const (
		fixedPrefix     = "fixed"
		canonicalPrefix = "canonical"
	)
	canonical := func(resource string) string { return "/control/api/v2/" + resource }
	checkSurface := func(surface string, urlFor func(string) string, phase string) {
		t.Helper()
		for _, resource := range []string{"installation/upgrade/preflight", "installation/upgrade/status"} {
			target := urlFor(resource)
			if w := request(http.MethodGet, target, ""); w.Code != http.StatusUnauthorized {
				t.Fatalf("%s (%s) without a session = %d", target, phase, w.Code)
			}
			// The fixed Bootstrap API takes only the installation session; the
			// canonical continuation also accepts its own Admin credential.
			admin := http.StatusUnauthorized
			if surface == canonicalPrefix {
				admin = http.StatusOK
			}
			if w := request(http.MethodGet, target, "Bearer "+strategyTestAdmin); w.Code != admin {
				t.Fatalf("%s (%s) with an Admin key = %d, want %d", target, phase, w.Code, admin)
			}
			if w := request(http.MethodPost, target, "Fresh "+token); w.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s (%s) write = %d", target, phase, w.Code)
			}
			w := request(http.MethodGet, target, "Fresh "+token)
			if w.Code != http.StatusOK {
				t.Fatalf("%s (%s) = %d: %s", target, phase, w.Code, w.Body.String())
			}
			if body := w.Body.String(); !strings.Contains(body, `"contractVersion"`) ||
				strings.Contains(body, legacy) || strings.Contains(body, "legacy-data-key-material") {
				t.Fatalf("%s (%s) body = %s", target, phase, body)
			}
		}
	}

	// 1. Installation session before route cutover: the fixed Bootstrap API.
	checkSurface(fixedPrefix, func(resource string) string { return bootstrap.FixedBootstrapAPIPath + resource }, "pre-cutover")

	// 2. Canonical continuation after cutover, before completion.
	commitInstallationRouting(t, app)
	if w := request(http.MethodGet, bootstrap.FixedBootstrapAPIPath+"installation/upgrade/status", "Fresh "+token); w.Code != http.StatusNotFound {
		t.Fatalf("fixed surface survived cutover: %d", w.Code)
	}
	checkSurface(canonicalPrefix, canonical, "pre-completion")

	// A recorded operation reaches the projection without its fingerprints.
	db, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	if err := upgradeoperation.New(db).Put(t.Context(), model.UpgradeOperation{
		Stage:                   model.UpgradeStageCopyIn,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy},
		SourceKind:              "upgraded-legacy",
		SourceVersion:           "1.14.4",
		SourceFingerprintBefore: "fingerprint-before-secret-value",
		SourceFingerprintAfter:  "fingerprint-after-secret-value",
		BackupManifestSHA256:    "backup-manifest-secret-value",
		ManualItems:             []string{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	w := request(http.MethodGet, canonical("installation/upgrade/status"), "Fresh "+token)
	if w.Code != http.StatusOK {
		t.Fatalf("recorded status = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, secret := range []string{"fingerprint-before-secret-value", "fingerprint-after-secret-value", "backup-manifest-secret-value", database} {
		if strings.Contains(body, secret) {
			t.Fatalf("status leaked %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, `"stage":"copy_in"`) || !strings.Contains(body, `"nextAction":"copy_in"`) {
		t.Fatalf("recorded status = %s", body)
	}

	// 3. Completed installation: no Upgrade read on either surface.
	markFreshProductComplete(t, app)
	for _, path := range []string{canonical("installation/upgrade/preflight"), canonical("installation/upgrade/status")} {
		if w := request(http.MethodGet, path, "Fresh "+token); w.Code != http.StatusUnauthorized {
			t.Fatalf("%s after completion = %d", path, w.Code)
		}
	}
}
