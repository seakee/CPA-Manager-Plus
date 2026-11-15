package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	adaptersqliteidentityprojection "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identityprojection"
	usageadapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identityusage"
	usageapp "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/bootstrap"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	keyrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshconfig"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	freshstrategy "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshstrategy"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/legacypreflight"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestOrdinaryManagedKeyCreateMultiKeyRevokeAndFailureRecovery(t *testing.T) {
	harness := newManagedInstallHarness(t)

	// Create 3 additional workspace keys (total 4 active keys > old 2-key cap)
	// via ordinary POST api-keys without manual initial-install or keeper selection.
	reqIDs := []string{strings.Repeat("a", 32), strings.Repeat("b", 32), strings.Repeat("c", 32)}
	created := make([]freshkeys.Result, 0, len(reqIDs))
	for i, reqID := range reqIDs {
		res := harness.createKey(t, reqID)
		if res.Key.RuntimeState != "effective_observed" {
			t.Fatalf("key %d runtimeState = %q, want effective_observed", i, res.Key.RuntimeState)
		}
		created = append(created, res)
	}
	if got := harness.destination.installed(); len(got) != 4 {
		t.Fatalf("installed Managed key count = %d, want 4", len(got))
	}

	// Replay with existing requestId returns 200 OK with same canonical ID,
	// effective_observed state, and no createdSecret; secret retrieval route works.
	replay := harness.call(t, http.MethodPost, "api-keys", `{"requestId":"`+reqIDs[0]+`"}`)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay POST api-keys = %d %s, want 200", replay.Code, replay.Body.String())
	}
	var replayBody clientKeyCreateResponse
	if err := json.Unmarshal(replay.Body.Bytes(), &replayBody); err != nil {
		t.Fatal(err)
	}
	if replayBody.Key.ID != created[0].Key.ID || replayBody.Key.RuntimeState != "effective_observed" || replayBody.CreatedSecret != "" {
		t.Fatalf("replay body = %+v", replayBody)
	}
	secretResp := harness.call(t, http.MethodPost, "api-keys/"+string(created[0].Key.ID)+"/secret", `{}`)
	if secretResp.Code != http.StatusOK || secretResp.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("POST secret = %d", secretResp.Code)
	}
	var revealed struct {
		Secret string `json:"secret"`
	}
	if err := json.Unmarshal(secretResp.Body.Bytes(), &revealed); err != nil || revealed.Secret != created[0].CreatedSecret {
		t.Fatal("revealed secret mismatch")
	}

	// Name partial failure: invalid name returns 400 without minting a new key or mutating CPA;
	// Retrying PUT updates canonical metadata without requiring a remote auth probe.
	writesBeforeRename := harness.destination.writes
	badRename := harness.call(t, http.MethodPut, "api-keys/"+string(created[0].Key.ID), `{"name":"bad\nname","nameRevision":1}`)
	if badRename.Code != http.StatusBadRequest {
		t.Fatalf("invalid rename = %d, want 400", badRename.Code)
	}
	goodRename := harness.call(t, http.MethodPut, "api-keys/"+string(created[0].Key.ID), `{"name":"Production Service A","nameRevision":1}`)
	if goodRename.Code != http.StatusOK {
		t.Fatalf("valid rename = %d %s", goodRename.Code, goodRename.Body.String())
	}
	var renamedKey freshkeys.Key
	if err := json.Unmarshal(goodRename.Body.Bytes(), &renamedKey); err != nil || renamedKey.Name != "Production Service A" || renamedKey.ID != created[0].Key.ID || renamedKey.RequestID != created[0].Key.RequestID || renamedKey.NameRevision != 2 {
		t.Fatalf("renamed key = %+v (%v)", renamedKey, err)
	}
	if harness.destination.writes != writesBeforeRename {
		t.Fatalf("rename mutated CPA (%d -> %d writes)", writesBeforeRename, harness.destination.writes)
	}

	// Simulate CPA management outage during ordinary POST api-keys:
	// Key is durably persisted as not_applied with non-2xx error, read-only request
	// lookup reports not_applied, and retrying same requestId after recovery activates
	// that exact Key without generating a second key.
	harness.destination.mu.Lock()
	savedMgmt := harness.destination.management
	harness.destination.management = "outage-mismatch-token"
	harness.destination.mu.Unlock()

	recoverReqID := strings.Repeat("d", 32)
	failedCreate := harness.call(t, http.MethodPost, "api-keys", `{"requestId":"`+recoverReqID+`"}`)
	if failedCreate.Code == http.StatusOK || failedCreate.Code == http.StatusCreated {
		t.Fatalf("POST api-keys during CPA outage returned fake success %d", failedCreate.Code)
	}
	var createErr clientKeyCreateErrorResponse
	if err := json.Unmarshal(failedCreate.Body.Bytes(), &createErr); err != nil || createErr.Key == nil || createErr.Key.ID == "" {
		t.Fatalf("failed create error body missing persisted key: %v %s", err, failedCreate.Body.String())
	}
	if createErr.Key.RuntimeState != "not_applied" {
		t.Fatalf("persisted key runtimeState during precheck outage = %q, want not_applied", createErr.Key.RuntimeState)
	}

	lookupDuringOutage := harness.call(t, http.MethodGet, "api-keys/requests/"+recoverReqID, "")
	if lookupDuringOutage.Code != http.StatusOK {
		t.Fatalf("GET api-keys/requests during outage = %d", lookupDuringOutage.Code)
	}
	var lookedUp freshkeys.Key
	if err := json.Unmarshal(lookupDuringOutage.Body.Bytes(), &lookedUp); err != nil || lookedUp.ID != createErr.Key.ID {
		t.Fatalf("request lookup mismatch: %+v (%v)", lookedUp, err)
	}

	harness.destination.mu.Lock()
	harness.destination.management = savedMgmt
	harness.destination.mu.Unlock()

	recoveredCreate := harness.call(t, http.MethodPost, "api-keys", `{"requestId":"`+recoverReqID+`"}`)
	if recoveredCreate.Code != http.StatusOK {
		t.Fatalf("recovered POST api-keys = %d %s, want 200", recoveredCreate.Code, recoveredCreate.Body.String())
	}
	var recoveredBody clientKeyCreateResponse
	if err := json.Unmarshal(recoveredCreate.Body.Bytes(), &recoveredBody); err != nil || recoveredBody.Key.ID != createErr.Key.ID || recoveredBody.Key.RuntimeState != "effective_observed" {
		t.Fatalf("recovered key = %+v (%v)", recoveredBody, err)
	}

	// Keeperless preflight & revoke of middle key created[1] preserves exact order of all 4 other keys.
	preflight := harness.call(t, http.MethodGet, "api-keys/"+string(created[1].Key.ID)+"/revocation/revoke", "")
	if preflight.Code != http.StatusOK || !strings.Contains(preflight.Body.String(), `"ready":true`) {
		t.Fatalf("keeperless preflight = %d %s", preflight.Code, preflight.Body.String())
	}
	revokeOpID := strings.Repeat("e", 32)
	prep := harness.call(t, http.MethodPost, "api-keys/"+string(created[1].Key.ID)+"/revocation", `{"requestId":"`+revokeOpID+`","mode":"revoke"}`)
	if prep.Code != http.StatusOK {
		t.Fatalf("keeperless prepare = %d %s", prep.Code, prep.Body.String())
	}
	cont := harness.call(t, http.MethodPost, "api-keys/operations/"+revokeOpID+"/continue", `{}`)
	if cont.Code != http.StatusOK {
		t.Fatalf("keeperless continue = %d %s", cont.Code, cont.Body.String())
	}

	listResp := harness.call(t, http.MethodGet, "api-keys", "")
	if listResp.Code != http.StatusOK {
		t.Fatalf("GET api-keys = %d", listResp.Code)
	}
	var listPayload struct {
		Keys               []freshkeys.Key `json:"keys"`
		PresenceUnverified bool            `json:"presenceUnverified"`
	}
	if err := json.Unmarshal(listResp.Body.Bytes(), &listPayload); err != nil || listPayload.PresenceUnverified || len(listPayload.Keys) != 5 {
		t.Fatalf("list payload = %+v (%v)", listPayload, err)
	}
	for _, k := range listPayload.Keys {
		if k.ID == created[1].Key.ID {
			if k.Lifecycle != "superseded" || k.RuntimeState != "revoked_observed" {
				t.Fatalf("revoked key projection = %+v", k)
			}
		} else if k.RuntimeState != "effective_observed" {
			t.Fatalf("active key projection = %+v, want effective_observed", k)
		}
	}
}

