package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradeauth"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

// TestStartupUpgradeSurfaceAcceptsOneCredentialEverywhere is the M1 contract at
// the process boundary. The Upgrade surface resolves one credential file: the
// configured 1.x source when it holds a database, otherwise the Manager's own
// store. Two sources with two different keys must therefore not split the
// surface: the resolved key opens the entry leaf and both reads, and the key in
// the other file is refused by all three.
//
// The entry leaf additionally runs the public discovery observation, which
// deliberately refuses a WAL source as recovery-owned. The "unobservable" case
// below records what that boundary does: the reads still verify the key, and the
// entry leaf names the source instead of blaming the credential.
func TestStartupUpgradeSurfaceAcceptsOneCredentialEverywhere(t *testing.T) {
	const (
		legacyKey  = "legacy-source-administrator-key"
		managerKey = "manager-store-administrator-key"
	)
	base := bootstrap.FixedBootstrapAPIPath + "installation/upgrade"
	cases := []struct {
		name         string
		mountLegacy  bool
		observable   bool
		accepted     string
		refused      string
		entryStatus  int
		entryCode    string
		preflight    int
		preflightErr string
		sourceRoot   string
	}{
		{
			name: "legacy mount wins", mountLegacy: true, observable: false,
			accepted: legacyKey, refused: managerKey,
			entryStatus: http.StatusOK,
			preflight:   http.StatusOK,
			sourceRoot:  "available",
		},
		{
			name: "manager store without a mount", mountLegacy: false, observable: true,
			accepted: managerKey, refused: legacyKey,
			entryStatus:  http.StatusOK,
			preflight:    http.StatusServiceUnavailable,
			preflightErr: "upgrade_legacy_root_missing",
			sourceRoot:   "upgrade_legacy_root_missing",
		},
		{
			name: "unobservable manager store is not a credential verdict", mountLegacy: false, observable: false,
			accepted: managerKey, refused: legacyKey,
			entryStatus:  http.StatusServiceUnavailable,
			entryCode:    "source_unavailable",
			preflight:    http.StatusServiceUnavailable,
			preflightErr: "upgrade_legacy_root_missing",
			sourceRoot:   "upgrade_legacy_root_missing",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			legacyRoot, _ := upgradeAuthSource(t, legacyKey)
			if testCase.mountLegacy {
				t.Setenv(legacyRootEnvironment, legacyRoot)
			} else {
				t.Setenv(legacyRootEnvironment, "")
				if configuredLegacyRoot() != "" {
					t.Skipf("this host mounts %s, so there is no unmounted case", defaultLegacyRoot)
				}
			}
			database := upgradeSurfaceManagerStore(t, managerKey, testCase.observable)

			h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
			h.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(database), legacypreflight.Options{}, 5*time.Second)
			if h.upgradeAuth == nil || h.upgradeWizard == nil {
				t.Fatal("the Upgrade surface was not mounted")
			}
			server := httptest.NewServer(h)
			defer server.Close()

			request := func(method, path, authorization, origin string) (int, string) {
				t.Helper()
				req, err := http.NewRequest(method, server.URL+path, nil)
				if err != nil {
					t.Fatal(err)
				}
				if authorization != "" {
					req.Header.Set("Authorization", "Bearer "+authorization)
				}
				if origin != "" {
					req.Header.Set("Origin", origin)
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
				return response.StatusCode, string(body)
			}

			// The resolved key is the surface's credential. The entry leaf names
			// an unobservable source as a source condition, never as a wrong key.
			status, body := request(http.MethodPost, upgradeauth.Path, testCase.accepted, "")
			if status != testCase.entryStatus {
				t.Fatalf("entry page with the resolved key = %d want %d: %s", status, testCase.entryStatus, body)
			}
			if testCase.entryCode != "" && !strings.Contains(body, `"`+testCase.entryCode+`"`) {
				t.Fatalf("entry page did not name %s: %s", testCase.entryCode, body)
			}
			if testCase.entryStatus == http.StatusOK && !strings.Contains(body, `"status":"unsupported_upgrade"`) {
				t.Fatalf("entry page accepted the key without a projection: %s", body)
			}

			// Both reads answer the recorded operation from the Manager store,
			// mounted source or not.
			status, body = request(http.MethodGet, base+"/status", testCase.accepted, "")
			if status != http.StatusOK {
				t.Fatalf("status with the resolved key = %d: %s", status, body)
			}
			for _, want := range []string{
				`"recorded":true`, `"evidence":"operation_row"`, `"stage":"copy_in"`,
				`"nextAction":"copy_in"`, `"sourceRoot":"` + testCase.sourceRoot + `"`,
			} {
				if !strings.Contains(body, want) {
					t.Fatalf("status with the resolved key is missing %s: %s", want, body)
				}
			}
			status, body = request(http.MethodGet, base+"/preflight", testCase.accepted, "")
			if status != testCase.preflight {
				t.Fatalf("preflight with the resolved key = %d want %d: %s", status, testCase.preflight, body)
			}
			if testCase.preflightErr != "" && !strings.Contains(body, `"`+testCase.preflightErr+`"`) {
				t.Fatalf("preflight did not name %s: %s", testCase.preflightErr, body)
			}

			// The other file's key is a wrong key on every leaf, never a source
			// verdict: the surface has one credential, not two.
			for name, target := range map[string][2]string{
				"entry page": {http.MethodPost, upgradeauth.Path},
				"preflight":  {http.MethodGet, base + "/preflight"},
				"status":     {http.MethodGet, base + "/status"},
			} {
				status, body = request(target[0], target[1], testCase.refused, "")
				if status != http.StatusUnauthorized || !strings.Contains(body, `"admin_unauthorized"`) {
					t.Fatalf("%s with the other file's key = %d: %s", name, status, body)
				}
			}

			// L4 at the surface boundary: a cross-site browser gets the entry
			// leaf's answer on the reads as well, with the resolved key.
			for _, resource := range []string{"/preflight", "/status"} {
				status, body = request(http.MethodGet, base+resource, testCase.accepted, "https://attacker.test")
				if status != http.StatusForbidden || !strings.Contains(body, `"origin_rejected"`) {
					t.Fatalf("%s from another site = %d: %s", resource, status, body)
				}
			}
		})
	}
}

// upgradeSurfaceManagerStore writes a v2 Manager store that carries both the
// administrator credential and the recorded copy_in operation row. observable
// switches the file to the rollback journal: the entry leaf additionally runs
// the public discovery observation, which refuses a WAL source as
// recovery-owned, so only that journal mode can prove the accepted key end to
// end. A Manager that has actually run writes WAL, which the reads accept.
func upgradeSurfaceManagerStore(t *testing.T, key string, observable bool) string {
	t.Helper()
	database := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(database)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := security.NewAdminCredential(key, "test")
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := setting.New(db).SaveAdminCredential(t.Context(), credential); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := upgradeoperation.New(db).Put(t.Context(), model.UpgradeOperation{
		Stage:                   model.UpgradeStageCopyIn,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy},
		SourceKind:              "upgraded-legacy",
		SourceVersion:           "1.14.4",
		SourceFingerprintBefore: "fingerprint-before-value",
		SourceFingerprintAfter:  "fingerprint-after-value",
		BackupManifestSHA256:    "backup-manifest-value",
		ManualItems:             []string{},
	}); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if observable {
		if _, err := db.Exec(`pragma journal_mode = delete`); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// The ownership marker is read-only evidence: the credential inspection
	// probes it, so the fixture carries one like a real stopped source.
	if err := os.WriteFile(database+".manager.lock", nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return database
}
