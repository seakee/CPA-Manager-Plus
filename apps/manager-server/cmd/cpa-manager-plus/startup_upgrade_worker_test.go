package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	bootstrap "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	bootstrapcontract "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/contract/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/http/controller/upgradeauth"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrapdiscovery"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	upgradeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgrade"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/upgradeworker"
)

func TestStartupWorkerModeSurvivesUnreachableWorkerWithoutFoldingInto401(t *testing.T) {
	targetDir := t.TempDir()
	dbPath := filepath.Join(targetDir, "usage.sqlite")
	dataKeyPath := filepath.Join(targetDir, "data.key")
	if _, _, err := security.LoadOrCreateDataKey("", dataKeyPath); err != nil {
		t.Fatal(err)
	}
	db, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := security.NewAdminCredential(upgradeWizardAdminKey, "legacy")
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := setting.New(db).SaveAdminCredential(t.Context(), cred); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	recordedRevision := strings.Repeat("d", 64)
	if err := upgradeoperation.New(db).Put(t.Context(), model.UpgradeOperation{
		Stage:                   model.UpgradeStageReconcile,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainUsageEvents},
		SourceKind:              "upgraded-legacy",
		SourceVersion:           "unknown",
		SourceFingerprintBefore: strings.Repeat("e", 64),
		SourceFingerprintAfter:  recordedRevision,
		BackupManifestSHA256:    strings.Repeat("f", 64),
		ManualItems:             []string{},
	}); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	missingSocket := filepath.Join(t.TempDir(), "down.sock")
	t.Setenv(upgradeworker.SocketEnvironment, missingSocket)

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(dbPath), legacypreflight.Options{}, 5*time.Second)
	server := httptest.NewServer(h)
	defer server.Close()

	do := func(method, path, auth, origin string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		} else {
			req.Header.Set("Origin", server.URL)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode, string(raw)
	}

	// 0. Root path remains closed (404) on startup Upgrade-only surface.
	if rootCode, _ := do(http.MethodGet, "/", "", ""); rootCode != http.StatusNotFound {
		t.Fatalf("GET / = %d, want 404", rootCode)
	}

	// 1. Discovery projects the prepared Upgrade admin_session contract, never Fresh.
	discCode, discBody := do(http.MethodGet, bootstrapdiscovery.Path, "", "")
	if discCode != http.StatusOK {
		t.Fatalf("discovery = %d: %s", discCode, discBody)
	}
	var discovery bootstrapcontract.DiscoveryDTO
	if err := json.Unmarshal([]byte(discBody), &discovery); err != nil {
		t.Fatalf("decode discovery DTO: %v; body = %s", err, discBody)
	}
	if discovery.Status != bootstrap.PublicUnsupportedUpgrade ||
		discovery.Mode == nil || *discovery.Mode != bootstrap.UnsupportedUpgrade ||
		discovery.AuthEntry != bootstrap.AuthEntryAdminSession ||
		discovery.Reason != bootstrap.ReasonQualificationUnavailable ||
		!discovery.AuthenticationRequired {
		t.Fatalf("discovery = %+v, want unsupported_upgrade/admin_session", discovery)
	}

	// 2. Entry auth rejects invalid credentials with 401 and cross-origin with 403
	// cpamp.upgrade-auth.v1, while valid inherited admin succeeds from cached
	// digest with observation_unavailable instead of folding socket error into 401.
	if code, _ := do(http.MethodPost, upgradeauth.Path, "Bearer wrong-key", ""); code != http.StatusUnauthorized {
		t.Fatalf("wrong entry password = %d, want 401", code)
	}
	crossEntryCode, crossEntryBody := do(http.MethodPost, upgradeauth.Path, "Bearer "+upgradeWizardAdminKey, "https://other.example")
	var crossEntryDoc struct {
		ContractVersion string `json:"contract_version"`
		Error           struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(crossEntryBody), &crossEntryDoc); err != nil {
		t.Fatalf("decode cross-origin entry JSON: %v (%s)", err, crossEntryBody)
	}
	if crossEntryCode != http.StatusForbidden || crossEntryDoc.ContractVersion != upgradeauth.Contract || crossEntryDoc.Error.Code != "origin_rejected" {
		t.Fatalf("cross-origin entry = %d (%+v)", crossEntryCode, crossEntryDoc)
	}
	entryCode, entryBody := do(http.MethodPost, upgradeauth.Path, "Bearer "+upgradeWizardAdminKey, "")
	if entryCode != http.StatusOK {
		t.Fatalf("valid entry auth while worker down = %d: %s", entryCode, entryBody)
	}
	var entryDoc struct {
		ContractVersion   string `json:"contract_version"`
		Authenticated     bool   `json:"authenticated"`
		Status            string `json:"status"`
		SourceRevision    string `json:"source_revision"`
		OperationRevision string `json:"operation_revision"`
	}
	if err := json.Unmarshal([]byte(entryBody), &entryDoc); err != nil {
		t.Fatalf("decode entry response: %v", err)
	}
	expectedOpRev := upgradeworker.OperationRevision(model.UpgradeOperation{
		Stage:                   model.UpgradeStageReconcile,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainUsageEvents},
		SourceKind:              "upgraded-legacy",
		SourceVersion:           "unknown",
		SourceFingerprintBefore: strings.Repeat("e", 64),
		SourceFingerprintAfter:  recordedRevision,
		BackupManifestSHA256:    strings.Repeat("f", 64),
		ManualItems:             []string{},
	})
	if entryDoc.ContractVersion != upgradeauth.Contract || !entryDoc.Authenticated || entryDoc.Status != "observation_unavailable" ||
		entryDoc.SourceRevision != recordedRevision || entryDoc.OperationRevision != expectedOpRev {
		t.Fatalf("unexpected entry doc: %+v (want operation_revision=%q)", entryDoc, expectedOpRev)
	}

	// 3. Status route projects recorded stage + execution.available=false with
	// upgrade_worker_unavailable without claiming sourceRoot=="available".
	statusPath := bootstrap.FixedBootstrapAPIPath + "installation/upgrade/status"
	statusCode, statusBody := do(http.MethodGet, statusPath, "Bearer "+upgradeWizardAdminKey, "")
	if statusCode != http.StatusOK {
		t.Fatalf("degraded status = %d: %s", statusCode, statusBody)
	}
	var statusDoc struct {
		Recorded   bool                     `json:"recorded"`
		Stage      string                   `json:"stage"`
		SourceRoot string                   `json:"sourceRoot"`
		Execution  *upgradeworker.Execution `json:"execution"`
	}
	if err := json.Unmarshal([]byte(statusBody), &statusDoc); err != nil {
		t.Fatalf("decode degraded status: %v", err)
	}
	if !statusDoc.Recorded || statusDoc.Stage != "reconcile" || statusDoc.SourceRoot != upgradeworker.CodeUnavailable {
		t.Fatalf("unexpected degraded status: %+v", statusDoc)
	}
	if statusDoc.Execution == nil || statusDoc.Execution.Available || statusDoc.Execution.Code != upgradeworker.CodeUnavailable {
		t.Fatalf("unexpected degraded execution: %+v", statusDoc.Execution)
	}
}

