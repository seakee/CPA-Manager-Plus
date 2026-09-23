package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	quotasnapshotrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotasnapshot"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

func TestDerivedIndexesAreDeferredUntilPostListenMaintenance(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	for _, index := range derivedIndexStatements {
		var count int
		if err := db.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = ?`, index.name).Scan(&count); err != nil {
			t.Fatalf("inspect deferred index %s: %v", index.name, err)
		}
		if count != 0 {
			t.Fatalf("deferred index %s exists before post-listen maintenance", index.name)
		}
	}

	if err := RunDerivedStartupMaintenance(context.Background(), db); err != nil {
		t.Fatalf("run post-listen startup maintenance: %v", err)
	}
	for _, index := range derivedIndexStatements {
		var count int
		if err := db.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = ?`, index.name).Scan(&count); err != nil {
			t.Fatalf("inspect post-listen index %s: %v", index.name, err)
		}
		if count != 1 {
			t.Fatalf("post-listen index %s count = %d, want 1", index.name, count)
		}
	}
	for _, query := range []struct {
		sql       string
		indexName string
	}{
		{`EXPLAIN QUERY PLAN SELECT api_key_id FROM gateway_api_key_source_bindings WHERE api_key_hash = ?`, "idx_gateway_api_key_binding_history_hash"},
		{`EXPLAIN QUERY PLAN SELECT credential_id FROM gateway_credential_source_bindings WHERE source_auth_id = ?`, "idx_gateway_credential_binding_history_auth_id"},
	} {
		var id, parent, unused int
		var detail string
		if err := db.QueryRow(query.sql, "source").Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("explain history lookup: %v", err)
		}
		if !strings.Contains(detail, query.indexName) {
			t.Fatalf("history lookup plan = %q, want %s", detail, query.indexName)
		}
	}
}

func TestDerivedIndexesOnNonEmptyTablesRequireOfflineCleanup(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`insert into usage_monitoring_selector_daily_rollups_v1 (
		model_format_revision, bucket_ms, model, api_key_hash, provider,
		auth_file_snapshot, account_snapshot, auth_label_snapshot,
		auth_index, source, source_hash, updated_at_ms
	) values (?, 0, 'model', '', '', '', '', '', '', '', '', 1)`, usageidentity.ModelFormatVersion); err != nil {
		t.Fatalf("seed non-empty upgrade table: %v", err)
	}
	for _, statement := range []string{
		`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms)
			values ('key', 1, 'active', 1, 1)`,
		`insert into gateway_credential_identities (id, revision, lifecycle, created_at_ms, updated_at_ms)
			values ('credential', 1, 'active', 1, 1)`,
		`insert into gateway_api_key_source_bindings
			(api_key_id, runtime_identity, api_key_hash, first_seen_at_ms, last_seen_at_ms)
			values ('key', 'runtime', 'hash', 1, 1)`,
		`insert into gateway_credential_source_bindings
			(credential_id, runtime_identity, source_auth_id, first_seen_at_ms, last_seen_at_ms)
			values ('credential', 'runtime', 'auth', 1, 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed source binding history: %v", err)
		}
	}

	ctx := context.Background()
	if err := RunDerivedStartupMaintenance(ctx, db); err != nil {
		t.Fatalf("run bounded startup preparation for non-empty table: %v", err)
	}
	var count int
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'index'
		and name = 'idx_usage_monitoring_selector_revision_bucket'`).Scan(&count); err != nil {
		t.Fatalf("inspect revision index: %v", err)
	}
	if count != 0 {
		t.Fatalf("revision index count after startup = %d, want deferred", count)
	}
	for _, name := range []string{"idx_gateway_api_key_binding_history_hash", "idx_gateway_credential_binding_history_auth_id"} {
		if err := db.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = ?`, name).Scan(&count); err != nil || count != 0 {
			t.Fatalf("binding history index %s after startup = %d, err = %v", name, count, err)
		}
	}
	result, err := CleanupDerivedOffline(ctx, db)
	if err != nil {
		t.Fatalf("prepare deferred indexes offline: %v", err)
	}
	if result.PreparedIndexes == 0 {
		t.Fatalf("offline cleanup result = %+v, want prepared indexes", result)
	}
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'index'
		and name = 'idx_usage_monitoring_selector_revision_bucket'`).Scan(&count); err != nil {
		t.Fatalf("inspect offline revision index: %v", err)
	}
	if count != 1 {
		t.Fatalf("revision index count after offline cleanup = %d, want 1", count)
	}
	for _, name := range []string{"idx_gateway_api_key_binding_history_hash", "idx_gateway_credential_binding_history_auth_id"} {
		if err := db.QueryRow(`select count(*) from sqlite_master where type = 'index' and name = ?`, name).Scan(&count); err != nil || count != 1 {
			t.Fatalf("binding history index %s after offline cleanup = %d, err = %v", name, count, err)
		}
	}
}

