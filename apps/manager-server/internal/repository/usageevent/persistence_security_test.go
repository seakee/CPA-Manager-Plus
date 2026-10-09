package usageevent

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestEventHashPersistenceBoundary(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	ctx := context.Background()

	// 1. Canonical lowercase SHA-256 hex
	validLowerHash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	res, err := repo.InsertBatch(ctx, []usage.Event{
		makeBaseTestEvent(validLowerHash, 1000),
	})
	if err != nil {
		t.Fatalf("insert canonical hash failed: %v", err)
	}
	if res.Inserted != 1 || len(res.InsertedEventHashes) != 1 || res.InsertedEventHashes[0] != validLowerHash {
		t.Fatalf("unexpected insert result: %+v", res)
	}

	// 2. Canonical uppercase SHA-256 hex (must preserve exact value)
	validUpperHash := "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"
	resUpper, err := repo.InsertBatch(ctx, []usage.Event{
		makeBaseTestEvent(validUpperHash, 2000),
	})
	if err != nil {
		t.Fatalf("insert canonical uppercase hash failed: %v", err)
	}
	if resUpper.Inserted != 1 || resUpper.InsertedEventHashes[0] != validUpperHash {
		t.Fatalf("uppercase hash not preserved exactly: %+v", resUpper)
	}

	// 3. New noncanonical event hash must return ErrInvalidEventHash
	invalidHashes := []string{
		"short-hash",
		"not-a-hash-at-all",
		"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeg", // 'g' is not hex
		"",
	}
	for _, inv := range invalidHashes {
		_, err := repo.InsertBatch(ctx, []usage.Event{
			makeBaseTestEvent(inv, 3000),
		})
		if !errors.Is(err, ErrInvalidEventHash) {
			t.Fatalf("expected ErrInvalidEventHash for hash %q, got: %v", inv, err)
		}
	}

	// 4. Historical noncanonical duplicate: if historical row exists, it must be recognized as duplicate
	historicalHash := "legacy-noncanonical-raw-hash-12345"
	// Seed historical row directly into usage_events table
	_, err = db.ExecContext(ctx, `insert into usage_events (event_hash, timestamp_ms, timestamp, model, total_tokens, created_at_ms)
		values (?, 4000, '2026-01-01T00:00:04Z', 'gpt-test', 10, 4000)`, historicalHash)
	if err != nil {
		t.Fatalf("seed historical event: %v", err)
	}

	// Calling InsertBatch with this historical hash must not fail with ErrInvalidEventHash,
	// and must deduplicate it (Skipped == 1, Inserted == 0) and backfill ledger.
	dupRes, err := repo.InsertBatch(ctx, []usage.Event{
		makeBaseTestEvent(historicalHash, 4000),
	})
	if err != nil {
		t.Fatalf("historical duplicate InsertBatch failed: %v", err)
	}
	if dupRes.Skipped != 1 || dupRes.Inserted != 0 {
		t.Fatalf("expected historical duplicate to be skipped, got: %+v", dupRes)
	}

	// Verify ledger backfill
	var ledgerCount int
	err = db.QueryRowContext(ctx, `select count(*) from usage_event_identity_ledger where event_hash = ?`, historicalHash).Scan(&ledgerCount)
	if err != nil || ledgerCount != 1 {
		t.Fatalf("expected historical ledger entry backfilled, count=%d, err=%v", ledgerCount, err)
	}
}

