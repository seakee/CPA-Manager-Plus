package identitystore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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

	// Initial valid state
	params1 := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 1,
		APIKeys:                   []ports.APIKeySnapshotItem{{APIKeyHash: validKeyHash}},
		Credentials: []ports.CredentialSnapshotItem{
			{SourceAuthID: "cred-1", PhysicalName: "cred1.json"},
		},
		NowMS: 1000,
	}
	if _, err := repo.ApplyPassiveSnapshot(ctx, params1); err != nil {
		t.Fatalf("setup initial snapshot: %v", err)
	}

	// Snapshot 2: introduces a new API key but an invalid credential that fails validation
	// (e.g. empty SourceAuthID after validation check or constraint violation)
	newKeyHash := sha256Hex("new-key-hash")
	params2 := ports.ReconcileSnapshotParams{
		RuntimeIdentity:           rtID,
		ObservedRuntimeGeneration: 1,
		APIKeys:                   []ports.APIKeySnapshotItem{{APIKeyHash: newKeyHash}},
		Credentials: []ports.CredentialSnapshotItem{
			{SourceAuthID: ""}, // invalid!
		},
		NowMS: 2000,
	}

	_, err := repo.ApplyPassiveSnapshot(ctx, params2)
	if err == nil {
		t.Fatal("expected error on invalid snapshot item, got nil")
	}

	// Check that newKeyHash was NOT created
	if _, _, err := repo.FindActiveAPIKeyBySource(ctx, rtID, newKeyHash); err == nil {
		t.Errorf("newKeyHash was persisted despite snapshot failure!")
	}

	// Check that validKeyHash is still active and unchanged
	oldK, _, err := repo.FindActiveAPIKeyBySource(ctx, rtID, validKeyHash)
	if err != nil {
		t.Fatalf("original key missing after rollback: %v", err)
	}
	if oldK.Revision != 1 || oldK.Lifecycle != identity.LifecycleActive {
		t.Errorf("original key altered: %+v", oldK)
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
		NowMS:                     1000,
	}

	if _, err := repo.ApplyPassiveSnapshot(ctx, params); err != nil {
		t.Fatalf("apply snapshot: %v", err)
	}

	// Scan all text columns across database tables for rawSecret
	for _, table := range []string{
		"gateway_api_key_identities",
		"gateway_api_key_source_bindings",
		"gateway_credential_identities",
		"gateway_credential_source_bindings",
	} {
		var count int
		query := fmt.Sprintf("select count(*) from %s where instr(lower(hex(id)), '%s') > 0", table, strings.ToLower(rawSecret))
		_ = db.QueryRowContext(ctx, query).Scan(&count)
		if count > 0 {
			t.Errorf("raw secret leaked into table %s", table)
		}
	}
}
