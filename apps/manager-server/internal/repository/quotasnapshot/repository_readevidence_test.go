package quotasnapshot_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	quotasnapshot "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/quotasnapshot"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

type readEvidenceFixture struct {
	dbPath     string
	accountKey string
	scopeKey   string
	db         *sql.DB
	repo       quotasnapshot.Repository
}

func newReadEvidenceFixture(t *testing.T, accountKey, scopeKey string) readEvidenceFixture {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "usage.sqlite")
	db, err := sqliterepo.Open(dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return readEvidenceFixture{
		dbPath:     dbPath,
		accountKey: accountKey,
		scopeKey:   scopeKey,
		db:         db,
		repo:       quotasnapshot.New(db),
	}
}

func (f readEvidenceFixture) observation(hash, mode string, observedAtMS int64, snapshots []model.AccountQuotaSnapshot) model.AccountQuotaObservationWrite {
	return model.AccountQuotaObservationWrite{
		Observation: model.AccountQuotaObservation{
			ObservationHash:     hash,
			AccountKey:          f.accountKey,
			Provider:            "codex",
			Source:              "inspection",
			SourceObservationID: hash,
			InventoryScopeKey:   f.scopeKey,
			InventoryMode:       mode,
			ObservedAtMS:        observedAtMS,
			WindowCount:         len(snapshots),
			CreatedAtMS:         observedAtMS,
		},
		Snapshots: snapshots,
	}
}

func (f readEvidenceFixture) identifiableSnapshot(id, displayName string, observedAtMS int64) model.AccountQuotaSnapshot {
	return model.AccountQuotaSnapshot{
		AccountKey: f.accountKey, Provider: "codex",
		ProviderWindowID: id, WindowKind: "weekly", WindowMode: "fixed",
		ScopeDisplayName: displayName, ModelScopeKind: "feature", ModelScopeKey: f.scopeKey,
		ScopeFingerprint: quotasnapshot.ScopeFingerprint("feature", f.scopeKey, nil),
		ContentHash:      id + "-content", InventoryScopeKey: f.scopeKey,
		Source: "inspection", SourceObservationID: "obs-" + id,
		ObservedAtMS: observedAtMS, BoundaryAccuracy: "exact", CreatedAtMS: observedAtMS,
	}
}

func (f readEvidenceFixture) ambiguousSnapshot(index int, usedPercent float64, observedAtMS int64) model.AccountQuotaSnapshot {
	snapshot := f.identifiableSnapshot(
		fmt.Sprintf("cpamp:ambiguous:%s-weekly-%d", f.scopeKey, index),
		"Ambiguous Quota",
		observedAtMS,
	)
	snapshot.ContentHash += "-ambiguous"
	snapshot.UsedPercent = &usedPercent
	return snapshot
}

// countAmbiguousSnapshots reads the ambiguous current-observation rows with a
// lightweight direct query so the snapshot-isolation test can look inside a
// transaction without reaching into repository internals.
func (f readEvidenceFixture) countAmbiguousSnapshots(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, observedAtMS int64) (int, error) {
	var count int
	err := db.QueryRowContext(ctx, `select count(*)
		from account_quota_snapshots snapshot
		join account_quota_observations observation on observation.id = snapshot.observation_id
		where snapshot.account_key = ? and lower(trim(snapshot.provider)) = 'codex'
			and lower(trim(snapshot.provider_window_id)) like 'cpamp:ambiguous:%'
			and observation.observed_at_ms = ?`,
		f.accountKey, observedAtMS).Scan(&count)
	return count, err
}

