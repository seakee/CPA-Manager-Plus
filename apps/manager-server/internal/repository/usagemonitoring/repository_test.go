package usagemonitoring_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/codexquota"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	monitoringrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagemonitoring"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usage"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

const testDayMS = int64(24 * time.Hour / time.Millisecond)

func TestMigrationCreatesUsageMonitoringRollupSchema(t *testing.T) {
	sqlDB, db := newMonitoringRepositoryStore(t)
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
	var accountWindowIndexes int
	if err := sqlDB.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = 'idx_usage_monitoring_event_projection_account_window'`).Scan(&accountWindowIndexes); err != nil {
		t.Fatalf("inspect projection account window index: %v", err)
	}
	if accountWindowIndexes != 0 {
		t.Fatalf("projection account window indexes before post-listen maintenance = %d, want 0", accountWindowIndexes)
	}
	var modelIndexes int
	if err := sqlDB.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = 'idx_usage_monitoring_event_projection_model_timestamp'`).Scan(&modelIndexes); err != nil {
		t.Fatalf("inspect projection analytics model index: %v", err)
	}
	if modelIndexes != 0 {
		t.Fatalf("projection analytics model indexes before post-listen maintenance = %d, want 0", modelIndexes)
	}
	if err := db.RunDerivedStartupMaintenance(context.Background()); err != nil {
		t.Fatalf("run post-listen startup maintenance: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for accountWindowIndexes != 1 || modelIndexes != 1 {
		if time.Now().After(deadline) {
			t.Fatalf("post-listen indexes not ready: account=%d model=%d", accountWindowIndexes, modelIndexes)
		}
		time.Sleep(10 * time.Millisecond)
		if err := sqlDB.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = 'idx_usage_monitoring_event_projection_account_window'`).Scan(&accountWindowIndexes); err != nil {
			t.Fatalf("inspect post-listen account window index: %v", err)
		}
		if err := sqlDB.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = 'idx_usage_monitoring_event_projection_model_timestamp'`).Scan(&modelIndexes); err != nil {
			t.Fatalf("inspect post-listen analytics model index: %v", err)
		}
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

func TestRequestMonitoringProjectionTimestampIndexAvoidsTemporaryOrderBy(t *testing.T) {
	sqlDB, db := newMonitoringRepositoryStore(t)
	if err := db.RunDerivedStartupMaintenance(context.Background()); err != nil {
		t.Fatalf("prepare monitoring projection indexes: %v", err)
	}
	if _, err := sqlDB.Exec(`with recursive ids(event_id) as (
		select 1
		union all
		select event_id + 1 from ids where event_id < 100000
	) insert into usage_monitoring_event_projection_v1 (
		event_id, timestamp_ms, search_text, account_key, provider, executor_type,
		model, analytics_model, resolved_model, auth_index, source, source_hash,
		api_key_hash, account_snapshot, auth_label_snapshot, auth_file_snapshot,
		auth_provider_snapshot, auth_project_id_snapshot, reasoning_effort,
		service_tier, failed, latency_ms, input_tokens, output_tokens,
		reasoning_tokens, cached_tokens, cache_tokens, cache_read_tokens,
		cache_creation_tokens, normalized_total_input_tokens, total_tokens,
		header_quota_plan_type, header_error_kind, header_error_code,
		header_trace_id, updated_at_ms
	) select
		event_id, 1800000000000 + event_id, '', 'account', 'provider', 'executor',
		'model', 'model', 'model', 'auth', 'source', 'source-hash',
		'api-key', 'account', 'auth-label', 'auth-file', 'provider', 'project',
		'', '', 0, null, 1, 1, 0, 0, 0, 0, 0, 1, 2, '', '', '', '',
		1800000000000 + event_id
	from ids`); err != nil {
		t.Fatalf("seed 100k monitoring projection rows: %v", err)
	}

	query := `explain query plan with filtered_events as (
		select p.event_id as id, p.timestamp_ms
		from usage_monitoring_event_projection_v1 p
		where p.timestamp_ms >= ? and p.timestamp_ms < ?
	) select id, timestamp_ms
	from filtered_events
	order by timestamp_ms desc, id desc
	limit ?`
	withIndex := explainMonitoringPlan(t, sqlDB, query, int64(1800000000000), int64(1800000200000), 100)
	withIndexText := strings.Join(withIndex, "\n")
	if !strings.Contains(withIndexText, "idx_usage_monitoring_event_projection_timestamp") {
		t.Fatalf("request monitoring plan with index = %v", withIndex)
	}
	if strings.Contains(withIndexText, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("request monitoring plan with index uses temporary order by: %v", withIndex)
	}

	if _, err := sqlDB.Exec(`drop index idx_usage_monitoring_event_projection_timestamp`); err != nil {
		t.Fatalf("drop monitoring projection timestamp index: %v", err)
	}
	withoutIndex := explainMonitoringPlan(t, sqlDB, query, int64(1800000000000), int64(1800000200000), 100)
	withoutIndexText := strings.Join(withoutIndex, "\n")
	if strings.Contains(withoutIndexText, "idx_usage_monitoring_event_projection_timestamp") {
		t.Fatalf("request monitoring plan without index still references timestamp index: %v", withoutIndex)
	}
	if !strings.Contains(withoutIndexText, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("request monitoring plan without index = %v, want temporary order by", withoutIndex)
	}
}

func TestUsageMonitoringProjectionCatchUpResumesAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	sqlDB, db := openMonitoringRepositoryStore(t, path)
	ctx := context.Background()
	baseMS := int64(1_800_000_000_000)
	for index := 1; index <= 3; index++ {
		event := monitoringRepositoryEvent(
			fmt.Sprintf("resume-%d", index),
			baseMS+int64(index),
			"model-a",
			"key-a",
			"account-a",
			"auth-a",
			"source-a",
			false,
			1,
			1,
			1,
		)
		if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
			_ = sqlDB.Close()
			t.Fatalf("insert resumable projection event %d: %v", index, err)
		}
	}
	first, err := db.CatchUpUsageMonitoringProjection(ctx, 1, baseMS+100)
	if err != nil {
		_ = sqlDB.Close()
		t.Fatalf("run first projection batch: %v", err)
	}
	if first.CoverageEventID != 1 || !first.Pending {
		_ = sqlDB.Close()
		t.Fatalf("first projection batch = %#v", first)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close partial projection database: %v", err)
	}

	sqlDB, db = openMonitoringRepositoryStore(t, path)
	t.Cleanup(func() { _ = sqlDB.Close() })
	state, err := db.UsageMonitoringState(ctx, monitoringrepo.ProjectionRollupName)
	if err != nil {
		t.Fatalf("read resumed projection state: %v", err)
	}
	if state.CoverageEventID != 1 {
		t.Fatalf("projection coverage after restart = %d, want 1", state.CoverageEventID)
	}
	second, err := db.CatchUpUsageMonitoringProjection(ctx, 1, baseMS+200)
	if err != nil {
		t.Fatalf("run resumed projection batch: %v", err)
	}
	if second.CoverageEventID != 2 || !second.Pending || second.Rebuilt {
		t.Fatalf("resumed projection batch = %#v", second)
	}
}

