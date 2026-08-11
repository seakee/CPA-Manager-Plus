package usageevent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestWriteCompatibleUsageMatchesBuildPayload(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)

	latency := int64(125)
	events := []usage.Event{
		streamTestEvent("old", 100, "GET /old", "old-model"),
		streamTestEvent("b-model", 200, "POST /v1/responses", "gpt-b"),
		streamTestEvent("a-success", 300, "GET /v1/models", "gpt-a"),
		streamTestEvent("a-failure", 400, "GET /v1/models", "gpt-a"),
	}
	events[1].ResolvedModel = "gpt-b-resolved"
	events[2].LatencyMS = &latency
	events[2].CachedTokens = 7
	events[2].CacheTokens = 7
	events[2].CacheReadTokens = 3
	events[2].CacheCreationTokens = 2
	events[3].Failed = true
	events[3].FailStatusCode = 429
	events[3].FailSummary = "rate limited"
	usage.AttachResponseHeaderMetadata(&events[3], &usage.ResponseHeaderMetadata{
		Trace: &usage.HeaderTraceMetadata{PrimaryTraceID: "trace-stream"},
	})
	if _, err := repo.InsertBatch(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	recent, err := repo.ListRecent(context.Background(), 3)
	if err != nil {
		t.Fatalf("list recent: %v", err)
	}
	expected := usage.BuildPayload(recent)
	var output bytes.Buffer
	if err := repo.WriteCompatibleUsage(context.Background(), &output, 3); err != nil {
		t.Fatalf("write compatible usage: %v", err)
	}
	if !json.Valid(output.Bytes()) {
		t.Fatalf("invalid JSON: %s", output.String())
	}
	var actual usage.Payload
	if err := json.Unmarshal(output.Bytes(), &actual); err != nil {
		t.Fatalf("decode compatible usage: %v", err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("streamed payload mismatch\nactual: %#v\nexpected: %#v", actual, expected)
	}
}

func TestWriteCompatibleUsageMatchesBuildPayloadAcrossBatchBoundary(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := &repository{db: db}

	total := usageExportBatchSize + 3
	events := make([]usage.Event, 0, total)
	for index := 1; index <= total; index++ {
		events = append(events, streamTestEvent(fmt.Sprintf("batch-%03d", index), int64(index), "POST /v1/responses", "gpt-test"))
	}
	if _, err := repo.InsertBatch(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	recent, err := repo.ListRecent(context.Background(), total)
	if err != nil {
		t.Fatalf("list recent: %v", err)
	}
	expected := usage.BuildPayload(recent)
	var output bytes.Buffer
	if err := repo.WriteCompatibleUsage(context.Background(), &output, total); err != nil {
		t.Fatalf("write compatible usage: %v", err)
	}
	if !json.Valid(output.Bytes()) {
		t.Fatalf("invalid JSON: %s", output.String())
	}
	var actual usage.Payload
	if err := json.Unmarshal(output.Bytes(), &actual); err != nil {
		t.Fatalf("decode compatible usage: %v", err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("streamed payload mismatch\nactual: %#v\nexpected: %#v", actual, expected)
	}
}

func TestCompatibleDetailBatchPreservesSnapshotIDOrderWithNonContiguousIDs(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := &repository{db: db}

	total := usageExportBatchSize + 3
	events := make([]usage.Event, 0, total)
	for index := 1; index <= total; index++ {
		events = append(events, streamTestEvent(fmt.Sprintf("noncontig-%03d", index), int64(index), "POST /v1/responses", "gpt-test"))
	}
	if _, err := repo.InsertBatch(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `delete from usage_events where id = 3`); err != nil {
		t.Fatalf("delete event: %v", err)
	}

	snapshot, err := repo.captureUsageSnapshot(context.Background(), total)
	if err != nil {
		t.Fatalf("capture snapshot: %v", err)
	}
	ids, err := repo.compatibleSnapshotIDs(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("snapshot ids: %v", err)
	}
	if len(ids) != total-1 {
		t.Fatalf("snapshot id count = %d, want %d", len(ids), total-1)
	}
	for _, id := range ids {
		if id == 3 {
			t.Fatalf("snapshot ids include deleted id 3: %v", ids)
		}
	}
	for index := 1; index < len(ids); index++ {
		if ids[index] >= ids[index-1] {
			t.Fatalf("snapshot ids not descending: %v", ids)
		}
	}

	rows, err := repo.compatibleDetailBatch(context.Background(), ids)
	if err != nil {
		t.Fatalf("detail batch: %v", err)
	}
	if len(rows) != len(ids) {
		t.Fatalf("detail row count = %d, want %d", len(rows), len(ids))
	}
	for index, row := range rows {
		if row.id != ids[index] {
			t.Fatalf("detail row %d id = %d, want %d", index, row.id, ids[index])
		}
	}
}

func TestCompatibleSnapshotIDsExcludeRowsInsertedAfterSnapshot(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := &repository{db: db}

	initial := []usage.Event{
		streamTestEvent("snapshot-1", 1, "POST /v1/responses", "gpt-test"),
		streamTestEvent("snapshot-2", 2, "POST /v1/responses", "gpt-test"),
		streamTestEvent("snapshot-3", 3, "POST /v1/responses", "gpt-test"),
		streamTestEvent("snapshot-4", 4, "POST /v1/responses", "gpt-test"),
		streamTestEvent("snapshot-5", 5, "POST /v1/responses", "gpt-test"),
	}
	if _, err := repo.InsertBatch(context.Background(), initial); err != nil {
		t.Fatalf("insert initial events: %v", err)
	}
	snapshot, err := repo.captureUsageSnapshot(context.Background(), len(initial))
	if err != nil {
		t.Fatalf("capture snapshot: %v", err)
	}

	extra := []usage.Event{
		streamTestEvent("snapshot-after-1", 6, "POST /v1/responses", "gpt-test"),
		streamTestEvent("snapshot-after-2", 7, "POST /v1/responses", "gpt-test"),
	}
	if _, err := repo.InsertBatch(context.Background(), extra); err != nil {
		t.Fatalf("insert extra events: %v", err)
	}

	ids, err := repo.compatibleSnapshotIDs(context.Background(), snapshot)
	if err != nil {
		t.Fatalf("snapshot ids: %v", err)
	}
	if len(ids) != len(initial) {
		t.Fatalf("snapshot id count = %d, want %d", len(ids), len(initial))
	}
	for _, id := range ids {
		if id > snapshot.maxID {
			t.Fatalf("snapshot ids include rows inserted after snapshot: %v", ids)
		}
	}
}

func TestInsertBatchSelectsServiceTierByProviderSemantics(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)

	codex := streamTestEvent("codex-tier", 100, "POST /v1/responses", "gpt-5.4")
	codex.ExecutorType = "codex"
	codex.RequestServiceTier = "priority"
	codex.ResponseServiceTier = "default"
	codex.ServiceTier = "priority"
	nonCodex := streamTestEvent("openai-tier", 200, "POST /v1/responses", "gpt-5.4")
	nonCodex.Provider = "openai-compatible"
	nonCodex.RequestServiceTier = "priority"
	nonCodex.ResponseServiceTier = "default"
	nonCodex.ServiceTier = "priority"

	if _, err := repo.InsertBatch(context.Background(), []usage.Event{codex, nonCodex}); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	recent, err := repo.ListRecent(context.Background(), 2)
	if err != nil {
		t.Fatalf("list recent: %v", err)
	}
	byHash := make(map[string]usage.Event, len(recent))
	for _, event := range recent {
		byHash[event.EventHash] = event
	}
	if event := byHash["codex-tier"]; event.ServiceTier != "priority" || event.RequestServiceTier != "priority" || event.ResponseServiceTier != "default" {
		t.Fatalf("codex tiers = %q/%q/%q", event.ServiceTier, event.RequestServiceTier, event.ResponseServiceTier)
	}
	if event := byHash["openai-tier"]; event.ServiceTier != "default" || event.RequestServiceTier != "priority" || event.ResponseServiceTier != "default" {
		t.Fatalf("non-Codex tiers = %q/%q/%q", event.ServiceTier, event.RequestServiceTier, event.ResponseServiceTier)
	}
}

func TestWriteExportJSONLUsesRecentLimitAndAscendingKeysetOrder(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)

	events := make([]usage.Event, 0, usageExportBatchSize+3)
	for index := 1; index <= usageExportBatchSize+3; index++ {
		event := streamTestEvent(fmt.Sprintf("event-%03d", index), int64(index), "POST /v1/responses", "gpt-test")
		event.RawJSON = `{"secret":"must-not-export"}`
		event.FailBody = "must-not-export"
		events = append(events, event)
	}
	usage.AttachResponseHeaderMetadata(&events[len(events)-1], &usage.ResponseHeaderMetadata{
		Trace: &usage.HeaderTraceMetadata{PrimaryTraceID: "trace-export"},
	})
	if _, err := repo.InsertBatch(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	var output bytes.Buffer
	if err := repo.WriteExportJSONL(context.Background(), &output, usageExportBatchSize+1); err != nil {
		t.Fatalf("write export: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != usageExportBatchSize+1 {
		t.Fatalf("line count = %d", len(lines))
	}
	for index, line := range lines {
		var event usage.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode line %d: %v", index, err)
		}
		wantTimestamp := int64(index + 3)
		if event.TimestampMS != wantTimestamp {
			t.Fatalf("line %d timestamp = %d, want %d", index, event.TimestampMS, wantTimestamp)
		}
		if event.RawJSON != "" || event.FailBody != "" {
			t.Fatalf("line %d exposes sensitive fields: %#v", index, event)
		}
	}
	var last usage.Event
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("decode last line: %v", err)
	}
	if last.ResponseMetadata == nil || last.ResponseMetadata.Trace == nil || last.ResponseMetadata.Trace.PrimaryTraceID != "trace-export" {
		t.Fatalf("last metadata = %#v", last.ResponseMetadata)
	}
}

func TestValidatedMetadataJSONRequiresJSONObject(t *testing.T) {
	for _, raw := range []string{"", "null", "[]", `{"trace":`, `"text"`} {
		if metadata := validatedMetadataJSON(raw); metadata != nil {
			t.Fatalf("metadata for %q = %s", raw, metadata)
		}
	}
	if metadata := validatedMetadataJSON(`{"trace":{"primary_trace_id":"trace"}}`); metadata == nil {
		t.Fatal("valid metadata was rejected")
	}
}

func streamTestEvent(hash string, timestampMS int64, endpoint, model string) usage.Event {
	return usage.Event{
		EventHash:    hash,
		TimestampMS:  timestampMS,
		Timestamp:    fmt.Sprintf("2026-01-01T00:00:%02dZ", timestampMS%60),
		Model:        model,
		Endpoint:     endpoint,
		Source:       "test-source",
		InputTokens:  1,
		OutputTokens: 2,
		TotalTokens:  3,
		CreatedAtMS:  timestampMS,
	}
}
