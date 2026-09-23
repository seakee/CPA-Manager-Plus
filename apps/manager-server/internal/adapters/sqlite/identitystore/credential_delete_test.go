package identitystore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func credentialDeleteFixture(t *testing.T, sources ...string) (*sql.DB, *repository, map[string]identity.CredentialID) {
	t.Helper()
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "credential-delete.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	repo := New(db).(*repository)
	items := make([]ports.CredentialSnapshotItem, len(sources))
	for i, source := range sources {
		items[i] = ports.CredentialSnapshotItem{SourceAuthID: source, PhysicalName: "shared.json", Provider: "codex"}
	}
	_, err = repo.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1, Credentials: items, NowMS: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]identity.CredentialID)
	for _, source := range sources {
		ent, _, err := repo.FindActiveCredentialBySource(context.Background(), "runtime-1", source)
		if err != nil {
			t.Fatal(err)
		}
		ids[source] = ent.ID
	}
	return db, repo, ids
}

func prepareCredentialDeleteFixture(t *testing.T, repo *repository, sources ...string) string {
	t.Helper()
	id, err := repo.PrepareCredentialDelete(context.Background(), ports.PrepareCredentialDeleteParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1, PhysicalName: "shared.json",
		SourceAuthIDs: sources, OwnerInstance: "process-A", NowMS: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func credentialDeleteState(t *testing.T, db *sql.DB, id identity.CredentialID) (int64, string, sql.NullInt64) {
	t.Helper()
	var revision int64
	var lifecycle string
	var retired sql.NullInt64
	err := db.QueryRow(`select i.revision, i.lifecycle, b.retired_at_ms
		from gateway_credential_identities i join gateway_credential_source_bindings b on b.credential_id = i.id
		where i.id = ?`, string(id)).Scan(&revision, &lifecycle, &retired)
	if err != nil {
		t.Fatal(err)
	}
	return revision, lifecycle, retired
}

func assertCredentialDeleteIntentCount(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var parents, items int
	if err := db.QueryRow(`select count(*) from gateway_credential_delete_intents`).Scan(&parents); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`select count(*) from gateway_credential_delete_intent_items`).Scan(&items); err != nil {
		t.Fatal(err)
	}
	if parents != want || (want == 0 && items != 0) {
		t.Fatalf("intents parents=%d items=%d, want parents=%d", parents, items, want)
	}
}

func TestCredentialDeleteMultiMemberTruthTableAndAtomicTerminal(t *testing.T) {
	for _, tc := range []struct {
		name     string
		observed []string
		want     ports.CredentialDeleteOutcome
	}{
		{"all absent", nil, ports.CredentialDeleteSuccess},
		{"all present", []string{"A", "B", "C"}, ports.CredentialDeleteNotApplied},
		{"mixed", []string{"C"}, ports.CredentialDeleteUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, repo, ids := credentialDeleteFixture(t, "A", "B", "C")
			id := prepareCredentialDeleteFixture(t, repo, "A", "B", "C")
			if err := repo.MarkCredentialDeleteForwardComplete(context.Background(), id, "process-A", 2500); err != nil {
				t.Fatal(err)
			}
			got, err := repo.ResolveCredentialDelete(context.Background(), ports.ResolveCredentialDeleteParams{
				RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2,
				ObservedSourceAuthIDs: tc.observed, IntentID: id, NowMS: 3000, PhysicalEvidence: ports.PhysicalSourcePresent,
			})
			if err != nil || got != tc.want {
				t.Fatalf("outcome=%q error=%v, want %q", got, err, tc.want)
			}
			wantPending := 0
			if tc.want == ports.CredentialDeleteUnknown {
				wantPending = 1
			}
			assertCredentialDeleteIntentCount(t, db, wantPending)
			for _, source := range []string{"A", "B", "C"} {
				revision, lifecycle, retired := credentialDeleteState(t, db, ids[source])
				if tc.want == ports.CredentialDeleteSuccess {
					if revision != 2 || lifecycle != string(identity.LifecycleSuperseded) || !retired.Valid {
						t.Fatalf("%s: revision=%d lifecycle=%q retired=%v", source, revision, lifecycle, retired)
					}
				} else if revision != 1 || lifecycle != string(identity.LifecycleActive) || retired.Valid {
					t.Fatalf("%s changed on %s: revision=%d lifecycle=%q retired=%v", source, tc.name, revision, lifecycle, retired)
				}
			}
		})
	}
}

