package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	settingrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/fresh"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
)

func runAccessForTest(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := runAccess(t.Context(), args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func newSyntheticIngressControlServer(t *testing.T, current *atomic.Value) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != freshrouting.InternalPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		val := current.Load()
		state, ok := val.(freshrouting.State)
		if !ok {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(state)
	}))
	t.Cleanup(server.Close)
	return server
}

func seedSyntheticUpgradeStore(
	t *testing.T,
	stage model.UpgradeStage,
	targetMode string,
	controlBasePath string,
	inferenceBasePath string,
	routingCommitted bool,
	inferenceCommitted bool,
	rawExternalURL string,
) (string, *sql.DB, *security.Protector) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	db, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	keyBytes, _, err := security.LoadOrCreateDataKey("", filepath.Join(dir, ".data.key"))
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		t.Fatal(err)
	}

	ctx := t.Context()
	cred, err := security.NewAdminCredential("cpamp_SyntheticAdmin0123456789!", "upgrade")
	if err != nil {
		t.Fatal(err)
	}
	if err := settingrepo.New(db).SaveAdminCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}

	fpr := "sha256:" + strings.Repeat("b", 64)
	manifest := "sha256:" + strings.Repeat("c", 64)
	domains := []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig}
	initialStage := model.UpgradeStageCPAImport
	if targetMode == model.UpgradeTargetExternal {
		domains = []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainIdentity, model.UpgradeDomainCPAConfig}
		initialStage = model.UpgradeStageExternalConnect
	}
	if stage != model.UpgradeStageComplete {
		initialStage = stage
	}

	repo := upgradeoperation.New(db)
	if err := repo.Put(ctx, model.UpgradeOperation{
		Stage:                      initialStage,
		LastForwardStage:           initialStage,
		DomainsDone:                domains,
		SourceKind:                 legacypreflight.SourceKindUpgradedLegacy,
		SourceVersion:              "v1.14.4",
		SourceRoot:                 filepath.Join(dir, "legacy"),
		SourceFingerprintBefore:    fpr,
		SourceFingerprintAfter:     fpr,
		BackupManifestSHA256:       manifest,
		Reconciliation:             json.RawMessage(`{"ok":true}`),
		ControlBasePath:            controlBasePath,
		InferenceBasePath:          inferenceBasePath,
		SharedEntry:                controlBasePath != "" && controlBasePath == inferenceBasePath,
		SuggestedControlBasePath:   "/suggestedcontrol",
		SuggestedInferenceBasePath: "/suggestedinference",
		CreatedAtMS:                1000,
		UpdatedAtMS:                1000,
	}); err != nil {
		t.Fatal(err)
	}

	var extIdentity string
	if targetMode == model.UpgradeTargetExternal && rawExternalURL != "" {
		normURL, err := cpa.NormalizeV8BaseURL(strings.TrimSpace(rawExternalURL))
		if err != nil {
			t.Fatal(err)
		}
		canonURL, err := identity.CanonicalExternalURL(normURL)
		if err != nil {
			t.Fatal(err)
		}
		extIdentity = freshkeyapply.ExternalIdentity(canonURL)
		sealedKey, err := protector.ProtectString(strings.Repeat("a", 64))
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := upgradeoperation.RecordExternalBindingTx(ctx, tx, upgradeoperation.ExternalBinding{
			BaseURL:       rawExternalURL,
			Identity:      extIdentity,
			Generation:    extIdentity,
			ManagementKey: sealedKey,
			ObservedAtMS:  2000,
		}); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	if stage == model.UpgradeStageComplete {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		verif := model.UpgradeVerification{
			TargetMode:        targetMode,
			SourceFingerprint: fpr,
			Domains:           domains,
			ObservedAtMS:      2001,
		}
		if targetMode == model.UpgradeTargetExternal {
			verif.BindingIdentity = extIdentity
			verif.BindingGeneration = extIdentity
			verif.BindingObservedAtMS = 2000
		}
		if _, err := upgradeoperation.RecordCompletionTx(ctx, tx, verif); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if !routingCommitted || !inferenceCommitted {
			if _, err := db.ExecContext(
				ctx,
				`update upgrade_operations set routing_committed = ?, inference_committed = ? where id = 1`,
				routingCommitted,
				inferenceCommitted,
			); err != nil {
				t.Fatal(err)
			}
		}
	}

	return dbPath, db, protector
}

