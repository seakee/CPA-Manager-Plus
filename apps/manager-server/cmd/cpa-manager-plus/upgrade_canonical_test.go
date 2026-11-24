package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/collector"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/config"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/httpapi"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	keyrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshstore"
	settingrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/setting"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	bootstrapservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshrouting"
	freshstrategy "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
)

func seedCompletedManagedUpgradeStore(t *testing.T, controlBasePath, inferenceBasePath string) (*store.Store, config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	sqlDB, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	keyBytes, _, err := security.LoadOrCreateDataKey("", filepath.Join(dir, ".data.key"))
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(sqlDB, protector)
	ctx := context.Background()

	adminKey := "cpamp_UpgradeAdmin0123456789!"
	cred, err := security.NewAdminCredential(adminKey, "upgrade")
	if err != nil {
		t.Fatal(err)
	}
	if err := settingrepo.New(sqlDB).SaveAdminCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveManagerConfig(ctx, model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{
			CPABaseURL:    "http://127.0.0.1:18318",
			ManagementKey: "cpa-mgmt-secret",
		},
	}); err != nil {
		t.Fatal(err)
	}
	// The canonical server proves gateway admission from the persisted setup, so
	// the fixture writes the same resolved connection through the production
	// settings row the running Manager reads after a restart.
	if err := st.SaveSetup(ctx, model.Setup{
		CPAUpstreamURL: "http://127.0.0.1:18318",
		ManagementKey:  "cpa-mgmt-secret",
	}); err != nil {
		t.Fatal(err)
	}

	repo := upgradeoperation.New(sqlDB)
	fpr := "sha256:" + strings.Repeat("b", 64)
	manifest := "sha256:" + strings.Repeat("c", 64)
	if err := repo.Put(ctx, model.UpgradeOperation{
		Stage:                   model.UpgradeStageCPAImport,
		LastForwardStage:        model.UpgradeStageCPAImport,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig},
		SourceKind:              legacypreflight.SourceKindUpgradedLegacy,
		SourceVersion:           "v1.8.0",
		SourceRoot:              filepath.Join(dir, "legacy"),
		SourceFingerprintBefore: fpr,
		SourceFingerprintAfter:  fpr,
		BackupManifestSHA256:    manifest,
		Reconciliation:          json.RawMessage(`{"ok":true}`),
		ControlBasePath:         controlBasePath,
		InferenceBasePath:       inferenceBasePath,
		CreatedAtMS:             1000,
		UpdatedAtMS:             1000,
	}); err != nil {
		t.Fatalf("seed upgrade row: %v", err)
	}
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upgradeoperation.RecordCompletionTx(ctx, tx, model.UpgradeVerification{
		TargetMode:        model.UpgradeTargetManaged,
		SourceFingerprint: fpr,
		Domains:           []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig},
		ObservedAtMS:      2000,
	}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("complete upgrade row: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	cfg := config.Config{DBPath: dbPath, DataKeyPath: filepath.Join(dir, ".data.key")}
	return st, cfg, adminKey
}

