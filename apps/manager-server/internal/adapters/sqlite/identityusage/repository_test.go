package identityusage

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

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityusage"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

const (
	testKeyID   = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	otherKeyID  = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"
	testRuntime = "runtime-1"
)

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func setupTestDB(t *testing.T) (*sql.DB, ports.Repository) {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqliterepo.Migrate(db); err != nil {
		t.Fatalf("migrate test db: %v", err)
	}
	return db, New(db)
}

func insertIdentity(t *testing.T, db *sql.DB, id, lifecycle string) {
	t.Helper()
	insertIdentityAt(t, db, id, lifecycle, 100)
}

func insertIdentityAt(t *testing.T, db *sql.DB, id, lifecycle string, createdAtMS int64) {
	t.Helper()
	if _, err := db.Exec(`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms)
		values (?, 1, ?, ?, ?)`, id, lifecycle, createdAtMS, createdAtMS); err != nil {
		t.Fatalf("insert identity: %v", err)
	}
}

func insertStagedKey(t *testing.T, db *sql.DB, id, hash string) {
	t.Helper()
	insertStagedKeyWithOrigin(t, db, id, hash, "workspace")
}

func insertStagedKeyWithOrigin(t *testing.T, db *sql.DB, id, hash, origin string) {
	t.Helper()
	if _, err := db.Exec(`insert into fresh_client_api_keys (request_id, api_key_id, secret_ciphertext, secret_hash, masked_preview, origin)
		values (?, ?, 'cipher', ?, 'cpamp_sk_…0000', ?)`, sha256Hex(id)[:32], id, hash, origin); err != nil {
		t.Fatalf("insert staged key: %v", err)
	}
}

func insertBinding(t *testing.T, db *sql.DB, id, hash string, firstSeen int64, retired any) {
	t.Helper()
	if _, err := db.Exec(`insert into gateway_api_key_source_bindings
		(api_key_id, runtime_identity, api_key_hash, observed_runtime_generation, first_seen_at_ms, last_seen_at_ms, retired_at_ms)
		values (?, ?, ?, null, ?, ?, ?)`, id, testRuntime, hash, firstSeen, firstSeen, retired); err != nil {
		t.Fatalf("insert binding: %v", err)
	}
}

func eventHash(hash string, eventID int64) string {
	return sha256Hex(fmt.Sprintf("event-%s-%d", hash, eventID))
}

func insertEvent(t *testing.T, db *sql.DB, eventID int64, hash string, ts, total, failed int64) {
	t.Helper()
	if _, err := db.Exec(`insert into usage_events
		(id, event_hash, request_id, timestamp_ms, timestamp, model, api_key_hash, input_tokens, output_tokens, total_tokens, failed, created_at_ms)
		values (?, ?, ?, ?, '2026-01-01T00:00:00Z', 'gpt-4', ?, 10, 5, ?, ?, 100)`,
		eventID, eventHash(hash, eventID), "request", ts, hash, total, failed); err != nil {
		t.Fatalf("insert usage event: %v", err)
	}
}

func insertProjection(t *testing.T, db *sql.DB, eventID int64, state, keyID, hash string, ts int64) {
	t.Helper()
	var keyValue any
	if keyID != "" {
		keyValue = keyID
	}
	if _, err := db.Exec(`insert into gateway_usage_identity_projection_v1
		(usage_event_id, event_hash, request_id, evidence_timestamp_ms, api_key_state, api_key_id, api_key_source_hash,
		 credential_state, credential_id, credential_source_auth_id, schema_version, projected_at_ms)
		values (?, ?, 'request', ?, ?, ?, ?, 'unknown', null, '', 1, ?)`,
		eventID, eventHash(hash, eventID), ts, state, keyValue, hash, ts); err != nil {
		t.Fatalf("insert projection: %v", err)
	}
}

func setProjectionState(t *testing.T, db *sql.DB, status string, lastProcessed, target int64) {
	t.Helper()
	if _, err := db.Exec(`update gateway_usage_identity_projection_state
		set status = ?, last_processed_event_id = ?, target_event_id = ? where state_name = 'canonical_identity_v1'`,
		status, lastProcessed, target); err != nil {
		t.Fatalf("update projection state: %v", err)
	}
}

