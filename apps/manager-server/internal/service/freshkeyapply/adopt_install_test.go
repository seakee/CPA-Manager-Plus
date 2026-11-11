package freshkeyapply

import (
	"context"
	"database/sql"
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
func adoptedFixtureWithDB(t *testing.T, clientKeys ...string) (*Service, *managedCPA, *ModelSource, *sql.DB) {
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
	keys := freshkeys.New(r, protector, freshkeys.WithSourceRuntime(func(context.Context) (string, error) {
		return managedRuntimeIdentity, nil
	}))
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
		Strategy: "adopt_existing", ProductComplete: true,
	}}
	return NewManaged(r, keys, rt, server.URL, source.get), cpa, source, db
}

func adoptedFixture(t *testing.T, clientKeys ...string) (*Service, *managedCPA, *ModelSource) {
	t.Helper()
	s, cpa, source, _ := adoptedFixtureWithDB(t, clientKeys...)
	return s, cpa, source
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

// Config, admission and key-operation authority require BOTH the claimed
// one-shot install journal AND durable `ProductComplete`. If completion has not
// committed yet — even after `RegisterAdoption` wrote keys and claimed the
// journal — every consumer fails closed until `ProductComplete` becomes true.
func TestAdoptionRegistrationGrantsTheConfigAuthority(t *testing.T) {
	s, cpa, source := adoptedFixture(t, adoptedSourceKeys...)
	source.set(func(install *ManagedInstall) { install.ProductComplete = false })
	ctx := t.Context()
	if _, err := s.ConfigTarget(ctx, 9); err == nil {
		t.Fatal("an unresolved adoption already held configuration authority")
	}
	if s.DefaultInstallCPAAuthorityProven(ctx) {
		t.Fatal("an incomplete adoption was admitted before registration")
	}
	keys, err := s.RegisterAdoption(ctx, adoptedSourceKeys)
	if err != nil {
		t.Fatal(err)
	}
	// Registration alone — before `ProductComplete` commits — grants neither
	// config writes, Gateway admission, nor key-operation authority.
	if _, err := s.ConfigTarget(ctx, 9); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("incomplete adoption granted ConfigTarget: %v", err)
	}
	if s.DefaultInstallCPAAuthorityProven(ctx) {
		t.Fatal("incomplete adoption proved Gateway admission before ProductComplete")
	}
	if _, err := s.Prepare(ctx, strings.Repeat("1", 32), string(keys[0].ID), string(keys[1].ID), "revoke", 9); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("incomplete adoption prepared a key operation: %v", err)
	}
	// Conversely, `ProductComplete` without a matching install journal still
	// refuses configuration writes.
	unregistered, _, _ := adoptedFixture(t, adoptedSourceKeys...)
	if _, err := unregistered.ConfigTarget(ctx, 9); !errors.Is(err, ErrGeneration) {
		t.Fatalf("completed adoption without journal granted ConfigTarget: %v", err)
	}
	// Once `ProductComplete` commits alongside the claimed journal, authority
	// and admission become active for the staged generation only.
	source.set(func(install *ManagedInstall) { install.ProductComplete = true })
	managementKey, err := s.ConfigTarget(ctx, 9)
	if err != nil || managementKey != cpa.management {
		t.Fatalf("config authority = %q %v", managementKey, err)
	}
	if !s.DefaultInstallCPAAuthorityProven(ctx) {
		t.Fatal("the completed adopted destination is not admitted")
	}
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