func TestStartupWorkerModeFailsClosedOnUnpreparedEmptyTarget(t *testing.T) {
	emptyDir := t.TempDir()
	emptyDB := filepath.Join(emptyDir, "usage.sqlite")
	missingSocket := filepath.Join(t.TempDir(), "down.sock")
	t.Setenv(upgradeworker.SocketEnvironment, missingSocket)

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	h.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(emptyDB), legacypreflight.Options{}, 5*time.Second)
	server := httptest.NewServer(h)
	defer server.Close()

	discRes, err := server.Client().Get(server.URL + bootstrapdiscovery.Path)
	if err != nil {
		t.Fatal(err)
	}
	discRaw, _ := io.ReadAll(discRes.Body)
	_ = discRes.Body.Close()
	var unprepDisc bootstrapcontract.DiscoveryDTO
	if err := json.Unmarshal(discRaw, &unprepDisc); err != nil {
		t.Fatalf("decode unprepared discovery DTO: %v (%s)", err, discRaw)
	}
	if discRes.StatusCode != http.StatusServiceUnavailable || unprepDisc.Status != bootstrap.PublicUnavailable || unprepDisc.Mode != nil {
		t.Fatalf("unprepared worker target discovery = %d (%+v), want 503 unavailable", discRes.StatusCode, unprepDisc)
	}

	authReq, err := http.NewRequest(http.MethodPost, server.URL+upgradeauth.Path, nil)
	if err != nil {
		t.Fatal(err)
	}
	authReq.Header.Set("Authorization", "Bearer "+upgradeWizardAdminKey)
	authReq.Header.Set("Origin", server.URL)
	authRes, err := server.Client().Do(authReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = authRes.Body.Close()
	if authRes.StatusCode != http.StatusNotFound {
		t.Fatalf("unprepared worker target entry auth = %d, want 404", authRes.StatusCode)
	}
}