func TestAccountActionIdentityIndexReplacementRequiresOfflineCleanup(t *testing.T) {
	const rowCount = 100_000
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`create unique index idx_account_action_candidates_pending_file_action
			on account_action_candidates(auth_file_name, action_type) where status = 'pending'`,
		`with recursive ids(id) as (
			select 1 union all select id + 1 from ids where id < 100000
		) insert into account_action_candidates (
			action_type, status, auth_file_name, reason_code,
			first_seen_at_ms, last_seen_at_ms, created_at_ms, updated_at_ms
		) select 'review', 'completed', 'credential-' || id, 'legacy', id, id, id, id from ids`,
		`insert into account_action_candidates (
			action_type, status, auth_file_name, reason_code,
			first_seen_at_ms, last_seen_at_ms, created_at_ms, updated_at_ms
		) values ('reauth', 'pending', 'shared.json', 'token_revoked', 1, 1, 1, 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare legacy account action index fixture: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy account action fixture: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen legacy account action fixture: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	assertTableCount(t, db, "account_action_candidates", rowCount+1)
	assertIndexTable := func(name string, want string) {
		t.Helper()
		var tableName string
		err := db.QueryRow(`select tbl_name from sqlite_master where type = 'index' and name = ?`, name).Scan(&tableName)
		if want == "" {
			if err != sql.ErrNoRows {
				t.Fatalf("index %s lookup error = %v, want missing", name, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("inspect index %s: %v", name, err)
		}
		if tableName != want {
			t.Fatalf("index %s table = %q, want %q", name, tableName, want)
		}
	}
	assertIndexTable("idx_account_action_candidates_pending_file_action", "account_action_candidates")
	assertIndexTable("idx_account_action_candidates_pending_identity_action", "")

	ctx := context.Background()
	if err := RunDerivedStartupMaintenance(ctx, db); err != nil {
		t.Fatalf("run bounded account action index preparation: %v", err)
	}
	assertIndexTable("idx_account_action_candidates_pending_file_action", "account_action_candidates")
	assertIndexTable("idx_account_action_candidates_pending_identity_action", "")

	result, err := CleanupDerivedOffline(ctx, db)
	if err != nil {
		t.Fatalf("replace account action identity index offline: %v", err)
	}
	if result.PreparedIndexes == 0 {
		t.Fatalf("offline cleanup result = %+v, want index changes", result)
	}
	assertIndexTable("idx_account_action_candidates_pending_file_action", "")
	assertIndexTable("idx_account_action_candidates_pending_identity_action", "account_action_candidates")
	if _, err := db.Exec(`insert into account_action_candidates (
		action_type, status, auth_file_name, reason_code,
		first_seen_at_ms, last_seen_at_ms, created_at_ms, updated_at_ms
	) values ('reauth', 'pending', 'shared.json', 'authentication_review', 2, 2, 2, 2)`); err != nil {
		t.Fatalf("insert distinct pending account action after identity index replacement: %v", err)
	}
	if _, err := db.Exec(`insert into account_action_candidates (
		action_type, status, auth_file_name, reason_code,
		first_seen_at_ms, last_seen_at_ms, created_at_ms, updated_at_ms
	) values ('reauth', 'pending', 'shared.json', 'token_revoked', 3, 3, 3, 3)`); err == nil {
		t.Fatal("duplicate pending account action insert succeeded after identity index replacement")
	}
}

func TestDerivedIndexPreparationReclaimsNamesFromParkedTables(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`create index idx_usage_monitoring_selector_daily_bucket
		on usage_monitoring_selector_daily_rollups_v1(bucket_ms)`); err != nil {
		t.Fatalf("create v1.11.12 selector index: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin selector park: %v", err)
	}
	if err := parkAndRecreateDerivedTable(tx, usageMonitoringSelectorDailyTable, usageMonitoringSelectorLegacy); err != nil {
		_ = tx.Rollback()
		t.Fatalf("park v1.11.12 selector table: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit selector park: %v", err)
	}
	var indexedTable string
	if err := db.QueryRow(`select tbl_name from sqlite_master where type = 'index'
		and name = 'idx_usage_monitoring_selector_daily_bucket'`).Scan(&indexedTable); err != nil {
		t.Fatalf("inspect parked selector index: %v", err)
	}
	if indexedTable != usageMonitoringSelectorLegacy {
		t.Fatalf("parked selector index table = %q, want %q", indexedTable, usageMonitoringSelectorLegacy)
	}

	ctx := context.Background()
	if err := RunDerivedStartupMaintenance(ctx, db); err != nil {
		t.Fatalf("prepare indexes after selector park: %v", err)
	}
	if err := db.QueryRow(`select tbl_name from sqlite_master where type = 'index'
		and name = 'idx_usage_monitoring_selector_daily_bucket'`).Scan(&indexedTable); err != nil {
		t.Fatalf("inspect deferred selector index: %v", err)
	}
	if indexedTable != usageMonitoringSelectorLegacy {
		t.Fatalf("startup changed parked selector index table = %q, want %q", indexedTable, usageMonitoringSelectorLegacy)
	}
	result, err := CleanupDerivedOffline(ctx, db)
	if err != nil {
		t.Fatalf("reclaim selector index offline: %v", err)
	}
	if result.PreparedIndexes == 0 {
		t.Fatalf("offline cleanup result = %+v, want prepared indexes", result)
	}
	if err := db.QueryRow(`select tbl_name from sqlite_master where type = 'index'
		and name = 'idx_usage_monitoring_selector_daily_bucket'`).Scan(&indexedTable); err != nil {
		t.Fatalf("inspect reclaimed selector index: %v", err)
	}
	if indexedTable != usageMonitoringSelectorDailyTable {
		t.Fatalf("reclaimed selector index table = %q, want %q", indexedTable, usageMonitoringSelectorDailyTable)
	}
}

func TestCleanupDerivedBatchIsBoundedAndResumable(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`create table ` + usageAccountModelRollupsLegacy + ` (id integer primary key)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if _, err := db.Exec(`with recursive ids(id) as (
		select 1 union all select id + 1 from ids where id < 2501
	) insert into ` + usageAccountModelRollupsLegacy + ` (id) select id from ids`); err != nil {
		t.Fatalf("seed legacy rows: %v", err)
	}

	ctx := context.Background()
	for index, wantProcessed := range []int64{1000, 1000, 501} {
		processed, pending, err := cleanupDerivedBatch(ctx, db, 1000)
		if err != nil {
			t.Fatalf("cleanup batch %d: %v", index+1, err)
		}
		if processed != wantProcessed || !pending {
			t.Fatalf("cleanup batch %d = processed:%d pending:%t, want %d,true", index+1, processed, pending, wantProcessed)
		}
	}
	processed, pending, err := cleanupDerivedBatch(ctx, db, 1000)
	if err != nil {
		t.Fatalf("drop empty legacy table: %v", err)
	}
	if processed != 0 || !pending {
		t.Fatalf("drop empty legacy table = processed:%d pending:%t", processed, pending)
	}
	exists, err := derivedTableExists(ctx, db, usageAccountModelRollupsLegacy)
	if err != nil {
		t.Fatalf("inspect removed legacy table: %v", err)
	}
	if exists {
		t.Fatal("legacy table still exists after bounded cleanup")
	}
}

