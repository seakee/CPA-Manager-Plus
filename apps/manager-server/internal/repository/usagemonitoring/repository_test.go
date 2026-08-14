package usagemonitoring_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	monitoringrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagemonitoring"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const testDayMS = int64(24 * time.Hour / time.Millisecond)

func TestMigrationCreatesUsageMonitoringRollupSchema(t *testing.T) {
	sqlDB, _ := newMonitoringRepositoryStore(t)
	for _, table := range []string{
		"usage_monitoring_account_daily_rollups_v1",
		"usage_monitoring_api_key_daily_rollups_v1",
		"usage_monitoring_selector_daily_rollups_v1",
		"usage_monitoring_event_projection_v1",
		"usage_monitoring_event_search_v1",
		"usage_monitoring_header_latest_v1",
		"usage_monitoring_rollup_state",
		"usage_monitoring_search_index_state",
	} {
		var count int
		if err := sqlDB.QueryRow(`select count(*) from sqlite_master where type = 'table' and name = ?`, table).Scan(&count); err != nil {
			t.Fatalf("query table %s: %v", table, err)
		}
		if count != 1 {
			t.Fatalf("table %s count = %d, want 1", table, count)
		}
	}
	for _, name := range []string{monitoringrepo.StatsRollupName, monitoringrepo.MetadataRollupName, monitoringrepo.ProjectionRollupName} {
		var version int
		var status string
		if err := sqlDB.QueryRow(`select schema_version, status from usage_monitoring_rollup_state where rollup_name = ?`, name).Scan(&version, &status); err != nil {
			t.Fatalf("query state %s: %v", name, err)
		}
		if version != monitoringrepo.SchemaVersion || status != "ready" {
			t.Fatalf("state %s = version:%d status:%q", name, version, status)
		}
	}
	var accountKeyColumns int
	if err := sqlDB.QueryRow(`select count(*) from pragma_table_info('usage_monitoring_event_projection_v1') where name = 'account_key'`).Scan(&accountKeyColumns); err != nil {
		t.Fatalf("inspect projection account key column: %v", err)
	}
	if accountKeyColumns != 1 {
		t.Fatalf("projection account key columns = %d, want 1", accountKeyColumns)
	}
	for _, column := range []string{
		"display_input_tokens",
		"display_output_tokens",
		"display_non_reasoning_output_tokens",
		"display_reasoning_tokens",
		"display_unclassified_tokens",
		"display_cached_tokens",
		"display_cache_read_tokens",
		"display_cache_creation_tokens",
		"display_total_tokens",
	} {
		var count int
		if err := sqlDB.QueryRow(`select count(*) from pragma_table_info('usage_monitoring_account_daily_rollups_v1') where name = ?`, column).Scan(&count); err != nil {
			t.Fatalf("inspect account daily display column %s: %v", column, err)
		}
		if count != 1 {
			t.Fatalf("account daily display column %s count = %d, want 1", column, count)
		}
	}
	var accountWindowIndexes int
	if err := sqlDB.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = 'idx_usage_monitoring_event_projection_account_window'`).Scan(&accountWindowIndexes); err != nil {
		t.Fatalf("inspect projection account window index: %v", err)
	}
	if accountWindowIndexes != 1 {
		t.Fatalf("projection account window indexes = %d, want 1", accountWindowIndexes)
	}
	var modelIndexes int
	if err := sqlDB.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = 'idx_usage_monitoring_event_projection_model_timestamp'`).Scan(&modelIndexes); err != nil {
		t.Fatalf("inspect projection analytics model index: %v", err)
	}
	if modelIndexes != 1 {
		t.Fatalf("projection analytics model indexes = %d, want 1", modelIndexes)
	}
	planRows, err := sqlDB.Query(`explain query plan select event_id
		from usage_monitoring_event_projection_v1
		where analytics_model = ? and timestamp_ms >= ? and timestamp_ms < ?
		order by timestamp_ms desc, event_id desc`, "deepseek-v4-flash", int64(0), int64(1))
	if err != nil {
		t.Fatalf("explain projection analytics model query: %v", err)
	}
	var planDetails []string
	for planRows.Next() {
		var id, parent, unused int
		var detail string
		if err := planRows.Scan(&id, &parent, &unused, &detail); err != nil {
			_ = planRows.Close()
			t.Fatalf("scan projection analytics model query plan: %v", err)
		}
		planDetails = append(planDetails, detail)
	}
	if err := planRows.Close(); err != nil {
		t.Fatalf("close projection analytics model query plan: %v", err)
	}
	if err := planRows.Err(); err != nil {
		t.Fatalf("read projection analytics model query plan: %v", err)
	}
	if !strings.Contains(strings.Join(planDetails, "\n"), "idx_usage_monitoring_event_projection_model_timestamp") {
		t.Fatalf("projection analytics model query plan = %v", planDetails)
	}
	for indexName, expression := range map[string]string{
		"idx_usage_monitoring_account_daily_credential_window": "trim(auth_file_snapshot)",
		"idx_usage_monitoring_account_daily_legacy_window":     "trim(source)",
	} {
		var count int
		if err := sqlDB.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = ?`, indexName).Scan(&count); err != nil {
			t.Fatalf("inspect account daily window index %s: %v", indexName, err)
		}
		if count != 1 {
			t.Fatalf("account daily window index %s count = %d, want 1", indexName, count)
		}
		var definition string
		if err := sqlDB.QueryRow(`select sql from sqlite_master where type = 'index' and name = ?`, indexName).Scan(&definition); err != nil {
			t.Fatalf("read account daily window index %s: %v", indexName, err)
		}
		if !strings.Contains(definition, expression) || !strings.Contains(definition, "trim(auth_index)") {
			t.Fatalf("account daily window index %s definition = %q", indexName, definition)
		}
	}
}

func TestAccountWindowProjectionMatchesRawAcrossCoverageTailAndIdentity(t *testing.T) {
	sqlDB, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	fromMS := int64(1_800_060_000_000)
	toMS := fromMS + 10_000
	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"gpt-window": {
			Prompt: 1,
			ContextTiers: []store.ModelPriceContextTier{
				{ThresholdTokens: 100, Prompt: 2, PromptConfigured: true},
			},
		},
	}); err != nil {
		t.Fatalf("save model prices: %v", err)
	}

	first := monitoringRepositoryEvent("window-projected", fromMS, "gpt-window", "key-a", "shared@example.com", "auth-shared", "source-a", false, 150, 30, 10)
	first.AuthFileSnapshot = "first.json"
	otherCredential := monitoringRepositoryEvent("window-other-credential", fromMS+1_000, "gpt-window", "key-b", "shared@example.com", "auth-shared", "source-b", false, 9_000, 9_000, 10)
	otherCredential.AuthFileSnapshot = "second.json"
	toBoundary := monitoringRepositoryEvent("window-to-boundary", toMS, "gpt-window", "key-a", "shared@example.com", "auth-shared", "source-a", false, 8_000, 8_000, 10)
	toBoundary.AuthFileSnapshot = "first.json"
	if _, err := db.InsertEvents(ctx, []usage.Event{first, otherCredential, toBoundary}); err != nil {
		t.Fatalf("insert projected events: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)
	expectedAccountKey, valid := usageidentity.AccountKey(usageidentity.Fields{
		AuthFileSnapshot:     "first.json",
		AuthIndex:            "auth-shared",
		AuthProviderSnapshot: "codex",
		AccountSnapshot:      "shared@example.com",
	})
	if !valid {
		t.Fatal("invalid expected account key")
	}
	var projectedAccountKey string
	if err := sqlDB.QueryRowContext(ctx, `select account_key
		from usage_monitoring_event_projection_v1
		where event_id = (select id from usage_events where event_hash = ?)`, first.EventHash).Scan(&projectedAccountKey); err != nil {
		t.Fatalf("read projected account key: %v", err)
	}
	if projectedAccountKey != expectedAccountKey {
		t.Fatalf("projected account key = %q, want %q", projectedAccountKey, expectedAccountKey)
	}

	windows := []store.AccountWindowUsageQuery{{
		RequestIndex:          0,
		FromMS:                fromMS,
		ToMS:                  toMS,
		AccountSnapshot:       "shared@example.com",
		AuthFileSnapshot:      "first.json",
		AuthProviderSnapshot:  "codex",
		AuthProjectIDSnapshot: "project-a",
		AuthIndex:             "auth-shared",
		Source:                "first.json",
	}}
	assertMatchesRaw := func(phase string) []store.AccountWindowModelStat {
		t.Helper()
		raw, err := db.AccountWindowModelStats(ctx, windows)
		if err != nil {
			t.Fatalf("%s raw account window stats: %v", phase, err)
		}
		projected, _, available, err := db.UsageMonitoringAccountWindowStats(ctx, windows)
		if err != nil {
			t.Fatalf("%s projected account window stats: %v", phase, err)
		}
		if !available {
			t.Fatalf("%s projected account window stats unavailable", phase)
		}
		if !reflect.DeepEqual(projected, raw) {
			t.Fatalf("%s account window mismatch\nprojection=%#v\nraw=%#v", phase, projected, raw)
		}
		return projected
	}

	projected := assertMatchesRaw("complete coverage")
	if len(projected) != 1 || projected[0].Calls != 1 || projected[0].InputTokens != 150 || projected[0].ContextThresholdTokens != 100 {
		t.Fatalf("complete coverage stats = %#v", projected)
	}

	tail := monitoringRepositoryEvent("window-raw-tail", fromMS+2_000, "gpt-window", "key-a", "shared@example.com", "auth-shared", "source-a", true, 1_000_000, 40, 10)
	tail.AuthFileSnapshot = "first.json"
	if _, err := db.InsertEvents(ctx, []usage.Event{tail}); err != nil {
		t.Fatalf("insert raw tail event: %v", err)
	}
	projected = assertMatchesRaw("partial coverage")
	if len(projected) != 1 || projected[0].Calls != 2 || projected[0].SuccessCalls != 1 || projected[0].FailureCalls != 1 || projected[0].LongInputTokens != 1_000_000 {
		t.Fatalf("partial coverage stats = %#v", projected)
	}

	if _, err := sqlDB.ExecContext(ctx, `update usage_monitoring_rollup_state set schema_version = 0 where rollup_name = ?`, monitoringrepo.ProjectionRollupName); err != nil {
		t.Fatalf("invalidate projection schema: %v", err)
	}
	_, state, available, err := db.UsageMonitoringAccountWindowStats(ctx, windows)
	if err != nil {
		t.Fatalf("read unavailable projection: %v", err)
	}
	if available || state.SchemaVersion != 0 {
		t.Fatalf("unavailable projection state = %#v available=%v", state, available)
	}
}

func TestAccountWindowProjectionUsesDailyStatsWithEdgesAndRawTail(t *testing.T) {
	sqlDB, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	dayStartMS := int64(1_800_057_600_000)
	fromMS := dayStartMS + int64(time.Hour/time.Millisecond)
	toMS := dayStartMS + 3*testDayMS + int64(2*time.Hour/time.Millisecond)

	events := []usage.Event{
		monitoringRepositoryEvent("window-daily-start-edge", dayStartMS+2*int64(time.Hour/time.Millisecond), "gpt-window", "key-a", "daily@example.com", "auth-daily", "daily.json", false, 10, 1, 10),
		monitoringRepositoryEvent("window-daily-full-a", dayStartMS+testDayMS+int64(time.Hour/time.Millisecond), "gpt-window", "key-a", "daily@example.com", "auth-daily", "daily.json", false, 20, 2, 10),
		monitoringRepositoryEvent("window-daily-full-b", dayStartMS+2*testDayMS+int64(time.Hour/time.Millisecond), "gpt-window", "key-a", "daily@example.com", "auth-daily", "daily.json", true, 30, 3, 10),
		monitoringRepositoryEvent("window-daily-end-edge", dayStartMS+3*testDayMS+int64(time.Hour/time.Millisecond), "gpt-window", "key-a", "daily@example.com", "auth-daily", "daily.json", false, 40, 4, 10),
		monitoringRepositoryEvent("window-daily-other-credential", dayStartMS+testDayMS+2*int64(time.Hour/time.Millisecond), "gpt-window", "key-b", "daily@example.com", "auth-other", "other.json", false, 9_000, 9_000, 10),
	}
	for index := range events {
		events[index].AuthFileSnapshot = events[index].Source
		events[index].AuthProviderSnapshot = "codex"
	}
	if _, err := db.InsertEvents(ctx, events); err != nil {
		t.Fatalf("insert daily account window events: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	if _, err := sqlDB.ExecContext(ctx, `update usage_monitoring_event_projection_v1
		set normalized_total_input_tokens = 999999
		where event_id in (
			select id from usage_events where event_hash in ('window-daily-full-a', 'window-daily-full-b')
		)`); err != nil {
		t.Fatalf("corrupt covered full-day projection rows: %v", err)
	}

	tail := monitoringRepositoryEvent(
		"window-daily-raw-tail",
		dayStartMS+2*testDayMS+2*int64(time.Hour/time.Millisecond),
		"gpt-window",
		"key-a",
		"daily@example.com",
		"auth-daily",
		"daily.json",
		false,
		50,
		5,
		10,
	)
	tail.AuthFileSnapshot = "daily.json"
	tail.AuthProviderSnapshot = "codex"
	if _, err := db.InsertEvents(ctx, []usage.Event{tail}); err != nil {
		t.Fatalf("insert daily account window raw tail: %v", err)
	}

	windows := []store.AccountWindowUsageQuery{{
		RequestIndex:         0,
		FromMS:               fromMS,
		ToMS:                 toMS,
		AccountSnapshot:      "daily@example.com",
		AuthFileSnapshot:     "daily.json",
		AuthProviderSnapshot: "codex",
		AuthIndex:            "auth-daily",
		Source:               "daily.json",
	}}
	raw, err := db.AccountWindowModelStats(ctx, windows)
	if err != nil {
		t.Fatalf("read raw daily account window stats: %v", err)
	}
	projected, _, available, err := db.UsageMonitoringAccountWindowStats(ctx, windows)
	if err != nil {
		t.Fatalf("read projected daily account window stats: %v", err)
	}
	if !available {
		t.Fatal("projected daily account window stats unavailable")
	}
	if !reflect.DeepEqual(projected, raw) {
		t.Fatalf("daily account window mismatch\nprojection=%#v\nraw=%#v", projected, raw)
	}
	if len(projected) != 1 || projected[0].Calls != 5 || projected[0].SuccessCalls != 4 || projected[0].FailureCalls != 1 || projected[0].InputTokens != 150 {
		t.Fatalf("daily account window stats = %#v", projected)
	}
}

func TestUsageMonitoringSearchIndexTracksProjectionInsertUpdateAndDelete(t *testing.T) {
	sqlDB, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	fromMS := int64(1_800_057_600_000)
	toMS := fromMS + testDayMS
	event := monitoringRepositoryEvent(
		"search-index-event",
		fromMS+1_000,
		"gpt-search",
		"key-search",
		"search@example.com",
		"auth-search",
		"source-search",
		false,
		10,
		2,
		0,
	)
	event.RequestID = "initial-marker"
	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert search event: %v", err)
	}
	var eventID int64
	if err := sqlDB.QueryRowContext(ctx, `select id from usage_events where event_hash = ?`, event.EventHash).Scan(&eventID); err != nil {
		t.Fatalf("read search event id: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	countSearchEvents := func(query string) int64 {
		t.Helper()
		count, _, available, err := db.UsageMonitoringEventsCount(ctx, store.AnalyticsFilter{
			FromMS:        fromMS,
			ToMS:          toMS,
			SearchQuery:   query,
			IncludeFailed: true,
		})
		if err != nil || !available {
			t.Fatalf("search count query %q: available=%v err=%v", query, available, err)
		}
		return count
	}
	if got := countSearchEvents("initial-marker"); got != 1 {
		t.Fatalf("initial search count = %d, want 1", got)
	}

	if _, err := sqlDB.ExecContext(ctx, `update usage_monitoring_event_projection_v1
		set search_text = 'updated-marker' where event_id = ?`, eventID); err != nil {
		t.Fatalf("update event projection search text: %v", err)
	}
	if got := countSearchEvents("initial-marker"); got != 0 {
		t.Fatalf("stale search count = %d, want 0", got)
	}
	if got := countSearchEvents("updated-marker"); got != 1 {
		t.Fatalf("updated search count = %d, want 1", got)
	}

	if _, err := sqlDB.ExecContext(ctx, `delete from usage_monitoring_event_projection_v1 where event_id = ?`, eventID); err != nil {
		t.Fatalf("delete event projection: %v", err)
	}
	if got := countSearchEvents("updated-marker"); got != 0 {
		t.Fatalf("deleted search count = %d, want 0", got)
	}
}

func TestMigrationBackfillsSearchIndexForExistingProjection(t *testing.T) {
	sqlDB, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	fromMS := int64(1_800_057_600_000)
	event := monitoringRepositoryEvent(
		"search-index-upgrade",
		fromMS+1_000,
		"gpt-search",
		"key-search",
		"upgrade@example.com",
		"auth-search",
		"source-search",
		false,
		10,
		2,
		0,
	)
	event.RequestID = "upgrade-marker"
	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert upgrade search event: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	if _, err := sqlDB.ExecContext(ctx, `drop table usage_monitoring_event_search_v1`); err != nil {
		t.Fatalf("drop search index fixture: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `drop table usage_monitoring_search_index_state`); err != nil {
		t.Fatalf("drop search index state fixture: %v", err)
	}
	if err := sqliterepo.Migrate(sqlDB); err != nil {
		t.Fatalf("migrate existing projection search index: %v", err)
	}

	count, _, available, err := db.UsageMonitoringEventsCount(ctx, store.AnalyticsFilter{
		FromMS:        fromMS,
		ToMS:          fromMS + testDayMS,
		SearchQuery:   "upgrade-marker",
		IncludeFailed: true,
	})
	if err != nil || !available || count != 1 {
		t.Fatalf("backfilled search count = %d, available=%v err=%v", count, available, err)
	}
}

func TestMigrationRebuildsSearchIndexWhenReadyStateSurvivesMissingIndex(t *testing.T) {
	sqlDB, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	fromMS := int64(1_800_057_600_000)
	event := monitoringRepositoryEvent(
		"search-index-recovery",
		fromMS+1_000,
		"gpt-search",
		"key-search",
		"recovery@example.com",
		"auth-search",
		"source-search",
		false,
		10,
		2,
		0,
	)
	event.RequestID = "recovery-marker"
	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert recovery search event: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	if _, err := sqlDB.ExecContext(ctx, `drop table usage_monitoring_event_search_v1`); err != nil {
		t.Fatalf("drop search index fixture: %v", err)
	}
	var ready int
	if err := sqlDB.QueryRowContext(ctx, `select ready from usage_monitoring_search_index_state where id = 1`).Scan(&ready); err != nil {
		t.Fatalf("read surviving search index state: %v", err)
	}
	if ready != 1 {
		t.Fatalf("surviving search index ready = %d, want 1", ready)
	}
	if err := sqliterepo.Migrate(sqlDB); err != nil {
		t.Fatalf("migrate recreated projection search index: %v", err)
	}

	count, _, available, err := db.UsageMonitoringEventsCount(ctx, store.AnalyticsFilter{
		FromMS:        fromMS,
		ToMS:          fromMS + testDayMS,
		SearchQuery:   "recovery-marker",
		IncludeFailed: true,
	})
	if err != nil || !available || count != 1 {
		t.Fatalf("recovered search count = %d, available=%v err=%v", count, available, err)
	}
}

func TestMigrationRecoversMissingStatsTableAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	sqlDB, db := openMonitoringRepositoryStore(t, path)
	ctx := context.Background()
	baseMS := int64(1_800_057_600_000)
	if _, err := db.InsertEvents(ctx, []usage.Event{
		monitoringRepositoryEvent("stats-recovery-a", baseMS+1_000, "gpt-a", "key-a", "alice@example.com", "auth-a", "source-a", false, 10, 5, 10),
		monitoringRepositoryEvent("stats-recovery-b", baseMS+2_000, "gpt-b", "key-b", "bob@example.com", "auth-b", "source-b", true, 20, 7, 20),
	}); err != nil {
		t.Fatalf("insert stats recovery events: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)
	filter := store.AnalyticsFilter{FromMS: baseMS, ToMS: baseMS + testDayMS, IncludeFailed: true}
	assertMonitoringReadersMatchRaw(t, ctx, db, filter)

	if _, err := sqlDB.ExecContext(ctx, `drop table usage_monitoring_api_key_daily_rollups_v1`); err != nil {
		t.Fatalf("drop API key stats table: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close damaged stats database: %v", err)
	}
	sqlDB, db = openMonitoringRepositoryStore(t, path)
	t.Cleanup(func() { _ = sqlDB.Close() })

	state, err := db.UsageMonitoringState(ctx, monitoringrepo.StatsRollupName)
	if err != nil {
		t.Fatalf("load recovered stats state: %v", err)
	}
	if state.Status != "pending" || state.CoverageEventID != 0 || state.ProcessedEvents != 0 || state.TargetEventID == 0 {
		t.Fatalf("recovered stats state = %#v", state)
	}
	var accountRows int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from usage_monitoring_account_daily_rollups_v1`).Scan(&accountRows); err != nil {
		t.Fatalf("count reset account stats rows: %v", err)
	}
	if accountRows != 0 {
		t.Fatalf("surviving account stats rows = %d, want 0", accountRows)
	}

	catchUpMonitoringRepository(t, ctx, db)
	assertMonitoringReadersMatchRaw(t, ctx, db, filter)
}

func TestMigrationRecoversMissingMetadataTableAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	sqlDB, db := openMonitoringRepositoryStore(t, path)
	ctx := context.Background()
	baseMS := int64(1_800_057_600_000)
	if _, err := db.InsertEvents(ctx, []usage.Event{
		monitoringRepositoryEvent("metadata-recovery", baseMS+1_000, "gpt-a", "key-a", "alice@example.com", "auth-a", "source-a", false, 10, 5, 10),
	}); err != nil {
		t.Fatalf("insert metadata recovery event: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	if _, err := sqlDB.ExecContext(ctx, `drop table usage_monitoring_header_latest_v1`); err != nil {
		t.Fatalf("drop header metadata table: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close damaged metadata database: %v", err)
	}
	sqlDB, db = openMonitoringRepositoryStore(t, path)
	t.Cleanup(func() { _ = sqlDB.Close() })

	state, err := db.UsageMonitoringState(ctx, monitoringrepo.MetadataRollupName)
	if err != nil {
		t.Fatalf("load recovered metadata state: %v", err)
	}
	if state.Status != "pending" || state.CoverageEventID != 0 || state.ProcessedEvents != 0 || state.TargetEventID == 0 {
		t.Fatalf("recovered metadata state = %#v", state)
	}
	var selectorRows int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from usage_monitoring_selector_daily_rollups_v1`).Scan(&selectorRows); err != nil {
		t.Fatalf("count reset selector rows: %v", err)
	}
	if selectorRows != 0 {
		t.Fatalf("surviving selector rows = %d, want 0", selectorRows)
	}

	catchUpMonitoringRepository(t, ctx, db)
	filter := store.AnalyticsFilter{FromMS: baseMS, ToMS: baseMS + testDayMS, IncludeFailed: true}
	assertMonitoringReadersMatchRaw(t, ctx, db, filter)
	assertHeaderReadersMatchRaw(t, ctx, db, baseMS, 10)
}

func TestMigrationRecoversMissingProjectionAndClearsStaleSearchIndexAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	sqlDB, db := openMonitoringRepositoryStore(t, path)
	ctx := context.Background()
	baseMS := int64(1_800_057_600_000)
	event := monitoringRepositoryEvent("projection-recovery", baseMS+1_000, "gpt-a", "key-a", "alice@example.com", "auth-a", "source-a", false, 10, 5, 10)
	event.RequestID = "freshprojectionmarker"
	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert projection recovery event: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)
	if _, err := sqlDB.ExecContext(ctx, `update usage_monitoring_event_projection_v1 set search_text = 'staleprojectionmarker'`); err != nil {
		t.Fatalf("seed stale projection search text: %v", err)
	}
	assertSearchIndexCount(t, ctx, sqlDB, "staleprojectionmarker", 1)

	if _, err := sqlDB.ExecContext(ctx, `drop table usage_monitoring_event_projection_v1`); err != nil {
		t.Fatalf("drop event projection table: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close damaged projection database: %v", err)
	}
	sqlDB, db = openMonitoringRepositoryStore(t, path)
	t.Cleanup(func() { _ = sqlDB.Close() })

	state, err := db.UsageMonitoringState(ctx, monitoringrepo.ProjectionRollupName)
	if err != nil {
		t.Fatalf("load recovered projection state: %v", err)
	}
	if state.Status != "pending" || state.CoverageEventID != 0 || state.ProcessedEvents != 0 || state.TargetEventID == 0 {
		t.Fatalf("recovered projection state = %#v", state)
	}
	assertSearchIndexCount(t, ctx, sqlDB, "staleprojectionmarker", 0)

	catchUpMonitoringRepository(t, ctx, db)
	filter := store.AnalyticsFilter{FromMS: baseMS, ToMS: baseMS + testDayMS, IncludeFailed: true}
	assertProjectionCoreReadersMatchRaw(t, ctx, db, filter)
	assertSearchIndexCount(t, ctx, sqlDB, "freshprojectionmarker", 1)
}

func TestMigrationRecoversMissingStatsStateWithoutDoubleCounting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	sqlDB, db := openMonitoringRepositoryStore(t, path)
	ctx := context.Background()
	baseMS := int64(1_800_057_600_000)
	if _, err := db.InsertEvents(ctx, []usage.Event{
		monitoringRepositoryEvent("stats-state-recovery", baseMS+1_000, "gpt-a", "key-a", "alice@example.com", "auth-a", "source-a", false, 10, 5, 10),
	}); err != nil {
		t.Fatalf("insert stats state recovery event: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)
	if _, err := sqlDB.ExecContext(ctx, `delete from usage_monitoring_rollup_state where rollup_name = ?`, monitoringrepo.StatsRollupName); err != nil {
		t.Fatalf("delete stats rollup state: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close missing-state database: %v", err)
	}
	sqlDB, db = openMonitoringRepositoryStore(t, path)
	t.Cleanup(func() { _ = sqlDB.Close() })

	catchUpMonitoringRepository(t, ctx, db)
	filter := store.AnalyticsFilter{FromMS: baseMS, ToMS: baseMS + testDayMS, IncludeFailed: true}
	assertMonitoringReadersMatchRaw(t, ctx, db, filter)
}

func TestUsageMonitoringRollupsMatchRawAcrossEdgesTailAndOutOfOrderTimestamps(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	day0 := int64(1_800_057_600_000)
	fromMS := day0 + 6*60*60*1000
	toMS := day0 + 4*testDayMS + 18*60*60*1000
	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"gpt-a": {
			Prompt: 1,
			ContextTiers: []store.ModelPriceContextTier{
				{ThresholdTokens: 100, Prompt: 2, PromptConfigured: true},
			},
		},
		"gpt-b": {Prompt: 3, Completion: 4},
	}); err != nil {
		t.Fatalf("save prices: %v", err)
	}

	initial := []usage.Event{
		monitoringRepositoryEvent("edge-left", day0+8*60*60*1000, "gpt-a", "key-a", "alice@example.com", "auth-a", "source-a", false, 150, 30, 12),
		monitoringRepositoryEvent("day-one", day0+testDayMS+2*60*60*1000, "gpt-a", "key-a", "alice@example.com", "auth-a", "source-a", true, 280_000, 40, 25),
		monitoringRepositoryEvent("day-two", day0+2*testDayMS+3*60*60*1000, "gpt-b", "key-b", "bob@example.com", "auth-b", "source-b", false, 90, 20, 0),
		monitoringRepositoryEvent("day-three", day0+3*testDayMS+4*60*60*1000, "gpt-a", "key-a", "alice@example.com", "auth-a", "source-a", false, 80, 10, 35),
		monitoringRepositoryEvent("edge-right", day0+4*testDayMS+10*60*60*1000, "gpt-b", "key-b", "bob@example.com", "auth-b", "source-b", false, 70, 15, 45),
		monitoringRepositoryEvent("zero-token", day0+2*testDayMS+4*60*60*1000, "gpt-b", "key-b", "bob@example.com", "auth-b", "source-b", false, 0, 0, 15),
	}
	initial[0].ReasoningTokens = 7
	if _, err := db.InsertEvents(ctx, initial); err != nil {
		t.Fatalf("insert initial events: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	assertMonitoringReadersMatchRaw(t, ctx, db, store.AnalyticsFilter{
		FromMS:        fromMS,
		ToMS:          toMS,
		IncludeFailed: true,
	})
	assertProjectionCoreReadersMatchRaw(t, ctx, db, store.AnalyticsFilter{
		FromMS:        fromMS,
		ToMS:          toMS,
		IncludeFailed: true,
	})
	assertMonitoringReadersMatchRaw(t, ctx, db, store.AnalyticsFilter{
		FromMS:        fromMS,
		ToMS:          toMS,
		Models:        []string{"gpt-a"},
		Providers:     []string{"codex"},
		Accounts:      []string{"alice@example.com"},
		APIKeyHashes:  []string{"key-a"},
		RequestTypes:  []string{"codex"},
		IncludeFailed: false,
	})
	assertProjectionCoreReadersMatchRaw(t, ctx, db, store.AnalyticsFilter{
		FromMS:        fromMS,
		ToMS:          toMS,
		SearchQuery:   "alice",
		IncludeFailed: true,
	})

	olderHeaderTail := monitoringRepositoryEvent(
		"tail-older-header",
		day0+testDayMS+60*60*1000,
		"gpt-a",
		"key-a",
		"alice@example.com",
		"auth-a",
		"source-z",
		false,
		110,
		22,
		18,
	)
	newerHeaderTail := monitoringRepositoryEvent(
		"tail-newer-header",
		day0+2*testDayMS+12*60*60*1000,
		"gpt-a",
		"key-a",
		"alice@example.com",
		"auth-a",
		"source-z",
		false,
		120,
		24,
		20,
	)
	if _, err := db.InsertEvents(ctx, []usage.Event{olderHeaderTail, newerHeaderTail}); err != nil {
		t.Fatalf("insert pending tail: %v", err)
	}

	filter := store.AnalyticsFilter{FromMS: fromMS, ToMS: toMS, IncludeFailed: true}
	assertMonitoringReadersMatchRaw(t, ctx, db, filter)
	assertProjectionCoreReadersMatchRaw(t, ctx, db, filter)
	assertHeaderReadersMatchRaw(t, ctx, db, fromMS, 100)

	catchUpMonitoringRepository(t, ctx, db)
	assertMonitoringReadersMatchRaw(t, ctx, db, filter)
	assertProjectionCoreReadersMatchRaw(t, ctx, db, filter)
	assertHeaderReadersMatchRaw(t, ctx, db, fromMS, 100)

	statsBefore, err := db.UsageMonitoringState(ctx, monitoringrepo.StatsRollupName)
	if err != nil {
		t.Fatalf("stats state before idempotent catch-up: %v", err)
	}
	metadataBefore, err := db.UsageMonitoringState(ctx, monitoringrepo.MetadataRollupName)
	if err != nil {
		t.Fatalf("metadata state before idempotent catch-up: %v", err)
	}
	statsResult, err := db.CatchUpUsageMonitoringStats(ctx, 1000, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("idempotent stats catch-up: %v", err)
	}
	metadataResult, err := db.CatchUpUsageMonitoringMetadata(ctx, 1000, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("idempotent metadata catch-up: %v", err)
	}
	if statsResult.Processed != 0 || metadataResult.Processed != 0 {
		t.Fatalf("idempotent catch-up processed stats:%d metadata:%d", statsResult.Processed, metadataResult.Processed)
	}
	statsAfter, _ := db.UsageMonitoringState(ctx, monitoringrepo.StatsRollupName)
	metadataAfter, _ := db.UsageMonitoringState(ctx, monitoringrepo.MetadataRollupName)
	if statsAfter.CoverageEventID != statsBefore.CoverageEventID || metadataAfter.CoverageEventID != metadataBefore.CoverageEventID {
		t.Fatalf("idempotent coverage changed stats:%#v metadata:%#v", statsAfter, metadataAfter)
	}
}

func TestUsageMonitoringStatsRevisionMismatchUsesEventProjectionUntilRebuilt(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	baseMS := int64(1_800_057_600_000)
	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{"gpt-a": {Prompt: 1}}); err != nil {
		t.Fatalf("save initial prices: %v", err)
	}
	if _, err := db.InsertEvents(ctx, []usage.Event{
		monitoringRepositoryEvent("revision", baseMS+testDayMS, "gpt-a", "key-a", "alice@example.com", "auth-a", "source-a", false, 150, 10, 10),
	}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"gpt-a": {Prompt: 1, ContextTiers: []store.ModelPriceContextTier{{ThresholdTokens: 100, Prompt: 2, PromptConfigured: true}}},
	}); err != nil {
		t.Fatalf("change price structure: %v", err)
	}
	filter := store.AnalyticsFilter{FromMS: baseMS, ToMS: baseMS + 3*testDayMS, IncludeFailed: true}
	assertMonitoringReadersMatchRaw(t, ctx, db, filter)
	catchUpMonitoringRepository(t, ctx, db)
	assertMonitoringReadersMatchRaw(t, ctx, db, filter)
}

func TestUsageMonitoringStatsFailureDoesNotAdvanceCheckpoint(t *testing.T) {
	sqlDB, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	baseMS := int64(1_800_057_600_000)
	if _, err := db.InsertEvents(ctx, []usage.Event{
		monitoringRepositoryEvent("before-failure", baseMS+testDayMS, "gpt-a", "key-a", "alice@example.com", "auth-a", "source-a", false, 10, 5, 10),
	}); err != nil {
		t.Fatalf("insert initial event: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)
	before, err := db.UsageMonitoringState(ctx, monitoringrepo.StatsRollupName)
	if err != nil {
		t.Fatalf("state before failure: %v", err)
	}
	if _, err := db.InsertEvents(ctx, []usage.Event{
		monitoringRepositoryEvent("during-failure", baseMS+2*testDayMS, "gpt-a", "key-a", "alice@example.com", "auth-a", "source-a", false, 20, 5, 10),
	}); err != nil {
		t.Fatalf("insert pending event: %v", err)
	}
	if _, err := sqlDB.Exec(`drop table usage_monitoring_api_key_daily_rollups_v1`); err != nil {
		t.Fatalf("drop api key rollup table: %v", err)
	}
	if _, err := db.CatchUpUsageMonitoringStats(ctx, 1000, time.Now().UnixMilli()); err == nil {
		t.Fatal("catch-up unexpectedly succeeded after dropping rollup table")
	}
	after, err := db.UsageMonitoringState(ctx, monitoringrepo.StatsRollupName)
	if err != nil {
		t.Fatalf("state after failure: %v", err)
	}
	if after.CoverageEventID != before.CoverageEventID || after.ProcessedEvents != before.ProcessedEvents {
		t.Fatalf("checkpoint advanced after failed batch: before=%#v after=%#v", before, after)
	}
}

func TestUsageMonitoringProjectionSearchMatchesRawAndPreservesWildcardFallback(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	baseMS := int64(1_800_057_600_000)
	first := monitoringRepositoryEvent(
		"beta",
		baseMS+testDayMS,
		"model-qx",
		"key-a",
		"alice@example.com",
		"auth-a",
		"source-a",
		false,
		10,
		5,
		10,
	)
	first.RequestID = "alpha"
	second := monitoringRepositoryEvent(
		"search-second",
		baseMS+testDayMS+1_000,
		"model-other",
		"key-b",
		"bob@example.com",
		"auth-b",
		"source-b",
		false,
		20,
		5,
		20,
	)
	if _, err := db.InsertEvents(ctx, []usage.Event{first, second}); err != nil {
		t.Fatalf("insert search events: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	for _, query := range []string{"q", "qx", "ALPHA", "alphabeta"} {
		t.Run("query-"+query, func(t *testing.T) {
			assertProjectionCoreReadersMatchRaw(t, ctx, db, store.AnalyticsFilter{
				FromMS:        baseMS,
				ToMS:          baseMS + 3*testDayMS,
				SearchQuery:   query,
				IncludeFailed: true,
			})
		})
	}

	for _, query := range []string{"%", "_", "alpha%beta", "alpha_beta"} {
		t.Run("wildcard-"+query, func(t *testing.T) {
			filter := store.AnalyticsFilter{
				FromMS:        baseMS,
				ToMS:          baseMS + 3*testDayMS,
				SearchQuery:   query,
				IncludeFailed: true,
			}
			if _, err := db.EventsCountWithFilter(ctx, filter); err != nil {
				t.Fatalf("raw wildcard count: %v", err)
			}
			if _, _, available, err := db.UsageMonitoringEventsCount(ctx, filter); err != nil {
				t.Fatalf("projection wildcard count: %v", err)
			} else if available {
				t.Fatal("projection accepted a LIKE wildcard search that requires raw per-field semantics")
			}
			if _, _, available, err := db.UsageMonitoringFilterOptions(ctx, filter); err != nil {
				t.Fatalf("projection wildcard filter options: %v", err)
			} else if available {
				t.Fatal("filter option projection accepted a LIKE wildcard search that requires raw per-field semantics")
			}
		})
	}
}

func TestUsageMonitoringCanonicalModelMatchesProjectionTailSelectorsSearchAndPricing(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	baseMS := int64(1_800_057_600_000)
	if err := db.SaveModelPrices(ctx, map[string]store.ModelPrice{
		"resolved-model":         {Prompt: 1},
		"deepseek-v4-flash":      {Prompt: 2},
		"deepseek-v4-flash(max)": {Prompt: 3},
		"deepseek-v4-flash(low)": {Prompt: 4},
	}); err != nil {
		t.Fatalf("save model prices: %v", err)
	}

	projected := monitoringRepositoryEvent(
		"canonical-projected",
		baseMS+1_000,
		"stored-display-model",
		"key-a",
		"alice@example.com",
		"auth-a",
		"source-a",
		false,
		10,
		5,
		10,
	)
	projected.ResolvedModel = ""
	projected.RequestedModel = "deepseek-v4-flash(low)"
	unknown := monitoringRepositoryEvent(
		"canonical-unknown",
		baseMS+2_000,
		"deepseek-v4-flash(region-us)",
		"key-b",
		"bob@example.com",
		"auth-b",
		"source-b",
		false,
		10,
		5,
		10,
	)
	unknown.ResolvedModel = ""
	resolved := monitoringRepositoryEvent(
		"canonical-resolved",
		baseMS+3_000,
		"deepseek-v4-flash(max)",
		"key-c",
		"carol@example.com",
		"auth-c",
		"source-c",
		false,
		10,
		5,
		10,
	)
	resolved.ResolvedModel = "resolved-model"
	if _, err := db.InsertEvents(ctx, []usage.Event{projected, unknown, resolved}); err != nil {
		t.Fatalf("insert projected canonical events: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	tail := monitoringRepositoryEvent(
		"canonical-tail",
		baseMS+4_000,
		"deepseek-v4-flash(max)",
		"key-d",
		"dave@example.com",
		"auth-d",
		"source-d",
		false,
		10,
		5,
		10,
	)
	tail.ResolvedModel = ""
	if _, err := db.InsertEvents(ctx, []usage.Event{tail}); err != nil {
		t.Fatalf("insert canonical tail: %v", err)
	}

	filter := store.AnalyticsFilter{
		FromMS:        baseMS,
		ToMS:          baseMS + testDayMS,
		Models:        []string{"deepseek-v4-flash"},
		IncludeFailed: true,
	}
	assertMonitoringReadersMatchRaw(t, ctx, db, filter)
	assertProjectionCoreReadersMatchRaw(t, ctx, db, filter)
	suffixFilter := filter
	suffixFilter.Models = []string{"deepseek-v4-flash(max)"}
	assertMonitoringReadersMatchRaw(t, ctx, db, suffixFilter)
	assertProjectionCoreReadersMatchRaw(t, ctx, db, suffixFilter)
	suffixPage, _, available, err := db.UsageMonitoringEventsPage(ctx, suffixFilter, 0, 0, 10)
	if err != nil || !available || len(suffixPage.Items) != 3 {
		t.Fatalf("suffix model filter page: available=%v err=%v items=%#v", available, err, suffixPage.Items)
	}

	selectors, _, available, err := db.UsageMonitoringFilterSelectors(ctx, store.AnalyticsFilter{
		FromMS:        baseMS,
		ToMS:          baseMS + testDayMS,
		IncludeFailed: true,
	})
	if err != nil || !available {
		t.Fatalf("load canonical selectors: available=%v err=%v", available, err)
	}
	if !reflect.DeepEqual(selectors.Models, []string{"deepseek-v4-flash", "deepseek-v4-flash(region-us)"}) {
		t.Fatalf("canonical selector models = %#v", selectors.Models)
	}

	for _, query := range []string{"deepseek-v4-flash(max)", "deepseek-v4-flash"} {
		searchFilter := store.AnalyticsFilter{
			FromMS:        baseMS,
			ToMS:          baseMS + testDayMS,
			SearchQuery:   query,
			IncludeFailed: true,
		}
		assertProjectionCoreReadersMatchRaw(t, ctx, db, searchFilter)
	}

	models, _, available, err := db.UsageMonitoringModelStats(ctx, store.AnalyticsFilter{
		FromMS:        baseMS,
		ToMS:          baseMS + testDayMS,
		IncludeFailed: true,
	})
	if err != nil || !available {
		t.Fatalf("load canonical model stats: available=%v err=%v", available, err)
	}
	byBillingModel := make(map[string]store.ModelStat, len(models))
	for _, row := range models {
		byBillingModel[row.BillingModel] = row
	}
	if row := byBillingModel["resolved-model"]; row.Model != "deepseek-v4-flash" || row.PricingModel != "resolved-model" || row.Calls != 1 {
		t.Fatalf("resolved pricing row = %#v", row)
	}
	if row := byBillingModel["deepseek-v4-flash"]; row.Model != "deepseek-v4-flash" || row.PricingModel != "deepseek-v4-flash" || row.Calls != 2 {
		t.Fatalf("canonical pricing row = %#v", row)
	}
	if row := byBillingModel["deepseek-v4-flash(region-us)"]; row.Model != "deepseek-v4-flash(region-us)" || row.PricingModel != "deepseek-v4-flash(region-us)" || row.Calls != 1 {
		t.Fatalf("raw fallback pricing row = %#v", row)
	}

	page, _, available, err := db.UsageMonitoringEventsPage(ctx, filter, 0, 0, 10)
	if err != nil || !available {
		t.Fatalf("load canonical event page: available=%v err=%v", available, err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("canonical event page = %#v", page.Items)
	}
	for _, item := range page.Items {
		if item.AnalyticsModel != "deepseek-v4-flash" || item.Model == item.AnalyticsModel {
			t.Fatalf("event page model identities = raw:%q analytics:%q", item.Model, item.AnalyticsModel)
		}
		if item.EventHash == projected.EventHash {
			if item.RequestedModel != projected.RequestedModel {
				t.Fatalf("event page requested model = %q, want explicit value %q", item.RequestedModel, projected.RequestedModel)
			}
		} else if item.RequestedModel != item.Model {
			t.Fatalf("event page requested model = %q, want raw fallback %q", item.RequestedModel, item.Model)
		}
	}
}

func TestUsageMonitoringCacheStatusPrefersCanonicalBucketsAcrossProjectionAndTail(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	baseMS := int64(1_800_057_600_000)

	projectedHit := monitoringRepositoryEvent(
		"canonical-cache-projected", baseMS+1_000, "gpt-cache", "key-a",
		"alice@example.com", "auth-a", "source-a", false, 10, 5, 10,
	)
	projectedHit.CachedTokens = 0
	projectedHit.CacheReadTokens = 0
	projectedHit.CacheCreationTokens = 0
	projectedHit.AccountingVersion = usage.TokenAccountingSchemaVersion
	projectedHit.AccountingValid = true
	projectedHit.TokenBreakdown = usage.TokenBreakdown{
		SchemaVersion: usage.TokenAccountingSchemaVersion,
		Quality:       usage.TokenAccountingQualityComplete,
		TotalTokens:   15,
		Input: usage.TokenInputBreakdown{
			TotalTokens:     10,
			UncachedTokens:  5,
			CacheReadTokens: 5,
		},
		Output: usage.TokenOutputBreakdown{TotalTokens: 5, NonReasoningTokens: 5},
	}
	projectedMiss := monitoringRepositoryEvent(
		"canonical-cache-miss", baseMS+2_000, "gpt-cache", "key-b",
		"bob@example.com", "auth-b", "source-b", false, 10, 5, 10,
	)
	projectedMiss.CachedTokens = 999
	projectedMiss.CacheReadTokens = 999
	projectedMiss.CacheCreationTokens = 999
	projectedMiss.AccountingVersion = usage.TokenAccountingSchemaVersion
	projectedMiss.AccountingValid = true
	projectedMiss.TokenBreakdown = usage.TokenBreakdown{
		SchemaVersion: usage.TokenAccountingSchemaVersion,
		Quality:       usage.TokenAccountingQualityComplete,
		TotalTokens:   15,
		Input:         usage.TokenInputBreakdown{TotalTokens: 10, UncachedTokens: 10},
		Output:        usage.TokenOutputBreakdown{TotalTokens: 5, NonReasoningTokens: 5},
	}
	if _, err := db.InsertEvents(ctx, []usage.Event{projectedHit, projectedMiss}); err != nil {
		t.Fatalf("insert projected cache events: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	tailHit := projectedHit
	tailHit.EventHash = "canonical-cache-tail"
	tailHit.TimestampMS = baseMS + 3_000
	tailHit.Timestamp = time.UnixMilli(tailHit.TimestampMS).UTC().Format(time.RFC3339Nano)
	tailHit.CreatedAtMS = tailHit.TimestampMS
	tailHit.APIKeyHash = "key-c"
	tailHit.AuthIndex = "auth-c"
	tailHit.AuthFileSnapshot = "auth-c.json"
	if _, err := db.InsertEvents(ctx, []usage.Event{tailHit}); err != nil {
		t.Fatalf("insert cache tail: %v", err)
	}

	filter := store.AnalyticsFilter{
		FromMS:        baseMS,
		ToMS:          baseMS + testDayMS,
		IncludeFailed: true,
		CacheStatus:   "hit",
	}
	page, _, available, err := db.UsageMonitoringEventsPage(ctx, filter, 0, 0, 10)
	if err != nil || !available || len(page.Items) != 2 {
		t.Fatalf("canonical cache hit page: available=%v err=%v items=%#v", available, err, page.Items)
	}
	wantHashes := map[string]bool{"canonical-cache-projected": true, "canonical-cache-tail": true}
	for _, item := range page.Items {
		if !wantHashes[item.EventHash] {
			t.Fatalf("unexpected canonical cache hit item: %#v", item)
		}
	}
	assertMonitoringReadersMatchRaw(t, ctx, db, filter)
	assertProjectionCoreReadersMatchRaw(t, ctx, db, filter)

	filter.CacheStatus = "miss"
	page, _, available, err = db.UsageMonitoringEventsPage(ctx, filter, 0, 0, 10)
	if err != nil || !available || len(page.Items) != 1 || page.Items[0].EventHash != "canonical-cache-miss" {
		t.Fatalf("canonical cache miss page: available=%v err=%v items=%#v", available, err, page.Items)
	}
}

func TestUsageMonitoringEventPagesFailClosedAccountingValidity(t *testing.T) {
	sqlDB, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	baseMS := int64(1_800_057_600_000)
	events := []usage.Event{
		monitoringRepositoryEvent(
			"corrupt-canonical-validity", baseMS+1_000, "gpt-a", "key-a",
			"alice@example.com", "auth-a", "source-a", false, 10, 5, 10,
		),
		monitoringRepositoryEvent(
			"version-zero-validity", baseMS+2_000, "gpt-a", "key-b",
			"bob@example.com", "auth-b", "source-b", false, 10, 5, 10,
		),
	}
	if _, err := db.InsertEvents(ctx, events); err != nil {
		t.Fatalf("insert validity events: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `update usage_events set
		accounting_version = 2, accounting_valid = 1, accounting_quality = 'complete',
		normalized_uncached_input_tokens = 9, normalized_total_input_tokens = 10,
		normalized_cache_read_tokens = 0, normalized_cache_creation_tokens = 0,
		normalized_non_reasoning_output_tokens = 5, normalized_reasoning_output_tokens = 0,
		normalized_total_output_tokens = 5, unclassified_tokens = 0, total_tokens = 15
		where event_hash = 'corrupt-canonical-validity'`); err != nil {
		t.Fatalf("corrupt canonical event: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `update usage_events set
		accounting_version = 0, accounting_valid = 1, accounting_quality = 'complete',
		normalized_uncached_input_tokens = 10, normalized_total_input_tokens = 10,
		normalized_cache_read_tokens = 0, normalized_cache_creation_tokens = 0,
		normalized_non_reasoning_output_tokens = 5, normalized_reasoning_output_tokens = 0,
		normalized_total_output_tokens = 5, unclassified_tokens = 0, total_tokens = 15
		where event_hash = 'version-zero-validity'`); err != nil {
		t.Fatalf("set version zero validity event: %v", err)
	}

	catchUpMonitoringRepository(t, ctx, db)
	rows, err := sqlDB.QueryContext(ctx, `select e.event_hash, p.accounting_valid
		from usage_monitoring_event_projection_v1 p
		join usage_events e on e.id = p.event_id
		where e.event_hash in ('corrupt-canonical-validity', 'version-zero-validity')`)
	if err != nil {
		t.Fatalf("read projected validity: %v", err)
	}
	defer rows.Close()
	projected := map[string]int{}
	for rows.Next() {
		var eventHash string
		var valid int
		if err := rows.Scan(&eventHash, &valid); err != nil {
			t.Fatalf("scan projected validity: %v", err)
		}
		projected[eventHash] = valid
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read projected validity rows: %v", err)
	}
	if !reflect.DeepEqual(projected, map[string]int{
		"corrupt-canonical-validity": 0,
		"version-zero-validity":      0,
	}) {
		t.Fatalf("projected validity = %#v", projected)
	}

	filter := store.AnalyticsFilter{
		FromMS:        baseMS,
		ToMS:          baseMS + testDayMS,
		IncludeFailed: true,
	}
	rawPage, err := db.EventsPageWithFilter(ctx, filter, 0, 0, 10)
	if err != nil {
		t.Fatalf("load raw validity page: %v", err)
	}
	projectedPage, _, available, err := db.UsageMonitoringEventsPage(ctx, filter, 0, 0, 10)
	if err != nil || !available {
		t.Fatalf("load projected validity page: available=%v err=%v", available, err)
	}
	if !reflect.DeepEqual(projectedPage, rawPage) || len(projectedPage.Items) != 2 {
		t.Fatalf("validity pages mismatch\nprojection=%#v\nraw=%#v", projectedPage, rawPage)
	}
	for _, item := range projectedPage.Items {
		if item.AccountingValid {
			t.Fatalf("event %q exposed invalid accounting as valid: %#v", item.EventHash, item)
		}
	}
}

func TestUsageMonitoringProjectionTailKeysetMatchesRawWithOutOfOrderTimestamps(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	baseMS := int64(1_800_057_600_000)
	initialOffsets := []int64{5_000, 1_000, 5_000, 3_000}
	initial := make([]usage.Event, 0, len(initialOffsets))
	for index, offset := range initialOffsets {
		initial = append(initial, monitoringRepositoryEvent(
			fmt.Sprintf("initial-%d", index),
			baseMS+offset,
			"gpt-a",
			"key-a",
			"alice@example.com",
			"auth-a",
			"source-a",
			false,
			10,
			5,
			10,
		))
	}
	if _, err := db.InsertEvents(ctx, initial); err != nil {
		t.Fatalf("insert initial events: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	tailOffsets := []int64{2_000, 6_000, 5_000}
	tail := make([]usage.Event, 0, len(tailOffsets))
	for index, offset := range tailOffsets {
		tail = append(tail, monitoringRepositoryEvent(
			fmt.Sprintf("tail-%d", index),
			baseMS+offset,
			"gpt-a",
			"key-a",
			"alice@example.com",
			"auth-a",
			"source-a",
			false,
			10,
			5,
			10,
		))
	}
	if _, err := db.InsertEvents(ctx, tail); err != nil {
		t.Fatalf("insert tail events: %v", err)
	}

	filter := store.AnalyticsFilter{
		FromMS:        baseMS,
		ToMS:          baseMS + testDayMS,
		IncludeFailed: true,
	}
	assertProjectionPagesMatchRaw(t, ctx, db, filter, 2, len(initial)+len(tail))
	catchUpMonitoringRepository(t, ctx, db)
	assertProjectionPagesMatchRaw(t, ctx, db, filter, 2, len(initial)+len(tail))
}

func TestUsageMonitoringMetadataBackfillRefreshesHistoricalHeadersWithoutReset(t *testing.T) {
	sqlDB, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	baseMS := int64(1_800_057_600_000)
	if _, err := sqlDB.ExecContext(ctx, `insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, model, auth_index,
		source, source_hash, account_snapshot, auth_file_snapshot,
		auth_provider_snapshot, failed, header_quota_plan_type, raw_json,
		created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"metadata-backfill",
		baseMS+testDayMS,
		time.UnixMilli(baseMS+testDayMS).UTC().Format(time.RFC3339Nano),
		"codex",
		"gpt-a",
		"auth-a",
		"source-a",
		"hash-auth-a",
		"alice@example.com",
		"auth-a.json",
		"codex",
		0,
		"legacy",
		`{"response_headers":{"X-Codex-Plan-Type":["plus"],"X-OAI-Request-ID":["backfilled-trace"]}}`,
		baseMS+testDayMS,
	); err != nil {
		t.Fatalf("insert legacy event: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)
	items, _, available, err := db.UsageMonitoringHeaderSnapshots(ctx, baseMS, 10)
	if err != nil || !available {
		t.Fatalf("load initial headers: available=%v err=%v", available, err)
	}
	if len(items) != 1 || items[0].HeaderQuotaPlanType != "legacy" || items[0].HeaderTraceID != "" {
		t.Fatalf("initial headers = %#v", items)
	}
	stateBefore, err := db.UsageMonitoringState(ctx, monitoringrepo.MetadataRollupName)
	if err != nil {
		t.Fatalf("metadata state before backfill: %v", err)
	}
	updated, err := db.BackfillUsageResponseMetadata(ctx, 10)
	if err != nil || updated != 1 {
		t.Fatalf("backfill response metadata updated=%d err=%v", updated, err)
	}
	stateAfter, err := db.UsageMonitoringState(ctx, monitoringrepo.MetadataRollupName)
	if err != nil {
		t.Fatalf("metadata state after backfill: %v", err)
	}
	if stateAfter.CoverageEventID != stateBefore.CoverageEventID || stateAfter.ProcessedEvents != stateBefore.ProcessedEvents {
		t.Fatalf("metadata checkpoint changed during targeted refresh: before=%#v after=%#v", stateBefore, stateAfter)
	}
	items, _, available, err = db.UsageMonitoringHeaderSnapshots(ctx, baseMS, 10)
	if err != nil || !available {
		t.Fatalf("load rebuilt headers: available=%v err=%v", available, err)
	}
	if len(items) != 1 || items[0].HeaderTraceID != "backfilled-trace" || items[0].HeaderQuotaPlanType != "plus" {
		t.Fatalf("target-refreshed headers = %#v", items)
	}
	var searchText string
	if err := sqlDB.QueryRowContext(ctx, `select search_text from usage_monitoring_event_projection_v1 where event_id = ?`, items[0].ID).Scan(&searchText); err != nil {
		t.Fatalf("read refreshed event projection: %v", err)
	}
	if !strings.Contains(searchText, "backfilled-trace") {
		t.Fatalf("event projection search_text was not refreshed: %q", searchText)
	}
}

func TestUsageMonitoringMetadataBackfillRollsBackEventWhenProjectionRefreshFails(t *testing.T) {
	sqlDB, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	baseMS := int64(1_800_057_600_000)
	if _, err := sqlDB.ExecContext(ctx, `insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, model, auth_index,
		source, source_hash, account_snapshot, auth_file_snapshot,
		auth_provider_snapshot, failed, raw_json, created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"metadata-rollback",
		baseMS+testDayMS,
		time.UnixMilli(baseMS+testDayMS).UTC().Format(time.RFC3339Nano),
		"codex",
		"gpt-a",
		"auth-a",
		"source-a",
		"hash-auth-a",
		"alice@example.com",
		"auth-a.json",
		"codex",
		0,
		`{"response_headers":{"X-Codex-Plan-Type":["plus"],"X-OAI-Request-ID":["rollback-trace"]}}`,
		baseMS+testDayMS,
	); err != nil {
		t.Fatalf("insert legacy event: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `drop table usage_monitoring_event_projection_v1`); err != nil {
		t.Fatalf("drop event projection: %v", err)
	}
	updated, err := db.BackfillUsageResponseMetadata(ctx, 10)
	if err == nil || updated != 0 {
		t.Fatalf("backfill after projection failure updated=%d err=%v", updated, err)
	}
	var metadata sql.NullString
	var planType sql.NullString
	var traceID sql.NullString
	if err := sqlDB.QueryRowContext(ctx, `select response_metadata_json,
		header_quota_plan_type, header_trace_id from usage_events
		where event_hash = 'metadata-rollback'`).Scan(&metadata, &planType, &traceID); err != nil {
		t.Fatalf("read rolled back event: %v", err)
	}
	if metadata.Valid || planType.Valid || traceID.Valid {
		t.Fatalf("usage event was partially updated: metadata=%#v plan=%#v trace=%#v", metadata, planType, traceID)
	}
}

func TestUsageMonitoringMetadataBackfillOlderEventDoesNotReplaceLatestHeader(t *testing.T) {
	sqlDB, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	baseMS := int64(1_800_057_600_000)
	olderMS := baseMS + testDayMS
	if _, err := sqlDB.ExecContext(ctx, `insert into usage_events (
		event_hash, timestamp_ms, timestamp, provider, model, auth_index,
		source, source_hash, account_snapshot, auth_file_snapshot,
		auth_provider_snapshot, failed, header_quota_plan_type, raw_json,
		created_at_ms
	) values (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"metadata-older",
		olderMS,
		time.UnixMilli(olderMS).UTC().Format(time.RFC3339Nano),
		"codex",
		"gpt-a",
		"auth-a",
		"source-a",
		"hash-auth-a",
		"alice@example.com",
		"auth-a.json",
		"codex",
		0,
		"legacy",
		`{"response_headers":{"X-Codex-Plan-Type":["plus"],"X-OAI-Request-ID":["older-backfilled-trace"]}}`,
		olderMS,
	); err != nil {
		t.Fatalf("insert older legacy event: %v", err)
	}
	newer := monitoringRepositoryEvent(
		"metadata-newer",
		olderMS+10_000,
		"gpt-a",
		"key-a",
		"alice@example.com",
		"auth-a",
		"source-a",
		false,
		10,
		5,
		10,
	)
	newer.HeaderQuotaPlanType = "team"
	newer.HeaderTraceID = "newer-trace"
	if _, err := db.InsertEvents(ctx, []usage.Event{newer}); err != nil {
		t.Fatalf("insert newer event: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	updated, err := db.BackfillUsageResponseMetadata(ctx, 10)
	if err != nil || updated != 1 {
		t.Fatalf("backfill older metadata updated=%d err=%v", updated, err)
	}
	items, _, available, err := db.UsageMonitoringHeaderSnapshots(ctx, baseMS, 10)
	if err != nil || !available {
		t.Fatalf("load headers after older backfill: available=%v err=%v", available, err)
	}
	if len(items) != 1 || items[0].EventHash != "metadata-newer" ||
		items[0].HeaderQuotaPlanType != "team" || items[0].HeaderTraceID != "newer-trace" {
		t.Fatalf("older backfill replaced latest header: %#v", items)
	}
}

func assertMonitoringReadersMatchRaw(t *testing.T, ctx context.Context, db *store.Store, filter store.AnalyticsFilter) {
	t.Helper()
	rawAccounts, err := db.AccountModelStatsWithFilter(ctx, filter)
	if err != nil {
		t.Fatalf("raw account stats: %v", err)
	}
	rolledAccounts, _, available, err := db.UsageMonitoringAccountStats(ctx, filter)
	if err != nil {
		t.Fatalf("rollup account stats: %v", err)
	}
	if !available {
		t.Fatal("rollup account stats unavailable")
	}
	if !reflect.DeepEqual(rolledAccounts, rawAccounts) {
		t.Fatalf("account stats mismatch\nrollup=%#v\nraw=%#v", rolledAccounts, rawAccounts)
	}

	rawAPIKeys, err := db.APIKeyModelStatsWithFilter(ctx, filter)
	if err != nil {
		t.Fatalf("raw api key stats: %v", err)
	}
	rolledAPIKeys, _, available, err := db.UsageMonitoringAPIKeyStats(ctx, filter)
	if err != nil {
		t.Fatalf("rollup api key stats: %v", err)
	}
	if !available {
		t.Fatal("rollup api key stats unavailable")
	}
	if !reflect.DeepEqual(rolledAPIKeys, rawAPIKeys) {
		t.Fatalf("api key stats mismatch\nrollup=%#v\nraw=%#v", rolledAPIKeys, rawAPIKeys)
	}

	selectorFilter := store.AnalyticsFilter{FromMS: filter.FromMS, ToMS: filter.ToMS, IncludeFailed: true}
	rawSelectors, err := db.FilterSelectorValuesWithFilter(ctx, selectorFilter)
	if err != nil {
		t.Fatalf("raw selectors: %v", err)
	}
	rolledSelectors, _, available, err := db.UsageMonitoringFilterSelectors(ctx, selectorFilter)
	if err != nil {
		t.Fatalf("rollup selectors: %v", err)
	}
	if !available {
		t.Fatal("rollup selectors unavailable")
	}
	if !reflect.DeepEqual(rolledSelectors, rawSelectors) {
		t.Fatalf("selector mismatch\nrollup=%#v\nraw=%#v", rolledSelectors, rawSelectors)
	}
}

func assertProjectionCoreReadersMatchRaw(t *testing.T, ctx context.Context, db *store.Store, filter store.AnalyticsFilter) {
	t.Helper()
	rawAggregate, err := db.AggregateWithFilter(ctx, filter)
	if err != nil {
		t.Fatalf("raw aggregate: %v", err)
	}
	projectedAggregate, _, available, err := db.UsageMonitoringAggregate(ctx, filter)
	if err != nil {
		t.Fatalf("projected aggregate: %v", err)
	}
	if !available || !reflect.DeepEqual(projectedAggregate, rawAggregate) {
		t.Fatalf("aggregate mismatch available=%v\nprojection=%#v\nraw=%#v", available, projectedAggregate, rawAggregate)
	}

	rawModels, err := db.ModelStatsWithFilter(ctx, filter, 0)
	if err != nil {
		t.Fatalf("raw model stats: %v", err)
	}
	projectedModels, _, available, err := db.UsageMonitoringModelStats(ctx, filter)
	if err != nil {
		t.Fatalf("projected model stats: %v", err)
	}
	if !available || !reflect.DeepEqual(projectedModels, rawModels) {
		t.Fatalf("model stats mismatch available=%v\nprojection=%#v\nraw=%#v", available, projectedModels, rawModels)
	}

	rawCount, err := db.EventsCountWithFilter(ctx, filter)
	if err != nil {
		t.Fatalf("raw events count: %v", err)
	}
	projectedCount, _, available, err := db.UsageMonitoringEventsCount(ctx, filter)
	if err != nil {
		t.Fatalf("projected events count: %v", err)
	}
	if !available || projectedCount != rawCount {
		t.Fatalf("events count mismatch available=%v projection=%d raw=%d", available, projectedCount, rawCount)
	}

	rawPage, err := db.EventsPageWithFilter(ctx, filter, 0, 0, 3)
	if err != nil {
		t.Fatalf("raw events page: %v", err)
	}
	projectedPage, _, available, err := db.UsageMonitoringEventsPage(ctx, filter, 0, 0, 3)
	if err != nil {
		t.Fatalf("projected events page: %v", err)
	}
	if !available || !reflect.DeepEqual(projectedPage, rawPage) {
		t.Fatalf("events page mismatch available=%v\nprojection=%#v\nraw=%#v", available, projectedPage, rawPage)
	}

	rawFilterOptions, err := db.FilterOptionValuesWithFilter(ctx, filter)
	if err != nil {
		t.Fatalf("raw filter options: %v", err)
	}
	projectedFilterOptions, _, available, err := db.UsageMonitoringFilterOptions(ctx, filter)
	if err != nil {
		t.Fatalf("projected filter options: %v", err)
	}
	if !available || !reflect.DeepEqual(projectedFilterOptions, rawFilterOptions) {
		t.Fatalf("filter options mismatch available=%v\nprojection=%#v\nraw=%#v", available, projectedFilterOptions, rawFilterOptions)
	}
}

func assertProjectionPagesMatchRaw(
	t *testing.T,
	ctx context.Context,
	db *store.Store,
	filter store.AnalyticsFilter,
	limit int,
	wantItems int,
) {
	t.Helper()
	beforeMS := int64(0)
	beforeID := int64(0)
	seen := map[int64]struct{}{}
	for pageIndex := 0; ; pageIndex++ {
		rawPage, err := db.EventsPageWithFilter(ctx, filter, beforeMS, beforeID, limit)
		if err != nil {
			t.Fatalf("raw events page %d: %v", pageIndex, err)
		}
		projectedPage, _, available, err := db.UsageMonitoringEventsPage(ctx, filter, beforeMS, beforeID, limit)
		if err != nil {
			t.Fatalf("projected events page %d: %v", pageIndex, err)
		}
		if !available || !reflect.DeepEqual(projectedPage, rawPage) {
			t.Fatalf("events page %d mismatch available=%v\nprojection=%#v\nraw=%#v", pageIndex, available, projectedPage, rawPage)
		}
		for _, item := range projectedPage.Items {
			if _, ok := seen[item.ID]; ok {
				t.Fatalf("event %d appeared on multiple keyset pages", item.ID)
			}
			seen[item.ID] = struct{}{}
		}
		if !projectedPage.HasMore {
			break
		}
		if projectedPage.NextBeforeMS <= 0 || projectedPage.NextBeforeID <= 0 {
			t.Fatalf("page %d has_more without a complete cursor: %#v", pageIndex, projectedPage)
		}
		beforeMS = projectedPage.NextBeforeMS
		beforeID = projectedPage.NextBeforeID
		if pageIndex > wantItems {
			t.Fatal("keyset pagination did not terminate")
		}
	}
	if len(seen) != wantItems {
		t.Fatalf("keyset pagination returned %d unique items, want %d", len(seen), wantItems)
	}
}

func assertHeaderReadersMatchRaw(t *testing.T, ctx context.Context, db *store.Store, sinceMS int64, limit int) {
	t.Helper()
	raw, err := db.LatestHeaderSnapshots(ctx, sinceMS, limit)
	if err != nil {
		t.Fatalf("raw header snapshots: %v", err)
	}
	rolled, _, available, err := db.UsageMonitoringHeaderSnapshots(ctx, sinceMS, limit)
	if err != nil {
		t.Fatalf("rollup header snapshots: %v", err)
	}
	if !available {
		t.Fatal("rollup header snapshots unavailable")
	}
	if !reflect.DeepEqual(rolled, raw) {
		t.Fatalf("header snapshot mismatch\nrollup=%#v\nraw=%#v", rolled, raw)
	}
}

func assertSearchIndexCount(t *testing.T, ctx context.Context, sqlDB *sql.DB, query string, want int) {
	t.Helper()
	var count int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from usage_monitoring_event_search_v1 where search_text like ?`, "%"+query+"%").Scan(&count); err != nil {
		t.Fatalf("count search index query %q: %v", query, err)
	}
	if count != want {
		t.Fatalf("search index query %q count = %d, want %d", query, count, want)
	}
}

func catchUpMonitoringRepository(t *testing.T, ctx context.Context, db *store.Store) {
	t.Helper()
	for _, catchUp := range []func(context.Context, int, int64) (store.UsageMonitoringCatchUpResult, error){
		db.CatchUpUsageMonitoringProjection,
		db.CatchUpUsageMonitoringMetadata,
		db.CatchUpUsageMonitoringStats,
	} {
		for {
			result, err := catchUp(ctx, 2, time.Now().UnixMilli())
			if err != nil {
				t.Fatalf("catch up monitoring repository: %v", err)
			}
			if !result.Pending {
				break
			}
		}
	}
}

func monitoringRepositoryEvent(
	hash string,
	timestampMS int64,
	modelID string,
	apiKeyHash string,
	account string,
	authIndex string,
	source string,
	failed bool,
	inputTokens int64,
	outputTokens int64,
	latencyMS int64,
) usage.Event {
	usedPercent := 42.5
	latency := latencyMS
	event := usage.Event{
		EventHash:              hash,
		TimestampMS:            timestampMS,
		Timestamp:              time.UnixMilli(timestampMS).UTC().Format(time.RFC3339Nano),
		Provider:               "codex",
		ExecutorType:           "codex",
		Model:                  modelID,
		ResolvedModel:          modelID,
		Endpoint:               "POST /v1/responses",
		Method:                 "POST",
		Path:                   "/v1/responses",
		AuthIndex:              authIndex,
		Source:                 source,
		SourceHash:             "hash-" + authIndex,
		APIKeyHash:             apiKeyHash,
		AccountSnapshot:        account,
		AuthLabelSnapshot:      "label-" + account,
		AuthFileSnapshot:       authIndex + ".json",
		AuthProviderSnapshot:   "codex",
		AuthProjectIDSnapshot:  "project-a",
		ServiceTier:            "default",
		InputTokens:            inputTokens,
		OutputTokens:           outputTokens,
		CachedTokens:           inputTokens / 10,
		CacheReadTokens:        inputTokens / 20,
		CacheCreationTokens:    inputTokens / 25,
		TotalTokens:            inputTokens + outputTokens,
		LatencyMS:              &latency,
		Failed:                 failed,
		HeaderQuotaUsedPercent: &usedPercent,
		HeaderQuotaPlanType:    "pro",
		HeaderTraceID:          "trace-" + hash,
		CreatedAtMS:            timestampMS,
	}
	if latencyMS == 0 {
		event.LatencyMS = nil
	}
	return event
}

func newMonitoringRepositoryStore(t *testing.T) (*sql.DB, *store.Store) {
	t.Helper()
	sqlDB, db := openMonitoringRepositoryStore(t, filepath.Join(t.TempDir(), "usage.sqlite"))
	t.Cleanup(func() { _ = sqlDB.Close() })
	return sqlDB, db
}

func openMonitoringRepositoryStore(t *testing.T, path string) (*sql.DB, *store.Store) {
	t.Helper()
	sqlDB, err := sqliterepo.Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return sqlDB, store.New(sqlDB)
}

func TestUsageMonitoringUnknownFailureName(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	err := db.RecordUsageMonitoringFailure(context.Background(), "unknown", errors.New(fmt.Sprint("boom")), time.Now().UnixMilli())
	if err == nil {
		t.Fatal("unknown rollup name unexpectedly accepted")
	}
}