func TestStartupWorkerModeRejectsNonUpgradeStoresAndFailsClosedWithUnreadableWAL(t *testing.T) {
	adminKey, err := security.GenerateAdminKey()
	if err != nil {
		t.Fatalf("GenerateAdminKey: %v", err)
	}

	t.Run("native_store_without_upgrade_operation_fails_closed", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "usage.sqlite")
		db, err := sqliterepo.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		cred, err := security.NewAdminCredential(adminKey, "native")
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := setting.New(db).SaveAdminCredential(t.Context(), cred); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dbPath+".manager.lock", nil, 0o600); err != nil {
			t.Fatal(err)
		}

		t.Setenv(upgradeworker.SocketEnvironment, filepath.Join(t.TempDir(), "down.sock"))
		h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
		h.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(dbPath), legacypreflight.Options{}, 5*time.Second)
		srv := httptest.NewServer(h)
		defer srv.Close()

		res, err := srv.Client().Get(srv.URL + bootstrapdiscovery.Path)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("native store discovery = %d, want 503", res.StatusCode)
		}
	})

	t.Run("fresh_marker_store_fails_closed_and_fresh_wizard_omits_worker", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "usage.sqlite")
		db, err := sqliterepo.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		cred, err := security.NewAdminCredential(adminKey, "legacy")
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := setting.New(db).SaveAdminCredential(t.Context(), cred); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := upgradeoperation.New(db).Put(t.Context(), model.UpgradeOperation{
			Stage:                   model.UpgradeStageReconcile,
			DomainsDone:             []string{model.UpgradeDomainSourceCopy},
			SourceKind:              "upgraded-legacy",
			SourceFingerprintBefore: strings.Repeat("a", 64),
			SourceFingerprintAfter:  strings.Repeat("a", 64),
			BackupManifestSHA256:    strings.Repeat("b", 64),
		}); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		// Non-empty .manager.lock indicates Fresh marker provenance and must fail closed.
		if err := os.WriteFile(dbPath+".manager.lock", []byte(`{"version":1,"state":"pending_fresh_admission"}`), 0o600); err != nil {
			t.Fatal(err)
		}

		t.Setenv(upgradeworker.SocketEnvironment, filepath.Join(t.TempDir(), "down.sock"))
		h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
		h.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(dbPath), legacypreflight.Options{}, 5*time.Second)
		srv := httptest.NewServer(h)
		defer srv.Close()

		res, err := srv.Client().Get(srv.URL + bootstrapdiscovery.Path)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("Fresh-marked store discovery = %d, want 503", res.StatusCode)
		}
	})

	t.Run("unreadable_wal_preserves_discovery_but_not_credential_authority", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("chmod 0000 permission simulation requires non-root UID")
		}
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "usage.sqlite")
		dataKeyPath := filepath.Join(dir, "data.key")
		if _, _, err := security.LoadOrCreateDataKey("", dataKeyPath); err != nil {
			t.Fatal(err)
		}
		db, err := sqliterepo.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		cred, err := security.NewAdminCredential(adminKey, "legacy")
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := setting.New(db).SaveAdminCredential(t.Context(), cred); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		rev := strings.Repeat("9", 64)
		if err := upgradeoperation.New(db).Put(t.Context(), model.UpgradeOperation{
			Stage:                   model.UpgradeStageCPAImport,
			LastForwardStage:        model.UpgradeStageCPAImport,
			DomainsDone:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig},
			SourceKind:              "upgraded-legacy",
			SourceFingerprintBefore: rev,
			SourceFingerprintAfter:  rev,
			BackupManifestSHA256:    strings.Repeat("8", 64),
			ControlBasePath:         "/cpamp-admin",
			InferenceBasePath:       "/cpamp-api",
			ManualItems:             []string{},
			CreatedAtMS:             1000,
			UpdatedAtMS:             1000,
		}); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if _, err := upgradeoperation.RecordCompletionTx(t.Context(), tx, model.UpgradeVerification{
			TargetMode:        model.UpgradeTargetManaged,
			SourceFingerprint: rev,
			Domains:           []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig},
			ObservedAtMS:      2000,
		}); err != nil {
			_ = tx.Rollback()
			_ = db.Close()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}

		// Create a real non-empty WAL sidecar and make it unreadable (simulating 0600 root:root).
		rawDB, err := sql.Open("sqlite", dbPath)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rawDB.Exec(`pragma journal_mode = wal; pragma wal_autocheckpoint = 0;`); err != nil {
			_ = rawDB.Close()
			t.Fatal(err)
		}
		baseBytes, err := os.ReadFile(dbPath)
		if err != nil {
			_ = rawDB.Close()
			t.Fatal(err)
		}
		if _, err := rawDB.Exec(`insert into settings (key, value, updated_at_ms) values ('wal-frame', 'pending', 1)`); err != nil {
			_ = rawDB.Close()
			t.Fatal(err)
		}
		walBytes, err := os.ReadFile(dbPath + "-wal")
		_ = rawDB.Close()
		if err != nil || len(walBytes) == 0 {
			t.Fatalf("read wal bytes: len=%d err=%v", len(walBytes), err)
		}
		if err := os.WriteFile(dbPath, baseBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		walPath := dbPath + "-wal"
		if err := os.WriteFile(walPath, walBytes, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(walPath, 0o600) })

		if authErr := upgradeworker.NewPreparedCredentialVerifier(dbPath).Authenticate(t.Context(), "Bearer "+adminKey); authErr == nil {
			t.Fatal("authenticated without a current complete snapshot")
		}
		t.Setenv(upgradeworker.SocketEnvironment, filepath.Join(t.TempDir(), "down.sock"))
		h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
		h.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(dbPath), legacypreflight.Options{}, 5*time.Second)
		srv := httptest.NewServer(h)
		defer srv.Close()

		// Discovery stays Upgrade (never Fresh, never 503).
		discRes, err := srv.Client().Get(srv.URL + bootstrapdiscovery.Path)
		if err != nil {
			t.Fatal(err)
		}
		discBody, _ := io.ReadAll(discRes.Body)
		_ = discRes.Body.Close()
		if discRes.StatusCode != http.StatusOK {
			t.Fatalf("discovery with unreadable WAL = %d (%s), want 200", discRes.StatusCode, discBody)
		}

		// An unavailable credential snapshot is not evidence of a bad key.
		authReq, _ := http.NewRequest(http.MethodPost, srv.URL+upgradeauth.Path, nil)
		authReq.Header.Set("Authorization", "Bearer "+adminKey)
		authReq.Header.Set("Origin", srv.URL)
		authRes, err := srv.Client().Do(authReq)
		if err != nil {
			t.Fatal(err)
		}
		_ = authRes.Body.Close()
		if authRes.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("entry auth with unreadable WAL = %d, want 503", authRes.StatusCode)
		}

		// A request must not get a migration record without a verified key.
		statusReq, _ := http.NewRequest(http.MethodGet, srv.URL+bootstrap.FixedBootstrapAPIPath+"installation/upgrade/status", nil)
		statusReq.Header.Set("Authorization", "Bearer "+adminKey)
		statusReq.Header.Set("Origin", srv.URL)
		statusRes, err := srv.Client().Do(statusReq)
		if err != nil {
			t.Fatal(err)
		}
		statusRaw, _ := io.ReadAll(statusRes.Body)
		_ = statusRes.Body.Close()
		if statusRes.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("degraded status with unreadable WAL = %d, want 503", statusRes.StatusCode)
		}
		if bytes.Contains(statusRaw, []byte(`"stage"`)) || bytes.Contains(statusRaw, []byte(`"accessInfo"`)) {
			t.Fatal("degraded status included migration state despite unavailable credential authority")
		}
		if err := os.Chmod(walPath, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := upgradeworker.NewPreparedCredentialVerifier(dbPath).Authenticate(t.Context(), "Bearer "+adminKey); err != nil {
			t.Fatalf("authentication did not recover after snapshot became readable: %v", err)
		}
	})
}

