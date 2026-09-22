package identitystore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
)

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestApplyPassiveSnapshot_FirstSnapshot(t *testing.T) {
	ctx := context.Background()
	_, repo := setupTestDB(t)

	rtID := "runtime-embedded-1"
	keyHash1 := sha256Hex("key-secret-1")
	keyHash2 := sha256Hex("key-secret-2")

	params := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 10,
		APIKeys: []ports.APIKeySnapshotItem{
			{APIKeyHash: keyHash1},
			{APIKeyHash: keyHash2},
		},
		Credentials: []ports.CredentialSnapshotItem{
			{
				SourceAuthID:      "auth-cred-1",
				AuthIndex:         "1",
				Provider:          "codex",
				PhysicalName:      "cred1.json",
				AccountSnapshot:   "u1@example.com",
				AccountIDSnapshot: "acct-1",
				Disabled:          false,
			},
			{
				SourceAuthID:      "auth-cred-2",
				AuthIndex:         "2",
				Provider:          "claude",
				PhysicalName:      "cred2.json",
				AccountSnapshot:   "u2@example.com",
				AccountIDSnapshot: "acct-2",
				Disabled:          true,
			},
		},
		NowMS: 1000,
	}

	result, err := repo.ApplyPassiveSnapshot(ctx, params)
	if err != nil {
		t.Fatalf("first snapshot failed: %v", err)
	}

	if result.APIKeysCreated != 2 || result.CredentialsCreated != 2 {
		t.Fatalf("unexpected creation counts: %+v", result)
	}

	// Verify API key 1
	k1Ent, k1Bind, err := repo.FindActiveAPIKeyBySource(ctx, rtID, keyHash1)
	if err != nil {
		t.Fatalf("find active key 1: %v", err)
	}
	if k1Ent.Revision != 1 || k1Ent.Lifecycle != identity.LifecycleActive {
		t.Errorf("k1Ent = %+v", k1Ent)
	}
	if k1Bind.ObservedRuntimeGeneration != 10 || k1Bind.LastSeenAtMS != 1000 {
		t.Errorf("k1Bind = %+v", k1Bind)
	}

	// Verify Credential 1
	c1Ent, c1Bind, err := repo.FindActiveCredentialBySource(ctx, rtID, "auth-cred-1")
	if err != nil {
		t.Fatalf("find active cred 1: %v", err)
	}
	if c1Ent.Revision != 1 || c1Ent.Lifecycle != identity.LifecycleActive {
		t.Errorf("c1Ent = %+v", c1Ent)
	}
	if c1Bind.PhysicalName != "cred1.json" || c1Bind.ObservedRuntimeGeneration != 10 {
		t.Errorf("c1Bind = %+v", c1Bind)
	}
}

func TestApplyPassiveSnapshot_IdenticalSecondSnapshot(t *testing.T) {
	ctx := context.Background()
	_, repo := setupTestDB(t)

	rtID := "runtime-embedded-1"
	keyHash := sha256Hex("key-secret-1")

	params1 := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 10,
		APIKeys:                   []ports.APIKeySnapshotItem{{APIKeyHash: keyHash}},
		Credentials: []ports.CredentialSnapshotItem{
			{
				SourceAuthID: "auth-1",
				PhysicalName: "cred1.json",
				Provider:     "codex",
			},
		},
		NowMS: 1000,
	}

	if _, err := repo.ApplyPassiveSnapshot(ctx, params1); err != nil {
		t.Fatalf("snapshot 1: %v", err)
	}
	k1Before, _, _ := repo.FindActiveAPIKeyBySource(ctx, rtID, keyHash)
	c1Before, _, _ := repo.FindActiveCredentialBySource(ctx, rtID, "auth-1")

	// Snapshot 2: identical, later timestamp
	params2 := params1
	params2.NowMS = 2000

	res2, err := repo.ApplyPassiveSnapshot(ctx, params2)
	if err != nil {
		t.Fatalf("snapshot 2: %v", err)
	}
	if res2.APIKeysRefreshed != 1 || res2.CredentialsRefreshed != 1 {
		t.Errorf("res2 = %+v", res2)
	}

	k1After, k1BindAfter, _ := repo.FindActiveAPIKeyBySource(ctx, rtID, keyHash)
	c1After, c1BindAfter, _ := repo.FindActiveCredentialBySource(ctx, rtID, "auth-1")

	if k1After.ID != k1Before.ID || k1After.Revision != k1Before.Revision {
		t.Errorf("k1 ID or revision changed: before=%+v, after=%+v", k1Before, k1After)
	}
	if c1After.ID != c1Before.ID || c1After.Revision != c1Before.Revision {
		t.Errorf("c1 ID or revision changed: before=%+v, after=%+v", c1Before, c1After)
	}
	if k1BindAfter.LastSeenAtMS != 2000 || c1BindAfter.LastSeenAtMS != 2000 {
		t.Errorf("binding lastSeen not updated to 2000")
	}
}