func TestCleanupDerivedBatchRemovesIdentityLegacyTable(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "identity-legacy.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`create table ` + usageAccountModelIdentityLegacy + ` (id integer primary key)`); err != nil {
		t.Fatalf("create identity legacy table: %v", err)
	}
	if _, err := db.Exec(`insert into ` + usageAccountModelIdentityLegacy + ` (id) values (1)`); err != nil {
		t.Fatalf("seed identity legacy table: %v", err)
	}

	processed, pending, err := cleanupDerivedBatch(context.Background(), db, 1000)
	if err != nil {
		t.Fatalf("clean identity legacy rows: %v", err)
	}
	if processed != 1 || !pending {
		t.Fatalf("clean identity legacy rows = processed:%d pending:%t, want 1,true", processed, pending)
	}
	processed, pending, err = cleanupDerivedBatch(context.Background(), db, 1000)
	if err != nil {
		t.Fatalf("drop identity legacy table: %v", err)
	}
	if processed != 0 || !pending {
		t.Fatalf("drop identity legacy table = processed:%d pending:%t, want 0,true", processed, pending)
	}
	exists, err := derivedTableExists(context.Background(), db, usageAccountModelIdentityLegacy)
	if err != nil {
		t.Fatalf("inspect identity legacy table: %v", err)
	}
	if exists {
		t.Fatal("identity legacy table still exists after cleanup")
	}
}