// An adopted destination commonly carries more than two client keys. Revoking one
// removes exactly that credential from the destination — it stops authenticating
// while every other key the adoption registered keeps working — and the key left
// installed can then be replaced.
func TestAdoptedInstallationRevokesAndReplacesWithThreeKeys(t *testing.T) {
	destinationKeys := []string{"source-client-one", "source-client-two", "source-client-three"}
	s, cpa, _ := adoptedFixture(t, destinationKeys...)
	ctx := t.Context()
	keys, err := s.RegisterAdoption(ctx, destinationKeys)
	if err != nil || len(keys) != 3 {
		t.Fatalf("register adoption: %#v %v", keys, err)
	}
	w, ok := s.wire.(keyWire)
	if !ok {
		t.Fatal("adopt fixture has no key wire")
	}

	// Revoke the second adopted key and keep the installation's own key.
	id := strings.Repeat("8", 32)
	if _, err = s.Prepare(ctx, id, string(keys[1].ID), string(keys[0].ID), "revoke", 9); err != nil {
		t.Fatalf("three-key adopted revoke prepare: %v", err)
	}
	result, err := s.ContinueOperation(ctx, id, 9)
	if err != nil || result.Operation == nil || result.Operation.Outcome != "committed" ||
		result.RuntimeState != "effective_observed" {
		t.Fatalf("three-key adopted revoke: %#v %v", result, err)
	}
	if got := cpa.snapshot(); !slices.Equal(got, []string{destinationKeys[0], destinationKeys[2]}) {
		t.Fatalf("revoke left %v", got)
	}
	// The destination's own authentication is the proof the page's tests use: the
	// revoked credential is 401 and both survivors are 200.
	if ok, err = w.SampleKeySet(ctx, []string{destinationKeys[0], destinationKeys[2]}, []string{destinationKeys[1]}); err != nil || !ok {
		t.Fatalf("post-revoke sampling: %v %v", ok, err)
	}

	// The surviving adopted keys are still the destination's set, so one of them
	// can be replaced by a key this Manager mints without touching the other.
	replacement, _, err := s.keys.Create(ctx, strings.Repeat("e", 32))
	if err != nil {
		t.Fatal(err)
	}
	replaceID := strings.Repeat("9", 32)
	if _, err = s.Prepare(ctx, replaceID, string(keys[0].ID), string(replacement.Key.ID), "replace", 9); err != nil {
		t.Fatalf("three-key adopted replace prepare: %v", err)
	}
	if result, err = s.ContinueOperation(ctx, replaceID, 9); err != nil || result.NextStep != "revoke" ||
		!slices.Equal(cpa.snapshot(), []string{destinationKeys[0], destinationKeys[2], replacement.CreatedSecret}) {
		t.Fatalf("three-key adopted replace add step: %#v %v keys=%v", result, err, cpa.snapshot())
	}
	if result, err = s.ContinueOperation(ctx, replaceID, 9); err != nil || result.Operation == nil ||
		result.Operation.Outcome != "committed" ||
		!slices.Equal(cpa.snapshot(), []string{destinationKeys[2], replacement.CreatedSecret}) {
		t.Fatalf("three-key adopted replace revoke step: %#v %v keys=%v", result, err, cpa.snapshot())
	}
}

// The journal and the adopted key batch commit together or not at all. An
// adoption that meets another installation's journal — or a retired key later
// in the batch — leaves neither adopted workspace records nor source bindings.
func TestAdoptionRegistrationRefusesAnotherInstallationsJournal(t *testing.T) {
	s, _, source, db := adoptedFixtureWithDB(t, adoptedSourceKeys...)
	ctx := t.Context()
	owner, _, err := s.keys.Create(ctx, strings.Repeat("b", 32))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.repo.ClaimInitialInstall(ctx, repo.InitialInstall{KeyID: string(owner.Key.ID), RunID: "managed:another-install", Generation: "9", AttemptedAtMS: 1}); err != nil {
		t.Fatal(err)
	}
	var bindingsBefore int
	if err = db.QueryRow(`select count(*) from gateway_api_key_source_bindings`).Scan(&bindingsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RegisterAdoption(ctx, adoptedSourceKeys); !errors.Is(err, ErrAdoptionConflict) || !errors.Is(err, ErrGeneration) {
		t.Fatalf("foreign journal = %v", err)
	}
	source.set(func(install *ManagedInstall) { install.RunID = "managed:operation-adoption" })
	if _, err = s.RegisterAdoption(ctx, adoptedSourceKeys); !errors.Is(err, ErrAdoptionConflict) || !errors.Is(err, ErrGeneration) {
		t.Fatalf("journal rebound to a second run: %v", err)
	}
	journal, err := s.repo.InitialInstall(ctx)
	if err != nil || journal == nil || journal.RunID != "managed:another-install" {
		t.Fatalf("journal was rewritten: %#v %v", journal, err)
	}
	listed, err := s.keys.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != owner.Key.ID {
		t.Fatalf("refused registration leaked adopted keys: %#v %v", listed, err)
	}
	var bindingsAfter int
	if err = db.QueryRow(`select count(*) from gateway_api_key_source_bindings`).Scan(&bindingsAfter); err != nil || bindingsAfter != bindingsBefore {
		t.Fatalf("refused registration changed source bindings from %d to %d: %v", bindingsBefore, bindingsAfter, err)
	}
}

// An adopted destination's keys are the source CPA's own shapes, and the private
// chat proof is the S1/S2 install proof: it admits a credential this Manager
// minted, by canonical length first and by the installation's staged secret
// second. Reading `Configured` as membership for adoption is what lets the install
// observation answer at all; the length gate is what keeps that reading from
// widening this consumer, so a non-canonical adopted credential is refused before
// the destination is asked anything.
func TestChatAdmissionStaysFailClosedForAnAdoptedDestinationsKeys(t *testing.T) {
	s, cpa, _ := adoptedFixture(t, adoptedSourceKeys...)
	ctx := t.Context()
	if _, err := s.RegisterAdoption(ctx, adoptedSourceKeys); err != nil {
		t.Fatal(err)
	}
	reads := cpa.v8Reads
	for _, candidate := range []string{
		adoptedSourceKeys[0],
		adoptedSourceKeys[1],
		model.MaskedImportedClientKey(adoptedSourceKeys[0]),
	} {
		if _, err := s.Admit(ctx, candidate, 9, strings.Repeat("a", 64)); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("an adopted key of %d characters was admitted: %v", len(candidate), err)
		}
	}
	if cpa.v8Reads != reads {
		t.Fatalf("a refused candidate reached the destination's config")
	}
}
