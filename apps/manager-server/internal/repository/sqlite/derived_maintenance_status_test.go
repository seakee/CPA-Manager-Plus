package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	quotasnapshotrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotasnapshot"
)

func TestReadDerivedMaintenanceStatusFreshDatabaseIsClean(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "fresh.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	status, err := ReadDerivedMaintenanceStatus(context.Background(), db)
	if err != nil {
		t.Fatalf("read maintenance status: %v", err)
	}
	assertDerivedMaintenanceClean(t, status)
}

func TestReadDerivedMaintenanceStatusReportsDeferredIndexesOnNonEmptyTables(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "deferred.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, created_at_ms
	) values ('maintenance-status-event', 1, '1970-01-01T00:00:00Z', 'model', 1)`); err != nil {
		t.Fatalf("seed usage event: %v", err)
	}

	status, err := ReadDerivedMaintenanceStatus(context.Background(), db)
	if err != nil {
		t.Fatalf("read maintenance status: %v", err)
	}
	if !status.Required || !status.PerformanceDegraded || status.DeferredIndexes == 0 {
		t.Fatalf("maintenance status = %+v, want deferred degraded state", status)
	}
	if !containsString(status.Reasons, DerivedMaintenanceReasonDeferredIndexes) {
		t.Fatalf("maintenance reasons = %v, want %q", status.Reasons, DerivedMaintenanceReasonDeferredIndexes)
	}
	if status.Command != DerivedMaintenanceCommand {
		t.Fatalf("maintenance command = %q, want %q", status.Command, DerivedMaintenanceCommand)
	}
}

func TestReadDerivedMaintenanceStatusReportsOfflineCleanupJob(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "offline-job.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`insert into usage_derived_cleanup_jobs (
		generation, kind, status, fts_table, processed_rows, created_at_ms, updated_at_ms
	) values (99, 'monitoring_fts', 'offline_required', 'legacy_fts', 0, 1, 1)`); err != nil {
		t.Fatalf("seed offline cleanup job: %v", err)
	}

	status, err := ReadDerivedMaintenanceStatus(context.Background(), db)
	if err != nil {
		t.Fatalf("read maintenance status: %v", err)
	}
	if !status.Required || !status.PerformanceDegraded || status.OfflineJobs != 1 {
		t.Fatalf("maintenance status = %+v, want one offline degraded job", status)
	}
	if !containsString(status.Reasons, DerivedMaintenanceReasonOfflineDerived) {
		t.Fatalf("maintenance reasons = %v, want %q", status.Reasons, DerivedMaintenanceReasonOfflineDerived)
	}
}

func TestReadDerivedMaintenanceStatusReportsParkedProjectionIndex(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "parked-index.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`create table usage_monitoring_event_projection_v1_legacy_g000001 (
		event_id integer primary key,
		timestamp_ms integer not null
	)`); err != nil {
		t.Fatalf("create parked projection table: %v", err)
	}
	if _, err := db.Exec(`create index idx_usage_monitoring_event_projection_timestamp
		on usage_monitoring_event_projection_v1_legacy_g000001(timestamp_ms desc, event_id desc)`); err != nil {
		t.Fatalf("create parked projection index: %v", err)
	}

	status, err := ReadDerivedMaintenanceStatus(context.Background(), db)
	if err != nil {
		t.Fatalf("read maintenance status: %v", err)
	}
	if !status.Required || status.DeferredIndexes == 0 {
		t.Fatalf("maintenance status = %+v, want parked index maintenance", status)
	}
	if !containsString(status.Reasons, DerivedMaintenanceReasonLegacyIndexReplaced) {
		t.Fatalf("maintenance reasons = %v, want %q", status.Reasons, DerivedMaintenanceReasonLegacyIndexReplaced)
	}
	for _, forbidden := range []string{
		"idx_usage_monitoring_event_projection_timestamp",
		"usage_monitoring_event_projection_v1_legacy_g000001",
	} {
		if strings.Contains(strings.Join(status.Reasons, " "), forbidden) {
			t.Fatalf("maintenance reasons leak internal detail %q: %v", forbidden, status.Reasons)
		}
	}
}

func TestReadDerivedMaintenanceStatusClearsAfterOfflineCleanupAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cleanup-status.sqlite")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`insert into usage_events (
		event_hash, timestamp_ms, timestamp, model, created_at_ms
	) values ('cleanup-status-event', 1, '1970-01-01T00:00:00Z', 'model', 1)`); err != nil {
		_ = db.Close()
		t.Fatalf("seed usage event: %v", err)
	}
	beforeCount, beforeHash := usageEventsFingerprint(t, db)
	if err := RunDerivedStartupMaintenance(context.Background(), db); err != nil {
		_ = db.Close()
		t.Fatalf("run startup maintenance: %v", err)
	}
	degraded, err := ReadDerivedMaintenanceStatus(context.Background(), db)
	if err != nil {
		_ = db.Close()
		t.Fatalf("read degraded status: %v", err)
	}
	if !degraded.Required {
		_ = db.Close()
		t.Fatalf("status before cleanup = %+v, want required", degraded)
	}
	if _, err := CleanupDerivedOffline(context.Background(), db); err != nil {
		_ = db.Close()
		t.Fatalf("run offline cleanup: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite after cleanup: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("reopen sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	clean, err := ReadDerivedMaintenanceStatus(context.Background(), db)
	if err != nil {
		t.Fatalf("read clean status: %v", err)
	}
	assertDerivedMaintenanceClean(t, clean)
	afterCount, afterHash := usageEventsFingerprint(t, db)
	if beforeCount != afterCount || beforeHash != afterHash {
		t.Fatalf("usage_events changed across cleanup: before=%d/%s after=%d/%s", beforeCount, beforeHash, afterCount, afterHash)
	}
}

func TestReadDerivedMaintenanceStatusRetainsDeferredIndexAfterTargetIsEmptied(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "deferred-ledger.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`insert into usage_account_model_rollups (
		account_key, model, billing_model, service_tier,
		first_seen_ms, last_seen_ms, updated_at_ms
	) values ('account-1', 'model', 'model', '', 1, 1, 1)`); err != nil {
		t.Fatalf("seed derived rollup: %v", err)
	}
	if err := RunDerivedStartupMaintenance(context.Background(), db); err != nil {
		t.Fatalf("run startup maintenance: %v", err)
	}
	degraded, err := ReadDerivedMaintenanceStatus(context.Background(), db)
	if err != nil {
		t.Fatalf("read deferred status: %v", err)
	}
	if !degraded.Required || degraded.DeferredIndexes == 0 {
		t.Fatalf("degraded status = %+v, want deferred index", degraded)
	}
	var ledgerRows int
	if err := db.QueryRow(`select count(*) from usage_derived_deferred_indexes`).Scan(&ledgerRows); err != nil {
		t.Fatalf("count deferred index ledger: %v", err)
	}
	if ledgerRows == 0 {
		t.Fatal("deferred index ledger is empty")
	}
	if _, err := db.Exec(`delete from usage_account_model_rollups`); err != nil {
		t.Fatalf("empty derived rollup: %v", err)
	}
	stillDegraded, err := ReadDerivedMaintenanceStatus(context.Background(), db)
	if err != nil {
		t.Fatalf("read status after target emptied: %v", err)
	}
	if !stillDegraded.Required || stillDegraded.DeferredIndexes == 0 {
		t.Fatalf("status after target emptied = %+v, want persisted deferred state", stillDegraded)
	}
	if _, err := CleanupDerivedOffline(context.Background(), db); err != nil {
		t.Fatalf("complete deferred index cleanup: %v", err)
	}
	clean, err := ReadDerivedMaintenanceStatus(context.Background(), db)
	if err != nil {
		t.Fatalf("read clean status: %v", err)
	}
	assertDerivedMaintenanceClean(t, clean)
}

func TestReadDerivedMaintenanceStatusIgnoresLedgerForMissingTargetTable(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "missing-target-ledger.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`insert into usage_derived_deferred_indexes (
		index_name, table_name, reason, created_at_ms, updated_at_ms
	) values ('idx_usage_account_model_rollups_last_seen',
		'parked_derived_table_that_is_gone', 'deferred_indexes', 1, 1)`); err != nil {
		t.Fatalf("seed stale deferred index ledger: %v", err)
	}

	status, err := ReadDerivedMaintenanceStatus(context.Background(), db)
	if err != nil {
		t.Fatalf("read maintenance status: %v", err)
	}
	assertDerivedMaintenanceClean(t, status)
}

func TestReadDerivedMaintenanceStatusRechecksCurrentTargetAfterStaleLedgerTargetDisappears(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "stale-ledger-target.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`insert into usage_account_model_rollups (
		account_key, model, billing_model, service_tier,
		first_seen_ms, last_seen_ms, updated_at_ms
	) values ('account-1', 'model', 'model', '', 1, 1, 1)`); err != nil {
		t.Fatalf("seed current derived target: %v", err)
	}
	if _, err := db.Exec(`insert into usage_derived_deferred_indexes (
		index_name, table_name, reason, created_at_ms, updated_at_ms
	) values ('idx_usage_account_model_rollups_last_seen',
		'parked_derived_table_that_is_gone', 'deferred_indexes', 1, 1)`); err != nil {
		t.Fatalf("seed stale deferred index ledger: %v", err)
	}

	status, err := ReadDerivedMaintenanceStatus(context.Background(), db)
	if err != nil {
		t.Fatalf("read maintenance status: %v", err)
	}
	if !status.Required || status.DeferredIndexes == 0 {
		t.Fatalf("maintenance status = %+v, want current non-empty target to remain degraded", status)
	}
	if !containsString(status.Reasons, DerivedMaintenanceReasonDeferredIndexes) {
		t.Fatalf("maintenance reasons = %v, want %q", status.Reasons, DerivedMaintenanceReasonDeferredIndexes)
	}
}

func TestReadDerivedMaintenanceStatusReportsOfflineQuotaSnapshotMigration(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "offline-quota.sqlite"))
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
			'inspection', 'oversized-status-group', 1000, 'exact', 3600, 25, 75, ?)`,
			windowID, windowKind, 1000+index,
		); err != nil {
			t.Fatalf("seed quota snapshot %s: %v", windowID, err)
		}
	}
	_, err = quotasnapshotrepo.BackfillLegacySnapshotsBatch(context.Background(), db, 1)
	if !errors.Is(err, quotasnapshotrepo.ErrLegacySnapshotGroupTooLarge) {
		t.Fatalf("oversized quota migration error = %v", err)
	}
	if err := quotasnapshotrepo.RecordLegacyBackfillFailure(context.Background(), db, err); err != nil {
		t.Fatalf("record offline quota migration: %v", err)
	}
	status, err := ReadDerivedMaintenanceStatus(context.Background(), db)
	if err != nil {
		t.Fatalf("read offline quota status: %v", err)
	}
	if !status.Required || status.OfflineJobs != 1 || !containsString(status.Reasons, DerivedMaintenanceReasonOfflineQuota) {
		t.Fatalf("offline quota status = %+v", status)
	}
	if _, err := CleanupDerivedOffline(context.Background(), db); err != nil {
		t.Fatalf("complete offline quota migration: %v", err)
	}
	clean, err := ReadDerivedMaintenanceStatus(context.Background(), db)
	if err != nil {
		t.Fatalf("read clean quota status: %v", err)
	}
	assertDerivedMaintenanceClean(t, clean)
}

