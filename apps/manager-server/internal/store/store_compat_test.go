package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestStoreCompatMigratesLegacyUsageEventSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open raw sqlite: %v", err)
	}
	if _, err := raw.Exec(`create table usage_events (
		id integer primary key autoincrement,
		request_id text,
		event_hash text not null unique,
		timestamp_ms integer not null,
		timestamp text not null,
		provider text,
		model text not null,
		endpoint text,
		method text,
		path text,
		auth_type text,
		auth_index text,
		source text,
		source_hash text,
		api_key_hash text,
		input_tokens integer not null default 0,
		output_tokens integer not null default 0,
		reasoning_tokens integer not null default 0,
		cached_tokens integer not null default 0,
		cache_tokens integer not null default 0,
		total_tokens integer not null default 0,
		latency_ms integer,
		failed integer not null default 0,
		raw_json text,
		created_at_ms integer not null
	)`); err != nil {
		_ = raw.Close()
		t.Fatalf("create legacy usage_events: %v", err)
	}
	if _, err := raw.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, endpoint, input_tokens, output_tokens, total_tokens, created_at_ms
	) values (
		'pre-migration-event', 1777999999000, '2026-05-05T23:59:59Z', 'gpt-old', 'POST /v1/chat/completions', 1, 2, 3, 1777999999001
	)`); err != nil {
		_ = raw.Close()
		t.Fatalf("insert legacy usage event: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw sqlite: %v", err)
	}

	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	t.Cleanup(func() {
		_ = store.Close()
	})

	columns := usageEventColumns(t, store.db)
	for _, column := range []string{
		"account_snapshot",
		"auth_label_snapshot",
		"auth_file_snapshot",
		"auth_provider_snapshot",
		"auth_snapshot_at_ms",
		"executor_type",
		"reasoning_effort",
		"service_tier",
		"cache_read_tokens",
		"cache_creation_tokens",
		"ttft_ms",
		"fail_status_code",
		"fail_summary",
		"fail_body",
	} {
		if !columns[column] {
			t.Fatalf("missing migrated column %s in %#v", column, columns)
		}
	}
	var cacheReadTokens, cacheCreationTokens int64
	var failStatusCode, ttftMS sql.NullInt64
	var failSummary, failBody sql.NullString
	if err := store.db.QueryRow(`select cache_read_tokens, cache_creation_tokens, ttft_ms, fail_status_code, fail_summary, fail_body
		from usage_events where event_hash = 'pre-migration-event'`).Scan(
		&cacheReadTokens,
		&cacheCreationTokens,
		&ttftMS,
		&failStatusCode,
		&failSummary,
		&failBody,
	); err != nil {
		t.Fatalf("read pre-migration defaults: %v", err)
	}
	if cacheReadTokens != 0 || cacheCreationTokens != 0 || ttftMS.Valid || failStatusCode.Valid ||
		failSummary.Valid || failBody.Valid {
		t.Fatalf("pre-migration defaults read=%d creation=%d ttft=%#v status=%#v summary=%#v body=%#v",
			cacheReadTokens, cacheCreationTokens, ttftMS, failStatusCode, failSummary, failBody)
	}

	ttft := int64(320)
	_, err = store.InsertEvents(context.Background(), []usage.Event{
		{
			EventHash:            "legacy-schema-event",
			TimestampMS:          1_778_000_000_000,
			Timestamp:            "2026-05-06T00:00:00Z",
			Model:                "gpt-test",
			Endpoint:             "POST /v1/chat/completions",
			AccountSnapshot:      "alice@example.com",
			AuthLabelSnapshot:    "Alice",
			AuthFileSnapshot:     "alice.json",
			AuthProviderSnapshot: "codex",
			AuthSnapshotAtMS:     1_778_000_000_100,
			ExecutorType:         "codex",
			ReasoningEffort:      "medium",
			ServiceTier:          "priority",
			InputTokens:          1,
			OutputTokens:         2,
			CacheReadTokens:      4,
			CacheCreationTokens:  1,
			TotalTokens:          3,
			TTFTMS:               &ttft,
			Failed:               true,
			FailStatusCode:       429,
			FailSummary:          "rate limit exceeded",
			FailBody:             "rate limit exceeded",
			CreatedAtMS:          1_778_000_000_200,
		},
	})
	if err != nil {
		t.Fatalf("insert after migration: %v", err)
	}
	events, err := store.RecentEvents(context.Background(), 10)
	if err != nil {
		t.Fatalf("recent after migration: %v", err)
	}
	var migrated usage.Event
	for _, event := range events {
		if event.EventHash == "legacy-schema-event" {
			migrated = event
			break
		}
	}
	if migrated.EventHash == "" || migrated.AccountSnapshot != "alice@example.com" ||
		migrated.AuthProviderSnapshot != "codex" || migrated.ExecutorType != "codex" ||
		migrated.ReasoningEffort != "medium" || migrated.ServiceTier != "priority" ||
		migrated.CacheReadTokens != 4 || migrated.CacheCreationTokens != 1 ||
		migrated.TTFTMS == nil || *migrated.TTFTMS != 320 ||
		migrated.FailStatusCode != 429 || migrated.FailSummary != "rate limit exceeded" ||
		migrated.FailBody != "" ||
		!migrated.Failed {
		t.Fatalf("migrated event = %#v in %#v", migrated, events)
	}
}

func TestStoreCompatSettingsUsageAndExport(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	setup := Setup{
		CPAUpstreamURL: "http://cpa.local:8317",
		ManagementKey:  "management-key",
		Queue:          "usage",
		PopSide:        "right",
	}
	if err := db.SaveSetup(context.Background(), setup); err != nil {
		t.Fatalf("save setup: %v", err)
	}
	loadedSetup, ok, err := db.LoadSetup(context.Background())
	if err != nil || !ok {
		t.Fatalf("load setup ok=%v err=%v", ok, err)
	}
	if loadedSetup != setup {
		t.Fatalf("setup = %#v, want %#v", loadedSetup, setup)
	}

	enabled := false
	managerCfg := ManagerConfig{
		CPAConnection: ManagerCPAConnectionConfig{
			CPABaseURL:    "http://cpa.local:8317",
			ManagementKey: "management-key",
		},
		Collector: ManagerCollectorConfig{
			Enabled:        &enabled,
			CollectorMode:  "http",
			Queue:          "usage",
			PopSide:        "right",
			BatchSize:      25,
			PollIntervalMS: 500,
			QueryLimit:     100,
		},
		ExternalUsageService: ManagerExternalUsageServiceConfig{
			Enabled:     true,
			ServiceBase: "http://usage.local",
		},
	}
	if err := db.SaveManagerConfig(context.Background(), managerCfg); err != nil {
		t.Fatalf("save manager config: %v", err)
	}
	loadedManagerCfg, ok, err := db.LoadManagerConfig(context.Background())
	if err != nil || !ok {
		t.Fatalf("load manager config ok=%v err=%v", ok, err)
	}
	if loadedManagerCfg.CPAConnection.CPABaseURL != managerCfg.CPAConnection.CPABaseURL ||
		loadedManagerCfg.CPAConnection.ManagementKey != managerCfg.CPAConnection.ManagementKey ||
		loadedManagerCfg.Collector.BatchSize != 25 ||
		loadedManagerCfg.ExternalUsageService.ServiceBase != "http://usage.local" ||
		loadedManagerCfg.UpdatedAtMS <= 0 {
		t.Fatalf("manager config = %#v", loadedManagerCfg)
	}

	insertResult, err := db.InsertEvents(context.Background(), []usage.Event{
		compatStoreEvent("event-a", 1),
		compatStoreEvent("event-b", 2),
		compatStoreEvent("event-a", 1),
	})
	if err != nil {
		t.Fatalf("insert events: %v", err)
	}
	if insertResult.Inserted != 2 || insertResult.Skipped != 1 {
		t.Fatalf("insert result = %#v", insertResult)
	}
	if err := db.AddDeadLetter(context.Background(), `{"bad":true}`, errors.New("parse failed")); err != nil {
		t.Fatalf("add dead letter: %v", err)
	}
	events, deadLetters, err := db.Counts(context.Background())
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if events != 2 || deadLetters != 1 {
		t.Fatalf("counts events=%d deadLetters=%d", events, deadLetters)
	}

	exported, err := db.ExportJSONL(context.Background())
	if err != nil {
		t.Fatalf("export jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(exported)), "\n")
	if len(lines) != 2 {
		t.Fatalf("export lines = %#v", lines)
	}
	var first usage.Event
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("decode first export line: %v", err)
	}
	if first.EventHash != "event-a" {
		t.Fatalf("first exported hash = %q, want event-a", first.EventHash)
	}
	if first.FailBody != "" || first.RawJSON != "" {
		t.Fatalf("export should omit raw sensitive fields: %#v", first)
	}
}

func TestRecentEventsPreservesRawCachedTokens(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	event := compatStoreEvent("raw-cache", 1)
	event.CachedTokens = 10
	event.CacheTokens = 10
	event.CacheReadTokens = 4
	event.CacheCreationTokens = 1
	event.TotalTokens = 43
	if _, err := db.InsertEvents(context.Background(), []usage.Event{event}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	events, err := db.RecentEvents(context.Background(), 10)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	if events[0].CachedTokens != 10 || events[0].CacheTokens != 10 ||
		events[0].CacheReadTokens != 4 || events[0].CacheCreationTokens != 1 {
		t.Fatalf("recent event cache fields = %#v", events[0])
	}

	exported, err := db.ExportJSONL(context.Background())
	if err != nil {
		t.Fatalf("export jsonl: %v", err)
	}
	var exportedEvent usage.Event
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(exported))), &exportedEvent); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if exportedEvent.CachedTokens != 10 || exportedEvent.CacheReadTokens != 4 ||
		exportedEvent.CacheCreationTokens != 1 {
		t.Fatalf("exported cache fields = %#v", exportedEvent)
	}
}

func TestInsertEventsSanitizesImportedFailSummary(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	event := compatStoreEvent("unsafe-summary", 1)
	event.Failed = true
	event.FailStatusCode = 500
	event.FailSummary = "Authorization: Bearer bearer-secret-12345 api_key=sk-test-secret-value"
	if _, err := db.InsertEvents(context.Background(), []usage.Event{event}); err != nil {
		t.Fatalf("insert event: %v", err)
	}

	events, err := db.RecentEvents(context.Background(), 10)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v", events)
	}
	if strings.Contains(events[0].FailSummary, "bearer-secret-12345") ||
		strings.Contains(events[0].FailSummary, "sk-test-secret-value") ||
		!strings.Contains(events[0].FailSummary, "[redacted]") {
		t.Fatalf("fail summary not sanitized: %#v", events[0])
	}
}

func TestStoreCompatModelPricesAndAPIKeyAliases(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	err = db.SaveModelPrices(context.Background(), map[string]ModelPrice{
		"gpt-a": {
			Prompt: 1, Completion: 2, Cache: 0.5, CacheRead: 0.25, CacheCreation: 1.5,
			PromptConfigured: true, CompletionConfigured: true, CacheReadConfigured: true, CacheCreationConfigured: true,
		},
		"gpt-b": {Prompt: 0, Completion: 0, Cache: 0, PromptConfigured: true, CompletionConfigured: true},
	})
	if err != nil {
		t.Fatalf("save model prices: %v", err)
	}
	prices, err := db.LoadModelPrices(context.Background())
	if err != nil {
		t.Fatalf("load model prices: %v", err)
	}
	if len(prices) != 2 || prices["gpt-a"].Prompt != 1 || prices["gpt-a"].CacheRead != 0.25 ||
		prices["gpt-a"].CacheCreation != 1.5 || !prices["gpt-a"].CacheReadConfigured ||
		!prices["gpt-a"].CacheCreationConfigured || prices["gpt-b"].Completion != 0 ||
		!prices["gpt-b"].PromptConfigured || !prices["gpt-b"].CompletionConfigured {
		t.Fatalf("prices = %#v", prices)
	}

	result, err := db.UpsertSyncedModelPrices(context.Background(), map[string]ModelPrice{
		"gpt-a": {
			Prompt: 5, Completion: 6, Cache: 1, CacheRead: 0.75, CacheCreation: 4,
			PromptConfigured: true, CompletionConfigured: true, CacheReadConfigured: true, CacheCreationConfigured: true,
			Source: "litellm",
		},
		"bad": {Prompt: -1, Completion: 0, Cache: 0},
	})
	if err != nil {
		t.Fatalf("upsert synced prices: %v", err)
	}
	if result.Imported != 1 || result.Skipped != 1 {
		t.Fatalf("sync result = %#v", result)
	}
	prices, err = db.LoadModelPrices(context.Background())
	if err != nil {
		t.Fatalf("reload model prices: %v", err)
	}
	if prices["gpt-a"].Prompt != 5 || prices["gpt-a"].CacheRead != 0.75 ||
		prices["gpt-a"].CacheCreation != 4 || prices["gpt-a"].SyncedAtMS == nil ||
		prices["gpt-a"].Source != "litellm" {
		t.Fatalf("synced price = %#v", prices["gpt-a"])
	}

	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := db.UpsertAPIKeyAliases(context.Background(), []APIKeyAlias{{APIKeyHash: hash, Alias: "Team A"}}); err != nil {
		t.Fatalf("upsert alias: %v", err)
	}
	aliases, err := db.LoadAPIKeyAliases(context.Background())
	if err != nil {
		t.Fatalf("load aliases: %v", err)
	}
	if len(aliases) != 1 || aliases[0].APIKeyHash != hash || aliases[0].Alias != "Team A" || aliases[0].UpdatedAtMS <= 0 {
		t.Fatalf("aliases = %#v", aliases)
	}
	if err := db.DeleteAPIKeyAlias(context.Background(), hash); err != nil {
		t.Fatalf("delete alias: %v", err)
	}
	aliases, err = db.LoadAPIKeyAliases(context.Background())
	if err != nil {
		t.Fatalf("reload aliases: %v", err)
	}
	if len(aliases) != 0 {
		t.Fatalf("aliases after delete = %#v", aliases)
	}
}

func usageEventColumns(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`pragma table_info(usage_events)`)
	if err != nil {
		t.Fatalf("pragma table_info: %v", err)
	}
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		columns[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("column rows: %v", err)
	}
	return columns
}

func compatStoreEvent(hash string, offset int64) usage.Event {
	return usage.Event{
		EventHash:    hash,
		TimestampMS:  1_778_000_000_000 + offset,
		Timestamp:    "2026-05-06T00:00:00Z",
		Model:        "gpt-test",
		Endpoint:     "POST /v1/chat/completions",
		InputTokens:  1,
		OutputTokens: 2,
		TotalTokens:  3,
		CreatedAtMS:  1_778_000_000_100 + offset,
	}
}
