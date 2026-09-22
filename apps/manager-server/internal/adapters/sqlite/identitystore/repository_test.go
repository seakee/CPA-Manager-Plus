package identitystore_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"

	adaptersqlite "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/adapters/sqlite/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func setupTestDB(t *testing.T) (*sql.DB, ports.Repository) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test-identity.sqlite")
	db, err := sqlite.Open(dbPath)
	if err != nil {
		t.Fatalf("failed to open test sqlite db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	repo := adaptersqlite.New(db)
	return db, repo
}

func TestAPIKeyRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, repo := setupTestDB(t)

	keyID, err := identity.NewAPIKeyID()
	if err != nil {
		t.Fatalf("generate keyID: %v", err)
	}
	keyHash := strings.Repeat("a", 64)
	rtID := "runtime-embedded-1"

	ent := identity.APIKeyIdentity{
		ID:          keyID,
		Revision:    identity.InitialRevision,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 1000,
		UpdatedAtMS: 1000,
	}
	binding := identity.APIKeySourceBinding{
		APIKeyID:                  keyID,
		RuntimeIdentity:           rtID,
		APIKeyHash:                keyHash,
		ObservedRuntimeGeneration: 42,
		FirstSeenAtMS:             1000,
		LastSeenAtMS:              1000,
		RetiredAtMS:               0,
	}

	// Create
	if err := repo.CreateAPIKey(ctx, ent, binding); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	// Load by ID
	loadedEnt, err := repo.LoadAPIKeyByID(ctx, keyID)
	if err != nil {
		t.Fatalf("load api key by ID: %v", err)
	}
	if loadedEnt != ent {
		t.Fatalf("loaded entity %+v != expected %+v", loadedEnt, ent)
	}

	// Find by source
	foundEnt, foundBinding, err := repo.FindActiveAPIKeyBySource(ctx, rtID, keyHash)
	if err != nil {
		t.Fatalf("find active api key by source: %v", err)
	}
	if foundEnt != ent {
		t.Fatalf("found entity %+v != expected %+v", foundEnt, ent)
	}
	if foundBinding.APIKeyID != keyID ||
		foundBinding.RuntimeIdentity != rtID ||
		foundBinding.APIKeyHash != keyHash ||
		foundBinding.ObservedRuntimeGeneration != 42 ||
		foundBinding.FirstSeenAtMS != 1000 ||
		foundBinding.LastSeenAtMS != 1000 ||
		foundBinding.RetiredAtMS != 0 {
		t.Fatalf("found binding %+v does not match expected", foundBinding)
	}

	// Non-existent ID returns ErrNotFound
	nonExistentID := identity.APIKeyID("ffffffffffffffffffffffffffffffff")
	_, err = repo.LoadAPIKeyByID(ctx, nonExistentID)
	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-existent ID, got %v", err)
	}

	// Non-existent source returns ErrNotFound
	_, _, err = repo.FindActiveAPIKeyBySource(ctx, rtID, strings.Repeat("b", 64))
	if !errors.Is(err, ports.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for non-existent source, got %v", err)
	}
}

