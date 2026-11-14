package freshkeys

import (
	"context"
	"errors"
	"strings"
	"testing"

	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
)

// An adopted destination's client keys are stored as canonical records of their
// own origin: the Key workspace lists them with the masked rendering their shape
// allows, and the secret never leaves the protected record.
func TestRegisterAdoptedStoresMaskedAdoptedRecords(t *testing.T) {
	_, service, _, _ := installKeyStore(t)
	ctx := t.Context()
	secrets := []string{"sk-adopt-source-key-01", "short"}
	keys, err := service.RegisterAdopted(ctx, secrets)
	if err != nil || len(keys) != 2 {
		t.Fatalf("register: %#v %v", keys, err)
	}
	want := []string{"…y-01", "…"}
	for index, key := range keys {
		if key.Origin != repo.OriginAdopted || key.Lifecycle != "active" || key.MaskedPreview != want[index] {
			t.Fatalf("key %d = %#v, want preview %q", index, key, want[index])
		}
	}
	// The stored rows decrypt back to exactly the destination's secrets, which is
	// what a revoke or replace step consumes.
	for index, key := range keys {
		if err := service.WithStagedSecret(ctx, string(key.ID), func(secret string) error {
			if secret != secrets[index] {
				t.Fatalf("staged secret %d = %q", index, secret)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	listed, err := service.List(ctx)
	if err != nil || len(listed) != 2 {
		t.Fatalf("list = %#v %v", listed, err)
	}
	// The workspace projection still works with a non-canonical adopted record in
	// the same table: the staged key proof reads every row.
	if matched, err := service.MatchStagedSecret(ctx, "cpamp_sk_"+strings.Repeat("d", 64)); err != nil || matched {
		t.Fatalf("staged key proof over an adopted record: %v %v", matched, err)
	}
}

// Registration is idempotent per secret: a replayed completion returns the
// existing record, and a secret that is already this Manager's key keeps that one
// identity instead of registering a second one.
func TestRegisterAdoptedReplaysInsteadOfDuplicating(t *testing.T) {
	_, service, _, _ := installKeyStore(t)
	ctx := t.Context()
	workspace, _, err := service.Create(ctx, strings.Repeat("c", 32))
	if err != nil {
		t.Fatal(err)
	}
	keys, err := service.RegisterAdopted(ctx, []string{"source-client-one"})
	if err != nil || len(keys) != 1 {
		t.Fatalf("register: %#v %v", keys, err)
	}
	replayed, err := service.RegisterAdopted(ctx, []string{"source-client-one"})
	if err != nil || len(replayed) != 1 || replayed[0].ID != keys[0].ID {
		t.Fatalf("replay = %#v %v", replayed, err)
	}
	// A destination that carries a key this workspace already minted keeps that
	// canonical record: one secret has exactly one identity.
	existing, err := service.RegisterAdopted(ctx, []string{workspace.CreatedSecret})
	if err != nil || len(existing) != 1 || existing[0].ID != workspace.Key.ID || existing[0].Origin != repo.OriginWorkspace {
		t.Fatalf("workspace secret registered twice: %#v %v", existing, err)
	}
	if listed, err := service.List(ctx); err != nil || len(listed) != 2 {
		t.Fatalf("list = %#v %v", listed, err)
	}
}

// A digest that resolves to a superseded record is refused: that identity was
// retired by a committed key operation, so it can neither become the
// installation's key nor be revived by an adoption.
func TestRegisterAdoptedRefusesASupersededRecord(t *testing.T) {
	db, service, _, _ := installKeyStore(t)
	ctx := t.Context()
	workspace, _, err := service.Create(ctx, strings.Repeat("e", 32))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`update gateway_api_key_identities set lifecycle='superseded', revision=revision+1 where id=?`, string(workspace.Key.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err = service.RegisterAdopted(ctx, []string{workspace.CreatedSecret}); !errors.Is(err, ErrAdoptedSecretRetired) {
		t.Fatalf("superseded registration = %v", err)
	}
	listed, err := service.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].Lifecycle != "superseded" || listed[0].Origin != repo.OriginWorkspace {
		t.Fatalf("a retired record was rewritten: %#v %v", listed, err)
	}
}

func TestRegisterAdoptedDistinguishesPassiveMissingFromRetiredBinding(t *testing.T) {
	db, _, protector, _ := installKeyStore(t)
	service := New(repo.New(db), protector, WithSourceRuntime(func(context.Context) (string, error) {
		return "embedded-default", nil
	}))
	ctx := t.Context()

	missingID := strings.Repeat("a", 32)
	missingSecret := "passive-missing-source-key"
	if _, err := db.Exec(`insert into gateway_api_key_identities(id,revision,lifecycle,created_at_ms,updated_at_ms) values(?,1,'missing',1000,2000)`, missingID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into gateway_api_key_source_bindings(api_key_id,runtime_identity,api_key_hash,first_seen_at_ms,last_seen_at_ms,retired_at_ms) values(?,'legacy-runtime',?,1000,2000,null)`,
		missingID, SecretDigest(missingSecret)); err != nil {
		t.Fatal(err)
	}
	registered, err := service.RegisterAdopted(ctx, []string{missingSecret})
	if err != nil || len(registered) != 1 || string(registered[0].ID) != missingID || registered[0].Lifecycle != "active" {
		t.Fatalf("RegisterAdopted(passive missing) = %#v %v", registered, err)
	}
	if allStaged, err := service.AllStaged(ctx, []string{missingSecret}); err != nil || !allStaged {
		t.Fatalf("AllStaged after passive missing recovery = %v (%v), want true", allStaged, err)
	}

	retiredID := strings.Repeat("b", 32)
	retiredSecret := "explicit-retired-binding-secret"
	activeRotatedSecret := "explicit-active-rotated-secret"
	if _, err := db.Exec(`insert into gateway_api_key_identities(id,revision,lifecycle,created_at_ms,updated_at_ms) values(?,2,'active',1000,2000)`, retiredID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into gateway_api_key_source_bindings(api_key_id,runtime_identity,api_key_hash,first_seen_at_ms,last_seen_at_ms,retired_at_ms) values(?,'legacy-runtime',?,1000,1500,2000)`,
		retiredID, SecretDigest(retiredSecret)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`insert into gateway_api_key_source_bindings(api_key_id,runtime_identity,api_key_hash,first_seen_at_ms,last_seen_at_ms,retired_at_ms) values(?,'legacy-runtime',?,2000,2000,null)`,
		retiredID, SecretDigest(activeRotatedSecret)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RegisterAdopted(ctx, []string{retiredSecret}); !errors.Is(err, ErrAdoptedSecretRetired) {
		t.Fatalf("RegisterAdopted(explicit retired binding) = %v, want ErrAdoptedSecretRetired", err)
	}
}

// A registration outside the adoption bound is refused before anything is
// written, and a duplicate secret in one call is refused rather than silently
// collapsed.
func TestRegisterAdoptedRefusesMalformedInput(t *testing.T) {
	_, service, _, _ := installKeyStore(t)
	ctx := t.Context()
	for name, secrets := range map[string][]string{
		"empty":           {},
		"empty secret":    {""},
		"duplicate":       {"source-client-one", "source-client-one"},
		"invalid utf8":    {string([]byte{0xff, 0xfe, 0xfd})},
		"overlong secret": {"source-client-one", strings.Repeat("k", maxAdoptedSecretLength+1)},
	} {
		if _, err := service.RegisterAdopted(ctx, secrets); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if listed, err := service.List(ctx); err != nil || len(listed) != 0 {
		t.Fatalf("a refused registration wrote a record: %#v %v", listed, err)
	}
}

// RegisterAdopted is all-or-nothing across the whole slice: a retired key or
// ambiguous active Canonical owners later in the batch leave no earlier writes.
// A single pre-existing Canonical owner is reusable, including across migration.
func TestRegisterAdoptedRollsBackBatchWhenNthKeyFails(t *testing.T) {
	t.Run("retired key after cross-runtime handoff and valid keys", func(t *testing.T) {
		db, _, protector, _ := installKeyStore(t)
		service := New(repo.New(db), protector, WithSourceRuntime(func(context.Context) (string, error) {
			return "embedded-default", nil
		}))
		ctx := t.Context()

		legacyID := strings.Repeat("9", 32)
		legacySecret := "source-batch-legacy-first"
		if _, err := db.Exec(`insert into gateway_api_key_identities(id,revision,lifecycle,created_at_ms,updated_at_ms) values(?,1,'active',1000,1000)`, legacyID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into gateway_api_key_source_bindings(api_key_id,runtime_identity,api_key_hash,first_seen_at_ms,last_seen_at_ms,retired_at_ms) values(?,'legacy-runtime',?,1000,1000,null)`,
			legacyID, SecretDigest(legacySecret)); err != nil {
			t.Fatal(err)
		}

		retired, _, err := service.Create(ctx, strings.Repeat("a", 32))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`update gateway_api_key_identities set lifecycle='superseded', revision=revision+1 where id=?`, string(retired.Key.ID)); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`update gateway_api_key_source_bindings set retired_at_ms=1 where api_key_id=?`, string(retired.Key.ID)); err != nil {
			t.Fatal(err)
		}
		_, err = service.RegisterAdopted(ctx, []string{legacySecret, "source-batch-second", retired.CreatedSecret})
		if !errors.Is(err, ErrAdoptedSecretRetired) {
			t.Fatalf("retired third key = %v, want ErrAdoptedSecretRetired", err)
		}
		listed, err := service.List(ctx)
		if err != nil || len(listed) != 1 || listed[0].ID != retired.Key.ID {
			t.Fatalf("batch refusal left workspace keys: %#v %v", listed, err)
		}
		var activeLegacy, embeddedActive int
		if err = db.QueryRow(`select count(*) from gateway_api_key_source_bindings where api_key_id=? and runtime_identity='legacy-runtime' and retired_at_ms is null`, legacyID).Scan(&activeLegacy); err != nil || activeLegacy != 1 {
			t.Fatalf("batch refusal retired legacy binding: count=%d err=%v", activeLegacy, err)
		}
		if err = db.QueryRow(`select count(*) from gateway_api_key_source_bindings where runtime_identity='embedded-default' and retired_at_ms is null`).Scan(&embeddedActive); err != nil || embeddedActive != 0 {
			t.Fatalf("batch refusal left %d active embedded-default bindings: %v", embeddedActive, err)
		}
	})

	t.Run("ambiguous source binding after valid keys with and without runtime source", func(t *testing.T) {
		db, unboundService, protector, _ := installKeyStore(t)
		boundService := New(repo.New(db), protector, WithSourceRuntime(func(context.Context) (string, error) {
			return "embedded-default", nil
		}))
		ctx := t.Context()
		foreignID := strings.Repeat("b", 32)
		if _, err := db.Exec(`insert into gateway_api_key_identities(id,revision,lifecycle,created_at_ms,updated_at_ms) values(?,1,'active',1,1)`, foreignID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into gateway_api_key_source_bindings(api_key_id,runtime_identity,api_key_hash,first_seen_at_ms,last_seen_at_ms) values(?,'embedded-default',?,1,1)`,
			foreignID, SecretDigest("source-owned-third")); err != nil {
			t.Fatal(err)
		}
		otherID := strings.Repeat("c", 32)
		if _, err := db.Exec(`insert into gateway_api_key_identities(id,revision,lifecycle,created_at_ms,updated_at_ms) values(?,1,'active',1,1)`, otherID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`insert into gateway_api_key_source_bindings(api_key_id,runtime_identity,api_key_hash,first_seen_at_ms,last_seen_at_ms) values(?,'other-runtime',?,1,1)`, otherID, SecretDigest("source-owned-third")); err != nil {
			t.Fatal(err)
		}
		for label, svc := range map[string]*Service{"unbound_runtime": unboundService, "bound_runtime": boundService} {
			if _, err := svc.RegisterAdopted(ctx, []string{"source-clean-first", "source-clean-second", "source-owned-third"}); !errors.Is(err, repo.ErrSourceOwned) {
				t.Fatalf("%s source-owned third key = %v, want ErrSourceOwned", label, err)
			}
		}
		listed, err := boundService.List(ctx)
		if err != nil || len(listed) != 0 {
			t.Fatalf("source-binding refusal left workspace keys: %#v %v", listed, err)
		}
		var activeBindings int
		if err = db.QueryRow(`select count(*) from gateway_api_key_source_bindings where retired_at_ms is null`).Scan(&activeBindings); err != nil || activeBindings != 2 {
			t.Fatalf("source-binding refusal changed bindings to %d: %v", activeBindings, err)
		}
	})
}

// Reusing a secret already staged in the Key workspace binds an unbound source,
// replays matching runtime bindings idempotently, hands off prior-runtime
// attribution windows atomically for the same Canonical Key, and fails closed
// as ErrSourceOwned when a different active identity or hash conflicts.
func TestRegisterAdoptedBindsOrValidatesExistingWorkspaceKeySourceInTx(t *testing.T) {
	t.Run("existing workspace key without binding acquires current runtime binding", func(t *testing.T) {
		db, preCommitSvc, protector, _ := installKeyStore(t)
		ctx := t.Context()
		staged, _, err := preCommitSvc.Create(ctx, strings.Repeat("1", 32))
		if err != nil {
			t.Fatal(err)
		}
		service := New(repo.New(db), protector, WithSourceRuntime(func(context.Context) (string, error) {
			return "embedded-default", nil
		}))
		registered, err := service.RegisterAdopted(ctx, []string{staged.CreatedSecret, "adopted-new-peer"})
		if err != nil || len(registered) != 2 {
			t.Fatalf("RegisterAdopted = %#v %v", registered, err)
		}
		if registered[0].ID != staged.Key.ID || registered[0].Origin != "workspace" || registered[1].Origin != "adopted" {
			t.Fatalf("registered origins/IDs = %#v", registered)
		}
		var boundCount int
		if err = db.QueryRow(`select count(*) from gateway_api_key_source_bindings where retired_at_ms is null and runtime_identity='embedded-default'`).Scan(&boundCount); err != nil || boundCount != 2 {
			t.Fatalf("active bindings for embedded-default = %d (%v), want 2", boundCount, err)
		}
		// Replaying with the same runtime identity stays idempotent.
		if _, err = service.RegisterAdopted(ctx, []string{staged.CreatedSecret, "adopted-new-peer"}); err != nil {
			t.Fatalf("replay RegisterAdopted: %v", err)
		}
		if err = db.QueryRow(`select count(*) from gateway_api_key_source_bindings where retired_at_ms is null and runtime_identity='embedded-default'`).Scan(&boundCount); err != nil || boundCount != 2 {
			t.Fatalf("active bindings after replay = %d (%v), want 2", boundCount, err)
		}
	})

	t.Run("existing workspace key bound to prior runtime hands off attribution window atomically", func(t *testing.T) {
		db, _, protector, _ := installKeyStore(t)
		ctx := t.Context()
		priorRuntimeSvc := New(repo.New(db), protector, WithSourceRuntime(func(context.Context) (string, error) {
			return "external-other", nil
		}))
		staged, _, err := priorRuntimeSvc.Create(ctx, strings.Repeat("2", 32))
		if err != nil {
			t.Fatal(err)
		}
		adoptSvc := New(repo.New(db), protector, WithSourceRuntime(func(context.Context) (string, error) {
			return "embedded-default", nil
		}))
		registered, err := adoptSvc.RegisterAdopted(ctx, []string{"adopted-first-peer", staged.CreatedSecret})
		if err != nil || len(registered) != 2 {
			t.Fatalf("cross-runtime handoff = %#v (%v)", registered, err)
		}
		if registered[1].ID != staged.Key.ID || registered[1].Origin != repo.OriginWorkspace {
			t.Fatalf("handed-off key = %#v, want ID=%s origin=%s", registered[1], staged.Key.ID, repo.OriginWorkspace)
		}
		var retiredPrior, activeTarget int
		if err = db.QueryRow(`select count(*) from gateway_api_key_source_bindings where api_key_id=? and runtime_identity='external-other' and retired_at_ms is not null`, string(staged.Key.ID)).Scan(&retiredPrior); err != nil || retiredPrior != 1 {
			t.Fatalf("prior runtime retired bindings = %d (%v), want 1", retiredPrior, err)
		}
		if err = db.QueryRow(`select count(*) from gateway_api_key_source_bindings where api_key_id=? and runtime_identity='embedded-default' and retired_at_ms is null`, string(staged.Key.ID)).Scan(&activeTarget); err != nil || activeTarget != 1 {
			t.Fatalf("target runtime active bindings = %d (%v), want 1", activeTarget, err)
		}
	})

	t.Run("existing workspace key with foreign owner or conflicting active hash refuses and rolls back batch", func(t *testing.T) {
		db, unboundSvc, protector, _ := installKeyStore(t)
		ctx := t.Context()
		staged, _, err := unboundSvc.Create(ctx, strings.Repeat("3", 32))
		if err != nil {
			t.Fatal(err)
		}
		foreignID := strings.Repeat("4", 32)
		if _, err = db.Exec(`insert into gateway_api_key_identities(id,revision,lifecycle,created_at_ms,updated_at_ms) values(?,1,'active',1000,1000)`, foreignID); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`insert into gateway_api_key_source_bindings(api_key_id,runtime_identity,api_key_hash,first_seen_at_ms,last_seen_at_ms,retired_at_ms) values(?,'external-other',?,1000,1000,null)`,
			foreignID, SecretDigest(staged.CreatedSecret)); err != nil {
			t.Fatal(err)
		}
		adoptSvc := New(repo.New(db), protector, WithSourceRuntime(func(context.Context) (string, error) {
			return "embedded-default", nil
		}))
		for label, svc := range map[string]*Service{"unbound_runtime": unboundSvc, "bound_runtime": adoptSvc} {
			if _, err = svc.RegisterAdopted(ctx, []string{"adopted-first-before-conflict", staged.CreatedSecret}); !errors.Is(err, repo.ErrSourceOwned) {
				t.Fatalf("%s foreign-owner existing key = %v, want ErrSourceOwned", label, err)
			}
		}

		// Remove foreign owner and instead bind staged.Key.ID on embedded-default to a DIFFERENT active hash.
		if _, err = db.Exec(`delete from gateway_api_key_source_bindings where api_key_id=?`, foreignID); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`insert into gateway_api_key_source_bindings(api_key_id,runtime_identity,api_key_hash,first_seen_at_ms,last_seen_at_ms,retired_at_ms) values(?,'embedded-default',?,1000,1000,null)`,
			string(staged.Key.ID), SecretDigest("different-active-secret-on-target")); err != nil {
			t.Fatal(err)
		}
		if _, err = adoptSvc.RegisterAdopted(ctx, []string{"adopted-first-before-conflict", staged.CreatedSecret}); !errors.Is(err, repo.ErrSourceOwned) {
			t.Fatalf("same-ID different-hash existing key = %v, want ErrSourceOwned", err)
		}
		listed, err := adoptSvc.List(ctx)
		if err != nil || len(listed) != 1 || listed[0].ID != staged.Key.ID {
			t.Fatalf("conflict leaked peer key: %#v %v", listed, err)
		}
	})
}
