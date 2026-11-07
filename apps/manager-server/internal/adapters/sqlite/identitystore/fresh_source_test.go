package identitystore_test

import (
	"database/sql"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/domain/identity"
	ports "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/ports/identitystore"
)

// seedStagedFreshKey reproduces the Fresh workspace shape: one Canonical Key
// with one staged secret and no runtime source binding yet.
func seedStagedFreshKey(t *testing.T, db *sql.DB, keyID, hash, lifecycle string) {
	t.Helper()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`insert into gateway_api_key_identities (id, revision, lifecycle, created_at_ms, updated_at_ms)
			values (?, 1, ?, 100, 100)`, []any{keyID, lifecycle}},
		{`insert into fresh_client_api_keys (request_id, api_key_id, secret_ciphertext, secret_hash, masked_preview)
			values (?, ?, 'cipher', ?, 'cpamp_sk_…0000')`, []any{sha256Hex(keyID)[:32], keyID, hash}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed staged key: %v", err)
		}
	}
}

func countAPIKeyIdentities(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`select count(*) from gateway_api_key_identities`).Scan(&count); err != nil {
		t.Fatalf("count identities: %v", err)
	}
	return count
}

func TestApplyPassiveSnapshotBindsStagedFreshKeyInsteadOfCreatingSecondIdentity(t *testing.T) {
	ctx := t.Context()
	db, repo := setupTestDB(t)
	stagedID := "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	hash := sha256Hex("staged-client-secret")
	seedStagedFreshKey(t, db, stagedID, hash, "active")

	result, err := repo.ApplyPassiveSnapshot(ctx, ports.ReconcileSnapshotParams{
		RuntimeIdentity:           "runtime-managed-1",
		ObservedRuntimeGeneration: 3,
		APIKeys:                   []ports.APIKeySnapshotItem{{APIKeyHash: hash}},
		NowMS:                     5000,
	})
	if err != nil {
		t.Fatalf("apply snapshot: %v", err)
	}
	if result.APIKeysBound != 1 || result.APIKeysCreated != 0 {
		t.Fatalf("result = %+v, want one bound staged key and no new identity", result)
	}
	entity, binding, err := repo.FindActiveAPIKeyBySource(ctx, "runtime-managed-1", hash)
	if err != nil {
		t.Fatalf("find bound source: %v", err)
	}
	if string(entity.ID) != stagedID {
		t.Fatalf("identity = %s, want the staged canonical key %s", entity.ID, stagedID)
	}
	if binding.FirstSeenAtMS != 5000 || binding.APIKeyHash != hash {
		t.Fatalf("binding = %+v", binding)
	}
	if identities := countAPIKeyIdentities(t, db); identities != 1 {
		t.Fatalf("identities = %d, want exactly the staged one", identities)
	}
}

func TestApplyPassiveSnapshotSkipsTerminalStagedHashPerKey(t *testing.T) {
	ctx := t.Context()
	db, repo := setupTestDB(t)
	stagedID := "a1b2c3d4e5f60718293a4b5c6d7e8f90"
	hash := sha256Hex("revoked-client-secret")
	seedStagedFreshKey(t, db, stagedID, hash, "superseded")

	// The revoke/replace commit closes the superseded key's window; the binding
	// row survives as history.
	if _, err := db.Exec(`insert into gateway_api_key_source_bindings
		(api_key_id, runtime_identity, api_key_hash, observed_runtime_generation,
		 first_seen_at_ms, last_seen_at_ms, retired_at_ms)
		values (?, 'runtime-managed-1', ?, null, 100, 100, 2000)`, stagedID, hash); err != nil {
		t.Fatalf("seed retired binding: %v", err)
	}

	// A lagging snapshot that still reports the revoked hash must never mint a
	// second Canonical ID for it, and the refusal must not abort the snapshot.
	result, err := repo.ApplyPassiveSnapshot(ctx, ports.ReconcileSnapshotParams{
		RuntimeIdentity:           "runtime-managed-1",
		ObservedRuntimeGeneration: 3,
		APIKeys:                   []ports.APIKeySnapshotItem{{APIKeyHash: hash}},
		NowMS:                     5000,
	})
	if err != nil {
		t.Fatalf("apply snapshot: %v", err)
	}
	if result.APIKeysTerminalSkipped != 1 || result.APIKeysBound != 0 || result.APIKeysCreated != 0 {
		t.Fatalf("result = %+v, want one skipped terminal hash", result)
	}
	if identities := countAPIKeyIdentities(t, db); identities != 1 {
		t.Fatalf("identities = %d, want exactly the staged one", identities)
	}
	var bindings int
	if err := db.QueryRow(`select count(*) from gateway_api_key_source_bindings where api_key_hash = ? and retired_at_ms is null`, hash).Scan(&bindings); err != nil {
		t.Fatalf("count active bindings: %v", err)
	}
	if bindings != 0 {
		t.Fatalf("active bindings = %d, want none for a terminal hash", bindings)
	}
	if _, err := repo.LoadAPIKeyByID(ctx, identity.APIKeyID(stagedID)); err != nil {
		t.Fatalf("staged history lost: %v", err)
	}
}

