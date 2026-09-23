package identitystore_test

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"testing"

	adapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func mutationRepo(t *testing.T) (*sql.DB, ports.Repository, ports.MutationRepository) {
	t.Helper()
	db, repo := setupTestDB(t)
	return db, repo, repo.(ports.MutationRepository)
}

func seedAPIKey(t *testing.T, repo ports.Repository, hash string) identity.APIKeyIdentity {
	t.Helper()
	_, err := repo.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1,
		APIKeys: []ports.APIKeySnapshotItem{{APIKeyHash: hash}}, NowMS: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	ent, _, err := repo.FindActiveAPIKeyBySource(context.Background(), "runtime-1", hash)
	if err != nil {
		t.Fatal(err)
	}
	return ent
}

func prepareMutation(t *testing.T, repo ports.MutationRepository, kind ports.APIKeyMutationKind, oldHash, newHash string, revisionTime int64) string {
	t.Helper()
	evidence := ports.APIKeyMutationEvidence{OldHash: oldHash, NewHash: newHash, NormalizedOldCount: 1}
	if kind == ports.APIKeyMutationRotate {
		evidence.ExactOldCount = 1
	}
	id, err := repo.PrepareAPIKeyMutation(context.Background(), ports.PrepareAPIKeyMutationParams{
		Kind: kind, RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1,
		Evidence: evidence, OwnerInstance: "process-A", NowMS: revisionTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPendingRotationSuppressesUntilForwardCompletes(t *testing.T) {
	db, repo, mutations := mutationRepo(t)
	ctx := context.Background()
	oldHash, newHash, otherHash := sha256Hex("old"), sha256Hex("new"), sha256Hex("other")
	old := seedAPIKey(t, repo, oldHash)
	id := prepareMutation(t, mutations, ports.APIKeyMutationRotate, oldHash, newHash, 2000)
	var persistedID string
	if err := db.QueryRow("select id from " + sqlite.GatewayAPIKeyMutationIntentsTable).Scan(&persistedID); err != nil || persistedID != id {
		t.Fatalf("intent was not committed: id=%q err=%v", persistedID, err)
	}
	result, err := repo.ApplyPassiveSnapshot(ctx, ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1, ProcessInstanceID: "process-A",
		CaptureStartedAtMS: 1999, NowMS: 2002,
		APIKeys: []ports.APIKeySnapshotItem{{APIKeyHash: newHash}, {APIKeyHash: otherHash}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.APIKeysCreated != 1 || result.APIKeysMissing != 0 {
		t.Fatalf("pending rotation was passively reinterpreted: %+v", result)
	}
	if _, _, err := repo.FindActiveAPIKeyBySource(ctx, "runtime-1", newHash); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("new hash acquired an ID before resolution: %v", err)
	}
	unchanged, err := repo.LoadAPIKeyByID(ctx, old.ID)
	if err != nil || unchanged.Revision != old.Revision || unchanged.Lifecycle != identity.LifecycleActive {
		t.Fatalf("old ID changed while pending: %+v %v", unchanged, err)
	}
	if err := mutations.MarkAPIKeyMutationForwardComplete(ctx, id, "process-A", 2003); err != nil {
		t.Fatal(err)
	}
	// This inventory began before the completion marker. It must still suppress.
	_, err = repo.ApplyPassiveSnapshot(ctx, ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1, ProcessInstanceID: "process-A",
		CaptureStartedAtMS: 2002, NowMS: 2004,
		APIKeys: []ports.APIKeySnapshotItem{{APIKeyHash: newHash}, {APIKeyHash: otherHash}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if pending, _ := mutations.HasPendingAPIKeyMutation(ctx, "runtime-1"); !pending {
		t.Fatal("pre-completion snapshot cleared the intent")
	}
	_, err = repo.ApplyPassiveSnapshot(ctx, ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2, ProcessInstanceID: "process-A",
		CaptureStartedAtMS: 2005, NowMS: 2006,
		APIKeys: []ports.APIKeySnapshotItem{{APIKeyHash: newHash}, {APIKeyHash: otherHash}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rotated, binding, err := repo.FindActiveAPIKeyBySource(ctx, "runtime-1", newHash)
	if err != nil || rotated.ID != old.ID || rotated.Revision != old.Revision+1 ||
		binding.ObservedRuntimeGeneration != 2 {
		t.Fatalf("rotation state: %+v %+v %v", rotated, binding, err)
	}
	if pending, _ := mutations.HasPendingAPIKeyMutation(ctx, "runtime-1"); pending {
		t.Fatal("resolved intent still pending")
	}
}

func TestRecoveredRotationKeepsSourceHandoffOrderedAfterClockRollback(t *testing.T) {
	db, repo, mutations := mutationRepo(t)
	ctx := context.Background()
	oldHash, newHash := sha256Hex("old"), sha256Hex("new")
	old := seedAPIKey(t, repo, oldHash)
	if _, err := repo.ApplyPassiveSnapshot(ctx, ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1,
		APIKeys: []ports.APIKeySnapshotItem{{APIKeyHash: oldHash}}, NowMS: 5000,
	}); err != nil {
		t.Fatal(err)
	}
	prepareMutation(t, mutations, ports.APIKeyMutationRotate, oldHash, newHash, 6000)

	// The old process forwarded the rotation and died before finalization.
	// A new process observes the new source after the wall clock moved backward.
	if _, err := repo.ApplyPassiveSnapshot(ctx, ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2,
		ProcessInstanceID: "process-B", CaptureStartedAtMS: 800, NowMS: 800,
		APIKeys: []ports.APIKeySnapshotItem{{APIKeyHash: newHash}},
	}); err != nil {
		t.Fatal(err)
	}
	rotated, newBinding, err := repo.FindActiveAPIKeyBySource(ctx, "runtime-1", newHash)
	if err != nil || rotated.ID != old.ID || rotated.Revision != old.Revision+1 {
		t.Fatalf("recovered rotation identity: %+v %+v %v", rotated, newBinding, err)
	}
	var oldLastSeen, oldRetired int64
	if err := db.QueryRow(`select last_seen_at_ms, retired_at_ms from gateway_api_key_source_bindings
		where api_key_hash = ?`, oldHash).Scan(&oldLastSeen, &oldRetired); err != nil {
		t.Fatal(err)
	}
	if oldLastSeen != 5000 || oldRetired != 5000 ||
		newBinding.FirstSeenAtMS != oldRetired || newBinding.LastSeenAtMS != oldRetired ||
		newBinding.ObservedRuntimeGeneration != 2 {
		t.Fatalf("source handoff moved backward: old last=%d retired=%d new=%+v",
			oldLastSeen, oldRetired, newBinding)
	}
	if pending, err := mutations.HasPendingAPIKeyMutation(ctx, "runtime-1"); err != nil || pending {
		t.Fatalf("recovered rotation left intent: %v %v", pending, err)
	}
}

func TestAPIMutationResolverTruthTable(t *testing.T) {
	oldHash, newHash := sha256Hex("old"), sha256Hex("new")
	cases := []struct {
		name    string
		kind    ports.APIKeyMutationKind
		present []string
		want    ports.APIKeyMutationOutcome
	}{
		{"rotate-success", ports.APIKeyMutationRotate, []string{newHash}, ports.APIKeyMutationSuccess},
		{"rotate-not-applied", ports.APIKeyMutationRotate, []string{oldHash}, ports.APIKeyMutationNotApplied},
		{"rotate-both", ports.APIKeyMutationRotate, []string{oldHash, newHash}, ports.APIKeyMutationUnknown},
		{"rotate-neither", ports.APIKeyMutationRotate, nil, ports.APIKeyMutationUnknown},
		{"delete-success", ports.APIKeyMutationDelete, nil, ports.APIKeyMutationSuccess},
		{"delete-not-applied", ports.APIKeyMutationDelete, []string{oldHash}, ports.APIKeyMutationNotApplied},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, repo, mutations := mutationRepo(t)
			original := seedAPIKey(t, repo, oldHash)
			intentNew := ""
			if tc.kind == ports.APIKeyMutationRotate {
				intentNew = newHash
			}
			id := prepareMutation(t, mutations, tc.kind, oldHash, intentNew, 2000)
			if err := mutations.MarkAPIKeyMutationForwardComplete(context.Background(), id, "process-A", 2001); err != nil {
				t.Fatal(err)
			}
			outcome, err := mutations.ResolveAPIKeyMutation(context.Background(), ports.ResolveAPIKeyMutationParams{
				IntentID: id, RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2,
				ObservedHashes: tc.present, NowMS: 2002,
			})
			if err != nil || outcome != tc.want {
				t.Fatalf("outcome=%q err=%v, want %q", outcome, err, tc.want)
			}
			pending, err := mutations.HasPendingAPIKeyMutation(context.Background(), "runtime-1")
			if err != nil || pending != (tc.want == ports.APIKeyMutationUnknown) {
				t.Fatalf("pending=%v err=%v", pending, err)
			}
			current, err := repo.LoadAPIKeyByID(context.Background(), original.ID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == ports.APIKeyMutationSuccess {
				if current.Revision != original.Revision+1 {
					t.Fatalf("revision=%d", current.Revision)
				}
				wantLifecycle := identity.LifecycleActive
				if tc.kind == ports.APIKeyMutationDelete {
					wantLifecycle = identity.LifecycleSuperseded
				}
				if current.Lifecycle != wantLifecycle {
					t.Fatalf("lifecycle=%q", current.Lifecycle)
				}
			} else if current != original {
				t.Fatalf("non-success modified identity: %+v", current)
			}
		})
	}
}

func TestMutationCrashRecoveryAndDeleteReappearance(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repo := adapter.New(db)
	mutations := repo.(ports.MutationRepository)
	oldHash, newHash := sha256Hex("old"), sha256Hex("new")
	old := seedAPIKey(t, repo, oldHash)
	prepareMutation(t, mutations, ports.APIKeyMutationRotate, oldHash, newHash, 2000)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repo = adapter.New(db)
	mutations = repo.(ports.MutationRepository)
	_, err = repo.ApplyPassiveSnapshot(ctx, ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2, ProcessInstanceID: "process-B",
		CaptureStartedAtMS: 2100, NowMS: 2200,
		APIKeys: []ports.APIKeySnapshotItem{{APIKeyHash: newHash}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rotated, _, err := repo.FindActiveAPIKeyBySource(ctx, "runtime-1", newHash)
	if err != nil || rotated.ID != old.ID || rotated.Revision != old.Revision+1 {
		t.Fatalf("restart rotation: %+v %v", rotated, err)
	}
	id := prepareMutation(t, mutations, ports.APIKeyMutationDelete, newHash, "", 2300)
	if err := mutations.MarkAPIKeyMutationForwardComplete(ctx, id, "process-A", 2301); err != nil {
		t.Fatal(err)
	}
	_, err = mutations.ResolveAPIKeyMutation(ctx, ports.ResolveAPIKeyMutationParams{
		IntentID: id, RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2, NowMS: 2302,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.ApplyPassiveSnapshot(ctx, ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 3, ProcessInstanceID: "process-B",
		CaptureStartedAtMS: 2400, NowMS: 2400,
		APIKeys: []ports.APIKeySnapshotItem{{APIKeyHash: newHash}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reappeared, _, err := repo.FindActiveAPIKeyBySource(ctx, "runtime-1", newHash)
	if err != nil || reappeared.ID == old.ID {
		t.Fatalf("superseded identity was reused: %+v %v", reappeared, err)
	}
}

func TestMutationFinalizeFailureRollsBackAndKeepsIntent(t *testing.T) {
	db, repo, mutations := mutationRepo(t)
	ctx := context.Background()
	oldHash, newHash := sha256Hex("old"), sha256Hex("new")
	old := seedAPIKey(t, repo, oldHash)
	id := prepareMutation(t, mutations, ports.APIKeyMutationRotate, oldHash, newHash, 2000)
	if err := mutations.MarkAPIKeyMutationForwardComplete(ctx, id, "process-A", 2001); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`create trigger fail_rotated_binding before insert on gateway_api_key_source_bindings
		when new.api_key_hash = '` + newHash + `' begin select raise(fail, 'injected insert failure'); end`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = mutations.ResolveAPIKeyMutation(ctx, ports.ResolveAPIKeyMutationParams{
		IntentID: id, RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2,
		ObservedHashes: []string{newHash}, NowMS: 2002,
	})
	if err == nil {
		t.Fatal("finalize unexpectedly succeeded")
	}
	current, err := repo.LoadAPIKeyByID(ctx, old.ID)
	if err != nil || current != old {
		t.Fatalf("partial identity mutation: %+v %v", current, err)
	}
	if pending, _ := mutations.HasPendingAPIKeyMutation(ctx, "runtime-1"); !pending {
		t.Fatal("failed finalize lost recovery intent")
	}
	var activeOld int
	if err := db.QueryRow("select count(*) from gateway_api_key_source_bindings where api_key_hash = ? and retired_at_ms is null", oldHash).Scan(&activeOld); err != nil || activeOld != 1 {
		t.Fatalf("old binding partially retired: count=%d err=%v", activeOld, err)
	}
}

func TestMutationCorruptionOverflowAndMissingRotation(t *testing.T) {
	oldHash, newHash := sha256Hex("old"), sha256Hex("new")
	t.Run("missing rotation one bump", func(t *testing.T) {
		_, repo, mutations := mutationRepo(t)
		old := seedAPIKey(t, repo, oldHash)
		missing, err := repo.SetAPIKeyLifecycle(context.Background(), old.ID, old.Revision, identity.LifecycleMissing, 1500)
		if err != nil {
			t.Fatal(err)
		}
		id := prepareMutation(t, mutations, ports.APIKeyMutationRotate, oldHash, newHash, 2000)
		if err := mutations.MarkAPIKeyMutationForwardComplete(context.Background(), id, "process-A", 2001); err != nil {
			t.Fatal(err)
		}
		_, err = mutations.ResolveAPIKeyMutation(context.Background(), ports.ResolveAPIKeyMutationParams{
			IntentID: id, RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2,
			ObservedHashes: []string{newHash}, NowMS: 2002,
		})
		if err != nil {
			t.Fatal(err)
		}
		current, _ := repo.LoadAPIKeyByID(context.Background(), old.ID)
		if current.Revision != missing.Revision+1 || current.Lifecycle != identity.LifecycleActive {
			t.Fatalf("missing rotation: %+v", current)
		}
	})
	t.Run("revision overflow", func(t *testing.T) {
		db, repo, mutations := mutationRepo(t)
		old := seedAPIKey(t, repo, oldHash)
		if _, err := db.Exec("update gateway_api_key_identities set revision = ? where id = ?", int64(math.MaxInt64), old.ID); err != nil {
			t.Fatal(err)
		}
		id := prepareMutation(t, mutations, ports.APIKeyMutationRotate, oldHash, newHash, 2000)
		if err := mutations.MarkAPIKeyMutationForwardComplete(context.Background(), id, "process-A", 2001); err != nil {
			t.Fatal(err)
		}
		_, err := mutations.ResolveAPIKeyMutation(context.Background(), ports.ResolveAPIKeyMutationParams{
			IntentID: id, RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2,
			ObservedHashes: []string{newHash}, NowMS: 2002,
		})
		if !errors.Is(err, ports.ErrRevisionOverflow) {
			t.Fatalf("overflow error: %v", err)
		}
	})
	t.Run("persisted intent corruption", func(t *testing.T) {
		db, repo, mutations := mutationRepo(t)
		seedAPIKey(t, repo, oldHash)
		prepareMutation(t, mutations, ports.APIKeyMutationRotate, oldHash, newHash, 2000)
		if _, err := db.Exec("pragma ignore_check_constraints = on"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("update gateway_api_key_mutation_intents set observed_runtime_generation = 'bad'"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("pragma ignore_check_constraints = off"); err != nil {
			t.Fatal(err)
		}
		if _, err := mutations.HasPendingAPIKeyMutation(context.Background(), "runtime-1"); err == nil {
			t.Fatal("corrupt intent accepted")
		}
	})
}

func TestPendingDeleteSuppressesOldButReconcilesOtherSources(t *testing.T) {
	_, repo, mutations := mutationRepo(t)
	oldHash, otherHash := sha256Hex("old"), sha256Hex("other")
	old := seedAPIKey(t, repo, oldHash)
	prepareMutation(t, mutations, ports.APIKeyMutationDelete, oldHash, "", 2000)
	result, err := repo.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1,
		ProcessInstanceID: "process-A", CaptureStartedAtMS: 2001, NowMS: 2002,
		APIKeys:     []ports.APIKeySnapshotItem{{APIKeyHash: otherHash}},
		Credentials: []ports.CredentialSnapshotItem{{SourceAuthID: "auth-1"}},
	})
	if err != nil || result.APIKeysMissing != 0 || result.APIKeysCreated != 1 ||
		result.CredentialsCreated != 1 {
		t.Fatalf("pending delete suppression: %+v %v", result, err)
	}
	current, err := repo.LoadAPIKeyByID(context.Background(), old.ID)
	if err != nil || current != old {
		t.Fatalf("delete became passive missing: %+v %v", current, err)
	}
}

func TestPendingMutationDoesNotResolveFromAnotherRuntimeIdentity(t *testing.T) {
	_, repo, mutations := mutationRepo(t)
	oldHash, newHash := sha256Hex("old"), sha256Hex("new")
	old := seedAPIKey(t, repo, oldHash)
	prepareMutation(t, mutations, ports.APIKeyMutationRotate, oldHash, newHash, 2000)
	result, err := repo.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-2", ObservedRuntimeGeneration: 8,
		ProcessInstanceID: "process-B", CaptureStartedAtMS: 2100, NowMS: 2100,
		APIKeys: []ports.APIKeySnapshotItem{{APIKeyHash: newHash}},
	})
	if err != nil || result.APIKeysCreated != 1 {
		t.Fatalf("other runtime reconciliation: %+v %v", result, err)
	}
	current, err := repo.LoadAPIKeyByID(context.Background(), old.ID)
	if err != nil || current != old {
		t.Fatalf("other runtime resolved old identity: %+v %v", current, err)
	}
	if pending, _ := mutations.HasPendingAPIKeyMutation(context.Background(), "runtime-1"); !pending {
		t.Fatal("other runtime removed pending intent")
	}
}

func TestMutationFinalizeRejectsConflictsAndCorruption(t *testing.T) {
	oldHash, newHash := sha256Hex("old"), sha256Hex("new")
	cases := []struct {
		name   string
		change func(t *testing.T, db *sql.DB, repo ports.Repository, old identity.APIKeyIdentity)
		want   error
	}{
		{"revision conflict", func(t *testing.T, db *sql.DB, _ ports.Repository, old identity.APIKeyIdentity) {
			_, err := db.Exec("update gateway_api_key_identities set revision = revision + 1 where id = ?", old.ID)
			if err != nil {
				t.Fatal(err)
			}
		}, ports.ErrRevisionConflict},
		{"corrupt entity", func(t *testing.T, db *sql.DB, _ ports.Repository, old identity.APIKeyIdentity) {
			_, err := db.Exec("update gateway_api_key_identities set lifecycle = 'broken' where id = ?", old.ID)
			if err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"corrupt binding", func(t *testing.T, db *sql.DB, _ ports.Repository, _ identity.APIKeyIdentity) {
			_, err := db.Exec("update gateway_api_key_source_bindings set first_seen_at_ms = 0 where api_key_hash = ?", oldHash)
			if err != nil {
				t.Fatal(err)
			}
		}, nil},
		{"new source conflict", func(t *testing.T, _ *sql.DB, repo ports.Repository, _ identity.APIKeyIdentity) {
			id, err := identity.NewAPIKeyID()
			if err != nil {
				t.Fatal(err)
			}
			err = repo.CreateAPIKey(context.Background(),
				identity.APIKeyIdentity{ID: id, Revision: 1, Lifecycle: identity.LifecycleActive, CreatedAtMS: 1500, UpdatedAtMS: 1500},
				identity.APIKeySourceBinding{APIKeyID: id, RuntimeIdentity: "runtime-1", APIKeyHash: newHash,
					ObservedRuntimeGeneration: 1, FirstSeenAtMS: 1500, LastSeenAtMS: 1500})
			if err != nil {
				t.Fatal(err)
			}
		}, ports.ErrSourceBindingConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, repo, mutations := mutationRepo(t)
			old := seedAPIKey(t, repo, oldHash)
			id := prepareMutation(t, mutations, ports.APIKeyMutationRotate, oldHash, newHash, 2000)
			if err := mutations.MarkAPIKeyMutationForwardComplete(context.Background(), id, "process-A", 2001); err != nil {
				t.Fatal(err)
			}
			tc.change(t, db, repo, old)
			_, err := mutations.ResolveAPIKeyMutation(context.Background(), ports.ResolveAPIKeyMutationParams{
				IntentID: id, RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2,
				ObservedHashes: []string{newHash}, NowMS: 2002,
			})
			if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("invalid state accepted or wrong error: %v", err)
			}
			if pending, _ := mutations.HasPendingAPIKeyMutation(context.Background(), "runtime-1"); !pending {
				t.Fatal("failure lost pending intent")
			}
		})
	}
}

func TestDeleteMissingIdentityAndClockRollback(t *testing.T) {
	db, repo, mutations := mutationRepo(t)
	oldHash := sha256Hex("old")
	old := seedAPIKey(t, repo, oldHash)
	missing, err := repo.SetAPIKeyLifecycle(context.Background(), old.ID, old.Revision, identity.LifecycleMissing, 1500)
	if err != nil {
		t.Fatal(err)
	}
	id := prepareMutation(t, mutations, ports.APIKeyMutationDelete, oldHash, "", 900)
	if err := mutations.MarkAPIKeyMutationForwardComplete(context.Background(), id, "process-A", 901); err != nil {
		t.Fatal(err)
	}
	_, err = mutations.ResolveAPIKeyMutation(context.Background(), ports.ResolveAPIKeyMutationParams{
		IntentID: id, RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2, NowMS: 800,
	})
	if err != nil {
		t.Fatal(err)
	}
	current, err := repo.LoadAPIKeyByID(context.Background(), old.ID)
	if err != nil || current.Revision != missing.Revision+1 ||
		current.Lifecycle != identity.LifecycleSuperseded || current.UpdatedAtMS <= missing.UpdatedAtMS {
		t.Fatalf("missing delete / rollback: %+v %v", current, err)
	}
	var retiredAt int64
	if err := db.QueryRow("select retired_at_ms from gateway_api_key_source_bindings where api_key_hash = ?", oldHash).Scan(&retiredAt); err != nil ||
		retiredAt < 1000 {
		t.Fatalf("retirement timestamp moved backwards: %d %v", retiredAt, err)
	}
}