func TestApplyPassiveSnapshot_CredentialRenameAndMetadataDrift(t *testing.T) {
	ctx := context.Background()
	_, repo := setupTestDB(t)

	rtID := "runtime-embedded-1"

	params1 := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 10,
		Credentials: []ports.CredentialSnapshotItem{
			{
				SourceAuthID:      "auth-1",
				AuthIndex:         "1",
				Provider:          "codex",
				PhysicalName:      "original-name.json",
				AccountSnapshot:   "old@example.com",
				AccountIDSnapshot: "acct-old",
				Disabled:          false,
			},
		},
		NowMS: 1000,
	}
	if _, err := repo.ApplyPassiveSnapshot(ctx, params1); err != nil {
		t.Fatalf("snapshot 1: %v", err)
	}
	cBefore, _, _ := repo.FindActiveCredentialBySource(ctx, rtID, "auth-1")

	// Drift: rename file, change provider casing, change account, disable
	params2 := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 10,
		Credentials: []ports.CredentialSnapshotItem{
			{
				SourceAuthID:      "auth-1", // same source Auth.ID
				AuthIndex:         "99",
				Provider:          "codex-updated",
				PhysicalName:      "renamed-file.json",
				AccountSnapshot:   "new@example.com",
				AccountIDSnapshot: "acct-new",
				Disabled:          true,
			},
		},
		NowMS: 2000,
	}
	res2, err := repo.ApplyPassiveSnapshot(ctx, params2)
	if err != nil {
		t.Fatalf("snapshot 2: %v", err)
	}
	if res2.CredentialsRefreshed != 1 {
		t.Errorf("res2 = %+v", res2)
	}

	cAfter, cBindAfter, _ := repo.FindActiveCredentialBySource(ctx, rtID, "auth-1")

	// Invariants: same CredentialID, same business revision!
	if cAfter.ID != cBefore.ID {
		t.Errorf("ID changed from %s to %s", cBefore.ID, cAfter.ID)
	}
	if cAfter.Revision != cBefore.Revision {
		t.Errorf("revision changed from %d to %d (metadata change must not bump revision)", cBefore.Revision, cAfter.Revision)
	}
	if cBindAfter.PhysicalName != "renamed-file.json" || cBindAfter.AuthIndex != "99" || cBindAfter.AccountSnapshot != "new@example.com" {
		t.Errorf("binding metadata not updated: %+v", cBindAfter)
	}
}

func TestApplyPassiveSnapshot_RuntimeGenerationDriftBetweenSuccessfulRuns(t *testing.T) {
	ctx := context.Background()
	_, repo := setupTestDB(t)

	rtID := "runtime-embedded-1"
	keyHash := sha256Hex("key-secret-1")

	params1 := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 10,
		APIKeys:                   []ports.APIKeySnapshotItem{{APIKeyHash: keyHash}},
		Credentials: []ports.CredentialSnapshotItem{
			{SourceAuthID: "auth-1", PhysicalName: "cred1.json"},
		},
		NowMS: 1000,
	}
	if _, err := repo.ApplyPassiveSnapshot(ctx, params1); err != nil {
		t.Fatalf("snapshot 1: %v", err)
	}
	kBefore, _, _ := repo.FindActiveAPIKeyBySource(ctx, rtID, keyHash)
	cBefore, _, _ := repo.FindActiveCredentialBySource(ctx, rtID, "auth-1")

	// Runtime restarted between runs: generation 10 -> 20, same RuntimeIdentity
	params2 := params1
	params2.ObservedRuntimeGeneration = 20
	params2.NowMS = 2000

	if _, err := repo.ApplyPassiveSnapshot(ctx, params2); err != nil {
		t.Fatalf("snapshot 2: %v", err)
	}

	kAfter, kBindAfter, _ := repo.FindActiveAPIKeyBySource(ctx, rtID, keyHash)
	cAfter, cBindAfter, _ := repo.FindActiveCredentialBySource(ctx, rtID, "auth-1")

	if kAfter.Revision != kBefore.Revision || cAfter.Revision != cBefore.Revision {
		t.Errorf("generation drift bumped business revision! kRev=%d, cRev=%d", kAfter.Revision, cAfter.Revision)
	}
	if kBindAfter.ObservedRuntimeGeneration != 20 || cBindAfter.ObservedRuntimeGeneration != 20 {
		t.Errorf("binding generation not updated to 20")
	}
}