func TestCompletedUpgradeRetiresWizardAndFixedBootstrap(t *testing.T) {
	prevDist := testUpgradeFullWebDist
	testUpgradeFullWebDist = fstest.MapFS{
		"index.html":               {Data: []byte("<!doctype html><html><head><script type=\"module\" src=\"./assets/index-AbCd1234.js\"></script><script>__CPAMP_ROUTING_CONTEXT__</script></head><body><div id=\"root\"></div></body></html>")},
		"assets/index-AbCd1234.js": {Data: []byte("console.log('cpamp-upgrade-test');")},
	}
	t.Cleanup(func() { testUpgradeFullWebDist = prevDist })

	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/cpamp-admin", "/cpamp-api")
	serverApp := httpapi.New(cfg, st, nil)
	startup := newStartupHTTPHandler(bootstrap.PublicDiscovery{})
	if err := attachCompletedUpgradeRouting(t.Context(), cfg, st, serverApp, func(context.Context) bool { return true }, upgradeCanonicalDependencies{Startup: startup}); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting: %v", err)
	}
	ts := httptest.NewServer(startup)
	defer ts.Close()

	for _, target := range []string{
		"/.well-known/cpamp-bootstrap",
		bootstrap.FixedBootstrapAPIPath + "installation/upgrade/status",
		"/cpamp-admin/api/v2/installation/upgrade/status",
		"/cpamp-admin/api/v2/installation/upgrade/preflight",
		"/cpamp-admin/api/v2/installation/upgrade/verify",
	} {
		for _, key := range []string{"", adminKey} {
			req, err := http.NewRequest(http.MethodGet, ts.URL+target, nil)
			if err != nil {
				t.Fatal(err)
			}
			if key != "" {
				req.Header.Set("Authorization", "Bearer "+key)
			}
			res, err := ts.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = res.Body.Close()
			if res.StatusCode != http.StatusNotFound {
				t.Fatalf("completed upgrade %s = %d, want 404", target, res.StatusCode)
			}
		}
	}

	// Ordinary canonical admin API routes remain protected by RejectCrossSiteAdmin.
	adminAPIReq, err := http.NewRequest(http.MethodGet, ts.URL+"/cpamp-admin/api/v2/installation/operation", nil)
	if err != nil {
		t.Fatal(err)
	}
	adminAPIReq.Header.Set("Sec-Fetch-Site", "cross-site")
	adminAPIReq.Header.Set("Sec-Fetch-Dest", "empty")
	adminAPIRes, err := ts.Client().Do(adminAPIReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = adminAPIRes.Body.Close()
	if adminAPIRes.StatusCode != http.StatusForbidden {
		t.Fatalf("ordinary cross-site canonical admin API = %d, want 403", adminAPIRes.StatusCode)
	}
}