func TestCredentialDeleteCrashRecoveryAndStaleCapture(t *testing.T) {
	for _, tc := range []struct {
		name          string
		observed      []ports.CredentialSnapshotItem
		wantLifecycle identity.Lifecycle
	}{
		{"before upstream", []ports.CredentialSnapshotItem{{SourceAuthID: "A"}}, identity.LifecycleActive},
		{"after upstream", nil, identity.LifecycleSuperseded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, repo, ids := credentialDeleteFixture(t, "A")
			intentID := prepareCredentialDeleteFixture(t, repo, "A")
			// A same-process capture made before completion cannot decide absence.
			if _, err := repo.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
				RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1,
				ProcessInstanceID: "process-A", CaptureStartedAtMS: 2100, NowMS: 2200,
			}); err != nil {
				t.Fatal(err)
			}
			rev, lifecycle, _ := credentialDeleteState(t, db, ids["A"])
			if rev != 1 || lifecycle != string(identity.LifecycleActive) {
				t.Fatal("stale capture changed pending credential")
			}
			// A new process may resolve a predecessor's pending intent without a marker.
			if _, err := repo.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
				RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2,
				ProcessInstanceID: "process-B", CaptureStartedAtMS: 100, NowMS: 2300,
				Credentials:                      tc.observed,
				CredentialDeletePhysicalEvidence: map[string]ports.PhysicalSourceEvidence{intentID: ports.PhysicalSourcePresent},
			}); err != nil {
				t.Fatal(err)
			}
			assertCredentialDeleteIntentCount(t, db, 0)
			_, lifecycle, _ = credentialDeleteState(t, db, ids["A"])
			if lifecycle != string(tc.wantLifecycle) {
				t.Fatalf("lifecycle=%q want=%q", lifecycle, tc.wantLifecycle)
			}
		})
	}
}

func TestCredentialDeletePassiveMixedSuppressesOnlyTargetSources(t *testing.T) {
	db, repo, ids := credentialDeleteFixture(t, "A", "B", "C")
	prepareCredentialDeleteFixture(t, repo, "A", "B", "C")
	result, err := repo.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2,
		ProcessInstanceID: "process-B", CaptureStartedAtMS: 10, NowMS: 3000,
		Credentials: []ports.CredentialSnapshotItem{
			{SourceAuthID: "C"}, {SourceAuthID: "unrelated"},
		},
		APIKeys: []ports.APIKeySnapshotItem{{APIKeyHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.CredentialsCreated != 1 || result.APIKeysCreated != 1 ||
		result.CredentialsMissing != 0 || result.CredentialsRefreshed != 0 {
		t.Fatalf("passive result=%+v", result)
	}
	assertCredentialDeleteIntentCount(t, db, 1)
	for _, source := range []string{"A", "B", "C"} {
		rev, lifecycle, retired := credentialDeleteState(t, db, ids[source])
		if rev != 1 || lifecycle != string(identity.LifecycleActive) || retired.Valid {
			t.Fatalf("%s changed under mixed observation: %d %s %v", source, rev, lifecycle, retired)
		}
	}
	var lastSeen int64
	if err := db.QueryRow(`select last_seen_at_ms from gateway_credential_source_bindings
		where credential_id = ? and retired_at_ms is null`, ids["C"]).Scan(&lastSeen); err != nil {
		t.Fatal(err)
	}
	if lastSeen != 1000 {
		t.Fatalf("present but suppressed C was refreshed at %d", lastSeen)
	}
}

func TestCredentialDeleteMissingToSupersededOnceAndReappearanceNewID(t *testing.T) {
	db, repo, ids := credentialDeleteFixture(t, "A")
	if _, err := repo.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1, NowMS: 1500,
	}); err != nil {
		t.Fatal(err)
	}
	id := prepareCredentialDeleteFixture(t, repo, "A")
	if err := repo.MarkCredentialDeleteForwardComplete(context.Background(), id, "process-A", 2001); err != nil {
		t.Fatal(err)
	}
	outcome, err := repo.ResolveCredentialDelete(context.Background(), ports.ResolveCredentialDeleteParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1, IntentID: id, NowMS: 2500,
	})
	if err != nil || outcome != ports.CredentialDeleteSuccess {
		t.Fatalf("outcome=%q error=%v", outcome, err)
	}
	rev, lifecycle, retired := credentialDeleteState(t, db, ids["A"])
	if rev != 3 || lifecycle != string(identity.LifecycleSuperseded) || !retired.Valid {
		t.Fatalf("missing delete revision=%d lifecycle=%q retired=%v", rev, lifecycle, retired)
	}
	if _, err := repo.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2, NowMS: 3000,
		Credentials: []ports.CredentialSnapshotItem{{SourceAuthID: "A"}},
	}); err != nil {
		t.Fatal(err)
	}
	newEnt, _, err := repo.FindActiveCredentialBySource(context.Background(), "runtime-1", "A")
	if err != nil || newEnt.ID == ids["A"] {
		t.Fatalf("reappearing source reused old ID: new=%q old=%q err=%v", newEnt.ID, ids["A"], err)
	}
}