func TestUpgradeManagedAndExternalKeyWorkspacesPreserveIdentitiesAndEnforceAuthorities(t *testing.T) {
	t.Run("upgrade_managed_preserves_canonical_id_usage_and_activates_new_keys", func(t *testing.T) {
		ctx := t.Context()
		dbPath := filepath.Join(t.TempDir(), "upgraded-managed.sqlite")
		db, err := sqlite.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if err := sqlite.Migrate(db); err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector([]byte("upgrade-managed-key-workspace-32"))
		if err != nil {
			t.Fatal(err)
		}

		// Seed a pre-existing canonical identity + source binding + usage event
		// from 1.x migration before the key workspace runs.
		migratedSecret := "legacy-v1-client-key-alpha"
		sum := sha256.Sum256([]byte(migratedSecret))
		migratedHash := hex.EncodeToString(sum[:])
		preExistingID := "99887766554433221100ffeeddccbbaa"
		createdAtMS := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 1, 'active', ?, ?)`,
			preExistingID, createdAtMS, createdAtMS); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, observed_runtime_generation, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, ?, ?, 1, ?, ?, null)`,
			preExistingID, "embedded-default", migratedHash, createdAtMS, createdAtMS+1000); err != nil {
			t.Fatal(err)
		}

		// Start destination CPA carrying migratedSecret with adopt-compatible Managed posture.
		dest := newManagedDestination(t, migratedSecret)
		destServer := dest.serve(t)
		seedUpgradeManagedImportDestination(
			t, db, protector, destServer.URL, dest.management,
			"embedded-default", 7, 1, []string{migratedSecret}, createdAtMS,
		)

		// Record genuine completed Upgrade Managed verdict via production gate.
		recordCompletedUpgradeVerdict(t, db, model.UpgradeVerification{
			TargetMode:        model.UpgradeTargetManaged,
			SourceFingerprint: "sha256:" + strings.Repeat("2", 64),
			Domains:           []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig},
			ObservedAtMS:      createdAtMS + 2000,
		})

		runtimeToken := "supervisor-token-upgrade-managed"
		rtServer := managedKeyRuntime(t, upgradeManagedRuntimeStatus(t), runtimeToken)
		// The Runtime is reached through its production client, so the Upgrade
		// workspace reads the same authenticated status contract a real install
		// publishes. No test-only Runtime adapter stands in for it.
		rtClient := runtimeservice.NewEmbeddedClient(rtServer.URL, runtimeToken)
		usageSvc := usageapp.New(usageadapter.New(db), func() usageapp.CollectionPhase { return usageapp.PhaseStopped })
		if err := usageSvc.MarkCollectionStart(ctx); err != nil {
			t.Fatal(err)
		}

		handler, closeFn, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
			DB:        db,
			Protector: protector,
			Runtime:   rtClient,
			Usage:     usageSvc,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeFn)

		callUpgrade := func(method, resource, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, currentInstallManagementAPIV2Prefix+""+resource, bytes.NewBufferString(body))
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			handler.serveHTTP(rec, req, resource)
			return rec
		}

		// Verify fresh_install_state stays completely empty (no fabricated Fresh checkpoint!).
		var freshRows int
		_ = db.QueryRowContext(ctx, `select count(1) from fresh_install_state`).Scan(&freshRows)
		if freshRows != 0 {
			t.Fatalf("fresh_install_state rows = %d, want 0", freshRows)
		}

		listRes := callUpgrade(http.MethodGet, "api-keys", "")
		if listRes.Code != http.StatusOK {
			t.Fatalf("Upgrade Managed GET api-keys = %d %s", listRes.Code, listRes.Body.String())
		}
		var listBody struct {
			Keys                []freshkeys.Key `json:"keys"`
			GatewayPending      bool            `json:"gatewayPending"`
			RevocationAvailable bool            `json:"revocationAvailable"`
			PresenceUnverified  bool            `json:"presenceUnverified"`
		}
		if err := json.Unmarshal(listRes.Body.Bytes(), &listBody); err != nil {
			t.Fatal(err)
		}
		if len(listBody.Keys) != 1 || string(listBody.Keys[0].ID) != preExistingID || listBody.Keys[0].RuntimeState != "effective_observed" || listBody.GatewayPending || !listBody.RevocationAvailable {
			t.Fatalf("Upgrade Managed list = %+v", listBody)
		}

		// Create a second key via ordinary POST api-keys on Upgrade Managed.
		newReqID := strings.Repeat("1", 32)
		createRes := callUpgrade(http.MethodPost, "api-keys", `{"requestId":"`+newReqID+`"}`)
		if createRes.Code != http.StatusCreated {
			t.Fatalf("Upgrade Managed POST api-keys = %d %s", createRes.Code, createRes.Body.String())
		}
		var createdResp clientKeyCreateResponse
		if err := json.Unmarshal(createRes.Body.Bytes(), &createdResp); err != nil || createdResp.Key.RuntimeState != "effective_observed" || createdResp.CreatedSecret == "" {
			t.Fatalf("Upgrade Managed created = %+v (%v)", createdResp, err)
		}
		if got := dest.installed(); len(got) != 2 || got[0] != migratedSecret || got[1] != createdResp.CreatedSecret {
			t.Fatalf("Upgrade Managed CPA keys count = %d, want 2 preserving migrated key first", len(got))
		}

		// Out-of-band CPA edit after initial Upgrade Managed registration must fail closed with source conflict!
		dest.mu.Lock()
		dest.keys = append(dest.keys, "out-of-band-unregistered-key")
		dest.mu.Unlock()
		conflictCreate := callUpgrade(http.MethodPost, "api-keys", `{"requestId":"`+strings.Repeat("2", 32)+`"}`)
		if conflictCreate.Code != http.StatusConflict || !strings.Contains(conflictCreate.Body.String(), `"key_operation_source_conflict"`) {
			t.Fatalf("out-of-band edit did not fail closed: %d %s", conflictCreate.Code, conflictCreate.Body.String())
		}
	})

	t.Run("upgrade_external_activates_keys_binds_usage_and_gates_rejected_management_credential", func(t *testing.T) {
		ctx := t.Context()
		dbPath := filepath.Join(t.TempDir(), "upgraded-external.sqlite")
		db, err := sqlite.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if err := sqlite.Migrate(db); err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector([]byte("upgrade-external-key-workspace32"))
		if err != nil {
			t.Fatal(err)
		}

		extMgmt := strings.Repeat("ab", 32)
		extCPA := newStatefulExternalCPA(t, extMgmt)
		extCPA.mu.Lock()
		extCPA.clientKeys = []string{"user-preexisting-ext-key"}
		extCPA.mu.Unlock()

		normalizedURL, err := cpa.NormalizeV8BaseURL(extCPA.server.URL)
		if err != nil {
			t.Fatal(err)
		}
		extIdentity := freshkeyapply.ExternalIdentity(normalizedURL)
		sealedMgmt, err := protector.ProtectString(extMgmt)
		if err != nil {
			t.Fatal(err)
		}
		nowMS := time.Now().UnixMilli()
		// The binding is written exactly as the External connect step writes it: the
		// address-derived identity, its observation time and the sealed credential,
		// committed in one transaction so no row can name a target it never proved.
		bindingTx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := upgradeoperation.RecordExternalBindingTx(ctx, bindingTx, upgradeoperation.ExternalBinding{
			BaseURL:       normalizedURL,
			Identity:      extIdentity,
			Generation:    extIdentity,
			ManagementKey: sealedMgmt,
			ObservedAtMS:  nowMS,
			ClientKeys:    1,
		}); err != nil {
			_ = bindingTx.Rollback()
			t.Fatal(err)
		}
		if err := bindingTx.Commit(); err != nil {
			t.Fatal(err)
		}
		recordCompletedUpgradeVerdict(t, db, model.UpgradeVerification{
			TargetMode:          model.UpgradeTargetExternal,
			SourceFingerprint:   "sha256:" + strings.Repeat("4", 64),
			Domains:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig, model.UpgradeDomainIdentity},
			BindingIdentity:     extIdentity,
			BindingGeneration:   extIdentity,
			BindingObservedAtMS: nowMS,
			ObservedAtMS:        nowMS + 1,
		})
		// Seed an unbound active staged key before workspace startup to verify
		// UpgradeExternal startup backfill binds it and wakes projection.
		unboundSvc := freshkeys.New(keyrepo.New(db), protector)
		preUnbound, _, err := unboundSvc.Create(ctx, strings.Repeat("7", 32))
		if err != nil {
			t.Fatal(err)
		}

		usageSvc := usageapp.New(usageadapter.New(db), func() usageapp.CollectionPhase { return usageapp.PhaseRunning })
		if err := usageSvc.MarkCollectionStart(ctx); err != nil {
			t.Fatal(err)
		}
		var wakeCount atomic.Int32
		handler, closeFn, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
			DB:        db,
			Protector: protector,
			Usage:     usageSvc,
			WakeProjection: func() {
				wakeCount.Add(1)
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeFn)

		if wakeCount.Load() != 1 {
			t.Fatalf("startup backfill wakeCount = %d, want 1", wakeCount.Load())
		}
		var preBoundIdentity string
		if err := db.QueryRowContext(ctx, `select runtime_identity from gateway_api_key_source_bindings where api_key_id = ? and retired_at_ms is null`, string(preUnbound.Key.ID)).Scan(&preBoundIdentity); err != nil || preBoundIdentity != extIdentity {
			t.Fatalf("backfilled pre-existing key binding = %q err=%v, want %q", preBoundIdentity, err, extIdentity)
		}

		callExt := func(method, resource, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, currentInstallManagementAPIV2Prefix+""+resource, bytes.NewBufferString(body))
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			handler.serveHTTP(rec, req, resource)
			return rec
		}

		wakesBeforeCreate := wakeCount.Load()
		createRes := callExt(http.MethodPost, "api-keys", `{"requestId":"`+strings.Repeat("8", 32)+`"}`)
		if createRes.Code != http.StatusCreated {
			t.Fatalf("Upgrade External POST api-keys = %d %s", createRes.Code, createRes.Body.String())
		}
		var createdBody clientKeyCreateResponse
		if err := json.Unmarshal(createRes.Body.Bytes(), &createdBody); err != nil || createdBody.Key.RuntimeState != "effective_observed" || createdBody.CreatedSecret == "" {
			t.Fatalf("Upgrade External create body = %+v (%v)", createdBody, err)
		}
		if wakeCount.Load() != wakesBeforeCreate+1 {
			t.Fatalf("wakeCount after UpgradeExternal POST api-keys = %d, want %d", wakeCount.Load(), wakesBeforeCreate+1)
		}
		extCPA.mu.Lock()
		extKeys := slices.Clone(extCPA.clientKeys)
		extCPA.mu.Unlock()
		if len(extKeys) != 2 || extKeys[0] != "user-preexisting-ext-key" || extKeys[1] != createdBody.CreatedSecret {
			t.Fatalf("Upgrade External keys = %d, want user key preserved first", len(extKeys))
		}

		// Verify immediate source binding at CreatedAtMS and immediate usage mapping.
		sum := sha256.Sum256([]byte(createdBody.CreatedSecret))
		createdHash := hex.EncodeToString(sum[:])
		var (
			boundRuntime string
			boundHash    string
			firstSeenMS  int64
			retiredMS    sql.NullInt64
		)
		if err := db.QueryRowContext(ctx, `select runtime_identity, api_key_hash, first_seen_at_ms, retired_at_ms
			from gateway_api_key_source_bindings where api_key_id = ?`, string(createdBody.Key.ID)).
			Scan(&boundRuntime, &boundHash, &firstSeenMS, &retiredMS); err != nil {
			t.Fatalf("UpgradeExternal immediate binding missing: %v", err)
		}
		if boundRuntime != extIdentity || boundHash != createdHash || firstSeenMS != createdBody.Key.CreatedAtMS || retiredMS.Valid {
			t.Fatalf("UpgradeExternal binding = runtime:%q hash:%q firstSeen:%d retired:%+v, want runtime:%q firstSeen:%d",
				boundRuntime, boundHash, firstSeenMS, retiredMS, extIdentity, createdBody.Key.CreatedAtMS)
		}

		legacyStore := store.New(db, protector)
		rawEv, _ := json.Marshal(map[string]any{
			"request_id":    "upg-ext-imm-1",
			"timestamp":     strconv.FormatInt(createdBody.Key.CreatedAtMS, 10),
			"model":         "gpt-4",
			"api_key":       createdBody.CreatedSecret,
			"input_tokens":  35,
			"output_tokens": 20,
			"total_tokens":  55,
		})
		ev, err := usage.NormalizeRaw(rawEv)
		if err != nil {
			t.Fatal(err)
		}
		if res, err := legacyStore.InsertEvents(ctx, []usage.Event{ev}); err != nil || res.Inserted != 1 {
			t.Fatalf("insert external event = %+v err=%v", res, err)
		}
		projRepo := adaptersqliteidentityprojection.New(db)
		if _, err := projRepo.CatchUp(ctx, 100, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
		for time.Now().UnixMilli() <= ev.TimestampMS {
			time.Sleep(time.Millisecond)
		}
		detailRes := callExt(http.MethodGet, "api-keys/"+string(createdBody.Key.ID)+"/usage", "")
		if detailRes.Code != http.StatusOK {
			t.Fatalf("UpgradeExternal GET key usage = %d %s", detailRes.Code, detailRes.Body.String())
		}
		detail := decodeUsage[usageapp.DetailResponse](t, detailRes)
		if detail.Totals == nil || detail.Totals.Calls != 1 || detail.Totals.TotalTokens != 55 {
			t.Fatalf("UpgradeExternal immediate key usage = %+v", detail)
		}

		// Revoke createdBody.Key via Prepare + ContinueOperation: retires binding and wakes projection.
		wakesBeforeRevoke := wakeCount.Load()
		revokeOpID := strings.Repeat("a", 32)
		prep := callExt(http.MethodPost, "api-keys/"+string(createdBody.Key.ID)+"/revocation", `{"requestId":"`+revokeOpID+`","mode":"revoke"}`)
		if prep.Code != http.StatusOK {
			t.Fatalf("UpgradeExternal prepare revoke = %d %s", prep.Code, prep.Body.String())
		}
		cont := callExt(http.MethodPost, "api-keys/operations/"+revokeOpID+"/continue", `{}`)
		if cont.Code != http.StatusOK {
			t.Fatalf("UpgradeExternal continue revoke = %d %s", cont.Code, cont.Body.String())
		}
		if wakeCount.Load() <= wakesBeforeRevoke {
			t.Fatalf("wakeCount did not advance after UpgradeExternal revoke commit (%d -> %d)", wakesBeforeRevoke, wakeCount.Load())
		}
		if err := db.QueryRowContext(ctx, `select retired_at_ms from gateway_api_key_source_bindings where api_key_id = ?`, string(createdBody.Key.ID)).Scan(&retiredMS); err != nil || !retiredMS.Valid {
			t.Fatalf("UpgradeExternal retired_at_ms after revoke = %+v (%v)", retiredMS, err)
		}

		// Rotate management credential on External CPA: first call records durable refusal,
		// second call answers locally without sending another management request.
		extCPA.mu.Lock()
		extCPA.activeKey = strings.Repeat("cd", 32)
		extCPA.mu.Unlock()

		firstRefused := callExt(http.MethodGet, "api-keys", "")
		if firstRefused.Code != http.StatusConflict || !strings.Contains(firstRefused.Body.String(), freshconfig.ReasonManagementRejected) {
			t.Fatalf("first refused GET api-keys = %d %s", firstRefused.Code, firstRefused.Body.String())
		}
		reqsAfterFirstRefusal := extCPA.requests.Load()

		gatedCreate := callExt(http.MethodPost, "api-keys", `{"requestId":"`+strings.Repeat("9", 32)+`"}`)
		if gatedCreate.Code != http.StatusConflict || !strings.Contains(gatedCreate.Body.String(), freshconfig.ReasonManagementRejected) {
			t.Fatalf("gated POST api-keys = %d %s", gatedCreate.Code, gatedCreate.Body.String())
		}
		if extCPA.requests.Load() != reqsAfterFirstRefusal {
			t.Fatalf("gated POST api-keys sent extra management requests: %d -> %d", reqsAfterFirstRefusal, extCPA.requests.Load())
		}
	})

	t.Run("upgrade_external_enforces_base_url_self_consistency_and_error_gates", func(t *testing.T) {
		ctx := t.Context()
		dbPath := filepath.Join(t.TempDir(), "upgraded-external-gates.sqlite")
		db, err := sqlite.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if err := sqlite.Migrate(db); err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector([]byte("upgrade-external-gates-key-32len"))
		if err != nil {
			t.Fatal(err)
		}
		extMgmt := strings.Repeat("bc", 32)
		extCPA := newStatefulExternalCPA(t, extMgmt)
		extCPA.mu.Lock()
		extCPA.clientKeys = []string{"user-ext-gate-key"}
		extCPA.mu.Unlock()

		normalizedURL, err := cpa.NormalizeV8BaseURL(extCPA.server.URL)
		if err != nil {
			t.Fatal(err)
		}
		extIdentity := freshkeyapply.ExternalIdentity(normalizedURL)
		sealedMgmt, err := protector.ProtectString(extMgmt)
		if err != nil {
			t.Fatal(err)
		}
		nowMS := time.Now().UnixMilli()
		bindingTx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := upgradeoperation.RecordExternalBindingTx(ctx, bindingTx, upgradeoperation.ExternalBinding{
			BaseURL:       normalizedURL,
			Identity:      extIdentity,
			Generation:    extIdentity,
			ManagementKey: sealedMgmt,
			ObservedAtMS:  nowMS,
			ClientKeys:    1,
		}); err != nil {
			_ = bindingTx.Rollback()
			t.Fatal(err)
		}
		if err := bindingTx.Commit(); err != nil {
			t.Fatal(err)
		}
		recordCompletedUpgradeVerdict(t, db, model.UpgradeVerification{
			TargetMode:          model.UpgradeTargetExternal,
			SourceFingerprint:   "sha256:" + strings.Repeat("5", 64),
			Domains:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig, model.UpgradeDomainIdentity},
			BindingIdentity:     extIdentity,
			BindingGeneration:   extIdentity,
			BindingObservedAtMS: nowMS,
			ObservedAtMS:        nowMS + 1,
		})
		handler, closeFn, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
			DB:        db,
			Protector: protector,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeFn)
		callExt := func(method, resource, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, currentInstallManagementAPIV2Prefix+""+resource, bytes.NewBufferString(body))
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			handler.serveHTTP(rec, req, resource)
			return rec
		}

		// 1. Drift base_url without changing binding.identity: loadExternalBindingGrant
		// must refuse via ExternalIdentity(normalizedBaseURL) != binding.Identity (409 target_moved),
		// create zero keys, fabricate zero source bindings, and send zero requests to CPA.
		if _, err := db.ExecContext(ctx, `update upgrade_external_connect set base_url = 'https://drifted-external.example' where id = 1`); err != nil {
			t.Fatal(err)
		}
		listMoved := callExt(http.MethodGet, "api-keys", "")
		if listMoved.Code != http.StatusConflict || !strings.Contains(listMoved.Body.String(), freshconfig.ReasonTargetMoved) {
			t.Fatalf("drifted base_url GET api-keys = %d %s, want 409 target_moved", listMoved.Code, listMoved.Body.String())
		}
		createMoved := callExt(http.MethodPost, "api-keys", `{"requestId":"`+strings.Repeat("1", 32)+`"}`)
		if createMoved.Code != http.StatusConflict || !strings.Contains(createMoved.Body.String(), freshconfig.ReasonTargetMoved) {
			t.Fatalf("drifted base_url POST api-keys = %d %s, want 409 target_moved", createMoved.Code, createMoved.Body.String())
		}
		var errBody clientKeyCreateErrorResponse
		if err := json.Unmarshal(createMoved.Body.Bytes(), &errBody); err != nil || errBody.Key != nil {
			t.Fatalf("drifted POST must not create key: err=%v body=%s", err, createMoved.Body.String())
		}
		var keysCreated int
		if err := db.QueryRowContext(ctx, `select count(*) from fresh_client_api_keys`).Scan(&keysCreated); err != nil || keysCreated != 0 {
			t.Fatalf("drifted base_url created %d keys (%v), want 0", keysCreated, err)
		}
		var bindingsOnMoved int
		if err := db.QueryRowContext(ctx, `select count(*) from gateway_api_key_source_bindings`).Scan(&bindingsOnMoved); err != nil || bindingsOnMoved != 0 {
			t.Fatalf("drifted base_url fabricated %d source bindings (%v)", bindingsOnMoved, err)
		}
		movedKeyID := strings.Repeat("a", 32)
		preflightMoved := callExt(http.MethodGet, "api-keys/"+movedKeyID+"/revocation/revoke", "")
		if preflightMoved.Code != http.StatusOK || !strings.Contains(preflightMoved.Body.String(), `"ready":false`) || !strings.Contains(preflightMoved.Body.String(), freshconfig.ReasonTargetMoved) {
			t.Fatalf("drifted base_url preflight = %d %s, want ready:false target_moved", preflightMoved.Code, preflightMoved.Body.String())
		}
		prepareMoved := callExt(http.MethodPost, "api-keys/"+movedKeyID+"/revocation", `{"requestId":"`+strings.Repeat("2", 32)+`","mode":"revoke"}`)
		if prepareMoved.Code != http.StatusConflict || !strings.Contains(prepareMoved.Body.String(), freshconfig.ReasonTargetMoved) {
			t.Fatalf("drifted base_url prepare = %d %s, want 409 target_moved", prepareMoved.Code, prepareMoved.Body.String())
		}
		if extCPA.requests.Load() != 0 {
			t.Fatalf("drifted base_url contacted CPA (%d requests)", extCPA.requests.Load())
		}

		// 2. Restore base_url, drift valid binding (identity == generation != ev.BindingIdentity) -> 409 target_moved, no key created.
		driftedIdentity := freshkeyapply.ExternalIdentity("https://other.example")
		if _, err := db.ExecContext(ctx, `update upgrade_external_connect set base_url = ?, identity = ?, generation = ? where id = 1`,
			normalizedURL, driftedIdentity, driftedIdentity); err != nil {
			t.Fatal(err)
		}
		identMovedList := callExt(http.MethodGet, "api-keys", "")
		if identMovedList.Code != http.StatusConflict || !strings.Contains(identMovedList.Body.String(), freshconfig.ReasonTargetMoved) {
			t.Fatalf("drifted identity GET api-keys = %d %s, want 409 target_moved", identMovedList.Code, identMovedList.Body.String())
		}
		identMoved := callExt(http.MethodPost, "api-keys", `{"requestId":"`+strings.Repeat("3", 32)+`"}`)
		if identMoved.Code != http.StatusConflict || !strings.Contains(identMoved.Body.String(), freshconfig.ReasonTargetMoved) {
			t.Fatalf("drifted identity POST api-keys = %d %s, want 409 target_moved", identMoved.Code, identMoved.Body.String())
		}
		var identErrBody clientKeyCreateErrorResponse
		if err := json.Unmarshal(identMoved.Body.Bytes(), &identErrBody); err != nil || identErrBody.Key != nil {
			t.Fatalf("drifted identity POST must not create key: err=%v body=%s", err, identMoved.Body.String())
		}
		if err := db.QueryRowContext(ctx, `select count(*) from fresh_client_api_keys`).Scan(&keysCreated); err != nil || keysCreated != 0 {
			t.Fatalf("drifted identity created %d keys (%v), want 0", keysCreated, err)
		}

		// 3. Delete ExternalBinding -> read-only list degrades unverified, POST refuses 409 key_operation_host_proof_required without creating key.
		if _, err := db.ExecContext(ctx, `delete from upgrade_external_connect where id = 1`); err != nil {
			t.Fatal(err)
		}
		unavailList := callExt(http.MethodGet, "api-keys", "")
		if unavailList.Code != http.StatusOK || !strings.Contains(unavailList.Body.String(), `"presenceUnverified":true`) {
			t.Fatalf("missing binding GET api-keys = %d %s, want 200 presenceUnverified:true", unavailList.Code, unavailList.Body.String())
		}
		unavailCreate := callExt(http.MethodPost, "api-keys", `{"requestId":"`+strings.Repeat("4", 32)+`"}`)
		if unavailCreate.Code != http.StatusConflict || !strings.Contains(unavailCreate.Body.String(), "key_operation_host_proof_required") {
			t.Fatalf("missing binding POST api-keys = %d %s, want 409 key_operation_host_proof_required", unavailCreate.Code, unavailCreate.Body.String())
		}
		var unavailErrBody clientKeyCreateErrorResponse
		if err := json.Unmarshal(unavailCreate.Body.Bytes(), &unavailErrBody); err != nil || unavailErrBody.Key != nil {
			t.Fatalf("missing binding POST must not create key: err=%v body=%s", err, unavailCreate.Body.String())
		}

		// 4. Corrupt binding row (identity != generation) and SQL read error must fail closed 503 without creating keys or leaking SQL details.
		if _, err := db.ExecContext(ctx,
			`insert into upgrade_external_connect (id, base_url, identity, generation, management_key, observed_at_ms, client_keys) values (1, ?, ?, ?, ?, ?, 1)`,
			normalizedURL, driftedIdentity, extIdentity, sealedMgmt, nowMS); err != nil {
			t.Fatal(err)
		}
		invalidList := callExt(http.MethodGet, "api-keys", "")
		if invalidList.Code != http.StatusServiceUnavailable {
			t.Fatalf("corrupt binding GET api-keys = %d %s, want 503", invalidList.Code, invalidList.Body.String())
		}
		invalidCreate := callExt(http.MethodPost, "api-keys", `{"requestId":"`+strings.Repeat("5", 32)+`"}`)
		if invalidCreate.Code != http.StatusServiceUnavailable || !strings.Contains(invalidCreate.Body.String(), "key_operation_unknown") {
			t.Fatalf("corrupt binding POST api-keys = %d %s, want 503 key_operation_unknown", invalidCreate.Code, invalidCreate.Body.String())
		}
		var invalidErrBody clientKeyCreateErrorResponse
		if err := json.Unmarshal(invalidCreate.Body.Bytes(), &invalidErrBody); err != nil || invalidErrBody.Key != nil {
			t.Fatalf("corrupt binding POST must not create key: err=%v body=%s", err, invalidCreate.Body.String())
		}

		if _, err := db.ExecContext(ctx, `alter table upgrade_external_connect rename to upgrade_external_connect_hidden`); err != nil {
			t.Fatal(err)
		}
		sqlErrList := callExt(http.MethodGet, "api-keys", "")
		if sqlErrList.Code != http.StatusServiceUnavailable || strings.Contains(sqlErrList.Body.String(), "upgrade_external_connect") {
			t.Fatalf("SQL failure GET api-keys = %d %s, want redacted 503", sqlErrList.Code, sqlErrList.Body.String())
		}
		sqlErrCreate := callExt(http.MethodPost, "api-keys", `{"requestId":"`+strings.Repeat("6", 32)+`"}`)
		if sqlErrCreate.Code != http.StatusServiceUnavailable || !strings.Contains(sqlErrCreate.Body.String(), "key_operation_unknown") || strings.Contains(sqlErrCreate.Body.String(), "upgrade_external_connect") {
			t.Fatalf("SQL failure POST api-keys = %d %s, want redacted 503 key_operation_unknown", sqlErrCreate.Code, sqlErrCreate.Body.String())
		}
		var sqlErrBody clientKeyCreateErrorResponse
		if err := json.Unmarshal(sqlErrCreate.Body.Bytes(), &sqlErrBody); err != nil || sqlErrBody.Key != nil {
			t.Fatalf("SQL failure POST must not create key: err=%v body=%s", err, sqlErrCreate.Body.String())
		}

		if err := db.QueryRowContext(ctx, `select count(*) from fresh_client_api_keys`).Scan(&keysCreated); err != nil || keysCreated != 0 {
			t.Fatalf("rejected external authority created %d keys (%v), want 0", keysCreated, err)
		}
		if err := db.QueryRowContext(ctx, `select count(*) from gateway_api_key_source_bindings`).Scan(&bindingsOnMoved); err != nil || bindingsOnMoved != 0 {
			t.Fatalf("rejected external authority fabricated %d source bindings (%v)", bindingsOnMoved, err)
		}
		if extCPA.requests.Load() != 0 {
			t.Fatalf("error gates leaked %d requests to CPA", extCPA.requests.Load())
		}
	})

	t.Run("upgrade_managed_atomic_registration_concurrency_and_runtime_handoff", func(t *testing.T) {
		ctx := t.Context()
		dbPath := filepath.Join(t.TempDir(), "upgraded-managed-handoff.sqlite")
		db, err := sqlite.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if err := sqlite.Migrate(db); err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector([]byte("upgrade-managed-handoff-32bytes!"))
		if err != nil {
			t.Fatal(err)
		}

		migratedSecret := "legacy-v1-client-key-handoff"
		sum := sha256.Sum256([]byte(migratedSecret))
		migratedHash := hex.EncodeToString(sum[:])
		preExistingID := "11223344556677889900aabbccddeeff"
		createdAtMS := time.Now().Add(-10 * time.Minute).UnixMilli()
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 1, 'active', ?, ?)`,
			preExistingID, createdAtMS, createdAtMS); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, observed_runtime_generation, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, ?, ?, 1, ?, ?, null)`,
			preExistingID, "embedded-default", migratedHash, createdAtMS, createdAtMS+1000); err != nil {
			t.Fatal(err)
		}
		// Seed a historical usage event from R1 ("embedded-default") era.
		legacyStore := store.New(db, protector)
		rawR1, _ := json.Marshal(map[string]any{
			"request_id":    "managed-r1-event",
			"timestamp":     strconv.FormatInt(createdAtMS+500, 10),
			"model":         "gpt-4",
			"api_key":       migratedSecret,
			"input_tokens":  10,
			"output_tokens": 5,
			"total_tokens":  15,
		})
		evR1, err := usage.NormalizeRaw(rawR1)
		if err != nil {
			t.Fatal(err)
		}
		if res, err := legacyStore.InsertEvents(ctx, []usage.Event{evR1}); err != nil || res.Inserted != 1 {
			t.Fatalf("insert evR1 = %+v err=%v", res, err)
		}
		dest := newManagedDestination(t, migratedSecret)
		destServer := dest.serve(t)
		destTarget, err := url.Parse(destServer.URL)
		if err != nil {
			t.Fatal(err)
		}
		reverseProxy := httputil.NewSingleHostReverseProxy(destTarget)
		var (
			proveGateOnce atomic.Bool
			proveHits     atomic.Int32
			proveEntered  = make(chan struct{})
			proveRelease  = make(chan struct{})
		)
		proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet && r.URL.Path == "/v8/management/config/access/api-keys" {
				proveHits.Add(1)
				if proveGateOnce.CompareAndSwap(true, false) {
					close(proveEntered)
					<-proveRelease
				}
			}
			reverseProxy.ServeHTTP(w, r)
		}))
		t.Cleanup(proxyServer.Close)

		seedUpgradeManagedImportDestination(
			t, db, protector, proxyServer.URL, dest.management,
			"embedded-default", 7, 1, []string{migratedSecret}, createdAtMS,
		)
		recordCompletedUpgradeVerdict(t, db, model.UpgradeVerification{
			TargetMode:        model.UpgradeTargetManaged,
			SourceFingerprint: "sha256:" + strings.Repeat("6", 64),
			Domains:           []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig},
			ObservedAtMS:      createdAtMS + 2000,
		})

		runtimeToken := "supervisor-token-handoff"
		var runtimeStatus atomic.Value
		runtimeStatus.Store(upgradeManagedRuntimeStatus(t))
		rtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/v1/runtime/status" || r.Header.Get("Authorization") != "Bearer "+runtimeToken {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			st := runtimeStatus.Load().(model.RuntimeObservedStatus)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"protocolVersion":       string(st.ProtocolVersion),
				"runtimeIdentity":       string(st.Identity),
				"runtimeGeneration":     st.Generation,
				"state":                 string(st.State),
				"cpaObservedVersion":    string(st.CPAObservedVersion),
				"activeGatewayArtifact": map[string]any{"engine": st.ActiveGatewayArtifact.Engine, "artifactId": string(st.ActiveGatewayArtifact.ArtifactID), "version": st.ActiveGatewayArtifact.Version},
			})
		}))
		t.Cleanup(rtServer.Close)
		rtClient := runtimeservice.NewEmbeddedClient(rtServer.URL, runtimeToken)

		usageSvc := usageapp.New(usageadapter.New(db), func() usageapp.CollectionPhase { return usageapp.PhaseRunning })
		usageSvc.SetClock(func() int64 { return createdAtMS })
		if err := usageSvc.MarkCollectionStart(ctx); err != nil {
			t.Fatal(err)
		}
		usageSvc.SetClock(func() int64 { return time.Now().UnixMilli() })
		var handler *upgradeKeyWorkspace
		var wakeCount atomic.Int32
		var reentered atomic.Bool
		handler, closeFn, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
			DB:        db,
			Protector: protector,
			Runtime:   rtClient,
			Usage:     usageSvc,
			WakeProjection: func() {
				wakeCount.Add(1)
				if handler != nil && reentered.CompareAndSwap(false, true) {
					// Re-entering workspace methods inside WakeProjection callback must never deadlock.
					if id, err := handler.currentRuntimeIdentity(ctx); err != nil || id == "" {
						t.Errorf("re-entrant currentRuntimeIdentity = %q err=%v", id, err)
					}
				}
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeFn)

		callUpgrade := func(method, resource, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, currentInstallManagementAPIV2Prefix+""+resource, bytes.NewBufferString(body))
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			handler.serveHTTP(rec, req, resource)
			return rec
		}

		// 1. Abort marker write via SQLite trigger: StageAdoptedTx must roll back atomically!
		if _, err := db.ExecContext(ctx, `create trigger test_abort_upgrade_marker
			before insert on settings
			when new.key = '`+upgradeManagedKeysRegisteredKey+`'
			begin
				select raise(abort, 'injected marker failure');
			end`); err != nil {
			t.Fatal(err)
		}
		aborted := callUpgrade(http.MethodGet, "api-keys", "")
		if aborted.Code != http.StatusServiceUnavailable || strings.Contains(aborted.Body.String(), "injected marker failure") {
			t.Fatalf("GET api-keys on marker failure = %d %s, want redacted 503", aborted.Code, aborted.Body.String())
		}
		if wakeCount.Load() != 0 {
			t.Fatalf("failed marker write woke projection %d times", wakeCount.Load())
		}
		var stagedAfterAbort int
		if err := db.QueryRowContext(ctx, `select count(*) from fresh_client_api_keys`).Scan(&stagedAfterAbort); err != nil || stagedAfterAbort != 0 {
			t.Fatalf("StageAdoptedTx did not roll back on marker failure: %d rows (%v)", stagedAfterAbort, err)
		}
		var activeR1AfterAbort int
		if err := db.QueryRowContext(ctx, `select count(*) from gateway_api_key_source_bindings where api_key_id = ? and runtime_identity = 'embedded-default' and retired_at_ms is null`, preExistingID).Scan(&activeR1AfterAbort); err != nil || activeR1AfterAbort != 1 {
			t.Fatalf("BindFreshKeySourcesTx did not roll back cleanly on marker failure: activeR1=%d (%v)", activeR1AfterAbort, err)
		}
		if _, err := db.ExecContext(ctx, `drop trigger test_abort_upgrade_marker`); err != nil {
			t.Fatal(err)
		}

		// 2. Concurrent workspace registration requests behind ProveAdoptedDestination barrier:
		// serialize registration cleanly, run ProveAdoptedDestination once, and wake projection without deadlocking.
		proveHits.Store(0)
		proveGateOnce.Store(true)
		const workers = 4
		results := make(chan *httptest.ResponseRecorder, workers)
		go func() {
			results <- callUpgrade(http.MethodGet, "api-keys", "")
		}()
		<-proveEntered
		for range workers - 1 {
			go func() {
				results <- callUpgrade(http.MethodGet, "api-keys", "")
			}()
		}
		time.Sleep(25 * time.Millisecond)
		if got := proveHits.Load(); got != 1 {
			close(proveRelease)
			t.Fatalf("concurrent registration fired %d parallel ProveAdoptedDestination calls, want 1", got)
		}
		close(proveRelease)
		for range workers {
			res := <-results
			if res.Code != http.StatusOK {
				t.Fatalf("concurrent GET api-keys = %d %s", res.Code, res.Body.String())
			}
		}
		if !reentered.Load() || wakeCount.Load() < 1 {
			t.Fatalf("WakeProjection re-entry check failed (reentered=%v wakeCount=%d)", reentered.Load(), wakeCount.Load())
		}

		// Verify marker includes real runtimeIdentity "embedded-default".
		var rawMarker string
		if err := db.QueryRowContext(ctx, `select value from settings where key = ?`, upgradeManagedKeysRegisteredKey).Scan(&rawMarker); err != nil {
			t.Fatal(err)
		}
		var marker upgradeManagedRegistrationMarker
		if err := json.Unmarshal([]byte(rawMarker), &marker); err != nil || marker.RuntimeIdentity != "embedded-default" {
			t.Fatalf("registration marker = %q (%v), want runtimeIdentity=embedded-default", rawMarker, err)
		}

		// Foreign runtime identity not matching upgrade_cpa_import_destination_v1 must fail closed.
		foreignStatus := upgradeManagedRuntimeStatus(t)
		foreignStatus.Identity = "embedded-foreign"
		foreignStatus.Generation = 8
		runtimeStatus.Store(foreignStatus)
		foreignList := callUpgrade(http.MethodGet, "api-keys", "")
		if foreignList.Code != http.StatusOK || !strings.Contains(foreignList.Body.String(), `"gatewayPending":true`) {
			t.Fatalf("foreign runtime GET api-keys = %d %s, want degraded gatewayPending:true", foreignList.Code, foreignList.Body.String())
		}

		// 3. Legitimate runtime change R1 ("embedded-default") -> R2 ("embedded-next")
		// with committed destination provenance updated for attempt 2: re-registers atomically,
		// preserves preExistingID, performs temporal handoff, and preserves all historical+new usage.
		seedUpgradeManagedImportDestination(
			t, db, protector, proxyServer.URL, dest.management,
			"embedded-next", 8, 2, []string{migratedSecret}, createdAtMS+3000,
		)
		nextStatus := upgradeManagedRuntimeStatus(t)
		nextStatus.Identity = "embedded-next"
		nextStatus.Generation = 8
		runtimeStatus.Store(nextStatus)
		// Concurrent requests across R1 -> R2 transition must preserve one canonical ID and zero ErrSourceOwned.
		var wg sync.WaitGroup
		handoffRes := make([]*httptest.ResponseRecorder, 3)
		for i := range handoffRes {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				handoffRes[idx] = callUpgrade(http.MethodGet, "api-keys", "")
			}(i)
		}
		wg.Wait()
		for i, res := range handoffRes {
			if res.Code != http.StatusOK {
				t.Fatalf("concurrent R1->R2 GET api-keys[%d] = %d %s", i, res.Code, res.Body.String())
			}
		}
		if err := db.QueryRowContext(ctx, `select value from settings where key = ?`, upgradeManagedKeysRegisteredKey).Scan(&rawMarker); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(rawMarker), &marker); err != nil || marker.RuntimeIdentity != "embedded-next" {
			t.Fatalf("post-handoff marker = %q (%v), want runtimeIdentity=embedded-next", rawMarker, err)
		}

		// Verify canonical ID preserved and temporal handoff window [R1: first, handoff) + [R2: handoff, inf).
		var keyCount int
		if err := db.QueryRowContext(ctx, `select count(*) from fresh_client_api_keys where api_key_id = ?`, preExistingID).Scan(&keyCount); err != nil || keyCount != 1 {
			t.Fatalf("canonical key rows = %d (%v), want 1", keyCount, err)
		}
		type bindingRow struct {
			runtimeIdentity string
			firstSeenAtMS   int64
			retiredAtMS     sql.NullInt64
		}
		rows, err := db.QueryContext(ctx, `select runtime_identity, first_seen_at_ms, retired_at_ms
			from gateway_api_key_source_bindings where api_key_id = ? order by binding_id asc`, preExistingID)
		if err != nil {
			t.Fatal(err)
		}
		var bindings []bindingRow
		for rows.Next() {
			var b bindingRow
			if err := rows.Scan(&b.runtimeIdentity, &b.firstSeenAtMS, &b.retiredAtMS); err != nil {
				_ = rows.Close()
				t.Fatal(err)
			}
			bindings = append(bindings, b)
		}
		_ = rows.Close()
		if len(bindings) != 2 {
			t.Fatalf("bindings after R1->R2 = %+v, want 2 rows (retired R1 + active R2)", bindings)
		}
		if bindings[0].runtimeIdentity != "embedded-default" || !bindings[0].retiredAtMS.Valid ||
			bindings[1].runtimeIdentity != "embedded-next" || bindings[1].retiredAtMS.Valid ||
			bindings[1].firstSeenAtMS != bindings[0].retiredAtMS.Int64 {
			t.Fatalf("temporal handoff bindings invalid: %+v", bindings)
		}

		// Record a new usage event in R2 era (>= handoffAtMS) and verify BOTH R1 and R2 events
		// project as mapped to the SAME preExistingID with zero ambiguity.
		r2TS := bindings[1].firstSeenAtMS
		rawR2, _ := json.Marshal(map[string]any{
			"request_id":    "managed-r2-event",
			"timestamp":     strconv.FormatInt(r2TS, 10),
			"model":         "gpt-4",
			"api_key":       migratedSecret,
			"input_tokens":  20,
			"output_tokens": 10,
			"total_tokens":  30,
		})
		evR2, err := usage.NormalizeRaw(rawR2)
		if err != nil {
			t.Fatal(err)
		}
		if res, err := legacyStore.InsertEvents(ctx, []usage.Event{evR2}); err != nil || res.Inserted != 1 {
			t.Fatalf("insert evR2 = %+v err=%v", res, err)
		}
		projRepo := adaptersqliteidentityprojection.New(db)
		if _, err := projRepo.CatchUp(ctx, 100, time.Now().UnixMilli()); err != nil {
			t.Fatal(err)
		}
		var mappedCount int
		if err := db.QueryRowContext(ctx, `select count(*) from gateway_usage_identity_projection_v1 where api_key_state = 'mapped' and api_key_id = ?`, preExistingID).Scan(&mappedCount); err != nil || mappedCount != 2 {
			t.Fatalf("mapped events across R1+R2 = %d (%v), want 2", mappedCount, err)
		}
		for time.Now().UnixMilli() <= evR2.TimestampMS {
			time.Sleep(time.Millisecond)
		}
		detailRes := callUpgrade(http.MethodGet, "api-keys/"+preExistingID+"/usage", "")
		if detailRes.Code != http.StatusOK {
			t.Fatalf("GET handoff key usage = %d %s", detailRes.Code, detailRes.Body.String())
		}
		detail := decodeUsage[usageapp.DetailResponse](t, detailRes)
		if detail.Totals == nil || detail.Totals.Calls != 2 || detail.Totals.TotalTokens != 45 {
			t.Fatalf("R1+R2 combined usage totals = %+v, want calls=2 totalTokens=45", detail)
		}

		// 4. Genuine runtime unavailability degrades read-only GET api-keys to gatewayPending:true,
		// whereas corrupt marker / SQL failures must still fail closed 503.
		stoppedStatus := nextStatus
		stoppedStatus.State = model.RuntimeStateOffline
		runtimeStatus.Store(stoppedStatus)
		unavailList := callUpgrade(http.MethodGet, "api-keys", "")
		if unavailList.Code != http.StatusOK {
			t.Fatalf("runtime-stopped GET api-keys = %d %s, want 200", unavailList.Code, unavailList.Body.String())
		}
		var unavailBody struct {
			Keys                []freshkeys.Key `json:"keys"`
			GatewayPending      bool            `json:"gatewayPending"`
			RevocationAvailable bool            `json:"revocationAvailable"`
			PresenceUnverified  bool            `json:"presenceUnverified"`
		}
		if err := json.Unmarshal(unavailList.Body.Bytes(), &unavailBody); err != nil ||
			!unavailBody.GatewayPending || unavailBody.RevocationAvailable || !unavailBody.PresenceUnverified ||
			len(unavailBody.Keys) != 1 || string(unavailBody.Keys[0].ID) != preExistingID || unavailBody.Keys[0].RuntimeState != "effect_unknown" {
			t.Fatalf("runtime-stopped GET api-keys body = %+v (%v)", unavailBody, err)
		}

		runtimeStatus.Store(nextStatus)
		if _, err := db.ExecContext(ctx, `update settings set value = '{invalid-json' where key = ?`, upgradeManagedKeysRegisteredKey); err != nil {
			t.Fatal(err)
		}
		corruptMarker := callUpgrade(http.MethodGet, "api-keys", "")
		if corruptMarker.Code != http.StatusServiceUnavailable {
			t.Fatalf("corrupt marker GET api-keys = %d %s, want 503", corruptMarker.Code, corruptMarker.Body.String())
		}
		if _, err := db.ExecContext(ctx, `alter table settings rename to settings_hidden`); err != nil {
			t.Fatal(err)
		}
		sqlErrList := callUpgrade(http.MethodGet, "api-keys", "")
		if sqlErrList.Code != http.StatusServiceUnavailable || strings.Contains(sqlErrList.Body.String(), "settings") {
			t.Fatalf("settings SQL failure GET api-keys = %d %s, want redacted 503", sqlErrList.Code, sqlErrList.Body.String())
		}
	})

	t.Run("upgrade_managed_recovers_pending_journal_across_supervisor_restart_and_enforces_provenance", func(t *testing.T) {
		ctx := t.Context()
		dbPath := filepath.Join(t.TempDir(), "upgraded-managed-restart-journal.sqlite")
		db, err := sqlite.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if err := sqlite.Migrate(db); err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector([]byte("upgrade-managed-restart-journ-32"))
		if err != nil {
			t.Fatal(err)
		}

		migratedSecret := "legacy-v1-restart-alpha"
		dest := newManagedDestination(t, migratedSecret)
		destBase := dest.serve(t)
		destURL, err := url.Parse(destBase.URL)
		if err != nil {
			t.Fatal(err)
		}
		var failPreWrite atomic.Bool
		proxy := httputil.NewSingleHostReverseProxy(destURL)
		proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if failPreWrite.Load() && r.Method == http.MethodGet && r.URL.Path == "/v8/management/config" {
				http.Error(w, "temporary cpa snapshot failure", http.StatusInternalServerError)
				return
			}
			proxy.ServeHTTP(w, r)
		}))
		t.Cleanup(proxyServer.Close)

		nowMS := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
		seedUpgradeManagedImportDestination(
			t, db, protector, proxyServer.URL, dest.management,
			"embedded-default", 7, 1, []string{migratedSecret}, nowMS,
		)
		recordCompletedUpgradeVerdict(t, db, model.UpgradeVerification{
			TargetMode:        model.UpgradeTargetManaged,
			SourceFingerprint: "sha256:" + strings.Repeat("7", 64),
			Domains:           []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig},
			ObservedAtMS:      nowMS + 1000,
		})

		runtimeToken := "supervisor-token-restart-journal"
		var runtimeStatus atomic.Value
		initialStatus := upgradeManagedRuntimeStatus(t)
		initialStatus.Generation = 7
		runtimeStatus.Store(initialStatus)
		rtServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/v1/runtime/status" || r.Header.Get("Authorization") != "Bearer "+runtimeToken {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			st := runtimeStatus.Load().(model.RuntimeObservedStatus)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"protocolVersion":       string(st.ProtocolVersion),
				"runtimeIdentity":       string(st.Identity),
				"runtimeGeneration":     st.Generation,
				"state":                 string(st.State),
				"cpaObservedVersion":    string(st.CPAObservedVersion),
				"activeGatewayArtifact": map[string]any{"engine": st.ActiveGatewayArtifact.Engine, "artifactId": string(st.ActiveGatewayArtifact.ArtifactID), "version": st.ActiveGatewayArtifact.Version},
			})
		}))
		t.Cleanup(rtServer.Close)
		rtClient := runtimeservice.NewEmbeddedClient(rtServer.URL, runtimeToken)

		handler, closeFn, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
			DB:        db,
			Protector: protector,
			Runtime:   rtClient,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeFn)
		callWS := func(ws *upgradeKeyWorkspace, method, resource, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, currentInstallManagementAPIV2Prefix+resource, bytes.NewBufferString(body))
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			ws.serveHTTP(rec, req, resource)
			return rec
		}

		// Create a second key while Supervisor is at Generation 7.
		createRes := callWS(handler, http.MethodPost, "api-keys", `{"requestId":"`+strings.Repeat("a", 32)+`"}`)
		if createRes.Code != http.StatusCreated {
			t.Fatalf("POST api-keys = %d %s, want 201", createRes.Code, createRes.Body.String())
		}
		var created clientKeyCreateResponse
		if err := json.Unmarshal(createRes.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}

		// Prepare a revocation journal on Generation 7, then fail its first continuation so
		// the durable operation journal remains non-terminal across a Supervisor + Manager restart.
		prepRes := callWS(handler, http.MethodPost, "api-keys/"+string(created.Key.ID)+"/revocation",
			`{"requestId":"`+strings.Repeat("b", 32)+`","mode":"revoke"}`)
		if prepRes.Code != http.StatusCreated && prepRes.Code != http.StatusOK {
			t.Fatalf("prepare revocation = %d %s", prepRes.Code, prepRes.Body.String())
		}
		var prepBody struct {
			Operation *keyrepo.Operation `json:"operation"`
		}
		if err := json.Unmarshal(prepRes.Body.Bytes(), &prepBody); err != nil || prepBody.Operation == nil {
			t.Fatalf("decode prepared operation: %v body=%s", err, prepRes.Body.String())
		}
		opID := prepBody.Operation.ID

		if missingCT := callWS(handler, http.MethodPost, "api-keys/operations/"+opID+"/continue", ""); missingCT.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("POST continue without Content-Type = %d %s, want 415", missingCT.Code, missingCT.Body.String())
		}

		failPreWrite.Store(true)
		firstContinue := callWS(handler, http.MethodPost, "api-keys/operations/"+opID+"/continue", `{}`)
		if firstContinue.Code == http.StatusOK || firstContinue.Code == http.StatusUnsupportedMediaType {
			t.Fatalf("continue during pre-write CPA failure = %d %s, want non-200 refusal", firstContinue.Code, firstContinue.Body.String())
		}

		// Simulate Supervisor restart with a smaller random non-zero runtimeGeneration (2),
		// then a larger random runtimeGeneration (987654321), plus a fresh Manager workspace instance.
		restartedLow := initialStatus
		restartedLow.Generation = 2
		runtimeStatus.Store(restartedLow)
		handler2, closeFn2, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
			DB:        db,
			Protector: protector,
			Runtime:   rtClient,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeFn2)

		checkAfterLowRestart := callWS(handler2, http.MethodGet, "api-keys/operations/"+opID, "")
		if checkAfterLowRestart.Code != http.StatusOK {
			t.Fatalf("GET operation after low-gen Supervisor restart = %d %s, want 200 (no ErrGeneration)", checkAfterLowRestart.Code, checkAfterLowRestart.Body.String())
		}

		restartedHigh := initialStatus
		restartedHigh.Generation = 987654321
		runtimeStatus.Store(restartedHigh)
		failPreWrite.Store(false)

		continueAfterRestart := callWS(handler2, http.MethodPost, "api-keys/operations/"+opID+"/continue", `{}`)
		if continueAfterRestart.Code != http.StatusOK || !strings.Contains(continueAfterRestart.Body.String(), `"outcome":"committed"`) {
			t.Fatalf("POST continue after Supervisor restart = %d %s, want 200 committed", continueAfterRestart.Code, continueAfterRestart.Body.String())
		}

		// Verify legacy setting alignment and provenance gates.
		var rawImport string
		if err := db.QueryRowContext(ctx, `select value from settings where key = ?`, upgradeImportDestinationSettingKey).Scan(&rawImport); err != nil {
			t.Fatal(err)
		}
		// Aligned legacy upgrade_cpa_destination alongside upgrade_cpa_import_destination_v1 is accepted.
		if _, err := db.ExecContext(ctx, `insert into settings (key, value, updated_at_ms) values (?, ?, ?)`,
			upgradeDestinationSettingKey, rawImport, nowMS+2000); err != nil {
			t.Fatal(err)
		}
		if alignedList := callWS(handler2, http.MethodGet, "api-keys", ""); alignedList.Code != http.StatusOK || strings.Contains(alignedList.Body.String(), `"gatewayPending":true`) {
			t.Fatalf("aligned legacy destination GET api-keys = %d %s, want verified 200", alignedList.Code, alignedList.Body.String())
		}

		// Misaligned legacy upgrade_cpa_destination fails closed.
		if _, err := db.ExecContext(ctx, `update settings set value = ? where key = ?`,
			strings.Replace(rawImport, `"attempt":1`, `"attempt":2`, 1), upgradeDestinationSettingKey); err != nil {
			t.Fatal(err)
		}
		if misalignedCreate := callWS(handler2, http.MethodPost, "api-keys", `{"requestId":"`+strings.Repeat("c", 32)+`"}`); misalignedCreate.Code != http.StatusServiceUnavailable {
			t.Fatalf("misaligned legacy destination POST api-keys = %d %s, want 503", misalignedCreate.Code, misalignedCreate.Body.String())
		}
		if _, err := db.ExecContext(ctx, `delete from settings where key = ?`, upgradeDestinationSettingKey); err != nil {
			t.Fatal(err)
		}

		// Unqualified expectedActiveArtifactId in upgrade_cpa_import_destination_v1 fails closed.
		var doc map[string]any
		if err := json.Unmarshal([]byte(rawImport), &doc); err != nil {
			t.Fatal(err)
		}
		doc["intent"].(map[string]any)["expectedActiveArtifactId"] = "sha256:" + strings.Repeat("0", 64)
		badBytes, _ := json.Marshal(doc)
		if _, err := db.ExecContext(ctx, `update settings set value = ? where key = ?`, string(badBytes), upgradeImportDestinationSettingKey); err != nil {
			t.Fatal(err)
		}
		if badArtifactCreate := callWS(handler2, http.MethodPost, "api-keys", `{"requestId":"`+strings.Repeat("d", 32)+`"}`); badArtifactCreate.Code != http.StatusServiceUnavailable {
			t.Fatalf("unqualified artifact POST api-keys = %d %s, want 503", badArtifactCreate.Code, badArtifactCreate.Body.String())
		}

		// Restore valid destination, then mismatch upgrade_operations.source_fingerprint_after -> 409 Conflict.
		if _, err := db.ExecContext(ctx, `update settings set value = ? where key = ?`, rawImport, upgradeImportDestinationSettingKey); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `update upgrade_operations set source_fingerprint_after = 'sha256:tampered' where id = 1`); err != nil {
			t.Fatal(err)
		}
		if badVerdict := callWS(handler2, http.MethodGet, "api-keys", ""); badVerdict.Code != http.StatusConflict {
			t.Fatalf("tampered verdict fingerprint GET api-keys = %d %s, want 409", badVerdict.Code, badVerdict.Body.String())
		}
	})

	t.Run("upgrade_external_enforces_refused_gate_observed_at_and_canonical_url_identity", func(t *testing.T) {
		ctx := t.Context()
		dbPath := filepath.Join(t.TempDir(), "upgraded-external-refused.sqlite")
		db, err := sqlite.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if err := sqlite.Migrate(db); err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector([]byte("upgrade-external-refused-gate-32"))
		if err != nil {
			t.Fatal(err)
		}

		extMgmt := strings.Repeat("ab", 32)
		extCPA := newStatefulExternalCPA(t, extMgmt)
		nonCanonicalURL := strings.Replace(extCPA.server.URL, "http://127.0.0.1:", "HTTP://127.0.0.1:", 1) + "/"
		normalizedURL, err := cpa.NormalizeV8BaseURL(nonCanonicalURL)
		if err != nil {
			t.Fatal(err)
		}
		canonicalURL, err := canonicalUpgradeUsageURL(normalizedURL)
		if err != nil {
			t.Fatal(err)
		}
		extIdentity := freshkeyapply.ExternalIdentity(canonicalURL)
		sealedMgmt, err := protector.ProtectString(extMgmt)
		if err != nil {
			t.Fatal(err)
		}
		nowMS := time.Now().UnixMilli()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := upgradeoperation.RecordExternalBindingTx(ctx, tx, upgradeoperation.ExternalBinding{
			BaseURL:       nonCanonicalURL,
			Identity:      extIdentity,
			Generation:    extIdentity,
			ManagementKey: sealedMgmt,
			ObservedAtMS:  nowMS,
			ClientKeys:    1,
		}); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		recordCompletedUpgradeVerdict(t, db, model.UpgradeVerification{
			TargetMode:          model.UpgradeTargetExternal,
			SourceFingerprint:   "sha256:" + strings.Repeat("8", 64),
			Domains:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig, model.UpgradeDomainIdentity},
			BindingIdentity:     extIdentity,
			BindingGeneration:   extIdentity,
			BindingObservedAtMS: nowMS,
			ObservedAtMS:        nowMS + 1,
		})

		handler, closeFn, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
			DB:        db,
			Protector: protector,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeFn)
		callExt := func(method, resource, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, currentInstallManagementAPIV2Prefix+resource, bytes.NewBufferString(body))
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			handler.serveHTTP(rec, req, resource)
			return rec
		}

		// Non-canonical input URL normalizes to canonical ExternalIdentity cleanly.
		if okRes := callExt(http.MethodGet, "api-keys", ""); okRes.Code != http.StatusOK {
			t.Fatalf("canonicalized External GET api-keys = %d %s, want 200", okRes.Code, okRes.Body.String())
		}

		// Latch durable credential refusal: resolveExternalInstall and HTTP endpoints must honor grant.Refused
		// with zero CPA requests.
		extCPA.requests.Store(0)
		if err := handler.rejection.record(ctx, extIdentity, envelopeDigest(sealedMgmt)); err != nil {
			t.Fatal(err)
		}
		if _, err := handler.resolveExternalInstall(ctx); err == nil || !strings.Contains(err.Error(), cpa.ErrManagementRejected.Error()) {
			t.Fatalf("resolveExternalInstall when refused = %v, want ErrManagementRejected", err)
		}
		refusedList := callExt(http.MethodGet, "api-keys", "")
		if refusedList.Code != http.StatusConflict || !strings.Contains(refusedList.Body.String(), freshconfig.ReasonManagementRejected) {
			t.Fatalf("refused External GET api-keys = %d %s, want 409 management_key_rejected", refusedList.Code, refusedList.Body.String())
		}
		refusedCreate := callExt(http.MethodPost, "api-keys", `{"requestId":"`+strings.Repeat("e", 32)+`"}`)
		if refusedCreate.Code != http.StatusConflict || !strings.Contains(refusedCreate.Body.String(), freshconfig.ReasonManagementRejected) {
			t.Fatalf("refused External POST api-keys = %d %s, want 409 management_key_rejected", refusedCreate.Code, refusedCreate.Body.String())
		}
		if extCPA.requests.Load() != 0 {
			t.Fatalf("refused External gate leaked %d CPA requests, want 0", extCPA.requests.Load())
		}

		// Clear refusal, then drift binding.ObservedAtMS away from verification.BindingObservedAtMS -> 409 target_moved.
		if err := handler.rejection.clear(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `update upgrade_external_connect set observed_at_ms = observed_at_ms + 1000 where id = 1`); err != nil {
			t.Fatal(err)
		}
		obsMoved := callExt(http.MethodGet, "api-keys", "")
		if obsMoved.Code != http.StatusConflict || !strings.Contains(obsMoved.Body.String(), freshconfig.ReasonTargetMoved) {
			t.Fatalf("observed_at_ms drift GET api-keys = %d %s, want 409 target_moved", obsMoved.Code, obsMoved.Body.String())
		}
		if extCPA.requests.Load() != 0 {
			t.Fatalf("observed_at_ms drift leaked %d CPA requests, want 0", extCPA.requests.Load())
		}
	})
}