func TestStartupWorkerModeRecoversAfterTransientDatabaseProbeOutage(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod 0000 permission simulation requires non-root UID")
	}

	t.Run("prepared_upgrade_store_transitions_503_to_200_without_ever_returning_404", func(t *testing.T) {
		targetDir := t.TempDir()
		dbPath := filepath.Join(targetDir, "usage.sqlite")
		dataKeyPath := filepath.Join(targetDir, "data.key")
		if _, _, err := security.LoadOrCreateDataKey("", dataKeyPath); err != nil {
			t.Fatal(err)
		}
		db, err := sqliterepo.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		cred, err := security.NewAdminCredential(upgradeWizardAdminKey, "legacy")
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := setting.New(db).SaveAdminCredential(t.Context(), cred); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		recordedRevision := strings.Repeat("a", 64)
		if err := upgradeoperation.New(db).Put(t.Context(), model.UpgradeOperation{
			Stage:                   model.UpgradeStageReconcile,
			DomainsDone:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainUsageEvents},
			SourceKind:              "upgraded-legacy",
			SourceVersion:           "unknown",
			SourceFingerprintBefore: strings.Repeat("b", 64),
			SourceFingerprintAfter:  recordedRevision,
			BackupManifestSHA256:    strings.Repeat("c", 64),
			ManualItems:             []string{},
		}); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}

		// Simulate transient unreadable base DB at startup discovery time.
		if err := os.Chmod(dbPath, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dbPath, 0o644) })

		t.Setenv(upgradeworker.SocketEnvironment, filepath.Join(t.TempDir(), "down.sock"))
		h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
		h.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(dbPath), legacypreflight.Options{}, 5*time.Second)
		srv := httptest.NewServer(h)
		defer srv.Close()

		do := func(method, path, auth string) (int, string) {
			t.Helper()
			req, reqErr := http.NewRequest(method, srv.URL+path, nil)
			if reqErr != nil {
				t.Fatal(reqErr)
			}
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			req.Header.Set("Origin", srv.URL)
			res, doErr := srv.Client().Do(req)
			if doErr != nil {
				t.Fatal(doErr)
			}
			defer res.Body.Close()
			raw, readErr := io.ReadAll(res.Body)
			if readErr != nil {
				t.Fatal(readErr)
			}
			return res.StatusCode, string(raw)
		}

		statusPath := bootstrap.FixedBootstrapAPIPath + "installation/upgrade/status"

		// During the outage: every route returns 503 (never 404 and never authenticating).
		discCode, discBody := do(http.MethodGet, bootstrapdiscovery.Path, "")
		if discCode != http.StatusServiceUnavailable {
			t.Fatalf("outage discovery = %d (%s), want 503", discCode, discBody)
		}
		for _, presented := range []string{"", "Bearer wrong-key", "Bearer " + upgradeWizardAdminKey} {
			entryCode, entryBody := do(http.MethodPost, upgradeauth.Path, presented)
			if entryCode != http.StatusServiceUnavailable || !strings.Contains(entryBody, `"source_unavailable"`) {
				t.Fatalf("outage entry (%q) = %d (%s), want 503 source_unavailable", presented, entryCode, entryBody)
			}
			wizCode, wizBody := do(http.MethodGet, statusPath, presented)
			if wizCode != http.StatusServiceUnavailable || !strings.Contains(wizBody, `"upgrade_store_unavailable"`) {
				t.Fatalf("outage status (%q) = %d (%s), want 503 upgrade_store_unavailable", presented, wizCode, wizBody)
			}
		}

		// Restore permissions on the same running HTTP handler and verify recovery to 200.
		if err := os.Chmod(dbPath, 0o644); err != nil {
			t.Fatal(err)
		}

		discCode, discBody = do(http.MethodGet, bootstrapdiscovery.Path, "")
		if discCode != http.StatusOK {
			t.Fatalf("recovered discovery = %d (%s), want 200", discCode, discBody)
		}
		var discovery bootstrapcontract.DiscoveryDTO
		if err := json.Unmarshal([]byte(discBody), &discovery); err != nil {
			t.Fatalf("decode recovered discovery: %v", err)
		}
		if discovery.Status != bootstrap.PublicUnsupportedUpgrade || discovery.AuthEntry != bootstrap.AuthEntryAdminSession {
			t.Fatalf("unexpected recovered discovery: %+v", discovery)
		}

		if wrongCode, _ := do(http.MethodPost, upgradeauth.Path, "Bearer wrong-key"); wrongCode != http.StatusUnauthorized {
			t.Fatalf("wrong key after recovery = %d, want 401", wrongCode)
		}
		entryCode, entryBody := do(http.MethodPost, upgradeauth.Path, "Bearer "+upgradeWizardAdminKey)
		if entryCode != http.StatusOK || !strings.Contains(entryBody, `"authenticated":true`) {
			t.Fatalf("valid entry after recovery = %d (%s), want 200 authenticated", entryCode, entryBody)
		}
		wizCode, wizBody := do(http.MethodGet, statusPath, "Bearer "+upgradeWizardAdminKey)
		if wizCode != http.StatusOK || !strings.Contains(wizBody, `"stage":"reconcile"`) {
			t.Fatalf("wizard status after recovery = %d (%s), want 200 reconcile", wizCode, wizBody)
		}
	})

	t.Run("non_upgrade_store_with_transient_startup_outage_latches_404_once_cleanly_probed", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "usage.sqlite")
		db, err := sqliterepo.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		cred, err := security.NewAdminCredential(upgradeWizardAdminKey, "native")
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := setting.New(db).SaveAdminCredential(t.Context(), cred); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}

		if err := os.Chmod(dbPath, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dbPath, 0o644) })

		t.Setenv(upgradeworker.SocketEnvironment, filepath.Join(t.TempDir(), "down.sock"))
		h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
		h.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(dbPath), legacypreflight.Options{}, 5*time.Second)
		srv := httptest.NewServer(h)
		defer srv.Close()

		postAuth := func() int {
			t.Helper()
			req, reqErr := http.NewRequest(http.MethodPost, srv.URL+upgradeauth.Path, nil)
			if reqErr != nil {
				t.Fatal(reqErr)
			}
			req.Header.Set("Authorization", "Bearer "+upgradeWizardAdminKey)
			req.Header.Set("Origin", srv.URL)
			res, doErr := srv.Client().Do(req)
			if doErr != nil {
				t.Fatal(doErr)
			}
			_ = res.Body.Close()
			return res.StatusCode
		}

		if code := postAuth(); code != http.StatusServiceUnavailable {
			t.Fatalf("indeterminate non-upgrade entry = %d, want 503", code)
		}
		if err := os.Chmod(dbPath, 0o644); err != nil {
			t.Fatal(err)
		}
		if code := postAuth(); code != http.StatusNotFound {
			t.Fatalf("cleanly proven non-upgrade entry = %d, want 404", code)
		}
	})

	t.Run("enable_admitted_discovery_with_native_v2_report_and_zero_byte_lock_serves_unsupported_upgrade", func(t *testing.T) {
		targetDir := t.TempDir()
		dbPath := filepath.Join(targetDir, "usage.sqlite")
		dataKeyPath := filepath.Join(targetDir, "data.key")
		if _, _, err := security.LoadOrCreateDataKey("", dataKeyPath); err != nil {
			t.Fatal(err)
		}
		db, err := sqliterepo.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		cred, err := security.NewAdminCredential(upgradeWizardAdminKey, "legacy")
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := setting.New(db).SaveAdminCredential(t.Context(), cred); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := upgradeoperation.New(db).Put(t.Context(), model.UpgradeOperation{
			Stage:                   model.UpgradeStageCopyIn,
			DomainsDone:             []string{model.UpgradeDomainSourceCopy},
			SourceKind:              "upgraded-legacy",
			SourceVersion:           "unknown",
			SourceFingerprintBefore: strings.Repeat("b", 64),
			SourceFingerprintAfter:  strings.Repeat("a", 64),
			BackupManifestSHA256:    strings.Repeat("c", 64),
			ManualItems:             []string{},
		}); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dbPath+".manager.lock", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(upgradeworker.SocketEnvironment, filepath.Join(t.TempDir(), "down.sock"))
		h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
		h.EnableAdmittedDiscovery(
			legacypreflight.DefaultPaths(dbPath),
			legacypreflight.Options{},
			5*time.Second,
			legacypreflight.Report{SourceKind: legacypreflight.SourceKindNativeV2},
		)
		srv := httptest.NewServer(h)
		defer srv.Close()

		discRes, err := srv.Client().Get(srv.URL + bootstrap.FixedBootstrapDiscoveryPath)
		if err != nil {
			t.Fatal(err)
		}
		discBodyBytes, _ := io.ReadAll(discRes.Body)
		_ = discRes.Body.Close()
		if discRes.StatusCode != http.StatusOK || !strings.Contains(string(discBodyBytes), `"status":"unsupported_upgrade"`) {
			t.Fatalf("discovery = %d (%s), want 200 unsupported_upgrade", discRes.StatusCode, string(discBodyBytes))
		}
	})
}

