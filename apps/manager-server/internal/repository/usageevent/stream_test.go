package usageevent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestNormalizeCompatibleUsageStreamLimit(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	tests := []struct {
		name  string
		limit int
		want  int
	}{
		{name: "negative defaults", limit: -1, want: defaultUsageStreamLimit},
		{name: "zero defaults", limit: 0, want: defaultUsageStreamLimit},
		{name: "below maximum", limit: 1024, want: 1024},
		{name: "at maximum", limit: maxCompatibleUsageStreamLimit, want: maxCompatibleUsageStreamLimit},
		{name: "above maximum", limit: maxCompatibleUsageStreamLimit + 1, want: maxCompatibleUsageStreamLimit},
		{name: "maximum integer", limit: maxInt, want: maxCompatibleUsageStreamLimit},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := normalizeCompatibleUsageStreamLimit(test.limit); got != test.want {
				t.Fatalf("normalize compatible usage limit %d = %d, want %d", test.limit, got, test.want)
			}
		})
	}

	exportLimit := maxCompatibleUsageStreamLimit + 1
	if got := normalizeUsageStreamLimit(exportLimit); got != exportLimit {
		t.Fatalf("normalize JSONL export limit %d = %d, want unchanged", exportLimit, got)
	}
}

func TestWriteCompatibleUsageMatchesBuildPayloadAcrossDetailBatchSizes(t *testing.T) {
	_, repo := openCompatibleUsageStreamTestRepository(t)
	ctx := context.Background()

	endpoints := []string{"", "GET /v1/models", "POST /v1/chat/completions", "POST /v1/responses"}
	models := []string{"", "gpt-a", "gpt-b", "gpt-c", "gpt-d"}
	events := make([]usage.Event, 0, 2049)
	for index := 1; index <= 2049; index++ {
		event := streamTestEvent(
			fmt.Sprintf("compatible-size-%04d", index),
			int64(index/2),
			endpoints[index%len(endpoints)],
			models[(index/3)%len(models)],
		)
		event.Source = fmt.Sprintf("source-%04d", index)
		events = append(events, event)
	}
	if _, err := repo.InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	for _, limit := range []int{1, 1023, 1024, 1025, 2049} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			recent, err := repo.ListRecent(ctx, limit)
			if err != nil {
				t.Fatalf("list recent: %v", err)
			}
			expected := usage.BuildPayload(recent)
			actual := writeAndDecodeCompatibleUsage(t, ctx, repo, limit)
			if !reflect.DeepEqual(actual, expected) {
				t.Fatalf("payload mismatch for limit %d", limit)
			}
		})
	}
}

func TestWriteCompatibleUsagePreservesGroupingAcrossDetailBatchBoundaries(t *testing.T) {
	_, repo := openCompatibleUsageStreamTestRepository(t)
	ctx := context.Background()

	groups := []struct {
		endpoint string
		model    string
		count    int
	}{
		{endpoint: "endpoint-a", model: "model-a", count: 1023},
		{endpoint: "endpoint-a", model: "model-b", count: 1025},
		{endpoint: "endpoint-b", model: "model-a", count: 1},
	}
	events := make([]usage.Event, 0, 2049)
	sequence := 0
	for _, group := range groups {
		for offset := 0; offset < group.count; offset++ {
			sequence++
			event := streamTestEvent(
				fmt.Sprintf("compatible-group-%04d", sequence),
				int64(sequence),
				group.endpoint,
				group.model,
			)
			event.Source = fmt.Sprintf("group-source-%04d", sequence)
			events = append(events, event)
		}
	}
	if _, err := repo.InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	recent, err := repo.ListRecent(ctx, len(events))
	if err != nil {
		t.Fatalf("list recent: %v", err)
	}
	expected := usage.BuildPayload(recent)
	actual := writeAndDecodeCompatibleUsage(t, ctx, repo, len(events))
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("payload mismatch across detail batch grouping boundaries")
	}
	if got := len(actual.APIs["endpoint-a"].Models["model-a"].Details); got != 1023 {
		t.Fatalf("endpoint-a/model-a details = %d, want 1023", got)
	}
	if got := len(actual.APIs["endpoint-a"].Models["model-b"].Details); got != 1025 {
		t.Fatalf("endpoint-a/model-b details = %d, want 1025", got)
	}
	if got := len(actual.APIs["endpoint-b"].Models["model-a"].Details); got != 1 {
		t.Fatalf("endpoint-b/model-a details = %d, want 1", got)
	}
}