func TestSensitiveSourcePseudonymizationRoundTrip(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	ctx := context.Background()

	// Case 1: Known-prefix synthetic API key in Source
	secretKey := "sk-proj-syntheticKeyDirectlyInSource1234567890"
	h := sha256.Sum256([]byte(secretKey))
	expectedHex := hex.EncodeToString(h[:])
	expectedPseudonym := "h:" + expectedHex

	hash1 := canonicalTestHash("event-with-secret-source")
	event1 := makeBaseTestEvent(hash1, 1000)
	event1.Source = secretKey
	event1.FailBody = `{"cpaManagementKey":"cpa-management-key-sample-123456789","detail":"request failed"}`
	event1.FailSummary = `{"cpaManagementKey":"cpa-management-key-sample-123456789","short":"fail"}`
	event1.RawJSON = `{"key":"cpa-management-key-sample-123456789","model":"gpt-test"}`
	event1.ResponseMetadataJSON = `{"Authorization":"Bearer my-auth-token-123456789"}`
	event1.APIKeyHash = "raw-secret-apikey-sample"
	expectedAPIKeyHex := sha256Hex("raw-secret-apikey-sample")

	res, err := repo.InsertBatch(ctx, []usage.Event{event1})
	if err != nil {
		t.Fatalf("insert event with sensitive source: %v", err)
	}
	if res.Inserted != 1 {
		t.Fatalf("expected 1 inserted, got: %+v", res)
	}

	// Query SQLite directly to verify actual persisted columns
	var (
		source, sourceHash, failBody, failSummary, respMetaJSON, rawJSON string
		apiKeyHash                                                       sql.NullString
	)
	err = db.QueryRowContext(ctx, `
		SELECT source, source_hash, api_key_hash, fail_body, fail_summary, response_metadata_json, raw_json
		FROM usage_events WHERE event_hash = ?`, hash1).
		Scan(&source, &sourceHash, &apiKeyHash, &failBody, &failSummary, &respMetaJSON, &rawJSON)
	if err != nil {
		t.Fatalf("query row for hash1: %v", err)
	}

	if !apiKeyHash.Valid || apiKeyHash.String != expectedAPIKeyHex {
		t.Fatalf("APIKeyHash not normalized to sha256 in DB: got %v, want %q", apiKeyHash, expectedAPIKeyHex)
	}

	if source != expectedPseudonym {
		t.Fatalf("Source not pseudonymized in DB: got %q, want %q", source, expectedPseudonym)
	}
	if sourceHash != expectedHex {
		t.Fatalf("SourceHash not populated in DB: got %q, want %q", sourceHash, expectedHex)
	}
	if strings.Contains(source, "syntheticKeyDirectlyInSource") {
		t.Fatalf("Source leaked plaintext credential in DB: %q", source)
	}
	if strings.Contains(failBody, "cpa-management-key-sample-123456789") {
		t.Fatalf("FailBody leaked secret in DB: %s", failBody)
	}
	if !strings.Contains(failBody, `"detail":"request failed"`) {
		t.Fatalf("FailBody lost non-secret diagnostic content: %s", failBody)
	}
	if strings.Contains(failSummary, "cpa-management-key-sample-123456789") {
		t.Fatalf("FailSummary leaked secret in DB: %s", failSummary)
	}
	if strings.Contains(rawJSON, "cpa-management-key-sample-123456789") {
		t.Fatalf("RawJSON leaked secret in DB: %s", rawJSON)
	}
	if strings.Contains(respMetaJSON, "my-auth-token-123456789") {
		t.Fatalf("ResponseMetadataJSON leaked secret in DB: %s", respMetaJSON)
	}

	// Case 2: Plain unprefixed API key correlated with APIKeyHash
	plainKey := "ordinary-unprefixed-key"
	plainSum := sha256.Sum256([]byte(plainKey))
	plainHex := hex.EncodeToString(plainSum[:])
	plainPseudonym := "h:" + plainHex

	hash2 := canonicalTestHash("event-with-unprefixed-key-source")
	event2 := makeBaseTestEvent(hash2, 2000)
	event2.Source = plainKey
	event2.APIKeyHash = plainHex // Parser computed APIKeyHash = SHA256(plainKey)

	res2, err := repo.InsertBatch(ctx, []usage.Event{event2})
	if err != nil {
		t.Fatalf("insert event with plain key source: %v", err)
	}
	if res2.Inserted != 1 {
		t.Fatalf("expected 1 inserted, got: %+v", res2)
	}

	var (
		source2, sourceHash2 string
		apiKeyHash2          sql.NullString
	)
	err = db.QueryRowContext(ctx, `
		SELECT source, source_hash, api_key_hash
		FROM usage_events WHERE event_hash = ?`, hash2).
		Scan(&source2, &sourceHash2, &apiKeyHash2)
	if err != nil {
		t.Fatalf("query row for hash2: %v", err)
	}

	if source2 != plainPseudonym {
		t.Fatalf("plain unprefixed key Source not pseudonymized: got %q, want %q", source2, plainPseudonym)
	}
	if sourceHash2 != plainHex {
		t.Fatalf("plain key SourceHash not set to sha256 hex: got %q, want %q", sourceHash2, plainHex)
	}
	if !apiKeyHash2.Valid || apiKeyHash2.String != plainHex {
		t.Fatalf("apiKeyHash not preserved: got %v, want %q", apiKeyHash2, plainHex)
	}

	// Case 3: Safe filename source must NOT be pseudonymized
	safeSource := "sk-account.json"
	hash3 := canonicalTestHash("event-with-safe-filename-source")
	event3 := makeBaseTestEvent(hash3, 3000)
	event3.Source = safeSource

	res3, err := repo.InsertBatch(ctx, []usage.Event{event3})
	if err != nil {
		t.Fatalf("insert event with safe filename source: %v", err)
	}
	if res3.Inserted != 1 {
		t.Fatalf("expected 1 inserted, got: %+v", res3)
	}

	var source3 string
	err = db.QueryRowContext(ctx, `SELECT source FROM usage_events WHERE event_hash = ?`, hash3).Scan(&source3)
	if err != nil {
		t.Fatalf("query row for hash3: %v", err)
	}
	if source3 != safeSource {
		t.Fatalf("safe filename source wrongly pseudonymized: got %q, want %q", source3, safeSource)
	}
}

