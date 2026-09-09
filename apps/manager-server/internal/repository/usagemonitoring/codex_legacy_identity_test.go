package usagemonitoring_test

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usageevent"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/usagemonitoring"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/usageidentity"
)

func TestCodexLegacyIdentityEvidenceRollupResumesAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, st := openMonitoringRepositoryStore(t, dbPath)
	defer func() { _ = db.Close() }()
	seedCodexLegacyIdentityEvents(t, db, 2001)
	result, err := st.CatchUpCodexLegacyIdentityEvidence(ctx, 100_000, 1)
	if err != nil || result.Processed != 1000 || result.CoverageEventID != 1000 || !result.Pending {
		t.Fatalf("first bounded batch = %+v err=%v", result, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, st = openMonitoringRepositoryStore(t, dbPath)
	state, err := st.UsageMonitoringState(ctx, usageevent.CodexLegacyIdentityRollupName)
	if err != nil || state.CoverageEventID != 1000 || state.ProcessedEvents != 1000 {
		t.Fatalf("reopened checkpoint = %+v err=%v", state, err)
	}
	result, err = st.CatchUpCodexLegacyIdentityEvidence(ctx, 1000, 2)
	if err != nil || result.Processed != 1000 || result.CoverageEventID != 2000 {
		t.Fatalf("resumed batch = %+v err=%v", result, err)
	}
	assertCodexEvidenceAlias(t, st, true)
	result, err = st.CatchUpCodexLegacyIdentityEvidence(ctx, 1000, 3)
	if err != nil || result.Processed != 1 || result.Pending || result.CoverageEventID != 2001 {
		t.Fatalf("final batch = %+v err=%v", result, err)
	}
	result, err = st.CatchUpCodexLegacyIdentityEvidence(ctx, 1000, 4)
	if err != nil || result.Processed != 0 || result.Pending {
		t.Fatalf("idempotent catch-up = %+v err=%v", result, err)
	}
	assertCodexEvidenceAlias(t, st, true)
	var groups int
	if err := db.QueryRow("select count(*) from " + usageevent.CodexLegacyIdentityEvidenceTable).Scan(&groups); err != nil || groups != 6 {
		t.Fatalf("compacted groups = %d err=%v, want 6", groups, err)
	}
	assertCodexEvidenceRawEvents(t, db, 2001)
}

func TestCodexLegacyIdentityEvidenceRollupCommitsCheckpointAtomically(t *testing.T) {
	ctx := context.Background()
	db, st := newMonitoringRepositoryStore(t)
	seedCodexLegacyIdentityEvents(t, db, 3)
	if _, err := db.Exec(`create trigger fail_identity_checkpoint before update of coverage_event_id on usage_monitoring_rollup_state
		when new.rollup_name = 'codex_legacy_identity_v1' and new.coverage_event_id > 0
		begin select raise(abort, 'checkpoint failure'); end`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CatchUpCodexLegacyIdentityEvidence(ctx, 1000, 1); err == nil {
		t.Fatal("expected checkpoint failure")
	}
	state, err := st.UsageMonitoringState(ctx, usageevent.CodexLegacyIdentityRollupName)
	if err != nil || state.CoverageEventID != 0 || state.ProcessedEvents != 0 {
		t.Fatalf("failed checkpoint = %+v err=%v", state, err)
	}
	var groups int
	if err := db.QueryRow("select count(*) from " + usageevent.CodexLegacyIdentityEvidenceTable).Scan(&groups); err != nil || groups != 0 {
		t.Fatalf("uncommitted evidence survived = %d err=%v", groups, err)
	}
	if _, err := db.Exec("drop trigger fail_identity_checkpoint"); err != nil {
		t.Fatal(err)
	}
	finishCodexEvidenceRollup(t, st, 1)
	assertCodexEvidenceAlias(t, st, true)
	assertCodexEvidenceRawEvents(t, db, 3)
}

func TestCodexLegacyIdentityEvidenceRevisionClearsInBoundedBatches(t *testing.T) {
	ctx := context.Background()
	db, st := newMonitoringRepositoryStore(t)
	seedCodexLegacyIdentityEvents(t, db, 5)
	finishCodexEvidenceRollup(t, st, 1000)
	if _, err := db.Exec("update " + usageevent.CodexLegacyIdentityEvidenceTable + " set structure_revision = 'old', auth_account_id_snapshot = auth_account_id_snapshot || '-conflict'"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("update usage_monitoring_rollup_state set structure_revision = 'old' where rollup_name = 'codex_legacy_identity_v1'"); err != nil {
		t.Fatal(err)
	}
	var before int
	if err := db.QueryRow("select count(*) from " + usageevent.CodexLegacyIdentityEvidenceTable).Scan(&before); err != nil {
		t.Fatal(err)
	}
	result, err := st.CatchUpCodexLegacyIdentityEvidence(ctx, 1, 1)
	if err != nil || result.Processed != 0 || !result.Pending || !result.ContinueSoon {
		t.Fatalf("clearing batch = %+v err=%v", result, err)
	}
	var after int
	if err := db.QueryRow("select count(*) from " + usageevent.CodexLegacyIdentityEvidenceTable).Scan(&after); err != nil || after != before-1 {
		t.Fatalf("bounded clearing = before:%d after:%d err:%v", before, after, err)
	}
	assertCodexEvidenceAlias(t, st, true)
	finishCodexEvidenceRollup(t, st, 1)
	var stale int
	if err := db.QueryRow("select count(*) from " + usageevent.CodexLegacyIdentityEvidenceTable + " where structure_revision <> '1'").Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("stale revision rows = %d err=%v", stale, err)
	}
	assertCodexEvidenceAlias(t, st, true)
	assertCodexEvidenceRawEvents(t, db, 5)
}

func TestCodexLegacyIdentityEvidenceMigrationRecoversMissingDerivation(t *testing.T) {
	for _, missing := range []string{"table", "state"} {
		t.Run(missing, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
			db, st := openMonitoringRepositoryStore(t, dbPath)
			defer func() { _ = db.Close() }()
			seedCodexLegacyIdentityEvents(t, db, 5)
			finishCodexEvidenceRollup(t, st, 1000)
			query := "drop table " + usageevent.CodexLegacyIdentityEvidenceTable
			if missing == "state" {
				query = "delete from usage_monitoring_rollup_state where rollup_name = 'codex_legacy_identity_v1'"
			}
			if _, err := db.Exec(query); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db, st = openMonitoringRepositoryStore(t, dbPath)
			state, err := st.UsageMonitoringState(context.Background(), usageevent.CodexLegacyIdentityRollupName)
			if err != nil || state.CoverageEventID != 0 || state.TargetEventID != 5 {
				t.Fatalf("recovered state = %+v err=%v", state, err)
			}
			assertCodexEvidenceAlias(t, st, true)
			finishCodexEvidenceRollup(t, st, 1)
			if err := sqliterepo.Migrate(db); err != nil {
				t.Fatal(err)
			}
			state, err = st.UsageMonitoringState(context.Background(), usageevent.CodexLegacyIdentityRollupName)
			if err != nil || state.CoverageEventID != 5 || state.ProcessedEvents != 5 {
				t.Fatalf("repeated migration reset valid evidence = %+v err=%v", state, err)
			}
			assertCodexEvidenceAlias(t, st, true)
			assertCodexEvidenceRawEvents(t, db, 5)
		})
	}
}

func seedCodexLegacyIdentityEvents(t testing.TB, db *sql.DB, count int64) {
	t.Helper()
	_, err := db.Exec(`with recursive ids(id) as (
		select 1 union all select id + 1 from ids where id < ?
	) insert into usage_events (
		id, event_hash, timestamp_ms, timestamp, model, created_at_ms,
		provider, auth_provider_snapshot, auth_file_snapshot, source,
		auth_index, auth_account_id_snapshot, account_snapshot
	) select id, 'identity-' || id, id, cast(id as text), 'gpt-test', id,
		'codex', 'codex', case id % 3 when 0 then 'codex-a.json' when 1 then null else '' end,
		'codex-a.json', 'auth-a', case when id <= ? then '' else 'account-a' end,
		'same@example.com' from ids`, count, count/2)
	if err != nil {
		t.Fatal(err)
	}
}

func finishCodexEvidenceRollup(t testing.TB, st *store.Store, limit int) {
	t.Helper()
	for i := int64(1); i <= 1000; i++ {
		result, err := st.CatchUpCodexLegacyIdentityEvidence(context.Background(), limit, i)
		if err != nil {
			t.Fatal(err)
		}
		if !result.Pending {
			return
		}
	}
	t.Fatal("identity evidence backfill did not complete")
}

func assertCodexEvidenceAlias(t testing.TB, st *store.Store, want bool) {
	t.Helper()
	_, allowed, err := st.UsageEvents.ResolveCodexLegacyAccountKey(context.Background(), usageidentity.Fields{
		AuthFileSnapshot: "codex-a.json", AuthIndex: "auth-a", Source: "codex-a.json",
		AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "account-a", AccountSnapshot: "same@example.com",
	})
	if err != nil || allowed != want {
		t.Fatalf("identity allowed=%v err=%v, want %v", allowed, err, want)
	}
}

func assertCodexEvidenceRawEvents(t testing.TB, db *sql.DB, count int64) {
	t.Helper()
	var rows, sumIDs, sumCreated, strong int64
	err := db.QueryRow(`select count(*), sum(id), sum(created_at_ms),
		sum(case when auth_account_id_snapshot = 'account-a' then 1 else 0 end) from usage_events`).Scan(&rows, &sumIDs, &sumCreated, &strong)
	if err != nil || rows != count || sumIDs != count*(count+1)/2 || sumCreated != sumIDs || strong != count-count/2 {
		t.Fatalf("raw identity events changed: rows=%d ids=%d created=%d strong=%d err=%v", rows, sumIDs, sumCreated, strong, err)
	}
}

func TestCodexLegacyIdentityEvidenceSurvivesRawDeletionAndCatchUp(t *testing.T) {
	ctx := context.Background()
	db, st := newMonitoringRepositoryStore(t)

	// 1. insert Codex events
	seedCodexLegacyIdentityEvents(t, db, 10)

	// 2. CatchUpCodexLegacyIdentityEvidence until ready
	finishCodexEvidenceRollup(t, st, 1000)

	// 3. assert state ready, coverage > 0, target >= coverage
	stateBefore, err := st.UsageMonitoringState(ctx, usageevent.CodexLegacyIdentityRollupName)
	if err != nil {
		t.Fatalf("get state before raw deletion: %v", err)
	}
	if stateBefore.Status != "ready" {
		t.Fatalf("state status = %q, want ready", stateBefore.Status)
	}
	if stateBefore.CoverageEventID <= 0 {
		t.Fatalf("state coverage_event_id = %d, want > 0", stateBefore.CoverageEventID)
	}
	if stateBefore.TargetEventID < stateBefore.CoverageEventID {
		t.Fatalf("state target (%d) < coverage (%d)", stateBefore.TargetEventID, stateBefore.CoverageEventID)
	}

	var evidenceCountBefore int
	if err := db.QueryRow("select count(*) from " + usageevent.CodexLegacyIdentityEvidenceTable).Scan(&evidenceCountBefore); err != nil {
		t.Fatalf("count evidence before: %v", err)
	}
	if evidenceCountBefore == 0 {
		t.Fatal("expected evidence rows before deletion, got 0")
	}

	keyBefore, allowedBefore, err := st.UsageEvents.ResolveCodexLegacyAccountKey(ctx, usageidentity.Fields{
		AuthFileSnapshot: "codex-a.json", AuthIndex: "auth-a", Source: "codex-a.json",
		AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "account-a", AccountSnapshot: "same@example.com",
	})
	if err != nil || !allowedBefore || keyBefore == "" {
		t.Fatalf("resolve key before: key=%q allowed=%v err=%v", keyBefore, allowedBefore, err)
	}

	// 4. delete all usage_events
	if _, err := db.Exec("delete from usage_events"); err != nil {
		t.Fatalf("delete all usage_events: %v", err)
	}

	// 5. raw max = 0
	var rawMax int64
	if err := db.QueryRow("select coalesce(max(id), 0) from usage_events").Scan(&rawMax); err != nil {
		t.Fatalf("query raw max: %v", err)
	}
	if rawMax != 0 {
		t.Fatalf("raw max = %d, want 0", rawMax)
	}

	// 6. 再次 CatchUpCodexLegacyIdentityEvidence
	result, err := st.CatchUpCodexLegacyIdentityEvidence(ctx, 1000, 123456789)
	if err != nil {
		t.Fatalf("catch up after raw deletion: %v", err)
	}
	if result.Pending {
		t.Fatalf("catch up result pending = true, want false")
	}

	// 7. 断言：
	// evidence row count 不减少
	// status 不进入 clearing
	// coverage 不回退
	// target 不错误回退到 0
	// structure_revision 不变化
	// stored evidence available == true
	// ResolveCodexLegacyAccountKey 仍得到原结果
	var evidenceCountAfter int
	if err := db.QueryRow("select count(*) from " + usageevent.CodexLegacyIdentityEvidenceTable).Scan(&evidenceCountAfter); err != nil {
		t.Fatalf("count evidence after: %v", err)
	}
	if evidenceCountAfter < evidenceCountBefore {
		t.Fatalf("evidence count decreased: before=%d after=%d", evidenceCountBefore, evidenceCountAfter)
	}

	stateAfter, err := st.UsageMonitoringState(ctx, usageevent.CodexLegacyIdentityRollupName)
	if err != nil {
		t.Fatalf("get state after: %v", err)
	}
	if stateAfter.Status == "clearing" {
		t.Fatalf("state status entered clearing: %s", stateAfter.Status)
	}
	if stateAfter.CoverageEventID < stateBefore.CoverageEventID {
		t.Fatalf("coverage rewound: before=%d after=%d", stateBefore.CoverageEventID, stateAfter.CoverageEventID)
	}
	if stateAfter.TargetEventID == 0 {
		t.Fatalf("target reset to 0: target_event_id=%d", stateAfter.TargetEventID)
	}
	if stateAfter.StructureRevision != stateBefore.StructureRevision {
		t.Fatalf("structure_revision changed: before=%q after=%q", stateBefore.StructureRevision, stateAfter.StructureRevision)
	}

	keyAfter, allowedAfter, err := st.UsageEvents.ResolveCodexLegacyAccountKey(ctx, usageidentity.Fields{
		AuthFileSnapshot: "codex-a.json", AuthIndex: "auth-a", Source: "codex-a.json",
		AuthProviderSnapshot: "codex", AuthAccountIDSnapshot: "account-a", AccountSnapshot: "same@example.com",
	})
	if err != nil || !allowedAfter || keyAfter != keyBefore {
		t.Fatalf("resolve key after: key=%q allowed=%v (want key=%q, allowed=true) err=%v", keyAfter, allowedAfter, keyBefore, err)
	}

	// 8. 验证 new raw tail 仍正常工作
	if _, err := db.Exec(`insert into usage_events (
		id, event_hash, timestamp_ms, timestamp, model, created_at_ms,
		provider, auth_provider_snapshot, auth_file_snapshot, source,
		auth_index, auth_account_id_snapshot, account_snapshot
	) values (
		11, 'identity-11', 11, '11', 'gpt-test', 11,
		'codex', 'codex', 'codex-a.json', 'codex-a.json',
		'auth-a', 'account-a', 'same@example.com'
	)`); err != nil {
		t.Fatalf("insert tail event: %v", err)
	}

	tailResult, err := st.CatchUpCodexLegacyIdentityEvidence(ctx, 1000, 123456790)
	if err != nil {
		t.Fatalf("tail catch up: %v", err)
	}
	if tailResult.CoverageEventID < 11 {
		t.Fatalf("tail coverage did not advance: got %d, want >= 11", tailResult.CoverageEventID)
	}
	stateTail, err := st.UsageMonitoringState(ctx, usageevent.CodexLegacyIdentityRollupName)
	if err != nil {
		t.Fatalf("state after tail: %v", err)
	}
	if stateTail.CoverageEventID < 11 {
		t.Fatalf("coverage after tail = %d, want >= 11", stateTail.CoverageEventID)
	}
}

func TestCodexLegacyIdentityRollupDecoupledSchemaVersion(t *testing.T) {
	ctx := context.Background()
	db, st := newMonitoringRepositoryStore(t)
	seedCodexLegacyIdentityEvents(t, db, 5)

	if _, err := db.Exec("update usage_monitoring_rollup_state set schema_version = ? where rollup_name = ?",
		usageidentity.CodexLegacyIdentityEvidenceSchemaVersion+1, usageevent.CodexLegacyIdentityRollupName); err != nil {
		t.Fatal(err)
	}

	_, err := st.CatchUpCodexLegacyIdentityEvidence(ctx, 1000, 1)
	if !errors.Is(err, usagemonitoring.ErrUnsupportedSchema) {
		t.Fatalf("expected ErrUnsupportedSchema when schema_version is drifted, got %v", err)
	}

	if _, err := db.Exec("update usage_monitoring_rollup_state set schema_version = ? where rollup_name = ?",
		usageidentity.CodexLegacyIdentityEvidenceSchemaVersion, usageevent.CodexLegacyIdentityRollupName); err != nil {
		t.Fatal(err)
	}

	result, err := st.CatchUpCodexLegacyIdentityEvidence(ctx, 1000, 2)
	if err != nil {
		t.Fatalf("catch-up with decoupled schema version failed: %v", err)
	}
	if result.Processed != 5 {
		t.Fatalf("expected processed=5, got %d", result.Processed)
	}
}