func TestCredentialRoundTripAndFullMetadata(t *testing.T) {
	ctx := context.Background()
	_, repo := setupTestDB(t)

	credID, err := identity.NewCredentialID()
	if err != nil {
		t.Fatalf("generate credID: %v", err)
	}
	rtID := "runtime-embedded-1"
	authID := "auth-session-xyz"

	ent := identity.CredentialIdentity{
		ID:          credID,
		Revision:    identity.InitialRevision,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 2000,
		UpdatedAtMS: 2000,
	}
	binding := identity.CredentialSourceBinding{
		CredentialID:              credID,
		RuntimeIdentity:           rtID,
		SourceAuthID:              authID,
		AuthIndex:                 "openai-default",
		Provider:                  "openai",
		PhysicalName:              "openai.json",
		AccountSnapshot:           "alice@company.com",
		AccountIDSnapshot:         "acc-alice-123",
		ObservedRuntimeGeneration: 99999,
		FirstSeenAtMS:             2000,
		LastSeenAtMS:              2500,
		RetiredAtMS:               0,
	}

	// Create
	if err := repo.CreateCredential(ctx, ent, binding); err != nil {
		t.Fatalf("create credential: %v", err)
	}

	// Load by ID
	loadedEnt, err := repo.LoadCredentialByID(ctx, credID)
	if err != nil {
		t.Fatalf("load credential by ID: %v", err)
	}
	if loadedEnt != ent {
		t.Fatalf("loaded entity %+v != expected %+v", loadedEnt, ent)
	}

	// Find by source
	foundEnt, foundBinding, err := repo.FindActiveCredentialBySource(ctx, rtID, authID)
	if err != nil {
		t.Fatalf("find active credential by source: %v", err)
	}
	if foundEnt != ent {
		t.Fatalf("found entity %+v != expected %+v", foundEnt, ent)
	}
	if foundBinding.CredentialID != credID ||
		foundBinding.RuntimeIdentity != rtID ||
		foundBinding.SourceAuthID != authID ||
		foundBinding.AuthIndex != "openai-default" ||
		foundBinding.Provider != "openai" ||
		foundBinding.PhysicalName != "openai.json" ||
		foundBinding.AccountSnapshot != "alice@company.com" ||
		foundBinding.AccountIDSnapshot != "acc-alice-123" ||
		foundBinding.ObservedRuntimeGeneration != 99999 ||
		foundBinding.FirstSeenAtMS != 2000 ||
		foundBinding.LastSeenAtMS != 2500 ||
		foundBinding.RetiredAtMS != 0 {
		t.Fatalf("found binding %+v does not match expected", foundBinding)
	}
}

func TestRuntimeGenerationMaxUint64AndZeroRoundTrip(t *testing.T) {
	ctx := context.Background()
	_, repo := setupTestDB(t)

	// 1. math.MaxUint64 round-trip
	maxGen := uint64(math.MaxUint64)
	credID1, _ := identity.NewCredentialID()
	ent1 := identity.CredentialIdentity{
		ID:          credID1,
		Revision:    1,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 1000,
		UpdatedAtMS: 1000,
	}
	binding1 := identity.CredentialSourceBinding{
		CredentialID:              credID1,
		RuntimeIdentity:           "rt-1",
		SourceAuthID:              "auth-max-gen",
		ObservedRuntimeGeneration: maxGen,
		FirstSeenAtMS:             1000,
		LastSeenAtMS:              1000,
	}
	if err := repo.CreateCredential(ctx, ent1, binding1); err != nil {
		t.Fatalf("create credential with MaxUint64 generation: %v", err)
	}
	_, foundBinding1, err := repo.FindActiveCredentialBySource(ctx, "rt-1", "auth-max-gen")
	if err != nil {
		t.Fatalf("find credential with MaxUint64 generation: %v", err)
	}
	if foundBinding1.ObservedRuntimeGeneration != maxGen {
		t.Fatalf("ObservedRuntimeGeneration = %d, want math.MaxUint64 (%d)",
			foundBinding1.ObservedRuntimeGeneration, maxGen)
	}

	// 2. Generation = 0 (unavailable) persists as NULL and decodes as 0
	credID2, _ := identity.NewCredentialID()
	ent2 := identity.CredentialIdentity{
		ID:          credID2,
		Revision:    1,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 2000,
		UpdatedAtMS: 2000,
	}
	binding2 := identity.CredentialSourceBinding{
		CredentialID:              credID2,
		RuntimeIdentity:           "rt-1",
		SourceAuthID:              "auth-zero-gen",
		ObservedRuntimeGeneration: 0,
		FirstSeenAtMS:             2000,
		LastSeenAtMS:              2000,
	}
	if err := repo.CreateCredential(ctx, ent2, binding2); err != nil {
		t.Fatalf("create credential with 0 generation: %v", err)
	}
	_, foundBinding2, err := repo.FindActiveCredentialBySource(ctx, "rt-1", "auth-zero-gen")
	if err != nil {
		t.Fatalf("find credential with 0 generation: %v", err)
	}
	if foundBinding2.ObservedRuntimeGeneration != 0 {
		t.Fatalf("ObservedRuntimeGeneration = %d, want 0", foundBinding2.ObservedRuntimeGeneration)
	}
}

