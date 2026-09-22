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

func TestSetLifecycleFailsClosedOnCorruptedPersistedRow(t *testing.T) {
	ctx := context.Background()

	t.Run("APIKeyIdentity", func(t *testing.T) {
		corruptions := []struct {
			name      string
			updateSQL string
			args      func(id identity.APIKeyID) []any
		}{
			{
				name:      "revision_zero",
				updateSQL: `update ` + sqlite.GatewayAPIKeyIdentitiesTable + ` set revision = 0 where id = ?`,
				args:      func(id identity.APIKeyID) []any { return []any{string(id)} },
			},
			{
				name:      "created_at_zero",
				updateSQL: `update ` + sqlite.GatewayAPIKeyIdentitiesTable + ` set created_at_ms = 0 where id = ?`,
				args:      func(id identity.APIKeyID) []any { return []any{string(id)} },
			},
			{
				name:      "created_at_negative",
				updateSQL: `update ` + sqlite.GatewayAPIKeyIdentitiesTable + ` set created_at_ms = -1 where id = ?`,
				args:      func(id identity.APIKeyID) []any { return []any{string(id)} },
			},
			{
				name:      "updated_at_less_than_created_at",
				updateSQL: `update ` + sqlite.GatewayAPIKeyIdentitiesTable + ` set created_at_ms = 2000, updated_at_ms = 1000 where id = ?`,
				args:      func(id identity.APIKeyID) []any { return []any{string(id)} },
			},
			{
				name:      "invalid_lifecycle",
				updateSQL: `update ` + sqlite.GatewayAPIKeyIdentitiesTable + ` set lifecycle = 'corrupted_state' where id = ?`,
				args:      func(id identity.APIKeyID) []any { return []any{string(id)} },
			},
		}

		for _, tc := range corruptions {
			t.Run(tc.name, func(t *testing.T) {
				db, repo := setupTestDB(t)

				keyID, err := identity.NewAPIKeyID()
				if err != nil {
					t.Fatalf("generate keyID: %v", err)
				}
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
					APIKeyHash:      strings.Repeat("a", 64),
					FirstSeenAtMS:   1000,
					LastSeenAtMS:    1000,
				}
				if err := repo.CreateAPIKey(ctx, ent, binding); err != nil {
					t.Fatalf("create api key: %v", err)
				}

				// Inject corrupted state via direct SQL
				if _, err := db.Exec(tc.updateSQL, tc.args(keyID)...); err != nil {
					t.Fatalf("inject corruption: %v", err)
				}

				// Attempt lifecycle mutation: must fail closed!
				_, err = repo.SetAPIKeyLifecycle(ctx, keyID, 1, identity.LifecycleMissing, 2000)
				if err == nil {
					t.Fatalf("expected error mutating corrupted identity, got nil")
				}
				if !strings.Contains(err.Error(), "persisted api key identity invalid") {
					t.Fatalf("expected 'persisted api key identity invalid' error, got: %v", err)
				}

				// Verify database was NOT updated to target lifecycle
				var currentLC string
				if err := db.QueryRow(`select lifecycle from `+sqlite.GatewayAPIKeyIdentitiesTable+` where id = ?`, string(keyID)).Scan(&currentLC); err != nil {
					t.Fatalf("read lifecycle after failed mutation: %v", err)
				}
				if currentLC == string(identity.LifecycleMissing) {
					t.Fatalf("mutation unexpectedly modified lifecycle to %q on corrupted entity", currentLC)
				}
			})
		}
	})

	t.Run("CredentialIdentity", func(t *testing.T) {
		corruptions := []struct {
			name      string
			updateSQL string
			args      func(id identity.CredentialID) []any
		}{
			{
				name:      "revision_zero",
				updateSQL: `update ` + sqlite.GatewayCredentialIdentitiesTable + ` set revision = 0 where id = ?`,
				args:      func(id identity.CredentialID) []any { return []any{string(id)} },
			},
			{
				name:      "created_at_zero",
				updateSQL: `update ` + sqlite.GatewayCredentialIdentitiesTable + ` set created_at_ms = 0 where id = ?`,
				args:      func(id identity.CredentialID) []any { return []any{string(id)} },
			},
			{
				name:      "created_at_negative",
				updateSQL: `update ` + sqlite.GatewayCredentialIdentitiesTable + ` set created_at_ms = -1 where id = ?`,
				args:      func(id identity.CredentialID) []any { return []any{string(id)} },
			},
			{
				name:      "updated_at_less_than_created_at",
				updateSQL: `update ` + sqlite.GatewayCredentialIdentitiesTable + ` set created_at_ms = 2000, updated_at_ms = 1000 where id = ?`,
				args:      func(id identity.CredentialID) []any { return []any{string(id)} },
			},
			{
				name:      "invalid_lifecycle",
				updateSQL: `update ` + sqlite.GatewayCredentialIdentitiesTable + ` set lifecycle = 'corrupted_state' where id = ?`,
				args:      func(id identity.CredentialID) []any { return []any{string(id)} },
			},
		}

		for _, tc := range corruptions {
			t.Run(tc.name, func(t *testing.T) {
				db, repo := setupTestDB(t)

				credID, err := identity.NewCredentialID()
				if err != nil {
					t.Fatalf("generate credID: %v", err)
				}
				ent := identity.CredentialIdentity{
					ID:          credID,
					Revision:    1,
					Lifecycle:   identity.LifecycleActive,
					CreatedAtMS: 1000,
					UpdatedAtMS: 1000,
				}
				binding := identity.CredentialSourceBinding{
					CredentialID:    credID,
					RuntimeIdentity: "rt-1",
					SourceAuthID:    "auth-1",
					FirstSeenAtMS:   1000,
					LastSeenAtMS:    1000,
				}
				if err := repo.CreateCredential(ctx, ent, binding); err != nil {
					t.Fatalf("create credential: %v", err)
				}

				// Inject corrupted state via direct SQL
				if _, err := db.Exec(tc.updateSQL, tc.args(credID)...); err != nil {
					t.Fatalf("inject corruption: %v", err)
				}

				// Attempt lifecycle mutation: must fail closed!
				_, err = repo.SetCredentialLifecycle(ctx, credID, 1, identity.LifecycleMissing, 2000)
				if err == nil {
					t.Fatalf("expected error mutating corrupted identity, got nil")
				}
				if !strings.Contains(err.Error(), "persisted credential identity invalid") {
					t.Fatalf("expected 'persisted credential identity invalid' error, got: %v", err)
				}

				// Verify database was NOT updated to target lifecycle
				var currentLC string
				if err := db.QueryRow(`select lifecycle from `+sqlite.GatewayCredentialIdentitiesTable+` where id = ?`, string(credID)).Scan(&currentLC); err != nil {
					t.Fatalf("read lifecycle after failed mutation: %v", err)
				}
				if currentLC == string(identity.LifecycleMissing) {
					t.Fatalf("mutation unexpectedly modified lifecycle to %q on corrupted entity", currentLC)
				}
			})
		}
	})
}