func TestApplyPassiveSnapshot_ActiveAbsentTransitionsToMissingAndRecovers(t *testing.T) {
	ctx := context.Background()
	_, repo := setupTestDB(t)

	rtID := "runtime-embedded-1"
	keyHash1 := sha256Hex("key-1")
	keyHash2 := sha256Hex("key-2")

	// Run 1: Two keys, two credentials
	params1 := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 1,
		APIKeys: []ports.APIKeySnapshotItem{
			{APIKeyHash: keyHash1},
			{APIKeyHash: keyHash2},
		},
		Credentials: []ports.CredentialSnapshotItem{
			{SourceAuthID: "auth-1", PhysicalName: "cred1.json"},
			{SourceAuthID: "auth-2", PhysicalName: "cred2.json"},
		},
		NowMS: 1000,
	}
	if _, err := repo.ApplyPassiveSnapshot(ctx, params1); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	k2ID, _, _ := repo.FindActiveAPIKeyBySource(ctx, rtID, keyHash2)
	c2ID, _, _ := repo.FindActiveCredentialBySource(ctx, rtID, "auth-2")

	// Run 2: key2 and cred2 are absent from the complete snapshot (Negative evidence)
	params2 := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 1,
		APIKeys: []ports.APIKeySnapshotItem{
			{APIKeyHash: keyHash1},
		},
		Credentials: []ports.CredentialSnapshotItem{
			{SourceAuthID: "auth-1", PhysicalName: "cred1.json"},
		},
		NowMS: 2000,
	}
	res2, err := repo.ApplyPassiveSnapshot(ctx, params2)
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if res2.APIKeysMissing != 1 || res2.CredentialsMissing != 1 {
		t.Errorf("expected 1 missing each, got %+v", res2)
	}

	// key2 and cred2 should no longer be found by FindActive*BySource
	if _, _, err := repo.FindActiveAPIKeyBySource(ctx, rtID, keyHash2); err == nil {
		t.Errorf("expected key2 to be inactive/missing")
	}
	if _, _, err := repo.FindActiveCredentialBySource(ctx, rtID, "auth-2"); err == nil {
		t.Errorf("expected cred2 to be inactive/missing")
	}

	// Inspect key2 and cred2 directly by ID
	k2Ent, err := repo.LoadAPIKeyByID(ctx, k2ID.ID)
	if err != nil {
		t.Fatalf("load key2: %v", err)
	}
	if k2Ent.Lifecycle != identity.LifecycleMissing || k2Ent.Revision != 2 {
		t.Errorf("key2 lifecycle/rev: %+v, want missing, rev 2", k2Ent)
	}

	c2Ent, err := repo.LoadCredentialByID(ctx, c2ID.ID)
	if err != nil {
		t.Fatalf("load cred2: %v", err)
	}
	if c2Ent.Lifecycle != identity.LifecycleMissing || c2Ent.Revision != 2 {
		t.Errorf("cred2 lifecycle/rev: %+v, want missing, rev 2", c2Ent)
	}

	// Run 3: key2 and cred2 still absent -> missing -> missing: NO revision bump!
	params3 := params2
	params3.NowMS = 3000
	res3, err := repo.ApplyPassiveSnapshot(ctx, params3)
	if err != nil {
		t.Fatalf("run 3: %v", err)
	}
	if res3.APIKeysMissing != 0 || res3.CredentialsMissing != 0 {
		t.Errorf("expected 0 missing transition (already missing), got %+v", res3)
	}
	k2EntAfter, _ := repo.LoadAPIKeyByID(ctx, k2ID.ID)
	if k2EntAfter.Revision != 2 {
		t.Errorf("missing -> missing bumped revision! got %d, want 2", k2EntAfter.Revision)
	}

	// Run 4: key2 and cred2 RETURN! (missing -> active: revision + 1, same ID)
	params4 := params1
	params4.NowMS = 4000
	res4, err := repo.ApplyPassiveSnapshot(ctx, params4)
	if err != nil {
		t.Fatalf("run 4: %v", err)
	}
	if res4.APIKeysRecovered != 1 || res4.CredentialsRecovered != 1 {
		t.Errorf("expected 1 recovery each, got %+v", res4)
	}

	k2Recovered, _, err := repo.FindActiveAPIKeyBySource(ctx, rtID, keyHash2)
	if err != nil {
		t.Fatalf("find active key2 after recovery: %v", err)
	}
	if k2Recovered.ID != k2ID.ID {
		t.Errorf("recovered ID mismatch: got %s, want %s", k2Recovered.ID, k2ID.ID)
	}
	if k2Recovered.Lifecycle != identity.LifecycleActive || k2Recovered.Revision != 3 {
		t.Errorf("recovered key2 state: %+v, want active, rev 3", k2Recovered)
	}

	c2Recovered, _, err := repo.FindActiveCredentialBySource(ctx, rtID, "auth-2")
	if err != nil {
		t.Fatalf("find active cred2 after recovery: %v", err)
	}
	if c2Recovered.ID != c2ID.ID {
		t.Errorf("recovered ID mismatch: got %s, want %s", c2Recovered.ID, c2ID.ID)
	}
	if c2Recovered.Lifecycle != identity.LifecycleActive || c2Recovered.Revision != 3 {
		t.Errorf("recovered cred2 state: %+v, want active, rev 3", c2Recovered)
	}
}

