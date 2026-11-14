package freshkeys_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	identityrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	identityports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

type stubRuntimeClient struct {
	runtimeservice.RuntimeClient
	status model.RuntimeObservedStatus
}

func (r *stubRuntimeClient) Status(context.Context) (model.RuntimeObservedStatus, error) {
	return r.status, nil
}

type stubManagedCPA struct {
	mu          sync.Mutex
	mgmtKey     string
	keys        []string
	authDelay   map[string]bool
	onPutKeys   func([]string)
	putRequests int
}

func (c *stubManagedCPA) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.keys)
}

func (c *stubManagedCPA) setAuthDelay(secret string, stillAccepted bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.authDelay == nil {
		c.authDelay = map[string]bool{}
	}
	if stillAccepted {
		c.authDelay[secret] = true
	} else {
		delete(c.authDelay, secret)
	}
}

func (c *stubManagedCPA) setOnPut(fn func([]string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onPutKeys = fn
}

func (c *stubManagedCPA) config() []byte {
	keys := c.snapshot()
	if keys == nil {
		keys = []string{}
	}
	body, _ := json.Marshal(map[string]any{
		"config-version": 8,
		"server":         map[string]any{"host": "0.0.0.0", "port": 8317},
		"management":     map[string]any{"allow-remote": true, "secret-key": "$2a$10$abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXY12", "disable-control-panel": true, "disable-auto-update-panel": true},
		"access":         map[string]any{"api-keys": keys},
		"oauth":          map[string]any{"auth-dir": "/runtime/supervisor/destination/auth"},
		"plugins":        map[string]any{"enabled": false, "dir": "/runtime/supervisor/plugins"},
		"observability":  map[string]any{"logs": map[string]any{"logging-to-file": false}, "usage": map[string]any{"usage-statistics-enabled": true}},
	})
	return body
}
func (c *stubManagedCPA) handler() http.Handler {
	mux := http.NewServeMux()
	readConfig := func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(c.config()) }
	mux.HandleFunc("GET /v8/management/config", readConfig)
	mux.HandleFunc("GET /v8/management/config.yaml", readConfig)
	mux.HandleFunc("/v8/management/config/access/api-keys", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		if r.Header.Get("Authorization") != "Bearer "+c.mgmtKey {
			c.mu.Unlock()
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			keys := slices.Clone(c.keys)
			c.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if keys == nil {
				keys = []string{}
			}
			_ = json.NewEncoder(w).Encode(keys)
		case http.MethodPut:
			var next []string
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				c.mu.Unlock()
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			c.keys = slices.Clone(next)
			c.putRequests++
			hook := c.onPutKeys
			snapshot := slices.Clone(c.keys)
			c.mu.Unlock()
			if hook != nil {
				hook(snapshot)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		default:
			c.mu.Unlock()
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" || (!slices.Contains(c.keys, token) && !c.authDelay[token]) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"gpt-5","object":"model"}]}`))
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-CPA-Version", "v8.0.55")
		if strings.HasPrefix(r.URL.Path, "/v8/") && r.Header.Get("Authorization") != "Bearer "+c.mgmtKey {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func sha256Hex(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func snapshotItems(secrets []string) []identityports.APIKeySnapshotItem {
	items := make([]identityports.APIKeySnapshotItem, 0, len(secrets))
	for _, secret := range secrets {
		items = append(items, identityports.APIKeySnapshotItem{APIKeyHash: sha256Hex(secret)})
	}
	return items
}

func openPassiveInterleavingDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "passive-interleaving.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlite.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPassiveMissingInterleavingRecoversContinueOperationAndCrossRuntimeHandoff(t *testing.T) {
	t.Setenv("CPAMP_PRIVATE_CONFIG_AUTHORITY", "")
	ctx := t.Context()
	db := openPassiveInterleavingDB(t)

	const (
		runtimeOne = "managed-install-runtime-1"
		runtimeTwo = "managed-install-runtime-2"
		mgmtKey    = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	)
	fakeCPA := &stubManagedCPA{mgmtKey: mgmtKey}
	server := httptest.NewServer(fakeCPA.handler())
	t.Cleanup(server.Close)

	protector, err := security.NewProtector([]byte("passive-missing-interleaving-secret-key"))
	if err != nil {
		t.Fatal(err)
	}
	keyRepo := repo.New(db)
	identities := identityrepo.New(db)
	keySvc := freshkeys.New(keyRepo, protector, freshkeys.WithSourceRuntime(func(context.Context) (string, error) {
		return runtimeOne, nil
	}))
	rtClient := &stubRuntimeClient{
		status: model.RuntimeObservedStatus{
			Identity:           runtimeOne,
			Generation:         9,
			ProtocolVersion:    "v1",
			State:              model.RuntimeStateReady,
			CPAObservedVersion: "v8.0.55",
			ActiveGatewayArtifact: &model.ActiveGatewayArtifact{
				Engine:     "cpa",
				ArtifactID: model.RuntimeArtifactID("sha256:" + strings.Repeat("b", 64)),
				Version:    "v8.0.55",
			},
		},
	}
	install := freshkeyapply.ManagedInstall{
		RunID:         "managed:run-passive-1",
		Generation:    "9",
		Identity:      runtimeOne,
		ArtifactID:    "sha256:" + strings.Repeat("b", 64),
		BaseURL:       server.URL,
		ManagementKey: mgmtKey,
		Strategy:      "new_managed",
	}
	applySvc := freshkeyapply.NewManaged(keyRepo, keySvc, rtClient, server.URL, func(context.Context) (freshkeyapply.ManagedInstall, error) {
		return install, nil
	})

	// Stage K1 and K2, install K1, add K2, and run passive snapshot so both have observed_runtime_generation != 0.
	k1, _, err := keySvc.Create(ctx, strings.Repeat("1", 32))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := applySvc.Apply(ctx, string(k1.Key.ID), 9); err != nil || state.RuntimeState != "effective_observed" {
		t.Fatalf("initial install K1 = %+v (%v)", state, err)
	}
	k2, _, err := keySvc.Create(ctx, strings.Repeat("2", 32))
	if err != nil {
		t.Fatal(err)
	}
	if state, err := applySvc.ApplyAdd(ctx, strings.Repeat("a", 32), string(k2.Key.ID), 9); err != nil || state.RuntimeState != "effective_observed" {
		t.Fatalf("ApplyAdd K2 = %+v (%v)", state, err)
	}
	if _, err := identities.ApplyPassiveSnapshot(ctx, identityports.ReconcileSnapshotParams{
		RuntimeIdentity:           runtimeOne,
		ObservedRuntimeGeneration: 9,
		APIKeys:                   snapshotItems(fakeCPA.snapshot()),
		NowMS:                     k2.Key.CreatedAtMS + 100,
	}); err != nil {
		t.Fatalf("ApplyPassiveSnapshot(initial observed): %v", err)
	}

	// 1. Revoke K2: hold /v1/models auth rejection on first ContinueOperation so PUT succeeds and journal stays
	// Outcome="unknown", then run ApplyPassiveSnapshot (marking K2 LifecycleMissing), release auth delay,
	// and verify ContinueOperation reconciles and commits K2 -> LifecycleSuperseded without re-PUTting.
	revokeOpID := strings.Repeat("b", 32)
	if _, err := applySvc.Prepare(ctx, revokeOpID, string(k2.Key.ID), "", "revoke", 9); err != nil {
		t.Fatalf("Prepare(revoke K2): %v", err)
	}
	fakeCPA.setAuthDelay(k2.CreatedSecret, true)
	putsBeforeRevoke := fakeCPA.putRequests
	revokeRes, err := applySvc.ContinueOperation(ctx, revokeOpID, 9)
	if !errors.Is(err, cpa.ErrApplyUnknown) || revokeRes.Operation.Outcome != "unknown" {
		t.Fatalf("ContinueOperation(revoke K2 delayed reload) = %+v (%v), want Outcome=unknown", revokeRes, err)
	}
	if fakeCPA.putRequests != putsBeforeRevoke+1 {
		t.Fatalf("revoke PUT count = %d, want %d", fakeCPA.putRequests, putsBeforeRevoke+1)
	}

	// Passive reconciler polls CPA between PUT and journal commit and marks K2 LifecycleMissing.
	if _, err := identities.ApplyPassiveSnapshot(ctx, identityports.ReconcileSnapshotParams{
		RuntimeIdentity:           runtimeOne,
		ObservedRuntimeGeneration: 10,
		APIKeys:                   snapshotItems(fakeCPA.snapshot()),
		NowMS:                     k2.Key.CreatedAtMS + 200,
	}); err != nil {
		t.Fatalf("ApplyPassiveSnapshot(K2 absent before revoke commit): %v", err)
	}
	k2DuringUnknown, err := keySvc.Detail(ctx, string(k2.Key.ID))
	if err != nil || k2DuringUnknown.Lifecycle != identity.LifecycleMissing {
		t.Fatalf("K2 lifecycle during unsettled revoke = %+v (%v), want missing", k2DuringUnknown, err)
	}

	// Hot reload settles; ContinueOperation must recover the unsettled revoke to Outcome="committed" and supersede K2.
	fakeCPA.setAuthDelay(k2.CreatedSecret, false)
	revokeRes, err = applySvc.ContinueOperation(ctx, revokeOpID, 9)
	if err != nil || revokeRes.Operation.Outcome != "committed" || revokeRes.RuntimeState != "effective_observed" {
		t.Fatalf("ContinueOperation(revoke K2 after passive missing) = %+v (%v), want committed/effective_observed", revokeRes, err)
	}
	if fakeCPA.putRequests != putsBeforeRevoke+1 {
		t.Fatalf("reconciling ContinueOperation issued extra PUT: got %d, want %d", fakeCPA.putRequests, putsBeforeRevoke+1)
	}
	k2AfterRevoke, err := keySvc.Detail(ctx, string(k2.Key.ID))
	if err != nil || k2AfterRevoke.Lifecycle != identity.LifecycleSuperseded {
		t.Fatalf("K2 after revoke commit = %+v (%v), want superseded", k2AfterRevoke, err)
	}

	// 2. Replace K1 -> K3: interleave ApplyPassiveSnapshot directly inside the revoke PUT handler before
	// ContinueOperation's post-write checkOperation commits SaveOperation.
	k3, _, err := keySvc.Create(ctx, strings.Repeat("3", 32))
	if err != nil {
		t.Fatal(err)
	}
	replaceOpID := strings.Repeat("c", 32)
	if _, err := applySvc.Prepare(ctx, replaceOpID, string(k1.Key.ID), string(k3.Key.ID), "replace", 9); err != nil {
		t.Fatalf("Prepare(replace K1->K3): %v", err)
	}
	stepAddRes, err := applySvc.ContinueOperation(ctx, replaceOpID, 9)
	if err != nil || stepAddRes.NextStep != "revoke" {
		t.Fatalf("ContinueOperation(replace add step) = %+v (%v), want NextStep=revoke", stepAddRes, err)
	}
	// Passive snapshot observes {K1, K3} before step 1 revokes K1.
	if _, err := identities.ApplyPassiveSnapshot(ctx, identityports.ReconcileSnapshotParams{
		RuntimeIdentity:           runtimeOne,
		ObservedRuntimeGeneration: 11,
		APIKeys:                   snapshotItems(fakeCPA.snapshot()),
		NowMS:                     k3.Key.CreatedAtMS + 300,
	}); err != nil {
		t.Fatalf("ApplyPassiveSnapshot(before replace revoke step): %v", err)
	}
	var hookErr error
	fakeCPA.setOnPut(func(liveKeys []string) {
		_, hookErr = identities.ApplyPassiveSnapshot(ctx, identityports.ReconcileSnapshotParams{
			RuntimeIdentity:           runtimeOne,
			ObservedRuntimeGeneration: 12,
			APIKeys:                   snapshotItems(liveKeys),
			NowMS:                     k3.Key.CreatedAtMS + 400,
		})
	})
	stepRevokeRes, err := applySvc.ContinueOperation(ctx, replaceOpID, 9)
	fakeCPA.setOnPut(nil)
	if hookErr != nil {
		t.Fatalf("in-flight ApplyPassiveSnapshot hook failed: %v", hookErr)
	}
	if err != nil || stepRevokeRes.Operation.Outcome != "committed" || stepRevokeRes.RuntimeState != "effective_observed" {
		t.Fatalf("ContinueOperation(replace revoke step with in-flight passive missing) = %+v (%v)", stepRevokeRes, err)
	}
	k1AfterReplace, err := keySvc.Detail(ctx, string(k1.Key.ID))
	if err != nil || k1AfterReplace.Lifecycle != identity.LifecycleSuperseded {
		t.Fatalf("K1 after replace commit = %+v (%v), want superseded", k1AfterReplace, err)
	}
	k3AfterReplace, err := keySvc.Detail(ctx, string(k3.Key.ID))
	if err != nil || k3AfterReplace.Lifecycle != identity.LifecycleActive {
		t.Fatalf("K3 after replace commit = %+v (%v), want active", k3AfterReplace, err)
	}
	if err := keyRepo.H1Idle(ctx); err != nil {
		t.Fatalf("H1Idle after committed replace = %v, want nil", err)
	}

	// 3. Passive snapshot marks surviving K3 LifecycleMissing on runtimeOne; ObserveKey still reports
	// RuntimeState="not_applied" without failing secret access, and BindFreshKeySourcesTx onto runtimeTwo
	// recovers K3 -> LifecycleActive while leaving superseded K1 and K2 unresurrected.
	if _, err := identities.ApplyPassiveSnapshot(ctx, identityports.ReconcileSnapshotParams{
		RuntimeIdentity:           runtimeOne,
		ObservedRuntimeGeneration: 13,
		APIKeys:                   nil,
		NowMS:                     k3.Key.CreatedAtMS + 500,
	}); err != nil {
		t.Fatalf("ApplyPassiveSnapshot(K3 absent): %v", err)
	}
	k3Missing, err := keySvc.Detail(ctx, string(k3.Key.ID))
	if err != nil || k3Missing.Lifecycle != identity.LifecycleMissing {
		t.Fatalf("K3 after passive absent = %+v (%v), want missing", k3Missing, err)
	}
	fakeCPA.mu.Lock()
	fakeCPA.keys = []string{"cpamp_sk_" + strings.Repeat("e", 64)}
	fakeCPA.mu.Unlock()
	observedMissing, err := applySvc.ObserveKey(ctx, k3Missing, 9)
	if err != nil || observedMissing.RuntimeState != "not_applied" {
		t.Fatalf("ObserveKey(passive missing K3) = %+v (%v), want RuntimeState=not_applied", observedMissing, err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := keyRepo.BindFreshKeySourcesTx(ctx, tx, runtimeTwo, k3.Key.CreatedAtMS+600)
	if err != nil || bound != 1 {
		_ = tx.Rollback()
		t.Fatalf("BindFreshKeySourcesTx(handoff to runtimeTwo) = %d (%v), want 1", bound, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	k3Recovered, err := keySvc.Detail(ctx, string(k3.Key.ID))
	if err != nil || k3Recovered.Lifecycle != identity.LifecycleActive {
		t.Fatalf("K3 after handoff = %+v (%v), want active", k3Recovered, err)
	}
	for _, retiredID := range []identity.APIKeyID{k1.Key.ID, k2.Key.ID} {
		rec, err := keyRepo.ByID(ctx, retiredID)
		if err != nil || rec == nil || rec.Identity.Lifecycle != identity.LifecycleSuperseded {
			t.Fatalf("retired key %s after handoff = %+v (%v), want superseded", retiredID, rec, err)
		}
		var activeBindings int
		if err := db.QueryRowContext(ctx,
			`select count(*) from gateway_api_key_source_bindings where api_key_id = ? and retired_at_ms is null`,
			retiredID).Scan(&activeBindings); err != nil || activeBindings != 0 {
			t.Fatalf("retired key %s has %d active bindings (%v), want 0", retiredID, activeBindings, err)
		}
	}
}