func TestInsertBatchDuplicatePreflightOptimization(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	ctx := context.Background()

	hashA := canonicalTestHash("event-batch-a")
	hashB := canonicalTestHash("event-batch-b")
	hashC := canonicalTestHash("event-batch-c")

	// Step 1: Insert A and B
	res1, err := repo.InsertBatch(ctx, []usage.Event{
		makeBaseTestEvent(hashA, 1000),
		makeBaseTestEvent(hashB, 2000),
	})
	if err != nil || res1.Inserted != 2 {
		t.Fatalf("first insert failed: %+v, err: %v", res1, err)
	}

	// Step 2: Insert full duplicate batch [A, B]
	// Preflight should skip without needing to insert anything
	res2, err := repo.InsertBatch(ctx, []usage.Event{
		makeBaseTestEvent(hashA, 1000),
		makeBaseTestEvent(hashB, 2000),
	})
	if err != nil {
		t.Fatalf("duplicate batch insert failed: %v", err)
	}
	if res2.Inserted != 0 || res2.Skipped != 2 || len(res2.InsertedEventHashes) != 0 {
		t.Fatalf("expected all skipped in preflight, got: %+v", res2)
	}

	// Step 3: Mixed batch [A, C] (A is duplicate, C is new)
	res3, err := repo.InsertBatch(ctx, []usage.Event{
		makeBaseTestEvent(hashA, 1000),
		makeBaseTestEvent(hashC, 3000),
	})
	if err != nil {
		t.Fatalf("mixed batch insert failed: %v", err)
	}
	if res3.Inserted != 1 || res3.Skipped != 1 || len(res3.InsertedEventHashes) != 1 || res3.InsertedEventHashes[0] != hashC {
		t.Fatalf("expected 1 inserted (hashC) and 1 skipped, got: %+v", res3)
	}
}

func TestInsertedEventHashesContract(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	ctx := context.Background()

	hashes := []string{
		canonicalTestHash("order-test-1"),
		canonicalTestHash("order-test-2"),
		canonicalTestHash("order-test-3"),
	}
	events := []usage.Event{
		makeBaseTestEvent(hashes[0], 1000),
		makeBaseTestEvent(hashes[1], 2000),
		makeBaseTestEvent(hashes[2], 3000),
	}

	res, err := repo.InsertBatch(ctx, events)
	if err != nil {
		t.Fatalf("insert batch: %v", err)
	}
	if res.Inserted != 3 {
		t.Fatalf("inserted count = %d, want 3", res.Inserted)
	}
	if len(res.InsertedEventHashes) != 3 {
		t.Fatalf("inserted event hashes length = %d, want 3", len(res.InsertedEventHashes))
	}
	for i := range hashes {
		if res.InsertedEventHashes[i] != hashes[i] {
			t.Fatalf("hash at index %d mismatch: got %q want %q", i, res.InsertedEventHashes[i], hashes[i])
		}
	}
}