func TestApplyPassiveSnapshot_PassiveAPIKeyReplacementNotRotation(t *testing.T) {
	ctx := context.Background()
	_, repo := setupTestDB(t)

	rtID := "runtime-embedded-1"
	oldHash := sha256Hex("old-api-key")
	newHash := sha256Hex("new-api-key")

	// Snapshot 1: oldHash present
	params1 := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 1,
		APIKeys:                   []ports.APIKeySnapshotItem{{APIKeyHash: oldHash}},
		NowMS:                     1000,
	}
	if _, err := repo.ApplyPassiveSnapshot(ctx, params1); err != nil {
		t.Fatalf("snapshot 1: %v", err)
	}
	oldEnt, _, _ := repo.FindActiveAPIKeyBySource(ctx, rtID, oldHash)

	// Snapshot 2: oldHash absent, newHash present
	// Passive reconciliation must NOT infer rotation: old -> missing, new -> NEW APIKeyID!
	params2 := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 1,
		APIKeys:                   []ports.APIKeySnapshotItem{{APIKeyHash: newHash}},
		NowMS:                     2000,
	}
	res2, err := repo.ApplyPassiveSnapshot(ctx, params2)
	if err != nil {
		t.Fatalf("snapshot 2: %v", err)
	}
	if res2.APIKeysMissing != 1 || res2.APIKeysCreated != 1 {
		t.Errorf("expected 1 missing, 1 created, got %+v", res2)
	}

	// Verify old key is missing
	oldEntAfter, err := repo.LoadAPIKeyByID(ctx, oldEnt.ID)
	if err != nil {
		t.Fatalf("load old key: %v", err)
	}
	if oldEntAfter.Lifecycle != identity.LifecycleMissing {
		t.Errorf("old key lifecycle = %s, want missing", oldEntAfter.Lifecycle)
	}

	// Verify new key is a completely NEW ID
	newEnt, _, err := repo.FindActiveAPIKeyBySource(ctx, rtID, newHash)
	if err != nil {
		t.Fatalf("find new key: %v", err)
	}
	if newEnt.ID == oldEnt.ID {
		t.Fatalf("new key reused old ID! This violates passive replacement != rotation invariant")
	}
	if newEnt.Revision != 1 || newEnt.Lifecycle != identity.LifecycleActive {
		t.Errorf("new key state: %+v", newEnt)
	}
}

