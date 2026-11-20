package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	usageadapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identityusage"
	usageapp "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/application/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	keyrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/upgradeoperation"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/cpa"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeyapply"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
	runtimeservice "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/runtime"
)

func TestFreshManagedNamedCreateRegenerateRecoveryAndRevokedState(t *testing.T) {
	harness := newManagedInstallHarness(t)

	// Unauthenticated request to regenerate must fail closed with 401.
	unauthReq := httptest.NewRequest(http.MethodPost, currentInstallManagementAPIV2Prefix+"api-keys/"+strings.Repeat("a", 32)+"/regenerate", bytes.NewBufferString(`{"requestId":"`+strings.Repeat("1", 32)+`"}`))
	unauthReq.Header.Set("Content-Type", "application/json")
	unauthRes := httptest.NewRecorder()
	harness.app.canonicalAPI(unauthRes, unauthReq)
	if unauthRes.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated regenerate = %d, want 401", unauthRes.Code)
	}

	// List initial installer key and regenerate it in place while it is the ONLY key on CPA.
	list0 := harness.call(t, http.MethodGet, "api-keys", "")
	if list0.Code != http.StatusOK {
		t.Fatalf("GET api-keys = %d %s", list0.Code, list0.Body.String())
	}
	var listBody struct {
		Keys []freshkeys.Key `json:"keys"`
	}
	if err := json.Unmarshal(list0.Body.Bytes(), &listBody); err != nil || len(listBody.Keys) != 1 {
		t.Fatalf("initial keys = %+v (%v)", listBody, err)
	}
	installerKey := listBody.Keys[0]

	// Missing Content-Type -> 415; Content-Type application/json with empty body / missing requestId / unknown fields -> 400.
	if missingCT := harness.call(t, http.MethodPost, "api-keys/"+string(installerKey.ID)+"/regenerate", ""); missingCT.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("missing Content-Type regenerate = %d, want 415", missingCT.Code)
	}
	emptyJSONReq := httptest.NewRequest(http.MethodPost, currentInstallManagementAPIV2Prefix+"api-keys/"+string(installerKey.ID)+"/regenerate", bytes.NewBufferString(""))
	emptyJSONReq.Header.Set("Authorization", "Bearer "+strategyTestAdmin)
	emptyJSONReq.Header.Set("Content-Type", "application/json")
	emptyJSONRes := httptest.NewRecorder()
	harness.app.canonicalAPI(emptyJSONRes, emptyJSONReq)
	if emptyJSONRes.Code != http.StatusBadRequest {
		t.Fatalf("empty JSON regenerate body = %d, want 400", emptyJSONRes.Code)
	}
	if emptyObj := harness.call(t, http.MethodPost, "api-keys/"+string(installerKey.ID)+"/regenerate", `{}`); emptyObj.Code != http.StatusBadRequest {
		t.Fatalf("empty object regenerate body = %d, want 400", emptyObj.Code)
	}
	if bad := harness.call(t, http.MethodPost, "api-keys/"+string(installerKey.ID)+"/regenerate", `{"requestId":"`+strings.Repeat("1", 32)+`","extra":true}`); bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown field regenerate = %d, want 400", bad.Code)
	}

	rotInstallerReq := strings.Repeat("1", 32)
	rotInstallerRes := harness.call(t, http.MethodPost, "api-keys/"+string(installerKey.ID)+"/regenerate", `{"requestId":"`+rotInstallerReq+`"}`)
	if rotInstallerRes.Code != http.StatusOK {
		t.Fatalf("single-key regenerate = %d %s", rotInstallerRes.Code, rotInstallerRes.Body.String())
	}
	var rotInstallerBody clientKeyRegenerateResponse
	if err := json.Unmarshal(rotInstallerRes.Body.Bytes(), &rotInstallerBody); err != nil {
		t.Fatal(err)
	}
	if rotInstallerBody.CreatedSecret == "" ||
		rotInstallerBody.Key.ID != installerKey.ID ||
		rotInstallerBody.Key.Origin != freshkeys.OriginInstaller ||
		rotInstallerBody.Key.Name != installerKey.Name ||
		rotInstallerBody.Key.NameRevision != installerKey.NameRevision ||
		rotInstallerBody.Key.CreatedAtMS != installerKey.CreatedAtMS ||
		rotInstallerBody.Key.MaskedPreview != model.MaskedClientKey(rotInstallerBody.CreatedSecret) ||
		rotInstallerBody.Key.RuntimeState != "effective_observed" ||
		rotInstallerBody.Key.LastRotatedAtMS <= 0 {
		t.Fatalf("regenerated installer key = %+v", rotInstallerBody)
	}

	// Idempotent replay of the same requestId returns 200 OK without CreatedSecret.
	replayRes := harness.call(t, http.MethodPost, "api-keys/"+string(installerKey.ID)+"/regenerate", `{"requestId":"`+rotInstallerReq+`"}`)
	if replayRes.Code != http.StatusOK {
		t.Fatalf("replay regenerate = %d %s", replayRes.Code, replayRes.Body.String())
	}
	var replayBody clientKeyRegenerateResponse
	if err := json.Unmarshal(replayRes.Body.Bytes(), &replayBody); err != nil {
		t.Fatal(err)
	}
	if replayBody.CreatedSecret != "" || replayBody.Key.ID != installerKey.ID || replayBody.Key.MaskedPreview != rotInstallerBody.Key.MaskedPreview {
		t.Fatalf("replay regenerate body = %+v", replayBody)
	}

	// POST api-keys with atomic name (and 400 on invalid control-character name).
	if badCreate := harness.call(t, http.MethodPost, "api-keys", `{"requestId":"`+strings.Repeat("2", 32)+`","name":"bad\nname"}`); badCreate.Code != http.StatusBadRequest {
		t.Fatalf("invalid create name = %d, want 400", badCreate.Code)
	}
	namedCreateReq := strings.Repeat("2", 32)
	namedCreateRes := harness.call(t, http.MethodPost, "api-keys", `{"requestId":"`+namedCreateReq+`","name":"  Billing Worker  "}`)
	if namedCreateRes.Code != http.StatusCreated {
		t.Fatalf("named POST api-keys = %d %s", namedCreateRes.Code, namedCreateRes.Body.String())
	}
	var namedCreated clientKeyCreateResponse
	if err := json.Unmarshal(namedCreateRes.Body.Bytes(), &namedCreated); err != nil {
		t.Fatal(err)
	}
	if namedCreated.Key.Name != "Billing Worker" || namedCreated.Key.NameRevision != 1 || namedCreated.Key.RuntimeState != "effective_observed" {
		t.Fatalf("named created key = %+v", namedCreated)
	}

	// Simulate CPA management outage during regenerate: candidate is cleaned up when Prepare fails before BeginOperation,
	// error envelope includes code + key, and subsequent regenerate succeeds cleanly.
	harness.destination.mu.Lock()
	savedMgmt := harness.destination.management
	harness.destination.management = "outage-mismatch-token"
	harness.destination.mu.Unlock()

	midFlightReq := strings.Repeat("3", 32)
	midFlightRes := harness.call(t, http.MethodPost, "api-keys/"+string(namedCreated.Key.ID)+"/regenerate", `{"requestId":"`+midFlightReq+`"}`)
	if midFlightRes.Code != http.StatusServiceUnavailable && midFlightRes.Code != http.StatusConflict {
		t.Fatalf("outage regenerate = %d %s, want 409/503", midFlightRes.Code, midFlightRes.Body.String())
	}
	var errEnv clientKeyCreateErrorResponse
	if err := json.Unmarshal(midFlightRes.Body.Bytes(), &errEnv); err != nil || errEnv.Code == "" || errEnv.Key == nil {
		t.Fatalf("outage error envelope = %+v (%v) raw=%s", errEnv, err, midFlightRes.Body.String())
	}

	harness.destination.mu.Lock()
	harness.destination.management = savedMgmt
	harness.destination.mu.Unlock()

	// Verify normal multi-key regenerate preserves order and secret reveal returns rotated secret.
	rotNamedReq := strings.Repeat("4", 32)
	rotNamedRes := harness.call(t, http.MethodPost, "api-keys/"+string(namedCreated.Key.ID)+"/regenerate", `{"requestId":"`+rotNamedReq+`"}`)
	if rotNamedRes.Code != http.StatusOK {
		t.Fatalf("multi-key regenerate = %d %s", rotNamedRes.Code, rotNamedRes.Body.String())
	}
	var rotNamedBody clientKeyRegenerateResponse
	if err := json.Unmarshal(rotNamedRes.Body.Bytes(), &rotNamedBody); err != nil {
		t.Fatal(err)
	}
	if rotNamedBody.Key.Name != "Billing Worker" || rotNamedBody.Key.NameRevision != 1 || rotNamedBody.CreatedSecret == "" {
		t.Fatalf("rotated named key = %+v", rotNamedBody)
	}
	if got := harness.destination.installed(); !slices.Equal(got, []string{rotInstallerBody.CreatedSecret, rotNamedBody.CreatedSecret}) {
		t.Fatalf("CPA keys = %v, want [%s, %s]", got, rotInstallerBody.CreatedSecret, rotNamedBody.CreatedSecret)
	}
	revealRes := harness.call(t, http.MethodPost, "api-keys/"+string(namedCreated.Key.ID)+"/secret", `{}`)
	if revealRes.Code != http.StatusOK || !strings.Contains(revealRes.Body.String(), rotNamedBody.CreatedSecret) {
		t.Fatalf("secret reveal after regenerate = %d %s", revealRes.Code, revealRes.Body.String())
	}

	// Revoke namedCreated.Key.ID and verify settled superseded key reports runtimeState "revoked_observed", revokedAtMs > 0, and regenerate returns 409.
	revokeID := strings.Repeat("5", 32)
	prepRevoke := harness.call(t, http.MethodPost, "api-keys/"+string(namedCreated.Key.ID)+"/revocation",
		`{"requestId":"`+revokeID+`","keeperId":"`+string(installerKey.ID)+`","mode":"revoke"}`)
	if prepRevoke.Code != http.StatusOK {
		t.Fatalf("prepare revoke = %d %s", prepRevoke.Code, prepRevoke.Body.String())
	}
	contRevoke := harness.call(t, http.MethodPost, "api-keys/operations/"+revokeID+"/continue", `{}`)
	if contRevoke.Code != http.StatusOK {
		t.Fatalf("continue revoke = %d %s", contRevoke.Code, contRevoke.Body.String())
	}

	detailRevoked := harness.call(t, http.MethodGet, "api-keys/"+string(namedCreated.Key.ID), "")
	if detailRevoked.Code != http.StatusOK {
		t.Fatalf("GET revoked key = %d %s", detailRevoked.Code, detailRevoked.Body.String())
	}
	var revokedKey freshkeys.Key
	if err := json.Unmarshal(detailRevoked.Body.Bytes(), &revokedKey); err != nil {
		t.Fatal(err)
	}
	if revokedKey.Lifecycle != "superseded" || revokedKey.RuntimeState != "revoked_observed" || revokedKey.RevokedAtMS <= 0 {
		t.Fatalf("revoked key DTO = %+v", revokedKey)
	}

	regenRevoked := harness.call(t, http.MethodPost, "api-keys/"+string(namedCreated.Key.ID)+"/regenerate", `{"requestId":"`+strings.Repeat("6", 32)+`"}`)
	if regenRevoked.Code != http.StatusConflict {
		t.Fatalf("regenerate revoked key = %d %s, want 409", regenRevoked.Code, regenRevoked.Body.String())
	}
}

