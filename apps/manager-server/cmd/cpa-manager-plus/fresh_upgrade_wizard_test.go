package main

import (
	"context"
	"database/sql"
	"encoding/json"
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
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

// upgradeWizardAdminKey is the 1.x Manager administrator credential the Upgrade
// entry page verifies against the source. It is deliberately not the Fresh
// installation's Admin key or session: the wizard surface never borrows the
// installation's identity.
const upgradeWizardAdminKey = "Cpamp_Legacy_Upgrade_Admin_Secret_11440"

// upgradeWizardLegacyRoot writes the smallest 1.x data directory a read-only
// preflight can inspect, carrying the administrator credential the entry page
// verifies.
func upgradeWizardLegacyRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data.key"), []byte("legacy-data-key-material"), 0o600); err != nil {
		t.Fatal(err)
	}
	credential, err := security.NewAdminCredential(upgradeWizardAdminKey, "test")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(credential)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "usage.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`pragma journal_mode = delete;
create table settings (key text primary key, value text not null);
create table usage_events (id integer primary key autoincrement, event_hash text not null unique, timestamp_ms integer not null);`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into settings (key, value) values ('admin_credential_v1', ?)`, string(raw)); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "usage.sqlite.manager.lock"), nil, 0o600); err != nil {
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

// TestUpgradeWizardReadRoutesUseTheSourceAdministratorCredential is the route
// contract at the process boundary. The credential the Upgrade entry page
// verified is the only one both read routes accept, on the fixed Bootstrap API
// before cutover and on the canonical continuation before completion; the Fresh
// installation session and the Fresh Admin key are refused, a write is a method
// error, and after completion the resources stop existing.
func TestUpgradeWizardReadRoutesUseTheSourceAdministratorCredential(t *testing.T) {
	app, token := setupContinuationApp(t)
	legacy := upgradeWizardLegacyRoot(t)
	t.Setenv(legacyRootEnvironment, legacy)
	database := filepath.Join(t.TempDir(), "manager.sqlite")
	app.cfg.DBPath = database

	// The entry page proves the operator's key against the source before the
	// wizard is reachable at all; the routes must accept exactly that credential.
	verified := false
	legacypreflight.WithAuthenticatedInspection(context.Background(),
		legacypreflight.DefaultPaths(filepath.Join(legacy, "usage.sqlite")), upgradeWizardAdminKey,
		func(_ legacypreflight.Report, err error) {
			if err != nil {
				t.Fatalf("the entry page's verification refused the credential: %v", err)
			}
			verified = true
		})
	if !verified {
		t.Fatal("the entry page's verification never observed the source")
	}

	request := func(method, path, authorization string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, nil)
		if authorization != "" {
			r.Header.Set("Authorization", authorization)
		}
		w := httptest.NewRecorder()
		app.ServeHTTP(w, r)
		return w
	}
	canonical := func(resource string) string { return "/control/api/v2/" + resource }
	checkSurface := func(surface string, urlFor func(string) string, phase string) {
		t.Helper()
		for _, resource := range []string{"installation/upgrade/preflight", "installation/upgrade/status"} {
			target := urlFor(resource)
			if w := request(http.MethodGet, target, ""); w.Code != http.StatusUnauthorized {
				t.Fatalf("%s (%s) without a credential = %d", target, phase, w.Code)
			}
			for name, authorization := range map[string]string{
				"installation session": "Fresh " + token,
				"fresh admin key":      "Bearer " + strategyTestAdmin,
				"foreign key":          "Bearer another-Manager-key",
			} {
				if w := request(http.MethodGet, target, authorization); w.Code != http.StatusUnauthorized {
					t.Fatalf("%s (%s) with the %s = %d", target, phase, name, w.Code)
				}
			}
			if w := request(http.MethodPost, target, "Bearer "+upgradeWizardAdminKey); w.Code != http.StatusMethodNotAllowed {
				t.Fatalf("%s (%s) write = %d", target, phase, w.Code)
			}
			w := request(http.MethodGet, target, "Bearer "+upgradeWizardAdminKey)
			if w.Code != http.StatusOK {
				t.Fatalf("%s (%s) = %d: %s", target, phase, w.Code, w.Body.String())
			}
			body := w.Body.String()
			if !strings.Contains(body, `"contractVersion"`) {
				t.Fatalf("%s (%s) body = %s", target, phase, body)
			}
			for _, secret := range []string{legacy, "legacy-data-key-material", upgradeWizardAdminKey} {
				if strings.Contains(body, secret) {
					t.Fatalf("%s (%s) leaked %q: %s", target, phase, secret, body)
				}
			}
		}
	}

	// 1. Before route cutover: the fixed Bootstrap API.
	checkSurface("fixed", func(resource string) string { return bootstrap.FixedBootstrapAPIPath + resource }, "pre-cutover")

	// 2. Canonical continuation after cutover, before completion.
	commitInstallationRouting(t, app)
	if w := request(http.MethodGet, bootstrap.FixedBootstrapAPIPath+"installation/upgrade/status", "Bearer "+upgradeWizardAdminKey); w.Code != http.StatusNotFound {
		t.Fatalf("fixed surface survived cutover: %d", w.Code)
	}
	checkSurface("canonical", canonical, "pre-completion")

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
	w := request(http.MethodGet, canonical("installation/upgrade/status"), "Bearer "+upgradeWizardAdminKey)
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

	// 3. Completed installation: the Upgrade reads stop existing, for the
	// source credential as much as for the installation session.
	markFreshProductComplete(t, app)
	for _, path := range []string{canonical("installation/upgrade/preflight"), canonical("installation/upgrade/status")} {
		for _, authorization := range []string{"Bearer " + upgradeWizardAdminKey, "Fresh " + token, ""} {
			if w := request(http.MethodGet, path, authorization); w.Code != http.StatusNotFound {
				t.Fatalf("%s after completion = %d", path, w.Code)
			}
		}
	}
}