func TestApplyPassiveSnapshot_CombinedRollbackOnFailure(t *testing.T) {
	ctx := context.Background()
	db, repo := setupTestDB(t)

	rtID := "runtime-embedded-1"
	validKeyHash := sha256Hex("valid-key")
	validAuthID := "cred-1"

	// Initial valid state: 1 key and 1 credential
	params1 := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 1,
		APIKeys:                   []ports.APIKeySnapshotItem{{APIKeyHash: validKeyHash}},
		Credentials: []ports.CredentialSnapshotItem{
			{SourceAuthID: validAuthID, PhysicalName: "cred1.json"},
		},
		NowMS: 1000,
	}
	if _, err := repo.ApplyPassiveSnapshot(ctx, params1); err != nil {
		t.Fatalf("setup initial snapshot: %v", err)
	}

	// Create test trigger to abort inside transaction when inserting credential source binding:
	// Execution order inside ApplyPassiveSnapshot:
	// 1. Query existing API keys & credentials
	// 2. Insert new API key identity + binding (succeeds inside tx)
	// 3. Insert new Credential identity + binding (aborts via trigger inside same tx)
	// 4. Whole transaction rolls back
	_, err := db.ExecContext(ctx, `create trigger fail_cred_binding_insert
		before insert on gateway_credential_source_bindings
		begin
			select raise(abort, 'forced credential failure');
		end;`)
	if err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	defer func() {
		_, _ = db.ExecContext(ctx, `drop trigger if exists fail_cred_binding_insert`)
	}()

	newKeyHash := sha256Hex("new-key-hash")
	newAuthID := "cred-new"
	params2 := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 1,
		APIKeys: []ports.APIKeySnapshotItem{
			{APIKeyHash: validKeyHash},
			{APIKeyHash: newKeyHash},
		},
		Credentials: []ports.CredentialSnapshotItem{
			{SourceAuthID: validAuthID, PhysicalName: "cred1.json"},
			{SourceAuthID: newAuthID, PhysicalName: "cred2.json"},
		},
		NowMS: 2000,
	}

	_, err = repo.ApplyPassiveSnapshot(ctx, params2)
	if err == nil {
		t.Fatal("expected error from trigger abort, got nil")
	}
	if !strings.Contains(err.Error(), "forced credential failure") {
		t.Errorf("expected error to contain 'forced credential failure', got: %v", err)
	}

	// Verify rollback: newKeyHash MUST NOT exist in DB
	if _, _, err := repo.FindActiveAPIKeyBySource(ctx, rtID, newKeyHash); err == nil {
		t.Errorf("newKeyHash was persisted despite rollback!")
	}
	var apiKeyCount int
	if err := db.QueryRowContext(ctx, `select count(*) from gateway_api_key_identities`).Scan(&apiKeyCount); err != nil {
		t.Fatalf("query api key count: %v", err)
	}
	if apiKeyCount != 1 {
		t.Errorf("expected 1 api key identity, got %d", apiKeyCount)
	}

	var apiKeyBindingCount int
	if err := db.QueryRowContext(ctx, `select count(*) from gateway_api_key_source_bindings`).Scan(&apiKeyBindingCount); err != nil {
		t.Fatalf("query api key binding count: %v", err)
	}
	if apiKeyBindingCount != 1 {
		t.Errorf("expected 1 api key binding, got %d", apiKeyBindingCount)
	}

	// Verify newAuthID credential MUST NOT exist in DB
	if _, _, err := repo.FindActiveCredentialBySource(ctx, rtID, newAuthID); err == nil {
		t.Errorf("newAuthID credential was persisted despite rollback!")
	}
	var credCount int
	if err := db.QueryRowContext(ctx, `select count(*) from gateway_credential_identities`).Scan(&credCount); err != nil {
		t.Fatalf("query cred count: %v", err)
	}
	if credCount != 1 {
		t.Errorf("expected 1 credential identity, got %d", credCount)
	}

	// Verify original rows unchanged
	oldK, _, err := repo.FindActiveAPIKeyBySource(ctx, rtID, validKeyHash)
	if err != nil {
		t.Fatalf("original key missing: %v", err)
	}
	if oldK.Revision != 1 || oldK.Lifecycle != identity.LifecycleActive {
		t.Errorf("original key altered: %+v", oldK)
	}

	oldC, _, err := repo.FindActiveCredentialBySource(ctx, rtID, validAuthID)
	if err != nil {
		t.Fatalf("original cred missing: %v", err)
	}
	if oldC.Revision != 1 || oldC.Lifecycle != identity.LifecycleActive {
		t.Errorf("original cred altered: %+v", oldC)
	}

	// Verify usage_events untouched
	var usageCount int
	if err := db.QueryRowContext(ctx, "select count(*) from usage_events").Scan(&usageCount); err != nil {
		t.Fatalf("count usage_events: %v", err)
	}
	if usageCount != 0 {
		t.Errorf("usage_events modified: count = %d", usageCount)
	}
}

func TestApplyPassiveSnapshot_NoRawKeyPersisted(t *testing.T) {
	ctx := context.Background()
	db, repo := setupTestDB(t)

	rawSecret := "ultra-confidential-secret-key-not-to-persist"
	hashed := sha256Hex(rawSecret)

	params := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           "runtime-1",
		ObservedRuntimeGeneration: 1,
		APIKeys:                   []ports.APIKeySnapshotItem{{APIKeyHash: hashed}},
		Credentials: []ports.CredentialSnapshotItem{
			{
				SourceAuthID:      "auth-1",
				AuthIndex:         "0",
				Provider:          "codex",
				PhysicalName:      "cred1.json",
				AccountSnapshot:   "user@example.com",
				AccountIDSnapshot: "acct-1",
			},
		},
		NowMS: 1000,
	}

	if _, err := repo.ApplyPassiveSnapshot(ctx, params); err != nil {
		t.Fatalf("apply snapshot: %v", err)
	}

	// Columns to check across all 4 tables for rawSecret
	tableColumns := map[string][]string{
		"gateway_api_key_identities": {
			"id", "lifecycle",
		},
		"gateway_api_key_source_bindings": {
			"api_key_hash", "runtime_identity", "observed_runtime_generation",
		},
		"gateway_credential_identities": {
			"id", "lifecycle",
		},
		"gateway_credential_source_bindings": {
			"credential_id", "runtime_identity", "source_auth_id",
			"auth_index", "provider", "physical_name",
			"account_snapshot", "account_id_snapshot",
			"observed_runtime_generation",
		},
	}

	for table, cols := range tableColumns {
		for _, col := range cols {
			var count int
			query := fmt.Sprintf("select count(*) from %s where instr(%s, ?) > 0", table, col)
			err := db.QueryRowContext(ctx, query, rawSecret).Scan(&count)
			if err != nil {
				t.Fatalf("querying table %s column %s failed: %v", table, col, err)
			}
			if count > 0 {
				t.Errorf("raw secret leaked into table %s column %s: count = %d", table, col, count)
			}
		}
	}
}