func TestReadQueryEvidenceAggregatesAllEvidenceGroups(t *testing.T) {
	fixture := newReadEvidenceFixture(t, "account-evidence", "future_feature")

	// Identifiable lineage with display evidence plus a current ambiguous set.
	if err := fixture.repo.InsertObservationWrites(context.Background(), []model.AccountQuotaObservationWrite{
		fixture.observation("complete-1", "complete", 100, []model.AccountQuotaSnapshot{
			fixture.identifiableSnapshot("future-feature-weekly-0", "Future Feature", 100),
			fixture.ambiguousSnapshot(0, 25, 100),
			fixture.ambiguousSnapshot(1, 35, 100),
		}),
	}); err != nil {
		t.Fatalf("insert complete observation: %v", err)
	}

	evidence, err := fixture.repo.ReadQueryEvidence(context.Background(), fixture.accountKey, "codex", 0)
	if err != nil {
		t.Fatalf("read query evidence: %v", err)
	}
	if len(evidence.Candidates) != 1 || evidence.Candidates[0].ProviderWindowID != "future-feature-weekly-0" {
		t.Fatalf("candidates = %#v", evidence.Candidates)
	}
	if len(evidence.States) != 1 || evidence.States[0].ProviderWindowID != "future-feature-weekly-0" {
		t.Fatalf("states = %#v", evidence.States)
	}
	if len(evidence.AmbiguousCandidates) != 2 {
		t.Fatalf("ambiguous candidates = %#v", evidence.AmbiguousCandidates)
	}
	for _, candidate := range evidence.AmbiguousCandidates {
		if candidate.ObservedAtMS != 100 || candidate.InventoryScopeKey != fixture.scopeKey {
			t.Fatalf("ambiguous candidate = %#v", candidate)
		}
	}
	if len(evidence.DisplayCandidates) != 1 ||
		evidence.DisplayCandidates[0].ScopeDisplayName != "Future Feature" {
		t.Fatalf("display candidates = %#v", evidence.DisplayCandidates)
	}

	// Non-Codex providers never carry the Codex-only ambiguous namespace.
	claudeFixture := newReadEvidenceFixture(t, "account-claude", "future_feature")
	if err := claudeFixture.repo.InsertObservationWrites(context.Background(), []model.AccountQuotaObservationWrite{
		{
			Observation: model.AccountQuotaObservation{
				ObservationHash: "claude-complete-1", AccountKey: claudeFixture.accountKey,
				Provider: "claude", Source: "api_query", SourceObservationID: "claude-complete-1",
				InventoryScopeKey: "claude:quota-windows", InventoryMode: "complete",
				ObservedAtMS: 100, WindowCount: 1, CreatedAtMS: 100,
			},
			Snapshots: []model.AccountQuotaSnapshot{{
				AccountKey: claudeFixture.accountKey, Provider: "claude",
				ProviderWindowID: "weekly-scoped-demo", WindowKind: "weekly", WindowMode: "unknown",
				ScopeDisplayName: "Demo Model A", ModelScopeKind: "feature", ModelScopeKey: "scope_unknown",
				ScopeFingerprint: quotasnapshot.ScopeFingerprint("feature", "scope_unknown", nil),
				ContentHash:      "claude-content", InventoryScopeKey: "claude:quota-windows",
				Source: "api_query", SourceObservationID: "claude-complete-1",
				ObservedAtMS: 100, BoundaryAccuracy: "unknown", CreatedAtMS: 100,
			}},
		},
	}); err != nil {
		t.Fatalf("insert claude observation: %v", err)
	}
	claudeEvidence, err := claudeFixture.repo.ReadQueryEvidence(context.Background(), claudeFixture.accountKey, "claude", 0)
	if err != nil {
		t.Fatalf("read claude evidence: %v", err)
	}
	if len(claudeEvidence.Candidates) != 1 || claudeEvidence.AmbiguousCandidates != nil {
		t.Fatalf("claude evidence = %#v", claudeEvidence)
	}
}

