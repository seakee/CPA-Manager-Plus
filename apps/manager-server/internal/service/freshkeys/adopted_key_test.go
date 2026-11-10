package freshkeys

import (
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
