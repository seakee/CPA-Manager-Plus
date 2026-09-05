package quotasnapshot_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	quotasnapshotrepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotasnapshot"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestBackfillLegacySnapshotsBatchProcessesWholeGroupsAndResumes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	seedLegacySnapshot(t, db, "account-codex", "codex", "weekly", "weekly", "inspection", "codex-1", 1000)
	seedLegacySnapshot(t, db, "account-codex", "codex", "five-hour", "five_hour", "inspection", "codex-1", 1000)
	seedLegacySnapshot(t, db, "account-xai", "xai", "included-free-rolling-24h", "rolling_24h", "response_body", "xai-1", 2000)
	repository := quotasnapshotrepo.New(db)
	candidates, err := repository.ListCandidates(context.Background(), "account-codex", "codex", 10)
	if err != nil {
		t.Fatalf("list legacy candidates before backfill: %v", err)
	}
	if len(candidates) != 2 || candidates[0].ObservationID != 0 || candidates[1].ObservationID != 0 {
		t.Fatalf("legacy candidate fallback before backfill = %#v", candidates)
	}

	result, err := quotasnapshotrepo.BackfillLegacySnapshotsBatch(context.Background(), db, 10)
	if err != nil {
		t.Fatalf("backfill first legacy group: %v", err)
	}
	if result.Processed != 2 || result.LastSnapshotID != 2 || !result.Pending || result.Completed {
		t.Fatalf("first legacy group result = %#v", result)
	}
	assertLegacySnapshotAttachment(t, db, 1, true)
	assertLegacySnapshotAttachment(t, db, 2, true)
	assertLegacySnapshotAttachment(t, db, 3, false)
	candidates, err = repository.ListCandidates(context.Background(), "account-codex", "codex", 10)
	if err != nil {
		t.Fatalf("list candidates after first backfill: %v", err)
	}
	if len(candidates) != 2 || candidates[0].ObservationID == 0 || candidates[1].ObservationID == 0 {
		t.Fatalf("candidate fallback after first backfill = %#v", candidates)
	}
	var relationshipKind, containerID string
	if err := db.QueryRow(`select coalesce(relationship_kind, ''),
		coalesce(container_provider_window_id, '') from account_quota_windows
		where account_key = 'account-codex' and provider_window_id = 'five-hour'`).Scan(
		&relationshipKind,
		&containerID,
	); err != nil {
		t.Fatalf("read migrated Codex relationship: %v", err)
	}
	if relationshipKind != "concurrent_subwindow" || containerID != "weekly" {
		t.Fatalf("migrated Codex relationship = %q/%q", relationshipKind, containerID)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close interrupted migration database: %v", err)
	}

	db, err = sqliterepo.Open(path)
	if err != nil {
		t.Fatalf("reopen interrupted migration database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	result, err = quotasnapshotrepo.BackfillLegacySnapshotsBatch(context.Background(), db, 10)
	if err != nil {
		t.Fatalf("resume legacy migration: %v", err)
	}
	if result.Processed != 1 || result.LastSnapshotID != 3 || result.Pending || !result.Completed {
		t.Fatalf("resumed legacy group result = %#v", result)
	}
	assertLegacySnapshotAttachment(t, db, 3, true)
	var inventoryScope string
	if err := db.QueryRow(`select inventory_scope_key from account_quota_windows
		where account_key = 'account-xai'`).Scan(&inventoryScope); err != nil {
		t.Fatalf("read migrated xAI inventory scope: %v", err)
	}
	if inventoryScope != "xai:included-free" {
		t.Fatalf("migrated xAI inventory scope = %q, want xai:included-free", inventoryScope)
	}

	result, err = quotasnapshotrepo.BackfillLegacySnapshotsBatch(context.Background(), db, 10)
	if err != nil {
		t.Fatalf("repeat completed legacy migration: %v", err)
	}
	if result.Processed != 0 || !result.Completed {
		t.Fatalf("repeated completed migration result = %#v", result)
	}
	var observations, snapshots, processedRows int
	var status string
	if err := db.QueryRow(`select count(*) from account_quota_observations`).Scan(&observations); err != nil {
		t.Fatalf("count migrated observations: %v", err)
	}
	if err := db.QueryRow(`select count(*) from account_quota_snapshots`).Scan(&snapshots); err != nil {
		t.Fatalf("count preserved snapshots: %v", err)
	}
	if err := db.QueryRow(`select status, processed_rows from usage_data_migrations where name = ?`,
		quotasnapshotrepo.LegacySnapshotMigrationName,
	).Scan(&status, &processedRows); err != nil {
		t.Fatalf("read completed migration state: %v", err)
	}
	if observations != 2 || snapshots != 3 || status != "completed" || processedRows != 3 {
		t.Fatalf("completed migration = observations:%d snapshots:%d status:%q processed:%d", observations, snapshots, status, processedRows)
	}
}

func TestBackfillLegacySnapshotsBatchLeavesLegacyCodexWorkspaceSnapshotsOrphaned(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	legacyKey := "usage-account-history:3:codex-account:636F646578:776F726B73706163652D31"
	seedLegacySnapshot(t, db, legacyKey, "codex", "five-hour", "five_hour", "api_query", "legacy-workspace", 1000)

	result, err := quotasnapshotrepo.BackfillLegacySnapshotsBatch(context.Background(), db, 1000)
	if err != nil {
		t.Fatalf("backfill legacy Codex workspace snapshot: %v", err)
	}
	if result.Processed != 0 || result.Pending || !result.Completed {
		t.Fatalf("legacy Codex workspace backfill result = %#v", result)
	}
	assertLegacySnapshotAttachment(t, db, 1, false)

	var windowCount int
	if err := db.QueryRow(`select count(*) from account_quota_windows where account_key = ?`, legacyKey).Scan(&windowCount); err != nil {
		t.Fatalf("count orphaned Codex workspace windows: %v", err)
	}
	if windowCount != 0 {
		t.Fatalf("orphaned Codex workspace snapshot created %d lifecycle windows", windowCount)
	}
	candidates, err := quotasnapshotrepo.New(db).ListCandidates(context.Background(), legacyKey, "codex", 10)
	if err != nil {
		t.Fatalf("list orphaned Codex workspace candidates: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("orphaned Codex workspace candidates = %#v, want none", candidates)
	}
	states, err := quotasnapshotrepo.New(db).ListWindowStates(context.Background(), legacyKey, "codex")
	if err != nil {
		t.Fatalf("list orphaned Codex workspace states: %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("orphaned Codex workspace states = %#v, want none", states)
	}
	var status string
	if err := db.QueryRow(`select status from usage_data_migrations where name = ?`, quotasnapshotrepo.LegacySnapshotMigrationName).Scan(&status); err != nil {
		t.Fatalf("read orphaned Codex workspace migration status: %v", err)
	}
	if status != "completed" {
		t.Fatalf("orphaned Codex workspace migration status = %q, want completed", status)
	}
}

func TestQuotaReadersHideAttachedLegacyCodexWorkspaceRows(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	legacyKey := "usage-account-history:3:codex-account:636F646578:776F726B73706163652D31"
	scopeFingerprint := quotasnapshotrepo.ScopeFingerprint("all", "", nil)
	observationResult, err := db.Exec(`insert into account_quota_observations (
		observation_hash, account_key, provider, source, source_observation_id,
		inventory_scope_key, inventory_mode, observed_at_ms, window_count,
		lifecycle_applied, created_at_ms
	) values ('legacy-observation-hash', ?, 'codex', 'api_query', 'legacy-observation',
		'codex:rate-limits', 'partial', 1000, 1, 1, 1000)`, legacyKey)
	if err != nil {
		t.Fatalf("insert legacy observation: %v", err)
	}
	observationID, err := observationResult.LastInsertId()
	if err != nil {
		t.Fatalf("read legacy observation id: %v", err)
	}
	windowResult, err := db.Exec(`insert into account_quota_windows (
		account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, model_scope_key, model_ids_json, scope_fingerprint,
		inventory_scope_key, relationship_kind, container_provider_window_id,
		availability, generation, absence_count, first_seen_at_ms, last_seen_at_ms,
		last_observation_id, created_at_ms, updated_at_ms
	) values (?, 'codex', 'five-hour', 'five_hour', 'fixed', 'all', null, '[]', ?,
		'codex:rate-limits', null, null, 'active', 1, 0, 1000, 1000, ?, 1000, 1000)`,
		legacyKey, scopeFingerprint, observationID)
	if err != nil {
		t.Fatalf("insert legacy lifecycle window: %v", err)
	}
	windowID, err := windowResult.LastInsertId()
	if err != nil {
		t.Fatalf("read legacy lifecycle window id: %v", err)
	}
	if _, err := db.Exec(`insert into account_quota_snapshots (
		observation_id, logical_window_id, account_key, provider, provider_window_id,
		window_kind, window_mode, model_scope_kind, model_ids_json, scope_fingerprint,
		source, source_observation_id, observed_at_ms, boundary_accuracy,
		duration_seconds, used_percent, remaining_percent, created_at_ms
	) values (?, ?, ?, 'codex', 'five-hour', 'five_hour', 'fixed', 'all', '[]', ?,
		'api_query', 'legacy-observation', 1000, 'exact', 18000, 99, 1, 1000)`,
		observationID, windowID, legacyKey, scopeFingerprint); err != nil {
		t.Fatalf("insert attached legacy snapshot: %v", err)
	}

	repository := quotasnapshotrepo.New(db)
	candidates, err := repository.ListCandidates(context.Background(), legacyKey, "codex", 10)
	if err != nil {
		t.Fatalf("list attached legacy candidates: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("attached legacy Codex candidates = %#v, want none", candidates)
	}
	states, err := repository.ListWindowStates(context.Background(), legacyKey, "codex")
	if err != nil {
		t.Fatalf("list attached legacy window states: %v", err)
	}
	if len(states) != 0 {
		t.Fatalf("attached legacy Codex window states = %#v, want none", states)
	}
	legacyStates, err := repository.ListLegacyCodexWorkspaceWindowStates(context.Background(), legacyKey, "codex")
	if err != nil {
		t.Fatalf("list attached legacy window states through compatibility reader: %v", err)
	}
	if len(legacyStates) != 1 || legacyStates[0].ID != windowID || legacyStates[0].AccountKey != legacyKey {
		t.Fatalf("legacy compatibility window states = %#v, want window %d", legacyStates, windowID)
	}

	memberKey := strings.Replace(legacyKey, ":codex-account:", ":codex-member:", 1)
	memberWindowResult, err := db.Exec(`insert into account_quota_windows (
		account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, model_scope_key, model_ids_json, scope_fingerprint,
		inventory_scope_key, relationship_kind, container_provider_window_id,
		availability, generation, absence_count, first_seen_at_ms, last_seen_at_ms,
		last_observation_id, created_at_ms, updated_at_ms
	) select ?, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, model_scope_key, model_ids_json, scope_fingerprint,
		inventory_scope_key, relationship_kind, container_provider_window_id,
		availability, generation, absence_count, first_seen_at_ms, last_seen_at_ms,
		last_observation_id, created_at_ms, updated_at_ms
		from account_quota_windows where id = ?`, memberKey, windowID)
	if err != nil {
		t.Fatalf("insert member lifecycle window: %v", err)
	}
	memberWindowID, err := memberWindowResult.LastInsertId()
	if err != nil {
		t.Fatalf("read member lifecycle window id: %v", err)
	}
	memberStates, err := repository.ListWindowStates(context.Background(), memberKey, "codex")
	if err != nil {
		t.Fatalf("list member window states: %v", err)
	}
	if len(memberStates) != 1 || memberStates[0].ID != memberWindowID {
		t.Fatalf("member window states = %#v, want window %d", memberStates, memberWindowID)
	}
	legacyMemberStates, err := repository.ListLegacyCodexWorkspaceWindowStates(context.Background(), memberKey, "codex")
	if err != nil {
		t.Fatalf("list member states through legacy compatibility reader: %v", err)
	}
	if len(legacyMemberStates) != 0 {
		t.Fatalf("legacy compatibility reader exposed member states = %#v", legacyMemberStates)
	}
}

func TestQuotaReadersHideLegacyCodexWorkspaceRowsWithoutProviderMetadata(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	legacyKey := "usage-account-history:3:codex-account:636F646578:776F726B73706163652D31"
	if _, err := db.Exec(`insert into account_quota_snapshots (
		account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, source, source_observation_id, observed_at_ms,
		boundary_accuracy, used_percent, created_at_ms
	) values (?, '', 'five-hour', 'five_hour', 'fixed', 'all',
		'api_query', 'legacy-without-provider', 1000, 'exact', 99, 1000)`, legacyKey); err != nil {
		t.Fatalf("insert legacy snapshot without provider: %v", err)
	}

	repository := quotasnapshotrepo.New(db)
	candidates, err := repository.ListCandidates(context.Background(), legacyKey, "", 10)
	if err != nil {
		t.Fatalf("list legacy candidates without provider: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("legacy candidates without provider = %#v, want none", candidates)
	}
}

func TestBackfillLegacySnapshotsBatchRejectsOversizedGroupAtomically(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seedLegacySnapshot(t, db, "account-1", "codex", "weekly", "weekly", "inspection", "group-1", 1000)
	seedLegacySnapshot(t, db, "account-1", "codex", "five-hour", "five_hour", "inspection", "group-1", 1000)

	_, err = quotasnapshotrepo.BackfillLegacySnapshotsBatch(context.Background(), db, 1)
	if !errors.Is(err, quotasnapshotrepo.ErrLegacySnapshotGroupTooLarge) || !strings.Contains(err.Error(), "exceeds safe batch limit 1") {
		t.Fatalf("oversized group error = %v", err)
	}
	var attached, observations int
	if err := db.QueryRow(`select count(*) from account_quota_snapshots where observation_id is not null`).Scan(&attached); err != nil {
		t.Fatalf("count partially attached snapshots: %v", err)
	}
	if err := db.QueryRow(`select count(*) from account_quota_observations`).Scan(&observations); err != nil {
		t.Fatalf("count partially inserted observations: %v", err)
	}
	if attached != 0 || observations != 0 {
		t.Fatalf("oversized group partially migrated snapshots:%d observations:%d", attached, observations)
	}

	result, err := quotasnapshotrepo.BackfillLegacySnapshotsBatch(context.Background(), db, 2)
	if err != nil {
		t.Fatalf("retry oversized group with sufficient limit: %v", err)
	}
	if result.Processed != 2 || !result.Completed {
		t.Fatalf("retried group result = %#v", result)
	}
}

func TestRecordLegacyBackfillFailureMarksOversizedGroupOfflineRequired(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	migrationErr := quotasnapshotrepo.LegacySnapshotGroupTooLargeError{Limit: 1}
	if err := quotasnapshotrepo.RecordLegacyBackfillFailure(context.Background(), db, migrationErr); err != nil {
		t.Fatalf("record oversized migration state: %v", err)
	}
	var status string
	var finishedAt sql.NullInt64
	if err := db.QueryRow(`select status, finished_at_ms from usage_data_migrations
		where name = ?`, quotasnapshotrepo.LegacySnapshotMigrationName).Scan(&status, &finishedAt); err != nil {
		t.Fatalf("read oversized migration state: %v", err)
	}
	if status != "offline_required" || finishedAt.Valid {
		t.Fatalf("oversized migration state = status:%q finished:%v", status, finishedAt)
	}
}

func TestRecordLegacyBackfillFailureKeepsRetryableFailureState(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := quotasnapshotrepo.RecordLegacyBackfillFailure(context.Background(), db, errors.New("temporary failure")); err != nil {
		t.Fatalf("record retryable migration failure: %v", err)
	}
	var status string
	var finishedAt sql.NullInt64
	if err := db.QueryRow(`select status, finished_at_ms from usage_data_migrations
		where name = ?`, quotasnapshotrepo.LegacySnapshotMigrationName).Scan(&status, &finishedAt); err != nil {
		t.Fatalf("read retryable migration state: %v", err)
	}
	if status != "failed" || !finishedAt.Valid {
		t.Fatalf("retryable migration state = status:%q finished:%v", status, finishedAt)
	}
}

func TestBackfillLegacySnapshotsBatchRepairsUnattachedSnapshotsForExistingObservation(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seedLegacySnapshot(t, db, "account-1", "codex", "weekly", "weekly", "inspection", "group-1", 1000)

	result, err := quotasnapshotrepo.BackfillLegacySnapshotsBatch(context.Background(), db, 10)
	if err != nil {
		t.Fatalf("create initial legacy observation: %v", err)
	}
	if result.Processed != 1 || !result.Completed {
		t.Fatalf("initial legacy observation result = %#v", result)
	}
	if _, err := db.Exec(`update account_quota_snapshots set
		observation_id = null, logical_window_id = null, activation_id = null, cycle_id = null
		where id = 1`); err != nil {
		t.Fatalf("detach legacy snapshot from existing observation: %v", err)
	}

	result, err = quotasnapshotrepo.BackfillLegacySnapshotsBatch(context.Background(), db, 10)
	if err != nil {
		t.Fatalf("repair detached legacy snapshot: %v", err)
	}
	if result.Processed != 1 || result.Pending || !result.Completed {
		t.Fatalf("repaired legacy observation result = %#v", result)
	}
	assertLegacySnapshotAttachment(t, db, 1, true)
	var observations, windows, activations, cycles int
	for query, destination := range map[string]*int{
		`select count(*) from account_quota_observations`:       &observations,
		`select count(*) from account_quota_windows`:            &windows,
		`select count(*) from account_quota_window_activations`: &activations,
		`select count(*) from account_quota_cycles`:             &cycles,
	} {
		if err := db.QueryRow(query).Scan(destination); err != nil {
			t.Fatalf("inspect repaired lifecycle state: %v", err)
		}
	}
	if observations != 1 || windows != 1 || activations != 1 || cycles != 0 {
		t.Fatalf("repaired lifecycle duplicated state observations:%d windows:%d activations:%d cycles:%d", observations, windows, activations, cycles)
	}
}

func TestBackfillLegacySnapshotsBatchRollsBackLifecycleWhenProgressUpdateFails(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seedLegacySnapshot(t, db, "account-1", "codex", "weekly", "weekly", "inspection", "group-1", 1000)
	if _, err := db.Exec(`create trigger reject_legacy_migration_progress
		before update on usage_data_migrations
		when new.name = 'quota_snapshot_lifecycle_v1'
		begin select raise(abort, 'blocked progress update'); end`); err != nil {
		t.Fatalf("create progress failure trigger: %v", err)
	}

	if _, err := quotasnapshotrepo.BackfillLegacySnapshotsBatch(context.Background(), db, 10); err == nil || !strings.Contains(err.Error(), "blocked progress update") {
		t.Fatalf("progress update failure = %v", err)
	}
	assertLegacySnapshotAttachment(t, db, 1, false)
	var observations, windows, activations, cycles int
	for query, destination := range map[string]*int{
		`select count(*) from account_quota_observations`:       &observations,
		`select count(*) from account_quota_windows`:            &windows,
		`select count(*) from account_quota_window_activations`: &activations,
		`select count(*) from account_quota_cycles`:             &cycles,
	} {
		if err := db.QueryRow(query).Scan(destination); err != nil {
			t.Fatalf("inspect rolled-back lifecycle state: %v", err)
		}
	}
	if observations != 0 || windows != 0 || activations != 0 || cycles != 0 {
		t.Fatalf("lifecycle state committed without progress observations:%d windows:%d activations:%d cycles:%d", observations, windows, activations, cycles)
	}

	if _, err := db.Exec(`drop trigger reject_legacy_migration_progress`); err != nil {
		t.Fatalf("drop progress failure trigger: %v", err)
	}
	result, err := quotasnapshotrepo.BackfillLegacySnapshotsBatch(context.Background(), db, 10)
	if err != nil {
		t.Fatalf("retry legacy migration after progress failure: %v", err)
	}
	if result.Processed != 1 || !result.Completed {
		t.Fatalf("retried migration result = %#v", result)
	}
	assertLegacySnapshotAttachment(t, db, 1, true)
}

func TestBackfillLegacySnapshotsBatchUsesLifecycleOrderInsteadOfInsertionOrder(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	seedLegacySnapshot(t, db, "account-1", "codex", "weekly", "weekly", "inspection", "newer", 2000)
	seedLegacySnapshot(t, db, "account-1", "codex", "weekly", "weekly", "response_body", "older", 1000)

	result, err := quotasnapshotrepo.BackfillLegacySnapshotsBatch(context.Background(), db, 10)
	if err != nil {
		t.Fatalf("backfill earliest lifecycle group: %v", err)
	}
	if result.Processed != 1 || result.LastSnapshotID != 2 || !result.Pending {
		t.Fatalf("earliest lifecycle group result = %#v", result)
	}
	assertLegacySnapshotAttachment(t, db, 1, false)
	assertLegacySnapshotAttachment(t, db, 2, true)

	result, err = quotasnapshotrepo.BackfillLegacySnapshotsBatch(context.Background(), db, 10)
	if err != nil {
		t.Fatalf("backfill later lifecycle group: %v", err)
	}
	if result.Processed != 1 || !result.Completed {
		t.Fatalf("later lifecycle group result = %#v", result)
	}
	var appliedRows int
	if err := db.QueryRow(`select count(*) from account_quota_observations where lifecycle_applied = 1`).Scan(&appliedRows); err != nil {
		t.Fatalf("count lifecycle-applied observations: %v", err)
	}
	if appliedRows != 2 {
		t.Fatalf("lifecycle-applied observations = %d, want 2", appliedRows)
	}
}

func TestListCandidatesKeepsDistinctLegacyModelScopes(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for index := 0; index < 9; index++ {
		modelIDs := fmt.Sprintf(`["model-%d"]`, index)
		if _, err := db.Exec(`insert into account_quota_snapshots (
			account_key, provider, provider_window_id, window_kind, window_mode,
			model_scope_kind, model_scope_key, model_ids_json, source,
			source_observation_id, observed_at_ms, boundary_accuracy,
			duration_seconds, used_percent, remaining_percent, created_at_ms
		) values ('account-1', 'codex', 'model-window', 'model_quota', 'fixed',
			'models', 'shared', ?, 'inspection', ?, ?, 'exact', 3600, 25, 75, ?)`,
			modelIDs,
			fmt.Sprintf("legacy-model-%d", index),
			1000+index,
			1000+index,
		); err != nil {
			t.Fatalf("seed legacy model scope %d: %v", index, err)
		}
	}

	candidates, err := quotasnapshotrepo.New(db).ListCandidates(context.Background(), "account-1", "codex", 100)
	if err != nil {
		t.Fatalf("list legacy model scopes: %v", err)
	}
	if len(candidates) != 9 {
		t.Fatalf("legacy model-scope candidates = %d, want 9: %#v", len(candidates), candidates)
	}
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		seen[candidate.ModelIDsJSON] = struct{}{}
	}
	if len(seen) != 9 {
		t.Fatalf("distinct legacy model identities = %d, want 9", len(seen))
	}
}

func seedLegacySnapshot(
	t *testing.T,
	db *sql.DB,
	accountKey, provider, providerWindowID, windowKind, source, sourceObservationID string,
	observedAtMS int64,
) {
	t.Helper()
	if _, err := db.Exec(`insert into account_quota_snapshots (
		account_key, provider, provider_window_id, window_kind, window_mode,
		model_scope_kind, source, source_observation_id, observed_at_ms,
		boundary_accuracy, duration_seconds, used_percent, remaining_percent,
		created_at_ms
	) values (?, ?, ?, ?, 'fixed', 'all', ?, ?, ?, 'exact', 3600, 25, 75, ?)`,
		accountKey,
		provider,
		providerWindowID,
		windowKind,
		source,
		sourceObservationID,
		observedAtMS,
		observedAtMS,
	); err != nil {
		t.Fatalf("seed legacy snapshot %s: %v", providerWindowID, err)
	}
}

func assertLegacySnapshotAttachment(t *testing.T, db *sql.DB, id int64, wantAttached bool) {
	t.Helper()
	var observationID sql.NullInt64
	if err := db.QueryRow(`select observation_id from account_quota_snapshots where id = ?`, id).Scan(&observationID); err != nil {
		t.Fatalf("read legacy snapshot %d: %v", id, err)
	}
	if observationID.Valid != wantAttached {
		t.Fatalf("legacy snapshot %d attachment = %v, want %t", id, observationID, wantAttached)
	}
}