func TestCredentialDeletePersistentOverlapAndDistinctFiles(t *testing.T) {
	_, repo, _ := credentialDeleteFixture(t, "A", "B")
	prepareCredentialDeleteFixture(t, repo, "A")
	for _, tc := range []struct {
		names    []string
		all      bool
		conflict bool
	}{
		{[]string{"shared.json"}, false, true},
		{[]string{"SHARED.JSON"}, false, true},
		{nil, true, true},
		{[]string{"other.json"}, false, false},
	} {
		err := repo.CheckPendingCredentialDelete(context.Background(), tc.names, tc.all)
		if (err != nil) != tc.conflict {
			t.Fatalf("names=%v all=%t err=%v", tc.names, tc.all, err)
		}
	}
	_, err := repo.PrepareCredentialDelete(context.Background(), ports.PrepareCredentialDeleteParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1,
		PhysicalName: "other.json", SourceAuthIDs: []string{"B"}, OwnerInstance: "process-A", NowMS: 2100,
	})
	if err != nil {
		t.Fatalf("unrelated physical file blocked: %v", err)
	}
}

func TestCredentialDeleteNotAppliedRestoresOwnershipAcrossRecovery(t *testing.T) {
	for _, initiallyUnknown := range []bool{false, true} {
		t.Run(map[bool]string{false: "crash before forward", true: "unknown then not applied"}[initiallyUnknown], func(t *testing.T) {
			db, repo, ids := credentialDeleteFixture(t, "A")
			owner := model.CodexInspectionDisableOwnership{FileName: "shared.json", Provider: "codex", AuthIndex: "idx-A", AccountID: "account-A", AccountSnapshot: "account@example.com", DisabledAtMS: 1100, UpdatedAtMS: 1200}
			intentID, err := repo.PrepareCredentialDelete(context.Background(), ports.PrepareCredentialDeleteParams{
				RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1, PhysicalName: "shared.json",
				SourceAuthIDs: []string{"A"}, OwnerInstance: "dead-process", NowMS: 2000,
				RevokedOwnership: []model.CodexInspectionDisableOwnership{owner},
			})
			if err != nil {
				t.Fatal(err)
			}
			apply := func(evidence ports.PhysicalSourceEvidence, now int64) error {
				_, err := repo.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
					RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2,
					ProcessInstanceID: "new-process", CaptureStartedAtMS: now - 100, NowMS: now,
					Credentials:                      []ports.CredentialSnapshotItem{{SourceAuthID: "A", PhysicalName: "shared.json", Provider: "codex"}},
					CredentialDeletePhysicalEvidence: map[string]ports.PhysicalSourceEvidence{intentID: evidence},
				})
				return err
			}
			if initiallyUnknown {
				if err := apply(ports.PhysicalSourceAbsent, 2500); err != nil {
					t.Fatal(err)
				}
				assertCredentialDeleteIntentCount(t, db, 1)
			}
			if _, err := db.Exec(`create trigger fail_restore before insert on codex_inspection_disable_ownership begin select raise(abort, 'temporary restore failure'); end`); err != nil {
				t.Fatal(err)
			}
			if err := apply(ports.PhysicalSourcePresent, 3000); err == nil {
				t.Fatal("failed ownership restore resolved intent")
			}
			assertCredentialDeleteIntentCount(t, db, 1)
			if _, err := db.Exec(`drop trigger fail_restore`); err != nil {
				t.Fatal(err)
			}
			if err := apply(ports.PhysicalSourcePresent, 3500); err != nil {
				t.Fatal(err)
			}
			assertCredentialDeleteIntentCount(t, db, 0)
			var count int
			if err := db.QueryRow(`select count(*) from codex_inspection_disable_ownership where file_name = ? and provider = ? and auth_index = ? and account_id = ? and account_snapshot = ?`, owner.FileName, owner.Provider, owner.AuthIndex, owner.AccountID, owner.AccountSnapshot).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("restored ownership count=%d", count)
			}
			rev, lifecycle, retired := credentialDeleteState(t, db, ids["A"])
			if rev != 1 || lifecycle != "active" || retired.Valid {
				t.Fatalf("Canonical changed: %d %s %v", rev, lifecycle, retired)
			}
		})
	}
}

func TestCredentialDeleteCorruptPendingItemFailsBeforeSuppression(t *testing.T) {
	db, repo, ids := credentialDeleteFixture(t, "A", "B")
	intentID := prepareCredentialDeleteFixture(t, repo, "A")
	if _, err := db.Exec(`update gateway_credential_delete_intent_items set source_auth_id = 'B' where intent_id = ?`, intentID); err != nil {
		t.Fatal(err)
	}
	_, err := repo.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2,
		ProcessInstanceID: "process-A", CaptureStartedAtMS: 2100, NowMS: 2200,
	})
	if err == nil {
		t.Fatal("corrupt pending item suppressed another source")
	}
	assertCredentialDeleteIntentCount(t, db, 1)
	for _, source := range []string{"A", "B"} {
		rev, lifecycle, _ := credentialDeleteState(t, db, ids[source])
		if rev != 1 || lifecycle != "active" {
			t.Fatalf("%s changed: %d %s", source, rev, lifecycle)
		}
	}
}