func TestUsageMonitoringProjectionPreservesRebuildStateAcrossFailure(t *testing.T) {
	sqlDB, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	baseMS := int64(1_800_000_000_000)
	if _, err := db.InsertEvents(ctx, []usage.Event{
		monitoringRepositoryEvent("projection-rebuild-resume-1", baseMS+1_000, "model-a", "key-a", "account-a", "auth-a", "source-a", false, 10, 5, 1),
		monitoringRepositoryEvent("projection-rebuild-resume-2", baseMS+2_000, "model-a", "key-a", "account-a", "auth-a", "source-a", false, 20, 7, 1),
	}); err != nil {
		t.Fatalf("insert projection rebuild events: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `delete from usage_monitoring_event_projection_v1`); err != nil {
		t.Fatalf("clear projection rows: %v", err)
	}
	if _, err := sqlDB.ExecContext(ctx, `update usage_monitoring_rollup_state set
		status = 'pending', backfill_last_event_id = 0, coverage_event_id = 0,
		target_event_id = 2, processed_events = 0, last_error = null
		where rollup_name = ?`, monitoringrepo.ProjectionRollupName); err != nil {
		t.Fatalf("schedule projection rebuild: %v", err)
	}
	filter := store.AnalyticsFilter{
		FromMS:        baseMS,
		ToMS:          baseMS + 10_000,
		IncludeFailed: true,
	}

	if err := db.RecordUsageMonitoringFailure(ctx, monitoringrepo.ProjectionRollupName, errors.New("interrupted pending projection rebuild"), baseMS+20_000); err != nil {
		t.Fatalf("record pending projection rebuild failure: %v", err)
	}
	state, err := db.UsageMonitoringState(ctx, monitoringrepo.ProjectionRollupName)
	if err != nil {
		t.Fatalf("read pending projection rebuild state: %v", err)
	}
	if state.Status != "pending" || state.LastError != "interrupted pending projection rebuild" {
		t.Fatalf("pending projection rebuild state = %#v", state)
	}
	assertProjectionCoreReadersMatchRaw(t, ctx, db, filter)

	first, err := db.CatchUpUsageMonitoringProjection(ctx, 1, baseMS+30_000)
	if err != nil {
		t.Fatalf("run partial projection rebuild: %v", err)
	}
	if !first.Rebuilt || !first.Pending || first.CoverageEventID != 1 {
		t.Fatalf("partial projection rebuild = %#v", first)
	}
	if err := db.RecordUsageMonitoringFailure(ctx, monitoringrepo.ProjectionRollupName, errors.New("interrupted active projection rebuild"), baseMS+40_000); err != nil {
		t.Fatalf("record active projection rebuild failure: %v", err)
	}
	state, err = db.UsageMonitoringState(ctx, monitoringrepo.ProjectionRollupName)
	if err != nil {
		t.Fatalf("read active projection rebuild state: %v", err)
	}
	if state.Status != "rebuilding" || state.LastError != "interrupted active projection rebuild" {
		t.Fatalf("active projection rebuild state = %#v", state)
	}
	assertProjectionCoreReadersMatchRaw(t, ctx, db, filter)

	completed, err := db.CatchUpUsageMonitoringProjection(ctx, 10, baseMS+50_000)
	if err != nil {
		t.Fatalf("resume projection rebuild: %v", err)
	}
	if !completed.Rebuilt || completed.Pending || completed.CoverageEventID != 2 {
		t.Fatalf("resumed projection rebuild = %#v", completed)
	}
	assertProjectionCoreReadersMatchRaw(t, ctx, db, filter)
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
	otherCredential.AuthProjectIDSnapshot = "project-b"
	toBoundary := monitoringRepositoryEvent("window-to-boundary", toMS, "gpt-window", "key-a", "shared@example.com", "auth-shared", "source-a", false, 8_000, 8_000, 10)
	toBoundary.AuthFileSnapshot = "first.json"
	if _, err := db.InsertEvents(ctx, []usage.Event{first, otherCredential, toBoundary}); err != nil {
		t.Fatalf("insert projected events: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)
	expectedAccountKey, valid := usageidentity.AccountKey(usageidentity.Fields{
		AuthFileSnapshot:      "first.json",
		AuthIndex:             "auth-shared",
		AuthProviderSnapshot:  "codex",
		AuthProjectIDSnapshot: "project-a",
		AccountSnapshot:       "shared@example.com",
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

func TestCodexAccountWindowKeepsHistoryAcrossSameAccountReauth(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	dayStartMS := int64(1_800_057_600_000)
	previousFromMS := dayStartMS
	currentFromMS := dayStartMS + testDayMS
	toMS := currentFromMS + testDayMS

	makeEvent := func(hash string, timestampMS int64, file, authIndex, accountID string, input int64) usage.Event {
		event := monitoringRepositoryEvent(
			hash,
			timestampMS,
			"gpt-window",
			"key-a",
			"same@example.com",
			authIndex,
			file,
			false,
			input,
			input/10,
			10,
		)
		event.AuthFileSnapshot = file
		event.AuthIndex = authIndex
		event.AuthAccountIDSnapshot = accountID
		return event
	}

	projectedEvents := []usage.Event{
		makeEvent("previous-old-credential", previousFromMS+1_000, "codex-a-free.json", "auth-1", "account-a", 10),
		makeEvent("previous-new-credential-before-workspace", previousFromMS+2_000, "codex-a-pro.json", "auth-2", "", 20),
		makeEvent("current-old-credential", currentFromMS+1_000, "codex-a-free.json", "auth-1", "account-a", 30),
		makeEvent("different-space-same-email", currentFromMS+2_000, "codex-b.json", "auth-3", "account-b", 9_000),
	}
	legacyCurrentCredential := makeEvent("current-new-credential-before-account-snapshot", currentFromMS+2_500, "codex-a-pro.json", "auth-2", "", 5)
	legacyCurrentCredential.AuthAccountIDSnapshot = "account-a"
	projectedEvents = append(projectedEvents, legacyCurrentCredential)
	if _, err := db.InsertEvents(ctx, projectedEvents); err != nil {
		t.Fatalf("insert projected reauth events: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	windows := []store.AccountWindowUsageQuery{
		{
			RequestIndex:          0,
			FromMS:                currentFromMS,
			ToMS:                  toMS,
			AccountSnapshot:       "same@example.com",
			AuthFileSnapshot:      "codex-a-pro.json",
			AuthProviderSnapshot:  "codex",
			AuthAccountIDSnapshot: "account-a",
			AuthIndex:             "auth-2",
			Source:                "codex-a-pro.json",
		},
		{
			RequestIndex:          1,
			FromMS:                previousFromMS,
			ToMS:                  currentFromMS,
			AccountSnapshot:       "same@example.com",
			AuthFileSnapshot:      "codex-a-pro.json",
			AuthProviderSnapshot:  "codex",
			AuthAccountIDSnapshot: "account-a",
			AuthIndex:             "auth-2",
			Source:                "codex-a-pro.json",
		},
	}
	assertStats := func(phase string, currentCalls, currentInput int64) {
		t.Helper()
		raw, err := db.AccountWindowModelStats(ctx, windows)
		if err != nil {
			t.Fatalf("%s raw window stats: %v", phase, err)
		}
		projected, _, available, err := db.UsageMonitoringAccountWindowStats(ctx, windows)
		if err != nil || !available {
			t.Fatalf("%s projected window stats: available=%v err=%v", phase, available, err)
		}
		if !reflect.DeepEqual(projected, raw) {
			t.Fatalf("%s projection/raw mismatch\nprojection=%#v\nraw=%#v", phase, projected, raw)
		}
		if len(projected) != 2 {
			t.Fatalf("%s stats = %#v, want current and previous", phase, projected)
		}
		if projected[0].RequestIndex != 0 || projected[0].Calls != currentCalls || projected[0].InputTokens != currentInput {
			t.Fatalf("%s current stats = %#v", phase, projected[0])
		}
		if projected[1].RequestIndex != 1 || projected[1].Calls != 2 || projected[1].InputTokens != 30 {
			t.Fatalf("%s previous stats = %#v", phase, projected[1])
		}
	}

	assertStats("projection complete with daily rollup available", 2, 35)
	rawTail := makeEvent("current-new-credential-tail", currentFromMS+3_000, "codex-a-pro.json", "auth-2", "account-a", 40)
	if _, err := db.InsertEvents(ctx, []usage.Event{rawTail}); err != nil {
		t.Fatalf("insert raw reauth tail: %v", err)
	}
	assertStats("projection plus raw tail", 3, 75)
}

func TestCodexAccountWindowSeparatesMembersSharingWorkspace(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	fromMS := int64(1_800_057_600_000)
	toMS := fromMS + testDayMS

	makeEvent := func(hash, member, file, authIndex string, input int64) usage.Event {
		event := monitoringRepositoryEvent(hash, fromMS+input, "gpt-team-window", "key-"+member, member, authIndex, file, false, input, 10, 1)
		event.AuthFileSnapshot = file
		event.AuthIndex = authIndex
		event.AuthAccountIDSnapshot = "workspace-team"
		return event
	}
	if _, err := db.InsertEvents(ctx, []usage.Event{
		makeEvent("team-alice", "alice@example.com", "alice.json", "auth-alice", 100),
		makeEvent("team-bob", "bob@example.com", "bob.json", "auth-bob", 200),
	}); err != nil {
		t.Fatalf("insert shared-workspace events: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	windows := []store.AccountWindowUsageQuery{
		{
			RequestIndex:          0,
			FromMS:                fromMS,
			ToMS:                  toMS,
			AccountSnapshot:       "alice@example.com",
			AuthFileSnapshot:      "alice.json",
			AuthProviderSnapshot:  "codex",
			AuthAccountIDSnapshot: "workspace-team",
			AuthIndex:             "auth-alice",
			Source:                "alice.json",
			AccountKey:            "usage-account-history:3:codex-account:636F646578:776F726B73706163652D31",
		},
		{
			RequestIndex:          1,
			FromMS:                fromMS,
			ToMS:                  toMS,
			AccountSnapshot:       "bob@example.com",
			AuthFileSnapshot:      "bob.json",
			AuthProviderSnapshot:  "codex",
			AuthAccountIDSnapshot: "workspace-team",
			AuthIndex:             "auth-bob",
			Source:                "bob.json",
			AccountKey:            "usage-account-history:3:codex-account:636F646578:776F726B73706163652D31",
		},
	}

	raw, err := db.AccountWindowModelStats(ctx, windows)
	if err != nil {
		t.Fatalf("raw shared-workspace account window stats: %v", err)
	}
	projected, _, available, err := db.UsageMonitoringAccountWindowStats(ctx, windows)
	if err != nil || !available {
		t.Fatalf("projected shared-workspace account window stats: available=%v err=%v", available, err)
	}
	if !reflect.DeepEqual(projected, raw) {
		t.Fatalf("shared-workspace projection/raw mismatch\nprojection=%#v\nraw=%#v", projected, raw)
	}
	if len(projected) != 2 {
		t.Fatalf("shared-workspace stats = %#v, want one row per member", projected)
	}
	if projected[0].RequestIndex != 0 || projected[0].Calls != 1 || projected[0].InputTokens != 100 {
		t.Fatalf("Alice account window stats = %#v, want one call and 100 input tokens", projected[0])
	}
	if projected[1].RequestIndex != 1 || projected[1].Calls != 1 || projected[1].InputTokens != 200 {
		t.Fatalf("Bob account window stats = %#v, want one call and 200 input tokens", projected[1])
	}

	tail := makeEvent("team-alice-tail", "alice@example.com", "alice.json", "auth-alice", 300)
	if _, err := db.InsertEvents(ctx, []usage.Event{tail}); err != nil {
		t.Fatalf("insert Alice raw tail: %v", err)
	}
	raw, err = db.AccountWindowModelStats(ctx, windows)
	if err != nil {
		t.Fatalf("raw shared-workspace account window stats with tail: %v", err)
	}
	projected, _, available, err = db.UsageMonitoringAccountWindowStats(ctx, windows)
	if err != nil || !available {
		t.Fatalf("projected shared-workspace account window stats with tail: available=%v err=%v", available, err)
	}
	if !reflect.DeepEqual(projected, raw) {
		t.Fatalf("shared-workspace tail projection/raw mismatch\nprojection=%#v\nraw=%#v", projected, raw)
	}
	if projected[0].Calls != 2 || projected[0].InputTokens != 400 || projected[1].Calls != 1 || projected[1].InputTokens != 200 {
		t.Fatalf("shared-workspace tail stats = %#v, want Alice 2/400 and Bob 1/200", projected)
	}
}

func TestCodexAccountWindowDoesNotUseDailyCredentialFallbackWithoutMember(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	fromMS := int64(1_800_057_600_000)
	toMS := fromMS + testDayMS

	makeEvent := func(hash, member string, input int64) usage.Event {
		event := monitoringRepositoryEvent(hash, fromMS+input, "gpt-team-window", "key-shared", member, "auth-shared", "shared.json", false, input, 1, 1)
		event.AuthFileSnapshot = "shared.json"
		event.AuthIndex = "auth-shared"
		event.AuthProviderSnapshot = "codex"
		event.AuthAccountIDSnapshot = "workspace-team"
		return event
	}
	if _, err := db.InsertEvents(ctx, []usage.Event{
		makeEvent("team-shared-alice", "alice@example.com", 100),
		makeEvent("team-shared-bob", "bob@example.com", 200),
	}); err != nil {
		t.Fatalf("insert shared-credential events: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	windows := []store.AccountWindowUsageQuery{{
		RequestIndex:          0,
		FromMS:                fromMS,
		ToMS:                  toMS,
		AuthFileSnapshot:      "shared.json",
		AuthProviderSnapshot:  "codex",
		AuthAccountIDSnapshot: "workspace-team",
		AuthIndex:             "auth-shared",
		Source:                "shared.json",
	}}

	raw, err := db.AccountWindowModelStats(ctx, windows)
	if err != nil {
		t.Fatalf("raw workspace-only account window stats: %v", err)
	}
	projected, _, available, err := db.UsageMonitoringAccountWindowStats(ctx, windows)
	if err != nil || !available {
		t.Fatalf("projected workspace-only account window stats: available=%v err=%v", available, err)
	}
	if len(raw) != 0 || len(projected) != 0 {
		t.Fatalf("workspace-only target inherited member daily rows: raw=%#v projected=%#v", raw, projected)
	}
}

func TestCodexAccountWindowRejectsConflictingLegacyFileIndex(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	fromMS := int64(1_800_057_600_000)
	toMS := fromMS + testDayMS
	makeEvent := func(hash string, offset, input int64, accountID string) usage.Event {
		event := monitoringRepositoryEvent(hash, fromMS+offset, "gpt-window", "key-a", "same@example.com", "auth-a", "codex-a.json", false, input, 1, 10)
		event.AuthFileSnapshot = "codex-a.json"
		event.AuthProviderSnapshot = "codex"
		event.AuthAccountIDSnapshot = accountID
		return event
	}
	if _, err := db.InsertEvents(ctx, []usage.Event{
		makeEvent("window-conflict-stable", 1_000, 10, "account-a"),
		makeEvent("window-conflict-legacy", 2_000, 20, ""),
		makeEvent("window-conflict-other", 3_000, 90, "account-b"),
	}); err != nil {
		t.Fatalf("insert conflicting account-window events: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	windows := []store.AccountWindowUsageQuery{{
		RequestIndex:          0,
		FromMS:                fromMS,
		ToMS:                  toMS,
		AccountSnapshot:       "same@example.com",
		AuthFileSnapshot:      "codex-a.json",
		AuthProviderSnapshot:  "codex",
		AuthAccountIDSnapshot: "account-a",
		AuthIndex:             "auth-a",
		Source:                "codex-a.json",
	}}
	raw, err := db.AccountWindowModelStats(ctx, windows)
	if err != nil {
		t.Fatalf("raw conflicting account-window stats: %v", err)
	}
	projected, _, available, err := db.UsageMonitoringAccountWindowStats(ctx, windows)
	if err != nil || !available {
		t.Fatalf("projected conflicting account-window stats: available=%v err=%v", available, err)
	}
	if !reflect.DeepEqual(projected, raw) {
		t.Fatalf("conflicting account-window projection/raw mismatch\nprojection=%#v\nraw=%#v", projected, raw)
	}
	if len(projected) != 1 || projected[0].Calls != 1 || projected[0].InputTokens != 10 {
		t.Fatalf("conflicting legacy account-window bucket was merged = %#v, want only stable account event", projected)
	}
}

func TestCodexAccountWindowUsesRawProjectionForConflictingWorkspaceEvidence(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	fromMS := int64(1_800_057_600_000)
	toMS := fromMS + testDayMS

	event := monitoringRepositoryEvent(
		"window-conflicting-workspace-evidence",
		fromMS+1_000,
		"gpt-team-window",
		"key-alice",
		"alice@example.com",
		"auth-alice",
		"alice.json",
		false,
		100,
		10,
		1,
	)
	event.AuthFileSnapshot = "alice.json"
	event.AuthIndex = "auth-alice"
	event.AuthProviderSnapshot = "codex"
	event.AuthAccountIDSnapshot = "workspace-1"
	event.AuthProjectIDSnapshot = usageidentity.CodexAccountIDSnapshot("workspace-2")
	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert conflicting workspace evidence event: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	window := store.AccountWindowUsageQuery{
		RequestIndex:          0,
		FromMS:                fromMS,
		ToMS:                  toMS,
		AccountSnapshot:       "alice@example.com",
		AuthFileSnapshot:      "alice.json",
		AuthProviderSnapshot:  "codex",
		AuthAccountIDSnapshot: "workspace-1",
		AuthProjectIDSnapshot: usageidentity.CodexAccountIDSnapshot("workspace-2"),
		AuthIndex:             "auth-alice",
		Source:                "alice.json",
	}
	raw, err := db.AccountWindowModelStats(ctx, []store.AccountWindowUsageQuery{window})
	if err != nil {
		t.Fatalf("raw conflicting workspace-evidence stats: %v", err)
	}
	projected, _, available, err := db.UsageMonitoringAccountWindowStats(ctx, []store.AccountWindowUsageQuery{window})
	if err != nil || !available {
		t.Fatalf("projected conflicting workspace-evidence stats: available=%v err=%v", available, err)
	}
	if !reflect.DeepEqual(projected, raw) {
		t.Fatalf("conflicting workspace-evidence projection/raw mismatch\nprojection=%#v\nraw=%#v", projected, raw)
	}
	if len(projected) != 1 || projected[0].Calls != 1 || projected[0].InputTokens != 100 {
		t.Fatalf("conflicting workspace-evidence stats = %#v, want one raw-equivalent event", projected)
	}
}

func TestCodexAccountWindowDoesNotMergeConflictingWorkspaceEvidenceFromDailyRollup(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	fromMS := int64(1_800_057_600_000)
	toMS := fromMS + testDayMS

	valid := monitoringRepositoryEvent(
		"window-daily-valid-workspace-evidence",
		fromMS+1_000,
		"gpt-team-window",
		"key-alice",
		"alice@example.com",
		"auth-alice",
		"alice.json",
		false,
		100,
		10,
		1,
	)
	valid.AuthFileSnapshot = "alice.json"
	valid.AuthIndex = "auth-alice"
	valid.AuthProviderSnapshot = "codex"
	valid.AuthAccountIDSnapshot = "workspace-1"

	conflicting := valid
	conflicting.EventHash = "window-daily-conflicting-workspace-evidence"
	conflicting.TimestampMS = fromMS + 2_000
	conflicting.AuthProjectIDSnapshot = usageidentity.CodexAccountIDSnapshot("workspace-2")
	conflicting.InputTokens = 200
	conflicting.TotalTokens = 20
	if _, err := db.InsertEvents(ctx, []usage.Event{valid, conflicting}); err != nil {
		t.Fatalf("insert daily workspace-evidence events: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	window := store.AccountWindowUsageQuery{
		RequestIndex:          0,
		FromMS:                fromMS,
		ToMS:                  toMS,
		AccountSnapshot:       "alice@example.com",
		AuthFileSnapshot:      "alice.json",
		AuthProviderSnapshot:  "codex",
		AuthAccountIDSnapshot: "workspace-1",
		AuthIndex:             "auth-alice",
		Source:                "alice.json",
	}
	raw, err := db.AccountWindowModelStats(ctx, []store.AccountWindowUsageQuery{window})
	if err != nil {
		t.Fatalf("raw daily workspace-evidence stats: %v", err)
	}
	projected, _, available, err := db.UsageMonitoringAccountWindowStats(ctx, []store.AccountWindowUsageQuery{window})
	if err != nil || !available {
		t.Fatalf("projected daily workspace-evidence stats: available=%v err=%v", available, err)
	}
	if !reflect.DeepEqual(projected, raw) {
		t.Fatalf("daily workspace-evidence projection/raw mismatch\nprojection=%#v\nraw=%#v", projected, raw)
	}
	if len(projected) != 1 || projected[0].Calls != 1 || projected[0].InputTokens != 100 {
		t.Fatalf("daily conflicting workspace evidence was merged = %#v, want only valid event", projected)
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
		events[index].Provider = "openai"
		events[index].AuthFileSnapshot = events[index].Source
		events[index].AuthProviderSnapshot = "openai"
		events[index].AuthProjectIDSnapshot = ""
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
	tail.Provider = "openai"
	tail.AuthProviderSnapshot = "openai"
	tail.AuthProjectIDSnapshot = ""
	if _, err := db.InsertEvents(ctx, []usage.Event{tail}); err != nil {
		t.Fatalf("insert daily account window raw tail: %v", err)
	}

	windows := []store.AccountWindowUsageQuery{{
		RequestIndex:         0,
		FromMS:               fromMS,
		ToMS:                 toMS,
		AccountSnapshot:      "daily@example.com",
		AuthFileSnapshot:     "daily.json",
		AuthProviderSnapshot: "openai",
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

func TestUsageMonitoringSearchDoesNotIndexHistoricalCodexProjectMarker(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	baseMS := int64(1_800_057_600_000)
	marker := usageidentity.CodexAccountIDSnapshot("historical-account")
	event := monitoringRepositoryEvent(
		"search-legacy-codex-marker",
		baseMS+1_000,
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
	event.AuthProjectIDSnapshot = marker
	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert legacy Codex search event: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	count, _, available, err := db.UsageMonitoringEventsCount(ctx, store.AnalyticsFilter{
		FromMS:        baseMS,
		ToMS:          baseMS + testDayMS,
		SearchQuery:   marker,
		IncludeFailed: true,
	})
	if err != nil || !available {
		t.Fatalf("projected marker search count: count=%d available=%v err=%v", count, available, err)
	}
	if count != 0 {
		t.Fatalf("projected marker search count = %d, want 0", count)
	}

	// Leave the next event outside the projection coverage so the raw tail
	// search path is exercised as well.
	tail := event
	tail.EventHash = "search-legacy-codex-marker-tail"
	tail.TimestampMS = baseMS + 2_000
	tail.Timestamp = time.UnixMilli(tail.TimestampMS).UTC().Format(time.RFC3339Nano)
	if _, err := db.InsertEvents(ctx, []usage.Event{tail}); err != nil {
		t.Fatalf("insert raw-tail Codex search event: %v", err)
	}
	count, _, available, err = db.UsageMonitoringEventsCount(ctx, store.AnalyticsFilter{
		FromMS:        baseMS,
		ToMS:          baseMS + testDayMS,
		SearchQuery:   marker,
		IncludeFailed: true,
	})
	if err != nil || !available {
		t.Fatalf("raw-tail marker search count: count=%d available=%v err=%v", count, available, err)
	}
	if count != 0 {
		t.Fatalf("raw-tail marker search count = %d, want 0", count)
	}
}

func TestUsageMonitoringStatsIncludeHistoricalCodexProjectMarker(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	fromMS := int64(1_800_057_600_000)
	toMS := fromMS + testDayMS
	event := monitoringRepositoryEvent(
		"stats-legacy-codex-marker",
		fromMS+1_000,
		"gpt-marker",
		"key-marker",
		"alice@example.com",
		"auth-marker",
		"marker.json",
		false,
		100,
		20,
		10,
	)
	event.AuthProjectIDSnapshot = usageidentity.CodexAccountIDSnapshot("workspace-marker")
	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert historical Codex marker event: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)

	filter := store.AnalyticsFilter{FromMS: fromMS, ToMS: toMS, IncludeFailed: true}
	assertProjectionCoreReadersMatchRaw(t, ctx, db, filter)
	assertMonitoringReadersMatchRaw(t, ctx, db, filter)
}

func TestSuccessfulResponseHeadersDoNotEnterFailureSearchStorage(t *testing.T) {
	sqlDB, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	marker := strings.Repeat("unindexed-success-header-marker-", 128)
	payload, err := json.Marshal(map[string]any{
		"timestamp": "2026-04-25T00:00:00Z",
		"failed":    false,
		"provider":  "openai",
		"model":     "gpt-5.4",
		"endpoint":  "POST /v1/chat/completions",
		"tokens":    map[string]any{"input_tokens": 1, "total_tokens": 1},
		"response_headers": map[string]any{
			"Content-Type":                 []any{"application/json"},
			"X-CPAMP-Unindexed-Diagnostic": []any{marker},
		},
	})
	if err != nil {
		t.Fatalf("marshal successful event: %v", err)
	}
	event, err := usage.NormalizeRaw(payload)
	if err != nil {
		t.Fatalf("normalize successful event: %v", err)
	}
	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert successful event: %v", err)
	}

	var eventID int64
	var failBody, failSummary, metadataJSON, rawJSON string
	if err := sqlDB.QueryRowContext(ctx, `select id, coalesce(fail_body, ''), coalesce(fail_summary, ''),
		coalesce(response_metadata_json, ''), coalesce(raw_json, '')
		from usage_events where event_hash = ?`, event.EventHash).Scan(&eventID, &failBody, &failSummary, &metadataJSON, &rawJSON); err != nil {
		t.Fatalf("read persisted successful event: %v", err)
	}
	if failBody != "" || failSummary != "" {
		t.Fatalf("persisted failure fields = body:%q summary:%q", failBody, failSummary)
	}
	if !strings.Contains(metadataJSON, "application/json") || !strings.Contains(rawJSON, marker) {
		t.Fatalf("persisted metadata/raw json missing: metadata=%q rawHasMarker=%v", metadataJSON, strings.Contains(rawJSON, marker))
	}

	catchUpMonitoringRepository(t, ctx, db)
	var searchText string
	if err := sqlDB.QueryRowContext(ctx, `select search_text from usage_monitoring_event_projection_v1 where event_id = ?`, eventID).Scan(&searchText); err != nil {
		t.Fatalf("read successful event projection: %v", err)
	}
	if strings.Contains(searchText, marker) {
		t.Fatalf("projection search text contains response header marker")
	}
	assertSearchIndexCount(t, ctx, sqlDB, marker, 0)
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

// TestUsageMonitoringStatsRevisionRollbackDoesNotDoubleCount covers a
// deterministic A-to-B-to-A revision rollback. The target revision is cleared
// in bounded batches, readers use raw events while clearing, and rebuilding the
// restored revision must not add to its retained rows.
func TestUsageMonitoringStatsRevisionRollbackDoesNotDoubleCount(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	baseMS := int64(1_800_057_600_000)
	pricesA := map[string]store.ModelPrice{"gpt-a": {Prompt: 1}}
	pricesB := map[string]store.ModelPrice{
		"gpt-a": {Prompt: 1, ContextTiers: []store.ModelPriceContextTier{{ThresholdTokens: 100, Prompt: 2, PromptConfigured: true}}},
	}
	if err := db.SaveModelPrices(ctx, pricesA); err != nil {
		t.Fatalf("save prices A: %v", err)
	}
	if _, err := db.InsertEvents(ctx, []usage.Event{
		monitoringRepositoryEvent("rollback", baseMS+testDayMS, "gpt-a", "key-a", "alice@example.com", "auth-a", "source-a", false, 150, 10, 10),
	}); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	filter := store.AnalyticsFilter{FromMS: baseMS, ToMS: baseMS + 3*testDayMS, IncludeFailed: true}

	// Build revision A.
	catchUpMonitoringRepository(t, ctx, db)
	assertMonitoringReadersMatchRaw(t, ctx, db, filter)
	// Switch to revision B and rebuild it.
	if err := db.SaveModelPrices(ctx, pricesB); err != nil {
		t.Fatalf("save prices B: %v", err)
	}
	catchUpMonitoringRepository(t, ctx, db)
	assertMonitoringReadersMatchRaw(t, ctx, db, filter)
	// Restore revision A. A limit of one can clear only the account row in the
	// first transaction, leaving the API-key row for a later transaction.
	if err := db.SaveModelPrices(ctx, pricesA); err != nil {
		t.Fatalf("rollback to prices A: %v", err)
	}
	result, err := db.CatchUpUsageMonitoringStats(ctx, 1, 30_000)
	if err != nil {
		t.Fatalf("start bounded rollback clearing: %v", err)
	}
	if !result.Pending || !result.Rebuilt || result.Processed != 0 || !result.ContinueSoon {
		t.Fatalf("first bounded rollback result = %#v", result)
	}
	state, err := db.UsageMonitoringState(ctx, monitoringrepo.StatsRollupName)
	if err != nil {
		t.Fatalf("read clearing state: %v", err)
	}
	if state.Status != "clearing" || state.CoverageEventID != 0 {
		t.Fatalf("bounded clearing state = %#v", state)
	}
	assertMonitoringReadersMatchRaw(t, ctx, db, filter)
	if err := db.RecordUsageMonitoringFailure(ctx, monitoringrepo.StatsRollupName, errors.New("interrupted clearing"), 31_000); err != nil {
		t.Fatalf("record clearing interruption: %v", err)
	}
	state, err = db.UsageMonitoringState(ctx, monitoringrepo.StatsRollupName)
	if err != nil {
		t.Fatalf("read interrupted clearing state: %v", err)
	}
	if state.Status != "clearing" || state.LastError != "interrupted clearing" {
		t.Fatalf("interrupted clearing state = %#v", state)
	}
	for {
		result, err = db.CatchUpUsageMonitoringStats(ctx, 1, 32_000)
		if err != nil {
			t.Fatalf("resume bounded rollback clearing: %v", err)
		}
		if !result.Pending {
			break
		}
	}
	// Revision A has been rebuilt exactly once.
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

func TestUsageMonitoringHeaderSnapshotsPreserveRequestAndResolvedModelIdentity(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	ctx := context.Background()
	baseMS := int64(1_800_057_700_000)
	event := monitoringRepositoryEvent(
		"header-model-identity",
		baseMS+1_000,
		"my-spark",
		"key-a",
		"alice@example.com",
		"auth-a",
		"source-a",
		false,
		10,
		5,
		10,
	)
	event.RequestedModel = "my-spark"
	event.ResolvedModel = codexquota.SparkModelID
	if _, err := db.InsertEvents(ctx, []usage.Event{event}); err != nil {
		t.Fatalf("insert header identity event: %v", err)
	}

	raw, err := db.LatestHeaderSnapshots(ctx, baseMS, 10)
	if err != nil {
		t.Fatalf("load raw header identity: %v", err)
	}
	if len(raw) != 1 || raw[0].Model != "my-spark" || raw[0].AnalyticsModel != "my-spark" ||
		raw[0].RequestedModel != "my-spark" || raw[0].ResolvedModel != codexquota.SparkModelID {
		t.Fatalf("raw header identity = %#v", raw)
	}

	catchUpMonitoringRepository(t, ctx, db)
	rolled, _, available, err := db.UsageMonitoringHeaderSnapshots(ctx, baseMS, 10)
	if err != nil || !available {
		t.Fatalf("load rolled header identity: available=%v err=%v", available, err)
	}
	if !reflect.DeepEqual(rolled, raw) {
		t.Fatalf("rolled header identity mismatch\nrolled=%#v\nraw=%#v", rolled, raw)
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

func explainMonitoringPlan(t *testing.T, db *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("explain monitoring query plan: %v", err)
	}
	defer rows.Close()

	details := make([]string, 0, 4)
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan monitoring query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read monitoring query plan: %v", err)
	}
	return details
}

func TestUsageMonitoringUnknownFailureName(t *testing.T) {
	_, db := newMonitoringRepositoryStore(t)
	err := db.RecordUsageMonitoringFailure(context.Background(), "unknown", errors.New(fmt.Sprint("boom")), time.Now().UnixMilli())
	if err == nil {
		t.Fatal("unknown rollup name unexpectedly accepted")
	}
}