func TestWriteCompatibleUsagePreservesIDDescendingTieBreaker(t *testing.T) {
	_, repo := openCompatibleUsageStreamTestRepository(t)
	ctx := context.Background()

	events := make([]usage.Event, 0, 5)
	for index := 1; index <= 5; index++ {
		event := streamTestEvent(
			fmt.Sprintf("compatible-tie-%d", index),
			100,
			"POST /v1/responses",
			"gpt-tie",
		)
		event.Source = fmt.Sprintf("source-%d", index)
		events = append(events, event)
	}
	if _, err := repo.InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	actual := writeAndDecodeCompatibleUsage(t, ctx, repo, len(events))
	details := actual.APIs["POST /v1/responses"].Models["gpt-tie"].Details
	if len(details) != len(events) {
		t.Fatalf("details = %d, want %d", len(details), len(events))
	}
	for index, detail := range details {
		want := fmt.Sprintf("source-%d", len(events)-index)
		if detail.Source != want {
			t.Fatalf("detail %d source = %q, want %q", index, detail.Source, want)
		}
	}
}

func TestWriteCompatibleUsagePreservesEndpointAndModelJSONKeyOrder(t *testing.T) {
	_, repo := openCompatibleUsageStreamTestRepository(t)
	ctx := context.Background()
	events := []usage.Event{
		streamTestEvent("compatible-order-z-b", 100, "endpoint-z", "model-b"),
		streamTestEvent("compatible-order-a-c", 200, "endpoint-a", "model-c"),
		streamTestEvent("compatible-order-empty", 300, "", ""),
		streamTestEvent("compatible-order-z-a", 400, "endpoint-z", "model-a"),
		streamTestEvent("compatible-order-a-a", 500, "endpoint-a", "model-a"),
	}
	if _, err := repo.InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	var output bytes.Buffer
	if err := repo.WriteCompatibleUsage(ctx, &output, len(events)); err != nil {
		t.Fatalf("write compatible usage: %v", err)
	}
	endpointOrder, modelOrder := compatibleUsageJSONKeyOrder(t, output.Bytes())
	if want := []string{"-", "endpoint-a", "endpoint-z"}; !reflect.DeepEqual(endpointOrder, want) {
		t.Fatalf("endpoint JSON key order = %q, want %q", endpointOrder, want)
	}
	wantModels := map[string][]string{
		"-":          {"-"},
		"endpoint-a": {"model-a", "model-c"},
		"endpoint-z": {"model-a", "model-b"},
	}
	if !reflect.DeepEqual(modelOrder, wantModels) {
		t.Fatalf("model JSON key order = %#v, want %#v", modelOrder, wantModels)
	}
}

func TestCompatibleRowsByIDsFailsWhenSnapshotRowDisappears(t *testing.T) {
	db, repo := openCompatibleUsageStreamTestRepository(t)
	ctx := context.Background()
	events := []usage.Event{
		streamTestEvent("compatible-present", 100, "endpoint", "model"),
		streamTestEvent("compatible-disappears", 200, "endpoint", "model"),
	}
	if _, err := repo.InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	snapshot, err := repo.captureUsageSnapshot(ctx, len(events))
	if err != nil {
		t.Fatalf("capture snapshot: %v", err)
	}
	orderedIDs, err := repo.compatibleOrderedIDs(ctx, snapshot, len(events))
	if err != nil {
		t.Fatalf("ordered ids: %v", err)
	}
	missingID := orderedIDs[0]
	if _, err := db.ExecContext(ctx, "delete from usage_events where id = ?", missingID); err != nil {
		t.Fatalf("delete snapshot row: %v", err)
	}

	_, err = repo.compatibleRowsByIDs(ctx, orderedIDs)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("snapshot row disappeared: id=%d", missingID)) {
		t.Fatalf("missing row error = %v", err)
	}
}

