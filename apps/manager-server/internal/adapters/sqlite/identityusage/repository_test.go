package identityusage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityusage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/modelprice"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/pricing"
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

func TestWindowsPopulatesTopModelsTokenBreakdownAndEstimatedCost(t *testing.T) {
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
		(id, event_hash, request_id, timestamp_ms, timestamp, model, api_key_hash, input_tokens, output_tokens, cache_read_tokens, total_tokens, failed, created_at_ms)
		values
		(1, ?, 'r1', 1000, '2026-01-01T00:00:01Z', 'claude-sonnet-4-5', ?, 1000000, 100000, 200000, 1100000, 0, 100),
		(2, ?, 'r2', 2000, '2026-01-01T00:00:02Z', 'claude-sonnet-4-5', ?, 100000, 0, 0, 100000, 1, 100),
		(3, ?, 'r3', 3000, '2026-01-01T00:00:03Z', 'gpt-5', ?, 5000, 1000, 1000, 6000, 0, 100)`,
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
	if win.Totals.InputTokens != 1_105_000 || win.Totals.OutputTokens != 101_000 || win.Totals.CachedTokens != 201_000 {
		t.Fatalf("Totals breakdown = %+v", win.Totals)
	}
	if win.ModelCount != 2 || len(win.TopModels) != 2 || win.TopModels[0].Model != "claude-sonnet-4-5" ||
		win.TopModels[0].Calls != 2 || win.TopModels[0].TotalTokens != 1_200_000 ||
		win.TopModels[1].Model != "gpt-5" || win.TopModels[1].Calls != 1 || win.TopModels[1].TotalTokens != 6_000 {
		t.Fatalf("TopModels = %+v (ModelCount=%d)", win.TopModels, win.ModelCount)
	}
	// claude-sonnet-4-5 exact cost: 0.9M * $3.0 + 0.2M * $0.3 + 0.1M * $15.0 = $4.26.
	if win.TopModels[0].EstimatedCostUSD == nil || math.Abs(*win.TopModels[0].EstimatedCostUSD-4.26) > 1e-6 {
		t.Fatalf("TopModels[0].EstimatedCostUSD = %v, want 4.26", win.TopModels[0].EstimatedCostUSD)
	}
	if win.TopModels[1].EstimatedCostUSD != nil {
		t.Fatalf("unpriced TopModels[1].EstimatedCostUSD = %v, want nil", win.TopModels[1].EstimatedCostUSD)
	}
	// Key-level EstimatedCostUSD must be suppressed (nil) while gpt-5 is unpriced.
	if win.EstimatedCostUSD != nil {
		t.Fatalf("partially unpriced key EstimatedCostUSD = %v, want nil", *win.EstimatedCostUSD)
	}

	// Once every model in the window has a matched price, key-level EstimatedCostUSD publishes the exact sum.
	if _, err := db.Exec(`insert into model_prices (model, prompt_per_1m, completion_per_1m, cache_per_1m, updated_at_ms)
		values ('gpt-5', 2.0, 8.0, 0.5, 100)`); err != nil {
		t.Fatalf("insert gpt-5 price: %v", err)
	}
	windows, err = repo.Windows(ctx, []ports.KeyRange{{APIKeyID: testKeyID, FromMS: 500}}, 5000)
	if err != nil {
		t.Fatalf("Windows(all priced): %v", err)
	}
	win = windows[testKeyID]
	// gpt-5 exact cost: 0.004M * $2.0 + 0.001M * $0.5 + 0.001M * $8.0 = $0.0165; total = $4.2765.
	if win.TopModels[1].EstimatedCostUSD == nil || math.Abs(*win.TopModels[1].EstimatedCostUSD-0.0165) > 1e-9 {
		t.Fatalf("TopModels[1].EstimatedCostUSD = %v, want 0.0165", win.TopModels[1].EstimatedCostUSD)
	}
	if win.EstimatedCostUSD == nil || math.Abs(*win.EstimatedCostUSD-4.2765) > 1e-6 {
		t.Fatalf("fully priced EstimatedCostUSD = %v, want 4.2765", win.EstimatedCostUSD)
	}
}

func TestWindowsNormalizesAnthropicSeparateCacheInputAndReasoningTokens(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()
	hash := sha256Hex("anthropic-separate-secret")
	insertIdentity(t, db, testKeyID, "active")
	insertStagedKey(t, db, testKeyID, hash)
	insertBinding(t, db, testKeyID, hash, 100, nil)

	if _, err := db.Exec(`insert into model_prices (
		model, prompt_per_1m, completion_per_1m, cache_per_1m, cache_read_per_1m, cache_creation_per_1m,
		prompt_configured, completion_configured, cache_read_configured, cache_creation_configured, updated_at_ms
	) values ('claude-sonnet-4-5', 3.0, 15.0, 0.3, 0.3, 3.75, 1, 1, 1, 1, 100)`); err != nil {
		t.Fatalf("insert model price: %v", err)
	}
	// CPA mirrors cache_read_tokens + cache_creation_tokens into cached_tokens for separate_from_input,
	// while normalized_total_input_tokens = input + cache_read + cache_creation and
	// normalized_cache_read_tokens = cache_read_tokens.
	if _, err := db.Exec(`insert into usage_events (
		id, event_hash, request_id, timestamp_ms, timestamp, model, api_key_hash,
		cache_input_mode, input_tokens, output_tokens, reasoning_tokens,
		cached_tokens, cache_read_tokens, cache_creation_tokens,
		normalized_total_input_tokens, normalized_cache_read_tokens, normalized_cache_creation_tokens,
		total_tokens, failed, created_at_ms
	) values
	(1, ?, 'a1', 1000, '2026-01-01T00:00:01Z', 'claude-sonnet-4-5', ?, 'separate_from_input', 1000, 10000, 4000, 250000, 200000, 50000, 251000, 200000, 50000, 261000, 0, 100),
	(2, ?, 'a2', 2000, '2026-01-01T00:00:02Z', 'claude-sonnet-4-5', ?, 'separate_from_input', 500, 5000, 1500, 100000, 100000, 0, 100500, 100000, 0, 105500, 0, 100)`,
		eventHash(hash, 1), hash, eventHash(hash, 2), hash); err != nil {
		t.Fatalf("insert anthropic events: %v", err)
	}
	insertProjection(t, db, 1, "mapped", testKeyID, hash, 1000)
	insertProjection(t, db, 2, "mapped", testKeyID, hash, 2000)

	windows, err := repo.Windows(ctx, []ports.KeyRange{{APIKeyID: testKeyID, FromMS: 500}}, 5000)
	if err != nil {
		t.Fatalf("Windows: %v", err)
	}
	win := windows[testKeyID]
	if win.Totals.InputTokens != 351_500 || win.Totals.CachedTokens != 300_000 ||
		win.Totals.ReasoningTokens != 5_500 || win.Totals.OutputTokens != 15_000 || win.Totals.TotalTokens != 366_500 {
		t.Fatalf("normalized Anthropic Totals = %+v, want input=351500 cached=300000 reasoning=5500 output=15000 total=366500", win.Totals)
	}
	// Exact cost: 0.0015M*3.0 (0.0045) + 0.3M*0.3 (0.09) + 0.05M*3.75 (0.1875) + 0.015M*15.0 (0.225) = 0.507.
	if win.EstimatedCostUSD == nil || math.Abs(*win.EstimatedCostUSD-0.507) > 1e-9 {
		t.Fatalf("EstimatedCostUSD = %v, want 0.507", win.EstimatedCostUSD)
	}
	if len(win.TopModels) != 1 || win.TopModels[0].EstimatedCostUSD == nil || math.Abs(*win.TopModels[0].EstimatedCostUSD-0.507) > 1e-9 {
		t.Fatalf("TopModels[0] = %+v, want cost=0.507", win.TopModels)
	}
}

func TestWindowsSuppressesCaseMismatchAndPartialAliasCostAndAppliesContextTiers(t *testing.T) {
	// Direct unit verification of CostForModelCandidatesWithServiceTierMatched.
	casePrices := map[string]model.ModelPrice{"Claude-Sonnet-4-5": {Prompt: 3.0, Completion: 15.0}}
	if cost, matched := pricing.CostForModelCandidatesWithServiceTierMatched(
		[]string{"claude-sonnet-4-5"}, "", pricing.ModelTokens{InputTokens: 1_000_000}, casePrices,
	); matched || cost != 0 {
		t.Fatalf("case-mismatched candidate = (%v, %v), want (0, false)", cost, matched)
	}
	if cost, matched := pricing.CostForModelCandidatesWithServiceTierMatched(
		[]string{"openai/gpt-5.6-sol"}, "", pricing.ModelTokens{InputTokens: 1_000_000}, nil,
	); !matched || math.Abs(cost-5.0) > 1e-6 {
		t.Fatalf("official gpt-5.6 fallback = (%v, %v), want (5.0, true)", cost, matched)
	}

	db, repo := setupTestDB(t)
	ctx := context.Background()
	const (
		caseKeyID   = identity.APIKeyID("1111111111111111111111111111111a")
		aliasKeyID  = identity.APIKeyID("2222222222222222222222222222222b")
		tieredKeyID = identity.APIKeyID("3333333333333333333333333333333c")
	)
	seedKey := func(id identity.APIKeyID, label string) string {
		h := sha256Hex(label)
		insertIdentity(t, db, string(id), "active")
		insertStagedKey(t, db, string(id), h)
		insertBinding(t, db, string(id), h, 100, nil)
		return h
	}
	hashCase := seedKey(caseKeyID, "case-key")
	hashAlias := seedKey(aliasKeyID, "alias-key")
	hashTiered := seedKey(tieredKeyID, "tiered-key")

	if _, err := db.Exec(`insert into model_prices (model, prompt_per_1m, completion_per_1m, cache_per_1m, prompt_configured, completion_configured, updated_at_ms)
		values
		('Claude-Sonnet-4-5', 3.0, 15.0, 0.0, 1, 1, 100),
		('priced-backend', 2.0, 4.0, 0.0, 1, 1, 100),
		('tiered-model', 2.0, 4.0, 0.0, 1, 1, 100)`); err != nil {
		t.Fatalf("insert model_prices: %v", err)
	}
	if _, err := db.Exec(`insert into model_price_context_tiers (
		model, threshold_tokens, prompt_per_1m, completion_per_1m, prompt_configured, completion_configured
	) values ('tiered-model', 200000, 6.0, 12.0, 1, 1)`); err != nil {
		t.Fatalf("insert model_price_context_tiers: %v", err)
	}

	// 1. Case mismatch ('claude-sonnet-4-5' vs 'Claude-Sonnet-4-5'): must NOT publish $0.00.
	if _, err := db.Exec(`insert into usage_events
		(id, event_hash, request_id, timestamp_ms, timestamp, model, api_key_hash, input_tokens, output_tokens, total_tokens, failed, created_at_ms)
		values (1, ?, 'c1', 1000, '2026-01-01T00:00:01Z', 'claude-sonnet-4-5', ?, 500000, 50000, 550000, 0, 100)`,
		eventHash(hashCase, 1), hashCase); err != nil {
		t.Fatal(err)
	}
	insertProjection(t, db, 1, "mapped", string(caseKeyID), hashCase, 1000)

	// 2. Same displayModel ('router-alias', unpriced) backed by one priced resolved_model and one unpriced resolved_model:
	// both the displayModel entry and the key must suppress EstimatedCostUSD instead of understating it.
	if _, err := db.Exec(`insert into usage_events
		(id, event_hash, request_id, timestamp_ms, timestamp, model, requested_model, resolved_model, api_key_hash, input_tokens, output_tokens, total_tokens, failed, created_at_ms)
		values
		(2, ?, 'al1', 1100, '2026-01-01T00:00:01Z', 'router-alias', 'router-alias', 'priced-backend', ?, 1000000, 0, 1000000, 0, 100),
		(3, ?, 'al2', 1200, '2026-01-01T00:00:01Z', 'router-alias', 'router-alias', 'unpriced-backend', ?, 1000000, 0, 1000000, 0, 100)`,
		eventHash(hashAlias, 2), hashAlias, eventHash(hashAlias, 3), hashAlias); err != nil {
		t.Fatal(err)
	}
	insertProjection(t, db, 2, "mapped", string(aliasKeyID), hashAlias, 1100)
	insertProjection(t, db, 3, "mapped", string(aliasKeyID), hashAlias, 1200)

	// 3. Context-tier parity with monitoring: 100k <= 200k uses $2/1M ($0.20), 300k > 200k uses $6/1M ($1.80) -> $2.00.
	if _, err := db.Exec(`insert into usage_events
		(id, event_hash, request_id, timestamp_ms, timestamp, model, api_key_hash, input_tokens, output_tokens, total_tokens, failed, created_at_ms)
		values
		(4, ?, 't1', 1300, '2026-01-01T00:00:01Z', 'tiered-model', ?, 100000, 0, 100000, 0, 100),
		(5, ?, 't2', 1400, '2026-01-01T00:00:01Z', 'tiered-model', ?, 300000, 0, 300000, 0, 100)`,
		eventHash(hashTiered, 4), hashTiered, eventHash(hashTiered, 5), hashTiered); err != nil {
		t.Fatal(err)
	}
	insertProjection(t, db, 4, "mapped", string(tieredKeyID), hashTiered, 1300)
	insertProjection(t, db, 5, "mapped", string(tieredKeyID), hashTiered, 1400)

	windows, err := repo.Windows(ctx, []ports.KeyRange{
		{APIKeyID: caseKeyID, FromMS: 500},
		{APIKeyID: aliasKeyID, FromMS: 500},
		{APIKeyID: tieredKeyID, FromMS: 500},
	}, 5000)
	if err != nil {
		t.Fatalf("Windows: %v", err)
	}
	if winCase := windows[caseKeyID]; winCase.EstimatedCostUSD != nil || len(winCase.TopModels) != 1 || winCase.TopModels[0].EstimatedCostUSD != nil {
		t.Fatalf("case-mismatch window = %+v, want nil EstimatedCostUSD", winCase)
	}
	if winAlias := windows[aliasKeyID]; winAlias.EstimatedCostUSD != nil || len(winAlias.TopModels) != 1 ||
		winAlias.TopModels[0].Calls != 2 || winAlias.TopModels[0].EstimatedCostUSD != nil {
		t.Fatalf("partially priced alias window = %+v, want nil EstimatedCostUSD on key and model", winAlias)
	}
	if winTiered := windows[tieredKeyID]; winTiered.EstimatedCostUSD == nil || math.Abs(*winTiered.EstimatedCostUSD-2.0) > 1e-6 ||
		len(winTiered.TopModels) != 1 || winTiered.TopModels[0].EstimatedCostUSD == nil || math.Abs(*winTiered.TopModels[0].EstimatedCostUSD-2.0) > 1e-6 {
		t.Fatalf("context-tiered window = %+v, want exact cost 2.0", winTiered)
	}

	// 4. Legacy long-context premium (>272k input without context_tiers) matches monitoring/pricing:
	// gpt-5.4 at prompt=2.5, completion=15.0 with 300k input + 100k output -> 0.3*2.5*2 + 0.1*15.0*1.5 = 3.75.
	const longKeyID = identity.APIKeyID("4444444444444444444444444444444d")
	hashLong := seedKey(longKeyID, "long-context-key")
	if _, err := db.Exec(`insert into model_prices (model, prompt_per_1m, completion_per_1m, cache_per_1m, prompt_configured, completion_configured, updated_at_ms)
		values ('gpt-5.4', 2.5, 15.0, 0.25, 1, 1, 100)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into usage_events
		(id, event_hash, request_id, timestamp_ms, timestamp, model, api_key_hash, input_tokens, output_tokens, total_tokens, failed, created_at_ms)
		values (6, ?, 'lc1', 1500, '2026-01-01T00:00:01Z', 'gpt-5.4', ?, 300000, 100000, 400000, 0, 100)`,
		eventHash(hashLong, 6), hashLong); err != nil {
		t.Fatal(err)
	}
	insertProjection(t, db, 6, "mapped", string(longKeyID), hashLong, 1500)
	longWins, err := repo.Windows(ctx, []ports.KeyRange{{APIKeyID: longKeyID, FromMS: 500}}, 5000)
	if err != nil {
		t.Fatalf("Windows(long-context): %v", err)
	}
	if winLong := longWins[longKeyID]; winLong.EstimatedCostUSD == nil || math.Abs(*winLong.EstimatedCostUSD-3.75) > 1e-6 {
		t.Fatalf("long-context window = %+v, want exact cost 3.75", winLong)
	}

	// 5. LoadAll errors must be propagated rather than swallowed into an empty price book.
	if _, err := db.Exec(`drop table model_price_service_tiers`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Windows(ctx, []ports.KeyRange{{APIKeyID: longKeyID, FromMS: 500}}, 5000); err == nil {
		t.Fatal("expected LoadAll error when model_price_service_tiers is missing, got nil")
	}
}

func TestWindowsCapsTopModelsAtTenWhilePreservingModelCountAndTotalCost(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()
	hash := sha256Hex("many-models-secret")
	insertIdentity(t, db, testKeyID, "active")
	insertStagedKey(t, db, testKeyID, hash)
	insertBinding(t, db, testKeyID, hash, 100, nil)

	const totalModels = 12
	eventID := int64(0)
	for m := 1; m <= totalModels; m++ {
		modelName := fmt.Sprintf("model-%02d", m)
		if _, err := db.Exec(`insert into model_prices (model, prompt_per_1m, completion_per_1m, cache_per_1m, prompt_configured, completion_configured, updated_at_ms)
			values (?, 1.0, 0.0, 0.0, 1, 1, 100)`, modelName); err != nil {
			t.Fatalf("insert price %s: %v", modelName, err)
		}
		calls := totalModels - m + 1
		for range calls {
			eventID++
			ts := 1000 + eventID
			if _, err := db.Exec(`insert into usage_events
				(id, event_hash, request_id, timestamp_ms, timestamp, model, api_key_hash, input_tokens, output_tokens, total_tokens, failed, created_at_ms)
				values (?, ?, ?, ?, '2026-01-01T00:00:01Z', ?, ?, 1000, 0, 1000, 0, 100)`,
				eventID, eventHash(hash, eventID), fmt.Sprintf("r-%d", eventID), ts, modelName, hash); err != nil {
				t.Fatalf("insert event %d: %v", eventID, err)
			}
			insertProjection(t, db, eventID, "mapped", testKeyID, hash, ts)
		}
	}

	windows, err := repo.Windows(ctx, []ports.KeyRange{{APIKeyID: testKeyID, FromMS: 500}}, 5000)
	if err != nil {
		t.Fatalf("Windows: %v", err)
	}
	win := windows[testKeyID]
	if win.ModelCount != totalModels || len(win.TopModels) != maxSummaryTopModels {
		t.Fatalf("ModelCount=%d len(TopModels)=%d, want %d and %d", win.ModelCount, len(win.TopModels), totalModels, maxSummaryTopModels)
	}
	for i := range maxSummaryTopModels {
		wantModel := fmt.Sprintf("model-%02d", i+1)
		wantCalls := int64(totalModels - i)
		wantTokens := wantCalls * 1000
		if win.TopModels[i].Model != wantModel || win.TopModels[i].Calls != wantCalls || win.TopModels[i].TotalTokens != wantTokens {
			t.Fatalf("TopModels[%d] = %+v, want model=%s calls=%d tokens=%d", i, win.TopModels[i], wantModel, wantCalls, wantTokens)
		}
	}
	// Total calls = 12*13/2 = 78, each costing $0.001 -> $0.078 including models beyond TopModels[:10].
	if win.EstimatedCostUSD == nil || math.Abs(*win.EstimatedCostUSD-0.078) > 1e-9 {
		t.Fatalf("EstimatedCostUSD = %v, want 0.078 across all 12 models", win.EstimatedCostUSD)
	}
}

func TestWindowsRecentSlotsCeilingBoundariesSub20msGuardAndZeroCallsNil(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	hash := sha256Hex("slots-secret")
	insertIdentity(t, db, testKeyID, "active")
	insertStagedKey(t, db, testKeyID, hash)
	insertBinding(t, db, testKeyID, hash, 100, nil)

	hashIdle := sha256Hex("idle-slots-secret")
	insertIdentity(t, db, otherKeyID, "active")
	insertStagedKey(t, db, otherKeyID, hashIdle)
	insertBinding(t, db, otherKeyID, hashIdle, 100, nil)

	// Prior call for otherKeyID at ts=500 so it appears in result with Calls=0 inside [1000, 1021).
	insertEvent(t, db, 1, hashIdle, 500, 10, 0)
	insertProjection(t, db, 1, "mapped", otherKeyID, hashIdle, 500)

	// Non-multiple-of-20 span [1000, 1021) -> span = 21:
	// slot 0 covers [1000, 1002): ts=1000 and ts=1001 both have ((offset*20)/21) == 0.
	// slot 1 covers [1002, 1003): ts=1002 has ((2*20)/21) == 1.
	// slot 19 covers [1020, 1021): ts=1020 has ((20*20)/21) == 19.
	events := []struct {
		id     int64
		ts     int64
		failed int64
	}{
		{2, 1000, 0},
		{3, 1001, 0},
		{4, 1002, 1},
		{5, 1020, 0},
	}
	for _, ev := range events {
		insertEvent(t, db, ev.id, hash, ev.ts, 10, ev.failed)
		insertProjection(t, db, ev.id, "mapped", testKeyID, hash, ev.ts)
	}

	windows, err := repo.Windows(ctx, []ports.KeyRange{
		{APIKeyID: testKeyID, FromMS: 1000},
		{APIKeyID: otherKeyID, FromMS: 1000},
	}, 1021)
	if err != nil {
		t.Fatalf("Windows(span=21): %v", err)
	}
	winIdle := windows[otherKeyID]
	if winIdle.Totals.Calls != 0 || winIdle.RecentSlots != nil {
		t.Fatalf("idle key window = %+v, want Calls=0 and RecentSlots=nil", winIdle)
	}

	win := windows[testKeyID]
	if len(win.RecentSlots) != ports.RecentStatusSlotCount {
		t.Fatalf("len(RecentSlots) = %d, want %d", len(win.RecentSlots), ports.RecentStatusSlotCount)
	}
	if win.RecentSlots[0].StartMS != 1000 || win.RecentSlots[19].EndMS != 1021 {
		t.Fatalf("RecentSlots outer bounds = [%d, %d), want [1000, 1021)", win.RecentSlots[0].StartMS, win.RecentSlots[19].EndMS)
	}
	for i, slot := range win.RecentSlots {
		if slot.EndMS <= slot.StartMS {
			t.Fatalf("slot[%d] = %+v has non-positive width", i, slot)
		}
		if i > 0 && slot.StartMS != win.RecentSlots[i-1].EndMS {
			t.Fatalf("slot[%d].StartMS = %d != slot[%d].EndMS = %d", i, slot.StartMS, i-1, win.RecentSlots[i-1].EndMS)
		}
	}
	if win.RecentSlots[0] != (ports.StatusSlot{StartMS: 1000, EndMS: 1002, Success: 2, Failure: 0}) {
		t.Fatalf("RecentSlots[0] = %+v, want [1000,1002) success=2 failure=0", win.RecentSlots[0])
	}
	if win.RecentSlots[1] != (ports.StatusSlot{StartMS: 1002, EndMS: 1003, Success: 0, Failure: 1}) {
		t.Fatalf("RecentSlots[1] = %+v, want [1002,1003) success=0 failure=1", win.RecentSlots[1])
	}
	if win.RecentSlots[19] != (ports.StatusSlot{StartMS: 1020, EndMS: 1021, Success: 1, Failure: 0}) {
		t.Fatalf("RecentSlots[19] = %+v, want [1020,1021) success=1 failure=0", win.RecentSlots[19])
	}

	// Sub-20ms span [1000, 1015) must omit RecentSlots cleanly instead of emitting zero-width slots.
	shortWin, err := repo.Windows(ctx, []ports.KeyRange{{APIKeyID: testKeyID, FromMS: 1000}}, 1015)
	if err != nil {
		t.Fatalf("Windows(span=15): %v", err)
	}
	if shortWin[testKeyID].Totals.Calls != 3 || shortWin[testKeyID].RecentSlots != nil {
		t.Fatalf("sub-20ms window = %+v, want Calls=3 and RecentSlots=nil", shortWin[testKeyID])
	}
}

func TestWindowsZeroTokenFailedSentinelAndPreRoutingAliasCostInvariants(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	const (
		pricedWithFailedKey    = identity.APIKeyID("5555555555555555555555555555555a")
		zeroTokenSucceededKey  = identity.APIKeyID("6666666666666666666666666666666b")
		onlyZeroTokenFailedKey = identity.APIKeyID("7777777777777777777777777777777c")
		tokenBearingFailedKey  = identity.APIKeyID("8888888888888888888888888888888d")
	)
	seedKey := func(id identity.APIKeyID, label string) string {
		h := sha256Hex(label)
		insertIdentity(t, db, string(id), "active")
		insertStagedKey(t, db, string(id), h)
		insertBinding(t, db, string(id), h, 100, nil)
		return h
	}
	hashPriced := seedKey(pricedWithFailedKey, "priced-with-zero-failed-key")
	hashSucceededZero := seedKey(zeroTokenSucceededKey, "succeeded-zero-unpriced-key")
	hashOnlyZeroFailed := seedKey(onlyZeroTokenFailedKey, "only-zero-failed-unpriced-key")
	hashTokenFailed := seedKey(tokenBearingFailedKey, "token-bearing-failed-unpriced-key")

	if _, err := db.Exec(`insert into model_prices (model, prompt_per_1m, completion_per_1m, cache_per_1m, prompt_configured, completion_configured, updated_at_ms)
		values ('claude-sonnet-4-5', 3.0, 15.0, 0.3, 1, 1, 100)`); err != nil {
		t.Fatalf("insert model price: %v", err)
	}

	// Key A: priced calls + 0-token failed "-" sentinel + 0-token failed pre-routing aliases (resolved_model = "").
	// Exact cost:
	//   e1 (claude-sonnet-4-5): 0.8M*3.0 + 0.2M*0.3 + 0.1M*15.0 = 3.96
	//   e2 (router-alias -> claude-sonnet-4-5): 0.1M*3.0 = 0.30
	//   e3 (router-alias -> "", failed=1, 0 tokens): $0 without suppressing router-alias or key cost
	//   e4 (unrouted-alias -> "", failed=1, 0 tokens): nil model cost without suppressing key cost
	//   e5 (model="-", failed=1, 0 tokens): excluded from ModelCount and TopModels, does not suppress key cost
	if _, err := db.Exec(`insert into usage_events (
		id, event_hash, request_id, timestamp_ms, timestamp, model, requested_model, resolved_model,
		api_key_hash, input_tokens, output_tokens, cache_read_tokens, total_tokens, failed, created_at_ms
	) values
	(1, ?, 'p1', 1000, '2026-01-01T00:00:01Z', 'claude-sonnet-4-5', 'claude-sonnet-4-5', 'claude-sonnet-4-5', ?, 1000000, 100000, 200000, 1100000, 0, 100),
	(2, ?, 'p2', 1100, '2026-01-01T00:00:02Z', 'router-alias', 'router-alias', 'claude-sonnet-4-5', ?, 100000, 0, 0, 100000, 0, 100),
	(3, ?, 'p3', 1200, '2026-01-01T00:00:03Z', 'router-alias', 'router-alias', '', ?, 0, 0, 0, 0, 1, 100),
	(4, ?, 'p4', 1300, '2026-01-01T00:00:04Z', 'unrouted-alias', 'unrouted-alias', '', ?, 0, 0, 0, 0, 1, 100),
	(5, ?, 'p5', 1400, '2026-01-01T00:00:05Z', '-', '', '', ?, 0, 0, 0, 0, 1, 100)`,
		eventHash(hashPriced, 1), hashPriced,
		eventHash(hashPriced, 2), hashPriced,
		eventHash(hashPriced, 3), hashPriced,
		eventHash(hashPriced, 4), hashPriced,
		eventHash(hashPriced, 5), hashPriced,
	); err != nil {
		t.Fatalf("insert Key A events: %v", err)
	}
	for id, ts := range map[int64]int64{1: 1000, 2: 1100, 3: 1200, 4: 1300, 5: 1400} {
		insertProjection(t, db, id, "mapped", string(pricedWithFailedKey), hashPriced, ts)
	}

	// Key B: priced call + 0-token failed=0 SUCCEEDED call on unpriced model -> STILL suppresses EstimatedCostUSD.
	if _, err := db.Exec(`insert into usage_events (
		id, event_hash, request_id, timestamp_ms, timestamp, model, requested_model, resolved_model,
		api_key_hash, input_tokens, output_tokens, total_tokens, failed, created_at_ms
	) values
	(6, ?, 's1', 2000, '2026-01-01T00:00:06Z', 'claude-sonnet-4-5', 'claude-sonnet-4-5', 'claude-sonnet-4-5', ?, 1000000, 0, 1000000, 0, 100),
	(7, ?, 's2', 2100, '2026-01-01T00:00:07Z', 'unpriced-zero-ok', 'unpriced-zero-ok', '', ?, 0, 0, 0, 0, 100)`,
		eventHash(hashSucceededZero, 6), hashSucceededZero,
		eventHash(hashSucceededZero, 7), hashSucceededZero,
	); err != nil {
		t.Fatalf("insert Key B events: %v", err)
	}
	insertProjection(t, db, 6, "mapped", string(zeroTokenSucceededKey), hashSucceededZero, 2000)
	insertProjection(t, db, 7, "mapped", string(zeroTokenSucceededKey), hashSucceededZero, 2100)

	// Key C: ONLY 0-token failed=1 unpriced calls -> EstimatedCostUSD remains nil (never fabricated as $0.00).
	if _, err := db.Exec(`insert into usage_events (
		id, event_hash, request_id, timestamp_ms, timestamp, model, requested_model, resolved_model,
		api_key_hash, input_tokens, output_tokens, total_tokens, failed, created_at_ms
	) values
	(8, ?, 'z1', 3000, '2026-01-01T00:00:08Z', '-', '', '', ?, 0, 0, 0, 1, 100),
	(9, ?, 'z2', 3100, '2026-01-01T00:00:09Z', 'unpriced-pre-route', 'unpriced-pre-route', '', ?, 0, 0, 0, 1, 100)`,
		eventHash(hashOnlyZeroFailed, 8), hashOnlyZeroFailed,
		eventHash(hashOnlyZeroFailed, 9), hashOnlyZeroFailed,
	); err != nil {
		t.Fatalf("insert Key C events: %v", err)
	}
	insertProjection(t, db, 8, "mapped", string(onlyZeroTokenFailedKey), hashOnlyZeroFailed, 3000)
	insertProjection(t, db, 9, "mapped", string(onlyZeroTokenFailedKey), hashOnlyZeroFailed, 3100)

	// Key D: priced call + token-bearing failed=1 unpriced call -> STILL suppresses EstimatedCostUSD.
	if _, err := db.Exec(`insert into usage_events (
		id, event_hash, request_id, timestamp_ms, timestamp, model, requested_model, resolved_model,
		api_key_hash, input_tokens, output_tokens, total_tokens, failed, created_at_ms
	) values
	(10, ?, 'tf1', 4000, '2026-01-01T00:00:10Z', 'claude-sonnet-4-5', 'claude-sonnet-4-5', 'claude-sonnet-4-5', ?, 1000000, 0, 1000000, 0, 100),
	(11, ?, 'tf2', 4100, '2026-01-01T00:00:11Z', 'unpriced-token-fail', 'unpriced-token-fail', '', ?, 500, 0, 500, 1, 100)`,
		eventHash(hashTokenFailed, 10), hashTokenFailed,
		eventHash(hashTokenFailed, 11), hashTokenFailed,
	); err != nil {
		t.Fatalf("insert Key D events: %v", err)
	}
	insertProjection(t, db, 10, "mapped", string(tokenBearingFailedKey), hashTokenFailed, 4000)
	insertProjection(t, db, 11, "mapped", string(tokenBearingFailedKey), hashTokenFailed, 4100)

	windows, err := repo.Windows(ctx, []ports.KeyRange{
		{APIKeyID: pricedWithFailedKey, FromMS: 500},
		{APIKeyID: zeroTokenSucceededKey, FromMS: 500},
		{APIKeyID: onlyZeroTokenFailedKey, FromMS: 500},
		{APIKeyID: tokenBearingFailedKey, FromMS: 500},
	}, 5000)
	if err != nil {
		t.Fatalf("Windows: %v", err)
	}

	winA := windows[pricedWithFailedKey]
	if winA.Totals.Calls != 5 || winA.Totals.Failure != 3 {
		t.Fatalf("Key A totals = %+v, want calls=5 failure=3", winA.Totals)
	}
	if winA.EstimatedCostUSD == nil || math.Abs(*winA.EstimatedCostUSD-4.26) > 1e-6 {
		t.Fatalf("Key A EstimatedCostUSD = %v, want exact 4.26", winA.EstimatedCostUSD)
	}
	if winA.ModelCount != 3 || len(winA.TopModels) != 3 {
		t.Fatalf("Key A ModelCount=%d TopModels=%+v, want 3 models without '-'", winA.ModelCount, winA.TopModels)
	}
	for _, entry := range winA.TopModels {
		if entry.Model == "-" {
			t.Fatalf("Key A TopModels leaked '-' sentinel: %+v", winA.TopModels)
		}
	}
	// Ranked order: router-alias (2 calls, $0.30), unrouted-alias (1 call, lastMS=1300, nil cost), claude-sonnet-4-5 (1 call, lastMS=1000, $3.96).
	if winA.TopModels[0].Model != "router-alias" || winA.TopModels[0].Calls != 2 ||
		winA.TopModels[0].EstimatedCostUSD == nil || math.Abs(*winA.TopModels[0].EstimatedCostUSD-0.30) > 1e-6 {
		t.Fatalf("Key A TopModels[0] = %+v, want router-alias calls=2 cost=0.30", winA.TopModels[0])
	}
	if winA.TopModels[1].Model != "unrouted-alias" || winA.TopModels[1].Calls != 1 || winA.TopModels[1].EstimatedCostUSD != nil {
		t.Fatalf("Key A TopModels[1] = %+v, want unrouted-alias calls=1 cost=nil", winA.TopModels[1])
	}
	if winA.TopModels[2].Model != "claude-sonnet-4-5" || winA.TopModels[2].Calls != 1 ||
		winA.TopModels[2].EstimatedCostUSD == nil || math.Abs(*winA.TopModels[2].EstimatedCostUSD-3.96) > 1e-6 {
		t.Fatalf("Key A TopModels[2] = %+v, want claude-sonnet-4-5 calls=1 cost=3.96", winA.TopModels[2])
	}

	winB := windows[zeroTokenSucceededKey]
	if winB.EstimatedCostUSD != nil {
		t.Fatalf("Key B (0-token succeeded unpriced call) EstimatedCostUSD = %v, want nil", *winB.EstimatedCostUSD)
	}
	if winB.ModelCount != 2 || len(winB.TopModels) != 2 {
		t.Fatalf("Key B TopModels = %+v (ModelCount=%d), want 2 models", winB.TopModels, winB.ModelCount)
	}

	winC := windows[onlyZeroTokenFailedKey]
	if winC.Totals.Calls != 2 || winC.Totals.Failure != 2 || winC.EstimatedCostUSD != nil {
		t.Fatalf("Key C (only 0-token failed unpriced calls) = %+v cost=%v, want calls=2 failure=2 cost=nil", winC.Totals, winC.EstimatedCostUSD)
	}
	if winC.ModelCount != 1 || len(winC.TopModels) != 1 || winC.TopModels[0].Model != "unpriced-pre-route" || winC.TopModels[0].EstimatedCostUSD != nil {
		t.Fatalf("Key C TopModels = %+v (ModelCount=%d), want single unpriced-pre-route with nil cost", winC.TopModels, winC.ModelCount)
	}

	winD := windows[tokenBearingFailedKey]
	if winD.EstimatedCostUSD != nil {
		t.Fatalf("Key D (token-bearing failed unpriced call) EstimatedCostUSD = %v, want nil", *winD.EstimatedCostUSD)
	}
}

func TestWindowsUnmigratedNullNormalizedCacheReadTokensFallback(t *testing.T) {
	db, repo := setupTestDB(t)
	ctx := context.Background()

	hashClaude := sha256Hex("unmigrated-claude-mirror")
	insertIdentity(t, db, testKeyID, "active")
	insertStagedKey(t, db, testKeyID, hashClaude)
	insertBinding(t, db, testKeyID, hashClaude, 100, nil)

	hashLegacy := sha256Hex("unmigrated-legacy-cached")
	insertIdentity(t, db, otherKeyID, "active")
	insertStagedKey(t, db, otherKeyID, hashLegacy)
	insertBinding(t, db, otherKeyID, hashLegacy, 100, nil)

	// Unmigrated Claude mirror row: normalized_cache_read_tokens IS NULL, cached_tokens=700 (= 200 read + 500 creation).
	// CachedTokens must aggregate 200, never 700.
	if _, err := db.Exec(`insert into usage_events (
		id, event_hash, request_id, timestamp_ms, timestamp, model, api_key_hash,
		input_tokens, output_tokens, cached_tokens, cache_read_tokens, cache_creation_tokens,
		normalized_cache_read_tokens, total_tokens, failed, created_at_ms
	) values
	(1, ?, 'cm1', 1000, '2026-01-01T00:00:01Z', 'claude-sonnet-4-5', ?, 1000, 100, 700, 200, 500, null, 1800, 0, 100),
	(2, ?, 'lg1', 1100, '2026-01-01T00:00:02Z', 'gpt-4o', ?, 1000, 100, 350, 0, 0, null, 1100, 0, 100)`,
		eventHash(hashClaude, 1), hashClaude,
		eventHash(hashLegacy, 2), hashLegacy,
	); err != nil {
		t.Fatalf("insert unmigrated cache events: %v", err)
	}
	insertProjection(t, db, 1, "mapped", testKeyID, hashClaude, 1000)
	insertProjection(t, db, 2, "mapped", otherKeyID, hashLegacy, 1100)

	windows, err := repo.Windows(ctx, []ports.KeyRange{
		{APIKeyID: testKeyID, FromMS: 500},
		{APIKeyID: otherKeyID, FromMS: 500},
	}, 5000)
	if err != nil {
		t.Fatalf("Windows: %v", err)
	}
	if got := windows[testKeyID].Totals.CachedTokens; got != 200 {
		t.Fatalf("unmigrated Claude mirror CachedTokens = %d, want 200", got)
	}
	if got := windows[otherKeyID].Totals.CachedTokens; got != 350 {
		t.Fatalf("unmigrated legacy OpenAI CachedTokens = %d, want 350", got)
	}
}

func TestWindowsRejectsSlotsFromMSExceedingFromMS(t *testing.T) {
	_, repo := setupTestDB(t)
	ctx := context.Background()

	if _, err := repo.Windows(ctx, []ports.KeyRange{
		{APIKeyID: testKeyID, FromMS: 2000, SlotsFromMS: 2500},
	}, 5000); err == nil {
		t.Fatal("expected validation error when SlotsFromMS > FromMS, got nil")
	}

	if _, err := repo.Windows(ctx, []ports.KeyRange{
		{APIKeyID: testKeyID, FromMS: 2000, SlotsFromMS: 1000},
		{APIKeyID: otherKeyID, FromMS: 2000, SlotsFromMS: 2000},
	}, 5000); err != nil {
		t.Fatalf("valid SlotsFromMS <= FromMS rejected: %v", err)
	}
}

type blockingModelPriceRepository struct {
	modelprice.Repository
	afterLoad chan struct{}
	resume    <-chan struct{}
}

func (b *blockingModelPriceRepository) LoadAllTx(ctx context.Context, tx *sql.Tx) (map[string]model.ModelPrice, error) {
	prices, err := b.Repository.LoadAllTx(ctx, tx)
	close(b.afterLoad)
	select {
	case <-b.resume:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return prices, err
}

func TestWindowsReadsModelPricesAndEventsFromSingleReadOnlySnapshot(t *testing.T) {
	db, rawRepo := setupTestDB(t)
	ctx := context.Background()

	hash := sha256Hex("snapshot-secret")
	insertIdentity(t, db, testKeyID, "active")
	insertStagedKey(t, db, testKeyID, hash)
	insertBinding(t, db, testKeyID, hash, 100, nil)

	priceRepo := modelprice.New(db)
	if err := priceRepo.ReplaceAll(ctx, map[string]model.ModelPrice{
		"model-a": {Prompt: 1.0, PromptConfigured: true},
	}); err != nil {
		t.Fatalf("seed initial price: %v", err)
	}
	if _, err := db.Exec(`insert into usage_events
		(id, event_hash, request_id, timestamp_ms, timestamp, model, api_key_hash, input_tokens, output_tokens, total_tokens, failed, created_at_ms)
		values (1, ?, 'snap-1', 1000, '2026-01-01T00:00:01Z', 'model-a', ?, 300000, 0, 300000, 0, 100)`,
		eventHash(hash, 1), hash); err != nil {
		t.Fatalf("insert initial event: %v", err)
	}
	insertProjection(t, db, 1, "mapped", testKeyID, hash, 1000)

	repoImpl := rawRepo.(*repository)
	afterLoad := make(chan struct{})
	resume := make(chan struct{})
	repoImpl.modelPrices = &blockingModelPriceRepository{
		Repository: priceRepo,
		afterLoad:  afterLoad,
		resume:     resume,
	}

	type winResult struct {
		windows map[identity.APIKeyID]ports.Window
		err     error
	}
	readDone := make(chan winResult, 1)
	go func() {
		w, wErr := rawRepo.Windows(ctx, []ports.KeyRange{{APIKeyID: testKeyID, FromMS: 500}}, 5000)
		readDone <- winResult{windows: w, err: wErr}
	}()
	<-afterLoad

	writerDone := make(chan error, 1)
	go func() {
		if err := priceRepo.ReplaceAll(ctx, map[string]model.ModelPrice{
			"model-a": {
				Prompt:           2.0,
				PromptConfigured: true,
				ContextTiers: []model.ModelPriceContextTier{{
					ThresholdTokens:  200_000,
					Prompt:           6.0,
					PromptConfigured: true,
				}},
			},
		}); err != nil {
			writerDone <- err
			return
		}
		if _, err := db.Exec(`insert into usage_events
			(id, event_hash, request_id, timestamp_ms, timestamp, model, api_key_hash, input_tokens, output_tokens, total_tokens, failed, created_at_ms)
			values (2, ?, 'snap-2', 2000, '2026-01-01T00:00:02Z', 'model-a', ?, 300000, 0, 300000, 0, 100)`,
			eventHash(hash, 2), hash); err != nil {
			writerDone <- err
			return
		}
		_, err := db.Exec(`insert into gateway_usage_identity_projection_v1
			(usage_event_id, event_hash, request_id, evidence_timestamp_ms, api_key_state, api_key_id, api_key_source_hash,
			 credential_state, credential_id, credential_source_auth_id, schema_version, projected_at_ms)
			values (2, ?, 'request', 2000, 'mapped', ?, ?, 'unknown', null, '', 1, 2000)`,
			eventHash(hash, 2), testKeyID, hash)
		writerDone <- err
	}()

	var writerErr error
	writerCompleted := false
	select {
	case writerErr = <-writerDone:
		writerCompleted = true
	case <-time.After(250 * time.Millisecond):
	}
	close(resume)
	snap := <-readDone
	if snap.err != nil {
		t.Fatalf("Windows snapshot read: %v", snap.err)
	}
	if !writerCompleted {
		writerErr = <-writerDone
	}
	if writerErr != nil {
		t.Fatalf("concurrent writer: %v", writerErr)
	}
	repoImpl.modelPrices = priceRepo

	first := snap.windows[testKeyID]
	// Snapshot must see initial 1 call and $0.30 (0.3M * $1.0), never mixed 2 calls or mismatched 200k tier band.
	if first.Totals.Calls != 1 || len(first.TopModels) != 1 || first.TopModels[0].Calls != 1 ||
		first.EstimatedCostUSD == nil || math.Abs(*first.EstimatedCostUSD-0.30) > 1e-6 {
		t.Fatalf("in-flight snapshot window = %+v, want calls=1 cost=0.30", first)
	}

	latest, err := rawRepo.Windows(ctx, []ports.KeyRange{{APIKeyID: testKeyID, FromMS: 500}}, 5000)
	if err != nil {
		t.Fatalf("latest Windows read: %v", err)
	}
	after := latest[testKeyID]
	// Subsequent read sees both 300k (>200k tier @ $6/1M) calls -> 0.6M * $6.0 = $3.60.
	if after.Totals.Calls != 2 || len(after.TopModels) != 1 || after.TopModels[0].Calls != 2 ||
		after.EstimatedCostUSD == nil || math.Abs(*after.EstimatedCostUSD-3.60) > 1e-6 {
		t.Fatalf("post-write snapshot window = %+v, want calls=2 cost=3.60", after)
	}
}