// TestReadQueryEvidenceReadsOneSQLiteSnapshot pins the read-snapshot guarantee:
// a read transaction opened before a concurrent observation commit keeps seeing
// the pre-commit evidence for the rest of the transaction, and a fresh
// ReadQueryEvidence call observes the commit. The sequence is deterministic —
// no sleeps, no production test hooks. The reader pool deliberately uses a
// deferred transaction lock so it exercises the WAL reader/writer parallelism
// the production DSN reserves with _txlock=immediate.
func TestReadQueryEvidenceReadsOneSQLiteSnapshot(t *testing.T) {
	fixture := newReadEvidenceFixture(t, "account-snapshot", "future_feature")

	if err := fixture.repo.InsertObservationWrites(context.Background(), []model.AccountQuotaObservationWrite{
		fixture.observation("complete-1", "complete", 100, []model.AccountQuotaSnapshot{
			fixture.identifiableSnapshot("future-feature-weekly-0", "Future Feature", 100),
			fixture.ambiguousSnapshot(0, 25, 100),
		}),
	}); err != nil {
		t.Fatalf("insert pre-commit observation: %v", err)
	}

	readerDB, err := sql.Open("sqlite", deferredReaderDSN(fixture.dbPath))
	if err != nil {
		t.Fatalf("open deferred reader connection: %v", err)
	}
	t.Cleanup(func() { _ = readerDB.Close() })

	ctx := context.Background()
	readTx, err := readerDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin reader transaction: %v", err)
	}
	t.Cleanup(func() { _ = readTx.Rollback() })

	preCommit, err := fixture.countAmbiguousSnapshots(ctx, readTx, 100)
	if err != nil {
		t.Fatalf("read ambiguous inside transaction: %v", err)
	}
	if preCommit != 1 {
		t.Fatalf("pre-commit ambiguous evidence count = %d, want 1", preCommit)
	}

	// A concurrent writer connection commits a newer complete observation that
	// empties the ambiguous current set and adds a new identifiable window
	// while the reader transaction is open.
	if err := fixture.repo.InsertObservationWrites(ctx, []model.AccountQuotaObservationWrite{
		fixture.observation("complete-2", "complete", 200, []model.AccountQuotaSnapshot{
			fixture.identifiableSnapshot("future-feature-monthly-0", "Future Feature", 200),
		}),
	}); err != nil {
		t.Fatalf("commit concurrent observation: %v", err)
	}

	inTransaction, err := fixture.countAmbiguousSnapshots(ctx, readTx, 100)
	if err != nil {
		t.Fatalf("re-read ambiguous inside transaction: %v", err)
	}
	if inTransaction != 1 {
		t.Fatalf("read transaction mixed committed state: ambiguous count = %d, want 1", inTransaction)
	}
	var inTransactionStates int
	if err := readTx.QueryRowContext(ctx,
		`select count(*) from account_quota_windows where account_key = ?`,
		fixture.accountKey).Scan(&inTransactionStates); err != nil {
		t.Fatalf("read states inside transaction: %v", err)
	}
	if inTransactionStates != 1 {
		t.Fatalf("read transaction mixed window states: count = %d, want 1", inTransactionStates)
	}

	freshStates, err := fixture.repo.ReadQueryEvidence(ctx, fixture.accountKey, "codex", 0)
	if err != nil {
		t.Fatalf("fresh read query evidence states: %v", err)
	}
	if len(freshStates.States) != 2 {
		t.Fatalf("fresh evidence did not observe the new window state: %#v", freshStates.States)
	}
	if err := readTx.Commit(); err != nil {
		t.Fatalf("commit reader transaction: %v", err)
	}

	fresh, err := fixture.repo.ReadQueryEvidence(ctx, fixture.accountKey, "codex", 0)
	if err != nil {
		t.Fatalf("fresh read query evidence: %v", err)
	}
	if len(fresh.AmbiguousCandidates) != 0 {
		t.Fatalf("fresh evidence did not observe the empty ambiguous current set: %#v", fresh.AmbiguousCandidates)
	}
	if len(fresh.Candidates) != 2 {
		t.Fatalf("fresh candidates = %#v, want both identifiable windows", fresh.Candidates)
	}
}

// deferredReaderDSN opens the same SQLite file without the production
// _txlock=immediate directive, so a read transaction stays a pure WAL reader.
func deferredReaderDSN(dbPath string) string {
	dsn := &url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(dbPath)}
	query := dsn.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	dsn.RawQuery = query.Encode()
	return dsn.String()
}