func TestBindingReportsMissingConflictAndAttributionStart(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()
	hash := sha256Hex("staged-secret")
	insertIdentity(t, db, testKeyID, "active")
	insertStagedKey(t, db, testKeyID, hash)

	if _, err := repo.Binding(ctx, identity.APIKeyID(testKeyID)); err != ports.ErrBindingNotFound {
		t.Fatalf("unbound key error = %v, want ErrBindingNotFound", err)
	}
	insertBinding(t, db, testKeyID, hash, 500, nil)
	binding, err := repo.Binding(ctx, identity.APIKeyID(testKeyID))
	if err != nil || binding.FirstSeenAtMS != 500 || binding.Origin != "workspace" || binding.IdentityCreatedAtMS != 100 {
		t.Fatalf("binding = %+v err = %v", binding, err)
	}

	// The same source hash actively bound to another identity is a conflict,
	// never a silent relay of another key's traffic.
	insertIdentity(t, db, otherKeyID, "active")
	insertStagedKey(t, db, otherKeyID, sha256Hex("other-secret"))
	if _, err := db.Exec(`insert into gateway_api_key_source_bindings
		(api_key_id, runtime_identity, api_key_hash, observed_runtime_generation, first_seen_at_ms, last_seen_at_ms, retired_at_ms)
		values (?, 'runtime-2', ?, null, 600, 600, null)`, otherKeyID, hash); err != nil {
		t.Fatalf("insert conflicting binding: %v", err)
	}
	if _, err := repo.Binding(ctx, identity.APIKeyID(testKeyID)); err != ports.ErrBindingConflict {
		t.Fatalf("conflicting key error = %v, want ErrBindingConflict", err)
	}
}

func TestWindowsAggregatesOnlyAttributedEventsInRange(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()
	hash := sha256Hex("staged-secret")
	insertIdentity(t, db, testKeyID, "active")
	insertStagedKey(t, db, testKeyID, hash)
	insertBinding(t, db, testKeyID, hash, 100, nil)

	insertEvent(t, db, 1, hash, 1000, 30, 0)
	insertEvent(t, db, 2, hash, 2000, 70, 1)
	insertEvent(t, db, 3, hash, 9000, 50, 0) // outside the window
	insertProjection(t, db, 1, "mapped", testKeyID, hash, 1000)
	insertProjection(t, db, 2, "mapped", testKeyID, hash, 2000)
	insertProjection(t, db, 3, "mapped", testKeyID, hash, 9000)
	insertEvent(t, db, 4, hash, 1500, 5, 0)
	insertProjection(t, db, 4, "stale", "", hash, 1500)

	windows, err := repo.Windows(ctx, []ports.KeyRange{{APIKeyID: testKeyID, FromMS: 500}}, 5000)
	if err != nil {
		t.Fatal(err)
	}
	window := windows[testKeyID]
	if window.Totals.Calls != 2 || window.Totals.Failure != 1 || window.Totals.TotalTokens != 100 ||
		window.Totals.InputTokens != 20 || window.Totals.OutputTokens != 10 {
		t.Fatalf("window totals = %+v", window.Totals)
	}
	if window.Totals.LastUsedAtMS == nil || *window.Totals.LastUsedAtMS != 2000 {
		t.Fatalf("last used = %v, want 2000 (window maximum)", window.Totals.LastUsedAtMS)
	}
	if window.UnmappedEvents != 1 {
		t.Fatalf("unmapped events = %d, want 1", window.UnmappedEvents)
	}

	// Only attributed events are bucketed: the unattributable event at 1500ms
	// must not be presented as a zero bucket.
	series, err := repo.Series(ctx, identity.APIKeyID(testKeyID), 500, 5000, 1000)
	if err != nil || len(series) != 2 {
		t.Fatalf("series = %+v err = %v", series, err)
	}
	if series[0].BucketMS != 1000 || series[0].Calls != 1 || series[0].Failure != 0 || series[0].TotalTokens != 30 {
		t.Fatalf("first bucket = %+v", series[0])
	}
	if series[1].BucketMS != 2000 || series[1].Calls != 1 || series[1].Failure != 1 || series[1].TotalTokens != 70 {
		t.Fatalf("second bucket = %+v", series[1])
	}
}