func TestApplyPassiveSnapshot_TimestampMonotonicity_WallClockRollback(t *testing.T) {
	ctx := context.Background()
	_, repo := setupTestDB(t)

	rtID := "runtime-embedded-1"
	keyHash := sha256Hex("key-rollback-test")
	authID := "cred-rollback-test"

	// Snapshot 1: observed at 2000
	params1 := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 1,
		APIKeys:                   []ports.APIKeySnapshotItem{{APIKeyHash: keyHash}},
		Credentials: []ports.CredentialSnapshotItem{
			{SourceAuthID: authID, PhysicalName: "cred.json"},
		},
		NowMS: 2000,
	}
	if _, err := repo.ApplyPassiveSnapshot(ctx, params1); err != nil {
		t.Fatalf("snapshot 1: %v", err)
	}

	_, kBind1, err := repo.FindActiveAPIKeyBySource(ctx, rtID, keyHash)
	if err != nil {
		t.Fatalf("find active key: %v", err)
	}
	if kBind1.FirstSeenAtMS != 2000 || kBind1.LastSeenAtMS != 2000 {
		t.Errorf("expected 2000, got first=%d, last=%d", kBind1.FirstSeenAtMS, kBind1.LastSeenAtMS)
	}

	_, cBind1, err := repo.FindActiveCredentialBySource(ctx, rtID, authID)
	if err != nil {
		t.Fatalf("find active cred: %v", err)
	}
	if cBind1.FirstSeenAtMS != 2000 || cBind1.LastSeenAtMS != 2000 {
		t.Errorf("expected 2000, got first=%d, last=%d", cBind1.FirstSeenAtMS, cBind1.LastSeenAtMS)
	}

	// Snapshot 2: Wall clock rolls back to 1000!
	params2 := params1
	params2.NowMS = 1000
	res2, err := repo.ApplyPassiveSnapshot(ctx, params2)
	if err != nil {
		t.Fatalf("snapshot 2 with clock rollback: %v", err)
	}
	if res2.APIKeysRefreshed != 1 || res2.CredentialsRefreshed != 1 {
		t.Errorf("expected 1 refresh each, got %+v", res2)
	}

	_, kBind2, err := repo.FindActiveAPIKeyBySource(ctx, rtID, keyHash)
	if err != nil {
		t.Fatalf("find active key after rollback: %v", err)
	}
	if kBind2.LastSeenAtMS != 2000 {
		t.Errorf("APIKey last_seen_at_ms decreased after clock rollback! got %d, want 2000", kBind2.LastSeenAtMS)
	}
	if kBind2.LastSeenAtMS < kBind2.FirstSeenAtMS {
		t.Errorf("last_seen_at_ms (%d) < first_seen_at_ms (%d)", kBind2.LastSeenAtMS, kBind2.FirstSeenAtMS)
	}

	_, cBind2, err := repo.FindActiveCredentialBySource(ctx, rtID, authID)
	if err != nil {
		t.Fatalf("find active cred after rollback: %v", err)
	}
	if cBind2.LastSeenAtMS != 2000 {
		t.Errorf("Credential last_seen_at_ms decreased after clock rollback! got %d, want 2000", cBind2.LastSeenAtMS)
	}
	if cBind2.LastSeenAtMS < cBind2.FirstSeenAtMS {
		t.Errorf("last_seen_at_ms (%d) < first_seen_at_ms (%d)", cBind2.LastSeenAtMS, cBind2.FirstSeenAtMS)
	}

	// Snapshot 3: Clock advances normally to 3000
	params3 := params1
	params3.NowMS = 3000
	if _, err := repo.ApplyPassiveSnapshot(ctx, params3); err != nil {
		t.Fatalf("snapshot 3: %v", err)
	}
	_, kBind3, _ := repo.FindActiveAPIKeyBySource(ctx, rtID, keyHash)
	if kBind3.LastSeenAtMS != 3000 {
		t.Errorf("APIKey last_seen_at_ms = %d, want 3000", kBind3.LastSeenAtMS)
	}
}

