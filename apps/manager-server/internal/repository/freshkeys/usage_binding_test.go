package freshkeys

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	projectionadapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identityprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	projectionports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identityprojection"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
)

const (
	usageRuntime = "runtime-managed-1"
	usageKeyID   = "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	usageKeeper  = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"
	usageOtherID = "11223344556677889900aabbccddeeff"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "fresh-keys.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqlite.Migrate(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func stagedRecord(t *testing.T, requestID, keyID, secret string) Record {
	t.Helper()
	protector, err := security.NewProtector([]byte("fresh key usage binding test data key"))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := protector.ProtectString(secret)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	return Record{
		RequestID:    requestID,
		Identity:     identity.APIKeyIdentity{ID: identity.APIKeyID(keyID), Revision: identity.InitialRevision, Lifecycle: identity.LifecycleActive, CreatedAtMS: now, UpdatedAtMS: now},
		Ciphertext:   ciphertext,
		Hash:         sha256Hex(secret),
		Preview:      "cpamp_sk_…" + secret[len(secret)-4:],
		NameRevision: 1,
	}
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func bindingRows(t *testing.T, db *sql.DB, keyID string) []struct {
	runtimeIdentity string
	hash            string
	firstSeenAtMS   int64
	retiredAtMS     sql.NullInt64
} {
	t.Helper()
	rows, err := db.Query(`select runtime_identity, api_key_hash, first_seen_at_ms, retired_at_ms
		from gateway_api_key_source_bindings where api_key_id = ? order by binding_id`, keyID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := []struct {
		runtimeIdentity string
		hash            string
		firstSeenAtMS   int64
		retiredAtMS     sql.NullInt64
	}{}
	for rows.Next() {
		var row struct {
			runtimeIdentity string
			hash            string
			firstSeenAtMS   int64
			retiredAtMS     sql.NullInt64
		}
		if err := rows.Scan(&row.runtimeIdentity, &row.hash, &row.firstSeenAtMS, &row.retiredAtMS); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestCreateWritesTheSourceBindingWithTheKey(t *testing.T) {
	db := openTestDB(t)
	repo := New(db)
	secret := "cpamp_sk_" + strings.Repeat("a", 64)
	record := stagedRecord(t, strings.Repeat("b", 32), usageKeyID, secret)

	created, err := repo.Create(t.Context(), record, usageRuntime)
	if err != nil || !created {
		t.Fatalf("create = %v err = %v", created, err)
	}
	rows := bindingRows(t, db, usageKeyID)
	if len(rows) != 1 {
		t.Fatalf("bindings = %+v", rows)
	}
	if rows[0].runtimeIdentity != usageRuntime || rows[0].hash != record.Hash ||
		rows[0].firstSeenAtMS != record.Identity.CreatedAtMS || rows[0].retiredAtMS.Valid {
		t.Fatalf("binding = %+v", rows[0])
	}

	// A replay of the same creation request never adds a second attribution.
	created, err = repo.Create(t.Context(), record, usageRuntime)
	if err != nil || created {
		t.Fatalf("replay created = %v err = %v", created, err)
	}
	if rows = bindingRows(t, db, usageKeyID); len(rows) != 1 {
		t.Fatalf("replay bindings = %+v", rows)
	}
}

func TestCreateWithoutCommittedRuntimeDefersAttribution(t *testing.T) {
	db := openTestDB(t)
	repo := New(db)
	record := stagedRecord(t, strings.Repeat("c", 32), usageKeyID, "cpamp_sk_"+strings.Repeat("d", 64))
	if created, err := repo.Create(t.Context(), record, ""); err != nil || !created {
		t.Fatalf("create = %v err = %v", created, err)
	}
	if rows := bindingRows(t, db, usageKeyID); len(rows) != 0 {
		t.Fatalf("staged key already bound: %+v", rows)
	}
}

func TestBindFreshKeySourcesBackfillsOnlyUnboundKeys(t *testing.T) {
	db := openTestDB(t)
	repo := New(db)
	unbound := stagedRecord(t, strings.Repeat("e", 32), usageKeyID, "cpamp_sk_"+strings.Repeat("f", 64))
	if _, err := repo.Create(t.Context(), unbound, ""); err != nil {
		t.Fatal(err)
	}
	bound := stagedRecord(t, strings.Repeat("1", 32), usageOtherID, "cpamp_sk_"+strings.Repeat("2", 64))
	if _, err := repo.Create(t.Context(), bound, usageRuntime); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()

	backfilled, err := repo.BindFreshKeySources(t.Context(), usageRuntime, now)
	if err != nil || backfilled != 1 {
		t.Fatalf("backfill = %d err = %v, want 1", backfilled, err)
	}
	// The attribution window opens at the key's creation, so a restart cannot
	// erase history the key already had.
	rows := bindingRows(t, db, usageKeyID)
	if len(rows) != 1 || rows[0].runtimeIdentity != usageRuntime ||
		rows[0].firstSeenAtMS != unbound.Identity.CreatedAtMS || rows[0].hash != unbound.Hash {
		t.Fatalf("backfilled bindings = %+v", rows)
	}
	// Idempotent: a second startup never double-binds.
	if again, err := repo.BindFreshKeySources(t.Context(), usageRuntime, now+1); err != nil || again != 0 {
		t.Fatalf("repeat backfill = %d err = %v", again, err)
	}
	if rows := bindingRows(t, db, usageKeyID); len(rows) != 1 {
		t.Fatalf("repeated bindings = %+v", rows)
	}
}

func TestCommittedOperationClosesTheSupersededAttributionWindow(t *testing.T) {
	db := openTestDB(t)
	repo := New(db)
	secret := "cpamp_sk_" + strings.Repeat("9", 64)
	if _, err := repo.Create(t.Context(), stagedRecord(t, strings.Repeat("8", 32), usageKeyID, secret), usageRuntime); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(t.Context(), stagedRecord(t, strings.Repeat("7", 32), usageKeeper, "cpamp_sk_"+strings.Repeat("6", 64)), usageRuntime); err != nil {
		t.Fatal(err)
	}
	operation := Operation{
		ID: strings.Repeat("5", 32), TargetID: usageKeyID, KeeperID: usageKeeper,
		Mode: "revoke", RunID: usageRuntime, Generation: "3", Revision: 1, Outcome: "pending",
		CreatedAtMS: time.Now().UnixMilli(),
		Steps:       []KeyStep{{Name: "revoke", State: "unattempted"}},
	}
	if err := repo.BeginOperation(t.Context(), operation); err != nil {
		t.Fatal(err)
	}
	operation.Steps[0].State = "observed"
	operation.Outcome = "committed"
	operation.CompletedAtMS = time.Now().UnixMilli() + 1000
	if err := repo.SaveOperation(t.Context(), operation); err != nil {
		t.Fatal(err)
	}

	superseded := bindingRows(t, db, usageKeyID)
	if len(superseded) != 1 || !superseded[0].retiredAtMS.Valid || superseded[0].retiredAtMS.Int64 != operation.CompletedAtMS {
		t.Fatalf("superseded binding = %+v", superseded)
	}
	if superseded[0].firstSeenAtMS <= 0 || superseded[0].firstSeenAtMS >= superseded[0].retiredAtMS.Int64 {
		t.Fatalf("attribution window was destroyed: %+v", superseded[0])
	}
	// The keeper keeps an open attribution window.
	keeperRows := bindingRows(t, db, usageKeeper)
	if len(keeperRows) != 1 || keeperRows[0].retiredAtMS.Valid {
		t.Fatalf("keeper binding = %+v", keeperRows)
	}
}

// VerifyAdoptionRegistration must read the initial-install journal and
// adoption-registration record inside one SQLite snapshot so a concurrent
// RegisterAdoption committing between the two reads never synthesizes
// ErrJournalConflict.
func TestVerifyAdoptionRegistrationUsesConsistentReadSnapshotAcrossConcurrentCommit(t *testing.T) {
	for _, preloadLegacyJournal := range []bool{false, true} {
		name := "no_prior_journal"
		if preloadLegacyJournal {
			name = "legacy_non_adoption_journal"
		}
		t.Run(name, func(t *testing.T) {
			db := openTestDB(t)
			readerRepo := New(db)
			writerRepo := New(db)
			ctx := t.Context()

			const (
				runID = "managed:op-snapshot"
				gen   = "3"
				seed  = "seed-sha256"
			)
			if preloadLegacyJournal {
				workspaceRec := stagedRecord(t, strings.Repeat("7", 32), usageOtherID, "cpamp_sk_workspace_0001")
				workspaceRec.Origin = OriginWorkspace
				if _, err := writerRepo.Create(ctx, workspaceRec, usageRuntime); err != nil {
					t.Fatal(err)
				}
				if err := writerRepo.ClaimInitialInstall(ctx, InitialInstall{
					KeyID:              usageOtherID,
					RunID:              runID,
					Generation:         gen,
					AttemptedAtMS:      1,
					ProviderSeedSHA256: seed,
				}); err != nil {
					t.Fatal(err)
				}
			}

			candidates := []Record{
				stagedRecord(t, strings.Repeat("8", 32), usageKeyID, "cpamp_sk_adopted_0001"),
				stagedRecord(t, strings.Repeat("9", 32), usageKeeper, "cpamp_sk_adopted_0002"),
			}
			var hookFired sync.Once
			readerRepo.SetVerifyAfterInitialInstallHookForTest(func() {
				hookFired.Do(func() {
					if _, err := writerRepo.RegisterAdoption(ctx, candidates, usageRuntime, InitialInstall{
						RunID:              runID,
						Generation:         gen,
						AttemptedAtMS:      2,
						ProviderSeedSHA256: seed,
					}); err != nil {
						t.Errorf("concurrent RegisterAdoption inside hook: %v", err)
					}
				})
			})
			t.Cleanup(func() { readerRepo.SetVerifyAfterInitialInstallHookForTest(nil) })

			ready, err := readerRepo.VerifyAdoptionRegistration(ctx, runID, gen, seed, usageRuntime)
			if err != nil {
				t.Fatalf("VerifyAdoptionRegistration across concurrent commit returned error = %v, want nil", err)
			}
			if ready {
				t.Fatal("pre-commit read snapshot observed post-commit readiness")
			}

			readerRepo.SetVerifyAfterInitialInstallHookForTest(nil)
			readyAfter, err := readerRepo.VerifyAdoptionRegistration(ctx, runID, gen, seed, usageRuntime)
			if err != nil || !readyAfter {
				t.Fatalf("VerifyAdoptionRegistration after commit = (%v, %v), want (true, nil)", readyAfter, err)
			}
		})
	}
}

// Once an adoption commitment is recorded, a replay presenting the same key set
// in a different order (even with freshly generated candidate IDs) stays
// idempotent, while any replay presenting a different key set fails with
// ErrJournalConflict without staging extra keys, identities, or bindings.
func TestRegisterAdoptionEnforcesCommittedKeySetDigestOnReplay(t *testing.T) {
	db := openTestDB(t)
	repo := New(db)
	ctx := t.Context()

	const (
		runID = "managed:op-set-commitment"
		gen   = "4"
		seed  = "seed-commitment-sha256"
		secA  = "cpamp_sk_adopt_set_alpha"
		secB  = "cpamp_sk_adopt_set_beta"
		secC  = "cpamp_sk_adopt_set_gamma"
	)
	recA := stagedRecord(t, strings.Repeat("1", 32), usageKeyID, secA)
	recB := stagedRecord(t, strings.Repeat("2", 32), usageKeeper, secB)
	recC := stagedRecord(t, strings.Repeat("3", 32), usageOtherID, secC)

	committed, err := repo.RegisterAdoption(ctx, []Record{recA, recB}, usageRuntime, InitialInstall{
		RunID:              runID,
		Generation:         gen,
		AttemptedAtMS:      10,
		ProviderSeedSHA256: seed,
	})
	if err != nil || len(committed) != 2 {
		t.Fatalf("initial RegisterAdoption = %#v (%v), want 2 records", committed, err)
	}
	firstJournal, err := repo.InitialInstall(ctx)
	if err != nil || firstJournal == nil || firstJournal.AdoptedSetSHA256 == "" {
		t.Fatalf("first journal = %#v (%v)", firstJournal, err)
	}

	// Same secret set in reverse order with freshly minted candidate IDs stays idempotent.
	reorderedB := stagedRecord(t, strings.Repeat("4", 32), "22223333444455556666777788889999", secB)
	reorderedA := stagedRecord(t, strings.Repeat("5", 32), "99998888777766665555444433332222", secA)
	replayed, err := repo.RegisterAdoption(ctx, []Record{reorderedB, reorderedA}, usageRuntime, InitialInstall{
		RunID:              runID,
		Generation:         gen,
		AttemptedAtMS:      20,
		ProviderSeedSHA256: seed,
	})
	if err != nil || len(replayed) != 2 || replayed[0].Identity.ID != usageKeeper || replayed[1].Identity.ID != usageKeyID {
		t.Fatalf("reordered same-set replay = %#v (%v)", replayed, err)
	}
	if afterReorder, jErr := repo.InitialInstall(ctx); jErr != nil || afterReorder == nil || *afterReorder != *firstJournal {
		t.Fatalf("reordered replay mutated journal: got %#v (%v), want %#v", afterReorder, jErr, firstJournal)
	}

	// Different sets ({A,B,C}, {A}, {A,C}) must fail with ErrJournalConflict and
	// leave zero rows for C across keys, identities, and source bindings.
	for _, tc := range []struct {
		name       string
		candidates []Record
	}{
		{name: "superset_with_C", candidates: []Record{recA, recB, recC}},
		{name: "subset_only_A", candidates: []Record{recA}},
		{name: "replaced_B_with_C", candidates: []Record{recA, recC}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := repo.RegisterAdoption(ctx, tc.candidates, usageRuntime, InitialInstall{
				RunID:              runID,
				Generation:         gen,
				AttemptedAtMS:      30,
				ProviderSeedSHA256: seed,
			}); !errors.Is(err, ErrJournalConflict) {
				t.Fatalf("RegisterAdoption(%s) = %v, want ErrJournalConflict", tc.name, err)
			}
			if afterMismatch, jErr := repo.InitialInstall(ctx); jErr != nil || afterMismatch == nil || *afterMismatch != *firstJournal {
				t.Fatalf("mismatched replay(%s) mutated journal: %#v (%v)", tc.name, afterMismatch, jErr)
			}
			for _, query := range []string{
				`select count(*) from fresh_client_api_keys where api_key_id=? or secret_hash=?`,
				`select count(*) from gateway_api_key_identities where id=? and ? <> ''`,
				`select count(*) from gateway_api_key_source_bindings where api_key_id=? or api_key_hash=?`,
			} {
				var count int
				if err := db.QueryRow(query, usageOtherID, recC.Hash).Scan(&count); err != nil || count != 0 {
					t.Fatalf("mismatched replay(%s) left C row (%s): count=%d err=%v", tc.name, query, count, err)
				}
			}
		})
	}
	listed, err := repo.List(ctx)
	if err != nil || len(listed) != 2 {
		t.Fatalf("final listed keys = %#v (%v), want 2", listed, err)
	}
}

func TestBindFreshKeySourcesHandsOffPriorRuntimeSkipsRetiredAndRollsBackOnConflict(t *testing.T) {
	t.Run("hands_off_prior_runtime_clamps_timestamps_and_skips_retired_bindings", func(t *testing.T) {
		db := openTestDB(t)
		repo := New(db)
		ctx := t.Context()

		priorKey := stagedRecord(t, strings.Repeat("1", 32), usageKeyID, "cpamp_sk_"+strings.Repeat("a", 64))
		priorKey.Identity.CreatedAtMS = 1000
		priorKey.Identity.UpdatedAtMS = 1000
		if _, err := repo.Create(ctx, priorKey, "legacy-runtime"); err != nil {
			t.Fatal(err)
		}

		unboundKey := stagedRecord(t, strings.Repeat("2", 32), usageKeeper, "cpamp_sk_"+strings.Repeat("b", 64))
		unboundKey.Identity.CreatedAtMS = 4000
		unboundKey.Identity.UpdatedAtMS = 4000
		if _, err := repo.Create(ctx, unboundKey, ""); err != nil {
			t.Fatal(err)
		}

		// Explicitly closed source window (even if lifecycle remained active) must never be resurrected.
		retiredKey := stagedRecord(t, strings.Repeat("3", 32), usageOtherID, "cpamp_sk_"+strings.Repeat("c", 64))
		retiredKey.Identity.CreatedAtMS = 1000
		retiredKey.Identity.UpdatedAtMS = 1000
		if _, err := repo.Create(ctx, retiredKey, "legacy-runtime"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `update gateway_api_key_source_bindings set retired_at_ms = 2000 where api_key_id = ?`, usageOtherID); err != nil {
			t.Fatal(err)
		}

		// Pass nowMS = 3000 (< unboundKey.CreatedAtMS = 4000) to also prove monotonic last_seen_at_ms clamp.
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		boundCount, err := repo.BindFreshKeySourcesTx(ctx, tx, usageRuntime, 3000)
		if err != nil || boundCount != 2 {
			_ = tx.Rollback()
			t.Fatalf("BindFreshKeySourcesTx = %d (%v), want 2", boundCount, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}

		priorRows := bindingRows(t, db, usageKeyID)
		if len(priorRows) != 2 ||
			priorRows[0].runtimeIdentity != "legacy-runtime" || !priorRows[0].retiredAtMS.Valid || priorRows[0].retiredAtMS.Int64 != 3000 ||
			priorRows[1].runtimeIdentity != usageRuntime || priorRows[1].firstSeenAtMS != 3000 || priorRows[1].retiredAtMS.Valid {
			t.Fatalf("priorKey handoff rows = %+v", priorRows)
		}

		var unboundLastSeen int64
		if err := db.QueryRowContext(ctx,
			`select last_seen_at_ms from gateway_api_key_source_bindings where api_key_id = ? and runtime_identity = ? and retired_at_ms is null`,
			usageKeeper, usageRuntime).Scan(&unboundLastSeen); err != nil || unboundLastSeen != 4000 {
			t.Fatalf("unboundKey last_seen_at_ms = %d (%v), want 4000 (>= first_seen_at_ms)", unboundLastSeen, err)
		}

		retiredRows := bindingRows(t, db, usageOtherID)
		if len(retiredRows) != 1 || !retiredRows[0].retiredAtMS.Valid || retiredRows[0].retiredAtMS.Int64 != 2000 {
			t.Fatalf("retiredKey bindings resurrected = %+v", retiredRows)
		}

		insertUsageEventForKeyTest(t, db, "ev-bind-pre", 2000, priorKey.Hash, 4, 4, 8, 0)
		insertUsageEventForKeyTest(t, db, "ev-bind-post", 3000, priorKey.Hash, 6, 6, 12, 0)
		projRepo := projectionadapter.New(db)
		if _, err := projRepo.CatchUp(ctx, 50, 5000); err != nil {
			t.Fatal(err)
		}
		for _, ev := range []string{"ev-bind-pre", "ev-bind-post"} {
			p, err := projRepo.GetProjectionByEventHash(ctx, ev)
			if err != nil || p == nil || p.APIKeyState != projectionports.StateMapped || p.APIKeyID == nil || *p.APIKeyID != usageKeyID {
				t.Fatalf("projection(%s) = %+v (%v)", ev, p, err)
			}
		}

		if again, err := repo.BindFreshKeySources(ctx, usageRuntime, 5000); err != nil || again != 0 {
			t.Fatalf("idempotent BindFreshKeySources = %d (%v), want 0", again, err)
		}
	})

	t.Run("rolls_back_all_handoffs_when_later_key_conflicts", func(t *testing.T) {
		db := openTestDB(t)
		repo := New(db)
		ctx := t.Context()

		firstKey := stagedRecord(t, strings.Repeat("4", 32), usageKeyID, "cpamp_sk_"+strings.Repeat("d", 64))
		firstKey.Identity.CreatedAtMS = 1000
		firstKey.Identity.UpdatedAtMS = 1000
		if _, err := repo.Create(ctx, firstKey, "legacy-runtime"); err != nil {
			t.Fatal(err)
		}

		secondKey := stagedRecord(t, strings.Repeat("5", 32), usageKeeper, "cpamp_sk_"+strings.Repeat("e", 64))
		secondKey.Identity.CreatedAtMS = 2000
		secondKey.Identity.UpdatedAtMS = 2000
		if _, err := repo.Create(ctx, secondKey, ""); err != nil {
			t.Fatal(err)
		}
		// Seed a conflicting active hash on (usageKeeper, usageRuntime) so BindFreshKeySources must fail with ErrSourceOwned.
		if _, err := db.ExecContext(ctx,
			`insert into gateway_api_key_source_bindings (api_key_id, runtime_identity, api_key_hash, first_seen_at_ms, last_seen_at_ms, retired_at_ms) values (?, ?, ?, 2000, 2000, null)`,
			usageKeeper, usageRuntime, hashHex("conflicting-other-secret")); err != nil {
			t.Fatal(err)
		}

		if _, err := repo.BindFreshKeySources(ctx, usageRuntime, 3000); !errors.Is(err, ErrSourceOwned) {
			t.Fatalf("BindFreshKeySources conflict = %v, want ErrSourceOwned", err)
		}
		firstRows := bindingRows(t, db, usageKeyID)
		if len(firstRows) != 1 || firstRows[0].runtimeIdentity != "legacy-runtime" || firstRows[0].retiredAtMS.Valid {
			t.Fatalf("failed BindFreshKeySources mutated firstKey binding: %+v", firstRows)
		}
	})

	t.Run("recovers_passive_missing_on_valid_handoff_preserves_same_runtime_missing_and_never_resurrects_superseded", func(t *testing.T) {
		db := openTestDB(t)
		repo := New(db)
		ctx := t.Context()

		// Key A: bound on legacy-runtime, passively marked LifecycleMissing (last_seen = 2500).
		handoffMissing := stagedRecord(t, strings.Repeat("6", 32), usageKeyID, "cpamp_sk_"+strings.Repeat("1", 64))
		handoffMissing.Identity.CreatedAtMS = 1000
		handoffMissing.Identity.UpdatedAtMS = 1000
		if _, err := repo.Create(ctx, handoffMissing, "legacy-runtime"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`update gateway_api_key_identities set lifecycle = 'missing', revision = 2, updated_at_ms = 2000 where id = ?`,
			usageKeyID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`update gateway_api_key_source_bindings set observed_runtime_generation = '3', last_seen_at_ms = 2500 where api_key_id = ?`,
			usageKeyID); err != nil {
			t.Fatal(err)
		}

		// Key B: already bound on usageRuntime and passively marked LifecycleMissing on that same runtime.
		sameRuntimeMissing := stagedRecord(t, strings.Repeat("7", 32), usageKeeper, "cpamp_sk_"+strings.Repeat("2", 64))
		sameRuntimeMissing.Identity.CreatedAtMS = 1000
		sameRuntimeMissing.Identity.UpdatedAtMS = 1000
		if _, err := repo.Create(ctx, sameRuntimeMissing, usageRuntime); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`update gateway_api_key_identities set lifecycle = 'missing', revision = 2, updated_at_ms = 2000 where id = ?`,
			usageKeeper); err != nil {
			t.Fatal(err)
		}

		// Key C: superseded identity must never be resurrected.
		supersededID := "22334455667788990011aabbccddeeff"
		supersededKey := stagedRecord(t, strings.Repeat("8", 32), supersededID, "cpamp_sk_"+strings.Repeat("3", 64))
		supersededKey.Identity.CreatedAtMS = 1000
		supersededKey.Identity.UpdatedAtMS = 1000
		if _, err := repo.Create(ctx, supersededKey, "legacy-runtime"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`update gateway_api_key_identities set lifecycle = 'superseded', revision = 2, updated_at_ms = 2000 where id = ?`,
			supersededID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`update gateway_api_key_source_bindings set retired_at_ms = 2001 where api_key_id = ?`,
			supersededID); err != nil {
			t.Fatal(err)
		}

		// Key D: explicitly retired binding (activeRows == 0, retiredRows == 1) must never be resurrected even if missing.
		retiredBindingKey := stagedRecord(t, strings.Repeat("9", 32), usageOtherID, "cpamp_sk_"+strings.Repeat("4", 64))
		retiredBindingKey.Identity.CreatedAtMS = 1000
		retiredBindingKey.Identity.UpdatedAtMS = 1000
		if _, err := repo.Create(ctx, retiredBindingKey, "legacy-runtime"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`update gateway_api_key_identities set lifecycle = 'missing', revision = 2, updated_at_ms = 2000 where id = ?`,
			usageOtherID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx,
			`update gateway_api_key_source_bindings set retired_at_ms = 2001 where api_key_id = ?`,
			usageOtherID); err != nil {
			t.Fatal(err)
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		boundCount, err := repo.BindFreshKeySourcesTx(ctx, tx, usageRuntime, 2200)
		if err != nil || boundCount != 1 {
			_ = tx.Rollback()
			t.Fatalf("BindFreshKeySourcesTx = %d (%v), want 1", boundCount, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}

		recoveredA, err := repo.ByID(ctx, identity.APIKeyID(usageKeyID))
		if err != nil || recoveredA == nil || recoveredA.Identity.Lifecycle != identity.LifecycleActive ||
			recoveredA.Identity.Revision != 3 || recoveredA.Identity.UpdatedAtMS != 2500 {
			t.Fatalf("handed-off missing key A = %+v (%v), want active rev=3 updatedAtMs=2500", recoveredA, err)
		}
		rowsA := bindingRows(t, db, usageKeyID)
		if len(rowsA) != 2 || !rowsA[0].retiredAtMS.Valid || rowsA[0].retiredAtMS.Int64 != 2500 ||
			rowsA[1].runtimeIdentity != usageRuntime || rowsA[1].firstSeenAtMS != 2500 || rowsA[1].retiredAtMS.Valid {
			t.Fatalf("handed-off key A bindings = %+v, want [1000, 2500) and [2500, null)", rowsA)
		}

		unchangedB, err := repo.ByID(ctx, identity.APIKeyID(usageKeeper))
		if err != nil || unchangedB == nil || unchangedB.Identity.Lifecycle != identity.LifecycleMissing || unchangedB.Identity.Revision != 2 {
			t.Fatalf("same-runtime missing key B = %+v (%v), want preserved missing rev=2", unchangedB, err)
		}
		unchangedC, err := repo.ByID(ctx, identity.APIKeyID(supersededID))
		if err != nil || unchangedC == nil || unchangedC.Identity.Lifecycle != identity.LifecycleSuperseded {
			t.Fatalf("superseded key C = %+v (%v), want superseded", unchangedC, err)
		}
		rowsD := bindingRows(t, db, usageOtherID)
		if len(rowsD) != 1 || !rowsD[0].retiredAtMS.Valid || rowsD[0].retiredAtMS.Int64 != 2001 {
			t.Fatalf("explicitly retired binding key D = %+v, want no active binding", rowsD)
		}
	})
}