func TestWriteCompatibleUsageSnapshotExcludesConcurrentInsert(t *testing.T) {
	_, repo := openCompatibleUsageStreamTestRepository(t)
	ctx := context.Background()

	events := make([]usage.Event, 0, 1500)
	for index := 1; index <= cap(events); index++ {
		events = append(events, streamTestEvent(
			fmt.Sprintf("compatible-snapshot-%04d", index),
			int64(index),
			"POST /v1/responses",
			"gpt-snapshot",
		))
	}
	if _, err := repo.InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	writer, errCh := startBlockedCompatibleUsageExport(t, ctx, repo, len(events))
	waitForCompatibleUsageWriterBlock(t, writer)
	concurrent := streamTestEvent(
		"compatible-concurrent-insert",
		int64(len(events)+1),
		"CONCURRENT /new",
		"gpt-concurrent",
	)
	if _, err := repo.InsertBatch(ctx, []usage.Event{concurrent}); err != nil {
		t.Fatalf("insert concurrent event: %v", err)
	}
	if err := finishBlockedCompatibleUsageExport(t, writer, errCh); err != nil {
		t.Fatalf("write compatible usage: %v", err)
	}

	actual := decodeCompatibleUsageBytes(t, writer.Bytes())
	if actual.TotalRequests != int64(len(events)) {
		t.Fatalf("total requests = %d, want %d", actual.TotalRequests, len(events))
	}
	if _, ok := actual.APIs[concurrent.Endpoint]; ok {
		t.Fatalf("concurrent endpoint %q entered the captured response", concurrent.Endpoint)
	}
}

func TestWriteCompatibleUsageReleasesReaderBeforeSlowWriter(t *testing.T) {
	db, repo := openCompatibleUsageStreamTestRepository(t)
	ctx := context.Background()

	events := make([]usage.Event, 0, 1500)
	for index := 1; index <= cap(events); index++ {
		events = append(events, streamTestEvent(
			fmt.Sprintf("compatible-wal-%04d", index),
			int64(index),
			"POST /v1/responses",
			"gpt-wal",
		))
	}
	if _, err := repo.InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	busy, _, _ := runCompatibleUsageCheckpoint(t, db, "truncate")
	if busy != 0 {
		t.Fatalf("initial truncate checkpoint busy = %d", busy)
	}

	writer, errCh := startBlockedCompatibleUsageExport(t, ctx, repo, len(events))
	waitForCompatibleUsageWriterBlock(t, writer)
	inUse := db.Stats().InUse
	t.Logf("database connections in use while HTTP writer is blocked: %d", inUse)
	if inUse != 0 {
		t.Fatalf("database connections in use while HTTP writer is blocked = %d, want 0", inUse)
	}

	appended := make([]usage.Event, 0, 32)
	for index := 1; index <= cap(appended); index++ {
		appended = append(appended, streamTestEvent(
			fmt.Sprintf("compatible-wal-appended-%02d", index),
			int64(len(events)+index),
			"POST /v1/responses",
			"gpt-wal-appended",
		))
	}
	if _, err := repo.InsertBatch(ctx, appended); err != nil {
		t.Fatalf("append WAL events: %v", err)
	}
	busy, logFrames, checkpointedFrames := runCompatibleUsageCheckpoint(t, db, "passive")
	t.Logf(
		"passive checkpoint while HTTP writer is blocked: busy=%d log_frames=%d checkpointed_frames=%d",
		busy,
		logFrames,
		checkpointedFrames,
	)
	if busy != 0 {
		t.Fatalf("passive checkpoint busy = %d", busy)
	}
	if logFrames <= 0 {
		t.Fatalf("passive checkpoint log frames = %d, want positive", logFrames)
	}
	if checkpointedFrames != logFrames {
		t.Fatalf(
			"passive checkpoint frames = %d/%d, want checkpoint to catch up while writer is blocked",
			checkpointedFrames,
			logFrames,
		)
	}

	if err := finishBlockedCompatibleUsageExport(t, writer, errCh); err != nil {
		t.Fatalf("write compatible usage: %v", err)
	}
	if !json.Valid(writer.Bytes()) {
		t.Fatal("compatible usage output is not valid JSON")
	}
}