func TestApplyPassiveSnapshot_UpdatedAtMonotonicAndMaxInt64FailsClosed(t *testing.T) {
	ctx := context.Background()
	db, repo := setupTestDB(t)

	rtID := "runtime-embedded-1"
	keyHash := sha256Hex("key-overflow-test")
	authID := "cred-overflow-test"

	// 1. Initial snapshot at 2000
	params1 := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 1,
		APIKeys:                   []ports.APIKeySnapshotItem{{APIKeyHash: keyHash}},
		Credentials: []ports.CredentialSnapshotItem{
			{SourceAuthID: authID, PhysicalName: "cred.json"},
		},
		NowMS: 2000,
	}
	if _, err := repo.ApplyPassiveSnapshot(ctx, params1); err != nil {
		t.Fatalf("snapshot 1: %v", err)
	}

	kEnt, _, _ := repo.FindActiveAPIKeyBySource(ctx, rtID, keyHash)
	cEnt, _, _ := repo.FindActiveCredentialBySource(ctx, rtID, authID)

	// 2. Snapshot 2: key and cred absent, but clock rolled back to 1500
	// transition active -> missing must advance updated_at_ms monotonically to current + 1 (2001)
	params2 := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 1,
		NowMS:                     1500,
	}
	if _, err := repo.ApplyPassiveSnapshot(ctx, params2); err != nil {
		t.Fatalf("snapshot 2 (missing transition with clock rollback): %v", err)
	}

	kMissing, err := repo.LoadAPIKeyByID(ctx, kEnt.ID)
	if err != nil {
		t.Fatalf("load missing key: %v", err)
	}
	if kMissing.UpdatedAtMS != 2001 {
		t.Errorf("APIKey updated_at_ms was not monotonically advanced! got %d, want 2001", kMissing.UpdatedAtMS)
	}

	cMissing, err := repo.LoadCredentialByID(ctx, cEnt.ID)
	if err != nil {
		t.Fatalf("load missing cred: %v", err)
	}
	if cMissing.UpdatedAtMS != 2001 {
		t.Errorf("Credential updated_at_ms was not monotonically advanced! got %d, want 2001", cMissing.UpdatedAtMS)
	}

	// 3. Set updated_at_ms to MaxInt64 in DB for API key
	_, err = db.ExecContext(ctx, `update gateway_api_key_identities set updated_at_ms = ? where id = ?`,
		math.MaxInt64, string(kEnt.ID))
	if err != nil {
		t.Fatalf("update key updated_at_ms to MaxInt64: %v", err)
	}

	// Recovery (missing -> active) must fail closed when updated_at_ms is MaxInt64
	params3 := params1
	params3.NowMS = 3000
	_, err = repo.ApplyPassiveSnapshot(ctx, params3)
	if err == nil {
		t.Fatal("expected error on MaxInt64 updated_at_ms overflow, got nil")
	}
	if !strings.Contains(err.Error(), "cannot advance updatedAtMs: int64 max reached") {
		t.Errorf("unexpected error: %v", err)
	}

	// Restore API key updated_at_ms, now test Credential MaxInt64
	_, err = db.ExecContext(ctx, `update gateway_api_key_identities set updated_at_ms = 2001 where id = ?`, string(kEnt.ID))
	if err != nil {
		t.Fatalf("reset key updated_at_ms: %v", err)
	}
	_, err = db.ExecContext(ctx, `update gateway_credential_identities set updated_at_ms = ? where id = ?`,
		math.MaxInt64, string(cEnt.ID))
	if err != nil {
		t.Fatalf("update cred updated_at_ms to MaxInt64: %v", err)
	}

	_, err = repo.ApplyPassiveSnapshot(ctx, params3)
	if err == nil {
		t.Fatal("expected error on Credential MaxInt64 updated_at_ms overflow, got nil")
	}
	if !strings.Contains(err.Error(), "cannot advance updatedAtMs: int64 max reached") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestApplyPassiveSnapshot_CorruptedPersistedState_APIKey_RevisionZero(t *testing.T) {
	ctx := context.Background()
	db, repo := setupTestDB(t)

	rtID := "runtime-embedded-1"
	keyHash := sha256Hex("key-corrupted-rev")

	params := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 1,
		APIKeys:                   []ports.APIKeySnapshotItem{{APIKeyHash: keyHash}},
		NowMS:                     1000,
	}
	if _, err := repo.ApplyPassiveSnapshot(ctx, params); err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}

	// Corrupt revision in DB directly: set revision = 0
	res, err := db.ExecContext(ctx, `update gateway_api_key_identities set revision = 0`)
	if err != nil {
		t.Fatalf("corrupt revision: %v", err)
	}
	if ra, _ := res.RowsAffected(); ra != 1 {
		t.Fatalf("expected 1 row affected, got %d", ra)
	}

	// Next snapshot must FAIL CLOSED
	params2 := params
	params2.NowMS = 2000
	_, err = repo.ApplyPassiveSnapshot(ctx, params2)
	if err == nil {
		t.Fatal("expected error on corrupted persisted revision 0, got nil")
	}

	// Verify ZERO writes: revision remains 0 in DB, not repaired
	var rev int64
	if err := db.QueryRowContext(ctx, `select revision from gateway_api_key_identities`).Scan(&rev); err != nil {
		t.Fatalf("query revision: %v", err)
	}
	if rev != 0 {
		t.Errorf("persisted corruption was silently mutated! revision = %d, want 0", rev)
	}
}

