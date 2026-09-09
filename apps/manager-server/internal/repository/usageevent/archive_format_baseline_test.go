package usageevent

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

const (
	usageArchiveBaselineEnabledEnv = "CPA_MANAGER_USAGE_ARCHIVE_BASELINE"
	usageArchiveBaselineCountsEnv  = "CPA_MANAGER_USAGE_ARCHIVE_BASELINE_COUNTS"
)

type archiveBaselineRecord struct {
	ArchiveEventID int64 `json:"_cpamp_archive_event_id"`
	usage.Event
	FailBody             string `json:"fail_body,omitempty"`
	ResponseMetadataJSON string `json:"response_metadata_json,omitempty"`
}

type archiveBaselineResult struct {
	Path           string
	Bytes          int64
	WriteElapsed   time.Duration
	VerifyElapsed  time.Duration
	RestoreElapsed time.Duration
	Digest         string
	Records        int
}

// TestUsageArchiveFormatBaseline is intentionally opt-in. It compares the two
// Phase 4 archive candidates with identical deterministic records without
// affecting normal unit-test or CI duration.
func TestUsageArchiveFormatBaseline(t *testing.T) {
	if os.Getenv(usageArchiveBaselineEnabledEnv) != "1" {
		t.Skipf("set %s=1 to run the usage archive format baseline", usageArchiveBaselineEnabledEnv)
	}

	counts := parsePositiveIntList(os.Getenv(usageArchiveBaselineCountsEnv), []int{100_000})
	for _, count := range counts {
		t.Run(fmt.Sprintf("events_%d", count), func(t *testing.T) {
			directory := t.TempDir()
			gzipResult := runGzipJSONLBaseline(t, directory, count)
			sqliteResult := runSQLiteSegmentBaseline(t, directory, count)

			if gzipResult.Records != count || sqliteResult.Records != count {
				t.Fatalf("record counts gzip=%d sqlite=%d want=%d", gzipResult.Records, sqliteResult.Records, count)
			}
			if gzipResult.Digest != sqliteResult.Digest {
				t.Fatalf("format digests differ: gzip=%s sqlite=%s", gzipResult.Digest, sqliteResult.Digest)
			}

			t.Logf(
				"format=gzip-jsonl events=%d bytes=%d write=%s verify=%s restore=%s digest=%s",
				count,
				gzipResult.Bytes,
				gzipResult.WriteElapsed,
				gzipResult.VerifyElapsed,
				gzipResult.RestoreElapsed,
				gzipResult.Digest,
			)
			t.Logf(
				"format=sqlite-segment events=%d bytes=%d write=%s verify=%s restore=%s digest=%s",
				count,
				sqliteResult.Bytes,
				sqliteResult.WriteElapsed,
				sqliteResult.VerifyElapsed,
				sqliteResult.RestoreElapsed,
				sqliteResult.Digest,
			)
		})
	}
}

func runGzipJSONLBaseline(t *testing.T, directory string, count int) archiveBaselineResult {
	t.Helper()
	path := filepath.Join(directory, "usage-archive.jsonl.gz")
	started := time.Now()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create gzip archive: %v", err)
	}
	zipper, err := gzip.NewWriterLevel(file, gzip.BestSpeed)
	if err != nil {
		_ = file.Close()
		t.Fatalf("create gzip writer: %v", err)
	}
	buffer := bufio.NewWriterSize(zipper, 64*1024)
	encoder := json.NewEncoder(buffer)
	for index := range count {
		if err := encoder.Encode(archiveBaselineEvent(index)); err != nil {
			_ = file.Close()
			t.Fatalf("encode gzip record %d: %v", index, err)
		}
	}
	if err := buffer.Flush(); err != nil {
		_ = file.Close()
		t.Fatalf("flush gzip buffer: %v", err)
	}
	if err := zipper.Close(); err != nil {
		_ = file.Close()
		t.Fatalf("close gzip writer: %v", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		t.Fatalf("sync gzip archive: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close gzip archive: %v", err)
	}
	writeElapsed := time.Since(started)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat gzip archive: %v", err)
	}

	verifyStarted := time.Now()
	records, digest := verifyGzipJSONL(t, path)
	verifyElapsed := time.Since(verifyStarted)
	restoreStarted := time.Now()
	restoreGzipJSONL(t, path, count)
	restoreElapsed := time.Since(restoreStarted)
	return archiveBaselineResult{
		Path:           path,
		Bytes:          info.Size(),
		WriteElapsed:   writeElapsed,
		VerifyElapsed:  verifyElapsed,
		RestoreElapsed: restoreElapsed,
		Digest:         digest,
		Records:        records,
	}
}