func TestUpgradeManagedAndExternalNamedCreateAdoptedAliasAndRegenerate(t *testing.T) {
	t.Run("upgrade_managed_alias_backfill_and_regenerate", func(t *testing.T) {
		ctx := t.Context()
		db, err := sqlite.Open(filepath.Join(t.TempDir(), "upgrade-managed-regen.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if err := sqlite.Migrate(db); err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector([]byte("upgrade-managed-regen-key-32byte"))
		if err != nil {
			t.Fatal(err)
		}

		migratedSecret := "legacy-v1-client-key-with-alias"
		sum := sha256.Sum256([]byte(migratedSecret))
		migratedHash := hex.EncodeToString(sum[:])
		preExistingID := "99887766554433221100ffeeddccbbaa"
		createdAtMS := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC).UnixMilli()

		if _, err := db.ExecContext(ctx, `insert into api_key_aliases (api_key_hash, alias, updated_at_ms) values (?, '  Legacy V1 Alias  ', ?)`,
			migratedHash, createdAtMS); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms) values (?, 1, 'active', ?, ?)`,
			preExistingID, createdAtMS, createdAtMS); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, observed_runtime_generation, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, ?, ?, '1', ?, ?, null)`,
			preExistingID, "embedded-default", migratedHash, createdAtMS, createdAtMS+1000); err != nil {
			t.Fatal(err)
		}

		dest := newManagedDestination(t, migratedSecret)
		destServer := dest.serve(t)
		seedUpgradeManagedImportDestination(t, db, protector, destServer.URL, dest.management, "embedded-default", 7, 1, []string{migratedSecret}, createdAtMS)
		recordCompletedUpgradeVerdict(t, db, model.UpgradeVerification{
			TargetMode:        model.UpgradeTargetManaged,
			SourceFingerprint: "sha256:" + strings.Repeat("2", 64),
			Domains:           []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig},
			ObservedAtMS:      createdAtMS + 2000,
		})

		runtimeToken := "supervisor-token-upgrade-managed-regen"
		rtServer := managedKeyRuntime(t, upgradeManagedRuntimeStatus(t), runtimeToken)
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

		call := func(method, resource, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, currentInstallManagementAPIV2Prefix+resource, bytes.NewBufferString(body))
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			handler.serveHTTP(rec, req, resource)
			return rec
		}

		// GET api-keys registers the migrated key and backfills its legacy alias with nameRevision=2!
		listRes := call(http.MethodGet, "api-keys", "")
		if listRes.Code != http.StatusOK {
			t.Fatalf("GET api-keys = %d %s", listRes.Code, listRes.Body.String())
		}
		var listBody struct {
			Keys []freshkeys.Key `json:"keys"`
		}
		if err := json.Unmarshal(listRes.Body.Bytes(), &listBody); err != nil || len(listBody.Keys) != 1 {
			t.Fatalf("listBody = %+v (%v)", listBody, err)
		}
		if listBody.Keys[0].Name != "Legacy V1 Alias" || listBody.Keys[0].NameRevision != 2 || listBody.Keys[0].Origin != freshkeys.OriginAdopted {
			t.Fatalf("adopted alias backfill mismatch = %+v", listBody.Keys[0])
		}

		// Regenerate the migrated adopted key in place; ID, Name, NameRevision=2, Origin="adopted", CreatedAtMS stay intact!
		regenRes := call(http.MethodPost, "api-keys/"+preExistingID+"/regenerate", `{"requestId":"`+strings.Repeat("a", 32)+`"}`)
		if regenRes.Code != http.StatusOK {
			t.Fatalf("Upgrade Managed regenerate = %d %s", regenRes.Code, regenRes.Body.String())
		}
		var regenBody clientKeyRegenerateResponse
		if err := json.Unmarshal(regenRes.Body.Bytes(), &regenBody); err != nil {
			t.Fatal(err)
		}
		if string(regenBody.Key.ID) != preExistingID ||
			regenBody.Key.Name != "Legacy V1 Alias" ||
			regenBody.Key.NameRevision != 2 ||
			regenBody.Key.Origin != freshkeys.OriginAdopted ||
			regenBody.Key.CreatedAtMS != createdAtMS ||
			regenBody.CreatedSecret == "" ||
			regenBody.Key.LastRotatedAtMS <= 0 {
			t.Fatalf("Upgrade Managed regenerated key = %+v", regenBody)
		}
		if got := dest.installed(); !slices.Equal(got, []string{regenBody.CreatedSecret}) {
			t.Fatalf("Upgrade Managed CPA keys = %v, want [%s]", got, regenBody.CreatedSecret)
		}
	})

	t.Run("upgrade_external_named_create_and_regenerate", func(t *testing.T) {
		ctx := t.Context()
		db, err := sqlite.Open(filepath.Join(t.TempDir(), "upgrade-external-regen.sqlite"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = db.Close() })
		if err := sqlite.Migrate(db); err != nil {
			t.Fatal(err)
		}
		protector, err := security.NewProtector([]byte("upgrade-external-regen-key-32byt"))
		if err != nil {
			t.Fatal(err)
		}

		extMgmt := strings.Repeat("ab", 32)
		extCPA := newStatefulExternalCPA(t, extMgmt)
		extCPA.mu.Lock()
		extCPA.clientKeys = []string{"external-unmanaged-peer-key"}
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
			SourceFingerprint:   "sha256:" + strings.Repeat("4", 64),
			Domains:             []string{model.UpgradeDomainSourceCopy, model.UpgradeDomainCPAConfig, model.UpgradeDomainIdentity},
			BindingIdentity:     extIdentity,
			BindingGeneration:   extIdentity,
			BindingObservedAtMS: nowMS,
			ObservedAtMS:        nowMS + 1,
		})

		usageSvc := usageapp.New(usageadapter.New(db), func() usageapp.CollectionPhase { return usageapp.PhaseRunning })
		if err := usageSvc.MarkCollectionStart(ctx); err != nil {
			t.Fatal(err)
		}
		wakeCount := 0
		handler, closeFn, err := newUpgradeAPIKeysHandler(upgradeAPIKeysConfig{
			DB:             db,
			Protector:      protector,
			Usage:          usageSvc,
			WakeProjection: func() { wakeCount++ },
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(closeFn)

		call := func(method, resource, body string) *httptest.ResponseRecorder {
			req := httptest.NewRequest(method, currentInstallManagementAPIV2Prefix+resource, bytes.NewBufferString(body))
			if body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			rec := httptest.NewRecorder()
			handler.serveHTTP(rec, req, resource)
			return rec
		}

		createRes := call(http.MethodPost, "api-keys", `{"requestId":"`+strings.Repeat("b", 32)+`","name":"External Bot"}`)
		if createRes.Code != http.StatusCreated {
			t.Fatalf("Upgrade External named create = %d %s", createRes.Code, createRes.Body.String())
		}
		var created clientKeyCreateResponse
		if err := json.Unmarshal(createRes.Body.Bytes(), &created); err != nil || created.Key.Name != "External Bot" || created.Key.NameRevision != 1 {
			t.Fatalf("created External key = %+v (%v)", created, err)
		}

		regenRes := call(http.MethodPost, "api-keys/"+string(created.Key.ID)+"/regenerate", `{"requestId":"`+strings.Repeat("c", 32)+`"}`)
		if regenRes.Code != http.StatusOK {
			t.Fatalf("Upgrade External regenerate = %d %s", regenRes.Code, regenRes.Body.String())
		}
		var regen clientKeyRegenerateResponse
		if err := json.Unmarshal(regenRes.Body.Bytes(), &regen); err != nil {
			t.Fatal(err)
		}
		if regen.Key.ID != created.Key.ID || regen.Key.Name != "External Bot" || regen.CreatedSecret == "" || regen.Key.LastRotatedAtMS <= 0 {
			t.Fatalf("Upgrade External regenerated = %+v", regen)
		}
		extCPA.mu.Lock()
		gotKeys := slices.Clone(extCPA.clientKeys)
		extCPA.mu.Unlock()
		if !slices.Equal(gotKeys, []string{"external-unmanaged-peer-key", regen.CreatedSecret}) {
			t.Fatalf("External CPA keys = %v, want [external-unmanaged-peer-key, %s]", gotKeys, regen.CreatedSecret)
		}
		// 1) Failure mid-flight before CPA accepts PUT (pending/unattempted): reconciles via GET operations/latest + POST operations/{id}/continue ({}) without minting a second key.
		pendingReqID := strings.Repeat("d", 32)
		extCPA.mu.Lock()
		keyGets := 0
		extCPA.onRequest = func(r *http.Request) bool {
			if r.Method == http.MethodGet && r.URL.Path == "/v8/management/config/access/api-keys" {
				keyGets++
				if keyGets == 3 {
					return true
				}
			}
			return false
		}
		extCPA.mu.Unlock()

		pendingRes := call(http.MethodPost, "api-keys/"+string(created.Key.ID)+"/regenerate", `{"requestId":"`+pendingReqID+`"}`)
		if pendingRes.Code != http.StatusServiceUnavailable && pendingRes.Code != http.StatusConflict {
			t.Fatalf("pending mid-flight regenerate = %d %s, want 409/503", pendingRes.Code, pendingRes.Body.String())
		}
		var pendingErr clientKeyCreateErrorResponse
		if err := json.Unmarshal(pendingRes.Body.Bytes(), &pendingErr); err != nil || pendingErr.Result == nil || pendingErr.Result.Operation == nil || pendingErr.Result.Operation.Outcome != keyrepo.OutcomePending {
			t.Fatalf("pending error envelope = %+v (%v) raw=%s", pendingErr, err, pendingRes.Body.String())
		}

		extCPA.mu.Lock()
		extCPA.onRequest = nil
		extCPA.activeKey = extMgmt
		extCPA.mu.Unlock()

		latestPending := call(http.MethodGet, "api-keys/operations/latest", "")
		if latestPending.Code != http.StatusOK {
			t.Fatalf("GET operations/latest (pending) = %d %s", latestPending.Code, latestPending.Body.String())
		}
		var latestPendingRes freshkeyapply.OperationResult
		if err := json.Unmarshal(latestPending.Body.Bytes(), &latestPendingRes); err != nil || latestPendingRes.Operation == nil || latestPendingRes.Operation.ID != pendingReqID || latestPendingRes.NextStep != "rotate" {
			t.Fatalf("latest pending operation = %+v (%v)", latestPendingRes, err)
		}

		contPending := call(http.MethodPost, "api-keys/operations/"+pendingReqID+"/continue", `{}`)
		if contPending.Code != http.StatusOK {
			t.Fatalf("POST continue (pending) = %d %s", contPending.Code, contPending.Body.String())
		}
		var contPendingRes freshkeyapply.OperationResult
		if err := json.Unmarshal(contPending.Body.Bytes(), &contPendingRes); err != nil || contPendingRes.Operation == nil || contPendingRes.Operation.Outcome != keyrepo.OutcomeCommitted || contPendingRes.RuntimeState != "effective_observed" {
			t.Fatalf("continued pending operation = %+v (%v)", contPendingRes, err)
		}

		// 2) Failure mid-flight after CPA applied PUT (unknown/attempted): reconciles via POST operations/{id}/continue ({}) without a second key or second PUT.
		unknownReqID := strings.Repeat("e", 32)
		extCPA.mu.Lock()
		sawPut := false
		failedPostPutRead := false
		extCPA.onRequest = func(r *http.Request) bool {
			if r.Method == http.MethodPut && r.URL.Path == "/v8/management/config/access/api-keys" {
				sawPut = true
				return false
			}
			if sawPut && !failedPostPutRead && r.Method == http.MethodGet && r.URL.Path == "/v8/management/config/access/api-keys" {
				failedPostPutRead = true
				return true
			}
			return false
		}
		extCPA.mu.Unlock()

		unknownRes := call(http.MethodPost, "api-keys/"+string(created.Key.ID)+"/regenerate", `{"requestId":"`+unknownReqID+`"}`)
		if unknownRes.Code != http.StatusServiceUnavailable && unknownRes.Code != http.StatusConflict {
			t.Fatalf("unknown mid-flight regenerate = %d %s, want 409/503", unknownRes.Code, unknownRes.Body.String())
		}
		var unknownErr clientKeyCreateErrorResponse
		if err := json.Unmarshal(unknownRes.Body.Bytes(), &unknownErr); err != nil || unknownErr.Result == nil || unknownErr.Result.Operation == nil || unknownErr.Result.Operation.Outcome != keyrepo.OutcomeUnknown {
			t.Fatalf("unknown error envelope = %+v (%v) raw=%s", unknownErr, err, unknownRes.Body.String())
		}

		extCPA.mu.Lock()
		extCPA.onRequest = nil
		extCPA.mu.Unlock()

		contUnknown := call(http.MethodPost, "api-keys/operations/"+unknownReqID+"/continue", `{}`)
		if contUnknown.Code != http.StatusOK {
			t.Fatalf("POST continue (unknown) = %d %s", contUnknown.Code, contUnknown.Body.String())
		}
		var contUnknownRes freshkeyapply.OperationResult
		if err := json.Unmarshal(contUnknown.Body.Bytes(), &contUnknownRes); err != nil || contUnknownRes.Operation == nil || contUnknownRes.Operation.Outcome != keyrepo.OutcomeCommitted || contUnknownRes.RuntimeState != "effective_observed" {
			t.Fatalf("continued unknown operation = %+v (%v)", contUnknownRes, err)
		}

		finalList := call(http.MethodGet, "api-keys", "")
		var finalListBody struct {
			Keys []freshkeys.Key `json:"keys"`
		}
		if err := json.Unmarshal(finalList.Body.Bytes(), &finalListBody); err != nil || len(finalListBody.Keys) != 1 || finalListBody.Keys[0].ID != created.Key.ID {
			t.Fatalf("final keys after recoveries = %+v (%v)", finalListBody, err)
		}

		// 3) Earlier committed rotate operation (pendingReqID) remains observable as OutcomeCommitted even after a later rotation (unknownReqID) superseded its secret!
		histOpRes := call(http.MethodGet, "api-keys/operations/"+pendingReqID, "")
		if histOpRes.Code != http.StatusOK {
			t.Fatalf("GET superseded rotate operation = %d %s, want 200", histOpRes.Code, histOpRes.Body.String())
		}
		var histOpBody freshkeyapply.OperationResult
		if err := json.Unmarshal(histOpRes.Body.Bytes(), &histOpBody); err != nil || histOpBody.Operation == nil || histOpBody.Operation.Outcome != keyrepo.OutcomeCommitted {
			t.Fatalf("GET superseded rotate operation body = %+v (%v)", histOpBody, err)
		}

		// 4) During an unsettled mid-flight rotate when CPA is unreachable, POST api-keys/{id}/secret refuses with 409 Conflict instead of returning potentially dead old secret, then returns the new secret once reconciled!
		secretGuardReqID := strings.Repeat("f", 32)
		extCPA.mu.Lock()
		sawGuardPut := false
		blockGuardReads := false
		extCPA.onRequest = func(r *http.Request) bool {
			if r.Method == http.MethodPut && r.URL.Path == "/v8/management/config/access/api-keys" {
				sawGuardPut = true
				blockGuardReads = true
				return false
			}
			if sawGuardPut && blockGuardReads && r.Method == http.MethodGet && r.URL.Path == "/v8/management/config/access/api-keys" {
				return true
			}
			return false
		}
		extCPA.mu.Unlock()

		_ = call(http.MethodPost, "api-keys/"+string(created.Key.ID)+"/regenerate", `{"requestId":"`+secretGuardReqID+`"}`)
		blockedSecret := call(http.MethodPost, "api-keys/"+string(created.Key.ID)+"/secret", `{}`)
		if blockedSecret.Code != http.StatusConflict {
			t.Fatalf("POST secret during unsettled rotate = %d %s, want 409", blockedSecret.Code, blockedSecret.Body.String())
		}

		extCPA.mu.Lock()
		extCPA.onRequest = nil
		activeOnCPA := append([]string(nil), extCPA.clientKeys...)
		extCPA.mu.Unlock()

		// If credentialGate is held while unsettled rotation requires reconciliation and caller cancels, answer 499 external_credential_wait_cancelled.
		if !handler.credentialGate.acquire(ctx) {
			t.Fatal("acquire credentialGate for cancel test failed")
		}
		cancelCtx, cancelReq := context.WithCancel(ctx)
		defer cancelReq()
		cancelTimer := time.AfterFunc(25*time.Millisecond, cancelReq)
		defer cancelTimer.Stop()
		cancelHttpReq := httptest.NewRequest(http.MethodPost, currentInstallManagementAPIV2Prefix+"api-keys/"+string(created.Key.ID)+"/secret", bytes.NewBufferString(`{}`)).WithContext(cancelCtx)
		cancelHttpReq.Header.Set("Content-Type", "application/json")
		cancelRec := httptest.NewRecorder()
		handler.serveHTTP(cancelRec, cancelHttpReq, "api-keys/"+string(created.Key.ID)+"/secret")
		handler.credentialGate.release()
		if cancelRec.Code != 499 || !strings.Contains(cancelRec.Body.String(), "external_credential_wait_cancelled") {
			t.Fatalf("cancelled unsettled secret read = %d %s, want 499 external_credential_wait_cancelled", cancelRec.Code, cancelRec.Body.String())
		}

		wakeBeforeReconcile := wakeCount
		reconciledSecret := call(http.MethodPost, "api-keys/"+string(created.Key.ID)+"/secret", `{}`)
		if reconciledSecret.Code != http.StatusOK {
			t.Fatalf("POST secret after CPA restored = %d %s, want 200", reconciledSecret.Code, reconciledSecret.Body.String())
		}
		if wakeCount <= wakeBeforeReconcile {
			t.Fatalf("wakeProjection count = %d (before %d), want wake on reconciled secret commit", wakeCount, wakeBeforeReconcile)
		}
		var secBody struct {
			Secret string `json:"secret"`
		}
		if err := json.Unmarshal(reconciledSecret.Body.Bytes(), &secBody); err != nil || secBody.Secret == "" || !slices.Contains(activeOnCPA, secBody.Secret) {
			t.Fatalf("reconciled secret = %q, activeOnCPA = %v, err = %v", secBody.Secret, activeOnCPA, err)
		}

		// Once settled, POST api-keys/{id}/secret reads locally without blocking on credentialGate!
		if !handler.credentialGate.acquire(ctx) {
			t.Fatal("acquire credentialGate for settled fast-path test failed")
		}
		settledWhileLocked := call(http.MethodPost, "api-keys/"+string(created.Key.ID)+"/secret", `{}`)
		handler.credentialGate.release()
		if settledWhileLocked.Code != http.StatusOK {
			t.Fatalf("settled POST secret while credentialGate held = %d %s, want immediate 200", settledWhileLocked.Code, settledWhileLocked.Body.String())
		}
	})
}
