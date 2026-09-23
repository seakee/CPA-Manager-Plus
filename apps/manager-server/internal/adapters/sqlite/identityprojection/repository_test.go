package identityprojection

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityprojection"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func setupTestDB(t *testing.T) (*sql.DB, ports.Repository) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test_identity_projection.db")
	db, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := sqliterepo.Migrate(db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	repo := New(db)
	return db, repo
}

func insertTestAPIKeyIdentity(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms)
		VALUES (?, 1, 'active', 100, 100)`, id)
	if err != nil {
		t.Fatalf("insert api key identity: %v", err)
	}
}

func insertTestCredentialIdentity(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO gateway_credential_identities (id, revision, lifecycle, created_at_ms, updated_at_ms)
		VALUES (?, 1, 'active', 100, 100)`, id)
	if err != nil {
		t.Fatalf("insert credential identity: %v", err)
	}
}

func insertTestAPIKeyBinding(t *testing.T, db *sql.DB, apiKeyID, runtimeID, hash string, firstSeen int64, retired *int64) {
	t.Helper()
	var retiredVal any
	if retired != nil {
		retiredVal = *retired
	}
	_, err := db.Exec(`INSERT INTO gateway_api_key_source_bindings (
		api_key_id, runtime_identity, api_key_hash, observed_runtime_generation, first_seen_at_ms, last_seen_at_ms, retired_at_ms
	) VALUES (?, ?, ?, '1', ?, ?, ?)`, apiKeyID, runtimeID, hash, firstSeen, firstSeen, retiredVal)
	if err != nil {
		t.Fatalf("insert api key binding: %v", err)
	}
}

func insertTestCredentialBinding(t *testing.T, db *sql.DB, credID, runtimeID, sourceAuthID string, firstSeen int64, retired *int64) {
	t.Helper()
	var retiredVal any
	if retired != nil {
		retiredVal = *retired
	}
	_, err := db.Exec(`INSERT INTO gateway_credential_source_bindings (
		credential_id, runtime_identity, source_auth_id, first_seen_at_ms, last_seen_at_ms, retired_at_ms
	) VALUES (?, ?, ?, ?, ?, ?)`, credID, runtimeID, sourceAuthID, firstSeen, firstSeen, retiredVal)
	if err != nil {
		t.Fatalf("insert credential binding: %v", err)
	}
}