func runSQLiteSegmentBaseline(t *testing.T, directory string, count int) archiveBaselineResult {
	t.Helper()
	path := filepath.Join(directory, "usage-archive.sqlite")
	started := time.Now()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open archive sqlite: %v", err)
	}
	if _, err := db.Exec(`pragma journal_mode = delete; pragma synchronous = full; pragma temp_store = memory`); err != nil {
		_ = db.Close()
		t.Fatalf("configure archive sqlite: %v", err)
	}
	if _, err := db.Exec(`create table archive_events (
		event_id integer primary key,
		event_hash text not null unique,
		timestamp_ms integer not null,
		payload_json blob not null
	)`); err != nil {
		_ = db.Close()
		t.Fatalf("create archive table: %v", err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		_ = db.Close()
		t.Fatalf("begin archive transaction: %v", err)
	}
	statement, err := tx.Prepare(`insert into archive_events (event_id, event_hash, timestamp_ms, payload_json) values (?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		t.Fatalf("prepare archive insert: %v", err)
	}
	for index := range count {
		record := archiveBaselineEvent(index)
		payload, err := json.Marshal(record)
		if err != nil {
			_ = statement.Close()
			_ = tx.Rollback()
			_ = db.Close()
			t.Fatalf("marshal sqlite record %d: %v", index, err)
		}
		if _, err := statement.Exec(record.ArchiveEventID, record.EventHash, record.TimestampMS, payload); err != nil {
			_ = statement.Close()
			_ = tx.Rollback()
			_ = db.Close()
			t.Fatalf("insert sqlite record %d: %v", index, err)
		}
	}
	if err := statement.Close(); err != nil {
		_ = tx.Rollback()
		_ = db.Close()
		t.Fatalf("close archive insert: %v", err)
	}
	if err := tx.Commit(); err != nil {
		_ = db.Close()
		t.Fatalf("commit archive sqlite: %v", err)
	}
	if _, err := db.Exec(`pragma optimize`); err != nil {
		_ = db.Close()
		t.Fatalf("optimize archive sqlite: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close archive sqlite: %v", err)
	}
	writeElapsed := time.Since(started)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat archive sqlite: %v", err)
	}

	verifyStarted := time.Now()
	records, digest := verifySQLiteSegment(t, path)
	verifyElapsed := time.Since(verifyStarted)
	restoreStarted := time.Now()
	restoreSQLiteSegment(t, path, count)
	restoreElapsed := time.Since(restoreStarted)
	return archiveBaselineResult{
		Path:           path,
		Bytes:          info.Size(),
		WriteElapsed:   writeElapsed,
		VerifyElapsed:  verifyElapsed,
		RestoreElapsed: restoreElapsed,
		Digest:         digest,
		Records:        records,
	}
}

func verifyGzipJSONL(t *testing.T, path string) (int, string) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open gzip archive: %v", err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("open gzip reader: %v", err)
	}
	defer reader.Close()
	return verifyArchiveJSONL(t, reader)
}

func verifyArchiveJSONL(t *testing.T, reader io.Reader) (int, string) {
	t.Helper()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), usage.MaxJSONLRecordBytes+1)
	digest := sha256.New()
	count := 0
	for scanner.Scan() {
		var record archiveBaselineRecord
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("decode archive record %d: %v", count, err)
		}
		appendArchiveDigest(digest, record.ArchiveEventID, record.EventHash)
		count++
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan archive jsonl: %v", err)
	}
	return count, hex.EncodeToString(digest.Sum(nil))
}

func verifySQLiteSegment(t *testing.T, path string) (int, string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite segment for verification: %v", err)
	}
	defer db.Close()
	rows, err := db.Query(`select event_id, event_hash, payload_json from archive_events order by event_id`)
	if err != nil {
		t.Fatalf("query sqlite segment: %v", err)
	}
	defer rows.Close()
	digest := sha256.New()
	count := 0
	for rows.Next() {
		var eventID int64
		var eventHash string
		var payload []byte
		if err := rows.Scan(&eventID, &eventHash, &payload); err != nil {
			t.Fatalf("scan sqlite segment %d: %v", count, err)
		}
		var record archiveBaselineRecord
		if err := json.Unmarshal(payload, &record); err != nil {
			t.Fatalf("decode sqlite payload %d: %v", count, err)
		}
		if record.ArchiveEventID != eventID || record.EventHash != eventHash {
			t.Fatalf("sqlite envelope mismatch at %d", count)
		}
		appendArchiveDigest(digest, eventID, eventHash)
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite segment: %v", err)
	}
	return count, hex.EncodeToString(digest.Sum(nil))
}

func restoreGzipJSONL(t *testing.T, path string, want int) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open gzip archive for restore: %v", err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("open gzip restore reader: %v", err)
	}
	defer reader.Close()
	result, err := usage.StreamImportPayload(reader, 256, func(events []usage.Event) error {
		return nil
	})
	if err != nil {
		t.Fatalf("restore gzip archive: %v", err)
	}
	if result.Total != want || result.Failed != 0 || result.Unsupported != 0 {
		t.Fatalf("gzip restore result = %#v, want total=%d", result, want)
	}
}

func restoreSQLiteSegment(t *testing.T, path string, want int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open sqlite segment for restore: %v", err)
	}
	defer db.Close()
	rows, err := db.Query(`select payload_json from archive_events order by event_id`)
	if err != nil {
		t.Fatalf("query sqlite restore rows: %v", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			t.Fatalf("scan sqlite restore row %d: %v", count, err)
		}
		result, err := usage.StreamImportPayload(bufio.NewReaderSize(bytesReader(payload), len(payload)), 1, func(events []usage.Event) error {
			return nil
		})
		if err != nil {
			t.Fatalf("restore sqlite row %d: %v", count, err)
		}
		if result.Total != 1 || result.Failed != 0 || result.Unsupported != 0 {
			t.Fatalf("sqlite restore row result = %#v", result)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate sqlite restore rows: %v", err)
	}
	if count != want {
		t.Fatalf("sqlite restore count=%d want=%d", count, want)
	}
}

func archiveBaselineEvent(index int) archiveBaselineRecord {
	timestampMS := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC).UnixMilli() + int64(index)*30_000
	latencyMS := int64(100 + index%900)
	ttftMS := int64(20 + index%180)
	failed := index%17 == 0
	failBody := ""
	failSummary := ""
	if failed {
		failSummary = "upstream request failed"
		failBody = fmt.Sprintf(`{"error":{"code":"rate_limit","request":%d,"message":"temporary upstream failure"}}`, index)
	}
	metadata := fmt.Sprintf(`{"request_id":"req-%08d","rate_limit":{"remaining":%d},"data_policy":{"zero_retention":false}}`, index, index%1000)
	return archiveBaselineRecord{
		ArchiveEventID: int64(index + 1),
		Event: usage.Event{
			RequestID:             fmt.Sprintf("req-%08d", index),
			EventHash:             fmt.Sprintf("usage-archive-baseline-%08d", index),
			TimestampMS:           timestampMS,
			Timestamp:             time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
			Provider:              []string{"codex", "claude", "gemini"}[index%3],
			ExecutorType:          []string{"CodexExecutor", "ClaudeExecutor", "GeminiExecutor"}[index%3],
			Model:                 fmt.Sprintf("model-%02d", index%12),
			RequestedModel:        fmt.Sprintf("alias-%02d", index%12),
			ResolvedModel:         fmt.Sprintf("billing-model-%02d", index%12),
			Endpoint:              "POST /v1/responses",
			Method:                "POST",
			Path:                  "/v1/responses",
			AuthType:              "oauth",
			AuthIndex:             fmt.Sprintf("auth-%03d", index%100),
			Source:                fmt.Sprintf("account-%03d", index%100),
			SourceHash:            fmt.Sprintf("source-hash-%03d", index%100),
			APIKeyHash:            fmt.Sprintf("api-key-hash-%03d", index%50),
			AccountSnapshot:       fmt.Sprintf("account-%03d@example.com", index%100),
			AuthLabelSnapshot:     fmt.Sprintf("Account %03d", index%100),
			AuthFileSnapshot:      fmt.Sprintf("account-%03d.json", index%100),
			AuthProviderSnapshot:  []string{"codex", "claude", "gemini"}[index%3],
			AuthProjectIDSnapshot: fmt.Sprintf("project-%03d", index%20),
			AuthSnapshotAtMS:      timestampMS,
			ReasoningEffort:       []string{"low", "medium", "high"}[index%3],
			ServiceTier:           []string{"default", "priority", ""}[index%3],
			RequestServiceTier:    []string{"default", "priority", ""}[index%3],
			InputTokens:           int64(100 + index%300),
			OutputTokens:          int64(50 + index%150),
			ReasoningTokens:       int64(index % 40),
			CachedTokens:          int64(index % 80),
			CacheReadTokens:       int64(index % 60),
			CacheCreationTokens:   int64(index % 20),
			TotalTokens:           int64(150 + index%500),
			LatencyMS:             &latencyMS,
			TTFTMS:                &ttftMS,
			Failed:                failed,
			FailStatusCode:        map[bool]int{true: 429}[failed],
			FailSummary:           failSummary,
			RawJSON:               fmt.Sprintf(`{"request":{"id":%d,"model":"model-%02d"},"response":{"status":%d}}`, index, index%12, map[bool]int{true: 429, false: 200}[failed]),
			CreatedAtMS:           timestampMS,
		},
		FailBody:             failBody,
		ResponseMetadataJSON: metadata,
	}
}

func appendArchiveDigest(digest hash.Hash, eventID int64, eventHash string) {
	_, _ = io.WriteString(digest, strconv.FormatInt(eventID, 10))
	_, _ = digest.Write([]byte{0})
	_, _ = io.WriteString(digest, eventHash)
	_, _ = digest.Write([]byte{'\n'})
}

type byteReader struct {
	data   []byte
	offset int
}

func bytesReader(data []byte) *byteReader {
	return &byteReader{data: data}
}

func (r *byteReader) Read(target []byte) (int, error) {
	if r.offset >= len(r.data) {
		return 0, io.EOF
	}
	written := copy(target, r.data[r.offset:])
	r.offset += written
	return written, nil
}