func TestApplyPassiveSnapshot_CorruptedPersistedState_APIKey_MalformedGeneration(t *testing.T) {
	ctx := context.Background()
	db, repo := setupTestDB(t)

	rtID := "runtime-embedded-1"
	keyHash := sha256Hex("key-corrupted-gen")

	params := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 1,
		APIKeys:                   []ports.APIKeySnapshotItem{{APIKeyHash: keyHash}},
		NowMS:                     1000,
	}
	if _, err := repo.ApplyPassiveSnapshot(ctx, params); err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}

	// Corrupt observed_runtime_generation in DB directly to 'abc'
	_, err := db.ExecContext(ctx, `update gateway_api_key_source_bindings set observed_runtime_generation = 'abc'`)
	if err != nil {
		t.Fatalf("corrupt generation: %v", err)
	}

	// Next snapshot must fail closed
	params2 := params
	params2.NowMS = 2000
	_, err = repo.ApplyPassiveSnapshot(ctx, params2)
	if err == nil {
		t.Fatal("expected error on malformed observed_runtime_generation, got nil")
	}

	// Verify ZERO writes: generation remains 'abc'
	var gen string
	if err := db.QueryRowContext(ctx, `select observed_runtime_generation from gateway_api_key_source_bindings`).Scan(&gen); err != nil {
		t.Fatalf("query gen: %v", err)
	}
	if gen != "abc" {
		t.Errorf("corrupted generation was overwritten! got %q, want 'abc'", gen)
	}
}

func TestApplyPassiveSnapshot_CorruptedPersistedState_APIKey_InvalidTimestamp(t *testing.T) {
	ctx := context.Background()
	db, repo := setupTestDB(t)

	rtID := "runtime-embedded-1"
	keyHash := sha256Hex("key-corrupted-ts")

	params := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 1,
		APIKeys:                   []ports.APIKeySnapshotItem{{APIKeyHash: keyHash}},
		NowMS:                     1000,
	}
	if _, err := repo.ApplyPassiveSnapshot(ctx, params); err != nil {
		t.Fatalf("initial snapshot: %v", err)
	}

	// Corrupt timestamps: first_seen_at_ms (2000) > last_seen_at_ms (1000)
	_, err := db.ExecContext(ctx, `update gateway_api_key_source_bindings set first_seen_at_ms = 2000, last_seen_at_ms = 1000`)
	if err != nil {
		t.Fatalf("corrupt timestamps: %v", err)
	}

	// Next snapshot must fail closed
	params2 := params
	params2.NowMS = 3000
	_, err = repo.ApplyPassiveSnapshot(ctx, params2)
	if err == nil {
		t.Fatal("expected error on corrupted binding timestamps, got nil")
	}
}

func TestApplyPassiveSnapshot_CorruptedPersistedState_Credential_FailClosed(t *testing.T) {
	t.Run("revision_zero", func(t *testing.T) {
		ctx := context.Background()
		db, repo := setupTestDB(t)

		rtID := "runtime-embedded-1"
		params := ports.ReconcileSnapshotParams{
			RuntimeIdentity:           rtID,
			ObservedRuntimeGeneration: 1,
			Credentials: []ports.CredentialSnapshotItem{
				{SourceAuthID: "auth-cred-corrupt", PhysicalName: "cred.json"},
			},
			NowMS: 1000,
		}
		if _, err := repo.ApplyPassiveSnapshot(ctx, params); err != nil {
			t.Fatalf("initial snapshot: %v", err)
		}

		_, err := db.ExecContext(ctx, `update gateway_credential_identities set revision = 0`)
		if err != nil {
			t.Fatalf("corrupt credential revision: %v", err)
		}

		params2 := params
		params2.NowMS = 2000
		_, err = repo.ApplyPassiveSnapshot(ctx, params2)
		if err == nil {
			t.Fatal("expected error on corrupted credential revision 0, got nil")
		}

		var rev int64
		if err := db.QueryRowContext(ctx, `select revision from gateway_credential_identities`).Scan(&rev); err != nil {
			t.Fatalf("query credential revision: %v", err)
		}
		if rev != 0 {
			t.Errorf("corrupted revision was silently mutated! got %d, want 0", rev)
		}
	})

	t.Run("invalid_timestamp", func(t *testing.T) {
		ctx := context.Background()
		db, repo := setupTestDB(t)

		rtID := "runtime-embedded-1"
		params := ports.ReconcileSnapshotParams{
			RuntimeIdentity:           rtID,
			ObservedRuntimeGeneration: 1,
			Credentials: []ports.CredentialSnapshotItem{
				{SourceAuthID: "auth-cred-corrupt-ts", PhysicalName: "cred.json"},
			},
			NowMS: 1000,
		}
		if _, err := repo.ApplyPassiveSnapshot(ctx, params); err != nil {
			t.Fatalf("initial snapshot: %v", err)
		}

		_, err := db.ExecContext(ctx, `update gateway_credential_source_bindings set first_seen_at_ms = 2000, last_seen_at_ms = 1000`)
		if err != nil {
			t.Fatalf("corrupt credential timestamps: %v", err)
		}

		params2 := params
		params2.NowMS = 3000
		_, err = repo.ApplyPassiveSnapshot(ctx, params2)
		if err == nil {
			t.Fatal("expected error on corrupted credential timestamps, got nil")
		}
	})
}