func TestRuntimeScopeIsolation(t *testing.T) {
	ctx := context.Background()
	_, repo := setupTestDB(t)

	// Same apiKeyHash in two different RuntimeIdentity scopes can bind to different Canonical APIKeyIDs
	keyHash := strings.Repeat("c", 64)

	keyID_A, _ := identity.NewAPIKeyID()
	entA := identity.APIKeyIdentity{
		ID:          keyID_A,
		Revision:    1,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 1000,
		UpdatedAtMS: 1000,
	}
	bindingA := identity.APIKeySourceBinding{
		APIKeyID:        keyID_A,
		RuntimeIdentity: "runtime-A",
		APIKeyHash:      keyHash,
		FirstSeenAtMS:   1000,
		LastSeenAtMS:    1000,
	}
	if err := repo.CreateAPIKey(ctx, entA, bindingA); err != nil {
		t.Fatalf("create api key in runtime-A: %v", err)
	}

	keyID_B, _ := identity.NewAPIKeyID()
	entB := identity.APIKeyIdentity{
		ID:          keyID_B,
		Revision:    1,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 1000,
		UpdatedAtMS: 1000,
	}
	bindingB := identity.APIKeySourceBinding{
		APIKeyID:        keyID_B,
		RuntimeIdentity: "runtime-B",
		APIKeyHash:      keyHash,
		FirstSeenAtMS:   1000,
		LastSeenAtMS:    1000,
	}
	if err := repo.CreateAPIKey(ctx, entB, bindingB); err != nil {
		t.Fatalf("create api key in runtime-B with same hash should succeed: %v", err)
	}

	foundA, _, err := repo.FindActiveAPIKeyBySource(ctx, "runtime-A", keyHash)
	if err != nil || foundA.ID != keyID_A {
		t.Fatalf("expected keyID_A in runtime-A, got %v, err=%v", foundA.ID, err)
	}
	foundB, _, err := repo.FindActiveAPIKeyBySource(ctx, "runtime-B", keyHash)
	if err != nil || foundB.ID != keyID_B {
		t.Fatalf("expected keyID_B in runtime-B, got %v, err=%v", foundB.ID, err)
	}

	// Same CPA Auth.ID in two different RuntimeIdentity scopes can bind to different CredentialIDs
	authID := "auth-shared-id"
	credID_A, _ := identity.NewCredentialID()
	credEntA := identity.CredentialIdentity{
		ID:          credID_A,
		Revision:    1,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 1000,
		UpdatedAtMS: 1000,
	}
	credBindingA := identity.CredentialSourceBinding{
		CredentialID:    credID_A,
		RuntimeIdentity: "runtime-A",
		SourceAuthID:    authID,
		FirstSeenAtMS:   1000,
		LastSeenAtMS:    1000,
	}
	if err := repo.CreateCredential(ctx, credEntA, credBindingA); err != nil {
		t.Fatalf("create credential in runtime-A: %v", err)
	}

	credID_B, _ := identity.NewCredentialID()
	credEntB := identity.CredentialIdentity{
		ID:          credID_B,
		Revision:    1,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 1000,
		UpdatedAtMS: 1000,
	}
	credBindingB := identity.CredentialSourceBinding{
		CredentialID:    credID_B,
		RuntimeIdentity: "runtime-B",
		SourceAuthID:    authID,
		FirstSeenAtMS:   1000,
		LastSeenAtMS:    1000,
	}
	if err := repo.CreateCredential(ctx, credEntB, credBindingB); err != nil {
		t.Fatalf("create credential in runtime-B with same authID should succeed: %v", err)
	}
}

