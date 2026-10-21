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
	events[2].ClientIP = "192.0.2.10"
	events[2].XForwardedFor = "203.0.113.5"
	events[2].UserAgent = "test-client/1.0"
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
	for _, key := range []string{"client_ip", "x_forwarded_for", "user_agent"} {
		if strings.Contains(output.String(), key) {
			t.Fatalf("compatible usage leaked %s: %s", key, output.String())
		}
	}
	var actual usage.Payload
	if err := json.Unmarshal(output.Bytes(), &actual); err != nil {
		t.Fatalf("decode compatible usage: %v", err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("streamed payload mismatch\nactual: %#v\nexpected: %#v", actual, expected)
	}
}

func TestWriteCompatibleUsagePreservesExplicitRequestedModel(t *testing.T) {
	_, repo := openCompatibleUsageStreamTestRepository(t)
	ctx := context.Background()
	event := streamTestEvent("explicit-requested-model", 100, "POST /v1/responses", "stored-display-model")
	event.RequestedModel = "deepseek-v4-flash(max)"
	event.ResolvedModel = "deepseek-v4-flash"
	if _, err := repo.InsertBatch(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	payload := writeAndDecodeCompatibleUsage(t, ctx, repo, 1)
	model := payload.APIs[event.Endpoint].Models["deepseek-v4-flash"]
	if model == nil || len(model.Details) != 1 {
		t.Fatalf("canonical model aggregate = %#v", payload.APIs[event.Endpoint].Models)
	}
	detail := model.Details[0]
	if detail.RequestedModel != event.RequestedModel {
		t.Fatalf("requested model = %q, want %q", detail.RequestedModel, event.RequestedModel)
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
	if event := byHash[codex.EventHash]; event.ServiceTier != "priority" || event.RequestServiceTier != "priority" || event.ResponseServiceTier != "default" {
		t.Fatalf("codex tiers = %q/%q/%q", event.ServiceTier, event.RequestServiceTier, event.ResponseServiceTier)
	}
	if event := byHash[nonCodex.EventHash]; event.ServiceTier != "default" || event.RequestServiceTier != "priority" || event.ResponseServiceTier != "default" {
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
		event.ClientIP = "192.0.2.10"
		event.XForwardedFor = "203.0.113.5"
		event.UserAgent = "test-client/1.0"
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
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			t.Fatalf("decode raw line %d: %v", index, err)
		}
		for _, key := range []string{"client_ip", "x_forwarded_for", "user_agent"} {
			if _, ok := raw[key]; ok {
				t.Fatalf("line %d exposes request metadata %s: %s", index, key, line)
			}
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

func TestWriteFullExportJSONLIgnoresQueryLimitAndUsesSnapshotBoundary(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	initial := make([]usage.Event, 0, 7)
	for index, timestamp := range []int64{30, 10, 20, 10, 40, 50, 60} {
		initial = append(initial, streamTestEvent(fmt.Sprintf("full-export-%d", index), timestamp, "POST /v1/responses", "gpt-test"))
	}
	if _, err := repo.InsertBatch(context.Background(), initial); err != nil {
		t.Fatalf("insert initial events: %v", err)
	}

	inserted := false
	var lateHash string
	writer := exportInsertWriter{onFirstWrite: func() {
		if inserted {
			return
		}
		inserted = true
		newEvent := streamTestEvent("full-export-after-snapshot", 5, "POST /v1/responses", "gpt-test")
		lateHash = newEvent.EventHash
		if _, err := repo.InsertBatch(context.Background(), []usage.Event{newEvent}); err != nil {
			t.Fatalf("insert concurrent event: %v", err)
		}
	}}
	if err := repo.WriteFullExportJSONL(context.Background(), &writer); err != nil {
		t.Fatalf("write full export: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(writer.String()), "\n")
	if len(lines) != len(initial) {
		t.Fatalf("full export line count = %d, want %d", len(lines), len(initial))
	}
	previousTimestamp := int64(-1)
	for index, line := range lines {
		var event usage.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode line %d: %v", index, err)
		}
		if event.TimestampMS < previousTimestamp {
			t.Fatalf("line %d timestamp %d is not ordered after %d", index, event.TimestampMS, previousTimestamp)
		}
		previousTimestamp = event.TimestampMS
		if event.EventHash == lateHash {
			t.Fatal("export included event inserted after snapshot boundary")
		}
	}
}

func TestWriteFullExportJSONLRetainsRowsDeletedAfterSnapshot(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	events := make([]usage.Event, 0, usageExportBatchSize+32)
	for index := 0; index < cap(events); index++ {
		events = append(events, streamTestEvent(
			fmt.Sprintf("full-export-delete-%04d", index),
			int64(index+1),
			"POST /v1/responses",
			"gpt-test",
		))
	}
	if _, err := repo.InsertBatch(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}

	deleted := false
	writer := exportInsertWriter{onFirstWrite: func() {
		if deleted {
			return
		}
		deleted = true
		if _, err := db.Exec(`delete from usage_events where event_hash = ?`, events[0].EventHash); err != nil {
			t.Fatalf("delete concurrent event: %v", err)
		}
	}}
	if err := repo.WriteFullExportJSONL(context.Background(), &writer); err != nil {
		t.Fatalf("write full export: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(writer.String()), "\n")
	if len(lines) != len(events) {
		t.Fatalf("full export line count = %d, want %d", len(lines), len(events))
	}
	seen := make(map[string]bool, len(lines))
	for index, line := range lines {
		var event usage.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode line %d: %v", index, err)
		}
		seen[event.EventHash] = true
	}
	if !seen[events[0].EventHash] {
		t.Fatalf("snapshot row deleted during export was missing")
	}
}

func TestWriteFullExportJSONLStopsWhenContextIsCancelledDuringStreaming(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)
	events := make([]usage.Event, 0, 4)
	for index := 0; index < 4; index++ {
		events = append(events, streamTestEvent(fmt.Sprintf("full-export-cancel-%d", index), int64(index+1), "POST /v1/responses", "gpt-test"))
	}
	if _, err := repo.InsertBatch(context.Background(), events); err != nil {
		t.Fatalf("insert events: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	writer := &cancelExportWriter{cancel: cancel}
	err = repo.WriteFullExportJSONL(ctx, writer)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("full export error = %v, want context canceled", err)
	}
}

func TestWriteFullExportJSONLEmptyDatabaseReturnsEmptyJSONL(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var output bytes.Buffer
	if err := New(db).WriteFullExportJSONL(context.Background(), &output); err != nil {
		t.Fatalf("empty full export: %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("empty full export bytes = %d, want zero", output.Len())
	}
}

type exportInsertWriter struct {
	bytes.Buffer
	onFirstWrite func()
}

type cancelExportWriter struct {
	cancel context.CancelFunc
}

func (w *cancelExportWriter) Write(p []byte) (int, error) {
	w.cancel()
	return len(p), nil
}

func (w *exportInsertWriter) Write(p []byte) (int, error) {
	if w.onFirstWrite != nil {
		callback := w.onFirstWrite
		w.onFirstWrite = nil
		callback()
	}
	return w.Buffer.Write(p)
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
		EventHash:    canonicalTestHash(hash),
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

func TestWriteExportJSONLAndCompatibleUsagePreserveRequestMetadata(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)

	genFalse := false
	streamFalse := false
	event := streamTestEvent("stream-meta-test", 100, "POST /v1/chat/completions", "gpt-4o")
	event.ResponseModel = "gpt-4o-mini"
	event.SessionID = "sess-export-1"
	event.ParentSessionID = "parent-export-1"
	event.AccessTokenSHA256 = "sha256-export-hash"
	event.Generate = &genFalse
	event.Stream = &streamFalse
	event.CacheInputMode = usage.CacheInputModeIncluded
	event.InputTokens = 100
	event.OutputTokens = 2
	event.CachedTokens = 80
	event.CacheReadTokens = 80
	event.TotalTokens = 102
	event.NormalizedUncachedInputTokens = 20
	event.NormalizedTotalInputTokens = 100
	event.NormalizedCacheReadTokens = 80

	if _, err := repo.InsertBatch(context.Background(), []usage.Event{event}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	// 1. Verify JSONL export stream through real JSONL import parsing
	var jsonlBuf bytes.Buffer
	if err := repo.WriteExportJSONL(context.Background(), &jsonlBuf, 10); err != nil {
		t.Fatalf("write export JSONL: %v", err)
	}
	var exportedRecord map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(jsonlBuf.Bytes()), &exportedRecord); err != nil {
		t.Fatalf("decode exported JSONL record: %v", err)
	}
	if _, exists := exportedRecord["cache_input_mode"]; exists {
		t.Fatal("derived cache mode must not be exported as explicit cache_input_mode")
	}
	if got := exportedRecord["derived_cache_input_mode"]; got != usage.CacheInputModeIncluded {
		t.Fatalf("derived_cache_input_mode = %v, want %q", got, usage.CacheInputModeIncluded)
	}

	importResult, err := usage.ParseImportPayload(jsonlBuf.Bytes())
	if err != nil {
		t.Fatalf("parse imported JSONL: %v", err)
	}
	if importResult.Format != usage.ImportFormatJSONL || len(importResult.Events) != 1 {
		t.Fatalf("unexpected import result: format=%q, count=%d", importResult.Format, len(importResult.Events))
	}
	exported := importResult.Events[0]
	if exported.ResponseModel != event.ResponseModel {
		t.Fatalf("ResponseModel mismatch: got %q, want %q", exported.ResponseModel, event.ResponseModel)
	}
	if exported.SessionID != event.SessionID {
		t.Fatalf("SessionID mismatch: got %q, want %q", exported.SessionID, event.SessionID)
	}
	if exported.ParentSessionID != event.ParentSessionID {
		t.Fatalf("ParentSessionID mismatch: got %q, want %q", exported.ParentSessionID, event.ParentSessionID)
	}
	if exported.AccessTokenSHA256 != event.AccessTokenSHA256 {
		t.Fatalf("AccessTokenSHA256 mismatch: got %q, want %q", exported.AccessTokenSHA256, event.AccessTokenSHA256)
	}
	if exported.Generate == nil {
		t.Fatal("Generate is nil; false presence must not be dropped by omitempty or parser")
	}
	if *exported.Generate != false {
		t.Fatalf("Generate = %v, want false", *exported.Generate)
	}
	if exported.Stream == nil {
		t.Fatal("Stream is nil; false presence must not be dropped by omitempty or parser")
	}
	if *exported.Stream != false {
		t.Fatalf("Stream = %v, want false", *exported.Stream)
	}
	if exported.CacheInputMode != usage.CacheInputModeIncluded {
		t.Fatalf("CacheInputMode = %q, want %q", exported.CacheInputMode, usage.CacheInputModeIncluded)
	}
	if hints := usage.RawCacheAccountingHintsFromJSON(exported.RawJSON); hints.ExplicitMode != "" {
		t.Fatalf("derived cache mode was promoted to raw explicit provenance: %q", hints.ExplicitMode)
	}

	// 2. Verify compatible usage stream
	var compBuf bytes.Buffer
	if err := repo.WriteCompatibleUsage(context.Background(), &compBuf, 10); err != nil {
		t.Fatalf("write compatible usage: %v", err)
	}
	var compPayload usage.Payload
	if err := json.Unmarshal(compBuf.Bytes(), &compPayload); err != nil {
		t.Fatalf("unmarshal compatible payload: %v", err)
	}
	details := compPayload.APIs[event.Endpoint].Models[event.Model].Details
	if len(details) != 1 {
		t.Fatalf("compatible details count = %d", len(details))
	}
	compDetail := details[0]
	if compDetail.ResponseModel != event.ResponseModel ||
		compDetail.SessionID != event.SessionID ||
		compDetail.ParentSessionID != event.ParentSessionID ||
		compDetail.AccessTokenSHA256 != event.AccessTokenSHA256 ||
		compDetail.Generate == nil || *compDetail.Generate != false ||
		compDetail.Stream == nil || *compDetail.Stream != false {
		t.Fatalf("compatible detail metadata mismatch: %+v", compDetail)
	}
}

func TestListRecentUsesCanonicalCacheAccountingEvidence(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)

	raw := `{
		"timestamp":"2026-10-12T00:00:00Z",
		"provider":"workbuddy",
		"executor_type":"executorAdapter",
		"model":"custom-model",
		"endpoint":"POST /v1/chat/completions",
		"tokens":{"input_tokens":100,"output_tokens":10,"cached_tokens":80,"cache_read_tokens":80,"cache_creation_tokens":0,"total_tokens":110},
		"accounting_version":2,
		"token_breakdown":{
			"schema_version":2,
			"quality":"complete",
			"total_tokens":110,
			"input":{"total_tokens":100,"uncached_tokens":20,"cache_read_tokens":80,"cache_write_tokens":0},
			"output":{"total_tokens":10,"non_reasoning_tokens":10,"reasoning_tokens":0},
			"unclassified_tokens":0
		}
	}`
	event, err := usage.NormalizeRaw([]byte(raw))
	if err != nil {
		t.Fatalf("normalize canonical event: %v", err)
	}
	if _, err := repo.InsertBatch(context.Background(), []usage.Event{event}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	recent, err := repo.ListRecent(context.Background(), 1)
	if err != nil {
		t.Fatalf("list recent: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("recent events = %d, want 1", len(recent))
	}
	got := recent[0]
	if got.CacheInputMode != usage.CacheInputModeIncluded ||
		got.NormalizedUncachedInputTokens != 20 ||
		got.NormalizedTotalInputTokens != 100 {
		t.Fatalf("recent accounting = mode:%q uncached:%d total:%d",
			got.CacheInputMode,
			got.NormalizedUncachedInputTokens,
			got.NormalizedTotalInputTokens,
		)
	}
}

func TestWriteExportJSONLPreservesExplicitCacheModeProvenance(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := New(db)

	event := streamTestEvent("stream-explicit-cache-mode", 100, "POST /v1/chat/completions", "opaque-model")
	event.CacheInputMode = usage.CacheInputModeIncluded
	event.InputTokens = 100
	event.OutputTokens = 10
	event.CachedTokens = 80
	event.CacheReadTokens = 80
	event.TotalTokens = 110
	event.RawJSON = `{"cache_input_mode":"included_in_input","total_tokens":110}`
	if _, err := repo.InsertBatch(context.Background(), []usage.Event{event}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	var buf bytes.Buffer
	if err := repo.WriteExportJSONL(context.Background(), &buf, 10); err != nil {
		t.Fatalf("write export JSONL: %v", err)
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("decode exported record: %v", err)
	}
	if got := record["cache_input_mode"]; got != usage.CacheInputModeIncluded {
		t.Fatalf("cache_input_mode = %v, want %q", got, usage.CacheInputModeIncluded)
	}
	if _, exists := record["derived_cache_input_mode"]; exists {
		t.Fatal("explicit cache mode must not be exported as derived_cache_input_mode")
	}

	result, err := usage.ParseImportPayload(buf.Bytes())
	if err != nil {
		t.Fatalf("parse imported JSONL: %v", err)
	}
	if len(result.Events) != 1 {
		t.Fatalf("imported events = %d, want 1", len(result.Events))
	}
	hints := usage.RawCacheAccountingHintsFromJSON(result.Events[0].RawJSON)
	if hints.ExplicitMode != usage.CacheInputModeIncluded {
		t.Fatalf("explicit cache mode provenance = %q, want %q", hints.ExplicitMode, usage.CacheInputModeIncluded)
	}
}



func TestDerivedCacheModeJSONLRoundTripPersistsAsOneAccountingResult(t *testing.T) {
	ctx := context.Background()
	sourceDB, err := sqliterepo.Open(filepath.Join(t.TempDir(), "source.sqlite"))
	if err != nil {
		t.Fatalf("open source database: %v", err)
	}
	t.Cleanup(func() { _ = sourceDB.Close() })
	sourceRepo := New(sourceDB)

	raw := `{
		"timestamp":"2026-10-12T00:00:00Z",
		"provider":"workbuddy",
		"executor_type":"executorAdapter",
		"model":"Deepseek-V4.1-Flash",
		"endpoint":"POST /v1/chat/completions",
		"tokens":{"input_tokens":233705,"output_tokens":1148,"reasoning_tokens":458,"cached_tokens":232576,"cache_read_tokens":232576,"cache_creation_tokens":0,"total_tokens":234853},
		"accounting_version":2,
		"token_breakdown":{
			"schema_version":2,
			"quality":"complete",
			"total_tokens":234853,
			"input":{"total_tokens":233705,"uncached_tokens":1129,"cache_read_tokens":232576,"cache_write_tokens":0},
			"output":{"total_tokens":1148,"non_reasoning_tokens":690,"reasoning_tokens":458},
			"unclassified_tokens":0
		}
	}`
	event, err := usage.NormalizeRaw([]byte(raw))
	if err != nil {
		t.Fatalf("normalize #941 fixture: %v", err)
	}
	if _, err := sourceRepo.InsertBatch(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert source event: %v", err)
	}

	var firstExport bytes.Buffer
	if err := sourceRepo.WriteExportJSONL(ctx, &firstExport, 10); err != nil {
		t.Fatalf("write first JSONL export: %v", err)
	}
	var firstRecord map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(firstExport.Bytes()), &firstRecord); err != nil {
		t.Fatalf("decode first JSONL export: %v", err)
	}
	if _, exists := firstRecord["cache_input_mode"]; exists {
		t.Fatal("derived cache mode must not be promoted to explicit provenance")
	}
	if got := firstRecord["derived_cache_input_mode"]; got != usage.CacheInputModeIncluded {
		t.Fatalf("derived_cache_input_mode = %v, want %q", got, usage.CacheInputModeIncluded)
	}

	parsed, err := usage.ParseImportPayload(firstExport.Bytes())
	if err != nil {
		t.Fatalf("parse first JSONL export: %v", err)
	}
	if len(parsed.Events) != 1 {
		t.Fatalf("parsed events = %d, want 1", len(parsed.Events))
	}
	if hints := usage.RawCacheAccountingHintsFromJSON(parsed.Events[0].RawJSON); hints.ExplicitMode != "" {
		t.Fatalf("derived mode escalated into raw explicit provenance: %q", hints.ExplicitMode)
	}

	restoredDB, err := sqliterepo.Open(filepath.Join(t.TempDir(), "restored.sqlite"))
	if err != nil {
		t.Fatalf("open restored database: %v", err)
	}
	t.Cleanup(func() { _ = restoredDB.Close() })
	restoredRepo := New(restoredDB)
	if _, err := restoredRepo.InsertBatch(ctx, parsed.Events); err != nil {
		t.Fatalf("insert restored event: %v", err)
	}

	recent, err := restoredRepo.ListRecent(ctx, 1)
	if err != nil {
		t.Fatalf("list restored event: %v", err)
	}
	if len(recent) != 1 {
		t.Fatalf("restored events = %d, want 1", len(recent))
	}
	got := recent[0]
	if got.CacheInputMode != usage.CacheInputModeIncluded ||
		got.NormalizedUncachedInputTokens != 1129 ||
		got.NormalizedTotalInputTokens != 233705 ||
		got.NormalizedCacheReadTokens != 232576 ||
		got.NormalizedCacheCreationTokens != 0 {
		t.Fatalf("restored accounting = mode:%q uncached:%d total:%d read:%d creation:%d",
			got.CacheInputMode,
			got.NormalizedUncachedInputTokens,
			got.NormalizedTotalInputTokens,
			got.NormalizedCacheReadTokens,
			got.NormalizedCacheCreationTokens,
		)
	}

	var compatible bytes.Buffer
	if err := restoredRepo.WriteCompatibleUsage(ctx, &compatible, 10); err != nil {
		t.Fatalf("write compatible usage: %v", err)
	}
	var payload usage.Payload
	if err := json.Unmarshal(compatible.Bytes(), &payload); err != nil {
		t.Fatalf("decode compatible usage: %v", err)
	}
	api := payload.APIs[event.Endpoint]
	if api == nil {
		t.Fatalf("compatible endpoint %q missing", event.Endpoint)
	}
	model := api.Models[event.AnalyticsModel]
	if model == nil || len(model.Details) != 1 {
		t.Fatalf("compatible model details missing for %q", event.AnalyticsModel)
	}
	if model.Details[0].CacheInputMode != usage.CacheInputModeIncluded {
		t.Fatalf("compatible cache_input_mode = %q, want %q", model.Details[0].CacheInputMode, usage.CacheInputModeIncluded)
	}

	var secondExport bytes.Buffer
	if err := restoredRepo.WriteExportJSONL(ctx, &secondExport, 10); err != nil {
		t.Fatalf("write second JSONL export: %v", err)
	}
	var secondRecord map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(secondExport.Bytes()), &secondRecord); err != nil {
		t.Fatalf("decode second JSONL export: %v", err)
	}
	if _, exists := secondRecord["cache_input_mode"]; exists {
		t.Fatal("restored derived cache mode must remain non-explicit on re-export")
	}
	if got := secondRecord["derived_cache_input_mode"]; got != usage.CacheInputModeIncluded {
		t.Fatalf("re-exported derived_cache_input_mode = %v, want %q", got, usage.CacheInputModeIncluded)
	}
}