// A superseded staged key that was never bound (revoked before the installation
// committed) owns the hash just the same, and a mixed batch still applies every
// other key.
func TestApplyPassiveSnapshotSkippedTerminalHashDoesNotBlockTheBatch(t *testing.T) {
	ctx := t.Context()
	db, repo := setupTestDB(t)
	stagedActive := "b1b2c3d4e5f60718293a4b5c6d7e8f91"
	stagedTerminal := "c1b2c3d4e5f60718293a4b5c6d7e8f92"
	activeHash := sha256Hex("staged-active-secret")
	terminalHash := sha256Hex("revoked-before-binding")
	freshHash := sha256Hex("brand-new-secret")
	seedStagedFreshKey(t, db, stagedActive, activeHash, "active")
	seedStagedFreshKey(t, db, stagedTerminal, terminalHash, "superseded")

	result, err := repo.ApplyPassiveSnapshot(ctx, ports.ReconcileSnapshotParams{
		RuntimeIdentity:           "runtime-managed-1",
		ObservedRuntimeGeneration: 3,
		APIKeys: []ports.APIKeySnapshotItem{
			{APIKeyHash: activeHash}, {APIKeyHash: terminalHash}, {APIKeyHash: freshHash},
		},
		NowMS: 5000,
	})
	if err != nil {
		t.Fatalf("mixed snapshot: %v", err)
	}
	if result.APIKeysBound != 1 || result.APIKeysCreated != 1 || result.APIKeysTerminalSkipped != 1 {
		t.Fatalf("result = %+v, want one bound, one created and one skipped", result)
	}
	bound, _, err := repo.FindActiveAPIKeyBySource(ctx, "runtime-managed-1", activeHash)
	if err != nil || string(bound.ID) != stagedActive {
		t.Fatalf("active staged source = %v err = %v", bound.ID, err)
	}
	created, _, err := repo.FindActiveAPIKeyBySource(ctx, "runtime-managed-1", freshHash)
	if err != nil || string(created.ID) == stagedTerminal {
		t.Fatalf("new source = %v err = %v", created.ID, err)
	}
	if _, _, err := repo.FindActiveAPIKeyBySource(ctx, "runtime-managed-1", terminalHash); err == nil {
		t.Fatal("terminal hash gained an active binding")
	}
	if identities := countAPIKeyIdentities(t, db); identities != 3 {
		t.Fatalf("identities = %d, want the two staged keys plus one new", identities)
	}
}

// A missing staged key is recoverable, not terminal: a returning hash binds to
// the existing Canonical Key instead of minting a new identity.
func TestApplyPassiveSnapshotBindsMissingStagedKey(t *testing.T) {
	ctx := t.Context()
	db, repo := setupTestDB(t)
	stagedID := "d1b2c3d4e5f60718293a4b5c6d7e8f93"
	hash := sha256Hex("missing-client-secret")
	seedStagedFreshKey(t, db, stagedID, hash, "missing")

	result, err := repo.ApplyPassiveSnapshot(ctx, ports.ReconcileSnapshotParams{
		RuntimeIdentity:           "runtime-managed-1",
		ObservedRuntimeGeneration: 3,
		APIKeys:                   []ports.APIKeySnapshotItem{{APIKeyHash: hash}},
		NowMS:                     5000,
	})
	if err != nil {
		t.Fatalf("apply snapshot: %v", err)
	}
	if result.APIKeysBound != 1 || result.APIKeysCreated != 0 || result.APIKeysTerminalSkipped != 0 || result.APIKeysRecovered != 1 {
		t.Fatalf("result = %+v, want the missing staged key recovered and bound", result)
	}
	entity, _, err := repo.FindActiveAPIKeyBySource(ctx, "runtime-managed-1", hash)
	if err != nil || string(entity.ID) != stagedID {
		t.Fatalf("missing staged source = %v err = %v", entity.ID, err)
	}
	if identities := countAPIKeyIdentities(t, db); identities != 1 {
		t.Fatalf("identities = %d, want exactly the staged one", identities)
	}
}

// A staged workspace key pre-bound at creation time (observed_runtime_generation
// IS NULL) has not been applied to CPA yet; passive snapshots must not flip its
// identity lifecycle to missing before it is first observed on the runtime.
func TestApplyPassiveSnapshotKeepsPreboundUnobservedStagedKeyActive(t *testing.T) {
	ctx := t.Context()
	db, repo := setupTestDB(t)
	stagedID := "e1b2c3d4e5f60718293a4b5c6d7e8f94"
	stagedHash := sha256Hex("prebound-unapplied-workspace-secret")
	liveHash := sha256Hex("currently-installed-secret")
	seedStagedFreshKey(t, db, stagedID, stagedHash, "active")
	if _, err := db.Exec(`insert into gateway_api_key_source_bindings
		(api_key_id, runtime_identity, api_key_hash, observed_runtime_generation,
		 first_seen_at_ms, last_seen_at_ms, retired_at_ms)
		values (?, 'runtime-managed-1', ?, null, 100, 100, null)`, stagedID, stagedHash); err != nil {
		t.Fatalf("seed prebound source binding: %v", err)
	}

	result, err := repo.ApplyPassiveSnapshot(ctx, ports.ReconcileSnapshotParams{
		RuntimeIdentity:           "runtime-managed-1",
		ObservedRuntimeGeneration: 3,
		APIKeys:                   []ports.APIKeySnapshotItem{{APIKeyHash: liveHash}},
		NowMS:                     5000,
	})
	if err != nil {
		t.Fatalf("apply snapshot: %v", err)
	}
	if result.APIKeysMissing != 0 {
		t.Fatalf("APIKeysMissing = %d, want 0 for prebound unobserved staged key", result.APIKeysMissing)
	}
	entity, err := repo.LoadAPIKeyByID(ctx, identity.APIKeyID(stagedID))
	if err != nil {
		t.Fatalf("LoadAPIKeyByID: %v", err)
	}
	if entity.Lifecycle != identity.LifecycleActive {
		t.Fatalf("staged key lifecycle = %q, want %q", entity.Lifecycle, identity.LifecycleActive)
	}
}