func TestReadDerivedMaintenanceStatusRecognizesLegacyFailedQuotaMigrationMarker(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "legacy-offline-quota.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`update usage_data_migrations set status = 'failed',
		last_error = ? where name = ?`,
		quotasnapshotrepo.LegacySnapshotOfflineErrorMarker+" 1000",
		quotasnapshotrepo.LegacySnapshotMigrationName,
	); err != nil {
		t.Fatalf("seed legacy failed migration marker: %v", err)
	}
	status, err := ReadDerivedMaintenanceStatus(context.Background(), db)
	if err != nil {
		t.Fatalf("read legacy offline quota status: %v", err)
	}
	if !status.Required || status.OfflineJobs != 1 || !containsString(status.Reasons, DerivedMaintenanceReasonOfflineQuota) {
		t.Fatalf("legacy offline quota status = %+v", status)
	}
}

func TestReadDerivedMaintenanceStatusIgnoresUnrelatedFailedQuotaMigration(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "unrelated-failed-quota.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`update usage_data_migrations set status = 'failed',
		last_error = 'temporary write failure' where name = ?`,
		quotasnapshotrepo.LegacySnapshotMigrationName,
	); err != nil {
		t.Fatalf("seed unrelated quota migration failure: %v", err)
	}
	status, err := ReadDerivedMaintenanceStatus(context.Background(), db)
	if err != nil {
		t.Fatalf("read unrelated failed quota status: %v", err)
	}
	assertDerivedMaintenanceClean(t, status)
}

func assertDerivedMaintenanceClean(t *testing.T, status DerivedMaintenanceStatus) {
	t.Helper()
	if status.Required || status.PerformanceDegraded || status.DeferredIndexes != 0 || status.OfflineJobs != 0 {
		t.Fatalf("maintenance status = %+v, want clean state", status)
	}
	if len(status.Reasons) != 0 || status.Command != "" {
		t.Fatalf("clean maintenance status = %+v, want no reasons or command", status)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func usageEventsFingerprint(t *testing.T, db *sql.DB) (count int64, hash string) {
	t.Helper()
	if err := db.QueryRow(`select count(*), coalesce(group_concat(event_hash, '|'), '')
		from (select event_hash from usage_events order by id)`).Scan(&count, &hash); err != nil {
		t.Fatalf("fingerprint usage_events: %v", err)
	}
	return count, hash
}