func TestMonitoringFTSCleanupIsBoundedAndRequiresOfflineFinalization(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`with recursive ids(id) as (
		select 1 union all select id + 1 from ids where id < 2501
	) insert into usage_events (
		id, request_id, event_hash, timestamp_ms, timestamp, model, created_at_ms
	) select id, 'request-' || id, 'event-' || id, id, cast(id as text), 'model', id from ids`); err != nil {
		t.Fatalf("seed usage events: %v", err)
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin projection seed: %v", err)
	}
	if err := usageprojection.UpsertEventRange(ctx, tx, 0, 2501, 1); err != nil {
		_ = tx.Rollback()
		t.Fatalf("seed projection rows: %v", err)
	}
	if err := dropUsageMonitoringSearchTriggers(tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("drop active search triggers: %v", err)
	}
	ftsTable, projectionTable, err := parkUsageMonitoringSearchGeneration(tx, true)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("park monitoring generation: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit parked monitoring generation: %v", err)
	}

	for index, wantProcessed := range []int64{1000, 1000, 501} {
		processed, pending, err := cleanupDerivedBatch(ctx, db, 1000)
		if err != nil {
			t.Fatalf("cleanup monitoring batch %d: %v", index+1, err)
		}
		if processed != wantProcessed || !pending {
			t.Fatalf("cleanup monitoring batch %d = processed:%d pending:%t, want %d,true", index+1, processed, pending, wantProcessed)
		}
	}
	var status string
	var processedRows int64
	if err := db.QueryRow(`select status, processed_rows from usage_derived_cleanup_jobs
		where fts_table = ?`, ftsTable).Scan(&status, &processedRows); err != nil {
		t.Fatalf("read monitoring cleanup job: %v", err)
	}
	if status != "offline_required" || processedRows != 2501 {
		t.Fatalf("monitoring cleanup job = status:%q processed:%d", status, processedRows)
	}
	assertTableCount(t, db, projectionTable, 0)
	assertTableCount(t, db, "usage_events", 2501)

	result, err := CleanupDerivedOffline(ctx, db)
	if err != nil {
		t.Fatalf("finalize monitoring cleanup offline: %v", err)
	}
	if result.CompletedJobs != 1 || result.ProcessedRows != 0 {
		t.Fatalf("offline cleanup result = %+v", result)
	}
	for _, tableName := range []string{ftsTable, projectionTable} {
		exists, err := derivedTableExists(ctx, db, tableName)
		if err != nil {
			t.Fatalf("inspect finalized table %s: %v", tableName, err)
		}
		if exists {
			t.Fatalf("offline cleanup retained table %s", tableName)
		}
	}
	if err := db.QueryRow(`select status from usage_derived_cleanup_jobs where fts_table = ?`, ftsTable).Scan(&status); err != nil {
		t.Fatalf("read finalized monitoring cleanup job: %v", err)
	}
	if status != "completed" {
		t.Fatalf("finalized monitoring cleanup status = %q", status)
	}
	assertTableCount(t, db, "usage_events", 2501)
}

func TestCleanupDerivedOfflineCompletesLegacyQuotaGroupBeyondOnlineLimit(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for index, windowID := range []string{"weekly", "five-hour"} {
		windowKind := "weekly"
		if windowID == "five-hour" {
			windowKind = "five_hour"
		}
		if _, err := db.Exec(`insert into account_quota_snapshots (
			account_key, provider, provider_window_id, window_kind, window_mode,
			model_scope_kind, source, source_observation_id, observed_at_ms,
			boundary_accuracy, duration_seconds, used_percent, remaining_percent,
			created_at_ms
		) values ('account-1', 'codex', ?, ?, 'fixed', 'all',
			'inspection', 'oversized-group', 1000, 'exact', 3600, 25, 75, ?)`,
			windowID,
			windowKind,
			1000+index,
		); err != nil {
			t.Fatalf("seed offline quota snapshot %s: %v", windowID, err)
		}
	}
	if _, err := quotasnapshotrepo.BackfillLegacySnapshotsBatch(context.Background(), db, 1); !errors.Is(err, quotasnapshotrepo.ErrLegacySnapshotGroupTooLarge) {
		t.Fatalf("online oversized quota group error = %v", err)
	}

	result, err := CleanupDerivedOffline(context.Background(), db)
	if err != nil {
		t.Fatalf("complete oversized quota group offline: %v", err)
	}
	if result.ProcessedRows < 2 {
		t.Fatalf("offline cleanup result = %+v, want at least two migrated quota snapshots", result)
	}
	var pendingSnapshots int
	if err := db.QueryRow(`select count(*) from account_quota_snapshots where observation_id is null`).Scan(&pendingSnapshots); err != nil {
		t.Fatalf("count pending quota snapshots: %v", err)
	}
	if pendingSnapshots != 0 {
		t.Fatalf("pending quota snapshots after offline cleanup = %d", pendingSnapshots)
	}
}

func TestCleanupDerivedOfflineHonorsPreCanceledContextWithoutMutation(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "offline-cleanup-canceled.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`create table ` + usageAccountModelRollupsLegacy + ` (id integer primary key)`); err != nil {
		t.Fatalf("create retained legacy table: %v", err)
	}
	if _, err := db.Exec(`insert into ` + usageAccountModelRollupsLegacy + ` (id) values (1)`); err != nil {
		t.Fatalf("seed retained legacy table: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := CleanupDerivedOffline(ctx, db)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cleanup error = %v, want context canceled", err)
	}
	if result != (OfflineCleanupResult{}) {
		t.Fatalf("cleanup result = %+v, want zero result", result)
	}
	assertTableCount(t, db, usageAccountModelRollupsLegacy, 1)
}

