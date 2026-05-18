package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/usage-service/internal/usage"
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
	} {
		if !columns[column] {
			t.Fatalf("missing migrated column %s in %#v", column, columns)
		}
	}

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
			InputTokens:          1,
			OutputTokens:         2,
			TotalTokens:          3,
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
	if len(events) != 1 || events[0].AccountSnapshot != "alice@example.com" || events[0].AuthProviderSnapshot != "codex" {
		t.Fatalf("migrated event = %#v", events)
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
		"gpt-a": {Prompt: 1, Completion: 2, Cache: 0.5},
		"gpt-b": {Prompt: 3, Completion: 4, Cache: 0},
	})
	if err != nil {
		t.Fatalf("save model prices: %v", err)
	}
	prices, err := db.LoadModelPrices(context.Background())
	if err != nil {
		t.Fatalf("load model prices: %v", err)
	}
	if len(prices) != 2 || prices["gpt-a"].Prompt != 1 || prices["gpt-b"].Completion != 4 {
		t.Fatalf("prices = %#v", prices)
	}

	result, err := db.UpsertSyncedModelPrices(context.Background(), map[string]ModelPrice{
		"gpt-a": {Prompt: 5, Completion: 6, Cache: 1, Source: "litellm"},
		"bad":   {Prompt: -1, Completion: 0, Cache: 0},
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
	if prices["gpt-a"].Prompt != 5 || prices["gpt-a"].SyncedAtMS == nil || prices["gpt-a"].Source != "litellm" {
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