func seedCompletedExternalUpgradeStore(t *testing.T, controlBasePath, rawExternalURL string) (*store.Store, config.Config, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "usage.sqlite")
	sqlDB, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	keyBytes, _, err := security.LoadOrCreateDataKey("", filepath.Join(dir, ".data.key"))
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	st := store.New(sqlDB, protector)
	ctx := context.Background()

	adminKey := "cpamp_ExternalAdmin0123456789!"
	cred, err := security.NewAdminCredential(adminKey, "upgrade")
	if err != nil {
		t.Fatal(err)
	}
	if err := settingrepo.New(sqlDB).SaveAdminCredential(ctx, cred); err != nil {
		t.Fatal(err)
	}
	sealedKey, err := protector.ProtectString(strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	canonicalExtURL := strings.TrimRight(rawExternalURL, "/")
	extIdentity := freshkeyapply.ExternalIdentity(canonicalExtURL)
	fpr := "sha256:" + strings.Repeat("d", 64)
	manifest := "sha256:" + strings.Repeat("e", 64)

	repo := upgradeoperation.New(sqlDB)
	if err := repo.Put(ctx, model.UpgradeOperation{
		Stage:                   model.UpgradeStageExternalConnect,
		LastForwardStage:        model.UpgradeStageExternalConnect,
		DomainsDone:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainIdentity, model.UpgradeDomainCPAConfig},
		SourceKind:              legacypreflight.SourceKindUpgradedLegacy,
		SourceVersion:           "v1.8.0",
		SourceRoot:              filepath.Join(dir, "legacy"),
		SourceFingerprintBefore: fpr,
		SourceFingerprintAfter:  fpr,
		BackupManifestSHA256:    manifest,
		Reconciliation:          json.RawMessage(`{"ok":true}`),
		ControlBasePath:         controlBasePath,
		CreatedAtMS:             1000,
		UpdatedAtMS:             1000,
	}); err != nil {
		t.Fatalf("seed external upgrade row: %v", err)
	}
	tx, err := sqlDB.BeginTx(ctx, nil)
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
		t.Fatalf("record external binding: %v", err)
	}
	if _, err := upgradeoperation.RecordCompletionTx(ctx, tx, model.UpgradeVerification{
		TargetMode:          model.UpgradeTargetExternal,
		SourceFingerprint:   fpr,
		Domains:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainIdentity, model.UpgradeDomainCPAConfig},
		BindingIdentity:     extIdentity,
		BindingGeneration:   extIdentity,
		BindingObservedAtMS: 2000,
		ObservedAtMS:        2001,
	}); err != nil {
		_ = tx.Rollback()
		t.Fatalf("complete external upgrade row: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return st, config.Config{DBPath: dbPath, DataKeyPath: filepath.Join(dir, ".data.key")}, adminKey
}

type upgradeIngressCallbackHarness struct {
	control *httptest.Server
	public  *httptest.Server

	mu           sync.Mutex
	state        freshrouting.State
	admitted     bool
	observations []freshAuthorityReply
	failVerify   bool
	failFinalPut atomic.Bool
}

func testTransitionConflict(current, requested freshrouting.State) bool {
	if current.OperationID == "" {
		return false
	}
	if current.OperationID != requested.OperationID ||
		current.InstallationID != requested.InstallationID ||
		current.Generation != requested.Generation ||
		current.Target != requested.Target {
		return true
	}
	if current.Committed && !requested.Committed {
		return true
	}
	if current.InferenceCommitted && !requested.InferenceCommitted {
		return true
	}
	if current.InferenceTarget != "" {
		if requested.InferenceTarget != current.InferenceTarget ||
			requested.SharedEntry != current.SharedEntry ||
			requested.Revision != current.Revision {
			return true
		}
		return false
	}
	if requested.InferenceTarget != "" && current.Committed {
		return requested.Revision < current.Revision
	}
	return requested.Revision != current.Revision
}

func newUpgradeIngressCallbackHarness(t *testing.T, managerURL func() string) *upgradeIngressCallbackHarness {
	t.Helper()
	prevDist := testUpgradeFullWebDist
	testUpgradeFullWebDist = fstest.MapFS{
		"index.html":               {Data: []byte("<!doctype html><html><head><script type=\"module\" src=\"./assets/index-AbCd1234.js\"></script><script>__CPAMP_ROUTING_CONTEXT__</script></head><body><div id=\"root\"></div></body></html>")},
		"assets/index-AbCd1234.js": {Data: []byte("console.log('cpamp-upgrade-test');")},
	}
	t.Cleanup(func() { testUpgradeFullWebDist = prevDist })

	h := &upgradeIngressCallbackHarness{}
	h.control = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/routing/fresh" {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			h.mu.Lock()
			cur := h.state
			h.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(cur)
		case http.MethodPut:
			var requested freshrouting.State
			if err := json.NewDecoder(r.Body).Decode(&requested); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if requested.InferenceCommitted && h.failFinalPut.CompareAndSwap(true, false) {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			mgr := managerURL()
			if mgr == "" {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			authRes, err := http.Get(mgr + freshrouting.AuthorityPath)
			if err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			var authReply freshAuthorityReply
			decErr := json.NewDecoder(authRes.Body).Decode(&authReply)
			_ = authRes.Body.Close()
			if authRes.StatusCode != http.StatusOK || decErr != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			h.mu.Lock()
			h.observations = append(h.observations, authReply)
			if authReply.State != requested || testTransitionConflict(h.state, requested) {
				h.mu.Unlock()
				w.WriteHeader(http.StatusConflict)
				return
			}
			h.state = requested
			h.admitted = authReply.GatewayAdmission.Admitted
			h.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(requested)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	t.Cleanup(h.control.Close)

	h.public = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		st := h.state
		admitted := h.admitted
		failVerify := h.failVerify
		h.mu.Unlock()

		if failVerify {
			w.Header().Set("X-CPAMP-Route-Owner", "gateway")
			w.WriteHeader(http.StatusOK)
			return
		}
		if st.InferenceTarget != "" {
			if r.URL.Path == st.InferenceTarget+"/v0/management/config" || r.URL.Path == st.InferenceTarget+"/v8/management/config" {
				w.Header().Set("X-CPAMP-Route-Owner", "gateway")
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.URL.Path == st.InferenceTarget+"/v1" || strings.HasPrefix(r.URL.Path, st.InferenceTarget+"/v1/") {
				w.Header().Set("X-CPAMP-Route-Owner", "gateway")
				if st.InferenceCommitted && admitted {
					w.WriteHeader(http.StatusOK)
				} else {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
				return
			}
		}
		if st.Target != "" && (r.URL.Path == st.Target || strings.HasPrefix(r.URL.Path, st.Target+"/")) {
			mgr := managerURL()
			proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, mgr+r.URL.RequestURI(), nil)
			if err != nil {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			for k, vals := range r.Header {
				for _, v := range vals {
					proxyReq.Header.Add(k, v)
				}
			}
			res, err := http.DefaultClient.Do(proxyReq)
			if err != nil {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			defer res.Body.Close()
			for k, vals := range res.Header {
				for _, v := range vals {
					w.Header().Add(k, v)
				}
			}
			w.Header().Set("X-CPAMP-Route-Owner", "manager")
			w.WriteHeader(res.StatusCode)
			_, _ = io.Copy(w, res.Body)
			return
		}
		w.Header().Set("X-CPAMP-Route-Owner", "gateway")
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(h.public.Close)
	return h
}

func (h *upgradeIngressCallbackHarness) currentState() (freshrouting.State, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state, h.admitted
}

func (h *upgradeIngressCallbackHarness) setFailVerify(v bool) {
	h.mu.Lock()
	h.failVerify = v
	h.mu.Unlock()
}

func (h *upgradeIngressCallbackHarness) authorityHistory() []freshAuthorityReply {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]freshAuthorityReply, len(h.observations))
	copy(out, h.observations)
	return out
}

func TestCompletedUpgradeCanonicalServingAndMissingInferenceOneTimeCompletion(t *testing.T) {
	st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/upg-console", "")
	var mgrURL atomic.Value
	mgrURL.Store("")
	ingress := newUpgradeIngressCallbackHarness(t, func() string { return mgrURL.Load().(string) })
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", ingress.control.URL)
	t.Setenv("CPAMP_PUBLIC_ORIGIN", ingress.public.URL)

	serverApp := httpapi.New(cfg, st, nil)
	var hookedResource string
	serverApp.SetUpgradeAPIKeys(func(w http.ResponseWriter, _ *http.Request, resource string) {
		hookedResource = resource
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"keys":[],"gatewayPending":false,"revocationAvailable":true}`)
	})
	gatewayAuthority := func(ctx context.Context) bool {
		setup, ok, err := st.LoadSetup(ctx)
		return err == nil && ok && setup.CPAUpstreamURL != "" && setup.ManagementKey != ""
	}

	// Start httptest server before attachCompletedUpgradeRouting so startup Apply callback reaches Manager.
	var handler atomic.Value
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h, ok := handler.Load().(http.Handler); ok && h != nil {
			h.ServeHTTP(w, r)
			return
		}
		serverApp.Handler().ServeHTTP(w, r)
	}))
	defer ts.Close()
	mgrURL.Store(ts.URL)

	if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, gatewayAuthority); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting: %v", err)
	}
	handler.Store(serverApp.Handler())
	// Re-publish now that handler wraps serverApp, proving startup committed control route syncs cleanly.
	if curState, _ := ingress.currentState(); curState.Target == "" {
		d, err := freshrouting.NewHTTPDriver(freshrouting.HTTPConfig{IngressControlURL: ingress.control.URL})
		if err != nil {
			t.Fatal(err)
		}
		authRes, err := http.Get(ts.URL + freshrouting.AuthorityPath)
		if err != nil {
			t.Fatal(err)
		}
		var reply freshAuthorityReply
		_ = json.NewDecoder(authRes.Body).Decode(&reply)
		_ = authRes.Body.Close()
		if err := d.Apply(context.Background(), reply.State); err != nil {
			t.Fatalf("initial control route Apply: %v", err)
		}
	}

	// 1. Unprefixed root and management endpoints are closed (404), while /health stays 200.
	for _, closed := range []string{"/", "/v0/management/config", "/usage-service/config"} {
		res, err := http.Get(ts.URL + closed)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("unprefixed %s status = %d, want 404", closed, res.StatusCode)
		}
	}
	healthRes, err := http.Get(ts.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = healthRes.Body.Close()
	if healthRes.StatusCode != http.StatusOK {
		t.Fatalf("/health = %d, want 200", healthRes.StatusCode)
	}

	// 2. Unauthenticated bootstrap/identity succeeds without leaking inferenceTarget.
	idRes, err := http.Get(ts.URL + "/upg-console/api/v2/bootstrap/identity")
	if err != nil {
		t.Fatal(err)
	}
	idBytes, _ := io.ReadAll(idRes.Body)
	_ = idRes.Body.Close()
	if idRes.StatusCode != http.StatusOK || strings.Contains(string(idBytes), "inferenceTarget") {
		t.Fatalf("unexpected bootstrap/identity: %d %s", idRes.StatusCode, string(idBytes))
	}

	// 3. Before inference completion, fresh-authority reports Admitted=false and InferenceCommitted=false.
	authRes, err := http.Get(ts.URL + freshrouting.AuthorityPath)
	if err != nil {
		t.Fatal(err)
	}
	var authReply freshAuthorityReply
	_ = json.NewDecoder(authRes.Body).Decode(&authReply)
	_ = authRes.Body.Close()
	if authReply.Target != "/upg-console" || authReply.InferenceCommitted || authReply.GatewayAdmission.Admitted {
		t.Fatalf("pre-completion authority = %+v", authReply)
	}

	// 4. Authenticated api-keys hook works under /upg-console/api/v2/api-keys.
	keyReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/upg-console/api/v2/api-keys", nil)
	keyReq.Header.Set("Authorization", "Bearer "+adminKey)
	keyRes, err := http.DefaultClient.Do(keyReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = keyRes.Body.Close()
	if keyRes.StatusCode != http.StatusOK || hookedResource != "api-keys" {
		t.Fatalf("upgrade api-keys hook = %d resource=%q", keyRes.StatusCode, hookedResource)
	}

	// 5. Authenticated one-time completion executes Apply(staged) -> authority callback -> Verify -> CAS commit -> final Apply(committed).
	compReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/upg-console/api/v2/installation/access/completion", bytes.NewBufferString(`{"inferenceBasePath":"/upg-api","sharedEntry":false}`))
	compReq.Header.Set("Authorization", "Bearer "+adminKey)
	compRes, err := http.DefaultClient.Do(compReq)
	if err != nil {
		t.Fatal(err)
	}
	compBody, _ := io.ReadAll(compRes.Body)
	_ = compRes.Body.Close()
	if compRes.StatusCode != http.StatusOK {
		t.Fatalf("POST completion = %d (%s), want 200", compRes.StatusCode, string(compBody))
	}

	// Verify callback history observed staged uncommitted state BEFORE committed state!
	obs := ingress.authorityHistory()
	sawStagedUncommitted := false
	sawCommitted := false
	for _, o := range obs {
		if o.Target == "/upg-console" && o.InferenceTarget == "/upg-api" && o.Committed && !o.InferenceCommitted && !o.GatewayAdmission.Admitted && o.Revision == 2 {
			sawStagedUncommitted = true
		}
		if o.Target == "/upg-console" && o.InferenceTarget == "/upg-api" && o.Committed && o.InferenceCommitted && o.GatewayAdmission.Admitted && o.Revision == 2 {
			sawCommitted = true
		}
	}
	if !sawStagedUncommitted || !sawCommitted {
		t.Fatalf("expected authority callbacks for both staged-uncommitted and committed states, got %+v", obs)
	}

	// 6. Different intent conflicts with 409; same already-committed intent returns truthful 200 without a second DB write.
	repo := upgradeoperation.New(st.SQLDB())
	committedRow, _, err := repo.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !committedRow.InferenceCommitted || committedRow.InferenceBasePath != "/upg-api" {
		t.Fatalf("expected persisted committed inference row, got %+v", committedRow)
	}

	replayReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/upg-console/api/v2/installation/access/completion", bytes.NewBufferString(`{"inferenceBasePath":"/upg-api","sharedEntry":false}`))
	replayReq.Header.Set("Authorization", "Bearer "+adminKey)
	replayRes, err := http.DefaultClient.Do(replayReq)
	if err != nil {
		t.Fatal(err)
	}
	var replayStatus freshrouting.AccessCompletionStatus
	_ = json.NewDecoder(replayRes.Body).Decode(&replayStatus)
	_ = replayRes.Body.Close()
	if replayRes.StatusCode != http.StatusOK || replayStatus.Required || !replayStatus.InferenceCommitted || replayStatus.InferenceBasePath != "/upg-api" {
		t.Fatalf("replay POST completion = %d %+v", replayRes.StatusCode, replayStatus)
	}
	afterReplayRow, _, err := repo.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if afterReplayRow.Revision != committedRow.Revision {
		t.Fatalf("replay performed a second DB write: revision %d -> %d", committedRow.Revision, afterReplayRow.Revision)
	}

	secondReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/upg-console/api/v2/installation/access/completion", bytes.NewBufferString(`{"inferenceBasePath":"/upg-api-2","sharedEntry":false}`))
	secondReq.Header.Set("Authorization", "Bearer "+adminKey)
	secondRes, err := http.DefaultClient.Do(secondReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = secondRes.Body.Close()
	if secondRes.StatusCode != http.StatusConflict {
		t.Fatalf("second POST completion with different intent = %d, want 409", secondRes.StatusCode)
	}
}

func TestCompletedManagedUpgradeNilDriverAndAbsentPublicOriginFailClosed(t *testing.T) {
	// 1. Nil driver (no CPAMP_INGRESS_CONTROL_URL) refuses completion without mutating or committing DB.
	t.Run("nil-driver-cannot-commit", func(t *testing.T) {
		t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
		t.Setenv("CPAMP_PUBLIC_ORIGIN", "")
		st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/upg-console", "")
		serverApp := httpapi.New(cfg, st, nil)
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, func(context.Context) bool { return true }); err != nil {
			t.Fatalf("attachCompletedUpgradeRouting: %v", err)
		}
		ts := httptest.NewServer(serverApp.Handler())
		defer ts.Close()

		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/upg-console/api/v2/installation/access/completion", bytes.NewBufferString(`{"inferenceBasePath":"/upg-api"}`))
		req.Header.Set("Authorization", "Bearer "+adminKey)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("nil driver POST completion = %d, want 503", res.StatusCode)
		}
		op, _, err := upgradeoperation.New(st.SQLDB()).Get(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if op.InferenceCommitted || op.InferenceBasePath != "" {
			t.Fatalf("nil driver mutated row: %+v", op)
		}
	})

	// 2. CPAMP_INGRESS_CONTROL_URL without optional CPAMP_PUBLIC_ORIGIN publishes committed startup route, while public Verify fails closed.
	t.Run("control-only-startup-publishes-and-verify-fails-closed", func(t *testing.T) {
		st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/upg-console", "")
		var mgrURL atomic.Value
		mgrURL.Store("")
		ingress := newUpgradeIngressCallbackHarness(t, func() string { return mgrURL.Load().(string) })
		t.Setenv("CPAMP_INGRESS_CONTROL_URL", ingress.control.URL)
		t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

		var handler atomic.Value
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if h, ok := handler.Load().(http.Handler); ok && h != nil {
				h.ServeHTTP(w, r)
				return
			}
			http.NotFound(w, r)
		}))
		defer ts.Close()
		mgrURL.Store(ts.URL)

		// Pre-bind handler via temporary wrapper so startup publishToIngress inside attachCompletedUpgradeRouting succeeds synchronously.
		serverApp := httpapi.New(cfg, st, nil)
		handler.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serverApp.Handler().ServeHTTP(w, r)
		}))
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, func(context.Context) bool { return true }); err != nil {
			t.Fatalf("attachCompletedUpgradeRouting without CPAMP_PUBLIC_ORIGIN: %v", err)
		}

		published, admitted := ingress.currentState()
		if published.Target != "/upg-console" || !published.Committed || published.InferenceCommitted || admitted {
			t.Fatalf("startup published state without CPAMP_PUBLIC_ORIGIN = %+v admitted=%t", published, admitted)
		}

		// Completion POST stages intent, syncs to Ingress, and fails closed at Verify (503) because CPAMP_PUBLIC_ORIGIN is absent.
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/upg-console/api/v2/installation/access/completion", bytes.NewBufferString(`{"inferenceBasePath":"/upg-api"}`))
		req.Header.Set("Authorization", "Bearer "+adminKey)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("completion without CPAMP_PUBLIC_ORIGIN = %d, want 503", res.StatusCode)
		}
		op, _, err := upgradeoperation.New(st.SQLDB()).Get(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if op.InferenceCommitted || op.InferenceBasePath != "/upg-api" {
			t.Fatalf("expected uncommitted staged intent when PublicOrigin absent, got %+v", op)
		}
	})
}

func TestCompletedManagedUpgradeFailureRestartCompetingIntentAndFailedFinalPublication(t *testing.T) {
	st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/upg-console", "")
	var mgrURL atomic.Value
	mgrURL.Store("")
	ingress := newUpgradeIngressCallbackHarness(t, func() string { return mgrURL.Load().(string) })
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", ingress.control.URL)
	t.Setenv("CPAMP_PUBLIC_ORIGIN", ingress.public.URL)

	startInstance := func() *httptest.Server {
		t.Helper()
		serverApp := httpapi.New(cfg, st, nil)
		var readyHandler atomic.Value
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if h, ok := readyHandler.Load().(http.Handler); ok && h != nil {
				h.ServeHTTP(w, r)
				return
			}
			serverApp.Handler().ServeHTTP(w, r)
		}))
		mgrURL.Store(ts.URL)
		readyHandler.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serverApp.Handler().ServeHTTP(w, r)
		}))
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, func(context.Context) bool { return true }); err != nil {
			ts.Close()
			t.Fatalf("attachCompletedUpgradeRouting: %v", err)
		}
		return ts
	}

	ts1 := startInstance()
	// 1. Force public Verify failure: intent is persisted as UNCOMMITTED; management remains committed.
	ingress.setFailVerify(true)
	failReq, _ := http.NewRequest(http.MethodPost, ts1.URL+"/upg-console/api/v2/installation/access/completion", bytes.NewBufferString(`{"inferenceBasePath":"/upg-api","sharedEntry":false}`))
	failReq.Header.Set("Authorization", "Bearer "+adminKey)
	failRes, err := http.DefaultClient.Do(failReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = failRes.Body.Close()
	if failRes.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("Verify failure status = %d, want 503", failRes.StatusCode)
	}
	ts1.Close()

	// 2. Simulate process restart over the same store with staged uncommitted intent.
	ingress.setFailVerify(false)
	ts2 := startInstance()
	defer ts2.Close()

	// Operation document keeps real canonical common identity, empty Fresh-only fields, and MissingInferenceCompletionRequired=true.
	opReq, _ := http.NewRequest(http.MethodGet, ts2.URL+"/upg-console/api/v2/operation", nil)
	opReq.Header.Set("Authorization", "Bearer "+adminKey)
	opRes, err := http.DefaultClient.Do(opReq)
	if err != nil {
		t.Fatal(err)
	}
	var opDoc freshrouting.OperationDocument
	_ = json.NewDecoder(opRes.Body).Decode(&opDoc)
	_ = opRes.Body.Close()
	if opDoc.Identity.InstallationID == "" || opDoc.Identity.Generation == "" || opDoc.Identity.OperationID == "" ||
		opDoc.Identity.AdmissionID != "" || opDoc.Identity.DatabasePath != "" ||
		opDoc.Routing.OperationID != opDoc.Identity.OperationID ||
		opDoc.Routing.InferenceTarget != "/upg-api" || opDoc.Routing.InferenceCommitted || !opDoc.Routing.MissingInferenceCompletionRequired {
		t.Fatalf("unexpected restarted OperationDocument: %+v", opDoc)
	}

	// Access-info must not advertise an uncommitted staged inference prefix as active.
	infoReq, _ := http.NewRequest(http.MethodGet, ts2.URL+"/upg-console/api/v2/api-keys/access-info", nil)
	infoReq.Header.Set("Authorization", "Bearer "+adminKey)
	infoRes, err := http.DefaultClient.Do(infoReq)
	if err != nil {
		t.Fatal(err)
	}
	var preInfo keyAccessInfo
	_ = json.NewDecoder(infoRes.Body).Decode(&preInfo)
	_ = infoRes.Body.Close()
	if preInfo.Ready || preInfo.InferenceBasePath != "" {
		t.Fatalf("uncommitted staged prefix leaked as active access-info: %+v", preInfo)
	}

	// Competing different intent is rejected (409) without altering stored staged intent.
	competingReq, _ := http.NewRequest(http.MethodPost, ts2.URL+"/upg-console/api/v2/installation/access/completion", bytes.NewBufferString(`{"inferenceBasePath":"/competing-api","sharedEntry":false}`))
	competingReq.Header.Set("Authorization", "Bearer "+adminKey)
	competingRes, err := http.DefaultClient.Do(competingReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = competingRes.Body.Close()
	if competingRes.StatusCode != http.StatusConflict {
		t.Fatalf("competing different intent status = %d, want 409", competingRes.StatusCode)
	}

	// 3. Inject failed final publication: CAS commit succeeds in DB, final Apply fails -> must return 503 (no false success).
	ingress.failFinalPut.Store(true)
	pubFailReq, _ := http.NewRequest(http.MethodPost, ts2.URL+"/upg-console/api/v2/installation/access/completion", bytes.NewBufferString(`{"inferenceBasePath":"/upg-api","sharedEntry":false}`))
	pubFailReq.Header.Set("Authorization", "Bearer "+adminKey)
	pubFailRes, err := http.DefaultClient.Do(pubFailReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = pubFailRes.Body.Close()
	if pubFailRes.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("failed final publication status = %d, want 503", pubFailRes.StatusCode)
	}

	repo := upgradeoperation.New(st.SQLDB())
	afterPubFail, _, err := repo.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !afterPubFail.InferenceCommitted || afterPubFail.InferenceBasePath != "/upg-api" {
		t.Fatalf("expected DB CAS committed row before failed final Apply, got %+v", afterPubFail)
	}

	// 4. Retry same intent: publishes final committed state to Ingress without a second DB write and returns 200 OK.
	retryReq, _ := http.NewRequest(http.MethodPost, ts2.URL+"/upg-console/api/v2/installation/access/completion", bytes.NewBufferString(`{"inferenceBasePath":"/upg-api","sharedEntry":false}`))
	retryReq.Header.Set("Authorization", "Bearer "+adminKey)
	retryRes, err := http.DefaultClient.Do(retryReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = retryRes.Body.Close()
	if retryRes.StatusCode != http.StatusOK {
		t.Fatalf("retry after failed final publication = %d, want 200", retryRes.StatusCode)
	}
	afterRetry, _, err := repo.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if afterRetry.Revision != afterPubFail.Revision {
		t.Fatalf("retry after failed final publication wrote DB a second time: %d -> %d", afterPubFail.Revision, afterRetry.Revision)
	}
	ingState, ingAdmitted := ingress.currentState()
	if !ingState.InferenceCommitted || ingState.InferenceTarget != "/upg-api" || !ingAdmitted {
		t.Fatalf("final Ingress state after retry = %+v admitted=%t", ingState, ingAdmitted)
	}
}

func TestCompletedManagedUpgradeConcurrentCompletionRequests(t *testing.T) {
	t.Run("competing-different-intents", func(t *testing.T) {
		st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/upg-console", "")
		var mgrURL atomic.Value
		mgrURL.Store("")
		ingress := newUpgradeIngressCallbackHarness(t, func() string { return mgrURL.Load().(string) })
		t.Setenv("CPAMP_INGRESS_CONTROL_URL", ingress.control.URL)
		t.Setenv("CPAMP_PUBLIC_ORIGIN", ingress.public.URL)

		serverApp := httpapi.New(cfg, st, nil)
		var readyHandler atomic.Value
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if h, ok := readyHandler.Load().(http.Handler); ok && h != nil {
				h.ServeHTTP(w, r)
				return
			}
			serverApp.Handler().ServeHTTP(w, r)
		}))
		defer ts.Close()
		mgrURL.Store(ts.URL)
		readyHandler.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serverApp.Handler().ServeHTTP(w, r)
		}))
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, func(context.Context) bool { return true }); err != nil {
			t.Fatal(err)
		}

		candidates := []string{"/conc-api-a", "/conc-api-b"}
		codes := make([]int, len(candidates))
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i, prefix := range candidates {
			wg.Add(1)
			go func(idx int, p string) {
				defer wg.Done()
				<-start
				req, _ := http.NewRequest(http.MethodPost, ts.URL+"/upg-console/api/v2/installation/access/completion", bytes.NewBufferString(`{"inferenceBasePath":"`+p+`"}`))
				req.Header.Set("Authorization", "Bearer "+adminKey)
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					return
				}
				codes[idx] = res.StatusCode
				_ = res.Body.Close()
			}(i, prefix)
		}
		close(start)
		wg.Wait()

		okCount, conflictCount := 0, 0
		for _, c := range codes {
			if c == http.StatusOK {
				okCount++
			} else if c == http.StatusConflict {
				conflictCount++
			}
		}
		if okCount != 1 || conflictCount != 1 {
			t.Fatalf("concurrent competing statuses = %v, want one 200 and one 409", codes)
		}
	})

	t.Run("identical-concurrent-intents", func(t *testing.T) {
		st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/upg-console", "")
		var mgrURL atomic.Value
		mgrURL.Store("")
		ingress := newUpgradeIngressCallbackHarness(t, func() string { return mgrURL.Load().(string) })
		t.Setenv("CPAMP_INGRESS_CONTROL_URL", ingress.control.URL)
		t.Setenv("CPAMP_PUBLIC_ORIGIN", ingress.public.URL)

		serverApp := httpapi.New(cfg, st, nil)
		var readyHandler atomic.Value
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if h, ok := readyHandler.Load().(http.Handler); ok && h != nil {
				h.ServeHTTP(w, r)
				return
			}
			serverApp.Handler().ServeHTTP(w, r)
		}))
		defer ts.Close()
		mgrURL.Store(ts.URL)
		readyHandler.Store(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			serverApp.Handler().ServeHTTP(w, r)
		}))
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, func(context.Context) bool { return true }); err != nil {
			t.Fatal(err)
		}

		const workers = 4
		codes := make([]int, workers)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range workers {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				<-start
				req, _ := http.NewRequest(http.MethodPost, ts.URL+"/upg-console/api/v2/installation/access/completion", bytes.NewBufferString(`{"inferenceBasePath":"/conc-same"}`))
				req.Header.Set("Authorization", "Bearer "+adminKey)
				res, err := http.DefaultClient.Do(req)
				if err != nil {
					return
				}
				codes[idx] = res.StatusCode
				_ = res.Body.Close()
			}(i)
		}
		close(start)
		wg.Wait()

		for _, c := range codes {
			if c != http.StatusOK {
				t.Fatalf("concurrent identical statuses = %v, want all 200", codes)
			}
		}
		op, _, err := upgradeoperation.New(st.SQLDB()).Get(context.Background())
		if err != nil || !op.InferenceCommitted || op.InferenceBasePath != "/conc-same" {
			t.Fatalf("final concurrent identical row = %+v, err=%v", op, err)
		}
	})
}

func TestCompletedExternalUpgradeCanonicalOperationAndFencedAccessInfo(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")
	st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", "https://ext-cpa.example:8317/custom/")
	serverApp := httpapi.New(cfg, st, nil)
	if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting(External): %v", err)
	}
	ts := httptest.NewServer(serverApp.Handler())
	defer ts.Close()

	// 1. Canonical operation document carries real common identity and empty Fresh-only fields.
	opReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/ext-console/api/v2/operation", nil)
	opReq.Header.Set("Authorization", "Bearer "+adminKey)
	opRes, err := http.DefaultClient.Do(opReq)
	if err != nil {
		t.Fatal(err)
	}
	var opDoc freshrouting.OperationDocument
	_ = json.NewDecoder(opRes.Body).Decode(&opDoc)
	_ = opRes.Body.Close()
	if opDoc.Identity.InstallationID == "" || opDoc.Identity.OperationID == "" ||
		opDoc.Identity.AdmissionID != "" || opDoc.Identity.DatabasePath != "" ||
		opDoc.Routing.InferenceTarget != "" || opDoc.Routing.MissingInferenceCompletionRequired {
		t.Fatalf("unexpected External OperationDocument: %+v", opDoc)
	}

	// 2. Canonical api-keys/access-info exposes normalized External URL and protocol URLs, never Manager inference prefix.
	infoReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/ext-console/api/v2/api-keys/access-info", nil)
	infoReq.Header.Set("Authorization", "Bearer "+adminKey)
	infoRes, err := http.DefaultClient.Do(infoReq)
	if err != nil {
		t.Fatal(err)
	}
	var info keyAccessInfo
	_ = json.NewDecoder(infoRes.Body).Decode(&info)
	_ = infoRes.Body.Close()
	if !info.Ready || info.Mode != "connect_external" || info.ExternalBaseURL != "https://ext-cpa.example:8317/custom" || info.InferenceBasePath != "" || len(info.Protocols) != 4 || info.Protocols[0].BasePath != "https://ext-cpa.example:8317/custom/v1" {
		t.Fatalf("unexpected External keyAccessInfo: %+v", info)
	}

	// 3. Drifting upgrade_external_connect identity/generation from VerificationEvidence fails closed.
	if _, err := st.SQLDB().Exec(`update upgrade_external_connect set identity = 'drifted', generation = 'drifted' where id = 1`); err != nil {
		t.Fatal(err)
	}
	infoReq2, _ := http.NewRequest(http.MethodGet, ts.URL+"/ext-console/api/v2/api-keys/access-info", nil)
	infoReq2.Header.Set("Authorization", "Bearer "+adminKey)
	infoRes2, err := http.DefaultClient.Do(infoReq2)
	if err != nil {
		t.Fatal(err)
	}
	var info2 keyAccessInfo
	_ = json.NewDecoder(infoRes2.Body).Decode(&info2)
	_ = infoRes2.Body.Close()
	if info2.Ready || info2.ExternalBaseURL != "" || len(info2.Protocols) != 0 || info2.Code != "external_target_unavailable" {
		t.Fatalf("drifted External keyAccessInfo did not fail closed: %+v", info2)
	}
}

type upgradeCanonicalTestRuntime struct {
	mu              sync.Mutex
	status          model.RuntimeObservedStatus
	startCalls      int
	restartCalls    int
	lastMutationReq model.RuntimeMutationRequest
}

func (r *upgradeCanonicalTestRuntime) Status(context.Context) (model.RuntimeObservedStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status, nil
}

func (r *upgradeCanonicalTestRuntime) Start(_ context.Context, req model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	r.mu.Lock()
	r.startCalls++
	r.lastMutationReq = req
	r.mu.Unlock()
	return model.RuntimeOperationResult{
		OperationID:       req.OperationID,
		OperationType:     model.RuntimeOperationStart,
		RuntimeIdentity:   req.ExpectedRuntimeIdentity,
		RuntimeGeneration: req.ExpectedRuntimeGeneration,
		State:             model.RuntimeOperationSucceeded,
	}, nil
}

func (r *upgradeCanonicalTestRuntime) Stop(_ context.Context, req model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	r.mu.Lock()
	r.lastMutationReq = req
	r.mu.Unlock()
	return model.RuntimeOperationResult{
		OperationID:       req.OperationID,
		OperationType:     model.RuntimeOperationStop,
		RuntimeIdentity:   req.ExpectedRuntimeIdentity,
		RuntimeGeneration: req.ExpectedRuntimeGeneration,
		State:             model.RuntimeOperationSucceeded,
	}, nil
}

func (r *upgradeCanonicalTestRuntime) Restart(_ context.Context, req model.RuntimeMutationRequest) (model.RuntimeOperationResult, error) {
	r.mu.Lock()
	r.restartCalls++
	r.lastMutationReq = req
	r.mu.Unlock()
	return model.RuntimeOperationResult{
		OperationID:       req.OperationID,
		OperationType:     model.RuntimeOperationRestart,
		RuntimeIdentity:   req.ExpectedRuntimeIdentity,
		RuntimeGeneration: req.ExpectedRuntimeGeneration,
		State:             model.RuntimeOperationSucceeded,
	}, nil
}

func (r *upgradeCanonicalTestRuntime) PrepareUpdate(context.Context, model.RuntimePrepareUpdateRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, nil
}

func (r *upgradeCanonicalTestRuntime) ActivateUpdate(context.Context, model.RuntimeActivateUpdateRequest) (model.RuntimeOperationResult, error) {
	return model.RuntimeOperationResult{}, nil
}

func (r *upgradeCanonicalTestRuntime) ObserveUpdateOperation(context.Context, model.RuntimeObserveUpdateOperationRequest) (model.RuntimeUpdateOperationObservation, error) {
	return model.RuntimeUpdateOperationObservation{}, nil
}

func TestCompletedUnroutedUpgradeFailsClosedAndStartsCleanlyAfterOneTimeRepair(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/temp-console", "https://ext-cpa.example:8317")
	// Simulate pre-existing v2@9060054e completed row without committed prefixes.
	if _, err := st.SQLDB().Exec(`update upgrade_operations set control_base_path = '', inference_base_path = '', shared_entry = 0, routing_committed = 0, inference_committed = 0 where id = 1`); err != nil {
		t.Fatal(err)
	}

	serverApp := httpapi.New(cfg, st, nil)
	err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil)
	if err == nil {
		t.Fatal("attachCompletedUpgradeRouting succeeded on completed unrouted store")
	}

	// Belt-and-suspenders: even if a caller hits serverApp.Handler(), every surface
	// fails closed with 503 (never 204 authority, never unwrapped legacy router, never root Gateway).
	closedTS := httptest.NewServer(serverApp.Handler())
	defer closedTS.Close()
	for _, blockedPath := range []string{
		freshrouting.AuthorityPath,
		"/health",
		"/v1/models",
		"/management.html",
		"/v0/management/config",
		"/usage-service/status",
	} {
		res, reqErr := http.Get(closedTS.URL + blockedPath)
		if reqErr != nil {
			t.Fatalf("GET %s: %v", blockedPath, reqErr)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("unrouted complete handler GET %s status = %d, want 503 (never 204/200)", blockedPath, res.StatusCode)
		}
	}

	// One-time missing-prefix repair commits /repaired-console, after which canonical startup succeeds.
	cur, found, err := upgradeoperation.New(st.SQLDB()).Get(context.Background())
	if err != nil || !found {
		t.Fatalf("Get unrouted row: found=%t err=%v", found, err)
	}
	if _, err := upgradeoperation.New(st.SQLDB()).RepairMissingRoutingOnCompletedUpgrade(
		context.Background(),
		cur.Revision,
		"/repaired-console",
		"",
		false,
		"/repaired-console",
		"",
	); err != nil {
		t.Fatalf("RepairMissingRoutingOnCompletedUpgrade: %v", err)
	}

	repairedApp := httpapi.New(cfg, st, nil)
	if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, repairedApp, nil); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting after one-time repair: %v", err)
	}
	repairedTS := httptest.NewServer(repairedApp.Handler())
	defer repairedTS.Close()

	opReq, _ := http.NewRequest(http.MethodGet, repairedTS.URL+"/repaired-console/api/v2/operation", nil)
	opReq.Header.Set("Authorization", "Bearer "+adminKey)
	opRes, err := http.DefaultClient.Do(opReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = opRes.Body.Close()
	if opRes.StatusCode != http.StatusOK {
		t.Fatalf("repaired /repaired-console/api/v2/operation status = %d, want 200", opRes.StatusCode)
	}
	unprefixedRes, err := http.Get(repairedTS.URL + "/v0/management/config")
	if err != nil {
		t.Fatal(err)
	}
	_ = unprefixedRes.Body.Close()
	if unprefixedRes.StatusCode != http.StatusNotFound {
		t.Fatalf("unprefixed /v0/management/config status = %d, want 404", unprefixedRes.StatusCode)
	}
}

func TestUpgradeCanonicalSQLAndRowDriftPropagate503AndDuplicateAuthorizationRejected(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	// Use Managed uncommitted-inference store so we also exercise completion POST SQL 503.
	st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/upg-console", "")
	serverApp := httpapi.New(cfg, st, nil)
	if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, func(context.Context) bool { return true }); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting: %v", err)
	}
	ts := httptest.NewServer(serverApp.Handler())
	defer ts.Close()

	// 1. Duplicate Authorization headers must be rejected with 401 Unauthorized.
	dupReq, _ := http.NewRequest(http.MethodGet, ts.URL+"/upg-console/api/v2/operation", nil)
	dupReq.Header.Add("Authorization", "Bearer "+adminKey)
	dupReq.Header.Add("Authorization", "Bearer "+adminKey)
	dupRes, err := http.DefaultClient.Do(dupReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = dupRes.Body.Close()
	if dupRes.StatusCode != http.StatusUnauthorized {
		t.Fatalf("duplicate Authorization status = %d, want 401", dupRes.StatusCode)
	}

	// 2. Deleting upgrade_operations row propagates 503 across authority and canonical surfaces (never stale memory).
	if _, err := st.SQLDB().Exec(`delete from upgrade_operations where id = 1`); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		method string
		path   string
		body   string
		auth   bool
	}{
		{method: http.MethodGet, path: freshrouting.AuthorityPath},
		{method: http.MethodGet, path: "/health"},
		{method: http.MethodGet, path: "/upg-console/api/v2/operation", auth: true},
		{method: http.MethodGet, path: "/upg-console/api/v2/api-keys/access-info", auth: true},
		{method: http.MethodGet, path: "/upg-console/api/v2/installation/access/completion", auth: true},
		{method: http.MethodPost, path: "/upg-console/api/v2/installation/access/completion", body: `{"inference_base_path":"/upg-gw","shared_entry":false}`, auth: true},
	} {
		var reader io.Reader
		if check.body != "" {
			reader = strings.NewReader(check.body)
		}
		req, _ := http.NewRequest(check.method, ts.URL+check.path, reader)
		if check.auth {
			req.Header.Set("Authorization", "Bearer "+adminKey)
		}
		if check.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, reqErr := http.DefaultClient.Do(req)
		if reqErr != nil {
			t.Fatalf("%s %s: %v", check.method, check.path, reqErr)
		}
		_ = res.Body.Close()
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("missing row %s %s status = %d, want 503 (never stale memory)", check.method, check.path, res.StatusCode)
		}
	}

	// 3. Closed SQLite database also propagates 503 on loadCurrentOp and completion writes.
	st2, cfg2, adminKey2 := seedCompletedManagedUpgradeStore(t, "/upg-closed", "")
	serverApp2 := httpapi.New(cfg2, st2, nil)
	if err := attachCompletedUpgradeRouting(context.Background(), cfg2, st2, serverApp2, nil); err != nil {
		t.Fatal(err)
	}
	ts2 := httptest.NewServer(serverApp2.Handler())
	defer ts2.Close()
	_ = st2.SQLDB().Close()

	postReq, _ := http.NewRequest(http.MethodPost, ts2.URL+"/upg-closed/api/v2/installation/access/completion", strings.NewReader(`{"inference_base_path":"/upg-gw","shared_entry":false}`))
	postReq.Header.Set("Authorization", "Bearer "+adminKey2)
	postReq.Header.Set("Content-Type", "application/json")
	postRes, err := http.DefaultClient.Do(postReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = postRes.Body.Close()
	if postRes.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("closed SQLite completion POST status = %d, want 503 (not 409)", postRes.StatusCode)
	}
}

func TestCompletedExternalUpgradeAccessInfoFencesURLAndEnvelopeAndStoredConnectionDrift(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	t.Run("base-url-drift-from-binding-identity", func(t *testing.T) {
		st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", "https://ext-cpa.example:8317")
		serverApp := httpapi.New(cfg, st, nil)
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil); err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(serverApp.Handler())
		defer ts.Close()

		if _, err := st.SQLDB().Exec(`update upgrade_external_connect set base_url = 'https://evil-cpa.example:8317' where id = 1`); err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ext-console/api/v2/access-info/default", nil)
		req.Header.Set("Authorization", "Bearer "+adminKey)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var info keyAccessInfo
		_ = json.NewDecoder(res.Body).Decode(&info)
		_ = res.Body.Close()
		if info.Ready || info.ExternalBaseURL != "" || len(info.Protocols) != 0 {
			t.Fatalf("base_url drift did not blank External access-info: %+v", info)
		}
	})

	t.Run("corrupted-sealed-management-key-envelope", func(t *testing.T) {
		st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", "https://ext-cpa.example:8317")
		serverApp := httpapi.New(cfg, st, nil)
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil); err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(serverApp.Handler())
		defer ts.Close()

		if _, err := st.SQLDB().Exec(`update upgrade_external_connect set management_key = 'enc:v1:corrupted-ciphertext' where id = 1`); err != nil {
			t.Fatal(err)
		}
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ext-console/api/v2/api-keys/access-info", nil)
		req.Header.Set("Authorization", "Bearer "+adminKey)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var info keyAccessInfo
		_ = json.NewDecoder(res.Body).Decode(&info)
		_ = res.Body.Close()
		if info.Ready || info.ExternalBaseURL != "" || len(info.Protocols) != 0 {
			t.Fatalf("corrupted envelope did not blank External access-info: %+v", info)
		}
	})

	t.Run("stored-manager-config-connection-drift", func(t *testing.T) {
		st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", "https://ext-cpa.example:8317")
		if err := st.SaveManagerConfig(context.Background(), model.ManagerConfig{
			CPAConnection: model.ManagerCPAConnectionConfig{
				CPABaseURL:    "https://different-stored-cpa.example:8317",
				ManagementKey: strings.Repeat("a", 64),
			},
		}); err != nil {
			t.Fatal(err)
		}
		serverApp := httpapi.New(cfg, st, nil)
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil); err != nil {
			t.Fatal(err)
		}
		ts := httptest.NewServer(serverApp.Handler())
		defer ts.Close()

		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/ext-console/api/v2/api-keys/access-info", nil)
		req.Header.Set("Authorization", "Bearer "+adminKey)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var info keyAccessInfo
		_ = json.NewDecoder(res.Body).Decode(&info)
		_ = res.Body.Close()
		if info.Ready || info.ExternalBaseURL != "" || len(info.Protocols) != 0 {
			t.Fatalf("stored manager_config drift did not blank External access-info: %+v", info)
		}
	})
}

func TestCompletedManagedUpgradeMountsConsoleResourcesAndIsolatesBridge(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	mgmtKey := strings.Repeat("b", 64)
	clientKey := "sk-managed-client-key-01"
	var docMu sync.Mutex
	injectXSearch := false
	pluginsEnabled := false
	var sawAdminTokenAtModels atomic.Bool

	cpaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if r.URL.Path == "/v1/models" {
			if strings.Contains(auth, "cpamp_UpgradeAdmin") {
				sawAdminTokenAtModels.Store(true)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if auth != "Bearer "+clientKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5","object":"model"}]}`))
			return
		}
		if auth != "Bearer "+mgmtKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.55")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config":
			docMu.Lock()
			curX := injectXSearch
			curPlugins := pluginsEnabled
			docMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"config-version": 8,
				"access":         map[string]any{"api-keys": []string{clientKey}},
				"plugins":        map[string]any{"dir": "/data/runtime/supervisor/plugins", "enabled": curPlugins},
				"upstream":       map[string]any{"xai": map[string]any{"inject-x-search": curX}},
			})
		case r.Method == http.MethodPatch && r.URL.Path == "/v8/management/config/upstream/xai":
			var body map[string]bool
			_ = json.NewDecoder(r.Body).Decode(&body)
			docMu.Lock()
			injectXSearch = body["inject-x-search"]
			docMu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodPatch && r.URL.Path == "/v8/management/config/plugins":
			var body map[string]bool
			_ = json.NewDecoder(r.Body).Decode(&body)
			docMu.Lock()
			pluginsEnabled = body["enabled"]
			docMu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config/api-keys":
			w.Header().Set("ETag", `"upg-etag-1"`)
			_, _ = w.Write([]byte(`{"claude":[{"name":"claude-upg"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config/api-keys/claude":
			w.Header().Set("ETag", `"upg-etag-1"`)
			_, _ = w.Write([]byte(`[{"name":"claude-upg"}]`))
		case r.Method == http.MethodPut && r.URL.Path == "/v8/management/config/api-keys/claude":
			if r.Header.Get("If-Match") != `"upg-etag-1"` {
				w.Header().Set("ETag", `"upg-etag-1"`)
				w.WriteHeader(http.StatusPreconditionFailed)
				_, _ = w.Write([]byte(`{"error":"precondition_failed"}`))
				return
			}
			w.Header().Set("ETag", `"upg-etag-2"`)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer cpaServer.Close()

	st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/upg-console", "/upg-gw")
	keyBytes, _, err := security.LoadOrCreateDataKey("", cfg.DataKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	sealedDestKey, err := protector.ProtectString(mgmtKey)
	if err != nil {
		t.Fatal(err)
	}
	qualifiedArtifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
	if err != nil {
		t.Fatal(err)
	}
	sourceIdentity := "external:sha256:" + strings.Repeat("c", 64)
	pluginsInit := false
	destPayload, _ := json.Marshal(upgradeDestinationRecord{
		BaseURL:           cpaServer.URL,
		URLProved:         true,
		ManagementKey:     sealedDestKey,
		ServerFingerprint: "external:sha256:" + strings.Repeat("b", 64),
		Attempt:           1,
		OperationID:       upgradeManagedDestinationOperationID(sourceIdentity, 1),
		Intent: upgradeDestinationIntent{
			ExpectedRuntimeIdentity:   "rt-managed-1",
			ExpectedRuntimeGeneration: 7,
			ExpectedActiveArtifactID:  qualifiedArtifact,
			SourceIdentity:            sourceIdentity,
			SourceClass:               upgradeManagedDestinationClass,
			APIKeys:                   []string{clientKey},
			PluginsEnabled:            &pluginsInit,
		},
	})
	if _, err := st.SQLDB().Exec(`insert into settings(key, value, updated_at_ms) values(?, ?, ?)`, upgradeImportDestinationSettingKey, string(destPayload), time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetEmbeddedRuntimeDesiredLifecycle(context.Background(), model.EmbeddedRuntimeDesiredRunning); err != nil {
		t.Fatal(err)
	}

	rt := &upgradeCanonicalTestRuntime{
		status: model.RuntimeObservedStatus{
			Identity:           "rt-managed-1",
			Generation:         19,
			ProtocolVersion:    "v1",
			State:              model.RuntimeStateReady,
			CPAObservedVersion: "v8.0.55",
			Capabilities:       model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStop, model.RuntimeCapabilityRestart},
			ActiveGatewayArtifact: &model.ActiveGatewayArtifact{
				Engine:     "cpa",
				ArtifactID: qualifiedArtifact,
				Version:    "v8.0.55",
			},
		},
	}

	manager := collector.NewManager(cfg, st)
	t.Cleanup(func() { manager.Stop() })
	serverApp := httpapi.New(cfg, st, manager)
	if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, func(context.Context) bool { return true }, upgradeCanonicalDependencies{
		Protector: protector,
		Runtime:   rt,
	}); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting(Managed resources): %v", err)
	}
	ts := httptest.NewServer(serverApp.Handler())
	defer ts.Close()

	authReq := func(method, path, body string) *http.Response {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, ts.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+adminKey)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// 1. Gateway config read + group write + plugin support header.
	gwRes := authReq(http.MethodGet, "/upg-console/api/v2/gateway/config", "")
	if gwRes.StatusCode != http.StatusOK || gwRes.Header.Get(supportPluginHeader) != supportPluginHeaderValue {
		t.Fatalf("GET gateway/config status=%d pluginHeader=%q", gwRes.StatusCode, gwRes.Header.Get(supportPluginHeader))
	}
	var gwView freshconfig.View
	_ = json.NewDecoder(gwRes.Body).Decode(&gwView)
	_ = gwRes.Body.Close()
	if gwView.Capability.Mode != freshconfig.ModeManaged || !gwView.Capability.Writable || !gwView.Capability.RulesWritable || gwView.Generation != "7" {
		t.Fatalf("unexpected Managed gateway/config view: %+v", gwView.Capability)
	}
	var xaiBaseline string
	for _, g := range gwView.Groups {
		if g.ID == "xai" {
			xaiBaseline = g.Baseline
		}
	}
	savePayload := `{"requestId":"` + strings.Repeat("a", 32) + `","expected":{"generation":"7","baseline":"` + xaiBaseline + `"},"edits":[{"path":"upstream.xai.inject-x-search","action":"set","value":true}]}`
	saveRes := authReq(http.MethodPost, "/upg-console/api/v2/gateway/config/groups/xai", savePayload)
	_ = saveRes.Body.Close()
	if saveRes.StatusCode != http.StatusOK {
		t.Fatalf("POST gateway/config/groups/xai status = %d, want 200", saveRes.StatusCode)
	}

	// 2. Plugin runtime read + write.
	plRes := authReq(http.MethodGet, "/upg-console/api/v2/plugins/runtime", "")
	var plView freshconfig.View
	_ = json.NewDecoder(plRes.Body).Decode(&plView)
	_ = plRes.Body.Close()
	if plRes.StatusCode != http.StatusOK || !plView.Capability.Writable || len(plView.Groups) != 1 {
		t.Fatalf("GET plugins/runtime status=%d cap=%+v", plRes.StatusCode, plView.Capability)
	}
	plSave := `{"requestId":"` + strings.Repeat("b", 32) + `","expected":{"generation":"7","baseline":"` + plView.Groups[0].Baseline + `"},"edits":[{"path":"plugins.enabled","action":"set","value":true}]}`
	plSaveRes := authReq(http.MethodPost, "/upg-console/api/v2/plugins/runtime/groups/plugins", plSave)
	_ = plSaveRes.Body.Close()
	if plSaveRes.StatusCode != http.StatusOK {
		t.Fatalf("POST plugins/runtime/groups/plugins status = %d, want 200", plSaveRes.StatusCode)
	}

	// 3. Runtime status, lifecycle operations (separate Supervisor generation fence), artifact surface, and update-channel.
	rtStatusRes := authReq(http.MethodGet, "/upg-console/api/v2/runtime/status", "")
	var rtStatus freshRuntimeStatusResponse
	_ = json.NewDecoder(rtStatusRes.Body).Decode(&rtStatus)
	_ = rtStatusRes.Body.Close()
	if rtStatusRes.StatusCode != http.StatusOK || rtStatus.Mode != "embedded" || rtStatus.Status != "ready" {
		t.Fatalf("unexpected runtime/status: code=%d body=%+v", rtStatusRes.StatusCode, rtStatus)
	}

	rtOpRes := authReq(http.MethodPost, "/upg-console/api/v2/runtime/operations", `{"requestId":"`+strings.Repeat("c", 32)+`","action":"restart"}`)
	_ = rtOpRes.Body.Close()
	if rtOpRes.StatusCode != http.StatusOK || rt.restartCalls != 1 || rt.lastMutationReq.ExpectedRuntimeGeneration != 19 {
		t.Fatalf("POST runtime/operations status=%d restartCalls=%d fencedGen=%d, want 200/1/19", rtOpRes.StatusCode, rt.restartCalls, rt.lastMutationReq.ExpectedRuntimeGeneration)
	}
	rtGetOpRes := authReq(http.MethodGet, "/upg-console/api/v2/runtime/operations/"+strings.Repeat("c", 32), "")
	_ = rtGetOpRes.Body.Close()
	if rtGetOpRes.StatusCode != http.StatusOK {
		t.Fatalf("GET runtime/operations/%s status = %d, want 200", strings.Repeat("c", 32), rtGetOpRes.StatusCode)
	}
	artRes := authReq(http.MethodGet, "/upg-console/api/v2/runtime/artifact", "")
	_ = artRes.Body.Close()
	if artRes.StatusCode == http.StatusNotFound {
		t.Fatal("GET runtime/artifact returned 404 on post-upgrade Console")
	}
	chRes := authReq(http.MethodPut, "/upg-console/api/v2/settings/update-channel", `{"channel_preference":"beta"}`)
	_ = chRes.Body.Close()
	if chRes.StatusCode != http.StatusOK {
		t.Fatalf("PUT settings/update-channel status = %d, want 200", chRes.StatusCode)
	}
	chGetRes := authReq(http.MethodGet, "/upg-console/api/v2/settings/update-channel", "")
	var chView struct {
		Preference string `json:"channel_preference"`
	}
	_ = json.NewDecoder(chGetRes.Body).Decode(&chView)
	_ = chGetRes.Body.Close()
	if chGetRes.StatusCode != http.StatusOK || chView.Preference != "beta" {
		t.Fatalf("GET settings/update-channel status=%d body=%+v", chGetRes.StatusCode, chView)
	}

	// 4. Both cpamp/v1/models and cpamp/models present CPA client key and never forward Admin Bearer; real cpamp/status alias resolves; private/unknown bridge & root Gateway paths remain 404.
	for _, modelPath := range []string{"/upg-console/api/v2/cpamp/v1/models", "/upg-console/api/v2/cpamp/models"} {
		modelsRes := authReq(http.MethodGet, modelPath, "")
		modelsBody, _ := io.ReadAll(modelsRes.Body)
		_ = modelsRes.Body.Close()
		if modelsRes.StatusCode != http.StatusOK || sawAdminTokenAtModels.Load() || !strings.Contains(string(modelsBody), `"gpt-5"`) {
			t.Fatalf("%s status=%d sawAdmin=%t body=%s", modelPath, modelsRes.StatusCode, sawAdminTokenAtModels.Load(), string(modelsBody))
		}
	}
	bridgeStatusRes := authReq(http.MethodGet, "/upg-console/api/v2/cpamp/status", "")
	_ = bridgeStatusRes.Body.Close()
	if bridgeStatusRes.StatusCode != http.StatusOK {
		t.Fatalf("GET cpamp/status status = %d, want 200", bridgeStatusRes.StatusCode)
	}
	v8KeysRes := authReq(http.MethodGet, "/upg-console/api/v2/cpamp/v8/management/config/api-keys", "")
	v8KeysBody, _ := io.ReadAll(v8KeysRes.Body)
	_ = v8KeysRes.Body.Close()
	if v8KeysRes.StatusCode != http.StatusOK || v8KeysRes.Header.Get("ETag") != `"upg-etag-1"` || !strings.Contains(string(v8KeysBody), `"claude-upg"`) {
		t.Fatalf("GET cpamp/v8/management/config/api-keys status=%d etag=%q body=%s", v8KeysRes.StatusCode, v8KeysRes.Header.Get("ETag"), string(v8KeysBody))
	}
	v8PutReq, err := http.NewRequest(http.MethodPut, ts.URL+"/upg-console/api/v2/cpamp/v8/management/config/api-keys/claude", strings.NewReader(`[{"name":"claude-upg-2"}]`))
	if err != nil {
		t.Fatal(err)
	}
	v8PutReq.Header.Set("Authorization", "Bearer "+adminKey)
	v8PutReq.Header.Set("Content-Type", "application/json")
	v8PutReq.Header.Set("If-Match", `"upg-etag-1"`)
	v8PutRes, err := http.DefaultClient.Do(v8PutReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = v8PutRes.Body.Close()
	if v8PutRes.StatusCode != http.StatusOK || v8PutRes.Header.Get("ETag") != `"upg-etag-2"` {
		t.Fatalf("PUT cpamp/v8/management/config/api-keys/claude status=%d etag=%q", v8PutRes.StatusCode, v8PutRes.Header.Get("ETag"))
	}

	for _, closed := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/upg-console/api/v2/cpamp/setup"},
		{http.MethodGet, "/upg-console/api/v2/cpamp/setup/status"},
		{http.MethodGet, "/upg-console/api/v2/cpamp/v8/management/config"},
		{http.MethodGet, "/upg-console/api/v2/cpamp/v8/management/config/access/api-keys"},
		{http.MethodGet, "/upg-console/api/v2/cpamp/v8/management/config/api-keys/unknown"},
		{http.MethodPut, "/upg-console/api/v2/cpamp/v8/management/config/api-keys"},
		{http.MethodPatch, "/upg-console/api/v2/cpamp/v8/management/config/api-keys/claude"},
		{http.MethodDelete, "/upg-console/api/v2/cpamp/v8/management/config/api-keys/claude"},
		{http.MethodGet, "/upg-console/api/v2/cpamp/v0/management/collector/restart"},
		{http.MethodGet, "/upg-console/api/v2/cpamp/v0/management/%2e%2e/status"},
		{http.MethodGet, "/upg-console/api/v2/cpamp/internal/routing/fresh-authority"},
		{http.MethodGet, "/upg-console/api/v2/cpamp/unlisted-internal-route"},
		{http.MethodGet, "/v1/models"},
	} {
		res := authReq(closed.method, closed.path, "")
		_ = res.Body.Close()
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404", closed.method, closed.path, res.StatusCode)
		}
	}
}

func TestCompletedExternalUpgradeMountsUsageRebindAndFencesBridge(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	oldKey := strings.Repeat("a", 64)
	newKey := strings.Repeat("e", 64)
	var activeKey atomic.Value
	activeKey.Store(oldKey)
	var statsEnabled atomic.Bool
	var pluginHits atomic.Int32

	extCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v8/management/config/plugins") {
			pluginHits.Add(1)
		}
		if r.Header.Get("Authorization") != "Bearer "+activeKey.Load().(string) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.55")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/config":
			_, _ = w.Write([]byte(`{"port":8317}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config":
			_, _ = w.Write([]byte(`{"config-version":8,"access":{"api-keys":[]}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/usage-statistics-enabled":
			_ = json.NewEncoder(w).Encode(map[string]any{"usage-statistics-enabled": statsEnabled.Load()})
		case r.Method == http.MethodPut && r.URL.Path == "/v0/management/usage-statistics-enabled":
			statsEnabled.Store(true)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config/api-keys/claude":
			w.Header().Set("ETag", `"ext-upg-etag-1"`)
			_, _ = w.Write([]byte(`[{"name":"claude-ext-upg"}]`))
		case r.Method == http.MethodPut && r.URL.Path == "/v8/management/config/api-keys/claude":
			if r.Header.Get("If-Match") != `"ext-upg-etag-1"` {
				w.Header().Set("ETag", `"ext-upg-etag-1"`)
				w.WriteHeader(http.StatusPreconditionFailed)
				_, _ = w.Write([]byte(`{"error":"precondition_failed"}`))
				return
			}
			w.Header().Set("ETag", `"ext-upg-etag-2"`)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer extCPA.Close()

	st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPA.URL)
	if err := st.SaveManagerConfig(context.Background(), model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{
			CPABaseURL:    extCPA.URL,
			ManagementKey: oldKey,
		},
	}); err != nil {
		t.Fatal(err)
	}
	serverApp := httpapi.New(cfg, st, nil)
	if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting(External resources): %v", err)
	}
	ts := httptest.NewServer(serverApp.Handler())
	defer ts.Close()

	authReq := func(method, path, body string) *http.Response {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, ts.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+adminKey)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}

	// 1. Plugin runtime and payload rule writes are refused locally on External without contacting External CPA.
	zeroBaseline := strings.Repeat("0", 64)
	plPost := authReq(http.MethodPost, "/ext-console/api/v2/plugins/runtime/groups/plugins", `{"requestId":"`+strings.Repeat("a", 32)+`","expected":{"generation":"2000","baseline":"`+zeroBaseline+`"},"edits":[{"path":"plugins.enabled","action":"set","value":true}]}`)
	_ = plPost.Body.Close()
	if plPost.StatusCode != http.StatusConflict || pluginHits.Load() != 0 {
		t.Fatalf("External POST plugins/runtime/groups/plugins status=%d pluginHits=%d, want 409 and 0", plPost.StatusCode, pluginHits.Load())
	}
	rulePost := authReq(http.MethodPost, "/ext-console/api/v2/gateway/config/rules/default", `{"requestId":"`+strings.Repeat("b", 32)+`","expected":{"generation":"2000","baseline":"`+zeroBaseline+`"},"rules":[]}`)
	_ = rulePost.Body.Close()
	if rulePost.StatusCode != http.StatusConflict {
		t.Fatalf("External POST gateway/config/rules/default status = %d, want 409", rulePost.StatusCode)
	}

	// 2. External usage GET & POST enable statistics on External CPA.
	usageGet := authReq(http.MethodGet, "/ext-console/api/v2/external/usage", "")
	var usageBefore externalUsageState
	_ = json.NewDecoder(usageGet.Body).Decode(&usageBefore)
	_ = usageGet.Body.Close()
	if usageGet.StatusCode != http.StatusOK || usageBefore.StatisticsEnabled {
		t.Fatalf("GET external/usage status=%d before=%+v", usageGet.StatusCode, usageBefore)
	}
	usagePost := authReq(http.MethodPost, "/ext-console/api/v2/external/usage", `{"requestId":"`+strings.Repeat("c", 32)+`","confirmEnableStatistics":true}`)
	var usageAfter externalUsageState
	_ = json.NewDecoder(usagePost.Body).Decode(&usageAfter)
	_ = usagePost.Body.Close()
	if usagePost.StatusCode != http.StatusOK || !usageAfter.StatisticsEnabled || !statsEnabled.Load() {
		t.Fatalf("POST external/usage status=%d after=%+v cpaEnabled=%t", usagePost.StatusCode, usageAfter, statsEnabled.Load())
	}

	// 3. Bridge fences External replaced writes (409 external_legacy_write_replaced).
	putCfg := authReq(http.MethodPut, "/ext-console/api/v2/cpamp/v0/management/config.yaml", `{}`)
	var replacedErr struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(putCfg.Body).Decode(&replacedErr)
	_ = putCfg.Body.Close()
	if putCfg.StatusCode != http.StatusConflict || replacedErr.Code != "external_legacy_write_replaced" {
		t.Fatalf("External PUT cpamp/v0/management/config.yaml status=%d body=%+v, want 409/external_legacy_write_replaced", putCfg.StatusCode, replacedErr)
	}

	// 4. Rotate External CPA key -> 401 records rejection, blanks access-info, and POST external/rebind aborts unattempted key journal op & restores authority.
	activeKey.Store(newKey)
	gwRejected := authReq(http.MethodGet, "/ext-console/api/v2/gateway/config", "")
	var rejectedErr struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(gwRejected.Body).Decode(&rejectedErr)
	_ = gwRejected.Body.Close()
	if gwRejected.StatusCode != http.StatusConflict || rejectedErr.Code != freshconfig.ReasonManagementRejected {
		t.Fatalf("expected 409 %s on gateway/config, got status=%d body=%+v", freshconfig.ReasonManagementRejected, gwRejected.StatusCode, rejectedErr)
	}

	fencedInfoRes := authReq(http.MethodGet, "/ext-console/api/v2/api-keys/access-info", "")
	var fencedInfo keyAccessInfo
	_ = json.NewDecoder(fencedInfoRes.Body).Decode(&fencedInfo)
	_ = fencedInfoRes.Body.Close()
	if fencedInfo.Ready || fencedInfo.ExternalBaseURL != "" {
		t.Fatalf("rejected External credential did not fence access-info: %+v", fencedInfo)
	}

	extIdentity := freshkeyapply.ExternalIdentity(strings.TrimRight(extCPA.URL, "/"))
	kRepo := keyrepo.New(st.SQLDB())
	if err := kRepo.BeginOperation(context.Background(), keyrepo.Operation{
		ID:          strings.Repeat("d", 32),
		TargetID:    strings.Repeat("e", 32),
		Mode:        "add",
		RunID:       extIdentity,
		Generation:  extIdentity,
		Revision:    1,
		Outcome:     keyrepo.OutcomePending,
		Steps:       []keyrepo.KeyStep{{Name: "add", State: "unattempted"}},
		CreatedAtMS: 2001,
	}); err != nil {
		t.Fatalf("seed unattempted key operation: %v", err)
	}

	rebindRes := authReq(http.MethodPost, "/ext-console/api/v2/external/rebind", `{"requestId":"`+strings.Repeat("f", 32)+`","managementKey":"`+newKey+`"}`)
	var rebindBody struct {
		Status     string `json:"status"`
		Generation uint64 `json:"generation"`
	}
	_ = json.NewDecoder(rebindRes.Body).Decode(&rebindBody)
	_ = rebindRes.Body.Close()
	if rebindRes.StatusCode != http.StatusOK || rebindBody.Status != "rebound" || rebindBody.Generation == 0 || rebindBody.Generation == 2000 {
		t.Fatalf("POST external/rebind status=%d body=%+v", rebindRes.StatusCode, rebindBody)
	}
	abortedOp, err := kRepo.Operation(context.Background(), strings.Repeat("d", 32))
	if err != nil || abortedOp == nil || abortedOp.Outcome != keyrepo.OutcomeAborted || !abortedOp.IsTerminal() {
		t.Fatalf("expected unattempted key operation to abort on rebind, got op=%+v err=%v", abortedOp, err)
	}

	restoredInfoRes := authReq(http.MethodGet, "/ext-console/api/v2/api-keys/access-info", "")
	var restoredInfo keyAccessInfo
	_ = json.NewDecoder(restoredInfoRes.Body).Decode(&restoredInfo)
	_ = restoredInfoRes.Body.Close()
	if !restoredInfo.Ready || restoredInfo.ExternalBaseURL != extCPA.URL {
		t.Fatalf("rebound External access-info was not restored: %+v", restoredInfo)
	}

	extV8Get := authReq(http.MethodGet, "/ext-console/api/v2/cpamp/v8/management/config/api-keys/claude", "")
	_ = extV8Get.Body.Close()
	if extV8Get.StatusCode != http.StatusOK || extV8Get.Header.Get("ETag") != `"ext-upg-etag-1"` {
		t.Fatalf("GET ext cpamp/v8/management/config/api-keys/claude status=%d etag=%q", extV8Get.StatusCode, extV8Get.Header.Get("ETag"))
	}
	extV8PutReq, err := http.NewRequest(http.MethodPut, ts.URL+"/ext-console/api/v2/cpamp/v8/management/config/api-keys/claude", strings.NewReader(`[{"name":"claude-ext-upg-2"}]`))
	if err != nil {
		t.Fatal(err)
	}
	extV8PutReq.Header.Set("Authorization", "Bearer "+adminKey)
	extV8PutReq.Header.Set("Content-Type", "application/json")
	extV8PutReq.Header.Set("If-Match", `"ext-upg-etag-1"`)
	extV8PutRes, err := http.DefaultClient.Do(extV8PutReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = extV8PutRes.Body.Close()
	if extV8PutRes.StatusCode != http.StatusOK || extV8PutRes.Header.Get("ETag") != `"ext-upg-etag-2"` {
		t.Fatalf("PUT ext cpamp/v8/management/config/api-keys/claude status=%d etag=%q", extV8PutRes.StatusCode, extV8PutRes.Header.Get("ETag"))
	}
}

func TestCompletedExternalUpgradeRebindFencesAttemptedKeyOperation(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	oldKey := strings.Repeat("a", 64)
	newKey := strings.Repeat("f", 64)
	extCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+newKey && r.Header.Get("Authorization") != "Bearer "+oldKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.55")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"port":8317}`))
	}))
	defer extCPA.Close()

	st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPA.URL)
	extIdentity := freshkeyapply.ExternalIdentity(strings.TrimRight(extCPA.URL, "/"))
	if err := keyrepo.New(st.SQLDB()).BeginOperation(context.Background(), keyrepo.Operation{
		ID:          strings.Repeat("a", 32),
		TargetID:    strings.Repeat("b", 32),
		Mode:        "add",
		RunID:       extIdentity,
		Generation:  extIdentity,
		Revision:    1,
		Outcome:     keyrepo.OutcomePending,
		Steps:       []keyrepo.KeyStep{{Name: "add", State: "attempted"}},
		CreatedAtMS: 2001,
	}); err != nil {
		t.Fatalf("seed attempted key operation: %v", err)
	}

	serverApp := httpapi.New(cfg, st, nil)
	if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(serverApp.Handler())
	defer ts.Close()

	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/ext-console/api/v2/external/rebind", strings.NewReader(`{"requestId":"`+strings.Repeat("c", 32)+`","managementKey":"`+newKey+`"}`))
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var errBody struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(res.Body).Decode(&errBody)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusConflict || errBody.Code != "external_rebind_refused" {
		t.Fatalf("attempted key journal did not fence External rebind: status=%d body=%+v", res.StatusCode, errBody)
	}
}

