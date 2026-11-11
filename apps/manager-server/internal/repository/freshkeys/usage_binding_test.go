package freshkeys

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
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