func TestActiveSourceConflictAndAtomicRollback(t *testing.T) {
	ctx := context.Background()
	db, repo := setupTestDB(t)

	keyHash := strings.Repeat("d", 64)
	rtID := "runtime-embedded-1"

	// 1. Initial successful create
	keyID1, _ := identity.NewAPIKeyID()
	ent1 := identity.APIKeyIdentity{
		ID:          keyID1,
		Revision:    1,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 1000,
		UpdatedAtMS: 1000,
	}
	binding1 := identity.APIKeySourceBinding{
		APIKeyID:        keyID1,
		RuntimeIdentity: rtID,
		APIKeyHash:      keyHash,
		FirstSeenAtMS:   1000,
		LastSeenAtMS:    1000,
	}
	if err := repo.CreateAPIKey(ctx, ent1, binding1); err != nil {
		t.Fatalf("initial create api key: %v", err)
	}

	// 2. Conflicting create with same (RuntimeIdentity, APIKeyHash) but new APIKeyID
	keyID2, _ := identity.NewAPIKeyID()
	ent2 := identity.APIKeyIdentity{
		ID:          keyID2,
		Revision:    1,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 2000,
		UpdatedAtMS: 2000,
	}
	binding2 := identity.APIKeySourceBinding{
		APIKeyID:        keyID2,
		RuntimeIdentity: rtID,
		APIKeyHash:      keyHash,
		FirstSeenAtMS:   2000,
		LastSeenAtMS:    2000,
	}
	err := repo.CreateAPIKey(ctx, ent2, binding2)
	if err == nil {
		t.Fatalf("expected ErrSourceBindingConflict on duplicate active source, got nil")
	}
	if !errors.Is(err, ports.ErrSourceBindingConflict) {
		t.Fatalf("expected ErrSourceBindingConflict, got %v", err)
	}

	// 3. Verify atomic rollback: keyID2 row MUST NOT exist in gateway_api_key_identities
	var count int
	err = db.QueryRow(`select count(*) from `+sqlite.GatewayAPIKeyIdentitiesTable+` where id = ?`, string(keyID2)).Scan(&count)
	if err != nil {
		t.Fatalf("query keyID2 existence: %v", err)
	}
	if count != 0 {
		t.Fatalf("conflict transaction left orphan APIKeyIdentity row in database! count = %d", count)
	}

	// 4. Same conflict and rollback verification for Credential
	authID := "auth-duplicate"
	credID1, _ := identity.NewCredentialID()
	credEnt1 := identity.CredentialIdentity{
		ID:          credID1,
		Revision:    1,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 1000,
		UpdatedAtMS: 1000,
	}
	credBinding1 := identity.CredentialSourceBinding{
		CredentialID:    credID1,
		RuntimeIdentity: rtID,
		SourceAuthID:    authID,
		FirstSeenAtMS:   1000,
		LastSeenAtMS:    1000,
	}
	if err := repo.CreateCredential(ctx, credEnt1, credBinding1); err != nil {
		t.Fatalf("initial create credential: %v", err)
	}

	credID2, _ := identity.NewCredentialID()
	credEnt2 := identity.CredentialIdentity{
		ID:          credID2,
		Revision:    1,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 2000,
		UpdatedAtMS: 2000,
	}
	credBinding2 := identity.CredentialSourceBinding{
		CredentialID:    credID2,
		RuntimeIdentity: rtID,
		SourceAuthID:    authID,
		FirstSeenAtMS:   2000,
		LastSeenAtMS:    2000,
	}
	err = repo.CreateCredential(ctx, credEnt2, credBinding2)
	if err == nil {
		t.Fatalf("expected ErrSourceBindingConflict on duplicate active credential source, got nil")
	}
	if !errors.Is(err, ports.ErrSourceBindingConflict) {
		t.Fatalf("expected ErrSourceBindingConflict, got %v", err)
	}

	err = db.QueryRow(`select count(*) from `+sqlite.GatewayCredentialIdentitiesTable+` where id = ?`, string(credID2)).Scan(&count)
	if err != nil {
		t.Fatalf("query credID2 existence: %v", err)
	}
	if count != 0 {
		t.Fatalf("conflict transaction left orphan CredentialIdentity row in database! count = %d", count)
	}
}