func TestStartupWorkerModeProbeTimeoutAndTransientCorruptReadDoNotLatch404(t *testing.T) {
	seedPreparedStore := func(t *testing.T) (dbPath string, goodBytes []byte) {
		t.Helper()
		targetDir := t.TempDir()
		dbPath = filepath.Join(targetDir, "usage.sqlite")
		dataKeyPath := filepath.Join(targetDir, "data.key")
		if _, _, err := security.LoadOrCreateDataKey("", dataKeyPath); err != nil {
			t.Fatal(err)
		}
		db, err := sqliterepo.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		cred, err := security.NewAdminCredential(upgradeWizardAdminKey, "legacy")
		if err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := setting.New(db).SaveAdminCredential(t.Context(), cred); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := upgradeoperation.New(db).Put(t.Context(), model.UpgradeOperation{
			Stage:                   model.UpgradeStageCopyIn,
			DomainsDone:             []string{model.UpgradeDomainSourceCopy},
			SourceKind:              "upgraded-legacy",
			SourceVersion:           "unknown",
			SourceFingerprintBefore: strings.Repeat("b", 64),
			SourceFingerprintAfter:  strings.Repeat("a", 64),
			BackupManifestSHA256:    strings.Repeat("c", 64),
			ManualItems:             []string{},
		}); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dbPath+".manager.lock", nil, 0o600); err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		return dbPath, raw
	}

	t.Run("snapshot_timeout_exceeding_probe_budget_still_admits_via_fresh_verifier_context", func(t *testing.T) {
		dbPath, goodBytes := seedPreparedStore(t)
		// Start in indeterminate state so workerLazyAdmission resolves on the HTTP request.
		if err := os.WriteFile(dbPath, []byte("transient-torn-sqlite-image-before-startup"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(upgradeworker.SocketEnvironment, filepath.Join(t.TempDir(), "down.sock"))

		h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
		h.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(dbPath), legacypreflight.Options{}, 5*time.Second)

		if err := os.WriteFile(dbPath, goodBytes, 0o600); err != nil {
			t.Fatal(err)
		}
		origOpener := workerProbeOpenSnapshot
		var timedOutCalls atomic.Int32
		workerProbeOpenSnapshot = func(ctx context.Context, _, _ string) (*upgradeservice.Snapshot, error) {
			timedOutCalls.Add(1)
			<-ctx.Done()
			return nil, ctx.Err()
		}
		t.Cleanup(func() {
			workerProbeOpenSnapshot = origOpener
		})

		srv := httptest.NewServer(h)
		defer srv.Close()

		discRes, err := srv.Client().Get(srv.URL + bootstrap.FixedBootstrapDiscoveryPath)
		if err != nil {
			t.Fatal(err)
		}
		discBody, _ := io.ReadAll(discRes.Body)
		_ = discRes.Body.Close()
		if timedOutCalls.Load() == 0 {
			t.Fatal("expected workerProbeOpenSnapshot to exhaust probeCtx deadline")
		}
		if discRes.StatusCode != http.StatusOK || !strings.Contains(string(discBody), `"status":"unsupported_upgrade"`) {
			t.Fatalf("discovery after snapshot probe timeout = %d (%s), want 200 unsupported_upgrade", discRes.StatusCode, string(discBody))
		}
	})

	t.Run("transient_corrupt_read_returns_503_then_latches_200_on_recovery", func(t *testing.T) {
		dbPath, goodBytes := seedPreparedStore(t)
		// Overwrite with corrupt non-SQLite bytes so OpenSnapshot (sql.Open + Ping)
		// succeeds while ReadRecordFrom returns a corrupt/not-a-db error.
		if err := os.WriteFile(dbPath, []byte("corrupt-torn-sqlite-image-not-valid-btree-header"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv(upgradeworker.SocketEnvironment, filepath.Join(t.TempDir(), "down.sock"))

		h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
		h.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(dbPath), legacypreflight.Options{}, 5*time.Second)
		srv := httptest.NewServer(h)
		defer srv.Close()

		// First requests while corrupt must return 503 (never latch 404).
		disc1, err := srv.Client().Get(srv.URL + bootstrap.FixedBootstrapDiscoveryPath)
		if err != nil {
			t.Fatal(err)
		}
		_ = disc1.Body.Close()
		if disc1.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("corrupt first discovery = %d, want 503 (not 404)", disc1.StatusCode)
		}

		authReq1, err := http.NewRequest(http.MethodPost, srv.URL+upgradeauth.Path, nil)
		if err != nil {
			t.Fatal(err)
		}
		authReq1.Header.Set("Authorization", "Bearer "+upgradeWizardAdminKey)
		authReq1.Header.Set("Origin", srv.URL)
		authRes1, err := srv.Client().Do(authReq1)
		if err != nil {
			t.Fatal(err)
		}
		_ = authRes1.Body.Close()
		if authRes1.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("corrupt first entry auth = %d, want 503 (not 404)", authRes1.StatusCode)
		}

		wizReq1, err := http.NewRequest(http.MethodGet, srv.URL+bootstrap.FixedBootstrapAPIPath+"installation/upgrade/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		wizReq1.Header.Set("Authorization", "Bearer "+upgradeWizardAdminKey)
		wizRes1, err := srv.Client().Do(wizReq1)
		if err != nil {
			t.Fatal(err)
		}
		_ = wizRes1.Body.Close()
		if wizRes1.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("corrupt first wizard status = %d, want 503 (not 404)", wizRes1.StatusCode)
		}

		// Restore healthy prepared DB bytes; second requests must recover and latch 200.
		if err := os.WriteFile(dbPath, goodBytes, 0o600); err != nil {
			t.Fatal(err)
		}

		disc2, err := srv.Client().Get(srv.URL + bootstrap.FixedBootstrapDiscoveryPath)
		if err != nil {
			t.Fatal(err)
		}
		disc2Body, _ := io.ReadAll(disc2.Body)
		_ = disc2.Body.Close()
		if disc2.StatusCode != http.StatusOK || !strings.Contains(string(disc2Body), `"status":"unsupported_upgrade"`) {
			t.Fatalf("recovered second discovery = %d (%s), want 200 unsupported_upgrade", disc2.StatusCode, string(disc2Body))
		}

		authReq2, err := http.NewRequest(http.MethodPost, srv.URL+upgradeauth.Path, nil)
		if err != nil {
			t.Fatal(err)
		}
		authReq2.Header.Set("Authorization", "Bearer "+upgradeWizardAdminKey)
		authReq2.Header.Set("Origin", srv.URL)
		authRes2, err := srv.Client().Do(authReq2)
		if err != nil {
			t.Fatal(err)
		}
		_ = authRes2.Body.Close()
		if authRes2.StatusCode != http.StatusOK {
			t.Fatalf("recovered second entry auth = %d, want 200", authRes2.StatusCode)
		}

		wizReq2, err := http.NewRequest(http.MethodGet, srv.URL+bootstrap.FixedBootstrapAPIPath+"installation/upgrade/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		wizReq2.Header.Set("Authorization", "Bearer "+upgradeWizardAdminKey)
		wizRes2, err := srv.Client().Do(wizReq2)
		if err != nil {
			t.Fatal(err)
		}
		wiz2Body, _ := io.ReadAll(wizRes2.Body)
		_ = wizRes2.Body.Close()
		if wizRes2.StatusCode != http.StatusOK || !strings.Contains(string(wiz2Body), `"stage":"copy_in"`) {
			t.Fatalf("recovered second wizard status = %d (%s), want 200 copy_in", wizRes2.StatusCode, string(wiz2Body))
		}
	})
}