func TestCredentialDeleteFinalizeRollsBackAllMembersOnMiddleFailure(t *testing.T) {
	db, repo, ids := credentialDeleteFixture(t, "A", "B", "C")
	id := prepareCredentialDeleteFixture(t, repo, "A", "B", "C")
	if err := repo.MarkCredentialDeleteForwardComplete(context.Background(), id, "process-A", 2100); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`create trigger fail_second_credential_delete before update on gateway_credential_identities
		when old.id = '` + string(ids["B"]) + `' begin select raise(abort, 'injected finalize failure'); end`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.ResolveCredentialDelete(context.Background(), ports.ResolveCredentialDeleteParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1, IntentID: id, NowMS: 3000,
	})
	if err == nil {
		t.Fatal("injected middle failure was ignored")
	}
	assertCredentialDeleteIntentCount(t, db, 1)
	for _, source := range []string{"A", "B", "C"} {
		rev, lifecycle, retired := credentialDeleteState(t, db, ids[source])
		if rev != 1 || lifecycle != string(identity.LifecycleActive) || retired.Valid {
			t.Fatalf("%s partially changed: %d %s %v", source, rev, lifecycle, retired)
		}
	}
}

func TestCredentialDeleteFinalizationKeepsTimestampsMonotonicAfterClockRollback(t *testing.T) {
	db, repo, ids := credentialDeleteFixture(t, "A")
	if _, err := repo.ApplyPassiveSnapshot(context.Background(), ports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1, NowMS: 6000,
		Credentials: []ports.CredentialSnapshotItem{{SourceAuthID: "A"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`update gateway_credential_identities set updated_at_ms = 7000 where id = ?`, ids["A"]); err != nil {
		t.Fatal(err)
	}
	id, err := repo.PrepareCredentialDelete(context.Background(), ports.PrepareCredentialDeleteParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1,
		PhysicalName: "shared.json", SourceAuthIDs: []string{"A"}, OwnerInstance: "process-A", NowMS: 8000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkCredentialDeleteForwardComplete(context.Background(), id, "process-A", 8001); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ResolveCredentialDelete(context.Background(), ports.ResolveCredentialDeleteParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2, IntentID: id, NowMS: 1,
	}); err != nil {
		t.Fatal(err)
	}
	var updatedAt, retiredAt int64
	if err := db.QueryRow(`select i.updated_at_ms, b.retired_at_ms from gateway_credential_identities i
		join gateway_credential_source_bindings b on b.credential_id = i.id where i.id = ?`, ids["A"]).
		Scan(&updatedAt, &retiredAt); err != nil {
		t.Fatal(err)
	}
	if updatedAt != 7001 || retiredAt != 6000 {
		t.Fatalf("updatedAt=%d retiredAt=%d", updatedAt, retiredAt)
	}
}

func TestCredentialDeleteRevisionAndPersistedCorruptionFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		sql  string
	}{
		{"parent", `update gateway_credential_delete_intents set owner_instance = ''`},
		{"item", `update gateway_credential_delete_intent_items set expected_revision = 0`},
		{"identity", `update gateway_credential_identities set lifecycle = 'invalid'`},
		{"binding", `update gateway_credential_source_bindings set first_seen_at_ms = 0`},
		{"generation", `update gateway_credential_delete_intents set observed_runtime_generation = 'bad'`},
		{"binding generation", `update gateway_credential_source_bindings set observed_runtime_generation = 'bad'`},
		{"revision conflict", `update gateway_credential_identities set revision = 2`},
		{"revision overflow", `update gateway_credential_identities set revision = 9223372036854775807; update gateway_credential_delete_intent_items set expected_revision = 9223372036854775807`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, repo, _ := credentialDeleteFixture(t, "A")
			id := prepareCredentialDeleteFixture(t, repo, "A")
			if err := repo.MarkCredentialDeleteForwardComplete(context.Background(), id, "process-A", 2100); err != nil {
				t.Fatal(err)
			}
			// Deliberately corrupt persisted data through SQL to exercise read validation.
			if _, err := db.Exec(`pragma ignore_check_constraints=on`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(tc.sql); err != nil {
				t.Fatal(err)
			}
			_, err := repo.ResolveCredentialDelete(context.Background(), ports.ResolveCredentialDeleteParams{
				RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 1, IntentID: id, NowMS: 3000,
			})
			if err == nil {
				t.Fatal("corrupt state was accepted")
			}
			assertCredentialDeleteIntentCount(t, db, 1)
		})
	}
}
