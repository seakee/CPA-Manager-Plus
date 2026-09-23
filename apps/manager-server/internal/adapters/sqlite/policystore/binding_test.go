package policystore_test

import (
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	identityadapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identitystore"
	adapter "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/policystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/resourcepolicy"
	identityports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/policystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func snapshot(t *testing.T, repo identityports.Repository, generation uint64, now int64, hashes ...string) {
	t.Helper()
	items := make([]identityports.APIKeySnapshotItem, 0, len(hashes))
	for _, h := range hashes {
		items = append(items, identityports.APIKeySnapshotItem{APIKeyHash: h})
	}
	_, err := repo.ApplyPassiveSnapshot(ctx, identityports.ReconcileSnapshotParams{
		RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: generation,
		APIKeys: items, NowMS: now,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func seedKey(t *testing.T, repo identityports.Repository, raw string) identity.APIKeyIdentity {
	t.Helper()
	h := hash(raw)
	snapshot(t, repo, 1, 1000, h)
	key, _, err := repo.FindActiveAPIKeyBySource(ctx, "runtime-1", h)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func bind(t *testing.T, repo ports.Repository, keyID identity.APIKeyID, policyID resourcepolicy.PolicyID) resourcepolicy.PolicyBinding {
	t.Helper()
	b := resourcepolicy.PolicyBinding{APIKeyID: keyID, PolicyID: policyID, Revision: 1, Enabled: true, CreatedAtMS: 1000, UpdatedAtMS: 1000}
	if err := repo.BindAPIKey(ctx, b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBindingCardinalityCASLifecycleAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.sqlite")
	db, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	repo, identities := adapter.New(db), identityadapter.New(db)
	p1, p2 := mustPolicy(t, repo), mustPolicy(t, repo)
	hashes := []string{hash("a"), hash("b"), hash("c"), hash("unbound")}
	snapshot(t, identities, 1, 1000, hashes...)
	keys := make([]identity.APIKeyIdentity, 0, len(hashes))
	for _, h := range hashes {
		key, _, err := identities.FindActiveAPIKeyBySource(ctx, "runtime-1", h)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, key)
		if len(keys) <= 3 {
			bind(t, repo, key.ID, p1.ID)
		}
	}
	missingID, err := identity.NewAPIKeyID()
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.BindAPIKey(ctx, resourcepolicy.PolicyBinding{APIKeyID: missingID, PolicyID: p1.ID, Revision: 1, Enabled: true, CreatedAtMS: 1000, UpdatedAtMS: 1000}); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("unknown Canonical APIKeyID accepted: %v", err)
	}
	if err := repo.BindAPIKey(ctx, resourcepolicy.PolicyBinding{APIKeyID: keys[0].ID, PolicyID: p2.ID, Revision: 1, Enabled: true, CreatedAtMS: 1000, UpdatedAtMS: 1000}); err == nil {
		t.Fatal("second binding slot accepted")
	}
	// G1 missing is recoverable and remains a valid binding subject.
	missing, err := identities.SetAPIKeyLifecycle(ctx, keys[1].ID, keys[1].Revision, identity.LifecycleMissing, 1100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RebindAPIKey(ctx, missing.ID, 1, p2.ID, 1200); err != nil {
		t.Fatalf("missing key rebind: %v", err)
	}
	// Existing binding survives G1 supersession; new/rebound bindings cannot target it.
	superseded, err := identities.SetAPIKeyLifecycle(ctx, keys[2].ID, keys[2].Revision, identity.LifecycleSuperseded, 1100)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RebindAPIKey(ctx, superseded.ID, 1, p2.ID, 1200); !errors.Is(err, ports.ErrSupersededAPIKey) {
		t.Fatalf("superseded key rebound: %v", err)
	}
	if got, err := repo.LoadBinding(ctx, superseded.ID); err != nil || got.PolicyID != p1.ID {
		t.Fatalf("superseded binding lost: %+v %v", got, err)
	}
	// Also reject a first binding to an already superseded Canonical ID.
	if _, err := identities.SetAPIKeyLifecycle(ctx, keys[3].ID, keys[3].Revision, identity.LifecycleSuperseded, 1100); err != nil {
		t.Fatal(err)
	}
	if err := repo.BindAPIKey(ctx, resourcepolicy.PolicyBinding{APIKeyID: keys[3].ID, PolicyID: p2.ID, Revision: 1, Enabled: true, CreatedAtMS: 1200, UpdatedAtMS: 1200}); !errors.Is(err, ports.ErrSupersededAPIKey) {
		t.Fatalf("superseded bind: %v", err)
	}
	// A failed target FK must leave the same row and revision intact.
	before, err := repo.LoadBinding(ctx, missing.ID)
	if err != nil {
		t.Fatal(err)
	}
	invalidPolicyID := resourcepolicy.PolicyID(strings.Repeat("f", 32))
	if _, err := repo.RebindAPIKey(ctx, missing.ID, before.Revision, invalidPolicyID, 2000); err == nil {
		t.Fatal("missing target policy accepted")
	}
	after, err := repo.LoadBinding(ctx, missing.ID)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("failed rebind changed binding: %+v -> %+v, %v", before, after, err)
	}
	if _, err := repo.RebindAPIKey(ctx, missing.ID, 1, p1.ID, 2000); !errors.Is(err, ports.ErrRevisionConflict) {
		t.Fatalf("stale binding rebind: %v", err)
	}
	disabled, err := repo.SetBindingEnabled(ctx, missing.ID, before.Revision, false, 900)
	if err != nil || disabled.Enabled || disabled.Revision != before.Revision+1 || disabled.UpdatedAtMS != before.UpdatedAtMS+1 || disabled.PolicyID != p2.ID {
		t.Fatalf("disable: %+v %v", disabled, err)
	}
	if _, err := repo.SetBindingEnabled(ctx, missing.ID, before.Revision, false, 3000); !errors.Is(err, ports.ErrRevisionConflict) {
		t.Fatalf("stale binding no-op: %v", err)
	}
	enabled, err := repo.SetBindingEnabled(ctx, missing.ID, disabled.Revision, true, 900)
	if err != nil || !enabled.Enabled || enabled.Revision != disabled.Revision+1 || enabled.PolicyID != p2.ID {
		t.Fatalf("re-enable: %+v %v", enabled, err)
	}
	var count int
	if err := db.QueryRow(`select count(*) from gateway_api_key_policy_bindings`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("binding slots=%d %v", count, err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	reopened := adapter.New(db)
	if got, err := reopened.LoadPolicy(ctx, p1.ID); err != nil || len(got.Rules) != 3 {
		t.Fatalf("restarted policy: %+v %v", got, err)
	}
	if got, err := reopened.LoadBinding(ctx, missing.ID); err != nil || !reflect.DeepEqual(got, enabled) {
		t.Fatalf("restarted binding: %+v %v", got, err)
	}
}

func TestExplicitRotationRetainsBindingAndPassiveReplacementDoesNotInherit(t *testing.T) {
	_, repo, identities := open(t)
	p := mustPolicy(t, repo)
	oldHash, newHash := hash("old-secret"), hash("new-secret")
	old := seedKey(t, identities, "old-secret")
	want := bind(t, repo, old.ID, p.ID)
	mutations := identities.(identityports.MutationRepository)
	intent, err := mutations.PrepareAPIKeyMutation(ctx, identityports.PrepareAPIKeyMutationParams{
		Kind: identityports.APIKeyMutationRotate, RuntimeIdentity: "runtime-1",
		ObservedRuntimeGeneration: 1, Evidence: identityports.APIKeyMutationEvidence{
			OldHash: oldHash, NewHash: newHash, ExactOldCount: 1, NormalizedOldCount: 1,
		}, OwnerInstance: "process-A", NowMS: 2000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mutations.MarkAPIKeyMutationForwardComplete(ctx, intent, "process-A", 2001); err != nil {
		t.Fatal(err)
	}
	outcome, err := mutations.ResolveAPIKeyMutation(ctx, identityports.ResolveAPIKeyMutationParams{
		IntentID: intent, RuntimeIdentity: "runtime-1", ObservedRuntimeGeneration: 2,
		ObservedHashes: []string{newHash}, NowMS: 2002,
	})
	if err != nil || outcome != identityports.APIKeyMutationSuccess {
		t.Fatalf("rotation: %s %v", outcome, err)
	}
	rotated, _, err := identities.FindActiveAPIKeyBySource(ctx, "runtime-1", newHash)
	if err != nil || rotated.ID != old.ID {
		t.Fatalf("canonical ID changed on rotation: %+v %v", rotated, err)
	}
	got, err := repo.LoadBinding(ctx, old.ID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("rotation mutated policy binding: %+v -> %+v, %v", want, got, err)
	}
	// Passive source replacement creates B; it must never inherit A's binding.
	passiveHash := hash("passively-replaced")
	snapshot(t, identities, 3, 3000, passiveHash)
	replacement, _, err := identities.FindActiveAPIKeyBySource(ctx, "runtime-1", passiveHash)
	if err != nil || replacement.ID == old.ID {
		t.Fatalf("passive replacement reused Canonical ID: %+v %v", replacement, err)
	}
	if _, err := repo.LoadBinding(ctx, replacement.ID); !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("passive replacement inherited policy: %v", err)
	}
	got, err = repo.LoadBinding(ctx, old.ID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("old binding changed on passive replacement: %+v %v", got, err)
	}
}

func TestMissingKeyCanBeFirstBoundAndRevisionCannotOverflow(t *testing.T) {
	db, repo, identities := open(t)
	p := mustPolicy(t, repo)
	key := seedKey(t, identities, "temporarily-absent")
	missing, err := identities.SetAPIKeyLifecycle(ctx, key.ID, key.Revision, identity.LifecycleMissing, 1200)
	if err != nil {
		t.Fatal(err)
	}
	want := bind(t, repo, missing.ID, p.ID)
	if got, err := repo.LoadBinding(ctx, missing.ID); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("missing bind: %+v %v", got, err)
	}
	if _, err := db.Exec(`update gateway_quota_policies set revision = ? where id = ?`, int64(math.MaxInt64), p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetPolicyState(ctx, p.ID, resourcepolicy.Revision(math.MaxInt64), resourcepolicy.StateDisabled, 2000); !errors.Is(err, ports.ErrRevisionOverflow) {
		t.Fatalf("policy revision overflow: %v", err)
	}
	if _, err := db.Exec(`update gateway_api_key_policy_bindings set revision = ? where api_key_id = ?`, int64(math.MaxInt64), key.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetBindingEnabled(ctx, key.ID, resourcepolicy.Revision(math.MaxInt64), false, 2000); !errors.Is(err, ports.ErrRevisionOverflow) {
		t.Fatalf("binding revision overflow: %v", err)
	}
}

func TestBindingSQLiteFKAndChecks(t *testing.T) {
	db, repo, identities := open(t)
	p := mustPolicy(t, repo)
	key := seedKey(t, identities, "fk-test")
	for _, row := range []struct {
		keyID, policyID   string
		revision, enabled int64
	}{
		{strings.Repeat("e", 32), p.ID.String(), 1, 1},   // unknown Canonical APIKeyID
		{key.ID.String(), strings.Repeat("f", 32), 1, 1}, // unknown PolicyID
		{key.ID.String(), p.ID.String(), 0, 1},
		{key.ID.String(), p.ID.String(), 1, 2},
		{"bad-id", p.ID.String(), 1, 1},
	} {
		_, err := db.Exec(`insert into gateway_api_key_policy_bindings
			(api_key_id, policy_id, revision, enabled, created_at_ms, updated_at_ms)
			values (?, ?, ?, ?, 1, 1)`, row.keyID, row.policyID, row.revision, row.enabled)
		if err == nil {
			t.Fatalf("SQLite accepted invalid binding %+v", row)
		}
	}
	bind(t, repo, key.ID, p.ID)
	if _, err := db.Exec(`update gateway_api_key_policy_bindings set updated_at_ms = 0 where api_key_id = ?`, key.ID); err == nil {
		t.Fatal("SQLite accepted non-monotonic binding timestamp")
	}
}

func TestBindingSchemaHasNoSourceOrRuntimeAuthority(t *testing.T) {
	db, _, _ := open(t)
	for _, table := range []string{"gateway_quota_policies", "gateway_quota_policy_rules", "gateway_api_key_policy_bindings"} {
		rows, err := db.Query(`pragma table_info(` + table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, fieldType string
			var defaultValue sql.NullString
			if err := rows.Scan(&cid, &name, &fieldType, &notNull, &defaultValue, &primaryKey); err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"hash", "alias", "source", "runtime", "credential", "provider", "endpoint", "model", "tenant", "account", "raw_key"} {
				if strings.Contains(name, forbidden) {
					t.Fatalf("%s contains forbidden column %q", table, name)
				}
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
	}
	var count int
	if err := db.QueryRow(`select count(*) from gateway_quota_policy_rules`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration backfilled rules: %d %v", count, err)
	}
	if err := db.QueryRow(`select count(*) from gateway_api_key_policy_bindings`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("migration backfilled bindings: %d %v", count, err)
	}
}