func TestCASLifecycleMutationAndFencing(t *testing.T) {
	ctx := context.Background()
	_, repo := setupTestDB(t)

	keyID, _ := identity.NewAPIKeyID()
	ent := identity.APIKeyIdentity{
		ID:          keyID,
		Revision:    1,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 1000,
		UpdatedAtMS: 1000,
	}
	binding := identity.APIKeySourceBinding{
		APIKeyID:        keyID,
		RuntimeIdentity: "rt-1",
		APIKeyHash:      strings.Repeat("e", 64),
		FirstSeenAtMS:   1000,
		LastSeenAtMS:    1000,
	}
	if err := repo.CreateAPIKey(ctx, ent, binding); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	// 1. Revision 1/active -> expected 1/missing -> Revision 2
	mutated1, err := repo.SetAPIKeyLifecycle(ctx, keyID, 1, identity.LifecycleMissing, 2000)
	if err != nil {
		t.Fatalf("CAS mutation 1 -> 2: %v", err)
	}
	if mutated1.Revision != 2 || mutated1.Lifecycle != identity.LifecycleMissing || mutated1.UpdatedAtMS != 2000 {
		t.Fatalf("unexpected mutated1 state: %+v", mutated1)
	}

	// 2. Stale expected 1 -> ErrRevisionConflict
	_, err = repo.SetAPIKeyLifecycle(ctx, keyID, 1, identity.LifecycleActive, 3000)
	if !errors.Is(err, ports.ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict for stale expected revision 1, got %v", err)
	}

	// 3. Stale expected 1 with same-state (missing -> missing) MUST still fail with ErrRevisionConflict
	_, err = repo.SetAPIKeyLifecycle(ctx, keyID, 1, identity.LifecycleMissing, 3000)
	if !errors.Is(err, ports.ErrRevisionConflict) {
		t.Fatalf("expected ErrRevisionConflict for stale same-state mutation, got %v", err)
	}

	// 4. Expected 2/missing -> expected 2/missing (fresh same-state no-op) returns unchanged current
	noopEnt, err := repo.SetAPIKeyLifecycle(ctx, keyID, 2, identity.LifecycleMissing, 3000)
	if err != nil {
		t.Fatalf("expected successful no-op, got %v", err)
	}
	if noopEnt.Revision != 2 || noopEnt.UpdatedAtMS != 2000 {
		t.Fatalf("no-op should retain revision 2 and updatedAtMs 2000, got %+v", noopEnt)
	}

	// 5. Expected 2/missing -> expected 2/active -> Revision 3
	mutated2, err := repo.SetAPIKeyLifecycle(ctx, keyID, 2, identity.LifecycleActive, 4000)
	if err != nil {
		t.Fatalf("CAS mutation 2 -> 3: %v", err)
	}
	if mutated2.Revision != 3 || mutated2.Lifecycle != identity.LifecycleActive || mutated2.UpdatedAtMS != 4000 {
		t.Fatalf("unexpected mutated2 state: %+v", mutated2)
	}

	// 6. Expected 3/active -> expected 3/superseded -> Revision 4
	mutated3, err := repo.SetAPIKeyLifecycle(ctx, keyID, 3, identity.LifecycleSuperseded, 5000)
	if err != nil {
		t.Fatalf("CAS mutation 3 -> 4: %v", err)
	}
	if mutated3.Revision != 4 || mutated3.Lifecycle != identity.LifecycleSuperseded {
		t.Fatalf("unexpected mutated3 state: %+v", mutated3)
	}

	// 7. Superseded is terminal: attempting to reactivate must return ErrInvalidLifecycleTransition
	_, err = repo.SetAPIKeyLifecycle(ctx, keyID, 4, identity.LifecycleActive, 6000)
	if !errors.Is(err, ports.ErrInvalidLifecycleTransition) {
		t.Fatalf("expected ErrInvalidLifecycleTransition when reactivating superseded entity, got %v", err)
	}

	// 8. Timestamp monotonicity: when nowMS <= current.UpdatedAtMS, persisted is current.UpdatedAtMS + 1
	credID, _ := identity.NewCredentialID()
	credEnt := identity.CredentialIdentity{
		ID:          credID,
		Revision:    1,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 5000,
		UpdatedAtMS: 5000,
	}
	credBinding := identity.CredentialSourceBinding{
		CredentialID:    credID,
		RuntimeIdentity: "rt-1",
		SourceAuthID:    "auth-time",
		FirstSeenAtMS:   5000,
		LastSeenAtMS:    5000,
	}
	if err := repo.CreateCredential(ctx, credEnt, credBinding); err != nil {
		t.Fatalf("create credential: %v", err)
	}
	// Pass nowMS = 4000 (< 5000)
	timeMutated, err := repo.SetCredentialLifecycle(ctx, credID, 1, identity.LifecycleMissing, 4000)
	if err != nil {
		t.Fatalf("SetCredentialLifecycle with smaller timestamp: %v", err)
	}
	if timeMutated.UpdatedAtMS != 5001 {
		t.Fatalf("expected monotonic updatedAtMs = 5001, got %d", timeMutated.UpdatedAtMS)
	}
}