func TestTransactionAtomicityOnFailure(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately to induce failure during tx

	hash1 := canonicalTestHash("atomicity-test-1")
	events := []usage.Event{
		makeBaseTestEvent(hash1, 1000),
	}

	_, err = repo.InsertBatch(ctx, events)
	if err == nil {
		t.Fatalf("expected error from canceled context")
	}

	// Verify no orphaned ledger record was persisted
	var ledgerCount int
	err = db.QueryRowContext(context.Background(), `select count(*) from usage_event_identity_ledger where event_hash = ?`, hash1).Scan(&ledgerCount)
	if err != nil {
		t.Fatalf("query ledger count: %v", err)
	}
	if ledgerCount != 0 {
		t.Fatalf("orphaned ledger entry survived rollback, count = %d", ledgerCount)
	}
}

func TestUppercaseAPIKeyHashCorrelationRoundTrip(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	ctx := context.Background()

	plainKey := "ordinary-unprefixed-source-key"
	h := sha256.Sum256([]byte(plainKey))
	lowerHex := hex.EncodeToString(h[:])
	upperHex := strings.ToUpper(lowerHex)
	expectedPseudonym := "h:" + lowerHex

	hash := canonicalTestHash("event-with-uppercase-apikey-hash")
	event := makeBaseTestEvent(hash, 1000)
	event.Source = plainKey
	event.APIKeyHash = upperHex // uppercase SHA-256

	res, err := repo.InsertBatch(ctx, []usage.Event{event})
	if err != nil {
		t.Fatalf("insert event with uppercase APIKeyHash: %v", err)
	}
	if res.Inserted != 1 {
		t.Fatalf("expected 1 inserted, got: %+v", res)
	}

	var (
		source, sourceHash string
		apiKeyHash         sql.NullString
	)
	err = db.QueryRowContext(ctx, `
		SELECT source, source_hash, api_key_hash
		FROM usage_events WHERE event_hash = ?`, hash).
		Scan(&source, &sourceHash, &apiKeyHash)
	if err != nil {
		t.Fatalf("query row for uppercase APIKeyHash event: %v", err)
	}

	if source != expectedPseudonym {
		t.Fatalf("Source not pseudonymized: got %q, want %q", source, expectedPseudonym)
	}
	if sourceHash != lowerHex {
		t.Fatalf("SourceHash not lowercase sha256: got %q, want %q", sourceHash, lowerHex)
	}
	if !apiKeyHash.Valid || apiKeyHash.String != upperHex {
		t.Fatalf("APIKeyHash uppercase not preserved in DB: got %v, want %q", apiKeyHash, upperHex)
	}
}

func TestResponseMetadataAuthorizationErrorPersistence(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	ctx := context.Background()

	hash := canonicalTestHash("event-with-auth-error-meta")
	event := makeBaseTestEvent(hash, 2000)
	event.ResponseMetadataJSON = `{"errors":{"authorization_error":"HTTP 401 cpaManagementKey=ordinary-secret auth failed code=401"},"status":401}`

	res, err := repo.InsertBatch(ctx, []usage.Event{event})
	if err != nil {
		t.Fatalf("insert event with auth error metadata: %v", err)
	}
	if res.Inserted != 1 {
		t.Fatalf("expected 1 inserted, got: %+v", res)
	}

	var respMetaJSON string
	err = db.QueryRowContext(ctx, `
		SELECT response_metadata_json
		FROM usage_events WHERE event_hash = ?`, hash).
		Scan(&respMetaJSON)
	if err != nil {
		t.Fatalf("query row: %v", err)
	}

	if strings.Contains(respMetaJSON, "ordinary-secret") {
		t.Fatalf("ResponseMetadataJSON leaked secret in DB: %s", respMetaJSON)
	}
	if !strings.Contains(respMetaJSON, "HTTP 401") {
		t.Fatalf("ResponseMetadataJSON dropped HTTP 401 in DB: %s", respMetaJSON)
	}
	if !strings.Contains(respMetaJSON, "auth failed code=401") {
		t.Fatalf("ResponseMetadataJSON dropped diagnostic details in DB: %s", respMetaJSON)
	}
	if !strings.Contains(respMetaJSON, "authorization_error") {
		t.Fatalf("authorization_error key was missing in DB: %s", respMetaJSON)
	}
	if !strings.Contains(respMetaJSON, "errors") {
		t.Fatalf("errors parent object was missing in DB: %s", respMetaJSON)
	}
}