func insertTestUsageEvent(t *testing.T, db *sql.DB, id int64, hash, reqID string, ts int64, keyHash, rawJSON string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO usage_events (
		id, event_hash, request_id, timestamp_ms, timestamp, model, api_key_hash, raw_json, created_at_ms
	) VALUES (?, ?, ?, ?, '2026-01-01T00:00:00Z', 'gpt-4', ?, ?, 100)`, id, hash, reqID, ts, keyHash, rawJSON)
	if err != nil {
		t.Fatalf("insert usage event: %v", err)
	}
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// 1. API-key event + 唯一有效 binding => mapped
func TestAPIKeyEventUniqueBindingMapped(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	hash := sha256Hex("key-secret-1")
	insertTestAPIKeyIdentity(t, db, "can-key-1")
	insertTestAPIKeyBinding(t, db, "can-key-1", "rt-1", hash, 100, nil)

	insertTestUsageEvent(t, db, 1, "ev-1", "req-1", 200, hash, "{}")

	res, err := repo.CatchUp(ctx, 10, 300)
	if err != nil {
		t.Fatalf("CatchUp error: %v", err)
	}
	if res.Processed != 1 {
		t.Fatalf("expected 1 processed, got %d", res.Processed)
	}

	proj, err := repo.GetProjectionByEventID(ctx, 1)
	if err != nil {
		t.Fatalf("GetProjection error: %v", err)
	}
	if proj.APIKeyState != ports.StateMapped || proj.APIKeyID == nil || *proj.APIKeyID != "can-key-1" {
		t.Fatalf("want mapped/can-key-1, got %v/%v", proj.APIKeyState, proj.APIKeyID)
	}
	if proj.APIKeySourceHash != hash {
		t.Fatalf("want source hash %s, got %s", hash, proj.APIKeySourceHash)
	}
}

// 2. Credential event + raw_json Auth.ID + 唯一有效 binding => mapped
func TestCredentialEventUniqueBindingMapped(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	insertTestCredentialIdentity(t, db, "can-cred-1")
	insertTestCredentialBinding(t, db, "can-cred-1", "rt-1", "cpa-auth-id-1", 100, nil)

	insertTestUsageEvent(t, db, 1, "ev-1", "req-1", 200, "", `{"auth_id":"cpa-auth-id-1"}`)

	res, err := repo.CatchUp(ctx, 10, 300)
	if err != nil {
		t.Fatalf("CatchUp error: %v", err)
	}
	if res.Processed != 1 {
		t.Fatalf("expected 1 processed, got %d", res.Processed)
	}

	proj, err := repo.GetProjectionByEventID(ctx, 1)
	if err != nil {
		t.Fatalf("GetProjection error: %v", err)
	}
	if proj.CredentialState != ports.StateMapped || proj.CredentialID == nil || *proj.CredentialID != "can-cred-1" {
		t.Fatalf("want mapped/can-cred-1, got %v/%v", proj.CredentialState, proj.CredentialID)
	}
	if proj.CredentialSourceAuthID != "cpa-auth-id-1" {
		t.Fatalf("want source auth id cpa-auth-id-1, got %s", proj.CredentialSourceAuthID)
	}
}

// 3. Credential filename/auth_index/provider/account metadata 改变不影响映射
func TestCredentialMetadataChangeDoesNotAffectMapping(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	insertTestCredentialIdentity(t, db, "can-cred-1")
	insertTestCredentialBinding(t, db, "can-cred-1", "rt-1", "cpa-auth-id-1", 100, nil)

	// Event with conflicting provider/auth_index in event fields, but auth_id in raw_json
	_, err := db.Exec(`INSERT INTO usage_events (
		id, event_hash, request_id, timestamp_ms, timestamp, model, provider, auth_index, account_snapshot, raw_json, created_at_ms
	) VALUES (1, 'ev-1', 'req-1', 200, '2026-01-01T00:00:00Z', 'gpt-4', 'provider-x', '999', 'account-y', '{"auth_id":"cpa-auth-id-1"}', 100)`)
	if err != nil {
		t.Fatalf("insert usage event: %v", err)
	}

	_, err = repo.CatchUp(ctx, 10, 300)
	if err != nil {
		t.Fatalf("CatchUp error: %v", err)
	}

	proj, err := repo.GetProjectionByEventID(ctx, 1)
	if err != nil {
		t.Fatalf("GetProjection error: %v", err)
	}
	if proj.CredentialState != ports.StateMapped || proj.CredentialID == nil || *proj.CredentialID != "can-cred-1" {
		t.Fatalf("want mapped/can-cred-1 despite metadata mismatch, got %v/%v", proj.CredentialState, proj.CredentialID)
	}
}

// 4. 缺少 Auth.ID 时，即使 auth_index/file/account 完全匹配也只能 unknown
func TestMissingAuthIDYieldsUnknownEvenIfMetadataMatches(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	insertTestCredentialIdentity(t, db, "can-cred-1")
	insertTestCredentialBinding(t, db, "can-cred-1", "rt-1", "cpa-auth-id-1", 100, nil)

	// Event with auth_index matching but NO auth_id in raw_json
	_, err := db.Exec(`INSERT INTO usage_events (
		id, event_hash, request_id, timestamp_ms, timestamp, model, auth_index, account_snapshot, raw_json, created_at_ms
	) VALUES (1, 'ev-1', 'req-1', 200, '2026-01-01T00:00:00Z', 'gpt-4', '1', 'my-account', '{"auth_index":"1","account":"my-account"}', 100)`)
	if err != nil {
		t.Fatalf("insert usage event: %v", err)
	}

	_, err = repo.CatchUp(ctx, 10, 300)
	if err != nil {
		t.Fatalf("CatchUp error: %v", err)
	}

	proj, err := repo.GetProjectionByEventID(ctx, 1)
	if err != nil {
		t.Fatalf("GetProjection error: %v", err)
	}
	if proj.CredentialState != ports.StateUnknown || proj.CredentialID != nil {
		t.Fatalf("want unknown/nil for missing Auth.ID, got %v/%v", proj.CredentialState, proj.CredentialID)
	}
}

// 5. B1 explicit rotation old/new hash 映射同一个 Canonical APIKeyID
func TestExplicitRotationMapsOldAndNewToSameCanonicalID(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	oldHash := sha256Hex("old-secret")
	newHash := sha256Hex("new-secret")
	ret1 := int64(200)

	insertTestAPIKeyIdentity(t, db, "can-key-shared")
	insertTestAPIKeyBinding(t, db, "can-key-shared", "rt-1", oldHash, 100, &ret1)
	insertTestAPIKeyBinding(t, db, "can-key-shared", "rt-1", newHash, 200, nil)

	// Event 1 at ts=150 with old hash
	insertTestUsageEvent(t, db, 1, "ev-1", "req-1", 150, oldHash, "{}")
	// Event 2 at ts=250 with new hash
	insertTestUsageEvent(t, db, 2, "ev-2", "req-2", 250, newHash, "{}")

	_, err := repo.CatchUp(ctx, 10, 300)
	if err != nil {
		t.Fatalf("CatchUp error: %v", err)
	}

	p1, _ := repo.GetProjectionByEventID(ctx, 1)
	p2, _ := repo.GetProjectionByEventID(ctx, 2)

	if p1.APIKeyState != ports.StateMapped || p1.APIKeyID == nil || *p1.APIKeyID != "can-key-shared" {
		t.Fatalf("p1 want mapped/can-key-shared, got %v/%v", p1.APIKeyState, p1.APIKeyID)
	}
	if p2.APIKeyState != ports.StateMapped || p2.APIKeyID == nil || *p2.APIKeyID != "can-key-shared" {
		t.Fatalf("p2 want mapped/can-key-shared, got %v/%v", p2.APIKeyState, p2.APIKeyID)
	}
}

// 6. Passive replacement old/new hash 映射不同 Canonical APIKeyID
func TestPassiveReplacementMapsOldAndNewToDifferentCanonicalIDs(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	oldHash := sha256Hex("old-secret")
	newHash := sha256Hex("new-secret")
	ret1 := int64(200)

	insertTestAPIKeyIdentity(t, db, "can-key-A")
	insertTestAPIKeyIdentity(t, db, "can-key-B")
	insertTestAPIKeyBinding(t, db, "can-key-A", "rt-1", oldHash, 100, &ret1)
	insertTestAPIKeyBinding(t, db, "can-key-B", "rt-1", newHash, 200, nil)

	insertTestUsageEvent(t, db, 1, "ev-1", "req-1", 150, oldHash, "{}")
	insertTestUsageEvent(t, db, 2, "ev-2", "req-2", 250, newHash, "{}")

	_, err := repo.CatchUp(ctx, 10, 300)
	if err != nil {
		t.Fatalf("CatchUp error: %v", err)
	}

	p1, _ := repo.GetProjectionByEventID(ctx, 1)
	p2, _ := repo.GetProjectionByEventID(ctx, 2)

	if p1.APIKeyState != ports.StateMapped || *p1.APIKeyID != "can-key-A" {
		t.Fatalf("p1 want can-key-A, got %v", p1.APIKeyID)
	}
	if p2.APIKeyState != ports.StateMapped || *p2.APIKeyID != "can-key-B" {
		t.Fatalf("p2 want can-key-B, got %v", p2.APIKeyID)
	}
}

// 7. Superseded + source reappearance 前后历史 event 分别映射 old/new Canonical ID
func TestSupersededSourceReappearanceSeparation(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	hash := sha256Hex("reused-secret")
	ret1 := int64(200)

	insertTestAPIKeyIdentity(t, db, "can-key-old")
	insertTestAPIKeyIdentity(t, db, "can-key-new")
	insertTestAPIKeyBinding(t, db, "can-key-old", "rt-1", hash, 100, &ret1)
	insertTestAPIKeyBinding(t, db, "can-key-new", "rt-1", hash, 300, nil)

	insertTestUsageEvent(t, db, 1, "ev-1", "req-1", 150, hash, "{}")
	insertTestUsageEvent(t, db, 2, "ev-2", "req-2", 250, hash, "{}")
	insertTestUsageEvent(t, db, 3, "ev-3", "req-3", 350, hash, "{}")

	_, err := repo.CatchUp(ctx, 10, 400)
	if err != nil {
		t.Fatalf("CatchUp error: %v", err)
	}

	p1, _ := repo.GetProjectionByEventID(ctx, 1)
	p2, _ := repo.GetProjectionByEventID(ctx, 2)
	p3, _ := repo.GetProjectionByEventID(ctx, 3)

	if p1.APIKeyState != ports.StateMapped || *p1.APIKeyID != "can-key-old" {
		t.Fatalf("p1 want can-key-old, got %v", p1.APIKeyID)
	}
	if p2.APIKeyState != ports.StateStale || p2.APIKeyID != nil {
		t.Fatalf("p2 in gap want stale/nil, got %v/%v", p2.APIKeyState, p2.APIKeyID)
	}
	if p3.APIKeyState != ports.StateMapped || *p3.APIKeyID != "can-key-new" {
		t.Fatalf("p3 want can-key-new (no resurrection of old ID), got %v", p3.APIKeyID)
	}
}

// 8. Known source 但 timestamp 不在任何 interval => stale
func TestKnownSourceOutsideIntervalIsStale(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	hash := sha256Hex("key-secret")
	ret1 := int64(200)
	insertTestAPIKeyIdentity(t, db, "can-key-1")
	insertTestAPIKeyBinding(t, db, "can-key-1", "rt-1", hash, 100, &ret1)

	// Event before first_seen
	insertTestUsageEvent(t, db, 1, "ev-1", "req-1", 50, hash, "{}")
	// Event after retired
	insertTestUsageEvent(t, db, 2, "ev-2", "req-2", 250, hash, "{}")

	_, err := repo.CatchUp(ctx, 10, 300)
	if err != nil {
		t.Fatalf("CatchUp error: %v", err)
	}

	p1, _ := repo.GetProjectionByEventID(ctx, 1)
	p2, _ := repo.GetProjectionByEventID(ctx, 2)

	if p1.APIKeyState != ports.StateStale || p1.APIKeyID != nil {
		t.Fatalf("p1 before first_seen want stale/nil, got %v/%v", p1.APIKeyState, p1.APIKeyID)
	}
	if p2.APIKeyState != ports.StateStale || p2.APIKeyID != nil {
		t.Fatalf("p2 after retired want stale/nil, got %v/%v", p2.APIKeyState, p2.APIKeyID)
	}
}

// 9. Overlapping candidates => ambiguous
func TestOverlappingCandidatesAmbiguous(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	hash := sha256Hex("key-secret")
	ret1 := int64(300)
	ret2 := int64(400)
	insertTestAPIKeyIdentity(t, db, "can-key-1")
	insertTestAPIKeyIdentity(t, db, "can-key-2")
	insertTestAPIKeyBinding(t, db, "can-key-1", "rt-1", hash, 100, &ret1)
	insertTestAPIKeyBinding(t, db, "can-key-2", "rt-1", hash, 200, &ret2)

	// Event at 250 falls in both [100, 300) and [200, 400)
	insertTestUsageEvent(t, db, 1, "ev-1", "req-1", 250, hash, "{}")

	_, err := repo.CatchUp(ctx, 10, 500)
	if err != nil {
		t.Fatalf("CatchUp error: %v", err)
	}

	p1, _ := repo.GetProjectionByEventID(ctx, 1)
	if p1.APIKeyState != ports.StateAmbiguous || p1.APIKeyID != nil {
		t.Fatalf("want ambiguous/nil, got %v/%v", p1.APIKeyState, p1.APIKeyID)
	}
}

// 10. Cross-Runtime overlapping candidates => ambiguous
func TestCrossRuntimeOverlappingCandidatesAmbiguous(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	hash := sha256Hex("key-secret")
	ret1 := int64(300)
	ret2 := int64(400)
	insertTestAPIKeyIdentity(t, db, "can-key-1")
	insertTestAPIKeyIdentity(t, db, "can-key-2")
	insertTestAPIKeyBinding(t, db, "can-key-1", "runtime-alpha", hash, 100, &ret1)
	insertTestAPIKeyBinding(t, db, "can-key-2", "runtime-beta", hash, 200, &ret2)

	// Event at 250
	insertTestUsageEvent(t, db, 1, "ev-1", "req-1", 250, hash, "{}")

	_, err := repo.CatchUp(ctx, 10, 500)
	if err != nil {
		t.Fatalf("CatchUp error: %v", err)
	}

	p1, _ := repo.GetProjectionByEventID(ctx, 1)
	if p1.APIKeyState != ports.StateAmbiguous || p1.APIKeyID != nil {
		t.Fatalf("want ambiguous/nil for cross-runtime candidates, got %v/%v", p1.APIKeyState, p1.APIKeyID)
	}
}

// 11. Conflicting raw_json Auth.ID aliases => ambiguous
func TestConflictingRawJSONAuthIDAliasesAmbiguous(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	insertTestCredentialIdentity(t, db, "can-cred-1")
	insertTestCredentialBinding(t, db, "can-cred-1", "rt-1", "auth-1", 100, nil)

	insertTestUsageEvent(t, db, 1, "ev-1", "req-1", 200, "", `{"auth_id":"auth-1","authId":"auth-2"}`)

	_, err := repo.CatchUp(ctx, 10, 300)
	if err != nil {
		t.Fatalf("CatchUp error: %v", err)
	}

	p1, _ := repo.GetProjectionByEventID(ctx, 1)
	if p1.CredentialState != ports.StateAmbiguous || p1.CredentialID != nil {
		t.Fatalf("want ambiguous/nil for conflicting aliases, got %v/%v", p1.CredentialState, p1.CredentialID)
	}
}

// 12. Completely unknown source => unknown
func TestCompletelyUnknownSourceYieldsUnknown(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	unknownHash := sha256Hex("completely-unknown-secret")
	insertTestUsageEvent(t, db, 1, "ev-1", "req-1", 200, unknownHash, `{"auth_id":"completely-unknown-auth"}`)

	_, err := repo.CatchUp(ctx, 10, 300)
	if err != nil {
		t.Fatalf("CatchUp error: %v", err)
	}

	p1, _ := repo.GetProjectionByEventID(ctx, 1)
	if p1.APIKeyState != ports.StateUnknown || p1.APIKeyID != nil {
		t.Fatalf("want api key unknown, got %v", p1.APIKeyState)
	}
	if p1.CredentialState != ports.StateUnknown || p1.CredentialID != nil {
		t.Fatalf("want credential unknown, got %v", p1.CredentialState)
	}
}

// 13. Malformed raw_json => unknown, worker does not crash
func TestMalformedRawJSONYieldsUnknownWithoutCrash(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	insertTestUsageEvent(t, db, 1, "ev-1", "req-1", 200, "", `this-is-not-json{{{`)

	_, err := repo.CatchUp(ctx, 10, 300)
	if err != nil {
		t.Fatalf("CatchUp should not crash on malformed json, got error: %v", err)
	}

	p1, _ := repo.GetProjectionByEventID(ctx, 1)
	if p1.CredentialState != ports.StateUnknown || p1.CredentialID != nil {
		t.Fatalf("want credential unknown on malformed json, got %v", p1.CredentialState)
	}
}

// 14. RequestID 只复制，不生成 attempt ordering
func TestRequestIDCorrelationOnlyNoAttemptInference(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	insertTestUsageEvent(t, db, 1, "ev-1", "req-common", 100, "", "{}")
	insertTestUsageEvent(t, db, 2, "ev-2", "req-common", 200, "", "{}")
	insertTestUsageEvent(t, db, 3, "ev-3", "req-common", 300, "", "{}")

	_, err := repo.CatchUp(ctx, 10, 400)
	if err != nil {
		t.Fatalf("CatchUp error: %v", err)
	}

	for _, id := range []int64{1, 2, 3} {
		p, err := repo.GetProjectionByEventID(ctx, id)
		if err != nil || p == nil {
			t.Fatalf("failed to get projection %d: %v", id, err)
		}
		if p.RequestID != "req-common" {
			t.Fatalf("event %d: want request_id 'req-common', got %q", id, p.RequestID)
		}
	}

	// Ensure 3 distinct rows are preserved
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM gateway_usage_identity_projection_v1`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("expected 3 distinct projection rows, got %d", count)
	}
}

// 15. 删除 projection/checkpoint 后 rebuild 结果一致
func TestResetAndRebuildYieldsIdenticalResults(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	hash := sha256Hex("key-secret")
	insertTestAPIKeyIdentity(t, db, "can-key-1")
	insertTestAPIKeyBinding(t, db, "can-key-1", "rt-1", hash, 100, nil)
	insertTestCredentialIdentity(t, db, "can-cred-1")
	insertTestCredentialBinding(t, db, "can-cred-1", "rt-1", "auth-1", 100, nil)

	insertTestUsageEvent(t, db, 1, "ev-1", "req-1", 200, hash, `{"auth_id":"auth-1"}`)
	insertTestUsageEvent(t, db, 2, "ev-2", "req-2", 250, hash, `{"auth_id":"unknown-auth"}`)

	_, err := repo.CatchUp(ctx, 10, 300)
	if err != nil {
		t.Fatalf("initial catch-up error: %v", err)
	}

	p1First, _ := repo.GetProjectionByEventID(ctx, 1)
	p2First, _ := repo.GetProjectionByEventID(ctx, 2)

	// Reset projection and checkpoint
	if err := repo.Reset(ctx); err != nil {
		t.Fatalf("Reset error: %v", err)
	}

	var count int
	_ = db.QueryRow(`SELECT count(*) FROM gateway_usage_identity_projection_v1`).Scan(&count)
	if count != 0 {
		t.Fatalf("projection table not empty after reset: %d", count)
	}

	// Rebuild
	_, err = repo.CatchUp(ctx, 10, 300)
	if err != nil {
		t.Fatalf("rebuild catch-up error: %v", err)
	}

	p1Rebuilt, _ := repo.GetProjectionByEventID(ctx, 1)
	p2Rebuilt, _ := repo.GetProjectionByEventID(ctx, 2)

	if p1First.APIKeyState != p1Rebuilt.APIKeyState || *p1First.APIKeyID != *p1Rebuilt.APIKeyID {
		t.Fatalf("p1 API key mismatch: first=%v, rebuilt=%v", p1First, p1Rebuilt)
	}
	if p1First.CredentialState != p1Rebuilt.CredentialState || *p1First.CredentialID != *p1Rebuilt.CredentialID {
		t.Fatalf("p1 Cred mismatch: first=%v, rebuilt=%v", p1First, p1Rebuilt)
	}
	if p2First.CredentialState != p2Rebuilt.CredentialState {
		t.Fatalf("p2 Cred mismatch: first=%v, rebuilt=%v", p2First, p2Rebuilt)
	}
}

// 16. Batch interruption 后 restart 正确 resume (bounded batches)
func TestBoundedBatchesAndCheckpointResume(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	// Insert 5 events
	for i := int64(1); i <= 5; i++ {
		insertTestUsageEvent(t, db, i, fmt.Sprintf("ev-%d", i), "req", 100*i, "", "{}")
	}

	// Batch 1: limit 2 -> processes 1 and 2
	res1, err := repo.CatchUp(ctx, 2, 1000)
	if err != nil {
		t.Fatalf("batch 1 error: %v", err)
	}
	if res1.Processed != 2 || res1.LastProcessedEventID != 2 || !res1.Pending {
		t.Fatalf("batch 1 want 2 processed, lastID 2, pending true; got %v", res1)
	}

	// Verify checkpoint in DB
	state, err := repo.GetState(ctx)
	if err != nil {
		t.Fatalf("GetState error: %v", err)
	}
	if state.LastProcessedEventID != 2 || state.ProcessedEvents != 2 {
		t.Fatalf("checkpoint mismatch: %v", state)
	}

	// Simulate restart by creating new repo instance
	repoRestart := New(db)

	// Batch 2: limit 2 -> processes 3 and 4
	res2, err := repoRestart.CatchUp(ctx, 2, 1000)
	if err != nil {
		t.Fatalf("batch 2 error: %v", err)
	}
	if res2.Processed != 2 || res2.LastProcessedEventID != 4 || !res2.Pending {
		t.Fatalf("batch 2 want 2 processed, lastID 4, pending true; got %v", res2)
	}

	// Batch 3: limit 2 -> processes 5
	res3, err := repoRestart.CatchUp(ctx, 2, 1000)
	if err != nil {
		t.Fatalf("batch 3 error: %v", err)
	}
	if res3.Processed != 1 || res3.LastProcessedEventID != 5 || res3.Pending {
		t.Fatalf("batch 3 want 1 processed, lastID 5, pending false; got %v", res3)
	}

	// Ensure all 5 projected exactly once
	var count int
	_ = db.QueryRow(`SELECT count(*) FROM gateway_usage_identity_projection_v1`).Scan(&count)
	if count != 5 {
		t.Fatalf("expected 5 projections, got %d", count)
	}
}

// 17. Replay 不产生重复 projection
func TestReplayIdempotence(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	insertTestUsageEvent(t, db, 1, "ev-1", "req-1", 100, "", "{}")
	insertTestUsageEvent(t, db, 2, "ev-2", "req-2", 200, "", "{}")

	_, err := repo.CatchUp(ctx, 10, 300)
	if err != nil {
		t.Fatal(err)
	}

	// Manually reset last_processed_event_id in state to 0 to simulate replay
	_, err = db.Exec(`UPDATE gateway_usage_identity_projection_state SET last_processed_event_id = 0`)
	if err != nil {
		t.Fatal(err)
	}

	// Replay CatchUp
	_, err = repo.CatchUp(ctx, 10, 300)
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	var count int
	_ = db.QueryRow(`SELECT count(*) FROM gateway_usage_identity_projection_v1`).Scan(&count)
	if count != 2 {
		t.Fatalf("expected 2 projections after replay, got %d", count)
	}
}

// 18. READY 后新增 usage event 可以 incremental catch-up
func TestIncrementalCatchUpAfterReady(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	insertTestUsageEvent(t, db, 1, "ev-1", "req-1", 100, "", "{}")
	res1, _ := repo.CatchUp(ctx, 10, 200)
	if res1.Pending {
		t.Fatal("expected not pending after first batch")
	}

	state1, _ := repo.GetState(ctx)
	if state1.Status != "ready" {
		t.Fatalf("expected ready status, got %q", state1.Status)
	}

	// Add new event
	insertTestUsageEvent(t, db, 2, "ev-2", "req-2", 300, "", "{}")

	res2, err := repo.CatchUp(ctx, 10, 400)
	if err != nil {
		t.Fatalf("incremental catchup error: %v", err)
	}
	if res2.Processed != 1 || res2.LastProcessedEventID != 2 {
		t.Fatalf("expected 1 processed, lastID 2; got %v", res2)
	}

	p2, _ := repo.GetProjectionByEventID(ctx, 2)
	if p2 == nil {
		t.Fatal("expected event 2 to be projected")
	}
}

// 19. Projection 整个过程不 update/delete usage_events
func TestUsageEventsRemainsImmutable(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	for i := int64(1); i <= 3; i++ {
		insertTestUsageEvent(t, db, i, fmt.Sprintf("ev-%d", i), "req", 100*i, "", "{}")
	}

	// Take snapshot checksum of usage_events
	var beforeChecksum string
	_ = db.QueryRow(`SELECT group_concat(id || ':' || event_hash || ':' || timestamp_ms, ',') FROM usage_events ORDER BY id ASC`).Scan(&beforeChecksum)

	// Run catchup, reset, catchup again
	_, _ = repo.CatchUp(ctx, 10, 500)
	_ = repo.Reset(ctx)
	_, _ = repo.CatchUp(ctx, 10, 500)

	var afterChecksum string
	_ = db.QueryRow(`SELECT group_concat(id || ':' || event_hash || ':' || timestamp_ms, ',') FROM usage_events ORDER BY id ASC`).Scan(&afterChecksum)

	if beforeChecksum != afterChecksum {
		t.Fatalf("usage_events was modified! before=%q after=%q", beforeChecksum, afterChecksum)
	}
}

// 20. Raw API key 不进入 projection、日志或错误
func TestRawAPIKeyNeverPersistedInProjectionOrErrors(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	rawSecret := "sk-proj-super-secret-1234567890abcdef"
	insertTestUsageEvent(t, db, 1, "ev-1", "req-1", 100, rawSecret, `{"api_key":"`+rawSecret+`"}`)

	_, err := repo.CatchUp(ctx, 10, 200)
	if err != nil {
		t.Fatal(err)
	}

	proj, err := repo.GetProjectionByEventID(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(proj.APIKeySourceHash, rawSecret) {
		t.Fatalf("raw secret leaked into APIKeySourceHash: %q", proj.APIKeySourceHash)
	}
	if proj.APIKeySourceHash != "" {
		t.Fatalf("expected empty APIKeySourceHash for non-sha256 key, got %q", proj.APIKeySourceHash)
	}

	// Verify projection table content across all columns
	var rowContent string
	_ = db.QueryRow(`SELECT event_hash || ' ' || api_key_source_hash || ' ' || credential_source_auth_id
		FROM gateway_usage_identity_projection_v1 WHERE usage_event_id = 1`).Scan(&rowContent)
	if strings.Contains(rowContent, rawSecret) {
		t.Fatalf("raw secret found in projection table row: %q", rowContent)
	}

	state, _ := repo.GetState(ctx)
	if strings.Contains(state.LastError, rawSecret) {
		t.Fatalf("raw secret found in state last_error: %q", state.LastError)
	}
}

// 21. Populated large-history DB 仍保持 listener-first startup
func TestLargeHistoryDBPreservesListenerFirstStartup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "large_startup.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Pre-create usage_events and populate 2000 events before migration
	_, err = db.Exec(`CREATE TABLE usage_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_hash TEXT NOT NULL UNIQUE,
		request_id TEXT,
		timestamp_ms INTEGER NOT NULL,
		timestamp TEXT NOT NULL,
		model TEXT NOT NULL,
		api_key_hash TEXT,
		raw_json TEXT,
		created_at_ms INTEGER NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}

	tx, _ := db.Begin()
	stmt, _ := tx.Prepare(`INSERT INTO usage_events (event_hash, request_id, timestamp_ms, timestamp, model, created_at_ms)
		VALUES (?, ?, ?, '2026-01-01', 'gpt-4', 100)`)
	for i := 1; i <= 2000; i++ {
		_, _ = stmt.Exec(fmt.Sprintf("hash-%d", i), fmt.Sprintf("req-%d", i), 1000+i)
	}
	stmt.Close()
	_ = tx.Commit()

	// Measure Migrate duration
	start := time.Now()
	if err := sqliterepo.Migrate(db); err != nil {
		t.Fatalf("Migrate failed on pre-populated DB: %v", err)
	}
	elapsed := time.Since(start)

	// Migration must be near-instant and not scan usage_events synchronously
	if elapsed > 5*time.Second {
		t.Fatalf("Migrate took too long (%v), likely performing synchronous table scan", elapsed)
	}

	// Verify projection table is created empty
	var projCount int
	_ = db.QueryRow(`SELECT count(*) FROM gateway_usage_identity_projection_v1`).Scan(&projCount)
	if projCount != 0 {
		t.Fatalf("expected projection table to be empty at startup, got %d rows", projCount)
	}

	// Verify initial state is pending with 0 processed events
	var stateStatus string
	var lastProcessed, processedEvents int64
	_ = db.QueryRow(`SELECT status, last_processed_event_id, processed_events FROM gateway_usage_identity_projection_state WHERE state_name = 'canonical_identity_v1'`).Scan(&stateStatus, &lastProcessed, &processedEvents)
	if stateStatus != "pending" || lastProcessed != 0 || processedEvents != 0 {
		t.Fatalf("unexpected state at startup: status=%q lastProcessed=%d processed=%d", stateStatus, lastProcessed, processedEvents)
	}
}