func TestWriteCompatibleUsageStopsAfterContextCancellation(t *testing.T) {
	_, repo := openCompatibleUsageStreamTestRepository(t)
	events := make([]usage.Event, 0, compatibleUsageDetailBatchSize+100)
	for index := 1; index <= cap(events); index++ {
		events = append(events, streamTestEvent(
			fmt.Sprintf("compatible-cancel-%04d", index),
			int64(index),
			"POST /v1/responses",
			"gpt-cancel",
		))
	}
	if _, err := repo.InsertBatch(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	writer := &cancelCompatibleUsageWriter{cancel: cancel}
	err := repo.WriteCompatibleUsage(ctx, writer, len(events))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("write error = %v, want context canceled", err)
	}
}

func TestCompatibleUsageQueryPlans(t *testing.T) {
	db, repo := openCompatibleUsageStreamTestRepository(t)
	ctx := context.Background()
	events := []usage.Event{
		streamTestEvent("compatible-plan-a", 100, "endpoint-a", "model-a"),
		streamTestEvent("compatible-plan-b", 200, "endpoint-a", "model-b"),
		streamTestEvent("compatible-plan-c", 300, "endpoint-b", "model-a"),
	}
	if _, err := repo.InsertBatch(ctx, events); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	snapshot, err := repo.captureUsageSnapshot(ctx, len(events))
	if err != nil {
		t.Fatalf("capture snapshot: %v", err)
	}
	orderedIDs, err := repo.compatibleOrderedIDs(ctx, snapshot, len(events))
	if err != nil {
		t.Fatalf("ordered ids: %v", err)
	}

	orderedPlan := explainCompatibleUsageQueryPlan(
		t,
		db,
		compatibleUsageOrderedIDsQuery,
		snapshot.maxID,
		snapshot.cutoffTimestampMS,
		snapshot.cutoffTimestampMS,
		snapshot.cutoffID,
	)
	detailArgs := make([]any, len(orderedIDs))
	for index, id := range orderedIDs {
		detailArgs[index] = id
	}
	detailPlan := explainCompatibleUsageQueryPlan(
		t,
		db,
		compatibleUsageDetailQuery(len(orderedIDs)),
		detailArgs...,
	)
	t.Logf("ordered ID query plan:\n%s", strings.Join(orderedPlan, "\n"))
	t.Logf("detail batch query plan:\n%s", strings.Join(detailPlan, "\n"))

	detailPlanText := strings.Join(detailPlan, "\n")
	if !strings.Contains(detailPlanText, "USING INTEGER PRIMARY KEY") {
		t.Fatalf("detail query does not use INTEGER PRIMARY KEY:\n%s", detailPlanText)
	}
	if strings.Contains(detailPlanText, "USE TEMP B-TREE") {
		t.Fatalf("detail query performs a temporary sort:\n%s", detailPlanText)
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

func openCompatibleUsageStreamTestRepository(t *testing.T) (*sql.DB, *repository) {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db, &repository{db: db}
}

func writeAndDecodeCompatibleUsage(t *testing.T, ctx context.Context, repo *repository, limit int) usage.Payload {
	t.Helper()
	var output bytes.Buffer
	if err := repo.WriteCompatibleUsage(ctx, &output, limit); err != nil {
		t.Fatalf("write compatible usage: %v", err)
	}
	return decodeCompatibleUsageBytes(t, output.Bytes())
}

func decodeCompatibleUsageBytes(t *testing.T, output []byte) usage.Payload {
	t.Helper()
	if !json.Valid(output) {
		t.Fatalf("invalid compatible usage JSON: %s", output)
	}
	var payload usage.Payload
	if err := json.Unmarshal(output, &payload); err != nil {
		t.Fatalf("decode compatible usage: %v", err)
	}
	return payload
}

func compatibleUsageJSONKeyOrder(t *testing.T, output []byte) ([]string, map[string][]string) {
	t.Helper()
	if !json.Valid(output) {
		t.Fatalf("invalid compatible usage JSON: %s", output)
	}
	var root struct {
		APIs json.RawMessage `json:"apis"`
	}
	if err := json.Unmarshal(output, &root); err != nil {
		t.Fatalf("decode compatible usage root: %v", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(root.APIs))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		t.Fatalf("decode APIs object start: token=%v err=%v", token, err)
	}
	endpointOrder := make([]string, 0)
	modelOrder := make(map[string][]string)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			t.Fatalf("decode endpoint key: %v", err)
		}
		endpoint, ok := token.(string)
		if !ok {
			t.Fatalf("endpoint key token = %T, want string", token)
		}
		var endpointPayload struct {
			Models json.RawMessage `json:"models"`
		}
		if err := decoder.Decode(&endpointPayload); err != nil {
			t.Fatalf("decode endpoint %q payload: %v", endpoint, err)
		}
		endpointOrder = append(endpointOrder, endpoint)
		modelOrder[endpoint] = jsonObjectKeyOrder(t, endpointPayload.Models)
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		t.Fatalf("decode APIs object end: token=%v err=%v", token, err)
	}
	return endpointOrder, modelOrder
}