func makeBaseTestEvent(hash string, timestampMS int64) usage.Event {
	return usage.Event{
		EventHash:        hash,
		TimestampMS:      timestampMS,
		Timestamp:        time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Provider:         "codex",
		Model:            "gpt-5",
		AuthFileSnapshot: "account.json",
		AuthIndex:        "auth-1",
		Source:           "account.json",
		InputTokens:      10,
		OutputTokens:     5,
		TotalTokens:      15,
		CreatedAtMS:      timestampMS,
	}
}

func TestInsertBatchArchiveRestoreSecurityWithDerivedFieldPreservation(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	ctx := context.Background()

	hash := canonicalTestHash("archive-restore-cross-boundary-test")
	secretToken := "my-secret-bearer-token-12345678"
	secretKey := "sk-proj-archiveSecretKeyDirectlyInSource12345"
	secretAPIKey := "secret-api-key-value-99999"

	quotaUsed := 42.5
	event := usage.Event{
		EventHash:                     hash,
		TimestampMS:                   1778000000000,
		Timestamp:                     time.UnixMilli(1778000000000).UTC().Format(time.RFC3339Nano),
		Provider:                      "codex",
		ExecutorType:                  "CodexExecutor",
		Model:                         "gpt-5",
		RequestedModel:                "gpt-5",
		ResolvedModel:                 "gpt-5",
		Endpoint:                      "POST /v1/responses",
		Method:                        "POST",
		Path:                          "/v1/responses",
		AuthType:                      "oauth",
		AuthIndex:                     "auth-1",
		Source:                        secretKey,
		InputTokens:                   100,
		OutputTokens:                  50,
		ReasoningTokens:               25,
		CachedTokens:                  20,
		CacheReadTokens:               20,
		CacheCreationTokens:           10,
		CreatedAtMS:                   1778000000000,
		PreserveArchiveDerivedFields:  true,
		CacheInputMode:                "separate_from_input",
		NormalizedUncachedInputTokens: 777,
		NormalizedTotalInputTokens:    888,
		NormalizedCacheReadTokens:     333,
		NormalizedCacheCreationTokens: 444,
		TotalTokens:                   9999,
		ServiceTier:                   "default",
		RequestServiceTier:            "priority",
		ResponseServiceTier:           "default",
		FailBody:                      "Authorization: Bearer " + secretToken,
		FailSummary:                   "Error: Authorization: Bearer " + secretToken,
		RawJSON:                       `{"api_key":"` + secretAPIKey + `","model":"gpt-5"}`,
		ResponseMetadataJSON:          `{"Authorization":"Bearer ` + secretToken + `"}`,
		HeaderQuotaRecoverAtMS:        1778000100000,
		HeaderQuotaUsedPercent:        &quotaUsed,
		HeaderQuotaPlanType:           "archive-plan",
		HeaderErrorKind:               "archive-error",
		HeaderErrorCode:               "ERR_ARCHIVE",
		HeaderTraceID:                 "trace-archive-123",
	}

	res, err := repo.InsertBatch(ctx, []usage.Event{event})
	if err != nil {
		t.Fatalf("InsertBatch archive event: %v", err)
	}
	if res.Inserted != 1 {
		t.Fatalf("expected 1 inserted, got: %+v", res)
	}

	// Query SQLite directly to verify actual persisted columns
	var (
		source, failBody, failSummary, rawJSON, respMetaJSON string
		serviceTier, cacheInputMode                          string
		uncachedTokens, totalInputTokens                     int64
		cacheReadTokens, cacheCreationTokens                 int64
		totalTokens                                          int64
		quotaRecoverAtMS                                     int64
		quotaUsedPercent                                     float64
		quotaPlanType, errorKind, errorCode, traceID         string
	)
	err = db.QueryRowContext(ctx, `
		SELECT source, fail_body, fail_summary, raw_json, response_metadata_json,
		       service_tier, cache_input_mode,
		       normalized_uncached_input_tokens, normalized_total_input_tokens,
		       normalized_cache_read_tokens, normalized_cache_creation_tokens,
		       total_tokens,
		       coalesce(header_quota_recover_at_ms, 0), coalesce(header_quota_used_percent, 0),
		       coalesce(header_quota_plan_type, ''), coalesce(header_error_kind, ''),
		       coalesce(header_error_code, ''), coalesce(header_trace_id, '')
		FROM usage_events WHERE event_hash = ?`, hash).
		Scan(
			&source, &failBody, &failSummary, &rawJSON, &respMetaJSON,
			&serviceTier, &cacheInputMode,
			&uncachedTokens, &totalInputTokens,
			&cacheReadTokens, &cacheCreationTokens,
			&totalTokens,
			&quotaRecoverAtMS, &quotaUsedPercent,
			&quotaPlanType, &errorKind, &errorCode, &traceID,
		)
	if err != nil {
		t.Fatalf("query row for archive event: %v", err)
	}

	// Assertion A: Security sanitizer MUST have run on all fields
	if strings.Contains(source, "archiveSecretKeyDirectlyInSource") {
		t.Fatalf("Source leaked credential in DB: %q", source)
	}
	if strings.Contains(failBody, secretToken) {
		t.Fatalf("FailBody leaked secret token in DB: %q", failBody)
	}
	if strings.Contains(failSummary, secretToken) {
		t.Fatalf("FailSummary leaked secret token in DB: %q", failSummary)
	}
	if strings.Contains(rawJSON, secretAPIKey) {
		t.Fatalf("RawJSON leaked secret api_key in DB: %q", rawJSON)
	}
	if strings.Contains(respMetaJSON, secretToken) {
		t.Fatalf("ResponseMetadataJSON leaked secret token in DB: %q", respMetaJSON)
	}

	// Assertion B: Historical derived fields MUST NOT be recalculated
	if uncachedTokens != 777 {
		t.Fatalf("normalized_uncached_input_tokens = %d, want 777", uncachedTokens)
	}
	if totalInputTokens != 888 {
		t.Fatalf("normalized_total_input_tokens = %d, want 888", totalInputTokens)
	}
	if cacheReadTokens != 333 {
		t.Fatalf("normalized_cache_read_tokens = %d, want 333", cacheReadTokens)
	}
	if cacheCreationTokens != 444 {
		t.Fatalf("normalized_cache_creation_tokens = %d, want 444", cacheCreationTokens)
	}
	if totalTokens != 9999 {
		t.Fatalf("total_tokens = %d, want 9999 (should not recalculate)", totalTokens)
	}
	if serviceTier != "default" {
		t.Fatalf("service_tier = %q, want %q (should preserve archive value instead of recalculating to %q)", serviceTier, "default", "priority")
	}
	if quotaRecoverAtMS != 1778000100000 {
		t.Fatalf("header_quota_recover_at_ms = %d, want 1778000100000", quotaRecoverAtMS)
	}
	if quotaUsedPercent != 42.5 {
		t.Fatalf("header_quota_used_percent = %f, want 42.5", quotaUsedPercent)
	}
	if quotaPlanType != "archive-plan" {
		t.Fatalf("header_quota_plan_type = %q, want archive-plan", quotaPlanType)
	}
	if errorKind != "archive-error" {
		t.Fatalf("header_error_kind = %q, want archive-error", errorKind)
	}
	if errorCode != "ERR_ARCHIVE" {
		t.Fatalf("header_error_code = %q, want ERR_ARCHIVE", errorCode)
	}
	if traceID != "trace-archive-123" {
		t.Fatalf("header_trace_id = %q, want trace-archive-123", traceID)
	}
}

func TestInsertBatchArchiveRestoreDoesNotBypassCanonicalEventHash(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	ctx := context.Background()

	event := makeBaseTestEvent("short-noncanonical-hash", 1000)
	event.PreserveArchiveDerivedFields = true
	event.CacheInputMode = "included_in_input"

	_, err = repo.InsertBatch(ctx, []usage.Event{event})
	if !errors.Is(err, ErrInvalidEventHash) {
		t.Fatalf("expected ErrInvalidEventHash for archive restore with noncanonical hash, got: %v", err)
	}
}