func TestRevisionOverflow(t *testing.T) {
	ctx := context.Background()
	db, repo := setupTestDB(t)

	keyID, _ := identity.NewAPIKeyID()
	ent := identity.APIKeyIdentity{
		ID:          keyID,
		Revision:    1,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 1000,
		UpdatedAtMS: 1000,
	}
	binding := identity.APIKeySourceBinding{
		APIKeyID:        keyID,
		RuntimeIdentity: "rt-1",
		APIKeyHash:      strings.Repeat("f", 64),
		FirstSeenAtMS:   1000,
		LastSeenAtMS:    1000,
	}
	if err := repo.CreateAPIKey(ctx, ent, binding); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	// Update DB to math.MaxInt64
	_, err := db.Exec(`update `+sqlite.GatewayAPIKeyIdentitiesTable+` set revision = ? where id = ?`,
		int64(math.MaxInt64), string(keyID))
	if err != nil {
		t.Fatalf("seed MaxInt64 revision: %v", err)
	}

	// Real transition from math.MaxInt64 must fail with ErrRevisionOverflow
	_, err = repo.SetAPIKeyLifecycle(ctx, keyID, identity.Revision(math.MaxInt64), identity.LifecycleMissing, 2000)
	if !errors.Is(err, ports.ErrRevisionOverflow) {
		t.Fatalf("expected ErrRevisionOverflow, got %v", err)
	}
}

func TestMalformedStoredGenerationFailsClosed(t *testing.T) {
	ctx := context.Background()
	db, repo := setupTestDB(t)

	keyHash := strings.Repeat("0", 64)
	keyID, _ := identity.NewAPIKeyID()
	ent := identity.APIKeyIdentity{
		ID:          keyID,
		Revision:    1,
		Lifecycle:   identity.LifecycleActive,
		CreatedAtMS: 1000,
		UpdatedAtMS: 1000,
	}
	binding := identity.APIKeySourceBinding{
		APIKeyID:                  keyID,
		RuntimeIdentity:           "rt-1",
		APIKeyHash:                keyHash,
		ObservedRuntimeGeneration: 100,
		FirstSeenAtMS:             1000,
		LastSeenAtMS:              1000,
	}
	if err := repo.CreateAPIKey(ctx, ent, binding); err != nil {
		t.Fatalf("create api key: %v", err)
	}

	malformedGenerations := []string{
		"-1",
		"abc",
		"18446744073709551616", // overflow > math.MaxUint64
		"01",                   // forbidden leading zero
		" 123",                 // leading space
		"123 ",                 // trailing space
		"0",                    // non-NULL generation cannot be 0
	}

	for _, badVal := range malformedGenerations {
		t.Run(fmt.Sprintf("malformed_%s", badVal), func(t *testing.T) {
			_, err := db.Exec(`update `+sqlite.GatewayAPIKeySourceBindingsTable+`
				set observed_runtime_generation = ? where api_key_id = ?`,
				badVal, string(keyID))
			if err != nil {
				t.Fatalf("update malformed generation: %v", err)
			}

			// Reading must fail closed!
			_, _, err = repo.FindActiveAPIKeyBySource(ctx, "rt-1", keyHash)
			if err == nil {
				t.Fatalf("expected error reading malformed generation %q, got nil", badVal)
			}
		})
	}
}