func TestCleanupDerivedOfflineResumesLegacyAndRevisionCleanupIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	for _, statement := range []string{
		`create table ` + usageDashboardHourlyLegacy + ` (value integer)`,
		`with recursive ids(id) as (
			select 1 union all select id + 1 from ids where id < 1501
		) insert into ` + usageDashboardHourlyLegacy + ` select id from ids`,
		`insert into usage_events (
			request_id, event_hash, timestamp_ms, timestamp, model, created_at_ms
		) values ('source-event', 'source-event', 1, '1', 'model', 1)`,
		`update usage_pricing_rollup_state set structure_revision = 'pricing-current'
			where rollup_name = 'pricing_v1'`,
		`insert into usage_pricing_hourly_rollups_v1 (
			structure_revision, bucket_ms, model, billing_model, pricing_model,
			service_tier, context_threshold_tokens, failed, calls, updated_at_ms
		) values
			('pricing-current', 0, 'current', 'current', 'current', '', -1, 0, 1, 1),
			('pricing-stale', 0, 'stale', 'stale', 'stale', '', -1, 0, 1, 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			_ = db.Close()
			t.Fatalf("prepare resumable offline cleanup fixture: %v", err)
		}
	}
	processed, pending, err := cleanupDerivedBatch(context.Background(), db, 1000)
	if err != nil {
		_ = db.Close()
		t.Fatalf("run interrupted cleanup batch: %v", err)
	}
	if processed != 1000 || !pending {
		_ = db.Close()
		t.Fatalf("interrupted cleanup batch = processed:%d pending:%t", processed, pending)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close interrupted cleanup database: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen interrupted cleanup database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	result, err := CleanupDerivedOffline(context.Background(), db)
	if err != nil {
		t.Fatalf("resume offline cleanup: %v", err)
	}
	if result.ProcessedRows < 502 {
		t.Fatalf("resumed offline cleanup result = %+v, want remaining legacy and stale rows", result)
	}
	legacyExists, err := derivedTableExists(context.Background(), db, usageDashboardHourlyLegacy)
	if err != nil {
		t.Fatalf("inspect resumed legacy table: %v", err)
	}
	if legacyExists {
		t.Fatalf("resumed offline cleanup retained %s", usageDashboardHourlyLegacy)
	}
	var pricingRows, stalePricingRows int
	if err := db.QueryRow(`select count(*), count(*) filter (where structure_revision <> 'pricing-current')
		from usage_pricing_hourly_rollups_v1`).Scan(&pricingRows, &stalePricingRows); err != nil {
		t.Fatalf("inspect resumed pricing revisions: %v", err)
	}
	if pricingRows != 1 || stalePricingRows != 0 {
		t.Fatalf("pricing rows after resumed offline cleanup = total:%d stale:%d", pricingRows, stalePricingRows)
	}
	assertTableCount(t, db, "usage_events", 1)

	second, err := CleanupDerivedOffline(context.Background(), db)
	if err != nil {
		t.Fatalf("repeat offline cleanup: %v", err)
	}
	if second.CompletedJobs != 0 || second.ProcessedRows != 0 || second.PreparedIndexes != 0 {
		t.Fatalf("repeat offline cleanup result = %+v, want no work", second)
	}
}

func TestMonitoringFTSRecoveryAllocatesNewGenerationWhilePriorJobAwaitsOfflineCleanup(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	seedProjectionEvent := func(id int64) {
		t.Helper()
		if _, err := db.Exec(`insert into usage_events (
			id, request_id, event_hash, timestamp_ms, timestamp, model, created_at_ms
		) values (?, ?, ?, ?, ?, 'model', ?)`, id, id, id, id, id, id); err != nil {
			t.Fatalf("seed usage event %d: %v", id, err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin projection seed %d: %v", id, err)
		}
		if err := usageprojection.UpsertEventRange(ctx, tx, id-1, id, id); err != nil {
			_ = tx.Rollback()
			t.Fatalf("seed projection event %d: %v", id, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit projection event %d: %v", id, err)
		}
	}
	rebuildGeneration := func() {
		t.Helper()
		if _, err := db.Exec(`delete from settings where key = ?`, usageMonitoringModelFormatVersionKey); err != nil {
			t.Fatalf("force monitoring projection recovery: %v", err)
		}
		if err := ensureUsageMonitoringProjectionIdentity(db); err != nil {
			t.Fatalf("recover monitoring projection: %v", err)
		}
	}

	seedProjectionEvent(1)
	rebuildGeneration()
	if _, err := cleanupDerivedUntilIdle(ctx, db); err != nil {
		t.Fatalf("drain first generation online rows: %v", err)
	}
	if err := ensureUsageMonitoringSearchIndex(db); err != nil {
		t.Fatalf("recreate active monitoring search index: %v", err)
	}
	seedProjectionEvent(2)
	rebuildGeneration()

	rows, err := db.Query(`select generation, status, fts_table, projection_table
		from usage_derived_cleanup_jobs where kind = 'monitoring_fts' order by generation`)
	if err != nil {
		t.Fatalf("read monitoring cleanup generations: %v", err)
	}
	defer rows.Close()
	type generationState struct {
		generation      int64
		status          string
		ftsTable        string
		projectionTable string
	}
	states := make([]generationState, 0, 2)
	for rows.Next() {
		var state generationState
		if err := rows.Scan(&state.generation, &state.status, &state.ftsTable, &state.projectionTable); err != nil {
			t.Fatalf("scan monitoring cleanup generation: %v", err)
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate monitoring cleanup generations: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("monitoring cleanup generations = %#v, want 2", states)
	}
	if states[0].status != "offline_required" || states[1].status != "online_cleanup" {
		t.Fatalf("monitoring cleanup generation states = %#v", states)
	}
	if states[0].generation >= states[1].generation || states[0].ftsTable == states[1].ftsTable || states[0].projectionTable == states[1].projectionTable {
		t.Fatalf("monitoring cleanup generation names were not advanced: %#v", states)
	}
}

func TestMonitoringFTSCleanupDefersInconsistentPairsToOfflineFinalization(t *testing.T) {
	for _, missingTable := range []string{"fts", "projection"} {
		t.Run(missingTable, func(t *testing.T) {
			db, ftsTable, projectionTable := parkSingleMonitoringGeneration(t)
			tableName := ftsTable
			if missingTable == "projection" {
				tableName = projectionTable
			}
			if _, err := db.Exec(`drop table ` + tableName); err != nil {
				t.Fatalf("drop paired %s table: %v", missingTable, err)
			}

			processed, pending, err := cleanupDerivedBatch(context.Background(), db, 1000)
			if err != nil {
				t.Fatalf("inspect inconsistent monitoring cleanup pair: %v", err)
			}
			if processed != 0 || !pending {
				t.Fatalf("inconsistent monitoring cleanup = processed:%d pending:%t", processed, pending)
			}
			var status string
			var detail *string
			if err := db.QueryRow(`select status, last_error from usage_derived_cleanup_jobs
				where fts_table = ?`, ftsTable).Scan(&status, &detail); err != nil {
				t.Fatalf("read inconsistent monitoring cleanup job: %v", err)
			}
			if status != "offline_required" || detail == nil || *detail == "" {
				t.Fatalf("inconsistent monitoring cleanup job = status:%q detail:%v", status, detail)
			}
			if missingTable == "fts" {
				assertTableCount(t, db, projectionTable, 1)
			}
		})
	}
}

func parkSingleMonitoringGeneration(t testing.TB) (*sql.DB, string, string) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`insert into usage_events (
		id, request_id, event_hash, timestamp_ms, timestamp, model, created_at_ms
	) values (1, 'request-1', 'event-1', 1, '1', 'model', 1)`); err != nil {
		t.Fatalf("seed usage event: %v", err)
	}
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin monitoring generation fixture: %v", err)
	}
	if err := usageprojection.UpsertEventRange(ctx, tx, 0, 1, 1); err != nil {
		_ = tx.Rollback()
		t.Fatalf("seed monitoring projection: %v", err)
	}
	if err := dropUsageMonitoringSearchTriggers(tx); err != nil {
		_ = tx.Rollback()
		t.Fatalf("drop active search triggers: %v", err)
	}
	ftsTable, projectionTable, err := parkUsageMonitoringSearchGeneration(tx, true)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("park monitoring generation: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit monitoring generation fixture: %v", err)
	}
	return db, ftsTable, projectionTable
}

func TestCleanupDerivedBatchKeepsActiveSelectorRevision(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`insert into usage_monitoring_selector_daily_rollups_v1 (
			model_format_revision, bucket_ms, model, api_key_hash, provider,
			auth_file_snapshot, account_snapshot, auth_label_snapshot,
			auth_index, source, source_hash, updated_at_ms
		) values ('legacy', 0, 'legacy-model', '', '', '', '', '', '', '', '', 1)`,
		`insert into usage_monitoring_selector_daily_rollups_v1 (
			model_format_revision, bucket_ms, model, api_key_hash, provider,
			auth_file_snapshot, account_snapshot, auth_label_snapshot,
			auth_index, source, source_hash, updated_at_ms
		) values ('1', 0, 'current-model', '', '', '', '', '', '', '', '', 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed selector revision fixture: %v", err)
		}
	}
	processed, pending, err := cleanupDerivedBatch(context.Background(), db, 1000)
	if err != nil {
		t.Fatalf("cleanup selector revisions: %v", err)
	}
	if processed != 1 || !pending {
		t.Fatalf("cleanup selector revisions = processed:%d pending:%t", processed, pending)
	}
	var currentRows, legacyRows int
	if err := db.QueryRow(`select count(*) from usage_monitoring_selector_daily_rollups_v1 where model_format_revision = ?`, usageidentity.ModelFormatVersion).Scan(&currentRows); err != nil {
		t.Fatalf("count current selector rows: %v", err)
	}
	if err := db.QueryRow(`select count(*) from usage_monitoring_selector_daily_rollups_v1 where model_format_revision <> ?`, usageidentity.ModelFormatVersion).Scan(&legacyRows); err != nil {
		t.Fatalf("count legacy selector rows: %v", err)
	}
	if currentRows != 1 || legacyRows != 0 {
		t.Fatalf("selector rows after cleanup = current:%d legacy:%d", currentRows, legacyRows)
	}
}