func jsonObjectKeyOrder(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		t.Fatalf("decode JSON object start: token=%v err=%v", token, err)
	}
	keys := make([]string, 0)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			t.Fatalf("decode JSON object key: %v", err)
		}
		key, ok := token.(string)
		if !ok {
			t.Fatalf("JSON object key token = %T, want string", token)
		}
		keys = append(keys, key)
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("decode JSON object value for %q: %v", key, err)
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		t.Fatalf("decode JSON object end: token=%v err=%v", token, err)
	}
	return keys
}

type blockingCompatibleUsageWriter struct {
	mu          sync.Mutex
	buffer      bytes.Buffer
	blocked     chan struct{}
	release     chan struct{}
	blockOnce   sync.Once
	releaseOnce sync.Once
}

func newBlockingCompatibleUsageWriter() *blockingCompatibleUsageWriter {
	return &blockingCompatibleUsageWriter{
		blocked: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (w *blockingCompatibleUsageWriter) Write(p []byte) (int, error) {
	w.blockOnce.Do(func() {
		close(w.blocked)
		<-w.release
	})
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(p)
}

func (w *blockingCompatibleUsageWriter) Unblock() {
	w.releaseOnce.Do(func() {
		close(w.release)
	})
}

func (w *blockingCompatibleUsageWriter) Bytes() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.buffer.Bytes()...)
}

func startBlockedCompatibleUsageExport(t *testing.T, ctx context.Context, repo *repository, limit int) (*blockingCompatibleUsageWriter, <-chan error) {
	t.Helper()
	writer := newBlockingCompatibleUsageWriter()
	errCh := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		errCh <- repo.WriteCompatibleUsage(ctx, writer, limit)
	}()
	t.Cleanup(func() {
		writer.Unblock()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Errorf("compatible usage export did not finish during cleanup")
		}
	})
	return writer, errCh
}

func waitForCompatibleUsageWriterBlock(t *testing.T, writer *blockingCompatibleUsageWriter) {
	t.Helper()
	select {
	case <-writer.blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("compatible usage writer did not block")
	}
}

func finishBlockedCompatibleUsageExport(t *testing.T, writer *blockingCompatibleUsageWriter, errCh <-chan error) error {
	t.Helper()
	writer.Unblock()
	select {
	case err := <-errCh:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("compatible usage export did not finish")
		return nil
	}
}

type cancelCompatibleUsageWriter struct {
	cancel context.CancelFunc
	once   sync.Once
}

func (w *cancelCompatibleUsageWriter) Write(p []byte) (int, error) {
	w.once.Do(w.cancel)
	return len(p), nil
}

func runCompatibleUsageCheckpoint(t *testing.T, db *sql.DB, mode string) (busy, logFrames, checkpointedFrames int64) {
	t.Helper()
	var query string
	switch mode {
	case "passive":
		query = "pragma wal_checkpoint(passive)"
	case "truncate":
		query = "pragma wal_checkpoint(truncate)"
	default:
		t.Fatalf("unsupported checkpoint mode %q", mode)
	}
	if err := db.QueryRow(query).Scan(&busy, &logFrames, &checkpointedFrames); err != nil {
		t.Fatalf("run %s checkpoint: %v", mode, err)
	}
	return busy, logFrames, checkpointedFrames
}

func explainCompatibleUsageQueryPlan(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query("explain query plan "+query, args...)
	if err != nil {
		t.Fatalf("explain query plan: %v", err)
	}
	defer rows.Close()

	plan := make([]string, 0)
	for rows.Next() {
		var id int
		var parent int
		var unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		plan = append(plan, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read query plan: %v", err)
	}
	return plan
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