func seedSettingsConnectionRows(
	t *testing.T,
	db *sql.DB,
	managerCfg *model.ManagerConfig,
	setupCfg *model.Setup,
) {
	t.Helper()
	if _, err := db.Exec(`delete from settings where key in ('manager_config_v1', 'setup')`); err != nil {
		t.Fatal(err)
	}
	if managerCfg != nil {
		raw, err := json.Marshal(*managerCfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into settings (key, value, updated_at_ms) values ('manager_config_v1', ?, 1)`, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if setupCfg != nil {
		raw, err := json.Marshal(*setupCfg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into settings (key, value, updated_at_ms) values ('setup', ?, 1)`, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAccessShowSavedVsActiveAndFullIngressEquality(t *testing.T) {
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	var ingressState atomic.Value
	ingress := newSyntheticIngressControlServer(t, &ingressState)
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", ingress.URL)

	// 1. Fresh store without any saved prefix reports the unconfigured guidance.
	unroutedDir := t.TempDir()
	unroutedDB := filepath.Join(unroutedDir, "manager.sqlite")
	unroutedPaths := legacypreflight.DefaultPaths(unroutedDB)
	unroutedPaths.Config = filepath.Join(unroutedDir, "config.json")
	openedUnrouted, err := fresh.Open(t.Context(), unroutedPaths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = openedUnrouted.Close() })
	stdout, _, err := runAccessForTest(t, "show", "--db-path", unroutedDB)
	if err != nil {
		t.Fatalf("unrouted access show failed: %v", err)
	}
	if strings.TrimSpace(stdout) != "Routing: no saved management prefix; finish installation or upgrade first." {
		t.Fatalf("unexpected unrouted output: %q", stdout)
	}

	// 2. Fresh store with draft prefixes only is reported as saved, never active.
	draftStore := freshstore.New(openedUnrouted.DB)
	if _, err := draftStore.Update(t.Context(), openedUnrouted.Record.Operation.Identity, openedUnrouted.Record.Operation.Revision, func(r *freshstore.Record) error {
		r.Operation.Routing.DraftControlBasePath = "/draft-admin"
		r.Operation.Routing.DraftInferenceBasePath = "/draft-api"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stdout, _, err = runAccessForTest(t, "show", "--db-path", unroutedDB)
	if err != nil {
		t.Fatalf("draft access show failed: %v", err)
	}
	if !strings.Contains(stdout, "Management routing: saved; not confirmed active\nManagement console: /draft-admin\nManagement API base: /draft-admin/api/v2\n") ||
		!strings.Contains(stdout, "Inference routing: saved; not confirmed active\nInference base: /draft-api\n") {
		t.Fatalf("unexpected draft output:\n%s", stdout)
	}

	// 3. Committed Managed Upgrade store: test saved vs active and full Ingress State equality.
	upgDBPath, upgDB, _ := seedSyntheticUpgradeStore(
		t,
		model.UpgradeStageComplete,
		model.UpgradeTargetManaged,
		"/upg-console",
		"/upg-inference",
		true,
		true,
		"",
	)
	expectedSaved, err := readSavedAccessRoutes(t.Context(), upgDB)
	if err != nil || expectedSaved.committed == nil {
		t.Fatalf("readSavedAccessRoutes(Managed complete): saved=%+v err=%v", expectedSaved, err)
	}
	committedState := *expectedSaved.committed

	// Before Ingress has any state (503), both routes are saved; not confirmed active.
	stdout, _, err = runAccessForTest(t, "show", "--db-path", upgDBPath)
	if err != nil {
		t.Fatalf("saved-only access show failed: %v", err)
	}
	if !strings.Contains(stdout, "Management routing: saved; not confirmed active\n") ||
		!strings.Contains(stdout, "Inference routing: saved; not confirmed active\n") {
		t.Fatalf("expected saved; not confirmed active when Ingress is 503:\n%s", stdout)
	}

	// Exact match across all State fields reports active in Ingress for both routes.
	ingressState.Store(committedState)
	stdout, _, err = runAccessForTest(t, "show", "--db-path", upgDBPath)
	if err != nil {
		t.Fatalf("active access show failed: %v", err)
	}
	if !strings.Contains(stdout, "Management routing: active in Ingress\nManagement console: /upg-console\nManagement API base: /upg-console/api/v2\n") ||
		!strings.Contains(stdout, "Inference routing: active in Ingress\nInference base: /upg-inference\n") {
		t.Fatalf("expected active in Ingress on exact State match:\n%s", stdout)
	}

	// Any single field difference in freshrouting.State must fail full equality and report saved; not confirmed active.
	for _, tc := range []struct {
		name   string
		mutate func(*freshrouting.State)
	}{
		{"installation-id-mismatch", func(s *freshrouting.State) { s.InstallationID = "upgrade:other-installation" }},
		{"generation-mismatch", func(s *freshrouting.State) { s.Generation = "other-generation" }},
		{"operation-id-mismatch", func(s *freshrouting.State) { s.OperationID = "upgrade-operation:other" }},
		{"revision-mismatch", func(s *freshrouting.State) { s.Revision = committedState.Revision + 1 }},
		{"target-mismatch", func(s *freshrouting.State) { s.Target = "/other-console" }},
		{"inference-target-mismatch", func(s *freshrouting.State) { s.InferenceTarget = "/other-inference" }},
		{"shared-entry-mismatch", func(s *freshrouting.State) { s.SharedEntry = !committedState.SharedEntry }},
		{"committed-false-mismatch", func(s *freshrouting.State) { s.Committed = false }},
		{"inference-committed-mismatch", func(s *freshrouting.State) { s.InferenceCommitted = !committedState.InferenceCommitted }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mismatched := committedState
			tc.mutate(&mismatched)
			ingressState.Store(mismatched)
			out, _, err := runAccessForTest(t, "show", "--db-path", upgDBPath)
			if err != nil {
				t.Fatalf("access show on %s failed: %v", tc.name, err)
			}
			if strings.Contains(out, "active in Ingress") ||
				!strings.Contains(out, "Management routing: saved; not confirmed active\n") ||
				!strings.Contains(out, "Inference routing: saved; not confirmed active\n") {
				t.Fatalf("%s unexpectedly confirmed active routing:\n%s", tc.name, out)
			}
		})
	}

	// 4. Staged uncommitted inference on a completed Managed upgrade keeps Management active
	// when Ingress matches the control-only committed state, while Inference stays saved; not confirmed active.
	stagedDBPath, stagedDB, _ := seedSyntheticUpgradeStore(
		t,
		model.UpgradeStageComplete,
		model.UpgradeTargetManaged,
		"/staged-console",
		"/staged-inference",
		true,
		false,
		"",
	)
	stagedSaved, err := readSavedAccessRoutes(t.Context(), stagedDB)
	if err != nil || stagedSaved.committed == nil {
		t.Fatalf("readSavedAccessRoutes(staged inference): saved=%+v err=%v", stagedSaved, err)
	}
	ingressState.Store(*stagedSaved.committed)
	stdout, _, err = runAccessForTest(t, "show", "--db-path", stagedDBPath)
	if err != nil {
		t.Fatalf("staged inference access show failed: %v", err)
	}
	if !strings.Contains(stdout, "Management routing: active in Ingress\n") ||
		!strings.Contains(stdout, "Inference routing: saved; not confirmed active\nInference base: /staged-inference\n") {
		t.Fatalf("staged uncommitted inference should not report active in Ingress:\n%s", stdout)
	}
}

func TestAccessShowOriginValidationAndRedaction(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "http://127.0.0.1:1")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	dbPath, _, _ := seedSyntheticUpgradeStore(
		t,
		model.UpgradeStageComplete,
		model.UpgradeTargetManaged,
		"/cpamp-console",
		"/cpamp-inference",
		true,
		true,
		"",
	)

	// Valid origin normalizes trailing slash and prepends to console, API, and inference paths.
	stdout, stderr, err := runAccessForTest(t, "show", "--db-path", dbPath, "--origin", "https://cpamp.example.com:8443/")
	if err != nil || stderr != "" {
		t.Fatalf("valid --origin failed: err=%v stderr=%q", err, stderr)
	}
	for _, want := range []string{
		"Management console: https://cpamp.example.com:8443/cpamp-console\n",
		"Management API base: https://cpamp.example.com:8443/cpamp-console/api/v2\n",
		"Inference base: https://cpamp.example.com:8443/cpamp-inference\n",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "These are paths.") {
		t.Fatalf("output with explicit --origin should not print raw-path hint:\n%s", stdout)
	}

	// Untrusted origins carrying credentials, query parameters, fragments, paths, or non-HTTP(S) schemes
	// must fail closed without echoing the untrusted input or secret.
	const secretMarker = "sensitive-origin-credential-value"
	for _, invalidOrigin := range []string{
		"https://operator:" + secretMarker + "@cpamp.example.com",
		"https://" + secretMarker + "@cpamp.example.com",
		"https://cpamp.example.com?token=" + secretMarker,
		"https://cpamp.example.com#" + secretMarker,
		"https://cpamp.example.com/" + secretMarker,
		"ftp://cpamp.example.com/" + secretMarker,
		"https://cpamp.example.com:0",
		"https://cpamp.example.com:65536",
		"https://cpamp.example.com:99999",
		"https://cpamp.example.com:-1",
		"https://cpamp.example.com:+80",
		"https://cpamp.example.com:",
		"http://[::1]:0",
		"http://[::1]:65536",
		"http://[::1]:-1",
	} {
		t.Run(invalidOrigin, func(t *testing.T) {
			out, errOut, err := runAccessForTest(t, "show", "--db-path", dbPath, "--origin", invalidOrigin)
			if err == nil {
				t.Fatalf("invalid origin %q unexpectedly succeeded", invalidOrigin)
			}
			combined := out + "\n" + errOut + "\n" + err.Error()
			if strings.Contains(combined, secretMarker) || strings.Contains(combined, "cpamp.example.com") || strings.Contains(combined, "[::1]") || strings.Contains(combined, "65536") || strings.Contains(combined, "99999") {
				t.Fatalf("invalid origin leaked untrusted value in output/error: %q", combined)
			}

			t.Setenv("CPAMP_PUBLIC_ORIGIN", invalidOrigin)
			outEnv, errOutEnv, errEnv := runAccessForTest(t, "show", "--db-path", dbPath)
			if errEnv == nil {
				t.Fatalf("invalid CPAMP_PUBLIC_ORIGIN %q unexpectedly succeeded", invalidOrigin)
			}
			combinedEnv := outEnv + "\n" + errOutEnv + "\n" + errEnv.Error()
			if strings.Contains(combinedEnv, secretMarker) || strings.Contains(combinedEnv, "cpamp.example.com") || strings.Contains(combinedEnv, "[::1]") || strings.Contains(combinedEnv, "65536") || strings.Contains(combinedEnv, "99999") {
				t.Fatalf("invalid CPAMP_PUBLIC_ORIGIN leaked untrusted value in output/error: %q", combinedEnv)
			}
		})
	}

	for _, validPortOrigin := range []string{
		"http://cpamp.example.com:1",
		"https://cpamp.example.com:65535",
		"http://[::1]:18317",
	} {
		t.Setenv("CPAMP_PUBLIC_ORIGIN", "")
		out, errOut, err := runAccessForTest(t, "show", "--db-path", dbPath, "--origin", validPortOrigin)
		if err != nil || errOut != "" {
			t.Fatalf("valid port origin %q failed: err=%v stderr=%q", validPortOrigin, err, errOut)
		}
		if !strings.Contains(out, "Management console: "+validPortOrigin+"/cpamp-console\n") {
			t.Fatalf("missing expected console URL for %q in:\n%s", validPortOrigin, out)
		}
	}
}

func TestAccessShowFencesExternalURLAgainstCurrentPersistedAuthority(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "http://127.0.0.1:1")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	const historicRawURL = "https://EXTERNAL-HISTORIC.example:8443/custom/"
	const historicCanonicalURL = "https://external-historic.example:8443/custom"
	const driftedURL = "https://external-current-drifted.example:8443/custom"
	const fallbackLine = "External CPA: use the directly reachable CPA address; no local inference prefix."

	t.Run("upgrade-external-authority-and-drift-matrix", func(t *testing.T) {
		dbPath, db, protector := seedSyntheticUpgradeStore(
			t,
			model.UpgradeStageComplete,
			model.UpgradeTargetExternal,
			"/ext-console",
			"",
			true,
			false,
			historicRawURL,
		)
		sealedKey, err := protector.ProtectString(strings.Repeat("k", 64))
		if err != nil {
			t.Fatal(err)
		}
		otherSealedKey, err := protector.ProtectString(strings.Repeat("m", 64))
		if err != nil {
			t.Fatal(err)
		}

		for _, tc := range []struct {
			name       string
			managerCfg *model.ManagerConfig
			setupCfg   *model.Setup
			wantURL    string
		}{
			{
				name:    "no-persisted-connection-authority-does-not-present-historic-address",
				wantURL: "",
			},
			{
				name: "empty-persisted-connection-rows-do-not-present-historic-address",
				managerCfg: &model.ManagerConfig{
					CPAConnection: model.ManagerCPAConnectionConfig{},
				},
				setupCfg: &model.Setup{},
				wantURL:  "",
			},
			{
				name: "complete-manager-authority-matches-and-ignores-leftover-setup",
				managerCfg: &model.ManagerConfig{
					CPAConnection: model.ManagerCPAConnectionConfig{
						CPABaseURL:    "https://external-historic.example:8443/custom/",
						ManagementKey: sealedKey,
					},
				},
				setupCfg: &model.Setup{
					CPAUpstreamURL: driftedURL,
					ManagementKey:  otherSealedKey,
				},
				wantURL: historicCanonicalURL,
			},
			{
				name: "complete-setup-authority-matches-when-manager-absent",
				setupCfg: &model.Setup{
					CPAUpstreamURL: historicRawURL,
					ManagementKey:  sealedKey,
				},
				wantURL: historicCanonicalURL,
			},
			{
				name: "partial-manager-matching-url-repaired-by-complete-setup",
				managerCfg: &model.ManagerConfig{
					CPAConnection: model.ManagerCPAConnectionConfig{
						CPABaseURL: historicCanonicalURL,
					},
				},
				setupCfg: &model.Setup{
					CPAUpstreamURL: historicCanonicalURL,
					ManagementKey:  sealedKey,
				},
				wantURL: historicCanonicalURL,
			},
			{
				name: "complete-manager-authority-drifted-hides-historic-and-drifted-addresses",
				managerCfg: &model.ManagerConfig{
					CPAConnection: model.ManagerCPAConnectionConfig{
						CPABaseURL:    driftedURL,
						ManagementKey: sealedKey,
					},
				},
				setupCfg: &model.Setup{
					CPAUpstreamURL: historicCanonicalURL,
					ManagementKey:  sealedKey,
				},
				wantURL: "",
			},
			{
				name: "setup-authority-drifted-hides-historic-and-drifted-addresses",
				setupCfg: &model.Setup{
					CPAUpstreamURL: driftedURL,
					ManagementKey:  sealedKey,
				},
				wantURL: "",
			},
			{
				name: "partial-manager-url-conflict-with-setup-fails-closed",
				managerCfg: &model.ManagerConfig{
					CPAConnection: model.ManagerCPAConnectionConfig{
						CPABaseURL: driftedURL,
					},
				},
				setupCfg: &model.Setup{
					CPAUpstreamURL: historicCanonicalURL,
					ManagementKey:  sealedKey,
				},
				wantURL: "",
			},
			{
				name: "partial-manager-key-conflict-with-setup-fails-closed",
				managerCfg: &model.ManagerConfig{
					CPAConnection: model.ManagerCPAConnectionConfig{
						ManagementKey: otherSealedKey,
					},
				},
				setupCfg: &model.Setup{
					CPAUpstreamURL: historicCanonicalURL,
					ManagementKey:  sealedKey,
				},
				wantURL: "",
			},
			{
				name: "partial-manager-url-without-any-credential-fails-closed",
				managerCfg: &model.ManagerConfig{
					CPAConnection: model.ManagerCPAConnectionConfig{
						CPABaseURL: historicCanonicalURL,
					},
				},
				wantURL: "",
			},
			{
				name: "unsealed-manager-credential-envelope-fails-closed",
				managerCfg: &model.ManagerConfig{
					CPAConnection: model.ManagerCPAConnectionConfig{
						CPABaseURL:    historicCanonicalURL,
						ManagementKey: "plaintext-unsealed-secret",
					},
				},
				setupCfg: &model.Setup{
					CPAUpstreamURL: historicCanonicalURL,
					ManagementKey:  sealedKey,
				},
				wantURL: "",
			},
			{
				name: "unsealed-setup-credential-envelope-fails-closed",
				setupCfg: &model.Setup{
					CPAUpstreamURL: historicCanonicalURL,
					ManagementKey:  "enc:v1:corrupted-envelope",
				},
				wantURL: "",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				seedSettingsConnectionRows(t, db, tc.managerCfg, tc.setupCfg)
				out, _, err := runAccessForTest(t, "show", "--db-path", dbPath)
				if err != nil {
					t.Fatalf("access show failed: %v", err)
				}
				if tc.wantURL != "" {
					wantLine := "Configured External CPA base (direct): " + tc.wantURL + "\n"
					if !strings.Contains(out, wantLine) || strings.Contains(out, driftedURL) {
						t.Fatalf("expected %q in output, got:\n%s", wantLine, out)
					}
					return
				}
				if strings.Contains(out, historicCanonicalURL) ||
					strings.Contains(out, "external-historic.example") ||
					strings.Contains(out, driftedURL) ||
					strings.Contains(out, "Configured External CPA base") ||
					!strings.Contains(out, fallbackLine) {
					t.Fatalf("expected fenced fallback without historic or drifted External URL, got:\n%s", out)
				}
			})
		}

		t.Run("binding-row-drift-fails-closed-even-with-complete-settings-authority", func(t *testing.T) {
			seedSettingsConnectionRows(t, db, &model.ManagerConfig{
				CPAConnection: model.ManagerCPAConnectionConfig{
					CPABaseURL:    historicCanonicalURL,
					ManagementKey: sealedKey,
				},
			}, nil)
			if _, err := db.Exec(`update upgrade_external_connect set base_url = ? where id = 1`, driftedURL); err != nil {
				t.Fatal(err)
			}
			out, _, err := runAccessForTest(t, "show", "--db-path", dbPath)
			if err != nil {
				t.Fatalf("access show failed: %v", err)
			}
			if strings.Contains(out, historicCanonicalURL) ||
				strings.Contains(out, driftedURL) ||
				strings.Contains(out, "Configured External CPA base") ||
				!strings.Contains(out, fallbackLine) {
				t.Fatalf("expected fenced fallback on binding base_url drift, got:\n%s", out)
			}
		})
	})

	t.Run("fresh-connect-external-authority-and-drift", func(t *testing.T) {
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "manager.sqlite")
		paths := legacypreflight.DefaultPaths(dbPath)
		paths.Config = filepath.Join(dir, "config.json")
		opened, err := fresh.Open(t.Context(), paths, fresh.OpenOptions{Initialize: true, AdminKey: strategyTestAdmin})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = opened.Close() })

		const freshExternalURL = "https://fresh-external.example:8443/v8"
		sealedKey, err := opened.Protector.ProtectString(strings.Repeat("f", 64))
		if err != nil {
			t.Fatal(err)
		}
		fStore := freshstore.New(opened.DB)
		if _, err := fStore.Update(t.Context(), opened.Record.Operation.Identity, opened.Record.Operation.Revision, func(r *freshstore.Record) error {
			r.Operation.Routing.OperationID = r.Operation.Identity.OperationID
			r.Operation.Routing.Revision = 1
			r.Operation.Routing.Target = "/fresh-ext-admin"
			r.Operation.Routing.Stage = bootstrap.PrefixCommitted
			r.Operation.Routing.Committed = true
			r.Operation.Strategy = bootstrap.FreshStrategyCheckpoint{
				Strategy:         bootstrap.ConnectExternal,
				Stage:            "committed",
				TargetIdentity:   freshkeyapply.ExternalIdentity(freshExternalURL),
				TargetGeneration: 1,
			}
			r.Operation.ProductComplete = true
			r.Connection = &model.ManagerCPAConnectionConfig{
				CPABaseURL:    freshExternalURL,
				ManagementKey: sealedKey,
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		out, _, err := runAccessForTest(t, "show", "--db-path", dbPath)
		if err != nil {
			t.Fatalf("fresh external access show failed: %v", err)
		}
		if !strings.Contains(out, "Configured External CPA base (direct): "+freshExternalURL+"\n") {
			t.Fatalf("expected configured Fresh External URL, got:\n%s", out)
		}

		// Drift persisted manager_config_v1 connection after completion: neither historic nor drifted URL may be presented.
		enabled := true
		driftedRaw, err := json.Marshal(model.ManagerConfig{
			CPAConnection: model.ManagerCPAConnectionConfig{
				CPABaseURL:    driftedURL,
				ManagementKey: sealedKey,
			},
			Collector: model.ManagerCollectorConfig{Enabled: &enabled},
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := opened.DB.Exec(`update settings set value = ? where key = 'manager_config_v1'`, string(driftedRaw)); err != nil {
			t.Fatal(err)
		}
		outDrifted, _, err := runAccessForTest(t, "show", "--db-path", dbPath)
		if err != nil {
			t.Fatalf("fresh external drifted access show failed: %v", err)
		}
		if strings.Contains(outDrifted, freshExternalURL) ||
			strings.Contains(outDrifted, driftedURL) ||
			strings.Contains(outDrifted, "Configured External CPA base") ||
			!strings.Contains(outDrifted, fallbackLine) {
			t.Fatalf("expected fenced fallback on Fresh External connection drift, got:\n%s", outDrifted)
		}
	})
}

func TestAccessShowSignalCancellationCleansUpSnapshot(t *testing.T) {
	if os.Getenv("CPAMP_TEST_ACCESS_SHOW_SIGNAL_CHILD") == "1" {
		dbPath := os.Getenv("CPAMP_TEST_ACCESS_SHOW_DB")
		var stdout, stderr bytes.Buffer
		_ = runAccess(context.Background(), []string{"show", "--db-path", dbPath}, &stdout, &stderr)
		return
	}

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	dbPath, _, _ := seedSyntheticUpgradeStore(
		t,
		model.UpgradeStageComplete,
		model.UpgradeTargetManaged,
		"/sig-console",
		"/sig-inference",
		true,
		true,
		"",
	)

	for _, tc := range []struct {
		name string
		sig  os.Signal
	}{
		{name: "SIGTERM", sig: syscall.SIGTERM},
		{name: "SIGINT", sig: os.Interrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scratchDir := t.TempDir()
			inspectEntered := make(chan struct{})
			unblockServer := make(chan struct{})
			var enteredOnce sync.Once
			blockingIngress := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				enteredOnce.Do(func() { close(inspectEntered) })
				select {
				case <-r.Context().Done():
				case <-unblockServer:
				}
			}))
			defer func() {
				close(unblockServer)
				blockingIngress.Close()
			}()

			cmd := exec.Command(exe, "-test.run=^TestAccessShowSignalCancellationCleansUpSnapshot$")
			cmd.Env = append(
				os.Environ(),
				"CPAMP_TEST_ACCESS_SHOW_SIGNAL_CHILD=1",
				"CPAMP_TEST_ACCESS_SHOW_DB="+dbPath,
				"CPAMP_INGRESS_CONTROL_URL="+blockingIngress.URL,
				"CPAMP_PUBLIC_ORIGIN=",
				"TMPDIR="+scratchDir,
			)
			var childOut bytes.Buffer
			cmd.Stdout = &childOut
			cmd.Stderr = &childOut
			if startErr := cmd.Start(); startErr != nil {
				t.Fatalf("start child: %v", startErr)
			}

			select {
			case <-inspectEntered:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				_, _ = cmd.Process.Wait()
				t.Fatalf("child did not reach Ingress inspect; output:\n%s", childOut.String())
			}

			countSnapshotDirs := func() int {
				t.Helper()
				entries, readErr := os.ReadDir(scratchDir)
				if readErr != nil {
					t.Fatalf("read scratch dir: %v", readErr)
				}
				n := 0
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), "cpamp-") {
						n++
					}
				}
				return n
			}
			if countSnapshotDirs() == 0 {
				t.Fatalf("expected active temporary snapshot directory inside TMPDIR %s", scratchDir)
			}
			signaledAt := time.Now()
			if sigErr := cmd.Process.Signal(tc.sig); sigErr != nil {
				t.Fatalf("signal child (%s): %v", tc.name, sigErr)
			}
			waitDone := make(chan error, 1)
			go func() { waitDone <- cmd.Wait() }()
			select {
			case waitErr := <-waitDone:
				if waitErr != nil {
					t.Fatalf("child exited with error on %s (expected clean signal-canceled return): %v\noutput:\n%s", tc.name, waitErr, childOut.String())
				}
			case <-time.After(3 * time.Second):
				_ = cmd.Process.Kill()
				t.Fatalf("child did not exit promptly after %s", tc.name)
			}
			if elapsed := time.Since(signaledAt); elapsed >= 1500*time.Millisecond {
				t.Fatalf("signal cancellation took %s, expected prompt cancellation well below 2s inspect timeout", elapsed)
			}

			if leaked := countSnapshotDirs(); leaked != 0 {
				t.Fatalf("temporary snapshot directories leaked (%d) in %s after %s", leaked, scratchDir, tc.name)
			}
		})
	}
}
