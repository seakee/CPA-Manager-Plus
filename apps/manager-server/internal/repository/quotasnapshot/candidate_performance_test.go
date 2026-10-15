package quotasnapshot_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	quotasnapshot "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotasnapshot"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/store"
	"modernc.org/sqlite"
)

var candidateProbeSequence atomic.Uint64

func TestListCandidatesBoundsHistoricalObservationReads(t *testing.T) {
	var reads atomic.Int64
	probe := fmt.Sprintf("quota_candidate_probe_%d", candidateProbeSequence.Add(1))
	if err := sqlite.RegisterScalarFunction(probe, 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		reads.Add(1)
		return args[0], nil
	}); err != nil {
		t.Fatalf("register lifecycle read probe: %v", err)
	}
	st, db := seedCandidateHistory(t, 128)
	// Interpose only the lifecycle flag read. The real candidate SQL, snapshot
	// table, indexes and result decoding still execute against SQLite.
	if _, err := db.Exec(`alter table account_quota_observations rename to candidate_test_observations`); err != nil {
		t.Fatalf("rename observations: %v", err)
	}
	if _, err := db.Exec(`create view account_quota_observations as
		select id, ` + probe + `(lifecycle_applied) as lifecycle_applied
		from candidate_test_observations`); err != nil {
		t.Fatalf("create observation read probe: %v", err)
	}

	items, err := st.QuotaSnapshots.ListCandidates(context.Background(), "account-history", "codex", 2000)
	if err != nil {
		t.Fatalf("list candidates: %v", err)
	}
	const candidateCount = 2 * 2 * 8 // windows * sources * evidence per source
	if len(items) != candidateCount {
		t.Fatalf("candidates = %d, want %d", len(items), candidateCount)
	}
	if got := reads.Load(); got > candidateCount*2 {
		t.Fatalf("historical observation reads = %d, want at most %d for %d candidates", got, candidateCount*2, candidateCount)
	}
	t.Logf("historical observation reads = %d for %d candidates", reads.Load(), len(items))
	for _, item := range items {
		if item.ObservedAtMS < 121 {
			t.Fatalf("returned old evidence: window=%s source=%s observed=%d", item.ProviderWindowID, item.Source, item.ObservedAtMS)
		}
	}
}

func TestListCandidatesIndexedAndDeferredReadersAgree(t *testing.T) {
	st, db := seedCandidateHistory(t, 16)
	for i := 0; i < 9; i++ {
		seedLegacySnapshot(t, db, "account-history", "codex", "legacy-models", "model_quota", "custom_legacy_source", fmt.Sprintf("legacy-%d", i), int64(20+i))
		if _, err := db.Exec(`update account_quota_snapshots set
			model_scope_kind = 'models', model_scope_key = 'shared', model_ids_json = ?
			where id = (select max(id) from account_quota_snapshots)`, fmt.Sprintf(`["model-%d"]`, i)); err != nil {
			t.Fatalf("set legacy model identity: %v", err)
		}
	}
	for _, statement := range []string{
		`update account_quota_windows set availability = case provider_window_id
			when 'window-0' then 'pending_absent' else 'inactive' end`,
		`update account_quota_snapshots set observed_at_ms = 16 where observed_at_ms = 15`,
		`update account_quota_observations set lifecycle_applied = 0 where id = 64`,
		`update account_quota_snapshots set logical_window_id = null where observation_id = 63`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("prepare candidate edge cases: %v", err)
		}
	}
	seedLegacySnapshot(t, db, "orphan:codex-account:workspace", "codex", "legacy-orphan", "five_hour", "inspection", "orphan", 30)
	type query struct {
		account, provider string
		limit             int
	}
	queries := []query{
		{"account-history", "codex", 1},
		{"account-history", "codex", 9},
		{"account-history", "codex", 2000},
		{" account-history ", " codex ", 0},
		{"orphan:codex-account:workspace", "codex", 2000},
		{"account-history", "xai", 2000},
		{"missing-account", "codex", 2000},
	}
	indexed := make([][]model.AccountQuotaSnapshot, len(queries))
	for i, q := range queries {
		items, err := st.QuotaSnapshots.ListCandidates(context.Background(), q.account, q.provider, q.limit)
		if err != nil {
			t.Fatalf("indexed query %+v: %v", q, err)
		}
		indexed[i] = items
	}
	if _, err := db.Exec(`drop index ` + quotasnapshot.CandidateIndexName); err != nil {
		t.Fatalf("remove candidate index for upgrade fallback: %v", err)
	}
	if err := st.RunDerivedStartupMaintenance(context.Background()); err != nil {
		t.Fatalf("defer populated index: %v", err)
	}
	var exists bool
	if err := db.QueryRow(`select exists(select 1 from sqlite_master where name = ?)`, quotasnapshot.CandidateIndexName).Scan(&exists); err != nil || exists {
		t.Fatalf("populated candidate index must remain deferred: exists=%t err=%v", exists, err)
	}
	for i, q := range queries {
		items, err := st.QuotaSnapshots.ListCandidates(context.Background(), q.account, q.provider, q.limit)
		if err != nil {
			t.Fatalf("deferred query %+v: %v", q, err)
		}
		if !reflect.DeepEqual(items, indexed[i]) {
			t.Fatalf("indexed and deferred candidates differ for %+v:\nindexed=%+v\ndeferred=%+v", q, indexed[i], items)
		}
	}
}

