package freshkeyapply

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/model"
	repo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/freshkeys"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/security"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/service/freshkeys"
)

// adoptedSourceKeys are the shape an adopted destination carries: the source
// CPA's own client keys, which are none of this Manager's canonical shapes.
var adoptedSourceKeys = []string{"source-client-one", "source-client-two"}

// adoptedFixture is the install-derived authority of a committed adoption: the
// destination CPA is the Supervisor's own, carrying the two client keys the
// adoption staged, and the checkpoint strategy is the one that selects the
// adopted posture.
func adoptedFixture(t *testing.T, clientKeys ...string) (*Service, *managedCPA, *ModelSource) {
	t.Helper()
	db, err := sqlite.Open(filepath.Join(t.TempDir(), "manager.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err = sqlite.Migrate(db); err != nil {
		t.Fatal(err)
	}
	r := repo.New(db)
	protector, _ := security.NewProtector([]byte("isolated-adopted-test-key"))
	keys := freshkeys.New(r, protector)
	cpa := &managedCPA{management: strings.Repeat("e", 64), keys: slices.Clone(clientKeys)}
	server := cpa.serve(t)
	rt := &observedRuntime{status: model.RuntimeObservedStatus{Identity: managedRuntimeIdentity, Generation: 9,
		ProtocolVersion:       "v1",
		State:                 model.RuntimeStateReady,
		CPAObservedVersion:    "v8.0.55",
		ActiveGatewayArtifact: &model.ActiveGatewayArtifact{Engine: "cpa", ArtifactID: model.RuntimeArtifactID(managedArtifact()), Version: "v8.0.55"}}}
	source := &ModelSource{install: ManagedInstall{
		RunID: "managed:operation-adoption", Generation: "9", Identity: managedRuntimeIdentity,
		ArtifactID: managedArtifact(), BaseURL: server.URL, ManagementKey: cpa.management,
		Strategy: "adopt_existing",
	}}
	return NewManaged(r, keys, rt, server.URL, source.get), cpa, source
}

// An adopted destination's client keys become the installation's own keys and the
// install journal is claimed for this run. Both writes are the ones the Key
// workspace and every install-derived surface read, and both replay.
func TestRegisterAdoptionRecordsTheDestinationKeysAndTheInstallJournal(t *testing.T) {
	s, _, _ := adoptedFixture(t, adoptedSourceKeys...)
	ctx := t.Context()

	keys, err := s.RegisterAdoption(ctx, adoptedSourceKeys)
	if err != nil || len(keys) != 2 {
		t.Fatalf("register adoption: %#v %v", keys, err)
	}
	for index, key := range keys {
		if key.Origin != repo.OriginAdopted || key.Lifecycle != "active" || key.NameRevision != 1 {
			t.Fatalf("key %d is not an adopted record: %#v", index, key)
		}
		if want := model.MaskedImportedClientKey(adoptedSourceKeys[index]); key.MaskedPreview != want {
			t.Fatalf("key %d preview = %q, want %q", index, key.MaskedPreview, want)
		}
	}
	journal, err := s.repo.InitialInstall(ctx)
	if err != nil || journal == nil {
		t.Fatalf("install journal: %#v %v", journal, err)
	}
	if journal.KeyID != string(keys[0].ID) || journal.RunID != "managed:operation-adoption" || journal.Generation != "9" {
		t.Fatalf("journal is not bound to this adoption: %#v", journal)
	}

	// A retried or resumed completion replays both writes: the same identities,
	// the same journal, and never a second record for one secret.
	replayed, err := s.RegisterAdoption(ctx, adoptedSourceKeys)
	if err != nil || len(replayed) != 2 {
		t.Fatalf("replayed adoption: %#v %v", replayed, err)
	}
	for index := range keys {
		if replayed[index].ID != keys[index].ID {
			t.Fatalf("replay minted a second identity for %q", adoptedSourceKeys[index])
		}
	}
	listed, err := s.keys.List(ctx)
	if err != nil || len(listed) != 2 {
		t.Fatalf("workspace list = %#v %v", listed, err)
	}
	again, err := s.repo.InitialInstall(ctx)
	if err != nil || again == nil || *again != *journal {
		t.Fatalf("replay rewrote the journal: %#v %v", again, err)
	}
}

// The install journal is what the gateway configuration page and the key
// operation journal require. Before completion's registration it does not exist,
// so the adopted installation has no configuration authority; afterwards it has
// exactly the one a New Managed install derives.
func TestAdoptionRegistrationGrantsTheConfigAuthority(t *testing.T) {
	s, cpa, _ := adoptedFixture(t, adoptedSourceKeys...)
	ctx := t.Context()
	if _, err := s.ConfigTarget(ctx, 9); err == nil {
		t.Fatal("an unresolved adoption already held configuration authority")
	}
	if _, err := s.RegisterAdoption(ctx, adoptedSourceKeys); err != nil {
		t.Fatal(err)
	}
	managementKey, err := s.ConfigTarget(ctx, 9)
	if err != nil || managementKey != cpa.management {
		t.Fatalf("config authority = %q %v", managementKey, err)
	}
	if !s.DefaultInstallCPAAuthorityProven(ctx) {
		t.Fatal("the adopted destination is not admitted after registration")
	}
	// A generation other than the staged one is still refused: registration
	// carries the binding forward, it does not replace it.
	if _, err := s.ConfigTarget(ctx, 10); !errors.Is(err, ErrGeneration) {
		t.Fatalf("foreign generation = %v", err)
	}
}

// Revoking an adopted key runs through the same journal as any other key
// operation: the destination keeps exactly the keeper, the revoked secret stops
// authenticating, and the replacement of the last key is then allowed.
func TestAdoptedKeysRevokeAndReplaceThroughTheInstallationJournal(t *testing.T) {
	s, cpa, _ := adoptedFixture(t, adoptedSourceKeys...)
	ctx := t.Context()
	keys, err := s.RegisterAdoption(ctx, adoptedSourceKeys)
	if err != nil {
		t.Fatal(err)
	}
	// Revoke the primary key and keep the second one.
	id := strings.Repeat("3", 32)
	if _, err = s.Prepare(ctx, id, string(keys[0].ID), string(keys[1].ID), "revoke", 9); err != nil {
		t.Fatalf("adopted revoke prepare: %v", err)
	}
	result, err := s.ContinueOperation(ctx, id, 9)
	if err != nil || result.Operation == nil || result.Operation.Outcome != "committed" ||
		result.RuntimeState != "effective_observed" {
		t.Fatalf("adopted revoke: %#v %v", result, err)
	}
	if got := cpa.snapshot(); len(got) != 1 || got[0] != adoptedSourceKeys[1] {
		t.Fatalf("revoke left %v", got)
	}
	// The surviving adopted key is now the installation's key, so it can be
	// replaced by a key this Manager mints.
	replacement, _, err := s.keys.Create(ctx, strings.Repeat("a", 32))
	if err != nil {
		t.Fatal(err)
	}
	replaceID := strings.Repeat("4", 32)
	if _, err = s.Prepare(ctx, replaceID, string(keys[1].ID), string(replacement.Key.ID), "replace", 9); err != nil {
		t.Fatalf("adopted replace prepare: %v", err)
	}
	if result, err = s.ContinueOperation(ctx, replaceID, 9); err != nil || result.NextStep != "revoke" ||
		!slices.Equal(cpa.snapshot(), []string{adoptedSourceKeys[1], replacement.CreatedSecret}) {
		t.Fatalf("adopted replace add step: %#v %v keys=%v", result, err, cpa.snapshot())
	}
	if result, err = s.ContinueOperation(ctx, replaceID, 9); err != nil || result.Operation == nil ||
		result.Operation.Outcome != "committed" || !slices.Equal(cpa.snapshot(), []string{replacement.CreatedSecret}) {
		t.Fatalf("adopted replace revoke step: %#v %v keys=%v", result, err, cpa.snapshot())
	}
}

// The journal is written once. An adoption that finds another installation's
// journal is refused instead of inheriting the key operations bound to it.
func TestAdoptionRegistrationRefusesAnotherInstallationsJournal(t *testing.T) {
	s, _, source := adoptedFixture(t, adoptedSourceKeys...)
	ctx := t.Context()
	owner, _, err := s.keys.Create(ctx, strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.repo.ClaimInitialInstall(ctx, repo.InitialInstall{KeyID: string(owner.Key.ID), RunID: "managed:another-install", Generation: "9", AttemptedAtMS: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RegisterAdoption(ctx, adoptedSourceKeys); !errors.Is(err, ErrGeneration) {
		t.Fatalf("foreign journal = %v", err)
	}
	source.set(func(install *ManagedInstall) { install.RunID = "managed:operation-adoption" })
	if _, err = s.RegisterAdoption(ctx, adoptedSourceKeys); !errors.Is(err, ErrGeneration) {
		t.Fatalf("journal rebound to a second run: %v", err)
	}
	journal, err := s.repo.InitialInstall(ctx)
	if err != nil || journal == nil || journal.RunID != "managed:another-install" {
		t.Fatalf("journal was rewritten: %#v %v", journal, err)
	}
	// The refusal happens after the Key records, so the installation is left in a
	// visible half-state: the workspace lists the keys while no journal binds them
	// to an installation, and every surface that needs one keeps refusing.
	listed, err := s.keys.List(ctx)
	if err != nil || len(listed) != 1+len(adoptedSourceKeys) {
		t.Fatalf("keys registered before the refusal = %#v %v", listed, err)
	}
	adopted := 0
	for _, key := range listed {
		if key.Origin == repo.OriginAdopted {
			adopted++
		}
	}
	if adopted != len(adoptedSourceKeys) {
		t.Fatalf("adopted records after the refusal = %d", adopted)
	}
}