func TestFindActiveBySourceOnlyReturnsActiveIdentities(t *testing.T) {
	ctx := context.Background()

	t.Run("APIKey", func(t *testing.T) {
		db, repo := setupTestDB(t)

		// 1. Active entity -> FindActive succeeds
		keyID1, _ := identity.NewAPIKeyID()
		hash1 := strings.Repeat("1", 64)
		ent1 := identity.APIKeyIdentity{
			ID:          keyID1,
			Revision:    1,
			Lifecycle:   identity.LifecycleActive,
			CreatedAtMS: 1000,
			UpdatedAtMS: 1000,
		}
		binding1 := identity.APIKeySourceBinding{
			APIKeyID:        keyID1,
			RuntimeIdentity: "rt-1",
			APIKeyHash:      hash1,
			FirstSeenAtMS:   1000,
			LastSeenAtMS:    1000,
		}
		if err := repo.CreateAPIKey(ctx, ent1, binding1); err != nil {
			t.Fatalf("create api key 1: %v", err)
		}
		found1, _, err := repo.FindActiveAPIKeyBySource(ctx, "rt-1", hash1)
		if err != nil {
			t.Fatalf("find active api key 1: %v", err)
		}
		if found1.ID != keyID1 {
			t.Fatalf("found ID = %v, want %v", found1.ID, keyID1)
		}

		// 2. Active -> SetAPIKeyLifecycle(missing) -> FindActive returns ErrNotFound
		_, err = repo.SetAPIKeyLifecycle(ctx, keyID1, 1, identity.LifecycleMissing, 2000)
		if err != nil {
			t.Fatalf("set lifecycle missing: %v", err)
		}
		_, _, err = repo.FindActiveAPIKeyBySource(ctx, "rt-1", hash1)
		if !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("expected ErrNotFound for missing identity, got: %v", err)
		}

		// 3. Create another entity -> SetAPIKeyLifecycle(superseded) -> FindActive returns ErrNotFound
		keyID2, _ := identity.NewAPIKeyID()
		hash2 := strings.Repeat("2", 64)
		ent2 := identity.APIKeyIdentity{
			ID:          keyID2,
			Revision:    1,
			Lifecycle:   identity.LifecycleActive,
			CreatedAtMS: 1000,
			UpdatedAtMS: 1000,
		}
		binding2 := identity.APIKeySourceBinding{
			APIKeyID:        keyID2,
			RuntimeIdentity: "rt-1",
			APIKeyHash:      hash2,
			FirstSeenAtMS:   1000,
			LastSeenAtMS:    1000,
		}
		if err := repo.CreateAPIKey(ctx, ent2, binding2); err != nil {
			t.Fatalf("create api key 2: %v", err)
		}
		_, err = repo.SetAPIKeyLifecycle(ctx, keyID2, 1, identity.LifecycleSuperseded, 2000)
		if err != nil {
			t.Fatalf("set lifecycle superseded: %v", err)
		}
		_, _, err = repo.FindActiveAPIKeyBySource(ctx, "rt-1", hash2)
		if !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("expected ErrNotFound for superseded identity, got: %v", err)
		}

		// 4. Create another entity -> direct SQL update to superseded -> FindActive returns ErrNotFound
		keyID3, _ := identity.NewAPIKeyID()
		hash3 := strings.Repeat("3", 64)
		ent3 := identity.APIKeyIdentity{
			ID:          keyID3,
			Revision:    1,
			Lifecycle:   identity.LifecycleActive,
			CreatedAtMS: 1000,
			UpdatedAtMS: 1000,
		}
		binding3 := identity.APIKeySourceBinding{
			APIKeyID:        keyID3,
			RuntimeIdentity: "rt-1",
			APIKeyHash:      hash3,
			FirstSeenAtMS:   1000,
			LastSeenAtMS:    1000,
		}
		if err := repo.CreateAPIKey(ctx, ent3, binding3); err != nil {
			t.Fatalf("create api key 3: %v", err)
		}
		if _, err := db.Exec(`update `+sqlite.GatewayAPIKeyIdentitiesTable+` set lifecycle = 'superseded' where id = ?`, string(keyID3)); err != nil {
			t.Fatalf("direct SQL set superseded: %v", err)
		}
		_, _, err = repo.FindActiveAPIKeyBySource(ctx, "rt-1", hash3)
		if !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("expected ErrNotFound for direct SQL superseded identity, got: %v", err)
		}
	})

	t.Run("Credential", func(t *testing.T) {
		db, repo := setupTestDB(t)

		// 1. Active entity -> FindActive succeeds
		credID1, _ := identity.NewCredentialID()
		authID1 := "auth-active-1"
		ent1 := identity.CredentialIdentity{
			ID:          credID1,
			Revision:    1,
			Lifecycle:   identity.LifecycleActive,
			CreatedAtMS: 1000,
			UpdatedAtMS: 1000,
		}
		binding1 := identity.CredentialSourceBinding{
			CredentialID:    credID1,
			RuntimeIdentity: "rt-1",
			SourceAuthID:    authID1,
			FirstSeenAtMS:   1000,
			LastSeenAtMS:    1000,
		}
		if err := repo.CreateCredential(ctx, ent1, binding1); err != nil {
			t.Fatalf("create credential 1: %v", err)
		}
		found1, _, err := repo.FindActiveCredentialBySource(ctx, "rt-1", authID1)
		if err != nil {
			t.Fatalf("find active credential 1: %v", err)
		}
		if found1.ID != credID1 {
			t.Fatalf("found ID = %v, want %v", found1.ID, credID1)
		}

		// 2. Active -> SetCredentialLifecycle(missing) -> FindActive returns ErrNotFound
		_, err = repo.SetCredentialLifecycle(ctx, credID1, 1, identity.LifecycleMissing, 2000)
		if err != nil {
			t.Fatalf("set lifecycle missing: %v", err)
		}
		_, _, err = repo.FindActiveCredentialBySource(ctx, "rt-1", authID1)
		if !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("expected ErrNotFound for missing credential, got: %v", err)
		}

		// 3. Create another entity -> SetCredentialLifecycle(superseded) -> FindActive returns ErrNotFound
		credID2, _ := identity.NewCredentialID()
		authID2 := "auth-active-2"
		ent2 := identity.CredentialIdentity{
			ID:          credID2,
			Revision:    1,
			Lifecycle:   identity.LifecycleActive,
			CreatedAtMS: 1000,
			UpdatedAtMS: 1000,
		}
		binding2 := identity.CredentialSourceBinding{
			CredentialID:    credID2,
			RuntimeIdentity: "rt-1",
			SourceAuthID:    authID2,
			FirstSeenAtMS:   1000,
			LastSeenAtMS:    1000,
		}
		if err := repo.CreateCredential(ctx, ent2, binding2); err != nil {
			t.Fatalf("create credential 2: %v", err)
		}
		_, err = repo.SetCredentialLifecycle(ctx, credID2, 1, identity.LifecycleSuperseded, 2000)
		if err != nil {
			t.Fatalf("set lifecycle superseded: %v", err)
		}
		_, _, err = repo.FindActiveCredentialBySource(ctx, "rt-1", authID2)
		if !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("expected ErrNotFound for superseded credential, got: %v", err)
		}

		// 4. Create another entity -> direct SQL update to superseded -> FindActive returns ErrNotFound
		credID3, _ := identity.NewCredentialID()
		authID3 := "auth-active-3"
		ent3 := identity.CredentialIdentity{
			ID:          credID3,
			Revision:    1,
			Lifecycle:   identity.LifecycleActive,
			CreatedAtMS: 1000,
			UpdatedAtMS: 1000,
		}
		binding3 := identity.CredentialSourceBinding{
			CredentialID:    credID3,
			RuntimeIdentity: "rt-1",
			SourceAuthID:    authID3,
			FirstSeenAtMS:   1000,
			LastSeenAtMS:    1000,
		}
		if err := repo.CreateCredential(ctx, ent3, binding3); err != nil {
			t.Fatalf("create credential 3: %v", err)
		}
		if _, err := db.Exec(`update `+sqlite.GatewayCredentialIdentitiesTable+` set lifecycle = 'superseded' where id = ?`, string(credID3)); err != nil {
			t.Fatalf("direct SQL set superseded: %v", err)
		}
		_, _, err = repo.FindActiveCredentialBySource(ctx, "rt-1", authID3)
		if !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("expected ErrNotFound for direct SQL superseded credential, got: %v", err)
		}
	})
}