func TestCleanupDerivedBatchScansSelectorRowsThroughPersistentBoundedCursor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`with recursive ids(id) as (
		select 1 union all select id + 1 from ids where id < 2500
	) insert into usage_monitoring_selector_daily_rollups_v1 (
		model_format_revision, bucket_ms, model, api_key_hash, provider,
		auth_file_snapshot, account_snapshot, auth_label_snapshot,
		auth_index, source, source_hash, updated_at_ms
	) select ?, id, 'current-model', '', '', '', '', '', '', '', '', 1 from ids`,
		usageidentity.ModelFormatVersion,
	); err != nil {
		_ = db.Close()
		t.Fatalf("seed current selector rows: %v", err)
	}
	if _, err := db.Exec(`insert into usage_monitoring_selector_daily_rollups_v1 (
		model_format_revision, bucket_ms, model, api_key_hash, provider,
		auth_file_snapshot, account_snapshot, auth_label_snapshot,
		auth_index, source, source_hash, updated_at_ms
	) values ('legacy', 2501, 'legacy-model', '', '', '', '', '', '', '', '', 1)`); err != nil {
		_ = db.Close()
		t.Fatalf("seed trailing stale selector row: %v", err)
	}
	var revisionIndexCount int
	if err := db.QueryRow(`select count(*) from sqlite_master where type = 'index'
		and name = 'idx_usage_monitoring_selector_revision_bucket'`).Scan(&revisionIndexCount); err != nil {
		_ = db.Close()
		t.Fatalf("inspect selector revision index: %v", err)
	}
	if revisionIndexCount != 0 {
		_ = db.Close()
		t.Fatalf("selector revision index count = %d, want deferred", revisionIndexCount)
	}

	processed, pending, err := cleanupDerivedBatch(context.Background(), db, 1000)
	if err != nil {
		_ = db.Close()
		t.Fatalf("scan first bounded selector window: %v", err)
	}
	if processed != 0 || !pending {
		_ = db.Close()
		t.Fatalf("first bounded selector window = processed:%d pending:%t", processed, pending)
	}
	assertDerivedCleanupCursor(t, db, "monitoring_selector_model_format", 1000)
	if err := db.Close(); err != nil {
		t.Fatalf("close interrupted selector cleanup: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen interrupted selector cleanup: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	processed, pending, err = cleanupDerivedBatch(context.Background(), db, 1000)
	if err != nil {
		t.Fatalf("resume second bounded selector window: %v", err)
	}
	if processed != 0 || !pending {
		t.Fatalf("second bounded selector window = processed:%d pending:%t", processed, pending)
	}
	assertDerivedCleanupCursor(t, db, "monitoring_selector_model_format", 2000)

	processed, pending, err = cleanupDerivedBatch(context.Background(), db, 1000)
	if err != nil {
		t.Fatalf("scan trailing bounded selector window: %v", err)
	}
	if processed != 1 || !pending {
		t.Fatalf("trailing bounded selector window = processed:%d pending:%t", processed, pending)
	}
	assertDerivedCleanupCursor(t, db, "monitoring_selector_model_format", 2501)
	var staleRows int
	if err := db.QueryRow(`select count(*) from usage_monitoring_selector_daily_rollups_v1
		where model_format_revision <> ?`, usageidentity.ModelFormatVersion).Scan(&staleRows); err != nil {
		t.Fatalf("count stale selector rows: %v", err)
	}
	if staleRows != 0 {
		t.Fatalf("stale selector rows after bounded scan = %d", staleRows)
	}
}

func TestCleanupDerivedBatchUsesCurrentPricingAndStatsRevisions(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, statement := range []string{
		`update usage_pricing_rollup_state set structure_revision = 'pricing-current'
			where rollup_name = 'pricing_v1'`,
		`update usage_monitoring_rollup_state set structure_revision = 'stats-current'
			where rollup_name = 'stats_v1'`,
		`insert into usage_pricing_hourly_rollups_v1 (
			structure_revision, bucket_ms, model, billing_model, pricing_model,
			service_tier, context_threshold_tokens, failed, calls, updated_at_ms
		) values
			('pricing-old', 0, 'old', 'old', 'old', '', -1, 0, 1, 1),
			('pricing-current', 0, 'current', 'current', 'current', '', -1, 0, 1, 1)`,
		`insert into usage_monitoring_account_daily_rollups_v1 (
			structure_revision, bucket_ms, account_snapshot, auth_label_snapshot,
			provider, auth_provider_snapshot, auth_index, source, source_hash,
			auth_file_snapshot, api_key_hash, executor_type, model, billing_model,
			pricing_model, service_tier, context_threshold_tokens, failed,
			calls, last_seen_ms, updated_at_ms
		) values
			('stats-old', 0, 'old', '', '', '', '', '', '', '', '', '',
				'old', 'old', 'old', '', -1, 0, 1, 1, 1),
			('stats-current', 0, 'current', '', '', '', '', '', '', '', '', '',
				'current', 'current', 'current', '', -1, 0, 1, 1, 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed revision cleanup fixture: %v", err)
		}
	}

	for index := 0; index < 2; index++ {
		processed, pending, err := cleanupDerivedBatch(context.Background(), db, 1000)
		if err != nil {
			t.Fatalf("cleanup revision batch %d: %v", index+1, err)
		}
		if processed != 1 || !pending {
			t.Fatalf("cleanup revision batch %d = processed:%d pending:%t", index+1, processed, pending)
		}
	}
	for tableName, revision := range map[string]string{
		"usage_pricing_hourly_rollups_v1": "pricing-current",
		usageMonitoringAccountDailyTable:  "stats-current",
	} {
		var rows, wrongRows int
		if err := db.QueryRow(`select count(*), count(*) filter (where structure_revision <> ?)
			from `+tableName, revision).Scan(&rows, &wrongRows); err != nil {
			t.Fatalf("inspect active rows in %s: %v", tableName, err)
		}
		if rows != 1 || wrongRows != 0 {
			t.Fatalf("active rows in %s = total:%d wrongRevision:%d", tableName, rows, wrongRows)
		}
	}

	if _, err := db.Exec(`update usage_pricing_rollup_state set structure_revision = 'pricing-next'
		where rollup_name = 'pricing_v1'`); err != nil {
		t.Fatalf("switch pricing revision: %v", err)
	}
	processed, _, err := cleanupDerivedBatch(context.Background(), db, 1000)
	if err != nil {
		t.Fatalf("cleanup after pricing revision switch: %v", err)
	}
	if processed != 1 {
		t.Fatalf("cleanup after pricing revision switch processed = %d, want 1", processed)
	}
	assertTableCount(t, db, "usage_pricing_hourly_rollups_v1", 0)
}

func TestCleanupStaleDerivedTargetRechecksRevisionInsideDeleteTransaction(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`update usage_pricing_rollup_state set structure_revision = 'pricing-old'
		where rollup_name = 'pricing_v1'`); err != nil {
		t.Fatalf("set initial pricing revision: %v", err)
	}
	targets, err := staleDerivedCleanupTargets(context.Background(), db)
	if err != nil {
		t.Fatalf("discover stale cleanup targets: %v", err)
	}
	var pricingTarget derivedCleanupTarget
	for _, target := range targets {
		if target.tableName == "usage_pricing_hourly_rollups_v1" {
			pricingTarget = target
			break
		}
	}
	if pricingTarget.tableName == "" {
		t.Fatal("pricing cleanup target was not discovered")
	}
	for _, statement := range []string{
		`update usage_pricing_rollup_state set structure_revision = 'pricing-current'
			where rollup_name = 'pricing_v1'`,
		`insert into usage_pricing_hourly_rollups_v1 (
			structure_revision, bucket_ms, model, billing_model, pricing_model,
			service_tier, context_threshold_tokens, failed, calls, updated_at_ms
		) values
			('pricing-old', 0, 'old', 'old', 'old', '', -1, 0, 1, 1),
			('pricing-current', 0, 'current', 'current', 'current', '', -1, 0, 1, 1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("prepare revision switch fixture: %v", err)
		}
	}

	processed, advanced, err := cleanupStaleDerivedTargetBatch(
		context.Background(),
		db,
		pricingTarget,
		1000,
	)
	if err != nil {
		t.Fatalf("clean stale pricing target after revision switch: %v", err)
	}
	if processed != 1 || !advanced {
		t.Fatalf("cleanup after revision switch = processed:%d advanced:%t", processed, advanced)
	}
	var oldRows, currentRows int
	if err := db.QueryRow(`select
		count(*) filter (where structure_revision = 'pricing-old'),
		count(*) filter (where structure_revision = 'pricing-current')
		from usage_pricing_hourly_rollups_v1`).Scan(&oldRows, &currentRows); err != nil {
		t.Fatalf("inspect pricing rows after revision-safe cleanup: %v", err)
	}
	if oldRows != 0 || currentRows != 1 {
		t.Fatalf("pricing rows after revision-safe cleanup = old:%d current:%d", oldRows, currentRows)
	}
}

func assertDerivedCleanupCursor(t testing.TB, db *sql.DB, targetName string, wantRowID int64) {
	t.Helper()
	var lastRowID int64
	if err := db.QueryRow(`select last_rowid from usage_derived_cleanup_cursors
		where target_name = ?`, targetName).Scan(&lastRowID); err != nil {
		t.Fatalf("read derived cleanup cursor %s: %v", targetName, err)
	}
	if lastRowID != wantRowID {
		t.Fatalf("derived cleanup cursor %s = %d, want %d", targetName, lastRowID, wantRowID)
	}
}