func TestStartupWorkerModeFallbackIdentityParityAcrossOfflineAndLiveWorker(t *testing.T) {
	targetDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve target dir: %v", err)
	}
	dbPath := filepath.Join(targetDir, "usage.sqlite")
	dataKeyPath := filepath.Join(targetDir, "data.key")
	if _, _, err := security.LoadOrCreateDataKey("", dataKeyPath); err != nil {
		t.Fatal(err)
	}
	db, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	cred, err := security.NewAdminCredential(upgradeWizardAdminKey, "legacy")
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := setting.New(db).SaveAdminCredential(t.Context(), cred); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	recordedRevision := strings.Repeat("d", 64)
	preparedOp := model.UpgradeOperation{
		Stage:                   model.UpgradeStageReconcile,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainUsageEvents},
		SourceKind:              "upgraded-legacy",
		SourceVersion:           "unknown",
		SourceFingerprintBefore: strings.Repeat("e", 64),
		SourceFingerprintAfter:  recordedRevision,
		BackupManifestSHA256:    strings.Repeat("f", 64),
		ManualItems:             []string{},
	}
	if err := upgradeoperation.New(db).Put(t.Context(), preparedOp); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	expectedOpRev := upgradeworker.OperationRevision(preparedOp)

	sockDir, err := os.MkdirTemp("/tmp", "cpamp-sw-*")
	if err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	socketPath := filepath.Join(sockDir, "worker.sock")
	t.Setenv(upgradeworker.SocketEnvironment, socketPath)

	h := newStartupHTTPHandler(bootstrapdiscovery.Unavailable())
	srv := httptest.NewServer(h)
	defer srv.Close()
	t.Setenv("CPAMP_PUBLIC_ORIGIN", srv.URL)
	h.EnableReadOnlyDiscovery(legacypreflight.DefaultPaths(dbPath), legacypreflight.Options{}, 5*time.Second)

	postEntry := func(priorRev string) (int, string, string, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, srv.URL+upgradeauth.Path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+upgradeWizardAdminKey)
		req.Header.Set("Origin", srv.URL)
		if priorRev != "" {
			req.Header.Set("X-CPAMP-Upgrade-Source", priorRev)
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Status            string `json:"status"`
			SourceRevision    string `json:"source_revision"`
			OperationRevision string `json:"operation_revision"`
		}
		_ = json.Unmarshal(raw, &doc)
		return res.StatusCode, doc.Status, doc.SourceRevision, doc.OperationRevision
	}

	// 1. Offline startup fallback issues recordedRevision and expectedOpRev.
	code1, status1, src1, op1 := postEntry("")
	if code1 != http.StatusOK || status1 != "observation_unavailable" || src1 != recordedRevision || op1 != expectedOpRev {
		t.Fatalf("offline entry = (%d, %q, %q, %q), want (200, observation_unavailable, %q, %q)",
			code1, status1, src1, op1, recordedRevision, expectedOpRev)
	}

	// 2. Start live worker with unmounted SourceRoot on the same socket and renew in place.
	unmountedSource, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve unmounted source: %v", err)
	}
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	defer cancelWorker()
	worker, err := upgradeworker.New(workerCtx, upgradeworker.Options{
		Config:          config.Config{DBPath: dbPath, DataKeyPath: dataKeyPath},
		TargetRoot:      targetDir,
		SourceRoot:      unmountedSource,
		PublicOrigin:    srv.URL,
		RepairOwnership: func(string) error { return nil },
	})
	if err != nil {
		t.Fatalf("upgradeworker.New: %v", err)
	}
	defer worker.Wait()

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	unixSrv := &http.Server{Handler: worker}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = unixSrv.Serve(listener)
	}()
	defer func() {
		_ = unixSrv.Close()
		_ = listener.Close()
		<-done
	}()

	code2, status2, src2, op2 := postEntry(src1)
	if code2 != http.StatusOK || status2 != "observation_unavailable" || src2 != recordedRevision || op2 != expectedOpRev {
		t.Fatalf("live unmounted renewal = (%d, %q, %q, %q), want (200, observation_unavailable, %q, %q)",
			code2, status2, src2, op2, recordedRevision, expectedOpRev)
	}
}