func TestCompletedManagedMissingInferenceRejectsSharedEntryFlag(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	st, cfg, _ := seedCompletedManagedUpgradeStore(t, "/upg-console", "")
	if _, err := st.SQLDB().Exec(`update upgrade_operations set inference_base_path = '', inference_committed = 0, shared_entry = 1 where id = 1`); err != nil {
		t.Fatal(err)
	}
	serverApp := httpapi.New(cfg, st, nil)
	if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, nil); err == nil {
		t.Fatal("expected attachCompletedUpgradeRouting to reject missing inference with sharedEntry=true")
	}
}

func TestCompletedManagedUpgradeSeedsDesiredRunningOnceAndExternalFencesReconciler(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	ctx := context.Background()
	qualifiedArtifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
	if err != nil {
		t.Fatalf("QualifiedUpgradeAdoptionArtifactID: %v", err)
	}

	st, cfg, _ := seedCompletedManagedUpgradeStore(t, "/upg-console", "/upg-api")
	cfg.RuntimeURL = "http://127.0.0.1:9081"
	tokenFile := filepath.Join(t.TempDir(), "runtime.token")
	if err := os.WriteFile(tokenFile, []byte("runtime-token-fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeTokenFile = tokenFile

	// Mirror real 1.x -> v2 Managed upgrade: legacy connection rows exist, so
	// bootstrapservice.Run leaves runtime_desired_v1 unseeded.
	if _, err := bootstrapservice.Run(ctx, cfg, st, false); err != nil {
		t.Fatalf("bootstrapservice.Run: %v", err)
	}
	if _, found, err := st.LoadEmbeddedRuntimeDesiredState(ctx); err != nil || found {
		t.Fatalf("pre-attach desired state = (found=%v, err=%v), want absent", found, err)
	}

	keyBytes, _, err := security.LoadOrCreateDataKey("", cfg.DataKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	sealedDestKey, err := protector.ProtectString(strings.Repeat("d", 64))
	if err != nil {
		t.Fatal(err)
	}
	sourceIdentity := "external:sha256:" + strings.Repeat("c", 64)
	pluginsInit := false
	destPayload, _ := json.Marshal(upgradeDestinationRecord{
		BaseURL:           "http://127.0.0.1:8317",
		URLProved:         true,
		ManagementKey:     sealedDestKey,
		ServerFingerprint: "external:sha256:" + strings.Repeat("b", 64),
		Attempt:           1,
		OperationID:       upgradeManagedDestinationOperationID(sourceIdentity, 1),
		Intent: upgradeDestinationIntent{
			ExpectedRuntimeIdentity:   "embedded-default",
			ExpectedRuntimeGeneration: 7,
			ExpectedActiveArtifactID:  qualifiedArtifact,
			SourceIdentity:            sourceIdentity,
			SourceClass:               upgradeManagedDestinationClass,
			APIKeys:                   []string{"sk-legacy-1"},
			PluginsEnabled:            &pluginsInit,
		},
	})
	if _, err := st.SQLDB().Exec(`insert into settings(key, value, updated_at_ms) values(?, ?, ?)`, upgradeImportDestinationSettingKey, string(destPayload), time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}

	rt := &upgradeCanonicalTestRuntime{
		status: model.RuntimeObservedStatus{
			Identity:           "embedded-default",
			Generation:         21,
			ProtocolVersion:    "v1",
			State:              model.RuntimeStateOffline,
			Capabilities:       model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStop, model.RuntimeCapabilityRestart},
			CPAObservedVersion: "8.0.55",
			Recovery:           &model.RuntimeRecoveryObservation{State: model.RuntimeRecoveryStateInactive},
			ActiveGatewayArtifact: &model.ActiveGatewayArtifact{
				Engine:     "cpa",
				ArtifactID: qualifiedArtifact,
				Version:    "8.0.55",
			},
		},
	}

	var desiredStore runtimeservice.DesiredStateStore = st
	serverApp := httpapi.New(cfg, st, nil)
	if err := attachCompletedUpgradeRouting(ctx, cfg, st, serverApp, func(context.Context) bool { return true }, upgradeCanonicalDependencies{
		Protector:       protector,
		Runtime:         rt,
		DesiredStateOut: &desiredStore,
	}); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting(Managed): %v", err)
	}

	seeded, found, err := desiredStore.LoadEmbeddedRuntimeDesiredState(ctx)
	if err != nil || !found || seeded.DesiredLifecycle != model.EmbeddedRuntimeDesiredRunning {
		t.Fatalf("Managed post-attach desired state = (%+v, found=%v, err=%v), want running", seeded, found, err)
	}
	reconciler := runtimeservice.NewReconciler(rt, desiredStore, time.Second, nil)
	if err := reconciler.ReconcileOnce(ctx); err != nil {
		t.Fatalf("ReconcileOnce(Managed offline -> Start): %v", err)
	}
	if rt.startCalls != 1 {
		t.Fatalf("ReconcileOnce Start calls = %d, want 1", rt.startCalls)
	}

	// Explicit operator Stop must survive subsequent startup attach without re-seeding Running.
	if _, err := st.SetEmbeddedRuntimeDesiredLifecycle(ctx, model.EmbeddedRuntimeDesiredStopped); err != nil {
		t.Fatal(err)
	}
	var secondDesiredStore runtimeservice.DesiredStateStore = st
	serverApp2 := httpapi.New(cfg, st, nil)
	if err := attachCompletedUpgradeRouting(ctx, cfg, st, serverApp2, func(context.Context) bool { return true }, upgradeCanonicalDependencies{
		Protector:       protector,
		Runtime:         rt,
		DesiredStateOut: &secondDesiredStore,
	}); err != nil {
		t.Fatalf("second attachCompletedUpgradeRouting(Managed): %v", err)
	}
	afterStop, found, err := secondDesiredStore.LoadEmbeddedRuntimeDesiredState(ctx)
	if err != nil || !found || afterStop.DesiredLifecycle != model.EmbeddedRuntimeDesiredStopped {
		t.Fatalf("Managed desired state after explicit stop + restart = (%+v, found=%v, err=%v), want stopped", afterStop, found, err)
	}

	// Completed External upgrade must never expose Embedded desired state to Reconciler,
	// even if a stale runtime_desired_v1 row exists in SQLite.
	extSt, extCfg, _ := seedCompletedExternalUpgradeStore(t, "/ext-console", "http://127.0.0.1:19317")
	if _, err := extSt.SetEmbeddedRuntimeDesiredLifecycle(ctx, model.EmbeddedRuntimeDesiredRunning); err != nil {
		t.Fatal(err)
	}
	var extDesiredStore runtimeservice.DesiredStateStore = extSt
	extApp := httpapi.New(extCfg, extSt, nil)
	if err := attachCompletedUpgradeRouting(ctx, extCfg, extSt, extApp, nil, upgradeCanonicalDependencies{
		DesiredStateOut: &extDesiredStore,
	}); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting(External): %v", err)
	}
	if _, extFound, extErr := extDesiredStore.LoadEmbeddedRuntimeDesiredState(ctx); extErr != nil || extFound {
		t.Fatalf("External desired state = (found=%v, err=%v), want absent", extFound, extErr)
	}
}

type syncCanonicalLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncCanonicalLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncCanonicalLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestCompletedUpgradeFastInitialIngressConvergence(t *testing.T) {
	prevDist := testUpgradeFullWebDist
	testUpgradeFullWebDist = fstest.MapFS{
		"index.html":               {Data: []byte("<!doctype html><html><head><script type=\"module\" src=\"./assets/index-AbCd1234.js\"></script><script>__CPAMP_ROUTING_CONTEXT__</script></head><body><div id=\"root\"></div></body></html>")},
		"assets/index-AbCd1234.js": {Data: []byte("console.log('cpamp-upgrade-test');")},
	}
	t.Cleanup(func() { testUpgradeFullWebDist = prevDist })

	st, cfg, _ := seedCompletedManagedUpgradeStore(t, "/cpamp-admin", "/cpamp-api")
	serverApp := httpapi.New(cfg, st, nil)
	mgrTS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serverApp.Handler().ServeHTTP(w, r)
	}))
	defer mgrTS.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	controlAddr := ln.Addr().String()
	_ = ln.Close()

	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "http://"+controlAddr)
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	var logBuf syncCanonicalLogBuffer
	prevLogOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(prevLogOut) })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	if err := attachCompletedUpgradeRouting(ctx, cfg, st, serverApp, func(context.Context) bool { return true }); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting: %v", err)
	}
	if strings.Contains(logBuf.String(), "publish upgraded routing state to ingress") {
		t.Fatalf("unexpected startup error logged while Ingress was not yet listening: %s", logBuf.String())
	}

	var (
		stateMu        sync.Mutex
		currentState   freshrouting.State
		publishedOnce  sync.Once
		publishedReady = make(chan freshrouting.State, 1)
	)
	controlHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != freshrouting.InternalPath {
			http.NotFound(w, r)
			return
		}
		switch r.Method {
		case http.MethodGet:
			stateMu.Lock()
			cur := currentState
			stateMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(cur)
		case http.MethodPut:
			var requested freshrouting.State
			if decErr := json.NewDecoder(r.Body).Decode(&requested); decErr != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			authRes, authErr := mgrTS.Client().Get(mgrTS.URL + freshrouting.AuthorityPath)
			if authErr != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			var authReply freshAuthorityReply
			decErr := json.NewDecoder(authRes.Body).Decode(&authReply)
			_ = authRes.Body.Close()
			if authRes.StatusCode != http.StatusOK || decErr != nil || authReply.State != requested {
				w.WriteHeader(http.StatusConflict)
				return
			}
			stateMu.Lock()
			currentState = requested
			stateMu.Unlock()
			publishedOnce.Do(func() {
				publishedReady <- requested
			})
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(requested)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	time.Sleep(80 * time.Millisecond)
	controlLn, err := net.Listen("tcp", controlAddr)
	if err != nil {
		t.Fatalf("bind delayed Ingress control listener on %s: %v", controlAddr, err)
	}
	controlSrv := &http.Server{Handler: controlHandler}
	defer func() { _ = controlSrv.Close() }()
	ingressStartedAt := time.Now()
	go func() { _ = controlSrv.Serve(controlLn) }()

	select {
	case got := <-publishedReady:
		elapsed := time.Since(ingressStartedAt)
		if elapsed >= 200*time.Millisecond {
			t.Fatalf("route publication took %s after Ingress started, want < 200ms", elapsed)
		}
		if got.Target != "/cpamp-admin" || got.InferenceTarget != "/cpamp-api" || !got.Committed || !got.InferenceCommitted {
			t.Fatalf("unexpected published routing state: %+v", got)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for fast initial Ingress route publication")
	}
}

func TestCompletedUpgradeCrossSiteMutationEnforcesSchemeAndPublicOrigin(t *testing.T) {
	t.Run("configured_https_public_origin_rejects_http_scheme_and_allows_https", func(t *testing.T) {
		t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
		t.Setenv("CPAMP_PUBLIC_ORIGIN", "https://example.com")
		st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/upg-console", "")
		serverApp := httpapi.New(cfg, st, nil)
		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, func(context.Context) bool { return true }); err != nil {
			t.Fatalf("attachCompletedUpgradeRouting: %v", err)
		}
		ts := httptest.NewServer(serverApp.Handler())
		defer ts.Close()

		// Cross-scheme Origin: http://example.com must be rejected with 403 origin_rejected.
		crossSchemeReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/upg-console/api/v2/installation/access/completion", bytes.NewBufferString(`{"inferenceBasePath":"/upg-api","sharedEntry":false}`))
		crossSchemeReq.Host = "example.com"
		crossSchemeReq.Header.Set("Authorization", "Bearer "+adminKey)
		crossSchemeReq.Header.Set("Origin", "http://example.com")
		crossSchemeRes, err := http.DefaultClient.Do(crossSchemeReq)
		if err != nil {
			t.Fatalf("cross-scheme POST completion: %v", err)
		}
		crossBody, _ := io.ReadAll(crossSchemeRes.Body)
		_ = crossSchemeRes.Body.Close()
		if crossSchemeRes.StatusCode != http.StatusForbidden || !strings.Contains(string(crossBody), "origin_rejected") {
			t.Fatalf("cross-scheme POST completion = %d (%s), want 403 origin_rejected", crossSchemeRes.StatusCode, string(crossBody))
		}

		// Same-origin Origin: https://example.com passes the origin check and reaches driver availability (503).
		sameOriginReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/upg-console/api/v2/installation/access/completion", bytes.NewBufferString(`{"inferenceBasePath":"/upg-api","sharedEntry":false}`))
		sameOriginReq.Host = "example.com"
		sameOriginReq.Header.Set("Authorization", "Bearer "+adminKey)
		sameOriginReq.Header.Set("Origin", "https://example.com")
		sameOriginRes, err := http.DefaultClient.Do(sameOriginReq)
		if err != nil {
			t.Fatalf("same-origin POST completion: %v", err)
		}
		sameBody, _ := io.ReadAll(sameOriginRes.Body)
		_ = sameOriginRes.Body.Close()
		if sameOriginRes.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("same-origin POST completion without driver = %d (%s), want 503", sameOriginRes.StatusCode, string(sameBody))
		}
	})

	t.Run("end_to_end_completion_allows_matching_public_origin_and_rejects_cross_scheme", func(t *testing.T) {
		st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/upg-console", "")
		var mgrURL atomic.Value
		mgrURL.Store("")
		ingress := newUpgradeIngressCallbackHarness(t, func() string { return mgrURL.Load().(string) })
		t.Setenv("CPAMP_INGRESS_CONTROL_URL", ingress.control.URL)
		t.Setenv("CPAMP_PUBLIC_ORIGIN", ingress.public.URL)

		serverApp := httpapi.New(cfg, st, nil)
		var handler atomic.Value
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if h, ok := handler.Load().(http.Handler); ok && h != nil {
				h.ServeHTTP(w, r)
				return
			}
			serverApp.Handler().ServeHTTP(w, r)
		}))
		defer ts.Close()
		mgrURL.Store(ts.URL)

		if err := attachCompletedUpgradeRouting(context.Background(), cfg, st, serverApp, func(context.Context) bool { return true }); err != nil {
			t.Fatalf("attachCompletedUpgradeRouting: %v", err)
		}
		handler.Store(serverApp.Handler())

		crossSchemeOrigin := strings.Replace(ingress.public.URL, "http://", "https://", 1)
		crossReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/upg-console/api/v2/installation/access/completion", bytes.NewBufferString(`{"inferenceBasePath":"/upg-api","sharedEntry":false}`))
		crossReq.Host = strings.TrimPrefix(ingress.public.URL, "http://")
		crossReq.Header.Set("Authorization", "Bearer "+adminKey)
		crossReq.Header.Set("Origin", crossSchemeOrigin)
		crossRes, err := http.DefaultClient.Do(crossReq)
		if err != nil {
			t.Fatalf("cross-scheme POST completion: %v", err)
		}
		crossBody, _ := io.ReadAll(crossRes.Body)
		_ = crossRes.Body.Close()
		if crossRes.StatusCode != http.StatusForbidden || !strings.Contains(string(crossBody), "origin_rejected") {
			t.Fatalf("cross-scheme POST completion = %d (%s), want 403 origin_rejected", crossRes.StatusCode, string(crossBody))
		}

		sameReq, _ := http.NewRequest(http.MethodPost, ts.URL+"/upg-console/api/v2/installation/access/completion", bytes.NewBufferString(`{"inferenceBasePath":"/upg-api","sharedEntry":false}`))
		sameReq.Header.Set("Authorization", "Bearer "+adminKey)
		sameReq.Header.Set("Origin", ingress.public.URL)
		sameRes, err := http.DefaultClient.Do(sameReq)
		if err != nil {
			t.Fatalf("same-origin POST completion: %v", err)
		}
		sameBody, _ := io.ReadAll(sameRes.Body)
		_ = sameRes.Body.Close()
		if sameRes.StatusCode != http.StatusOK {
			t.Fatalf("same-origin POST completion = %d (%s), want 200", sameRes.StatusCode, string(sameBody))
		}
	})

	t.Run("fallback_without_public_origin_enforces_request_scheme", func(t *testing.T) {
		u := &upgradeCanonicalServer{}
		httpReq := httptest.NewRequest(http.MethodPost, "http://example.com/upg-console/api/v2/installation/access/completion", nil)
		httpReq.Header.Set("Origin", "https://example.com")
		if !u.rejectUpgradeCrossSiteMutation(httpReq) {
			t.Fatal("expected http request with https Origin to be rejected")
		}
		httpSame := httptest.NewRequest(http.MethodPost, "http://example.com/upg-console/api/v2/installation/access/completion", nil)
		httpSame.Header.Set("Origin", "http://example.com")
		if u.rejectUpgradeCrossSiteMutation(httpSame) {
			t.Fatal("expected same-scheme same-host Origin to be allowed")
		}
	})
}

