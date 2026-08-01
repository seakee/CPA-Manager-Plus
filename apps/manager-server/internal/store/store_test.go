package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
)

func TestStorePausesRollupsUntilUsageCacheAccountingMigrationCompletes(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.InsertEvents(context.Background(), []usage.Event{{
		EventHash:   "rollup-event",
		TimestampMS: 1_778_000_000_000,
		Timestamp:   "2026-05-06T00:00:00Z",
		Model:       "gpt-test",
		CreatedAtMS: 1_778_000_000_100,
	}}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := db.db.Exec(`update usage_data_migrations set status = 'running' where name = 'usage_cache_accounting_v2'`); err != nil {
		t.Fatalf("mark migration running: %v", err)
	}

	accountResult, err := db.CatchUpAccountHistoryRollups(context.Background(), 100, 1_778_000_001_000)
	if err != nil {
		t.Fatalf("account rollup while migrating: %v", err)
	}
	dashboardResult, err := db.CatchUpDashboardHourlyRollups(context.Background(), 100, 1_778_000_001_000)
	if err != nil {
		t.Fatalf("dashboard rollup while migrating: %v", err)
	}
	if !accountResult.Pending || accountResult.Processed != 0 || !dashboardResult.Pending || dashboardResult.Processed != 0 {
		t.Fatalf("rollups were not paused: account=%#v dashboard=%#v", accountResult, dashboardResult)
	}

	if _, err := db.db.Exec(`update usage_data_migrations set status = 'completed' where name = 'usage_cache_accounting_v2'`); err != nil {
		t.Fatalf("mark migration completed: %v", err)
	}
	accountResult, err = db.CatchUpAccountHistoryRollups(context.Background(), 100, 1_778_000_001_000)
	if err != nil {
		t.Fatalf("account rollup after migration: %v", err)
	}
	dashboardResult, err = db.CatchUpDashboardHourlyRollups(context.Background(), 100, 1_778_000_001_000)
	if err != nil {
		t.Fatalf("dashboard rollup after migration: %v", err)
	}
	if accountResult.Processed != 1 || dashboardResult.Processed != 1 {
		t.Fatalf("rollups did not resume: account=%#v dashboard=%#v", accountResult, dashboardResult)
	}
}

func TestStorePersistsAccountSnapshot(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	_, err = db.InsertEvents(context.Background(), []usage.Event{
		{
			EventHash:            "event-1",
			TimestampMS:          1_778_000_000_000,
			Timestamp:            "2026-05-06T00:00:00Z",
			Model:                "gpt-test",
			Endpoint:             "POST /v1/chat/completions",
			AuthIndex:            "auth-1",
			APIKeyHash:           "api-key-hash-1",
			ExecutorType:         "codex",
			AccountSnapshot:      "alice@example.com",
			AuthLabelSnapshot:    "Alice",
			AuthFileSnapshot:     "alice.json",
			AuthProviderSnapshot: "codex",
			AuthSnapshotAtMS:     1_778_000_000_100,
			ServiceTier:          "default",
			CreatedAtMS:          1_778_000_000_200,
		},
	})
	if err != nil {
		t.Fatalf("insert events: %v", err)
	}

	events, err := db.RecentEvents(context.Background(), 10)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	event := events[0]
	if event.AccountSnapshot != "alice@example.com" {
		t.Fatalf("AccountSnapshot = %q", event.AccountSnapshot)
	}
	if event.AuthLabelSnapshot != "Alice" {
		t.Fatalf("AuthLabelSnapshot = %q", event.AuthLabelSnapshot)
	}
	if event.AuthFileSnapshot != "alice.json" {
		t.Fatalf("AuthFileSnapshot = %q", event.AuthFileSnapshot)
	}
	if event.AuthProviderSnapshot != "codex" {
		t.Fatalf("AuthProviderSnapshot = %q", event.AuthProviderSnapshot)
	}
	if event.AuthSnapshotAtMS != 1_778_000_000_100 {
		t.Fatalf("AuthSnapshotAtMS = %d", event.AuthSnapshotAtMS)
	}
	if event.APIKeyHash != "api-key-hash-1" {
		t.Fatalf("APIKeyHash = %q", event.APIKeyHash)
	}
	if event.ExecutorType != "codex" {
		t.Fatalf("ExecutorType = %q", event.ExecutorType)
	}
	if event.ServiceTier != "default" {
		t.Fatalf("ServiceTier = %q", event.ServiceTier)
	}

	payload := usage.BuildPayload(events)
	detail := payload.APIs["POST /v1/chat/completions"].Models["gpt-test"].Details[0]
	if detail.APIKeyHash != "api-key-hash-1" {
		t.Fatalf("payload APIKeyHash = %q", detail.APIKeyHash)
	}
	if detail.AccountSnapshot != "alice@example.com" {
		t.Fatalf("payload AccountSnapshot = %q", detail.AccountSnapshot)
	}
	if detail.AuthProviderSnapshot != "codex" {
		t.Fatalf("payload AuthProviderSnapshot = %q", detail.AuthProviderSnapshot)
	}
	if detail.ExecutorType != "codex" {
		t.Fatalf("payload ExecutorType = %q", detail.ExecutorType)
	}
	if detail.ServiceTier != "default" {
		t.Fatalf("payload ServiceTier = %q", detail.ServiceTier)
	}
}

func TestStoreBackfillsUsageResponseMetadata(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	rawJSON := `{"response_headers":{"Retry-After":["45"],"X-Codex-Plan-Type":["plus"],"X-OAI-Request-ID":["req-backfill"],"Set-Cookie":["session=secret"]}}`
	if _, err := db.db.ExecContext(context.Background(), `insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, failed, fail_status_code, raw_json, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?)`,
		"event-backfill",
		int64(1_778_000_000_000),
		"2026-05-06T00:00:00Z",
		"gpt-test",
		1,
		429,
		rawJSON,
		int64(1_778_000_000_100),
	); err != nil {
		t.Fatalf("insert legacy usage event: %v", err)
	}

	updated, err := db.BackfillUsageResponseMetadata(context.Background(), 10)
	if err != nil {
		t.Fatalf("backfill response metadata: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1", updated)
	}
	updated, err = db.BackfillUsageResponseMetadata(context.Background(), 10)
	if err != nil {
		t.Fatalf("second backfill response metadata: %v", err)
	}
	if updated != 0 {
		t.Fatalf("second updated = %d, want 0", updated)
	}

	events, err := db.RecentEvents(context.Background(), 10)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	event := events[0]
	if event.ResponseMetadata == nil || event.ResponseMetadata.Errors == nil || event.ResponseMetadata.Quota == nil || event.ResponseMetadata.Trace == nil {
		t.Fatalf("response metadata = %#v", event.ResponseMetadata)
	}
	if event.HeaderErrorKind != "rate_limit" || event.HeaderErrorCode != "retry_after" {
		t.Fatalf("header error = %q/%q", event.HeaderErrorKind, event.HeaderErrorCode)
	}
	if event.HeaderQuotaPlanType != "plus" || event.HeaderTraceID != "req-backfill" {
		t.Fatalf("header quota/trace = %q/%q", event.HeaderQuotaPlanType, event.HeaderTraceID)
	}
	if event.ResponseMetadata.Trace.PrimaryTraceID == "session=secret" {
		t.Fatalf("response metadata leaked filtered cookie header: %#v", event.ResponseMetadata.Trace)
	}
}

func TestStoreBackfillsCamelCaseUsageResponseMetadata(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	rawJSON := `{"responseHeaders":{"Retry-After":["30"],"X-Codex-Plan-Type":["team"],"X-OAI-Request-ID":["req-camel-backfill"]}}`
	if _, err := db.db.ExecContext(context.Background(), `insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, failed, fail_status_code, raw_json, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?)`,
		"event-camel-backfill",
		int64(1_778_000_000_000),
		"2026-05-06T00:00:00Z",
		"gpt-test",
		1,
		429,
		rawJSON,
		int64(1_778_000_000_100),
	); err != nil {
		t.Fatalf("insert camelCase legacy usage event: %v", err)
	}

	updated, err := db.BackfillUsageResponseMetadata(context.Background(), 10)
	if err != nil {
		t.Fatalf("backfill camelCase response metadata: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1", updated)
	}

	events, err := db.RecentEvents(context.Background(), 10)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	event := events[0]
	if event.ResponseMetadata == nil || event.ResponseMetadata.Errors == nil || event.ResponseMetadata.Quota == nil || event.ResponseMetadata.Trace == nil {
		t.Fatalf("response metadata = %#v", event.ResponseMetadata)
	}
	if event.HeaderErrorKind != "rate_limit" || event.HeaderErrorCode != "retry_after" {
		t.Fatalf("header error = %q/%q", event.HeaderErrorKind, event.HeaderErrorCode)
	}
	if event.HeaderQuotaPlanType != "team" || event.HeaderTraceID != "req-camel-backfill" {
		t.Fatalf("header quota/trace = %q/%q", event.HeaderQuotaPlanType, event.HeaderTraceID)
	}
}

func TestStoreEnrichesExistingXAIResponseMetadata(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	rawJSON := `{"provider":"xai","executor_type":"XAIExecutor","fail":{"status_code":429,"body":"{\"code\":\"subscription:free-usage-exhausted\",\"error\":\"You've used all the included free usage for model grok-4.5-build-free for now. Usage resets over a rolling 24-hour window — tokens (actual/limit): 1024413/1000000.\"}"},"response_headers":{"X-Request-Id":["req-xai"],"X-Should-Retry":["true"],"X-Data-Retention":["zdr"],"X-Zero-Retention":["true"]}}`
	if _, err := db.db.ExecContext(context.Background(), `insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, executor_type, model, failed, fail_status_code,
		raw_json, response_metadata_json, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"event-xai-enrich",
		int64(1_784_543_105_000),
		"2026-07-20T10:25:05Z",
		"xai",
		"XAIExecutor",
		"grok-4.5",
		1,
		429,
		rawJSON,
		`{"trace":{"request_id":"req-xai","primary_trace_id":"req-xai","client_request_id":"existing-client"}}`,
		int64(1_784_543_105_100),
	); err != nil {
		t.Fatalf("insert xAI usage event: %v", err)
	}

	updated, err := db.BackfillUsageResponseMetadata(context.Background(), 10)
	if err != nil {
		t.Fatalf("enrich xAI response metadata: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1", updated)
	}
	updated, err = db.BackfillUsageResponseMetadata(context.Background(), 10)
	if err != nil {
		t.Fatalf("second xAI enrichment: %v", err)
	}
	if updated != 0 {
		t.Fatalf("second updated = %d, want 0", updated)
	}

	events, err := db.RecentEvents(context.Background(), 10)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(events) != 1 || events[0].ResponseMetadata == nil || events[0].ResponseMetadata.ProviderUsage == nil {
		t.Fatalf("events = %#v", events)
	}
	metadata := events[0].ResponseMetadata
	if metadata.ProviderUsage.Actual == nil || *metadata.ProviderUsage.Actual != 1_024_413 || metadata.ProviderUsage.Overage == nil || *metadata.ProviderUsage.Overage != 24_413 {
		t.Fatalf("provider usage = %#v", metadata.ProviderUsage)
	}
	if metadata.DataPolicy == nil || metadata.DataPolicy.ZeroRetention == nil || !*metadata.DataPolicy.ZeroRetention {
		t.Fatalf("data policy = %#v", metadata.DataPolicy)
	}
	if metadata.Trace == nil || metadata.Trace.ClientRequestID != "existing-client" {
		t.Fatalf("existing trace metadata was not preserved: %#v", metadata.Trace)
	}
}

func TestStoreCompletesPartialXAIProviderUsageMetadata(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	rawJSON := `{"provider":"xai","executor_type":"XAIExecutor","fail":{"status_code":429,"body":"{\"code\":\"subscription:free-usage-exhausted\",\"error\":\"tokens (actual/limit): 1024413/1000000\"}"}}`
	if _, err := db.db.ExecContext(context.Background(), `insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, model, failed, fail_status_code, raw_json, response_metadata_json, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"event-xai-partial",
		int64(1_784_543_105_000),
		"2026-07-20T10:25:05Z",
		"xai",
		"grok-test",
		1,
		429,
		rawJSON,
		`{"provider_usage":{"provider":"xai","kind":"included_free_usage","state":"exhausted","code":"subscription:free-usage-exhausted","actual":1024413,"limit":1000000,"unit":"tokens","window_kind":"rolling_24h","observed_at_ms":1784543105000,"recover_at_ms":1784629505000,"recover_at_estimated":true,"source":"response_body"}}`,
		int64(1_784_543_105_100),
	); err != nil {
		t.Fatalf("insert partial xAI event: %v", err)
	}

	updated, err := db.BackfillUsageResponseMetadata(context.Background(), 10)
	if err != nil {
		t.Fatalf("backfill partial xAI metadata: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1", updated)
	}
	events, err := db.RecentEvents(context.Background(), 10)
	if err != nil || len(events) != 1 || events[0].ResponseMetadata == nil || events[0].ResponseMetadata.ProviderUsage == nil {
		t.Fatalf("events = %#v, err = %v", events, err)
	}
	usageMetadata := events[0].ResponseMetadata.ProviderUsage
	if usageMetadata.Actual == nil || *usageMetadata.Actual != 1_024_413 || usageMetadata.Limit == nil || *usageMetadata.Limit != 1_000_000 {
		t.Fatalf("partial provider usage was not completed: %#v", usageMetadata)
	}
	if usageMetadata.Remaining == nil || *usageMetadata.Remaining != 0 || usageMetadata.Overage == nil || *usageMetadata.Overage != 24_413 {
		t.Fatalf("derived provider usage fields were not completed: %#v", usageMetadata)
	}
}

func TestStoreBackfillSkipsUnsupportedRateLimitHeadersWithoutStarvingLaterRows(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	if _, err := db.db.ExecContext(context.Background(), `insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, failed, raw_json, response_metadata_json, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?)`,
		"event-unsupported-rate-limit",
		int64(1_784_543_104_000),
		"2026-07-20T10:25:04Z",
		"gpt-test",
		0,
		`{"response_headers":{"X-Ratelimit-Reset-Tokens":["60"],"X-Request-ID":["req-unsupported"]}}`,
		`{"trace":{"request_id":"req-unsupported","primary_trace_id":"req-unsupported"}}`,
		int64(1_784_543_104_100),
	); err != nil {
		t.Fatalf("insert unsupported rate-limit event: %v", err)
	}

	xaiRawJSON := `{"provider":"xai","fail":{"status_code":429,"body":"{\"code\":\"subscription:free-usage-exhausted\",\"error\":\"tokens (actual/limit): 1024413/1000000\"}"}}`
	if _, err := db.db.ExecContext(context.Background(), `insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, model, failed, fail_status_code, raw_json, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"event-actionable-xai",
		int64(1_784_543_105_000),
		"2026-07-20T10:25:05Z",
		"xai",
		"grok-test",
		1,
		429,
		xaiRawJSON,
		int64(1_784_543_105_100),
	); err != nil {
		t.Fatalf("insert actionable xAI event: %v", err)
	}

	updated, err := db.BackfillUsageResponseMetadata(context.Background(), 1)
	if err != nil {
		t.Fatalf("backfill response metadata: %v", err)
	}
	if updated != 1 {
		t.Fatalf("updated = %d, want 1", updated)
	}
	updated, err = db.BackfillUsageResponseMetadata(context.Background(), 1)
	if err != nil {
		t.Fatalf("second backfill response metadata: %v", err)
	}
	if updated != 0 {
		t.Fatalf("second updated = %d, want 0", updated)
	}

	var metadataJSON string
	if err := db.db.QueryRowContext(context.Background(), `select coalesce(response_metadata_json, '') from usage_events where event_hash = ?`, "event-actionable-xai").Scan(&metadataJSON); err != nil {
		t.Fatalf("read actionable metadata: %v", err)
	}
	if !strings.Contains(metadataJSON, `"provider_usage"`) {
		t.Fatalf("xAI provider usage was starved: %s", metadataJSON)
	}
}

func TestStoreBackfillMarksUnsupportedOnlyHeaderRowsProcessed(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.db.ExecContext(context.Background(), `insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, failed, raw_json, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?)`,
		"event-unsupported-only-header",
		int64(1_784_543_104_000),
		"2026-07-20T10:25:04Z",
		"gpt-test",
		0,
		`{"response_headers":{"X-Ratelimit-Reset-Tokens":["60"]}}`,
		int64(1_784_543_104_100),
	); err != nil {
		t.Fatalf("insert unsupported-only event: %v", err)
	}

	updated, err := db.BackfillUsageResponseMetadata(context.Background(), 10)
	if err != nil || updated != 1 {
		t.Fatalf("first backfill updated=%d err=%v, want one processed marker", updated, err)
	}
	updated, err = db.BackfillUsageResponseMetadata(context.Background(), 10)
	if err != nil || updated != 0 {
		t.Fatalf("second backfill updated=%d err=%v, want no rescan", updated, err)
	}

	var metadataJSON string
	if err := db.db.QueryRowContext(context.Background(), `select coalesce(response_metadata_json, '') from usage_events where event_hash = ?`, "event-unsupported-only-header").Scan(&metadataJSON); err != nil {
		t.Fatalf("read processed marker: %v", err)
	}
	if metadataJSON != "{}" {
		t.Fatalf("processed marker = %q, want {}", metadataJSON)
	}
}

func TestStorePersistsRequestedAndResolvedModels(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	_, err = db.InsertEvents(context.Background(), []usage.Event{
		{
			EventHash:      "event-dual",
			TimestampMS:    1_778_000_001_000,
			Timestamp:      "2026-05-06T00:00:01Z",
			Model:          "gpt-5.4",
			RequestedModel: "gpt-5.4",
			ResolvedModel:  "gpt-5.5",
			Endpoint:       "POST /v1/chat/completions",
			CreatedAtMS:    1_778_000_001_100,
		},
	})
	if err != nil {
		t.Fatalf("insert events: %v", err)
	}

	events, err := db.RecentEvents(context.Background(), 10)
	if err != nil {
		t.Fatalf("recent events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("len(events) = %d, want 1", len(events))
	}
	if events[0].RequestedModel != "gpt-5.4" {
		t.Fatalf("RequestedModel roundtrip = %q", events[0].RequestedModel)
	}
	if events[0].ResolvedModel != "gpt-5.5" {
		t.Fatalf("ResolvedModel roundtrip = %q", events[0].ResolvedModel)
	}

	payload := usage.BuildPayload(events)
	detail := payload.APIs["POST /v1/chat/completions"].Models["gpt-5.4"].Details[0]
	if detail.ResolvedModel != "gpt-5.5" {
		t.Fatalf("payload Detail.ResolvedModel = %q", detail.ResolvedModel)
	}
}

func TestStoreAPIKeyAliases(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	const hash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := db.UpsertAPIKeyAliases(context.Background(), []APIKeyAlias{
		{APIKeyHash: hash, Alias: " Alice "},
	}); err != nil {
		t.Fatalf("upsert alias: %v", err)
	}

	aliases, err := db.LoadAPIKeyAliases(context.Background())
	if err != nil {
		t.Fatalf("load aliases: %v", err)
	}
	if len(aliases) != 1 {
		t.Fatalf("len(aliases) = %d, want 1", len(aliases))
	}
	if aliases[0].APIKeyHash != hash || aliases[0].Alias != "Alice" || aliases[0].UpdatedAtMS <= 0 {
		t.Fatalf("alias = %#v", aliases[0])
	}

	if err := db.UpsertAPIKeyAliases(context.Background(), []APIKeyAlias{
		{APIKeyHash: hash, Alias: "Team A"},
	}); err != nil {
		t.Fatalf("update alias: %v", err)
	}
	aliases, err = db.LoadAPIKeyAliases(context.Background())
	if err != nil {
		t.Fatalf("reload aliases: %v", err)
	}
	if len(aliases) != 1 || aliases[0].Alias != "Team A" {
		t.Fatalf("updated aliases = %#v", aliases)
	}

	const otherHash = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	if err := db.UpsertAPIKeyAliases(context.Background(), []APIKeyAlias{
		{APIKeyHash: otherHash, Alias: " team a "},
	}); err == nil || err.Error() != "api key alias already exists" {
		t.Fatalf("duplicate alias error = %v", err)
	}
	if err := db.UpsertAPIKeyAliases(context.Background(), []APIKeyAlias{
		{APIKeyHash: hash, Alias: "Alpha"},
		{APIKeyHash: otherHash, Alias: " alpha "},
	}); err == nil || err.Error() != "api key alias already exists" {
		t.Fatalf("batch duplicate alias error = %v", err)
	}

	if err := db.DeleteAPIKeyAlias(context.Background(), hash); err != nil {
		t.Fatalf("delete alias: %v", err)
	}
	aliases, err = db.LoadAPIKeyAliases(context.Background())
	if err != nil {
		t.Fatalf("load after delete: %v", err)
	}
	if len(aliases) != 0 {
		t.Fatalf("aliases after delete = %#v", aliases)
	}
}

func TestStoreAPIKeyAliasesActiveHashesMigration(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	const orphanHash = "1111111111111111111111111111111111111111111111111111111111111111"
	const newHash = "2222222222222222222222222222222222222222222222222222222222222222"
	const activeHash = "3333333333333333333333333333333333333333333333333333333333333333"

	if err := db.UpsertAPIKeyAliases(context.Background(), []APIKeyAlias{
		{APIKeyHash: orphanHash, Alias: "team-a"},
		{APIKeyHash: activeHash, Alias: "team-b"},
	}); err != nil {
		t.Fatalf("seed aliases: %v", err)
	}

	if err := db.UpsertAPIKeyAliasesWithActiveHashes(context.Background(), []APIKeyAlias{
		{APIKeyHash: newHash, Alias: "team-a"},
	}, []string{newHash, activeHash}, false); err == nil || err.Error() != "api key alias already exists" {
		t.Fatalf("orphan cleanup without confirmation should be rejected, got err = %v", err)
	}

	aliases, err := db.LoadAPIKeyAliases(context.Background())
	if err != nil {
		t.Fatalf("load aliases after rejected cleanup: %v", err)
	}
	hashByAlias := map[string]string{}
	for _, alias := range aliases {
		hashByAlias[alias.Alias] = alias.APIKeyHash
	}
	if hashByAlias["team-a"] != orphanHash || hashByAlias["team-b"] != activeHash || len(aliases) != 2 {
		t.Fatalf("rejected cleanup should keep existing aliases, got %#v", aliases)
	}

	if err := db.UpsertAPIKeyAliasesWithActiveHashes(context.Background(), []APIKeyAlias{
		{APIKeyHash: newHash, Alias: "team-a"},
	}, []string{newHash, activeHash}, true); err != nil {
		t.Fatalf("migrate alias from orphan: %v", err)
	}

	aliases, err = db.LoadAPIKeyAliases(context.Background())
	if err != nil {
		t.Fatalf("load aliases: %v", err)
	}
	hashByAlias = map[string]string{}
	for _, alias := range aliases {
		hashByAlias[alias.Alias] = alias.APIKeyHash
	}
	if hashByAlias["team-a"] != newHash {
		t.Fatalf("team-a should belong to newHash, got %#v", aliases)
	}
	if hashByAlias["team-b"] != activeHash {
		t.Fatalf("team-b should remain on activeHash, got %#v", aliases)
	}
	if len(aliases) != 2 {
		t.Fatalf("orphan record should be cleaned up, got %#v", aliases)
	}

	if err := db.UpsertAPIKeyAliasesWithActiveHashes(context.Background(), []APIKeyAlias{
		{APIKeyHash: newHash, Alias: "team-b"},
	}, []string{newHash, activeHash}, true); err == nil || err.Error() != "api key alias already exists" {
		t.Fatalf("active conflict should be rejected, got err = %v", err)
	}
}