func BenchmarkListCandidatesHistory(b *testing.B) {
	for _, history := range []int{128, 8192} {
		b.Run(fmt.Sprintf("history_%d", history), func(b *testing.B) {
			st, _ := seedCandidateHistory(b, history)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				items, err := st.QuotaSnapshots.ListCandidates(context.Background(), "account-history", "codex", 2000)
				if err != nil || len(items) != 32 {
					b.Fatalf("list candidates: count=%d err=%v", len(items), err)
				}
			}
		})
	}
}

func seedCandidateHistory(t testing.TB, history int) (*store.Store, *sql.DB) {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	st := store.New(db)
	if err := st.RunDerivedStartupMaintenance(context.Background()); err != nil {
		t.Fatalf("prepare candidate indexes: %v", err)
	}
	writes := make([]model.AccountQuotaObservationWrite, 0, 4)
	for window := 0; window < 2; window++ {
		for _, source := range []string{"response_header", "inspection"} {
			identity := fmt.Sprintf("initial-%d-%s", window, source)
			writes = append(writes, model.AccountQuotaObservationWrite{
				Observation: model.AccountQuotaObservation{
					ObservationHash: identity, AccountKey: "account-history", Provider: "codex",
					Source: source, SourceObservationID: identity, InventoryScopeKey: "codex:rate-limits",
					InventoryMode: "partial", ObservedAtMS: 1, WindowCount: 1, CreatedAtMS: 1,
				},
				Snapshots: []model.AccountQuotaSnapshot{{
					AccountKey: "account-history", Provider: "codex", ProviderWindowID: fmt.Sprintf("window-%d", window),
					WindowKind: "model_quota", WindowMode: "unknown", ModelScopeKind: "all",
					ScopeFingerprint: quotasnapshot.ScopeFingerprint("all", "", nil), ContentHash: identity,
					InventoryScopeKey: "codex:rate-limits", Source: source, SourceObservationID: identity,
					ObservedAtMS: 1, BoundaryAccuracy: "unknown", CreatedAtMS: 1,
				}},
			})
		}
	}
	if err := st.QuotaSnapshots.InsertObservationWrites(context.Background(), writes); err != nil {
		t.Fatalf("seed candidate history: %v", err)
	}
	// Bulk-load repeated historical evidence without benchmarking the lifecycle
	// writer's setup cost. The initial observations establish real windows.
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin history seed: %v", err)
	}
	defer tx.Rollback()
	observation, err := tx.Prepare(`insert into account_quota_observations (
		observation_hash, account_key, provider, source, source_observation_id,
		inventory_scope_key, inventory_mode, observed_at_ms, window_count, lifecycle_applied, created_at_ms
	) select ?, account_key, provider, source, ?, inventory_scope_key, inventory_mode, ?, 1, 1, ?
	from account_quota_observations where id = ?`)
	if err != nil {
		t.Fatalf("prepare history observations: %v", err)
	}
	defer observation.Close()
	snapshot, err := tx.Prepare(`insert into account_quota_snapshots (
		observation_id, logical_window_id, activation_id, cycle_id, account_key, provider,
		provider_window_id, window_kind, window_mode, model_scope_kind, model_scope_key,
		model_ids_json, scope_fingerprint, content_hash, source, source_observation_id,
		observed_at_ms, boundary_accuracy, created_at_ms
	) select ?, logical_window_id, activation_id, cycle_id, account_key, provider,
		provider_window_id, window_kind, window_mode, model_scope_kind, model_scope_key,
		model_ids_json, scope_fingerprint, content_hash, source, ?, ?, boundary_accuracy, ?
	from account_quota_snapshots where id = ?`)
	if err != nil {
		t.Fatalf("prepare history snapshots: %v", err)
	}
	defer snapshot.Close()
	for timestamp := 2; timestamp <= history; timestamp++ {
		for baseID := 1; baseID <= 4; baseID++ {
			identity := fmt.Sprintf("history-%d-%d", timestamp, baseID)
			result, err := observation.Exec(identity, identity, timestamp, timestamp, baseID)
			if err != nil {
				t.Fatalf("seed history observation: %v", err)
			}
			id, err := result.LastInsertId()
			if err != nil {
				t.Fatalf("read history observation ID: %v", err)
			}
			if _, err := snapshot.Exec(id, identity, timestamp, timestamp, baseID); err != nil {
				t.Fatalf("seed history snapshot: %v", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit history seed: %v", err)
	}
	return st, db
}