func TestCanonicalIDCollisionDoesNotReturnBindingConflict(t *testing.T) {
	ctx := context.Background()

	t.Run("APIKey", func(t *testing.T) {
		db, repo := setupTestDB(t)

		canonicalID, err := identity.NewAPIKeyID()
		if err != nil {
			t.Fatalf("generate keyID: %v", err)
		}

		// 1. First create with canonicalID and source runtime-A / hash-A
		hashA := strings.Repeat("a", 64)
		entA := identity.APIKeyIdentity{
			ID:          canonicalID,
			Revision:    1,
			Lifecycle:   identity.LifecycleActive,
			CreatedAtMS: 1000,
			UpdatedAtMS: 1000,
		}
		bindingA := identity.APIKeySourceBinding{
			APIKeyID:        canonicalID,
			RuntimeIdentity: "runtime-A",
			APIKeyHash:      hashA,
			FirstSeenAtMS:   1000,
			LastSeenAtMS:    1000,
		}
		if err := repo.CreateAPIKey(ctx, entA, bindingA); err != nil {
			t.Fatalf("create initial api key: %v", err)
		}

		// 2. Second create with SAME canonicalID but DIFFERENT free source (runtime-B / hash-B)
		hashB := strings.Repeat("b", 64)
		entB := identity.APIKeyIdentity{
			ID:          canonicalID,
			Revision:    1,
			Lifecycle:   identity.LifecycleActive,
			CreatedAtMS: 2000,
			UpdatedAtMS: 2000,
		}
		bindingB := identity.APIKeySourceBinding{
			APIKeyID:        canonicalID,
			RuntimeIdentity: "runtime-B",
			APIKeyHash:      hashB,
			FirstSeenAtMS:   2000,
			LastSeenAtMS:    2000,
		}
		err = repo.CreateAPIKey(ctx, entB, bindingB)
		if err == nil {
			t.Fatalf("expected error on canonical ID PK collision, got nil")
		}
		if errors.Is(err, ports.ErrSourceBindingConflict) {
			t.Fatalf("canonical ID PK collision MUST NOT be classified as ErrSourceBindingConflict, got: %v", err)
		}
		if !strings.Contains(err.Error(), "insert api key identity") {
			t.Fatalf("expected generic wrapped identity insert error, got: %v", err)
		}

		// 3. Verify second binding was NOT written
		var count int
		if err := db.QueryRow(`select count(*) from ` + sqlite.GatewayAPIKeySourceBindingsTable + ` where runtime_identity = 'runtime-B'`).Scan(&count); err != nil {
			t.Fatalf("query second binding: %v", err)
		}
		if count != 0 {
			t.Fatalf("second binding was written despite identity PK collision! count = %d", count)
		}
		_, _, err = repo.FindActiveAPIKeyBySource(ctx, "runtime-B", hashB)
		if !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("expected ErrNotFound for unwritten second binding, got: %v", err)
		}
	})

	t.Run("Credential", func(t *testing.T) {
		db, repo := setupTestDB(t)

		canonicalID, err := identity.NewCredentialID()
		if err != nil {
			t.Fatalf("generate credID: %v", err)
		}

		// 1. First create with canonicalID and source runtime-A / auth-A
		entA := identity.CredentialIdentity{
			ID:          canonicalID,
			Revision:    1,
			Lifecycle:   identity.LifecycleActive,
			CreatedAtMS: 1000,
			UpdatedAtMS: 1000,
		}
		bindingA := identity.CredentialSourceBinding{
			CredentialID:    canonicalID,
			RuntimeIdentity: "runtime-A",
			SourceAuthID:    "auth-A",
			FirstSeenAtMS:   1000,
			LastSeenAtMS:    1000,
		}
		if err := repo.CreateCredential(ctx, entA, bindingA); err != nil {
			t.Fatalf("create initial credential: %v", err)
		}

		// 2. Second create with SAME canonicalID but DIFFERENT free source (runtime-B / auth-B)
		entB := identity.CredentialIdentity{
			ID:          canonicalID,
			Revision:    1,
			Lifecycle:   identity.LifecycleActive,
			CreatedAtMS: 2000,
			UpdatedAtMS: 2000,
		}
		bindingB := identity.CredentialSourceBinding{
			CredentialID:    canonicalID,
			RuntimeIdentity: "runtime-B",
			SourceAuthID:    "auth-B",
			FirstSeenAtMS:   2000,
			LastSeenAtMS:    2000,
		}
		err = repo.CreateCredential(ctx, entB, bindingB)
		if err == nil {
			t.Fatalf("expected error on canonical ID PK collision, got nil")
		}
		if errors.Is(err, ports.ErrSourceBindingConflict) {
			t.Fatalf("canonical ID PK collision MUST NOT be classified as ErrSourceBindingConflict, got: %v", err)
		}
		if !strings.Contains(err.Error(), "insert credential identity") {
			t.Fatalf("expected generic wrapped identity insert error, got: %v", err)
		}

		// 3. Verify second binding was NOT written
		var count int
		if err := db.QueryRow(`select count(*) from ` + sqlite.GatewayCredentialSourceBindingsTable + ` where runtime_identity = 'runtime-B'`).Scan(&count); err != nil {
			t.Fatalf("query second binding: %v", err)
		}
		if count != 0 {
			t.Fatalf("second binding was written despite identity PK collision! count = %d", count)
		}
		_, _, err = repo.FindActiveCredentialBySource(ctx, "runtime-B", "auth-B")
		if !errors.Is(err, ports.ErrNotFound) {
			t.Fatalf("expected ErrNotFound for unwritten second binding, got: %v", err)
		}
	})
}