func TestProjectionStateAndRawDeletionEvidence(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()
	insertEvent(t, db, 1, sha256Hex("staged-secret"), 1000, 10, 0)

	state, err := repo.ProjectionState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.CaughtUp() || state.LatestEventID != 1 {
		t.Fatalf("pending state = %+v", state)
	}
	setProjectionState(t, db, "ready", 1, 1)
	if state, err = repo.ProjectionState(ctx); err != nil || !state.CaughtUp() {
		t.Fatalf("ready state = %+v err = %v", state, err)
	}

	if deleted, err := repo.RawDeletedIn(ctx, 0, 5000); err != nil || deleted {
		t.Fatalf("raw deleted = %v err = %v, want false", deleted, err)
	}
	if _, err := db.Exec(`insert into usage_archive_deleted_coverage_daily (utc_day, deleted_event_count, min_timestamp_ms, max_timestamp_ms)
		values (0, 1, 900, 1100)`); err != nil {
		t.Fatal(err)
	}
	if deleted, err := repo.RawDeletedIn(ctx, 0, 5000); err != nil || !deleted {
		t.Fatalf("raw deleted = %v err = %v, want true", deleted, err)
	}
	if latest, anyDeleted, err := repo.LatestRawDeletedIn(ctx, 0, 5000); err != nil || !anyDeleted || latest != 1100 {
		t.Fatalf("latest raw deleted = (%d, %v, %v), want (1100, true, nil)", latest, anyDeleted, err)
	}
	if latest, anyDeleted, err := repo.LatestRawDeletedIn(ctx, 1101, 5000); err != nil || anyDeleted || latest != 0 {
		t.Fatalf("latest raw deleted after deleted day = (%d, %v, %v), want (0, false, nil)", latest, anyDeleted, err)
	}

	// Event-level raw deletion ref later than the daily rollup advances LatestRawDeletedIn.
	refHash := sha256Hex("archived-ref-1")
	if _, err := db.Exec(`insert into usage_event_identity_ledger (event_hash, timestamp_ms, bucket_ms, first_seen_at_ms, updated_at_ms)
		values (?, 2400, 0, 2400, 2400)`, refHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into usage_archive_runs (id, schema_version, format, status, cutoff_timestamp_ms, target_event_id, event_count, created_at_ms, updated_at_ms)
		values ('run-1', 1, 'jsonl.zst', 'completed', 3000, 1, 1, 3000, 3000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into usage_archive_segments (run_id, sequence, status, file_name, first_event_id, last_event_id, min_timestamp_ms, max_timestamp_ms, event_count, uncompressed_bytes, compressed_bytes, content_sha256, event_hash_digest, created_at_ms)
		values ('run-1', 1, 'verified', 'seg-1.jsonl.zst', 1, 1, 2400, 2400, 1, 10, 10, 'sha', 'digest', 3000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into usage_archive_event_refs (event_hash, run_id, segment_sequence, raw_event_id, timestamp_ms, archived_at_ms, raw_deleted_at_ms)
		values (?, 'run-1', 1, 1, 2400, 3000, 3100)`, refHash); err != nil {
		t.Fatal(err)
	}
	if latest, anyDeleted, err := repo.LatestRawDeletedIn(ctx, 1101, 5000); err != nil || !anyDeleted || latest != 2400 {
		t.Fatalf("latest raw deleted from event ref = (%d, %v, %v), want (2400, true, nil)", latest, anyDeleted, err)
	}
	if latest, anyDeleted, err := repo.LatestRawDeletedIn(ctx, 2401, 5000); err != nil || anyDeleted || latest != 0 {
		t.Fatalf("latest raw deleted after event ref = (%d, %v, %v), want (0, false, nil)", latest, anyDeleted, err)
	}
}

func TestCollectionStartMarkerKeepsEarliestInstant(t *testing.T) {
	_, repo := setupTestDB(t)
	ctx := context.Background()
	if startedAt, err := repo.CollectionStartedAtMS(ctx); err != nil || startedAt != 0 {
		t.Fatalf("initial start = %d err = %v", startedAt, err)
	}
	if err := repo.MarkCollectionStarted(ctx, 1_700_000_000_000); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkCollectionStarted(ctx, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if startedAt, err := repo.CollectionStartedAtMS(ctx); err != nil || startedAt != 1_700_000_000_000 {
		t.Fatalf("start = %d err = %v, want the earliest instant", startedAt, err)
	}
}

func TestProjectionStateIsNotCaughtUpWhilePending(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	// A fresh projection is pending with an empty frontier. It has processed
	// nothing yet, so it can never be mistaken for a caught-up window.
	state, err := repo.ProjectionState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state.CaughtUp() {
		t.Fatalf("pending projection with an empty frontier reported caught up: %+v", state)
	}

	// Pending is not caught up even when its checkpoint equals the frontier:
	// the projection has not committed a ready window.
	insertEvent(t, db, 1, sha256Hex("staged-secret"), 1000, 10, 0)
	setProjectionState(t, db, "pending", 1, 1)
	if state, err = repo.ProjectionState(ctx); err != nil || state.CaughtUp() {
		t.Fatalf("pending state = %+v err = %v", state, err)
	}

	// A lagging ready checkpoint still has events to read.
	setProjectionState(t, db, "ready", 0, 0)
	if state, err = repo.ProjectionState(ctx); err != nil || state.CaughtUp() {
		t.Fatalf("lagging ready state = %+v err = %v", state, err)
	}

	setProjectionState(t, db, "ready", 1, 1)
	if state, err = repo.ProjectionState(ctx); err != nil || !state.CaughtUp() {
		t.Fatalf("caught-up state = %+v err = %v", state, err)
	}
}

func TestWindowsAggregatesEveryBatchBeyondOneStatement(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()
	// More keys than one statement can bind: the adapter must batch and merge
	// instead of silently truncating the list.
	const keyCount = maxIdentityPlaceholders + 50
	ids := make([]identity.APIKeyID, 0, keyCount)
	ranges := make([]ports.KeyRange, 0, keyCount)
	for i := range keyCount {
		id := fmt.Sprintf("%032x", i+1)
		hash := sha256Hex("batched-secret-" + id)
		insertIdentity(t, db, id, "active")
		insertStagedKey(t, db, id, hash)
		insertBinding(t, db, id, hash, 100, nil)
		insertEvent(t, db, int64(i+1), hash, 1000+int64(i), 10, 0)
		insertProjection(t, db, int64(i+1), "mapped", id, hash, 1000+int64(i))
		keyID := identity.APIKeyID(id)
		ids = append(ids, keyID)
		ranges = append(ranges, ports.KeyRange{APIKeyID: keyID, FromMS: 500 + int64(i%3)*100})
	}

	windows, err := repo.Windows(ctx, ranges, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(windows) != keyCount {
		t.Fatalf("windows = %d, want every one of the %d requested keys", len(windows), keyCount)
	}
	for _, id := range ids {
		if window := windows[id]; window.Totals.Calls != 1 || window.Totals.TotalTokens != 10 {
			t.Fatalf("window %s = %+v, want its own single call", id, window)
		}
	}
}

func TestWindowsRespectsPerKeyFloorsInSingleBoundedBatch(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	hashA := sha256Hex("secret-a")
	hashB := sha256Hex("secret-b")
	insertIdentity(t, db, testKeyID, "active")
	insertStagedKey(t, db, testKeyID, hashA)
	insertBinding(t, db, testKeyID, hashA, 1000, nil)

	insertIdentity(t, db, otherKeyID, "active")
	insertStagedKey(t, db, otherKeyID, hashB)
	insertBinding(t, db, otherKeyID, hashB, 3000, nil)

	// Key A floor=1000: mapped event at 1500 and unmapped event at 2000 both fall inside [1000, 5000).
	insertEvent(t, db, 1, hashA, 1500, 12, 0)
	insertProjection(t, db, 1, "mapped", testKeyID, hashA, 1500)
	insertEvent(t, db, 2, hashA, 2000, 8, 0)
	insertProjection(t, db, 2, "stale", "", hashA, 2000)

	// Key B floor=3000: pre-floor mapped event at 1500 and pre-floor stale event at 2000 must be excluded,
	// while events at 3000 (inclusive lower bound) and 4000 are counted.
	insertEvent(t, db, 3, hashB, 1500, 99, 1)
	insertProjection(t, db, 3, "mapped", otherKeyID, hashB, 1500)
	insertEvent(t, db, 4, hashB, 2000, 77, 0)
	insertProjection(t, db, 4, "stale", "", hashB, 2000)
	insertEvent(t, db, 5, hashB, 3000, 20, 0)
	insertProjection(t, db, 5, "mapped", otherKeyID, hashB, 3000)
	insertEvent(t, db, 6, hashB, 4000, 30, 0)
	insertProjection(t, db, 6, "mapped", otherKeyID, hashB, 4000)

	windows, err := repo.Windows(ctx, []ports.KeyRange{
		{APIKeyID: testKeyID, FromMS: 1000},
		{APIKeyID: otherKeyID, FromMS: 3000},
	}, 5000)
	if err != nil {
		t.Fatalf("Windows per-key floors: %v", err)
	}
	winA := windows[testKeyID]
	if winA.Totals.Calls != 1 || winA.Totals.TotalTokens != 12 || winA.UnmappedEvents != 1 ||
		winA.Totals.LastUsedAtMS == nil || *winA.Totals.LastUsedAtMS != 1500 {
		t.Fatalf("Key A window = %+v, want calls=1 tokens=12 unmapped=1 lastUsed=1500", winA)
	}
	winB := windows[otherKeyID]
	if winB.Totals.Calls != 2 || winB.Totals.Failure != 0 || winB.Totals.TotalTokens != 50 || winB.UnmappedEvents != 0 ||
		winB.Totals.LastUsedAtMS == nil || *winB.Totals.LastUsedAtMS != 4000 {
		t.Fatalf("Key B window = %+v, want calls=2 failures=0 tokens=50 unmapped=0 lastUsed=4000", winB)
	}
}

func TestBindingPopulatesOriginIdentityCreatedAtAndEarliestRotatedStart(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	const (
		installerID = "11111111111111111111111111111111"
		adoptedID   = "22222222222222222222222222222222"
		rotatedID   = "33333333333333333333333333333333"
	)
	hashInstaller := sha256Hex("installer-secret")
	insertIdentityAt(t, db, installerID, "active", 1200)
	insertStagedKeyWithOrigin(t, db, installerID, hashInstaller, "installer")
	insertBinding(t, db, installerID, hashInstaller, 1200, nil)

	hashAdopted := sha256Hex("adopted-secret")
	insertIdentityAt(t, db, adoptedID, "active", 3400)
	insertStagedKeyWithOrigin(t, db, adoptedID, hashAdopted, "adopted")
	insertBinding(t, db, adoptedID, hashAdopted, 3400, nil)

	hashOld := sha256Hex("rotated-secret-old")
	hashNew := sha256Hex("rotated-secret-new")
	insertIdentityAt(t, db, rotatedID, "active", 250)
	insertStagedKeyWithOrigin(t, db, rotatedID, hashNew, "workspace")
	insertBinding(t, db, rotatedID, hashOld, 250, 800)
	insertBinding(t, db, rotatedID, hashNew, 800, nil)

	bInstaller, err := repo.Binding(ctx, installerID)
	if err != nil || bInstaller.Origin != "installer" || bInstaller.IdentityCreatedAtMS != 1200 || bInstaller.FirstSeenAtMS != 1200 {
		t.Fatalf("installer binding = %+v err = %v", bInstaller, err)
	}
	bAdopted, err := repo.Binding(ctx, adoptedID)
	if err != nil || bAdopted.Origin != "adopted" || bAdopted.IdentityCreatedAtMS != 3400 || bAdopted.FirstSeenAtMS != 3400 {
		t.Fatalf("adopted binding = %+v err = %v", bAdopted, err)
	}
	bRotated, err := repo.Binding(ctx, rotatedID)
	if err != nil || bRotated.Origin != "workspace" || bRotated.IdentityCreatedAtMS != 250 || bRotated.FirstSeenAtMS != 250 {
		t.Fatalf("rotated binding = %+v err = %v, want earliest first_seen_at_ms=250", bRotated, err)
	}
}

func TestWindowsPreservesLifetimeLastUsedAcross24hFloorAndRotatedBindings(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	const (
		dayMS = int64(24 * time.Hour / time.Millisecond)
		toMS  = int64(1_760_000_000_000)
	)
	fromMS := toMS - dayMS
	createdAtMS := toMS - 3*dayMS

	// Key A: single binding, last call was 25h ago (outside the 24h window), 0 calls in [fromMS, toMS).
	hashA := sha256Hex("idle-secret-a")
	insertIdentityAt(t, db, testKeyID, "active", createdAtMS)
	insertStagedKeyWithOrigin(t, db, testKeyID, hashA, "workspace")
	insertBinding(t, db, testKeyID, hashA, createdAtMS, nil)
	lastCallA := toMS - 25*3_600_000
	insertEvent(t, db, 1, hashA, lastCallA, 40, 0)
	insertProjection(t, db, 1, "mapped", testKeyID, hashA, lastCallA)

	// Key B: rotated binding chain (retired hashOld + active hashNew).
	hashOld := sha256Hex("rotated-b-old")
	hashNew := sha256Hex("rotated-b-new")
	rotatedAtMS := toMS - 26*3_600_000
	insertIdentityAt(t, db, otherKeyID, "active", createdAtMS)
	insertStagedKeyWithOrigin(t, db, otherKeyID, hashNew, "workspace")
	insertBinding(t, db, otherKeyID, hashOld, createdAtMS, rotatedAtMS)
	insertBinding(t, db, otherKeyID, hashNew, rotatedAtMS, nil)

	oldBindingCall := toMS - 30*3_600_000
	insertEvent(t, db, 2, hashOld, oldBindingCall, 25, 1)
	insertProjection(t, db, 2, "mapped", otherKeyID, hashOld, oldBindingCall)

	// 1. Before any call on hashNew: Key B preserves LastUsedAtMS from its retired binding, with 0 calls in 24h.
	windows, err := repo.Windows(ctx, []ports.KeyRange{
		{APIKeyID: testKeyID, FromMS: fromMS},
		{APIKeyID: otherKeyID, FromMS: fromMS},
	}, toMS)
	if err != nil {
		t.Fatalf("Windows(prior rotated): %v", err)
	}
	winA := windows[testKeyID]
	if winA.Totals.Calls != 0 || winA.Totals.Failure != 0 || winA.Totals.InputTokens != 0 ||
		winA.Totals.OutputTokens != 0 || winA.Totals.TotalTokens != 0 ||
		winA.Totals.LastUsedAtMS == nil || *winA.Totals.LastUsedAtMS != lastCallA {
		t.Fatalf("Key A (>24h idle) = %+v, want 0 totals and lastUsed=%d", winA.Totals, lastCallA)
	}
	winB := windows[otherKeyID]
	if winB.Totals.Calls != 0 || winB.Totals.TotalTokens != 0 ||
		winB.Totals.LastUsedAtMS == nil || *winB.Totals.LastUsedAtMS != oldBindingCall {
		t.Fatalf("Key B (retired binding >24h) = %+v, want 0 totals and lastUsed=%d", winB.Totals, oldBindingCall)
	}

	// 2. Call on hashNew 25h ago (still < fromMS) plus an event at toMS (must be excluded by < toMS).
	newBindingOldCall := toMS - 25*3_600_000
	insertEvent(t, db, 3, hashNew, newBindingOldCall, 35, 0)
	insertProjection(t, db, 3, "mapped", otherKeyID, hashNew, newBindingOldCall)
	insertEvent(t, db, 4, hashNew, toMS, 99, 0)
	insertProjection(t, db, 4, "mapped", otherKeyID, hashNew, toMS)

	windows, err = repo.Windows(ctx, []ports.KeyRange{{APIKeyID: otherKeyID, FromMS: fromMS}}, toMS)
	if err != nil {
		t.Fatalf("Windows(second binding >24h): %v", err)
	}
	winB = windows[otherKeyID]
	if winB.Totals.Calls != 0 || winB.Totals.TotalTokens != 0 ||
		winB.Totals.LastUsedAtMS == nil || *winB.Totals.LastUsedAtMS != newBindingOldCall {
		t.Fatalf("Key B (second binding >24h, excluding >=toMS) = %+v, want 0 totals and lastUsed=%d", winB.Totals, newBindingOldCall)
	}

	// 3. Call inside [fromMS, toMS): Totals count ONLY the in-window call while LastUsedAtMS advances to it.
	inWindowCall := toMS - 3_600_000
	insertEvent(t, db, 5, hashNew, inWindowCall, 60, 0)
	insertProjection(t, db, 5, "mapped", otherKeyID, hashNew, inWindowCall)

	windows, err = repo.Windows(ctx, []ports.KeyRange{{APIKeyID: otherKeyID, FromMS: fromMS}}, toMS)
	if err != nil {
		t.Fatalf("Windows(in-window call): %v", err)
	}
	winB = windows[otherKeyID]
	if winB.Totals.Calls != 1 || winB.Totals.TotalTokens != 60 ||
		winB.Totals.LastUsedAtMS == nil || *winB.Totals.LastUsedAtMS != inWindowCall {
		t.Fatalf("Key B (in-window call) = %+v, want calls=1 tokens=60 lastUsed=%d", winB.Totals, inWindowCall)
	}
}

func TestWindowsSuppressesPriorLastUsedWhenUnmappedProjectionExistsInGap(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()
	createdAtMS := int64(1_700_000_000_000)
	toMS := createdAtMS + 48*3_600_000
	fromMS := toMS - 24*3_600_000
	hash := strings.Repeat("9", 64)

	insertIdentityAt(t, db, testKeyID, "active", createdAtMS)
	insertStagedKeyWithOrigin(t, db, testKeyID, hash, "workspace")
	insertBinding(t, db, testKeyID, hash, createdAtMS, nil)

	priorMappedTS := toMS - 30*3_600_000
	insertEvent(t, db, 10, hash, priorMappedTS, 20, 0)
	insertProjection(t, db, 10, "mapped", testKeyID, hash, priorMappedTS)

	gapUnmappedTS := toMS - 26*3_600_000
	insertEvent(t, db, 11, hash, gapUnmappedTS, 15, 0)
	insertProjection(t, db, 11, "unknown", "", hash, gapUnmappedTS)

	windows, err := repo.Windows(ctx, []ports.KeyRange{{APIKeyID: testKeyID, FromMS: fromMS}}, toMS)
	if err != nil {
		t.Fatalf("Windows(with unmapped gap): %v", err)
	}
	if got := windows[testKeyID]; got.Totals.Calls != 0 || got.Totals.LastUsedAtMS != nil {
		t.Fatalf("Window with unmapped event in (priorMappedTS, fromMS) = %+v, want LastUsedAtMS == nil", got.Totals)
	}
}

func TestWindowsPopulatesTopModelsEstimatedCostAndRecentRequests(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()
	hash := sha256Hex("priced-secret")
	insertIdentity(t, db, testKeyID, "active")
	insertStagedKey(t, db, testKeyID, hash)
	insertBinding(t, db, testKeyID, hash, 100, nil)

	if _, err := db.Exec(`insert into model_prices (model, prompt_per_1m, completion_per_1m, cache_per_1m, updated_at_ms)
		values ('claude-sonnet-4-5', 3.0, 15.0, 0.3, 100)`); err != nil {
		t.Fatalf("insert model price: %v", err)
	}
	if _, err := db.Exec(`insert into usage_events
		(id, event_hash, request_id, timestamp_ms, timestamp, model, api_key_hash, input_tokens, output_tokens, total_tokens, failed, fail_status_code, created_at_ms)
		values
		(1, ?, 'r1', 1000, '2026-01-01T00:00:01Z', 'claude-sonnet-4-5', ?, 1000000, 100000, 1100000, 0, 0, 100),
		(2, ?, 'r2', 2000, '2026-01-01T00:00:02Z', 'claude-sonnet-4-5', ?, 100000, 0, 100000, 1, 429, 100),
		(3, ?, 'r3', 3000, '2026-01-01T00:00:03Z', 'gpt-5', ?, 5000, 1000, 6000, 0, 0, 100)`,
		eventHash(hash, 1), hash, eventHash(hash, 2), hash, eventHash(hash, 3), hash); err != nil {
		t.Fatalf("insert usage events: %v", err)
	}
	insertProjection(t, db, 1, "mapped", testKeyID, hash, 1000)
	insertProjection(t, db, 2, "mapped", testKeyID, hash, 2000)
	insertProjection(t, db, 3, "mapped", testKeyID, hash, 3000)

	windows, err := repo.Windows(ctx, []ports.KeyRange{{APIKeyID: testKeyID, FromMS: 500}}, 5000)
	if err != nil {
		t.Fatalf("Windows: %v", err)
	}
	win := windows[testKeyID]
	if win.ModelCount != 2 || len(win.TopModels) != 2 || win.TopModels[0].Model != "claude-sonnet-4-5" || win.TopModels[0].Calls != 2 {
		t.Fatalf("TopModels = %+v (ModelCount=%d)", win.TopModels, win.ModelCount)
	}
	// claude-sonnet-4-5: 1.1M input * $3/1M ($3.30) + 0.1M output * $15/1M ($1.50) = $4.80
	if win.EstimatedCostUSD == nil || *win.EstimatedCostUSD < 4.79 || *win.EstimatedCostUSD > 4.81 {
		t.Fatalf("EstimatedCostUSD = %v, want ~4.80", win.EstimatedCostUSD)
	}
	if len(win.RecentRequests) != 3 || win.RecentRequests[0].TimestampMS != 3000 || !win.RecentRequests[1].Failed || win.RecentRequests[1].StatusCode != 429 {
		t.Fatalf("RecentRequests = %+v", win.RecentRequests)
	}
}