func TestCompletedManagedUpgradeFencesForeignCPAConnectionPutAndReenablesCollection(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	managedSecret := strings.Repeat("d", 64)
	var statsEnabled atomic.Bool
	statsEnabled.Store(true)
	managedCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+managedSecret {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.64")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/config":
			_, _ = w.Write([]byte(`{"port":8317}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v8/management/config":
			_, _ = w.Write([]byte(`{"config-version":8,"access":{"api-keys":["sk-managed-1"]}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/usage-statistics-enabled":
			_ = json.NewEncoder(w).Encode(map[string]any{"usage-statistics-enabled": statsEnabled.Load()})
		case r.Method == http.MethodPut && r.URL.Path == "/v0/management/usage-statistics-enabled":
			statsEnabled.Store(true)
			_, _ = w.Write([]byte(`{"ok":true}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/usage":
			_, _ = w.Write([]byte(`{"usage":{}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v0/management/usage-queue":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer managedCPA.Close()

	ctx := context.Background()
	qualifiedArtifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
	if err != nil {
		t.Fatal(err)
	}
	st, cfg, adminKey := seedCompletedManagedUpgradeStore(t, "/upg-console", "/upg-api")
	cfg.CollectorMode = "http"
	cfg.PollInterval = 25 * time.Millisecond

	enabled := true
	managedURL := strings.TrimRight(managedCPA.URL, "/")
	if err := st.SaveManagerConfigAndSetup(ctx, model.ManagerConfig{
		CPAConnection: model.ManagerCPAConnectionConfig{
			CPABaseURL:    managedURL,
			ManagementKey: managedSecret,
		},
		Collector: model.ManagerCollectorConfig{
			Enabled:        &enabled,
			CollectorMode:  "http",
			Queue:          "managed-q",
			PopSide:        "left",
			PollIntervalMS: 25,
		},
	}, model.Setup{
		CPAUpstreamURL: managedURL,
		ManagementKey:  managedSecret,
		Queue:          "managed-q",
		PopSide:        "left",
	}); err != nil {
		t.Fatal(err)
	}

	keyBytes, _, err := security.LoadOrCreateDataKey("", cfg.DataKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	sealedDestKey, err := protector.ProtectString(managedSecret)
	if err != nil {
		t.Fatal(err)
	}
	sourceIdentity := "external:sha256:" + strings.Repeat("c", 64)
	pluginsInit := false
	destPayload, _ := json.Marshal(upgradeDestinationRecord{
		BaseURL:           managedURL,
		URLProved:         true,
		ManagementKey:     sealedDestKey,
		ServerFingerprint: "external:sha256:" + strings.Repeat("b", 64),
		Attempt:           1,
		OperationID:       upgradeManagedDestinationOperationID(sourceIdentity, 1),
		Intent: upgradeDestinationIntent{
			ExpectedRuntimeIdentity:   "embedded-default",
			ExpectedRuntimeGeneration: 7,
			ExpectedActiveArtifactID:  qualifiedArtifact,
			SourceIdentity:            sourceIdentity,
			SourceClass:               upgradeManagedDestinationClass,
			APIKeys:                   []string{"sk-managed-1"},
			PluginsEnabled:            &pluginsInit,
		},
	})
	if _, err := st.SQLDB().Exec(`insert into settings(key, value, updated_at_ms) values(?, ?, ?)`, upgradeImportDestinationSettingKey, string(destPayload), time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}

	rt := &upgradeCanonicalTestRuntime{
		status: model.RuntimeObservedStatus{
			Identity:           "embedded-default",
			Generation:         21,
			ProtocolVersion:    "v1",
			State:              model.RuntimeStateReady,
			Capabilities:       model.RuntimeCapabilities{model.RuntimeCapabilityStart, model.RuntimeCapabilityStop, model.RuntimeCapabilityRestart},
			CPAObservedVersion: "8.0.64",
			Recovery:           &model.RuntimeRecoveryObservation{State: model.RuntimeRecoveryStateInactive},
			ActiveGatewayArtifact: &model.ActiveGatewayArtifact{
				Engine:     "cpa",
				ArtifactID: qualifiedArtifact,
				Version:    "8.0.64",
			},
		},
	}

	manager := collector.NewManager(cfg, st)
	t.Cleanup(func() { _ = manager.StopAndWait(context.Background()) })
	serverApp := httpapi.New(cfg, st, manager)
	ws, closeWS, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
		DB:        st.SQLDB(),
		Protector: protector,
		Runtime:   rt,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeWS)

	var canonical *upgradeCanonicalServer
	if err := attachCompletedUpgradeRouting(ctx, cfg, st, serverApp, ws.gatewayAdmitted, upgradeCanonicalDependencies{
		Protector:    protector,
		Workspace:    ws,
		Runtime:      rt,
		CanonicalOut: &canonical,
	}); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting: %v", err)
	}

	callManaged := func(method, path, body string) (int, string) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Authorization", "Bearer "+adminKey)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		serverApp.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}

	// 1. PUT /cpamp/config and /cpamp/usage-service/config carrying foreign cpaConnection —
	//    including extra top-level "cpaConnection":123 and sequential duplicate "cpaConnection":{...},"cpaConnection":null —
	//    are rejected with 409 managed_connection_immutable and never mutate stored connection or collector state.
	foreignBodies := []string{
		`{"config":{"cpaConnection":{"cpaBaseUrl":"http://127.0.0.1:1","managementKey":"` + managedSecret + `"},"collector":{"enabled":false,"batchSize":50}}}`,
		`{"config":{"cpaConnection":{"cpaBaseUrl":"` + managedURL + `","managementKey":"` + strings.Repeat("f", 64) + `"},"collector":{"enabled":false}}}`,
		`{"cpaConnection":123,"config":{"cpaConnection":{"cpaBaseUrl":"http://127.0.0.1:1","managementKey":"` + managedSecret + `"},"collector":{"enabled":false,"batchSize":50}}}`,
		`{"cpaConnection":123,"config":{"cpaConnection":{"cpaBaseUrl":"` + managedURL + `","managementKey":"` + strings.Repeat("f", 64) + `"},"collector":{"enabled":false}}}`,
		`{"config":{"cpaConnection":{"cpaBaseUrl":"http://127.0.0.1:1","managementKey":"` + managedSecret + `"},"cpaConnection":null,"collector":{"enabled":false,"batchSize":50}}}`,
		`{"config":{"cpaConnection":{"cpaBaseUrl":"` + managedURL + `","managementKey":"` + strings.Repeat("f", 64) + `"},"cpaConnection":null,"collector":{"enabled":false}}}`,
	}
	for _, targetPath := range []string{"/upg-console/api/v2/cpamp/config", "/upg-console/api/v2/cpamp/usage-service/config"} {
		for i, foreignBody := range foreignBodies {
			code, raw := callManaged(http.MethodPut, targetPath, foreignBody)
			var errResp struct {
				Code string `json:"code"`
			}
			_ = json.Unmarshal([]byte(raw), &errResp)
			if code != http.StatusConflict || errResp.Code != "managed_connection_immutable" {
				t.Fatalf("foreign Managed PUT %s case %d = %d %s, want 409 managed_connection_immutable", targetPath, i, code, raw)
			}
		}
	}
	savedMgr, ok, err := st.LoadManagerConfig(ctx)
	if err != nil || !ok || savedMgr.CPAConnection.CPABaseURL != managedURL || savedMgr.CPAConnection.ManagementKey != managedSecret || savedMgr.Collector.Enabled == nil || !*savedMgr.Collector.Enabled {
		t.Fatalf("LoadManagerConfig after rejected foreign PUT = (%+v, collector.enabled=%v, ok=%v, err=%v), want %q enabled=true", savedMgr.CPAConnection, savedMgr.Collector.Enabled, ok, err, managedURL)
	}
	savedSetup, ok, err := st.LoadSetup(ctx)
	if err != nil || !ok || savedSetup.CPAUpstreamURL != managedURL || savedSetup.ManagementKey != managedSecret {
		t.Fatalf("LoadSetup after rejected foreign PUT = (%+v, ok=%v, err=%v), want %q", savedSetup, ok, err, managedURL)
	}

	// 2. Legitimate Managed PUT omitting cpaConnection succeeds with 200; even if setup drifted beforehand, reconciliation heals both rows.
	if err := st.SaveSetup(ctx, model.Setup{
		CPAUpstreamURL: "http://127.0.0.1:1",
		ManagementKey:  "stale-secret",
		Queue:          "managed-q",
		PopSide:        "left",
	}); err != nil {
		t.Fatal(err)
	}
	for _, targetPath := range []string{"/upg-console/api/v2/cpamp/config", "/upg-console/api/v2/cpamp/usage-service/config"} {
		if code, raw := callManaged(http.MethodPut, targetPath, `{"config":{"collector":{"enabled":false,"batchSize":64,"pollIntervalMs":25,"collectorMode":"http"}}}`); code != http.StatusOK {
			t.Fatalf("legitimate Managed PUT %s(enabled=false) = %d %s, want 200", targetPath, code, raw)
		}
	}
	healedSetup, ok, err := st.LoadSetup(ctx)
	if err != nil || !ok || healedSetup.CPAUpstreamURL != managedURL || healedSetup.ManagementKey != managedSecret {
		t.Fatalf("LoadSetup after Managed reconciliation = (%+v, ok=%v, err=%v), want %q", healedSetup, ok, err, managedURL)
	}

	// 3. Subsequent PATCH /cpamp/config {"enabled": true} cleanly re-enables collection against Managed CPA.
	if code, raw := callManaged(http.MethodPatch, "/upg-console/api/v2/cpamp/config", `{"enabled":true}`); code != http.StatusOK {
		t.Fatalf("PATCH /cpamp/config {\"enabled\":true} = %d %s, want 200", code, raw)
	}
	activeCfg, active := manager.ActiveRuntimeConfig()
	if !active || activeCfg.CPAUpstreamURL != managedURL || activeCfg.Queue != "managed-q" || activeCfg.PopSide != "left" {
		t.Fatalf("ActiveRuntimeConfig after Managed re-enable = (active=%v, %+v), want active on %q (managed-q/left)", active, activeCfg, managedURL)
	}
}

func TestCompletedExternalUpgradeGatewayConfigTargetMovedProjectionAndConcurrentRejection(t *testing.T) {
	t.Setenv("CPAMP_INGRESS_CONTROL_URL", "")
	t.Setenv("CPAMP_PUBLIC_ORIGIN", "")

	extKey := strings.Repeat("a", 64)
	extCPA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+extKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("X-CPA-Version", "v8.0.64")
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/v8/management/config" {
			_, _ = w.Write([]byte(`{"config-version":8,"routing":{"strategy":"round-robin"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer extCPA.Close()

	ctx := context.Background()
	st, cfg, adminKey := seedCompletedExternalUpgradeStore(t, "/ext-console", extCPA.URL)
	keyBytes, _, err := security.LoadOrCreateDataKey("", cfg.DataKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := security.NewProtector(keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	ws, closeWS, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
		DB:        st.SQLDB(),
		Protector: protector,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeWS)

	var canonical *upgradeCanonicalServer
	serverApp := httpapi.New(cfg, st, nil)
	if err := attachCompletedUpgradeRouting(ctx, cfg, st, serverApp, ws.gatewayAdmitted, upgradeCanonicalDependencies{
		Protector:    protector,
		Workspace:    ws,
		CanonicalOut: &canonical,
	}); err != nil {
		t.Fatalf("attachCompletedUpgradeRouting: %v", err)
	}

	// Seed one persisted operation and one unknown operation in upgrade_config_groups_v1.
	reqPersisted := strings.Repeat("1", 32)
	reqUnknown := strings.Repeat("2", 32)
	reqMissing := strings.Repeat("3", 32)
	journalGroups := map[string]*freshstore.ConfigGroupJournal{
		"routing": {
			Latest: reqPersisted,
			Operations: map[string]freshstore.ConfigGroupOperation{
				reqPersisted: {
					RequestID:  reqPersisted,
					Group:      "routing",
					Target:     "group",
					Prefix:     "routing",
					Baseline:   strings.Repeat("0", 64),
					Edits:      []freshstore.ConfigGroupEdit{{Path: "routing.strategy", Action: "set", Value: "round-robin"}},
					State:      "persisted",
					Generation: "2000",
				},
			},
		},
		freshconfig.ConfigSourceGroupID: {
			Latest: reqUnknown,
			Operations: map[string]freshstore.ConfigGroupOperation{
				reqUnknown: {
					RequestID:  reqUnknown,
					Group:      freshconfig.ConfigSourceGroupID,
					Target:     "group",
					Prefix:     "requests",
					Baseline:   strings.Repeat("0", 64),
					Edits:      []freshstore.ConfigGroupEdit{{Path: "requests.retry", Action: "set", Value: 3}},
					State:      "unknown",
					Generation: "2000",
				},
			},
		},
	}
	rawGroups := map[string]json.RawMessage{}
	for k, v := range journalGroups {
		b, _ := json.Marshal(v)
		rawGroups[k] = b
	}
	journalBytes, _ := json.Marshal(upgradeConfigJournalEnvelope{Groups: rawGroups})
	if _, err := st.SQLDB().Exec(`insert into settings(key, value, updated_at_ms) values(?, ?, ?)`, upgradeConfigGroupsSettingsKey, string(journalBytes), time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}

	callExt := func(method, path, body string) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Authorization", "Bearer "+adminKey)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		rec := httptest.NewRecorder()
		serverApp.Handler().ServeHTTP(rec, req)
		return rec.Code, rec.Body.Bytes()
	}

	// Induce ErrExternalTargetMoved by writing a conflicting legacy setup URL.
	if err := st.SaveSetup(ctx, model.Setup{
		CPAUpstreamURL: "http://127.0.0.1:19999",
		ManagementKey:  extKey,
	}); err != nil {
		t.Fatal(err)
	}

	// 1. GET /gateway/config under ErrExternalTargetMoved returns full non-empty groups/fields/families projection.
	code, raw := callExt(http.MethodGet, "/ext-console/api/v2/gateway/config", "")
	if code != http.StatusOK {
		t.Fatalf("GET /gateway/config (target moved) = %d %s, want 200", code, raw)
	}
	var cfgView freshconfig.View
	if err := json.Unmarshal(raw, &cfgView); err != nil {
		t.Fatal(err)
	}
	if cfgView.Capability.Mode != freshconfig.ModeUnavailable || cfgView.Capability.Reason != freshconfig.ReasonTargetMoved {
		t.Fatalf("GET /gateway/config capability = %+v, want unavailable/%s", cfgView.Capability, freshconfig.ReasonTargetMoved)
	}
	if len(cfgView.Groups) == 0 || len(cfgView.Fields) == 0 || len(cfgView.Families) == 0 {
		t.Fatalf("GET /gateway/config returned empty projection under ErrExternalTargetMoved: groups=%d fields=%d families=%d", len(cfgView.Groups), len(cfgView.Fields), len(cfgView.Families))
	}
	code, raw = callExt(http.MethodGet, "/ext-console/api/v2/gateway/config/source", "")
	if code != http.StatusOK {
		t.Fatalf("GET /gateway/config/source (target moved) = %d %s, want 200", code, raw)
	}
	var srcView freshconfig.SourceView
	if err := json.Unmarshal(raw, &srcView); err != nil {
		t.Fatal(err)
	}
	if srcView.Capability.Mode != freshconfig.ModeUnavailable || srcView.Capability.Reason != freshconfig.ReasonTargetMoved || srcView.SourceWritable || srcView.Reason != freshconfig.ReasonTargetMoved {
		t.Fatalf("GET /gateway/config/source under ErrExternalTargetMoved = %+v, want unavailable/%s sourceWritable=false", srcView, freshconfig.ReasonTargetMoved)
	}

	// 2. POST /gateway/config/operations/{id}/reconcile under ErrExternalTargetMoved:
	//    - missing -> 404
	//    - persisted -> 200
	//    - unknown -> 409 external_target_moved with result.operation populated
	if code, raw = callExt(http.MethodPost, "/ext-console/api/v2/gateway/config/operations/"+reqMissing+"/reconcile", `{}`); code != http.StatusNotFound {
		t.Fatalf("reconcile(missing) under ErrExternalTargetMoved = %d %s, want 404", code, raw)
	}
	code, raw = callExt(http.MethodPost, "/ext-console/api/v2/gateway/config/operations/"+reqPersisted+"/reconcile", `{}`)
	if code != http.StatusOK || !strings.Contains(string(raw), reqPersisted) {
		t.Fatalf("reconcile(persisted) under ErrExternalTargetMoved = %d %s, want 200 with operation", code, raw)
	}
	code, raw = callExt(http.MethodPost, "/ext-console/api/v2/gateway/config/operations/"+reqUnknown+"/reconcile", `{}`)
	var unknownReply struct {
		Code   string `json:"code"`
		Result *struct {
			Operation freshstore.ConfigGroupOperation `json:"operation"`
			Effective string                          `json:"effective"`
		} `json:"result"`
	}
	_ = json.Unmarshal(raw, &unknownReply)
	if code != http.StatusConflict || unknownReply.Code != freshconfig.ReasonTargetMoved || unknownReply.Result == nil || unknownReply.Result.Operation.RequestID != reqUnknown {
		t.Fatalf("reconcile(unknown) under ErrExternalTargetMoved = %d %s, want 409 %s with result.operation %s", code, raw, freshconfig.ReasonTargetMoved, reqUnknown)
	}

	// 2b. Degraded branches (GET /gateway/config, GET /gateway/config/source, POST /gateway/config/operations/{id}/reconcile)
	//     must never wait on credentialGate or invert lock order (configService.mu -> credentialGate) when another goroutine holds credentialGate.
	runDegradedHTTPWithOccupiedGate := func(method, path, body, waitStackSymbol string) (int, []byte) {
		t.Helper()
		if err := st.SaveSetup(ctx, model.Setup{
			CPAUpstreamURL: "http://127.0.0.1:19999",
			ManagementKey:  extKey,
		}); err != nil {
			t.Fatal(err)
		}
		canonical.configService.mu.Lock()
		reqCtx, cancelReq := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancelReq()

		type httpResult struct {
			code int
			body []byte
		}
		doneCh := make(chan httpResult, 1)
		go func() {
			var reader io.Reader
			if body != "" {
				reader = strings.NewReader(body)
			}
			req := httptest.NewRequest(method, path, reader).WithContext(reqCtx)
			req.Header.Set("Authorization", "Bearer "+adminKey)
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			serverApp.Handler().ServeHTTP(rec, req)
			doneCh <- httpResult{code: rec.Code, body: rec.Body.Bytes()}
		}()

		// Wait until the HTTP goroutine has exited enterExternalCredentialHold and parked on configService.mu.Lock().
		stackBuf := make([]byte, 1<<18)
		parkDeadline := time.Now().Add(2 * time.Second)
		for {
			n := runtime.Stack(stackBuf, true)
			stacks := string(stackBuf[:n])
			parked := false
			for _, block := range strings.Split(stacks, "\n\n") {
				if strings.Contains(block, waitStackSymbol) && strings.Contains(block, "(*Mutex).Lock") {
					parked = true
					break
				}
			}
			if parked {
				break
			}
			if time.Now().After(parkDeadline) {
				canonical.configService.mu.Unlock()
				t.Fatalf("HTTP %s %s did not park on configService.mu after enterExternalCredentialHold", method, path)
			}
			time.Sleep(time.Millisecond)
		}

		// Simulate another goroutine holding credentialGate (and heal setup so a stale re-probe would hit credentialGate).
		gateAcquired := make(chan struct{})
		releaseHolder := make(chan struct{})
		holderDone := make(chan struct{})
		go func() {
			defer close(holderDone)
			if !ws.credentialGate.acquire(ctx) {
				return
			}
			close(gateAcquired)
			<-releaseHolder
			ws.credentialGate.release()
		}()
		<-gateAcquired
		defer func() {
			close(releaseHolder)
			<-holderDone
		}()

		if err := st.SaveSetup(ctx, model.Setup{
			CPAUpstreamURL: strings.TrimRight(extCPA.URL, "/"),
			ManagementKey:  extKey,
		}); err != nil {
			canonical.configService.mu.Unlock()
			t.Fatal(err)
		}
		canonical.configService.mu.Unlock()

		select {
		case res := <-doneCh:
			return res.code, res.body
		case <-time.After(250 * time.Millisecond):
			t.Fatalf("degraded %s %s blocked on held credentialGate (AB-BA lock order inversion)", method, path)
			return 0, nil
		}
	}

	code, raw = runDegradedHTTPWithOccupiedGate(http.MethodGet, "/ext-console/api/v2/gateway/config", "", "(*upgradeConfigService).readScope")
	if code != http.StatusOK {
		t.Fatalf("degraded GET /gateway/config with held credentialGate = %d %s, want 200", code, raw)
	}
	cfgView = freshconfig.View{}
	if err := json.Unmarshal(raw, &cfgView); err != nil {
		t.Fatal(err)
	}
	if cfgView.Capability.Mode != freshconfig.ModeUnavailable || cfgView.Capability.Reason != freshconfig.ReasonTargetMoved || len(cfgView.Groups) == 0 || len(cfgView.Fields) == 0 || len(cfgView.Families) == 0 {
		t.Fatalf("degraded GET /gateway/config with held credentialGate = %+v (groups=%d fields=%d families=%d)", cfgView.Capability, len(cfgView.Groups), len(cfgView.Fields), len(cfgView.Families))
	}

	code, raw = runDegradedHTTPWithOccupiedGate(http.MethodGet, "/ext-console/api/v2/gateway/config/source", "", "(*upgradeConfigService).ReadSource")
	if code != http.StatusOK {
		t.Fatalf("degraded GET /gateway/config/source with held credentialGate = %d %s, want 200", code, raw)
	}
	srcView = freshconfig.SourceView{}
	if err := json.Unmarshal(raw, &srcView); err != nil {
		t.Fatal(err)
	}
	if srcView.Capability.Mode != freshconfig.ModeUnavailable || srcView.Capability.Reason != freshconfig.ReasonTargetMoved || srcView.SourceWritable || srcView.Reason != freshconfig.ReasonTargetMoved {
		t.Fatalf("degraded GET /gateway/config/source with held credentialGate = %+v", srcView)
	}

	if code, raw = runDegradedHTTPWithOccupiedGate(http.MethodPost, "/ext-console/api/v2/gateway/config/operations/"+reqMissing+"/reconcile", `{}`, "(*upgradeConfigService).operationScope"); code != http.StatusNotFound {
		t.Fatalf("degraded reconcile(missing) with held credentialGate = %d %s, want 404", code, raw)
	}
	code, raw = runDegradedHTTPWithOccupiedGate(http.MethodPost, "/ext-console/api/v2/gateway/config/operations/"+reqPersisted+"/reconcile", `{}`, "(*upgradeConfigService).operationScope")
	if code != http.StatusOK || !strings.Contains(string(raw), reqPersisted) {
		t.Fatalf("degraded reconcile(persisted) with held credentialGate = %d %s, want 200", code, raw)
	}
	code, raw = runDegradedHTTPWithOccupiedGate(http.MethodPost, "/ext-console/api/v2/gateway/config/operations/"+reqUnknown+"/reconcile", `{}`, "(*upgradeConfigService).operationScope")
	unknownReply.Code = ""
	unknownReply.Result = nil
	_ = json.Unmarshal(raw, &unknownReply)
	if code != http.StatusConflict || unknownReply.Code != freshconfig.ReasonTargetMoved || unknownReply.Result == nil || unknownReply.Result.Operation.RequestID != reqUnknown {
		t.Fatalf("degraded reconcile(unknown) with held credentialGate = %d %s, want 409 %s with result.operation %s", code, raw, freshconfig.ReasonTargetMoved, reqUnknown)
	}

	// Also verify ReasonUnavailable (non-target-moved precheck error) completes immediately while credentialGate is held by another goroutine.
	heldGateDone := make(chan struct{})
	releaseHeldGate := make(chan struct{})
	heldGateReady := make(chan struct{})
	go func() {
		defer close(heldGateDone)
		if ws.credentialGate.acquire(ctx) {
			close(heldGateReady)
			<-releaseHeldGate
			ws.credentialGate.release()
		}
	}()
	<-heldGateReady
	shortCtx, cancelShort := context.WithTimeout(ctx, 150*time.Millisecond)
	unavailView, unavailErr := canonical.configService.Read(withUpgradeExternalPrecheckErr(shortCtx, freshkeyapply.ErrExternalUnavailable))
	if unavailErr != nil || unavailView.Capability.Mode != freshconfig.ModeUnavailable || unavailView.Capability.Reason != freshconfig.ReasonUnavailable || len(unavailView.Groups) == 0 || len(unavailView.Fields) == 0 || len(unavailView.Families) == 0 {
		cancelShort()
		close(releaseHeldGate)
		<-heldGateDone
		t.Fatalf("Read(ErrExternalUnavailable) under held credentialGate = (%+v, err=%v), want unavailable/%s with non-empty projection", unavailView.Capability, unavailErr, freshconfig.ReasonUnavailable)
	}
	unavailSrc, unavailSrcErr := canonical.configService.ReadSource(withUpgradeExternalPrecheckErr(shortCtx, freshkeyapply.ErrExternalUnavailable))
	if unavailSrcErr != nil || unavailSrc.Capability.Mode != freshconfig.ModeUnavailable || unavailSrc.Capability.Reason != freshconfig.ReasonUnavailable || unavailSrc.SourceWritable || unavailSrc.Reason != freshconfig.ReasonUnavailable {
		cancelShort()
		close(releaseHeldGate)
		<-heldGateDone
		t.Fatalf("ReadSource(ErrExternalUnavailable) under held credentialGate = (%+v, err=%v), want unavailable/%s", unavailSrc, unavailSrcErr, freshconfig.ReasonUnavailable)
	}
	unavailOp, unavailOpErr := canonical.configService.Operation(withUpgradeExternalPrecheckErr(shortCtx, freshkeyapply.ErrExternalUnavailable), reqUnknown, true)
	cancelShort()
	close(releaseHeldGate)
	<-heldGateDone
	if !errors.Is(unavailOpErr, cpa.ErrApplyUnknown) || unavailOp.RequestID != reqUnknown {
		t.Fatalf("Operation(ErrExternalUnavailable, unknown) under held credentialGate = (%+v, err=%v), want (%s, ErrApplyUnknown)", unavailOp, unavailOpErr, reqUnknown)
	}
	// 3. Restore aligned setup, then simulate concurrent background collector recording rejection AFTER enterExternalCredentialHold succeeds.
	if err := st.SaveSetup(ctx, model.Setup{
		CPAUpstreamURL: strings.TrimRight(extCPA.URL, "/"),
		ManagementKey:  extKey,
	}); err != nil {
		t.Fatal(err)
	}
	binding, found, err := upgradeoperation.New(st.SQLDB()).ExternalBinding(ctx)
	if err != nil || !found {
		t.Fatalf("ExternalBinding: found=%v err=%v", found, err)
	}
	credDigest := envelopeDigest(strings.TrimSpace(binding.ManagementKey))

	// Hold configService.mu so GET /gateway/config passes enterExternalCredentialHold and parks before resolveAuthority.
	canonical.configService.mu.Lock()
	type respPair struct {
		code int
		body []byte
	}
	respCh := make(chan respPair, 1)
	go func() {
		c, b := callExt(http.MethodGet, "/ext-console/api/v2/gateway/config", "")
		respCh <- respPair{code: c, body: b}
	}()
	// Wait until the in-flight request acquires credentialGate (proving it passed enterExternalCredentialHold).
	waitCtx, cancelWait := context.WithTimeout(ctx, 20*time.Millisecond)
	if ws.credentialGate.acquire(waitCtx) {
		ws.credentialGate.release()
	}
	cancelWait()

	if err := ws.rejection.record(ctx, binding.Identity, credDigest); err != nil {
		canonical.configService.mu.Unlock()
		t.Fatalf("ws.rejection.record: %v", err)
	}
	canonical.configService.mu.Unlock()

	got := <-respCh
	if got.code != http.StatusConflict || !strings.Contains(string(got.body), freshconfig.ReasonManagementRejected) {
		t.Fatalf("GET /gateway/config with concurrent rejection after hold = %d %s, want 409 %s", got.code, got.body, freshconfig.ReasonManagementRejected)
	}
}