func upgradeManagedRuntimeStatus(t *testing.T) model.RuntimeObservedStatus {
	t.Helper()
	artifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
	if err != nil {
		t.Fatal(err)
	}
	return model.RuntimeObservedStatus{
		Identity:              "embedded-default",
		Generation:            7,
		ProtocolVersion:       "v1",
		State:                 model.RuntimeStateReady,
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: artifact},
	}
}

func seedUpgradeManagedImportDestination(
	t *testing.T,
	db *sql.DB,
	protector *security.Protector,
	baseURL string,
	mgmtSecret string,
	runtimeIdentity model.RuntimeIdentity,
	runtimeGeneration model.RuntimeGeneration,
	attempt int,
	apiKeys []string,
	updatedAtMS int64,
) {
	t.Helper()
	sealedMgmt, err := protector.ProtectString(mgmtSecret)
	if err != nil {
		t.Fatal(err)
	}
	qualifiedArtifact, err := freshstrategy.QualifiedUpgradeAdoptionArtifactID()
	if err != nil {
		t.Fatal(err)
	}
	sourceIdentity := "external:sha256:" + strings.Repeat("c", 64)
	pluginsEnabled := true
	payload, err := json.Marshal(upgradeDestinationRecord{
		BaseURL:           baseURL,
		URLProved:         true,
		ManagementKey:     sealedMgmt,
		ServerFingerprint: "external:sha256:" + strings.Repeat("b", 64),
		OperationID:       upgradeManagedDestinationOperationID(sourceIdentity, attempt),
		Attempt:           attempt,
		Intent: upgradeDestinationIntent{
			ExpectedRuntimeIdentity:   runtimeIdentity,
			ExpectedRuntimeGeneration: runtimeGeneration,
			ExpectedActiveArtifactID:  qualifiedArtifact,
			SourceIdentity:            sourceIdentity,
			SourceClass:               upgradeManagedDestinationClass,
			APIKeys:                   slices.Clone(apiKeys),
			PluginsEnabled:            &pluginsEnabled,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(),
		`insert into settings (key, value, updated_at_ms) values (?, ?, ?)
		 on conflict (key) do update set value = excluded.value, updated_at_ms = excluded.updated_at_ms`,
		upgradeImportDestinationSettingKey, string(payload), updatedAtMS,
	); err != nil {
		t.Fatal(err)
	}
}

func newManagedDestination(t *testing.T, initialKey string) *managedKeyDestination {
	t.Helper()
	d := &managedKeyDestination{
		management: strings.Repeat("f", 64),
		keys:       []string{initialKey},
	}
	return d
}

func recordCompletedUpgradeVerdict(t *testing.T, db *sql.DB, verification model.UpgradeVerification) {
	t.Helper()
	ctx := context.Background()
	repo := upgradeoperation.New(db)
	recorded, err := repo.Advance(ctx, model.UpgradeDomainSourceCopy, model.UpgradeStageCopyIn)
	if err != nil {
		t.Fatal(err)
	}
	fpr := strings.TrimSpace(verification.SourceFingerprint)
	if fpr == "" {
		fpr = "sha256:" + strings.Repeat("2", 64)
		verification.SourceFingerprint = fpr
	}
	recorded.SourceKind = legacypreflight.SourceKindUpgradedLegacy
	recorded.SourceVersion = "v1.14.7"
	recorded.SourceRoot = "/srv/cpamp/data"
	recorded.SourceFingerprintBefore = "sha256:" + strings.Repeat("1", 64)
	recorded.SourceFingerprintAfter = fpr
	recorded.BackupManifestSHA256 = strings.Repeat("3", 64)
	recorded.DomainsDone = slices.Clone(verification.Domains)
	if err := repo.Put(ctx, recorded); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := upgradeoperation.RecordCompletionTx(ctx, tx, verification); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptMultiKeyAbove128ListAddAndRevoke(t *testing.T) {
	// 135 source credentials, past the retired 128-entry adoption bound. They are
	// not canonical Manager shapes because an adopted destination carries the
	// previous CPA's own key shapes, and the adoption policy accepts them.
	sourceKeys := make([]string, 0, 135)
	for i := range 135 {
		sourceKeys = append(sourceKeys, "adopted-source-credential-"+strconv.Itoa(i))
	}
	d := &managedKeyDestination{
		management: strings.Repeat("e", 64),
		keys:       slices.Clone(sourceKeys),
	}
	server := d.serve(t)
	// The transport is the production one, with the adopted destination posture
	// the adoption path proves (not the New Managed default), so the 135-entry
	// list is judged by the policy a real adoption is judged by.
	transport := cpa.NewTransportForEndpoint(server.URL)
	transport.SetPostureSelector(func() (cpa.DestinationPosture, bool) {
		return cpa.PostureFor(string(bootstrap.AdoptExisting))
	})
	snap, err := transport.KeySnapshot(t.Context(), d.management)
	if err != nil || len(snap.Keys) != 135 {
		t.Fatalf("KeySnapshot on 135 adopted keys = len %d, err %v", len(snap.Keys), err)
	}
	// Every adopted credential is a working client credential after adoption: the
	// real auth middleware path must accept all 135, which it only can if no count
	// bound survives in the sampling path either.
	for index, key := range snap.Keys {
		if key != sourceKeys[index] {
			t.Fatalf("KeySnapshot[%d] = %q, want order-preserving %q", index, key, sourceKeys[index])
		}
	}
	ok, err := transport.SampleKeySet(t.Context(), snap.Keys, nil)
	if err != nil || !ok {
		t.Fatalf("SampleKeySet on 135 adopted keys = %t, %v", ok, err)
	}
	// A write of the whole retained set plus one added key is accepted, so the
	// workspace can add a third and further keys without truncation.
	withNew := append(slices.Clone(snap.Keys), "cpamp_sk_"+strings.Repeat("0", 64))
	if !transport.ValidKeySet(withNew) {
		t.Fatal("adopted policy refused 136 keys, a retained artificial total bound")
	}
}