func TestReadQueryEvidenceSuppressesUnattachedLegacyCodexCandidates(t *testing.T) {
	fixture := newReadEvidenceFixture(t, "account-legacy-suppress", "same_quota")

	canonicalID := "same-quota--additional-p-none-s-604800-weekly-0"
	legacyAliasID := "same-quota-weekly-0"
	scopeFP := quotasnapshot.ScopeFingerprint("feature", fixture.scopeKey, nil)
	nowMS := int64(1000)

	// 1. Insert an active canonical window state via complete observation
	if err := fixture.repo.InsertObservationWrites(context.Background(), []model.AccountQuotaObservationWrite{
		fixture.observation("obs-1", "complete", nowMS, []model.AccountQuotaSnapshot{
			{
				AccountKey: fixture.accountKey, Provider: "codex",
				ProviderWindowID: canonicalID, WindowKind: "weekly", WindowMode: "fixed",
				ScopeDisplayName: "Same Quota", ModelScopeKind: "feature", ModelScopeKey: fixture.scopeKey,
				ScopeFingerprint: scopeFP, ContentHash: "hash-1", InventoryScopeKey: fixture.scopeKey,
				Source: "inspection", SourceObservationID: "obs-1",
				ObservedAtMS: nowMS, BoundaryAccuracy: "exact", CreatedAtMS: nowMS,
			},
		}),
	}); err != nil {
		t.Fatalf("insert canonical observation: %v", err)
	}

	// 2. Insert an unattached legacy candidate into DB directly (observation_id is NULL, logical_window_id is NULL)
	if _, err := fixture.db.Exec(`insert into account_quota_snapshots (
		observation_id, logical_window_id, account_key, provider, provider_window_id,
		window_kind, window_mode, model_scope_kind, model_scope_key, model_ids_json, scope_fingerprint,
		source, source_observation_id, observed_at_ms, boundary_accuracy,
		duration_seconds, used_percent, remaining_percent, created_at_ms
	) values (
		null, null, ?, 'codex', ?,
		'weekly', 'fixed', 'feature', ?, '[]', ?,
		'inspection', 'legacy-unattached', ?, 'exact',
		604800, 50, 50, ?
	)`, fixture.accountKey, legacyAliasID, fixture.scopeKey, scopeFP, nowMS-100, nowMS-100); err != nil {
		t.Fatalf("insert unattached legacy snapshot: %v", err)
	}

	// 3. Query evidence: canonical window is active, matching scopeFP and strict role,
	// so the unattached legacy candidate MUST be suppressed.
	evidence, err := fixture.repo.ReadQueryEvidence(context.Background(), fixture.accountKey, "codex", 0)
	if err != nil {
		t.Fatalf("read query evidence: %v", err)
	}
	if len(evidence.Candidates) != 1 {
		t.Fatalf("expected 1 candidate after suppression, got %d: %#v", len(evidence.Candidates), evidence.Candidates)
	}
	if evidence.Candidates[0].ProviderWindowID != canonicalID {
		t.Fatalf("expected canonical candidate %s, got %s", canonicalID, evidence.Candidates[0].ProviderWindowID)
	}

	// 4. Test safety boundary: an unattached candidate with a different scope is NEVER suppressed.
	diffScopeFP := quotasnapshot.ScopeFingerprint("feature", "other_feature", nil)
	unsuppressedLegacyID := "other-quota-weekly-0"
	if _, err := fixture.db.Exec(`insert into account_quota_snapshots (
		observation_id, logical_window_id, account_key, provider, provider_window_id,
		window_kind, window_mode, model_scope_kind, model_scope_key, model_ids_json, scope_fingerprint,
		source, source_observation_id, observed_at_ms, boundary_accuracy,
		duration_seconds, used_percent, remaining_percent, created_at_ms
	) values (
		null, null, ?, 'codex', ?,
		'weekly', 'fixed', 'feature', 'other_feature', '[]', ?,
		'inspection', 'legacy-unattached-2', ?, 'exact',
		604800, 50, 50, ?
	)`, fixture.accountKey, unsuppressedLegacyID, diffScopeFP, nowMS-100, nowMS-100); err != nil {
		t.Fatalf("insert unsuppressed legacy snapshot: %v", err)
	}

	evidence2, err := fixture.repo.ReadQueryEvidence(context.Background(), fixture.accountKey, "codex", 0)
	if err != nil {
		t.Fatalf("read query evidence 2: %v", err)
	}
	foundUnsuppressed := false
	for _, c := range evidence2.Candidates {
		if c.ProviderWindowID == unsuppressedLegacyID {
			foundUnsuppressed = true
			break
		}
	}
	if !foundUnsuppressed {
		t.Fatalf("unsuppressed snapshot %s should NOT have been suppressed (different scope)", unsuppressedLegacyID)
	}
}
